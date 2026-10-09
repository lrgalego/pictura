// Package pipeline turns a script into characters, a page breakdown and
// rendered comic pages. It owns every prompt and JSON schema; the model
// calls go through the AI interface so the web layer can run on a fake.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/lrgalego/pictura/internal/store"
)

// AI is what the pipeline needs from a model provider.
// Image is a PNG handed to a model, with the label the prompt calls it by.
type Image struct {
	Label string
	PNG   []byte
}

type AI interface {
	// ChatJSON runs one exchange; images ride along with the user turn.
	ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error
	GenerateImage(ctx context.Context, prompt, size string) ([]byte, error)
	EditImage(ctx context.Context, prompt string, refs [][]byte, size string) ([]byte, error)
	// Segment finds every instance of a noun phrase in a PNG (SAM 3.1),
	// returning boxes in the image's pixels.
	Segment(ctx context.Context, png []byte, phrase string) ([]Region, error)
}

// Region is a box in image pixels, top-left inclusive, bottom-right
// exclusive.
type Region struct {
	X1, Y1, X2, Y2 int
}

// Reference is a writer-supplied image with its note, numbered as the
// prompts number them (1-based, in slice order).
type Reference struct {
	Filename string
	Note     string
	PNG      []byte
}

func referenceImages(refs []Reference) []Image {
	var out []Image
	for i, r := range refs {
		label := fmt.Sprintf("Reference image %d", i+1)
		if r.Filename != "" {
			label += " (file: " + r.Filename + ")"
		}
		if r.Note != "" {
			label += ": " + r.Note
		}
		out = append(out, Image{Label: label, PNG: r.PNG})
	}
	return out
}

// Aspect ratios handed to Muse Image.
const (
	SheetSize = "1536x1024" // landscape: turnaround + expressions read left to right
	PageSize  = "1024x1536" // portrait: a comic page
)

// Style is an art direction preset the writer picks in step 1.
type Style struct {
	Slug      string
	Name      string
	Blurb     string
	Direction string // the sentence handed to the image model
	Emoji     string
}

var Styles = []Style{
	{"comic", "Classic comic", "Bold inks, flat colors, halftone shadows.", "classic American comic book art: bold clean ink lines, flat vibrant colors, halftone shading, dynamic composition", "💥"},
	{"manga", "Manga", "Expressive black and white with screentones.", "Japanese manga art: expressive ink linework, black and white with screentones, dramatic speed lines and emotive faces", "⚡"},
	{"storybook", "Storybook", "Soft watercolor, cozy and warm.", "children's picture-book illustration: soft watercolor textures, warm gentle palette, rounded friendly shapes, cozy lighting", "🎨"},
	{"cartoon", "Saturday cartoon", "Chunky shapes, big eyes, bright and silly.", "retro Saturday-morning cartoon style: chunky simplified shapes, big expressive eyes, bright saturated colors, thick outlines", "📺"},
	{"noir", "Noir", "High-contrast shadows, moody and cinematic.", "graphic-novel noir: high-contrast chiaroscuro inks, moody limited palette with one accent color, cinematic angles, heavy shadows", "🕶️"},
	{"pixel", "Pixel art", "Retro 16-bit sprites and tiles.", "16-bit pixel art: crisp pixel sprites, limited retro palette, dithered shading, video-game charm", "👾"},
}

// StyleBySlug returns the preset, falling back to the first one.
func StyleBySlug(slug string) Style {
	for _, s := range Styles {
		if s.Slug == slug {
			return s
		}
	}
	return Styles[0]
}

// ---------- schemas ----------

func obj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func arr(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }

var characterSchema = obj(map[string]any{
	"name":        str("The character's name as used in the script"),
	"role":        str("Narrative role, e.g. protagonist, antagonist, sidekick, mentor"),
	"age":         str("Apparent age, e.g. '9 years old', 'early thirties', 'ancient'"),
	"visual":      str("Rich, concrete description of face, hair, body, skin, distinguishing marks and species — everything an illustrator needs to draw the character consistently"),
	"wardrobe":    str("Their signature outfit(s), colors and materials"),
	"items":       str("Props they carry or are associated with (weapons, tools, pets, vehicles); 'none' if nothing"),
	"personality": str("Two sentences: temperament and how it shows in posture and expression"),
}, "name", "role", "age", "visual", "wardrobe", "items", "personality")

