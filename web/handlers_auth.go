package web

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/lrgalego/htmx-ds/layout"
	"golang.org/x/crypto/bcrypt"

	"github.com/lrgalego/story-time/internal/store"
	"github.com/lrgalego/story-time/web/views"
)

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	render(w, r, views.Shell(s.shell(r, "Turn a script into a comic"), views.Home(userFrom(r.Context()) != nil)))
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if userFrom(r.Context()) != nil {
		http.Redirect(w, r, "/stories", http.StatusSeeOther)
		return
	}
	render(w, r, views.Shell(s.shell(r, "Log in"), views.Login(views.AuthForm{Next: r.URL.Query().Get("next")})))
}

func (s *server) signupPage(w http.ResponseWriter, r *http.Request) {
	if userFrom(r.Context()) != nil {
		http.Redirect(w, r, "/stories", http.StatusSeeOther)
		return
	}
	render(w, r, views.Shell(s.shell(r, "Create your account"), views.Signup(views.AuthForm{})))
}

var usernameRe = regexp.MustCompile(`^[a-z0-9_.-]{3,32}$`)

func (s *server) signup(w http.ResponseWriter, r *http.Request) {
	f := views.AuthForm{
		Username:    strings.ToLower(field(r, "username")),
		DisplayName: field(r, "display_name"),
		Password:    r.FormValue("password"),
	}
	f.Errors = map[string]string{}
	if !usernameRe.MatchString(f.Username) {
		f.Errors["username"] = "3 to 32 characters: lowercase letters, numbers, dots, dashes or underscores."
	}
	if f.DisplayName == "" {
		f.DisplayName = f.Username
	}
	if len(f.Password) < 8 {
		f.Errors["password"] = "At least 8 characters."
	}
	if len(f.Errors) == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte(f.Password), bcrypt.DefaultCost)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		u, err := s.st.CreateUser(r.Context(), f.Username, f.DisplayName, string(hash))
		if err != nil {
			f.Errors["username"] = "That username is taken. Pick another."
		} else {
			if err := s.setSession(w, r, u.ID); err != nil {
				s.fail(w, r, err)
				return
			}
			redirect(w, r, "/stories/new")
			return
		}
	}
	f.Password = ""
	if layout.IsFragment(r) {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.SignupFields(f, true))
		return
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	render(w, r, views.Shell(s.shell(r, "Create your account"), views.Signup(f)))
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	f := views.AuthForm{Username: strings.ToLower(field(r, "username")), Password: r.FormValue("password"), Next: field(r, "next")}
	f.Errors = map[string]string{}
	u, err := s.st.UserByUsername(r.Context(), f.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(f.Password)) != nil {
		f.Errors["password"] = "Wrong username or password."
	} else {
		if err := s.setSession(w, r, u.ID); err != nil {
			s.fail(w, r, err)
			return
		}
		next := f.Next
		if next == "" || !strings.HasPrefix(next, "/") {
			next = "/stories"
		}
		redirect(w, r, next)
		return
	}
	f.Password = ""
	if layout.IsFragment(r) {
		layout.FragmentsStatus(w, r, http.StatusUnprocessableEntity, views.LoginFields(f, true))
		return
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	render(w, r, views.Shell(s.shell(r, "Log in"), views.Login(f)))
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.st.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	redirect(w, r, "/")
}

// media serves a generated image, only to the story's owner.
func (s *server) media(w http.ResponseWriter, r *http.Request) {
	path, storyID, err := s.st.ImagePath(r.Context(), r.PathValue("name"))
	s.serveOwned(w, r, path, storyID, err)
}

// thumb serves the grid-sized JPEG of an image.
func (s *server) thumb(w http.ResponseWriter, r *http.Request) {
	path, storyID, err := s.st.ThumbPath(r.Context(), r.PathValue("name"))
	s.serveOwned(w, r, path, storyID, err)
}

func (s *server) serveOwned(w http.ResponseWriter, r *http.Request, path string, storyID int64, err error) {
	if err != nil {
		http.NotFound(w, r)
		return
	}
	st, err := s.st.Story(r.Context(), storyID)
	if err != nil || st.UserID != userFrom(r.Context()).ID {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

var _ = store.ErrNotFound
