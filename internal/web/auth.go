package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Aunali321/tally/internal/store"
)

type signInView struct {
	chrome
	Email string
	Next  string
	Error string
}

type signUpView struct {
	chrome
	Invite      string
	Valid       bool
	Email       string
	Error       string
	MinPassword int
}

func (s *server) signInPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessionUser(r); err == nil {
		redirect(w, r, "/")
		return
	}
	s.page(w, "signin", signInView{chrome: chrome{Title: "Sign in"}, Next: localPath(r.URL.Query().Get("next"))})
}

func (s *server) signInSubmit(w http.ResponseWriter, r *http.Request) {
	view := signInView{chrome: chrome{Title: "Sign in"}, Email: r.PostFormValue("email"), Next: localPath(r.PostFormValue("next"))}
	uid, err := s.store.Authenticate(r.Context(), view.Email, r.PostFormValue("password"))
	if errors.Is(err, store.ErrBadCredentials) {
		view.Error = "That email and password do not match."
		s.render(w, http.StatusUnauthorized, "signin", "layout", view)
		return
	}
	if err == nil {
		err = s.signIn(w, r, uid)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, view.Next)
}

func (s *server) signUpPage(w http.ResponseWriter, r *http.Request) {
	view := signUpView{chrome: chrome{Title: "Create your account"}, Invite: r.URL.Query().Get("invite"), MinPassword: store.MinPasswordLength}
	var err error
	if view.Invite != "" {
		view.Valid, err = s.store.InviteValid(r.Context(), view.Invite)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, "signup", view)
}

func (s *server) signUpSubmit(w http.ResponseWriter, r *http.Request) {
	view := signUpView{
		chrome:      chrome{Title: "Create your account"},
		Invite:      r.PostFormValue("invite"),
		Valid:       true,
		Email:       strings.TrimSpace(r.PostFormValue("email")),
		MinPassword: store.MinPasswordLength,
	}
	password := r.PostFormValue("password")
	var uid int64
	var err error
	if password != r.PostFormValue("repeat") {
		err = errors.New("the passwords do not match")
	} else {
		uid, err = s.store.SignUp(r.Context(), view.Invite, view.Email, password)
	}
	switch {
	case err == nil:
		if err := s.signIn(w, r, uid); err != nil {
			s.fail(w, r, err)
			return
		}
		redirect(w, r, "/")
		return
	case errors.Is(err, store.ErrInviteInvalid):
		view.Valid = false
	case errors.Is(err, store.ErrEmailTaken):
		view.Error = "There is already an account with this email. Sign in instead."
	default:
		view.Error = sentence(err.Error())
	}
	s.render(w, http.StatusUnprocessableEntity, "signup", "layout", view)
}

func (s *server) signOutSubmit(w http.ResponseWriter, r *http.Request) {
	if err := s.signOut(w, r); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/login")
}

// Only local paths, so a crafted link cannot send people off-site after signing in.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}
