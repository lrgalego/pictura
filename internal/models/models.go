// Package models is the catalog of models a story can pick for each role
// (who reads the script, who draws the pages, who reads aloud), and the
// router that hands the jobs the client for a story's pick.
package models

import (
	"context"

	"github.com/lrgalego/pictura/internal/elevenlabs"
	"github.com/lrgalego/pictura/internal/gemini"
	"github.com/lrgalego/pictura/internal/meta"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/internal/usage"
)

// Kind is what a model makes.
type Kind string

const (
	Text  Kind = "text"
	Image Kind = "image"
	Voice Kind = "voice"
)

// Model is one pickable model.
type Model struct {
	ID       string // what its provider calls it
	Provider string // usage.Meta, usage.Gemini, usage.ElevenLabs
	Kind     Kind
	Name     string // shown in the picker
	Note     string // what it is good for, in a word or two: it shares a phone-width select with the name
}

// Catalog is every model on offer, the default of each kind first.
var Catalog = []Model{
	{meta.DefaultTextModel, usage.Meta, Text, "Muse Spark", "Meta"},
	{"gemini-4-hydrogen", usage.Gemini, Text, "Gemini 4 Hydrogen", "Fast and sharp"},
	{"gemini-4-argon", usage.Gemini, Text, "Gemini 4 Argon", "Deepest, slower"},
	{meta.DefaultImageModel, usage.Meta, Image, "Muse Image", "Meta"},
	{"gemini-3-pro-image", usage.Gemini, Image, "Nano Banana Pro", "Best likeness"},
	{"gemini-nano-banana-2.1", usage.Gemini, Image, "Nano Banana 2.1", "Quicker, cheaper"},
	{elevenlabs.DefaultModel, usage.ElevenLabs, Voice, "ElevenLabs", "Premium"},
	{gemini.DefaultSpeechModel, usage.Gemini, Voice, "Gemini Flash Lite TTS", "Best value"},
	{"gemini-3.8-flash-tts", usage.Gemini, Voice, "Gemini Flash TTS", "More range"},
}

// Roles a story picks a model for.
const (
	Script     = "script"
	Sheets     = "sheets"
	Storyboard = "storyboard"
	Pages      = "pages"
	Voices     = "voice"
)

// Role is a job a model does for a story.
type Role struct {
	Key   string
	Kind  Kind
	Label string // "Script reader"
	Verb  string // how the picker reads: "Read by"
}

// Roles in the order a story meets them.
var Roles = []Role{
	{Script, Text, "Script reader", "Read by"},
	{Sheets, Image, "Character artist", "Sheets drawn by"},
	{Storyboard, Text, "Storyboard artist", "Planned by"},
	{Pages, Image, "Page artist", "Pages drawn by"},
	{Voices, Voice, "Voice", "Voiced by"},
}

// RoleByKey looks a role up.
func RoleByKey(key string) (Role, bool) {
	for _, r := range Roles {
		if r.Key == key {
			return r, true
		}
	}
	return Role{}, false
}

// ByID looks a model up in the catalog.
func ByID(id string) (Model, bool) {
	for _, m := range Catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// VoiceTag is how a line voiced with a voice is marked, so a line made by
// another voice model reads as stale and is voiced again. ElevenLabs keeps
// the bare voice id it always had, so lines voiced before there was a
// choice stay as they are. It reads only the story's pick: the readiness
// check and the narration must agree whatever is configured.
func VoiceTag(st *store.Story, voiceID string) string {
	m, ok := ByID(st.Models[Voices])
	if !ok || m.Provider == usage.ElevenLabs {
		return voiceID
	}
	return m.ID + "/" + voiceID
}

// Router hands out the client for a story's pick. A provider without a key
// is nil and its models are not offered; offline (Fake set) every model is
// offered and the fakes stand in for all of them.
type Router struct {
	Meta   *meta.Client
	Gemini *gemini.Client
	Eleven *elevenlabs.Client
	// Fake and FakeVoice, when set, answer for every model.
	Fake      pipeline.AI
	FakeVoice pipeline.Speaker
}

// Available reports whether a model can be used here.
func (r *Router) Available(m Model) bool {
	if r.Fake != nil {
		return true
	}
	switch m.Provider {
	case usage.Meta:
		return r.Meta != nil
	case usage.Gemini:
		return r.Gemini != nil
	case usage.ElevenLabs:
		return r.Eleven != nil
	}
	return false
}

// Options are the models a role can pick from here.
func (r *Router) Options(role string) []Model {
	rl, ok := RoleByKey(role)
	if !ok {
		return nil
	}
	var out []Model
	for _, m := range Catalog {
		if m.Kind == rl.Kind && r.Available(m) {
			out = append(out, m)
		}
	}
	return out
}

// Pick is the model a story uses for a role: its own pick when that is
// on offer, else the role's default (the first on offer).
func (r *Router) Pick(st *store.Story, role string) Model {
	opts := r.Options(role)
	if st != nil {
		for _, m := range opts {
			if m.ID == st.Models[role] {
				return m
			}
		}
	}
	if len(opts) == 0 {
		return Model{}
	}
	return opts[0]
}

// AI is the model client for a story's text or image role.
func (r *Router) AI(st *store.Story, role string) pipeline.AI {
	if r.Fake != nil {
		return r.Fake
	}
	m := r.Pick(st, role)
	rl, _ := RoleByKey(role)
	text, image := "", ""
	if rl.Kind == Image {
		image = m.ID
	} else {
		text = m.ID
	}
	switch {
	case m.Provider == usage.Gemini && r.Gemini != nil:
		return r.Gemini.With(text, image, "")
	case r.Meta != nil:
		if m.Provider != usage.Meta {
			text, image = "", ""
		}
		return r.Meta.With(text, image)
	case r.Gemini != nil:
		return r.Gemini
	}
	return nil
}

// Speaker is the voice for a story's pick. Sound effects are ElevenLabs'
// whatever the voice, as Gemini makes none.
func (r *Router) Speaker(st *store.Story) pipeline.Speaker {
	if r.Fake != nil {
		return r.FakeVoice
	}
	m := r.Pick(st, Voices)
	switch {
	case m.Provider == usage.Gemini && r.Gemini != nil:
		s := speaker{voice: r.Gemini.With("", "", m.ID), sounds: r.Gemini}
		if r.Eleven != nil {
			s.sounds = r.Eleven
		}
		return s
	case r.Eleven != nil:
		return r.Eleven
	}
	return nil
}

// speaker voices with one service and makes sound effects with another.
type speaker struct {
	voice  pipeline.Speaker
	sounds pipeline.Sounder
}

func (s speaker) Speak(ctx context.Context, text, voiceID string) (*pipeline.Speech, error) {
	return s.voice.Speak(ctx, text, voiceID)
}

func (s speaker) Sound(ctx context.Context, prompt string, seconds float64) (*pipeline.Speech, error) {
	return s.sounds.Sound(ctx, prompt, seconds)
}
