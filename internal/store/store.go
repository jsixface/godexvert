// Package store persists the video library and job history in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jsixface/godexvert/internal/media"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies pending migrations.
// Use ":memory:" for an in-memory database.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serialises writers and keeps an in-memory database alive.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, ?)`, version, time.Now().Unix())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Stamp identifies a stored file and its last seen modification time.
type Stamp struct {
	ID       int64
	Modified int64
}

// Stamps returns path -> stamp for every stored file.
func (s *Store) Stamps(ctx context.Context) (map[string]Stamp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, modified FROM video_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Stamp{}
	for rows.Next() {
		var st Stamp
		var p string
		if err := rows.Scan(&st.ID, &p, &st.Modified); err != nil {
			return nil, err
		}
		out[p] = st
	}
	return out, rows.Err()
}

// Files returns every stored file with its tracks, ordered by name.
func (s *Store) Files(ctx context.Context) ([]media.File, error) {
	return s.files(ctx, "")
}

// File returns the file stored at path, or nil if there is none.
func (s *Store) File(ctx context.Context, path string) (*media.File, error) {
	found, err := s.files(ctx, "WHERE path = ?", path)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

const fileCols = `id, path, name, size_mb, modified, added`
const trackCols = `video_file_id, kind, idx, codec, codec_tag, profile, resolution, aspect_ratio, frame_rate,
	pixel_format, bit_rate, channels, layout, sample_rate, language`

func (s *Store) files(ctx context.Context, where string, args ...any) ([]media.File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fileCols+` FROM video_files `+where+` ORDER BY name COLLATE NOCASE, path`, args...)
	if err != nil {
		return nil, err
	}
	var files []media.File
	byID := map[int64]int{}
	for rows.Next() {
		var f media.File
		if err := rows.Scan(&f.ID, &f.Path, &f.Name, &f.SizeMB, &f.Modified, &f.Added); err != nil {
			rows.Close()
			return nil, err
		}
		byID[f.ID] = len(files)
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(files) == 0 {
		return files, err
	}

	q := `SELECT ` + trackCols + ` FROM tracks`
	var targs []any
	if where != "" {
		q += ` WHERE video_file_id = ?`
		targs = append(targs, files[0].ID)
	}
	trows, err := s.db.QueryContext(ctx, q+` ORDER BY video_file_id, idx`, targs...)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var id int64
		var t media.Track
		if err := trows.Scan(&id, &t.Kind, &t.Index, &t.Codec, &t.CodecTag, &t.Profile, &t.Resolution, &t.AspectRatio,
			&t.FrameRate, &t.PixelFormat, &t.BitRate, &t.Channels, &t.Layout, &t.SampleRate, &t.Language); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			files[i].Tracks = append(files[i].Tracks, t)
		}
	}
	return files, trows.Err()
}

// Upsert inserts or replaces the file at f.Path together with its tracks.
// f.Added is only used for new rows.
func (s *Store) Upsert(ctx context.Context, f media.File) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO video_files (path, name, size_mb, modified, added) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET name = excluded.name, size_mb = excluded.size_mb, modified = excluded.modified
			RETURNING id`, f.Path, f.Name, f.SizeMB, f.Modified, f.Added).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tracks WHERE video_file_id = ?`, id); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO tracks (`+trackCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, t := range f.Tracks {
			if _, err := stmt.ExecContext(ctx, id, t.Kind, t.Index, t.Codec, t.CodecTag, t.Profile, t.Resolution, t.AspectRatio,
				t.FrameRate, t.PixelFormat, t.BitRate, t.Channels, t.Layout, t.SampleRate, t.Language); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete removes the files with the given ids (tracks cascade).
func (s *Store) Delete(ctx context.Context, ids ...int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM video_files WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// JobRecord is a finished conversion job.
type JobRecord struct {
	JobID     string
	Status    string
	FilePath  string
	FileName  string
	StartedAt time.Time
	Duration  time.Duration
	Error     string
}

func (s *Store) SaveJob(ctx context.Context, j JobRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO completed_jobs (job_id, status, file_path, file_name, started_at, duration_ms, error)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.JobID, j.Status, j.FilePath, j.FileName, j.StartedAt.Unix(), j.Duration.Milliseconds(), j.Error)
	return err
}

// Jobs returns finished jobs, newest first.
func (s *Store) Jobs(ctx context.Context, offset, limit int) ([]JobRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, status, file_path, file_name, started_at, duration_ms, error
		FROM completed_jobs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRecord
	for rows.Next() {
		var j JobRecord
		var started, ms int64
		if err := rows.Scan(&j.JobID, &j.Status, &j.FilePath, &j.FileName, &started, &ms, &j.Error); err != nil {
			return nil, err
		}
		j.StartedAt = time.Unix(started, 0)
		j.Duration = time.Duration(ms) * time.Millisecond
		out = append(out, j)
	}
	return out, rows.Err()
}

// CountJobsByStatus returns the number of finished jobs per status.
func (s *Store) CountJobsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM completed_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

func (s *Store) CountJobs(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM completed_jobs`).Scan(&n)
	return n, err
}
