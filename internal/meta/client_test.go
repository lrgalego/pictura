package meta

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/meta/metatest"
	"github.com/lrgalego/pictura/internal/pipeline"
)

func client(s *metatest.Server) *Client {
	c := New("LLM|test-key")
	c.BaseURL = s.URL
	return c
}

func TestNewDefaults(t *testing.T) {
	c := New("k")
	if c.BaseURL != DefaultBaseURL || c.TextModel != DefaultTextModel || c.ImageModel != DefaultImageModel || c.HTTP == nil {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestChatJSONStructuredOutput(t *testing.T) {
	s := metatest.New(t)
	c := client(s)
	var out struct {
		Characters []struct {
			Name   string `json:"name"`
			Visual string `json:"visual"`
		} `json:"characters"`
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"characters": map[string]any{"type": "array"}}}
	err := c.ChatJSON(context.Background(), "You are an editor.", "Find the cast.", nil, "characters", schema, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Characters) != 2 || out.Characters[0].Name != "Mara" || out.Characters[1].Name != "Pip" {
		t.Fatalf("decoded: %+v", out)
	}
	req := s.Last(t)
	if req.Path != "/chat/completions" || req.Auth != "Bearer LLM|test-key" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request shape: %+v", req)
	}
	if req.Body["model"] != DefaultTextModel || req.Body["reasoning_effort"] != "medium" || req.Body["stream"] != false {
		t.Fatalf("body: %v", req.Body)
	}
	msgs := req.Body["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "developer" || msgs[1].(map[string]any)["content"] != "Find the cast." {
		t.Fatalf("messages: %v", msgs)
	}
	rf := req.Body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "characters" || js["strict"] != false || js["schema"] == nil {
		t.Fatalf("response_format: %v", rf)
	}
}

func TestChatJSONSendsImageParts(t *testing.T) {
	s := metatest.New(t)
	c := client(s)
	var out map[string]any
	img := pipeline.Image{Label: "Reference image 1 (file: coat.png): the coat", PNG: []byte("PNGDATA")}
	if err := c.ChatJSON(context.Background(), "sys", "user text", []pipeline.Image{img}, "x", map[string]any{"type": "object"}, &out); err != nil {
		t.Fatal(err)
	}
	msgs := s.Last(t).Body["messages"].([]any)
	parts := msgs[1].(map[string]any)["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("expected text + label + image parts, got %d", len(parts))
	}
	p0, p1, p2 := parts[0].(map[string]any), parts[1].(map[string]any), parts[2].(map[string]any)
	if p0["type"] != "text" || p0["text"] != "user text" || p1["text"] != img.Label || p2["type"] != "image_url" {
		t.Fatalf("parts: %v", parts)
	}
	url := p2["image_url"].(map[string]any)["url"].(string)
	if url != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(img.PNG) {
		t.Fatalf("image data url: %s", url)
	}
}

func TestChatJSONErrors(t *testing.T) {
	ctx := context.Background()
	var out map[string]any
	schema := map[string]any{"type": "object"}

	s := metatest.New(t)
	c := client(s)
	unauthorized := metatest.Load(t, "error_unauthorized.json")
	s.Fail = &unauthorized
	err := c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out)
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") || !strings.Contains(err.Error(), "invalid_api_key") {
		t.Fatalf("401 should surface the API message, got %v", err)
	}
	bad := metatest.Load(t, "error_bad_request.json")
	s.Fail = &bad
	err = c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out)
	if err == nil || !strings.Contains(err.Error(), `does not support "none"`) {
		t.Fatalf("400 should surface the API message, got %v", err)
	}
	plain := metatest.JSON(http.StatusBadGateway, `<html>upstream down</html>`)
	s.Fail = &plain
	err = c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("non-JSON error should report the status, got %v", err)
	}
	s.Fail = nil
	s.Responses["/chat/completions"] = metatest.JSON(200, `{"choices": []}`)
	if err := c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out); err == nil || !strings.Contains(err.Error(), "empty completion") {
		t.Fatalf("empty choices: %v", err)
	}
	s.Responses["/chat/completions"] = metatest.ChatContent(t, "not json at all")
	if err := c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out); err == nil || !strings.Contains(err.Error(), "not the expected JSON") {
		t.Fatalf("garbage content: %v", err)
	}
	s.Responses["/chat/completions"] = metatest.ChatContent(t, "```json\n{\"ok\": true}\n```")
	if err := c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out); err != nil || out["ok"] != true {
		t.Fatalf("fenced JSON should be accepted: %v %v", out, err)
	}
	if err := c.ChatJSON(ctx, "s", "u", nil, "x", map[string]any{"bad": make(chan int)}, &out); err == nil {
		t.Fatal("unmarshalable request should fail before sending")
	}
	c.BaseURL = "http://127.0.0.1:1"
	if err := c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out); err == nil || !strings.Contains(err.Error(), "meta api") {
		t.Fatalf("connection failure: %v", err)
	}
	c.BaseURL = "://bad"
	if err := c.ChatJSON(ctx, "s", "u", nil, "x", schema, &out); err == nil {
		t.Fatal("bad base URL should fail")
	}
}

