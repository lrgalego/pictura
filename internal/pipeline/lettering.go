package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	xdraw "image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/lrgalego/pictura/internal/store"
)

// The lettering map is how the reader knows where every spoken word sits
// on a drawn page. The art, not the storyboard, is the truth: the image
// model sometimes letters text the plan never asked for, or drops a line.
// Three sources are combined:
//
//   - Muse Spark reads the page: every line of lettering in reading order,
//     who says it, and rough word boxes (right horizontally, but they can
//     drift ~5% of the page vertically).
//   - SAM 3.1 finds every bubble and caption as a tight box.
//   - The ink inside each box gives pixel-exact word boxes: glyphs are
//     split into rows and words by the gaps between them, guided by how
//     many rows and words Spark read.

// Box is a rectangle in fractions of the page (0..1), so it scales with
// however large the page is shown.
type Box struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// LetterWord is one lettered word and where it is drawn.
type LetterWord struct {
	Text string `json:"text"`
	Box  Box    `json:"box"`
	Tag  string `json:"tag,omitempty"` // a vocal the speaker performs instead of saying the word
}

// LetterLine is one balloon, caption, title or sound effect.
type LetterLine struct {
	Kind    string       `json:"kind"`    // bubble, caption, title, sfx
	Speaker string       `json:"speaker"` // a cast member's name; "" is the narrator
	Text    string       `json:"text"`
	Box     Box          `json:"box"` // the bubble or caption box
	Words   []LetterWord `json:"words"`
	Exact   bool         `json:"exact"` // word boxes were measured from the ink
}

const narratorName = "NARRATOR"

var letteringSchema = obj(map[string]any{
	"lines": arr(obj(map[string]any{
		"panel":   integer("1-based panel number"),
		"kind":    map[string]any{"type": "string", "enum": []any{"bubble", "caption", "title", "sfx"}},
		"speaker": str("For a speech or thought bubble, the cast name of who says it; NARRATOR for captions, titles and sound effects"),
		"text":    str("The lettering exactly as drawn"),
		"words": arr(obj(map[string]any{
			"w":  str("One word exactly as drawn, with its punctuation"),
			"x1": integer("Left edge, 0..1000 of the image width"),
			"y1": integer("Top edge, 0..1000 of the image height"),
			"x2": integer("Right edge, 0..1000 of the image width"),
			"y2": integer("Bottom edge, 0..1000 of the image height"),
		}, "w", "x1", "y1", "x2", "y2")),
	}, "panel", "kind", "speaker", "text", "words")),
}, "lines")

const letteringPersona = `You are a comic letterer's assistant. You read the lettering on a finished comic page exactly as it is drawn, word for word, and say where each word is. Answer only with JSON matching the schema.`

type sparkWord struct {
	W  string `json:"w"`
	X1 int    `json:"x1"`
	Y1 int    `json:"y1"`
	X2 int    `json:"x2"`
	Y2 int    `json:"y2"`
}

type sparkLine struct {
	Panel   int         `json:"panel"`
	Kind    string      `json:"kind"`
	Speaker string      `json:"speaker"`
	Text    string      `json:"text"`
	Words   []sparkWord `json:"words"`
}

// LetteringPrompt is what Muse Spark is asked about a page.
func LetteringPrompt(chars []*store.Character, page *store.Page) string {
	var b strings.Builder
	b.WriteString("Transcribe every piece of lettering on this comic page (speech and thought bubbles, captions, the title, sound effects) exactly as drawn, in reading order: panels left to right, top to bottom; within a panel, captions first, then bubbles in the order they are read. For each piece give the panel number, the kind, the speaker, the text, and every word with its tight bounding box in coordinates normalized to 0..1000 of the image width (x) and height (y). Follow each bubble's tail to its speaker and use the script below as a guide; captions, titles and sound effects are spoken by NARRATOR. Skip page numbers, signatures and text that is part of the scenery (signs, clothing).\n\nCAST:\n")
	for _, c := range chars {
		fmt.Fprintf(&b, "- %s\n", c.Name)
	}
	b.WriteString("\nSCRIPT FOR THIS PAGE (what was asked for; the drawing may differ, transcribe what is drawn):\n")
	b.WriteString(PageScript(page))
	return b.String()
}

// PageScript lists a page's lettering as planned, one line each.
func PageScript(page *store.Page) string {
	var b strings.Builder
	for _, p := range page.Panels {
		if strings.TrimSpace(p.Caption) != "" {
			fmt.Fprintf(&b, "Panel %d — caption: %q\n", p.Number, p.Caption)
		}
		for _, l := range p.Dialogue {
			fmt.Fprintf(&b, "Panel %d — %s says: %q\n", p.Number, l.Character, l.Text)
		}
	}
	return b.String()
}

