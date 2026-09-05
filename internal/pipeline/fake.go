package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/lrgalego/pictura/internal/store"
)

// Fake is an offline AI: a plausible cast and breakdown derived from the
// script text, and drawn placeholder images. It exists so the whole
// workflow runs (and is tested) without an API key.
type Fake struct {
	Delay time.Duration // simulates model latency
}

func (f *Fake) sleep(ctx context.Context) error {
	if f.Delay <= 0 {
		return nil
	}
	select {
	case <-time.After(f.Delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var speakerRe = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z' -]{1,30}?)\s*(?:\([^)]*\))?\s*:`)
var capRe = regexp.MustCompile(`\b([A-Z][a-z]{2,})\b`)

// names guesses the cast from screenplay-style speaker lines, falling back
// to frequent capitalized words.
func names(script string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range speakerRe.FindAllStringSubmatch(script, -1) {
		n := strings.TrimSpace(m[1])
		n = titleCase(n)
		if n == "Narrator" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) >= 2 {
		return out
	}
	count := map[string]int{}
	stop := map[string]bool{"The": true, "And": true, "But": true, "She": true, "They": true, "Then": true, "When": true, "There": true, "This": true, "That": true, "With": true, "From": true, "Into": true, "Once": true, "Her": true, "His": true, "Not": true, "You": true, "Are": true, "Was": true, "Were": true, "For": true, "Had": true, "One": true, "Two": true, "Every": true, "Just": true, "What": true, "Who": true, "How": true, "Now": true, "Ext": true, "Int": true, "Scene": true, "Page": true, "Panel": true, "Cut": true, "Fade": true}
	for _, m := range capRe.FindAllStringSubmatch(script, -1) {
		if !stop[m[1]] {
			count[m[1]]++
		}
	}
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range count {
		if v >= 2 {
			kvs = append(kvs, kv{k, v})
		}
	}
	for i := range kvs {
		for j := i + 1; j < len(kvs); j++ {
			if kvs[j].v > kvs[i].v || (kvs[j].v == kvs[i].v && kvs[j].k < kvs[i].k) {
				kvs[i], kvs[j] = kvs[j], kvs[i]
			}
		}
	}
	for _, e := range kvs {
		if !seen[e.k] {
			seen[e.k] = true
			out = append(out, e.k)
		}
		if len(out) == 4 {
			break
		}
	}
	if len(out) == 0 {
		out = []string{"The Hero", "The Rival"}
	}
	return out
}

var looks = []CharacterSpec{
	{Role: "protagonist", Age: "12 years old", Visual: "Small and wiry with a round face, freckles across the nose, wide hazel eyes and a mop of curly copper hair that never stays put.", Wardrobe: "An oversized mustard-yellow raincoat over a striped tee, rolled jeans and red rubber boots.", Items: "A brass compass on a string and a battered satchel.", Personality: "Curious and stubborn. Leans forward into everything, chin up, hands always busy."},
	{Role: "sidekick", Age: "ageless", Visual: "A stout robot the size of a suitcase, teal enamel body with brass rivets, a single round glass eye that glows amber, and stubby articulated arms.", Wardrobe: "A knitted green scarf someone tied on for it.", Items: "A retractable umbrella stored in its back.", Personality: "Anxious but loyal. Hunches when worried, bounces on its treads when happy."},
	{Role: "antagonist", Age: "late fifties", Visual: "Tall and angular with a hawk nose, slicked silver hair, deep-set grey eyes and a thin scar along the jaw.", Wardrobe: "A charcoal double-breasted coat with a violet lining, black leather gloves, polished boots.", Items: "A silver-topped cane hiding a key.", Personality: "Composed and theatrical. Stands perfectly still, smiles only with one side of the mouth."},
	{Role: "mentor", Age: "seventies", Visual: "Short and broad with warm brown skin, a cloud of white hair, laugh lines and half-moon spectacles.", Wardrobe: "A patched cardigan the color of moss, corduroy trousers, wool socks in sandals.", Items: "A steaming enamel mug that is somehow always full.", Personality: "Unhurried and wry. Sits low, eyebrows do the talking."},
	{Role: "supporting", Age: "twenties", Visual: "Athletic build, dark skin, close-cropped hair dyed cobalt at the tips, a gap-toothed grin.", Wardrobe: "A cropped bomber jacket covered in enamel pins, cargo shorts, high-tops.", Items: "A skateboard with a lightning-bolt deck.", Personality: "Loud, generous, impatient. Talks with the whole body."},
}

func (f *Fake) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	if err := f.sleep(ctx); err != nil {
		return err
	}

	script := after(user, "SCRIPT:")
	if script == "" {
		script = after(user, "SCRIPT (for reference):")
	}
	if script == "" {
		script = user
	}
	var v any
	switch schemaName {
	case "analysis":
		a := Analysis{Title: titleOf(script), Logline: "A small hero, an unlikely friend, and one very big secret.", World: "A rain-slicked harbour town of crooked houses and brass lampposts, somewhere between 1920 and never. Fog most mornings, gulls always.", Characters: cast(script)}
		v = a
	case "characters":
		specs := cast(script)
		if fb := after(user, "WRITER'S FEEDBACK:"); fb != "" {
			line := strings.TrimSpace(strings.SplitN(fb, "\n", 2)[0])
			if !strings.HasPrefix(line, "(no written notes") && !strings.HasPrefix(line, "A finished character sheet") {
				for i := range specs {
					specs[i].Wardrobe = specs[i].Wardrobe + " (revised: " + line + ")"
				}
			}
			if block := after(user, "CHARACTER:"); block != "" && strings.HasPrefix(strings.TrimSpace(block), "- ") {
				name := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(strings.TrimSpace(block), "- "), " (", 2)[0])
				specs = specs[:1]
				specs[0].Name = name
				if len(images) > 0 {
					specs[0].Visual += fmt.Sprintf(" (matched to %d reference image(s))", len(images))
				}
			}
		}
		v = map[string]any{"characters": specs}
	case "pages":
		pages := breakdown(script, cast(script))
		if fb := after(user, "WRITER'S FEEDBACK:"); fb != "" {
			line := strings.TrimSpace(strings.SplitN(fb, "\n", 2)[0])
			for i := range pages {
				pages[i].Summary += " (revised: " + line + ")"
			}
		}
		v = map[string]any{"pages": pages}
	case "page":
		pages := breakdown(script, cast(script))
		p := pages[0]
		if fb := after(user, "WRITER'S FEEDBACK:"); fb != "" {
			p.Summary += " (revised: " + strings.TrimSpace(strings.SplitN(fb, "\n", 2)[0]) + ")"
		}
		v = map[string]any{"page": p}
	default:
		return fmt.Errorf("fake ai: unknown schema %q", schemaName)
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func after(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(s[i+len(marker):])
}

func titleOf(script string) string {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "#*_ "))
		if len(line) > 2 && len(line) < 60 {
			return titleCase(line)
		}
	}
	return "Untitled Story"
}

