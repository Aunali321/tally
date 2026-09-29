package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Aunali321/tally/internal/nuvio"
	"github.com/Aunali321/tally/internal/store"
)

type chrome struct {
	Title   string
	Section string
	User    string
	Sync    *syncChip
	Error   string
}

type syncChip struct {
	Label string
}

func (s *server) chrome(ctx context.Context, uid int64, section, title string) (chrome, error) {
	email, err := s.store.Email(ctx, uid)
	if err != nil {
		return chrome{}, err
	}
	c := chrome{Title: title, Section: section, User: email}
	link, err := s.store.NuvioLink(ctx, uid)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return c, nil
	case err != nil:
		return c, err
	}
	c.Sync = nuvioAttention(link)
	return c, nil
}

func nuvioAttention(link store.NuvioLink) *syncChip {
	switch {
	case link.Status.SessionExpired:
		return &syncChip{"Nuvio sign-in expired"}
	case link.Profile == 0:
		return &syncChip{"Pick a Nuvio profile"}
	case link.Status.Error != "":
		return &syncChip{"Nuvio sync failed"}
	}
	return nil
}

type section struct {
	Name, Label, Href string
}

var sections = []section{
	{"home", "Home", "/"},
	{"lists", "Lists", "/lists"},
	{"search", "Search", "/search"},
	{"settings", "Settings", "/settings"},
}

var funcs = template.FuncMap{
	"sections":  func() []section { return sections },
	"seq":       seq,
	"weekdays":  func() []string { return []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} },
	"initial":   initial,
	"title":     itemTitle,
	"untitled":  func(it store.Item) bool { return it.Meta == nil || it.Meta.Title == "" },
	"year":      func(it store.Item) int { return metaOf(it).Year },
	"poster":    func(it store.Item) string { return metaOf(it).Poster },
	"episode":   episodeLabel,
	"titleURL":  titleURL,
	"imdbURL":   imdbURL,
	"percent":   percent,
	"remaining": remaining,
	"asItem":    resultItem,
	"tile":      func(e store.ListEntry, l *store.ListSummary) tileView { return tileView{e, l} },
	"picker": func(profiles []nuvio.Profile, current int, problem string) pickerView {
		return pickerView{profiles, current, problem}
	},
	"left": func(p *store.PlaybackState) string {
		return remaining(time.Duration(p.Position)*time.Millisecond, time.Duration(p.Duration)*time.Millisecond)
	},
	"clock":      func(t time.Time) string { return t.Format("15:04") },
	"iso":        func(t time.Time) string { return t.Format(time.RFC3339) },
	"when":       func(t time.Time) string { return when(t, time.Now()) },
	"session":    func(ss store.Session) string { return sessionSummary(ss, time.Now()) },
	"syncEvery":  func() string { return every(nuvio.PollInterval) },
	"longDate":   func(t time.Time) string { return t.Format("Monday 2 January 2006") },
	"shortDate":  func(t time.Time) string { return lowerDay(t, time.Now()) },
	"inviteDays": func() int { return int(store.InviteTTL.Hours() / 24) },
	"number":     number,
	"plural":     plural,
	"tally":      tallyGroups,
}

func seq(from, to int) []int {
	n := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		n = append(n, i)
	}
	return n
}

func initial(name string) string {
	r, _ := utf8.DecodeRuneInString(name)
	return strings.ToUpper(string(r))
}

func metaOf(it store.Item) store.Meta {
	if it.Meta == nil {
		return store.Meta{}
	}
	return *it.Meta
}

type pickerView struct {
	Profiles []nuvio.Profile
	Current  int
	Error    string
}

type tileView struct {
	Entry store.ListEntry
	List  *store.ListSummary
}

func resultItem(r store.SearchResult) store.Item {
	it := store.Item{Type: r.Type, ID: r.ID}
	if r.Meta != (store.Meta{}) {
		it.Meta = &r.Meta
	}
	return it
}

func itemTitle(it store.Item) string {
	if t := metaOf(it).Title; t != "" {
		return t
	}
	return "Untitled " + it.Type
}

func episodeLabel(it store.Item) string {
	if it.Season == nil {
		return ""
	}
	return fmt.Sprintf("S%d E%d", *it.Season, *it.Episode)
}

func titleURL(it store.Item) string {
	return "/title/" + it.Type + "/" + url.PathEscape(it.ID)
}

func imdbURL(id string) string {
	if !strings.HasPrefix(id, "tt") {
		return ""
	}
	return "https://www.imdb.com/title/" + id + "/"
}

func percent(position, duration time.Duration) int {
	if duration <= 0 {
		return 0
	}
	return int(100 * position / duration)
}

func remaining(position, duration time.Duration) string {
	left := (duration - position).Round(time.Minute)
	h, m := int(left.Hours()), int(left.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm left", h, m)
	case h > 0:
		return fmt.Sprintf("%dh left", h)
	}
	return fmt.Sprintf("%dm left", max(m, 1))
}

func number(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

type tallyGroup struct {
	Bars []struct{}
	Five bool
}

func tallyGroups(n int) []tallyGroup {
	var groups []tallyGroup
	for ; n > 0; n -= 5 {
		groups = append(groups, tallyGroup{make([]struct{}, min(n, 4)), n >= 5})
	}
	return groups
}

func every(d time.Duration) string {
	if m := int(d.Minutes()); m != 1 {
		return fmt.Sprintf("every %d minutes", m)
	}
	return "every minute"
}
