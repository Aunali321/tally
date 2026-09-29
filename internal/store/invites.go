package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const InviteTTL = 7 * 24 * time.Hour

var (
	ErrInviteInvalid = errors.New("invite used or expired")
	ErrEmailTaken    = errors.New("an account with this email exists")
)

// ID is the code's hash; the code itself is only known when the invite is created.
type Invite struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (s *Store) CreateInvite(ctx context.Context, by int64) (string, error) {
	return createInvite(ctx, s.db, sql.NullInt64{Int64: by, Valid: true})
}

// Replacing earlier setup invites leaves only the most recently logged link working.
func (s *Store) SetupInvite(ctx context.Context) (string, error) {
	var code string
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		var users int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil || users > 0 {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM invites WHERE created_by IS NULL`); err != nil {
			return err
		}
		var err error
		code, err = createInvite(ctx, tx, sql.NullInt64{})
		return err
	})
	return code, err
}

func (s *Store) PendingInvites(ctx context.Context, uid int64) ([]Invite, error) {
	return query(ctx, s.db, `
		SELECT code_hash, created_at, expires_at FROM invites
		WHERE created_by = ? AND used_at IS NULL AND expires_at > ? ORDER BY created_at DESC`,
		[]any{uid, time.Now().UnixMilli()}, func(rows *sql.Rows) (inv Invite, err error) {
			var created, expires int64
			err = rows.Scan(&inv.ID, &created, &expires)
			inv.CreatedAt, inv.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
			return inv, err
		})
}

func (s *Store) RevokeInvite(ctx context.Context, uid int64, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE code_hash = ? AND created_by = ? AND used_at IS NULL`, id, uid)
	return changed(res, err, ErrNotFound)
}

func (s *Store) InviteValid(ctx context.Context, code string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM invites WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?)`,
		hashSecret(code), time.Now().UnixMilli()).Scan(&ok)
	return ok, err
}

func (s *Store) SignUp(ctx context.Context, code, email, password string) (int64, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return 0, err
	}
	if err := ValidatePassword(password); err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	var uid int64
	err = inTx(ctx, s.db, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		res, err := tx.ExecContext(ctx, `UPDATE invites SET used_at = ? WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`,
			now, hashSecret(code), now)
		if err := changed(res, err, ErrInviteInvalid); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = ?)`, email).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrEmailTaken
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO users (email, password_hash) VALUES (?, ?) RETURNING id`, email, hash).Scan(&uid); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE invites SET used_by = ? WHERE code_hash = ?`, uid, hashSecret(code))
		return err
	})
	return uid, err
}

func createInvite(ctx context.Context, db execer, by sql.NullInt64) (string, error) {
	code, now := rand.Text(), time.Now()
	_, err := db.ExecContext(ctx, `INSERT INTO invites (code_hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashSecret(code), by, now.UnixMilli(), now.Add(InviteTTL).UnixMilli())
	return code, err
}
