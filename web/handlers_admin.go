package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/pictura/internal/elevenlabs"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/internal/usage"
	"github.com/lrgalego/pictura/web/views"
)

// ---------- admin ----------

// requireAdmin lets only admins through; to anyone else the page does not
// exist.
func (s *server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).Admin {
			s.notFound(w, r)
			return
		}
		next(w, r)
	})
}

// agentInfo names an agent: the role a model plays in Pictura.
type agentInfo struct {
	provider, kind, op string
	name, role         string
}

// agents is every role Pictura casts a model in, in the order a story
// meets them. Calls are matched to a role by provider, kind and op; a
// segmentation counts for its role whatever it looked for.
var agents = []agentInfo{
	{usage.Meta, usage.Chat, "analysis", "Script reader", "Reads the script: title, world and cast"},
	{usage.Meta, usage.Chat, "characters", "Cast designer", "Revises characters from notes and references"},
	{usage.Meta, usage.Chat, "voices", "Voice caster", "Picks a voice for the narrator and each character"},
	{usage.Meta, usage.Image, "sheet", "Character artist", "Draws the character sheets"},
	{usage.Meta, usage.Chat, "pages", "Storyboard artist", "Plans the pages and panels"},
	{usage.Meta, usage.Chat, "review", "Storyboard editor", "Checks the storyboard against the script"},
	{usage.Meta, usage.Chat, "page", "Page reviser", "Revises one page of the storyboard"},
	{usage.Meta, usage.Image, "page", "Page artist", "Draws the comic pages"},
	{usage.Meta, usage.Chat, "lettering", "Letterer", "Reads the lettering off each drawn page"},
	{usage.Meta, usage.Segment, "", "Bubble finder", "Finds the balloons and captions on a page (SAM)"},
	{usage.Meta, usage.Chat, "sounds", "Sound designer", "Plans each line's delivery and the sound effects"},
	{usage.Meta, usage.Chat, "line", "Line director", "Re-plans one line from the writer's note"},
	{usage.ElevenLabs, usage.Speech, "", "Narrator", "Voices every line"},
	{usage.ElevenLabs, usage.Sound, "", "Sound effects", "Makes the sound effects"},
}

// agentKey is how a call finds its agent.
func agentKey(provider, kind, op string) string {
	if kind == usage.Segment || provider == usage.ElevenLabs {
		op = ""
	}
	return provider + "/" + kind + "/" + op
}

var adminWindows = []struct {
	key, label string
	span       time.Duration
}{
	{"24h", "Last 24 hours", 24 * time.Hour},
	{"7d", "Last 7 days", 7 * 24 * time.Hour},
	{"30d", "Last 30 days", 30 * 24 * time.Hour},
}

func (s *server) adminPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.adminView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	render(w, r, views.Shell(s.shell(r, "Admin"), views.Admin(v)))
}

func (s *server) adminView(r *http.Request) (views.AdminView, error) {
	ctx := r.Context()
	win := adminWindows[0]
	for _, w := range adminWindows {
		if w.key == r.URL.Query().Get("window") {
			win = w
		}
	}
	v := views.AdminView{Window: win.key, WindowLabel: win.label, Fake: s.fake}
	for _, w := range adminWindows {
		v.Windows = append(v.Windows, views.AdminWindow{Key: w.key, Label: w.label})
	}
	since := time.Now().Add(-win.span)
	used, err := s.st.Usage(ctx, since)
	if err != nil {
		return v, err
	}
	states, err := s.st.AgentStates(ctx)
	if err != nil {
		return v, err
	}
	v.Agents = agentRows(used, states, s.models())
	for _, a := range v.Agents {
		v.Total.Add(a.Usage)
	}
	if v.Stories, err = s.st.UsageByStory(ctx, since, 10); err != nil {
		return v, err
	}
	if v.Active, err = s.st.ActiveJobs(ctx); err != nil {
		return v, err
	}
	if v.Failed, err = s.st.FailedJobs(ctx, 10); err != nil {
		return v, err
	}
	if v.Users, err = s.st.Users(ctx); err != nil {
		return v, err
	}
	return v, nil
}

// models are the model ids configured per provider and kind.
func (s *server) models() map[string]string {
	m := map[string]string{}
	if s.meta != nil {
		m[usage.Meta+"/"+usage.Chat] = s.meta.TextModel
		m[usage.Meta+"/"+usage.Image] = s.meta.ImageModel
		m[usage.Meta+"/"+usage.Segment] = s.meta.SegmentModel
	}
	if s.eleven != nil {
		m[usage.ElevenLabs+"/"+usage.Speech] = s.eleven.Model
		m[usage.ElevenLabs+"/"+usage.Sound] = elevenlabs.SoundModel
	}
	return m
}

