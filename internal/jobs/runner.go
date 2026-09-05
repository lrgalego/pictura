// Package jobs runs the long model calls in the background, one job per
// story at a time, recording progress in the store for the UI to poll.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lrgalego/story-time/internal/pipeline"
	"github.com/lrgalego/story-time/internal/store"
)

// ErrBusy is returned when a story already has a job running.
var ErrBusy = errors.New("this story is already working on something — wait for it to finish")

// Runner owns the goroutines and the concurrency limits.
type Runner struct {
	st     *store.Store
	ai     pipeline.AI
	mu     sync.Mutex
	active map[int64]bool
	imgSem chan struct{}
	wg     sync.WaitGroup
}

// New creates a runner; images limits concurrent image generations.
func New(st *store.Store, ai pipeline.AI, images int) *Runner {
	if images < 1 {
		images = 1
	}
	return &Runner{st: st, ai: ai, active: map[int64]bool{}, imgSem: make(chan struct{}, images)}
}

// Wait blocks until every running job has finished (tests, shutdown).
func (r *Runner) Wait() { r.wg.Wait() }

// Busy reports whether a story has a job in flight.
func (r *Runner) Busy(storyID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[storyID]
}

type reporter func(progress, total int, message string)

func (r *Runner) start(storyID int64, kind, message string, total int, fn func(ctx context.Context, report reporter) error) error {
	r.mu.Lock()
	if r.active[storyID] {
		r.mu.Unlock()
		return ErrBusy
	}
	r.active[storyID] = true
	r.mu.Unlock()

	ctx := context.Background()
	job, err := r.st.CreateJob(ctx, storyID, kind, message, total)
	if err != nil {
		r.release(storyID)
		return err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.release(storyID)
		report := func(progress, total int, message string) {
			_ = r.st.UpdateJob(ctx, job.ID, progress, total, message)
		}
		err := fn(ctx, report)
		msg := ""
		if err != nil {
			msg = err.Error()
			log.Printf("job %d (%s) story %d failed: %v", job.ID, kind, storyID, err)
		}
		_ = r.st.FinishJob(ctx, job.ID, msg)
		_ = r.st.Touch(ctx, storyID)
	}()
	return nil
}

func (r *Runner) release(storyID int64) {
	r.mu.Lock()
	delete(r.active, storyID)
	r.mu.Unlock()
}

// ---------- step 2: characters ----------

