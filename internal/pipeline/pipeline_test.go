package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/store"
)

func story() *store.Story {
	return &store.Story{ID: 1, Title: "The Lighthouse", Logline: "log", World: "a foggy harbour", Style: "storybook", Script: "MARA: Hello?\nPIP: Please don't scream."}
}

func mara() *store.Character {
	return &store.Character{ID: 7, Name: "Mara", Role: "protagonist", Age: "9", Visual: "freckles", Wardrobe: "yellow raincoat", Items: "compass", Personality: "brave"}
}

func TestStyles(t *testing.T) {
	if len(Styles) < 5 {
		t.Fatal("expected a handful of presets")
	}
	if StyleBySlug("noir").Name != "Noir" || StyleBySlug("nope").Slug != Styles[0].Slug {
		t.Fatal("StyleBySlug should find a slug and fall back to the first preset")
	}
}

func TestSuggestPages(t *testing.T) {
	if n := suggestPages("a few words"); n != 3 {
		t.Fatalf("min 3, got %d", n)
	}
	if n := suggestPages(strings.Repeat("word ", 600)); n != 5 {
		t.Fatalf("600 words ~ 5 pages, got %d", n)
	}
	if n := suggestPages(strings.Repeat("word ", 5000)); n != 16 {
		t.Fatalf("max 16, got %d", n)
	}
}

