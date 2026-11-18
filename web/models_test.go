package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/models"
	"github.com/lrgalego/pictura/internal/store"
)

func TestModelPickers(t *testing.T) {
	e := newEnv(t)
	e.signup("mara")

	// The script reader is picked with the script, before the story exists.
	_, body := e.get("/stories/new")
	if !strings.Contains(body, `name="model_script"`) || !strings.Contains(body, "Gemini 4 Hydrogen") || strings.Contains(body, "Nano Banana") {
		t.Fatalf("new story picker missing or wrong:\n%s", body)
	}
	resp, _ := e.post("/stories", url.Values{"title": {"Robot"}, "script": {script}, "model_script": {"gemini-4-hydrogen"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	e.waitIdle()
	stories, _ := e.st.StoriesByUser(context.Background(), 1)
	st := stories[0]
	if st.Models[models.Script] != "gemini-4-hydrogen" {
		t.Fatalf("script pick: %+v", st.Models)
	}
	base := fmt.Sprintf("/stories/%d", st.ID)
	if resp, _ := e.post(base+"/look", url.Values{"style": {"manga"}}, false); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("look: %d", resp.StatusCode)
	}
	e.waitIdle()
	if err := e.st.SetStep(context.Background(), st.ID, store.StepBook); err != nil {
		t.Fatal(err)
	}

	// Every step shows the picker for what it makes.
	for path, want := range map[string]string{
		"/script":     `id="model-pick-script"`,
		"/look":       `id="model-pick-sheets"`,
		"/characters": `id="model-pick-sheets"`,
		"/book":       `id="model-pick-voice"`,
	} {
		if _, body := e.get(base + path); !strings.Contains(body, want) {
			t.Errorf("%s: no %s", path, want)
		}
	}
	if _, body := e.get(base + "/book"); !strings.Contains(body, `id="model-pick-pages"`) {
		t.Error("the comic step picks who draws the pages")
	}
	if err := e.st.InsertPage(context.Background(), &store.Page{StoryID: st.ID, Number: 1, Panels: []store.Panel{{Number: 1, Description: "Mara on the stairs"}}}); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(base + "/pages")
	if !strings.Contains(body, `id="model-pick-storyboard"`) || !strings.Contains(body, `id="model-pick-pages"`) {
		t.Error("the storyboard picks who plans and who draws")
	}

	// A pick is saved as soon as it is made and the picker says so.
	resp, body = e.post(base+"/models", url.Values{"role": {models.Pages}, "model": {"gemini-3-pro-image"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Saved") || !strings.Contains(body, `value="gemini-3-pro-image" selected`) {
		t.Fatalf("save: %d\n%s", resp.StatusCode, body)
	}
	got, _ := e.st.Story(context.Background(), st.ID)
	if got.Models[models.Pages] != "gemini-3-pro-image" || got.Models[models.Script] != "gemini-4-hydrogen" {
		t.Errorf("picks: %+v", got.Models)
	}

	// Changing the reader on the script form keeps the script as it is.
	resp, _ = e.post(base+"/script", url.Values{"title": {"Robot"}, "script": {script}, "model_script": {"gemini-4-argon"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("script save: %d", resp.StatusCode)
	}
	got, _ = e.st.Story(context.Background(), st.ID)
	if got.Models[models.Script] != "gemini-4-argon" {
		t.Errorf("reader pick: %+v", got.Models)
	}

	// Only models on offer for the role are taken.
	for _, form := range []url.Values{
		{"role": {models.Pages}, "model": {"gemini-4-hydrogen"}},
		{"role": {"nonsense"}, "model": {"gemini-3-pro-image"}},
		{"role": {models.Voices}, "model": {"made-up"}},
	} {
		if resp, _ := e.post(base+"/models", form, true); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d", form, resp.StatusCode)
		}
	}

	// Someone else's story is not there to change.
	other := newEnvClient(t, e)
	other.signup("graves")
	if resp, _ := other.post(base+"/models", url.Values{"role": {models.Pages}, "model": {"gemini-3-pro-image"}}, true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another user's story: %d", resp.StatusCode)
	}
}

func TestNoPickerWithoutAChoice(t *testing.T) {
	s := &server{}
	if p := s.modelPick(nil, models.Pages); len(p.Options) != 0 {
		t.Errorf("no router, no picker: %+v", p)
	}
	if s.validModel(models.Pages, "muse-image-1.0") {
		t.Error("no router, no valid model")
	}
}
