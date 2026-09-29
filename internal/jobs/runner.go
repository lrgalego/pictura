// Package jobs runs the long model calls in the background, one job per
// story at a time, recording progress in the store for the UI to poll.
package jobs

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

// Runner is a per-story scheduler. Nothing is ever refused: work is queued
// and runs as soon as its locks are free. A character job needs its
// character free and no story-level job running; a story-level job (read
// the script, draw every sheet, adjust the cast, storyboard, render) waits
// for everything on the story and, once at the head of the queue, stops
// later character jobs from starting so it cannot be starved.
type Runner struct {
	// Voice speaks the read-aloud lines; New sets the offline tones.
	Voice pipeline.Speaker

	st           *store.Store
	ai           pipeline.AI
	mu           sync.Mutex
	queues       map[int64][]*task // per story, in submission order
	storyRunning map[int64]bool
	charRunning  map[int64]bool
	charCount    map[int64]int // running character jobs per story
	imgSem       chan struct{}
	wg           sync.WaitGroup
}

type task struct {
	job    *store.Job
	kind   string
	charID int64
	fn     func(ctx context.Context, report reporter) error
}

// New creates a runner; images limits concurrent image generations.
func New(st *store.Store, ai pipeline.AI, images int) *Runner {
	if images < 1 {
		images = 1
	}
	return &Runner{Voice: pipeline.FakeVoice{}, st: st, ai: ai, queues: map[int64][]*task{}, storyRunning: map[int64]bool{}, charRunning: map[int64]bool{}, charCount: map[int64]int{}, imgSem: make(chan struct{}, images)}
}

// Wait blocks until every queued and running job has finished (tests, shutdown).
func (r *Runner) Wait() {
	for {
		r.wg.Wait()
		r.mu.Lock()
		idle := true
		for _, q := range r.queues {
			if len(q) > 0 {
				idle = false
			}
		}
		r.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Busy reports whether a story-level job is running.
func (r *Runner) Busy(storyID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storyRunning[storyID]
}

// Working reports whether anything is queued or running for a story.
func (r *Runner) Working(storyID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storyRunning[storyID] || r.charCount[storyID] > 0 || len(r.queues[storyID]) > 0
}

// CharacterBusy reports whether a character has work running or queued.
func (r *Runner) CharacterBusy(storyID, charID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storyRunning[storyID] || r.charRunning[charID] {
		return true
	}
	for _, t := range r.queues[storyID] {
		if t.charID == charID {
			return true
		}
	}
	return false
}

type reporter func(progress, total int, message string)

// start queues a story-level job.
func (r *Runner) start(storyID int64, kind, message string, total int, fn func(ctx context.Context, report reporter) error) error {
	return r.enqueue(storyID, 0, kind, message, total, fn)
}

// startChar queues a character-level job.
func (r *Runner) startChar(storyID, charID int64, kind, message string, total int, fn func(ctx context.Context, report reporter) error) error {
	return r.enqueue(storyID, charID, kind, message, total, fn)
}

func (r *Runner) enqueue(storyID, charID int64, kind, message string, total int, fn func(ctx context.Context, report reporter) error) error {
	job, err := r.st.CreateJob(context.Background(), storyID, charID, kind, message, total)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.queues[storyID] = append(r.queues[storyID], &task{job: job, kind: kind, charID: charID, fn: fn})
	r.mu.Unlock()
	r.pump(storyID)
	return nil
}

// pump starts every queued task whose locks are free.
func (r *Runner) pump(storyID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queues[storyID]
	for i := 0; i < len(q); {
		t := q[i]
		if t.charID == 0 {
			if r.storyRunning[storyID] || r.charCount[storyID] > 0 {
				break // wait for the story to quiesce; nothing behind it may start
			}
			r.storyRunning[storyID] = true
		} else {
			if r.storyRunning[storyID] {
				break
			}
			if r.charRunning[t.charID] {
				i++ // this character is busy; later tasks on other characters may go
				continue
			}
			r.charRunning[t.charID] = true
			r.charCount[storyID]++
		}
		q = append(q[:i], q[i+1:]...)
		r.queues[storyID] = q
		r.wg.Add(1)
		go r.execute(storyID, t)
	}
	if len(q) == 0 {
		delete(r.queues, storyID)
	}
}

func (r *Runner) execute(storyID int64, t *task) {
	defer r.wg.Done()
	ctx := context.Background()
	_ = r.st.StartJob(ctx, t.job.ID)
	report := func(progress, total int, message string) {
		_ = r.st.UpdateJob(ctx, t.job.ID, progress, total, message)
	}
	err := t.fn(ctx, report)
	msg := ""
	if err != nil {
		msg = err.Error()
		log.Printf("job %d (%s) story %d failed: %v", t.job.ID, t.kind, storyID, err)
	}
	_ = r.st.FinishJob(ctx, t.job.ID, msg)
	_ = r.st.Touch(ctx, storyID)
	// Reclaim whatever this job made obsolete: the previous sheet or page
	// art, references of a dropped character.
	if n, err := r.st.Sweep(ctx, storyID); err != nil {
		log.Printf("sweep story %d: %v", storyID, err)
	} else if n > 0 {
		log.Printf("sweep story %d: reclaimed %d image(s)", storyID, n)
	}

	r.mu.Lock()
	if t.charID == 0 {
		delete(r.storyRunning, storyID)
	} else {
		delete(r.charRunning, t.charID)
		if r.charCount[storyID]--; r.charCount[storyID] <= 0 {
			delete(r.charCount, storyID)
		}
	}
	r.mu.Unlock()
	r.pump(storyID)
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
		report(0, 0, "Casting voices…")
		r.castVoices(ctx, storyID)
		return nil
	})
}

