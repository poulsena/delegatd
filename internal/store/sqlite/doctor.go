package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/poulsena/delegatd/internal/doctor"
	_ "modernc.org/sqlite"
)

// Config is the adapter-owned SQLite diagnosis configuration.
type Config struct {
	Path string `yaml:"path"`
}

// NewDoctorCheck constructs a read-only SQLite database diagnosis.
func NewDoctorCheck(cfg Config, dir string) doctor.Check {
	return doctor.Check{
		ID: "store.sqlite",
		Probe: func(ctx context.Context) (string, *doctor.Failure) {
			return probe(ctx, cfg, dir)
		},
	}
}

func probe(ctx context.Context, cfg Config, dir string) (string, *doctor.Failure) {
	if cfg.Path == "" {
		return "", doctor.NewFailure("path is required", nil)
	}
	path := cfg.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", doctor.NewFailure("SQLite database does not exist", err)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", doctor.NewFailure("SQLite database does not exist", err)
	}
	if err != nil {
		return "", doctor.NewFailure("SQLite database could not be opened read-only", err)
	}
	if !info.Mode().IsRegular() {
		return "", doctor.NewFailure("SQLite path is not a regular file", nil)
	}

	// immutable prevents SQLite's read-only WAL connection from creating -wal
	// or -shm sidecars while the diagnosis only reads stable database metadata.
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) + "?mode=ro&immutable=1&_pragma=query_only(1)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", doctor.NewFailure("SQLite database could not be opened read-only", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return "", doctor.NewFailure("SQLite database could not be opened read-only", err)
	}

	var schemaVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		return "", doctor.NewFailure("SQLite database probe failed", err)
	}
	var version string
	if err := database.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return "", doctor.NewFailure("SQLite database probe failed", err)
	}
	if err := database.Close(); err != nil {
		return "", doctor.NewFailure("SQLite database probe failed", err)
	}
	return "SQLite " + version, nil
}
