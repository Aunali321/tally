package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

type HistoryEntry struct {
	Item      Item
	Plays     int
	WatchedAt time.Time
	PlayID    int64
}

type HistoryCursor struct {
	WatchedAt time.Time
	Row       int64
}

func (e HistoryEntry) Cursor() HistoryCursor {
	return HistoryCursor{e.WatchedAt, e.PlayID}
}

type ProgressEntry struct {
	Item      Item
	Position  time.Duration
	Duration  time.Duration
	UpdatedAt time.Time
}

type Stats struct {
	Week  int
	Month int
	Plays int
}

type ListSummary struct {
	Slug  string
	Name  string
	Count int
}

type ListEntry struct {
	Item    Item
	AddedAt time.Time
}

type Title struct {
	Item     Item
	Watched  []HistoryEntry
	Plays    []HistoryEntry
	Progress []ProgressEntry
	Rating   int
	Lists    []string
}

const maxMillis = math.MaxInt64

const metaJoin = `LEFT JOIN meta m ON m.user_id = t.user_id AND m.key = t.type || ':' || t.id AND NOT m.deleted`

const playsSelect = `
	SELECT t.type, t.id, t.season, t.episode, m.title, m.year, m.poster, w.plays, t.watched_at, t.rowid FROM plays t
	JOIN watched w ON w.user_id = t.user_id AND w.key = t.key ` + metaJoin

func (s *Store) History(ctx context.Context, uid int64, after HistoryCursor, limit int) ([]HistoryEntry, error) {
	at, row := after.WatchedAt.UnixMilli(), after.Row
	if after.WatchedAt.IsZero() {
		at, row = maxMillis, 0
	}
	return query(ctx, s.db, playsSelect+`
		WHERE t.user_id = ? AND (t.watched_at, t.rowid) < (?, ?)
		ORDER BY t.watched_at DESC, t.rowid DESC LIMIT ?`,
		[]any{uid, at, row, limit}, scanPlay)
}

func (s *Store) Plays(ctx context.Context, uid int64, from, to time.Time) ([]HistoryEntry, error) {
	return query(ctx, s.db, playsSelect+`
		WHERE t.user_id = ? AND t.watched_at >= ? AND t.watched_at < ?
		ORDER BY t.watched_at, t.rowid`,
		[]any{uid, from.UnixMilli(), to.UnixMilli()}, scanPlay)
}

func (s *Store) InProgress(ctx context.Context, uid int64) ([]ProgressEntry, error) {
	return query(ctx, s.db, `
		SELECT t.type, t.id, t.season, t.episode, m.title, m.year, m.poster, t.position, t.duration, t.updated_at FROM progress t `+metaJoin+`
		WHERE t.user_id = ? AND NOT t.deleted ORDER BY t.updated_at DESC`,
		[]any{uid}, scanProgress)
}

