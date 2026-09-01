package storetest

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
)

// TaskStore is the application-owned persistence contract exercised by each
// store implementation.
type TaskStore interface {
	CreateTask(context.Context, domain.TaskDraft) (domain.Task, error)
	Task(context.Context, domain.TaskID) (domain.Task, error)
}

// Factory creates a fresh store for one contract subtest. The factory owns
// cleanup registration for resources that need closing.
type Factory func(*testing.T) TaskStore

// RunTaskStoreContract runs the shared behavioral contract for a task store.
func RunTaskStoreContract(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("round trip", func(t *testing.T) {
		store := factory(t)
		draft := testDraft("task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
		created, err := store.CreateTask(context.Background(), draft)
		if err != nil {
			t.Fatalf("CreateTask() error = %v", err)
		}
		got, err := store.Task(context.Background(), draft.ID)
		if err != nil {
			t.Fatalf("Task() error = %v", err)
		}
		if !reflect.DeepEqual(got, created) {
			t.Fatalf("Task() = %#v, want %#v", got, created)
		}
		if got.Configuration.String() != draft.Configuration.String() || got.Input.String() != draft.Input.String() || got.Policy.String() != draft.Policy.String() {
			t.Fatalf("stored snapshots changed: %#v", got)
		}
	})

	t.Run("missing task", func(t *testing.T) {
		store := factory(t)
		_, err := store.Task(context.Background(), domain.TaskID("task_"+strings.Repeat("Z", 26)))
		if err == nil || err.Error() != "task not found" {
			t.Fatalf("Task() error = %v, want task not found", err)
		}
	})

	t.Run("duplicate preserves original", func(t *testing.T) {
		store := factory(t)
		first := testDraft("task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
		if _, err := store.CreateTask(context.Background(), first); err != nil {
			t.Fatalf("first CreateTask() error = %v", err)
		}
		duplicate := testDraft(string(first.ID), string(first.Resource.CandidateID), "revision-v2")
		if _, err := store.CreateTask(context.Background(), duplicate); err == nil {
			t.Fatal("duplicate CreateTask() error = nil")
		}
		got, err := store.Task(context.Background(), first.ID)
		if err != nil {
			t.Fatalf("Task() after duplicate error = %v", err)
		}
		if got.Resource.Revision != "revision-v1" {
			t.Fatalf("revision after duplicate = %q, want revision-v1", got.Resource.Revision)
		}
	})

	t.Run("identity conflict across aliases", func(t *testing.T) {
		store := factory(t)
		first := testDraft("task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
		if _, err := store.CreateTask(context.Background(), first); err != nil {
			t.Fatalf("first CreateTask() error = %v", err)
		}
		second := testDraft("task_"+strings.Repeat("C", 26), "resource_"+strings.Repeat("D", 26), "revision-v2")
		second.Resource.Name = "other-service"
		if _, err := store.CreateTask(context.Background(), second); err == nil || err.Error() != "resource conflicts with stored onboarding" {
			t.Fatalf("cross-alias CreateTask() error = %v, want resource conflict", err)
		}
		if _, err := store.Task(context.Background(), second.ID); err == nil || err.Error() != "task not found" {
			t.Fatalf("conflicting task lookup error = %v, want task not found", err)
		}
	})
}

func testDraft(taskID, resourceID, revision string) domain.TaskDraft {
	configuration, _ := domain.NewSnapshot(domain.RepositoryConfiguration{
		Version:    1,
		Agent:      domain.AgentConfiguration{Instructions: []string{"AGENTS.md"}},
		Policy:     domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}},
		Validation: domain.ValidationConfiguration{Required: []domain.ValidationCommand{}},
	})
	requestedPolicy, _ := domain.NewSnapshot(domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}})
	input, _ := domain.NewSnapshot(domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "repair README"})
	policy, _ := domain.NewSnapshot(domain.EffectivePolicy{Version: 1, DefaultAction: domain.PolicyDeny, Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}})
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
		Policy:        policy,
		CreatedAt:     createdAt,
		InitialHistory: domain.TaskHistoryEntry{
			Sequence:   1,
			Status:     domain.TaskStatusPending,
			OccurredAt: createdAt,
			Reason:     domain.HistoryReasonManualSubmission,
		},
	}
}
