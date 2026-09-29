package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

type listsView struct {
	chrome
	Lists   []store.ListSummary
	Current *store.ListSummary
	Entries []store.ListEntry
	Removed *removedItem
	Problem string
}

type removedItem struct {
	Item    store.Item
	AddedAt int64
}

func (s *server) lists(w http.ResponseWriter, r *http.Request, uid int64) {
	view, err := s.listsView(r, uid, r.PathValue("slug"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r, uid, "There is no such list.")
	case err != nil:
		s.fail(w, r, err)
	default:
		s.page(w, "lists", view)
	}
}

func (s *server) listsView(r *http.Request, uid int64, slug string) (listsView, error) {
	ctx := r.Context()
	view := listsView{}
	var err error
	if view.chrome, err = s.chrome(ctx, uid, "lists", "Lists"); err != nil {
		return view, err
	}
	if view.Lists, err = s.store.Lists(ctx, uid); err != nil {
		return view, err
	}
	i := slices.IndexFunc(view.Lists, func(l store.ListSummary) bool { return l.Slug == slug })
	switch {
	case i >= 0:
	case slug != "":
		return view, store.ErrNotFound
	case len(view.Lists) == 0:
		return view, nil
	default:
		i = 0
	}
	view.Current = &view.Lists[i]
	view.Title = view.Current.Name
	view.Entries, err = s.store.ListEntries(ctx, uid, view.Current.Slug)
	return view, err
}

func (s *server) createList(w http.ResponseWriter, r *http.Request, uid int64) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	slug := store.SlugFor(name)
	if slug == "" || len(name) > 256 {
		s.listProblem(w, r, uid, "", "Give the list a name with letters or numbers.")
		return
	}
	if err := s.store.Write(r.Context(), uid, func(b *store.Batch) error { return b.PutList(slug, name) }); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/lists/"+slug)
}

func (s *server) renameList(w http.ResponseWriter, r *http.Request, uid int64) {
	slug, name := r.PathValue("slug"), strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 256 {
		s.listProblem(w, r, uid, slug, "Give the list a name.")
		return
	}
	s.listAction(w, r, uid, slug, nil, func(b *store.Batch) error { return b.PutList(slug, name) })
}

func (s *server) deleteList(w http.ResponseWriter, r *http.Request, uid int64) {
	if err := s.store.Write(r.Context(), uid, func(b *store.Batch) error { return b.DeleteList(r.PathValue("slug")) }); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/lists")
}

func (s *server) removeFromList(w http.ResponseWriter, r *http.Request, uid int64) {
	slug := r.PathValue("slug")
	it, err := formItem(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entries, err := s.store.ListEntries(r.Context(), uid, slug)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	i := slices.IndexFunc(entries, func(e store.ListEntry) bool { return e.Item.Key() == it.Key() })
	if i < 0 {
		redirect(w, r, "/lists/"+slug)
		return
	}
	removed := &removedItem{entries[i].Item, entries[i].AddedAt.UnixMilli()}
	s.listAction(w, r, uid, slug, removed, func(b *store.Batch) error { return b.RemoveFromList(slug, it) })
}

func (s *server) restoreToList(w http.ResponseWriter, r *http.Request, uid int64) {
	slug := r.PathValue("slug")
	it, err := formItem(r)
	added, err2 := strconv.ParseInt(r.PostFormValue("added"), 10, 64)
	if errors.Join(err, err2) != nil {
		http.Error(w, "Invalid item.", http.StatusBadRequest)
		return
	}
	s.listAction(w, r, uid, slug, nil, func(b *store.Batch) error { return b.AddToList(slug, it, time.UnixMilli(added)) })
}

func (s *server) listAction(w http.ResponseWriter, r *http.Request, uid int64, slug string, removed *removedItem, apply func(*store.Batch) error) {
	if err := s.store.Write(r.Context(), uid, apply); err != nil {
		s.fail(w, r, err)
		return
	}
	if !isHTMX(r) && removed == nil {
		redirect(w, r, "/lists/"+slug)
		return
	}
	view, err := s.listsView(r, uid, slug)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view.Removed = removed
	s.page(w, "lists", view)
}

func (s *server) listProblem(w http.ResponseWriter, r *http.Request, uid int64, slug, problem string) {
	view, err := s.listsView(r, uid, slug)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view.Problem = problem
	s.render(w, http.StatusUnprocessableEntity, "lists", "layout", view)
}
