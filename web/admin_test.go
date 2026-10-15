package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lrgalego/pictura/internal/elevenlabs"
	"github.com/lrgalego/pictura/internal/meta"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/internal/usage"
)

func TestAdminIsForAdmins(t *testing.T) {
	e := newEnv(t)
	e.signup("writer")
	for _, p := range []string{"/admin", "/admin/live"} {
		if resp, _ := e.get(p); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s for a writer: %d", p, resp.StatusCode)
		}
	}
	_, body := e.get("/stories")
	if strings.Contains(body, `href="/admin"`) {
		t.Fatal("no admin link for a writer")
	}
	if err := e.st.SetUserAdmin(context.Background(), "writer", true); err != nil {
		t.Fatal(err)
	}
	_, body = e.get("/stories")
	if !strings.Contains(body, `href="/admin"`) {
		t.Fatal("an admin finds the page in the account menu")
	}
	// Logged out: to the login page, like every other page.
	e.post("/logout", nil, false)
	if resp, _ := e.get("/admin"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logged out: %d", resp.StatusCode)
	}
}

func TestAdminPage(t *testing.T) {
	e := newEnv(t)
	e.signup("boss")
	ctx := context.Background()
	_ = e.st.SetUserAdmin(ctx, "boss", true)
	base, _ := e.finished(t)
	sid := storyID(t, base)
	for _, c := range []usage.Call{
		{Provider: usage.Meta, Kind: usage.Chat, Model: "muse-spark", Op: "pages", StoryID: sid, OK: true, InputTokens: 12000, OutputTokens: 3400, ReasoningTokens: 3000},
		{Provider: usage.Meta, Kind: usage.Image, Model: "muse-image", Op: "page", StoryID: sid, OK: true},
		{Provider: usage.Meta, Kind: usage.Segment, Model: "sam", Op: "speech bubble", StoryID: sid, OK: true},
		{Provider: usage.Meta, Kind: usage.Segment, Model: "sam", Op: "caption", StoryID: sid, OK: false, Error: "meta api: busy"},
		{Provider: usage.ElevenLabs, Kind: usage.Speech, Model: "eleven_v4_turbo", Op: "narration", StoryID: sid, OK: false, Chars: 40, Error: "elevenlabs: This request exceeds your quota of 10000. You have 0 credits remaining (quota_exceeded)"},
		{Provider: usage.Meta, Kind: usage.Chat, Model: "muse-spark", Op: "brand-new", OK: true, At: time.Now().Add(-10 * 24 * time.Hour)},
	} {
		if err := e.st.RecordCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := e.get("/admin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: %d", resp.StatusCode)
	}
	for _, want := range []string{
		"Storyboard artist", "Bubble finder", "Narrator", "Out of credits", "Failing", "Not used yet",
		"12k", "3k of them reasoning", "Book", "boss", `hx-get="/admin/live"`, "placeholder", "No job has failed",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Fatalf("admin page missing %q", want)
		}
	}
	// The window counts calls; the older one only shows in the month.
	v, err := (&server{st: e.st, fake: true}).adminView(httptest.NewRequest("GET", "/admin", nil))
	if err != nil || v.Total.Calls != 5 || v.Total.Images != 1 {
		t.Fatalf("day totals: %+v %v", v.Total, err)
	}
	v, _ = (&server{st: e.st, fake: true}).adminView(httptest.NewRequest("GET", "/admin?window=30d", nil))
	if v.Total.Calls != 6 || v.WindowLabel != "Last 30 days" {
		t.Fatalf("month totals: %+v", v.Total)
	}
	_, body = e.get("/admin?window=30d")
	if !strings.Contains(body, "brand-new") || !strings.Contains(body, "Last 30 days") {
		t.Fatal("a call no agent claims gets a row of its own")
	}
	// Offline: nothing to check.
	resp, body = e.get("/admin/live")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "no service to check") {
		t.Fatalf("live offline: %d", resp.StatusCode)
	}
}

