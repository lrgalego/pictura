package jobs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

const script = `THE LIGHTHOUSE

MARA: Hello? Is somebody up here?
PIP: Please don't scream. I am very easy to dent.
GRAVES: Find the robot. The key is inside it.
MARA: Whatever happens, don't whir.
`

func tinyPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// flaky wraps the fake and fails selected calls.
type flaky struct {
	pipeline.AI
	failChat  bool
	failImage bool
}

func (f *flaky) ChatJSON(ctx context.Context, system, user string, images []pipeline.Image, schemaName string, schema map[string]any, out any) error {
	if f.failChat {
		return errors.New("chat down")
	}
	return f.AI.ChatJSON(ctx, system, user, images, schemaName, schema, out)
}
func (f *flaky) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	if f.failImage {
		return nil, errors.New("image down")
	}
	return f.AI.GenerateImage(ctx, prompt, size)
}
func (f *flaky) EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error) {
	if f.failImage {
		return nil, errors.New("image down")
	}
	return f.AI.EditImage(ctx, prompt, refs, size)
}

type env struct {
	t   *testing.T
	st  *store.Store
	ai  *flaky
	r   *Runner
	ctx context.Context
	sto *store.Story
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "w", "W", "h")
	sto, _ := st.CreateStory(ctx, u.ID, "", script, "", "comic")
	ai := &flaky{AI: &pipeline.Fake{}}
	return &env{t: t, st: st, ai: ai, r: New(st, ai, 0), ctx: ctx, sto: sto}
}

func (e *env) wait() {
	e.r.Wait()
}

func (e *env) mustDoneJob(what string, id int64) {
	e.t.Helper()
	e.wait()
	if j, _ := e.st.Job(e.ctx, id); j == nil || j.Status != store.JobDone {
		e.t.Fatalf("%s: job %+v", what, j)
	}
}

// job returns the newest job on the story, story-level or per character.
func (e *env) job() *store.Job {
	newest, _ := e.st.LatestJob(e.ctx, e.sto.ID)
	perChar, _ := e.st.LatestCharacterJobs(e.ctx, e.sto.ID)
	for _, j := range perChar {
		if newest == nil || j.ID > newest.ID {
			newest = j
		}
	}
	return newest
}

func (e *env) chars() []*store.Character {
	c, _ := e.st.Characters(e.ctx, e.sto.ID)
	return c
}

func (e *env) pages() []*store.Page {
	p, _ := e.st.Pages(e.ctx, e.sto.ID)
	return p
}

func (e *env) mustDone(what string) {
	e.t.Helper()
	e.wait()
	if j := e.job(); j == nil || j.Status != store.JobDone {
		e.t.Fatalf("%s: job %+v", what, j)
	}
}

func (e *env) mustFail(what, contains string) {
	e.t.Helper()
	e.wait()
	if j := e.job(); j == nil || j.Status != store.JobError || !strings.Contains(j.Error, contains) {
		e.t.Fatalf("%s: expected failure containing %q, job %+v", what, contains, j)
	}
}

func TestAnalyzeThenDrawSheets(t *testing.T) {
	e := newEnv(t)
	if err := e.r.Analyze(e.sto.ID); err != nil {
		t.Fatal(err)
	}
	if !e.r.Working(e.sto.ID) {
		t.Fatal("story should be working while analyzing")
	}
	e.mustDone("analyze")
	if e.r.Busy(e.sto.ID) || e.r.Working(e.sto.ID) {
		t.Fatal("story should be idle after the job")
	}
	sto, _ := e.st.Story(e.ctx, e.sto.ID)
	chars := e.chars()
	if sto.Step != store.StepCharacters || sto.Title == "" || sto.World == "" || len(chars) != 3 {
		t.Fatalf("analysis result: %+v chars=%d", sto, len(chars))
	}
	if SheetsStarted(chars) || AllReady(chars) || AllReady(nil) {
		t.Fatal("no sheets before DrawSheets")
	}
	if err := e.r.DrawSheets(e.sto.ID); err != nil {
		t.Fatal(err)
	}
	e.mustDone("draw sheets")
	chars = e.chars()
	if !AllReady(chars) || !SheetsStarted(chars) {
		t.Fatalf("sheets should all be ready: %+v", chars)
	}
	if j := e.job(); j.Progress != 3 || j.Total != 3 {
		t.Fatalf("progress should reach 3/3: %+v", j)
	}
	// Nothing left to draw: the job completes with no work.
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("draw nothing")
	if Elapsed(e.job()) < 0 || Elapsed(nil) != 0 {
		t.Fatal("Elapsed")
	}
}

