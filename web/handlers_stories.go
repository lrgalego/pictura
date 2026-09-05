package web

import (
	"archive/zip"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/lrgalego/htmx-ds/components"
	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/story-time/internal/jobs"
	"github.com/lrgalego/story-time/internal/pdf"
	"github.com/lrgalego/story-time/internal/pipeline"
	"github.com/lrgalego/story-time/internal/store"
	"github.com/lrgalego/story-time/web/views"
)

// ---------- library & step 1 ----------

func (s *server) library(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	stories, err := s.st.StoriesByUser(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var cards []views.StoryCard
	for _, st := range stories {
		card := views.StoryCard{Story: st, URL: stepURL(st)}
		pages, _ := s.st.Pages(r.Context(), st.ID)
		chars, _ := s.st.Characters(r.Context(), st.ID)
		card.Pages, card.Characters = len(pages), len(chars)
		for _, p := range pages {
			if p.ImageStatus == store.ImageReady {
				card.Cover = "/media/t/" + p.Image
				card.Rendered++
			}
		}
		if card.Cover == "" {
			for _, c := range chars {
				if c.SheetStatus == store.ImageReady {
					card.Cover = "/media/t/" + c.SheetImage
					break
				}
			}
		}
		if j, _ := s.st.LatestJob(r.Context(), st.ID); j.Running() {
			card.Working = true
		}
		cards = append(cards, card)
	}
	render(w, r, views.Shell(s.shell(r, "Your stories"), views.Library(cards)))
}

func (s *server) newStoryPage(w http.ResponseWriter, r *http.Request) {
	render(w, r, views.Shell(s.shell(r, "New story"), views.ScriptForm(views.ScriptFormData{Style: pipeline.Styles[0].Slug})))
}

// uploads reads the "references" files of a multipart form. Size and count
// are capped; the store rejects anything that is not an image.
func uploads(r *http.Request) ([]*multipart.FileHeader, error) {
	return uploadsField(r, "references")
}

// uploadsField reads the files posted under one multipart field name.
func uploadsField(r *http.Request, name string) ([]*multipart.FileHeader, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, r.ParseForm()
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return nil, fmt.Errorf("the upload is too large (64 MB total)")
	}
	if r.MultipartForm == nil {
		return nil, nil
	}
	var out []*multipart.FileHeader
	for _, fh := range r.MultipartForm.File[name] {
		if fh.Size == 0 {
			continue
		}
		if fh.Size > 20<<20 {
			return nil, fmt.Errorf("%s is over 20 MB", fh.Filename)
		}
		out = append(out, fh)
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("at most 8 reference images at a time")
	}
	return out, nil
}

// saveRefs stores uploaded references for a story (and a character, or 0).
func (s *server) saveRefs(r *http.Request, storyID, characterID int64, files []*multipart.FileHeader, note string) error {
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, 21<<20))
		f.Close()
		if err != nil {
			return err
		}
		if _, err := s.st.InsertRef(r.Context(), storyID, characterID, fh.Filename, note, data); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) createStory(w http.ResponseWriter, r *http.Request) {
	f := views.ScriptFormData{Title: field(r, "title"), Script: field(r, "script"), Style: field(r, "style")}
	if len(strings.Fields(f.Script)) < 30 {
		f.Error = "Paste at least a few paragraphs — around 30 words is the minimum for a story worth drawing."
		w.WriteHeader(http.StatusUnprocessableEntity)
		render(w, r, views.Shell(s.shell(r, "New story"), views.ScriptForm(f)))
		return
	}
	st, err := s.st.CreateStory(r.Context(), userFrom(r.Context()).ID, f.Title, f.Script, pipeline.StyleBySlug(f.Style).Slug)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.jobs.Analyze(st.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, fmt.Sprintf("/stories/%d/characters", st.ID))
}

func (s *server) storyRedirect(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	http.Redirect(w, r, stepURL(st), http.StatusSeeOther)
}

func (s *server) scriptPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	f := views.ScriptFormData{Story: st, Title: st.Title, Script: st.Script, Style: st.Style}
	render(w, r, views.Shell(s.shell(r, st.Title), views.StoryShell(st, store.StepScript, views.ScriptForm(f))))
}

