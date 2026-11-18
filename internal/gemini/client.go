// Package gemini is a small client for the Gemini API
// (https://generativelanguage.googleapis.com/v1beta): generateContent for
// structured chat answers, Nano Banana images (drawn or conditioned on
// reference images) and speech. One endpoint serves all three; the model
// decides what comes back.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/usage"
)

const (
	DefaultBaseURL     = "https://generativelanguage.googleapis.com/v1beta"
	DefaultTextModel   = "gemini-4-hydrogen"
	DefaultImageModel  = "gemini-3-pro-image"
	// DefaultSpeechModel won a bake-off on real lines from Pictura stories:
	// judged blind within a hair of the best Gemini voice and above the
	// ElevenLabs clips, at a ninth of a cent per minute of audio.
	DefaultSpeechModel = "gemini-3.8-flash-lite-tts"
)

// Client talks to the Gemini API. It holds no state of its own, so With
// can hand out copies bound to other models.
type Client struct {
	APIKey      string
	BaseURL     string
	TextModel   string
	ImageModel  string
	SpeechModel string
	HTTP        *http.Client
	// Backoff is the wait before each retry; its length is how many
	// retries a transient failure (rate limit, overload) gets.
	Backoff []time.Duration
	// Record, when set, is told about every request and its token usage.
	Record usage.Recorder
}

// New returns a client with the default models and a generous timeout —
// a 2K page from Nano Banana Pro takes half a minute, more when busy.
func New(apiKey string) *Client {
	return &Client{
		APIKey:      apiKey,
		BaseURL:     DefaultBaseURL,
		TextModel:   DefaultTextModel,
		ImageModel:  DefaultImageModel,
		SpeechModel: DefaultSpeechModel,
		HTTP:        &http.Client{Timeout: 6 * time.Minute},
		Backoff:     []time.Duration{2 * time.Second, 6 * time.Second},
	}
}

// With returns a copy of the client using the given models; an empty one
// keeps the client's.
func (c *Client) With(text, image, speech string) *Client {
	cp := *c
	if text != "" {
		cp.TextModel = text
	}
	if image != "" {
		cp.ImageModel = image
	}
	if speech != "" {
		cp.SpeechModel = speech
	}
	return &cp
}

// ---------- wire format ----------

