package store

import (
	"context"
	"database/sql"
	"time"
)

type ImportRun struct {
	ID         int64
	Source     string
	Label      string
	StartedAt  time.Time
	FinishedAt time.Time
	Section    string
	Error      string
	Sections   []ImportSection
}

type ImportSection struct {
	Name     string
	Imported int
	NoID     int
	Skipped  int
}

func (r ImportRun) Running() bool {
	return r.FinishedAt.IsZero()
}

func (s *Store) StartImport(ctx context.Context, uid int64, source, label string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO imports (user_id, source, label, started_at) VALUES (?, ?, ?, ?) RETURNING id`,
		uid, source, label, time.Now().UnixMilli()).Scan(&id)
	return id, err
}

func (s *Store) ImportProgress(ctx context.Context, id int64, section string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE imports SET section = ? WHERE id = ?`, section, id)
	return err
}

func (s *Store) AddImportSection(ctx context.Context, id int64, sec ImportSection) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO import_sections (import_id, position, section, imported, no_id, skipped)
		VALUES (?, (SELECT count(*) FROM import_sections WHERE import_id = ?), ?, ?, ?, ?)`,
		id, id, sec.Name, sec.Imported, sec.NoID, sec.Skipped)
	return err
}

func (s *Store) FinishImport(ctx context.Context, id int64, failure string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE imports SET finished_at = ?, section = '', error = ? WHERE id = ?`,
		time.Now().UnixMilli(), failure, id)
	return err
}

func (s *Store) InterruptImports(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE imports SET finished_at = ?, section = '', error = 'Interrupted by a server restart. Run it again.' WHERE finished_at IS NULL`,
		time.Now().UnixMilli())
	return err
}

func (s *Store) LatestImports(ctx context.Context, uid int64) (map[string]ImportRun, error) {
	runs, err := query(ctx, s.db, `
		SELECT id, source, label, started_at, finished_at, section, error FROM imports i
		WHERE user_id = ? AND started_at = (SELECT max(started_at) FROM imports WHERE user_id = i.user_id AND source = i.source)`,
		[]any{uid}, func(rows *sql.Rows) (r ImportRun, err error) {
			var started int64
			var finished sql.NullInt64
			err = rows.Scan(&r.ID, &r.Source, &r.Label, &started, &finished, &r.Section, &r.Error)
			r.StartedAt = time.UnixMilli(started)
			if finished.Valid {
				r.FinishedAt = time.UnixMilli(finished.Int64)
			}
			return r, err
		})
	if err != nil {
		return nil, err
	}
	latest := make(map[string]ImportRun, len(runs))
	for _, r := range runs {
		if r.Sections, err = query(ctx, s.db, `SELECT section, imported, no_id, skipped FROM import_sections WHERE import_id = ? ORDER BY position`,
			[]any{r.ID}, func(rows *sql.Rows) (sec ImportSection, err error) {
				return sec, rows.Scan(&sec.Name, &sec.Imported, &sec.NoID, &sec.Skipped)
			}); err != nil {
			return nil, err
		}
		latest[r.Source] = r
	}
	return latest, nil
}
