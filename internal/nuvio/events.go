package nuvio

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const (
	progressFeed = "sync_pull_watch_progress_delta"
	watchedFeed  = "sync_pull_watched_items_delta"
	libraryFeed  = "sync_pull_library_delta"

	pageSize    = 1000
	opDelete    = "delete"
	libraryList = "watchlist"
)

type event struct {
	ID          int64  `json:"event_id"`
	Operation   string `json:"operation"`
	ContentID   string `json:"content_id"`
	ContentType string `json:"content_type"`
	Season      *int   `json:"season"`
	Episode     *int   `json:"episode"`
}

type progressEvent struct {
	event
	Position    int64 `json:"position"`
	Duration    int64 `json:"duration"`
	LastWatched int64 `json:"last_watched"`
}

type watchedEvent struct {
	event
	Title     string `json:"title"`
	WatchedAt int64  `json:"watched_at"`
}

type libraryEvent struct {
	event
	Name        string `json:"name"`
	Poster      string `json:"poster"`
	ReleaseInfo string `json:"release_info"`
	AddedAt     int64  `json:"added_at"`
}

// apply reports false for events tally cannot store, such as debrid cloud files without a movie or show id.
type feedEvent interface {
	eventID() int64
	apply(*store.Batch) (bool, error)
}

type deltaParams struct {
	Profile int   `json:"p_profile_id"`
	After   int64 `json:"p_since_event_id"`
	Limit   int   `json:"p_limit"`
}

func pull[E feedEvent](ctx context.Context, c *Client, token, feed string, profile int, after int64) ([]E, error) {
	var events []E
	for {
		var page []E
		if err := c.rpc(ctx, token, feed, deltaParams{profile, after, pageSize}, &page); err != nil {
			return nil, err
		}
		events = append(events, page...)
		if len(page) < pageSize {
			return events, nil
		}
		after = page[len(page)-1].eventID()
	}
}

func cursor[E feedEvent](events []E, current int64) int64 {
	if len(events) == 0 {
		return current
	}
	return events[len(events)-1].eventID()
}

func (e event) eventID() int64 {
	return e.ID
}

func (e event) item(meta *store.Meta) (store.Item, bool) {
	it := store.Item{ID: e.ContentID, Season: e.Season, Episode: e.Episode, Meta: meta}
	switch strings.ToLower(e.ContentType) {
	case "movie":
		it.Type = store.Movie
	case "series", "show", "tv":
		it.Type = store.Show
	}
	return it, it.Validate() == nil
}

func (e progressEvent) apply(b *store.Batch) (bool, error) {
	it, ok := e.item(nil)
	switch {
	case !ok:
		return false, nil
	case e.Operation == opDelete:
		return true, b.DeleteProgress(it)
	case e.Position < 0 || e.Duration <= 0:
		return false, nil
	}
	return true, b.Progress(it, e.Position, e.Duration, time.UnixMilli(e.LastWatched))
}

func (e watchedEvent) apply(b *store.Batch) (bool, error) {
	var meta *store.Meta
	// Episode events carry the episode's title, which must not replace the show's.
	if e.Season == nil && e.Title != "" {
		meta = &store.Meta{Title: e.Title}
	}
	it, ok := e.item(meta)
	switch {
	case !ok:
		return false, nil
	case e.Operation == opDelete:
		return true, b.Unwatch(it)
	}
	return true, b.Scrobble(it, time.UnixMilli(e.WatchedAt))
}

func (e libraryEvent) apply(b *store.Batch) (bool, error) {
	it, ok := e.item(&store.Meta{Title: e.Name, Year: releaseYear(e.ReleaseInfo), Poster: e.Poster})
	switch {
	case !ok:
		return false, nil
	case e.Operation == opDelete:
		return true, b.RemoveFromList(libraryList, it)
	}
	return true, b.AddToList(libraryList, it, time.UnixMilli(e.AddedAt))
}

func releaseYear(info string) int {
	year, _ := strconv.Atoi(info[:min(4, len(info))])
	return year
}
