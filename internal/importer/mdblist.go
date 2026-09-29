package importer

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const (
	mdblistAPI      = "https://api.mdblist.com"
	mdblistPageSize = 1000
)

// MDBList spells ids differently across endpoints.
type mdbIDs struct {
	IMDB    string `json:"imdb"`
	IMDBAlt string `json:"imdbid"`
	TMDB    int64  `json:"tmdb"`
	TMDBAlt int64  `json:"tmdbid"`
}

type mdbMedia struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   mdbIDs `json:"ids"`
}

type mdbEpisode struct {
	Season int       `json:"season"`
	Number int       `json:"number"`
	Show   *mdbMedia `json:"show"`
}

type mdbEntry struct {
	Movie         *mdbMedia   `json:"movie"`
	Show          *mdbMedia   `json:"show"`
	Episode       *mdbEpisode `json:"episode"`
	WatchedAt     *time.Time  `json:"watched_at"`
	LastWatchedAt *time.Time  `json:"last_watched_at"`
	Rating        int         `json:"rating"`
	RatedAt       *time.Time  `json:"rated_at"`
	DroppedAt     *time.Time  `json:"dropped_at"`
	Progress      float64     `json:"progress"`
	PausedAt      *time.Time  `json:"paused_at"`
	Runtime       int64       `json:"runtime"`
}

type mdbListItem struct {
	Title       string `json:"title"`
	MediaType   string `json:"mediatype"`
	ReleaseYear int    `json:"release_year"`
	IMDBID      string `json:"imdb_id"`
	IDs         mdbIDs `json:"ids"`
}

type mdbPagination struct {
	Pagination struct {
		NextCursor *string `json:"next_cursor"`
	} `json:"pagination"`
}

func (p mdbPagination) next() string {
	if p.Pagination.NextCursor == nil {
		return ""
	}
	return *p.Pagination.NextCursor
}

type mdbSyncPage struct {
	mdbPagination
	Movies   []mdbEntry `json:"movies"`
	Shows    []mdbEntry `json:"shows"`
	Episodes []mdbEntry `json:"episodes"`
}

type mdbItemsPage struct {
	mdbPagination
	Movies []mdbListItem `json:"movies"`
	Shows  []mdbListItem `json:"shows"`
}

type mdbList struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type mdblistImport struct {
	*run
	key string
}

func MDBList(ctx context.Context, st *store.Store, uid int64, apiKey string, opts Options) ([]store.ImportSection, error) {
	m := mdblistImport{newRun(ctx, st, uid, opts), apiKey}
	return m.steps(m.history, m.progress, m.ratings, m.dropped, m.watchlist, m.lists)
}

func (m mdblistImport) history() error {
	m.begin("Watched history")
	var viewings []mdbEntry
	for _, media := range []string{"movie", "episode"} {
		err := mdbFetchAll(m, "/sync/watched", url.Values{"mediatype": {media}}, func(p mdbSyncPage) {
			viewings = append(append(viewings, p.Movies...), p.Episodes...)
		})
		if err != nil {
			return err
		}
	}
	return plays(m.run, viewings, func(e mdbEntry) (store.Item, time.Time, error) {
		it, err := e.item()
		at := firstTime(e.WatchedAt, e.LastWatchedAt)
		if err == nil && at == nil {
			err = errUnsupported
		}
		return it, orNow(at), err
	})
}

func (m mdblistImport) progress() error {
	m.begin("Playback progress")
	var rows []mdbEntry
	if err := fetchJSON(m.ctx, http.MethodGet, m.url("/sync/playback", nil), nil, &rows); err != nil {
		return err
	}
	return importRows(m.run, rows, func(e mdbEntry) (write, error) {
		it, err := e.item()
		duration := minutes(e.Runtime)
		if err == nil && duration <= 0 {
			err = errUnsupported
		}
		position := int64(e.Progress / 100 * float64(duration))
		return func(b *store.Batch) error { return b.Progress(it, position, duration, orNow(e.PausedAt)) }, err
	})
}

