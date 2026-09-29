package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/nuvio"
	"github.com/Aunali321/tally/internal/store"
)

type settingsView struct {
	chrome
	Nuvio       *store.NuvioLink
	NuvioForm   nuvioForm
	Profiles    []nuvio.Profile
	Sources     []sourceView
	Sessions    []sessionView
	Invites     []store.Invite
	NewInvite   string
	Link        formResult
	Password    formResult
	Erase       formResult
	Remove      formResult
	MinPassword int
}

type nuvioForm struct {
	Email, Backend string
	Error          string
	BackendError   string
	ProfileError   string
}

type formResult struct {
	Error string
	Done  string
}

type sessionView struct {
	store.Session
	Current bool
}

func (s *server) settings(w http.ResponseWriter, r *http.Request, uid int64) {
	view, err := s.settingsView(r, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if view.Nuvio != nil && (view.Nuvio.Profile == 0 || r.URL.Query().Has("profiles")) {
		s.loadProfiles(r, &view)
	}
	s.page(w, "settings", view)
}

func (s *server) settingsView(r *http.Request, uid int64) (settingsView, error) {
	ctx := r.Context()
	view := settingsView{MinPassword: store.MinPasswordLength}
	var err error
	if view.chrome, err = s.chrome(ctx, uid, "settings", "Settings"); err != nil {
		return view, err
	}
	link, err := s.store.NuvioLink(ctx, uid)
	switch {
	case err == nil:
		view.Nuvio = &link
		view.NuvioForm.Email = link.Email
		if link.Backend != nuvio.DefaultBackend {
			view.NuvioForm.Backend = link.Backend
		}
	case !errors.Is(err, store.ErrNotFound):
		return view, err
	}
	if view.Sources, err = s.sources(r, uid); err != nil {
		return view, err
	}
	if view.Invites, err = s.store.PendingInvites(ctx, uid); err != nil {
		return view, err
	}
	sessions, err := s.store.Sessions(ctx, uid)
	if err != nil {
		return view, err
	}
	current := s.sessionID(r)
	for _, ss := range sessions {
		view.Sessions = append(view.Sessions, sessionView{ss, ss.ID == current})
	}
	return view, nil
}

func (s *server) loadProfiles(r *http.Request, view *settingsView) {
	profiles, err := nuvio.Profiles(r.Context(), s.store, *view.Nuvio)
	if err != nil {
		s.log.Info("nuvio profiles", "err", err)
		view.NuvioForm.ProfileError = "Could not load your Nuvio profiles. Try again in a moment."
		return
	}
	view.Profiles = profiles
}

func (s *server) settingsResult(w http.ResponseWriter, r *http.Request, uid int64, section string, set func(*settingsView) bool) {
	view, err := s.settingsView(r, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ok := set(&view)
	switch {
	case ok && !isHTMX(r) && section == "":
		redirect(w, r, "/settings")
	case ok && !isHTMX(r):
		redirect(w, r, "/settings#"+section)
	case ok:
		s.page(w, "settings", view)
	default:
		s.render(w, http.StatusUnprocessableEntity, "settings", "layout", view)
	}
}

func (s *server) signInNuvio(w http.ResponseWriter, r *http.Request, uid int64) {
	form := nuvioForm{Email: strings.TrimSpace(r.PostFormValue("email")), Backend: strings.TrimSpace(r.PostFormValue("backend"))}
	profiles, err := nuvio.SignIn(r.Context(), s.store, uid, cmp.Or(form.Backend, nuvio.DefaultBackend), form.Email, r.PostFormValue("password"))
	switch {
	case err == nil:
	case errors.Is(err, nuvio.ErrInvalidLogin):
		form.Error = "That Nuvio email and password do not match."
	case form.Backend != "":
		form.BackendError = "Could not reach a Nuvio backend at this address."
		s.log.Info("nuvio sign-in", "err", err)
	default:
		form.Error = "Could not reach Nuvio. Try again in a moment."
		s.log.Error("nuvio sign-in", "err", err)
	}
	if err == nil && len(profiles) == 1 {
		s.syncInBackground(uid)
		s.settingsResult(w, r, uid, "", func(*settingsView) bool { return true })
		return
	}
	view, verr := s.settingsView(r, uid)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	if err != nil {
		view.NuvioForm = form
		s.render(w, http.StatusUnprocessableEntity, "settings", "layout", view)
		return
	}
	view.Profiles = profiles
	s.page(w, "settings", view)
}

func (s *server) chooseNuvioProfile(w http.ResponseWriter, r *http.Request, uid int64) {
	index, err := strconv.Atoi(r.PostFormValue("profile"))
	if err == nil {
		_, err = nuvio.ChooseProfile(r.Context(), s.store, uid, index)
	}
	if err == nil {
		s.syncInBackground(uid)
	} else {
		s.log.Info("nuvio profile", "err", err)
	}
	s.settingsResult(w, r, uid, "", func(v *settingsView) bool {
		if err != nil {
			v.NuvioForm.ProfileError = "Could not switch to that profile. Pick one from the list."
			s.loadProfiles(r, v)
		}
		return err == nil
	})
}

func (s *server) syncNuvio(w http.ResponseWriter, r *http.Request, uid int64) {
	link, err := s.store.NuvioLink(r.Context(), uid)
	if err == nil && link.Profile != 0 {
		if _, err := nuvio.Sync(r.Context(), s.store, link); err != nil {
			s.log.Info("nuvio sync now", "user", uid, "err", err)
		}
	}
	s.settingsResult(w, r, uid, "", func(*settingsView) bool { return true })
}

func (s *server) disconnectNuvio(w http.ResponseWriter, r *http.Request, uid int64) {
	if err := s.store.DeleteNuvioLink(r.Context(), uid); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	s.settingsResult(w, r, uid, "", func(*settingsView) bool { return true })
}

func (s *server) syncInBackground(uid int64) {
	go func() {
		link, err := s.store.NuvioLink(s.background, uid)
		if err == nil {
			_, err = nuvio.Sync(s.background, s.store, link)
		}
		if err != nil {
			s.log.Error("nuvio first sync", "user", uid, "err", err)
		}
	}()
}

func (s *server) createInvite(w http.ResponseWriter, r *http.Request, uid int64) {
	code, err := s.store.CreateInvite(r.Context(), uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view, err := s.settingsView(r, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view.NewInvite = inviteURL(r, code)
	s.page(w, "settings", view)
}

func (s *server) revokeInvite(w http.ResponseWriter, r *http.Request, uid int64) {
	if err := s.store.RevokeInvite(r.Context(), uid, r.PathValue("invite")); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	s.settingsResult(w, r, uid, "account", func(*settingsView) bool { return true })
}

func (s *server) approveDeviceCode(w http.ResponseWriter, r *http.Request, uid int64) {
	err := s.store.ApproveDevice(r.Context(), uid, r.PostFormValue("code"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	s.settingsResult(w, r, uid, "devices", func(v *settingsView) bool {
		if err != nil {
			v.Link.Error = "That code is invalid or has expired. Check the TV for a new one."
			return false
		}
		v.Link.Done = "Linked. The TV signs in on its own within a few seconds."
		return true
	})
}

func (s *server) signOutDevice(w http.ResponseWriter, r *http.Request, uid int64) {
	id := r.PathValue("session")
	if err := s.store.RevokeSession(r.Context(), uid, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	if id == s.sessionID(r) {
		redirect(w, r, "/login")
		return
	}
	s.settingsResult(w, r, uid, "devices", func(*settingsView) bool { return true })
}

func (s *server) signOutEverywhere(w http.ResponseWriter, r *http.Request, uid int64) {
	if err := s.store.RevokeTokens(r.Context(), uid); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/login")
}

func (s *server) changePassword(w http.ResponseWriter, r *http.Request, uid int64) {
	next := r.PostFormValue("new")
	var err error
	if next != r.PostFormValue("repeat") {
		err = errors.New("the new passwords do not match")
	} else {
		err = s.store.ChangePassword(r.Context(), uid, r.PostFormValue("current"), next)
	}
	s.settingsResult(w, r, uid, "account", func(v *settingsView) bool {
		switch {
		case errors.Is(err, store.ErrBadCredentials):
			v.Password.Error = "Your current password is not right."
		case err != nil:
			v.Password.Error = sentence(err.Error())
		default:
			v.Password.Done = "Password changed."
		}
		return err == nil
	})
}

func (s *server) eraseHistory(w http.ResponseWriter, r *http.Request, uid int64) {
	err := s.store.VerifyPassword(r.Context(), uid, r.PostFormValue("password"))
	if err == nil {
		err = s.store.Write(r.Context(), uid, (*store.Batch).EraseHistory)
	}
	if err != nil && !errors.Is(err, store.ErrBadCredentials) {
		s.fail(w, r, err)
		return
	}
	s.settingsResult(w, r, uid, "delete", func(v *settingsView) bool {
		if err != nil {
			v.Erase.Error = "That password is not right."
			return false
		}
		v.Erase.Done = "Your history is deleted and Nuvio is disconnected."
		return true
	})
}

func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request, uid int64) {
	err := s.store.VerifyPassword(r.Context(), uid, r.PostFormValue("password"))
	if err == nil {
		err = s.store.DeleteUser(r.Context(), uid)
	}
	switch {
	case errors.Is(err, store.ErrBadCredentials):
		s.settingsResult(w, r, uid, "delete", func(v *settingsView) bool {
			v.Remove.Error = "That password is not right."
			return false
		})
	case err != nil:
		s.fail(w, r, err)
	default:
		s.clearSession(w, r)
		redirect(w, r, "/login")
	}
}

func inviteURL(r *http.Request, code string) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/signup?invite=" + code
}

func sentence(msg string) string {
	return strings.ToUpper(msg[:1]) + msg[1:] + "."
}

func sessionSummary(ss store.Session, now time.Time) string {
	return fmt.Sprintf("Signed in %s, last used %s", lowerDay(ss.CreatedAt, now), lowerDay(ss.LastUsedAt, now))
}

func lowerDay(t, now time.Time) string {
	switch label := dayLabel(t, now); label {
	case "Today", "Yesterday":
		return strings.ToLower(label)
	}
	if t.Year() != now.Year() {
		return t.Format("2 January 2006")
	}
	return t.Format("2 January")
}