func TestAnalyzeFailures(t *testing.T) {
	e := newEnv(t)
	e.ai.failChat = true
	_ = e.r.Analyze(e.sto.ID)
	e.mustFail("analyze", "chat down")
	if err := e.r.Analyze(99999); err == nil {
		t.Fatal("a job for a missing story cannot be recorded (foreign key)")
	}
	if e.r.Working(99999) {
		t.Fatal("a refused job must not leave the story marked working")
	}
}

func TestDrawSheetsFailureIsRecordedPerCharacter(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	e.ai.failImage = true
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustFail("draw", "image down")
	for _, c := range e.chars() {
		if c.SheetStatus != store.ImageError || c.SheetError != "image down" {
			t.Fatalf("each character should carry the error: %+v", c)
		}
	}
	e.ai.failImage = false
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("retry")
	if !AllReady(e.chars()) {
		t.Fatal("retry should draw the failed sheets")
	}
}

func TestReviseAndReferences(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	c := e.chars()[0]
	_, _ = e.st.InsertRef(e.ctx, e.sto.ID, c.ID, "coat.png", "the coat", tinyPNG(), 1600)

	// Casting phase: revise only, no drawing.
	if err := e.r.Revise(e.sto.ID, c.ID, "", true, false); err != nil {
		t.Fatal(err)
	}
	e.mustDone("revise")
	got, _ := e.st.Character(e.ctx, c.ID)
	if got.SheetStatus != store.ImagePending || !strings.Contains(got.Visual, "matched to 1 reference image") {
		t.Fatalf("revise without draw: %+v", got)
	}
	// Plain redraw with feedback draws from the references (edit path).
	if err := e.r.Revise(e.sto.ID, c.ID, "shorter hair", false, true); err != nil {
		t.Fatal(err)
	}
	e.mustDone("revise+draw")
	got, _ = e.st.Character(e.ctx, c.ID)
	if got.SheetStatus != store.ImageReady || !strings.Contains(got.Wardrobe, "revised: shorter hair") {
		t.Fatalf("revise with draw: %+v", got)
	}
	// Redraw without feedback keeps the words.
	before := got.Wardrobe
	_ = e.r.Revise(e.sto.ID, c.ID, "", false, true)
	e.mustDone("redraw")
	got, _ = e.st.Character(e.ctx, c.ID)
	if got.Wardrobe != before {
		t.Fatal("plain redraw must not touch the description")
	}
	// Errors: missing character, chat down, image down.
	_ = e.r.Revise(e.sto.ID, 424242, "", false, true)
	e.mustFail("missing character", "not found")
	e.ai.failChat = true
	_ = e.r.Revise(e.sto.ID, c.ID, "x", false, true)
	e.mustFail("chat down", "chat down")
	e.ai.failChat = false
	e.ai.failImage = true
	_ = e.r.Revise(e.sto.ID, c.ID, "", false, true)
	e.mustFail("image down", "image down")
	got, _ = e.st.Character(e.ctx, c.ID)
	if got.SheetStatus != store.ImageError {
		t.Fatal("failed redraw should mark the sheet errored")
	}
}

