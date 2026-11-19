package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lrgalego/pictura/internal/store"
)

// The world is the places a story happens in and the props it handles.
// Each gets concept art before the storyboard, as each character gets a
// sheet: drawn from words alone, a page artist guesses a kitchen or a pie
// anew on every page, so its shape, colors and layout drift.

var elementSchema = obj(map[string]any{
	"kind":   map[string]any{"type": "string", "enum": []string{store.KindPlace, store.KindProp}, "description": "place: a location scenes happen in; prop: an object or vehicle the story handles"},
	"name":   str("Short and specific, in the script's own words, the name every panel will use (\"the farmhouse kitchen\", \"Grandma's apple pie\")"),
	"visual": str("What it looks like, concretely enough to draw it the same every time: shape, size, materials, colors, condition, distinctive marks. Always name the colors, even for a black-and-white look"),
	"layout": str("For a place, its geography as a fixed plan: what is inside and what is outside, where the doors, windows, paths and main furniture are, what is left and right from the main view, how it connects to the places next to it. For a prop, its size against a person, its parts, and how it is held or used"),
}, "kind", "name", "visual", "layout")

var worldSchema = obj(map[string]any{
	"elements": arr(elementSchema),
}, "elements")

// ElementSpec is a place or a prop as the designer writes it.
type ElementSpec struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Visual string `json:"visual"`
	Layout string `json:"layout"`
}

const designerPersona = `You are the production designer of an illustrated comic adaptation. Before any page is drawn you design the places the story happens in and the props it handles, so they look the same on every page — the way character sheets keep the cast the same.

What to design:
- Places: every location where a scene happens (a scene heading, "they reached…", a room the action moves into). A place seen inside and outside in one scene (a cottage and its garden, a car and the road) is one place, so its layout can say what is inside and what is outside. A place that transforms (a studio that turns into a dungeon) gets one entry per look.
- Props: objects the story turns on or shows more than once — something carried, given, eaten, broken, chased or ridden (a pie, a map, a car, a coaster cart, a magic key). Vehicles count. Skip clothing and what a character always carries (their character sheet shows it) unless the plot hands it around. Skip background clutter.
- Never characters, animals or creatures that act: they are the cast.

How to write it:
- Name each one with the script's own words, short and specific, in natural capitalization: the name every panel will use.
- Describe what things look like, never how they are drawn: no art style, medium, linework or rendering — the writer picks the look separately and can change it. Always name colors and materials.
- Follow every detail the script gives (a red door, a pie cooling on the windowsill, three carts on one track) and invent the rest to fit the world.
- A place's layout is a plan the artist keeps for every panel there: decide where everything is once, so a character can only leave through a door that exists and something outside the window stays outside.

Order places by first appearance, then props by first appearance. At most 8 places and 10 props: keep the ones that matter most to the story. Answer only with JSON matching the schema.`

// DesignWorld finds the places and props of a story and describes them.
func DesignWorld(ctx context.Context, ai AI, story *store.Story, chars []*store.Character) ([]ElementSpec, error) {
	user := fmt.Sprintf("Title: %s\nWorld: %s\n\nCAST (already designed; never list them):\n%s\n\nSCRIPT:\n%s",
		story.Title, story.World, castNames(chars), story.Script)
	return askWorld(ctx, ai, user, true)
}

// AdjustWorld revises the whole world according to the writer's feedback.
func AdjustWorld(ctx context.Context, ai AI, story *store.Story, chars []*store.Character, world []*store.Element, feedback string) ([]ElementSpec, error) {
	user := fmt.Sprintf("World: %s\n\nCAST (never list them):\n%s\n\nCURRENT PLACES AND PROPS:\n%s\n\nWRITER'S FEEDBACK:\n%s\n\nReturn the full revised list (the same places and props unless the feedback adds or removes some), applying the feedback and keeping everything else unchanged, names included.\n\nSCRIPT (for reference):\n%s",
		story.World, castNames(chars), describeWorld(world), feedback, story.Script)
	return askWorld(ctx, ai, user, true)
}

