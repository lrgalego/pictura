package models

import (
	"context"
	"testing"

	"github.com/lrgalego/pictura/internal/elevenlabs"
	"github.com/lrgalego/pictura/internal/gemini"
	"github.com/lrgalego/pictura/internal/meta"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
)

func story(picks map[string]string) *store.Story {
	return &store.Story{Models: picks}
}

func TestOptionsFollowWhatIsConfigured(t *testing.T) {
	r := &Router{Meta: meta.New("k"), Eleven: elevenlabs.New("k")}
	if got := r.Options(Pages); len(got) != 1 || got[0].ID != meta.DefaultImageModel {
		t.Errorf("Meta only: %+v", got)
	}
	r.Gemini = gemini.New("k")
	if got := r.Options(Pages); len(got) != 3 || got[1].ID != "gemini-3-pro-image" {
		t.Errorf("with Gemini: %+v", got)
	}
	if got := r.Options(Voices); len(got) != 3 || got[0].ID != elevenlabs.DefaultModel {
		t.Errorf("voices: %+v", got)
	}
	if r.Options("nonsense") != nil {
		t.Error("an unknown role offers nothing")
	}
	fake := &Router{Fake: &pipeline.Fake{}}
	if len(fake.Options(Script)) != 3 {
		t.Error("offline, every model is on offer")
	}
}

func TestPick(t *testing.T) {
	r := &Router{Meta: meta.New("k"), Gemini: gemini.New("k")}
	if m := r.Pick(nil, Storyboard); m.ID != meta.DefaultTextModel {
		t.Errorf("default %s", m.ID)
	}
	if m := r.Pick(story(map[string]string{Storyboard: "gemini-4-hydrogen"}), Storyboard); m.ID != "gemini-4-hydrogen" {
		t.Errorf("picked %s", m.ID)
	}
	// A pick that is not on offer here (an image model for a text role, a
	// provider without a key) falls back to the default.
	if m := r.Pick(story(map[string]string{Storyboard: "gemini-3-pro-image"}), Storyboard); m.ID != meta.DefaultTextModel {
		t.Errorf("wrong kind: %s", m.ID)
	}
	noGemini := &Router{Meta: meta.New("k")}
	if m := noGemini.Pick(story(map[string]string{Pages: "gemini-3-pro-image"}), Pages); m.ID != meta.DefaultImageModel {
		t.Errorf("no key: %s", m.ID)
	}
	if m := (&Router{}).Pick(nil, Voices); m.ID != "" {
		t.Errorf("nothing configured: %+v", m)
	}
}

func TestAI(t *testing.T) {
	r := &Router{Meta: meta.New("k"), Gemini: gemini.New("k")}
	st := story(map[string]string{Pages: "gemini-3-pro-image", Storyboard: "gemini-4-argon"})
	if g, ok := r.AI(st, Pages).(*gemini.Client); !ok || g.ImageModel != "gemini-3-pro-image" {
		t.Errorf("pages: %#v", r.AI(st, Pages))
	}
	if g, ok := r.AI(st, Storyboard).(*gemini.Client); !ok || g.TextModel != "gemini-4-argon" {
		t.Errorf("storyboard: %#v", r.AI(st, Storyboard))
	}
	if m, ok := r.AI(st, Script).(*meta.Client); !ok || m.TextModel != meta.DefaultTextModel {
		t.Errorf("script: %#v", r.AI(st, Script))
	}
	if r.AI(st, Pages) == r.AI(st, Pages) {
		t.Error("each call gets its own bound copy")
	}
	onlyGemini := &Router{Gemini: gemini.New("k")}
	if _, ok := onlyGemini.AI(nil, Script).(*gemini.Client); !ok {
		t.Error("with only Gemini configured, Gemini answers")
	}
	if (&Router{}).AI(nil, Script) != nil {
		t.Error("nothing configured, no client")
	}
	fake := &pipeline.Fake{}
	if (&Router{Fake: fake}).AI(st, Pages) != fake {
		t.Error("offline, the fake answers")
	}
}

func TestSpeaker(t *testing.T) {
	eleven := elevenlabs.New("k")
	r := &Router{Meta: meta.New("k"), Gemini: gemini.New("k"), Eleven: eleven}
	if r.Speaker(nil) != eleven {
		t.Error("ElevenLabs is the default voice")
	}
	st := story(map[string]string{Voices: "gemini-3.8-flash-tts"})
	sp, ok := r.Speaker(st).(speaker)
	if !ok || sp.voice.(*gemini.Client).SpeechModel != "gemini-3.8-flash-tts" || sp.sounds != eleven {
		t.Fatalf("gemini voice: %#v", r.Speaker(st))
	}
	noEleven := &Router{Gemini: gemini.New("k")}
	sp = noEleven.Speaker(st).(speaker)
	if _, err := sp.Sound(context.Background(), "boom", 1); err == nil {
		t.Error("without ElevenLabs there are no sound effects")
	}
	if (&Router{}).Speaker(st) != nil {
		t.Error("nothing configured, no voice")
	}
	fv := pipeline.FakeVoice{}
	if (&Router{Fake: &pipeline.Fake{}, FakeVoice: fv}).Speaker(st) != fv {
		t.Error("offline, the fake voice speaks")
	}
}

func TestSpeakerSpeaks(t *testing.T) {
	sp := speaker{voice: pipeline.FakeVoice{}, sounds: pipeline.FakeVoice{}}
	if s, err := sp.Speak(context.Background(), "Hello there.", "Kore"); err != nil || len(s.Words) != 2 {
		t.Errorf("%v %v", s, err)
	}
	if _, err := sp.Sound(context.Background(), "boom", 1); err != nil {
		t.Error(err)
	}
}

func TestVoiceTag(t *testing.T) {
	if VoiceTag(story(nil), "v1") != "v1" || VoiceTag(story(map[string]string{Voices: elevenlabs.DefaultModel}), "v1") != "v1" {
		t.Error("ElevenLabs keeps the bare voice id")
	}
	if got := VoiceTag(story(map[string]string{Voices: "gemini-3.8-flash-tts"}), "v1"); got != "gemini-3.8-flash-tts/v1" {
		t.Errorf("gemini: %s", got)
	}
}

func TestLookups(t *testing.T) {
	if r, ok := RoleByKey(Pages); !ok || r.Kind != Image {
		t.Error("pages")
	}
	if _, ok := RoleByKey("x"); ok {
		t.Error("x")
	}
	if m, ok := ByID("gemini-4-hydrogen"); !ok || m.Kind != Text {
		t.Error("hydrogen")
	}
	if _, ok := ByID("x"); ok {
		t.Error("x")
	}
	if (&Router{}).Available(Model{Provider: "other"}) {
		t.Error("an unknown provider is never available")
	}
}