func TestAdoptSheet(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	c := e.chars()[1]
	_ = e.r.AdoptSheet(e.sto.ID, c.ID)
	e.mustFail("adopt without sheet", "no sheet to read")
	name, _ := e.st.SaveImage(e.ctx, e.sto.ID, "png", tinyPNG())
	_ = e.st.SetCharacterSheet(e.ctx, c.ID, name, store.ImageReady, "")
	if err := e.r.AdoptSheet(e.sto.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.mustDone("adopt")
	got, _ := e.st.Character(e.ctx, c.ID)
	if !strings.Contains(got.Visual, "matched to 1 reference image") || got.SheetStatus != store.ImageReady {
		t.Fatalf("adopt should describe from the sheet and keep it: %+v", got)
	}
	_ = e.r.AdoptSheet(e.sto.ID, 4242)
	e.mustFail("adopt missing", "not found")
	e.ai.failChat = true
	_ = e.r.AdoptSheet(e.sto.ID, c.ID)
	e.mustFail("adopt chat down", "chat down")
}

func TestAdjustCastInBothPhases(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	// Casting phase: descriptions change, nothing is drawn.
	if err := e.r.AdjustCast(e.sto.ID, "red scarf"); err != nil {
		t.Fatal(err)
	}
	e.mustDone("adjust casting")
	chars := e.chars()
	if SheetsStarted(chars) || !strings.Contains(chars[0].Wardrobe, "revised: red scarf") {
		t.Fatalf("casting-phase adjust: %+v", chars[0])
	}
	// Art phase: changed looks are redrawn, and pages are invalidated.
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("draw")
	_ = e.r.Breakdown(e.sto.ID)
	e.mustDone("breakdown")
	_ = e.r.RenderAll(e.sto.ID)
	e.mustDone("render")
	if err := e.r.AdjustCast(e.sto.ID, "blue scarf"); err != nil {
		t.Fatal(err)
	}
	e.mustDone("adjust art")
	chars = e.chars()
	if !AllReady(chars) || !strings.Contains(chars[0].Wardrobe, "revised: blue scarf") {
		t.Fatalf("art-phase adjust should redraw: %+v", chars[0])
	}
	for _, p := range e.pages() {
		if p.ImageStatus != store.ImagePending {
			t.Fatalf("pages should be marked for redraw after a cast change: %+v", p)
		}
	}
	// A cast revision that drops a character removes it; the fake always
	// returns the full cast, so simulate by renaming one before adjusting.
	extra := &store.Character{StoryID: e.sto.ID, Name: "Ghost", Position: 9}
	_ = e.st.InsertCharacter(e.ctx, extra)
	_ = e.r.AdjustCast(e.sto.ID, "drop the ghost")
	e.mustDone("adjust drop")
	for _, c := range e.chars() {
		if c.Name == "Ghost" {
			t.Fatal("characters absent from the revised cast should be deleted")
		}
	}
	e.ai.failChat = true
	_ = e.r.AdjustCast(e.sto.ID, "x")
	e.mustFail("adjust chat down", "chat down")
}

func TestBreakdownAndPageRevisions(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("draw")
	if err := e.r.Breakdown(e.sto.ID); err != nil {
		t.Fatal(err)
	}
	e.mustDone("breakdown")
	sto, _ := e.st.Story(e.ctx, e.sto.ID)
	pages := e.pages()
	if sto.Step != store.StepPages || len(pages) == 0 {
		t.Fatalf("breakdown: step=%d pages=%d", sto.Step, len(pages))
	}
	_ = e.r.RenderAll(e.sto.ID)
	e.mustDone("render")
	pages = e.pages()
	drawn := pages[0].Image

	// Adjusting one page resets only that page's art.
	if err := e.r.AdjustPage(e.sto.ID, pages[0].ID, "more rain"); err != nil {
		t.Fatal(err)
	}
	e.mustDone("adjust page")
	p0, _ := e.st.Page(e.ctx, pages[0].ID)
	if p0.ImageStatus != store.ImagePending || !strings.Contains(p0.Summary, "revised: more rain") {
		t.Fatalf("adjust page: %+v", p0)
	}
	if len(pages) > 1 {
		if p1, _ := e.st.Page(e.ctx, pages[1].ID); p1.ImageStatus != store.ImageReady {
			t.Fatal("other pages keep their art")
		}
	}
	// Adjusting the whole plan keeps art for pages whose plan is unchanged.
	_ = e.r.RenderAll(e.sto.ID)
	e.mustDone("render again")
	if err := e.r.AdjustPages(e.sto.ID, "tighter"); err != nil {
		t.Fatal(err)
	}
	e.mustDone("adjust pages")
	for _, p := range e.pages() {
		if p.ImageStatus == store.ImageReady && p.Image == drawn {
			t.Fatal("changed plans must not keep the old art")
		}
		if !strings.Contains(p.Summary, "revised: tighter") {
			t.Fatalf("all pages should be revised: %+v", p)
		}
	}
	// samePlan: identical plans keep the art.
	old := &store.Page{Title: "T", Panels: []store.Panel{{Shot: "wide", Description: "d", Caption: "c", Dialogue: []store.Line{{Character: "a", Text: "b"}}}}}
	same := pipeline.PageSpec{Title: "T", Panels: []store.Panel{{Shot: "wide", Description: "d", Caption: "c", Dialogue: []store.Line{{Character: "a", Text: "b"}}}}}
	if !samePlan(old, same) {
		t.Fatal("identical plans should match")
	}
	diff := same
	diff.Panels = []store.Panel{{Shot: "wide", Description: "d", Caption: "c", Dialogue: []store.Line{{Character: "a", Text: "changed"}}}}
	if samePlan(old, diff) {
		t.Fatal("different dialogue should not match")
	}
	diff.Title = "Other"
	if samePlan(old, diff) {
		t.Fatal("different title should not match")
	}

	// Errors.
	_ = e.r.AdjustPage(e.sto.ID, 4242, "x")
	e.mustFail("adjust missing page", "not found")
	e.ai.failChat = true
	_ = e.r.Breakdown(e.sto.ID)
	e.mustFail("breakdown chat down", "chat down")
	_ = e.r.AdjustPages(e.sto.ID, "x")
	e.mustFail("adjust pages chat down", "chat down")
	_ = e.r.AdjustPage(e.sto.ID, pages[0].ID, "x")
	e.mustFail("adjust page chat down", "chat down")
}

func TestRenderPagesAndFailures(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("draw")
	_ = e.r.Breakdown(e.sto.ID)
	e.mustDone("breakdown")

	e.ai.failImage = true
	_ = e.r.RenderAll(e.sto.ID)
	e.mustFail("render", "image down")
	sto, _ := e.st.Story(e.ctx, e.sto.ID)
	if sto.Step != store.StepBook {
		t.Fatal("render moves the story to the book step even when drawing fails")
	}
	for _, p := range e.pages() {
		if p.ImageStatus != store.ImageError {
			t.Fatalf("failed pages should carry the error: %+v", p)
		}
	}
	e.ai.failImage = false
	pages := e.pages()
	if err := e.r.RenderPage(e.sto.ID, pages[0].ID, "more drama"); err != nil {
		t.Fatal(err)
	}
	e.mustDone("render page")
	p0, _ := e.st.Page(e.ctx, pages[0].ID)
	if p0.ImageStatus != store.ImageReady {
		t.Fatalf("render page: %+v", p0)
	}
	_ = e.r.RenderAll(e.sto.ID)
	e.mustDone("render rest")
	for _, p := range e.pages() {
		if p.ImageStatus != store.ImageReady {
			t.Fatalf("all pages should be drawn: %+v", p)
		}
	}
	// Nothing to draw: completes immediately.
	_ = e.r.RenderAll(e.sto.ID)
	e.mustDone("render nothing")
	_ = e.r.RenderPage(e.sto.ID, 4242, "")
	e.mustFail("render missing page", "not found")
	e.ai.failImage = true
	_ = e.r.RenderPage(e.sto.ID, pages[0].ID, "")
	e.mustFail("render page image down", "image down")
}

func TestReferencesOnlyReadyOnes(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	chars := e.chars()
	refs, err := e.r.sheets(e.ctx, chars)
	if err != nil || len(refs) != 0 {
		t.Fatalf("no ready sheets: %v %v", refs, err)
	}
	pr, rows, err := e.r.references(e.ctx, e.sto.ID, chars[0].ID)
	if err != nil || len(pr) != 0 || len(rows) != 0 {
		t.Fatal("no references yet")
	}
	if refPNGs(nil) != nil {
		t.Fatal("refPNGs of nothing is nil")
	}
	// A ref whose file vanished is skipped, not fatal.
	r, _ := e.st.InsertRef(e.ctx, e.sto.ID, chars[0].ID, "a.png", "", tinyPNG(), 1600)
	_ = e.st.Blobs().Delete(e.ctx, r.Image)
	pr, _, err = e.r.references(e.ctx, e.sto.ID, chars[0].ID)
	if err != nil || len(pr) != 0 {
		t.Fatalf("missing file should be skipped: %v %v", pr, err)
	}
}

func TestConcurrencyLimitDefaultsToOne(t *testing.T) {
	r := New(nil, &pipeline.Fake{}, -3)
	if cap(r.imgSem) != 1 {
		t.Fatalf("images limit should default to 1, got %d", cap(r.imgSem))
	}
	if Elapsed(&store.Job{CreatedAt: time.Now().Add(-2 * time.Second)}) < time.Second {
		t.Fatal("Elapsed should measure since creation")
	}
}

// blocking holds every model call until released.
type blocking struct {
	pipeline.AI
	gate chan struct{}
}

func (b *blocking) ChatJSON(ctx context.Context, system, user string, images []pipeline.Image, schemaName string, schema map[string]any, out any) error {
	<-b.gate
	return b.AI.ChatJSON(ctx, system, user, images, schemaName, schema, out)
}

func TestQueueSchedulesByLock(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	chars := e.chars()
	a, b, c := chars[0], chars[1], chars[2]
	gate := make(chan struct{})
	e.r.ai = &blocking{AI: e.ai, gate: gate}

	// Two characters run side by side; a second change to A waits behind
	// the first; a story-level job waits behind everything and blocks C.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.r.Revise(e.sto.ID, a.ID, "older", false, false))
	must(e.r.Revise(e.sto.ID, b.ID, "younger", false, false))
	must(e.r.Revise(e.sto.ID, a.ID, "and taller", false, false))
	must(e.r.DrawSheets(e.sto.ID))
	must(e.r.Revise(e.sto.ID, c.ID, "later", false, false))

	// Cards show the running job first: A and B are running (A also has a
	// queued change behind it), C waits behind the story job.
	perChar, _ := e.st.LatestCharacterJobs(e.ctx, e.sto.ID)
	if !perChar[a.ID].Running() || !perChar[b.ID].Running() || !perChar[c.ID].Queued() {
		t.Fatalf("a running, b running, c queued behind the story job: %+v", perChar)
	}
	if !e.r.CharacterBusy(e.sto.ID, a.ID) {
		t.Fatal("A has work queued")
	}
	if j, _ := e.st.LatestJob(e.ctx, e.sto.ID); !j.Queued() {
		t.Fatalf("story job should be queued: %+v", j)
	}
	if e.r.Busy(e.sto.ID) || !e.r.Working(e.sto.ID) || !e.r.CharacterBusy(e.sto.ID, a.ID) || !e.r.CharacterBusy(e.sto.ID, c.ID) {
		t.Fatal("lock state: story not running but working; a and c busy")
	}
	if running, _ := e.st.AnyRunning(e.ctx, e.sto.ID); !running {
		t.Fatal("queued work counts as running for the UI")
	}
	close(gate)
	e.r.Wait()
	if e.r.Working(e.sto.ID) || e.r.CharacterBusy(e.sto.ID, a.ID) {
		t.Fatal("everything should be released")
	}
	// The fake rewrites the wardrobe on every revision, so the last queued
	// change is the one that shows (ordering was asserted above).
	got, _ := e.st.Character(e.ctx, a.ID)
	if !strings.Contains(got.Wardrobe, "revised: and taller") {
		t.Fatalf("the second change to A ran after the first: %+v", got)
	}
	got, _ = e.st.Character(e.ctx, c.ID)
	if !strings.Contains(got.Wardrobe, "revised: later") {
		t.Fatalf("C's change ran after the story job: %+v", got)
	}
	if !AllReady(e.chars()) {
		t.Fatal("the queued story job drew every sheet")
	}
	jobs, _ := e.st.LatestCharacterJobs(e.ctx, e.sto.ID)
	for id, j := range jobs {
		if j.Status != store.JobDone {
			t.Fatalf("character %d job: %+v", id, j)
		}
	}
}

func TestQueuedEditsKeepTheirOrder(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	a := e.chars()[0]
	gate := make(chan struct{})
	e.r.ai = &blocking{AI: e.ai, gate: gate}
	// A revision is running; a hand edit queued behind it must win.
	if err := e.r.Revise(e.sto.ID, a.ID, "older", false, false); err != nil {
		t.Fatal(err)
	}
	if err := e.r.Edit(e.sto.ID, a.ID, Fields{Name: "Mara Quinn", Visual: "hand-written", Wardrobe: "cape", Redraw: true}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	e.r.Wait()
	got, _ := e.st.Character(e.ctx, a.ID)
	if got.Name != "Mara Quinn" || got.Visual != "hand-written" || got.Wardrobe != "cape" || got.SheetStatus != store.ImagePending {
		t.Fatalf("the edit should be applied last and draw nothing in the casting phase: %+v", got)
	}
	// With a sheet in place, an edit that changes the look redraws.
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("draw")
	before, _ := e.st.Character(e.ctx, a.ID)
	_ = e.r.Edit(e.sto.ID, a.ID, Fields{Name: "Mara Quinn", Visual: "hand-written again", Wardrobe: "cape", Redraw: true})
	e.r.Wait()
	after, _ := e.st.Character(e.ctx, a.ID)
	if after.SheetImage == before.SheetImage || after.SheetStatus != store.ImageReady {
		t.Fatal("look change with redraw should produce a new sheet")
	}
	_ = e.r.Edit(e.sto.ID, a.ID, Fields{Name: "Mara Quinn", Visual: "hand-written again", Wardrobe: "cape", Personality: "wry", Redraw: true})
	e.r.Wait()
	same, _ := e.st.Character(e.ctx, a.ID)
	if same.SheetImage != after.SheetImage || same.Personality != "wry" {
		t.Fatal("a words-only edit keeps the sheet")
	}
	_ = e.r.Edit(e.sto.ID, 4242, Fields{Name: "x"})
	e.mustFail("edit missing", "not found")
}

func TestLinkAndSetSheetAreQueued(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	u, _ := e.st.UserByUsername(e.ctx, "w")
	other, _ := e.st.CreateStory(e.ctx, u.ID, "Other", script, "", "noir")
	src := &store.Character{StoryID: other.ID, Name: "Mara", Visual: "from elsewhere"}
	_ = e.st.InsertCharacter(e.ctx, src)
	name, _ := e.st.SaveImage(e.ctx, other.ID, "png", tinyPNG())
	_ = e.st.SetCharacterSheet(e.ctx, src.ID, name, store.ImageReady, "")
	a := e.chars()[0]
	if err := e.r.Link(e.sto.ID, a.ID, src.ID); err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	got, _ := e.st.Character(e.ctx, a.ID)
	if got.Visual != "from elsewhere" || got.SheetStatus != store.ImageReady || got.OriginID != src.ID {
		t.Fatalf("link: %+v", got)
	}
	_ = e.r.Link(e.sto.ID, a.ID, 4242)
	e.mustFail("link missing source", "not found")
	_ = e.r.Link(e.sto.ID, 4242, src.ID)
	e.mustFail("link missing target", "not found")

	b := e.chars()[1]
	sheet, _ := e.st.SaveImage(e.ctx, e.sto.ID, "png", tinyPNG())
	if err := e.r.SetSheet(e.sto.ID, b.ID, sheet); err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	got, _ = e.st.Character(e.ctx, b.ID)
	if got.SheetImage != sheet || got.SheetStatus != store.ImageReady || !strings.Contains(got.Visual, "matched to 1 reference image") {
		t.Fatalf("set sheet: %+v", got)
	}
	_ = e.r.SetSheet(e.sto.ID, 4242, sheet)
	e.mustFail("set sheet missing", "not found")
}

// noCasting wraps the fake and fails only the voice-casting call.
type noCasting struct{ pipeline.AI }

func (n noCasting) ChatJSON(ctx context.Context, system, user string, images []pipeline.Image, schemaName string, schema map[string]any, out any) error {
	if schemaName == "voices" {
		return errors.New("casting down")
	}
	return n.AI.ChatJSON(ctx, system, user, images, schemaName, schema, out)
}

func TestAnalyzeCastsVoices(t *testing.T) {
	e := newEnv(t)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	sto, _ := e.st.Story(e.ctx, e.sto.ID)
	if _, ok := pipeline.VoiceByID(sto.NarratorVoice); !ok {
		t.Fatalf("narrator not cast: %q", sto.NarratorVoice)
	}
	seen := map[string]string{sto.NarratorVoice: "narrator"}
	for _, c := range e.chars() {
		if _, ok := pipeline.VoiceByID(c.Voice); !ok {
			t.Fatalf("%s has no voice: %q", c.Name, c.Voice)
		}
		if who, dup := seen[c.Voice]; dup {
			t.Fatalf("%s and %s share a voice", c.Name, who)
		}
		seen[c.Voice] = c.Name
	}

	// A pick by hand survives a cast revision; a newcomer gets a voice.
	mara := e.chars()[0]
	_ = e.st.SetCharacterVoice(e.ctx, mara.ID, "pqHfZKP75CvOlQylNhV4")
	newcomer := &store.Character{StoryID: e.sto.ID, Name: "Innkeeper", Position: 9}
	_ = e.st.InsertCharacter(e.ctx, newcomer)
	_ = e.r.AdjustCast(e.sto.ID, "keep everyone")
	e.mustDone("adjust")
	if got, _ := e.st.Character(e.ctx, mara.ID); got.Voice != "pqHfZKP75CvOlQylNhV4" {
		t.Fatalf("the picked voice was replaced: %q", got.Voice)
	}
	for _, c := range e.chars() {
		if c.Voice == "" {
			t.Fatalf("%s left without a voice after the cast changed", c.Name)
		}
	}
}

func TestCastingFailureDoesNotFailTheRead(t *testing.T) {
	e := newEnv(t)
	e.r = New(e.st, noCasting{&pipeline.Fake{}}, 0)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze without casting")
	chars := e.chars()
	if len(chars) == 0 {
		t.Fatal("no cast")
	}
	for _, c := range chars {
		if c.Voice != "" || pipeline.CharacterVoice(c).ID == "" {
			t.Fatalf("%s: uncast characters keep the fallback voice: %+v", c.Name, c)
		}
	}
}

func TestCastVoicesJob(t *testing.T) {
	e := newEnv(t)
	e.r = New(e.st, noCasting{&pipeline.Fake{}}, 0)
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	// Casting was down during the read; asking again surfaces the failure.
	_ = e.r.CastVoices(e.sto.ID)
	e.mustFail("cast voices", "casting down")
	// Once the model answers, everyone and the narrator get a voice.
	e.r = New(e.st, &pipeline.Fake{}, 0)
	_ = e.r.CastVoices(e.sto.ID)
	e.mustDone("cast voices")
	if pipeline.Uncast(e.chars()) {
		t.Fatalf("still uncast: %+v", e.chars())
	}
	if st, _ := e.st.Story(e.ctx, e.sto.ID); st.NarratorVoice != pipeline.DefaultNarrator {
		t.Fatalf("narrator: %q", st.NarratorVoice)
	}
}

// countingVoice wraps the offline tones and counts syntheses per voice.
type countingVoice struct {
	mu    sync.Mutex
	calls map[string]int
	fail  bool
}

func (c *countingVoice) Speak(ctx context.Context, text, voice string) (*pipeline.Speech, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[voice]++
	fail := c.fail
	c.mu.Unlock()
	if fail {
		return nil, errors.New("voice down")
	}
	return pipeline.FakeVoice{}.Speak(ctx, text, voice)
}

func (c *countingVoice) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

func (e *env) drawnBook() {
	e.t.Helper()
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("draw")
	_ = e.r.Breakdown(e.sto.ID)
	e.mustDone("breakdown")
	_ = e.r.RenderAll(e.sto.ID)
	e.wait() // drawing, then the chained narration (which callers check)
}

func TestNarrateAfterDrawing(t *testing.T) {
	e := newEnv(t)
	voice := &countingVoice{}
	e.r.Voice = voice
	e.drawnBook()
	if j := e.job(); j.Kind != "narrate" || j.Status != store.JobDone {
		t.Fatalf("drawing the book should chain the narration: %+v", j)
	}
	sto, _ := e.st.Story(e.ctx, e.sto.ID)
	chars := e.chars()
	lines, _ := e.st.StoryLines(e.ctx, e.sto.ID)
	spoken := 0
	for _, p := range e.pages() {
		if !NarrationReady(sto, chars, p, lines[p.ID]) {
			t.Fatalf("page %d not ready: %+v", p.Number, p)
		}
		if len(lines[p.ID]) == 0 {
			t.Fatalf("page %d has no lines", p.Number)
		}
		for _, l := range lines[p.ID] {
			if !l.Exact || l.Audio == "" || l.Voice != VoiceFor(sto, chars, l.Speaker).ID {
				t.Fatalf("line: %+v", l)
			}
			if l.Kind == "bubble" && l.Speaker == "" {
				t.Fatalf("a bubble lost its speaker: %+v", l)
			}
			for i, w := range l.Words {
				if w.End <= w.Start || (i > 0 && w.Start < l.Words[i-1].Start) {
					t.Fatalf("timings: %+v", l.Words)
				}
			}
			if b, err := e.st.ReadImage(e.ctx, l.Audio); err != nil || string(b[:4]) != "RIFF" {
				t.Fatalf("clip %s: %v", l.Audio, err)
			}
			spoken++
		}
	}
	if voice.total() != spoken {
		t.Fatalf("%d syntheses for %d lines", voice.total(), spoken)
	}

	// Nothing changed: nothing is read or voiced again.
	_ = e.r.Narrate(e.sto.ID)
	e.mustDone("narrate again")
	if voice.total() != spoken {
		t.Fatalf("re-narrating an unchanged book cost %d syntheses", voice.total()-spoken)
	}

	// A new voice for one character: only their lines are voiced again,
	// and the old clips are reclaimed.
	var who *store.Character
	var theirs int
	for _, c := range chars {
		n := 0
		for _, ls := range lines {
			for _, l := range ls {
				if l.Speaker == c.Name {
					n++
				}
			}
		}
		if n > 0 {
			who, theirs = c, n
			break
		}
	}
	oldClip := ""
	for _, ls := range lines {
		for _, l := range ls {
			if l.Speaker == who.Name {
				oldClip = l.Audio
			}
		}
	}
	_ = e.st.SetCharacterVoice(e.ctx, who.ID, "pqHfZKP75CvOlQylNhV4")
	_ = e.r.Narrate(e.sto.ID)
	e.mustDone("revoice")
	if got := voice.total() - spoken; got != theirs {
		t.Fatalf("revoicing %s took %d syntheses, want %d", who.Name, got, theirs)
	}
	if _, err := e.st.ReadImage(e.ctx, oldClip); err == nil {
		t.Fatal("the replaced clip should be swept")
	}

	// A redrawn page is read again; the others are not.
	before := voice.total()
	p0 := e.pages()[0]
	_ = e.r.RenderPage(e.sto.ID, p0.ID, "brighter")
	e.mustDone("redraw")
	if p, _ := e.st.Page(e.ctx, p0.ID); p.ReadingImage == p.Image {
		t.Fatal("a redrawn page's reading is stale")
	}
	_ = e.r.Narrate(e.sto.ID)
	e.mustDone("narrate redrawn")
	if got := voice.total() - before; got != len(lines[p0.ID]) {
		t.Fatalf("re-reading one page took %d syntheses, want %d", got, len(lines[p0.ID]))
	}
}

func TestNarrateFailures(t *testing.T) {
	e := newEnv(t)
	voice := &countingVoice{fail: true}
	e.r.Voice = voice
	e.drawnBook()
	// Drawing succeeded; the chained narration failed and says why, per page.
	if j := e.job(); j.Kind != "narrate" || j.Status != store.JobError || !strings.Contains(j.Error, "voice down") {
		t.Fatalf("narrate job: %+v", j)
	}
	for _, p := range e.pages() {
		if p.ImageStatus != store.ImageReady || p.ReadingError == "" || p.ReadingImage != p.Image {
			t.Fatalf("page: %+v", p)
		}
	}
	// The reading is kept; only the voicing is retried.
	voice.fail = false
	_ = e.r.Narrate(e.sto.ID)
	e.mustDone("retry")
	for _, p := range e.pages() {
		if p.ReadingError != "" {
			t.Fatalf("error not cleared: %+v", p)
		}
	}
	// A reading that fails is recorded too.
	e2 := newEnv(t)
	e2.drawnBook()
	p := e2.pages()[0]
	_ = e2.r.RenderPage(e2.sto.ID, p.ID, "")
	e2.mustDone("redraw")
	e2.ai.failChat = true
	_ = e2.r.Narrate(e2.sto.ID)
	e2.mustFail("narrate", "chat down")
	if got, _ := e2.st.Page(e2.ctx, p.ID); !strings.Contains(got.ReadingError, "chat down") {
		t.Fatalf("reading error: %+v", got)
	}
	// No drawn pages: nothing to do.
	e3 := newEnv(t)
	_ = e3.r.Narrate(e3.sto.ID)
	e3.mustDone("narrate empty")
}
