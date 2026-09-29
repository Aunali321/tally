package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	DeviceTTL          = 10 * time.Minute
	DevicePollInterval = 5 * time.Second

	userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	userCodeLength   = 8

	MinPasswordLength = 8
	// Session last-used times are refreshed at most this often, so authenticated reads rarely write.
	touchInterval = time.Hour
)

// Compared against when the account does not exist, so login timing does not reveal which emails have accounts.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("tally"), bcrypt.DefaultCost)

type Device struct {
	Code     string
	UserCode string
}

// ID is the token's hash, safe to show because it cannot be turned back into the token.
type Session struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	LastUsedAt time.Time
}

func (s *Store) CreateUser(ctx context.Context, email, password string) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO users (email, password_hash) VALUES (?, ?)`, email, hash)
	return err
}

func NormalizeEmail(email string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || addr.Name != "" {
		return "", fmt.Errorf("%q is not an email address", email)
	}
	return strings.ToLower(addr.Address), nil
}

func (s *Store) SetPassword(ctx context.Context, email, password string) error {
	uid, err := s.UserID(ctx, email)
	if err != nil {
		return err
	}
	return s.replacePassword(ctx, uid, password)
}

func (s *Store) ChangePassword(ctx context.Context, uid int64, current, next string) error {
	if err := s.VerifyPassword(ctx, uid, current); err != nil {
		return err
	}
	if err := ValidatePassword(next); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, uid)
	return err
}

func (s *Store) VerifyPassword(ctx context.Context, uid int64, password string) error {
	var hash []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, uid).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return ErrBadCredentials
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, uid int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid)
	return changed(res, err, ErrNotFound)
}

func ValidatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	return nil
}

func (s *Store) RevokeTokens(ctx context.Context, uid int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE user_id = ?`, uid)
	return err
}

func (s *Store) replacePassword(ctx context.Context, uid int64, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return inTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, uid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE user_id = ?`, uid)
		return err
	})
}

func (s *Store) UserID(ctx context.Context, email string) (int64, error) {
	var uid int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("user %q: %w", email, ErrNotFound)
	}
	return uid, err
}

func (s *Store) Email(ctx context.Context, uid int64) (string, error) {
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, uid).Scan(&email)
	return email, err
}

func (s *Store) Authenticate(ctx context.Context, email, password string) (int64, error) {
	var uid int64
	var hash []byte
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).Scan(&uid, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return 0, ErrBadCredentials
	}
	if err != nil {
		return 0, err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return 0, ErrBadCredentials
	}
	return uid, nil
}

func (s *Store) IssueToken(ctx context.Context, uid int64, name string) (string, error) {
	return issueToken(ctx, s.db, uid, name)
}

func (s *Store) TokenUser(ctx context.Context, token string) (uid int64, session string, err error) {
	session, now := hashSecret(token), time.Now().UnixMilli()
	err = s.db.QueryRowContext(ctx, `SELECT user_id FROM tokens WHERE hash = ?`, session).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE hash = ? AND last_used_at < ?`,
		now, session, now-touchInterval.Milliseconds())
	return uid, session, err
}

func (s *Store) RevokeToken(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE hash = ?`, hashSecret(token))
	return err
}

func (s *Store) Sessions(ctx context.Context, uid int64) ([]Session, error) {
	return query(ctx, s.db, `SELECT hash, name, created_at, last_used_at FROM tokens WHERE user_id = ? ORDER BY last_used_at DESC`,
		[]any{uid}, func(rows *sql.Rows) (ss Session, err error) {
			var created, used int64
			err = rows.Scan(&ss.ID, &ss.Name, &created, &used)
			ss.CreatedAt, ss.LastUsedAt = time.UnixMilli(created), time.UnixMilli(used)
			return ss, err
		})
}

func (s *Store) RevokeSession(ctx context.Context, uid int64, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE hash = ? AND user_id = ?`, id, uid)
	return changed(res, err, ErrNotFound)
}

func (s *Store) StartDevice(ctx context.Context, name string) (Device, error) {
	d := Device{Code: rand.Text(), UserCode: randomUserCode()}
	now := time.Now()
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO devices (code_hash, user_code, name, expires_at) VALUES (?, ?, ?, ?)`,
			hashSecret(d.Code), d.UserCode, name, now.Add(DeviceTTL).UnixMilli())
		return err
	})
	return d, err
}

func (s *Store) ApproveDevice(ctx context.Context, uid int64, userCode string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET user_id = ? WHERE user_code = ? AND user_id IS NULL AND expires_at > ?`,
		uid, NormalizeUserCode(userCode), time.Now().UnixMilli())
	return changed(res, err, ErrNotFound)
}

func (s *Store) ClaimDevice(ctx context.Context, code string) (string, error) {
	var token string
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		var uid sql.NullInt64
		var name string
		var expires int64
		err := tx.QueryRowContext(ctx, `SELECT user_id, name, expires_at FROM devices WHERE code_hash = ?`, hashSecret(code)).Scan(&uid, &name, &expires)
		switch {
		case errors.Is(err, sql.ErrNoRows), err == nil && expires <= time.Now().UnixMilli():
			return ErrNotFound
		case err != nil:
			return err
		case !uid.Valid:
			return ErrPending
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE code_hash = ?`, hashSecret(code)); err != nil {
			return err
		}
		token, err = issueToken(ctx, tx, uid.Int64, name)
		return err
	})
	return token, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func issueToken(ctx context.Context, db execer, uid int64, name string) (string, error) {
	token, now := rand.Text(), time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `INSERT INTO tokens (hash, user_id, name, created_at, last_used_at) VALUES (?, ?, ?, ?, ?)`,
		hashSecret(token), uid, name, now, now)
	return token, err
}

func FormatUserCode(code string) string {
	return code[:userCodeLength/2] + "-" + code[userCodeLength/2:]
}

func NormalizeUserCode(s string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
}

func randomUserCode() string {
	b := make([]byte, userCodeLength)
	rand.Read(b)
	for i, v := range b {
		b[i] = userCodeAlphabet[int(v)%len(userCodeAlphabet)]
	}
	return string(b)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
