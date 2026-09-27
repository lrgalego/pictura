// Package web is the HTTP layer: routes, auth, and the handlers that turn
// store state into htmx-ds views.
package web

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	htmxds "github.com/lrgalego/htmx-ds"
	"github.com/lrgalego/htmx-ds/assets"
	"github.com/lrgalego/htmx-ds/components"
	"github.com/lrgalego/htmx-ds/layout"
	"github.com/lrgalego/htmx-ds/theme"

	"github.com/lrgalego/pictura/internal/jobs"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web/views"
)

//go:embed static
var staticFS embed.FS

// Deps is what the router needs.
type Deps struct {
	Store *store.Store
	Jobs  *jobs.Runner
	Fake  bool             // the offline provider is in use; the UI says so
	Voice pipeline.Speaker // text to speech; nil means the offline tones
}

type server struct {
	st      *store.Store
	jobs    *jobs.Runner
	fake    bool
	voice   pipeline.Speaker
	samples sampleCache
}

// Router wires the app.
func Router(d Deps) http.Handler {
	s := &server{st: d.Store, jobs: d.Jobs, fake: d.Fake, voice: d.Voice}
	if s.voice == nil {
		s.voice = pipeline.FakeVoice{}
	}
	mux := http.NewServeMux()
	// What htmxds.Mount would register, spelled out so the asset route can
	// carry our cache policy (versioned URLs immutable, bare ones no-cache).
	empty := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.Handle("GET /static/", cacheAssets(http.StripPrefix("/static/", assets.Handler())))
	mux.HandleFunc("GET /modal/close", empty)
	mux.HandleFunc("GET /panel/close", empty)
	mux.HandleFunc("GET /toasts/dismiss", empty)
	mux.HandleFunc("POST /theme/toggle", theme.ToggleHandler)
	mux.HandleFunc("POST /theme/set", theme.SetHandler)
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/app/", cacheAssets(http.StripPrefix("/static/app/", http.FileServer(http.FS(static)))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /signup", s.signupPage)
	mux.HandleFunc("POST /signup", s.signup)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /pending", s.pending)

	auth := s.requireUser
	mux.Handle("GET /stories", auth(s.library))
	mux.Handle("GET /cast", auth(s.castLibrary))
	mux.Handle("GET /stories/new", auth(s.newStoryPage))
	mux.Handle("POST /stories", auth(s.createStory))
	mux.Handle("POST /stories/draft", auth(s.draft))
	mux.Handle("POST /stories/{id}/draft", auth(s.draft))
	mux.Handle("GET /stories/{id}", auth(s.storyRedirect))
	mux.Handle("GET /stories/{id}/script", auth(s.scriptPage))
	mux.Handle("POST /stories/{id}/script", auth(s.updateScript))
	mux.Handle("GET /stories/{id}/delete", auth(s.deleteDialog))
	mux.Handle("POST /stories/{id}/delete", auth(s.deleteStory))

	mux.Handle("GET /stories/{id}/characters", auth(s.charactersPage))
	mux.Handle("GET /stories/{id}/characters/panel", auth(s.charactersPanel))
	mux.Handle("GET /stories/{id}/characters/adjust", auth(s.castAdjustDialog))
	mux.Handle("POST /stories/{id}/characters/adjust", auth(s.castAdjust))
	mux.Handle("POST /stories/{id}/characters/approve", auth(s.castApprove))
	mux.Handle("POST /stories/{id}/characters/read", auth(s.charactersRead))
	mux.Handle("GET /stories/{id}/characters/{cid}", auth(s.characterPage))
	mux.Handle("GET /stories/{id}/characters/{cid}/panel", auth(s.characterPanel))
	mux.Handle("GET /stories/{id}/characters/{cid}/adjust", auth(s.characterAdjustPanel))
	mux.Handle("POST /stories/{id}/characters/{cid}/adjust", auth(s.characterAdjust))
	mux.Handle("GET /stories/{id}/characters/{cid}/edit", auth(s.characterEditForm))
	mux.Handle("POST /stories/{id}/characters/{cid}/edit", auth(s.characterEdit))
	mux.Handle("POST /stories/{id}/characters/{cid}/redraw", auth(s.characterRedraw))
	mux.Handle("GET /stories/{id}/characters/{cid}/view", auth(s.characterLightbox))
	mux.Handle("POST /stories/{id}/characters/draw", auth(s.charactersDraw))
	mux.Handle("GET /stories/{id}/characters/{cid}/link", auth(s.characterLinkPanel))
	mux.Handle("POST /stories/{id}/characters/{cid}/link", auth(s.characterLink))
	mux.Handle("POST /stories/{id}/characters/{cid}/refs", auth(s.characterRefs))
	mux.Handle("POST /stories/{id}/characters/{cid}/sheet", auth(s.characterSheet))
	mux.Handle("POST /stories/{id}/refs/{rid}/delete", auth(s.refDelete))
	mux.Handle("GET /stories/{id}/characters/{cid}/voice", auth(s.characterVoicePanel))
	mux.Handle("POST /stories/{id}/characters/{cid}/voice", auth(s.characterVoice))
	mux.Handle("GET /stories/{id}/narrator", auth(s.narratorVoicePanel))
	mux.Handle("POST /stories/{id}/narrator", auth(s.narratorVoice))
	mux.Handle("GET /stories/{id}/voices/{voice}/preview", auth(s.voicePreview))
	mux.Handle("POST /stories/{id}/voices/cast", auth(s.castVoices))

	mux.Handle("GET /stories/{id}/pages", auth(s.pagesPage))
	mux.Handle("GET /stories/{id}/pages/panel", auth(s.pagesPanel))
	mux.Handle("GET /stories/{id}/pages/adjust", auth(s.pagesAdjustDialog))
	mux.Handle("POST /stories/{id}/pages/adjust", auth(s.pagesAdjust))
	mux.Handle("POST /stories/{id}/pages/approve", auth(s.pagesApprove))
	mux.Handle("POST /stories/{id}/pages/restart", auth(s.pagesRestart))
	mux.Handle("GET /stories/{id}/pages/{pid}/adjust", auth(s.pageAdjustDialog))
	mux.Handle("POST /stories/{id}/pages/{pid}/adjust", auth(s.pageAdjust))

	mux.Handle("GET /stories/{id}/book", auth(s.bookPage))
	mux.Handle("GET /stories/{id}/book/panel", auth(s.bookPanel))
	mux.Handle("GET /stories/{id}/book/view", auth(s.bookView))
	mux.Handle("POST /stories/{id}/book/draw", auth(s.bookDraw))
	mux.Handle("GET /stories/{id}/book/{pid}/redraw", auth(s.pageRedrawDialog))
	mux.Handle("POST /stories/{id}/book/{pid}/redraw", auth(s.pageRedraw))
	mux.Handle("GET /stories/{id}/read", auth(s.readPage))
	mux.Handle("GET /stories/{id}/read.json", auth(s.readData))
	mux.Handle("POST /stories/{id}/read/prepare", auth(s.readPrepare))
	mux.Handle("GET /stories/{id}/download.pdf", auth(s.downloadPDF))
	mux.Handle("GET /stories/{id}/download.zip", auth(s.downloadZip))

	mux.Handle("GET /media/{name}", auth(s.media))
	mux.Handle("GET /media/t/{name}", auth(s.thumb))

	return theme.Middleware(s.withUser(mux))
}