var analysisSchema = obj(map[string]any{
	"title":      str("A punchy title for the story, reused from the script if it has one"),
	"logline":    str("One sentence pitch"),
	"world":      str("The setting, era, mood and recurring locations, in two or three sentences, for background consistency"),
	"characters": arr(characterSchema),
}, "title", "logline", "world", "characters")

var charactersSchema = obj(map[string]any{
	"characters": arr(characterSchema),
}, "characters")

var lineSchema = obj(map[string]any{
	"character": str("Who says it: a cast name, or a short descriptive name for a voice that is not in the cast (e.g. 'Deep Voice'). Never NARRATOR: narration goes in the caption"),
	"text":      str("The line as the script has it, trimmed to bubble length — never reworded into something the character did not say"),
}, "character", "text")

var panelSchema = obj(map[string]any{
	"number":      integer("1-based panel number within the page"),
	"shot":        str("Camera: wide, medium, close-up, extreme close-up, over-the-shoulder, bird's-eye, low angle"),
	"setting":     str("Where this panel happens, as a place name reused word for word by every panel in that place (e.g. 'the stone chamber' then 'the gameshow studio'), so the art never drifts back to an earlier location; light and mood go in the description"),
	"description": str("The one moment drawn: who is where in the frame (left to right, in the order they speak), action, expressions, composition — concrete and visual"),
	"characters":  arr(str("Cast names of characters visible in the panel, nobody else")),
	"dialogue":    arr(lineSchema),
	"caption":     str("Narration caption box text, or empty string"),
	"sfx":         str("Sound-effect lettering drawn into the art (e.g. 'SLAM!'), or empty string"),
}, "number", "shot", "setting", "description", "characters", "dialogue", "caption", "sfx")

var pageSchema = obj(map[string]any{
	"number":  integer("1-based page number"),
	"title":   str("A short title for the page"),
	"summary": str("One sentence: what this page accomplishes in the story"),
	"panels":  arr(panelSchema),
}, "number", "title", "summary", "panels")

var pagesSchema = obj(map[string]any{
	"pages": arr(pageSchema),
}, "pages")

var reviewSchema = obj(map[string]any{
	"problems": arr(str("One rule the draft breaks, with its page and panel, e.g. 'p5 panel 4: Ash asks \"Who are you two?\" before the hosts appear'")),
	"pages":    arr(pageSchema),
}, "problems", "pages")

var singlePageSchema = obj(map[string]any{
	"page": pageSchema,
}, "page")

// ---------- results ----------

type CharacterSpec struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Age         string `json:"age"`
	Visual      string `json:"visual"`
	Wardrobe    string `json:"wardrobe"`
	Items       string `json:"items"`
	Personality string `json:"personality"`
}

type Analysis struct {
	Title      string          `json:"title"`
	Logline    string          `json:"logline"`
	World      string          `json:"world"`
	Characters []CharacterSpec `json:"characters"`
}

type PageSpec struct {
	Number  int           `json:"number"`
	Title   string        `json:"title"`
	Summary string        `json:"summary"`
	Panels  []store.Panel `json:"panels"`
}

// ---------- text steps ----------

const analystPersona = `You are a story editor and character designer for an illustrated comic adaptation. You read scripts closely and write precise, visual, illustrator-ready descriptions. Never invent characters that are not in the script; do include unnamed but recurring ones with a descriptive name (e.g. "The Innkeeper"). Only include characters that appear on the page — skip those merely mentioned. Order characters by importance. Write names in natural capitalization ("Mara", not "MARA") even when the script shouts them. Answer only with JSON matching the schema.`

// Analyze extracts the cast and the world from a script.
func Analyze(ctx context.Context, ai AI, script string, style Style) (*Analysis, error) {
	user := fmt.Sprintf("Art direction for the adaptation: %s.\n\nSCRIPT:\n%s", style.Direction, script)
	var out Analysis
	if err := ai.ChatJSON(ctx, analystPersona, user, nil, "analysis", analysisSchema, &out); err != nil {
		return nil, err
	}
	if len(out.Characters) == 0 {
		return nil, fmt.Errorf("the model found no characters in the script")
	}
	return &out, nil
}