type blob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type part struct {
	Text       string `json:"text,omitempty"`
	InlineData *blob  `json:"inlineData,omitempty"`
	Thought    bool   `json:"thought,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type imageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
	ImageSize   string `json:"imageSize,omitempty"`
}

type speechConfig struct {
	VoiceConfig struct {
		PrebuiltVoiceConfig struct {
			VoiceName string `json:"voiceName"`
		} `json:"prebuiltVoiceConfig"`
	} `json:"voiceConfig"`
}

type generationConfig struct {
	ResponseMimeType   string         `json:"responseMimeType,omitempty"`
	ResponseJSONSchema map[string]any `json:"responseJsonSchema,omitempty"`
	ResponseModalities []string       `json:"responseModalities,omitempty"`
	ImageConfig        *imageConfig   `json:"imageConfig,omitempty"`
	SpeechConfig       *speechConfig  `json:"speechConfig,omitempty"`
}

type request struct {
	SystemInstruction *content         `json:"systemInstruction,omitempty"`
	Contents          []content        `json:"contents"`
	GenerationConfig  generationConfig `json:"generationConfig"`
}

type response struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

// answer is the parts of the first candidate that are not thoughts.
func (r *response) answer() ([]part, error) {
	if len(r.Candidates) == 0 {
		if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
			return nil, fmt.Errorf("gemini: the prompt was blocked (%s)", r.PromptFeedback.BlockReason)
		}
		return nil, fmt.Errorf("gemini: empty answer")
	}
	var out []part
	for _, p := range r.Candidates[0].Content.Parts {
		if !p.Thought {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *response) finish() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	return r.Candidates[0].FinishReason
}

type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// transient marks a failure worth another attempt: the network, a rate
// limit or an overloaded model.
type transient struct{ error }

func (t transient) Unwrap() error { return t.error }

// failure turns an error answer into an error. A project out of prepaid
// credits answers 402, one at its monthly spending cap 429 with a message
// saying so; both are pipeline.ErrOutOfCredits, so the UI can say so
// rather than show the raw message, and neither is retried.
func failure(model string, status int, body []byte) error {
	msg := fmt.Sprintf("HTTP %d: %s", status, truncate(string(body), 300))
	var ae apiError
	if json.Unmarshal(body, &ae) == nil && ae.Error.Message != "" {
		msg = fmt.Sprintf("%s (%s)", strings.TrimSpace(ae.Error.Message), ae.Error.Status)
	}
	err := fmt.Errorf("gemini %s: %s", model, msg)
	switch {
	case status == http.StatusPaymentRequired, strings.Contains(msg, "spending cap"):
		return fmt.Errorf("gemini %s: %w: %s", model, pipeline.ErrOutOfCredits, msg)
	case status == http.StatusTooManyRequests || status >= 500:
		return transient{err}
	}
	return err
}

// billed is a decoded reply that knows what it was billed.
type billed interface {
	// tokens is input, output (thinking included), thinking and cached.
	tokens() (in, out, thoughts, cached int)
}

func (r *response) tokens() (int, int, int, int) {
	if r.UsageMetadata == nil {
		return 0, 0, 0, 0
	}
	u := r.UsageMetadata
	return u.PromptTokenCount, u.CandidatesTokenCount + u.ThoughtsTokenCount, u.ThoughtsTokenCount, u.CachedContentTokenCount
}

// generate runs one generateContent call.
func (c *Client) generate(ctx context.Context, model string, req request, call usage.Call) (*response, error) {
	var out response
	if err := c.post(ctx, model, "/models/"+model+":generateContent", req, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// post sends one request and decodes the answer into out, retrying
// transient failures; every attempt is reported (call holds its kind and
// op) to Record.
func (c *Client) post(ctx context.Context, model, path string, body any, call usage.Call, out billed) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err = c.postOnce(ctx, model, path, payload, call, out)
		var t transient
		if err == nil || !errors.As(err, &t) || attempt >= len(c.Backoff) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.Backoff[attempt]):
		}
	}
}

func (c *Client) postOnce(ctx context.Context, model, path string, payload []byte, call usage.Call, out billed) (err error) {
	start := time.Now()
	call.Model = model
	decoded := false
	defer func() {
		var a billed
		if decoded {
			a = out
		}
		c.record(ctx, call, start, a, err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return transient{fmt.Errorf("gemini %s: %w", model, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 96<<20))
	if err != nil {
		return transient{fmt.Errorf("gemini %s: %w", model, err)}
	}
	if resp.StatusCode >= 300 {
		return failure(model, resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("gemini %s: unreadable answer: %w", model, err)
	}
	decoded = true
	return nil
}

// record reports one request with the tokens its answer counted.
func (c *Client) record(ctx context.Context, call usage.Call, start time.Time, a billed, err error) {
	if c.Record == nil {
		return
	}
	call.Provider, call.Duration, call.OK = usage.Gemini, time.Since(start), err == nil
	if err != nil {
		call.Error = err.Error()
	}
	if a != nil {
		call.InputTokens, call.OutputTokens, call.ReasoningTokens, call.CachedTokens = a.tokens()
	}
	c.Record(ctx, usage.Stamp(ctx, call))
}

// Check lists the models the key can use, to tell a working key from a
// broken one or an outage, and returns their ids. Listing is free, so it
// cannot tell whether the prepaid credits have run out.
func (c *Client) Check(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models?pageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, failure("models", resp.StatusCode, data)
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Models {
		ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
	}
	return ids, nil
}

// ---------- chat ----------

// ChatJSON runs one system+user exchange constrained to a JSON schema and
// decodes the answer into out. Images (PNG) follow the user's text, each
// introduced by its label.
func (c *Client) ChatJSON(ctx context.Context, system, user string, images []pipeline.Image, schemaName string, schema map[string]any, out any) error {
	parts := []part{{Text: user}}
	for _, img := range images {
		parts = append(parts, part{Text: img.Label}, part{InlineData: &blob{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(img.PNG)}})
	}
	req := request{
		SystemInstruction: &content{Parts: []part{{Text: system}}},
		Contents:          []content{{Role: "user", Parts: parts}},
		GenerationConfig:  generationConfig{ResponseMimeType: "application/json", ResponseJSONSchema: schema},
	}
	resp, err := c.generate(ctx, c.TextModel, req, usage.Call{Kind: usage.Chat, Op: schemaName})
	if err != nil {
		return err
	}
	answer, err := resp.answer()
	if err != nil {
		return err
	}
	var text strings.Builder
	for _, p := range answer {
		text.WriteString(p.Text)
	}
	s := stripFence(strings.TrimSpace(text.String()))
	if s == "" {
		return fmt.Errorf("gemini %s: no answer (finish reason %s)", c.TextModel, resp.finish())
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("gemini %s: model answer is not the expected JSON: %w\n%s", c.TextModel, err, truncate(s, 400))
	}
	return nil
}

func stripFence(s string) string {
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