// ReadPage builds the lettering map of a drawn page. The Spark reading is
// required; the segmentation is best effort (without it, Spark's boxes
// are used as they are).
func ReadPage(ctx context.Context, ai AI, chars []*store.Character, page *store.Page, png []byte) ([]LetterLine, error) {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return nil, fmt.Errorf("the page image cannot be read: %w", err)
	}
	var (
		wg              sync.WaitGroup
		bubbles, labels []Region
	)
	wg.Add(2)
	go func() { defer wg.Done(); bubbles, _ = ai.Segment(ctx, png, "speech bubble") }()
	go func() { defer wg.Done(); labels, _ = ai.Segment(ctx, png, "caption") }()
	var out struct {
		Lines []sparkLine `json:"lines"`
	}
	err = ai.ChatJSON(ctx, letteringPersona, LetteringPrompt(chars, page), []Image{{Label: "The comic page", PNG: png}}, "lettering", letteringSchema, &out)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	return layout(toGray(img), out.Lines, append(bubbles, labels...), chars, page), nil
}

// Remeasure places lines already read from a page (and voiced) on its art
// again with the current measure: only the boxes change — text, speakers,
// clips and timings stay. Lines that share a box are one balloon the sound
// design split (speech, a vocal, an effect) and are measured together.
func Remeasure(ctx context.Context, ai AI, png []byte, lines []LetterLine) ([]LetterLine, error) {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return nil, fmt.Errorf("the page image cannot be read: %w", err)
	}
	type unit struct{ from, to int }
	var units []unit
	var reads []sparkLine
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j].Box == lines[i].Box {
			j++
		}
		sl := sparkLine{Kind: lines[i].Kind}
		blank := false
		for _, l := range lines[i:j] {
			for _, w := range l.Words {
				if strings.TrimSpace(w.Text) == "" {
					blank = true
				}
				sl.Words = append(sl.Words, sparkWord{w.Text, int(math.Round(w.Box.X1 * 1000)), int(math.Round(w.Box.Y1 * 1000)), int(math.Round(w.Box.X2 * 1000)), int(math.Round(w.Box.Y2 * 1000))})
			}
		}
		// The stored words are the older measure, not Spark's reading. On
		// lettering over the art it sometimes kept a stroke of one letter;
		// the line's own box was right, so the words are spread across it
		// again, by their lengths.
		if k := sl.Kind; (k == "sfx" || k == "title") && len(sl.Words) > 0 {
			lb := lines[i].Box
			u := Box{1, 1, 0, 0}
			for _, w := range sl.Words {
				u = boxUnion(u, sparkBox(w))
			}
			if boxArea(u) < 0.4*boxArea(lb) {
				total := 0.0
				for _, w := range sl.Words {
					total += weight(w.W)
				}
				acc := lb.X1
				for wi, w := range sl.Words {
					wv := (lb.X2 - lb.X1) * weight(w.W) / total
					sl.Words[wi] = sparkWord{w.W, int(math.Round(acc * 1000)), int(math.Round(lb.Y1 * 1000)), int(math.Round((acc + wv) * 1000)), int(math.Round(lb.Y2 * 1000))}
					acc += wv
				}
			}
		}
		if len(sl.Words) > 0 && !blank {
			units = append(units, unit{i, j})
			reads = append(reads, sl)
		}
		i = j
	}
	var (
		wg              sync.WaitGroup
		bubbles, labels []Region
	)
	wg.Add(2)
	go func() { defer wg.Done(); bubbles, _ = ai.Segment(ctx, png, "speech bubble") }()
	go func() { defer wg.Done(); labels, _ = ai.Segment(ctx, png, "caption") }()
	wg.Wait()
	measured := layout(toGray(img), reads, append(bubbles, labels...), nil, &store.Page{})
	if len(measured) != len(reads) {
		return lines, nil // cannot happen: every unit has words; keep what was there
	}
	out := append([]LetterLine(nil), lines...)
	for k, u := range units {
		m, off := measured[k], 0
		for i := u.from; i < u.to; i++ {
			words := append([]LetterWord(nil), out[i].Words...)
			for wi := range words {
				words[wi].Box = m.Words[off+wi].Box
			}
			off += len(words)
			out[i].Words, out[i].Box, out[i].Exact = words, m.Box, m.Exact
		}
	}
	return out, nil
}

func toGray(img image.Image) *image.Gray {
	if g, ok := img.(*image.Gray); ok {
		return g
	}
	g := image.NewGray(img.Bounds())
	xdraw.Draw(g, g.Bounds(), img, img.Bounds().Min, xdraw.Src)
	return g
}

