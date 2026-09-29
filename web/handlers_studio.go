package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/lrgalego/htmx-ds/components"
	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/pictura/internal/jobs"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web/views"
)

// ---------- the sound studio (advanced read-aloud) ----------

// studioCookie remembers, per browser, whether the Comic step shows the
// sound studio links. Off is the simple experience.
const studioCookie = "pictura_studio"

type studioKey struct{}

func studioOn(r *http.Request) bool {
	if on, ok := r.Context().Value(studioKey{}).(bool); ok {
		return on
	}
	c, err := r.Cookie(studioCookie)
	return err == nil && c.Value == "1"
}

// toggleStudio sets the preference to what the switch now says and
// re-renders the Comic step with it.
func (s *server) toggleStudio(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("studio") == "on"
	v := "0"
	if on {
		v = "1"
	}
	http.SetCookie(w, &http.Cookie{Name: studioCookie, Value: v, Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode})
	// This request still carries the old cookie; the new state wins.
	r = r.WithContext(context.WithValue(r.Context(), studioKey{}, on))
	id, _ := strconv.ParseInt(r.FormValue("story"), 10, 64)
	st, err := s.st.Story(r.Context(), id)
	if err != nil || st.UserID != userFrom(r.Context()).ID {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.answerBook(w, r, st)
}

// pageSound summarizes a page's read-aloud state for its card.
func (s *server) pageSound(st *store.Story, chars []*store.Character, p *store.Page, lines []*store.PageLine) views.PageSound {
	ps := views.PageSound{Busy: s.jobs.PageBusy(p.ID), Error: p.ReadingError, Read: p.ReadingImage == p.Image && p.Image != ""}
	for _, l := range lines {
		if len(l.Words) == 0 {
			continue
		}
		ps.Lines++
		if l.Error != "" {
			ps.Failed++
		}
	}
	ps.Ready = jobs.NarrationReady(st, chars, p, lines)
	return ps
}

// retryPage gets one page ready to be read aloud again. The reader calls it
// with fetch (JSON back); the Comic step and the studio with htmx.
func (s *server) retryPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.storyPage(w, r, st)
	if !ok {
		return
	}
	err := s.jobs.NarratePage(st.ID, p.ID)
	if !layout.IsFragment(r) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"started": err == nil})
		return
	}
	extra := toast(components.ToastSuccess, "Trying page "+strconv.Itoa(p.Number)+" again", "What already worked is kept; only the missing sounds are made.")
	if err != nil {
		extra = errorToast(err)
	}
	if fromStudio(r) {
		s.answerStudio(w, r, st, p, extra)
		return
	}
	s.answerBook(w, r, st, extra)
}

func (s *server) storyPage(w http.ResponseWriter, r *http.Request, st *store.Story) (*store.Page, bool) {
	pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	p, err := s.st.Page(r.Context(), pid)
	if err != nil || p.StoryID != st.ID {
		s.notFound(w, r)
		return nil, false
	}
	return p, true
}

func fromStudio(r *http.Request) bool {
	t := r.Header.Get("HX-Target")
	return t == "studio-panel" || t == "#studio-panel"
}

// storyLine loads the {lid} line and checks it belongs to the story.
func (s *server) storyLine(w http.ResponseWriter, r *http.Request, st *store.Story) (*store.PageLine, *store.Page, bool) {
	lid, _ := strconv.ParseInt(r.PathValue("lid"), 10, 64)
	l, err := s.st.PageLine(r.Context(), lid)
	if err != nil {
		s.notFound(w, r)
		return nil, nil, false
	}
	p, err := s.st.Page(r.Context(), l.PageID)
	if err != nil || p.StoryID != st.ID {
		s.notFound(w, r)
		return nil, nil, false
	}
	return l, p, true
}

func (s *server) studioView(r *http.Request, st *store.Story, p *store.Page) (views.StudioView, error) {
	v := views.StudioView{Story: st, Page: p, Script: versioned("/static/app/studio.js")}
	job, err := s.st.LatestJob(r.Context(), st.ID)
	if err != nil {
		return v, err
	}
	v.Job = job
	pages, err := s.st.Pages(r.Context(), st.ID)
	if err != nil {
		return v, err
	}
	v.Pages = pages
	if fresh, err := s.st.Page(r.Context(), p.ID); err == nil {
		v.Page = fresh
	}
	chars, err := s.st.Characters(r.Context(), st.ID)
	if err != nil {
		return v, err
	}
	v.Characters = chars
	lines, err := s.st.PageLines(r.Context(), p.ID)
	if err != nil {
		return v, err
	}
	v.Lines = lines
	v.Busy = s.jobs.PageBusy(p.ID) || (job != nil && job.Kind == "narrate" && job.Active())
	v.Sound = s.pageSound(st, chars, v.Page, lines)
	return v, nil
}

func (s *server) studioPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.storyPage(w, r, st)
	if !ok {
		return
	}
	v, err := s.studioView(r, st, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	render(w, r, views.Shell(s.shell(r, "Sound studio · page "+strconv.Itoa(p.Number)+" · "+titleOr(st)), views.StoryShell(st, store.StepBook, views.Studio(v))))
}

func (s *server) studioPanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.storyPage(w, r, st)
	if !ok {
		return
	}
	s.answerStudio(w, r, st, p)
}

func (s *server) answerStudio(w http.ResponseWriter, r *http.Request, st *store.Story, p *store.Page, extra ...templ.Component) {
	v, err := s.studioView(r, st, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout.Fragments(w, r, append([]templ.Component{views.StudioPanel(v)}, extra...)...)
}

func (s *server) retryLine(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	l, p, ok := s.storyLine(w, r, st)
	if !ok {
		return
	}
	if err := s.jobs.RetryLine(st.ID, l.ID); err != nil {
		s.answerStudio(w, r, st, p, errorToast(err))
		return
	}
	s.answerStudio(w, r, st, p, toast(components.ToastSuccess, "Making the line again", "“"+l.Text+"”"))
}

func (s *server) adjustLine(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	l, p, ok := s.storyLine(w, r, st)
	if !ok {
		return
	}
	dir := field(r, "direction")
	if dir == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, toast(components.ToastDestructive, "Say what to change", "Write a direction for the line, or pick one of the suggestions."))
		return
	}
	if len(dir) > 500 {
		dir = dir[:500]
	}
	if err := s.jobs.AdjustLine(st.ID, l.ID, dir); err != nil {
		s.answerStudio(w, r, st, p, errorToast(err))
		return
	}
	s.answerStudio(w, r, st, p, toast(components.ToastSuccess, "Directing the line", "“"+dir+"”"))
}
