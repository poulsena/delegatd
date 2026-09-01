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