// AdjustElement revises one place or prop according to feedback.
func AdjustElement(ctx context.Context, ai AI, story *store.Story, e *store.Element, feedback string) (*ElementSpec, error) {
	user := fmt.Sprintf("World: %s\n\nELEMENT:\n%s\n\nWRITER'S FEEDBACK:\n%s\n\nReturn this one %s revised per the feedback, as a list of one; keep the name unless asked to change it, and keep every detail the feedback does not touch.\n\nSCRIPT (for reference):\n%s",
		story.World, describeElement(e), feedback, e.Kind, story.Script)
	specs, err := askWorld(ctx, ai, user, false)
	if err != nil {
		return nil, err
	}
	specs[0].Kind = e.Kind
	return &specs[0], nil
}

func askWorld(ctx context.Context, ai AI, user string, many bool) ([]ElementSpec, error) {
	var out struct {
		Elements []ElementSpec `json:"elements"`
	}
	if err := ai.ChatJSON(ctx, designerPersona, user, nil, "world", worldSchema, &out); err != nil {
		return nil, err
	}
	var specs []ElementSpec
	for _, s := range out.Elements {
		s.Name = strings.TrimSpace(s.Name)
		if s.Name == "" {
			continue
		}
		if s.Kind != store.KindPlace {
			s.Kind = store.KindProp
		}
		specs = append(specs, s)
	}
	if len(specs) == 0 {
		if many {
			return nil, fmt.Errorf("the designer found no places or props in the script")
		}
		return nil, fmt.Errorf("the model returned no element")
	}
	return specs, nil
}

