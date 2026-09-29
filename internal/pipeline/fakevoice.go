package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/fnv"
	"math"
	"strings"
)

// FakeVoice is an offline Speaker: every word becomes a short tone whose
// pitch depends on the voice, with timings that match the tones exactly. It
// lets the read-aloud flow run (and be tested) without an API key, and you
// can hear the highlight keep time.
type FakeVoice struct{}

const fakeRate = 22050

func (FakeVoice) Speak(ctx context.Context, text, voiceID string) (*Speech, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h := fnv.New32a()
	h.Write([]byte(voiceID))
	pitch := 220 + float64(h.Sum32()%240) // 220–460 Hz, stable per voice

	var samples []int16
	var words []WordTiming
	at := func() float64 { return float64(len(samples)) / fakeRate }
	silence := func(sec float64) {
		samples = append(samples, make([]int16, int(sec*fakeRate))...)
	}
	silence(0.1)
	for _, w := range strings.Fields(text) {
		dur := math.Max(0.18, 0.055*float64(len([]rune(w))))
		start := at()
		n := int(dur * fakeRate)
		for i := 0; i < n; i++ {
			env := math.Sin(math.Pi * float64(i) / float64(n)) // fade in and out
			samples = append(samples, int16(6000*env*math.Sin(2*math.Pi*pitch*float64(i)/fakeRate)))
		}
		words = append(words, WordTiming{Text: w, Start: start, End: at()})
		gap := 0.08
		if strings.ContainsAny(w[len(w)-1:], ".!?…,") {
			gap = 0.25
		}
		silence(gap)
	}
	return &Speech{Audio: wav(samples), Ext: "wav", Words: words}, nil
}

// wav wraps 16-bit mono PCM in a RIFF header.
func wav(samples []int16) []byte {
	var b bytes.Buffer
	data := len(samples) * 2
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))       // fmt chunk size
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))        // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))        // mono
	_ = binary.Write(&b, binary.LittleEndian, uint32(fakeRate)) // sample rate
	_ = binary.Write(&b, binary.LittleEndian, uint32(fakeRate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))  // block align
	_ = binary.Write(&b, binary.LittleEndian, uint16(16)) // bits per sample
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(data))
	_ = binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

// Sound is the offline sound effect: a burst of noise, as long as asked,
// fading out.
func (FakeVoice) Sound(ctx context.Context, prompt string, seconds float64) (*Speech, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h := fnv.New32a()
	h.Write([]byte(prompt))
	seed := h.Sum32() | 1
	n := int(seconds * fakeRate)
	samples := make([]int16, n)
	for i := range samples {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		fade := 1 - float64(i)/float64(n)
		samples[i] = int16(float64(int16(seed)) * 0.2 * fade)
	}
	return &Speech{Audio: wav(samples), Ext: "wav"}, nil
}
