package control

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
)

func TestSubmitManualRepositoryCapturesImmutableSnapshots(t *testing.T) {
	store := &recordingTaskStore{}
	calls := 0
	source := repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) {
		calls++
		return domain.RepositoryMaterial{
			ExternalRef:      "acme/api",
			ExternalIdentity: "123",
			Revision:         "revision-v1",
			Configuration: domain.RepositoryConfiguration{
				Version: 1,
				Agent:   domain.AgentConfiguration{Instructions: []string{"AGENTS.md"}},
				Policy: domain.PolicyRequest{
					Actions:        map[string]domain.PolicyDecision{"change_request.open": domain.PolicyAllow},
					ProtectedPaths: []string{".github/workflows/**"},
				},
			},
		}, nil
	})
	fixedNow := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	service := NewTaskService(store, func(prefix string) string {
		return prefix + strings.Repeat("A", 26)
	}, func() time.Time { return fixedNow })
	input, err := domain.NormalizeManualInput([]byte("\r\nrepair README\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	operator := domain.PolicyRequest{
		Actions:        map[string]domain.PolicyDecision{"change_request.open": domain.PolicyAllow},
		ProtectedPaths: []string{"infra/**"},
	}

	got, err := service.SubmitManualRepository(context.Background(), "api-service", "github-main", input, operator, source)
	if err != nil {
		t.Fatalf("SubmitManualRepository() error = %v", err)
	}
	if calls != 1 || store.creates != 1 {
		t.Fatalf("source/store calls = %d/%d, want 1/1", calls, store.creates)
	}
	if got.Status != domain.TaskStatusPending || got.Resource.Name != "api-service" || got.Resource.Connector != "github-main" || got.Resource.Revision != "revision-v1" {
		t.Fatalf("task = %#v", got)
	}
	if got.Input.String() != `{"instructions":"repair README","source":"manual","version":1}` {
		t.Fatalf("input snapshot = %s", got.Input.Bytes())
	}
	if !strings.Contains(got.Configuration.String(), "AGENTS.md") || !strings.Contains(got.Policy.String(), "infra/**") || !strings.Contains(got.Policy.String(), ".github/workflows/**") {
		t.Fatalf("snapshots = configuration %s policy %s", got.Configuration.Bytes(), got.Policy.Bytes())
	}
	if strings.Contains(got.Policy.String(), `"change_request.open":"allow"`) {
		t.Fatalf("effective policy unexpectedly allowed action: %s", got.Policy.Bytes())
	}

	shown, err := service.Show(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if !reflect.DeepEqual(got, shown) {
		t.Fatalf("shown task = %#v, want %#v", shown, got)
	}
	if calls != 1 {
		t.Fatalf("source calls after Show = %d, want 1", calls)
	}
}

func TestSubmitManualRepositoryStopsBeforeStoreOnSourceFailure(t *testing.T) {
	store := &recordingTaskStore{}
	service := NewTaskService(store, func(prefix string) string { return prefix + strings.Repeat("A", 26) }, time.Now)
	sourceFailure := errors.New("source-secret-sentinel")
	source := repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) {
		return domain.RepositoryMaterial{}, sourceFailure
	})
	input, err := domain.NormalizeManualInput([]byte("instructions"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitManualRepository(context.Background(), "api-service", "github-main", input, domain.PolicyRequest{}, source)
	if result.ID != "" || err == nil || !errors.Is(err, sourceFailure) || err.Error() != "task failed" || strings.Contains(err.Error(), "source-secret-sentinel") {
		t.Fatalf("result/error = %#v/%v, want safe wrapped source error", result, err)
	}
	if store.creates != 0 {
		t.Fatalf("store creates = %d, want 0", store.creates)
	}
}

type repositorySourceFunc func(context.Context) (domain.RepositoryMaterial, error)

func (f repositorySourceFunc) Snapshot(ctx context.Context) (domain.RepositoryMaterial, error) {
	return f(ctx)
}

type recordingTaskStore struct {
	creates int
	task    domain.Task
}

func (s *recordingTaskStore) CreateTask(_ context.Context, draft domain.TaskDraft) (domain.Task, error) {
	s.creates++
	s.task = domain.Task{
		ID:        draft.ID,
		Status:    draft.Status,
		CreatedAt: draft.CreatedAt,
		Resource: domain.ResourceSnapshot{
			ID:        draft.Resource.CandidateID,
			Name:      draft.Resource.Name,
			Kind:      draft.Resource.Kind,
			Connector: draft.Resource.Connector,
			Revision:  draft.Resource.Revision,
		},
		Input:         draft.Input,
		Configuration: draft.Configuration,
		Policy:        draft.Policy,
	}
	return s.task, nil
}

func (s *recordingTaskStore) Task(context.Context, domain.TaskID) (domain.Task, error) {
	return s.task, nil
}

func TestSubmitManualRepositoryMaterializesEmptyRepositoryCollections(t *testing.T) {
	store := &recordingTaskStore{}
	source := repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) {
		return domain.RepositoryMaterial{ExternalRef: "acme/api", ExternalIdentity: "123", Revision: "revision-v1", Configuration: domain.RepositoryConfiguration{Version: 1}}, nil
	})
	input, err := domain.NormalizeManualInput([]byte("instructions"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewTaskService(store, func(prefix string) string { return prefix + strings.Repeat("A", 26) }, func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) })
	task, err := service.SubmitManualRepository(context.Background(), "api-service", "github-main", input, domain.PolicyRequest{}, source)
	if err != nil {
		t.Fatalf("SubmitManualRepository() error = %v", err)
	}
	for _, marker := range []string{`"instructions":[]`, `"actions":{}`, `"protected_paths":[]`, `"required":[]`} {
		if !strings.Contains(task.Configuration.String(), marker) {
			t.Fatalf("configuration = %s, missing %s", task.Configuration.Bytes(), marker)
		}
	}
}

