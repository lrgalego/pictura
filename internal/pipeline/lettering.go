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
		rows := wordRows(l.Words)
		line := LetterLine{Kind: l.Kind, Speaker: resolveSpeaker(l, chars, page)}
		var measured [][]Region
		var region *Region
		if match[i] >= 0 {
			region = &regions[match[i]]
			measured = measureWords(g, *region, rows)
		} else {
			// No bubble or caption box (titles, lettering straight on the
			// art): measure around where Spark saw it. Only a reading the
			// ink agrees with is kept; the line box stays Spark's.
			measured = measureWords(g, sparkRegion(l.Words, W, H), rows)
		}
		for ri, row := range rows {
			for wi, w := range row {
				var box Box
				switch {
				case measured != nil:
					r := measured[ri][wi]
					box = Box{float64(r.X1) / W, float64(r.Y1) / H, float64(r.X2) / W, float64(r.Y2) / H}
				default:
					box = sparkBox(w)
				}
				line.Words = append(line.Words, LetterWord{Text: strings.TrimSpace(w.W), Box: box})
			}
		}
		if measured == nil && region != nil {
			line.Words = snapInto(line.Words, regionBox(*region, W, H))
		}
		line.Exact = measured != nil
		if region != nil {
			line.Box = regionBox(*region, W, H)
		} else {
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

// wordRows groups Spark's words into the rows they are lettered in, top to
// bottom, each row left to right.
func wordRows(ws []sparkWord) [][]sparkWord {
	sorted := append([]sparkWord(nil), ws...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Y1 < sorted[b].Y1 })
	var rows [][]sparkWord
	var rowY []int
	for _, w := range sorted {
		h := max(1, w.Y2-w.Y1)
		if n := len(rows); n > 0 && math.Abs(float64(w.Y1-rowY[n-1])) < 0.6*float64(h) {
			rows[n-1] = append(rows[n-1], w)
			continue
		}
		rows = append(rows, []sparkWord{w})
		rowY = append(rowY, w.Y1)
	}
	for _, r := range rows {
		sort.SliceStable(r, func(a, b int) bool { return r[a].X1 < r[b].X1 })
	}
	return rows
}

// inkThreshold separates lettering ink from paper and bubble fill;
// paperThreshold is what counts as the light ground lettering sits on.
const (
	inkThreshold   = 110
	paperThreshold = 170
)

