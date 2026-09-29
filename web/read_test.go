package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func (e *env) readJSON(base string) readData {
	e.t.Helper()
	resp, body := e.get(base + "/read.json")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		e.t.Fatalf("read.json: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var d readData
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func TestReadToMe(t *testing.T) {
	e := newEnv(t)
	e.signup("bedtime")
	base, _ := e.finished(t)
	ctx := context.Background()

	// The comic step offers it; the reader page loads its player.
	_, body := e.get(base + "/book")
	if !strings.Contains(body, `href="`+base+`/read"`) || !strings.Contains(body, "Read to me") {
		t.Fatal("the comic should offer Read to me")
	}
	resp, body := e.get(base + "/read")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="reader"`) || !strings.Contains(body, `data-src="`+base+`/read.json"`) || !strings.Contains(body, "/static/app/reader.js?v=") {
		t.Fatalf("reader page: %d", resp.StatusCode)
	}

	// Drawing the book prepared everything: every page is ready, every line
	// has a clip, a speaker's voice and timed words inside its balloon.
	d := e.readJSON(base)
	if d.Preparing || d.Title != "Book" || len(d.Pages) == 0 {
		t.Fatalf("state: %+v", d)
	}
	for _, p := range d.Pages {
		if p.Status != readReady || !strings.HasPrefix(p.Image, "/media/") || len(p.Lines) == 0 {
			t.Fatalf("page %d: %+v", p.Number, p)
		}
		for _, l := range p.Lines {
			if !strings.HasPrefix(l.Audio, "/media/") || len(l.Words) == 0 {
				t.Fatalf("line: %+v", l)
			}
			w := l.Words[len(l.Words)-1]
			if w.End <= 0 || w.Box.X1 < l.Box.X1 || w.Box.X2 > l.Box.X2 {
				t.Fatalf("word: %+v in %+v", w, l.Box)
			}
		}
	}
	// Clips are served like images, to their owner only.
	resp, body = e.get(d.Pages[0].Lines[0].Audio)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/wav" || !strings.HasPrefix(body, "RIFF") {
		t.Fatalf("clip: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	other := newEnvClient(t, e)
	for _, u := range []string{base + "/read", base + "/read.json", d.Pages[0].Lines[0].Audio} {
		if resp, _ := other.get(u); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s must be owner-only: %d", u, resp.StatusCode)
		}
	}
	if resp, _ := other.post(base+"/read/prepare", nil, false); resp.StatusCode != http.StatusNotFound {
		t.Fatal("prepare must be owner-only")
	}

	// Nothing to prepare: prepare is a no-op.
	resp, body = e.post(base+"/read/prepare", nil, false)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"started":false`) {
		t.Fatalf("prepare when ready: %d %s", resp.StatusCode, body)
	}

	// A new voice for a character makes their pages stale until prepared
	// again; a held job shows as preparing.
	chars, _ := e.st.Characters(ctx, 1)
	_ = e.st.SetCharacterVoice(ctx, chars[0].ID, "pqHfZKP75CvOlQylNhV4")
	d = e.readJSON(base)
	stale := 0
	for _, p := range d.Pages {
		if p.Status == readStale {
			stale++
			if len(p.Lines) != 0 {
				t.Fatal("a stale page is not playable")
			}
		}
	}
	if stale == 0 {
		t.Fatalf("changing a voice should make pages stale: %+v", d.Pages)
	}
	e.hold()
	resp, body = e.post(base+"/read/prepare", nil, false)
	if !strings.Contains(body, `"started":true`) {
		t.Fatalf("prepare: %s", body)
	}
	d = e.readJSON(base)
	if !d.Preparing || d.Message == "" {
		t.Fatalf("preparing: %+v", d)
	}
	e.release()
	e.waitIdle()
	for _, p := range e.readJSON(base).Pages {
		if p.Status != readReady {
			t.Fatalf("after preparing: %+v", p)
		}
	}

	// A failure shows per page and prepare retries it.
	pages, _ := e.st.Pages(ctx, 1)
	_ = e.st.SetCharacterVoice(ctx, chars[0].ID, "onwK4e9ZLuTAKqWW03F9")
	_ = e.st.SetCharacterVoice(ctx, chars[1].ID, "onwK4e9ZLuTAKqWW03F9")
	_ = e.st.SetCharacterVoice(ctx, chars[2].ID, "onwK4e9ZLuTAKqWW03F9")
	_ = e.st.SetReadingError(ctx, pages[0].ID, "voice down")
	d = e.readJSON(base)
	if d.Pages[0].Status != readError || d.Pages[0].Error != "voice down" {
		t.Fatalf("error page: %+v", d.Pages[0])
	}
	if _, body := e.post(base+"/read/prepare", nil, false); !strings.Contains(body, `"started":true`) {
		t.Fatalf("retry: %s", body)
	}
	e.waitIdle()
	if p := e.readJSON(base).Pages[0]; p.Status != readReady || p.Error != "" {
		t.Fatalf("after retry: %+v", p)
	}
}

func TestReadAStoryWithoutArt(t *testing.T) {
	e := newEnv(t)
	e.signup("early")
	resp, _ := e.post("/stories", map[string][]string{"script": {script}, "style": {"comic"}}, false)
	base := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	d := e.readJSON(base)
	if len(d.Pages) != 0 {
		t.Fatalf("no pages yet: %+v", d)
	}
	e.post(base+"/characters/draw", nil, true)
	e.waitIdle()
	e.post(base+"/characters/approve", nil, true)
	e.waitIdle()
	for _, p := range e.readJSON(base).Pages {
		if p.Status != readUndrawn || p.Image != "" {
			t.Fatalf("an undrawn page: %+v", p)
		}
	}
	if _, body := e.post(base+"/read/prepare", nil, false); !strings.Contains(body, `"started":false`) {
		t.Fatalf("nothing to prepare without art: %s", body)
	}
}

func TestReadMarksSoundEffects(t *testing.T) {
	e := newEnv(t)
	e.signup("foley")
	sfx := "THE LAMP\n\nMARA: POOF! The lamp is lit.\nPIP: Yawn... so late.\nGRAVES: Who is there in the dark, up the old lighthouse stairs?\nMARA: Only us, and the sea, and a robot who hates heights and loves stories.\n"
	resp, _ := e.post("/stories", map[string][]string{"title": {"Lamp"}, "script": {sfx}, "style": {"comic"}}, false)
	base := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	for _, step := range []string{"/characters/draw", "/characters/approve", "/pages/approve"} {
		e.post(base+step, nil, true)
		e.waitIdle()
	}
	var effect *readLine
	var tagged bool
	for _, p := range e.readJSON(base).Pages {
		if p.Status != readReady {
			t.Fatalf("page %d: %+v", p.Number, p)
		}
		for i, l := range p.Lines {
			if l.Effect != "" && effect == nil {
				effect = &p.Lines[i]
			}
			for _, w := range l.Words {
				tagged = tagged || w.Tag == "yawns"
			}
		}
	}
	if effect == nil || effect.Text != "POOF!" || !strings.HasPrefix(effect.Audio, "/media/") {
		t.Fatalf("POOF! should come through as a sound effect: %+v", effect)
	}
	if !tagged {
		t.Fatal("the yawn's tag should reach the reader")
	}
	_, body := e.get(base + "/read")
	if !strings.Contains(body, "reader.js") {
		t.Fatal("reader page")
	}
}

func TestPrepareFromTheComicStep(t *testing.T) {
	e := newEnv(t)
	e.signup("again")
	base, _ := e.finished(t)
	resp, body := e.post(base+"/read/prepare", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="step-panel"`) || !strings.Contains(body, "Trying again") {
		t.Fatalf("htmx prepare answers with the Comic step: %d", resp.StatusCode)
	}
}