func TestAdminLiveChecks(t *testing.T) {
	e := newEnv(t)
	e.signup("boss")
	ctx := context.Background()
	_ = e.st.SetUserAdmin(ctx, "boss", true)
	metaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-requests", "150")
		w.Header().Set("x-ratelimit-remaining-requests", "150")
		w.Header().Set("x-ratelimit-limit-tokens", "3000000")
		w.Header().Set("x-ratelimit-remaining-tokens", "3000000")
		_, _ = io.WriteString(w, `{"data":[{"id":"`+meta.DefaultTextModel+`"},{"id":"`+meta.DefaultImageModel+`"}]}`)
	}))
	defer metaAPI.Close()
	plan := false
	elAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/user/subscription" {
			if !plan {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"detail":{"code":"unauthorized","status":"missing_permissions","message":"missing the permission user_read"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"tier":"creator","status":"active","character_count":2500,"character_limit":100000,"next_character_count_reset_unix":1793000000,"voice_slots_used":2,"voice_limit":30}`)
			return
		}
		_, _ = io.WriteString(w, `{"voices":[]}`)
	}))
	defer elAPI.Close()
	m := meta.New("k")
	m.BaseURL = metaAPI.URL
	el := elevenlabs.New("k")
	el.BaseURL = elAPI.URL + "/v1"
	// Same store and cookies, a server that has the live clients.
	live := httptest.NewServer(Router(Deps{Store: e.st, Jobs: e.runner, Meta: m, Eleven: el}))
	defer live.Close()
	get := func() string {
		t.Helper()
		resp, err := e.client.Get(live.URL + "/admin/live")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("live: %d", resp.StatusCode)
		}
		return string(b)
	}
	_ = e.st.RecordCall(ctx, usage.Call{Provider: usage.ElevenLabs, Kind: usage.Speech, OK: false, Error: "elevenlabs: This request exceeds your quota of 10000. You have 7 credits remaining, while 36 credits are required for this request. (quota_exceeded)"})
	body := get()
	for _, want := range []string{"Meta Model API", "A model is missing", meta.DefaultSegmentModel, "150 of 150 left", "3,000,000 of 3,000,000 left",
		"ElevenLabs", "Out of credits", "7 of 10,000 left", "user_read"} {
		if !strings.Contains(body, want) {
			t.Fatalf("live panel missing %q:\n%s", want, body)
		}
	}
	// A line voiced since the refusal: the credits came back.
	_ = e.st.RecordCall(ctx, usage.Call{At: time.Now().Add(time.Second), Provider: usage.ElevenLabs, Kind: usage.Speech, OK: true})
	if body = get(); !strings.Contains(body, "credits were added or reset") {
		t.Fatal("voicing after a refusal means credits were added")
	}
	// A key that may read the plan shows it.
	plan = true
	body = get()
	for _, want := range []string{"creator (active)", "97,500 of 100,000 left", "2 of 30"} {
		if !strings.Contains(body, want) {
			t.Fatalf("plan missing %q", want)
		}
	}
	// Services down.
	metaAPI.Close()
	elAPI.Close()
	if body = get(); strings.Count(body, "Not answering") != 2 {
		t.Fatalf("both services down:\n%s", body)
	}
}

func TestReadNotice(t *testing.T) {
	w := []store.LineWord{{Text: "hi"}}
	page := &store.Page{ID: 1}
	for _, tc := range []struct {
		lines []*store.PageLine
		want  string
	}{
		{[]*store.PageLine{{Words: w, Audio: "a.mp3"}}, ""},
		{[]*store.PageLine{{Words: w, Error: "voice down"}}, "1 line couldn't be voiced"},
		{[]*store.PageLine{{Words: w, Error: "voice down"}, {Words: w, Error: "voice down"}}, "2 lines couldn't be voiced"},
		{[]*store.PageLine{{Words: w, Error: "voice down"}, {Words: w, Error: "x (quota_exceeded)"}}, "out of credits"},
	} {
		got := readNotice([]*store.Page{page}, map[int64][]*store.PageLine{1: tc.lines})
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Fatalf("notice %q, want %q", got, tc.want)
		}
	}
	if got := readNotice([]*store.Page{{ID: 2, ReadingError: "elevenlabs: (quota_exceeded)"}}, nil); !strings.Contains(got, "out of credits") {
		t.Fatalf("a page refused for credits: %q", got)
	}
}
