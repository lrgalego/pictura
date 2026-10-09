package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/store"
)

// fixture is a real drawn page (JPEG) with what Muse Spark and SAM 3.1
// answered about it, recorded once so the geometry is tested offline.
type fixture struct {
	Bubbles  [][4]int `json:"bubbles"`
	Captions [][4]int `json:"captions"`
	Reading  struct {
		Lines []sparkLine `json:"lines"`
	} `json:"reading"`
}

func loadFixture(t *testing.T, name string) (*image.Gray, fixture) {
	t.Helper()
	f, err := os.Open("testdata/lettering/" + name + ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	b, _ := os.ReadFile("testdata/lettering/" + name + ".json")
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	return toGray(img), fx
}

func regionsOf(fx fixture) []Region {
	var out []Region
	for _, b := range append(fx.Bubbles, fx.Captions...) {
		out = append(out, Region{b[0], b[1], b[2], b[3]})
	}
	return out
}

func inside(w, l Box) bool {
	const eps = 1e-9
	return w.X1 >= l.X1-eps && w.Y1 >= l.Y1-eps && w.X2 <= l.X2+eps && w.Y2 <= l.Y2+eps
}

func TestLayoutMeasuresRealPages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		texts []string
	}{
		{"storybook", []string{"The stomping stops!", "Sorry... me go...", "Watch your step, okay?", "Safe, sweet, and together.", "You were so brave.", "So were you.", "And a kiss for bravery and love.", "Yay! Kiss!", "Love wins!", "Safe, sweet, and together.", "The meadow blooms on in love."}},
		{"manga", []string{"Remember Turbo", "Easy! Just enable your turbo mode!", "Oh yeah! How did I forget that?!", "Logan takes a big breath...", "Enable Tuuuuurbo mode!!"}},
		{"meadow", []string{"Butterfree Love Story", "In a sunlit meadow, Buttermee and Butterfree lived peacefully.", "Morning, meadow!", "Share with all!", "Yummy pollen!", "Follow me, little wings.", "Steady now.", "Then... STOMP! STOMP!", "Grr. Outta my way!", "Eep! Flowers!"}},
	} {
		g, fx := loadFixture(t, tc.name)
		lines := layout(g, fx.Reading.Lines, regionsOf(fx), nil, &store.Page{})
		if len(lines) != len(tc.texts) {
			t.Fatalf("%s: %d lines", tc.name, len(lines))
		}
		for i, l := range lines {
			if l.Text != tc.texts[i] {
				t.Errorf("%s line %d: %q, want %q", tc.name, i, l.Text, tc.texts[i])
			}
			if !l.Exact {
				t.Errorf("%s: %q was not measured from the ink", tc.name, l.Text)
			}
			for j, w := range l.Words {
				if !inside(w.Box, l.Box) || w.Box.X2 <= w.Box.X1 || w.Box.Y2 <= w.Box.Y1 {
					t.Errorf("%s: word %q box %+v outside its line %+v", tc.name, w.Text, w.Box, l.Box)
				}
				if j > 0 && w.Box.Y1 < l.Words[j-1].Box.Y2 && w.Box.X1 < l.Words[j-1].Box.X1 {
					t.Errorf("%s: %q is not after %q in reading order", tc.name, w.Text, l.Words[j-1].Text)
				}
			}
		}
	}
	// Spot checks against positions measured by hand (pixels of 1280x1920).
	g, fx := loadFixture(t, "storybook")
	lines := layout(g, fx.Reading.Lines, regionsOf(fx), nil, &store.Page{})
	near := func(got Box, x1, y1, x2, y2 float64) bool {
		const tol = 8.0
		return math.Abs(got.X1*1280-x1) < tol && math.Abs(got.Y1*1920-y1) < tol && math.Abs(got.X2*1280-x2) < tol && math.Abs(got.Y2*1920-y2) < tol
	}
	if b := lines[6].Words[4].Box; lines[6].Words[4].Text != "bravery" || !near(b, 313, 971, 411, 1002) {
		t.Errorf("bravery at %.0f,%.0f %.0f,%.0f", b.X1*1280, b.Y1*1920, b.X2*1280, b.Y2*1920)
	}
	// Spark placed "Yay! Kiss!" ~100px above its bubble; it lands inside.
	if b := lines[7].Box; !near(b, 232, 1396, 401, 1505) {
		t.Errorf("Yay! Kiss! bubble %+v", b)
	}
}

