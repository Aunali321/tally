package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

const searchLimit = 50

type Watched struct {
	Item      Item  `json:"item"`
	Plays     int   `json:"plays"`
	WatchedAt int64 `json:"watched_at"`
	Deleted   bool  `json:"deleted"`
}

type Progress struct {
	Item      Item  `json:"item"`
	Position  int64 `json:"position"`
	Duration  int64 `json:"duration"`
	UpdatedAt int64 `json:"updated_at"`
	Deleted   bool  `json:"deleted"`
}

type Rating struct {
	Item    Item  `json:"item"`
	Rating  int   `json:"rating"`
	RatedAt int64 `json:"rated_at"`
	Deleted bool  `json:"deleted"`
}

type List struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

type ListItem struct {
	List    string `json:"list"`
	Item    Item   `json:"item"`
	AddedAt int64  `json:"added_at"`
	Deleted bool   `json:"deleted"`
}

type TitleMeta struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Poster  string `json:"poster"`
	Deleted bool   `json:"deleted"`
}

type Changes struct {
	Rev       int64       `json:"rev"`
	Watched   []Watched   `json:"watched"`
	Progress  []Progress  `json:"progress"`
	Ratings   []Rating    `json:"ratings"`
	Lists     []List      `json:"lists"`
	ListItems []ListItem  `json:"list_items"`
	Meta      []TitleMeta `json:"meta"`
}

type SearchResult struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Meta
	Watched  bool           `json:"watched"`
	Plays    int            `json:"plays"`
	Episodes int            `json:"episodes,omitzero"`
	Lists    []string       `json:"lists"`
	Rating   *int           `json:"rating,omitempty"`
	Progress *PlaybackState `json:"progress,omitempty"`
}

type PlaybackState struct {
	Position int64 `json:"position"`
	Duration int64 `json:"duration"`
}

func (s *Store) Changes(ctx context.Context, uid, since int64) (Changes, error) {
	var c Changes
	err := inReadTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT rev FROM users WHERE id = ?`, uid).Scan(&c.Rev); err != nil {
			return err
		}
		args := []any{uid, since, since}
		var err error
		if c.Watched, err = query(ctx, tx, "SELECT type, id, season, episode, plays, watched_at, deleted FROM watched"+changedAfter, args,
			func(rows *sql.Rows) (v Watched, err error) {
				var ic itemColumns
				err = rows.Scan(append(ic.dest(), &v.Plays, &v.WatchedAt, &v.Deleted)...)
				v.Item = ic.item()
				return v, err
			}); err != nil {
			return err
		}
		if c.Progress, err = query(ctx, tx, "SELECT type, id, season, episode, position, duration, updated_at, deleted FROM progress"+changedAfter, args,
			func(rows *sql.Rows) (v Progress, err error) {
				var ic itemColumns
				err = rows.Scan(append(ic.dest(), &v.Position, &v.Duration, &v.UpdatedAt, &v.Deleted)...)
				v.Item = ic.item()
				return v, err
			}); err != nil {
			return err
		}
		if c.Ratings, err = query(ctx, tx, "SELECT type, id, season, episode, rating, rated_at, deleted FROM ratings"+changedAfter, args,
			func(rows *sql.Rows) (v Rating, err error) {
				var ic itemColumns
				err = rows.Scan(append(ic.dest(), &v.Rating, &v.RatedAt, &v.Deleted)...)
				v.Item = ic.item()
				return v, err
			}); err != nil {
			return err
		}
		if c.Lists, err = query(ctx, tx, "SELECT slug, name, deleted FROM lists"+changedAfter, args,
			func(rows *sql.Rows) (v List, err error) {
				return v, rows.Scan(&v.Slug, &v.Name, &v.Deleted)
			}); err != nil {
			return err
		}
		if c.ListItems, err = query(ctx, tx, "SELECT list, type, id, season, episode, added_at, deleted FROM list_items"+changedAfter, args,
			func(rows *sql.Rows) (v ListItem, err error) {
				var ic itemColumns
				err = rows.Scan(append(append([]any{&v.List}, ic.dest()...), &v.AddedAt, &v.Deleted)...)
				v.Item = ic.item()
				return v, err
			}); err != nil {
			return err
		}
		c.Meta, err = query(ctx, tx, "SELECT type, id, title, year, poster, deleted FROM meta"+changedAfter, args,
			func(rows *sql.Rows) (v TitleMeta, err error) {
				return v, rows.Scan(&v.Type, &v.ID, &v.Title, &v.Year, &v.Poster, &v.Deleted)
			})
		return err
	})
	return c, err
}

func (s *Store) HasWatched(ctx context.Context, uid int64) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM watched WHERE user_id = ? AND NOT deleted)`, uid).Scan(&ok)
	return ok, err
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Parameters: ?1 user id, ?2 LIKE pattern for ids, ?3 LIKE pattern for titles.
const trackedTitles = `
	SELECT type, id FROM meta WHERE user_id = ?1 AND NOT deleted AND (id LIKE ?2 ESCAPE '\' OR title LIKE ?3 ESCAPE '\')
	UNION SELECT type, id FROM watched WHERE user_id = ?1 AND NOT deleted AND id LIKE ?2 ESCAPE '\'
	UNION SELECT type, id FROM progress WHERE user_id = ?1 AND NOT deleted AND id LIKE ?2 ESCAPE '\'
	UNION SELECT type, id FROM ratings WHERE user_id = ?1 AND NOT deleted AND id LIKE ?2 ESCAPE '\'
	UNION SELECT type, id FROM list_items WHERE user_id = ?1 AND NOT deleted AND id LIKE ?2 ESCAPE '\'`

