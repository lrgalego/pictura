// Package usage describes one call to an outside model service — who made
// it, what it cost, how it went — so the API clients can report every call
// without knowing where the record goes.
package usage

import (
	"context"
	"time"
)

// Providers.
const (
	Meta       = "meta"
	ElevenLabs = "elevenlabs"
)

// Kinds of call.
const (
	Chat    = "chat"    // a text model answer (Muse Spark)
	Image   = "image"   // a drawn image (Muse Image)
	Segment = "segment" // finding things on an image (SAM)
	Speech  = "speech"  // a voiced line (ElevenLabs)
	Sound   = "sound"   // a sound effect (ElevenLabs)
)

// Call is one request to a provider and what it cost.
type Call struct {
	At       time.Time
	Provider string
	Kind     string
	Model    string
	// Op is the agent the call served: the answer schema of a chat call
	// ("pages", "review"), "sheet" or "page" for an image, the phrase for
	// a segmentation.
	Op      string
	StoryID int64  // 0 when the call was made outside a story's job
	Job     string // the job kind that made it, if any
	OK      bool
	Error   string
	// Meta reports tokens per answer.
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // part of OutputTokens
	CachedTokens    int // part of InputTokens
	// ElevenLabs bills credits: Chars sent (speech), Seconds asked for
	// (sound), and Credits when the answer said what it charged.
	Chars    int
	Seconds  float64
	Credits  int
	Duration time.Duration
}

// Recorder keeps a call. It must be safe for concurrent use.
type Recorder func(ctx context.Context, c Call)

type ctxKey struct{}

type origin struct {
	storyID int64
	job     string
}

// WithJob marks every call made under ctx as made by that story's job.
func WithJob(ctx context.Context, storyID int64, job string) context.Context {
	return context.WithValue(ctx, ctxKey{}, origin{storyID, job})
}

// Stamp fills in the story and job of ctx, and the time.
func Stamp(ctx context.Context, c Call) Call {
	if o, ok := ctx.Value(ctxKey{}).(origin); ok {
		c.StoryID, c.Job = o.storyID, o.job
	}
	if c.At.IsZero() {
		c.At = time.Now()
	}
	return c
}
