package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/usage"
)

// fakeAPI answers generateContent per model with canned bodies and keeps
// what it was sent.
type fakeAPI struct {
	mu      sync.Mutex
	answers map[string][]reply // per model, in order; the last repeats
	got     []sent
}

type reply struct {
	status int
	body   any
}

type sent struct {
	model string
	key   string
	req   request
	tts   interactionRequest
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/models" {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "models/gemini-4-hydrogen"}, {"name": "models/gemini-3-pro-image"}}})
		return
	}
	model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/models/"), ":generateContent")
	var req request
	var tts interactionRequest
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &req)
	if r.URL.Path == "/interactions" {
		_ = json.Unmarshal(b, &tts)
		model = tts.Model
	}
	f.mu.Lock()
	f.got = append(f.got, sent{model, r.Header.Get("x-goog-api-key"), req, tts})
	list := f.answers[model]
	a := list[0]
	if len(list) > 1 {
		f.answers[model] = list[1:]
	}
	f.mu.Unlock()
	w.WriteHeader(a.status)
	_ = json.NewEncoder(w).Encode(a.body)
}

func newFake(t *testing.T, answers map[string][]reply) (*Client, *fakeAPI, *[]usage.Call) {
	t.Helper()
	f := &fakeAPI{answers: answers}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("key")
	c.BaseURL = srv.URL
	c.Backoff = []time.Duration{time.Millisecond}
	var calls []usage.Call
	var mu sync.Mutex
	c.Record = func(_ context.Context, call usage.Call) {
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
	}
	return c, f, &calls
}