// Analyze extracts the cast from the script. Sheets are not drawn yet: the
// writer reads the descriptions and attaches references first, then asks
// for the art with DrawSheets.
func (r *Runner) Analyze(storyID int64) error {
	return r.start(storyID, "analyze", "Reading your script…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		an, err := pipeline.Analyze(ctx, r.ai, story.Script, pipeline.StyleBySlug(story.Style))
		if err != nil {
			return err
		}
		if strings.TrimSpace(story.Title) == "" {
			story.Title = an.Title
		}
		story.Logline = an.Logline
		story.World = an.World
		story.Step = store.StepCharacters
		if err := r.st.UpdateStory(ctx, story); err != nil {
			return err
		}
		if err := r.st.DeletePages(ctx, storyID); err != nil {
			return err
		}
		if err := r.st.DeleteCharacters(ctx, storyID); err != nil {
			return err
		}
		for i, spec := range an.Characters {
			c := fromSpec(storyID, i, spec)
			if err := r.st.InsertCharacter(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// DrawSheets draws every character sheet that is not ready yet.
func (r *Runner) DrawSheets(storyID int64) error {
	return r.start(storyID, "sheets", "Drawing character sheets…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		var todo []*store.Character
		for _, c := range chars {
			if c.SheetStatus != store.ImageReady {
				todo = append(todo, c)
			}
		}
		return r.drawSheets(ctx, story, todo, report)
	})
}

// AllReady reports whether every character has a finished sheet: the
// condition for storyboarding.
func AllReady(chars []*store.Character) bool {
	if len(chars) == 0 {
		return false
	}
	for _, c := range chars {
		if c.SheetStatus != store.ImageReady {
			return false
		}
	}
	return true
}

// SheetsStarted reports whether any sheet has been drawn or attempted: the
// line between the casting phase (descriptions only) and the art phase.
func SheetsStarted(chars []*store.Character) bool {
	for _, c := range chars {
		if c.SheetStatus != store.ImagePending {
			return true
		}
	}
	return false
}

func fromSpec(storyID int64, pos int, s pipeline.CharacterSpec) *store.Character {
	return &store.Character{StoryID: storyID, Position: pos, Name: s.Name, Role: s.Role, Age: s.Age, Visual: s.Visual, Wardrobe: s.Wardrobe, Items: s.Items, Personality: s.Personality, SheetStatus: store.ImagePending}
}

func applySpec(c *store.Character, s pipeline.CharacterSpec) {
	c.Name, c.Role, c.Age, c.Visual, c.Wardrobe, c.Items, c.Personality = s.Name, s.Role, s.Age, s.Visual, s.Wardrobe, s.Items, s.Personality
}

// references loads one character's reference images as pipeline input.
func (r *Runner) references(ctx context.Context, storyID, characterID int64) ([]pipeline.Reference, []*store.Ref, error) {
	all, err := r.st.Refs(ctx, storyID)
	if err != nil {
		return nil, nil, err
	}
	var refs []pipeline.Reference
	var rows []*store.Ref
	for _, ref := range all {
		if ref.CharacterID != characterID {
			continue
		}
		png, err := r.st.ReadImage(ctx, ref.Image)
		if err != nil {
			continue
		}
		refs = append(refs, pipeline.Reference{Filename: ref.Filename, Note: ref.Note, PNG: png})
		rows = append(rows, ref)
	}
	return refs, rows, nil
}

func refPNGs(refs []pipeline.Reference) [][]byte {
	var out [][]byte
	for _, r := range refs {
		out = append(out, r.PNG)
	}
	return out
}

func lookChanged(c *store.Character, s pipeline.CharacterSpec) bool {
	return c.Visual != s.Visual || c.Wardrobe != s.Wardrobe || c.Items != s.Items || c.Age != s.Age
}

// drawSheets renders character sheets concurrently, reporting progress.
func (r *Runner) drawSheets(ctx context.Context, story *store.Story, chars []*store.Character, report reporter) error {
	total := len(chars)
	if total == 0 {
		return nil
	}
	var mu sync.Mutex
	done := 0
	var firstErr error
	report(0, total, fmt.Sprintf("Drawing character sheets (0/%d)…", total))
	var wg sync.WaitGroup
	for _, c := range chars {
		wg.Add(1)
		go func(c *store.Character) {
			defer wg.Done()
			err := r.drawSheet(ctx, story, c)
			mu.Lock()
			done++
			if err != nil && firstErr == nil {
				firstErr = err
			}
			report(done, total, fmt.Sprintf("Drawing character sheets (%d/%d)…", done, total))
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	if firstErr != nil {
		return fmt.Errorf("some character sheets failed: %w", firstErr)
	}
	return nil
}

func (r *Runner) drawSheet(ctx context.Context, story *store.Story, c *store.Character) error {
	r.imgSem <- struct{}{}
	defer func() { <-r.imgSem }()
	_ = r.st.SetCharacterSheet(ctx, c.ID, c.SheetImage, store.ImageGenerating, "")
	refs, _, _ := r.references(ctx, story.ID, c.ID)
	if len(refs) > 6 {
		refs = refs[:6]
	}
	var png []byte
	var err error
	if len(refs) > 0 {
		png, err = r.ai.EditImage(ctx, pipeline.SheetPrompt(story, c, len(refs)), refPNGs(refs), pipeline.SheetSize)
	} else {
		png, err = r.ai.GenerateImage(ctx, pipeline.SheetPrompt(story, c, 0), pipeline.SheetSize)
	}
	if err != nil {
		_ = r.st.SetCharacterSheet(ctx, c.ID, c.SheetImage, store.ImageError, err.Error())
		return err
	}
	name, err := r.st.SaveImage(ctx, story.ID, "png", png)
	if err != nil {
		_ = r.st.SetCharacterSheet(ctx, c.ID, c.SheetImage, store.ImageError, err.Error())
		return err
	}
	c.SheetImage, c.SheetStatus, c.SheetError = name, store.ImageReady, ""
	return r.st.SetCharacterSheet(ctx, c.ID, name, store.ImageReady, "")
}

// Revise updates one character. With feedback, or with revise set (new
// reference images arrived), the description is rewritten first; with draw
// set the sheet is (re)drawn afterwards. In the casting phase callers pass
// draw=false so nothing is rendered before the writer asks.
func (r *Runner) Revise(storyID, charID int64, feedback string, revise, draw bool) error {
	msg := "Redrawing the character sheet…"
	if feedback != "" || revise {
		msg = "Revising the character…"
	}
	total := 0
	if draw {
		total = 1
	}
	return r.start(storyID, "sheet", msg, total, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		c, err := r.st.Character(ctx, charID)
		if err != nil || c.StoryID != storyID {
			return store.ErrNotFound
		}
		refs, _, err := r.references(ctx, storyID, c.ID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(feedback) != "" || revise {
			spec, err := pipeline.AdjustCharacter(ctx, r.ai, story, c, feedback, refs)
			if err != nil {
				return err
			}
			applySpec(c, *spec)
			if err := r.st.UpdateCharacter(ctx, c); err != nil {
				return err
			}
		}
		if !draw {
			return nil
		}
		report(0, 1, "Drawing the sheet…")
		r.invalidatePages(ctx, storyID)
		return r.drawSheet(ctx, story, c)
	})
}

// AdoptSheet rewrites a character's description from a finished sheet the
// writer uploaded, so the words match the art that will drive the pages.
func (r *Runner) AdoptSheet(storyID, charID int64) error {
	return r.start(storyID, "sheet", "Reading the uploaded sheet…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		c, err := r.st.Character(ctx, charID)
		if err != nil || c.StoryID != storyID {
			return store.ErrNotFound
		}
		if c.SheetStatus != store.ImageReady || c.SheetImage == "" {
			return fmt.Errorf("%s has no sheet to read", c.Name)
		}
		png, err := r.st.ReadImage(ctx, c.SheetImage)
		if err != nil {
			return err
		}
		refs := []pipeline.Reference{{Filename: "character-sheet.png", Note: "the finished character sheet the writer supplied; it is authoritative", PNG: png}}
		spec, err := pipeline.AdjustCharacter(ctx, r.ai, story, c,
			"A finished character sheet was uploaded and will be used as-is for every page. Rewrite the visual, wardrobe and items fields to describe exactly what the sheet shows, in the same order of detail as before. Keep the name, role, age and personality unless the sheet clearly contradicts them.", refs)
		if err != nil {
			return err
		}
		applySpec(c, *spec)
		if err := r.st.UpdateCharacter(ctx, c); err != nil {
			return err
		}
		r.invalidatePages(ctx, storyID)
		return nil
	})
}

// AdjustCast revises the whole cast from feedback and redraws what changed.
func (r *Runner) AdjustCast(storyID int64, feedback string) error {
	return r.start(storyID, "cast", "Revising the cast…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		existing, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		specs, err := pipeline.AdjustCharacters(ctx, r.ai, story, existing, feedback)
		if err != nil {
			return err
		}
		artPhase := SheetsStarted(existing)
		byName := map[string]*store.Character{}
		for _, c := range existing {
			byName[strings.ToLower(c.Name)] = c
		}
		var keep []*store.Character
		var redraw []*store.Character
		for i, spec := range specs {
			if c, ok := byName[strings.ToLower(spec.Name)]; ok {
				changed := lookChanged(c, spec) || c.SheetStatus != store.ImageReady
				applySpec(c, spec)
				c.Position = i
				if changed {
					c.SheetStatus = store.ImagePending
				}
				if err := r.st.UpdateCharacter(ctx, c); err != nil {
					return err
				}
				delete(byName, strings.ToLower(spec.Name))
				keep = append(keep, c)
				if changed {
					redraw = append(redraw, c)
				}
				continue
			}
			c := fromSpec(storyID, i, spec)
			if err := r.st.InsertCharacter(ctx, c); err != nil {
				return err
			}
			keep = append(keep, c)
			redraw = append(redraw, c)
		}
		for _, gone := range byName {
			_ = r.st.DeleteCharacter(ctx, gone.ID)
		}
		if !artPhase {
			return nil
		}
		r.invalidatePages(ctx, storyID)
		return r.drawSheets(ctx, story, redraw, report)
	})
}

// invalidatePages marks rendered pages stale after a cast change: the
// breakdown stays, the art must be redone.
func (r *Runner) invalidatePages(ctx context.Context, storyID int64) {
	pages, _ := r.st.Pages(ctx, storyID)
	for _, p := range pages {
		if p.ImageStatus == store.ImageReady {
			_ = r.st.SetPageImage(ctx, p.ID, p.Image, store.ImagePending, "")
		}
	}
}

// ---------- step 3: pages ----------

// Breakdown plans the pages from the script and the approved cast.
func (r *Runner) Breakdown(storyID int64) error {
	return r.start(storyID, "breakdown", "Storyboarding your pages…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		specs, err := pipeline.Breakdown(ctx, r.ai, story, chars)
		if err != nil {
			return err
		}
		if err := r.st.DeletePages(ctx, storyID); err != nil {
			return err
		}
		for _, s := range specs {
			if err := r.st.InsertPage(ctx, &store.Page{StoryID: storyID, Number: s.Number, Title: s.Title, Summary: s.Summary, Panels: s.Panels}); err != nil {
				return err
			}
		}
		return r.st.SetStep(ctx, storyID, store.StepPages)
	})
}

// AdjustPages revises the whole breakdown from feedback.
func (r *Runner) AdjustPages(storyID int64, feedback string) error {
	return r.start(storyID, "pages", "Revising the pages…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		pages, err := r.st.Pages(ctx, storyID)
		if err != nil {
			return err
		}
		specs, err := pipeline.AdjustBreakdown(ctx, r.ai, story, chars, pages, feedback)
		if err != nil {
			return err
		}
		if err := r.st.DeletePages(ctx, storyID); err != nil {
			return err
		}
		for _, s := range specs {
			p := &store.Page{StoryID: storyID, Number: s.Number, Title: s.Title, Summary: s.Summary, Panels: s.Panels}
			// A page whose plan is unchanged keeps its art.
			for _, old := range pages {
				if old.Number == s.Number && old.ImageStatus == store.ImageReady && samePlan(old, s) {
					p.Image, p.ImageStatus = old.Image, store.ImageReady
				}
			}
			if err := r.st.InsertPage(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
}

func samePlan(old *store.Page, s pipeline.PageSpec) bool {
	if old.Title != s.Title || len(old.Panels) != len(s.Panels) {
		return false
	}
	for i := range s.Panels {
		a, b := old.Panels[i], s.Panels[i]
		if a.Description != b.Description || a.Shot != b.Shot || a.Caption != b.Caption || len(a.Dialogue) != len(b.Dialogue) {
			return false
		}
		for j := range a.Dialogue {
			if a.Dialogue[j] != b.Dialogue[j] {
				return false
			}
		}
	}
	return true
}

// AdjustPage revises one page's plan from feedback.
func (r *Runner) AdjustPage(storyID, pageID int64, feedback string) error {
	return r.start(storyID, "page", "Revising the page…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		page, err := r.st.Page(ctx, pageID)
		if err != nil || page.StoryID != storyID {
			return store.ErrNotFound
		}
		spec, err := pipeline.AdjustPage(ctx, r.ai, story, chars, page, feedback)
		if err != nil {
			return err
		}
		page.Title, page.Summary, page.Panels = spec.Title, spec.Summary, spec.Panels
		if page.ImageStatus == store.ImageReady {
			page.ImageStatus = store.ImagePending
		}
		return r.st.UpdatePage(ctx, page)
	})
}

// ---------- step 4: art ----------

// RenderAll draws every page that has no current art.
func (r *Runner) RenderAll(storyID int64) error {
	return r.start(storyID, "render", "Drawing your pages…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		pages, err := r.st.Pages(ctx, storyID)
		if err != nil {
			return err
		}
		if err := r.st.SetStep(ctx, storyID, store.StepBook); err != nil {
			return err
		}
		var todo []*store.Page
		for _, p := range pages {
			if p.ImageStatus != store.ImageReady {
				todo = append(todo, p)
			}
		}
		total := len(todo)
		if total == 0 {
			return nil
		}
		refs, err := r.sheets(ctx, chars)
		if err != nil {
			return err
		}
		var mu sync.Mutex
		done := 0
		var firstErr error
		report(0, total, fmt.Sprintf("Drawing pages (0/%d)…", total))
		var wg sync.WaitGroup
		for _, p := range todo {
			wg.Add(1)
			go func(p *store.Page) {
				defer wg.Done()
				err := r.drawPage(ctx, story, chars, refs, p, "")
				mu.Lock()
				done++
				if err != nil && firstErr == nil {
					firstErr = err
				}
				report(done, total, fmt.Sprintf("Drawing pages (%d/%d)…", done, total))
				mu.Unlock()
			}(p)
		}
		wg.Wait()
		if firstErr != nil {
			return fmt.Errorf("some pages failed: %w", firstErr)
		}
		return nil
	})
}

// RenderPage redraws one page with optional revision notes.
func (r *Runner) RenderPage(storyID, pageID int64, feedback string) error {
	return r.start(storyID, "render-page", "Redrawing the page…", 1, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		page, err := r.st.Page(ctx, pageID)
		if err != nil || page.StoryID != storyID {
			return store.ErrNotFound
		}
		refs, err := r.sheets(ctx, chars)
		if err != nil {
			return err
		}
		return r.drawPage(ctx, story, chars, refs, page, feedback)
	})
}

// sheets loads the ready character sheets, keyed by character name.
func (r *Runner) sheets(ctx context.Context, chars []*store.Character) (map[string][]byte, error) {
	refs := map[string][]byte{}
	for _, c := range chars {
		if c.SheetStatus != store.ImageReady || c.SheetImage == "" {
			continue
		}
		b, err := r.st.ReadImage(ctx, c.SheetImage)
		if err != nil {
			return nil, err
		}
		refs[strings.ToLower(c.Name)] = b
	}
	return refs, nil
}

func (r *Runner) drawPage(ctx context.Context, story *store.Story, chars []*store.Character, refs map[string][]byte, p *store.Page, feedback string) error {
	r.imgSem <- struct{}{}
	defer func() { <-r.imgSem }()
	_ = r.st.SetPageImage(ctx, p.ID, p.Image, store.ImageGenerating, "")

	// Only the characters on this page ride along as references (the API
	// composes from a handful of images; the whole cast would dilute it).
	present := map[string]bool{}
	for _, pn := range p.Panels {
		for _, n := range pn.Characters {
			present[strings.ToLower(n)] = true
		}
	}
	var cast []*store.Character
	var images [][]byte
	for _, c := range chars {
		key := strings.ToLower(c.Name)
		if b, ok := refs[key]; ok && (present[key] || len(present) == 0) && len(images) < 6 {
			cast = append(cast, c)
			images = append(images, b)
		}
	}
	png, err := r.ai.EditImage(ctx, pipeline.PagePrompt(story, cast, p, feedback), images, pipeline.PageSize)
	if err != nil {
		_ = r.st.SetPageImage(ctx, p.ID, p.Image, store.ImageError, err.Error())
		return err
	}
	name, err := r.st.SaveImage(ctx, story.ID, "png", png)
	if err != nil {
		_ = r.st.SetPageImage(ctx, p.ID, p.Image, store.ImageError, err.Error())
		return err
	}
	return r.st.SetPageImage(ctx, p.ID, name, store.ImageReady, "")
}

// Elapsed is a helper for the UI: how long a job has been running.
func Elapsed(j *store.Job) time.Duration {
	if j == nil {
		return 0
	}
	return time.Since(j.CreatedAt).Round(time.Second)
}
