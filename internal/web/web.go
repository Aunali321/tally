package web

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const (
	sessionCookie = "tally_session"
	// Browsers cap cookie lifetimes at 400 days.
	sessionMaxAge = 400 * 24 * time.Hour
)

//go:embed static
var static embed.FS

type server struct {
	store      *store.Store
	log        *slog.Logger
	pages      pages
	background context.Context
}

type userHandlerFunc func(w http.ResponseWriter, r *http.Request, uid int64)

func New(ctx context.Context, st *store.Store, log *slog.Logger) (http.Handler, error) {
	p, err := parsePages()
	if err != nil {
		return nil, err
	}
	if err := st.InterruptImports(ctx); err != nil {
		return nil, err
	}
	s := &server{st, log, p, ctx}
	mux := http.NewServeMux()

	assets, err := fs.Sub(static, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))

	mux.HandleFunc("GET /login", s.signInPage)
	mux.HandleFunc("POST /login", s.signInSubmit)
	mux.HandleFunc("POST /signout", s.signOutSubmit)
	mux.HandleFunc("GET /signup", s.signUpPage)
	mux.HandleFunc("POST /signup", s.signUpSubmit)
	mux.HandleFunc("GET /link", s.linkPage)
	mux.HandleFunc("POST /link", s.approveDevice)

	mux.Handle("GET /{$}", s.user(s.home))
	mux.Handle("GET /history", s.user(s.history))
	mux.Handle("POST /progress/watched", s.user(s.markWatched))
	mux.Handle("POST /progress/clear", s.user(s.clearProgress))

	mux.Handle("GET /search", s.user(s.search))
	mux.Handle("GET /lists", s.user(s.lists))
	mux.Handle("POST /lists", s.user(s.createList))
	mux.Handle("GET /lists/{slug}", s.user(s.lists))
	mux.Handle("POST /lists/{slug}/rename", s.user(s.renameList))
	mux.Handle("POST /lists/{slug}/delete", s.user(s.deleteList))
	mux.Handle("POST /lists/{slug}/remove", s.user(s.removeFromList))
	mux.Handle("POST /lists/{slug}/restore", s.user(s.restoreToList))

	mux.Handle("GET /settings", s.user(s.settings))
	mux.Handle("POST /settings/nuvio", s.user(s.signInNuvio))
	mux.Handle("POST /settings/nuvio/profile", s.user(s.chooseNuvioProfile))
	mux.Handle("POST /settings/nuvio/sync", s.user(s.syncNuvio))
	mux.Handle("POST /settings/nuvio/disconnect", s.user(s.disconnectNuvio))
	mux.Handle("GET /settings/imports/{source}", s.user(s.importStatus))
	mux.Handle("POST /settings/imports/{source}", s.user(s.startImport))
	mux.Handle("POST /settings/devices/link", s.user(s.approveDeviceCode))
	mux.Handle("POST /settings/devices/signout", s.user(s.signOutEverywhere))
	mux.Handle("POST /settings/devices/{session}/signout", s.user(s.signOutDevice))
	mux.Handle("POST /settings/password", s.user(s.changePassword))
	mux.Handle("POST /settings/history/delete", s.user(s.eraseHistory))
	mux.Handle("POST /settings/account/delete", s.user(s.deleteAccount))
	mux.Handle("POST /settings/invites", s.user(s.createInvite))
	mux.Handle("POST /settings/invites/{invite}/revoke", s.user(s.revokeInvite))

	mux.Handle("GET /title/{type}/{id}", s.user(s.titlePage))
	mux.Handle("POST /title/{type}/{id}/rating", s.user(s.rateTitle))
	mux.Handle("POST /title/{type}/{id}/lists", s.user(s.addToNewList))
	mux.Handle("POST /title/{type}/{id}/lists/{slug}", s.user(s.toggleList))
	mux.Handle("POST /title/{type}/{id}/plays", s.user(s.watchTitle))
	mux.Handle("POST /title/{type}/{id}/plays/remove", s.user(s.removePlay))
	mux.Handle("POST /title/{type}/{id}/episodes", s.user(s.logEpisode))
	mux.Handle("POST /title/{type}/{id}/unwatch", s.user(s.unwatchEpisode))
	mux.Handle("POST /title/{type}/{id}/progress/clear", s.user(s.clearTitleProgress))

	return http.NewCrossOriginProtection().Handler(keepURL(mux)), nil
}

// Actions answer with the page they changed, so htmx must not push the action's URL.
func keepURL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && isHTMX(r) {
			w.Header().Set("HX-Push-Url", "false")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) signIn(w http.ResponseWriter, r *http.Request, uid int64) error {
	token, err := s.store.IssueToken(r.Context(), uid, "Web browser")
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *server) signOut(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.RevokeToken(r.Context(), c.Value); err != nil {
			return err
		}
	}
	s.clearSession(w, r)
	return nil
}

func (s *server) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
}

func (s *server) user(h userHandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := s.sessionUser(r)
		if errors.Is(err, store.ErrNotFound) {
			redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()))
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		h(w, r, uid)
	})
}

func (s *server) sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	_, id, _ := s.store.TokenUser(r.Context(), c.Value)
	return id
}

func (s *server) sessionUser(r *http.Request) (int64, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return 0, store.ErrNotFound
	}
	uid, _, err := s.store.TokenUser(r.Context(), c.Value)
	return uid, err
}

type notFoundView struct {
	chrome
	Message string
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request, uid int64, message string) {
	c, err := s.chrome(r.Context(), uid, "", "Not found")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusNotFound, "notfound", "layout", notFoundView{c, message})
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong. Try again.", http.StatusInternalServerError)
}

func redirect(w http.ResponseWriter, r *http.Request, target string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