func cast(script string) []CharacterSpec {
	ns := names(script)
	if len(ns) > len(looks) {
		ns = ns[:len(looks)]
	}
	var out []CharacterSpec
	for i, n := range ns {
		spec := looks[i]
		spec.Name = n
		out = append(out, spec)
	}
	return out
}

func breakdown(script string, chars []CharacterSpec) []PageSpec {
	var paras []string
	for _, p := range strings.Split(script, "\n") {
		if strings.TrimSpace(p) != "" {
			paras = append(paras, strings.TrimSpace(p))
		}
	}
	if len(paras) == 0 {
		paras = []string{"The story begins."}
	}
	n := suggestPages(script)
	if n > 6 {
		n = 6
	}
	if len(paras) < n {
		n = max(1, len(paras))
	}
	per := max(1, len(paras)/n)
	shots := []string{"wide", "medium", "close-up", "low angle", "over-the-shoulder", "bird's-eye"}
	var pages []PageSpec
	for i := 0; i < n; i++ {
		chunk := paras[i*per : min(len(paras), (i+1)*per)]
		if i == n-1 {
			chunk = paras[i*per:]
		}
		pg := PageSpec{Number: i + 1, Title: fmt.Sprintf("Page %d", i+1), Summary: clip(strings.Join(chunk, " "), 110)}
		panels := min(4, max(2, len(chunk)))
		for j := 0; j < panels; j++ {
			line := chunk[min(j, len(chunk)-1)]
			pn := store.Panel{Number: j + 1, Shot: shots[(i+j)%len(shots)], Description: clip(line, 220), Caption: ""}
			if len(chars) > 0 {
				pn.Characters = []string{chars[(i+j)%len(chars)].Name}
				if len(chars) > 1 && j%2 == 1 {
					pn.Characters = append(pn.Characters, chars[(i+j+1)%len(chars)].Name)
				}
				pn.Dialogue = []store.Line{{Character: pn.Characters[0], Text: clip(line, 60)}}
			}
			pg.Panels = append(pg.Panels, pn)
		}
		pages = append(pages, pg)
	}
	return pages
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-1]) + "…"
}