func TestTaskServiceRejectsCancellationAndInvalidState(t *testing.T) {
	service := NewTaskService(nil, nil, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	input := domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "instructions"}
	if _, err := service.SubmitManualRepository(cancelled, "resource", "connector", input, domain.PolicyRequest{}, repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) { return domain.RepositoryMaterial{}, nil })); err == nil || err.Error() != "task cancelled" {
		t.Fatalf("cancelled submit error = %v", err)
	}
	if _, err := service.Show(cancelled, domain.TaskID("task_"+strings.Repeat("A", 26))); err == nil || err.Error() != "task cancelled" {
		t.Fatalf("cancelled show error = %v", err)
	}
	if _, err := service.SubmitManualRepository(context.Background(), "resource", "connector", input, domain.PolicyRequest{}, nil); err == nil || err.Error() != "task failed" {
		t.Fatalf("unconfigured submit error = %v", err)
	}
	if _, err := service.Show(context.Background(), domain.TaskID("task_"+strings.Repeat("A", 26))); err == nil || err.Error() != "state store is unavailable" {
		t.Fatalf("unconfigured show error = %v", err)
	}
}

func TestTaskServiceMapsStoreFailureAndInvalidIDs(t *testing.T) {
	store := &failingTaskStore{err: safeReasonCause{}}
	service := NewTaskService(store, nil, nil)
	validID := domain.TaskID("task_" + strings.Repeat("A", 26))
	if _, err := service.Show(context.Background(), "invalid"); err == nil || err.Error() != "task not found" {
		t.Fatalf("invalid ID error = %v", err)
	}
	if _, err := service.Show(context.Background(), validID); err == nil || err.Error() != "safe cause" || !errors.Is(err, store.err) {
		t.Fatalf("store failure = %v", err)
	}
}

type safeReasonCause struct{}

func (safeReasonCause) Error() string { return "underlying secret" }

func (safeReasonCause) SafeReason() string { return "safe cause" }

type failingTaskStore struct {
	err error
}

func (s *failingTaskStore) CreateTask(context.Context, domain.TaskDraft) (domain.Task, error) {
	return domain.Task{}, s.err
}

func (s *failingTaskStore) Task(context.Context, domain.TaskID) (domain.Task, error) {
	return domain.Task{}, s.err
}

