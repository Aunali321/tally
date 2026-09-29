package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Profile is 0 until the user picks which Nuvio profile to mirror.
type NuvioLink struct {
	UserID        int64
	Backend       string
	APIKey        string
	Email         string
	Profile       int
	ProfileName   string
	Session       NuvioSession
	Cursors       NuvioCursors
	Status        NuvioStatus
	EventsApplied int
}

type NuvioSession struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type NuvioCursors struct {
	Progress int64
	Watched  int64
	Library  int64
}

type NuvioStatus struct {
	SyncedAt       time.Time
	Error          string
	SessionExpired bool
}

// Signing in to the same account again keeps the chosen profile and cursors; another account starts over.
func (s *Store) SaveNuvioLink(ctx context.Context, l NuvioLink) error {
	return inTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nuvio_links WHERE user_id = ? AND (backend != ? OR email != ?)`,
			l.UserID, l.Backend, l.Email); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nuvio_links (user_id, backend, api_key, email, access_token, refresh_token, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET
				api_key = excluded.api_key, access_token = excluded.access_token, refresh_token = excluded.refresh_token,
				expires_at = excluded.expires_at, sync_error = '', session_expired = 0`,
			l.UserID, l.Backend, l.APIKey, l.Email, l.Session.AccessToken, l.Session.RefreshToken, l.Session.ExpiresAt.UnixMilli())
		return err
	})
}

// A different profile restarts the cursors so its whole history is pulled.
func (s *Store) ChooseNuvioProfile(ctx context.Context, uid int64, profile int, name string) error {
	return inTx(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE nuvio_links SET progress_cursor = 0, watched_cursor = 0, library_cursor = 0, events_applied = 0, synced_at = NULL
			WHERE user_id = ? AND profile IS NOT ?`, uid, profile)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE nuvio_links SET profile = ?, profile_name = ? WHERE user_id = ?`, profile, name, uid)
		return changed(res, err, ErrNotFound)
	})
}

func (s *Store) DeleteNuvioLink(ctx context.Context, uid int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nuvio_links WHERE user_id = ?`, uid)
	return changed(res, err, ErrNotFound)
}

const nuvioLinkColumns = `SELECT user_id, backend, api_key, email, profile, profile_name, access_token, refresh_token, expires_at,
	progress_cursor, watched_cursor, library_cursor, events_applied, synced_at, sync_error, session_expired FROM nuvio_links`

func (s *Store) NuvioLink(ctx context.Context, uid int64) (NuvioLink, error) {
	l, err := scanNuvioLink(s.db.QueryRowContext(ctx, nuvioLinkColumns+` WHERE user_id = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

func (s *Store) NuvioLinks(ctx context.Context) ([]NuvioLink, error) {
	rows, err := s.db.QueryContext(ctx, nuvioLinkColumns+` WHERE profile IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []NuvioLink
	for rows.Next() {
		l, err := scanNuvioLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

func scanNuvioLink(row interface{ Scan(...any) error }) (NuvioLink, error) {
	var l NuvioLink
	var expires int64
	var profile, synced sql.NullInt64
	err := row.Scan(&l.UserID, &l.Backend, &l.APIKey, &l.Email, &profile, &l.ProfileName,
		&l.Session.AccessToken, &l.Session.RefreshToken, &expires,
		&l.Cursors.Progress, &l.Cursors.Watched, &l.Cursors.Library, &l.EventsApplied,
		&synced, &l.Status.Error, &l.Status.SessionExpired)
	l.Session.ExpiresAt = time.UnixMilli(expires)
	l.Profile = int(profile.Int64)
	if synced.Valid {
		l.Status.SyncedAt = time.UnixMilli(synced.Int64)
	}
	return l, err
}

func (s *Store) SaveNuvioSession(ctx context.Context, uid int64, session NuvioSession) error {
	_, err := s.db.ExecContext(ctx, `UPDATE nuvio_links SET access_token = ?, refresh_token = ?, expires_at = ? WHERE user_id = ?`,
		session.AccessToken, session.RefreshToken, session.ExpiresAt.UnixMilli(), uid)
	return err
}

func (s *Store) SaveNuvioStatus(ctx context.Context, uid int64, status NuvioStatus) error {
	var synced sql.NullInt64
	if !status.SyncedAt.IsZero() {
		synced = sql.NullInt64{Int64: status.SyncedAt.UnixMilli(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE nuvio_links SET synced_at = ?, sync_error = ?, session_expired = ? WHERE user_id = ?`,
		synced, status.Error, status.SessionExpired, uid)
	return err
}

// Compare-and-set: ErrConflict means another sync already applied these events.
func (b *Batch) AdvanceNuvioCursors(from, to NuvioCursors, applied int) error {
	res, err := b.tx.ExecContext(b.ctx, `
		UPDATE nuvio_links SET progress_cursor = ?, watched_cursor = ?, library_cursor = ?, events_applied = events_applied + ?
		WHERE user_id = ? AND progress_cursor = ? AND watched_cursor = ? AND library_cursor = ?`,
		to.Progress, to.Watched, to.Library, applied, b.uid, from.Progress, from.Watched, from.Library)
	return changed(res, err, ErrConflict)
}
