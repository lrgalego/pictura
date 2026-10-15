package pipeline

import (
	"context"
	"encoding/json"
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

func TestPageRange(t *testing.T) {
	if lo, hi := pageRange("a few words"); lo != 3 || hi != 5 {
		t.Fatalf("a tiny script still gets 3 to 5 pages, got %d-%d", lo, hi)
	}
	// 140 short lines of dialogue: far more panels than 280 words suggest.
	chatty := strings.Repeat(`"Wait." Tom frowned. `, 70)
	if b := beats(chatty); b != 140 {
		t.Fatalf("every sentence and line is a beat, got %d", b)
	}
	if lo, hi := pageRange(chatty); lo != 4 || hi != 7 {
		t.Fatalf("140 beats ~ 4 to 7 pages, got %d-%d", lo, hi)
	}
	if lo, hi := pageRange("MARA: Hello?\nPIP: Please don't scream\n\n- Run!"); beats("MARA: Hello?\nPIP: Please don't scream\n\n- Run!") != 3 || lo != 3 || hi != 5 {
		t.Fatalf("lines without end punctuation count too, got %d-%d", lo, hi)
	}
	if lo, hi := pageRange(strings.Repeat("Word word. ", 5000)); lo != 24 || hi != 24 {
		t.Fatalf("capped at 24, got %d-%d", lo, hi)
	}
}

func TestPageCast(t *testing.T) {
	cast := []*store.Character{{Name: "Ash"}, {Name: "Tom"}, {Name: "Mr. Y"}, {Name: "Ms. Y"}}
	page := &store.Page{Panels: []store.Panel{
		{Characters: []string{"Mr. Y", "ash"}, Dialogue: []store.Line{{Character: "Mr. Y", Text: "Hi"}, {Character: "Voice", Text: "Boo"}}},
		{Characters: []string{"Mr. Y"}},
	}}
	got := PageCast(cast, page, 8)
	if len(got) != 2 || got[0].Name != "Mr. Y" || got[1].Name != "Ash" {
		t.Fatalf("the most present first, nobody absent: %+v", got)
	}
	if got := PageCast(cast, page, 1); len(got) != 1 || got[0].Name != "Mr. Y" {
		t.Fatalf("capped to the most present: %+v", got)
	}
	if got := PageCast(cast, &store.Page{}, 3); len(got) != 3 || got[0].Name != "Ash" {
		t.Fatalf("no names: the cast in order, capped: %+v", got)
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
	p = RestylePrompt(story(), mara(), 0)
	if !strings.HasPrefix(p, "The first attached image is this character's current model sheet") || strings.Contains(p, "image(s) after it") || !strings.Contains(p, "watercolor") {
		t.Fatalf("restyle prompt:\n%s", p)
	}
	if p = RestylePrompt(story(), mara(), 2); !strings.Contains(p, "The 2 image(s) after it are the writer's references") || !strings.Contains(p, "turnaround") {
		t.Fatalf("restyle prompt with references:\n%s", p)
	}
}

func TestStoryboardKeepsBlockingAndTransitions(t *testing.T) {
	props := panelSchema["properties"].(map[string]any)
	if _, ok := props["staging"]; !ok || !strings.Contains(strings.Join(panelSchema["required"].([]string), ","), "staging") {
		t.Fatal("every panel states its staging")
	}
	for _, want := range []string{"Keep the blocking", "stay held until the script frees them", "Scene changes:", "establishing panel of the place as they arrive", "drawn in a panel, not told in a caption"} {
		if !strings.Contains(breakdownPersona, want) {
			t.Fatalf("storyboard rules missing %q", want)
		}
	}
	for _, want := range []string{"blocking that drifts", "a scene change the reader cannot follow", "told in a caption instead of drawn"} {
		if !strings.Contains(reviewerPersona, want) {
			t.Fatalf("editor checks missing %q", want)
		}
	}
}

func TestPagePrompt(t *testing.T) {
	page := &store.Page{Number: 1, Title: "The Climb", Panels: []store.Panel{
		{Number: 1, Shot: "wide", Setting: "lighthouse stairs, storm light.", Description: "Mara climbs", Characters: []string{"Mara"}, Caption: "Rain hammered.", Dialogue: []store.Line{{Character: "Mara", Text: "Hello?"}, {Character: "Deep Voice", Text: "Go back."}}, SFX: "CRASH!"},
		{Number: 2, Shot: "close-up", Description: "She listens."},
	}}
	p := PagePrompt(story(), []*store.Character{mara()}, page, "")
	if strings.Contains(p, "Staging") {
		t.Fatal("no staging line for panels without one")
	}
	page.Panels[1].Staging = "two rowboats side by side: Mara in the left one."
	if got := PagePrompt(story(), []*store.Character{mara()}, page, ""); !strings.Contains(got, "PANEL 2 (close-up shot): Staging, the same in every panel that has it: two rowboats side by side: Mara in the left one. She listens.") {
		t.Fatalf("staging goes to the artist before the moment:\n%s", got)
	}
	if d := describePage(page); !strings.Contains(d, "(staging: two rowboats side by side") {
		t.Fatalf("revisions see the staging: %s", d)
	}
	for _, want := range []string{"2 panels", `story title "The Lighthouse"`, "Reference image 1 = Mara", "PANEL 1 (wide shot): Setting: lighthouse stairs, storm light. Mara climbs.", "Characters present, each drawn once: Mara.", `Caption box: "Rain hammered."`, `Speech bubble from Mara: "Hello?"`, `Speech bubble from Deep Voice, who is off-panel`, `Sound effect lettered into the art: "CRASH!"`, "PANEL 2 (close-up shot): She listens.\n", "each exactly once", "in the order listed"} {
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
	users, schemas       []string // every exchange, oldest first
}

func (r *recorder) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	r.system, r.user, r.schema, r.images = system, user, schemaName, images
	r.users, r.schemas = append(r.users, user), append(r.schemas, schemaName)
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
func (r *recorder) Segment(ctx context.Context, png []byte, phrase string) ([]Region, error) {
	return nil, nil
}

func TestTextStepsBuildTheRightPrompts(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	st := story()
	an, err := Analyze(ctx, r, st.Script)
	if err != nil || len(an.Characters) == 0 {
		t.Fatalf("analyze: %+v %v", an, err)
	}
	if r.schema != "analysis" || strings.Contains(r.user, "Art direction") || !strings.Contains(r.system, "never how they are drawn") || !strings.Contains(r.user, "SCRIPT:") || !strings.Contains(r.system, "natural capitalization") {
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
	n := len(r.schemas)
	if n < 2 || r.schemas[n-2] != "pages" || !strings.Contains(r.users[n-2], "between 3 and 5 pages") || !strings.Contains(r.users[n-2], "CAST:") {
		t.Fatalf("breakdown prompt: %q", r.users)
	}
	if r.schema != "review" || !strings.Contains(r.user, "SCRIPT:\nMARA: Hello?") || !strings.Contains(r.user, "DRAFT STORYBOARD (JSON):\n[{\"number\":1") {
		t.Fatalf("the draft goes to the editor with the script: %q", r.user)
	}

	existing := []*store.Page{{Number: 1, Title: "One", Summary: "s", Panels: []store.Panel{{Number: 1, Shot: "wide", Description: "d", Caption: "c", Dialogue: []store.Line{{Character: "Mara", Text: "hi"}}}, {Number: 2, Shot: "close-up", Setting: "the attic", Description: "e", SFX: "BANG!"}}}}
	pages, err = AdjustBreakdown(ctx, r, st, []*store.Character{mara()}, existing, "make it longer")
	if err != nil || len(pages) == 0 {
		t.Fatalf("adjust breakdown: %v", err)
	}
	if !strings.Contains(r.user, "CURRENT PAGES:") || !strings.Contains(r.user, `caption: "c"`) || !strings.Contains(r.user, `Mara: "hi"`) || !strings.Contains(r.user, "Panel 1 (wide): d") || !strings.Contains(r.user, `Panel 2 (close-up) at the attic: e [] sfx: "BANG!"`) || !strings.Contains(r.user, "make it longer") {
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

// editor plans one page, then answers the review as told.
type editor struct {
	empty
	problems []string
	err      error
}

func (e editor) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	var answer string
	switch schemaName {
	case "pages":
		answer = `{"pages":[{"number":1,"title":"Draft","panels":[{"number":1,"shot":"wide","description":"d"}]}]}`
	case "review":
		if e.err != nil {
			return e.err
		}
		problems, _ := json.Marshal(e.problems)
		answer = `{"problems":` + string(problems) + `,"pages":[{"number":7,"title":"Fixed","panels":[{"number":3,"shot":"wide","description":"d"},{"number":9,"shot":"close-up","description":"r"}]}]}`
	}
	return json.Unmarshal([]byte(answer), out)
}

func TestBreakdownReview(t *testing.T) {
	ctx := context.Background()
	pages, err := Breakdown(ctx, editor{problems: []string{"p1 panel 1: reaction before its cause"}}, story(), nil)
	if err != nil || len(pages) != 1 || pages[0].Title != "Fixed" || pages[0].Number != 1 || pages[0].Panels[1].Number != 2 {
		t.Fatalf("the editor's fixes replace the draft, renumbered: %+v %v", pages, err)
	}
	for _, e := range []editor{{}, {err: errors.New("boom")}} {
		pages, err = Breakdown(ctx, e, story(), nil)
		if err != nil || len(pages) != 1 || pages[0].Title != "Draft" {
			t.Fatalf("no problems found, or the review failed: the draft stands: %+v %v", pages, err)
		}
	}
}

func TestTextStepsPropagateErrors(t *testing.T) {
	ctx := context.Background()
	r := &recorder{err: errors.New("boom")}
	st := story()
	if _, err := Analyze(ctx, r, "s"); err == nil {
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
func (empty) Segment(ctx context.Context, png []byte, phrase string) ([]Region, error) {
	return nil, nil
}

func TestTextStepsRejectEmptyAnswers(t *testing.T) {
	ctx := context.Background()
	st := story()
	if _, err := Analyze(ctx, empty{}, "s"); err == nil || !strings.Contains(err.Error(), "no characters") {
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