func (s *server) updateScript(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	f := views.ScriptFormData{Story: st, Title: field(r, "title"), Script: field(r, "script"), Style: field(r, "style")}
	status := http.StatusUnprocessableEntity
	switch {
	case len(strings.Fields(f.Script)) < 30:
		f.Error = "Paste at least a few paragraphs — around 30 words is the minimum for a story worth drawing."
	case s.jobs.Busy(st.ID):
		f.Error = "The story is still being worked on. Wait for it to finish before changing the script."
		status = http.StatusConflict
	}
	if f.Error != "" {
		w.WriteHeader(status)
		render(w, r, views.Shell(s.shell(r, st.Title), views.StoryShell(st, store.StepScript, views.ScriptForm(f))))
		return
	}
	changed := f.Script != st.Script || f.Style != st.Style
	st.Title, st.Script, st.Style = f.Title, f.Script, pipeline.StyleBySlug(f.Style).Slug
	if changed {
		st.Step = store.StepScript
	}
	if err := s.st.UpdateStory(r.Context(), st); err != nil {
		s.fail(w, r, err)
		return
	}
	chars, _ := s.st.Characters(r.Context(), st.ID)
	if changed || len(chars) == 0 {
		if err := s.jobs.Analyze(st.ID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, fmt.Sprintf("/stories/%d/characters", st.ID))
}

func (s *server) deleteDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.DeleteDialog(st))
}

func (s *server) deleteStory(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteStory(r.Context(), st.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/stories")
}

// ---------- step 2: characters ----------

func (s *server) charactersView(r *http.Request, st *store.Story) (views.CharactersView, error) {
	chars, err := s.st.Characters(r.Context(), st.ID)
	if err != nil {
		return views.CharactersView{}, err
	}
	job, err := s.st.LatestJob(r.Context(), st.ID)
	if err != nil {
		return views.CharactersView{}, err
	}
	refs, err := s.st.Refs(r.Context(), st.ID)
	if err != nil {
		return views.CharactersView{}, err
	}
	pages, _ := s.st.Pages(r.Context(), st.ID)
	lib, err := s.st.Library(r.Context(), st.UserID)
	if err != nil {
		return views.CharactersView{}, err
	}
	return views.CharactersView{
		Story: st, Characters: chars, Job: job, Refs: refs, PageCount: len(pages),
		AllReady:    jobs.AllReady(chars),
		Suggestions: suggestions(lib, chars, st.ID),
		Lineage:     lineage(lib, chars, st.ID),
	}, nil
}

func (s *server) charactersPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	v, err := s.charactersView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	render(w, r, views.Shell(s.shell(r, st.Title), views.StoryShell(st, store.StepCharacters, views.CharactersStep(v))))
}

func (s *server) charactersPanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	s.answerCharacters(w, r, st, nil)
}

// answerCharacters renders the polled panel, with optional OOB extras.
func (s *server) answerCharacters(w http.ResponseWriter, r *http.Request, st *store.Story, extra ...templ.Component) {
	v, err := s.charactersView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout.Fragments(w, r, append([]templ.Component{views.CharactersPanel(v)}, extra...)...)
}

func (s *server) castAdjustDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       "Adjust the whole cast",
		Description: "Tell the editor what to change. Add or remove characters, fix ages, rename, change outfits — anything.",
		Placeholder: "e.g. Make Mara older, give the robot a red scarf, and add the innkeeper who appears in scene 3.",
		Action:      fmt.Sprintf("/stories/%d/characters/adjust", st.ID),
		Target:      "#step-panel",
		Submit:      "Revise cast",
	}))
}

func (s *server) castAdjust(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	fb := field(r, "feedback")
	if fb == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField("", "", "Say what should change.", true))
		return
	}
	if err := s.jobs.AdjustCast(st.ID, fb); err != nil {
		s.answerCharacters(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerCharacters(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Revising the cast", "The editor is on it."))
}

func (s *server) castApprove(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	chars, _ := s.st.Characters(r.Context(), st.ID)
	if !jobs.AllReady(chars) {
		s.answerCharacters(w, r, st, toast(components.ToastDefault, "Draw the sheets first", "Pages are drawn from the character sheets, so every character needs one."))
		return
	}
	if err := s.jobs.Breakdown(st.ID); err != nil {
		s.answerCharacters(w, r, st, errorToast(err))
		return
	}
	redirect(w, r, fmt.Sprintf("/stories/%d/pages", st.ID))
}

func (s *server) character(w http.ResponseWriter, r *http.Request, st *store.Story) (*store.Character, bool) {
	cid, _ := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	c, err := s.st.Character(r.Context(), cid)
	if err != nil || c.StoryID != st.ID {
		s.notFound(w, r)
		return nil, false
	}
	return c, true
}

func (s *server) characterAdjustDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       "Adjust " + c.Name,
		Description: "Describe the change. The description is revised and the sheet redrawn.",
		Placeholder: "e.g. Shorter hair, a leather jacket instead of the coat, and she should look more tired.",
		Action:      fmt.Sprintf("/stories/%d/characters/%d/adjust", st.ID, c.ID),
		Target:      "#step-panel",
		Submit:      "Revise and redraw",
		Files:       true,
		FilesHelp:   "Photos, sketches, outfits or props " + c.Name + " should match. They stay attached to the character.",
	}))
}