func TestGenerateImage(t *testing.T) {
	s := metatest.New(t)
	c := client(s)
	png, err := c.GenerateImage(context.Background(), "a tiny teal robot", pipeline.SheetSize)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(png), "\x89PNG") {
		t.Fatal("decoded image should be the PNG from the fixture")
	}
	req := s.Last(t)
	if req.Path != "/images/generations" || req.Body["model"] != DefaultImageModel || req.Body["prompt"] != "a tiny teal robot" {
		t.Fatalf("request: %+v", req.Body)
	}
	if req.Body["n"] != float64(1) || req.Body["size"] != "1536x1024" || req.Body["response_format"] != "b64_json" || req.Body["output_format"] != "png" {
		t.Fatalf("image options: %v", req.Body)
	}
	if _, ok := req.Body["images"]; ok {
		t.Fatal("generation must not send an images array")
	}
}

func TestEditImageWithReferences(t *testing.T) {
	s := metatest.New(t)
	c := client(s)
	refs := [][]byte{[]byte("one"), []byte("two")}
	png, err := c.EditImage(context.Background(), "same robot, red scarf", refs, pipeline.PageSize)
	if err != nil || !strings.HasPrefix(string(png), "\x89PNG") {
		t.Fatalf("edit: %v", err)
	}
	req := s.Last(t)
	if req.Path != "/images/edits" || req.Body["size"] != "1024x1536" {
		t.Fatalf("request: %s %v", req.Path, req.Body)
	}
	images := req.Body["images"].([]any)
	if len(images) != 2 || images[1].(map[string]any)["image_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("two")) {
		t.Fatalf("images: %v", images)
	}
	// No references: falls back to a plain generation.
	s.Reset()
	if _, err := c.EditImage(context.Background(), "p", nil, pipeline.PageSize); err != nil {
		t.Fatal(err)
	}
	if s.Last(t).Path != "/images/generations" {
		t.Fatal("edit without references should generate")
	}
}

func TestImageResponseVariants(t *testing.T) {
	s := metatest.New(t)
	c := client(s)
	ctx := context.Background()

	s.Responses["/images/generations"] = metatest.JSON(200, `{"data": []}`)
	if _, err := c.GenerateImage(ctx, "p", ""); err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("empty data: %v", err)
	}
	s.Responses["/images/generations"] = metatest.JSON(200, `{"data": [{}]}`)
	if _, err := c.GenerateImage(ctx, "p", ""); err == nil || !strings.Contains(err.Error(), "neither b64_json nor url") {
		t.Fatalf("empty item: %v", err)
	}
	s.Responses["/images/generations"] = metatest.JSON(200, `{"data": [{"b64_json": "!!!not base64"}]}`)
	if _, err := c.GenerateImage(ctx, "p", ""); err == nil {
		t.Fatal("bad base64 should fail")
	}
	// A URL result is fetched.
	s.Responses["/hosted.png"] = metatest.JSON(200, `PNGBYTES`)
	s.Responses["/images/generations"] = metatest.JSON(200, `{"data": [{"url": "`+s.URL+`/hosted.png"}]}`)
	got, err := c.GenerateImage(ctx, "p", "")
	if err != nil || string(got) != "PNGBYTES" {
		t.Fatalf("url result: %q %v", got, err)
	}
	s.Responses["/images/generations"] = metatest.JSON(200, `{"data": [{"url": "http://127.0.0.1:1/x.png"}]}`)
	if _, err := c.GenerateImage(ctx, "p", ""); err == nil {
		t.Fatal("unreachable url should fail")
	}
	s.Responses["/images/generations"] = metatest.JSON(200, `{"data": [{"url": "::bad"}]}`)
	if _, err := c.GenerateImage(ctx, "p", ""); err == nil {
		t.Fatal("invalid url should fail")
	}
	unauthorized := metatest.Load(t, "error_unauthorized.json")
	s.Fail = &unauthorized
	if _, err := c.GenerateImage(ctx, "p", ""); err == nil {
		t.Fatal("api error should propagate")
	}
}

func TestHelpers(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Fatalf("truncate: %q", got)
	}
	if got := truncate("ab", 3); got != "ab" {
		t.Fatalf("truncate short: %q", got)
	}
	if got := stripFence("```json\n{}\n```"); got != "{}" {
		t.Fatalf("stripFence: %q", got)
	}
	if got := stripFence("{}"); got != "{}" {
		t.Fatalf("stripFence plain: %q", got)
	}
}