// layout turns Spark's reading and SAM's boxes into the lettering map.
func layout(g *image.Gray, lines []sparkLine, regions []Region, chars []*store.Character, page *store.Page) []LetterLine {
	b := g.Bounds()
	W, H := float64(b.Dx()), float64(b.Dy())
	var kept []sparkLine
	for _, l := range lines {
		var ws []sparkWord
		for _, w := range l.Words {
			if strings.TrimSpace(w.W) != "" {
				ws = append(ws, w)
			}
		}
		if len(ws) > 0 {
			l.Words = ws
			kept = append(kept, l)
		}
	}
	regions = dedupeRegions(regions)
	match := matchRegions(kept, regions, W, H)
	var out []LetterLine
	for i, l := range kept {
		line := LetterLine{Kind: l.Kind, Speaker: resolveSpeaker(l, chars, page)}
		var measured []Region
		var region *Region
		if match[i] >= 0 {
			region = &regions[match[i]]
			measured = measureLine(g, *region, l.Words)
		} else {
			// No bubble or caption box (titles, lettering straight on the
			// art): measure around where Spark saw it. Only a reading the
			// ink agrees with is kept.
			measured = measureLine(g, sparkRegion(l.Words, W, H), l.Words)
			// On busy art (outlined lettering over a drawing) the ink can
			// be a sliver of an outline; Spark's height, if not its place,
			// is right, so a measure much flatter than its reading is not
			// the lettering.
			if measured != nil {
				sy1, sy2, my1, my2 := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
				for wi, w := range l.Words {
					sy1, sy2 = math.Min(sy1, float64(w.Y1)*H/1000), math.Max(sy2, float64(w.Y2)*H/1000)
					my1, my2 = math.Min(my1, float64(measured[wi].Y1)), math.Max(my2, float64(measured[wi].Y2))
				}
				if my2-my1 < 0.5*(sy2-sy1) {
					measured = nil
				}
			}
		}
		spark := sparkUnion(l.Words)
		// Spark's boxes drift but are about the right size: ink measured
		// into much less than that is a fragment (a stroke of one letter),
		// not the line.
		if measured != nil {
			u := Region{math.MaxInt, math.MaxInt, math.MinInt, math.MinInt}
			for _, r := range measured {
				u = Region{min(u.X1, r.X1), min(u.Y1, r.Y1), max(u.X2, r.X2), max(u.Y2, r.Y2)}
			}
			if float64(area(u)) < 0.4*boxArea(spark)*W*H {
				measured = nil
			}
		}
		// Spark lists the words in reading order; its boxes only place
		// them roughly, so they never reorder them.
		for wi, w := range l.Words {
			box := sparkBox(w)
			if measured != nil {
				r := measured[wi]
				box = Box{float64(r.X1) / W, float64(r.Y1) / H, float64(r.X2) / W, float64(r.Y2) / H}
			}
			line.Words = append(line.Words, LetterWord{Text: strings.TrimSpace(w.W), Box: box})
		}
		if measured == nil && region != nil {
			line.Words = snapInto(line.Words, regionBox(*region, W, H))
		}
		line.Exact = measured != nil
		switch {
		case region != nil:
			// The lettering can spill a little past SAM's box (it is
			// measured just beyond it): the line holds both.
			line.Box = boxUnion(regionBox(*region, W, H), unionBox(line.Words))
		case l.Kind == "sfx" || l.Kind == "title":
			// Lettering on the art lights up as a whole: its box is never
			// smaller than where Spark read it, whatever the ink says.
			line.Box = padBox(boxUnion(spark, unionBox(line.Words)), 0.006)
		default:
			line.Box = padBox(unionBox(line.Words), 0.006)
		}
		texts := make([]string, len(line.Words))
		for i, w := range line.Words {
			texts[i] = w.Text
		}
		line.Text = strings.Join(texts, " ")
		out = append(out, line)
	}
	return out
}

// sparkRegion is the area around a line as Spark placed it, padded for
// its vertical drift, in pixels.
func sparkRegion(ws []sparkWord, W, H float64) Region {
	x1, y1, x2, y2 := 1000, 1000, 0, 0
	for _, w := range ws {
		x1, y1, x2, y2 = min(x1, w.X1), min(y1, w.Y1), max(x2, w.X2), max(y2, w.Y2)
	}
	padY := max(y2-y1, 15)
	padX := 20
	return Region{
		int(float64(max(0, x1-padX)) * W / 1000), int(float64(max(0, y1-padY)) * H / 1000),
		int(float64(min(1000, x2+padX)) * W / 1000), int(float64(min(1000, y2+padY)) * H / 1000),
	}
}

func sparkBox(w sparkWord) Box {
	c := func(v int) float64 { return math.Max(0, math.Min(1, float64(v)/1000)) }
	return Box{c(w.X1), c(w.Y1), c(w.X2), c(w.Y2)}
}

