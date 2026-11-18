package web

import (
	"fmt"
	"net/http"

	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/pictura/internal/models"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web/views"
)

// ---------- models per role ----------

// modelPick is the picker for one of a story's roles (st nil: a story not
// made yet, whose pick rides along with the form that makes it).
func (s *server) modelPick(st *store.Story, role string) views.ModelPick {
	rl, ok := models.RoleByKey(role)
	if !ok || s.router == nil {
		return views.ModelPick{}
	}
	p := views.ModelPick{Role: role, Verb: rl.Verb, Value: s.router.Pick(st, role).ID}
	if st != nil {
		p.Action = fmt.Sprintf("/stories/%d/models", st.ID)
	}
	for _, m := range s.router.Options(role) {
		p.Options = append(p.Options, views.ModelOption{ID: m.ID, Name: m.Name, Note: m.Note})
	}
	return p
}

// validModel reports whether a model is on offer for a role here.
func (s *server) validModel(role, model string) bool {
	if s.router == nil {
		return false
	}
	for _, m := range s.router.Options(role) {
		if m.ID == model {
			return true
		}
	}
	return false
}

// setModel saves a story's pick for a role. It applies to the next thing
// the role makes: nothing already made is redone.
func (s *server) setModel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	role, model := r.FormValue("role"), r.FormValue("model")
	if !s.validModel(role, model) {
		http.Error(w, "That model is not on offer for this.", http.StatusUnprocessableEntity)
		return
	}
	if err := s.st.SetStoryModel(r.Context(), st.ID, role, model); err != nil {
		s.fail(w, r, err)
		return
	}
	if st.Models == nil {
		st.Models = map[string]string{}
	}
	st.Models[role] = model
	p := s.modelPick(st, role)
	p.Saved = true
	layout.Fragments(w, r, views.ModelPicker(p))
}

// pickFromForm saves the pick a form carried as model_<role>, if any.
func (s *server) pickFromForm(r *http.Request, st *store.Story, role string) error {
	model := r.FormValue("model_" + role)
	if model == "" || !s.validModel(role, model) || model == st.Models[role] {
		return nil
	}
	return s.st.SetStoryModel(r.Context(), st.ID, role, model)
}

// speaker is the voice a story picked, for previews.
func (s *server) speaker(st *store.Story) pipeline.Speaker {
	if s.router != nil {
		if sp := s.router.Speaker(st); sp != nil {
			return sp
		}
	}
	return s.voice
}

// scriptPick is the script reader's picker on the script form: a field of
// that form, saved with it, as a new reader only matters when the script
// is read again.
func (s *server) scriptPick(st *store.Story) views.ModelPick {
	p := s.modelPick(st, models.Script)
	p.Action = ""
	return p
}
