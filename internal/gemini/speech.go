package gemini

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/usage"
)

// sampleRate is what Gemini speech comes back in: 24 kHz, 16-bit, mono.
const sampleRate = 24000

// Speech goes through the Interactions API, not generateContent: there the
// 3.8 TTS models take how a line is said as a style annotation beside the
// text. Through generateContent the same models read a style prefix, or a
// "[shouting]" tag, out loud (a bake-off on real lines: 10 of 11 clips).

type interactionText struct {
	Type        string              `json:"type"`
	Text        string              `json:"text"`
	Annotations []map[string]string `json:"annotations,omitempty"`
}

type interactionInput struct {
	Type    string            `json:"type"`
	Content []interactionText `json:"content"`
}

type interactionRequest struct {
	Model            string             `json:"model"`
	Input            []interactionInput `json:"input"`
	ResponseFormat   map[string]string  `json:"response_format"`
	GenerationConfig struct {
		SpeechConfig []map[string]string `json:"speech_config"`
	} `json:"generation_config"`
}

// interactionPart is a piece of an interaction's output; audio comes as
// one, under a step's content or on its own.
type interactionPart struct {
	Type     string          `json:"type"`
	Data     string          `json:"data"`
	MimeType string          `json:"mime_type"`
	Content  json.RawMessage `json:"content"`
}

type interactionResponse struct {
	Steps   []interactionPart `json:"steps"`
	Outputs []interactionPart `json:"outputs"`
	Usage   *struct {
		Input    int `json:"total_input_tokens"`
		Output   int `json:"total_output_tokens"`
		Thoughts int `json:"total_thought_tokens"`
		Cached   int `json:"total_cached_tokens"`
	} `json:"usage"`
}

func (r *interactionResponse) tokens() (int, int, int, int) {
	if r.Usage == nil {
		return 0, 0, 0, 0
	}
	return r.Usage.Input, r.Usage.Output + r.Usage.Thoughts, r.Usage.Thoughts, r.Usage.Cached
}

// audio is the first audio part of the answer.
func (r *interactionResponse) audio() *interactionPart {
	for _, s := range append(r.Steps, r.Outputs...) {
		if s.Type == "audio" && s.Data != "" {
			return &s
		}
		var parts []interactionPart
		if json.Unmarshal(s.Content, &parts) == nil {
			for _, p := range parts {
				if p.Type == "audio" && p.Data != "" {
					return &p
				}
			}
		}
	}
	return nil
}

// Speak voices one line in a catalog voice and times its words. Gemini
// gives no alignment, so the words are timed from the loudness of the
// clip (pipeline.TimeWords), against the line as Pictura wrote it. The
// voice id is a catalog id, which maps to a Gemini voice and the accent to
// ask of it, or a Gemini voice name.
func (c *Client) Speak(ctx context.Context, text, voiceID string) (*pipeline.Speech, error) {
	name, accent := pipeline.GeminiVoice(voiceID)
	said, style := SpeechText(text, accent)
	content := interactionText{Type: "text", Text: said}
	if style != "" {
		content.Annotations = []map[string]string{{"type": "speech_metadata", "style": style}}
	}
	req := interactionRequest{
		Model:          c.SpeechModel,
		Input:          []interactionInput{{Type: "user_input", Content: []interactionText{content}}},
		ResponseFormat: map[string]string{"type": "audio"},
	}
	req.GenerationConfig.SpeechConfig = []map[string]string{{"voice": name}}
	var resp interactionResponse
	if err := c.post(ctx, c.SpeechModel, "/interactions", req, usage.Call{Kind: usage.Speech, Chars: len([]rune(text))}, &resp); err != nil {
		return nil, err
	}
	if a := resp.audio(); a != nil {
		raw, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, fmt.Errorf("gemini %s: %w", c.SpeechModel, err)
		}
		if samples, rate := pcm(raw, a.MimeType); len(samples) > 0 {
			return &pipeline.Speech{Audio: wav(samples, rate), Ext: "wav", Words: pipeline.TimeWords(samples, rate, text)}, nil
		}
	}
	return nil, fmt.Errorf("gemini %s: %w", c.SpeechModel, pipeline.ErrNoAudio)
}

var tagRe = regexp.MustCompile(`\[([^\[\]]+)\]`)

// cues are Gemini's cue for each vocal Pictura's sound designer tags.
var cues = map[string]string{
	"yawns": "yawn", "laughs": "laugh", "giggles": "giggle", "sighs": "sigh", "gasps": "gasp",
	"growls": "growl", "snores": "snore", "screams": "scream", "cries": "cry",
}

// SpeechText turns a line written for ElevenLabs into what Gemini is
// handed: the words to say, and how to say them. A vocal tag ("[laughs]")
// is a sound the speaker makes and becomes Gemini's cue for it
// ("<laugh>"); any other tag ("[whispers] Hello") is the delivery and goes
// to the style, with the voice's accent.
func SpeechText(text, accent string) (said, style string) {
	var notes []string
	said = tagRe.ReplaceAllStringFunc(text, func(tag string) string {
		t := strings.ToLower(strings.TrimSpace(strings.Trim(tag, "[]")))
		if pipeline.IsVocalTag(t) {
			return "<" + cues[t] + ">"
		}
		notes = append(notes, "Say it "+t)
		return ""
	})
	said = strings.Join(strings.Fields(said), " ")
	if accent != "" {
		notes = append(notes, "with a "+accent+" accent")
	}
	return said, strings.Join(notes, ", ")
}

// Sound is not something Gemini makes; sound effects stay with ElevenLabs.
func (c *Client) Sound(context.Context, string, float64) (*pipeline.Speech, error) {
	return nil, fmt.Errorf("gemini: sound effects are not offered")
}

// pcm reads 16-bit mono samples from a WAV file or from raw L16
// ("audio/L16;codec=pcm;rate=24000"), with their sample rate.
func pcm(raw []byte, mime string) ([]int16, int) {
	rate := sampleRate
	if i := strings.Index(mime, "rate="); i >= 0 {
		if r, err := strconv.Atoi(strings.TrimSpace(strings.Split(mime[i+5:], ";")[0])); err == nil && r > 0 {
			rate = r
		}
	}
	data := raw
	if len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WAVE" {
		data = nil
		for off := 12; off+8 <= len(raw); {
			id, size := string(raw[off:off+4]), int(binary.LittleEndian.Uint32(raw[off+4:off+8]))
			body := raw[off+8 : min(off+8+size, len(raw))]
			switch id {
			case "fmt ":
				if len(body) >= 8 {
					rate = int(binary.LittleEndian.Uint32(body[4:8]))
				}
			case "data":
				data = body
			}
			off += 8 + size + size%2
		}
	}
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[2*i:]))
	}
	return out, rate
}

// wav wraps 16-bit mono samples in a WAV header, which every browser plays.
func wav(samples []int16, rate int) []byte {
	n := len(samples) * 2
	b := make([]byte, 44+n)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	return b
}
