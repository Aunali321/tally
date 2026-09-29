package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Aunali321/tally/internal/store"
)

const maxBody = 1 << 20

type server struct {
	store *store.Store
	log   *slog.Logger
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

type userHandlerFunc func(w http.ResponseWriter, r *http.Request, uid int64) error

type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string {
	return e.msg
}

func New(st *store.Store, log *slog.Logger) http.Handler {
	s := &server{st, log}
	mux := http.NewServeMux()

	mux.Handle("POST /v1/login", s.public(s.login))
	mux.Handle("POST /v1/device", s.public(s.startDevice))
	mux.Handle("POST /v1/device/token", s.public(s.claimDevice))

	mux.Handle("DELETE /v1/token", s.user(s.revoke))
	mux.Handle("GET /v1/me", s.user(s.me))
	mux.Handle("GET /v1/sync", s.user(s.changes))
	mux.Handle("GET /v1/search", s.user(s.search))
	mux.Handle("PUT /v1/progress", s.user(s.putProgress))
	mux.Handle("DELETE /v1/progress", s.user(s.deleteProgress))
	mux.Handle("POST /v1/watched", s.user(s.postWatched))
	mux.Handle("DELETE /v1/watched", s.user(s.deleteWatched))
	mux.Handle("PUT /v1/ratings", s.user(s.putRatings))
	mux.Handle("DELETE /v1/ratings", s.user(s.deleteRatings))
	mux.Handle("PUT /v1/lists/{slug}", s.user(s.putList))
	mux.Handle("DELETE /v1/lists/{slug}", s.user(s.deleteList))
	mux.Handle("PUT /v1/lists/{slug}/items", s.user(s.putListItems))
	mux.Handle("DELETE /v1/lists/{slug}/items", s.user(s.deleteListItems))
	return mux
}

func (s *server) public(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.fail(w, r, err)
		}
	})
}

func (s *server) user(h userHandlerFunc) http.Handler {
	return s.public(func(w http.ResponseWriter, r *http.Request) error {
		token, ok := bearer(r)
		if !ok {
			return &statusError{http.StatusUnauthorized, "missing bearer token"}
		}
		uid, _, err := s.store.TokenUser(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			return &statusError{http.StatusUnauthorized, "invalid token"}
		}
		if err != nil {
			return err
		}
		return h(w, r, uid)
	})
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if se, ok := errors.AsType[*statusError](err); ok {
		writeJSON(w, se.status, errorResponse{se.msg})
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, errorResponse{"internal error"})
}

type errorResponse struct {
	Error string `json:"error"`
}

func bearer(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token, ok && token != ""
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var v T
	if err := decode(r, &v); err != nil {
		return v, &statusError{http.StatusBadRequest, err.Error()}
	}
	return v, nil
}

func decodeOptionalJSON[T any](r *http.Request) (T, error) {
	var v T
	if err := decode(r, &v); err != nil && !errors.Is(err, io.EOF) {
		return v, &statusError{http.StatusBadRequest, err.Error()}
	}
	return v, nil
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
