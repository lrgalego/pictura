package elevenlabs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// server answers every request with a recorded body and remembers what it
// was asked. testdata holds responses recorded against the real API, with
// the audio replaced by a few bytes.
func server(t *testing.T, status int, fixture string) (*httptest.Server, *http.Request, *map[string]any) {
	t.Helper()
	body, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	var last http.Request
	var sent map[string]any
	var mu sync.Mutex
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = *r
		_ = json.Unmarshal(b, &sent)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s, &last, &sent
}

func client(s *httptest.Server) *Client {
	c := New("sk_test")
	c.BaseURL = s.URL
	return c
}

func TestNewDefaults(t *testing.T) {
	c := New("k")
	if c.BaseURL != DefaultBaseURL || c.Model != DefaultModel || c.Format != DefaultFormat || c.HTTP == nil || cap(c.sem) != 2 {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestSpeakWithTimestamps(t *testing.T) {
	s, req, sent := server(t, http.StatusOK, "with_timestamps.json")
	sp, err := client(s).Speak(context.Background(), "Sorry... me go...", "cgSgspJ2msm6clMCkdW9")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/text-to-speech/cgSgspJ2msm6clMCkdW9/with-timestamps" || req.URL.Query().Get("output_format") != DefaultFormat {
		t.Fatalf("url: %s", req.URL)
	}
	if req.Header.Get("xi-api-key") != "sk_test" || req.Method != http.MethodPost {
		t.Fatalf("headers: %v", req.Header)
	}
	if (*sent)["text"] != "Sorry... me go..." || (*sent)["model_id"] != DefaultModel {
		t.Fatalf("body: %v", *sent)
	}
	if sp.Ext != "mp3" || !strings.HasPrefix(string(sp.Audio), "ID3") {
		t.Fatalf("audio: %q %s", sp.Audio, sp.Ext)
	}
	want := []string{"Sorry...", "me", "go..."}
	if len(sp.Words) != len(want) {
		t.Fatalf("words: %+v", sp.Words)
	}
	for i, w := range sp.Words {
		if w.Text != want[i] || w.End <= w.Start {
			t.Fatalf("word %d: %+v", i, w)
		}
		if i > 0 && w.Start < sp.Words[i-1].End {
			t.Fatalf("words overlap: %+v", sp.Words)
		}
	}
	if sp.Words[0].Start != 0 || sp.Words[2].End != 3.04 {
		t.Fatalf("recorded timings changed: %+v", sp.Words)
	}
}

func TestSpeakErrors(t *testing.T) {
	s, _, _ := server(t, http.StatusNotFound, "error_voice_not_found.json")
	_, err := client(s).Speak(context.Background(), "hi", "nope")
	if err == nil || !strings.Contains(err.Error(), "was not found") || !strings.Contains(err.Error(), "voice_not_found") {
		t.Fatalf("err: %v", err)
	}
	for _, tc := range []struct {
		body, want string
	}{
		{`{"detail":"Too many concurrent requests"}`, "Too many concurrent requests"},
		{`<html>bad gateway</html>`, "HTTP 502: <html>bad gateway</html>"},
		{`{"detail":{"status":"quota_exceeded","message":"You have 3 credits left"}}`, "You have 3 credits left (quota_exceeded)"},
	} {
		if got := errorMessage(502, []byte(tc.body)); got != tc.want {
			t.Errorf("errorMessage(%s) = %q, want %q", tc.body, got, tc.want)
		}
	}
	if got := errorMessage(500, []byte(strings.Repeat("x", 400))); len(got) > 330 {
		t.Errorf("long body not truncated: %d", len(got))
	}
}

func TestSpeakBadResponses(t *testing.T) {
	for name, body := range map[string]string{
		"not json": `nope`,
		"no audio": `{"audio_base64":"","alignment":null}`,
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		if _, err := client(s).Speak(context.Background(), "hi", "v"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		s.Close()
	}
	// No alignment is not an error: the audio still plays, unhighlighted.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"audio_base64":"SUQz"}`)
	}))
	defer s.Close()
	sp, err := client(s).Speak(context.Background(), "hi", "v")
	if err != nil || len(sp.Words) != 0 || string(sp.Audio) != "ID3" {
		t.Fatalf("no alignment: %+v %v", sp, err)
	}
	// Transport failure.
	c := client(s)
	c.BaseURL = "http://127.0.0.1:1"
	if _, err := c.Speak(context.Background(), "hi", "v"); err == nil || !strings.HasPrefix(err.Error(), "elevenlabs:") {
		t.Fatalf("transport: %v", err)
	}
}

func TestSpeakLimitsConcurrency(t *testing.T) {
	var inflight, peak int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		_, _ = io.WriteString(w, `{"audio_base64":"SUQz"}`)
	}))
	defer s.Close()
	c := client(s)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Speak(context.Background(), "hi", "v"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak concurrency %d, want at most 2", peak)
	}
	// A caller that gives up while waiting for a slot gets its context error.
	c.sem <- struct{}{}
	c.sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Speak(ctx, "hi", "v"); err != context.Canceled {
		t.Fatalf("cancelled wait: %v", err)
	}
}

func TestSound(t *testing.T) {
	var got *http.Request
	var sent map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "ID3sound")
	}))
	defer s.Close()
	sp, err := client(s).Sound(context.Background(), "two heavy stomps", 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/sound-generation" || got.URL.Query().Get("output_format") != DefaultFormat || got.Header.Get("xi-api-key") != "sk_test" {
		t.Fatalf("request: %s %v", got.URL, got.Header)
	}
	if sent["text"] != "two heavy stomps" || sent["duration_seconds"] != 1.5 || sent["prompt_influence"] != 0.6 {
		t.Fatalf("body: %v", sent)
	}
	if sp.Ext != "mp3" || string(sp.Audio) != "ID3sound" || len(sp.Words) != 0 {
		t.Fatalf("speech: %+v", sp)
	}
	// Errors say why; an empty answer is an error too.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":{"status":"missing_permissions","message":"The API key you used is missing the permission sound_generation"}}`)
	}))
	defer bad.Close()
	if _, err := client(bad).Sound(context.Background(), "x", 1); err == nil || !strings.Contains(err.Error(), "sound_generation") {
		t.Fatalf("permission error: %v", err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer empty.Close()
	if _, err := client(empty).Sound(context.Background(), "x", 1); err == nil {
		t.Fatal("empty sound accepted")
	}
	c := client(s)
	c.BaseURL = "http://127.0.0.1:1"
	if _, err := c.Sound(context.Background(), "x", 1); err == nil {
		t.Fatal("transport error ignored")
	}
	c.sem <- struct{}{}
	c.sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Sound(ctx, "x", 1); err != context.Canceled {
		t.Fatalf("cancelled: %v", err)
	}
}
