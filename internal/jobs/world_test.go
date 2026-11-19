package jobs

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

func (e *env) world() []*store.Element {
	w, _ := e.st.Elements(e.ctx, e.sto.ID)
	return w
}

// cast reads the script and draws every sheet.
func (e *env) cast() {
	e.t.Helper()
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("analyze")
	_ = e.r.DrawSheets(e.sto.ID)
	e.mustDone("sheets")
}

func TestDesignAndDrawTheWorld(t *testing.T) {
	e := newEnv(t)
	e.cast()
	if err := e.r.DesignWorld(e.sto.ID); err != nil {
		t.Fatal(err)
	}
	e.mustDone("design")
	w := e.world()
	if len(w) != 2 || !w[0].IsPlace() || w[1].IsPlace() || WorldStarted(w) || WorldReady(w) {
		t.Fatalf("a place then a prop, nothing drawn: %+v", w)
	}
	if !WorldReady(nil) {
		t.Fatal("no world is ready")
	}
	_ = e.r.DrawWorld(e.sto.ID)
	e.mustDone("draw")
	w = e.world()
	if !WorldReady(w) || w[0].ArtImage == "" {
		t.Fatalf("drawn: %+v", w)
	}
	// Reading the script again starts the world over.
	_ = e.r.Analyze(e.sto.ID)
	e.mustDone("re-read")
	if len(e.world()) != 0 {
		t.Fatal("a new reading drops the old world")
	}
}

func TestWorldArtFails(t *testing.T) {
	e := newEnv(t)
	e.cast()
	_ = e.r.DesignWorld(e.sto.ID)
	e.mustDone("design")
	e.ai.failImage = true
	_ = e.r.DrawWorld(e.sto.ID)
	e.mustFail("draw", "some concept art failed")
	if w := e.world(); w[0].ArtStatus != store.ImageError || !WorldStarted(w) {
		t.Fatalf("failed art says so: %+v", w[0])
	}
	e.ai.failChat = true
	_ = e.r.DesignWorld(e.sto.ID)
	e.mustFail("design", "chat down")
	if len(e.world()) != 2 {
		t.Fatal("a failed reading keeps the world it had")
	}
}

func TestAdjustTheWorld(t *testing.T) {
	e := newEnv(t)
	e.cast()
	_ = e.r.DesignWorld(e.sto.ID)
	e.mustDone("design")
	// Before any art, a revision only rewrites the words.
	_ = e.r.AdjustWorld(e.sto.ID, "make it foggier")
	e.mustDone("adjust before art")
	for _, el := range e.world() {
		if el.ArtStatus != store.ImagePending || !strings.Contains(el.Visual, "revised: make it foggier") {
			t.Fatalf("revised, not drawn: %+v", el)
		}
	}
	_ = e.r.DrawWorld(e.sto.ID)
	e.mustDone("draw")
	before := e.world()
	// A stray element the designer no longer returns is dropped.
	_ = e.st.InsertElement(e.ctx, &store.Element{StoryID: e.sto.ID, Kind: store.KindProp, Name: "the umbrella", Position: 9})
	_ = e.r.AdjustWorld(e.sto.ID, "brighter")
	e.mustDone("adjust after art")
	after := e.world()
	if len(after) != 2 {
		t.Fatalf("the umbrella is gone: %+v", after)
	}
	for i := range after {
		if after[i].ArtImage == before[i].ArtImage || after[i].ArtStatus != store.ImageReady {
			t.Fatalf("changed art is redrawn: %+v", after[i])
		}
	}
}

