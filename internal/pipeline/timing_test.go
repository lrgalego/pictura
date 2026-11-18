package pipeline

import (
	"math"
	"testing"
)

// speechLike is bursts of tone, one per word, between silences of the
// given lengths: lead, then a gap after each word, then a tail.
func speechLike(rate int, lead float64, words []float64, gaps []float64) []int16 {
	var out []int16
	silence := func(s float64) { out = append(out, make([]int16, int(s*float64(rate)))...) }
	silence(lead)
	for i, d := range words {
		for j := range int(d * float64(rate)) {
			out = append(out, int16(9000*math.Sin(float64(j)*2*math.Pi*180/float64(rate))))
		}
		silence(gaps[i])
	}
	return out
}

func TestTimeWordsFollowsTheVoice(t *testing.T) {
	const rate = 24000
	// "Wait, I'm coming!" — the comma's pause is longer than the space.
	samples := speechLike(rate, 0.3, []float64{0.35, 0.25, 0.5}, []float64{0.3, 0.06, 0.4})
	got := TimeWords(samples, rate, "Wait, I'm coming!")
	if len(got) != 3 {
		t.Fatalf("got %d words", len(got))
	}
	near := func(v, want float64) bool { return math.Abs(v-want) < 0.12 }
	// Words start after the silence in front and end before the tail.
	if !near(got[0].Start, 0.3) || !near(got[2].End, 1.76) {
		t.Errorf("span %.2f..%.2f, want 0.30..1.76", got[0].Start, got[2].End)
	}
	// The boundary after "Wait," sits in the comma's pause (0.65..0.95).
	if got[0].End < 0.55 || got[1].Start > 1.05 || got[1].Start < got[0].End {
		t.Errorf("first boundary %.2f / %.2f", got[0].End, got[1].Start)
	}
	for i, w := range got {
		if w.End < w.Start || (i > 0 && w.Start < got[i-1].End) {
			t.Errorf("word %d out of order: %+v", i, got)
		}
	}
}

func TestTimeWordsTags(t *testing.T) {
	const rate = 16000
	samples := speechLike(rate, 0.1, []float64{0.3, 0.6, 0.3}, []float64{0.1, 0.1, 0.1})
	got := TimeWords(samples, rate, "[softly sad] Hi [laughs] there.")
	if len(got) != 5 {
		t.Fatalf("got %+v", got)
	}
	if got[0].End != got[0].Start || got[1].End != got[1].Start {
		t.Errorf("a direction takes no time, over all its words: %+v", got[:2])
	}
	if d := got[3].End - got[3].Start; d < 0.1 {
		t.Errorf("a performed vocal takes time: %+v", got[3])
	}
	// A vocal in front is performed, not a direction.
	got = TimeWords(samples, rate, "[laughs] You're scared? [unclosed")
	if d := got[0].End - got[0].Start; d < 0.1 || got[3].End <= got[3].Start {
		t.Errorf("a leading vocal is timed, an unclosed bracket is a word: %+v", got)
	}
}

func TestTimeWordsEdges(t *testing.T) {
	if TimeWords(nil, 24000, "") != nil || TimeWords([]int16{1}, 0, "hi") != nil {
		t.Error("nothing to time gives nothing")
	}
	// Silence: words share the whole clip.
	got := TimeWords(make([]int16, 24000), 24000, "one two")
	if len(got) != 2 || got[0].Start != 0 || got[1].End > 1.0001 {
		t.Errorf("silent clip: %+v", got)
	}
	// Ellipses, semicolons and digits weigh as they are said.
	if w, p := tokenWeight("well..."); w != 5 || p != 6 {
		t.Errorf("well...: %v %v", w, p)
	}
	if w, p := tokenWeight("42;"); w != 3 || p != 2.5 {
		t.Errorf("42;: %v %v", w, p)
	}
	if w, p := tokenWeight("—"); w != 2 || p != 2.5 {
		t.Errorf("dash: %v %v", w, p)
	}
}

func TestGeminiVoice(t *testing.T) {
	cases := []struct{ id, name, accent string }{
		{"JBFqnCBsd6RMkjVDRZzb", "Charon", "British"},  // George
		{"cgSgspJ2msm6clMCkdW9", "Leda", ""},           // Jessica, American
		{"IKne3meq5aSn9XLyUdCD", "Orus", "Australian"}, // Charlie
		{"Kore", "Kore", ""},                           // a Gemini name already
		{"", "Charon", "British"},                      // nobody cast: the default narrator
	}
	for _, c := range cases {
		if n, a := GeminiVoice(c.id); n != c.name || a != c.accent {
			t.Errorf("%q: %s/%s, want %s/%s", c.id, n, a, c.name, c.accent)
		}
	}
	seen := map[string]bool{}
	for _, v := range Voices {
		n, _ := GeminiVoice(v.ID)
		if n == "" || seen[n] {
			t.Errorf("%s maps to %q, which is empty or taken", v.Name, n)
		}
		seen[n] = true
	}
}
