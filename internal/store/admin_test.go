package store

import (
	"context"
	"testing"
	"time"

	"github.com/lrgalego/pictura/internal/usage"
)

func TestCallsUsageAndStates(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	u, st := seed(t, s)
	now := time.Now()
	calls := []usage.Call{
		{At: now.Add(-48 * time.Hour), Provider: usage.Meta, Kind: usage.Chat, Model: "spark", Op: "pages", StoryID: st.ID, OK: true, InputTokens: 1000, OutputTokens: 500},
		{At: now.Add(-time.Hour), Provider: usage.Meta, Kind: usage.Chat, Model: "spark", Op: "pages", StoryID: st.ID, OK: true, InputTokens: 100, OutputTokens: 50, ReasoningTokens: 40, CachedTokens: 10, Duration: 2 * time.Second},
		{At: now.Add(-50 * time.Minute), Provider: usage.Meta, Kind: usage.Image, Model: "muse", Op: "page", StoryID: st.ID, OK: true},
		{At: now.Add(-40 * time.Minute), Provider: usage.Meta, Kind: usage.Image, Model: "muse", Op: "page", StoryID: st.ID, OK: false, Error: "meta api: busy"},
		{At: now.Add(-30 * time.Minute), Provider: usage.ElevenLabs, Kind: usage.Speech, Model: "v4", StoryID: st.ID, OK: true, Chars: 120, Credits: 60},
		{At: now.Add(-20 * time.Minute), Provider: usage.ElevenLabs, Kind: usage.Speech, Model: "v4", StoryID: st.ID, OK: false, Chars: 30, Error: "elevenlabs: You have 0 credits remaining (quota_exceeded)"},
		{Provider: usage.ElevenLabs, Kind: usage.Sound, Model: "sfx", OK: true, Seconds: 2.5},
	}
	for _, c := range calls {
		if err := s.RecordCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	day, err := s.Usage(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]AgentUsage{}
	for _, a := range day {
		byKey[a.Provider+"/"+a.Kind+"/"+a.Op] = a
	}
	if p := byKey["meta/chat/pages"]; p.Calls != 1 || p.InputTokens != 100 || p.OutputTokens != 50 || p.ReasoningTokens != 40 || p.CachedTokens != 10 || p.Busy != 2*time.Second || p.Model != "spark" {
		t.Fatalf("the window leaves the old call out: %+v", p)
	}
	if i := byKey["meta/image/page"]; i.Calls != 2 || i.Failures != 1 {
		t.Fatalf("images: %+v", i)
	}
	if v := byKey["elevenlabs/speech/"]; v.Chars != 150 || v.Credits != 60 || v.Failures != 1 {
		t.Fatalf("speech: %+v", v)
	}
	if f := byKey["elevenlabs/sound/"]; f.Seconds != 2.5 {
		t.Fatalf("sound: %+v", f)
	}
	month, _ := s.Usage(ctx, now.Add(-30*24*time.Hour))
	for _, a := range month {
		if a.Op == "pages" && a.Calls != 2 {
			t.Fatalf("the month counts both: %+v", a)
		}
	}

	states, err := s.AgentStates(ctx)
	if err != nil || len(states) != 4 {
		t.Fatalf("states: %+v %v", states, err)
	}
	for _, a := range states {
		switch a.Provider + "/" + a.Kind {
		case "meta/image":
			if a.LastOK || a.LastError != "meta api: busy" || a.LastOKAt.IsZero() || !a.LastOKAt.Before(a.LastErrorAt) {
				t.Fatalf("image state: %+v", a)
			}
		case "meta/chat":
			if !a.LastOK || a.LastError != "" || !a.LastErrorAt.IsZero() {
				t.Fatalf("chat state: %+v", a)
			}
		}
	}

	stories, err := s.UsageByStory(ctx, now.Add(-24*time.Hour), 5)
	if err != nil || len(stories) != 1 || stories[0].Title != st.Title || stories[0].Username != u.Username || stories[0].Images != 1 || stories[0].Chars != 150 || stories[0].Calls != 5 {
		t.Fatalf("by story: %+v %v", stories, err)
	}

	msg, at, err := s.LastErrorLike(ctx, usage.ElevenLabs, "quota_exceeded")
	if err != nil || msg == "" || at.IsZero() {
		t.Fatalf("last refusal: %q %v %v", msg, at, err)
	}
	if msg, _, err := s.LastErrorLike(ctx, usage.Meta, "quota_exceeded"); err != nil || msg != "" {
		t.Fatalf("no such error: %q %v", msg, err)
	}
	if ok, err := s.LastOKAt(ctx, usage.ElevenLabs, usage.Speech); err != nil || ok.IsZero() || !ok.Before(at) {
		t.Fatalf("last voiced: %v %v", ok, err)
	}
	if ok, err := s.LastOKAt(ctx, "nobody", usage.Speech); err != nil || !ok.IsZero() {
		t.Fatalf("never: %v %v", ok, err)
	}
}

func TestAdminsAndJobsAcrossStories(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	u, st := seed(t, s)
	if u.Admin {
		t.Fatal("nobody is an admin by default")
	}
	if err := s.SetUserAdmin(ctx, u.Username, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); !got.Admin {
		t.Fatal("made an admin")
	}
	if users, _ := s.Users(ctx); !users[0].Admin {
		t.Fatal("the list says so")
	}
	if err := s.SetUserAdmin(ctx, "nobody", true); err != ErrNotFound {
		t.Fatalf("unknown user: %v", err)
	}
	running, _ := s.CreateJob(ctx, st.ID, 0, "render", "Drawing", 4)
	_ = s.StartJob(ctx, running.ID)
	failed, _ := s.CreateJob(ctx, st.ID, 0, "narrate", "Voices", 0)
	_ = s.FinishJob(ctx, failed.ID, "3 of 9 lines have no audio")
	active, err := s.ActiveJobs(ctx)
	if err != nil || len(active) != 1 || active[0].ID != running.ID || active[0].Title != st.Title || active[0].Username != u.Username || active[0].Total != 4 {
		t.Fatalf("active: %+v %v", active, err)
	}
	bad, err := s.FailedJobs(ctx, 5)
	if err != nil || len(bad) != 1 || bad[0].Kind != "narrate" || bad[0].Error == "" || bad[0].UpdatedAt.IsZero() {
		t.Fatalf("failed: %+v %v", bad, err)
	}
}
