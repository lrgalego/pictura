package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

func TestSoundStudio(t *testing.T) {
	e := newEnv(t)
	e.signup("mixer")
	base, _ := e.finished(t)
	ctx := context.Background()
	pages, _ := e.st.Pages(ctx, 1)
	p := pages[0]
	studio := fmt.Sprintf("%s/studio/%d", base, p.ID)

	// Simple by default: no studio links; the switch turns them on and
	// the browser remembers.
	_, body := e.get(base + "/book")
	if strings.Contains(body, studio) || !strings.Contains(body, "Sound studio") {
		t.Fatal("the studio is off by default, with a switch to turn it on")
	}
	resp, body := e.post("/prefs/studio", url.Values{"story": {"1"}, "studio": {"on"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, studio) {
		t.Fatalf("switch on: %d", resp.StatusCode)
	}
	if _, body = e.get(base + "/book"); !strings.Contains(body, studio) {
		t.Fatal("the choice should stick")
	}

	// The studio lists every line with its sound.
	resp, body = e.get(studio)
	lines, _ := e.st.PageLines(ctx, p.ID)
	if resp.StatusCode != http.StatusOK || strings.Count(body, `class="cue cue--`) != len(lines) || !strings.Contains(body, "Sound studio") || !strings.Contains(body, "/static/app/studio.js?v=") {
		t.Fatalf("studio: %d, %d cues for %d lines", resp.StatusCode, strings.Count(body, `class="cue cue--`), len(lines))
	}
	if !strings.Contains(body, `data-listen="/media/`+lines[0].Audio+`"`) || !strings.Contains(body, "4 of 4 ready") && !strings.Contains(body, "ready") {
		t.Fatal("a made line plays from the studio")
	}
	if !strings.Contains(body, "Play this page") || !strings.Contains(body, fmt.Sprintf("/read?page=%d", p.Number)) {
		t.Fatal("the studio plays its page in the reader")
	}

	// A failed line shows why, and the page card offers a simple retry.
	_ = e.st.SetLineAudio(ctx, lines[0].ID, "", "", lines[0].Words) // never made...
	_ = e.st.SetLineError(ctx, lines[0].ID, "elevenlabs: the voice service returned no audio")
	_, body = e.get(studio)
	if !strings.Contains(body, "cue--failed") || !strings.Contains(body, "returned no audio") || !strings.Contains(body, "Retry failed lines") {
		t.Fatal("failed line in the studio")
	}
	e.post("/prefs/studio", url.Values{"story": {"1"}}, true) // switched off: no "studio" field
	_, body = e.get(base + "/book")
	if !strings.Contains(body, "1 line couldn&#39;t be voiced") || !strings.Contains(body, fmt.Sprintf("/read/pages/%d/retry", p.ID)) || strings.Contains(body, studio) {
		t.Fatal("simple mode: the trouble and a Try again, no studio")
	}
	d := e.readJSON(base)
	if d.Pages[0].Status != readReady || d.Pages[0].Failed != 1 || len(d.Pages[0].Lines) != len(lines)-1 {
		t.Fatalf("the reader skips the failed line: %+v", d.Pages[0])
	}

	// Try again from the Comic step: only that page, and it heals.
	resp, body = e.post(fmt.Sprintf("%s/read/pages/%d/retry", base, p.ID), nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Trying page 1 again") {
		t.Fatalf("retry page: %d", resp.StatusCode)
	}
	e.waitIdle()
	if l, _ := e.st.PageLine(ctx, lines[0].ID); l.Error != "" || l.Audio == "" {
		t.Fatalf("after retry: %+v", l)
	}
	// From the reader (fetch, not htmx): JSON.
	resp, body = e.post(fmt.Sprintf("%s/read/pages/%d/retry", base, p.ID), nil, false)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"started":true`) {
		t.Fatalf("retry from the reader: %d %s", resp.StatusCode, body)
	}
	e.waitIdle()

	// Remake and direct one line from the studio.
	e.target = "studio-panel"
	lines, _ = e.st.PageLines(ctx, p.ID)
	resp, body = e.post(fmt.Sprintf("%s/studio/lines/%d/retry", base, lines[1].ID), nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Making the line again") || !strings.Contains(body, `id="studio-panel"`) {
		t.Fatalf("remake: %d", resp.StatusCode)
	}
	e.waitIdle()
	resp, _ = e.post(fmt.Sprintf("%s/studio/lines/%d/adjust", base, lines[1].ID), url.Values{"direction": {"  "}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty direction: %d", resp.StatusCode)
	}
	resp, body = e.post(fmt.Sprintf("%s/studio/lines/%d/adjust", base, lines[1].ID), url.Values{"direction": {"make it a sound effect"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Directing the line") {
		t.Fatalf("direct: %d", resp.StatusCode)
	}
	e.waitIdle()
	_, body = e.get(studio + "/panel")
	if !strings.Contains(body, "Your note: make it a sound effect") || !strings.Contains(body, "💥 Sound effect") {
		t.Fatal("the directed line shows as an effect with the note")
	}
	e.target = ""

	// Owner only; unknown things are 404.
	other := newEnvClient(t, e)
	for _, u := range []string{studio, studio + "/panel"} {
		if resp, _ := other.get(u); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", u, resp.StatusCode)
		}
	}
	for _, u := range []string{fmt.Sprintf("%s/studio/lines/%d/retry", base, lines[0].ID), fmt.Sprintf("%s/read/pages/%d/retry", base, p.ID)} {
		if resp, _ := other.post(u, nil, true); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", u, resp.StatusCode)
		}
	}
	for _, u := range []string{base + "/studio/99999", base + "/studio/lines/99999/retry"} {
		var resp *http.Response
		if strings.HasSuffix(u, "retry") {
			resp, _ = e.post(u, nil, true)
		} else {
			resp, _ = e.get(u)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", u, resp.StatusCode)
		}
	}
	// The switch without a story still answers.
	if resp, _ := e.post("/prefs/studio", nil, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("switch without a story: %d", resp.StatusCode)
	}
}

func TestStudioForAnUnreadPage(t *testing.T) {
	e := newEnv(t)
	e.signup("early2")
	base, _ := e.finished(t)
	ctx := context.Background()
	pages, _ := e.st.Pages(ctx, 1)
	_ = e.st.ReplacePageLines(ctx, pages[0].ID, "", 1, nil)
	_ = e.st.SetReadingError(ctx, pages[0].ID, "meta api: down")
	_, body := e.get(fmt.Sprintf("%s/studio/%d", base, pages[0].ID))
	if !strings.Contains(body, "Nothing to hear yet") || !strings.Contains(body, "couldn&#39;t be read aloud") || !strings.Contains(body, "Prepare this page") {
		t.Fatal("an unread page explains itself and offers to prepare it")
	}
	var _ = store.ErrNotFound
	var _ = pipeline.ReadingVersion
}
