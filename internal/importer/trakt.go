package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

type traktIDs struct {
	Trakt int64  `json:"trakt"`
	Slug  string `json:"slug"`
	IMDB  string `json:"imdb"`
	TMDB  int64  `json:"tmdb"`
}

type traktMedia struct {
	Title   string   `json:"title"`
	Year    int      `json:"year"`
	IDs     traktIDs `json:"ids"`
	Runtime int64    `json:"runtime"`
}

type traktEpisode struct {
	Season  int   `json:"season"`
	Number  int   `json:"number"`
	Runtime int64 `json:"runtime"`
}

type traktRef struct {
	Type    string        `json:"type"`
	Movie   *traktMedia   `json:"movie"`
	Show    *traktMedia   `json:"show"`
	Episode *traktEpisode `json:"episode"`
}

type traktHistory struct {
	traktRef
	ID        int64     `json:"id"`
	WatchedAt time.Time `json:"watched_at"`
}

type traktRating struct {
	traktRef
	Rating  int       `json:"rating"`
	RatedAt time.Time `json:"rated_at"`
}

type traktPlayback struct {
	traktRef
	Progress float64   `json:"progress"`
	PausedAt time.Time `json:"paused_at"`
}

type traktListEntry struct {
	traktRef
	ListedAt time.Time `json:"listed_at"`
}

type traktWatchedMovie struct {
	Plays         int        `json:"plays"`
	LastWatchedAt time.Time  `json:"last_watched_at"`
	Movie         traktMedia `json:"movie"`
}

type traktWatchedShow struct {
	Show    traktMedia `json:"show"`
	Seasons []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Number        int       `json:"number"`
			Plays         int       `json:"plays"`
			LastWatchedAt time.Time `json:"last_watched_at"`
		} `json:"episodes"`
	} `json:"seasons"`
}

type traktList struct {
	Name string   `json:"name"`
	IDs  traktIDs `json:"ids"`
}

type traktExport struct {
	*run
	files fs.FS
}

func Trakt(ctx context.Context, st *store.Store, uid int64, files fs.FS, opts Options) ([]store.ImportSection, error) {
	t := traktExport{newRun(ctx, st, uid, opts), files}
	return t.steps(t.history, t.progress, t.ratings, t.lists)
}

func (t traktExport) history() error {
	t.begin("Watched history")
	history, err := traktLoad[traktHistory](t.files, "watched-history")
	if err != nil {
		return err
	}
	slices.SortFunc(history, func(a, b traktHistory) int {
		return cmp.Or(a.WatchedAt.Compare(b.WatchedAt), cmp.Compare(a.ID, b.ID))
	})
	history = slices.CompactFunc(history, func(a, b traktHistory) bool { return a.ID == b.ID })
	if len(history) == 0 {
		if history, err = t.aggregatePlays(); err != nil {
			return err
		}
	}
	return plays(t.run, history, func(h traktHistory) (store.Item, time.Time, error) {
		it, err := h.item()
		return it, h.WatchedAt, err
	})
}

// Some exports have no watched-history, only per-title play counts.
func (t traktExport) aggregatePlays() ([]traktHistory, error) {
	movies, err := traktLoad[traktWatchedMovie](t.files, "watched-movies")
	if err != nil {
		return nil, err
	}
	shows, err := traktLoad[traktWatchedShow](t.files, "watched-shows")
	if err != nil {
		return nil, err
	}
	var history []traktHistory
	add := func(ref traktRef, n int, at time.Time) {
		history = append(history, slices.Repeat([]traktHistory{{traktRef: ref, WatchedAt: at}}, max(n, 1))...)
	}
	for _, m := range movies {
		add(traktRef{Type: "movie", Movie: &m.Movie}, m.Plays, m.LastWatchedAt)
	}
	for _, s := range shows {
		for _, season := range s.Seasons {
			for _, ep := range season.Episodes {
				add(traktRef{Type: "episode", Show: &s.Show, Episode: &traktEpisode{Season: season.Number, Number: ep.Number}}, ep.Plays, ep.LastWatchedAt)
			}
		}
	}
	slices.SortStableFunc(history, func(a, b traktHistory) int { return a.WatchedAt.Compare(b.WatchedAt) })
	return history, nil
}