// TestLayoutMultiRowBubbles is a page whose balloons run to five rows and
// whose tails reach far below the text. Spark grouped those rows wrongly
// and the old measure fell back to its drifting boxes; tail slivers made
// one-row words reach down into the tail.
func TestLayoutMultiRowBubbles(t *testing.T) {
	g, fx := loadFixture(t, "rows")
	lines := layout(g, fx.Reading.Lines, regionsOf(fx), nil, &store.Page{})
	if len(lines) != 8 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if !l.Exact {
			t.Errorf("%q was not measured from the ink", l.Text)
		}
	}
	word := func(li int, text string) Box {
		for _, w := range lines[li].Words {
			if w.Text == text {
				return w.Box
			}
		}
		t.Fatalf("line %d has no %q", li, text)
		return Box{}
	}
	near := func(name string, got Box, x1, y1, x2, y2 float64) {
		const tol = 8.0
		if math.Abs(got.X1*1280-x1) > tol || math.Abs(got.Y1*1920-y1) > tol || math.Abs(got.X2*1280-x2) > tol || math.Abs(got.Y2*1920-y2) > tol {
			t.Errorf("%s at %.0f,%.0f %.0f,%.0f, want %.0f,%.0f %.0f,%.0f", name, got.X1*1280, got.Y1*1920, got.X2*1280, got.Y2*1920, x1, y1, x2, y2)
		}
	}
	near("Mr.", word(0, "Mr."), 392, 63, 430, 85)
	near("remember", word(0, "remember"), 374, 130, 488, 153)
	near("go?", word(4, "go?"), 604, 777, 651, 810)
	near("Terribly.", word(5, "Terribly."), 1076, 756, 1195, 792)
	near("acquaintance.", word(6, "acquaintance."), 124, 1523, 281, 1551)
}

func TestLayWords(t *testing.T) {
	words := []sparkWord{{W: "I"}, {W: "said"}, {W: "nothing!"}}
	seg := func(x1, x2, row int) segment { return segment{x1, 0, x2, 20, row} }
	// Wide-set letters split "nothing!" in two, and a speck sits after it.
	got := layWords(words, []segment{seg(0, 10, 0), seg(30, 90, 0), seg(110, 150, 0), seg(156, 200, 0), seg(260, 262, 0)})
	if got == nil || got[2] != (Region{110, 0, 200, 20}) || got[0] != (Region{0, 0, 10, 20}) {
		t.Fatalf("split word, speck: %v", got)
	}
	// A tight space joins two words: they share the segment by length.
	got = layWords(words[1:], []segment{seg(0, 200, 0)})
	if got == nil || got[0].X2 > got[1].X1 || got[1].X2 != 200 {
		t.Fatalf("shared segment: %v", got)
	}
	// Unread ink in the row a word is in means the ink is not this line.
	five := []segment{seg(0, 60, 0), seg(100, 160, 0), seg(200, 260, 0), seg(300, 360, 0), seg(400, 460, 0)}
	if got := layWords([]sparkWord{{W: "was"}}, five); got != nil {
		t.Fatalf("too much ink left out: %v", got)
	}
	// A row left out whole (a tail's tip under the text) is fine.
	hello := []sparkWord{{"Hello", 0, 0, 50, 20}, {"there", 60, 0, 110, 20}}
	if got := layWords(hello, []segment{seg(0, 50, 0), seg(60, 110, 0), seg(0, 60, 1)}); got == nil || got[1] != (Region{60, 0, 110, 20}) {
		t.Fatalf("a whole stray row: %v", got)
	}
	// Spark's row breaks decide between equally long readings: "I" starts
	// the second row, so it is not the "?" left over at the end of the first.
	rows := []sparkWord{{"honestly?", 100, 0, 300, 20}, {"I", 80, 30, 90, 50}, {"was", 100, 30, 160, 50}}
	got = layWords(rows, []segment{seg(0, 180, 0), seg(190, 200, 0), seg(0, 70, 1)})
	if got == nil || got[0] != (Region{0, 0, 200, 20}) || got[1].X1 != 0 || got[2].X2 != 70 {
		t.Fatalf("row breaks: %v", got)
	}
	if layWords(words, nil) != nil {
		t.Fatal("no ink")
	}
}