func TestSubmitManualRepositoryRejectsInvalidPublicInputs(t *testing.T) {
	input := domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "instructions"}
	material := domain.RepositoryMaterial{
		ExternalRef:      "acme/api",
		ExternalIdentity: "123",
		Revision:         "revision-v1",
		Configuration:    domain.RepositoryConfiguration{Version: 1},
	}
	cases := []struct {
		name      string
		input     domain.TaskInput
		operator  domain.PolicyRequest
		material  domain.RepositoryMaterial
		newID     func(string) string
		now       func() time.Time
		wantCause string
	}{
		{name: "incomplete repository material", input: input, material: domain.RepositoryMaterial{Configuration: domain.RepositoryConfiguration{Version: 1}}, wantCause: "repository configuration is invalid"},
		{name: "invalid input version", input: domain.TaskInput{Version: 2, Source: domain.TaskSourceManual, Instructions: "instructions"}, material: material, wantCause: "task input is invalid"},
		{name: "invalid input source", input: domain.TaskInput{Version: 1, Source: "other", Instructions: "instructions"}, material: material, wantCause: "task input is invalid"},
		{name: "empty input", input: domain.TaskInput{Version: 1, Source: domain.TaskSourceManual}, material: material, wantCause: "task input is invalid"},
		{name: "invalid operator policy", input: input, operator: domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{"Invalid": domain.PolicyAllow}}, material: material, wantCause: "task failed"},
		{name: "zero clock", input: input, material: material, now: func() time.Time { return time.Time{} }, wantCause: "task failed"},
		{name: "invalid generated ID", input: input, material: material, newID: func(string) string { return "bad" }, wantCause: "task failed"},
		{name: "invalid generated resource ID", input: input, material: material, newID: func(prefix string) string {
			if prefix == "task_" {
				return prefix + strings.Repeat("A", 26)
			}
			return "bad"
		}, wantCause: "task failed"},
		{name: "invalid repository policy", input: input, material: domain.RepositoryMaterial{ExternalRef: "acme/api", ExternalIdentity: "123", Revision: "revision-v1", Configuration: domain.RepositoryConfiguration{Version: 1, Policy: domain.PolicyRequest{Actions: map[string]domain.PolicyDecision{"Invalid": domain.PolicyAllow}}}}, wantCause: "repository configuration is invalid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &recordingTaskStore{}
			newID := testCase.newID
			if newID == nil {
				newID = func(prefix string) string { return prefix + strings.Repeat("A", 26) }
			}
			now := testCase.now
			if now == nil {
				now = func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) }
			}
			service := NewTaskService(store, newID, now)
			if _, err := service.SubmitManualRepository(context.Background(), "api-service", "github-main", testCase.input, testCase.operator, repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) { return testCase.material, nil })); err == nil || err.Error() != testCase.wantCause {
				t.Fatalf("SubmitManualRepository() error = %v, want %q", err, testCase.wantCause)
			}
			if store.creates != 0 {
				t.Fatalf("store creates = %d, want 0", store.creates)
			}
		})
	}
}

func TestSubmitManualRepositoryMapsCreateFailureSafely(t *testing.T) {
	input := domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "instructions"}
	material := domain.RepositoryMaterial{ExternalRef: "acme/api", ExternalIdentity: "123", Revision: "revision-v1", Configuration: domain.RepositoryConfiguration{Version: 1}}
	cause := errors.New("store-secret")
	store := &failingTaskStore{err: cause}
	service := NewTaskService(store, func(prefix string) string { return prefix + strings.Repeat("A", 26) }, func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) })
	_, err := service.SubmitManualRepository(context.Background(), "api-service", "github-main", input, domain.PolicyRequest{}, repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) { return material, nil }))
	if err == nil || err.Error() != "task failed" || !errors.Is(err, cause) {
		t.Fatalf("SubmitManualRepository() error = %v", err)
	}
}

func TestSafeReasonProvidesStablePublicFallback(t *testing.T) {
	if got := SafeReason(NewFailure("repository unavailable", errors.New("private token"))); got != "repository unavailable" {
		t.Fatalf("SafeReason(safe failure) = %q", got)
	}
	if got := SafeReason(errors.New("private token")); got != "task failed" {
		t.Fatalf("SafeReason(generic failure) = %q", got)
	}
}
