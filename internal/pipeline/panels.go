package pipeline

import (
	"image"
	"sort"
)

// The panels of a drawn page, found in the art itself: pages are drawn
// as a grid of bordered panels on white (or paper-toned) gutters, so the
// page is cut recursively along every gutter that runs right across it —
// rows first, then the panels within each row — until no gutter is left
// (an "XY cut"). What remains and looks like a panel (big enough, framed
// or filled with art) is one; what does not (the title lettered above the
// first panel, a signature in the margin) is left out.
//
// Nothing is asked of a model: it is a few milliseconds of arithmetic on
// the pixels, and where it cannot tell (art bleeding to the edges, a
// balloon breaking across a gutter) it errs towards bigger panels, never
// towards cutting one in two.

// panelScale is the longest side the page is looked at in: gutters are
// still several pixels wide there and the scan is cheap.
const panelScale = 640

// FindPanels returns the panels of a page in reading order (top to bottom,
// then left to right within a row), in fractions of the page. It returns
// nil when the page has no gutters to go by.
func FindPanels(img image.Image) []Box {
	m := newBgMask(img)
	if m == nil {
		return nil
	}
	var leaves []image.Rectangle
	m.cut(image.Rect(0, 0, m.w, m.h), 0, &leaves)
	var out []Box
	for _, r := range leaves {
		if !m.isPanel(r) {
			continue
		}
		out = append(out, Box{
			X1: float64(r.Min.X) / float64(m.w), Y1: float64(r.Min.Y) / float64(m.h),
			X2: float64(r.Max.X) / float64(m.w), Y2: float64(r.Max.Y) / float64(m.h),
		})
	}
	return out
}

// PanelFor is the panel a line of lettering belongs to: the one holding
// most of it, if that is most of the line. A line no panel holds (a title
// over the page, a caption spanning two panels) has none.
func PanelFor(panels []Box, line Box) (int, bool) {
	a := boxArea(line)
	if a <= 0 {
		return -1, false
	}
	best, bestA := -1, 0.0
	for i, p := range panels {
		in := boxArea(Box{max(p.X1, line.X1), max(p.Y1, line.Y1), min(p.X2, line.X2), min(p.Y2, line.Y2)})
		if in > bestA {
			best, bestA = i, in
		}
	}
	if best < 0 || bestA < 0.6*a {
		return -1, false
	}
	return best, true
}

// bgMask marks, on the page scaled down to panelScale, the pixels of the
// gutter colour.
type bgMask struct {
	w, h int
	bg   []bool
}

func (m *bgMask) at(x, y int) bool { return m.bg[y*m.w+x] }

// newBgMask scales the page down (averaging) and marks every pixel close
// to the colour of the page's outer margin. A page whose margin is not
// one colour (art to the edges) has no gutters to find: nil.
func newBgMask(img image.Image) *bgMask {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	if W < 32 || H < 32 {
		return nil
	}
	f := (max(W, H) + panelScale - 1) / panelScale
	w, h := (W+f-1)/f, (H+f-1)/f
	px := make([][3]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, n uint32
			for yy := y * f; yy < min((y+1)*f, H); yy++ {
				for xx := x * f; xx < min((x+1)*f, W); xx++ {
					cr, cg, cb, _ := img.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r, g, bl, n = r+cr>>8, g+cg>>8, bl+cb>>8, n+1
				}
			}
			px[y*w+x] = [3]uint8{uint8(r / n), uint8(g / n), uint8(bl / n)}
		}
	}
	// The margin's colour: the median of a thin ring round the page.
	ring := max(2, min(w, h)/80)
	var rs, gs, bs []int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < ring || y < ring || x >= w-ring || y >= h-ring {
				p := px[y*w+x]
				rs, gs, bs = append(rs, int(p[0])), append(gs, int(p[1])), append(bs, int(p[2]))
			}
		}
	}
	bg := [3]int{median(rs), median(gs), median(bs)}
	near := func(p [3]uint8) bool {
		d := 0
		for c := 0; c < 3; c++ {
			d = max(d, abs(int(p[c])-bg[c]))
		}
		return d <= 30
	}
	m := &bgMask{w: w, h: h, bg: make([]bool, w*h)}
	inRing, onBg := 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.bg[y*w+x] = near(px[y*w+x])
			if x < ring || y < ring || x >= w-ring || y >= h-ring {
				inRing++
				if m.bg[y*w+x] {
					onBg++
				}
			}
		}
	}
	if onBg*10 < inRing*6 {
		return nil
	}
	return m
}