// sparkUnion is the box around a line's words as Spark read them.
func sparkUnion(ws []sparkWord) Box {
	u := Box{1, 1, 0, 0}
	for _, w := range ws {
		u = boxUnion(u, sparkBox(w))
	}
	return u
}

func boxUnion(a, b Box) Box {
	return Box{math.Min(a.X1, b.X1), math.Min(a.Y1, b.Y1), math.Max(a.X2, b.X2), math.Max(a.Y2, b.Y2)}
}

func boxArea(b Box) float64 { return math.Max(0, b.X2-b.X1) * math.Max(0, b.Y2-b.Y1) }

func padBox(b Box, p float64) Box {
	return Box{math.Max(0, b.X1-p), math.Max(0, b.Y1-p), math.Min(1, b.X2+p), math.Min(1, b.Y2+p)}
}

func regionBox(r Region, W, H float64) Box {
	return Box{float64(r.X1) / W, float64(r.Y1) / H, float64(r.X2) / W, float64(r.Y2) / H}
}

func unionBox(ws []LetterWord) Box {
	u := Box{1, 1, 0, 0}
	for _, w := range ws {
		u.X1, u.Y1 = math.Min(u.X1, w.Box.X1), math.Min(u.Y1, w.Box.Y1)
		u.X2, u.Y2 = math.Max(u.X2, w.Box.X2), math.Max(u.Y2, w.Box.Y2)
	}
	return u
}

// snapInto moves Spark's word boxes as a block so they are centred in the
// region SAM found (Spark's error is mostly a vertical shift) and clamps
// them inside it.
func snapInto(ws []LetterWord, r Box) []LetterWord {
	u := unionBox(ws)
	dx := (r.X1+r.X2)/2 - (u.X1+u.X2)/2
	dy := (r.Y1+r.Y2)/2 - (u.Y1+u.Y2)/2
	for i := range ws {
		b := ws[i].Box
		b.X1, b.X2 = clampF(b.X1+dx, r.X1, r.X2), clampF(b.X2+dx, r.X1, r.X2)
		b.Y1, b.Y2 = clampF(b.Y1+dy, r.Y1, r.Y2), clampF(b.Y2+dy, r.Y1, r.Y2)
		ws[i].Box = b
	}
	return ws
}

func clampF(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func area(r Region) int {
	if r.X2 <= r.X1 || r.Y2 <= r.Y1 {
		return 0
	}
	return (r.X2 - r.X1) * (r.Y2 - r.Y1)
}

func overlap(a, b Region) int {
	return area(Region{max(a.X1, b.X1), max(a.Y1, b.Y1), min(a.X2, b.X2), min(a.Y2, b.Y2)})
}

// dedupeRegions merges the two SAM queries: "speech bubble" boxes come
// first and win over near-identical "caption" boxes, and a box that
// swallows two others (SAM sometimes joins neighbouring bubbles) goes.
func dedupeRegions(rs []Region) []Region {
	var kept []Region
	for _, r := range rs {
		if area(r) == 0 {
			continue
		}
		dup := false
		for _, o := range kept {
			if float64(overlap(r, o)) > 0.85*float64(min(area(r), area(o))) && float64(area(o)) <= 1.2*float64(area(r)) {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, r)
		}
	}
	var out []Region
	for i, r := range kept {
		inside := 0
		for j, o := range kept {
			if i != j && float64(overlap(r, o)) > 0.8*float64(area(o)) {
				inside++
			}
		}
		if inside < 2 {
			out = append(out, r)
		}
	}
	return out
}

// matchRegions pairs each line with the box it sits in: the box must cover
// most of the line horizontally (Spark is reliable there) and be near it
// vertically; the closest pairs are taken first, one line per box.
func matchRegions(lines []sparkLine, regions []Region, W, H float64) []int {
	type pair struct {
		line, region int
		d            float64
	}
	var pairs []pair
	for i, l := range lines {
		onArt := l.Kind == "sfx" || l.Kind == "title"
		var lx1, ly1, lx2, ly2 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, w := range l.Words {
			lx1, lx2 = math.Min(lx1, float64(w.X1)*W/1000), math.Max(lx2, float64(w.X2)*W/1000)
			ly1, ly2 = math.Min(ly1, float64(w.Y1)*H/1000), math.Max(ly2, float64(w.Y2)*H/1000)
		}
		width := math.Max(1, lx2-lx1)
		cy := (ly1 + ly2) / 2
		for j, r := range regions {
			xo := math.Max(0, math.Min(lx2, float64(r.X2))-math.Max(lx1, float64(r.X1))) / width
			d := math.Abs(float64(r.Y1+r.Y2)/2 - cy)
			// Lettering on the art (a title, a sound effect) only takes a
			// box SAM drew around that very lettering — about its size and
			// close by — never the bubble next to it.
			if onArt {
				ratio := float64(area(r)) / math.Max(1, width*math.Max(1, ly2-ly1))
				if xo < 0.6 || d > 0.06*H || ratio < 0.3 || ratio > 3 {
					continue
				}
			}
			if xo >= 0.5 && d <= 0.12*H {
				pairs = append(pairs, pair{i, j, d})
			}
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].d < pairs[b].d })
	out := make([]int, len(lines))
	for i := range out {
		out[i] = -1
	}
	taken := map[int]bool{}
	for _, p := range pairs {
		if out[p.line] < 0 && !taken[p.region] {
			out[p.line], taken[p.region] = p.region, true
		}
	}
	return out
}