// measureWords finds the exact word boxes inside a region: dark glyphs,
// minus the bubble's outline and tail, split into as many rows and words
// as Spark read. It returns nil when the ink does not support that
// reading (the caller then falls back to Spark's boxes).
func measureWords(g *image.Gray, r Region, rows [][]sparkWord) [][]Region {
	b := g.Bounds()
	r = Region{max(r.X1, b.Min.X), max(r.Y1, b.Min.Y), min(r.X2, b.Max.X), min(r.Y2, b.Max.Y)}
	ww, hh := r.X2-r.X1, r.Y2-r.Y1
	if ww < 4 || hh < 4 || len(rows) == 0 {
		return nil
	}
	ink := make([]bool, ww*hh)
	light := make([]bool, ww*hh)
	for y := 0; y < hh; y++ {
		for x := 0; x < ww; x++ {
			v := g.GrayAt(r.X1+x, r.Y1+y).Y
			ink[y*ww+x] = v < inkThreshold
			light[y*ww+x] = v > paperThreshold
		}
	}
	ink = dropOutlines(ink, light, ww, hh)

	rowMass := make([]int, hh)
	for y := 0; y < hh; y++ {
		for x := 0; x < ww; x++ {
			if ink[y*ww+x] {
				rowMass[y]++
			}
		}
	}
	type run struct{ a, b, mass int }
	var runs []run
	for y := 0; y < hh; {
		if rowMass[y] == 0 {
			y++
			continue
		}
		s, m := y, 0
		for y < hh && rowMass[y] > 0 {
			m += rowMass[y]
			y++
		}
		runs = append(runs, run{s, y, m})
	}
	if len(runs) < len(rows) {
		return nil
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].mass > runs[j].mass })
	runs = runs[:len(rows)]
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].a < runs[j].a })

	out := make([][]Region, len(rows))
	for ri, rn := range runs {
		want := rows[ri]
		col := make([]int, ww)
		total := 0
		for y := rn.a; y < rn.b; y++ {
			for x := 0; x < ww; x++ {
				if ink[y*ww+x] {
					col[x]++
					total++
				}
			}
		}
		mass := func(s [2]int) int {
			m := 0
			for x := s[0]; x < s[1]; x++ {
				m += col[x]
			}
			return m
		}
		var segs [][2]int
		for x := 0; x < ww; {
			if col[x] == 0 {
				x++
				continue
			}
			s := x
			for x < ww && col[x] > 0 {
				x++
			}
			if float64(mass([2]int{s, x})) > 0.01*float64(total) {
				segs = append(segs, [2]int{s, x})
			}
		}
		// Too many pieces: drop specks (outline fragments), then join the
		// narrowest gaps, which are the gaps between letters.
		for len(segs) > len(want) {
			masses := make([]int, len(segs))
			for i, s := range segs {
				masses[i] = mass(s)
			}
			sorted := append([]int(nil), masses...)
			sort.Ints(sorted)
			med := sorted[len(sorted)/2]
			j := 0
			for i := range masses {
				if masses[i] < masses[j] {
					j = i
				}
			}
			// A speck far from everything is outline debris; one close to
			// a word is punctuation and joins it through the merging below.
			near := math.Inf(1)
			if j > 0 {
				near = float64(segs[j][0] - segs[j-1][1])
			}
			if j+1 < len(segs) {
				near = math.Min(near, float64(segs[j+1][0]-segs[j][1]))
			}
			if float64(masses[j]) < 0.25*float64(med) && near > 0.6*float64(rn.b-rn.a) {
				segs = append(segs[:j], segs[j+1:]...)
				continue
			}
			k := 0
			for i := 0; i < len(segs)-1; i++ {
				if segs[i+1][0]-segs[i][1] < segs[k+1][0]-segs[k][1] {
					k = i
				}
			}
			segs[k] = [2]int{segs[k][0], segs[k+1][1]}
			segs = append(segs[:k+1], segs[k+2:]...)
		}
		if len(segs) < len(want) {
			return nil
		}
		// The widths must resemble Spark's; otherwise (outlined display
		// lettering, say) share the row's ink span in Spark's proportions.
		if !widthsAgree(segs, want) {
			lo, hi := segs[0][0], segs[len(segs)-1][1]
			tw := 0
			for _, w := range want {
				tw += max(1, w.X2-w.X1)
			}
			acc := float64(lo)
			for i, w := range want {
				wv := float64(hi-lo) * float64(max(1, w.X2-w.X1)) / float64(tw)
				segs[i] = [2]int{int(acc), int(acc + wv)}
				acc += wv
			}
		}
		for _, s := range segs {
			top, bot := rn.b, rn.a
			for y := rn.a; y < rn.b; y++ {
				for x := s[0]; x < s[1]; x++ {
					if ink[y*ww+x] {
						top, bot = min(top, y), max(bot, y+1)
						break
					}
				}
			}
			if bot <= top {
				top, bot = rn.a, rn.b
			}
			out[ri] = append(out[ri], Region{r.X1 + s[0], r.Y1 + top, r.X1 + s[1], r.Y1 + bot})
		}
	}
	return out
}

func widthsAgree(segs [][2]int, want []sparkWord) bool {
	td, te := 0, 0
	for i, s := range segs {
		td += s[1] - s[0]
		te += max(1, want[i].X2-want[i].X1)
	}
	for i, s := range segs {
		ratio := (float64(s[1]-s[0]) / float64(td)) / (float64(max(1, want[i].X2-want[i].X1)) / float64(te))
		if ratio <= 0.5 || ratio >= 2 {
			return false
		}
	}
	return true
}

// dropOutlines removes connected ink that is not lettering: the bubble's
// outline and tail (wide, or tall and wide), specks, and dark shapes of
// the art that poke into the box (a glyph is thin strokes on a light
// ground, so the ring just around it is mostly light; an ear is not).
func dropOutlines(ink, light []bool, ww, hh int) []bool {
	seen := make([]bool, len(ink))
	keep := make([]bool, len(ink))
	var stack, pts []int
	for i := range ink {
		if !ink[i] || seen[i] {
			continue
		}
		stack, pts = append(stack[:0], i), pts[:0]
		seen[i] = true
		minX, minY, maxX, maxY := ww, hh, -1, -1
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pts = append(pts, p)
			x, y := p%ww, p/ww
			minX, maxX, minY, maxY = min(minX, x), max(maxX, x), min(minY, y), max(maxY, y)
			for _, q := range [4]int{p - 1, p + 1, p - ww, p + ww} {
				if q < 0 || q >= len(ink) || seen[q] || !ink[q] {
					continue
				}
				if (q == p-1 && x == 0) || (q == p+1 && x == ww-1) {
					continue
				}
				seen[q] = true
				stack = append(stack, q)
			}
		}
		bw, bh := maxX-minX+1, maxY-minY+1
		if float64(bw) > 0.5*float64(ww) || (float64(bh) > 0.6*float64(hh) && float64(bw) > 0.2*float64(ww)) || len(pts) < 4 {
			continue
		}
		if !onLightGround(light, ww, hh, minX-2, minY-2, maxX+2, maxY+2) {
			continue
		}
		for _, p := range pts {
			keep[p] = true
		}
	}
	return keep
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
