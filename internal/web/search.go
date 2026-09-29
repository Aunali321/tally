package web

import (
	"net/http"
	"strings"

	"github.com/Aunali321/tally/internal/store"
)

type searchView struct {
	chrome
	Query     string
	Tracked   int
	Results   []store.SearchResult
	ListNames map[string]string
}

func (s *server) search(w http.ResponseWriter, r *http.Request, uid int64) {
	ctx := r.Context()
	view := searchView{Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	var err error
	if view.chrome, err = s.chrome(ctx, uid, "search", "Search"); err != nil {
		s.fail(w, r, err)
		return
	}
	if view.Query == "" {
		view.Tracked, err = s.store.TitleCount(ctx, uid)
	} else {
		view.Results, err = s.store.Search(ctx, uid, view.Query, "")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	lists, err := s.store.Lists(ctx, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view.ListNames = make(map[string]string, len(lists))
	for _, l := range lists {
		view.ListNames[l.Slug] = l.Name
	}
	s.page(w, "search", view)
}