func castNames(chars []*store.Character) string {
	if len(chars) == 0 {
		return "(none)"
	}
	names := make([]string, len(chars))
	for i, c := range chars {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

func describeElement(e *store.Element) string {
	label := "Layout"
	if !e.IsPlace() {
		label = "Size and use"
	}
	return fmt.Sprintf("- %s (%s)\n  Appearance: %s\n  %s: %s", e.Name, e.Kind, e.Visual, label, e.Layout)
}

func describeWorld(world []*store.Element) string {
	if len(world) == 0 {
		return "(none)"
	}
	parts := make([]string, len(world))
	for i, e := range world {
		parts[i] = describeElement(e)
	}
	return strings.Join(parts, "\n")
}

// worldBlock is the list of designed places and props handed to the
// storyboard, so its settings and props use the names the art is filed
// under. Empty when the story has none.
func worldBlock(world []*store.Element) string {
	if len(world) == 0 {
		return ""
	}
	return "\n\nPLACES AND PROPS (designed already; name them exactly like this):\n" + describeWorld(world)
}

// ---------- concept art ----------

// ElementPrompt is the concept-art prompt for a place or a prop.
func ElementPrompt(story *store.Story, e *store.Element) string {
	return elementBody(story, e)
}

// RestyleElementPrompt redraws finished concept art in a new look: the
// attached image is the old art, whose design carries over.
func RestyleElementPrompt(story *store.Story, e *store.Element) string {
	return "The attached image is the current concept art of this " + e.Kind + ", drawn in a different art style. Keep the design exactly — shapes, layout, materials and colors — but redraw everything in the requested style: line, color, shading and rendering follow the new style, not the old art. " + elementBody(story, e)
}

// ElementRefs labels the old art a restyle is drawn from.
func ElementRefs(e *store.Element, old []byte) []Image {
	return []Image{{Label: fmt.Sprintf("Reference image 1: the current concept art of %s, in the old style. Keep the design, not the style.", e.Name), PNG: old}}
}

func elementBody(story *store.Story, e *store.Element) string {
	style := StyleBySlug(story.Style).Direction
	if e.IsPlace() {
		return fmt.Sprintf(`Environment concept art (a location design sheet) for a comic, %s. Place: %s.
Appearance: %s
Layout: %s
Layout of the sheet, on a plain white background with no text other than the place's name as a small title: a large establishing view of the whole place across the top that shows its layout clearly; below it, two smaller views — the reverse angle, seen from the other side, and a simple top-down plan with the doors, windows, paths and main furniture where the layout puts them. No people or animals: the place is empty, ready for the cast. Keep every view consistent — the same architecture, colors and furniture in the same places. World: %s`,
			style, e.Name, e.Visual, e.Layout, story.World)
	}
	return fmt.Sprintf(`Prop design sheet (model sheet) for a comic, %s. Object: %s.
Appearance: %s
Size and use: %s
Layout, on a plain white background with no text other than the object's name as a small title: the object in a row of views (front, three-quarter, side, and back or top), one or two close-ups of its distinctive details, and one small view of it beside a plain grey human silhouette for scale. No characters. Keep every view perfectly consistent — the same shape, proportions, materials and colors. World: %s`,
		style, e.Name, e.Visual, e.Layout, story.World)
}

// ---------- on the page ----------

// MaxWorldRefs is how many places and props ride along with a page, and
// MaxRefs how many reference images a page takes in all: Muse Image
// composes from ten. The cast comes first; the world fills what is left.
const (
	MaxWorldRefs = 4
	MaxRefs      = 10
)

// PageWorld picks the places and props whose concept art rides along with
// a page: the places its panels are set in, then the props they show, the
// most present first, at most max of them. A panel names a place by its
// setting and a prop in its props; a storyboard planned before the world
// was designed is matched by the names in its words.
func PageWorld(world []*store.Element, page *store.Page, max int) []*store.Element {
	weight := map[int64]int{}
	for _, pn := range page.Panels {
		words := strings.ToLower(pn.Setting + " " + pn.Staging + " " + pn.Description)
		for _, e := range world {
			switch {
			case e.IsPlace() && samePlace(pn.Setting, e.Name):
				weight[e.ID] += 2
			case !e.IsPlace() && listed(pn.Props, e.Name):
				weight[e.ID] += 2
			case mentions(words, e.Name):
				weight[e.ID]++
			}
		}
	}
	var out []*store.Element
	for _, e := range world {
		if weight[e.ID] > 0 {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsPlace() != out[j].IsPlace() {
			return out[i].IsPlace()
		}
		return weight[out[i].ID] > weight[out[j].ID]
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// bare is a name as panels may write it: lower case, without "the" or "a".
func bare(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, a := range []string{"the ", "a ", "an "} {
		n = strings.TrimPrefix(n, a)
	}
	return strings.TrimRight(n, ". ")
}

func samePlace(setting, name string) bool {
	s, n := bare(setting), bare(name)
	return s != "" && n != "" && (s == n || mentions(s, n) || mentions(n, s))
}

func listed(names []string, name string) bool {
	for _, n := range names {
		if bare(n) == bare(name) {
			return true
		}
	}
	return false
}

// mentions reports whether text names the element: its whole name, or the
// noun it ends on ("the pie" for "Grandma's apple pie"), as words.
func mentions(text, name string) bool {
	n := bare(name)
	if n == "" {
		return false
	}
	if wordIn(text, n) {
		return true
	}
	f := strings.Fields(n)
	head := strings.Trim(f[len(f)-1], "'’\".,")
	return len(f) > 1 && len([]rune(head)) >= 3 && wordIn(text, head)
}

func wordIn(text, phrase string) bool {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(phrase) + `\b`).MatchString(text)
}

// worldLabel is how a page names the concept art of an element.
func worldLabel(e *store.Element, n int) string {
	if e.IsPlace() {
		return fmt.Sprintf("Reference image %d: the concept art of %s, a place in the story. Whenever a panel is set there, keep this architecture, these colors and this layout: the doors, windows and furniture where they are here.", n, e.Name)
	}
	return fmt.Sprintf("Reference image %d: the design of %s, a prop in the story. Whenever it appears, draw it exactly like this: the same shape, size and colors.", n, e.Name)
}

// worldRule closes a page prompt that has concept art attached.
const worldRule = "PLACES AND PROPS: a place's concept art fixes its architecture, its colors and where everything is — the door, the windows, the table stay where the art puts them in every panel, whichever side the camera looks from. A prop keeps its shape, size and colors from panel to panel and page to page, and stays where the last panel left it until someone moves it."