// ---------- auth plumbing ----------

type ctxKey struct{}

const sessionCookie = "st_session"

func (s *server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if u, err := s.st.UserBySession(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxKey{}).(*store.User)
	return u
}

// requireUser lets only logged-in, enabled accounts through. A signed-up
// account that nobody has enabled yet is parked on /pending.
func (s *server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r.Context())
		switch {
		case u == nil:
			if layout.IsFragment(r) {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		case !u.Enabled:
			if layout.IsFragment(r) {
				w.Header().Set("HX-Redirect", "/pending")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/pending", http.StatusSeeOther)
		default:
			next(w, r)
		}
	})
}

// landing is where a freshly authenticated user goes.
func landing(u *store.User, next string) string {
	if !u.Enabled {
		return "/pending"
	}
	if next == "" || !strings.HasPrefix(next, "/") || next == "/pending" {
		return "/stories"
	}
	return next
}

func (s *server) setSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := s.st.CreateSession(r.Context(), userID, 30*24*time.Hour)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
	return nil
}

// secureRequest reports whether the client reached us over HTTPS. In
// production TLS terminates at Cloudflare's edge and the origin hop is plain
// HTTP, so the forwarded proto header is what decides the cookie's Secure
// flag; locally (plain HTTP, no proxy) the cookie stays usable.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---------- helpers ----------

func (s *server) shell(r *http.Request, title string) views.ShellProps {
	return views.ShellProps{
		Title: title, User: userFrom(r.Context()), Fake: s.fake, Path: r.URL.Path,
		CSS:     []string{versioned(assets.CSSPath), versioned("/static/app/app.css")},
		Scripts: []string{versioned(assets.HTMXPath), versioned(assets.DSJSPath), versioned("/static/app/voice.js")},
		Favicon: versioned("/static/app/favicon.svg"),
	}
}

// story loads the {id} story and checks ownership; answers 404 otherwise.
func (s *server) story(w http.ResponseWriter, r *http.Request) (*store.Story, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	st, err := s.st.Story(r.Context(), id)
	if err != nil || st.UserID != userFrom(r.Context()).ID {
		s.notFound(w, r)
		return nil, false
	}
	return st, true
}

// notFound and fail answer a page with a page, and an htmx request with a
// message: ds.js never swaps an error into the target, it shows the
// message as an error toast instead (with Try again for the 500).
func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	if layout.IsFragment(r) {
		htmxds.ErrorWith(w, http.StatusNotFound, htmxds.Failure{Title: "Not found", Message: "It isn't there any more. It may have been deleted in another tab."})
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_ = views.Shell(s.shell(r, "Not found"), views.NotFound()).Render(r.Context(), w)
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	if layout.IsFragment(r) {
		htmxds.ErrorWith(w, http.StatusInternalServerError, htmxds.Failure{Title: "Something broke", Message: "Pictura hit an error on its side. Try again in a moment."})
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
	_ = views.Shell(s.shell(r, "Something broke"), views.ServerError(err.Error())).Render(r.Context(), w)
}

// redirect answers a 303, or an HX-Redirect for htmx requests so the
// browser navigates instead of swapping.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func toast(variant components.ToastVariant, title, desc string) templ.Component {
	return components.Toast(components.ToastProps{Variant: variant, Title: title, Description: desc, OOB: true})
}

func errorToast(err error) templ.Component {
	return toast(components.ToastDestructive, "That didn't work", err.Error())
}

func render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render %s: %v", r.URL.Path, err)
	}
}

func field(r *http.Request, name string) string {
	return strings.TrimSpace(r.FormValue(name))
}

func stepURL(st *store.Story) string {
	base := "/stories/" + strconv.FormatInt(st.ID, 10)
	switch st.Step {
	case store.StepCharacters:
		return base + "/characters"
	case store.StepPages:
		return base + "/pages"
	case store.StepBook:
		return base + "/book"
	}
	return base + "/script"
}