// inkThreshold separates lettering ink from paper and bubble fill;
// paperThreshold is what counts as the light ground lettering sits on.
const (
	inkThreshold   = 110
	paperThreshold = 170
)

// glyph is one connected piece of lettering ink, in crop pixels.
type glyph struct{ x1, y1, x2, y2 int }

func (g glyph) h() int { return g.y2 - g.y1 }

// segment is a run of glyphs with no word space inside: a word, or part
// of one when the letters are spaced wide, or two when the space is tight.
type segment struct {
	x1, y1, x2, y2 int
	row            int
}

// measureLine finds where each of Spark's words is inked inside a region.
// The ink alone decides the rows and words (Spark's own boxes drift and
// often group a balloon's rows wrongly); Spark's words are then laid onto
// the inked words in reading order by their lengths. It returns one box
// per word, in page pixels, or nil when the ink does not fit the reading
// (the caller then falls back to Spark's boxes).
func measureLine(g *image.Gray, r Region, words []sparkWord) []Region {
	b := g.Bounds()
	r = Region{max(r.X1, b.Min.X), max(r.Y1, b.Min.Y), min(r.X2, b.Max.X), min(r.Y2, b.Max.Y)}
	if r.X2-r.X1 < 4 || r.Y2-r.Y1 < 4 || len(words) == 0 {
		return nil
	}
	// Look a little past the region so the bubble's outline is whole (and
	// dropped as one shape) and whatever the crop still cuts — a tail, the
	// art around a caption — touches the crop's edge and is dropped too.
	m := max(6, b.Dx()/100)
	c := Region{max(r.X1-m, b.Min.X), max(r.Y1-m, b.Min.Y), min(r.X2+m, b.Max.X), min(r.Y2+m, b.Max.Y)}
	glyphs := findGlyphs(g, c)
	if len(glyphs) == 0 {
		return nil
	}
	segs := wordSegments(glyphs)
	boxes := layWords(words, segs)
	if boxes == nil {
		return nil
	}
	for i := range boxes {
		boxes[i] = Region{c.X1 + boxes[i].X1, c.Y1 + boxes[i].Y1, c.X1 + boxes[i].X2, c.Y1 + boxes[i].Y2}
	}
	return boxes
}

// findGlyphs returns the lettering ink in a crop: dark connected shapes on
// a light ground, minus the bubble's outline and tail (wide, tall, or cut
// by the crop's edge), specks, and dark shapes of the art that poke in (a
// glyph is thin strokes on a light ground, so the ring just around it is
// mostly light; an ear is not). Shapes much taller than the lettering
// (a tail's tip, a stroke of art) go as well.
func findGlyphs(g *image.Gray, c Region) []glyph {
	ww, hh := c.X2-c.X1, c.Y2-c.Y1
	ink := make([]bool, ww*hh)
	light := make([]bool, ww*hh)
	for y := 0; y < hh; y++ {
		for x := 0; x < ww; x++ {
			v := g.GrayAt(c.X1+x, c.Y1+y).Y
			ink[y*ww+x] = v < inkThreshold
			light[y*ww+x] = v > paperThreshold
		}
	}
	seen := make([]bool, len(ink))
	var stack []int
	var out []glyph
	for i := range ink {
		if !ink[i] || seen[i] {
			continue
		}
		stack = append(stack[:0], i)
		seen[i] = true
		n := 0
		minX, minY, maxX, maxY := ww, hh, -1, -1
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n++
			x, y := p%ww, p/ww
			minX, maxX, minY, maxY = min(minX, x), max(maxX, x), min(minY, y), max(maxY, y)
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					qx, qy := x+dx, y+dy
					if qx < 0 || qy < 0 || qx >= ww || qy >= hh {
						continue
					}
					if q := qy*ww + qx; ink[q] && !seen[q] {
						seen[q] = true
						stack = append(stack, q)
					}
				}
			}
		}
		bw, bh := maxX-minX+1, maxY-minY+1
		switch {
		case n < 4,
			minX == 0 || minY == 0 || maxX == ww-1 || maxY == hh-1,
			bh > 7*bw, // a sliver of outline, not a letter
			float64(bw) > 0.5*float64(ww),
			float64(bh) > 0.6*float64(hh) && float64(bw) > 0.2*float64(ww),
			!onLightGround(light, ww, hh, minX-2, minY-2, maxX+2, maxY+2):
			continue
		}
		out = append(out, glyph{minX, minY, maxX + 1, maxY + 1})
	}
	if len(out) == 0 {
		return nil
	}
	ref := letterHeight(out)
	kept := out[:0]
	for _, gl := range out {
		if float64(gl.h()) <= 2.2*float64(ref) {
			kept = append(kept, gl)
		}
	}
	return kept
}