func (s *server) characterAdjust(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	files, upErr := uploads(r)
	fb := field(r, "feedback")
	if upErr != nil {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField(fb, "", upErr.Error(), true))
		return
	}
	if fb == "" && len(files) == 0 {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField("", "", "Say what should change, or attach reference images.", true))
		return
	}
	if s.jobs.Busy(st.ID) {
		s.answerCharacters(w, r, st, views.CloseModal(), errorToast(jobs.ErrBusy))
		return
	}
	if err := s.saveRefs(r, st.ID, c.ID, files, fb); err != nil {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField(fb, "", err.Error(), true))
		return
	}
	art := c.SheetStatus != store.ImagePending
	if err := s.jobs.Revise(st.ID, c.ID, fb, len(files) > 0, art); err != nil {
		s.answerCharacters(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	desc := "New description. Draw the sheets when the cast reads right."
	if art {
		desc = "New description, new sheet."
	}
	s.answerCharacters(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Revising "+c.Name, desc))
}

func (s *server) characterRedraw(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	if err := s.jobs.Revise(st.ID, c.ID, "", false, true); err != nil {
		s.answerCharacters(w, r, st, errorToast(err))
		return
	}
	s.answerCharacters(w, r, st, toast(components.ToastSuccess, "Redrawing "+c.Name, "Same description, fresh sheet."))
}

func (s *server) characterEditDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.CharacterEditDialog(st, c))
}

