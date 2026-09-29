package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const (
	historyPage   = 30
	postersPerDay = 3
	dayParam      = "2006-01-02"
	monthParam    = "2006-01"
	// Days with more plays than this show the count instead of tally marks, which would not fit the cell.
	tallyLimit = 10
)

type homeView struct {
	chrome
	Month month
	Week  []calendarDay
	Stats store.Stats
	Side  sideView
}

type month struct {
	Label      string
	Prev, Next string
	Days       []calendarDay
}

type calendarDay struct {
	Date     time.Time
	Label    string
	Out      bool
	Today    bool
	Selected bool
	Plays    int
	Tallies  bool
	Titles   []store.Item
}

type sideView struct {
	Resumes []store.ProgressEntry
	Day     *dayGroup
	Recent  []dayGroup
	More    string
	Linked  bool
	Empty   bool
}

type dayGroup struct {
	Key   string
	Label string
	Plays []store.HistoryEntry
}

func (s *server) home(w http.ResponseWriter, r *http.Request, uid int64) {
	view, err := s.homeView(r.Context(), uid, r.URL.Query())
	_, bad := errors.AsType[badRequest](err)
	switch {
	case bad:
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		s.fail(w, r, err)
	default:
		s.page(w, "home", view)
	}
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

func (s *server) homeView(ctx context.Context, uid int64, q url.Values) (homeView, error) {
	now := time.Now()
	today := startOfDay(now)
	shown := today
	var day *time.Time
	if v := q.Get("day"); v != "" {
		d, err := time.ParseInLocation(dayParam, v, time.Local)
		if err != nil {
			return homeView{}, badRequest("Invalid day.")
		}
		day, shown = &d, d
	}
	if v := q.Get("month"); v != "" {
		m, err := time.ParseInLocation(monthParam, v, time.Local)
		if err != nil {
			return homeView{}, badRequest("Invalid month.")
		}
		shown = m
	}

	var view homeView
	var err error
	if view.chrome, err = s.chrome(ctx, uid, "home", "Home"); err != nil {
		return view, err
	}
	week := today.AddDate(0, 0, -int(weekday(today)))
	if view.Stats, err = s.store.Stats(ctx, uid, week, time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.Local)); err != nil {
		return view, err
	}
	if view.Month, err = s.month(ctx, uid, shown, day, today); err != nil {
		return view, err
	}
	if view.Week, err = s.days(ctx, uid, today.AddDate(0, 0, -6), today.AddDate(0, 0, 1), time.Time{}, day, today); err != nil {
		return view, err
	}
	view.Side, err = s.side(ctx, uid, day, now)
	return view, err
}

func (s *server) history(w http.ResponseWriter, r *http.Request, uid int64) {
	after, err := parseCursor(r.URL.Query().Get("after"))
	if err != nil {
		http.Error(w, "Invalid cursor.", http.StatusBadRequest)
		return
	}
	side, err := s.recent(r.Context(), uid, after, r.URL.Query().Get("day"), time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "home", "history", side)
}

func (s *server) markWatched(w http.ResponseWriter, r *http.Request, uid int64) {
	s.progressAction(w, r, uid, func(b *store.Batch, it store.Item) error { return b.Watch(it, time.Now()) })
}

func (s *server) clearProgress(w http.ResponseWriter, r *http.Request, uid int64) {
	s.progressAction(w, r, uid, (*store.Batch).DeleteProgress)
}

func (s *server) progressAction(w http.ResponseWriter, r *http.Request, uid int64, apply func(*store.Batch, store.Item) error) {
	it, err := formItem(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.Write(r.Context(), uid, func(b *store.Batch) error { return apply(b, it) }); err != nil {
		s.fail(w, r, err)
		return
	}
	s.rerenderHome(w, r, uid)
}

func (s *server) rerenderHome(w http.ResponseWriter, r *http.Request, uid int64) {
	current, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if !isHTMX(r) || err != nil {
		redirect(w, r, "/")
		return
	}
	view, err := s.homeView(r.Context(), uid, current.Query())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.page(w, "home", view)
}

func (s *server) side(ctx context.Context, uid int64, day *time.Time, now time.Time) (sideView, error) {
	resumes, err := s.store.InProgress(ctx, uid)
	if err != nil {
		return sideView{}, err
	}
	var side sideView
	if day != nil {
		plays, err := s.store.Plays(ctx, uid, *day, day.AddDate(0, 0, 1))
		if err != nil {
			return sideView{}, err
		}
		side.Day = &dayGroup{Key: day.Format(dayParam), Label: dayLabel(*day, now), Plays: plays}
	} else if side, err = s.recent(ctx, uid, store.HistoryCursor{}, "", now); err != nil {
		return sideView{}, err
	}
	side.Resumes = resumes
	_, err = s.store.NuvioLink(ctx, uid)
	side.Linked = err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return sideView{}, err
	}
	side.Empty = day == nil && len(side.Recent) == 0 && len(resumes) == 0
	return side, nil
}

