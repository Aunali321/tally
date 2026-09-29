package importer

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const (
	stremioAPI = "https://api.strem.io/api/"
	// Cinemeta is Stremio's own catalog. Reading a show's watched episodes needs its episode list.
	cinemetaSeries = "https://v3-cinemeta.strem.io/meta/series/"
)

type stremioItem struct {
	ID      string    `json:"_id"`
	Type    string    `json:"type"`
	Name    string    `json:"name"`
	Poster  string    `json:"poster"`
	Removed bool      `json:"removed"`
	Temp    bool      `json:"temp"`
	Created time.Time `json:"_ctime"`
	State   struct {
		LastWatched    *time.Time `json:"lastWatched"`
		TimeOffset     int64      `json:"timeOffset"`
		Duration       int64      `json:"duration"`
		TimesWatched   int        `json:"timesWatched"`
		FlaggedWatched int        `json:"flaggedWatched"`
		VideoID        string     `json:"video_id"`
		Watched        string     `json:"watched"`
	} `json:"state"`
}

type stremioResponse[T any] struct {
	Result T `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type stremioLogin struct {
	Type     string `json:"type"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Facebook bool   `json:"facebook"`
}

type stremioDatastoreGet struct {
	AuthKey    string   `json:"authKey"`
	Collection string   `json:"collection"`
	IDs        []string `json:"ids"`
	All        bool     `json:"all"`
}

type cinemetaVideo struct {
	ID       string     `json:"id"`
	Season   *int       `json:"season"`
	Episode  *int       `json:"episode"`
	Released *time.Time `json:"released"`
}

type stremioPlay struct {
	item  stremioItem
	video string
	at    *time.Time
}

type stremioImport struct {
	*run
	library []stremioItem
}

func Stremio(ctx context.Context, st *store.Store, uid int64, email, password string, opts Options) ([]store.ImportSection, error) {
	var login stremioResponse[struct {
		AuthKey string `json:"authKey"`
	}]
	if err := stremioCall(ctx, "login", stremioLogin{"Login", email, password, false}, &login); err != nil {
		return nil, fmt.Errorf("sign in to Stremio: %w", err)
	}
	var library stremioResponse[[]stremioItem]
	if err := stremioCall(ctx, "datastoreGet", stremioDatastoreGet{login.Result.AuthKey, "libraryItem", []string{}, true}, &library); err != nil {
		return nil, fmt.Errorf("read Stremio library: %w", err)
	}
	s := stremioImport{newRun(ctx, st, uid, opts), library.Result}
	return s.steps(s.history, s.progress, s.saved)
}

func (s stremioImport) history() error {
	s.begin("Watched history")
	var viewings []stremioPlay
	for _, it := range s.library {
		switch it.Type {
		case "movie":
			n := max(it.State.TimesWatched, it.State.FlaggedWatched)
			viewings = append(viewings, slices.Repeat([]stremioPlay{{it, "", it.State.LastWatched}}, n)...)
		case "series":
			episodes, err := s.watchedEpisodes(it)
			if err != nil {
				return err
			}
			for _, video := range episodes {
				viewings = append(viewings, stremioPlay{it, video, it.State.LastWatched})
			}
		}
	}
	return plays(s.run, viewings, func(p stremioPlay) (store.Item, time.Time, error) {
		it, err := p.item.video(p.video)
		if err == nil && p.at == nil {
			err = errUnsupported
		}
		return it, orNow(p.at), err
	})
}

func (s stremioImport) progress() error {
	s.begin("Playback progress")
	var playing []stremioItem
	for _, it := range s.library {
		if it.State.TimeOffset > 0 && it.State.Duration > 0 && (!it.Removed || it.Temp) {
			playing = append(playing, it)
		}
	}
	return importRows(s.run, playing, func(it stremioItem) (write, error) {
		video := ""
		if it.Type == "series" {
			video = it.State.VideoID
		}
		item, err := it.video(video)
		at := orNow(it.State.LastWatched)
		return func(b *store.Batch) error { return b.Progress(item, it.State.TimeOffset, it.State.Duration, at) }, err
	})
}

