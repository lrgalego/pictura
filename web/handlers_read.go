package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"sync"

	"github.com/lrgalego/htmx-ds/components"
	"github.com/lrgalego/htmx-ds/layout"

	"github.com/lrgalego/pictura/internal/jobs"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web/views"
)

// ---------- read to me ----------

// Page states the reader acts on.
const (
	readReady     = "ready"     // read and voiced: playable
	readPreparing = "preparing" // the narration job is on it
	readStale     = "stale"     // needs (re)reading or voicing; nothing running
	readError     = "error"     // the last attempt failed
	readUndrawn   = "undrawn"   // no art yet
)

type readLine struct {
	ID      int64            `json:"id"`
	Kind    string           `json:"kind"`
	Speaker string           `json:"speaker"`
	Text    string           `json:"text"`
	Box     store.LineBox    `json:"box"`
	Audio   string           `json:"audio,omitempty"`
	Words   []store.LineWord `json:"words"`
	// Effect is set on a sound effect: its words light up together while
	// it plays, and nobody "says" it.
	Effect string `json:"effect,omitempty"`
	// Frame is what the camera shows while the line plays: the panel the
	// line is in, or the whole page when it is in none (a title, a
	// caption across two panels) or the page has no panels to go by.
	Frame store.LineBox `json:"frame"`
}

type readPageData struct {
	ID     int64      `json:"id"`
	Number int        `json:"number"`
	Title  string     `json:"title"`
	Image  string     `json:"image,omitempty"`
	Status string     `json:"status"`
	Error  string     `json:"error,omitempty"`
	Lines  []readLine `json:"lines"`
	Failed int        `json:"failed,omitempty"` // lines skipped because they could not be made
	// Panels are the page's panels as found in its art, in reading order
	// (only for a page that plays).
	Panels []store.LineBox `json:"panels"`
}

type readData struct {
	Title     string `json:"title"`
	Preparing bool   `json:"preparing"`
	Progress  int    `json:"progress"`
	Total     int    `json:"total"`
	Message   string `json:"message"`
	// Notice tells the listener that lines are skipped, and why.
	Notice string         `json:"notice,omitempty"`
	Pages  []readPageData `json:"pages"`
}

// readState is the reader's view of a story: every page with its lines
// and whether it can be played yet.
func (s *server) readState(r *http.Request, st *store.Story) (readData, error) {
	out := readData{Title: titleOr(st), Pages: []readPageData{}}
	// Jobs first, data after (see charactersView).
	job, err := s.st.LatestJob(r.Context(), st.ID)
	if err != nil {
		return out, err
	}
	preparing := job != nil && job.Kind == "narrate" && job.Active()
	if preparing {
		out.Preparing, out.Progress, out.Total, out.Message = true, job.Progress, job.Total, job.Message
	}
	pages, err := s.st.Pages(r.Context(), st.ID)
	if err != nil {
		return out, err
	}
	chars, err := s.st.Characters(r.Context(), st.ID)
	if err != nil {
		return out, err
	}
	lines, err := s.st.StoryLines(r.Context(), st.ID)
	if err != nil {
		return out, err
	}
	for _, p := range pages {
		d := readPageData{ID: p.ID, Number: p.Number, Title: p.Title, Lines: []readLine{}, Panels: []store.LineBox{}}
		switch {
		case p.ImageStatus != store.ImageReady || p.Image == "":
			d.Status = readUndrawn
		case jobs.NarrationReady(st, chars, p, lines[p.ID]):
			d.Status = readReady
		case preparing || s.jobs.PageBusy(p.ID):
			d.Status = readPreparing
		case p.ReadingError != "":
			d.Status, d.Error = readError, p.ReadingError
		default:
			d.Status = readStale
		}
		if d.Status != readUndrawn {
			d.Image = "/media/" + p.Image
		}
		if d.Status == readReady {
			for _, l := range lines[p.ID] {
				if len(l.Words) == 0 {
					continue
				}
				if l.Audio == "" {
					d.Failed++ // could not be made: the reader skips it
					continue
				}
				d.Lines = append(d.Lines, readLine{ID: l.ID, Kind: l.Kind, Speaker: l.Speaker, Text: l.Text, Box: l.Box, Audio: "/media/" + l.Audio, Words: l.Words, Effect: l.Sound, Frame: wholePage})
			}
		}
		out.Pages = append(out.Pages, d)
	}
	out.Notice = readNotice(pages, lines)
	s.framePanels(r.Context(), pages, out.Pages)
	return out, nil
}