func (t traktExport) progress() error {
	t.begin("Playback progress")
	rows, err := traktLoad[traktPlayback](t.files, "watched-playback")
	if err != nil {
		return err
	}
	return importRows(t.run, rows, func(p traktPlayback) (write, error) {
		it, err := p.item()
		if err != nil {
			return nil, err
		}
		duration := p.runtime()
		if duration <= 0 {
			return nil, errUnsupported
		}
		position := int64(p.Progress / 100 * float64(duration))
		return func(b *store.Batch) error { return b.Progress(it, position, duration, p.PausedAt) }, nil
	})
}

func (t traktExport) ratings() error {
	t.begin("Ratings")
	var rows []traktRating
	for _, kind := range []string{"movies", "shows", "seasons", "episodes"} {
		page, err := traktLoad[traktRating](t.files, "ratings-"+kind)
		if err != nil {
			return err
		}
		rows = append(rows, page...)
	}
	return importRows(t.run, rows, func(r traktRating) (write, error) {
		it, err := r.item()
		if err == nil {
			err = store.ValidateRating(r.Rating)
		}
		return func(b *store.Batch) error { return b.Rate(it, r.Rating, r.RatedAt) }, err
	})
}

func (t traktExport) lists() error {
	for _, l := range []struct{ slug, name string }{{"watchlist", "Watchlist"}, {"favorites", "Favorites"}} {
		rows, err := traktLoad[traktListEntry](t.files, "lists-"+l.slug)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := list(t.run, l.slug, l.name, rows, traktListEntry.entry); err != nil {
				return err
			}
		}
	}
	custom, err := traktLoad[traktList](t.files, "lists-lists")
	if err != nil {
		return err
	}
	for _, l := range custom {
		rows, err := traktLoad[traktListEntry](t.files, fmt.Sprintf("lists-list-%d-%s", l.IDs.Trakt, l.IDs.Slug))
		if err != nil {
			return err
		}
		slug := slugify(l.IDs.Slug, fmt.Sprintf("trakt-%d", l.IDs.Trakt))
		if err := list(t.run, slug, cmp.Or(strings.TrimSpace(l.Name), slug), rows, traktListEntry.entry); err != nil {
			return err
		}
	}
	return nil
}

func (e traktListEntry) entry() (store.Item, time.Time, error) {
	it, err := e.item()
	return it, e.ListedAt, err
}

func (r traktRef) item() (store.Item, error) {
	switch {
	case r.Type == "movie" && r.Movie != nil:
		return r.Movie.item(store.Movie)
	case r.Type == "show" && r.Show != nil:
		return r.Show.item(store.Show)
	case r.Type == "episode" && r.Show != nil && r.Episode != nil:
		show, err := r.Show.item(store.Show)
		if err != nil {
			return show, err
		}
		return episodeItem(show, r.Episode.Season, r.Episode.Number)
	}
	return store.Item{}, errUnsupported
}

func (r traktRef) runtime() int64 {
	switch {
	case r.Type == "movie" && r.Movie != nil:
		return minutes(r.Movie.Runtime)
	case r.Type == "episode" && r.Episode != nil:
		return minutes(r.Episode.Runtime)
	}
	return 0
}

func (m *traktMedia) item(typ string) (store.Item, error) {
	return newItem(typ, m.IDs.IMDB, m.IDs.TMDB, store.Meta{Title: m.Title, Year: m.Year})
}

func traktLoad[T any](files fs.FS, stem string) ([]T, error) {
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(stem) + `(?:-(\d+))?\.json$`)
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	type page struct {
		number int
		name   string
	}
	var pages []page
	for _, e := range entries {
		if m := pattern.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			pages = append(pages, page{n, e.Name()})
		}
	}
	slices.SortFunc(pages, func(a, b page) int { return cmp.Compare(a.number, b.number) })
	var rows []T
	for _, p := range pages {
		b, err := fs.ReadFile(files, p.name)
		if err != nil {
			return nil, err
		}
		var page []T
		if err := json.Unmarshal(b, &page); err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		rows = append(rows, page...)
	}
	return rows, nil
}
