package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	Movie = "movie"
	Show  = "show"
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	slugJunk    = regexp.MustCompile(`[^a-z0-9]+`)
)

type Item struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Season  *int   `json:"season,omitempty"`
	Episode *int   `json:"episode,omitempty"`
	Meta    *Meta  `json:"meta,omitempty"`
}

type Meta struct {
	Title  string `json:"title,omitzero"`
	Year   int    `json:"year,omitzero"`
	Poster string `json:"poster,omitzero"`
}

func (i Item) Key() string {
	if i.Season == nil {
		return i.titleKey()
	}
	return fmt.Sprintf("%s:%s:%d:%d", i.Type, i.ID, *i.Season, *i.Episode)
}

func (i Item) titleKey() string {
	return i.Type + ":" + i.ID
}

func (i Item) Validate() error {
	switch {
	case i.Type != Movie && i.Type != Show:
		return errors.New("type must be movie or show")
	case i.ID == "" || len(i.ID) > 128:
		return errors.New("id must be 1-128 chars")
	case (i.Season == nil) != (i.Episode == nil):
		return errors.New("season and episode must be set together")
	case i.Season != nil && i.Type != Show:
		return errors.New("season and episode are only valid on shows")
	case i.Season != nil && (*i.Season < 0 || *i.Episode < 1):
		return errors.New("season must be >= 0 and episode >= 1")
	}
	return nil
}

func ValidateRating(rating int) error {
	if rating < 1 || rating > 10 {
		return errors.New("rating must be 1..10")
	}
	return nil
}

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return errors.New("slug must match [a-z0-9-]{1,64}")
	}
	return nil
}

func SlugFor(name string) string {
	s := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(name), "-"), "-")
	s = strings.TrimRight(s[:min(len(s), 64)], "-")
	if ValidateSlug(s) != nil {
		return ""
	}
	return s
}

func nameFor(slug string) string {
	name := strings.ReplaceAll(slug, "-", " ")
	return strings.ToUpper(name[:1]) + name[1:]
}

type itemColumns struct {
	typ, id         string
	season, episode sql.NullInt64
	title, poster   sql.NullString
	year            sql.NullInt64
}

func (c *itemColumns) dest() []any {
	return []any{&c.typ, &c.id, &c.season, &c.episode}
}

func (c *itemColumns) metaDest() []any {
	return []any{&c.title, &c.year, &c.poster}
}

func (c *itemColumns) meta() *Meta {
	if !c.title.Valid {
		return nil
	}
	return &Meta{Title: c.title.String, Year: int(c.year.Int64), Poster: c.poster.String}
}

func (c *itemColumns) item() Item {
	it := Item{Type: c.typ, ID: c.id, Meta: c.meta()}
	if c.season.Valid && c.episode.Valid {
		s, e := int(c.season.Int64), int(c.episode.Int64)
		it.Season, it.Episode = &s, &e
	}
	return it
}
