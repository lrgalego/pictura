package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lrgalego/pictura/internal/pipeline"
)

func TestVoicePickingAndPreviews(t *testing.T) {
	e := newEnv(t)
	e.signup("listener")
	base, _ := e.finished(t)
	ctx := context.Background()
	_, body := e.get(base + "/characters")
	cid := firstCharacterID(body)
	page := base + "/characters/" + cid

	// The cast step shows the narrator; tiles name each character's voice.
	st, _ := e.st.Story(ctx, 1)
	narrator := pipeline.NarratorVoice(st)
	if !strings.Contains(body, "Narrator: "+narrator.Name) || !strings.Contains(body, "voice: ") || !strings.Contains(body, "/static/app/voice.js?v=") {
		t.Fatal("cast step should show the narrator, the voices and load the player script")
	}

	// The character page shows the voice with a sample and a way to change it.
	_, body = e.get(page)
	chars, _ := e.st.Characters(ctx, st.ID)
	c := chars[0]
	v := pipeline.CharacterVoice(c)
	if !strings.Contains(body, "Voice: "+v.Name) || !strings.Contains(body, `data-listen="`+base+"/voices/"+v.ID+"/preview?cid="+cid+`"`) {
		t.Fatalf("character page voice line missing")
	}

	// The picker lists the catalog, the current voice marked.
	resp, body := e.get(page + "/voice")
	if resp.StatusCode != http.StatusOK || strings.Count(body, "voice-row") < len(pipeline.Voices) || !strings.Contains(body, "A voice for "+c.Name) || !strings.Contains(body, "voice-row--current") {
		t.Fatalf("voice panel: %d", resp.StatusCode)
	}

	// Picking one saves it, re-renders the page and closes the panel.
	e.target = "char-panel"
	pick := pipeline.Voices[5]
	resp, body = e.post(page+"/voice", url.Values{"voice": {pick.ID}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, c.Name+" now speaks as "+pick.Name) || !strings.Contains(body, "Voice: "+pick.Name) || !strings.Contains(body, `id="panel-root" hx-swap-oob`) {
		t.Fatalf("pick: %d", resp.StatusCode)
	}
	if got, _ := e.st.Character(ctx, c.ID); got.Voice != pick.ID {
		t.Fatalf("voice not saved: %q", got.Voice)
	}
	resp, _ = e.post(page+"/voice", url.Values{"voice": {"nope"}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown voice: %d", resp.StatusCode)
	}
	e.target = ""

	// The narrator is picked the same way, from the cast step.
	resp, body = e.get(base + "/narrator")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "A voice for the narrator") {
		t.Fatalf("narrator panel: %d", resp.StatusCode)
	}
	bill := pipeline.Voices[len(pipeline.Voices)-1]
	resp, body = e.post(base+"/narrator", url.Values{"voice": {bill.ID}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "The narrator is now "+bill.Name) || !strings.Contains(body, "Narrator: "+bill.Name) {
		t.Fatalf("narrator pick: %d", resp.StatusCode)
	}
	if got, _ := e.st.Story(ctx, st.ID); got.NarratorVoice != bill.ID {
		t.Fatalf("narrator not saved: %q", got.NarratorVoice)
	}
	resp, _ = e.post(base+"/narrator", url.Values{"voice": {""}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty narrator voice: %d", resp.StatusCode)
	}

	// Previews play (offline tones here) for a character and the narrator.
	for _, u := range []string{base + "/voices/" + pick.ID + "/preview?cid=" + cid, base + "/voices/" + bill.ID + "/preview"} {
		resp, body = e.get(u)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/wav" || !strings.HasPrefix(body, "RIFF") || !strings.Contains(resp.Header.Get("Cache-Control"), "private") {
			t.Fatalf("preview %s: %d %s", u, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	for _, u := range []string{base + "/voices/nope/preview", base + "/voices/" + pick.ID + "/preview?cid=99999"} {
		if resp, _ := e.get(u); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", u, resp.StatusCode)
		}
	}
	// Someone else's story: nothing.
	other := newEnvClient(t, e)
	for _, u := range []string{page + "/voice", base + "/narrator", base + "/voices/" + pick.ID + "/preview"} {
		if resp, _ := other.get(u); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s must be owner-only: %d", u, resp.StatusCode)
		}
	}
}

// countingVoice counts syntheses and can fail.
type countingVoice struct {
	calls atomic.Int32
	fail  bool
}

func (c *countingVoice) Speak(ctx context.Context, text, voice string) (*pipeline.Speech, error) {
	c.calls.Add(1)
	if c.fail {
		return nil, errors.New("quota exceeded")
	}
	return &pipeline.Speech{Audio: []byte("ID3" + text), Ext: "mp3"}, nil
}

func (c *countingVoice) Sound(ctx context.Context, prompt string, seconds float64) (*pipeline.Speech, error) {
	return &pipeline.Speech{Audio: []byte("ID3sfx"), Ext: "mp3"}, nil
}

func TestVoicePreviewCacheAndErrors(t *testing.T) {
	e := newEnv(t)
	e.signup("cacher")
	base, _ := e.finished(t)
	sp := &countingVoice{}
	e2 := withVoice(t, e, sp)
	u := base + "/voices/" + pipeline.DefaultNarrator + "/preview"
	for i := 0; i < 3; i++ {
		resp, body := e2.get(u)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/mpeg" || !strings.HasPrefix(body, "ID3") {
			t.Fatalf("preview: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if sp.calls.Load() != 1 {
		t.Fatalf("the sample should be synthesized once, got %d", sp.calls.Load())
	}
	sp.fail = true
	resp, body := e2.get(base + "/voices/" + pipeline.Voices[3].ID + "/preview")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "quota exceeded") {
		t.Fatalf("failed synthesis: %d %s", resp.StatusCode, body)
	}
}

// withVoice serves the same store and runner through a router with another
// speaker; the cookie jar is shared (cookies ignore the port), so the
// session carries over.
func withVoice(t *testing.T, e *env, sp pipeline.Speaker) *env {
	srv := httptest.NewServer(Router(Deps{Store: e.st, Jobs: e.runner, Fake: true, Voice: sp}))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, client: e.client, runner: e.runner, st: e.st, ai: e.ai}
}

func TestSampleCacheEvicts(t *testing.T) {
	var c sampleCache
	sp := &countingVoice{}
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	for i := 0; i < sampleCacheSize+5; i++ {
		if _, err := c.get(r, sp, "v", strings.Repeat("x", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.items) != sampleCacheSize || len(c.order) != sampleCacheSize {
		t.Fatalf("cache size %d/%d", len(c.items), len(c.order))
	}
	if _, ok := c.items["v\x00x"]; ok {
		t.Fatal("the oldest sample should be evicted")
	}
}

func TestCastVoicesForAnOlderStory(t *testing.T) {
	e := newEnv(t)
	e.signup("oldtimer")
	base, _ := e.finished(t)
	ctx := context.Background()
	// Simulate a story made before voices: nobody cast.
	chars, _ := e.st.Characters(ctx, 1)
	for _, c := range chars {
		_ = e.st.SetCharacterVoice(ctx, c.ID, "")
	}
	_ = e.st.SetNarratorVoice(ctx, 1, "")
	_, body := e.get(base + "/characters")
	if !strings.Contains(body, base+"/voices/cast") {
		t.Fatal("an uncast story should offer to cast voices")
	}
	resp, body := e.post(base+"/voices/cast", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Casting voices") {
		t.Fatalf("cast: %d", resp.StatusCode)
	}
	e.waitIdle()
	chars, _ = e.st.Characters(ctx, 1)
	if pipeline.Uncast(chars) {
		t.Fatalf("still uncast: %+v", chars)
	}
	if st, _ := e.st.Story(ctx, 1); st.NarratorVoice == "" {
		t.Fatal("narrator not cast")
	}
	_, body = e.get(base + "/characters")
	if strings.Contains(body, base+"/voices/cast") {
		t.Fatal("the offer should go once everyone has a voice")
	}
}