// agentRows joins the catalogue with what each agent did and how its
// latest call went. Calls no catalogued agent claims get a row of their own.
func agentRows(used []store.AgentUsage, states []store.AgentState, models map[string]string) []views.AdminAgent {
	rows := map[string]*views.AdminAgent{}
	var order []string
	add := func(key string, a views.AdminAgent) *views.AdminAgent {
		if r, ok := rows[key]; ok {
			return r
		}
		rows[key] = &a
		order = append(order, key)
		return rows[key]
	}
	for _, a := range agents {
		add(agentKey(a.provider, a.kind, a.op), views.AdminAgent{Name: a.name, Role: a.role, Service: serviceName(a.provider, a.kind), Model: models[a.provider+"/"+a.kind], Status: views.AgentIdle})
	}
	other := func(provider, kind, op, model string) *views.AdminAgent {
		return add(agentKey(provider, kind, op), views.AdminAgent{Name: op, Role: "Calls no agent above claims", Service: serviceName(provider, kind), Model: model, Status: views.AgentIdle})
	}
	for _, u := range used {
		a := other(u.Provider, u.Kind, u.Op, u.Model)
		a.Usage.Add(views.AdminUsage{Calls: u.Calls, Failures: u.Failures, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ReasoningTokens: u.ReasoningTokens, CachedTokens: u.CachedTokens, Chars: u.Chars, Seconds: u.Seconds, Credits: u.Credits, Busy: u.Busy, Images: imagesOf(u)})
		if a.Model == "" {
			a.Model = u.Model
		}
	}
	for _, st := range states {
		a := other(st.Provider, st.Kind, st.Op, st.Model)
		if st.LastAt.After(a.LastAt) {
			a.LastAt = st.LastAt
			a.Status = views.AgentOK
			if !st.LastOK {
				a.Status = views.AgentFailing
				if pipeline.OutOfCredits(st.LastError) {
					a.Status = views.AgentNoCredits
				}
			}
		}
		if st.LastOKAt.After(a.LastOKAt) {
			a.LastOKAt = st.LastOKAt
		}
		if st.LastErrorAt.After(a.LastErrorAt) {
			a.LastError, a.LastErrorAt = st.LastError, st.LastErrorAt
		}
	}
	out := make([]views.AdminAgent, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return out
}

func imagesOf(u store.AgentUsage) int {
	if u.Kind == usage.Image {
		return u.Calls - u.Failures
	}
	return 0
}

func serviceName(provider, kind string) string {
	switch provider + "/" + kind {
	case usage.Meta + "/" + usage.Chat:
		return "Muse Spark"
	case usage.Meta + "/" + usage.Image:
		return "Muse Image"
	case usage.Meta + "/" + usage.Segment:
		return "SAM"
	case usage.ElevenLabs + "/" + usage.Speech:
		return "ElevenLabs voice"
	case usage.ElevenLabs + "/" + usage.Sound:
		return "ElevenLabs sound"
	}
	return provider + " " + kind
}

// adminLive asks each service, right now, whether it answers, and reads
// the plan and limits it reports. It is loaded after the page so a slow
// service never holds the page up.
func (s *server) adminLive(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	v := views.AdminLive{Fake: s.fake}
	var wg sync.WaitGroup
	if s.meta != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.Meta = s.checkMeta(ctx)
		}()
	}
	if s.eleven != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.Eleven = s.checkEleven(ctx)
		}()
	}
	wg.Wait()
	layout.Fragments(w, r, views.AdminLivePanel(v))
}

func (s *server) checkMeta(ctx context.Context) *views.MetaLive {
	m := &views.MetaLive{Text: s.meta.TextModel, Image: s.meta.ImageModel, Segment: s.meta.SegmentModel}
	models, err := s.meta.Check(ctx)
	if err != nil {
		m.Error = err.Error()
	} else {
		m.OK = true
		for _, want := range []string{m.Text, m.Image, m.Segment} {
			if !slices.Contains(models, want) {
				m.Missing = append(m.Missing, want)
			}
		}
	}
	m.Limits = s.meta.Limits()
	return m
}

// quotaRe reads the plan's quota out of ElevenLabs' refusal.
var quotaRe = regexp.MustCompile(`quota of (\d+)\. You have (\d+) credits remaining`)

func (s *server) checkEleven(ctx context.Context) *views.ElevenLive {
	e := &views.ElevenLive{Model: s.eleven.Model}
	if err := s.eleven.Check(ctx); err != nil {
		e.Error = err.Error()
	} else {
		e.OK = true
	}
	sub, err := s.eleven.Subscription(ctx)
	switch {
	case err == nil:
		e.Plan = sub
	case errors.Is(err, elevenlabs.ErrNoPermission):
		e.NoPermission = true
	default:
		e.PlanError = err.Error()
	}
	// Without the plan, the last refusal is the best word on the credits.
	if msg, at, err := s.st.LastErrorLike(ctx, usage.ElevenLabs, "quota_exceeded"); err == nil && msg != "" {
		e.LastRefusal, e.LastRefusalAt = msg, at
		if m := quotaRe.FindStringSubmatch(msg); m != nil {
			e.Quota, _ = strconv.Atoi(m[1])
			e.QuotaLeft, _ = strconv.Atoi(m[2])
		}
		if ok, err := s.st.LastOKAt(ctx, usage.ElevenLabs, usage.Speech); err == nil && ok.After(at) {
			e.WorkedSince = ok
		}
	}
	return e
}
