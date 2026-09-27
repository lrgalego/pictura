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
	if measureWords(blank, Region{0, 0, 2, 2}, wordRows(sl[0].Words)) != nil {
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
