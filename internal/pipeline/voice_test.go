package pipeline

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/store"
)

func TestVoiceCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Voices {
		if v.ID == "" || v.Name == "" || v.Blurb == "" || seen[v.ID] {
			t.Fatalf("bad or duplicate catalog entry: %+v", v)
		}
		seen[v.ID] = true
	}
	if _, ok := VoiceByID(DefaultNarrator); !ok {
		t.Fatal("the default narrator is not in the catalog")
	}
	if _, ok := VoiceByID("nope"); ok {
		t.Fatal("unknown voice found")
	}
}

func TestCharacterAndNarratorVoice(t *testing.T) {
	c := &store.Character{Name: "Mara", Voice: "cgSgspJ2msm6clMCkdW9"}
	if CharacterVoice(c).Name != "Jessica" {
		t.Fatalf("picked voice ignored: %+v", CharacterVoice(c))
	}
	// No voice (a story from before voices): dealt by position, distinct
	// across a cast and never the narrator's.
	seen := map[string]bool{DefaultNarrator: true}
	for pos := 0; pos < len(Voices)-1; pos++ {
		v := CharacterVoice(&store.Character{Name: "X", Position: pos, Voice: "stale-id"})
		if seen[v.ID] {
			t.Fatalf("fallback for position %d repeats %s", pos, v.Name)
		}
		seen[v.ID] = true
	}
	if CharacterVoice(&store.Character{Position: -1}).ID == "" {
		t.Fatal("negative position")
	}
	if !Uncast([]*store.Character{c, {Name: "Pip"}}) || Uncast([]*store.Character{c}) || Uncast(nil) {
		t.Fatal("Uncast")
	}
	if NarratorVoice(&store.Story{}).ID != DefaultNarrator {
		t.Fatal("narrator default")
	}
	if NarratorVoice(&store.Story{NarratorVoice: "pqHfZKP75CvOlQylNhV4"}).Name != "Bill" {
		t.Fatal("narrator pick ignored")
	}
}