// sliverPage is blank paper with one thin dark stroke inside where Spark
// read "SLAM!" (100..200 x 100..150 of 1000): ink that measures cleanly
// but is a stroke of one letter, as on outlined lettering over busy art.
func sliverPage() *image.Gray {
	g := image.NewGray(image.Rect(0, 0, 1000, 1000))
	for i := range g.Pix {
		g.Pix[i] = 255
	}
	for y := 105; y < 145; y++ {
		for x := 150; x < 156; x++ {
			g.Pix[y*1000+x] = 0
		}
	}
	return g
}

func TestArtLetteringNeverShrinksToASliver(t *testing.T) {
	sl := []sparkLine{{Kind: "sfx", Text: "SLAM!", Words: []sparkWord{{"SLAM!", 100, 100, 200, 150}}}}
	out := layout(sliverPage(), sl, nil, nil, &store.Page{})
	if len(out) != 1 || out[0].Exact {
		t.Fatalf("a sliver is not the lettering: %+v", out)
	}
	if b := out[0].Box; b.X1 > 0.1 || b.Y1 > 0.1 || b.X2 < 0.2 || b.Y2 < 0.15 {
		t.Fatalf("the line box must cover Spark's reading: %+v", b)
	}
	if b := out[0].Words[0].Box; b.X2-b.X1 < 0.09 {
		t.Fatalf("the word box must not be the sliver: %+v", b)
	}
	// Any kind: ink measured into much less than Spark's reading is dropped.
	sl[0].Kind = "bubble"
	if out := layout(sliverPage(), sl, nil, nil, &store.Page{}); out[0].Exact {
		t.Fatalf("a bubble's sliver: %+v", out[0])
	}
}

func TestRemeasureRepairsAStoredSliver(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, sliverPage()); err != nil {
		t.Fatal(err)
	}
	// The older measure kept the stroke as the word; the line box was right.
	stored := []LetterLine{{Kind: "sfx", Text: "SLAM!", Box: Box{0.1, 0.1, 0.2, 0.15}, Exact: true,
		Words: []LetterWord{{Text: "SLAM!", Box: Box{0.15, 0.105, 0.156, 0.145}}}}}
	out, err := Remeasure(context.Background(), empty{}, buf.Bytes(), stored)
	if err != nil || len(out) != 1 {
		t.Fatalf("remeasure: %+v %v", out, err)
	}
	if b := out[0].Box; b.X1 > 0.1 || b.X2 < 0.2 || b.Y1 > 0.1 || b.Y2 < 0.15 {
		t.Fatalf("line box shrank: %+v", b)
	}
	if b := out[0].Words[0].Box; b.X2-b.X1 < 0.09 || out[0].Text != "SLAM!" {
		t.Fatalf("word still a sliver: %+v", out[0].Words[0])
	}
	if _, err := Remeasure(context.Background(), empty{}, []byte("not a png"), stored); err == nil {
		t.Fatal("an unreadable page must fail")
	}
}

func TestMatchKeepsArtLetteringOutOfBubbles(t *testing.T) {
	lines := []sparkLine{
		{Kind: "sfx", Words: []sparkWord{{"SLAM!", 100, 100, 300, 160}}},
		{Kind: "sfx", Words: []sparkWord{{"BOOM!", 600, 600, 700, 640}}},
	}
	regions := []Region{
		{90, 90, 310, 170},   // drawn around SLAM!: taken
		{560, 560, 900, 900}, // the bubble next to BOOM!: far too big
	}
	if m := matchRegions(lines, regions, 1000, 1000); m[0] != 0 || m[1] != -1 {
		t.Fatalf("match: %v", m)
	}
}