// letterHeight is the height of the lettering's letters: the median of the
// taller half of the glyphs (the shorter half is punctuation and lowercase).
func letterHeight(gs []glyph) int {
	hs := make([]int, len(gs))
	for i, g := range gs {
		hs[i] = g.h()
	}
	sort.Ints(hs)
	upper := hs[len(hs)/2:]
	return max(1, upper[len(upper)/2])
}

// wordSegments groups glyphs into rows (bands of letter-height glyphs,
// top to bottom) and each row into segments split at word spaces, in
// reading order.
func wordSegments(gs []glyph) []segment {
	ref := float64(letterHeight(gs))
	type band struct {
		a, b int
		gs   []glyph
	}
	var bands []band
	var small []glyph
	sorted := append([]glyph(nil), gs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].y1 < sorted[j].y1 })
	for _, gl := range sorted {
		if float64(gl.h()) < 0.5*ref {
			small = append(small, gl)
			continue
		}
		if n := len(bands); n > 0 {
			cur := &bands[n-1]
			ov := min(cur.b, gl.y2) - max(cur.a, gl.y1)
			if float64(ov) > 0.35*float64(min(cur.b-cur.a, gl.h())) {
				cur.a, cur.b = min(cur.a, gl.y1), max(cur.b, gl.y2)
				cur.gs = append(cur.gs, gl)
				continue
			}
		}
		bands = append(bands, band{gl.y1, gl.y2, []glyph{gl}})
	}
	// Punctuation, dots and accents join the row they sit in.
	for _, gl := range small {
		cy := float64(gl.y1+gl.y2) / 2
		best, bd := -1, math.Inf(1)
		for i, bd2 := range bands {
			d := 0.0
			if cy < float64(bd2.a) {
				d = float64(bd2.a) - cy
			} else if cy > float64(bd2.b) {
				d = cy - float64(bd2.b)
			}
			if d < bd {
				best, bd = i, d
			}
		}
		if best >= 0 && bd <= 0.6*ref {
			bands[best].gs = append(bands[best].gs, gl)
		}
	}
	var out []segment
	for ri, bd := range bands {
		row := bd.gs
		sort.SliceStable(row, func(i, j int) bool { return row[i].x1 < row[j].x1 })
		gap := 0.28 * ref
		var cur *segment
		for _, gl := range row {
			if cur != nil && float64(gl.x1-cur.x2) <= gap {
				cur.x2, cur.y1, cur.y2 = max(cur.x2, gl.x2), min(cur.y1, gl.y1), max(cur.y2, gl.y2)
				continue
			}
			out = append(out, segment{gl.x1, gl.y1, gl.x2, gl.y2, ri})
			cur = &out[len(out)-1]
		}
	}
	return out
}

// weight is how wide a word is lettered, in letters: punctuation is narrow.
func weight(w string) float64 {
	n := 0.0
	for _, r := range strings.TrimSpace(w) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		} else {
			n += 0.45
		}
	}
	return math.Max(0.5, n)
}

