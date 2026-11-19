package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/store"
)

func farmhouse() *store.Element {
	return &store.Element{ID: 31, Kind: store.KindPlace, Name: "the farmhouse kitchen", Visual: "whitewashed stone walls, a red door", Layout: "the door on the left, the window over the sink on the right, the garden outside the window"}
}

func pie() *store.Element {
	return &store.Element{ID: 32, Kind: store.KindProp, Name: "Grandma's apple pie", Visual: "a golden lattice pie in a blue enamel dish", Layout: "as wide as two hands"}
}

func TestWorldTextSteps(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	st := story()
	specs, err := DesignWorld(ctx, r, st, []*store.Character{mara()})
	if err != nil || len(specs) != 2 || specs[0].Kind != store.KindPlace || specs[1].Kind != store.KindProp {
		t.Fatalf("design: %+v %v", specs, err)
	}
	if r.schema != "world" || !strings.Contains(r.user, "never list them):\nMara") || !strings.Contains(r.user, "SCRIPT:\n"+st.Script) || !strings.Contains(r.system, "never how they are drawn") {
		t.Fatalf("design prompt:\n%s", r.user)
	}

	world := []*store.Element{farmhouse(), pie()}
	if _, err := AdjustWorld(ctx, r, st, nil, world, "add the barn"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.user, "CAST (never list them):\n(none)") || !strings.Contains(r.user, "- the farmhouse kitchen (place)\n  Appearance: whitewashed") || !strings.Contains(r.user, "Size and use: as wide as two hands") || !strings.Contains(r.user, "WRITER'S FEEDBACK:\nadd the barn") {
		t.Fatalf("adjust world prompt:\n%s", r.user)
	}

	one, err := AdjustElement(ctx, r, st, farmhouse(), "make the door blue")
	if err != nil || one.Name != "the farmhouse kitchen" || one.Kind != store.KindPlace || !strings.Contains(one.Visual, "revised: make the door blue") {
		t.Fatalf("adjust element: %+v %v", one, err)
	}
	if !strings.Contains(r.user, "Return this one place revised") {
		t.Fatalf("adjust element prompt:\n%s", r.user)
	}
}

// canned returns a fixed JSON answer to every exchange.
type canned struct {
	empty
	json string
}

func (a canned) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	return jsonInto(a.json, out)
}

func TestWorldAnswersAreCleaned(t *testing.T) {
	ctx := context.Background()
	a := canned{json: `{"elements":[{"kind":"thing","name":" the lamp ","visual":"v","layout":"l"},{"kind":"place","name":"  ","visual":"","layout":""}]}`}
	specs, err := DesignWorld(ctx, a, story(), nil)
	if err != nil || len(specs) != 1 || specs[0].Name != "the lamp" || specs[0].Kind != store.KindProp {
		t.Fatalf("an unknown kind is a prop, a nameless element is dropped: %+v %v", specs, err)
	}
	if _, err := DesignWorld(ctx, empty{}, story(), nil); err == nil || !strings.Contains(err.Error(), "no places or props") {
		t.Fatalf("nothing found: %v", err)
	}
	if _, err := AdjustElement(ctx, empty{}, story(), pie(), "x"); err == nil || !strings.Contains(err.Error(), "no element") {
		t.Fatalf("nothing returned: %v", err)
	}
	boom := &recorder{err: errors.New("boom")}
	if _, err := AdjustWorld(ctx, boom, story(), nil, nil, "x"); err == nil {
		t.Fatal("a failed call fails")
	}
	if _, err := AdjustElement(ctx, boom, story(), pie(), "x"); err == nil {
		t.Fatal("a failed call fails")
	}
}

func TestElementPrompts(t *testing.T) {
	st := story()
	place := ElementPrompt(st, farmhouse())
	if !strings.Contains(place, "Environment concept art") || !strings.Contains(place, "top-down plan") || !strings.Contains(place, "Layout: the door on the left") || !strings.Contains(place, "No people or animals") || !strings.Contains(place, StyleBySlug(st.Style).Direction) {
		t.Fatalf("place:\n%s", place)
	}
	prop := ElementPrompt(st, pie())
	if !strings.Contains(prop, "Prop design sheet") || !strings.Contains(prop, "Size and use: as wide as two hands") || !strings.Contains(prop, "human silhouette for scale") {
		t.Fatalf("prop:\n%s", prop)
	}
	re := RestyleElementPrompt(st, pie())
	if !strings.HasPrefix(re, "The attached image is the current concept art of this prop") || !strings.Contains(re, "Prop design sheet") {
		t.Fatalf("restyle:\n%s", re)
	}
	refs := ElementRefs(pie(), []byte{1})
	if len(refs) != 1 || !strings.Contains(refs[0].Label, "Reference image 1: the current concept art of Grandma's apple pie") {
		t.Fatalf("refs: %+v", refs)
	}
}

