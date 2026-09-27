package pipeline

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"regexp"
	"strconv"
	"strings"
)

// The placeholder pages are lettered for real (a bitmap font in white
// bubbles and caption boxes), and the fake reading and segmentation answer
// with the same layout, so the whole read-aloud pipeline, ink measuring
// included, runs offline.

// fakePanel is what one grid cell of a placeholder page letters.
type fakePanel struct {
	Caption, Speaker, Text string
}

const (
	fakeCols, fakeRows  = 2, 3
	fakeMargin, fakeGap = 24, 14
	fakeW, fakeH        = 512, 768
	fakeAdvance         = 7  // basicfont.Face7x13
	fakeLine            = 14 // row pitch
	fakeAscent          = 11
)

func fakeCell(k int) (x, y, pw, ph int) {
	pw = (fakeW - 2*fakeMargin - (fakeCols-1)*fakeGap) / fakeCols
	ph = (fakeH - 2*fakeMargin - (fakeRows-1)*fakeGap) / fakeRows
	c, r := k%fakeCols, k/fakeCols
	return fakeMargin + c*(pw+fakeGap), fakeMargin + r*(ph+fakeGap), pw, ph
}

func fakeBubbleRect(k int) image.Rectangle {
	x, y, pw, ph := fakeCell(k)
	return image.Rect(x+10, y+10, x+pw*2/3, y+ph/4)
}

func fakeCaptionRect(k int) image.Rectangle {
	x, y, pw, ph := fakeCell(k)
	return image.Rect(x+10, y+ph-36, x+pw-10, y+ph-12)
}

type fakeWord struct {
	Text string
	R    image.Rectangle
}

// fakeLetter lays text out centred in r, wrapping on words; what does not
// fit is dropped.
func fakeLetter(text string, r image.Rectangle) []fakeWord {
	const pad = 6
	maxChars := (r.Dx() - 2*pad) / fakeAdvance
	maxRows := max(1, (r.Dy()-2*pad)/fakeLine)
	var rows [][]string
	cur := 0
	for _, w := range strings.Fields(text) {
		if len(w) > maxChars {
			w = w[:maxChars]
		}
		if len(rows) > 0 && cur+1+len(w) <= maxChars {
			rows[len(rows)-1] = append(rows[len(rows)-1], w)
			cur += 1 + len(w)
			continue
		}
		if len(rows) == maxRows {
			break
		}
		rows = append(rows, []string{w})
		cur = len(w)
	}
	top := r.Min.Y + (r.Dy()-len(rows)*fakeLine)/2
	var out []fakeWord
	for i, row := range rows {
		width := (len(strings.Join(row, " "))) * fakeAdvance
		x := r.Min.X + (r.Dx()-width)/2
		base := top + i*fakeLine + fakeAscent
		for _, w := range row {
			out = append(out, fakeWord{w, image.Rect(x, base-fakeAscent, x+len(w)*fakeAdvance, base+2)})
			x += (len(w) + 1) * fakeAdvance
		}
	}
	return out
}

var (
	quoted        = `("(?:[^"\\]|\\.)*")`
	promptCaption = regexp.MustCompile(`Caption box: ` + quoted)
	promptBubble  = regexp.MustCompile(`Speech bubble from ([^:]+): ` + quoted)
	scriptCaption = regexp.MustCompile(`(?m)^Panel (\d+) — caption: ` + quoted)
	scriptBubble  = regexp.MustCompile(`(?m)^Panel (\d+) — (.+?) says: ` + quoted)
)

// fakePanelsFromPrompt reads the page prompt (PagePrompt): per panel, the
// caption and the first speech bubble.
func fakePanelsFromPrompt(prompt string) map[int]fakePanel {
	out := map[int]fakePanel{}
	parts := strings.Split(prompt, "\nPANEL ")
	for i, part := range parts[1:] {
		var p fakePanel
		if m := promptCaption.FindStringSubmatch(part); m != nil {
			p.Caption, _ = strconv.Unquote(m[1])
		}
		if m := promptBubble.FindStringSubmatch(part); m != nil {
			p.Speaker = m[1]
			p.Text, _ = strconv.Unquote(m[2])
		}
		out[i] = p
	}
	return out
}

