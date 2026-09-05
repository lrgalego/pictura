// Package pdf writes a minimal PDF: one page per image, each image embedded
// as JPEG (DCTDecode) so no compression code is needed beyond the stdlib.
package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
)

// Page is one image to place on its own PDF page.
type Page struct {
	Image []byte // PNG or JPEG bytes
}

// Write encodes the pages into w. Each page is sized to the image's aspect
// ratio at a 612pt (US letter) width.
func Write(w io.Writer, title string, pages []Page) error {
	type obj struct {
		body []byte
	}
	var objs []obj
	add := func(b []byte) int {
		objs = append(objs, obj{b})
		return len(objs) // 1-based object number
	}
	// Reserve 1 = catalog, 2 = pages tree; filled after the kids exist.
	add(nil)
	add(nil)
	var kids []int
	for _, p := range pages {
		img, _, err := image.Decode(bytes.NewReader(p.Image))
		if err != nil {
			return fmt.Errorf("decode page image: %w", err)
		}
		var jpg bytes.Buffer
		if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 90}); err != nil {
			return err
		}
		bw, bh := img.Bounds().Dx(), img.Bounds().Dy()
		pw := 612.0
		ph := pw * float64(bh) / float64(bw)

		imgObj := add(append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", bw, bh, jpg.Len())), append(jpg.Bytes(), []byte("\nendstream")...)...))
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", pw, ph)
		contentObj := add([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)))
		pageObj := add([]byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pw, ph, imgObj, contentObj)))
		kids = append(kids, pageObj)
	}
	var kidRefs bytes.Buffer
	for _, k := range kids {
		fmt.Fprintf(&kidRefs, "%d 0 R ", k)
	}
	objs[0].body = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	objs[1].body = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kidRefs.String(), len(kids)))
	infoObj := add([]byte(fmt.Sprintf("<< /Title (%s) /Producer (pictura) >>", escape(title))))

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		out.Write(o.body)
		out.WriteString("\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, infoObj, xref)
	_, err := w.Write(out.Bytes())
	return err
}

func escape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 32 || r > 126:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
