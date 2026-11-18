package jobs

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lrgalego/pictura/internal/elevenlabs"
	"github.com/lrgalego/pictura/internal/gemini"
	"github.com/lrgalego/pictura/internal/models"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

// toneWAV is a second of tone between short silences, as a WAV.
func toneWAV() []byte {
	const rate = 24000
	samples := make([]int16, rate+rate/2)
	for i := rate / 4; i < rate+rate/4; i++ {
		samples[i] = int16(8000 * math.Sin(float64(i)*2*math.Pi*200/rate))
	}
	b := make([]byte, 44+2*len(samples))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(samples)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(samples)))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	return b
}

// A story voiced by Gemini tags its lines with the model, and when
// ElevenLabs cannot make a sound effect the narrator calls it out.
func TestGeminiVoiceCallsOutEffects(t *testing.T) {
	e, _, voice := sfxEnv(t)
	// ElevenLabs is out of credits: the first reading leaves every line silent.
	voice.fail = true
	voice.err = fmt.Errorf("elevenlabs: quota (quota_exceeded): %w", pipeline.ErrOutOfCredits)
	e.drawnBook()

	var mu sync.Mutex
	var said []string
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"input"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		mu.Lock()
		said = append(said, req.Input[0].Content[0].Text)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"steps": []map[string]any{{"type": "model_output", "content": []map[string]string{
			{"type": "audio", "mime_type": "audio/wav", "data": base64.StdEncoding.EncodeToString(toneWAV())},
		}}}})
	}))
	defer gem.Close()
	el := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":{"status":"quota_exceeded","message":"This request exceeds your quota of 10000. You have 0 credits remaining."}}`)
	}))
	defer el.Close()
	g := gemini.New("k")
	g.BaseURL = gem.URL
	eleven := elevenlabs.New("k")
	eleven.BaseURL = el.URL
	eleven.Backoff = nil
	e.r.Models = &models.Router{Gemini: g, Eleven: eleven}
	if err := e.st.SetStoryModel(e.ctx, e.sto.ID, models.Voices, "gemini-3.8-flash-lite-tts"); err != nil {
		t.Fatal(err)
	}

	_ = e.r.Narrate(e.sto.ID)
	e.mustDone("narrate with Gemini")
	sto, _ := e.st.Story(e.ctx, e.sto.ID)
	chars := e.chars()
	lines, _ := e.st.StoryLines(e.ctx, e.sto.ID)
	var effect *store.PageLine
	for _, p := range e.pages() {
		if !NarrationReady(sto, chars, p, lines[p.ID]) {
			t.Fatalf("page %d not ready", p.Number)
		}
		for _, l := range lines[p.ID] {
			if len(l.Words) == 0 {
				continue
			}
			if l.Sound != "" {
				effect = l
				continue
			}
			if !strings.HasPrefix(l.Voice, "gemini-3.8-flash-lite-tts/") || !strings.HasSuffix(l.Audio, ".wav") {
				t.Errorf("line voiced as %q into %q", l.Voice, l.Audio)
			}
		}
	}
	if effect == nil || effect.Audio == "" || effect.Voice != effectVoice || effect.Words[0].End <= 0 {
		t.Fatalf("the effect is called out: %+v", effect)
	}
	mu.Lock()
	defer mu.Unlock()
	called := false
	for _, s := range said {
		called = called || s == "POOF!"
	}
	if !called {
		t.Errorf("the narrator never called POOF! out: %q", said)
	}

	// Back to ElevenLabs: Gemini's lines are stale and are voiced again.
	if err := e.st.SetStoryModel(e.ctx, e.sto.ID, models.Voices, ""); err != nil {
		t.Fatal(err)
	}
	sto, _ = e.st.Story(e.ctx, e.sto.ID)
	for _, p := range e.pages() {
		if len(lines[p.ID]) > 1 && NarrationReady(sto, chars, p, lines[p.ID]) {
			t.Errorf("page %d still reads as ready in another voice model", p.Number)
		}
	}
}

func TestRoutesRolesToThePick(t *testing.T) {
	e := newEnv(t)
	if e.r.aiFor(e.sto, models.Pages) != e.ai || e.r.speaker(e.sto) != e.r.Voice {
		t.Fatal("without a router everything is the runner's own")
	}
	e.r.Models = &models.Router{}
	if e.r.aiFor(e.sto, models.Pages) != e.ai || e.r.speaker(e.sto) != e.r.Voice || e.r.callsOutEffects(e.sto) {
		t.Fatal("a router with nothing configured falls back to the runner's own")
	}
	g := gemini.New("k")
	e.r.Models = &models.Router{Gemini: g}
	e.sto.Models = map[string]string{models.Pages: "gemini-nano-banana-2.1"}
	if c, ok := e.r.aiFor(e.sto, models.Pages).(*gemini.Client); !ok || c.ImageModel != "gemini-nano-banana-2.1" {
		t.Fatalf("pages: %#v", e.r.aiFor(e.sto, models.Pages))
	}
	if !e.r.callsOutEffects(e.sto) {
		t.Error("a Gemini voice calls effects out")
	}
	var _ pipeline.Speaker = e.r.speaker(e.sto)
}