func (s *Store) Stats(ctx context.Context, uid int64, week, month time.Time) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT type || ':' || id) FILTER (WHERE watched_at >= ?),
			count(DISTINCT type || ':' || id) FILTER (WHERE watched_at >= ?),
			count(*)
		FROM plays WHERE user_id = ?`,
		week.UnixMilli(), month.UnixMilli(), uid).Scan(&st.Week, &st.Month, &st.Plays)
	return st, err
}

func (s *Store) Lists(ctx context.Context, uid int64) ([]ListSummary, error) {
	return query(ctx, s.db, `
		SELECT l.slug, l.name, count(i.key) FROM lists l
		LEFT JOIN list_items i ON i.user_id = l.user_id AND i.list = l.slug AND NOT i.deleted
		WHERE l.user_id = ? AND NOT l.deleted
		GROUP BY l.slug ORDER BY l.name COLLATE NOCASE`,
		[]any{uid}, func(rows *sql.Rows) (l ListSummary, err error) {
			return l, rows.Scan(&l.Slug, &l.Name, &l.Count)
		})
}

func (s *Store) ListEntries(ctx context.Context, uid int64, slug string) ([]ListEntry, error) {
	return query(ctx, s.db, `
		SELECT t.type, t.id, t.season, t.episode, m.title, m.year, m.poster, t.added_at FROM list_items t `+metaJoin+`
		WHERE t.user_id = ? AND t.list = ? AND NOT t.deleted ORDER BY t.added_at DESC`,
		[]any{uid, slug}, func(rows *sql.Rows) (e ListEntry, err error) {
			var c itemColumns
			var added int64
			err = rows.Scan(append(c.dest(), append(c.metaDest(), &added)...)...)
			e.Item, e.AddedAt = c.item(), time.UnixMilli(added)
			return e, err
		})
}

func (s *Store) Title(ctx context.Context, uid int64, typ, id string) (Title, error) {
	t := Title{Item: Item{Type: typ, ID: id}}
	err := inReadTx(ctx, s.db, func(tx *sql.Tx) error {
		var m itemColumns
		err := tx.QueryRowContext(ctx, `SELECT title, year, poster FROM meta WHERE user_id = ? AND key = ? AND NOT deleted`,
			uid, t.Item.titleKey()).Scan(m.metaDest()...)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		t.Item.Meta = m.meta()
		args := []any{uid, typ, id}
		if t.Watched, err = query(ctx, tx, `
			SELECT t.type, t.id, t.season, t.episode, NULL, NULL, NULL, t.plays, t.watched_at FROM watched t
			WHERE t.user_id = ? AND t.type = ? AND t.id = ? AND NOT t.deleted ORDER BY t.season, t.episode`,
			args, scanHistory); err != nil {
			return err
		}
		if t.Plays, err = query(ctx, tx, playsSelect+`
			WHERE t.user_id = ? AND t.type = ? AND t.id = ? ORDER BY t.watched_at DESC, t.rowid DESC`,
			args, scanPlay); err != nil {
			return err
		}
		if t.Progress, err = query(ctx, tx, `
			SELECT t.type, t.id, t.season, t.episode, NULL, NULL, NULL, t.position, t.duration, t.updated_at FROM progress t
			WHERE t.user_id = ? AND t.type = ? AND t.id = ? AND NOT t.deleted ORDER BY t.season, t.episode`,
			args, scanProgress); err != nil {
			return err
		}
		if t.Lists, err = query(ctx, tx, `
			SELECT DISTINCT list FROM list_items WHERE user_id = ? AND type = ? AND id = ? AND NOT deleted ORDER BY list`,
			args, func(rows *sql.Rows) (slug string, err error) { return slug, rows.Scan(&slug) }); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT rating FROM ratings WHERE user_id = ? AND key = ? AND NOT deleted`,
			uid, t.Item.titleKey()).Scan(&t.Rating)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if t.Item.Meta == nil && len(t.Watched)+len(t.Progress)+len(t.Lists) == 0 && t.Rating == 0 {
			return ErrNotFound
		}
		return nil
	})
	return t, err
}

func scanPlay(rows *sql.Rows) (e HistoryEntry, err error) {
	var c itemColumns
	var at int64
	err = rows.Scan(append(c.dest(), append(c.metaDest(), &e.Plays, &at, &e.PlayID)...)...)
	e.Item, e.WatchedAt = c.item(), time.UnixMilli(at)
	return e, err
}

func scanHistory(rows *sql.Rows) (e HistoryEntry, err error) {
	var c itemColumns
	var at int64
	err = rows.Scan(append(c.dest(), append(c.metaDest(), &e.Plays, &at)...)...)
	e.Item, e.WatchedAt = c.item(), time.UnixMilli(at)
	return e, err
}

func scanProgress(rows *sql.Rows) (e ProgressEntry, err error) {
	var c itemColumns
	var position, duration, at int64
	err = rows.Scan(append(c.dest(), append(c.metaDest(), &position, &duration, &at)...)...)
	e.Item, e.UpdatedAt = c.item(), time.UnixMilli(at)
	e.Position, e.Duration = time.Duration(position)*time.Millisecond, time.Duration(duration)*time.Millisecond
	return e, err
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func query[T any](ctx context.Context, db querier, q string, args []any, scan func(*sql.Rows) (T, error)) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
