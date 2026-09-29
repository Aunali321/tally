package importer

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

// Simkl sends ids as strings or numbers.
type simklID string

func (id *simklID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*id = simklID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = simklID(n.String())
	return nil
}

type simklMedia struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   struct {
		IMDB simklID `json:"imdb"`
		TMDB simklID `json:"tmdb"`
	} `json:"ids"`
}

type simklEntry struct {
	Status        string      `json:"status"`
	AddedAt       *time.Time  `json:"added_to_watchlist_at"`
	LastWatchedAt *time.Time  `json:"last_watched_at"`
	UserRating    *int        `json:"user_rating"`
	UserRatedAt   *time.Time  `json:"user_rated_at"`
	AnimeType     string      `json:"anime_type"`
	Movie         *simklMedia `json:"movie"`
	Show          *simklMedia `json:"show"`
	Seasons       []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Number    int        `json:"number"`
			WatchedAt *time.Time `json:"watched_at"`
		} `json:"episodes"`
	} `json:"seasons"`
}

type simklBackup struct {
	Movies []simklEntry `json:"movies"`
	Shows  []simklEntry `json:"shows"`
	Anime  []simklEntry `json:"anime"`
}

type simklPlay struct {
	entry           simklEntry
	season, episode *int
	at              *time.Time
}

// Older Simkl apps call dropped "notinteresting".
var simklLists = []struct {
	slug, name string
	statuses   []string
}{
	{"watchlist", "Watchlist", []string{"plantowatch"}},
	{"dropped", "Dropped", []string{"dropped", "notinteresting"}},
	{"on-hold", "On hold", []string{"hold"}},
}

type simklImport struct {
	*run
	entries []simklEntry
}

func Simkl(ctx context.Context, st *store.Store, uid int64, files fs.FS, opts Options) ([]store.ImportSection, error) {
	backup, err := simklLoad(files)
	if err != nil {
		return nil, err
	}
	s := simklImport{newRun(ctx, st, uid, opts), append(append(backup.Movies, backup.Shows...), backup.Anime...)}
	return s.steps(s.history, s.ratings, s.lists)
}

func simklLoad(files fs.FS) (simklBackup, error) {
	var backup simklBackup
	names, err := fs.Glob(files, "*.json")
	if err != nil {
		return backup, err
	}
	if len(names) != 1 {
		return backup, errors.New("expected one SimklBackup.json in the export")
	}
	b, err := fs.ReadFile(files, names[0])
	if err != nil {
		return backup, err
	}
	if err := json.Unmarshal(b, &backup); err != nil {
		return backup, &fs.PathError{Op: "parse", Path: path.Base(names[0]), Err: err}
	}
	return backup, nil
}

func (s simklImport) history() error {
	s.begin("Watched history")
	var viewings []simklPlay
	for _, e := range s.entries {
		if e.Movie != nil || e.AnimeType == "movie" {
			if e.Status == "completed" {
				viewings = append(viewings, simklPlay{entry: e, at: e.LastWatchedAt})
			}
			continue
		}
		for _, season := range e.Seasons {
			for _, ep := range season.Episodes {
				viewings = append(viewings, simklPlay{e, &season.Number, &ep.Number, firstTime(ep.WatchedAt, e.LastWatchedAt)})
			}
		}
	}
	return plays(s.run, viewings, func(p simklPlay) (store.Item, time.Time, error) {
		it, err := p.entry.item()
		if err == nil && p.season != nil {
			it, err = episodeItem(it, *p.season, *p.episode)
		}
		if err == nil && p.at == nil {
			err = errUnsupported
		}
		return it, orNow(p.at), err
	})
}

func (s simklImport) ratings() error {
	s.begin("Ratings")
	var rated []simklEntry
	for _, e := range s.entries {
		if e.UserRating != nil {
			rated = append(rated, e)
		}
	}
	return importRows(s.run, rated, func(e simklEntry) (write, error) {
		it, err := e.item()
		if err == nil {
			err = store.ValidateRating(*e.UserRating)
		}
		at := firstTime(e.UserRatedAt, e.LastWatchedAt, e.AddedAt)
		return func(b *store.Batch) error { return b.Rate(it, *e.UserRating, orNow(at)) }, err
	})
}

func (s simklImport) lists() error {
	for _, l := range simklLists {
		var entries []simklEntry
		for _, e := range s.entries {
			if slices.Contains(l.statuses, e.Status) {
				entries = append(entries, e)
			}
		}
		if len(entries) == 0 {
			continue
		}
		err := list(s.run, l.slug, l.name, entries, func(e simklEntry) (store.Item, time.Time, error) {
			it, err := e.item()
			return it, orNow(e.AddedAt), err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (e simklEntry) item() (store.Item, error) {
	switch {
	case e.Movie != nil:
		return e.Movie.item(store.Movie)
	case e.Show != nil && e.AnimeType == "movie":
		return e.Show.item(store.Movie)
	case e.Show != nil:
		return e.Show.item(store.Show)
	}
	return store.Item{}, errUnsupported
}

func (m *simklMedia) item(typ string) (store.Item, error) {
	tmdb, _ := strconv.ParseInt(string(m.IDs.TMDB), 10, 64)
	return newItem(typ, string(m.IDs.IMDB), tmdb, store.Meta{Title: m.Title, Year: m.Year})
}

func firstTime(times ...*time.Time) *time.Time {
	for _, t := range times {
		if t != nil {
			return t
		}
	}
	return nil
}

func orNow(t *time.Time) time.Time {
	if t == nil {
		return time.Now()
	}
	return *t
}