// AdjustCharacters revises the whole cast according to the writer's feedback.
func AdjustCharacters(ctx context.Context, ai AI, story *store.Story, chars []*store.Character, feedback string) ([]CharacterSpec, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Art direction: %s.\nWorld: %s\n\nCURRENT CAST:\n%s\n\nWRITER'S FEEDBACK:\n%s\n\nReturn the full revised cast (same characters unless the feedback adds or removes some), applying the feedback and keeping everything else unchanged.\n\nSCRIPT (for reference):\n%s",
		StyleBySlug(story.Style).Direction, story.World, describeCast(chars), feedback, story.Script)
	var out struct {
		Characters []CharacterSpec `json:"characters"`
	}
	if err := ai.ChatJSON(ctx, analystPersona, b.String(), nil, "characters", charactersSchema, &out); err != nil {
		return nil, err
	}
	if len(out.Characters) == 0 {
		return nil, fmt.Errorf("the model returned an empty cast")
	}
	return out.Characters, nil
}

// AdjustCharacter revises one character according to feedback.
func AdjustCharacter(ctx context.Context, ai AI, story *store.Story, c *store.Character, feedback string, refs []Reference) (*CharacterSpec, error) {
	if strings.TrimSpace(feedback) == "" {
		feedback = "(no written notes; match the attached reference images)"
	}
	rule := ""
	if len(refs) > 0 {
		rule = fmt.Sprintf("\n\nThe writer attached %d reference image(s) for this character: photos, sketches, outfits or props they should match. Describe what you see in them precisely in the visual, wardrobe and items fields.", len(refs))
	}
	user := fmt.Sprintf("Art direction: %s.\nWorld: %s\n\nCHARACTER:\n%s\n\nWRITER'S FEEDBACK:\n%s%s\n\nReturn this one character revised per the feedback; keep the name unless asked to change it, and keep every detail the feedback does not touch.",
		StyleBySlug(story.Style).Direction, story.World, describeCharacter(c), feedback, rule)
	var out struct {
		Characters []CharacterSpec `json:"characters"`
	}
	if err := ai.ChatJSON(ctx, analystPersona, user, referenceImages(refs), "characters", charactersSchema, &out); err != nil {
		return nil, err
	}
	if len(out.Characters) == 0 {
		return nil, fmt.Errorf("the model returned no character")
	}
	return &out.Characters[0], nil
}

const breakdownPersona = `You are a comic book writer and storyboard artist adapting a script into comic pages that a reader can follow without ever seeing the script.

Reading the script:
- Work out who says every line before you plan anything. Scripts often run several quoted lines in a row without naming the speaker: they usually belong to the last character who spoke or acted, until another character is named — but let the content decide when it clearly continues someone else (a host still listing prizes, an answer to a question). A character quoting or imitating someone else is still the one speaking.
- A voice that is not in the cast (an announcer, a voice from the dark, a screen) is its own speaker with a short descriptive name. Never give its lines to a cast member or to the narrator. When the script keeps a speaker hidden until a reveal, keep the mystery: name them as the script describes them ("Loud Voice") until they are revealed. A speaker who is heard but not seen in a panel is simply left out of that panel's characters.

Fidelity:
- Keep every line of dialogue that matters to the story, in the order it is spoken, with its real speaker, each exactly once: walk the script from top to bottom and never move a line earlier, repeat it or skip back. Trim long lines to bubble length but keep their meaning, jokes and punchlines; never invent dialogue the script does not have.
- A run of short lines from one speaker that makes one thought is one balloon ("Sure. We have time."), not three.
- Keep every plot beat, setup and payoff (a promise, a secret, a threat) — a reader must be able to retell the story from the pages alone.
- Sound effects (SLAM!, BOOM!, sirens, a laugh track) go in sfx, not in dialogue or captions. Captions carry the script's own narration, condensed, only where the art cannot show it (time, place, a transition, "To be continued..."); never add narration the script does not have.

Panels:
- One panel is one moment. Never more than three balloons or two speakers in a panel; a longer exchange takes more panels.
- Causes come before reactions: show the thing a character reacts to in the same panel or an earlier one — a character who makes an entrance is drawn arriving before anyone answers them. A turning point (a door slamming, a room transforming, a reveal) gets a panel of its own before the reactions.
- Silent actions that carry the story (everyone laughs, everyone turns to look at someone, a character looks away) are drawn in the description of the panel where they happen.
- Frame what the moment needs and vary the camera: an establishing wide shot whenever the setting is new or changes, close-ups for emotion, medium shots for conversation.
- characters lists exactly the cast members the description puts in the frame — if the description shows the whole group, list all of them; a close-up holds one or two.
- Every panel names its setting with the same place name as the panels before it, until the place changes or transforms; then the new name, reused from there on. A flashback names its own setting.

Pages:
- A page holds 3 to 6 panels: about five for conversation, fewer and bigger when a moment needs room (a transformation, a reveal, the last page). End each page on a beat that makes the reader turn: a question, a reveal, a punchline.
- Use as many pages as the story needs under these rules, within the range given: fuller pages over thin ones, but never cram a page to save one.

Use only the given cast for characters on the page. Answer only with JSON matching the schema.`

