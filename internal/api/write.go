package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

type progressWrite struct {
	Item     store.Item `json:"item"`
	Position int64      `json:"position"`
	Duration int64      `json:"duration"`
}

func (p progressWrite) Validate() error {
	if p.Position < 0 || p.Duration <= 0 {
		return errors.New("position must be >= 0 and duration > 0")
	}
	return p.Item.Validate()
}

type watchedWrite struct {
	Item      store.Item `json:"item"`
	WatchedAt int64      `json:"watched_at"`
}

func (v watchedWrite) Validate() error {
	if v.WatchedAt < 0 {
		return errors.New("watched_at must be >= 0")
	}
	return v.Item.Validate()
}

type ratingWrite struct {
	Item   store.Item `json:"item"`
	Rating int        `json:"rating"`
}

func (v ratingWrite) Validate() error {
	if err := store.ValidateRating(v.Rating); err != nil {
		return err
	}
	return v.Item.Validate()
}

type listRequest struct {
	Name string `json:"name"`
}

type validator interface {
	Validate() error
}

func (s *server) putProgress(w http.ResponseWriter, r *http.Request, uid int64) error {
	now := time.Now()
	return writeBatch(s, w, r, uid, func(b *store.Batch, p progressWrite) error {
		return b.Progress(p.Item, p.Position, p.Duration, now)
	})
}

func (s *server) deleteProgress(w http.ResponseWriter, r *http.Request, uid int64) error {
	return writeBatch(s, w, r, uid, (*store.Batch).DeleteProgress)
}

func (s *server) postWatched(w http.ResponseWriter, r *http.Request, uid int64) error {
	now := time.Now()
	return writeBatch(s, w, r, uid, func(b *store.Batch, v watchedWrite) error {
		at := now
		if v.WatchedAt > 0 {
			at = time.UnixMilli(v.WatchedAt)
		}
		return b.Watch(v.Item, at)
	})
}

func (s *server) deleteWatched(w http.ResponseWriter, r *http.Request, uid int64) error {
	return writeBatch(s, w, r, uid, (*store.Batch).Unwatch)
}

func (s *server) putRatings(w http.ResponseWriter, r *http.Request, uid int64) error {
	now := time.Now()
	return writeBatch(s, w, r, uid, func(b *store.Batch, v ratingWrite) error {
		return b.Rate(v.Item, v.Rating, now)
	})
}

func (s *server) deleteRatings(w http.ResponseWriter, r *http.Request, uid int64) error {
	return writeBatch(s, w, r, uid, (*store.Batch).Unrate)
}

func (s *server) putList(w http.ResponseWriter, r *http.Request, uid int64) error {
	slug, err := pathSlug(r)
	if err != nil {
		return err
	}
	req, err := decodeJSON[listRequest](r)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 256 {
		return &statusError{http.StatusBadRequest, "name must be 1-256 chars"}
	}
	return s.write(w, r, uid, func(b *store.Batch) error {
		return b.PutList(slug, name)
	})
}

func (s *server) deleteList(w http.ResponseWriter, r *http.Request, uid int64) error {
	slug, err := pathSlug(r)
	if err != nil {
		return err
	}
	return s.write(w, r, uid, func(b *store.Batch) error {
		return b.DeleteList(slug)
	})
}

func (s *server) putListItems(w http.ResponseWriter, r *http.Request, uid int64) error {
	slug, err := pathSlug(r)
	if err != nil {
		return err
	}
	now := time.Now()
	return writeBatch(s, w, r, uid, func(b *store.Batch, it store.Item) error {
		return b.AddToList(slug, it, now)
	})
}

func (s *server) deleteListItems(w http.ResponseWriter, r *http.Request, uid int64) error {
	slug, err := pathSlug(r)
	if err != nil {
		return err
	}
	return writeBatch(s, w, r, uid, func(b *store.Batch, it store.Item) error {
		return b.RemoveFromList(slug, it)
	})
}

func pathSlug(r *http.Request) (string, error) {
	slug := r.PathValue("slug")
	if err := store.ValidateSlug(slug); err != nil {
		return "", &statusError{http.StatusBadRequest, err.Error()}
	}
	return slug, nil
}

func (s *server) write(w http.ResponseWriter, r *http.Request, uid int64, fn func(*store.Batch) error) error {
	if err := s.store.Write(r.Context(), uid, fn); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func writeBatch[T validator](s *server, w http.ResponseWriter, r *http.Request, uid int64, apply func(*store.Batch, T) error) error {
	entries, err := decodeJSON[[]T](r)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if err := e.Validate(); err != nil {
			return &statusError{http.StatusBadRequest, fmt.Sprintf("item %d: %v", i, err)}
		}
	}
	return s.write(w, r, uid, func(b *store.Batch) error {
		for _, e := range entries {
			if err := apply(b, e); err != nil {
				return err
			}
		}
		return nil
	})
}