func TestReviseRedrawAndEditOneElement(t *testing.T) {
	e := newEnv(t)
	e.cast()
	_ = e.r.DesignWorld(e.sto.ID)
	e.mustDone("design")
	prop := e.world()[1]
	_ = e.r.ReviseElement(e.sto.ID, prop.ID, "dent it")
	e.mustDone("revise")
	got, _ := e.st.Element(e.ctx, prop.ID)
	if !strings.Contains(got.Visual, "revised: dent it") || got.ArtStatus != store.ImagePending {
		t.Fatalf("revised, still undrawn: %+v", got)
	}
	_ = e.r.RedrawElement(e.sto.ID, prop.ID)
	e.mustDone("redraw")
	got, _ = e.st.Element(e.ctx, prop.ID)
	if got.ArtStatus != store.ImageReady {
		t.Fatalf("drawn: %+v", got)
	}
	first := got.ArtImage
	_ = e.r.ReviseElement(e.sto.ID, prop.ID, "polish it")
	e.mustDone("revise drawn")
	if got, _ = e.st.Element(e.ctx, prop.ID); got.ArtImage == first {
		t.Fatal("a drawn element is redrawn when revised")
	}
	second := got.ArtImage
	_ = e.r.EditElement(e.sto.ID, prop.ID, ElementFields{Name: "the lamp", Visual: got.Visual, Layout: got.Layout, Redraw: true})
	e.mustDone("rename")
	if got, _ = e.st.Element(e.ctx, prop.ID); got.Name != "the lamp" || got.ArtImage != second {
		t.Fatalf("a rename keeps the art: %+v", got)
	}
	_ = e.r.EditElement(e.sto.ID, prop.ID, ElementFields{Name: "the lamp", Visual: "a tin lamp", Layout: got.Layout, Redraw: true})
	e.mustDone("edit looks")
	if got, _ = e.st.Element(e.ctx, prop.ID); got.Visual != "a tin lamp" || got.ArtImage == second {
		t.Fatalf("new looks are redrawn: %+v", got)
	}
	_ = e.r.RedrawElement(e.sto.ID, 4242)
	e.mustFail("missing", "not found")
	_ = e.r.ReviseElement(e.sto.ID, 4242, "x")
	e.mustFail("missing", "not found")
	_ = e.r.EditElement(e.sto.ID, 4242, ElementFields{Name: "x"})
	e.mustFail("missing", "not found")
}

// refsSeen records the labels of the reference images each page is drawn
// with.
type refsSeen struct {
	pipeline.AI
	mu     sync.Mutex
	labels [][]string
}

func (r *refsSeen) EditImage(ctx context.Context, prompt string, refs []pipeline.Image, size string) ([]byte, error) {
	var ls []string
	for _, i := range refs {
		ls = append(ls, i.Label)
	}
	r.mu.Lock()
	r.labels = append(r.labels, ls)
	r.mu.Unlock()
	return r.AI.EditImage(ctx, prompt, refs, size)
}

func TestPagesAreDrawnWithTheWorld(t *testing.T) {
	e := newEnv(t)
	e.cast()
	_ = e.r.DesignWorld(e.sto.ID)
	e.mustDone("design")
	_ = e.r.DrawWorld(e.sto.ID)
	e.mustDone("draw")
	_ = e.r.Breakdown(e.sto.ID)
	e.mustDone("breakdown")
	rec := &refsSeen{AI: e.ai}
	e.r.ai = rec
	_ = e.r.RenderPage(e.sto.ID, e.pages()[0].ID, "")
	e.mustDone("render")
	if len(rec.labels) != 1 {
		t.Fatalf("one page: %+v", rec.labels)
	}
	// Every fake panel is set at the harbour: its art rides along after
	// the sheets.
	ls := rec.labels[0]
	if last := ls[len(ls)-1]; !strings.Contains(last, "the concept art of the harbour at dusk, a place") {
		t.Fatalf("the harbour's art comes last: %q", ls)
	}

	// A new look redraws the world from its art too.
	before := e.world()
	_ = e.st.SetStyle(e.ctx, e.sto.ID, "manga")
	_ = e.r.Restyle(e.sto.ID, true)
	e.mustDone("restyle")
	for i, el := range e.world() {
		if el.ArtStatus != store.ImageReady || el.ArtImage == before[i].ArtImage {
			t.Fatalf("restyled: %+v", el)
		}
	}
	for _, p := range e.pages() {
		if p.ImageStatus == store.ImageReady {
			t.Fatal("pages go stale with the world")
		}
	}
}

func TestRestyleReportsWorldFailure(t *testing.T) {
	e := newEnv(t)
	_ = e.r.DesignWorld(e.sto.ID)
	e.mustDone("design")
	_ = e.r.DrawWorld(e.sto.ID)
	e.mustDone("draw")
	e.ai.failImage = true
	_ = e.r.Restyle(e.sto.ID, true)
	e.mustFail("restyle", "some concept art failed")
	e.ai.failImage = false
	// The retry redraws only what the failed restyle left behind.
	_ = e.r.Restyle(e.sto.ID, false)
	e.mustDone("retry")
	if !WorldReady(e.world()) {
		t.Fatal("redrawn on retry")
	}
}
