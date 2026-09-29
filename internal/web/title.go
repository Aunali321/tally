package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

type titleView struct {
	chrome
	Title   store.Title
	Summary string
	Seasons []seasonView
	Lists   []listToggle
	Log     episodeRef
	Today   string
}

type seasonView struct {
	Number   int
	Episodes []store.HistoryEntry
	Plays    int
}

type listToggle struct {
	Slug, Name string
	On         bool
}

type episodeRef struct {
	Season, Episode int
}

func (s *server) titlePage(w http.ResponseWriter, r *http.Request, uid int64) {
	it, err := pathItem(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view, err := s.titleView(r, uid, it)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r, uid, "Nothing recorded for this title.")
	case err != nil:
		s.fail(w, r, err)
	default:
		s.page(w, "title", view)
	}
}

func (s *server) titleView(r *http.Request, uid int64, it store.Item) (titleView, error) {
	ctx := r.Context()
	t, err := s.store.Title(ctx, uid, it.Type, it.ID)
	if err != nil {
		return titleView{}, err
	}
	view := titleView{Title: t, Today: time.Now().Format(dayParam), Log: nextEpisode(t.Watched)}
	if view.chrome, err = s.chrome(ctx, uid, "", itemTitle(t.Item)); err != nil {
		return view, err
	}
	lists, err := s.store.Lists(ctx, uid)
	if err != nil {
		return view, err
	}
	for _, l := range lists {
		view.Lists = append(view.Lists, listToggle{l.Slug, l.Name, slices.Contains(t.Lists, l.Slug)})
	}
	for _, e := range t.Watched {
		if e.Item.Season == nil {
			continue
		}
		if n := len(view.Seasons); n == 0 || view.Seasons[n-1].Number != *e.Item.Season {
			view.Seasons = append(view.Seasons, seasonView{Number: *e.Item.Season})
		}
		season := &view.Seasons[len(view.Seasons)-1]
		season.Episodes = append(season.Episodes, e)
		season.Plays += e.Plays
	}
	view.Summary = titleSummary(t, time.Now())
	return view, nil
}

func titleSummary(t store.Title, now time.Time) string {
	if len(t.Plays) == 0 {
		if t.Item.Type == store.Show {
			return "No episodes watched yet."
		}
		return "Not watched to the end yet."
	}
	last := t.Plays[0].WatchedAt
	on := dayLabel(last, now)
	if on != "Today" && on != "Yesterday" {
		on = "on " + on
	} else {
		on = strings.ToLower(on)
	}
	if t.Item.Type == store.Show {
		return fmt.Sprintf("%d %s, %d %s. Last %s at %s.",
			len(t.Watched), plural(len(t.Watched), "episode", "episodes"), len(t.Plays), plural(len(t.Plays), "play", "plays"), on, last.Format("15:04"))
	}
	if len(t.Plays) == 1 {
		return fmt.Sprintf("Watched once, %s.", on)
	}
	return fmt.Sprintf("Watched %d times, last %s.", len(t.Plays), on)
}

func nextEpisode(watched []store.HistoryEntry) episodeRef {
	var last *store.HistoryEntry
	for i, e := range watched {
		if e.Item.Season != nil && (last == nil || e.WatchedAt.After(last.WatchedAt)) {
			last = &watched[i]
		}
	}
	if last == nil {
		return episodeRef{1, 1}
	}
	return episodeRef{*last.Item.Season, *last.Item.Episode + 1}
}

func (s *server) rateTitle(w http.ResponseWriter, r *http.Request, uid int64) {
	s.titleAction(w, r, uid, func(b *store.Batch, it store.Item) error {
		v := r.PostFormValue("rating")
		if v == "" {
			return b.Unrate(it)
		}
		rating, err := strconv.Atoi(v)
		if err == nil {
			err = store.ValidateRating(rating)
		}
		if err != nil {
			return badRequest("Rating must be 1 to 10.")
		}
		return b.Rate(it, rating, time.Now())
	})
}

func (s *server) toggleList(w http.ResponseWriter, r *http.Request, uid int64) {
	slug := r.PathValue("slug")
	s.titleAction(w, r, uid, func(b *store.Batch, it store.Item) error {
		if r.PostFormValue("on") == "true" {
			return b.RemoveFromList(slug, it)
		}
		return b.AddToList(slug, it, time.Now())
	})
}