func (s *server) characterEdit(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	if s.jobs.Busy(st.ID) {
		s.answerCharacters(w, r, st, views.CloseModal(), errorToast(fmt.Errorf("the story is still being worked on")))
		return
	}
	before := *c
	c.Name, c.Role, c.Age = field(r, "name"), field(r, "role"), field(r, "age")
	c.Visual, c.Wardrobe, c.Items, c.Personality = field(r, "visual"), field(r, "wardrobe"), field(r, "items"), field(r, "personality")
	if c.Name == "" || c.Visual == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.CharacterEditFields(c, "A name and an appearance are required.", true))
		return
	}
	if err := s.st.UpdateCharacter(r.Context(), c); err != nil {
		s.fail(w, r, err)
		return
	}
	looksChanged := before.Visual != c.Visual || before.Wardrobe != c.Wardrobe || before.Items != c.Items || before.Age != c.Age
	if looksChanged && r.FormValue("redraw") != "" && before.SheetStatus != store.ImagePending {
		if err := s.jobs.Revise(st.ID, c.ID, "", false, true); err != nil {
			s.answerCharacters(w, r, st, views.CloseModal(), errorToast(err))
			return
		}
		s.answerCharacters(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Saved "+c.Name, "Redrawing the sheet with the new details."))
		return
	}
	s.answerCharacters(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Saved "+c.Name, ""))
}

func (s *server) ref(w http.ResponseWriter, r *http.Request, st *store.Story) (*store.Ref, bool) {
	rid, _ := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	ref, err := s.st.Ref(r.Context(), rid)
	if err != nil || ref.StoryID != st.ID {
		s.notFound(w, r)
		return nil, false
	}
	return ref, true
}

func (s *server) refDelete(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	ref, ok := s.ref(w, r, st)
	if !ok {
		return
	}
	if err := s.st.DeleteRef(r.Context(), ref.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if layout.IsFragment(r) {
		s.answerCharacters(w, r, st, toast(components.ToastDefault, "Reference removed", "Redraw the sheet when you want it to forget the image."))
		return
	}
	redirect(w, r, fmt.Sprintf("/stories/%d/script", st.ID))
}

// characterSheet stores an uploaded image as the character's finished sheet
// and has the editor describe the character from it.
func (s *server) characterSheet(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	files, err := uploadsField(r, "sheet")
	if err == nil && len(files) != 1 {
		err = fmt.Errorf("choose one image to use as the sheet")
	}
	if err != nil {
		s.answerCharacters(w, r, st, toast(components.ToastDestructive, "No sheet uploaded", err.Error()))
		return
	}
	if s.jobs.Busy(st.ID) {
		s.answerCharacters(w, r, st, errorToast(jobs.ErrBusy))
		return
	}
	f, err := files[0].Open()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 21<<20))
	f.Close()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	png, err := store.NormalizeImage(data, 2048)
	if err != nil {
		s.answerCharacters(w, r, st, toast(components.ToastDestructive, "No sheet uploaded", files[0].Filename+": not a readable image (PNG, JPEG, GIF or WebP)"))
		return
	}
	name, err := s.st.SaveImage(r.Context(), st.ID, "png", png)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.st.SetCharacterSheet(r.Context(), c.ID, name, store.ImageReady, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.st.Touch(r.Context(), st.ID)
	if err := s.jobs.AdoptSheet(st.ID, c.ID); err != nil {
		s.answerCharacters(w, r, st, errorToast(err))
		return
	}
	s.answerCharacters(w, r, st, toast(components.ToastSuccess, "Sheet set for "+c.Name, "Your image is the character sheet now. The editor is updating the description to match it."))
}

// ---------- cast registry ----------

// normalizeName lowercases and strips punctuation so "MARA," and "Mara" meet.
func normalizeName(n string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(n) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// sameName is true for an exact match or a shared first name of 3+ letters
// ("Mara" ~ "Mara Quinn").
func sameName(a, b string) bool {
	a, b = normalizeName(a), normalizeName(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	fa, fb := strings.Fields(a)[0], strings.Fields(b)[0]
	return len(fa) >= 3 && fa == fb
}

// registry reduces the library to one entry per lineage (the newest
// appearance), preferring entries with a finished sheet, skipping a story.
func registry(lib []store.LibraryEntry, skipStory int64) []store.LibraryEntry {
	best := map[int64]store.LibraryEntry{}
	var order []int64
	for _, e := range lib {
		if e.Story.ID == skipStory {
			continue
		}
		o := e.Character.Origin()
		cur, ok := best[o]
		if !ok {
			best[o] = e
			order = append(order, o)
			continue
		}
		if cur.Character.SheetStatus != store.ImageReady && e.Character.SheetStatus == store.ImageReady {
			best[o] = e
		}
	}
	var out []store.LibraryEntry
	for _, o := range order {
		out = append(out, best[o])
	}
	return out
}

// suggestions finds, for each character still without a sheet, registry
// entries from other stories that share its name.
func suggestions(lib []store.LibraryEntry, chars []*store.Character, storyID int64) map[int64][]store.LibraryEntry {
	reg := registry(lib, storyID)
	out := map[int64][]store.LibraryEntry{}
	for _, c := range chars {
		if c.SheetStatus != store.ImagePending || c.OriginID != 0 {
			continue
		}
		for _, e := range reg {
			if sameName(c.Name, e.Character.Name) && len(out[c.ID]) < 3 {
				out[c.ID] = append(out[c.ID], e)
			}
		}
	}
	return out
}

// lineage names the other stories each character also appears in.
func lineage(lib []store.LibraryEntry, chars []*store.Character, storyID int64) map[int64][]*store.Story {
	out := map[int64][]*store.Story{}
	for _, c := range chars {
		seen := map[int64]bool{}
		for _, e := range lib {
			if e.Story.ID == storyID || seen[e.Story.ID] || e.Character.Origin() != c.Origin() {
				continue
			}
			seen[e.Story.ID] = true
			out[c.ID] = append(out[c.ID], e.Story)
		}
	}
	return out
}

// castLibrary is the registry page: every distinct character across stories.
func (s *server) castLibrary(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	lib, err := s.st.Library(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var groups []views.CastGroup
	for _, e := range registry(lib, 0) {
		g := views.CastGroup{Character: e.Character}
		seen := map[int64]bool{}
		for _, o := range lib {
			if o.Character.Origin() == e.Character.Origin() && !seen[o.Story.ID] {
				seen[o.Story.ID] = true
				g.Stories = append(g.Stories, o.Story)
			}
		}
		groups = append(groups, g)
	}
	render(w, r, views.Shell(s.shell(r, "Your cast"), views.CastLibrary(groups)))
}

func (s *server) characterLinkDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	lib, err := s.st.Library(r.Context(), st.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout.Fragments(w, r, views.LinkDialog(st, c, registry(lib, st.ID)))
}

// characterLink makes this character the same as one from another story.
func (s *server) characterLink(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	srcID, _ := strconv.ParseInt(field(r, "source"), 10, 64)
	src, err := s.st.Character(r.Context(), srcID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	srcStory, err := s.st.Story(r.Context(), src.StoryID)
	if err != nil || srcStory.UserID != st.UserID || src.StoryID == st.ID {
		s.notFound(w, r)
		return
	}
	if s.jobs.Busy(st.ID) {
		s.answerCharacters(w, r, st, views.CloseModal(), errorToast(jobs.ErrBusy))
		return
	}
	if err := s.st.LinkCharacter(r.Context(), c, src); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.st.Touch(r.Context(), st.ID)
	desc := "Look, references and sheet copied from " + titleOr(srcStory) + "."
	if src.SheetStatus != store.ImageReady {
		desc = "Look and references copied from " + titleOr(srcStory) + "; the sheet still needs drawing."
	}
	s.answerCharacters(w, r, st, views.CloseModal(), toast(components.ToastSuccess, c.Name+" is now the same character", desc))
}

func titleOr(st *store.Story) string {
	if strings.TrimSpace(st.Title) == "" {
		return "Untitled story"
	}
	return st.Title
}

// charactersDraw starts the sheets: the end of the casting phase.
func (s *server) charactersDraw(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.DrawSheets(st.ID); err != nil {
		s.answerCharacters(w, r, st, errorToast(err))
		return
	}
	s.answerCharacters(w, r, st, toast(components.ToastSuccess, "Drawing the character sheets", "References attached to a character are used as the artist's model."))
}

// characterRefs attaches uploaded reference images to one character and
// has the editor fold them into the description (and redraw, once sheets
// exist).
func (s *server) characterRefs(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	c, ok := s.character(w, r, st)
	if !ok {
		return
	}
	files, err := uploads(r)
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("choose at least one image")
	}
	if err != nil {
		s.answerCharacters(w, r, st, toast(components.ToastDestructive, "Nothing attached", err.Error()))
		return
	}
	if s.jobs.Busy(st.ID) {
		s.answerCharacters(w, r, st, errorToast(jobs.ErrBusy))
		return
	}
	if err := s.saveRefs(r, st.ID, c.ID, files, field(r, "note")); err != nil {
		s.answerCharacters(w, r, st, toast(components.ToastDestructive, "Nothing attached", err.Error()))
		return
	}
	art := c.SheetStatus != store.ImagePending
	if err := s.jobs.Revise(st.ID, c.ID, "", true, art); err != nil {
		s.answerCharacters(w, r, st, errorToast(err))
		return
	}
	desc := "The editor is folding them into the description."
	if art {
		desc = "Description revised and sheet redrawn to match."
	}
	s.answerCharacters(w, r, st, toast(components.ToastSuccess, fmt.Sprintf("%s attached to %s", plural(len(files), "image", "images"), c.Name), desc))
}

func (s *server) characterView(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	chars, err := s.st.Characters(r.Context(), st.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var imgs []components.ViewerImage
	idx := 0
	want, _ := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	for _, c := range chars {
		if c.SheetStatus != store.ImageReady {
			continue
		}
		if c.ID == want {
			idx = len(imgs)
		}
		imgs = append(imgs, components.ViewerImage{Src: "/media/" + c.SheetImage, Thumb: "/media/t/" + c.SheetImage, Caption: c.Name + " — character sheet"})
	}
	if i, err := strconv.Atoi(r.URL.Query().Get("img")); err == nil && i >= 0 && i < len(imgs) {
		idx = i
	}
	if len(imgs) == 0 {
		layout.Fragments(w, r)
		return
	}
	layout.Fragments(w, r, components.ImageLightbox(components.ImageLightboxProps{Images: imgs, Index: idx, URL: fmt.Sprintf("/stories/%d/characters/%d/view", st.ID, want)}))
}

// ---------- step 3: pages ----------

func (s *server) pagesView(r *http.Request, st *store.Story) (views.PagesView, error) {
	pages, err := s.st.Pages(r.Context(), st.ID)
	if err != nil {
		return views.PagesView{}, err
	}
	job, err := s.st.LatestJob(r.Context(), st.ID)
	if err != nil {
		return views.PagesView{}, err
	}
	chars, _ := s.st.Characters(r.Context(), st.ID)
	return views.PagesView{Story: st, Pages: pages, Characters: chars, Job: job}, nil
}

func (s *server) pagesPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if st.Step < store.StepPages && !s.jobs.Busy(st.ID) {
		http.Redirect(w, r, stepURL(st), http.StatusSeeOther)
		return
	}
	v, err := s.pagesView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	render(w, r, views.Shell(s.shell(r, st.Title), views.StoryShell(st, store.StepPages, views.PagesStep(v))))
}

func (s *server) answerPages(w http.ResponseWriter, r *http.Request, st *store.Story, extra ...templ.Component) {
	v, err := s.pagesView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout.Fragments(w, r, append([]templ.Component{views.PagesPanel(v)}, extra...)...)
}

func (s *server) pagesPanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	s.answerPages(w, r, st)
}

func (s *server) pagesAdjustDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       "Adjust the whole breakdown",
		Description: "Pacing, page count, what to cut, what to give more room — the storyboard artist redoes the plan.",
		Placeholder: "e.g. Make it 8 pages, give the chase scene a full page, and end page 3 on the reveal.",
		Action:      fmt.Sprintf("/stories/%d/pages/adjust", st.ID),
		Target:      "#step-panel",
		Submit:      "Revise pages",
	}))
}

