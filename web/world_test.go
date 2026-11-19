package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/store"
)

// atTheWorld walks a new story to the world step, its places and props
// found but not drawn.
func (e *env) atTheWorld(t *testing.T) string {
	t.Helper()
	resp, _ := e.create(url.Values{"title": {"Book"}, "script": {script}, "style": {"comic"}})
	base := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	e.post(base+"/characters/draw", nil, true)
	e.waitIdle()
	e.post(base+"/characters/approve", nil, true)
	e.waitIdle()
	return base
}

func TestWorldStep(t *testing.T) {
	e := newEnv(t)
	e.signup("designer")
	resp, _ := e.create(url.Values{"title": {"Early"}, "script": {script}, "style": {"comic"}})
	early := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	if resp, _ := e.get(early + "/world"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the world waits for the cast: %d", resp.StatusCode)
	}

	base := e.atTheWorld(t)
	ctx := context.Background()
	world, _ := e.st.Elements(ctx, 2)
	if len(world) != 2 {
		t.Fatalf("found: %+v", world)
	}
	place, prop := world[0], world[1]
	el := func(x *store.Element) string { return fmt.Sprintf("%s/world/%d", base, x.ID) }

	resp, body := e.get(base + "/world")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, ">Places<") || !strings.Contains(body, ">Props<") || !strings.Contains(body, "Concept art drawn by") || !strings.Contains(body, "Not drawn yet") {
		t.Fatalf("world page: %d", resp.StatusCode)
	}
	if _, body = e.get(base + "/world/panel"); !strings.Contains(body, `id="step-panel"`) {
		t.Fatal("panel")
	}
	// The characters step now leads on to the world.
	if _, body = e.get(base + "/characters"); !strings.Contains(body, "On to the world") {
		t.Fatal("the cast step points at the world it has")
	}
	// Coming back from the cast keeps what was found.
	resp, _ = e.post(base+"/characters/approve", nil, true)
	if resp.Header.Get("HX-Redirect") != base+"/world" {
		t.Fatalf("back to the world: %s", resp.Header.Get("HX-Redirect"))
	}
	e.waitIdle()
	if again, _ := e.st.Elements(ctx, 2); again[0].ID != place.ID {
		t.Fatal("the world is not found again on a second visit")
	}

	// Dialogs.
	for path, want := range map[string]string{
		base + "/world/adjust": "Adjust the places and props",
		el(prop) + "/adjust":   "Change the brass lantern",
		el(prop) + "/edit":     "Size and use",
		el(place) + "/edit":    "Layout",
		el(prop) + "/delete":   "Remove the brass lantern?",
	} {
		if resp, body := e.get(path); resp.StatusCode != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("%s: %d, want %q", path, resp.StatusCode, want)
		}
	}
	if resp, _ := e.get(base + "/world/999/edit"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("someone else's element: %d", resp.StatusCode)
	}

	// Empty notes are refused; real ones run.
	for _, path := range []string{base + "/world/adjust", el(prop) + "/adjust"} {
		if resp, body := e.post(path, url.Values{"feedback": {""}}, true); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Say what should change") {
			t.Fatalf("%s empty: %d", path, resp.StatusCode)
		}
	}
	if resp, body = e.post(el(prop)+"/adjust", url.Values{"feedback": {"make it green"}}, true); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Changing the brass lantern") {
		t.Fatalf("element adjust: %d", resp.StatusCode)
	}
	e.waitIdle()
	if got, _ := e.st.Element(ctx, prop.ID); !strings.Contains(got.Visual, "revised: make it green") {
		t.Fatalf("revised: %+v", got)
	}
	if resp, _ = e.post(base+"/world/adjust", url.Values{"feedback": {"foggier"}}, true); resp.StatusCode != http.StatusOK {
		t.Fatalf("world adjust: %d", resp.StatusCode)
	}
	e.waitIdle()

	// Edits: a name is required.
	if resp, body = e.post(el(prop)+"/edit", url.Values{"name": {""}, "visual": {"v"}}, true); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Give it a name") {
		t.Fatalf("nameless edit: %d", resp.StatusCode)
	}
	if resp, _ = e.post(el(prop)+"/edit", url.Values{"name": {"the tin lamp"}, "visual": {"a tin lamp"}, "layout": {"small"}}, true); resp.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	e.waitIdle()
	if got, _ := e.st.Element(ctx, prop.ID); got.Name != "the tin lamp" || got.Layout != "small" {
		t.Fatalf("edited: %+v", got)
	}

	// Draw one, then the rest; the art opens in the viewer.
	if resp, body = e.post(el(place)+"/redraw", nil, true); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Drawing the harbour at dusk") {
		t.Fatalf("draw one: %d", resp.StatusCode)
	}
	e.waitIdle()
	if _, body = e.get(base + "/world"); !strings.Contains(body, "Draw the remaining art") || !strings.Contains(body, "Art ready") {
		t.Fatal("one drawn, the rest to go")
	}
	e.post(base+"/world/draw", nil, true)
	e.waitIdle()
	if resp, body = e.get(el(prop) + "/view?img=1"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "the tin lamp — concept art") {
		t.Fatalf("viewer: %d", resp.StatusCode)
	}
	if resp, body = e.post(el(prop)+"/redraw", nil, true); !strings.Contains(body, "Redrawing the tin lamp") {
		t.Fatalf("redraw: %d", resp.StatusCode)
	}
	e.waitIdle()

	// Remove one; the storyboard then draws on what is left.
	if resp, body = e.post(el(prop)+"/delete", nil, true); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Removed the tin lamp") {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if left, _ := e.st.Elements(ctx, 2); len(left) != 1 {
		t.Fatalf("left: %+v", left)
	}
	if resp, body = e.get(el(prop) + "/view"); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer after delete: %d", resp.StatusCode)
	}
	resp, _ = e.post(base+"/world/approve", nil, true)
	if resp.Header.Get("HX-Redirect") != base+"/pages" {
		t.Fatalf("storyboard: %s", resp.Header.Get("HX-Redirect"))
	}
	e.waitIdle()
	if _, body = e.get(base + "/world"); !strings.Contains(body, "Keep the storyboard") || !strings.Contains(body, "Storyboard again") {
		t.Fatal("with pages planned, the world offers to keep them")
	}
}

func TestWorldWithNothingFound(t *testing.T) {
	e := newEnv(t)
	e.signup("minimalist")
	base := e.atTheWorld(t)
	ctx := context.Background()
	_ = e.st.DeleteElements(ctx, 1)
	_, body := e.get(base + "/world")
	if !strings.Contains(body, "No places or props yet") || !strings.Contains(body, "Find the places and props") || !strings.Contains(body, "Storyboard without them") {
		t.Fatal("an empty world offers to look or to move on")
	}
	if resp, _ := e.get(base + "/world/1/view"); resp.StatusCode != http.StatusOK {
		t.Fatal("an empty viewer answers")
	}
	if _, body = e.post(base+"/world/read", nil, true); !strings.Contains(body, "Reading the script for places and props") {
		t.Fatal("read")
	}
	e.waitIdle()
	if w, _ := e.st.Elements(ctx, 1); len(w) != 2 {
		t.Fatalf("found again: %+v", w)
	}
	_ = e.st.DeleteElements(ctx, 1)
	resp, _ := e.post(base+"/world/approve", nil, true)
	if resp.Header.Get("HX-Redirect") != base+"/pages" {
		t.Fatal("a story with no places or props storyboards straight away")
	}
}