func TestWordsFromChars(t *testing.T) {
	text := " Hi,  you\nthere! "
	var chars []string
	var starts, ends []float64
	for i, r := range text {
		chars = append(chars, string(r))
		starts = append(starts, float64(i))
		ends = append(ends, float64(i)+0.5)
	}
	got := WordsFromChars(chars, starts, ends)
	want := []WordTiming{{"Hi,", 1, 3.5}, {"you", 6, 8.5}, {"there!", 10, 15.5}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("word %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	// Timings shorter than the characters: stop where they stop.
	if got := WordsFromChars([]string{"a", "b", " ", "c"}, []float64{0, 1}, []float64{1, 2}); len(got) != 1 || got[0].Text != "ab" {
		t.Fatalf("short timings: %+v", got)
	}
	if got := WordsFromChars(nil, nil, nil); got != nil {
		t.Fatalf("empty: %+v", got)
	}
}

// castRecorder answers the casting call with a fixed reply and keeps the prompt.
type castRecorder struct {
	reply string
	user  string
	err   error
}

func (c *castRecorder) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	c.user = user
	if c.err != nil {
		return c.err
	}
	if schemaName != "voices" {
		return errors.New("unexpected schema " + schemaName)
	}
	return jsonInto(c.reply, out)
}
func jsonInto(s string, out any) error { return json.Unmarshal([]byte(s), out) }

func (c *castRecorder) GenerateImage(context.Context, string, string) ([]byte, error) {
	return nil, nil
}
func (c *castRecorder) EditImage(context.Context, string, [][]byte, string) ([]byte, error) {
	return nil, nil
}

func TestCastVoices(t *testing.T) {
	st := &store.Story{Title: "The Lighthouse", Logline: "A girl and a robot.", World: "A harbour town."}
	mara := &store.Character{ID: 1, Name: "Mara", Role: "protagonist", Age: "12", Visual: "freckles", Personality: "curious"}
	pip := &store.Character{ID: 2, Name: "Pip", Role: "sidekick", Voice: "N2lVS1w4EtoT3dr4eOWO"}
	graves := &store.Character{ID: 3, Name: "Graves", Role: "antagonist"}
	ai := &castRecorder{reply: `{"narrator":"pqHfZKP75CvOlQylNhV4","characters":[{"name":"mara","voice":"cgSgspJ2msm6clMCkdW9"},{"name":"Graves","voice":"not-a-voice"}]}`}
	narrator, got, err := CastVoices(context.Background(), ai, st, []*store.Character{mara, pip, graves})
	if err != nil {
		t.Fatal(err)
	}
	if narrator != "pqHfZKP75CvOlQylNhV4" {
		t.Fatalf("narrator %s", narrator)
	}
	if got[1] != "cgSgspJ2msm6clMCkdW9" {
		t.Fatalf("Mara (matched case-insensitively): %v", got)
	}
	if _, ok := got[2]; ok {
		t.Fatalf("Pip already had a voice and must keep it: %v", got)
	}
	if got[3] != CharacterVoice(graves).ID {
		t.Fatalf("an invalid pick falls back to the stable voice: %v", got)
	}
	for _, want := range []string{"VOICE CATALOG", "Callum (N2lVS1w4EtoT3dr4eOWO)", "ALREADY CAST", "- Mara (protagonist, 12)", "- Graves (antagonist"} {
		if !strings.Contains(ai.user, want) {
			t.Errorf("prompt lacks %q:\n%s", want, ai.user)
		}
	}
	if strings.Contains(after(ai.user, "CAST THESE CHARACTERS:"), "Pip") {
		t.Error("Pip should not be cast again")
	}

	// A story with a narrator keeps it, even if the model suggests another.
	st.NarratorVoice = "onwK4e9ZLuTAKqWW03F9"
	narrator, _, _ = CastVoices(context.Background(), ai, st, []*store.Character{mara})
	if narrator != "onwK4e9ZLuTAKqWW03F9" || !strings.Contains(ai.user, "narrator is already Daniel") {
		t.Fatalf("kept narrator: %s", narrator)
	}

	// Everyone cast already: no model call at all.
	ai.user = ""
	narrator, got, err = CastVoices(context.Background(), ai, st, []*store.Character{pip})
	if err != nil || narrator != st.NarratorVoice || len(got) != 0 || ai.user != "" {
		t.Fatalf("no-op cast: %s %v %v %q", narrator, got, err, ai.user)
	}

	// An invalid narrator pick falls back to the default.
	st.NarratorVoice = ""
	ai.reply = `{"narrator":"x","characters":[]}`
	if narrator, _, _ := CastVoices(context.Background(), ai, st, []*store.Character{mara}); narrator != DefaultNarrator {
		t.Fatalf("narrator fallback: %s", narrator)
	}

	ai.err = errors.New("down")
	if _, _, err := CastVoices(context.Background(), ai, st, []*store.Character{mara}); err == nil {
		t.Fatal("error not propagated")
	}
}

func TestFakeCastingDealsDistinctVoices(t *testing.T) {
	st := &store.Story{Title: "T"}
	var chars []*store.Character
	for i, n := range []string{"Mara", "Pip", "Graves"} {
		chars = append(chars, &store.Character{ID: int64(i + 1), Name: n})
	}
	chars[1].Voice = Voices[1].ID // taken
	narrator, got, err := CastVoices(context.Background(), &Fake{}, st, chars)
	if err != nil {
		t.Fatal(err)
	}
	if narrator != DefaultNarrator || len(got) != 2 {
		t.Fatalf("narrator %s, got %v", narrator, got)
	}
	if got[1] == got[3] || got[1] == Voices[1].ID || got[3] == Voices[1].ID || got[1] == DefaultNarrator {
		t.Fatalf("voices not distinct from each other, the taken one and the narrator: %v", got)
	}
}

func TestSampleLines(t *testing.T) {
	mara := &store.Character{Name: "Mara"}
	pages := []*store.Page{{Panels: []store.Panel{
		{Dialogue: []store.Line{{Character: "Pip", Text: "Beep."}}},
		{Caption: "Night falls.", Dialogue: []store.Line{{Character: "MARA", Text: "  "}, {Character: "mara", Text: "Who's there?"}}},
	}}}
	if got := SampleLine(mara, pages); got != "Who's there?" {
		t.Fatalf("sample: %q", got)
	}
	if got := SampleLine(&store.Character{Name: "Graves"}, pages); !strings.Contains(got, "I'm Graves") {
		t.Fatalf("intro: %q", got)
	}
	st := &store.Story{Logline: "A girl and a robot."}
	if got := NarratorSample(st, pages); got != "Night falls." {
		t.Fatalf("caption: %q", got)
	}
	if got := NarratorSample(st, nil); got != "A girl and a robot." {
		t.Fatalf("logline: %q", got)
	}
	if got := NarratorSample(&store.Story{}, nil); got == "" {
		t.Fatal("empty narrator sample")
	}
}

func TestFakeVoice(t *testing.T) {
	sp, err := FakeVoice{}.Speak(context.Background(), "Hello there, Mara! Ready?", "v1")
	if err != nil {
		t.Fatal(err)
	}
	a := sp.Audio
	if sp.Ext != "wav" || string(a[:4]) != "RIFF" || string(a[8:16]) != "WAVEfmt " || string(a[36:40]) != "data" {
		t.Fatalf("not a wav: %q", a[:44])
	}
	if int(binary.LittleEndian.Uint32(a[40:44])) != len(a)-44 || int(binary.LittleEndian.Uint32(a[4:8])) != len(a)-8 {
		t.Fatal("wav sizes wrong")
	}
	if len(sp.Words) != 4 || sp.Words[0].Text != "Hello" || sp.Words[3].Text != "Ready?" {
		t.Fatalf("words: %+v", sp.Words)
	}
	dur := float64(len(a)-44) / 2 / fakeRate
	for i, w := range sp.Words {
		if w.End <= w.Start || w.End > dur || (i > 0 && w.Start < sp.Words[i-1].End) {
			t.Fatalf("timing %d out of order: %+v (clip %.2fs)", i, sp.Words, dur)
		}
	}
	// A different voice sounds different; the same voice the same.
	other, _ := FakeVoice{}.Speak(context.Background(), "Hello there, Mara! Ready?", "v2")
	again, _ := FakeVoice{}.Speak(context.Background(), "Hello there, Mara! Ready?", "v1")
	if string(other.Audio) == string(a) || string(again.Audio) != string(a) {
		t.Fatal("pitch should depend on the voice, deterministically")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (FakeVoice{}).Speak(ctx, "hi", "v"); err == nil {
		t.Fatal("cancelled context ignored")
	}
}