func (s *server) pagesAdjust(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	fb := field(r, "feedback")
	if fb == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField("", "", "Say what should change.", true))
		return
	}
	if err := s.jobs.AdjustPages(st.ID, fb); err != nil {
		s.answerPages(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerPages(w, r, st, views.CloseModal(), toast(components.ToastSuccess, "Revising the pages", "The storyboard is being redone."))
}

func (s *server) pagesRestart(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.Breakdown(st.ID); err != nil {
		s.answerPages(w, r, st, errorToast(err))
		return
	}
	s.answerPages(w, r, st, toast(components.ToastSuccess, "Storyboarding again", "A fresh breakdown from the script."))
}

func (s *server) pagesApprove(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.RenderAll(st.ID); err != nil {
		s.answerPages(w, r, st, errorToast(err))
		return
	}
	redirect(w, r, fmt.Sprintf("/stories/%d/book", st.ID))
}

func (s *server) page(w http.ResponseWriter, r *http.Request, st *store.Story) (*store.Page, bool) {
	pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	p, err := s.st.Page(r.Context(), pid)
	if err != nil || p.StoryID != st.ID {
		s.notFound(w, r)
		return nil, false
	}
	return p, true
}

func (s *server) pageAdjustDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.page(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       fmt.Sprintf("Adjust page %d", p.Number),
		Description: "Only this page changes. Panels, shots, dialogue — say what you want.",
		Placeholder: "e.g. Split panel 2 in two, make the last panel a close-up on her face, and cut the caption.",
		Action:      fmt.Sprintf("/stories/%d/pages/%d/adjust", st.ID, p.ID),
		Target:      "#step-panel",
		Submit:      "Revise page",
	}))
}

