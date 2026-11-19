package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lrgalego/pictura/internal/models"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

// ---------- step 4: the world ----------

// DesignWorld finds the story's places and props and describes them,
// replacing any found before. Like the cast, nothing is drawn yet: the
// writer reads the list first, then asks for the art with DrawWorld.
func (r *Runner) DesignWorld(storyID int64) error {
	return r.start(storyID, "world", "Finding the places and props…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		specs, err := pipeline.DesignWorld(ctx, r.aiFor(story, models.Script), story, chars)
		if err != nil {
			return err
		}
		if err := r.st.DeleteElements(ctx, storyID); err != nil {
			return err
		}
		for i, s := range specs {
			if err := r.st.InsertElement(ctx, elementFromSpec(storyID, i, s)); err != nil {
				return err
			}
		}
		r.invalidatePages(ctx, storyID)
		return nil
	})
}

// DrawWorld draws the concept art of every place and prop not drawn yet.
func (r *Runner) DrawWorld(storyID int64) error {
	return r.start(storyID, "world-art", "Drawing the concept art…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		world, err := r.st.Elements(ctx, storyID)
		if err != nil {
			return err
		}
		var todo []*store.Element
		for _, e := range world {
			if e.ArtStatus != store.ImageReady {
				todo = append(todo, e)
			}
		}
		return r.drawWorld(ctx, story, todo, false, report)
	})
}

// AdjustWorld revises the places and props from feedback. Those whose
// description changed, and newcomers, are drawn again once the art has
// started; the rest keep their art.
func (r *Runner) AdjustWorld(storyID int64, feedback string) error {
	return r.start(storyID, "world-adjust", "Revising the places and props…", 0, func(ctx context.Context, report reporter) error {
		story, err := r.st.Story(ctx, storyID)
		if err != nil {
			return err
		}
		chars, err := r.st.Characters(ctx, storyID)
		if err != nil {
			return err
		}
		existing, err := r.st.Elements(ctx, storyID)
		if err != nil {
			return err
		}
		specs, err := pipeline.AdjustWorld(ctx, r.aiFor(story, models.Script), story, chars, existing, feedback)
		if err != nil {
			return err
		}
		artPhase := WorldStarted(existing)
		byName := map[string]*store.Element{}
		for _, e := range existing {
			byName[strings.ToLower(e.Name)] = e
		}
		var redraw []*store.Element
		for i, s := range specs {
			if e, ok := byName[strings.ToLower(s.Name)]; ok {
				changed := e.Visual != s.Visual || e.Layout != s.Layout || e.Kind != s.Kind || e.ArtStatus != store.ImageReady
				e.Position, e.Kind, e.Name, e.Visual, e.Layout = i, s.Kind, s.Name, s.Visual, s.Layout
				if err := r.st.UpdateElement(ctx, e); err != nil {
					return err
				}
				delete(byName, strings.ToLower(s.Name))
				if changed {
					redraw = append(redraw, e)
				}
				continue
			}
			e := elementFromSpec(storyID, i, s)
			if err := r.st.InsertElement(ctx, e); err != nil {
				return err
			}
			redraw = append(redraw, e)
		}
		for _, gone := range byName {
			_ = r.st.DeleteElement(ctx, gone.ID)
		}
		r.invalidatePages(ctx, storyID)
		if !artPhase {
			return nil
		}
		return r.drawWorld(ctx, story, redraw, false, report)
	})
}

// ReviseElement rewrites one place or prop from feedback and, once its art
// has started, draws it again.
func (r *Runner) ReviseElement(storyID, elementID int64, feedback string) error {
	return r.start(storyID, "element", "Revising a place or prop…", 0, func(ctx context.Context, report reporter) error {
		story, e, err := r.element(ctx, storyID, elementID)
		if err != nil {
			return err
		}
		report(0, 0, "Revising "+e.Name+"…")
		spec, err := pipeline.AdjustElement(ctx, r.aiFor(story, models.Script), story, e, feedback)
		if err != nil {
			return err
		}
		e.Name, e.Visual, e.Layout = spec.Name, spec.Visual, spec.Layout
		if err := r.st.UpdateElement(ctx, e); err != nil {
			return err
		}
		if e.ArtStatus == store.ImagePending {
			return nil
		}
		r.invalidatePages(ctx, storyID)
		report(0, 1, "Drawing "+e.Name+"…")
		return r.drawElement(ctx, story, e, false)
	})
}

// RedrawElement draws one place or prop again from its description.
func (r *Runner) RedrawElement(storyID, elementID int64) error {
	return r.start(storyID, "element-art", "Drawing a place or prop…", 1, func(ctx context.Context, report reporter) error {
		story, e, err := r.element(ctx, storyID, elementID)
		if err != nil {
			return err
		}
		report(0, 1, "Drawing "+e.Name+"…")
		r.invalidatePages(ctx, storyID)
		return r.drawElement(ctx, story, e, false)
	})
}

// ElementFields is what the edit dialog can change.
type ElementFields struct {
	Name, Visual, Layout string
	Redraw               bool // draw it again if the look changed and art exists
}