func answer(parts ...map[string]any) map[string]any {
	return map[string]any{
		"candidates":    []map[string]any{{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP"}},
		"usageMetadata": map[string]int{"promptTokenCount": 100, "candidatesTokenCount": 40, "thoughtsTokenCount": 60, "cachedContentTokenCount": 10},
	}
}

func inline(mime string, data []byte) map[string]any {
	return map[string]any{"inlineData": map[string]string{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(data)}}
}

func TestChatJSON(t *testing.T) {
	c, f, calls := newFake(t, map[string][]reply{
		"gemini-4-hydrogen": {{200, answer(map[string]any{"text": "thinking about pies", "thought": true}, map[string]any{"text": "```json\n{\"title\": \"The Fox\"}\n```"})}},
	})
	var out struct{ Title string }
	err := c.ChatJSON(context.Background(), "You plan comics.", "Plan it.", []pipeline.Image{{Label: "Sheet of Ash", PNG: []byte("png")}}, "pages", map[string]any{"type": "object"}, &out)
	if err != nil || out.Title != "The Fox" {
		t.Fatalf("got %+v, %v", out, err)
	}
	g := f.got[0]
	if g.key != "key" || g.req.SystemInstruction.Parts[0].Text != "You plan comics." || g.req.GenerationConfig.ResponseMimeType != "application/json" || g.req.GenerationConfig.ResponseJSONSchema["type"] != "object" {
		t.Errorf("request: %+v", g)
	}
	parts := g.req.Contents[0].Parts
	if len(parts) != 3 || parts[0].Text != "Plan it." || parts[1].Text != "Sheet of Ash" || parts[2].InlineData == nil || parts[2].InlineData.MimeType != "image/png" {
		t.Errorf("parts: %+v", parts)
	}
	got := (*calls)[0]
	if got.Provider != usage.Gemini || got.Kind != usage.Chat || got.Op != "pages" || got.Model != "gemini-4-hydrogen" || !got.OK ||
		got.InputTokens != 100 || got.OutputTokens != 100 || got.ReasoningTokens != 60 || got.CachedTokens != 10 {
		t.Errorf("recorded %+v", got)
	}
}

func TestChatJSONFailures(t *testing.T) {
	c, _, _ := newFake(t, map[string][]reply{
		"bad":     {{200, answer(map[string]any{"text": "not json"})}},
		"empty":   {{200, answer()}},
		"blocked": {{200, map[string]any{"promptFeedback": map[string]string{"blockReason": "SAFETY"}}}},
		"broke":   {{400, map[string]any{"error": map[string]any{"code": 400, "message": "bad schema", "status": "INVALID_ARGUMENT"}}}},
	})
	for model, want := range map[string]string{"bad": "not the expected JSON", "empty": "no answer", "blocked": "blocked (SAFETY)", "broke": "bad schema (INVALID_ARGUMENT)"} {
		var out any
		err := c.With(model, "", "").ChatJSON(context.Background(), "s", "u", nil, "x", nil, &out)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", model, err, want)
		}
	}
}

func TestRetriesTransientFailures(t *testing.T) {
	c, f, calls := newFake(t, map[string][]reply{
		"gemini-4-hydrogen": {
			{429, map[string]any{"error": map[string]any{"code": 429, "message": "slow down", "status": "RESOURCE_EXHAUSTED"}}},
			{200, answer(map[string]any{"text": `{"ok": true}`})},
		},
	})
	var out struct{ OK bool }
	if err := c.ChatJSON(context.Background(), "s", "u", nil, "x", nil, &out); err != nil || !out.OK {
		t.Fatalf("%+v %v", out, err)
	}
	if len(f.got) != 2 || len(*calls) != 2 || (*calls)[0].OK || !(*calls)[1].OK {
		t.Errorf("attempts %d, calls %+v", len(f.got), *calls)
	}
}

func TestOutOfCredits(t *testing.T) {
	c, f, _ := newFake(t, map[string][]reply{
		DefaultSpeechModel: {{402, map[string]any{"error": map[string]any{"code": 402, "message": "Your prepayment credits are depleted.", "status": "RESOURCE_EXHAUSTED"}}}},
	})
	_, err := c.Speak(context.Background(), "Hello.", "")
	if !errors.Is(err, pipeline.ErrOutOfCredits) || !pipeline.OutOfCredits(err.Error()) || !strings.Contains(err.Error(), "gemini") {
		t.Fatalf("err = %v", err)
	}
	if len(f.got) != 1 {
		t.Errorf("a refusal for credits is not retried: %d calls", len(f.got))
	}
	capped, f2, _ := newFake(t, map[string][]reply{
		"gemini-3-pro-image": {{429, map[string]any{"error": map[string]any{"code": 429, "message": "Your project is temporarily rate-limited to prevent exceeding its monthly spending cap.", "status": "RESOURCE_EXHAUSTED"}}}},
	})
	if _, err := capped.GenerateImage(context.Background(), "x", pipeline.PageSize); !errors.Is(err, pipeline.ErrOutOfCredits) || len(f2.got) != 1 {
		t.Errorf("spending cap: %v after %d calls", err, len(f2.got))
	}
}

func jpegOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func TestEditImageScalesToTheSizeAsked(t *testing.T) {
	c, f, calls := newFake(t, map[string][]reply{
		"gemini-3-pro-image": {{200, answer(map[string]any{"text": "a draft", "thought": true}, inline("image/jpeg", jpegOf(200, 300)))}},
	})
	out, err := c.EditImage(context.Background(), "Draw page 2.", []pipeline.Image{{Label: "Reference image 1: the character sheet of Ash.", PNG: []byte("ash")}, {PNG: []byte("emmy")}}, pipeline.PageSize)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 1024 || img.Bounds().Dy() != 1536 {
		t.Fatalf("got %v, %v", img.Bounds(), err)
	}
	g := f.got[0].req
	if ic := g.GenerationConfig.ImageConfig; ic == nil || ic.AspectRatio != "2:3" || ic.ImageSize != "2K" || g.GenerationConfig.ResponseModalities[0] != "IMAGE" {
		t.Errorf("config %+v", g.GenerationConfig)
	}
	parts := g.Contents[0].Parts
	if len(parts) != 5 || parts[0].Text != "Reference image 1: the character sheet of Ash." || parts[2].Text != "Reference image 2:" || parts[4].Text != "Draw page 2." {
		t.Errorf("parts %+v", parts)
	}
	if (*calls)[0].Kind != usage.Image || (*calls)[0].Op != "page" {
		t.Errorf("recorded %+v", (*calls)[0])
	}
}

func TestGenerateImageWithoutAnImage(t *testing.T) {
	c, _, _ := newFake(t, map[string][]reply{
		"gemini-3-pro-image": {{200, answer(map[string]any{"text": "I can't draw that."})}},
		"broken":             {{200, answer(inline("image/png", []byte("not a png")))}},
	})
	_, err := c.GenerateImage(context.Background(), "x", pipeline.SheetSize)
	if err == nil || !strings.Contains(err.Error(), "I can't draw that.") {
		t.Errorf("err = %v", err)
	}
	if _, err := c.With("", "broken", "").GenerateImage(context.Background(), "x", "512x512"); err == nil {
		t.Error("an unreadable image is an error")
	}
}

func TestAspectAndDimensions(t *testing.T) {
	cases := map[string]string{pipeline.PageSize: "2:3", pipeline.SheetSize: "3:2", "1024x1024": "1:1", "1920x1080": "16:9", "nonsense": "1:1"}
	for size, want := range cases {
		if got := aspect(dimensions(size)); got != want {
			t.Errorf("%s: %s, want %s", size, got, want)
		}
	}
	if imageOp("1x1") != "image" || imageOp(pipeline.SheetSize) != "sheet" {
		t.Error("imageOp")
	}
}

// tone is speech-like audio: bursts of tone between silences, one per word.
func tone(rate int, words []float64, gap float64) []int16 {
	var out []int16
	silence := func(s float64) {
		out = append(out, make([]int16, int(s*float64(rate)))...)
	}
	silence(0.2)
	for _, d := range words {
		for i := range int(d * float64(rate)) {
			out = append(out, int16(8000*math.Sin(float64(i)*2*math.Pi*220/float64(rate))))
		}
		silence(gap)
	}
	silence(0.3)
	return out
}

// spoken is an Interactions answer carrying audio.
func spoken(mime string, data []byte) map[string]any {
	return map[string]any{
		"steps": []map[string]any{{"type": "model_output", "content": []map[string]string{{"type": "audio", "mime_type": mime, "data": base64.StdEncoding.EncodeToString(data)}}}},
		"usage": map[string]int{"total_input_tokens": 12, "total_output_tokens": 128},
	}
}

func TestSpeak(t *testing.T) {
	samples := tone(sampleRate, []float64{0.4, 0.4}, 0.15)
	c, f, calls := newFake(t, map[string][]reply{
		DefaultSpeechModel: {{200, spoken("audio/wav", wav(samples, sampleRate))}},
	})
	sp, err := c.Speak(context.Background(), "[whispers] Hello there.", "onwK4e9ZLuTAKqWW03F9") // Daniel: British
	if err != nil {
		t.Fatal(err)
	}
	if sp.Ext != "wav" || !bytes.HasPrefix(sp.Audio, []byte("RIFF")) || len(sp.Words) != 3 || sp.Words[0].End != sp.Words[0].Start {
		t.Fatalf("speech %+v", sp.Words)
	}
	if sp.Words[1].Start < 0.15 || sp.Words[2].End > 1.5 {
		t.Errorf("timings %+v", sp.Words)
	}
	g := f.got[0].tts
	if g.GenerationConfig.SpeechConfig[0]["voice"] != "Rasalgethi" || g.ResponseFormat["type"] != "audio" || g.Input[0].Type != "user_input" {
		t.Errorf("request %+v", g)
	}
	text := g.Input[0].Content[0]
	if text.Text != "Hello there." || text.Annotations[0]["type"] != "speech_metadata" || text.Annotations[0]["style"] != "Say it whispers, with a British accent" {
		t.Errorf("text %+v", text)
	}
	if got := (*calls)[0]; got.Kind != usage.Speech || got.Chars != len("[whispers] Hello there.") || got.Model != DefaultSpeechModel || got.InputTokens != 12 || got.OutputTokens != 128 {
		t.Errorf("recorded %+v", got)
	}
}

func TestSpeechText(t *testing.T) {
	cases := []struct{ in, accent, said, style string }{
		{"Hello.", "", "Hello.", ""},
		{"Hello.", "Australian", "Hello.", "with a Australian accent"},
		{"[excited] We won! [laughs] We won!", "", "We won! <laugh> We won!", "Say it excited"},
		{"[laughs] YOU'RE SCARED?", "", "<laugh> YOU'RE SCARED?", ""},
		{"[snores]...", "", "<snore>...", ""},
		{"[softly sad] Oh [sighs] fine. [growls]", "British", "Oh <sigh> fine. <growl>", "Say it softly sad, with a British accent"},
	}
	for _, c := range cases {
		if said, style := SpeechText(c.in, c.accent); said != c.said || style != c.style {
			t.Errorf("%q: %q / %q, want %q / %q", c.in, said, style, c.said, c.style)
		}
	}
}

func TestSpeakRawPCMAndSilence(t *testing.T) {
	samples := tone(16000, []float64{0.3}, 0.1)
	raw := wav(samples, 16000)[44:]
	c, f, _ := newFake(t, map[string][]reply{
		"pcm":    {{200, map[string]any{"outputs": []map[string]string{{"type": "audio", "mime_type": "audio/L16;codec=pcm;rate=16000", "data": base64.StdEncoding.EncodeToString(raw)}}}}},
		"silent": {{200, map[string]any{"steps": []map[string]any{{"type": "model_output", "content": []map[string]string{{"type": "text", "data": ""}}}}}}},
	})
	sp, err := c.With("", "", "pcm").Speak(context.Background(), "Boo!", "Kore")
	if err != nil || len(sp.Words) != 1 {
		t.Fatalf("%v %v", sp, err)
	}
	if _, rate := pcm(sp.Audio, "audio/wav"); rate != 16000 {
		t.Errorf("rate %d", rate)
	}
	if a := f.got[0].tts.Input[0].Content[0].Annotations; a != nil {
		t.Errorf("no style, no annotation: %v", a)
	}
	if _, err := c.With("", "", "silent").Speak(context.Background(), "Boo!", "Kore"); !errors.Is(err, pipeline.ErrNoAudio) {
		t.Errorf("err = %v", err)
	}
}

func TestCheckAndUnoffered(t *testing.T) {
	c, _, _ := newFake(t, nil)
	ids, err := c.Check(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != "gemini-4-hydrogen" {
		t.Fatalf("%v %v", ids, err)
	}
	if _, err := c.Sound(context.Background(), "boom", 1); err == nil {
		t.Error("Gemini makes no sound effects")
	}
	if _, err := c.Segment(context.Background(), nil, "bubble"); err == nil {
		t.Error("Gemini does no segmentation")
	}
	bad := New("k")
	bad.BaseURL = "http://127.0.0.1:1"
	if _, err := bad.Check(context.Background()); err == nil {
		t.Error("an unreachable API is an error")
	}
}

func TestWith(t *testing.T) {
	c := New("k")
	d := c.With("t", "", "s")
	if d.TextModel != "t" || d.ImageModel != DefaultImageModel || d.SpeechModel != "s" || c.TextModel != DefaultTextModel {
		t.Errorf("%+v", d)
	}
}
