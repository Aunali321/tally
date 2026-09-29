package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Aunali321/tally/internal/store"
)

func (s *server) changes(w http.ResponseWriter, r *http.Request, uid int64) error {
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return &statusError{http.StatusBadRequest, "since must be a non-negative integer"}
		}
		since = n
	}
	c, err := s.store.Changes(r.Context(), uid, since)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *server) search(w http.ResponseWriter, r *http.Request, uid int64) error {
	q, typ := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("type")
	if q == "" {
		return &statusError{http.StatusBadRequest, "q is required"}
	}
	if typ != "" && typ != store.Movie && typ != store.Show {
		return &statusError{http.StatusBadRequest, "type must be movie or show"}
	}
	results, err := s.store.Search(r.Context(), uid, q, typ)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, results)
	return nil
}
