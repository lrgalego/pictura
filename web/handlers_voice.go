package web

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/lrgalego/htmx-ds/components"
	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/web/views"
)

// ---------- voices: casting, picking, previews ----------

// characterVoicePanel lists the catalog beside a character, each voice
// playable with the character's own line.
func (s *server) characterVoicePanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.VoicePanel(views.VoicePicker{
		Story: st, Character: c, Current: pipeline.CharacterVoice(c).ID, Voices: pipeline.Voices,
	}))
}

// characterVoice sets a character's voice. It writes only the voice
// column, so it needs no queue: a revision running meanwhile cannot undo it.
func (s *server) characterVoice(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	v, ok := pipeline.VoiceByID(field(r, "voice"))
	if !ok {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, errorToast(errUnknownVoice))
		return
	}
	if err := s.st.SetCharacterVoice(r.Context(), c.ID, v.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.answerCharacter(w, r, st, c, views.ClosePanel(), toast(components.ToastSuccess, c.Name+" now speaks as "+v.Name, ""))
}

func (s *server) narratorVoicePanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.VoicePanel(views.VoicePicker{
		Story: st, Current: pipeline.NarratorVoice(st).ID, Voices: pipeline.Voices,
	}))
}

func (s *server) narratorVoice(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	v, ok := pipeline.VoiceByID(field(r, "voice"))
	if !ok {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, errorToast(errUnknownVoice))
		return
	}
	if err := s.st.SetNarratorVoice(r.Context(), st.ID, v.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	st.NarratorVoice = v.ID
	s.answerCharacters(w, r, st, views.ClosePanel(), toast(components.ToastSuccess, "The narrator is now "+v.Name, ""))
}

// castVoices asks the editor to cast every character still speaking with a
// stand-in voice (stories made before voices existed).
func (s *server) castVoices(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.CastVoices(st.ID); err != nil {
		s.answerCharacters(w, r, st, errorToast(err))
		return
	}
	s.answerCharacters(w, r, st, toast(components.ToastSuccess, "Casting voices", "Each character gets a voice that fits their description."))
}

type voiceError string

func (e voiceError) Error() string { return string(e) }

const errUnknownVoice = voiceError("That voice is not in the catalog.")

// voicePreview speaks a sample in a voice: the character's first line
// (?cid=), or the narrator's first caption without one. Samples are cached
// in memory, so replaying one, or several people trying the same voice,
// costs a single synthesis.
func (s *server) voicePreview(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	v, ok := pipeline.VoiceByID(r.PathValue("voice"))
	if !ok {
		s.notFound(w, r)
		return
	}
	pages, _ := s.st.Pages(r.Context(), st.ID)
	text := pipeline.NarratorSample(st, pages)
	if cid, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64); cid != 0 {
		c, err := s.st.Character(r.Context(), cid)
		if err != nil || c.StoryID != st.ID {
			s.notFound(w, r)
			return
		}
		text = pipeline.SampleLine(c, pages)
	}
	sp, err := s.samples.get(r, s.voice, v.ID, text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", audioType(sp.Ext))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(sp.Audio)
}

func audioType(ext string) string {
	if ext == "wav" {
		return "audio/wav"
	}
	return "audio/mpeg"
}

// sampleCache keeps recent previews. It is small and forgets everything on
// restart; a preview is a nicety, not data.
type sampleCache struct {
	mu    sync.Mutex
	items map[string]*pipeline.Speech
	order []string
}

const sampleCacheSize = 64

func (c *sampleCache) get(r *http.Request, sp pipeline.Speaker, voice, text string) (*pipeline.Speech, error) {
	key := voice + "\x00" + text
	c.mu.Lock()
	if got, ok := c.items[key]; ok {
		c.mu.Unlock()
		return got, nil
	}
	c.mu.Unlock()
	speech, err := sp.Speak(r.Context(), text, voice)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*pipeline.Speech{}
	}
	if _, ok := c.items[key]; !ok {
		c.order = append(c.order, key)
		if len(c.order) > sampleCacheSize {
			delete(c.items, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.items[key] = speech
	return speech, nil
}
