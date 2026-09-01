package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/storetest"
)

func TestStoreRoundTripsAndCopiesTaskSnapshots(t *testing.T) {
	store := New()
	draft := memoryDraft(t)
	created, err := store.CreateTask(context.Background(), draft)
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	changed := draft.Input.Bytes()
	changed[0] = '['
	got, err := store.Task(context.Background(), draft.ID)
	if err != nil {
		t.Fatalf("Task() error = %v", err)
	}
	if got.Input.String() != draft.Input.String() || got.Configuration.String() != draft.Configuration.String() || created.Resource.ID != draft.Resource.CandidateID {
		t.Fatalf("task = %#v", got)
	}
	if len(store.History(draft.ID)) != 1 {
		t.Fatalf("history = %#v", store.History(draft.ID))
	}
}

func TestStoreMapsMissingAndConflictingTasks(t *testing.T) {
	store := New()
	draft := memoryDraft(t)
	if _, err := store.Task(context.Background(), draft.ID); err == nil || !errors.Is(err, ErrTaskNotFound) || err.Error() != "task not found" {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := store.CreateTask(context.Background(), draft); err != nil {
		t.Fatal(err)
	}
	conflict := memoryDraft(t)
	conflict.ID = domain.TaskID("task_" + strings.Repeat("C", 26))
	conflict.Resource.CandidateID = domain.ResourceID("resource_" + strings.Repeat("D", 26))
	conflict.Resource.ExternalIdentity = "different"
	if _, err := store.CreateTask(context.Background(), conflict); err == nil || !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func memoryDraft(t *testing.T) domain.TaskDraft {
	t.Helper()
	return testDraft(t, "task_"+strings.Repeat("A", 26), "resource_"+strings.Repeat("B", 26), "revision-v1")
}

func testDraft(t *testing.T, taskID, resourceID, revision string) domain.TaskDraft {
	t.Helper()
	configuration, err := domain.NewSnapshot(domain.RepositoryConfiguration{Version: 1, Agent: domain.AgentConfiguration{Instructions: []string{"AGENTS.md"}}, Policy: domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}}, Validation: domain.ValidationConfiguration{Required: []domain.ValidationCommand{}}})
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
	policy, err := domain.NewSnapshot(domain.EffectivePolicy{Version: 1, DefaultAction: domain.PolicyDeny, Actions: map[string]domain.PolicyDecision{}, ProtectedPaths: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := fixedTime()
	return domain.TaskDraft{ID: domain.TaskID(taskID), Status: domain.TaskStatusPending, Resource: domain.ResourceDraft{CandidateID: domain.ResourceID(resourceID), Name: "api-service", Kind: domain.ResourceKindRepository, Connector: "github-main", ExternalRef: "acme/api", ExternalIdentity: "123", Revision: revision, Configuration: configuration, RequestedPolicy: requestedPolicy}, Input: input, Configuration: configuration, Policy: policy, CreatedAt: createdAt, InitialHistory: domain.TaskHistoryEntry{Sequence: 1, Status: domain.TaskStatusPending, OccurredAt: createdAt, Reason: domain.HistoryReasonManualSubmission}}
}

func fixedTime() time.Time {
	return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
}

func TestTaskStoreContract(t *testing.T) {
	storetest.RunTaskStoreContract(t, func(*testing.T) storetest.TaskStore {
		return New()
	})
}

func TestStoreRejectsCancelledAndInvalidDrafts(t *testing.T) {
	draft := memoryDraft(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().CreateTask(cancelled, draft); err == nil || err.Error() != "task could not be submitted" {
		t.Fatalf("cancelled CreateTask() error = %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*domain.TaskDraft)
	}{
		{name: "invalid task ID", mutate: func(value *domain.TaskDraft) { value.ID = "invalid" }},
		{name: "invalid status", mutate: func(value *domain.TaskDraft) { value.Status = "running" }},
		{name: "non-UTC creation", mutate: func(value *domain.TaskDraft) { value.CreatedAt = value.CreatedAt.In(time.FixedZone("local", 3600)) }},
		{name: "invalid resource ID", mutate: func(value *domain.TaskDraft) { value.Resource.CandidateID = "invalid" }},
		{name: "incomplete resource", mutate: func(value *domain.TaskDraft) { value.Resource.Name = "" }},
		{name: "different configuration", mutate: func(value *domain.TaskDraft) { value.Configuration = value.Resource.RequestedPolicy }},
		{name: "invalid snapshot", mutate: func(value *domain.TaskDraft) { value.Policy = domain.Snapshot{} }},
		{name: "invalid history", mutate: func(value *domain.TaskDraft) { value.InitialHistory.Sequence = 2 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value := memoryDraft(t)
			testCase.mutate(&value)
			if _, err := New().CreateTask(context.Background(), value); err == nil {
				t.Fatal("invalid draft was accepted")
			}
		})
	}
}
