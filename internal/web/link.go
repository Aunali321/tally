package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

type linkView struct {
	chrome
	Code      string
	Email     string
	SignedIn  bool
	Error     string
	Linked    bool
	LinkedAt  time.Time
	LinkedFor string
}

func (s *server) linkPage(w http.ResponseWriter, r *http.Request) {
	view := linkView{chrome: chrome{Title: "Link a TV"}, Code: r.URL.Query().Get("code")}
	if uid, err := s.sessionUser(r); err == nil {
		view.SignedIn = true
		if view.Email, err = s.store.Email(r.Context(), uid); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.page(w, "link", view)
}

func (s *server) approveDevice(w http.ResponseWriter, r *http.Request) {
	view := linkView{chrome: chrome{Title: "Link a TV"}, Code: store.NormalizeUserCode(r.PostFormValue("code")), Email: r.PostFormValue("email")}
	uid, err := s.sessionUser(r)
	if err == nil {
		view.SignedIn = true
		view.Email, err = s.store.Email(r.Context(), uid)
	} else {
		uid, err = s.store.Authenticate(r.Context(), view.Email, r.PostFormValue("password"))
	}
	if err == nil {
		err = s.store.ApproveDevice(r.Context(), uid, view.Code)
	}
	switch {
	case errors.Is(err, store.ErrBadCredentials):
		view.Error = "That email and password do not match."
	case errors.Is(err, store.ErrNotFound):
		view.Error = "That code is invalid or has expired. Check the TV for a new one."
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		view.Linked, view.LinkedAt, view.LinkedFor = true, time.Now(), view.Email
		view.Code = store.FormatUserCode(view.Code)
		s.page(w, "link", view)
		return
	}
	s.render(w, http.StatusUnprocessableEntity, "link", "layout", view)
}
