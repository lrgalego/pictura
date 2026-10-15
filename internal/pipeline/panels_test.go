package pipeline

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"os"
	"testing"
)

// Every recorded page: the panels found in the art are the panels Muse
// Spark numbered, and each line of lettering lands in the panel Spark
// read it in. The title over the first panel is in none.
func TestFindPanelsOnDrawnPages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		panels int
	}{
		{"manga", 3},
		{"meadow", 4},
		{"rows", 5},
		{"storybook", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, fx := loadFixture(t, tc.name)
			f, err := os.Open("testdata/lettering/" + tc.name + ".jpg")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			img, _, err := image.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			panels := FindPanels(img)
			if grey := FindPanels(g); len(grey) != len(panels) {
				t.Fatalf("in grey %d panels, in colour %d", len(grey), len(panels))
			}
			if len(panels) != tc.panels {
				t.Fatalf("found %d panels, want %d: %+v", len(panels), tc.panels, panels)
			}
			for i, p := range panels {
				if p.X2-p.X1 < 0.2 || p.Y2-p.Y1 < 0.15 || p.X1 < 0 || p.Y1 < 0 || p.X2 > 1 || p.Y2 > 1 {
					t.Fatalf("panel %d is not a panel: %+v", i+1, p)
				}
				for j, q := range panels[:i] {
					if boxArea(Box{max(p.X1, q.X1), max(p.Y1, q.Y1), min(p.X2, q.X2), min(p.Y2, q.Y2)}) > 0 {
						t.Fatalf("panels %d and %d overlap: %+v %+v", j+1, i+1, q, p)
					}
				}
			}
			for _, l := range fx.Reading.Lines {
				got, ok := PanelFor(panels, sparkUnion(l.Words))
				if l.Kind == "title" {
					if ok && l.Panel == 0 {
						t.Errorf("the title %q should be in no panel, got %d", l.Text, got+1)
					}
					continue
				}
				if !ok || got+1 != l.Panel {
					t.Errorf("%q: panel %d (%v), Spark read it in panel %d", l.Text, got+1, ok, l.Panel)
				}
			}
		})
	}
}

// The placeholder pages are a 2×3 grid: six panels, each exactly where it
// was drawn, and the "PLACEHOLDER" line in the bottom margin is not one.
func TestFindPanelsOnAPlaceholderPage(t *testing.T) {
	img, _, err := image.Decode(bytes.NewReader(draw("A test page\nPANEL 1 (wide shot): x", PageSize, true)))
	if err != nil {
		t.Fatal(err)
	}
	panels := FindPanels(img)
	if len(panels) != fakeCols*fakeRows {
		t.Fatalf("found %d panels: %+v", len(panels), panels)
	}
	for k, p := range panels {
		x, y, pw, ph := fakeCell(k)
		want := Box{float64(x) / fakeW, float64(y) / fakeH, float64(x+pw) / fakeW, float64(y+ph) / fakeH}
		for _, d := range []float64{p.X1 - want.X1, p.Y1 - want.Y1, p.X2 - want.X2, p.Y2 - want.Y2} {
			if math.Abs(d) > 0.01 {
				t.Fatalf("panel %d at %+v, drawn at %+v", k+1, p, want)
			}
		}
	}
}

// Art to the edges has no gutters to go by; a blank page has no panels.
func TestFindPanelsWithoutGutters(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 300, 450))
	for y := 0; y < 450; y++ {
		for x := 0; x < 300; x++ {
			full.Set(x, y, color.RGBA{uint8(x), uint8(y / 2), uint8((x + y) / 3), 255})
		}
	}
	if p := FindPanels(full); p != nil {
		t.Fatalf("full-bleed art: %+v", p)
	}
	blank := image.NewRGBA(image.Rect(0, 0, 300, 450))
	fill(blank, blank.Bounds(), color.RGBA{255, 255, 255, 255})
	if p := FindPanels(blank); len(p) != 0 {
		t.Fatalf("blank page: %+v", p)
	}
	// A single framed panel with a white band across it (a sky) is one
	// panel, not two: the band ends on the frame.
	one := image.NewRGBA(image.Rect(0, 0, 300, 450))
	fill(one, one.Bounds(), color.RGBA{255, 255, 255, 255})
	fill(one, image.Rect(20, 200, 280, 430), color.RGBA{90, 120, 60, 255})
	stroke(one, image.Rect(20, 20, 280, 430), color.RGBA{0, 0, 0, 255}, 2)
	if p := FindPanels(one); len(p) != 1 || p[0].Y1 > 0.06 || p[0].Y2 < 0.94 {
		t.Fatalf("one framed panel: %+v", p)
	}
}

func TestPanelFor(t *testing.T) {
	panels := []Box{{0, 0, 0.5, 0.5}, {0.5, 0, 1, 0.5}, {0, 0.5, 1, 1}}
	for _, tc := range []struct {
		line Box
		want int
	}{
		{Box{0.1, 0.1, 0.3, 0.2}, 0},
		{Box{0.6, 0.1, 0.8, 0.2}, 1},
		{Box{0.45, 0.1, 0.7, 0.2}, 1},   // mostly in the second
		{Box{0.3, 0.1, 0.7, 0.2}, -1},   // half and half: spans both
		{Box{0.1, 0.45, 0.3, 0.55}, -1}, // across the gutter
		{Box{0.2, 0.7, 0.4, 0.8}, 2},
		{Box{0.2, 0.7, 0.2, 0.8}, -1}, // no area
	} {
		got, ok := PanelFor(panels, tc.line)
		if (tc.want < 0) == ok || (ok && got != tc.want) {
			t.Errorf("%+v: %d %v, want %d", tc.line, got, ok, tc.want)
		}
	}
	if _, ok := PanelFor(nil, Box{0.1, 0.1, 0.2, 0.2}); ok {
		t.Error("no panels: no panel")
	}
}