func TestLayoutFallbacks(t *testing.T) {
	g, fx := loadFixture(t, "storybook")
	// No segmentation at all: lines are measured around where Spark put
	// them, which works for most (Spark drifts, so not all).
	lines := layout(g, fx.Reading.Lines, nil, nil, &store.Page{})
	exact := 0
	for _, l := range lines {
		if len(l.Words) == 0 {
			t.Fatalf("a line lost its words: %+v", l)
		}
		for _, w := range l.Words {
			if !inside(w.Box, l.Box) {
				t.Fatalf("word outside its line: %+v", l)
			}
		}
		if l.Exact {
			exact++
		}
	}
	if exact < len(lines)/2 {
		t.Fatalf("only %d of %d lines measured without segmentation", exact, len(lines))
	}
	// A region the ink does not support (blank paper) keeps Spark's words,
	// moved into the region.
	blank := image.NewGray(image.Rect(0, 0, 1000, 1000))
	for i := range blank.Pix {
		blank.Pix[i] = 255
	}
	sl := []sparkLine{{Kind: "bubble", Words: []sparkWord{{"Hi", 100, 100, 200, 150}, {"there", 220, 100, 400, 150}}}, {Kind: "caption", Words: []sparkWord{{" ", 1, 1, 2, 2}}}}
	out := layout(blank, sl, []Region{{90, 200, 420, 300}}, nil, &store.Page{})
	if len(out) != 1 || out[0].Exact || out[0].Text != "Hi there" {
		t.Fatalf("fallback: %+v", out)
	}
	for _, w := range out[0].Words {
		if !inside(w.Box, out[0].Box) {
			t.Fatalf("snapped word outside: %+v in %+v", w.Box, out[0].Box)
		}
	}
	// Degenerate regions never measure.
	if measureLine(blank, Region{0, 0, 2, 2}, sl[0].Words) != nil {
		t.Fatal("tiny region")
	}
}

func TestDedupeAndMatch(t *testing.T) {
	rs := dedupeRegions([]Region{
		{0, 0, 100, 50},    // bubble
		{2, 1, 101, 50},    // same bubble found as a caption
		{200, 0, 300, 50},  // bubble
		{400, 0, 500, 50},  // bubble
		{190, 0, 510, 60},  // joins the two above: dropped
		{0, 100, 0, 200},   // empty
		{0, 300, 400, 340}, // caption
	})
	want := []Region{{0, 0, 100, 50}, {200, 0, 300, 50}, {400, 0, 500, 50}, {0, 300, 400, 340}}
	if len(rs) != len(want) {
		t.Fatalf("dedupe: %+v", rs)
	}
	for i := range want {
		if rs[i] != want[i] {
			t.Fatalf("dedupe: %+v", rs)
		}
	}
	lines := []sparkLine{
		{Words: []sparkWord{{"a", 10, 100, 90, 140}}},   // above its bubble: still matched
		{Words: []sparkWord{{"b", 410, 10, 490, 40}}},   // clean
		{Words: []sparkWord{{"c", 600, 500, 700, 520}}}, // nothing near
	}
	m := matchRegions(lines, rs, 1000, 1000)
	if m[0] != 0 || m[1] != 2 || m[2] != -1 {
		t.Fatalf("match: %v", m)
	}
}

func TestSpeakersAndSpokenText(t *testing.T) {
	chars := []*store.Character{{Name: "Mara"}, {Name: "Captain Pip"}}
	page := &store.Page{Panels: []store.Panel{{Dialogue: []store.Line{{Character: "Mara", Text: "Who's up there?"}, {Character: "Captain Pip", Text: "Only me, I promise."}}}}}
	for _, tc := range []struct {
		l    sparkLine
		want string
	}{
		{sparkLine{Kind: "bubble", Speaker: "MARA"}, "Mara"},
		{sparkLine{Kind: "bubble", Speaker: "Pip"}, "Captain Pip"},
		{sparkLine{Kind: "bubble", Speaker: "Unknown", Text: "ONLY ME, I PROMISE!"}, "Captain Pip"},
		{sparkLine{Kind: "bubble", Speaker: narratorName, Words: []sparkWord{{W: "Who's"}, {W: "up"}, {W: "there?"}}}, "Mara"},
		{sparkLine{Kind: "bubble", Speaker: "", Text: "Something else entirely"}, ""},
		{sparkLine{Kind: "caption", Speaker: "Mara"}, ""},
	} {
		if got := resolveSpeaker(tc.l, chars, page); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.l, got, tc.want)
		}
	}
	w := func(s ...string) []LetterWord {
		var out []LetterWord
		for _, x := range s {
			out = append(out, LetterWord{Text: x})
		}
		return out
	}
	for in, want := range map[string]string{
		"WHOA-WHOA! I SAID STOP. OK?": "Whoa-whoa! I said stop. Ok?",
		"Sorry... me go...":           "Sorry... me go...",
		"A":                           "A",
		"42!":                         "42!",
	} {
		if got := SpokenText(w(strings.Fields(in)...)); got != want {
			t.Errorf("SpokenText(%q) = %q, want %q", in, got, want)
		}
	}
	if len(strings.Fields(SpokenText(w("I", "KNOW", "IT")))) != 3 {
		t.Fatal("spoken text must keep one token per word")
	}
}

