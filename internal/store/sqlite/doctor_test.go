package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDoctorCheckOpensRelativeDatabaseReadOnly(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "state #%?.db")
	createDatabase(t, databasePath)
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeEntries := directoryEntries(t, dir)
	beforeSchema := schemaVersion(t, databasePath)

	check := NewDoctorCheck(Config{Path: filepath.Base(databasePath)}, dir)
	detail, failure := check.Probe(context.Background())
	if failure != nil {
		t.Fatalf("failure = %v", failure)
	}
	if len(detail) <= len("SQLite ") || detail[:len("SQLite ")] != "SQLite " {
		t.Fatalf("detail = %q", detail)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("database bytes changed during read-only diagnosis")
	}
	if got := schemaVersion(t, databasePath); got != beforeSchema {
		t.Fatalf("schema version changed from %d to %d", beforeSchema, got)
	}
	afterEntries := directoryEntries(t, dir)
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("directory entries changed: before=%v after=%v", beforeEntries, afterEntries)
	}
	for name := range beforeEntries {
		if !gotEntry(afterEntries, name) {
			t.Fatalf("directory entry %q disappeared", name)
		}
	}
}

func TestDoctorCheckMapsSQLitePathAndProbeFailures(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		cfg  Config
		prep func(t *testing.T)
		want []string
	}{
		{name: "required", cfg: Config{}, want: []string{"path is required"}},
		{name: "missing", cfg: Config{Path: "missing.db"}, want: []string{"SQLite database does not exist"}},
		{name: "directory", cfg: Config{Path: "."}, want: []string{"SQLite path is not a regular file"}},
		{name: "invalid database", cfg: Config{Path: "invalid.db"}, prep: func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "invalid.db"), []byte("not sqlite"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: []string{"SQLite database could not be opened read-only", "SQLite database probe failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prep != nil {
				tc.prep(t)
			}
			check := NewDoctorCheck(tc.cfg, dir)
			_, failure := check.Probe(context.Background())
			if failure == nil {
				t.Fatal("failure = nil")
			}
			matched := false
			for _, want := range tc.want {
				if failure.Error() == want {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("failure = %q, want one of %v", failure.Error(), tc.want)
			}
		})
	}
}

func createDatabase(t *testing.T, path string) {
	t.Helper()
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) + "?mode=rwc"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE sample (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO sample(value) VALUES ('unchanged')"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
func schemaVersion(t *testing.T, path string) int {
	t.Helper()
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) + "?mode=ro"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRow("PRAGMA schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func directoryEntries(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		result[entry.Name()] = struct{}{}
	}
	return result
}

func gotEntry(entries map[string]struct{}, name string) bool {
	_, ok := entries[name]
	return ok
}