func (m mdblistImport) ratings() error {
	m.begin("Ratings")
	var rows []mdbEntry
	err := mdbFetchAll(m, "/sync/ratings", nil, func(p mdbSyncPage) {
		rows = append(append(append(rows, p.Movies...), p.Shows...), p.Episodes...)
	})
	if err != nil {
		return err
	}
	return importRows(m.run, rows, func(e mdbEntry) (write, error) {
		it, err := e.item()
		if err == nil {
			err = store.ValidateRating(e.Rating)
		}
		return func(b *store.Batch) error { return b.Rate(it, e.Rating, orNow(e.RatedAt)) }, err
	})
}

func (m mdblistImport) dropped() error {
	var rows []mdbEntry
	if err := mdbFetchAll(m, "/sync/dropped", nil, func(p mdbSyncPage) { rows = append(rows, p.Shows...) }); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return list(m.run, "dropped", "Dropped", rows, func(e mdbEntry) (store.Item, time.Time, error) {
		it, err := e.item()
		return it, orNow(e.DroppedAt), err
	})
}

func (m mdblistImport) watchlist() error {
	return m.listItems("/watchlist/items", "watchlist", "Watchlist")
}

func (m mdblistImport) lists() error {
	var lists []mdbList
	if err := fetchJSON(m.ctx, http.MethodGet, m.url("/lists/user", nil), nil, &lists); err != nil {
		return err
	}
	for _, l := range lists {
		slug := slugify(l.Slug, fmt.Sprintf("mdblist-%d", l.ID))
		if err := m.listItems(fmt.Sprintf("/lists/%d/items", l.ID), slug, cmp.Or(l.Name, slug)); err != nil {
			return err
		}
	}
	return nil
}

func (m mdblistImport) listItems(path, slug, name string) error {
	var items []mdbListItem
	err := mdbFetchAll(m, path, nil, func(p mdbItemsPage) { items = append(append(items, p.Movies...), p.Shows...) })
	if err != nil || len(items) == 0 {
		return err
	}
	return list(m.run, slug, name, items, func(i mdbListItem) (store.Item, time.Time, error) {
		it, err := i.item()
		return it, time.Now(), err
	})
}

func (m mdblistImport) url(path string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("apikey", m.key)
	return mdblistAPI + path + "?" + q.Encode()
}

func mdbFetchAll[P interface{ next() string }](m mdblistImport, path string, q url.Values, each func(P)) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("limit", strconv.Itoa(mdblistPageSize))
	for {
		var page P
		if err := fetchJSON(m.ctx, http.MethodGet, m.url(path, q), nil, &page); err != nil {
			return err
		}
		each(page)
		cursor := page.next()
		if cursor == "" {
			return nil
		}
		q.Set("cursor", cursor)
	}
}

func (e mdbEntry) item() (store.Item, error) {
	switch {
	case e.Movie != nil:
		return e.Movie.item(store.Movie)
	case e.Episode != nil && e.Episode.Show != nil:
		show, err := e.Episode.Show.item(store.Show)
		if err != nil {
			return show, err
		}
		return episodeItem(show, e.Episode.Season, e.Episode.Number)
	case e.Show != nil:
		return e.Show.item(store.Show)
	}
	return store.Item{}, errUnsupported
}

func (m *mdbMedia) item(typ string) (store.Item, error) {
	return newItem(typ, cmp.Or(m.IDs.IMDB, m.IDs.IMDBAlt), cmp.Or(m.IDs.TMDB, m.IDs.TMDBAlt), store.Meta{Title: m.Title, Year: m.Year})
}

func (i mdbListItem) item() (store.Item, error) {
	typ := store.Movie
	if i.MediaType == "show" {
		typ = store.Show
	}
	return newItem(typ, cmp.Or(i.IDs.IMDB, i.IMDBID), i.IDs.TMDB, store.Meta{Title: i.Title, Year: i.ReleaseYear})
}