// readNotice says why lines are silent: above all the voice service being
// out of credits, which no retry fixes until someone tops it up.
func readNotice(pages []*store.Page, lines map[int64][]*store.PageLine) string {
	silent, credits := 0, false
	for _, p := range pages {
		credits = credits || pipeline.OutOfCredits(p.ReadingError)
		for _, l := range lines[p.ID] {
			if len(l.Words) == 0 || l.Audio != "" || l.Error == "" {
				continue
			}
			silent++
			credits = credits || pipeline.OutOfCredits(l.Error)
		}
	}
	switch {
	case credits:
		return "The voice service is out of credits, so some lines are silent and are skipped. They'll be voiced once credits are added."
	case silent == 1:
		return "1 line couldn't be voiced and is skipped."
	case silent > 1:
		return fmt.Sprintf("%d lines couldn't be voiced and are skipped.", silent)
	}
	return ""
}

// wholePage frames a line that is in no panel.
var wholePage = store.LineBox{X1: 0, Y1: 0, X2: 1, Y2: 1}

// framePanels finds the panels of every page that plays and frames each
// line in its own, so the camera goes panel to panel. The pages are
// looked at side by side, each image only once (panelCache).
func (s *server) framePanels(ctx context.Context, pages []*store.Page, out []readPageData) {
	var wg sync.WaitGroup
	for i := range out {
		if len(out[i].Lines) == 0 {
			continue
		}
		wg.Add(1)
		go func(d *readPageData, name string) {
			defer wg.Done()
			panels := s.panels(ctx, name)
			for _, b := range panels {
				d.Panels = append(d.Panels, store.LineBox(b))
			}
			for k, l := range d.Lines {
				if at, ok := pipeline.PanelFor(panels, pipeline.Box(l.Box)); ok {
					d.Lines[k].Frame = store.LineBox(panels[at])
				}
			}
		}(&out[i], pages[i].Image)
	}
	wg.Wait()
}

// panels are where the panels sit on a page's art. Image names are random
// and an image is never rewritten (a redraw is a new name), so what was
// found for a name holds for good: it is kept, for the last few hundred.
func (s *server) panels(ctx context.Context, name string) []pipeline.Box {
	if b, ok := panelCache.get(name); ok {
		return b
	}
	data, err := s.st.ReadImage(ctx, name)
	if err != nil {
		return nil // not kept: the next poll tries again
	}
	var found []pipeline.Box
	if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		found = pipeline.FindPanels(img)
	}
	panelCache.put(name, found)
	return found
}

var panelCache = &boxCache{items: map[string][]pipeline.Box{}}

const panelCacheSize = 512

// boxCache keeps the panels found per image name, forgetting the oldest.
type boxCache struct {
	mu    sync.Mutex
	items map[string][]pipeline.Box
	order []string
}

func (c *boxCache) get(name string) ([]pipeline.Box, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[name]
	return b, ok
}

func (c *boxCache) put(name string, b []pipeline.Box) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[name]; ok {
		return
	}
	c.items[name] = b
	c.order = append(c.order, name)
	if len(c.order) > panelCacheSize {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

func (s *server) readPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	render(w, r, views.Shell(s.shell(r, "Read to me · "+titleOr(st)), views.Reader(views.ReaderView{
		Story: st, Script: versioned("/static/app/reader.js"),
	})))
}

func (s *server) readData(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	d, err := s.readState(r, st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(d)
}

// readPrepare starts the narration when some drawn page is not ready and
// nothing is preparing it already. Pages that failed are retried.
func (s *server) readPrepare(w http.ResponseWriter, r *http.Request) {
	st, ok := s.story(w, r)
	if !ok {
		return
	}
	d, err := s.readState(r, st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	need := false
	for _, p := range d.Pages {
		if p.Status == readStale || p.Status == readError {
			need = true
		}
	}
	started := false
	if need && !d.Preparing {
		if err := s.jobs.Narrate(st.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		started = true
	}
	if layout.IsFragment(r) {
		// From the Comic step's retry button.
		s.answerBook(w, r, st, toast(components.ToastSuccess, "Trying again", "What already worked is kept; only what's missing is made."))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"started": started})
}