// Breakdown plans the pages of the book.
func Breakdown(ctx context.Context, ai AI, story *store.Story, chars []*store.Character) ([]PageSpec, error) {
	lo, hi := pageRange(story.Script)
	user := fmt.Sprintf("Title: %s\nLogline: %s\nWorld: %s\nArt direction: %s\n\nCAST:\n%s\n\nThis script needs between %d and %d pages; pick the count the story needs within that range.\n\nSCRIPT:\n%s",
		story.Title, story.Logline, story.World, StyleBySlug(story.Style).Direction, describeCast(chars), lo, hi, story.Script)
	var out struct {
		Pages []PageSpec `json:"pages"`
	}
	if err := ai.ChatJSON(ctx, breakdownPersona, user, nil, "pages", pagesSchema, &out); err != nil {
		return nil, err
	}
	if len(out.Pages) == 0 {
		return nil, fmt.Errorf("the model returned no pages")
	}
	renumber(out.Pages)
	return reviewBreakdown(ctx, ai, story, chars, out.Pages), nil
}

const reviewerPersona = `You are the editor of a comic adaptation. A storyboard artist drafted the pages below from the script; you check the draft against the script, line by line, before anything is drawn, and fix it.

Check every panel for:
- a line given to the wrong speaker (runs of quoted lines usually belong to the last character who spoke or acted, unless the content clearly continues someone else; a character imitating someone is still the speaker; voices outside the cast keep their own descriptive name);
- a line out of the script's order, repeated, or invented; a dropped line, joke, setup or payoff the story needs;
- a reaction drawn or spoken before its cause, or a character answered before they have been drawn arriving;
- more than three balloons or two speakers in a panel, or two moments in one panel;
- characters that do not match who the description puts in the frame;
- a setting that does not follow the place the script is in (one place name per place, reused word for word);
- sound effects in dialogue or captions, or narration the script does not have.

These are not problems: lines and captions trimmed to bubble length, an off-panel speaker (a voice from the dark, someone heard from outside the frame), a narration detail the art already shows. Report only what would confuse a reader or break the art; if the draft is sound, return no problems.

List each problem you find, then return the whole corrected storyboard: fix only what is wrong and keep everything else exactly as drafted, the same pages unless a fix needs a panel moved across a page turn. Answer only with JSON matching the schema.`

// reviewBreakdown has an editor check the draft against the script and fix
// what breaks the rules: a single planning pass still slips now and then on
// a long script (a line moved, a reaction before its cause). The draft
// stands if the review fails or finds nothing.
func reviewBreakdown(ctx context.Context, ai AI, story *store.Story, chars []*store.Character, draft []PageSpec) []PageSpec {
	plan, _ := json.Marshal(draft)
	user := fmt.Sprintf("CAST:\n%s\n\nSCRIPT:\n%s\n\nDRAFT STORYBOARD (JSON):\n%s", describeCast(chars), story.Script, plan)
	var out struct {
		Problems []string   `json:"problems"`
		Pages    []PageSpec `json:"pages"`
	}
	if err := ai.ChatJSON(ctx, reviewerPersona, user, nil, "review", reviewSchema, &out); err != nil || len(out.Problems) == 0 || len(out.Pages) == 0 {
		return draft
	}
	renumber(out.Pages)
	return out.Pages
}

// AdjustBreakdown revises every page according to feedback.
func AdjustBreakdown(ctx context.Context, ai AI, story *store.Story, chars []*store.Character, pages []*store.Page, feedback string) ([]PageSpec, error) {
	user := fmt.Sprintf("Title: %s\nWorld: %s\nArt direction: %s\n\nCAST:\n%s\n\nCURRENT PAGES:\n%s\n\nWRITER'S FEEDBACK:\n%s\n\nReturn the full revised page list applying the feedback; keep pages the feedback does not touch as they are.\n\nSCRIPT (for reference):\n%s",
		story.Title, story.World, StyleBySlug(story.Style).Direction, describeCast(chars), describePages(pages), feedback, story.Script)
	var out struct {
		Pages []PageSpec `json:"pages"`
	}
	if err := ai.ChatJSON(ctx, breakdownPersona, user, nil, "pages", pagesSchema, &out); err != nil {
		return nil, err
	}
	if len(out.Pages) == 0 {
		return nil, fmt.Errorf("the model returned no pages")
	}
	renumber(out.Pages)
	return out.Pages, nil
}