func TestAlignWords(t *testing.T) {
	lettered := []LetterWord{{Text: "Hello"}, {Text: "there,"}, {Text: "Mara!"}}
	same := []WordTiming{{"Hello", 0, 0.4}, {"there,", 0.5, 0.9}, {"Mara!", 1, 1.5}}
	got := AlignWords(3, lettered, same)
	if got[1] != [2]float64{0.5, 0.9} || got[2] != [2]float64{1, 1.5} {
		t.Fatalf("one to one: %v", got)
	}
	// The voice split differently: positions along the line decide.
	other := []WordTiming{{"Hello", 0, 0.4}, {"there,Mara!", 0.5, 1.5}}
	got = AlignWords(3, lettered, other)
	if got[0] != [2]float64{0, 0.4} || got[2] != [2]float64{0.5, 1.5} {
		t.Fatalf("by position: %v", got)
	}
	if got := AlignWords(2, lettered[:2], nil); got[1] != [2]float64{} {
		t.Fatal("no timings")
	}
}

func TestReadPageOnTheFake(t *testing.T) {
	f := &Fake{}
	chars := []*store.Character{{Name: "Mara"}, {Name: "Pip"}}
	page := &store.Page{Number: 2, Panels: []store.Panel{
		{Number: 1, Caption: "Night falls on the harbour.", Dialogue: []store.Line{{Character: "Mara", Text: "Is somebody up here?"}}},
		{Number: 2, Dialogue: []store.Line{{Character: "Pip", Text: "Please don't scream."}}},
		{Number: 3},
	}}
	art, err := f.EditImage(context.Background(), PagePrompt(&store.Story{Style: "comic"}, chars, page, ""), nil, PageSize)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := ReadPage(context.Background(), f, chars, page, art)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ kind, speaker, text string }{
		{"caption", "", "Night falls on the harbour."},
		{"bubble", "Mara", "Is somebody up here?"},
		{"bubble", "Pip", "Please don't scream."},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines: %+v", lines)
	}
	for i, l := range lines {
		if l.Kind != want[i].kind || l.Speaker != want[i].speaker || l.Text != want[i].text || !l.Exact {
			t.Errorf("line %d: %+v", i, l)
		}
	}
	// The fake segmentation only knows placeholder pages.
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 10, 10)))
	if rs, err := f.Segment(context.Background(), buf.Bytes(), "caption"); err != nil || rs != nil {
		t.Fatalf("segment on a non-placeholder: %v %v", rs, err)
	}
	if _, err := ReadPage(context.Background(), f, chars, page, []byte("not a png")); err == nil {
		t.Fatal("an unreadable page must fail")
	}
	if _, err := ReadPage(context.Background(), &castRecorder{err: errors.New("down")}, chars, page, art); err == nil {
		t.Fatal("a failed reading must fail")
	}
}

func TestLetteringPromptCarriesTheScript(t *testing.T) {
	page := &store.Page{Panels: []store.Panel{{Number: 1, Caption: "Dawn.", Dialogue: []store.Line{{Character: "Mara", Text: `Say "hi"`}}}}}
	p := LetteringPrompt([]*store.Character{{Name: "Mara"}}, page)
	for _, want := range []string{"CAST:\n- Mara", `Panel 1 — caption: "Dawn."`, `Panel 1 — Mara says: "Say \"hi\""`, "NARRATOR", "0..1000"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	got := fakePanelsFromScript(after(p, "SCRIPT FOR THIS PAGE"))
	if got[0].Caption != "Dawn." || got[0].Text != `Say "hi"` || got[0].Speaker != "Mara" {
		t.Fatalf("fake parse: %+v", got)
	}
}
