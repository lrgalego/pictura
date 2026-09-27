package pipeline

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/lrgalego/pictura/internal/store"
)

// Speaker turns text into speech with per-word timings, so a reader can
// highlight each word as it is said.
type Speaker interface {
	Speak(ctx context.Context, text, voiceID string) (*Speech, error)
}

// Speech is one synthesized line.
type Speech struct {
	Audio []byte
	Ext   string // file extension of Audio: "mp3", "wav"
	Words []WordTiming
}

// WordTiming is when one word of the line is spoken, in seconds from the
// start of the clip. Text is the word as written, punctuation included.
type WordTiming struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Voice is one entry of the casting catalog.
type Voice struct {
	ID     string
	Name   string
	Gender string // female, male, neutral
	Age    string // young, middle-aged, old
	Accent string
	Blurb  string // how it sounds, for people and for the casting prompt
}

// Voices is the casting catalog: ElevenLabs' premade voices, which every
// account can use. Kept in code so casting, the picker and the fake speaker
// agree without a network call.
var Voices = []Voice{
	{"JBFqnCBsd6RMkjVDRZzb", "George", "male", "middle-aged", "British", "Warm, captivating storyteller"},
	{"cgSgspJ2msm6clMCkdW9", "Jessica", "female", "young", "American", "Playful, bright and cute"},
	{"FGY2WhTYpPnrIDTdsKH5", "Laura", "female", "young", "American", "Enthusiastic and quirky"},
	{"N2lVS1w4EtoT3dr4eOWO", "Callum", "male", "middle-aged", "American", "Husky trickster"},
	{"SOYHLrjzK2X1ezoPC6cr", "Harry", "male", "young", "American", "Fierce, rough warrior"},
	{"IKne3meq5aSn9XLyUdCD", "Charlie", "male", "young", "Australian", "Deep, confident and energetic"},
	{"TX3LPaxmHKxFdv7VOQHJ", "Liam", "male", "young", "American", "Energetic and confident"},
	{"bIHbv24MWmeRgasZH58o", "Will", "male", "young", "American", "Relaxed, chill optimist"},
	{"EXAVITQu4vr4xnSDxMaL", "Sarah", "female", "young", "American", "Mature, reassuring and confident"},
	{"hpp4J3VqNfWAUOO0d1Us", "Bella", "female", "middle-aged", "American", "Bright, warm and clear"},
	{"XrExE9yKIg1WjnnlVkGX", "Matilda", "female", "middle-aged", "American", "Upbeat and knowledgeable"},
	{"Xb7hH8MSUJpSbSDYk0k2", "Alice", "female", "middle-aged", "British", "Clear, engaging educator"},
	{"pFZP5JQG7iQjIQuC4Bku", "Lily", "female", "middle-aged", "British", "Velvety, confident actress"},
	{"SAz9YHcvj6GT2YYXdXww", "River", "neutral", "middle-aged", "American", "Relaxed, calm and neutral"},
	{"CwhRBWXzGAHq8TQ4Fs17", "Roger", "male", "middle-aged", "American", "Laid-back, casual, resonant"},
	{"cjVigY5qzO86Huf0OWal", "Eric", "male", "middle-aged", "American", "Smooth and trustworthy"},
	{"iP95p4xoKVk53GoZ742B", "Chris", "male", "middle-aged", "American", "Charming, down-to-earth"},
	{"nPczCjzI2devNBz1zQrb", "Brian", "male", "middle-aged", "American", "Deep, resonant and comforting"},
	{"onwK4e9ZLuTAKqWW03F9", "Daniel", "male", "middle-aged", "British", "Steady, formal broadcaster"},
	{"pNInz6obpgDQGcFmaJgB", "Adam", "male", "middle-aged", "American", "Dominant and firm"},
	{"pqHfZKP75CvOlQylNhV4", "Bill", "male", "old", "American", "Wise, mature and balanced"},
}

// DefaultNarrator reads captions when the story has no narrator cast yet.
const DefaultNarrator = "JBFqnCBsd6RMkjVDRZzb"

// VoiceByID looks a voice up in the catalog.
func VoiceByID(id string) (Voice, bool) {
	for _, v := range Voices {
		if v.ID == id {
			return v, true
		}
	}
	return Voice{}, false
}

// CharacterVoice is the voice a character speaks with: the one cast or
// picked, or else a fallback dealt by cast position, so the characters of a
// story made before voices existed still sound distinct from each other and
// from the default narrator.
func CharacterVoice(c *store.Character) Voice {
	if v, ok := VoiceByID(c.Voice); ok {
		return v
	}
	pool := Voices[1:] // everyone but the default narrator
	return pool[(c.Position%len(pool)+len(pool))%len(pool)]
}

// Uncast reports whether any character still speaks with a fallback voice.
func Uncast(chars []*store.Character) bool {
	for _, c := range chars {
		if _, ok := VoiceByID(c.Voice); !ok {
			return true
		}
	}
	return false
}

// NarratorVoice is the voice captions are read in.
func NarratorVoice(st *store.Story) Voice {
	if v, ok := VoiceByID(st.NarratorVoice); ok {
		return v
	}
	v, _ := VoiceByID(DefaultNarrator)
	return v
}