// CastVoices casts voices for a story whose characters have none yet (one
// made before voices existed, or one whose casting call failed).
func (r *Runner) CastVoices(storyID int64) error {
	return r.start(storyID, "voices", "Casting voices…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		narrator, voices, err := pipeline.CastVoices(ctx, r.ai, story, chars)
		if err != nil {
			return err
		}
		if err := r.st.SetNarratorVoice(ctx, storyID, narrator); err != nil {
			return err
		}
		for id, v := range voices {
			if err := r.st.SetCharacterVoice(ctx, id, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// castVoices gives a voice to every character that has none, and to the
// narrator. It is best effort: when the model call fails the characters
// keep speaking with their stable fallback voices (pipeline.CharacterVoice)
// and the writer can still pick by hand, so the job does not fail over it.
func (r *Runner) castVoices(ctx context.Context, storyID int64) {
	story, err := r.st.Story(ctx, storyID)
	if err != nil {
		return
	}
	chars, err := r.st.Characters(ctx, storyID)
	if err != nil {
		return
	}
	narrator, voices, err := pipeline.CastVoices(ctx, r.ai, story, chars)
	if err != nil {
		log.Printf("cast voices story %d: %v", storyID, err)
		return
	}
	if narrator != story.NarratorVoice {
		_ = r.st.SetNarratorVoice(ctx, storyID, narrator)
	}
	for id, v := range voices {
		_ = r.st.SetCharacterVoice(ctx, id, v)
	}
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
	return r.startChar(storyID, charID, "sheet", msg, total, func(ctx context.Context, report reporter) error {
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
	return r.startChar(storyID, charID, "adopt", "Reading the uploaded sheet…", 0, func(ctx context.Context, report reporter) error {
		return r.adopt(ctx, storyID, charID)
	})
}

func (r *Runner) adopt(ctx context.Context, storyID, charID int64) error {
	{
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
	}
}

// Fields is what the edit dialog can change.
type Fields struct {
	Name, Role, Age, Visual, Wardrobe, Items, Personality string
	Redraw                                                bool // redraw the sheet if the look changed and one exists
}

// Edit applies hand-written fields, queued behind any running change to
// the same character so a revision in flight cannot overwrite them.
func (r *Runner) Edit(storyID, charID int64, f Fields) error {
	return r.startChar(storyID, charID, "edit", "Saving your edits…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		c, err := r.st.Character(ctx, charID)
		if err != nil || c.StoryID != storyID {
			return store.ErrNotFound
		}
		looksChanged := c.Visual != f.Visual || c.Wardrobe != f.Wardrobe || c.Items != f.Items || c.Age != f.Age
		hadSheet := c.SheetStatus != store.ImagePending
		c.Name, c.Role, c.Age, c.Visual, c.Wardrobe, c.Items, c.Personality = f.Name, f.Role, f.Age, f.Visual, f.Wardrobe, f.Items, f.Personality
		if err := r.st.UpdateCharacter(ctx, c); err != nil {
			return err
		}
		if !(looksChanged && f.Redraw && hadSheet) {
			return nil
		}
		report(0, 1, "Redrawing the sheet with the new details…")
		r.invalidatePages(ctx, storyID)
		return r.drawSheet(ctx, story, c)
	})
}

// Link makes a character the same as one from another story (queued).
func (r *Runner) Link(storyID, charID, sourceID int64) error {
	return r.startChar(storyID, charID, "link", "Copying the character…", 0, func(ctx context.Context, report reporter) error {
		c, err := r.st.Character(ctx, charID)
		if err != nil || c.StoryID != storyID {
			return store.ErrNotFound
		}
		src, err := r.st.Character(ctx, sourceID)
		if err != nil {
			return store.ErrNotFound
		}
		if err := r.st.LinkCharacter(ctx, c, src); err != nil {
			return err
		}
		r.invalidatePages(ctx, storyID)
		return nil
	})
}

// SetSheet installs an uploaded image (already stored under imageName) as
// the character's finished sheet, then describes the character from it.
func (r *Runner) SetSheet(storyID, charID int64, imageName string) error {
	return r.startChar(storyID, charID, "adopt", "Reading the uploaded sheet…", 0, func(ctx context.Context, report reporter) error {
		c, err := r.st.Character(ctx, charID)
		if err != nil || c.StoryID != storyID {
			return store.ErrNotFound
		}
		if err := r.st.SetCharacterSheet(ctx, c.ID, imageName, store.ImageReady, ""); err != nil {
			return err
		}
		return r.adopt(ctx, storyID, charID)
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
		r.castVoices(ctx, storyID) // newcomers need a voice
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
		// The whole book is drawn: get it ready to be read aloud, so the
		// reader starts the moment someone presses play.
		return r.Narrate(storyID)
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

// ---------- read aloud ----------

// Narrate gets a story ready to be read aloud: every drawn page whose art
// changed since it was last read is read again (the lettering, who says
// what, where each word is), and every line without a clip in its
// speaker's current voice is voiced. Pages go in order, a few at a time,
// so the first page is ready first.
func (r *Runner) Narrate(storyID int64) error {
	return r.start(storyID, "narrate", "Getting the voices ready…", 0, func(ctx context.Context, report reporter) error {
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		if pipeline.Uncast(chars) {
			r.castVoices(ctx, storyID)
		}
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		if chars, err = r.st.Characters(ctx, storyID); err != nil {
			return err
		}
		pages, err := r.st.Pages(ctx, storyID)
		if err != nil {
			return err
		}
		var todo []*store.Page
		for _, p := range pages {
			if p.ImageStatus == store.ImageReady && p.Image != "" {
				todo = append(todo, p)
			}
		}
		total := len(todo)
		if total == 0 {
			return nil
		}
		var mu sync.Mutex
		done := 0
		var firstErr error
		report(0, total, fmt.Sprintf("Getting the voices ready (0/%d pages)…", total))
		sem := make(chan struct{}, 3)
		var wg sync.WaitGroup
		for _, p := range todo {
			wg.Add(1)
			sem <- struct{}{}
			go func(p *store.Page) {
				defer wg.Done()
				defer func() { <-sem }()
				err := r.narratePage(ctx, story, chars, p)
				mu.Lock()
				done++
				if err != nil {
					log.Printf("narrate story %d page %d: %v", storyID, p.Number, err)
					_ = r.st.SetReadingError(ctx, p.ID, err.Error())
					if firstErr == nil {
						firstErr = err
					}
				}
				report(done, total, fmt.Sprintf("Getting the voices ready (%d/%d pages)…", done, total))
				mu.Unlock()
			}(p)
		}
		wg.Wait()
		if firstErr != nil {
			return fmt.Errorf("some pages could not be read aloud: %w", firstErr)
		}
		return nil
	})
}

// NarrationReady reports whether a drawn page can be read aloud as it is:
// read from its current art with sound design done, every spoken line
// voiced in its speaker's voice and every sound effect made.
func NarrationReady(story *store.Story, chars []*store.Character, p *store.Page, lines []*store.PageLine) bool {
	if p.ImageStatus != store.ImageReady || p.Image == "" || p.ReadingImage != p.Image || p.ReadingVersion < pipeline.ReadingVersion {
		return false
	}
	for _, l := range lines {
		if len(l.Words) == 0 {
			continue
		}
		if l.Audio == "" || (l.Sound == "" && l.Voice != VoiceFor(story, chars, l.Speaker).ID) {
			return false
		}
	}
	return true
}

// effectVoice marks a line's clip as a sound effect rather than a voice.
const effectVoice = "effect"

// VoiceFor is the voice a line is read in: its speaker's, or the
// narrator's for captions and unknown speakers.
func VoiceFor(story *store.Story, chars []*store.Character, speaker string) pipeline.Voice {
	if speaker != "" {
		for _, c := range chars {
			if strings.EqualFold(c.Name, speaker) {
				return pipeline.CharacterVoice(c)
			}
		}
	}
	return pipeline.NarratorVoice(story)
}

func (r *Runner) narratePage(ctx context.Context, story *store.Story, chars []*store.Character, p *store.Page) error {
	if p.ReadingImage != p.Image || p.ReadingVersion < pipeline.ReadingVersion {
		png, err := r.st.ReadImage(ctx, p.Image)
		if err != nil {
			return err
		}
		var lines []*store.PageLine
		if p.ReadingImage != p.Image {
			read, err := pipeline.ReadPage(ctx, r.ai, chars, p, png)
			if err != nil {
				return err
			}
			for _, l := range read {
				pl := &store.PageLine{Kind: l.Kind, Speaker: l.Speaker, Text: l.Text, Box: store.LineBox(l.Box), Exact: l.Exact}
				for _, w := range l.Words {
					pl.Words = append(pl.Words, store.LineWord{Text: w.Text, Box: store.LineBox(w.Box)})
				}
				lines = append(lines, pl)
			}
			// Keep the reading even if sound design fails below: reading
			// the page is the expensive half, and it is done.
			if err := r.st.ReplacePageLines(ctx, p.ID, p.Image, 1, lines); err != nil {
				return err
			}
		} else if lines, err = r.st.PageLines(ctx, p.ID); err != nil {
			return err
		}
		designed, err := r.designSounds(ctx, story, p, png, lines)
		if err != nil {
			return err
		}
		if err := r.st.ReplacePageLines(ctx, p.ID, p.Image, pipeline.ReadingVersion, designed); err != nil {
			return err
		}
	}
	lines, err := r.st.PageLines(ctx, p.ID)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, l := range lines {
		if len(l.Words) == 0 {
			continue
		}
		var work func() error
		switch {
		case l.Sound != "":
			if l.Audio != "" {
				continue
			}
			l := l
			work = func() error { return r.soundLine(ctx, story.ID, l) }
		default:
			voice := VoiceFor(story, chars, l.Speaker)
			if l.Audio != "" && l.Voice == voice.ID {
				continue
			}
			l := l
			work = func() error { return r.voiceLine(ctx, story.ID, l, voice.ID) }
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := work(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return r.st.SetReadingError(ctx, p.ID, "")
}

// designSounds runs the sound-design pass over a page's read lines and
// splits them into what is played: a run of speech (with any vocals the
// speaker performs as audio tags) is one voiced line; each sound effect is
// a line of its own, in reading order, in the same balloon. A line that
// stays plain speech is kept as it was, clip included.
func (r *Runner) designSounds(ctx context.Context, story *store.Story, p *store.Page, png []byte, lines []*store.PageLine) ([]*store.PageLine, error) {
	plan := make([]pipeline.PlanLine, len(lines))
	for i, l := range lines {
		plan[i] = pipeline.PlanLine{Kind: l.Kind, Speaker: l.Speaker}
		for _, w := range l.Words {
			plan[i].Words = append(plan[i].Words, w.Text)
		}
	}
	parts, err := pipeline.PlanSounds(ctx, r.ai, story, p, png, plan)
	if err != nil {
		return nil, err
	}
	var out []*store.PageLine
	for i, l := range lines {
		out = append(out, splitLine(l, parts[i])...)
	}
	return out, nil
}

// splitLine applies a line's sound design.
func splitLine(l *store.PageLine, parts []pipeline.Part) []*store.PageLine {
	if len(parts) <= 1 && (len(parts) == 0 || parts[0].Kind == "speech") {
		return []*store.PageLine{l}
	}
	var out []*store.PageLine
	var cur *store.PageLine
	flush := func() {
		if cur != nil {
			cur.Text = joinWords(cur.Words)
			out = append(out, cur)
			cur = nil
		}
	}
	for _, pt := range parts {
		words := make([]store.LineWord, 0, pt.To-pt.From+1)
		for _, w := range l.Words[pt.From : pt.To+1] {
			words = append(words, store.LineWord{Text: w.Text, Box: w.Box})
		}
		if pt.Kind == "effect" {
			flush()
			fx := &store.PageLine{Kind: l.Kind, Speaker: l.Speaker, Box: l.Box, Exact: l.Exact, Words: words, Sound: pt.Sound, Seconds: pt.Seconds}
			fx.Text = joinWords(words)
			out = append(out, fx)
			continue
		}
		if cur == nil {
			cur = &store.PageLine{Kind: l.Kind, Speaker: l.Speaker, Box: l.Box, Exact: l.Exact}
		}
		for i := range words {
			if pt.Kind == "vocal" {
				words[i].Tag = pt.Tag
			}
		}
		cur.Words = append(cur.Words, words...)
	}
	flush()
	return out
}

func joinWords(ws []store.LineWord) string {
	t := make([]string, len(ws))
	for i, w := range ws {
		t[i] = w.Text
	}
	return strings.Join(t, " ")
}

// voiceLine synthesizes one line and times each lettered word. Words the
// speaker performs as a vocal go to the voice as an audio tag and share
// its time.
func (r *Runner) voiceLine(ctx context.Context, storyID int64, l *store.PageLine, voice string) error {
	lettered := make([]pipeline.LetterWord, len(l.Words))
	for i, w := range l.Words {
		lettered[i] = pipeline.LetterWord{Text: w.Text, Tag: w.Tag}
	}
	text, owners := pipeline.Script(lettered)
	sp, err := r.Voice.Speak(ctx, text, voice)
	if err != nil {
		return err
	}
	name, err := r.st.SaveAudio(ctx, storyID, sp.Ext, sp.Audio)
	if err != nil {
		return err
	}
	times := pipeline.AlignTokens(len(l.Words), owners, text, sp.Words)
	words := append([]store.LineWord(nil), l.Words...)
	for i := range words {
		words[i].Start, words[i].End = times[i][0], times[i][1]
	}
	return r.st.SetLineAudio(ctx, l.ID, voice, name, words)
}

// soundLine makes a line's sound effect; its words light up together for
// as long as it plays.
func (r *Runner) soundLine(ctx context.Context, storyID int64, l *store.PageLine) error {
	sp, err := r.Voice.Sound(ctx, l.Sound, l.Seconds)
	if err != nil {
		return err
	}
	name, err := r.st.SaveAudio(ctx, storyID, sp.Ext, sp.Audio)
	if err != nil {
		return err
	}
	words := append([]store.LineWord(nil), l.Words...)
	for i := range words {
		words[i].Start, words[i].End = 0, l.Seconds
	}
	return r.st.SetLineAudio(ctx, l.ID, effectVoice, name, words)
}
