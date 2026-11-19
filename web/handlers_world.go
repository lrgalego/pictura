package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/lrgalego/htmx-ds/components"
	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/pictura/internal/jobs"
	"github.com/lrgalego/pictura/internal/models"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web/views"
)

// ---------- step 4: the world ----------

func (s *server) worldView(r *http.Request, st *store.Story) (views.WorldView, error) {
	// The job is read before the data it changes, as on the cast step.
	job, err := s.st.LatestJob(r.Context(), st.ID)
	if err != nil {
		return views.WorldView{}, err
	}
	working, err := s.st.AnyRunning(r.Context(), st.ID)
	if err != nil {
		return views.WorldView{}, err
	}
	world, err := s.st.Elements(r.Context(), st.ID)
	if err != nil {
		return views.WorldView{}, err
	}
	pages, _ := s.st.Pages(r.Context(), st.ID)
	return views.WorldView{
		Story: st, Elements: world, Job: job, Working: working, PageCount: len(pages),
		Ready: len(world) > 0 && jobs.WorldReady(world),
		Pick:  s.modelPick(st, models.World),
	}, nil
}

func (s *server) worldPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if st.Step < store.StepWorld {
		http.Redirect(w, r, stepURL(st), http.StatusSeeOther)
		return
	}
	v, err := s.worldView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	render(w, r, views.Shell(s.shell(r, st.Title), views.StoryShell(st, store.StepWorld, views.WorldStep(v))))
}

func (s *server) worldPanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	s.answerWorld(w, r, st)
}

// answerWorld renders the polled panel, with optional OOB extras.
func (s *server) answerWorld(w http.ResponseWriter, r *http.Request, st *store.Story, extra ...templ.Component) {
	v, err := s.worldView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout.Fragments(w, r, append([]templ.Component{views.WorldPanel(v)}, extra...)...)
}

// worldRead finds the places and props in the script, replacing any found
// before.
func (s *server) worldRead(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.DesignWorld(st.ID); err != nil {
		s.answerWorld(w, r, st, errorToast(err))
		return
	}
	s.answerWorld(w, r, st, toast(components.ToastSuccess, "Reading the script for places and props", "The designer is on it."))
}

func (s *server) worldDraw(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.DrawWorld(st.ID); err != nil {
		s.answerWorld(w, r, st, errorToast(err))
		return
	}
	s.answerWorld(w, r, st, toast(components.ToastSuccess, "Drawing the concept art", "Every place and prop not drawn yet."))
}

func (s *server) worldAdjustDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       "Adjust the places and props",
		Description: "Add what is missing, drop what doesn't matter, or change how things look. What changes is drawn again.",
		Placeholder: "e.g. Add the treehouse from scene 2, drop the umbrella, and make the farmhouse door red with the window to its left.",
		Action:      fmt.Sprintf("/stories/%d/world/adjust", st.ID),
		Target:      "#step-panel",
		Submit:      "Revise the world",
	}))
}

func (s *server) worldAdjust(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	fb := field(r, "feedback")
	if fb == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField("", "", "Say what should change.", true))
		return
	}
	if err := s.jobs.AdjustWorld(st.ID, fb); err != nil {
		s.answerWorld(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerWorld(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Revising the places and props", "The designer is on it."))
}

// worldApprove storyboards the pages, drawing on the places and props.
func (s *server) worldApprove(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	world, err := s.st.Elements(r.Context(), st.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !jobs.WorldReady(world) {
		s.answerWorld(w, r, st, toast(components.ToastDefault, "Draw the concept art first", "Pages draw on it to keep every place and prop the same, so each one needs its art."))
		return
	}
	if err := s.jobs.Breakdown(st.ID); err != nil {
		s.answerWorld(w, r, st, errorToast(err))
		return
	}
	redirect(w, r, fmt.Sprintf("/stories/%d/pages", st.ID))
}

func (s *server) element(w http.ResponseWriter, r *http.Request, st *store.Story) (*store.Element, bool) {
	eid, _ := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	e, err := s.st.Element(r.Context(), eid)
	if err != nil || e.StoryID != st.ID {
		s.notFound(w, r)
		return nil, false
	}
	return e, true
}

func (s *server) elementLightbox(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	world, err := s.st.Elements(r.Context(), st.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var imgs []components.ViewerImage
	idx := 0
	want, _ := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	for _, e := range world {
		if e.ArtImage == "" {
			continue
		}
		if e.ID == want {
			idx = len(imgs)
		}
		imgs = append(imgs, components.ViewerImage{Src: "/media/" + e.ArtImage, Thumb: "/media/t/" + e.ArtImage, Caption: e.Name + " — concept art"})
	}
	if i, err := strconv.Atoi(r.URL.Query().Get("img")); err == nil && i >= 0 && i < len(imgs) {
		idx = i
	}
	if len(imgs) == 0 {
		layout.Fragments(w, r)
		return
	}
	layout.Fragments(w, r, components.ImageLightbox(components.ImageLightboxProps{Images: imgs, Index: idx, URL: fmt.Sprintf("/stories/%d/world/%d/view", st.ID, want)}))
}

func (s *server) elementAdjustDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	desc := "Say what is off. The description is rewritten"
	if e.ArtStatus != store.ImagePending {
		desc += " and the art drawn again"
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       "Change " + e.Name,
		Description: desc + ".",
		Placeholder: "e.g. The door is on the left, the walls are whitewashed stone, and there is a big window over the sink.",
		Action:      fmt.Sprintf("/stories/%d/world/%d/adjust", st.ID, e.ID),
		Target:      "#step-panel",
		Submit:      "Change it",
	}))
}

func (s *server) elementAdjust(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	fb := field(r, "feedback")
	if fb == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField("", "", "Say what should change.", true))
		return
	}
	if err := s.jobs.ReviseElement(st.ID, e.ID, fb); err != nil {
		s.answerWorld(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerWorld(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Changing "+e.Name, ""))
}

func (s *server) elementEditDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.ElementEditDialog(st, e))
}

func (s *server) elementEdit(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	f := jobs.ElementFields{Name: field(r, "name"), Visual: field(r, "visual"), Layout: field(r, "layout"), Redraw: r.FormValue("redraw") != ""}
	if f.Name == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.ElementNameField("", "Give it a name: pages refer to it by name.", true))
		return
	}
	if err := s.jobs.EditElement(st.ID, e.ID, f); err != nil {
		s.answerWorld(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerWorld(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Saved "+f.Name, ""))
}

func (s *server) elementRedraw(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	if err := s.jobs.RedrawElement(st.ID, e.ID); err != nil {
		s.answerWorld(w, r, st, errorToast(err))
		return
	}
	title := "Redrawing " + e.Name
	if e.ArtStatus == store.ImagePending {
		title = "Drawing " + e.Name
	}
	s.answerWorld(w, r, st, toast(components.ToastSuccess, title, ""))
}

func (s *server) elementDeleteDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.ElementDeleteDialog(st, e))
}

func (s *server) elementDelete(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	e, ok := s.element(w, r, st)
	if !ok {
		return
	}
	if s.jobs.Working(st.ID) {
		s.answerWorld(w, r, st, views.CloseModal(), toast(components.ToastDefault, "Still working", "Wait for the running work to finish before removing anything."))
		return
	}
	if err := s.st.DeleteElement(r.Context(), e.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.answerWorld(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Removed "+e.Name, ""))
}