// layWords lays Spark's words, in reading order, onto the inked segments,
// also in reading order. A word usually is one segment; widely spaced
// letters split a word into two or three, a tight space joins two or three
// words into one, and a stray mark can be left out — whichever makes the
// widths agree best with the words' lengths. Too poor an agreement, or too
// much ink left out, means the ink is not this line (nil).
func layWords(words []sparkWord, segs []segment) []Region {
	n, m := len(words), len(segs)
	if m == 0 {
		return nil
	}
	ws := make([]float64, n)
	total := 0.0
	for i, w := range words {
		ws[i] = weight(w.W)
		total += ws[i]
	}
	inked := 0.0
	for _, s := range segs {
		inked += float64(s.x2 - s.x1)
	}
	unit := inked / total // pixels per letter
	cost := func(width, letters float64) float64 {
		return math.Abs(math.Log(math.Max(1, width) / (unit * letters)))
	}
	// Where rows break must agree too: Spark's x is reliable, so a word
	// that starts left of the one before begins a new row in its reading;
	// in the ink, a segment with nothing sizeable left of it begins a row.
	// Lengths alone cannot tell "honestly" "?" "I was" from "honestly?"
	// "I" "was", and one slip would shift every word after it.
	sparkBreak := make([]bool, n)
	for i := range words {
		sparkBreak[i] = i == 0 || words[i].X1 < words[i-1].X1
	}
	inkBreak := make([]bool, m)
	for j, s := range segs {
		inkBreak[j] = true
		for _, o := range segs {
			if o.row == s.row && o.x1 < s.x1 && float64(o.x2-o.x1) >= 0.5*unit {
				inkBreak[j] = false
				break
			}
		}
	}
	skipCost := func(s segment) float64 {
		return math.Min(0.6, 0.1+0.3*float64(s.x2-s.x1)/unit)
	}
	const breakCost = 0.9
	breaks := func(i, j int) float64 {
		if sparkBreak[i] != inkBreak[j] {
			return breakCost
		}
		return 0
	}
	type step struct {
		words, segs int
	}
	inf := math.Inf(1)
	dp := make([][]float64, n+1)
	from := make([][]step, n+1)
	for i := range dp {
		dp[i] = make([]float64, m+1)
		from[i] = make([]step, m+1)
		for j := range dp[i] {
			dp[i][j] = inf
		}
	}
	dp[0][0] = 0
	for i := 0; i <= n; i++ {
		for j := 0; j <= m; j++ {
			c := dp[i][j]
			if c == inf {
				continue
			}
			relax := func(di, dj int, add float64) {
				if v := c + add; v < dp[i+di][j+dj] {
					dp[i+di][j+dj], from[i+di][j+dj] = v, step{di, dj}
				}
			}
			if j < m {
				// A stray mark nobody says: cheap when it is a speck, and
				// never dearer than a poor fit (whether too much was left
				// out is judged after, row by row).
				relax(0, 1, skipCost(segs[j]))
			}
			if i == n {
				continue
			}
			// One word over one to three segments of a row.
			for k := 1; k <= 3 && j+k <= m && segs[j+k-1].row == segs[j].row; k++ {
				join := 0.0 // a gap narrower than a letter is barely a space
				for q := j + 1; q < j+k; q++ {
					if float64(segs[q].x1-segs[q-1].x2) <= unit {
						join += 0.05
					} else {
						join += 0.35
					}
				}
				relax(1, k, cost(float64(segs[j+k-1].x2-segs[j].x1), ws[i])+join+breaks(i, j))
			}
			// Two or three words sharing one segment (never across a row).
			if j < m {
				sum, br := ws[i], breaks(i, j)
				for k := 2; k <= 3 && i+k <= n; k++ {
					sum += ws[i+k-1]
					if sparkBreak[i+k-1] {
						br += breakCost
					}
					relax(k, 1, cost(float64(segs[j].x2-segs[j].x1), sum)+0.5*float64(k-1)+br)
				}
			}
		}
	}
	if dp[n][m] == inf {
		return nil
	}
	out := make([]Region, n)
	fit := dp[n][m]
	skippedIn, inkedIn, used := map[int]float64{}, map[int]float64{}, map[int]bool{}
	for _, sg := range segs {
		inkedIn[sg.row] += float64(sg.x2 - sg.x1)
	}
	for i, j := n, m; i > 0 || j > 0; {
		st := from[i][j]
		i0, j0 := i-st.words, j-st.segs
		if st.words > 0 {
			used[segs[j0].row] = true
		}
		switch {
		case st.words == 0: // skipped mark
			w := float64(segs[j0].x2 - segs[j0].x1)
			skippedIn[segs[j0].row] += w
			fit -= skipCost(segs[j0])
		case st.words == 1:
			s, e := segs[j0], segs[j-1]
			y1, y2 := s.y1, s.y2
			for _, sg := range segs[j0:j] {
				y1, y2 = min(y1, sg.y1), max(y2, sg.y2)
			}
			out[i0] = Region{s.x1, y1, e.x2, y2}
		default: // several words in one segment, split by their lengths
			s := segs[j0]
			sum := 0.0
			for k := i0; k < i; k++ {
				sum += ws[k]
			}
			acc := float64(s.x1)
			for k := i0; k < i; k++ {
				wv := float64(s.x2-s.x1) * ws[k] / sum
				out[k] = Region{int(acc), s.y1, int(acc + wv), s.y2}
				acc += wv
			}
		}
		i, j = i0, j0
	}
	// The words must fit the ink they were laid on, and the ink left out
	// of the rows they are in must be debris, not words Spark did not
	// read. A row left out whole (a tail's tip, a mark under the text)
	// costs the words nothing.
	skipped, rowsInk := 0.0, 0.0
	for r := range used {
		skipped += skippedIn[r]
		rowsInk += inkedIn[r]
	}
	if fit/float64(n) > 0.75 || skipped > 0.33*rowsInk {
		return nil
	}
	return out
}

