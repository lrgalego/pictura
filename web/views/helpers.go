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