func median(v []int) int {
	sort.Ints(v)
	return v[len(v)/2]
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// gutterRow says whether row y of r is gutter right across: almost all
// background, and background at both ends — a row through a panel starts
// and ends on its border, a gutter between panels does not.
func (m *bgMask) gutterRow(r image.Rectangle, y int) bool {
	return m.gutter(r.Dx(), func(i int) bool { return m.at(r.Min.X+i, y) })
}

func (m *bgMask) gutterCol(r image.Rectangle, x int) bool {
	return m.gutter(r.Dy(), func(i int) bool { return m.at(x, r.Min.Y+i) })
}

func (m *bgMask) gutter(n int, bg func(int) bool) bool {
	if n <= 0 {
		return true
	}
	off := 0
	for i := 0; i < n; i++ {
		if !bg(i) {
			off++
		}
	}
	if off*50 > n { // more than 2% ink: art, or a border crossed
		return false
	}
	// The ends: the outermost pixels are paper, and nearly all of a short
	// stretch inwards — a panel's border, however thin, fails this.
	if n < 4 || !bg(0) || !bg(1) || !bg(n-1) || !bg(n-2) {
		return false
	}
	k := max(4, n/40)
	for _, from := range []int{0, n - k} {
		on := 0
		for i := max(0, from); i < min(n, from+k); i++ {
			if bg(i) {
				on++
			}
		}
		if on*10 < k*9 {
			return false
		}
	}
	return true
}

// trim shrinks r to what is not gutter.
func (m *bgMask) trim(r image.Rectangle) image.Rectangle {
	for r.Dy() > 0 && m.blankRow(r, r.Min.Y) {
		r.Min.Y++
	}
	for r.Dy() > 0 && m.blankRow(r, r.Max.Y-1) {
		r.Max.Y--
	}
	for r.Dx() > 0 && m.blankCol(r, r.Min.X) {
		r.Min.X++
	}
	for r.Dx() > 0 && m.blankCol(r, r.Max.X-1) {
		r.Max.X--
	}
	return r
}

func (m *bgMask) blankRow(r image.Rectangle, y int) bool {
	for x := r.Min.X; x < r.Max.X; x++ {
		if !m.at(x, y) {
			return false
		}
	}
	return true
}

func (m *bgMask) blankCol(r image.Rectangle, x int) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if !m.at(x, y) {
			return false
		}
	}
	return true
}

// minGutter is how thick (scaled pixels) a gutter must be: a stray light
// line inside art is thinner.
const minGutter = 2

// cut splits r along its gutters, rows first, and collects what cannot be
// cut any further.
func (m *bgMask) cut(r image.Rectangle, depth int, out *[]image.Rectangle) {
	r = m.trim(r)
	if r.Empty() {
		return
	}
	if depth < 8 {
		for _, rows := range []bool{true, false} {
			parts := m.split(r, rows)
			if len(parts) > 1 {
				for _, p := range parts {
					m.cut(p, depth+1, out)
				}
				return
			}
		}
	}
	*out = append(*out, r)
}

// split cuts r across every gutter at least minGutter thick, horizontally
// (rows) or vertically.
func (m *bgMask) split(r image.Rectangle, rows bool) []image.Rectangle {
	lo, hi := r.Min.X, r.Max.X
	isGutter := func(i int) bool { return m.gutterCol(r, i) }
	if rows {
		lo, hi = r.Min.Y, r.Max.Y
		isGutter = func(i int) bool { return m.gutterRow(r, i) }
	}
	var parts []image.Rectangle
	part := func(a, b int) {
		if b <= a {
			return
		}
		if rows {
			parts = append(parts, image.Rect(r.Min.X, a, r.Max.X, b))
		} else {
			parts = append(parts, image.Rect(a, r.Min.Y, b, r.Max.Y))
		}
	}
	start := lo
	for i := lo; i < hi; {
		if !isGutter(i) {
			i++
			continue
		}
		j := i
		for j < hi && isGutter(j) {
			j++
		}
		if j-i >= minGutter && i > lo && j < hi {
			part(start, i)
			start = j
		}
		i = j
	}
	if start == lo {
		return nil
	}
	part(start, hi)
	return parts
}

// isPanel tells a panel from the other things between gutters: a panel
// is a sizeable share of the page and is framed or full of art; a title
// or a signature is lettering on the bare paper.
func (m *bgMask) isPanel(r image.Rectangle) bool {
	if r.Dx()*14 < m.w || r.Dy()*14 < m.h {
		return false
	}
	ink := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if !m.at(x, y) {
				ink++
			}
		}
	}
	if ink*10 >= r.Dx()*r.Dy()*3 {
		return true
	}
	// Framed: three of its four edges are mostly ink.
	edge := func(n int, bg func(int) bool) bool {
		off := 0
		for i := 0; i < n; i++ {
			if !bg(i) {
				off++
			}
		}
		return off*10 >= n*7
	}
	framed := 0
	for _, e := range []bool{
		edge(r.Dx(), func(i int) bool { return m.at(r.Min.X+i, r.Min.Y) }),
		edge(r.Dx(), func(i int) bool { return m.at(r.Min.X+i, r.Max.Y-1) }),
		edge(r.Dy(), func(i int) bool { return m.at(r.Min.X, r.Min.Y+i) }),
		edge(r.Dy(), func(i int) bool { return m.at(r.Max.X-1, r.Min.Y+i) }),
	} {
		if e {
			framed++
		}
	}
	return framed >= 3
}