func (f *Fake) GenerateImage(ctx context.Context, prompt, size string) ([]byte, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, err
	}
	return draw(prompt, size, false), nil
}

func (f *Fake) EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error) {
	if err := f.sleep(ctx); err != nil {
		return nil, err
	}
	return draw(prompt, size, true), nil
}

// draw makes a placeholder: a tinted card with a comic panel grid for pages
// or a turnaround silhouette row for sheets, labelled with the first line
// of the prompt.
func draw(prompt, size string, page bool) []byte {
	w, h := 768, 512
	if size == PageSize {
		w, h = 512, 768
	}
	hsh := fnv.New32a()
	hsh.Write([]byte(prompt))
	seed := hsh.Sum32()
	tint := color.RGBA{uint8(120 + seed%100), uint8(90 + (seed>>8)%120), uint8(140 + (seed>>16)%100), 255}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), color.RGBA{250, 246, 236, 255})
	ink := color.RGBA{28, 24, 46, 255}
	if page {
		cols, rows := 2, 3
		m, g := 24, 14
		pw := (w - 2*m - (cols-1)*g) / cols
		ph := (h - 2*m - (rows-1)*g) / rows
		k := 0
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				x, y := m+c*(pw+g), m+r*(ph+g)
				rect := image.Rect(x, y, x+pw, y+ph)
				fill(img, rect, shade(tint, k))
				stroke(img, rect, ink, 4)
				// a "character": circle head + body block
				cx, cy := x+pw/2, y+ph/2
				disc(img, cx, cy-ph/8, pw/8, ink)
				fill(img, image.Rect(cx-pw/8, cy, cx+pw/8, y+ph-10), ink)
				// speech bubble
				bub := image.Rect(x+10, y+10, x+pw*2/3, y+ph/4)
				fill(img, bub, color.RGBA{255, 255, 255, 255})
				stroke(img, bub, ink, 3)
				k++
			}
		}
	} else {
		fill(img, image.Rect(0, 0, w, h), color.RGBA{255, 255, 255, 255})
		stroke(img, image.Rect(8, 8, w-8, h-8), tint, 6)
		n := 4
		cw := (w - 80) / n
		for i := 0; i < n; i++ {
			cx := 40 + i*cw + cw/2
			disc(img, cx, 150, 34, tint)
			fill(img, image.Rect(cx-30, 190, cx+30, 330), tint)
			fill(img, image.Rect(cx-30, 330, cx-6, 420), ink)
			fill(img, image.Rect(cx+6, 330, cx+30, 420), ink)
		}
		for i := 0; i < 5; i++ {
			disc(img, 90+i*((w-180)/4), h-60, 26, shade(tint, i))
		}
	}
	label := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if len(label) > 80 {
		label = label[:80] + "…"
	}
	text(img, 20, h-14, "PLACEHOLDER · "+label, ink)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func shade(c color.RGBA, k int) color.RGBA {
	f := uint8(k * 18)
	return color.RGBA{sat(c.R, f), sat(c.G, f/2), sat(c.B, f), 255}
}

func sat(a, b uint8) uint8 {
	if int(a)+int(b) > 255 {
		return 255
	}
	return a + b
}

func fill(img *image.RGBA, r image.Rectangle, c color.Color) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.Set(x, y, c)
		}
	}
}

func stroke(img *image.RGBA, r image.Rectangle, c color.Color, t int) {
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+t), c)
	fill(img, image.Rect(r.Min.X, r.Max.Y-t, r.Max.X, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+t, r.Max.Y), c)
	fill(img, image.Rect(r.Max.X-t, r.Min.Y, r.Max.X, r.Max.Y), c)
}

func disc(img *image.RGBA, cx, cy, r int, c color.Color) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				img.Set(cx+x, cy+y, c)
			}
		}
	}
}

func text(img *image.RGBA, x, y int, s string, c color.Color) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// titleCase capitalizes the first letter of each word and lowercases the
// rest, leaving apostrophes alone ("keeper's", not "Keeper'S").
func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
