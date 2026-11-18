package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg" // Nano Banana answers in JPEG
	"image/png"
	"strconv"
	"strings"

	"golang.org/x/image/draw"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/usage"
)

// GenerateImage renders a prompt to a PNG of the given size ("1024x1536").
func (c *Client) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	return c.EditImage(ctx, prompt, nil, size)
}

// EditImage renders a prompt conditioned on reference images, which is how
// character sheets keep a page on-model. Each goes in right after its label
// ("Reference image 2: the character sheet of Emmy"), which binds a sheet
// to its character far better than numbering them in the prompt alone.
//
// Nano Banana draws in an aspect ratio, not a size: the answer (a 2K JPEG,
// which costs what a 1K one does) is scaled to the size asked for, so its
// pages and sheets match Muse Image's and the reading pipeline sees the
// same pixels whichever model drew them.
func (c *Client) EditImage(ctx context.Context, prompt string, refs []pipeline.Image, size string) ([]byte, error) {
	var parts []part
	for i, r := range refs {
		label := r.Label
		if label == "" {
			label = fmt.Sprintf("Reference image %d:", i+1)
		}
		parts = append(parts, part{Text: label}, part{InlineData: &blob{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(r.PNG)}})
	}
	parts = append(parts, part{Text: prompt})
	w, h := dimensions(size)
	req := request{
		Contents: []content{{Role: "user", Parts: parts}},
		GenerationConfig: generationConfig{
			ResponseModalities: []string{"IMAGE"},
			ImageConfig:        &imageConfig{AspectRatio: aspect(w, h), ImageSize: "2K"},
		},
	}
	resp, err := c.generate(ctx, c.ImageModel, req, usage.Call{Kind: usage.Image, Op: imageOp(size)})
	if err != nil {
		return nil, err
	}
	answer, err := resp.answer()
	if err != nil {
		return nil, err
	}
	var said []string
	for _, p := range answer {
		if p.InlineData != nil && strings.HasPrefix(p.InlineData.MimeType, "image/") {
			raw, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil {
				return nil, fmt.Errorf("gemini %s: %w", c.ImageModel, err)
			}
			return toPNG(raw, w, h)
		}
		if p.Text != "" {
			said = append(said, p.Text)
		}
	}
	msg := fmt.Sprintf("gemini %s: no image returned (finish reason %s)", c.ImageModel, resp.finish())
	if len(said) > 0 {
		msg += ": " + truncate(strings.Join(said, " "), 300)
	}
	return nil, fmt.Errorf("%s", msg)
}

// imageOp names the agent an image is drawn for from its shape.
func imageOp(size string) string {
	switch size {
	case pipeline.SheetSize:
		return "sheet"
	case pipeline.PageSize:
		return "page"
	}
	return "image"
}

// dimensions reads "WxH"; anything else is a square.
func dimensions(size string) (int, int) {
	ws, hs, ok := strings.Cut(size, "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 1024, 1024
	}
	return w, h
}

// aspect is the supported ratio closest to w:h.
func aspect(w, h int) string {
	ratios := []struct {
		name string
		v    float64
	}{{"1:1", 1}, {"2:3", 2.0 / 3}, {"3:2", 1.5}, {"3:4", 0.75}, {"4:3", 4.0 / 3}, {"4:5", 0.8}, {"5:4", 1.25}, {"9:16", 9.0 / 16}, {"16:9", 16.0 / 9}, {"21:9", 21.0 / 9}}
	want := float64(w) / float64(h)
	best, diff := "1:1", 1e9
	for _, r := range ratios {
		d := r.v - want
		if d < 0 {
			d = -d
		}
		if d < diff {
			best, diff = r.name, d
		}
	}
	return best
}

// toPNG decodes an image and scales it to w×h as a PNG.
func toPNG(raw []byte, w, h int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gemini: unreadable image: %w", err)
	}
	var img image.Image = src
	if b := src.Bounds(); b.Dx() != w || b.Dy() != h {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Segment is SAM's job; Gemini is never asked.
func (c *Client) Segment(context.Context, []byte, string) ([]pipeline.Region, error) {
	return nil, fmt.Errorf("gemini: segmentation is not offered")
}
