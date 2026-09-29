package views

import (
	"fmt"
	"strings"

	"github.com/lrgalego/pictura/internal/store"
)

func refsFor(refs []*store.Ref, characterID int64) []*store.Ref {
	var out []*store.Ref
	for _, r := range refs {
		if r.CharacterID == characterID {
			out = append(out, r)
		}
	}
	return out
}

func base(st *store.Story) string { return fmt.Sprintf("/stories/%d", st.ID) }

func stepHref(st *store.Story, n int) string {
	switch n {
	case store.StepCharacters:
		return base(st) + "/characters"
	case store.StepPages:
		return base(st) + "/pages"
	case store.StepBook:
		return base(st) + "/book"
	}
	return base(st) + "/script"
}

func stepLabel(step int) string {
	switch step {
	case store.StepCharacters:
		return "Step 2 of 4: characters"
	case store.StepPages:
		return "Step 3 of 4: pages"
	case store.StepBook:
		return "Step 4 of 4: comic"
	}
	return "Step 1 of 4: script"
}

func titleOr(st *store.Story) string {
	if strings.TrimSpace(st.Title) == "" {
		return "Untitled story"
	}
	return st.Title
}

func initials(name string) string {
	var b strings.Builder
	for _, w := range strings.Fields(name) {
		b.WriteString(strings.ToUpper(w[:1]))
		if b.Len() >= 2 {
			break
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

func scriptAction(f ScriptFormData) string {
	if f.Story == nil {
		return "/stories"
	}
	return base(f.Story) + "/script"
}

// readyIndex maps a page id to its index among the drawn pages — the
// lightbox only knows those.
func readyIndex(pages []*store.Page) map[int64]int {
	m := map[int64]int{}
	i := 0
	for _, p := range pages {
		if p.ImageStatus == store.ImageReady {
			m[p.ID] = i
			i++
		}
	}
	return m
}

func readyCount(pages []*store.Page) int {
	n := 0
	for _, p := range pages {
		if p.ImageStatus == store.ImageReady {
			n++
		}
	}
	return n
}

func panelCount(pages []*store.Page) int {
	n := 0
	for _, p := range pages {
		n += len(p.Panels)
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// genericTitle is true when the page title just repeats its number.
func genericTitle(p *store.Page) bool {
	t := strings.ToLower(strings.TrimSpace(p.Title))
	return t == "" || t == fmt.Sprintf("page %d", p.Number)
}

func readySheets(chars []*store.Character) int {
	n := 0
	for _, c := range chars {
		if c.SheetStatus == store.ImageReady {
			n++
		}
	}
	return n
}

// jobFailure words a failed story-level job for the writer: what failed,
// in the terms of the step, and what it means. The model's own error stays
// available under "Details".
func jobFailure(j *store.Job) (title, hint string) {
	switch j.Kind {
	case "analyze":
		title = "Reading the script failed"
	case "cast":
		title = "Revising the cast failed"
	case "sheets":
		title = "Drawing the character sheets failed"
	case "breakdown":
		title = "Storyboarding failed"
	case "pages":
		title = "Revising the pages failed"
	case "page":
		title = "Revising the page failed"
	case "render":
		title = "Drawing the pages failed"
	case "render-page":
		title = "Redrawing the page failed"
	case "narrate":
		title = "Getting the book ready to read aloud failed"
	case "narrate-page":
		title = "Getting the page ready to read aloud failed"
	case "line":
		title = "Remaking the line failed"
	default:
		title = "The last step failed"
	}
	switch e := strings.ToLower(j.Error); {
	case strings.Contains(e, "server restart"):
		hint = "Pictura restarted while this was running. Nothing is lost; start it again."
	case strings.Contains(e, "elevenlabs"), strings.Contains(e, "voice service"):
		hint = "The voice service didn't answer properly — usually a hiccup on their side. Everything that worked is kept; trying again only makes what's missing."
	case strings.Contains(e, "meta api"), strings.Contains(e, "fake ai"), strings.Contains(e, "deadline"), strings.Contains(e, "timeout"):
		hint = "The writing and drawing models didn't answer properly. That's usually a hiccup on their side; try again in a moment."
	default:
		hint = "Something went wrong on our side. Try again; if it keeps failing, change the request a little."
	}
	return title, hint
}

// retryAction starts a failed job again from the step that shows it. A
// revision needs the notes again, so it reopens the adjust dialog.
type retryAction struct {
	URL, Label string
	Dialog     bool
}

func jobRetry(st *store.Story, j *store.Job, step int) (retryAction, bool) {
	switch {
	case step == store.StepCharacters && j.Kind == "analyze":
		return retryAction{URL: base(st) + "/characters/read", Label: "Read the script again"}, true
	case step == store.StepCharacters && j.Kind == "sheets":
		return retryAction{URL: base(st) + "/characters/draw", Label: "Draw the sheets again"}, true
	case step == store.StepCharacters && j.Kind == "cast":
		return retryAction{URL: base(st) + "/characters/adjust", Label: "Adjust the cast again", Dialog: true}, true
	case step == store.StepPages && j.Kind == "breakdown":
		return retryAction{URL: base(st) + "/pages/restart", Label: "Storyboard again"}, true
	case step == store.StepPages && j.Kind == "pages":
		return retryAction{URL: base(st) + "/pages/adjust", Label: "Adjust the pages again", Dialog: true}, true
	case step == store.StepBook && (j.Kind == "render" || j.Kind == "render-page"):
		return retryAction{URL: base(st) + "/book/draw", Label: "Draw the missing pages"}, true
	case step == store.StepBook && (j.Kind == "narrate" || j.Kind == "narrate-page"):
		return retryAction{URL: base(st) + "/read/prepare", Label: "Try reading aloud again"}, true
	}
	return retryAction{}, false
}
