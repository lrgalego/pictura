package pipeline

import (
	"math"
	"strings"
	"unicode"
)

// TimeWords estimates when each word of a line is said in a clip of 16-bit
// mono samples, for voices that return no alignment (Gemini). It returns
// one timing per whitespace-separated token of text, as WordsFromChars
// does, so AlignTokens can lay them onto the lettered words.
//
// The voiced span is found from the loudness. The tokens share it by how
// long they take to say: their letters, plus the pause their punctuation
// asks for; a vocal tag ("[laughs]") is performed and takes about as long
// as a long word, any other tag ("[whispers]", "[softly sad]") is a direction and
// takes no time. Each boundary between words is then moved to the quietest
// moment near where it fell, which is where speakers breathe and pause.
func TimeWords(samples []int16, rate int, text string) []WordTiming {
	toks := strings.Fields(text)
	if len(toks) == 0 || rate <= 0 {
		return nil
	}
	frame := max(rate/100, 1) // 10 ms
	env := envelope(samples, frame)
	step := float64(frame) / float64(rate)
	first, last := voiced(env)
	start, end := float64(first)*step, float64(last+1)*step
	if last < first {
		start, end = 0, float64(len(samples))/float64(rate)
	}

	weights := make([]float64, len(toks))
	pauses := make([]float64, len(toks))
	total := 0.0
	tags := tagTokens(toks)
	for i, t := range toks {
		switch tag, ok := tags[i]; {
		case ok && IsVocalTag(tag):
			weights[i], pauses[i] = vocalWeight, 2
		case ok:
			weights[i], pauses[i] = 0, 0
		default:
			weights[i], pauses[i] = tokenWeight(t)
		}
		if i == len(toks)-1 {
			pauses[i] = 0 // the clip ends where the voice does
		}
		total += weights[i] + pauses[i]
	}
	if total == 0 {
		total = 1
	}
	scale := (end - start) / total

	// Ideal times along the span, then snapped to quiet frames.
	out := make([]WordTiming, len(toks))
	at := start
	for i, t := range toks {
		w := WordTiming{Text: t, Start: at, End: at + weights[i]*scale}
		at = w.End + pauses[i]*scale
		out[i] = w
	}
	window := math.Min(0.15, 0.4*(end-start)/float64(len(toks)))
	for i := 0; i+1 < len(out); i++ {
		if weights[i+1] == 0 || weights[i] == 0 {
			continue
		}
		gapFrom, gapTo := out[i].End, out[i+1].Start
		half := (gapTo - gapFrom) / 2
		q := quietest(env, step, (gapFrom+gapTo)/2, window)
		// A snap may not eat more than half of either word, or two
		// boundaries drawn to one pause would leave a word no time.
		lo := out[i].Start + weights[i]*scale/2 + half
		hi := out[i+1].End - weights[i+1]*scale/2 - half
		if lo <= hi {
			q = clampF(q, lo, hi)
		}
		out[i].End = clampF(q-half, out[i].Start, end)
		out[i+1].Start = clampF(q+half, out[i].End, end)
	}
	for i := range out {
		if out[i].End < out[i].Start {
			out[i].End = out[i].Start
		}
	}
	return out
}

// vocalWeight is how long a performed vocal takes, in letters: about
// three quarters of a second. Tuned, with the rest of TimeWords, against
// ElevenLabs' character timings of real Pictura lines, which it misses by
// about 0.17 s a word on average.
const vocalWeight = 10

// tagTokens finds the tokens that belong to an audio tag, which may span
// several ("[softly", "sad]"), and the tag each belongs to.
func tagTokens(toks []string) map[int]string {
	out := map[int]string{}
	for i := 0; i < len(toks); i++ {
		if !strings.HasPrefix(toks[i], "[") {
			continue
		}
		j := i
		for j < len(toks) && !strings.Contains(toks[j], "]") {
			j++
		}
		if j == len(toks) {
			continue // an open bracket with no close is just text
		}
		tag := strings.Join(toks[i:j+1], " ")
		tag = strings.ToLower(strings.TrimSpace(tag[1:strings.Index(tag, "]")]))
		for k := i; k <= j; k++ {
			out[k] = tag
		}
		i = j
	}
	return out
}

// tokenWeight is how long a word takes to say, in letters, and the pause
// after it.
func tokenWeight(t string) (weight, pause float64) {
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			weight++
		}
	}
	weight = math.Max(weight, 1) + 1 // every word has an onset
	switch {
	case strings.HasSuffix(t, "...") || strings.HasSuffix(t, "…"):
		pause = 6
	case strings.HasSuffix(t, ".") || strings.HasSuffix(t, "!") || strings.HasSuffix(t, "?"):
		pause = 4
	case strings.HasSuffix(t, ",") || strings.HasSuffix(t, ";") || strings.HasSuffix(t, ":") || strings.HasSuffix(t, "—"):
		pause = 2.5
	}
	return weight, pause
}

// envelope is the RMS loudness of each frame.
func envelope(samples []int16, frame int) []float64 {
	n := (len(samples) + frame - 1) / frame
	env := make([]float64, n)
	for i := range env {
		var sum float64
		lo, hi := i*frame, min((i+1)*frame, len(samples))
		for _, s := range samples[lo:hi] {
			sum += float64(s) * float64(s)
		}
		env[i] = math.Sqrt(sum / float64(max(hi-lo, 1)))
	}
	return env
}

// voiced is the first and last frame loud enough to be voice: above a
// twentieth of the loudest frame. The frames before and after are
// silence or breath. last < first when nothing is.
func voiced(env []float64) (first, last int) {
	peak := 0.0
	for _, v := range env {
		peak = math.Max(peak, v)
	}
	if peak == 0 {
		return 0, -1
	}
	thr := peak / 20
	first, last = -1, -1
	for i, v := range env {
		if v >= thr {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0, -1
	}
	return first, last
}

// quietest is the time of the quietest frame within window of t, the
// nearest of equals; quiet means a smoothed loudness, so a single dip in
// a vowel does not win over a real gap.
func quietest(env []float64, step, t, window float64) float64 {
	c := int(t / step)
	r := int(window / step)
	best, bestV := c, math.Inf(1)
	for i := c - r; i <= c+r; i++ {
		if i < 1 || i+1 >= len(env) {
			continue
		}
		v := env[i-1] + 2*env[i] + env[i+1]
		if v < bestV || (v == bestV && abs(i-c) < abs(best-c)) {
			best, bestV = i, v
		}
	}
	return (float64(best) + 0.5) * step
}
