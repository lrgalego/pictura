package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lrgalego/story-time/internal/jobs"
	"github.com/lrgalego/story-time/internal/pipeline"
	"github.com/lrgalego/story-time/internal/store"
)

const script = `THE LIGHTHOUSE KEEPER'S ROBOT

INT. LIGHTHOUSE. NIGHT.

MARA, nine, climbs the last of the spiral stairs in a raincoat two sizes too big. Something whirs in the dark.

MARA: Hello? Is somebody up here?
PIP: Please don't scream. I am very easy to dent.

A small teal robot rolls out from behind the great lamp, one amber eye blinking.

MARA: You're a robot.
PIP: And you are trespassing. Shall we call it even?

EXT. LIGHTHOUSE. CONTINUOUS.

Below, a long black car pulls up on the gravel. GRAVES steps out, silver cane tapping.

GRAVES: Find the robot. The key is inside it.
`

type env struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	runner *jobs.Runner
	st     *store.Store
	ai     *gated
}

func (e *env) store() *store.Store { return e.st }

// gated wraps the fake provider so a test can hold every model call open
// (hold) and let it through later (release), making "busy" states
// deterministic instead of racing a job that finishes instantly.
type gated struct {
	pipeline.Fake
	mu sync.RWMutex
}

func (g *gated) ChatJSON(ctx context.Context, system, user string, images []pipeline.Image, schemaName string, schema map[string]any, out any) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.Fake.ChatJSON(ctx, system, user, images, schemaName, schema, out)
}
func (g *gated) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.Fake.GenerateImage(ctx, prompt, size)
}
func (g *gated) EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.Fake.EditImage(ctx, prompt, refs, size)
}

