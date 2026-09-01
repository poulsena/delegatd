package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/storetest"
)

func TestStoreCreatesAndReopensPendingTaskWithHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	store, err := Open(context.Background(), Config{Path: filepath.Base(path)}, dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	draft := testDraft(t, "task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
	created, err := store.CreateTask(context.Background(), draft)
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if created.ID != draft.ID || created.Status != domain.TaskStatusPending || created.Resource.ID != draft.Resource.CandidateID {
		t.Fatalf("created = %#v", created)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenReadOnly(context.Background(), Config{Path: filepath.Base(path)}, dir)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	got, err := reopened.Task(context.Background(), draft.ID)
	if err != nil {
		t.Fatalf("Task() error = %v", err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Fatalf("reopened = %#v, want %#v", got, created)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	inspect, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer inspect.Close()
	var version int
	if err := inspect.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}
	var history int
	if err := inspect.QueryRow("SELECT COUNT(*) FROM task_history").Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 1 {
		t.Fatalf("history rows = %d, want 1", history)
	}
}

func TestStoreRejectsResourceConflictWithoutPartialTask(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(context.Background(), Config{Path: "state.db"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	first := testDraft(t, "task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
	if _, err := store.CreateTask(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testDraft(t, "task_"+strings.Repeat("C", 26), "resource_"+strings.Repeat("D", 26), "revision-v2")
	second.Resource.ExternalIdentity = "different-identity"
	if _, err := store.CreateTask(context.Background(), second); err == nil || !errors.Is(err, ErrResourceConflict) || err.Error() != "resource conflicts with stored onboarding" {
		t.Fatalf("conflict error = %v", err)
	}
	if _, err := store.Task(context.Background(), second.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("second task lookup error = %v, want not found", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadOnlyLeavesDatabaseUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state #?.db")
	store, err := Open(context.Background(), Config{Path: filepath.Base(path)}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(context.Background(), testDraft(t, "task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeEntries := directoryEntries(t, dir)
	readOnly, err := OpenReadOnly(context.Background(), Config{Path: filepath.Base(path)}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Task(context.Background(), domain.TaskID("task_"+strings.Repeat("A", 26))); err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read-only open changed database bytes")
	}
	if afterEntries := directoryEntries(t, dir); !reflect.DeepEqual(beforeEntries, afterEntries) {
		t.Fatalf("directory entries changed: before=%v after=%v", beforeEntries, afterEntries)
	}
}

func TestOpenReadOnlySeesWriterCommitAfterOpen(t *testing.T) {
	dir := t.TempDir()
	writer, err := Open(context.Background(), Config{Path: "state.db"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	first := testDraft(t, "task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
	if _, err := writer.CreateTask(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(context.Background(), Config{Path: "state.db"}, dir)
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("reader Close() error = %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Errorf("writer Close() error = %v", err)
		}
	})
	second := testDraft(t, "task_"+strings.Repeat("C", 26), "resource_"+strings.Repeat("D", 26), "revision-v2")
	if _, err := writer.CreateTask(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Task(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("reader Task() error = %v", err)
	}
	if got.ID != second.ID || got.Resource.Revision != "revision-v2" {
		t.Fatalf("reader task = %#v", got)
	}
}

func testDraft(t *testing.T, taskID, resourceID, revision string) domain.TaskDraft {
	t.Helper()
	configuration, err := domain.NewSnapshot(domain.RepositoryConfiguration{
		Version:    1,
		Agent:      domain.AgentConfiguration{Instructions: []string{"AGENTS.md"}},
		Policy:     domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}},
		Validation: domain.ValidationConfiguration{Required: []domain.ValidationCommand{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestedPolicy, err := domain.NewSnapshot(domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := domain.NewSnapshot(domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "repair README"})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := domain.NewSnapshot(domain.EffectivePolicy{Version: 1, DefaultAction: domain.PolicyDeny, Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	return domain.TaskDraft{
		ID:     domain.TaskID(taskID),
		Status: domain.TaskStatusPending,
		Resource: domain.ResourceDraft{
			CandidateID:      domain.ResourceID(resourceID),
			Name:             "api-service",
			Kind:             domain.ResourceKindRepository,
			Connector:        "github-main",
			ExternalRef:      "acme/api",
			ExternalIdentity: "123",
			Revision:         revision,
			Configuration:    configuration,
			RequestedPolicy:  requestedPolicy,
		},
		Input:         input,
		Configuration: configuration,
		Policy:        effective,
		CreatedAt:     createdAt,
		InitialHistory: domain.TaskHistoryEntry{
			Sequence:   1,
			Status:     domain.TaskStatusPending,
			OccurredAt: createdAt,
			Reason:     domain.HistoryReasonManualSubmission,
		},
	}
}

func TestTaskStoreContract(t *testing.T) {
	storetest.RunTaskStoreContract(t, func(t *testing.T) storetest.TaskStore {
		dir := t.TempDir()
		store, err := Open(context.Background(), Config{Path: "state.db"}, dir)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		})
		return store
	})
}

func TestOpenRejectsUnrelatedVersionZeroSchemaWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE unrelated (value TEXT)"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeVersion, beforeTables := sqliteSchemaState(t, path)
	if beforeVersion != 0 || !reflect.DeepEqual(beforeTables, []string{"unrelated"}) {
		t.Fatalf("invalid version-zero fixture: version=%d tables=%v", beforeVersion, beforeTables)
	}
	if _, err := Open(context.Background(), Config{Path: filepath.Base(path)}, dir); err == nil {
		t.Fatal("Open() accepted an unrelated version-zero schema")
	}
	afterVersion, afterTables := sqliteSchemaState(t, path)
	if afterVersion != beforeVersion || !reflect.DeepEqual(afterTables, beforeTables) {
		t.Fatalf("schema changed: before version=%d tables=%v, after version=%d tables=%v", beforeVersion, beforeTables, afterVersion, afterTables)
	}
}

func TestOpenRejectsFutureSchemaVersionWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = 99"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeVersion, beforeTables := sqliteSchemaState(t, path)
	if beforeVersion != 99 || len(beforeTables) != 0 {
		t.Fatalf("invalid future-version fixture: version=%d tables=%v", beforeVersion, beforeTables)
	}
	if _, err := Open(context.Background(), Config{Path: filepath.Base(path)}, dir); err == nil {
		t.Fatal("Open() accepted a future schema version")
	}
	afterVersion, afterTables := sqliteSchemaState(t, path)
	if afterVersion != beforeVersion || !reflect.DeepEqual(afterTables, beforeTables) {
		t.Fatalf("schema changed: before version=%d tables=%v, after version=%d tables=%v", beforeVersion, beforeTables, afterVersion, afterTables)
	}
}

func TestOpenRejectsIncompleteCurrentSchemaWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE tasks (id TEXT)"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = 1"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeVersion, beforeTables := sqliteSchemaState(t, path)
	if beforeVersion != 1 || !reflect.DeepEqual(beforeTables, []string{"tasks"}) {
		t.Fatalf("invalid fixture: version=%d tables=%v", beforeVersion, beforeTables)
	}
	if _, err := Open(context.Background(), Config{Path: filepath.Base(path)}, dir); err == nil {
		t.Fatal("Open() accepted an incomplete current schema")
	}
	afterVersion, afterTables := sqliteSchemaState(t, path)
	if afterVersion != beforeVersion || !reflect.DeepEqual(afterTables, beforeTables) {
		t.Fatalf("schema changed: before version=%d tables=%v, after version=%d tables=%v", beforeVersion, beforeTables, afterVersion, afterTables)
	}
}

func sqliteSchemaState(t *testing.T, path string) (int, []string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query("SELECT name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return version, tables
}

func TestOpenReadOnlyEnforcesExistingPrivateDatabase(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenReadOnly(context.Background(), Config{Path: "missing.db"}, dir); err == nil {
		t.Fatal("OpenReadOnly() accepted a missing database")
	}
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(context.Background(), Config{Path: filepath.Base(directory)}, dir); err == nil {
		t.Fatal("OpenReadOnly() accepted a directory")
	}
	path := filepath.Join(dir, "state.db")
	writer, err := Open(context.Background(), Config{Path: filepath.Base(path)}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenReadOnly(context.Background(), Config{Path: filepath.Base(path)}, dir); err == nil {
			t.Fatal("OpenReadOnly() accepted broad database permissions")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := OpenReadOnly(context.Background(), Config{Path: filepath.Base(path)}, dir)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadOnlyRejectsFutureSchemaAndCancellation(t *testing.T) {
	dir := t.TempDir()
	futurePath := filepath.Join(dir, "future.db")
	database, err := sql.Open("sqlite", futurePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = 99"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(futurePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(context.Background(), Config{Path: filepath.Base(futurePath)}, dir); err == nil {
		t.Fatal("OpenReadOnly() accepted a future schema")
	}

	validPath := filepath.Join(dir, "valid.db")
	writer, err := Open(context.Background(), Config{Path: filepath.Base(validPath)}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenReadOnly(cancelled, Config{Path: filepath.Base(validPath)}, dir); err == nil {
		t.Fatal("OpenReadOnly() accepted a cancelled context")
	}
}