func (s stremioImport) saved() error {
	var saved []stremioItem
	for _, it := range s.library {
		if !it.Removed && !it.Temp {
			saved = append(saved, it)
		}
	}
	return list(s.run, "watchlist", "Watchlist", saved, func(it stremioItem) (store.Item, time.Time, error) {
		item, err := it.video("")
		return item, it.Created, err
	})
}

func (s stremioImport) watchedEpisodes(it stremioItem) ([]string, error) {
	if it.State.Watched == "" {
		return nil, nil
	}
	var meta struct {
		Meta struct {
			Videos []cinemetaVideo `json:"videos"`
		} `json:"meta"`
	}
	if err := fetchJSON(s.ctx, http.MethodGet, cinemetaSeries+url.PathEscape(it.ID)+".json", nil, &meta); err != nil {
		return nil, err
	}
	return decodeWatched(it.State.Watched, sortedVideoIDs(meta.Meta.Videos))
}

func (it stremioItem) video(videoID string) (store.Item, error) {
	item := store.Item{ID: it.ID, Meta: &store.Meta{Title: it.Name, Poster: it.Poster}}
	switch it.Type {
	case "movie":
		item.Type = store.Movie
	case "series":
		item.Type = store.Show
	default:
		return store.Item{}, errUnsupported
	}
	if videoID == "" {
		return item, item.Validate()
	}
	parts := strings.Split(videoID, ":")
	if len(parts) < 3 {
		return store.Item{}, errUnsupported
	}
	season, err1 := strconv.Atoi(parts[len(parts)-2])
	episode, err2 := strconv.Atoi(parts[len(parts)-1])
	if err1 != nil || err2 != nil {
		return store.Item{}, errUnsupported
	}
	return episodeItem(item, season, episode)
}

// Stremio indexes its watched bitfield in this order.
func sortedVideoIDs(videos []cinemetaVideo) []string {
	key := func(p *int) int64 {
		if p == nil {
			return minInt64
		}
		return int64(*p)
	}
	released := func(t *time.Time) int64 {
		if t == nil {
			return minInt64
		}
		return t.UnixMilli()
	}
	slices.SortStableFunc(videos, func(a, b cinemetaVideo) int {
		return cmp.Or(
			cmp.Compare(key(a.Season), key(b.Season)),
			cmp.Compare(key(a.Episode), key(b.Episode)),
			cmp.Compare(released(a.Released), released(b.Released)),
		)
	})
	ids := make([]string, len(videos))
	for i, v := range videos {
		ids[i] = v.ID
	}
	return ids
}

const minInt64 = -1 << 63

// decodeWatched reads Stremio's watched field, <anchor video id>:<anchor length>:<base64 zlib bitfield>. Bit i marks
// the i-th video as watched when the anchor sits at index length-1; a shifted anchor shifts every bit with it.
func decodeWatched(field string, videos []string) ([]string, error) {
	last := strings.LastIndexByte(field, ':')
	mid := strings.LastIndexByte(field[:max(last, 0)], ':')
	if last < 0 || mid < 0 {
		return nil, errors.New("malformed watched field")
	}
	anchor, data := field[:mid], field[last+1:]
	anchorLength, err := strconv.Atoi(field[mid+1 : last])
	if err != nil {
		return nil, fmt.Errorf("malformed watched field: %w", err)
	}
	compressed, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	bits, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	at := slices.Index(videos, anchor)
	if at < 0 {
		return nil, nil
	}
	offset := anchorLength - at - 1
	var watched []string
	for i, id := range videos {
		j := i + offset
		if j >= 0 && j < len(videos) && j/8 < len(bits) && bits[j/8]>>(j%8)&1 == 1 {
			watched = append(watched, id)
		}
	}
	return watched, nil
}

func stremioCall[T any](ctx context.Context, method string, body any, out *stremioResponse[T]) error {
	if err := fetchJSON(ctx, http.MethodPost, stremioAPI+method, body, out); err != nil {
		return err
	}
	if out.Error != nil {
		return errors.New(out.Error.Message)
	}
	return nil
}
