package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Players report completion repeatedly (92%, 95%, 100%) for one viewing.
const rescrobbleWindow = time.Hour

type Batch struct {
	ctx context.Context
	tx  *sql.Tx
	uid int64
	rev int64
}

func (s *Store) Write(ctx context.Context, uid int64, fn func(*Batch) error) error {
	return inTx(ctx, s.db, func(tx *sql.Tx) error {
		b := &Batch{ctx: ctx, tx: tx, uid: uid}
		if err := tx.QueryRowContext(ctx, `UPDATE users SET rev = rev + 1 WHERE id = ? RETURNING rev`, uid).Scan(&b.rev); err != nil {
			return err
		}
		return fn(b)
	})
}

// Positions older than the last completion are stale, so updates can arrive in any order.
func (b *Batch) Progress(it Item, position, duration int64, at time.Time) error {
	if err := b.meta(it); err != nil {
		return err
	}
	if position*10 >= duration*9 {
		if err := b.clearProgress(it, at); err != nil {
			return err
		}
		return b.scrobble(it, at)
	}
	return b.exec(`
		INSERT INTO progress (user_id, key, type, id, season, episode, position, duration, updated_at, rev)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM watched WHERE user_id = ? AND key = ? AND NOT deleted AND watched_at >= ?)
		ON CONFLICT (user_id, key) DO UPDATE SET
			position = excluded.position, duration = excluded.duration, updated_at = excluded.updated_at,
			rev = excluded.rev, deleted = 0
		WHERE progress.deleted OR excluded.updated_at >= progress.updated_at`,
		append(b.itemArgs(it), position, duration, at.UnixMilli(), b.rev, b.uid, it.Key(), at.UnixMilli())...)
}

func (b *Batch) DeleteProgress(it Item) error {
	return b.tombstone("progress", it)
}

func (b *Batch) Watch(it Item, at time.Time) error {
	if err := b.meta(it); err != nil {
		return err
	}
	return b.watch(it, at)
}