func TestSheetPrompt(t *testing.T) {
	p := SheetPrompt(story(), mara(), 0)
	for _, want := range []string{"Character reference sheet", "Mara", "freckles", "yellow raincoat", "compass", "watercolor", "a foggy harbour", "turnaround"} {
		if !strings.Contains(p, want) {
			t.Fatalf("sheet prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "attached image") {
		t.Fatal("no reference sentence without references")
	}
	p = SheetPrompt(story(), mara(), 2)
	if !strings.HasPrefix(p, "The 2 attached image(s) are the writer's references") {
		t.Fatalf("reference lead missing:\n%s", p)
	}
}

func TestPagePrompt(t *testing.T) {
	page := &store.Page{Number: 1, Title: "The Climb", Panels: []store.Panel{
		{Number: 1, Shot: "wide", Description: "Mara climbs", Characters: []string{"Mara"}, Caption: "Rain hammered.", Dialogue: []store.Line{{Character: "Mara", Text: "Hello?"}}},
		{Number: 2, Shot: "close-up", Description: "She listens"},
	}}
	p := PagePrompt(story(), []*store.Character{mara()}, page, "")
	for _, want := range []string{"2 panels", `story title "The Lighthouse"`, "Reference image 1 = Mara", "PANEL 1 (wide shot): Mara climbs", "Characters present: Mara.", `Caption box: "Rain hammered."`, `Speech bubble from Mara: "Hello?"`, "PANEL 2 (close-up shot)", "Letter every speech bubble"} {
		if !strings.Contains(p, want) {
			t.Fatalf("page prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "interior page") || strings.Contains(p, "Revision notes") {
		t.Fatal("page 1 is the opening page and has no revision notes")
	}
	page.Number = 2
	p = PagePrompt(story(), nil, page, "less rain")
	if !strings.Contains(p, "This is page 2, an interior page: no title") || !strings.Contains(p, "Revision notes from the writer, apply them: less rain") {
		t.Fatalf("interior page prompt:\n%s", p)
	}
	if strings.Contains(p, "attached reference images are the character sheets") {
		t.Fatal("no sheet sentence without cast")
	}
}

func TestReferenceImages(t *testing.T) {
	imgs := referenceImages([]Reference{{Filename: "coat.png", Note: "the coat", PNG: []byte("x")}, {PNG: []byte("y")}})
	if len(imgs) != 2 || imgs[0].Label != "Reference image 1 (file: coat.png): the coat" || imgs[1].Label != "Reference image 2" {
		t.Fatalf("labels: %+v", imgs)
	}
}

// recorder captures what the pipeline hands to the model and answers with
// canned JSON.
type recorder struct {
	system, user, schema string
	images               []Image
	answer               string
	err                  error
}

func (r *recorder) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	r.system, r.user, r.schema, r.images = system, user, schemaName, images
	if r.err != nil {
		return r.err
	}
	return (&Fake{}).ChatJSON(ctx, system, user, images, schemaName, schema, out)
}
func (r *recorder) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	return nil, nil
}
func (r *recorder) EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error) {
	return nil, nil
}

func TestTextStepsBuildTheRightPrompts(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	st := story()
	an, err := Analyze(ctx, r, st.Script, StyleBySlug("noir"))
	if err != nil || len(an.Characters) == 0 {
		t.Fatalf("analyze: %+v %v", an, err)
	}
	if r.schema != "analysis" || !strings.Contains(r.user, "noir") || !strings.Contains(r.user, "SCRIPT:") || !strings.Contains(r.system, "natural capitalization") {
		t.Fatalf("analyze prompt: %q %q", r.schema, r.user)
	}

	specs, err := AdjustCharacters(ctx, r, st, []*store.Character{mara()}, "make her older")
	if err != nil || len(specs) == 0 {
		t.Fatalf("adjust cast: %v", err)
	}
	if r.schema != "characters" || !strings.Contains(r.user, "CURRENT CAST:") || !strings.Contains(r.user, "make her older") || !strings.Contains(r.user, "- Mara (protagonist, 9)") {
		t.Fatalf("adjust cast prompt: %q", r.user)
	}

	spec, err := AdjustCharacter(ctx, r, st, mara(), "", []Reference{{Filename: "a.png", PNG: []byte("x")}})
	if err != nil || spec.Name != "Mara" {
		t.Fatalf("adjust one: %+v %v", spec, err)
	}
	if !strings.Contains(r.user, "(no written notes; match the attached reference images)") || !strings.Contains(r.user, "attached 1 reference image(s)") || len(r.images) != 1 {
		t.Fatalf("adjust one prompt: %q images=%d", r.user, len(r.images))
	}
	spec, _ = AdjustCharacter(ctx, r, st, mara(), "shorter hair", nil)
	if strings.Contains(r.user, "attached") || !strings.Contains(r.user, "shorter hair") {
		t.Fatalf("adjust one without references: %q", r.user)
	}

	pages, err := Breakdown(ctx, r, st, []*store.Character{mara()})
	if err != nil || len(pages) == 0 || pages[0].Number != 1 || pages[0].Panels[0].Number != 1 {
		t.Fatalf("breakdown: %+v %v", pages, err)
	}
	if r.schema != "pages" || !strings.Contains(r.user, "Aim for about 3 pages") || !strings.Contains(r.user, "CAST:") {
		t.Fatalf("breakdown prompt: %q", r.user)
	}

	existing := []*store.Page{{Number: 1, Title: "One", Summary: "s", Panels: []store.Panel{{Number: 1, Shot: "wide", Description: "d", Caption: "c", Dialogue: []store.Line{{Character: "Mara", Text: "hi"}}}}}}
	pages, err = AdjustBreakdown(ctx, r, st, []*store.Character{mara()}, existing, "make it longer")
	if err != nil || len(pages) == 0 {
		t.Fatalf("adjust breakdown: %v", err)
	}
	if !strings.Contains(r.user, "CURRENT PAGES:") || !strings.Contains(r.user, `caption: "c"`) || !strings.Contains(r.user, `Mara: "hi"`) || !strings.Contains(r.user, "make it longer") {
		t.Fatalf("adjust breakdown prompt: %q", r.user)
	}

	page, err := AdjustPage(ctx, r, st, []*store.Character{mara()}, existing[0], "split panel 1")
	if err != nil || page.Number != 1 {
		t.Fatalf("adjust page: %+v %v", page, err)
	}
	if r.schema != "page" || !strings.Contains(r.user, "PAGE 1:") || !strings.Contains(r.user, "split panel 1") {
		t.Fatalf("adjust page prompt: %q", r.user)
	}
}

func TestTextStepsPropagateErrors(t *testing.T) {
	ctx := context.Background()
	r := &recorder{err: errors.New("boom")}
	st := story()
	if _, err := Analyze(ctx, r, "s", Styles[0]); err == nil {
		t.Fatal("analyze error")
	}
	if _, err := AdjustCharacters(ctx, r, st, nil, "f"); err == nil {
		t.Fatal("adjust cast error")
	}
	if _, err := AdjustCharacter(ctx, r, st, mara(), "f", nil); err == nil {
		t.Fatal("adjust one error")
	}
	if _, err := Breakdown(ctx, r, st, nil); err == nil {
		t.Fatal("breakdown error")
	}
	if _, err := AdjustBreakdown(ctx, r, st, nil, nil, "f"); err == nil {
		t.Fatal("adjust breakdown error")
	}
	if _, err := AdjustPage(ctx, r, st, nil, &store.Page{Number: 1}, "f"); err == nil {
		t.Fatal("adjust page error")
	}
}

// empty answers: the model returned valid JSON with nothing in it.
type empty struct{}

func (empty) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	return nil
}
func (empty) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	return nil, nil
}
func (empty) EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error) {
	return nil, nil
}

func TestTextStepsRejectEmptyAnswers(t *testing.T) {
	ctx := context.Background()
	st := story()
	if _, err := Analyze(ctx, empty{}, "s", Styles[0]); err == nil || !strings.Contains(err.Error(), "no characters") {
		t.Fatalf("analyze: %v", err)
	}
	if _, err := AdjustCharacters(ctx, empty{}, st, nil, "f"); err == nil || !strings.Contains(err.Error(), "empty cast") {
		t.Fatalf("adjust cast: %v", err)
	}
	if _, err := AdjustCharacter(ctx, empty{}, st, mara(), "f", nil); err == nil || !strings.Contains(err.Error(), "no character") {
		t.Fatalf("adjust one: %v", err)
	}
	if _, err := Breakdown(ctx, empty{}, st, nil); err == nil || !strings.Contains(err.Error(), "no pages") {
		t.Fatalf("breakdown: %v", err)
	}
	if _, err := AdjustBreakdown(ctx, empty{}, st, nil, nil, "f"); err == nil || !strings.Contains(err.Error(), "no pages") {
		t.Fatalf("adjust breakdown: %v", err)
	}
}
