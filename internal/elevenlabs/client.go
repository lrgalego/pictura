// Package elevenlabs is a small client for the ElevenLabs text-to-speech
// API (https://api.elevenlabs.io/v1): one line of text in, an MP3 and the
// time every character is spoken out, which is what word highlighting
// needs. Meta's Model API has no speech synthesis, so this is the voice.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/lrgalego/pictura/internal/pipeline"
)

const (
	DefaultBaseURL = "https://api.elevenlabs.io/v1"
	// DefaultModel is the most expressive model: it acts out ellipses,
	// exclamations and questions, which suits stories read to children.
	DefaultModel = "eleven_v3"
	// DefaultFormat is the output format: MP3 plays in every browser.
	DefaultFormat = "mp3_44100_128"
)

// Client talks to ElevenLabs.
type Client struct {
	APIKey  string
	BaseURL string
	Model   string
	Format  string
	HTTP    *http.Client
	sem     chan struct{}
}

// New returns a client with the default model. ElevenLabs caps concurrent
// requests per plan (two on the free tier), so calls beyond that wait here
// rather than failing there.
func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		Model:   DefaultModel,
		Format:  DefaultFormat,
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
		sem:     make(chan struct{}, 2),
	}
}

type speechRequest struct {
	Text    string `json:"text"`
	ModelID string `json:"model_id"`
}

type alignment struct {
	Characters []string  `json:"characters"`
	Starts     []float64 `json:"character_start_times_seconds"`
	Ends       []float64 `json:"character_end_times_seconds"`
}

type speechResponse struct {
	Audio     string     `json:"audio_base64"`
	Alignment *alignment `json:"alignment"`
}

type apiError struct {
	Detail json.RawMessage `json:"detail"`
}

// Speak synthesizes one line in the given voice. The word timings come
// from the alignment of the text as sent (not the normalized one, which
// spells out numbers), so they line up with the words on the page.
func (c *Client) Speak(ctx context.Context, text, voiceID string) (*pipeline.Speech, error) {
	if c.sem != nil {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	payload, err := json.Marshal(speechRequest{Text: text, ModelID: c.Model})
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/text-to-speech/%s/with-timestamps?output_format=%s", c.BaseURL, url.PathEscape(voiceID), url.QueryEscape(c.Format))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("elevenlabs: %s", errorMessage(resp.StatusCode, data))
	}
	var out speechResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("elevenlabs: unreadable response: %w", err)
	}
	audio, err := base64.StdEncoding.DecodeString(out.Audio)
	if err != nil || len(audio) == 0 {
		return nil, fmt.Errorf("elevenlabs: response carried no audio")
	}
	sp := &pipeline.Speech{Audio: audio, Ext: "mp3"}
	if a := out.Alignment; a != nil {
		sp.Words = pipeline.WordsFromChars(a.Characters, a.Starts, a.Ends)
	}
	return sp, nil
}

// errorMessage pulls the human message out of ElevenLabs' error body, whose
// detail is an object with a message, or sometimes a plain string.
func errorMessage(status int, body []byte) string {
	var e apiError
	if json.Unmarshal(body, &e) == nil && len(e.Detail) > 0 {
		var d struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Status  string `json:"status"`
		}
		if json.Unmarshal(e.Detail, &d) == nil && d.Message != "" {
			code := d.Code
			if code == "" {
				code = d.Status
			}
			return fmt.Sprintf("%s (%s)", d.Message, code)
		}
		var s string
		if json.Unmarshal(e.Detail, &s) == nil && s != "" {
			return s
		}
	}
	msg := string(body)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", status, msg)
}