// AdjustPage revises a single page according to feedback.
func AdjustPage(ctx context.Context, ai AI, story *store.Story, chars []*store.Character, page *store.Page, feedback string) (*PageSpec, error) {
	user := fmt.Sprintf("Title: %s\nWorld: %s\nArt direction: %s\n\nCAST:\n%s\n\nPAGE %d:\n%s\n\nWRITER'S FEEDBACK:\n%s\n\nReturn this one page revised per the feedback, same page number.",
		story.Title, story.World, StyleBySlug(story.Style).Direction, describeCast(chars), page.Number, describePage(page), feedback)
	var out struct {
		Page PageSpec `json:"page"`
	}
	if err := ai.ChatJSON(ctx, breakdownPersona, user, nil, "page", singlePageSchema, &out); err != nil {
		return nil, err
	}
	out.Page.Number = page.Number
	for i := range out.Page.Panels {
		out.Page.Panels[i].Number = i + 1
	}
	return &out.Page, nil
}

func renumber(pages []PageSpec) {
	for i := range pages {
		pages[i].Number = i + 1
		for j := range pages[i].Panels {
			pages[i].Panels[j].Number = j + 1
		}
	}
}

// beats counts the script's sentences and lines of dialogue: a comic
// spends a panel on every two or three of them whatever their length, so
// they size a book better than its word count (a dialogue-heavy script of
// short lines needs far more panels than its words suggest).
func beats(script string) int {
	n := 0
	for _, chunk := range sentenceEnd.Split(script, -1) {
		if strings.IndexFunc(chunk, unicode.IsLetter) >= 0 {
			n++
		}
	}
	return n
}

var sentenceEnd = regexp.MustCompile(`[.!?…]+["”’)\]]*|\n`)

// pageRange is how many pages the script needs: about 25 beats a page
// (five panels of a few lines each), at least three, at most 24.
func pageRange(script string) (lo, hi int) {
	b := beats(script)
	lo = min(24, max(3, b/30))
	hi = min(24, max(lo+2, (b+21)/22))
	return lo, hi
}

// ---------- image prompts ----------

// SheetPrompt is the character-sheet prompt handed to Muse Image. refs is
// how many writer references ride along as input images.
func SheetPrompt(story *store.Story, c *store.Character, refs int) string {
	style := StyleBySlug(story.Style).Direction
	lead := ""
	if refs > 0 {
		lead = fmt.Sprintf("The %d attached image(s) are the writer's references for this character: match the face, hair, body, outfit, colors and props they show, redrawn in the requested style. ", refs)
	}
	return lead + fmt.Sprintf(`Character reference sheet (model sheet) for a comic, %s. Single character: %s (%s, %s).
Appearance: %s
Wardrobe: %s
Props: %s
Personality cues: %s
Layout, on a plain white background with no text other than the character's name as a small title: a full-body turnaround in a row (front view, three-quarter view, side profile, back view); below it a row of head studies with different expressions (neutral, happy, angry, surprised, sad); and a row of two or three action poses using the props. Keep every drawing perfectly consistent — same face, same proportions, same outfit colors. Clean, well-lit, professional model-sheet presentation. World: %s`,
		style, c.Name, c.Role, c.Age, c.Visual, c.Wardrobe, c.Items, c.Personality, story.World)
}

// MaxPageRefs is how many character sheets ride along with a page: Muse
// Image composes from ten reference images, and a page rarely shows more
// than eight characters.
const MaxPageRefs = 8

