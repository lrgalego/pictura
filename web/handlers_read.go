package web

import (
	"encoding/json"
	"net/http"

	"github.com/lrgalego/pictura/internal/jobs"
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
}

type readPageData struct {
	ID     int64      `json:"id"`
	Number int        `json:"number"`
	Title  string     `json:"title"`
	Image  string     `json:"image,omitempty"`
	Status string     `json:"status"`
	Error  string     `json:"error,omitempty"`
	Lines  []readLine `json:"lines"`
}

type readData struct {
	Title     string         `json:"title"`
	Preparing bool           `json:"preparing"`
	Progress  int            `json:"progress"`
	Total     int            `json:"total"`
	Message   string         `json:"message"`
	Pages     []readPageData `json:"pages"`
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
		d := readPageData{ID: p.ID, Number: p.Number, Title: p.Title, Lines: []readLine{}}
		switch {
		case p.ImageStatus != store.ImageReady || p.Image == "":
			d.Status = readUndrawn
		case jobs.NarrationReady(st, chars, p, lines[p.ID]):
			d.Status = readReady
		case preparing:
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
				d.Lines = append(d.Lines, readLine{ID: l.ID, Kind: l.Kind, Speaker: l.Speaker, Text: l.Text, Box: l.Box, Audio: "/media/" + l.Audio, Words: l.Words, Effect: l.Sound})
			}
		}
		out.Pages = append(out.Pages, d)
	}
	return out, nil
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"started": started})
}
