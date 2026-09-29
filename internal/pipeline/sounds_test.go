package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lrgalego/pictura/internal/store"
)

func TestValidParts(t *testing.T) {
	sp := func(f, to int) Part { return Part{From: f, To: to, Kind: "speech"} }
	for _, tc := range []struct {
		name  string
		parts []Part
		n     int
		ok    bool
		want  []Part
	}{
		{"all speech", []Part{sp(0, 2)}, 3, true, []Part{sp(0, 2)}},
		{"neighbouring speech merges", []Part{sp(0, 0), sp(1, 2)}, 3, true, []Part{sp(0, 2)}},
		{"effect then speech, length clamped", []Part{{From: 0, To: 1, Kind: "effect", Sound: " stomps ", Seconds: 9}, sp(2, 2)}, 3, true,
			[]Part{{From: 0, To: 1, Kind: "effect", Sound: "stomps", Seconds: MaxEffectSeconds}, sp(2, 2)}},
		{"effect without a length gets the default", []Part{{From: 0, To: 0, Kind: "effect", Sound: "poof"}}, 1, true,
			[]Part{{From: 0, To: 0, Kind: "effect", Sound: "poof", Seconds: defaultEffectSeconds}}},
		{"two effects stay two", []Part{{From: 0, To: 0, Kind: "effect", Sound: "a", Seconds: 1}, {From: 1, To: 1, Kind: "effect", Sound: "b", Seconds: 0.1}}, 2, true,
			[]Part{{From: 0, To: 0, Kind: "effect", Sound: "a", Seconds: 1}, {From: 1, To: 1, Kind: "effect", Sound: "b", Seconds: MinEffectSeconds}}},
		{"vocal repeats merge", []Part{{From: 0, To: 0, Kind: "vocal", Tag: "laughs"}, {From: 1, To: 1, Kind: "vocal", Tag: "laughs"}}, 2, true,
			[]Part{{From: 0, To: 1, Kind: "vocal", Tag: "laughs"}}},
		{"gap", []Part{sp(0, 0), sp(2, 2)}, 3, false, nil},
		{"short", []Part{sp(0, 1)}, 3, false, nil},
		{"past the end", []Part{sp(0, 3)}, 3, false, nil},
		{"backwards", []Part{{From: 1, To: 0, Kind: "speech"}}, 2, false, nil},
		{"unknown tag", []Part{{From: 0, To: 0, Kind: "vocal", Tag: "burps"}}, 1, false, nil},
		{"effect without a sound", []Part{{From: 0, To: 0, Kind: "effect", Sound: " "}}, 1, false, nil},
		{"unknown kind", []Part{{From: 0, To: 0, Kind: "music"}}, 1, false, nil},
		{"nothing", nil, 1, false, nil},
	} {
		got, ok := validParts(tc.parts, tc.n)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v", tc.name, ok)
			continue
		}
		if !ok {
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: %+v", tc.name, got)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: part %d = %+v, want %+v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// answering is an AI that answers the sound-design call with a fixed reply.
type answering struct {
	castRecorder
	schema string
	images int
}

func (a *answering) ChatJSON(ctx context.Context, system, user string, images []Image, schemaName string, schema map[string]any, out any) error {
	a.schema, a.images = schemaName, len(images)
	return a.castRecorder.ChatJSONAny(user, out)
}

func (c *castRecorder) ChatJSONAny(user string, out any) error {
	c.user = user
	if c.err != nil {
		return c.err
	}
	return jsonInto(c.reply, out)
}

func TestPlanSounds(t *testing.T) {
	story := &store.Story{Title: "Meadow", World: "a sunny meadow"}
	page := &store.Page{Number: 2, Summary: "Bannette stomps in."}
	lines := []PlanLine{
		{Kind: "caption", Words: []string{"Then...", "STOMP!", "STOMP!"}},
		{Kind: "bubble", Speaker: "Mara", Words: []string{"Yaaawn...", "so", "sleepy"}},
		{Kind: "bubble", Speaker: "Pip", Words: []string{"Hello!"}},
		{Kind: "bubble", Speaker: "Pip", Words: []string{"Oops"}},
	}
	ai := &answering{castRecorder: castRecorder{reply: `{"lines":[
		{"line":1,"parts":[{"from":0,"to":0,"kind":"speech","tag":"","sound":"","seconds":0},{"from":1,"to":2,"kind":"effect","tag":"","sound":"two heavy stomps","seconds":1.2}]},
		{"line":2,"parts":[{"from":0,"to":0,"kind":"vocal","tag":"yawns","sound":"","seconds":0},{"from":1,"to":2,"kind":"speech","tag":"","sound":"","seconds":0}]},
		{"line":4,"parts":[{"from":0,"to":5,"kind":"speech","tag":"","sound":"","seconds":0}]},
		{"line":9,"parts":[]}
	]}`}}
	got, err := PlanSounds(context.Background(), ai, story, page, []byte("png"), lines)
	if err != nil {
		t.Fatal(err)
	}
	if ai.schema != "sounds" || ai.images != 1 {
		t.Fatalf("call: %s with %d images", ai.schema, ai.images)
	}
	if len(got[0]) != 2 || got[0][1].Kind != "effect" || got[0][1].Sound != "two heavy stomps" || got[0][1].Seconds != 1.2 {
		t.Errorf("line 1: %+v", got[0])
	}
	if len(got[1]) != 2 || got[1][0].Tag != "yawns" || got[1][1].From != 1 {
		t.Errorf("line 2: %+v", got[1])
	}
	// Not answered, and answered wrongly: both stay plain speech.
	for _, i := range []int{2, 3} {
		if len(got[i]) != 1 || got[i][0].Kind != "speech" || got[i][0].To != len(lines[i].Words)-1 {
			t.Errorf("line %d should fall back to speech: %+v", i+1, got[i])
		}
	}
	for _, want := range []string{"Line 1 (caption, narrator): [0]Then... [1]STOMP! [2]STOMP!", "Line 2 (bubble, Mara): [0]Yaaawn...", `"vocal"`, `"effect"`, "Page 2: Bannette stomps in."} {
		if !strings.Contains(ai.user, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}

	// No lines: no call. A failed call is an error.
	ai.user = ""
	if got, err := PlanSounds(context.Background(), ai, story, page, nil, nil); err != nil || len(got) != 0 || ai.user != "" {
		t.Fatalf("empty page: %v %v", got, err)
	}
	ai.err = errors.New("down")
	if _, err := PlanSounds(context.Background(), ai, story, page, nil, lines); err == nil {
		t.Fatal("error not propagated")
	}
}

func TestScriptAndAlignTokens(t *testing.T) {
	w := func(text, tag string) LetterWord { return LetterWord{Text: text, Tag: tag} }
	text, owners := Script([]LetterWord{w("YAAAWN...", "yawns"), w("SO", ""), w("SLEEPY...", "")})
	if text != "[yawns] So sleepy..." {
		t.Fatalf("script: %q", text)
	}
	if len(owners) != 3 || owners[0][0] != 0 || owners[2][0] != 2 {
		t.Fatalf("owners: %v", owners)
	}
	// A run of words performed as one vocal is one tag; a vocal alone gets
	// the ellipsis eleven_v3 needs.
	text, owners = Script([]LetterWord{w("HA", "laughs"), w("HA!", "laughs")})
	if text != "[laughs]..." || len(owners) != 1 || len(owners[0]) != 2 {
		t.Fatalf("vocal alone: %q %v", text, owners)
	}
	// Timings: one per token, shared by a vocal's words.
	text, owners = Script([]LetterWord{w("Ha", "laughs"), w("ha!", "laughs"), w("Funny.", "")})
	spoken := []WordTiming{{"[laughs]", 0, 0.8}, {"Funny.", 0.9, 1.4}}
	times := AlignTokens(3, owners, text, spoken)
	if times[0] != [2]float64{0, 0.8} || times[1] != [2]float64{0, 0.8} || times[2] != [2]float64{0.9, 1.4} {
		t.Fatalf("times: %v", times)
	}
	// Split differently by the voice: matched by position, nothing empty.
	times = AlignTokens(3, owners, text, []WordTiming{{"[laughs]Funny.", 0, 1.4}})
	for i, tt := range times {
		if tt[1] == 0 {
			t.Fatalf("word %d untimed: %v", i, times)
		}
	}
	if got := AlignTokens(2, nil, "", nil); got[1] != [2]float64{} {
		t.Fatal("nothing to align")
	}
}

func TestFakeSoundDesignAndSound(t *testing.T) {
	lines := []PlanLine{
		{Kind: "caption", Words: []string{"Then...", "POOF!", "Gone."}},
		{Kind: "bubble", Speaker: "Mara", Words: []string{"Yawn...", "night"}},
	}
	got, err := PlanSounds(context.Background(), &Fake{}, &store.Story{}, &store.Page{}, nil, lines)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0]) != 3 || got[0][1].Kind != "effect" || got[0][1].Sound == "" {
		t.Fatalf("fake effect: %+v", got[0])
	}
	if got[1][0].Kind != "vocal" || got[1][0].Tag != "yawns" || got[1][1].Kind != "speech" {
		t.Fatalf("fake vocal: %+v", got[1])
	}
	sp, err := FakeVoice{}.Sound(context.Background(), "poof", 1.5)
	if err != nil || sp.Ext != "wav" || len(sp.Audio) != 44+2*int(1.5*fakeRate) {
		t.Fatalf("fake sound: %v %d", err, len(sp.Audio))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (FakeVoice{}).Sound(ctx, "x", 1); err == nil {
		t.Fatal("cancelled context ignored")
	}
}