// hold blocks model calls until release; the next job stays "running".
func (e *env) hold()    { e.ai.mu.Lock() }
func (e *env) release() { e.ai.mu.Unlock() }

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ai := &gated{}
	runner := jobs.New(st, ai, 2)
	srv := httptest.NewServer(Router(Deps{Store: st, Jobs: runner, Fake: true}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{t: t, srv: srv, client: client, runner: runner, st: st, ai: ai}
}

func (e *env) get(path string) (*http.Response, string) {
	e.t.Helper()
	resp, err := e.client.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (e *env) post(path string, form url.Values, hx bool) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (e *env) signup(name string) {
	e.t.Helper()
	resp, body := e.post("/signup", url.Values{"username": {name}, "password": {"correct-horse"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("signup: %d %s", resp.StatusCode, body)
	}
}

// waitIdle blocks until the runner has no job in flight.
func (e *env) waitIdle() {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		e.runner.Wait()
		return
	}
	e.t.Fatal("runner never went idle")
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.get("/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
}

func TestHomeAndAuthPages(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/login", "/signup"} {
		resp, body := e.get(p)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Story Time") {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	resp, _ := e.get("/stories")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("stories without login should redirect, got %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSignupValidationAndLogin(t *testing.T) {
	e := newEnv(t)
	resp, body := e.post("/signup", url.Values{"username": {"X!"}, "password": {"short"}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "At least 8 characters") {
		t.Fatalf("bad signup: %d %s", resp.StatusCode, body)
	}
	e.signup("lucas")
	resp, _ = e.post("/logout", nil, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	resp, body = e.post("/login", url.Values{"username": {"lucas"}, "password": {"wrong-password"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Wrong username or password") {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}
	resp, _ = e.post("/login", url.Values{"username": {"lucas"}, "password": {"correct-horse"}}, true)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != "/stories" {
		t.Fatalf("login: %d %s", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	resp, body = e.get("/stories")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "No stories yet") {
		t.Fatalf("library: %d", resp.StatusCode)
	}
}

func TestFullWorkflow(t *testing.T) {
	e := newEnv(t)
	e.signup("writer")

	// Step 1: a script that is too short is refused.
	resp, body := e.post("/stories", url.Values{"script": {"too short"}, "style": {"comic"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "at least a few paragraphs") {
		t.Fatalf("short script: %d", resp.StatusCode)
	}

	resp, _ = e.post("/stories", url.Values{"script": {script}, "style": {"manga"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	charsURL := resp.Header.Get("Location")
	if !strings.HasSuffix(charsURL, "/characters") {
		t.Fatalf("expected redirect to characters, got %s", charsURL)
	}
	storyBase := strings.TrimSuffix(charsURL, "/characters")

	// Step 2: the cast appears once the job finishes — descriptions only.
	e.waitIdle()
	resp, body = e.get(charsURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("characters: %d", resp.StatusCode)
	}
	for _, name := range []string{"Mara", "Pip", "Graves"} {
		if !strings.Contains(body, name) {
			t.Fatalf("cast is missing %s:\n%s", name, body)
		}
	}
	if strings.Contains(body, "/media/") || !strings.Contains(body, "Draw the character sheets") || strings.Contains(body, "Storyboard the pages") {
		t.Fatal("sheets must not be drawn before the writer asks")
	}
	// Storyboarding is refused until the sheets exist.
	resp, body = e.post(storyBase+"/characters/approve", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Draw the sheets first") {
		t.Fatalf("approve before sheets: %d", resp.StatusCode)
	}
	resp, _ = e.post(storyBase+"/characters/draw", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draw sheets: %d", resp.StatusCode)
	}
	e.waitIdle()
	_, body = e.get(charsURL)
	if !strings.Contains(body, "/media/") || !strings.Contains(body, "Storyboard the pages") {
		t.Fatal("no character sheet rendered after drawing")
	}
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Fatal("panel still polling after the job finished")
	}

	// A feedback dialog opens, empty feedback is refused, real feedback runs a job.
	resp, body = e.get(storyBase + "/characters/adjust")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Adjust the whole cast") {
		t.Fatalf("adjust dialog: %d", resp.StatusCode)
	}
	resp, body = e.post(storyBase+"/characters/adjust", url.Values{"feedback": {""}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Say what should change") {
		t.Fatalf("empty feedback: %d %s", resp.StatusCode, body)
	}
	resp, body = e.post(storyBase+"/characters/adjust", url.Values{"feedback": {"Give Pip a red scarf"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, `id="modal-root"`) {
		t.Fatalf("cast adjust: %d %s", resp.StatusCode, body)
	}
	e.waitIdle()
	_, body = e.get(charsURL)
	if !strings.Contains(body, "revised: Give Pip a red scarf") {
		t.Fatal("feedback was not applied to the cast")
	}

	// Approve the cast: the breakdown job starts and we land on the pages.
	resp, _ = e.post(storyBase+"/characters/approve", nil, true)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != storyBase+"/pages" {
		t.Fatalf("approve cast: %d %s", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	e.waitIdle()
	resp, body = e.get(storyBase + "/pages")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Page 1") || !strings.Contains(body, "Panel 1") {
		t.Fatalf("pages: %d", resp.StatusCode)
	}

	// Approve the pages: rendering starts, then the book has art and downloads.
	resp, _ = e.post(storyBase+"/pages/approve", nil, true)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != storyBase+"/book" {
		t.Fatalf("approve pages: %d", resp.StatusCode)
	}
	e.waitIdle()
	resp, body = e.get(storyBase + "/book")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Download PDF") || strings.Contains(body, "Waiting to be drawn") {
		t.Fatalf("book: %d\n%s", resp.StatusCode, body)
	}
	resp, body = e.get(storyBase + "/download.pdf")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "%PDF-1.4") || !strings.Contains(body, "%%EOF") {
		t.Fatalf("pdf: %d", resp.StatusCode)
	}
	resp, body = e.get(storyBase + "/download.zip")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "PK") {
		t.Fatalf("zip: %d", resp.StatusCode)
	}
	resp, body = e.get(storyBase + "/book/view?img=0")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Page 1") {
		t.Fatalf("lightbox: %d", resp.StatusCode)
	}

	// The library shows the story with a cover.
	_, body = e.get("/stories")
	if !strings.Contains(body, "Step 4 of 4") || !strings.Contains(body, "/media/") {
		t.Fatal("library should show the finished story with a cover")
	}

	// Another user cannot see it.
	other := newEnvClient(t, e)
	resp, _ = other.get(storyBase + "/book")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another user saw the story: %d", resp.StatusCode)
	}

	// Delete.
	resp, _ = e.post(storyBase+"/delete", nil, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = e.get(storyBase + "/book")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted story still served: %d", resp.StatusCode)
	}
}

// newEnvClient is a second logged-in user against the same server.
func newEnvClient(t *testing.T, e *env) *env {
	jar, _ := cookiejar.New(nil)
	o := &env{t: t, srv: e.srv, runner: e.runner, client: &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}
	o.signup("snoop")
	return o
}

// tinyPNG is a 4x4 solid image: enough to be decoded and normalized.
func tinyPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{200, 40, 60, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// postMultipart sends fields plus files under the "references" name.
func (e *env) postMultipart(path string, fields map[string]string, files map[string][]byte, hx bool) (*http.Response, string) {
	e.t.Helper()
	return e.postMultipartField(path, fields, "references", files, hx)
}

// postMultipartField sends fields plus files under the given field name.
func (e *env) postMultipartField(path string, fields map[string]string, field string, files map[string][]byte, hx bool) (*http.Response, string) {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for name, data := range files {
		fw, _ := mw.CreateFormFile(field, name)
		_, _ = fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestReferenceImages(t *testing.T) {
	e := newEnv(t)
	e.signup("illustrator")

	// The script form has no upload: references belong to a character.
	_, body := e.get("/stories/new")
	if strings.Contains(body, `name="references"`) {
		t.Fatal("script form should not offer uploads")
	}
	resp, _ := e.post("/stories", url.Values{"script": {script}, "style": {"comic"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	storyBase := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	_, body = e.get(storyBase + "/characters")
	cid := firstCharacterID(body)
	if !strings.Contains(body, "Attach reference images") {
		t.Fatal("cards should offer an upload in the casting phase")
	}

	// A non-image is refused; a real image attaches to the character and
	// revises the description without drawing anything.
	resp, body = e.postMultipart(storyBase+"/characters/"+cid+"/refs", nil, map[string][]byte{"notes.txt": []byte("hello")}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "not a readable image") {
		t.Fatalf("bad upload: %d", resp.StatusCode)
	}
	resp, _ = e.postMultipart(storyBase+"/characters/"+cid+"/refs", map[string]string{"note": "the raincoat"}, map[string][]byte{"coat.png": tinyPNG()}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach: %d", resp.StatusCode)
	}
	e.waitIdle()
	_, body = e.get(storyBase + "/characters")
	if !strings.Contains(body, "1 reference image") || !strings.Contains(body, "the raincoat") || !strings.Contains(body, "matched to 1 reference image") {
		t.Fatalf("reference not attached or description not revised:\n%s", body)
	}
	if strings.Contains(body, "/media/t/") && strings.Count(body, "/media/t/") != 1 {
		t.Fatal("only the reference thumbnail should be an image; no sheets yet")
	}

	// The character's adjust dialog offers uploads; empty is refused; an image alone works.
	_, body = e.get(storyBase + "/characters/" + cid + "/adjust")
	if !strings.Contains(body, `name="references"`) {
		t.Fatal("character adjust dialog has no upload field")
	}
	_, body = e.get(storyBase + "/characters/adjust")
	if strings.Contains(body, `name="references"`) {
		t.Fatal("cast-wide adjust must not offer uploads")
	}
	resp, body = e.postMultipart(storyBase+"/characters/"+cid+"/adjust", map[string]string{"feedback": ""}, nil, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "attach reference images") {
		t.Fatalf("empty adjust: %d", resp.StatusCode)
	}
	resp, _ = e.postMultipart(storyBase+"/characters/"+cid+"/adjust", map[string]string{"feedback": ""}, map[string][]byte{"sketch.png": tinyPNG()}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adjust with only an image: %d", resp.StatusCode)
	}
	e.waitIdle()
	_, body = e.get(storyBase + "/characters")
	if !strings.Contains(body, "2 reference images") {
		t.Fatalf("second reference not attached:\n%s", body)
	}

	// A finished sheet can be uploaded as-is: it becomes the sheet without any
	// drawing, and the description is rewritten from it.
	_, body = e.get(storyBase + "/characters")
	cids := allCharacterIDs(body)
	other := cids[1]
	resp, body = e.postMultipartField(storyBase+"/characters/"+other+"/sheet", nil, "sheet", map[string][]byte{"notes.txt": []byte("x")}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "not a readable image") {
		t.Fatalf("bad sheet accepted: %d", resp.StatusCode)
	}
	resp, body = e.postMultipartField(storyBase+"/characters/"+other+"/sheet", nil, "sheet", map[string][]byte{"pip-sheet.png": tinyPNG()}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Sheet set for") {
		t.Fatalf("sheet upload: %d %s", resp.StatusCode, body)
	}
	e.waitIdle()
	_, body = e.get(storyBase + "/characters")
	if strings.Count(body, `character sheet"`) != 1 || !strings.Contains(body, "Draw the remaining sheets") {
		t.Fatalf("uploaded sheet should be the only sheet so far:\n%s", body)
	}
	if !strings.Contains(body, "matched to 1 reference image") {
		t.Fatalf("the uploaded sheet should have revised the description:\n%s", body)
	}

	// Now draw: the sheet for the referenced character is made from its references.
	resp, _ = e.post(storyBase+"/characters/draw", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draw: %d", resp.StatusCode)
	}
	e.waitIdle()
	_, body = e.get(storyBase + "/characters")
	if strings.Count(body, "character sheet\"") != 3 || !strings.Contains(body, "Redraw") || !strings.Contains(body, "Storyboard the pages") {
		t.Fatalf("sheets not drawn:\n%s", body)
	}

	// Remove one reference.
	rid := firstRefID(body)
	resp, body = e.post(storyBase+"/refs/"+rid+"/delete", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "1 reference image") {
		t.Fatalf("delete reference: %d", resp.StatusCode)
	}
}

func allCharacterIDs(body string) []string {
	var out []string
	rest := body
	for {
		i := strings.Index(rest, `id="char-`)
		if i < 0 {
			return out
		}
		rest = rest[i+len(`id="char-`):]
		out = append(out, rest[:strings.Index(rest, `"`)])
	}
}

func firstCharacterID(body string) string {
	i := strings.Index(body, `id="char-`)
	rest := body[i+len(`id="char-`):]
	return rest[:strings.Index(rest, `"`)]
}

func firstRefID(body string) string {
	i := strings.Index(body, `id="ref-`)
	rest := body[i+len(`id="ref-`):]
	return rest[:strings.Index(rest, `"`)]
}

func TestCastRegistry(t *testing.T) {
	e := newEnv(t)
	e.signup("showrunner")

	// Story A: cast found and drawn.
	resp, _ := e.post("/stories", url.Values{"title": {"Book One"}, "script": {script}, "style": {"comic"}}, false)
	a := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	_, body := e.get(a + "/characters")
	if strings.Contains(body, "Same ") {
		t.Fatal("first story should have no suggestions")
	}
	e.post(a+"/characters/draw", nil, true)
	e.waitIdle()
	_, body = e.get(a + "/characters")
	maraA := firstCharacterID(body)

	// Story B with the same names: suggestions appear on every pending character.
	resp, _ = e.post("/stories", url.Values{"title": {"Book Two"}, "script": {script}, "style": {"manga"}}, false)
	b := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	_, body = e.get(b + "/characters")
	if !strings.Contains(body, "Same Mara as in Book One?") || !strings.Contains(body, "Same Pip as in Book One?") {
		t.Fatalf("expected name-based suggestions:\n%s", body)
	}
	maraB := firstCharacterID(body)

	// The link dialog lists the registry (one entry per character).
	_, body = e.get(b + "/characters/" + maraB + "/link")
	if strings.Count(body, `class="link-row"`) != 3 {
		t.Fatalf("link dialog should list 3 characters:\n%s", body)
	}

	// Link B's Mara to A's Mara: sheet copied, lineage shown, suggestion gone.
	resp, body = e.post(b+"/characters/"+maraB+"/link", url.Values{"source": {maraA}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "is now the same character") || !strings.Contains(body, "Copying the character") {
		t.Fatalf("link: %d %s", resp.StatusCode, body)
	}
	e.waitIdle()
	_, body = e.get(b + "/characters")
	if strings.Count(body, `character sheet"`) != 1 || !strings.Contains(body, "Also in") || !strings.Contains(body, "Book One") {
		t.Fatalf("linked character should carry the copied sheet and lineage:\n%s", body)
	}
	if strings.Contains(body, "Same Mara as in") || !strings.Contains(body, "Same Pip as in") {
		t.Fatal("suggestion should disappear only for the linked character")
	}
	if !strings.Contains(body, "Draw the remaining sheets") {
		t.Fatal("toolbar should offer to draw the remaining sheets")
	}

	// The registry page groups the two Maras as one character; the unlinked
	// Pip and Graves of book two stay separate.
	_, body = e.get("/cast")
	if n := strings.Count(body, `class="castlib__name"`); n != 5 {
		t.Fatalf("registry should list 5 characters, got %d:\n%s", n, body)
	}
	if n := strings.Count(body, `class="castlib__name">Mara<`); n != 1 {
		t.Fatalf("Mara should appear once in the registry, got %d", n)
	}
	if !strings.Contains(body, "Book One") || !strings.Contains(body, "Book Two") {
		t.Fatal("registry should name both stories for Mara")
	}

	// Another user has an empty registry and cannot link to these characters.
	other := newEnvClient(t, e)
	_, body = other.get("/cast")
	if !strings.Contains(body, "No characters yet") {
		t.Fatal("other user should see an empty registry")
	}
	resp, _ = other.post(b+"/characters/"+maraB+"/link", url.Values{"source": {maraA}}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other user linked into someone else's story: %d", resp.StatusCode)
	}
}