// EditElement applies hand-written fields, queued behind whatever runs.
func (r *Runner) EditElement(storyID, elementID int64, f ElementFields) error {
	return r.start(storyID, "element-edit", "Saving your edits…", 0, func(ctx context.Context, report reporter) error {
		story, e, err := r.element(ctx, storyID, elementID)
		if err != nil {
			return err
		}
		looksChanged := e.Visual != f.Visual || e.Layout != f.Layout
		e.Name, e.Visual, e.Layout = f.Name, f.Visual, f.Layout
		if err := r.st.UpdateElement(ctx, e); err != nil {
			return err
		}
		if !(looksChanged && f.Redraw && e.ArtStatus != store.ImagePending) {
			return nil
		}
		r.invalidatePages(ctx, storyID)
		report(0, 1, "Drawing "+e.Name+" with the new details…")
		return r.drawElement(ctx, story, e, false)
	})
}

func (r *Runner) element(ctx context.Context, storyID, elementID int64) (*store.Story, *store.Element, error) {
	story, err := r.st.Story(ctx, storyID)
	if err != nil {
		return nil, nil, err
	}
	e, err := r.st.Element(ctx, elementID)
	if err != nil || e.StoryID != storyID {
		return nil, nil, store.ErrNotFound
	}
	return story, e, nil
}

func elementFromSpec(storyID int64, pos int, s pipeline.ElementSpec) *store.Element {
	return &store.Element{StoryID: storyID, Position: pos, Kind: s.Kind, Name: s.Name, Visual: s.Visual, Layout: s.Layout, ArtStatus: store.ImagePending}
}

// WorldReady reports whether every place and prop has its art: the
// condition for storyboarding. A story with none is ready.
func WorldReady(world []*store.Element) bool {
	for _, e := range world {
		if e.ArtStatus != store.ImageReady {
			return false
		}
	}
	return true
}

// WorldStarted reports whether any concept art has been drawn or tried.
func WorldStarted(world []*store.Element) bool {
	for _, e := range world {
		if e.ArtStatus != store.ImagePending {
			return true
		}
	}
	return false
}

// drawWorld draws concept art concurrently, reporting progress; with
// restyle each is redrawn from the art it replaces.
func (r *Runner) drawWorld(ctx context.Context, story *store.Story, world []*store.Element, restyle bool, report reporter) error {
	total := len(world)
	if total == 0 {
		return nil
	}
	verb := "Drawing the concept art"
	if restyle {
		verb = "Redrawing the places and props in the new look"
	}
	var mu sync.Mutex
	done := 0
	var firstErr error
	report(0, total, fmt.Sprintf("%s (0/%d)…", verb, total))
	var wg sync.WaitGroup
	for _, e := range world {
		wg.Add(1)
		go func(e *store.Element) {
			defer wg.Done()
			err := r.drawElement(ctx, story, e, restyle)
			mu.Lock()
			done++
			if err != nil && firstErr == nil {
				firstErr = err
			}
			report(done, total, fmt.Sprintf("%s (%d/%d)…", verb, done, total))
			mu.Unlock()
		}(e)
	}
	wg.Wait()
	if firstErr != nil {
		return fmt.Errorf("some concept art failed: %w", firstErr)
	}
	return nil
}

func (r *Runner) drawElement(ctx context.Context, story *store.Story, e *store.Element, restyle bool) error {
	r.imgSem <- struct{}{}
	defer func() { <-r.imgSem }()
	fail := func(err error) error {
		_ = r.st.SetElementArt(ctx, e.ID, e.ArtImage, store.ImageError, err.Error())
		return err
	}
	var old []byte
	if restyle {
		var err error
		if old, err = r.st.ReadImage(ctx, e.ArtImage); err != nil {
			return fail(err)
		}
	}
	_ = r.st.SetElementArt(ctx, e.ID, e.ArtImage, store.ImageGenerating, "")
	ai := r.aiFor(story, models.World)
	var png []byte
	var err error
	if old != nil {
		png, err = ai.EditImage(ctx, pipeline.RestyleElementPrompt(story, e), pipeline.ElementRefs(e, old), pipeline.SheetSize)
	} else {
		png, err = ai.GenerateImage(ctx, pipeline.ElementPrompt(story, e), pipeline.SheetSize)
	}
	if err != nil {
		return fail(err)
	}
	name, err := r.st.SaveImage(ctx, story.ID, "png", png)
	if err != nil {
		return fail(err)
	}
	e.ArtImage, e.ArtStatus, e.ArtError = name, store.ImageReady, ""
	return r.st.SetElementArt(ctx, e.ID, name, store.ImageReady, "")
}

// worldArt loads the finished concept art, keyed by element id.
func (r *Runner) worldArt(ctx context.Context, storyID int64) ([]*store.Element, map[int64][]byte, error) {
	all, err := r.st.Elements(ctx, storyID)
	if err != nil {
		return nil, nil, err
	}
	var world []*store.Element
	art := map[int64][]byte{}
	for _, e := range all {
		if e.ArtStatus != store.ImageReady || e.ArtImage == "" {
			continue
		}
		b, err := r.st.ReadImage(ctx, e.ArtImage)
		if err != nil {
			return nil, nil, err
		}
		world = append(world, e)
		art[e.ID] = b
	}
	return world, art, nil
}