func (s *server) recent(ctx context.Context, uid int64, after store.HistoryCursor, continuing string, now time.Time) (sideView, error) {
	plays, err := s.store.History(ctx, uid, after, historyPage)
	if err != nil {
		return sideView{}, err
	}
	var side sideView
	for _, p := range plays {
		key := p.WatchedAt.Format(dayParam)
		if n := len(side.Recent); n > 0 && side.Recent[n-1].Key == key {
			side.Recent[n-1].Plays = append(side.Recent[n-1].Plays, p)
			continue
		}
		label := dayLabel(p.WatchedAt, now)
		if key == continuing {
			label = ""
		}
		side.Recent = append(side.Recent, dayGroup{Key: key, Label: label, Plays: []store.HistoryEntry{p}})
	}
	if len(plays) == historyPage {
		last := plays[len(plays)-1]
		side.More = fmt.Sprintf("/history?after=%s&day=%s", formatCursor(last.Cursor()), last.WatchedAt.Format(dayParam))
	}
	return side, nil
}

func (s *server) month(ctx context.Context, uid int64, shown time.Time, selected *time.Time, today time.Time) (month, error) {
	first := time.Date(shown.Year(), shown.Month(), 1, 0, 0, 0, 0, time.Local)
	start := first.AddDate(0, 0, -int(weekday(first)))
	end := first.AddDate(0, 1, 0)
	end = end.AddDate(0, 0, (7-int(weekday(end)))%7)
	days, err := s.days(ctx, uid, start, end, first, selected, today)
	if err != nil {
		return month{}, err
	}
	m := month{Label: first.Format("January 2006"), Prev: first.AddDate(0, -1, 0).Format(monthParam), Days: days}
	if next := first.AddDate(0, 1, 0); !next.After(today) {
		m.Next = next.Format(monthParam)
	}
	return m, nil
}

func (s *server) days(ctx context.Context, uid int64, from, to, inMonth time.Time, selected *time.Time, today time.Time) ([]calendarDay, error) {
	plays, err := s.store.Plays(ctx, uid, from, to)
	if err != nil {
		return nil, err
	}
	var days []calendarDay
	i := 0
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		cd := calendarDay{Date: d, Today: d.Equal(today), Selected: selected != nil && d.Equal(*selected)}
		cd.Out = !inMonth.IsZero() && d.Month() != inMonth.Month()
		next := d.AddDate(0, 0, 1)
		seen := map[string]bool{}
		for ; i < len(plays) && plays[i].WatchedAt.Before(next); i++ {
			cd.Plays++
			title := plays[i].Item
			title.Season, title.Episode = nil, nil
			if key := title.Key(); !seen[key] && len(cd.Titles) < postersPerDay {
				seen[key] = true
				cd.Titles = append(cd.Titles, title)
			}
		}
		cd.Tallies = cd.Plays <= tallyLimit
		cd.Label = dayLabel(d, today)
		days = append(days, cd)
	}
	return days, nil
}

func formItem(r *http.Request) (store.Item, error) {
	it := store.Item{Type: r.PostFormValue("type"), ID: r.PostFormValue("id")}
	if v := r.PostFormValue("season"); v != "" {
		season, err1 := strconv.Atoi(v)
		episode, err2 := strconv.Atoi(r.PostFormValue("episode"))
		if err := errors.Join(err1, err2); err != nil {
			return it, err
		}
		it.Season, it.Episode = &season, &episode
	}
	return it, it.Validate()
}

func formatCursor(c store.HistoryCursor) string {
	return fmt.Sprintf("%d.%d", c.WatchedAt.UnixMilli(), c.Row)
}

func parseCursor(s string) (store.HistoryCursor, error) {
	at, row, ok := strings.Cut(s, ".")
	ms, err1 := strconv.ParseInt(at, 10, 64)
	id, err2 := strconv.ParseInt(row, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return store.HistoryCursor{}, errors.New("invalid cursor")
	}
	return store.HistoryCursor{WatchedAt: time.UnixMilli(ms), Row: id}, nil
}

func dayLabel(t, now time.Time) string {
	day, today := startOfDay(t), startOfDay(now)
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday"
	}
	if t.Year() != now.Year() {
		return t.Format("Monday 2 January 2006")
	}
	return t.Format("Monday 2 January")
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func weekday(t time.Time) time.Weekday {
	return (t.Weekday() + 6) % 7
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