// fakePanelsFromScript reads PageScript, as embedded in the lettering prompt.
func fakePanelsFromScript(script string) map[int]fakePanel {
	out := map[int]fakePanel{}
	for _, m := range scriptCaption.FindAllStringSubmatch(script, -1) {
		n, _ := strconv.Atoi(m[1])
		p := out[n-1]
		p.Caption, _ = strconv.Unquote(m[2])
		out[n-1] = p
	}
	for _, m := range scriptBubble.FindAllStringSubmatch(script, -1) {
		n, _ := strconv.Atoi(m[1])
		p := out[n-1]
		if p.Text == "" {
			p.Speaker = m[2]
			p.Text, _ = strconv.Unquote(m[3])
		}
		out[n-1] = p
	}
	return out
}

// letterFakePage draws the lettering of a placeholder page.
func letterFakePage(img *image.RGBA, panels map[int]fakePanel) {
	ink := color.RGBA{28, 24, 46, 255}
	white := color.RGBA{255, 255, 255, 255}
	for k := 0; k < fakeCols*fakeRows; k++ {
		p := panels[k]
		if p.Caption != "" {
			r := fakeCaptionRect(k)
			fill(img, r, white)
			stroke(img, r, ink, 2)
			for _, w := range fakeLetter(p.Caption, r) {
				text(img, w.R.Min.X, w.R.Min.Y+fakeAscent, w.Text, ink)
			}
		}
		for _, w := range fakeLetter(p.Text, fakeBubbleRect(k)) {
			text(img, w.R.Min.X, w.R.Min.Y+fakeAscent, w.Text, ink)
		}
	}
}

// fakeLettering is the fake Muse Spark reading of a placeholder page.
func fakeLettering(user string) map[string]any {
	panels := fakePanelsFromScript(after(user, "SCRIPT FOR THIS PAGE"))
	norm := func(r image.Rectangle) map[string]int {
		return map[string]int{"x1": r.Min.X * 1000 / fakeW, "y1": r.Min.Y * 1000 / fakeH, "x2": r.Max.X * 1000 / fakeW, "y2": r.Max.Y * 1000 / fakeH}
	}
	words := func(ws []fakeWord) []map[string]any {
		var out []map[string]any
		for _, w := range ws {
			m := map[string]any{"w": w.Text}
			for k, v := range norm(w.R) {
				m[k] = v
			}
			out = append(out, m)
		}
		return out
	}
	var lines []map[string]any
	for k := 0; k < fakeCols*fakeRows; k++ {
		p, ok := panels[k]
		if !ok {
			continue
		}
		if p.Caption != "" {
			ws := fakeLetter(p.Caption, fakeCaptionRect(k))
			lines = append(lines, map[string]any{"panel": k + 1, "kind": "caption", "speaker": narratorName, "text": p.Caption, "words": words(ws)})
		}
		if p.Text != "" {
			ws := fakeLetter(p.Text, fakeBubbleRect(k))
			lines = append(lines, map[string]any{"panel": k + 1, "kind": "bubble", "speaker": p.Speaker, "text": p.Text, "words": words(ws)})
		}
	}
	return map[string]any{"lines": lines}
}

// Segment is the fake SAM: on a placeholder page every cell has a bubble,
// and a caption box where one was drawn (its corner is white).
func (f *Fake) Segment(ctx context.Context, png []byte, phrase string) ([]Region, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil || img.Bounds().Dx() != fakeW || img.Bounds().Dy() != fakeH {
		return nil, nil
	}
	var out []Region
	add := func(r image.Rectangle) { out = append(out, Region{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y}) }
	for k := 0; k < fakeCols*fakeRows; k++ {
		add(fakeBubbleRect(k))
		if phrase == "caption" {
			c := fakeCaptionRect(k)
			if r, g, b, _ := img.At(c.Min.X+3, c.Min.Y+3).RGBA(); r>>8 == 255 && g>>8 == 255 && b>>8 == 255 {
				add(c)
			}
		}
	}
	return out, nil
}
