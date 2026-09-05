package pdf

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), 80, uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestWriteProducesAValidPDF(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, "The (Lighthouse) \\ Keeper's Robot é", []Page{{Image: testPNG(20, 30)}, {Image: testPNG(40, 10)}}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.HasPrefix(s, "%PDF-1.4\n") || !strings.HasSuffix(s, "%%EOF\n") {
		t.Fatal("missing header or trailer")
	}
	if strings.Count(s, "/Type /Page ") != 2 || !strings.Contains(s, "/Count 2") || strings.Count(s, "/DCTDecode") != 2 {
		t.Fatalf("expected two JPEG pages:\n%s", s[:400])
	}
	// Page 1 is 20x30 -> 612 wide, 918 tall; page 2 is 40x10 -> 612x153.
	if !strings.Contains(s, "/MediaBox [0 0 612.00 918.00]") || !strings.Contains(s, "/MediaBox [0 0 612.00 153.00]") {
		t.Fatal("page boxes should follow the image aspect ratio at 612pt width")
	}
	if !strings.Contains(s, `/Title (The \(Lighthouse\) \\ Keeper's Robot ?)`) {
		t.Fatal("title should be escaped and non-ASCII replaced")
	}
	// The xref table must point at real object offsets.
	i := strings.LastIndex(s, "startxref\n")
	var xref int
	if _, err := fmtSscan(s[i+len("startxref\n"):], &xref); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s[xref:], "xref\n") {
		t.Fatalf("startxref should point at the xref table, found %q", s[xref:xref+10])
	}
}

func TestWriteRejectsBadImages(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, "x", []Page{{Image: []byte("not an image")}}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func fmtSscan(s string, n *int) (int, error) {
	var v int
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		v = v*10 + int(r-'0')
	}
	*n = v
	return 1, nil
}