// onLightGround reports whether most of the ring x1,y1–x2,y2 (clipped to
// the crop) is light.
func onLightGround(light []bool, ww, hh, x1, y1, x2, y2 int) bool {
	n, lit := 0, 0
	visit := func(x, y int) {
		if x < 0 || y < 0 || x >= ww || y >= hh {
			return
		}
		n++
		if light[y*ww+x] {
			lit++
		}
	}
	for x := x1; x <= x2; x++ {
		visit(x, y1)
		visit(x, y2)
	}
	for y := y1 + 1; y < y2; y++ {
		visit(x1, y)
		visit(x2, y)
	}
	return n == 0 || float64(lit) >= 0.6*float64(n)
}

// resolveSpeaker maps Spark's speaker to a cast member. When Spark names
// nobody in the cast, the line is looked up in the page's script.
func resolveSpeaker(l sparkLine, chars []*store.Character, page *store.Page) string {
	if l.Kind != "bubble" {
		return ""
	}
	name := strings.TrimSpace(l.Speaker)
	for _, c := range chars {
		if strings.EqualFold(c.Name, name) {
			return c.Name
		}
	}
	for _, c := range chars {
		if name != "" && !strings.EqualFold(name, narratorName) && (strings.Contains(strings.ToLower(c.Name), strings.ToLower(name)) || strings.Contains(strings.ToLower(name), strings.ToLower(c.Name))) {
			return c.Name
		}
	}
	best, score := "", 0.0
	text := l.Text
	if text == "" {
		var ws []string
		for _, w := range l.Words {
			ws = append(ws, w.W)
		}
		text = strings.Join(ws, " ")
	}
	for _, p := range page.Panels {
		for _, d := range p.Dialogue {
			if s := similarity(text, d.Text); s > score {
				best, score = d.Character, s
			}
		}
	}
	if score >= 0.5 {
		for _, c := range chars {
			if strings.EqualFold(c.Name, best) {
				return c.Name
			}
		}
	}
	return ""
}

// similarity is the Jaccard overlap of the two texts' words.
func similarity(a, b string) float64 {
	wa, wb := tokens(a), tokens(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(wa)+len(wb)-inter)
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		out[f] = true
	}
	return out
}

// SpokenText is what the voice is asked to say for a line: the lettered
// words, one token per word so the timings map back one to one, with
// comic all-caps turned into sentence case so it is not read as shouting.
func SpokenText(words []LetterWord) string {
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.Text
	}
	s := strings.Join(parts, " ")
	hasLower := strings.IndexFunc(s, unicode.IsLower) >= 0
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if hasLower || letters < 2 {
		return s
	}
	rs := []rune(strings.ToLower(s))
	start := true
	for i, r := range rs {
		if unicode.IsLetter(r) {
			if start {
				rs[i] = unicode.ToUpper(r)
				start = false
			}
		} else if r == '.' || r == '!' || r == '?' {
			start = true
		}
	}
	out := string(rs)
	// "i" on its own is "I".
	f := strings.Fields(out)
	for i, w := range f {
		if strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) == "i" {
			f[i] = strings.Replace(w, "i", "I", 1)
		}
	}
	return strings.Join(f, " ")
}

// AlignWords gives each lettered word its time in the clip. The voice says
// the same tokens (SpokenText), so they pair up one to one; if the counts
// ever differ, words are matched by their position along the line.
func AlignWords(n int, lettered []LetterWord, spoken []WordTiming) [][2]float64 {
	out := make([][2]float64, n)
	if len(spoken) == 0 || n == 0 {
		return out
	}
	if len(spoken) == n {
		for i, w := range spoken {
			out[i] = [2]float64{w.Start, w.End}
		}
		return out
	}
	total, cum := 0, make([]int, n)
	for i, w := range lettered {
		cum[i] = total
		total += len([]rune(w.Text)) + 1
	}
	stotal, scum := 0, make([]int, len(spoken))
	for i, w := range spoken {
		scum[i] = stotal
		stotal += len([]rune(w.Text)) + 1
	}
	for i := range out {
		pos := float64(cum[i]) / float64(max(1, total))
		j := 0
		for j+1 < len(spoken) && float64(scum[j+1])/float64(max(1, stotal)) <= pos {
			j++
		}
		out[i] = [2]float64{spoken[j].Start, spoken[j].End}
	}
	return out
}