// PageCast picks the characters whose sheets ride along with a page: the
// ones it shows, those in the most panels and with the most lines first,
// at most max of them. A page with no characters named gets the whole cast
// in order.
func PageCast(chars []*store.Character, page *store.Page, max int) []*store.Character {
	weight := map[string]int{}
	for _, pn := range page.Panels {
		for _, n := range pn.Characters {
			weight[strings.ToLower(n)] += 2
		}
		for _, l := range pn.Dialogue {
			if _, shown := weight[strings.ToLower(l.Character)]; shown {
				weight[strings.ToLower(l.Character)]++
			}
		}
	}
	var out []*store.Character
	for _, c := range chars {
		if len(weight) == 0 || weight[strings.ToLower(c.Name)] > 0 {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return weight[strings.ToLower(out[i].Name)] > weight[strings.ToLower(out[j].Name)]
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// PagePrompt is the page prompt; the character sheets ride along as
// reference images so the cast stays on-model.
func PagePrompt(story *store.Story, chars []*store.Character, page *store.Page, feedback string) string {
	style := StyleBySlug(story.Style).Direction
	var b strings.Builder
	fmt.Fprintf(&b, "A finished comic book page, %s. Portrait page, %d panels laid out in a clean comic grid with white gutters and bold panel borders, read left to right, top to bottom. World: %s\n", style, len(page.Panels), story.World)
	if page.Number == 1 {
		fmt.Fprintf(&b, "This is the opening page: letter the story title %q once as a title header across the top, in a bold comic title font, above the first panel.\n", story.Title)
	} else {
		fmt.Fprintf(&b, "This is page %d, an interior page: no title, no header text, no page number — only the panels with their bubbles and captions.\n", page.Number)
	}
	if len(chars) > 0 {
		b.WriteString("The attached reference images are the character sheets: draw each character exactly as on their sheet — same face, hair, proportions, outfit and colors.\n")
		for i, c := range chars {
			fmt.Fprintf(&b, "Reference image %d = %s (%s).\n", i+1, c.Name, c.Visual)
		}
	}
	for _, p := range page.Panels {
		fmt.Fprintf(&b, "\nPANEL %d (%s shot):", p.Number, p.Shot)
		if p.Setting != "" {
			fmt.Fprintf(&b, " Setting: %s.", strings.TrimRight(p.Setting, ". "))
		}
		if d := strings.TrimSpace(p.Description); d != "" {
			fmt.Fprintf(&b, " %s.", strings.TrimRight(d, ". "))
		}
		shown := map[string]bool{}
		if len(p.Characters) > 0 {
			for _, n := range p.Characters {
				shown[strings.ToLower(n)] = true
			}
			fmt.Fprintf(&b, " Characters present, each drawn once: %s.", strings.Join(p.Characters, ", "))
		}
		if p.Caption != "" {
			fmt.Fprintf(&b, " Caption box: %q.", p.Caption)
		}
		for _, l := range p.Dialogue {
			if shown[strings.ToLower(l.Character)] {
				fmt.Fprintf(&b, " Speech bubble from %s: %q.", l.Character, l.Text)
			} else {
				fmt.Fprintf(&b, " Speech bubble from %s, who is off-panel (not drawn here; the tail points out of the frame toward them): %q.", l.Character, l.Text)
			}
		}
		if p.SFX != "" {
			fmt.Fprintf(&b, " Sound effect lettered into the art: %q.", p.SFX)
		}
	}
	b.WriteString("\n\nLetter every caption, speech bubble and sound effect legibly in a comic font with the exact text given, each exactly once and only in its own panel. Within a panel, place bubbles in the order listed, left to right and top to bottom, so they read in that order, each tail pointing at its speaker. No other text, no watermark, no page number.")
	if strings.TrimSpace(feedback) != "" {
		fmt.Fprintf(&b, "\n\nRevision notes from the writer, apply them: %s", feedback)
	}
	return b.String()
}

// ---------- description helpers ----------

func describeCharacter(c *store.Character) string {
	return fmt.Sprintf("- %s (%s, %s)\n  Appearance: %s\n  Wardrobe: %s\n  Items: %s\n  Personality: %s", c.Name, c.Role, c.Age, c.Visual, c.Wardrobe, c.Items, c.Personality)
}

func describeCast(chars []*store.Character) string {
	var parts []string
	for _, c := range chars {
		parts = append(parts, describeCharacter(c))
	}
	return strings.Join(parts, "\n")
}

func describePage(p *store.Page) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Page %d — %s: %s\n", p.Number, p.Title, p.Summary)
	for _, pn := range p.Panels {
		fmt.Fprintf(&b, "  Panel %d (%s)", pn.Number, pn.Shot)
		if pn.Setting != "" {
			fmt.Fprintf(&b, " at %s", pn.Setting)
		}
		fmt.Fprintf(&b, ": %s [%s]", pn.Description, strings.Join(pn.Characters, ", "))
		if pn.Caption != "" {
			fmt.Fprintf(&b, " caption: %q", pn.Caption)
		}
		if pn.SFX != "" {
			fmt.Fprintf(&b, " sfx: %q", pn.SFX)
		}
		for _, l := range pn.Dialogue {
			fmt.Fprintf(&b, " %s: %q", l.Character, l.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func describePages(pages []*store.Page) string {
	var parts []string
	for _, p := range pages {
		parts = append(parts, describePage(p))
	}
	return strings.Join(parts, "\n")
}