// Topping up to n instead of adding makes re-running an import, or overlapping sources, add nothing twice.
func (b *Batch) AddPlays(it Item, at time.Time, n int) error {
	var have int
	err := b.tx.QueryRowContext(b.ctx, `SELECT count(*) FROM plays WHERE user_id = ? AND key = ? AND watched_at = ?`,
		b.uid, it.Key(), at.UnixMilli()).Scan(&have)
	if err != nil {
		return err
	}
	for range n - have {
		if err := b.Watch(it, at); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batch) Scrobble(it Item, at time.Time) error {
	if err := b.meta(it); err != nil {
		return err
	}
	return b.scrobble(it, at)
}

func (b *Batch) Unwatch(it Item) error {
	if err := b.exec(`DELETE FROM plays WHERE user_id = ? AND key = ?`, b.uid, it.Key()); err != nil {
		return err
	}
	return b.tombstone("watched", it)
}

func (b *Batch) RemovePlay(playID int64) error {
	var key string
	err := b.tx.QueryRowContext(b.ctx, `DELETE FROM plays WHERE rowid = ? AND user_id = ? RETURNING key`, playID, b.uid).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return b.exec(`
		UPDATE watched SET
			plays = max(plays - 1, 1),
			watched_at = coalesce((SELECT max(watched_at) FROM plays WHERE user_id = watched.user_id AND key = watched.key), watched_at),
			deleted = plays = 1,
			rev = ?
		WHERE user_id = ? AND key = ? AND NOT deleted`,
		b.rev, b.uid, key)
}

func (b *Batch) Rate(it Item, rating int, at time.Time) error {
	if err := b.meta(it); err != nil {
		return err
	}
	return b.exec(`
		INSERT INTO ratings (user_id, key, type, id, season, episode, rating, rated_at, rev)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET
			rating = excluded.rating, rated_at = excluded.rated_at, rev = excluded.rev, deleted = 0`,
		append(b.itemArgs(it), rating, at.UnixMilli(), b.rev)...)
}

func (b *Batch) Unrate(it Item) error {
	return b.tombstone("ratings", it)
}

func (b *Batch) PutList(slug, name string) error {
	return b.exec(`
		INSERT INTO lists (user_id, slug, name, rev) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, slug) DO UPDATE SET name = excluded.name, rev = excluded.rev, deleted = 0`,
		b.uid, slug, name, b.rev)
}

func (b *Batch) DeleteList(slug string) error {
	if err := b.exec(`UPDATE lists SET deleted = 1, rev = ? WHERE user_id = ? AND slug = ? AND NOT deleted`, b.rev, b.uid, slug); err != nil {
		return err
	}
	return b.exec(`UPDATE list_items SET deleted = 1, rev = ? WHERE user_id = ? AND list = ? AND NOT deleted`, b.rev, b.uid, slug)
}

func (b *Batch) AddToList(slug string, it Item, at time.Time) error {
	if err := b.meta(it); err != nil {
		return err
	}
	err := b.exec(`
		INSERT INTO lists (user_id, slug, name, rev) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, slug) DO UPDATE SET name = excluded.name, rev = excluded.rev, deleted = 0
		WHERE lists.deleted`,
		b.uid, slug, nameFor(slug), b.rev)
	if err != nil {
		return err
	}
	return b.exec(`
		INSERT INTO list_items (list, user_id, key, type, id, season, episode, added_at, rev)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, list, key) DO UPDATE SET added_at = excluded.added_at, rev = excluded.rev, deleted = 0
		WHERE list_items.deleted`,
		append(append([]any{slug}, b.itemArgs(it)...), at.UnixMilli(), b.rev)...)
}

func (b *Batch) RemoveFromList(slug string, it Item) error {
	return b.exec(`UPDATE list_items SET deleted = 1, rev = ? WHERE user_id = ? AND list = ? AND key = ? AND NOT deleted`,
		b.rev, b.uid, slug, it.Key())
}

// Synced rows are tombstoned rather than removed, so apps that already have them delete them too.
func (b *Batch) EraseHistory() error {
	for _, table := range []string{"watched", "progress", "ratings", "lists", "list_items", "meta"} {
		if err := b.exec(`UPDATE `+table+` SET deleted = 1, rev = ? WHERE user_id = ? AND NOT deleted`, b.rev, b.uid); err != nil {
			return err
		}
	}
	for _, table := range []string{"plays", "imports", "nuvio_links"} {
		if err := b.exec(`DELETE FROM `+table+` WHERE user_id = ?`, b.uid); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batch) scrobble(it Item, at time.Time) error {
	var counted bool
	err := b.tx.QueryRowContext(b.ctx,
		`SELECT EXISTS (SELECT 1 FROM watched WHERE user_id = ? AND key = ? AND NOT deleted AND abs(watched_at - ?) < ?)`,
		b.uid, it.Key(), at.UnixMilli(), rescrobbleWindow.Milliseconds()).Scan(&counted)
	if err != nil || counted {
		return err
	}
	return b.watch(it, at)
}

func (b *Batch) watch(it Item, at time.Time) error {
	err := b.exec(`
		INSERT INTO watched (user_id, key, type, id, season, episode, plays, watched_at, rev)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET
			plays = CASE WHEN deleted THEN 1 ELSE plays + 1 END,
			watched_at = CASE WHEN deleted THEN excluded.watched_at ELSE max(watched_at, excluded.watched_at) END,
			rev = excluded.rev, deleted = 0`,
		append(b.itemArgs(it), at.UnixMilli(), b.rev)...)
	if err != nil {
		return err
	}
	err = b.exec(`INSERT INTO plays (user_id, key, type, id, season, episode, watched_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		append(b.itemArgs(it), at.UnixMilli())...)
	if err != nil {
		return err
	}
	return b.clearProgress(it, at)
}

// Progress after at belongs to a rewatch.
func (b *Batch) clearProgress(it Item, at time.Time) error {
	return b.exec(`UPDATE progress SET deleted = 1, rev = ? WHERE user_id = ? AND key = ? AND NOT deleted AND updated_at <= ?`,
		b.rev, b.uid, it.Key(), at.UnixMilli())
}

func (b *Batch) meta(it Item) error {
	m := it.Meta
	if m == nil || *m == (Meta{}) {
		return nil
	}
	return b.exec(`
		INSERT INTO meta (user_id, key, type, id, title, year, poster, rev) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET
			title = coalesce(nullif(excluded.title, ''), title),
			year = coalesce(nullif(excluded.year, 0), year),
			poster = coalesce(nullif(excluded.poster, ''), poster),
			rev = excluded.rev, deleted = 0
		WHERE meta.deleted
			OR excluded.title NOT IN ('', meta.title)
			OR excluded.year NOT IN (0, meta.year)
			OR excluded.poster NOT IN ('', meta.poster)`,
		b.uid, it.titleKey(), it.Type, it.ID, m.Title, m.Year, m.Poster, b.rev)
}

func (b *Batch) tombstone(table string, it Item) error {
	return b.exec(`UPDATE `+table+` SET deleted = 1, rev = ? WHERE user_id = ? AND key = ? AND NOT deleted`,
		b.rev, b.uid, it.Key())
}

func (b *Batch) itemArgs(it Item) []any {
	return []any{b.uid, it.Key(), it.Type, it.ID, it.Season, it.Episode}
}

func (b *Batch) exec(query string, args ...any) error {
	_, err := b.tx.ExecContext(b.ctx, query, args...)
	return err
}