func (s *server) pageAdjust(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.page(w, r, st)
	if !ok {
		return
	}
	fb := field(r, "feedback")
	if fb == "" {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.FeedbackField("", "", "Say what should change.", true))
		return
	}
	if err := s.jobs.AdjustPage(st.ID, p.ID, fb); err != nil {
		s.answerPages(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerPages(w, r, st, views.CloseModal(), toast(components.ToastSuccess, fmt.Sprintf("Revising page %d", p.Number), ""))
}

// ---------- step 4: the book ----------

func (s *server) bookPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if st.Step < store.StepBook && !s.jobs.Busy(st.ID) {
		http.Redirect(w, r, stepURL(st), http.StatusSeeOther)
		return
	}
	v, err := s.pagesView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	render(w, r, views.Shell(s.shell(r, st.Title), views.StoryShell(st, store.StepBook, views.BookStep(v))))
}

func (s *server) answerBook(w http.ResponseWriter, r *http.Request, st *store.Story, extra ...templ.Component) {
	v, err := s.pagesView(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout.Fragments(w, r, append([]templ.Component{views.BookPanel(v)}, extra...)...)
}

func (s *server) bookPanel(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	s.answerBook(w, r, st)
}

func (s *server) bookDraw(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	if err := s.jobs.RenderAll(st.ID); err != nil {
		s.answerBook(w, r, st, errorToast(err))
		return
	}
	s.answerBook(w, r, st, toast(components.ToastSuccess, "Drawing the missing pages", ""))
}

func (s *server) bookView(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	pages, err := s.st.Pages(r.Context(), st.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var imgs []components.ViewerImage
	for _, p := range pages {
		if p.ImageStatus == store.ImageReady {
			imgs = append(imgs, components.ViewerImage{Src: "/media/" + p.Image, Thumb: "/media/t/" + p.Image, Caption: fmt.Sprintf("Page %d — %s", p.Number, p.Title)})
		}
	}
	if len(imgs) == 0 {
		layout.Fragments(w, r)
		return
	}
	idx, _ := strconv.Atoi(r.URL.Query().Get("img"))
	if idx < 0 || idx >= len(imgs) {
		idx = 0
	}
	layout.Fragments(w, r, components.ImageLightbox(components.ImageLightboxProps{Images: imgs, Index: idx, URL: fmt.Sprintf("/stories/%d/book/view", st.ID)}))
}

func (s *server) pageRedrawDialog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.page(w, r, st)
	if !ok {
		return
	}
	layout.Fragments(w, r, views.FeedbackDialog(views.FeedbackDialogProps{
		Title:       fmt.Sprintf("Redraw page %d", p.Number),
		Description: "Optional notes for the artist. Leave empty for a fresh take on the same plan.",
		Placeholder: "e.g. More dramatic lighting in panel 3, and the robot is too big.",
		Action:      fmt.Sprintf("/stories/%d/book/%d/redraw", st.ID, p.ID),
		Target:      "#step-panel",
		Submit:      "Redraw page",
		Optional:    true,
	}))
}

func (s *server) pageRedraw(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	p, ok := s.page(w, r, st)
	if !ok {
		return
	}
	if err := s.jobs.RenderPage(st.ID, p.ID, field(r, "feedback")); err != nil {
		s.answerBook(w, r, st, views.CloseModal(), errorToast(err))
		return
	}
	s.answerBook(w, r, st, views.CloseModal(), toast(components.ToastSuccess, fmt.Sprintf("Redrawing page %d", p.Number), ""))
}

func (s *server) readyPages(r *http.Request, st *store.Story) ([]*store.Page, error) {
	pages, err := s.st.Pages(r.Context(), st.ID)
	if err != nil {
		return nil, err
	}
	var out []*store.Page
	for _, p := range pages {
		if p.ImageStatus == store.ImageReady {
			out = append(out, p)
		}
	}
	return out, nil
}

func fileStem(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "story"
	}
	return s
}