func TestPageWorld(t *testing.T) {
	barn := &store.Element{ID: 33, Kind: store.KindPlace, Name: "the red barn"}
	lantern := &store.Element{ID: 34, Kind: store.KindProp, Name: "the lantern"}
	world := []*store.Element{pie(), farmhouse(), barn, lantern}
	page := &store.Page{Panels: []store.Panel{
		{Setting: "The farmhouse kitchen", Description: "Ash sniffs the pie on the sill."},
		{Setting: "the farmhouse kitchen", Props: []string{"grandma's apple pie"}},
		{Setting: "the garden", Description: "The fox eyes the window."},
	}}
	got := PageWorld(world, page, 4)
	if len(got) != 2 || got[0].ID != 31 || got[1].ID != 32 {
		t.Fatalf("the kitchen first, then the pie; not the barn or the lantern: %+v", got)
	}
	if got := PageWorld(world, page, 1); len(got) != 1 || got[0].ID != 31 {
		t.Fatalf("capped, places first: %+v", got)
	}
	// A storyboard from before the world: names in the words still count.
	old := &store.Page{Panels: []store.Panel{{Setting: "inside the red barn at night", Description: "Mara lifts the lantern."}}}
	if got := PageWorld(world, old, 4); len(got) != 2 || got[0].ID != 33 || got[1].ID != 34 {
		t.Fatalf("matched by words: %+v", got)
	}
	if PageWorld(nil, page, 4) != nil {
		t.Fatal("no world, nothing")
	}
}

func TestMentions(t *testing.T) {
	for _, c := range []struct {
		text, name string
		want       bool
	}{
		{"she cuts the pie", "Grandma's apple pie", true},
		{"a pied piper", "Grandma's apple pie", false}, // the noun must be a whole word
		{"the treehouse sways", "the house", false},
		{"the old house creaks", "the house", true},
		{"up the stairs", "the lighthouse stairs", true},
		{"anything", "the", false},
	} {
		if got := mentions(c.text, c.name); got != c.want {
			t.Errorf("mentions(%q, %q) = %v", c.text, c.name, got)
		}
	}
	if !samePlace("Inside the farmhouse kitchen.", "the farmhouse kitchen") || samePlace("", "x") || samePlace("the barn", "the farmhouse") {
		t.Fatal("samePlace")
	}
}

func TestPagePromptWithTheWorld(t *testing.T) {
	page := &store.Page{Number: 2, Panels: []store.Panel{
		{Number: 1, Shot: "wide", Setting: "the farmhouse kitchen", Description: "The pie cools on the sill.", Characters: []string{"Mara"}, Props: []string{"Grandma's apple pie"}},
		{Number: 2, Shot: "close-up", Setting: "inside the farmhouse kitchen", Description: "Mara smiles."},
		{Number: 3, Shot: "wide", Setting: "the garden", Description: "A fox waits."},
	}}
	world := []*store.Element{farmhouse(), pie()}
	p := PagePrompt(story(), []*store.Character{mara()}, world, page, "")
	for _, want := range []string{
		"Reference image 1 = Mara",
		"Reference image 2 = the farmhouse kitchen (place: whitewashed stone walls, a red door Layout: the door on the left",
		"Reference image 3 = Grandma's apple pie (prop: a golden lattice pie in a blue enamel dish).",
		"PANEL 1 (wide shot): Setting: the farmhouse kitchen (reference image 2).",
		"Props in the frame, as designed: Grandma's apple pie (reference image 3).",
		"Setting: inside the farmhouse kitchen (the farmhouse kitchen, reference image 2).",
		"Setting: the garden. A fox waits.",
		worldRule,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in:\n%s", want, p)
		}
	}
	if strings.Count(p, "Props in the frame") != 1 {
		t.Fatalf("only the panel with the pie lists it:\n%s", p)
	}
	if strings.Contains(PagePrompt(story(), nil, nil, page, ""), "PLACES AND PROPS") {
		t.Fatal("no world, no world rule")
	}

	refs := PageRefs([]*store.Character{mara()}, [][]byte{{1}}, world, [][]byte{{2}, {3}})
	if len(refs) != 3 || !strings.HasPrefix(refs[1].Label, "Reference image 2: the concept art of the farmhouse kitchen, a place") || !strings.HasPrefix(refs[2].Label, "Reference image 3: the design of Grandma's apple pie, a prop") {
		t.Fatalf("refs: %+v", refs)
	}
	if refs := PageRefs(nil, nil, world, [][]byte{{2}}); len(refs) != 1 || !strings.HasPrefix(refs[0].Label, "Reference image 1:") {
		t.Fatalf("art without its image is dropped: %+v", refs)
	}
}

func TestStoryboardKnowsTheWorld(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	st := story()
	world := []*store.Element{farmhouse(), pie()}
	if _, err := Breakdown(ctx, r, st, []*store.Character{mara()}, world); err != nil {
		t.Fatal(err)
	}
	for _, u := range r.users {
		if !strings.Contains(u, "PLACES AND PROPS (designed already; name them exactly like this):\n- the farmhouse kitchen (place)") {
			t.Fatalf("the planner and the editor both see the world:\n%s", u)
		}
	}
	if !strings.Contains(r.system, "listed place or prop") {
		t.Fatal("the editor checks the names")
	}
	pages := []*store.Page{{Number: 1, Panels: []store.Panel{{Number: 1, Description: "d", Props: []string{"Grandma's apple pie"}}}}}
	if _, err := AdjustBreakdown(ctx, r, st, nil, world, pages, "f"); err != nil || !strings.Contains(r.user, "PLACES AND PROPS") || !strings.Contains(r.user, "props: Grandma's apple pie") {
		t.Fatalf("adjust breakdown:\n%s", r.user)
	}
	if _, err := AdjustPage(ctx, r, st, nil, world, pages[0], "f"); err != nil || !strings.Contains(r.user, "PLACES AND PROPS") {
		t.Fatalf("adjust page:\n%s", r.user)
	}
	if _, err := Breakdown(ctx, r, st, nil, nil); err != nil || strings.Contains(r.user, "PLACES AND PROPS") {
		t.Fatal("no world, no list")
	}
}
