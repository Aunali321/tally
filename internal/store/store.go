package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrBadCredentials = errors.New("invalid username or password")
	ErrPending        = errors.New("authorization pending")
	ErrConflict       = errors.New("concurrent update")
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, file string) (*Store, error) {
	q := url.Values{
		"_txlock": {"immediate"},
		"_pragma": {"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)"},
	}
	db, err := sql.Open("sqlite", "file:"+file+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", file, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Backup(ctx context.Context, file string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, file)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

// PRAGMA user_version counts the migrations already applied.
func migrate(ctx context.Context, db *sql.DB) error {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	var applied int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&applied); err != nil {
		return err
	}
	if applied > len(files) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", applied, len(files))
	}
	for i, file := range files[applied:] {
		version := applied + i + 1
		if !strings.HasPrefix(path.Base(file), fmt.Sprintf("%04d_", version)) {
			return fmt.Errorf("%s: expected migration number %04d", file, version)
		}
		script, err := migrations.ReadFile(file)
		if err != nil {
			return err
		}
		if err := inTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(script)); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func changed(res sql.Result, err error, none error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return none
	}
	return nil
}