func (s *server) addToNewList(w http.ResponseWriter, r *http.Request, uid int64) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	slug := store.SlugFor(name)
	s.titleAction(w, r, uid, func(b *store.Batch, it store.Item) error {
		if slug == "" || len(name) > 256 {
			return badRequest("Give the list a name with letters or numbers.")
		}
		if err := b.PutList(slug, name); err != nil {
			return err
		}
		return b.AddToList(slug, it, time.Now())
	})
}

func (s *server) watchTitle(w http.ResponseWriter, r *http.Request, uid int64) {
	s.titleAction(w, r, uid, func(b *store.Batch, it store.Item) error { return b.Watch(it, time.Now()) })
}

func (s *server) logEpisode(w http.ResponseWriter, r *http.Request, uid int64) {
	s.titleAction(w, r, uid, func(b *store.Batch, show store.Item) error {
		season, err1 := strconv.Atoi(r.PostFormValue("season"))
		episode, err2 := strconv.Atoi(r.PostFormValue("episode"))
		day, err3 := time.ParseInLocation(dayParam, r.PostFormValue("date"), time.Local)
		ep := show
		ep.Season, ep.Episode = &season, &episode
		if errors.Join(err1, err2, err3) != nil || ep.Validate() != nil {
			return badRequest("Enter a season, an episode and a date.")
		}
		return b.Watch(ep, watchedOn(day, time.Now()))
	})
}

func (s *server) unwatchEpisode(w http.ResponseWriter, r *http.Request, uid int64) {
	s.titleAction(w, r, uid, func(b *store.Batch, show store.Item) error {
		ep, err := episodeOf(r, show)
		if err != nil {
			return err
		}
		return b.Unwatch(ep)
	})
}

func (s *server) removePlay(w http.ResponseWriter, r *http.Request, uid int64) {
	s.titleAction(w, r, uid, func(b *store.Batch, _ store.Item) error {
		id, err := strconv.ParseInt(r.PostFormValue("play"), 10, 64)
		if err != nil {
			return badRequest("Unknown play.")
		}
		return b.RemovePlay(id)
	})
}

func (s *server) clearTitleProgress(w http.ResponseWriter, r *http.Request, uid int64) {
	s.titleAction(w, r, uid, func(b *store.Batch, it store.Item) error {
		if r.PostFormValue("season") != "" {
			ep, err := episodeOf(r, it)
			if err != nil {
				return err
			}
			it = ep
		}
		return b.DeleteProgress(it)
	})
}

func (s *server) titleAction(w http.ResponseWriter, r *http.Request, uid int64, apply func(*store.Batch, store.Item) error) {
	it, err := pathItem(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	status := http.StatusOK
	err = s.store.Write(r.Context(), uid, func(b *store.Batch) error { return apply(b, it) })
	bad, invalid := errors.AsType[badRequest](err)
	switch {
	case invalid:
		status = http.StatusUnprocessableEntity
	case err != nil && !errors.Is(err, store.ErrNotFound):
		s.fail(w, r, err)
		return
	case !isHTMX(r):
		redirect(w, r, titleURL(it))
		return
	}
	view, err := s.titleView(r, uid, it)
	if errors.Is(err, store.ErrNotFound) {
		redirect(w, r, "/")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view.Error = string(bad)
	s.render(w, status, "title", "layout", view)
}

// The form asks only for a date, so past days get a fixed evening time.
func watchedOn(day, now time.Time) time.Time {
	if day.Equal(startOfDay(now)) {
		return now
	}
	return day.Add(20 * time.Hour)
}

func pathItem(r *http.Request) (store.Item, error) {
	it := store.Item{Type: r.PathValue("type"), ID: r.PathValue("id")}
	return it, it.Validate()
}

func episodeOf(r *http.Request, show store.Item) (store.Item, error) {
	season, err1 := strconv.Atoi(r.PostFormValue("season"))
	episode, err2 := strconv.Atoi(r.PostFormValue("episode"))
	show.Season, show.Episode = &season, &episode
	if errors.Join(err1, err2) != nil || show.Validate() != nil {
		return show, badRequest("Unknown episode.")
	}
	return show, nil
}

func when(t, now time.Time) string {
	day := dayLabel(t, now)
	switch {
	case day == "Today" || day == "Yesterday":
	case t.Year() != now.Year():
		day = t.Format("2 January 2006")
	default:
		day = t.Format("2 January")
	}
	return day + ", " + t.Format("15:04")
}