// WordsFromChars groups a character-level alignment (what ElevenLabs
// returns) into words: runs of non-space characters, each spanning from its
// first character's start to its last character's end.
func WordsFromChars(chars []string, starts, ends []float64) []WordTiming {
	var out []WordTiming
	var cur strings.Builder
	var start, end float64
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, WordTiming{Text: cur.String(), Start: start, End: end})
			cur.Reset()
		}
	}
	for i, ch := range chars {
		if i >= len(starts) || i >= len(ends) {
			break
		}
		if strings.TrimFunc(ch, unicode.IsSpace) == "" {
			flush()
			continue
		}
		if cur.Len() == 0 {
			start = starts[i]
		}
		cur.WriteString(ch)
		end = ends[i]
	}
	flush()
	return out
}

// ---------- casting ----------

var voiceIDs = func() []any {
	var ids []any
	for _, v := range Voices {
		ids = append(ids, v.ID)
	}
	return ids
}()

var castingSchema = obj(map[string]any{
	"narrator": map[string]any{"type": "string", "enum": voiceIDs, "description": "Voice id for the narrator who reads the captions"},
	"characters": arr(obj(map[string]any{
		"name":  str("The character's name exactly as given"),
		"voice": map[string]any{"type": "string", "enum": voiceIDs, "description": "Voice id from the catalog"},
	}, "name", "voice")),
}, "narrator", "characters")

const castingPersona = `You are a casting director for an illustrated story that will be read aloud. You pick a voice for each character from a fixed catalog of voice actors, matching gender, age and temperament, and making characters who share scenes sound clearly different. The narrator needs a warm voice that suits the story's mood and is not used by any character. Answer only with JSON matching the schema.`

// CastVoices picks voices for the characters that have none yet and, when
// the story has no narrator, for the narrator. Characters that already
// have a voice keep it and are listed as taken. It returns the narrator
// voice (the story's current one when it already had one) and a voice per
// character id that was missing one.
func CastVoices(ctx context.Context, ai AI, story *store.Story, chars []*store.Character) (string, map[int64]string, error) {
	var need []*store.Character
	var taken []string
	for _, c := range chars {
		if _, ok := VoiceByID(c.Voice); ok {
			v, _ := VoiceByID(c.Voice)
			taken = append(taken, fmt.Sprintf("- %s speaks as %s (%s)", c.Name, v.Name, v.ID))
		} else {
			need = append(need, c)
		}
	}
	_, hasNarrator := VoiceByID(story.NarratorVoice)
	if len(need) == 0 && hasNarrator {
		return story.NarratorVoice, map[int64]string{}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Story: %s\nLogline: %s\nWorld: %s\n\nVOICE CATALOG:\n", story.Title, story.Logline, story.World)
	for _, v := range Voices {
		fmt.Fprintf(&b, "- %s: %s, %s %s, %s accent. %s\n", v.ID, v.Name, v.Age, v.Gender, v.Accent, v.Blurb)
	}
	if len(taken) > 0 {
		fmt.Fprintf(&b, "\nALREADY CAST (keep these, avoid reusing their voices):\n%s\n", strings.Join(taken, "\n"))
	}
	if hasNarrator {
		v, _ := VoiceByID(story.NarratorVoice)
		fmt.Fprintf(&b, "\nThe narrator is already %s (%s); return that id as the narrator.\n", v.Name, v.ID)
	}
	b.WriteString("\nCAST THESE CHARACTERS:\n")
	for _, c := range need {
		fmt.Fprintf(&b, "- %s (%s, %s). Looks: %s Personality: %s\n", c.Name, c.Role, c.Age, clip(c.Visual, 240), c.Personality)
	}
	var out struct {
		Narrator   string `json:"narrator"`
		Characters []struct {
			Name  string `json:"name"`
			Voice string `json:"voice"`
		} `json:"characters"`
	}
	if err := ai.ChatJSON(ctx, castingPersona, b.String(), nil, "voices", castingSchema, &out); err != nil {
		return "", nil, err
	}
	narrator := story.NarratorVoice
	if !hasNarrator {
		narrator = out.Narrator
		if _, ok := VoiceByID(narrator); !ok {
			narrator = DefaultNarrator
		}
	}
	picked := map[string]string{}
	for _, p := range out.Characters {
		if _, ok := VoiceByID(p.Voice); ok {
			picked[strings.ToLower(strings.TrimSpace(p.Name))] = p.Voice
		}
	}
	got := map[int64]string{}
	for _, c := range need {
		if v, ok := picked[strings.ToLower(c.Name)]; ok {
			got[c.ID] = v
		} else {
			got[c.ID] = CharacterVoice(c).ID
		}
	}
	return narrator, got, nil
}

// SampleLine is what a voice preview says: the character's first line in
// the storyboard, or a short introduction before there is one.
func SampleLine(c *store.Character, pages []*store.Page) string {
	for _, p := range pages {
		for _, pn := range p.Panels {
			for _, l := range pn.Dialogue {
				if strings.EqualFold(strings.TrimSpace(l.Character), c.Name) && strings.TrimSpace(l.Text) != "" {
					return clip(l.Text, 140)
				}
			}
		}
	}
	return fmt.Sprintf("Hi! I'm %s. Want to hear a story?", c.Name)
}

// NarratorSample is what a narrator preview says: the first caption, or
// the logline.
func NarratorSample(st *store.Story, pages []*store.Page) string {
	for _, p := range pages {
		for _, pn := range p.Panels {
			if strings.TrimSpace(pn.Caption) != "" {
				return clip(pn.Caption, 140)
			}
		}
	}
	if strings.TrimSpace(st.Logline) != "" {
		return clip(st.Logline, 140)
	}
	return "Once upon a time, a story began."
}