func (s *server) downloadPDF(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	pages, err := s.readyPages(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(pages) == 0 {
		http.Error(w, "no pages drawn yet", http.StatusNotFound)
		return
	}
	var pp []pdf.Page
	for _, p := range pages {
		b, err := s.st.ReadImage(r.Context(), p.Image)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		pp = append(pp, pdf.Page{Image: b})
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, fileStem(st.Title)))
	if err := pdf.Write(w, st.Title, pp); err != nil {
		s.fail(w, r, err)
	}
}

func (s *server) downloadZip(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	pages, err := s.readyPages(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	chars, _ := s.st.Characters(r.Context(), st.ID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, fileStem(st.Title)))
	zw := zip.NewWriter(w)
	defer zw.Close()
	stem := fileStem(st.Title)
	for _, p := range pages {
		b, err := s.st.ReadImage(r.Context(), p.Image)
		if err != nil {
			continue
		}
		f, _ := zw.Create(fmt.Sprintf("%s/page-%02d.png", stem, p.Number))
		_, _ = f.Write(b)
	}
	for _, c := range chars {
		if c.SheetStatus != store.ImageReady {
			continue
		}
		b, err := s.st.ReadImage(r.Context(), c.SheetImage)
		if err != nil {
			continue
		}
		f, _ := zw.Create(fmt.Sprintf("%s/characters/%s.png", stem, fileStem(c.Name)))
		_, _ = f.Write(b)
	}
	if f, err := zw.Create(stem + "/script.txt"); err == nil {
		_, _ = f.Write([]byte(st.Script))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
