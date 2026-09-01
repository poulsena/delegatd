package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
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
