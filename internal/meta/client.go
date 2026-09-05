// Package meta is a small client for the Meta Model API
// (https://api.meta.ai/v1): OpenAI-compatible chat completions with
// structured output, and the Muse Image generation / edit endpoints.
package meta

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lrgalego/pictura/internal/pipeline"
)

const (
	DefaultBaseURL    = "https://api.meta.ai/v1"
	DefaultTextModel  = "muse-spark-1.3-contributor"
	DefaultImageModel = "muse-image-1.0"
)

// Client talks to the Meta Model API.
type Client struct {
	APIKey     string
	BaseURL    string
	TextModel  string
	ImageModel string
	HTTP       *http.Client
}

// New returns a client with the default models and a generous timeout —
// image generation regularly takes a minute.
func New(apiKey string) *Client {
	return &Client{
		APIKey:     apiKey,
		BaseURL:    DefaultBaseURL,
		TextModel:  DefaultTextModel,
		ImageModel: DefaultImageModel,
		HTTP:       &http.Client{Timeout: 6 * time.Minute},
	}
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("meta api: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var ae apiError
		if json.Unmarshal(data, &ae) == nil && ae.Error.Message != "" {
			return fmt.Errorf("meta api %s: %s (%s)", path, ae.Error.Message, ae.Error.Code)
		}
		return fmt.Errorf("meta api %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(data), 300))
	}
	return json.Unmarshal(data, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- chat ----------

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string, or []part for multimodal turns
}

type part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []message      `json:"messages"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ResponseFormat  map[string]any `json:"response_format,omitempty"`
	Stream          bool           `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// ChatJSON runs one developer+user exchange with a JSON schema response
// format and decodes the answer into out. Images (PNG) are appended to
// the user turn, each introduced by its label.
func (c *Client) ChatJSON(ctx context.Context, system, user string, images []pipeline.Image, schemaName string, schema map[string]any, out any) error {
	var turn any = user
	if len(images) > 0 {
		parts := []part{{Type: "text", Text: user}}
		for _, img := range images {
			parts = append(parts,
				part{Type: "text", Text: img.Label},
				part{Type: "image_url", ImageURL: &imageURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG)}},
			)
		}
		turn = parts
	}
	req := chatRequest{
		Model: c.TextModel,
		Messages: []message{
			{Role: "developer", Content: system},
			{Role: "user", Content: turn},
		},
		ReasoningEffort: "medium",
		ResponseFormat: map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   schemaName,
				"strict": false,
				"schema": schema,
			},
		},
	}
	var resp chatResponse
	if err := c.post(ctx, "/chat/completions", req, &resp); err != nil {
		return err
	}
	if len(resp.Choices) == 0 {
		return fmt.Errorf("meta api: empty completion")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	content = stripFence(content)
	if err := json.Unmarshal([]byte(content), out); err != nil {
		return fmt.Errorf("meta api: model answer is not the expected JSON: %w\n%s", err, truncate(content, 400))
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

// ---------- images ----------

type imageRef struct {
	ImageURL string `json:"image_url"`
}

type imageRequest struct {
	Model          string     `json:"model"`
	Prompt         string     `json:"prompt"`
	N              int        `json:"n"`
	Size           string     `json:"size,omitempty"`
	ResponseFormat string     `json:"response_format"`
	OutputFormat   string     `json:"output_format"`
	Images         []imageRef `json:"images,omitempty"`
}

type imageResponse struct {
	Data []struct {
		B64 string `json:"b64_json"`
		URL string `json:"url"`
	} `json:"data"`
}

// GenerateImage renders a prompt to a PNG. size is an aspect-ratio hint
// such as "1536x1024".
func (c *Client) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	req := imageRequest{Model: c.ImageModel, Prompt: prompt, N: 1, Size: size, ResponseFormat: "b64_json", OutputFormat: "png"}
	return c.image(ctx, "/images/generations", req)
}

// EditImage renders a prompt conditioned on reference images (PNG bytes),
// which is how character sheets keep a page on-model.
func (c *Client) EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error) {
	if len(refs) == 0 {
		return c.GenerateImage(ctx, prompt, size)
	}
	req := imageRequest{Model: c.ImageModel, Prompt: prompt, N: 1, Size: size, ResponseFormat: "b64_json", OutputFormat: "png"}
	for _, r := range refs {
		req.Images = append(req.Images, imageRef{ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(r)})
	}
	return c.image(ctx, "/images/edits", req)
}

func (c *Client) image(ctx context.Context, path string, req imageRequest) ([]byte, error) {
	var resp imageResponse
	if err := c.post(ctx, path, req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("meta api: no image returned")
	}
	if resp.Data[0].B64 != "" {
		return base64.StdEncoding.DecodeString(resp.Data[0].B64)
	}
	if resp.Data[0].URL != "" {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, resp.Data[0].URL, nil)
		if err != nil {
			return nil, err
		}
		res, err := c.HTTP.Do(r)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		return io.ReadAll(io.LimitReader(res.Body, 64<<20))
	}
	return nil, fmt.Errorf("meta api: image response carried neither b64_json nor url")
}
