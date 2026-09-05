// Package metatest is a stand-in for the Meta Model API built from recorded
// responses (testdata/*.json, captured from the real endpoints with the
// multi-megabyte image payloads replaced by a 2x2 PNG). Tests point a
// meta.Client at Server.URL and assert on the requests it received.
package metatest

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata/*.json
var fixtures embed.FS

// Fixture is one recorded exchange: the HTTP status and the JSON body.
type Fixture struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// Load reads a recorded fixture by file name (e.g. "chat_completion.json").
func Load(t testing.TB, name string) Fixture {
	t.Helper()
	b, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var f Fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return f
}

// Request is one call the server received, decoded for assertions.
type Request struct {
	Path   string
	Auth   string
	Body   map[string]any
	Raw    []byte
	Header http.Header
}

// Server serves recorded responses and records requests.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []Request
	// Responses maps a path ("/chat/completions", "/images/generations",
	// "/images/edits") to the fixture to answer with. Missing = 404.
	Responses map[string]Fixture
	// Fail, when set, is returned for every path instead of Responses.
	Fail *Fixture
}

// New starts a server answering with the happy-path fixtures.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{Responses: map[string]Fixture{
		"/chat/completions":   Load(t, "chat_completion.json"),
		"/images/generations": Load(t, "image_generation.json"),
		"/images/edits":       Load(t, "image_edit.json"),
	}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body, Raw: raw, Header: r.Header.Clone()})
	fail := s.Fail
	fx, ok := s.Responses[r.URL.Path]
	s.mu.Unlock()
	if fail != nil {
		fx, ok = *fail, true
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(fx.Status)
	_, _ = w.Write(fx.Body)
}

// Requests returns a copy of everything received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Last returns the most recent request, failing the test if there is none.
func (s *Server) Last(t testing.TB) Request {
	t.Helper()
	reqs := s.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request received")
	}
	return reqs[len(reqs)-1]
}

// Reset forgets recorded requests.
func (s *Server) Reset() {
	s.mu.Lock()
	s.requests = nil
	s.mu.Unlock()
}

// ChatContent returns a fixture whose completion content is the given
// string, for tests that need a specific model answer.
func ChatContent(t testing.TB, content string) Fixture {
	t.Helper()
	base := Load(t, "chat_completion.json")
	var body map[string]any
	_ = json.Unmarshal(base.Body, &body)
	choices := body["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	msg["content"] = content
	b, _ := json.Marshal(body)
	return Fixture{Status: 200, Body: b}
}

// JSON is a fixture from a literal body.
func JSON(status int, body string) Fixture {
	return Fixture{Status: status, Body: json.RawMessage(strings.TrimSpace(body))}
}