func (s *Store) TitleCount(ctx context.Context, uid int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (`+trackedTitles+`)`, uid, "%", "%").Scan(&n)
	return n, err
}

func (s *Store) Search(ctx context.Context, uid int64, query, typ string) ([]SearchResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH t AS (`+trackedTitles+`)
		SELECT t.type, t.id, coalesce(m.title, ''), coalesce(m.year, 0), coalesce(m.poster, ''),
			(SELECT count(*) FROM plays p WHERE p.user_id = ?1 AND p.type = t.type AND p.id = t.id),
			(SELECT count(*) FROM watched w WHERE w.user_id = ?1 AND w.type = t.type AND w.id = t.id AND w.season IS NOT NULL AND NOT w.deleted),
			(SELECT json_group_array(DISTINCT l.list ORDER BY l.list) FROM list_items l
				WHERE l.user_id = ?1 AND l.type = t.type AND l.id = t.id AND NOT l.deleted),
			(SELECT r.rating FROM ratings r WHERE r.user_id = ?1 AND r.key = t.type || ':' || t.id AND NOT r.deleted),
			(SELECT json_object('position', g.position, 'duration', g.duration) FROM progress g
				WHERE g.user_id = ?1 AND g.type = t.type AND g.id = t.id AND NOT g.deleted ORDER BY g.updated_at DESC LIMIT 1)
		FROM t LEFT JOIN meta m ON m.user_id = ?1 AND m.key = t.type || ':' || t.id AND NOT m.deleted
		WHERE ?4 = '' OR t.type = ?4
		ORDER BY m.title IS NULL, m.title COLLATE NOCASE, m.year
		LIMIT ?5`,
		uid, likeEscaper.Replace(query), "%"+likeEscaper.Replace(query)+"%", typ, searchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		var lists string
		var rating sql.NullInt64
		var progress sql.NullString
		if err := rows.Scan(&r.Type, &r.ID, &r.Title, &r.Year, &r.Poster, &r.Plays, &r.Episodes, &lists, &rating, &progress); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(lists), &r.Lists); err != nil {
			return nil, err
		}
		if progress.Valid {
			if err := json.Unmarshal([]byte(progress.String), &r.Progress); err != nil {
				return nil, err
			}
		}
		if rating.Valid {
			r.Rating = new(int(rating.Int64))
		}
		r.Watched = r.Plays > 0
		results = append(results, r)
	}
	return results, rows.Err()
}

const changedAfter = " WHERE user_id = ? AND rev > ? AND (NOT deleted OR ? > 0) ORDER BY rev"

func inReadTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}
