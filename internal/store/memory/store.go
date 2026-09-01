package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
)

var (
	ErrTaskNotFound     = errors.New("task not found")
	ErrResourceConflict = errors.New("resource conflicts with stored onboarding")
)

type Error struct {
	reason string
	cause  error
}

func (e *Error) Error() string {
	if e == nil || e.reason == "" {
		return "task store is unavailable"
	}
	return e.reason
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *Error) SafeReason() string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func storeError(reason string, cause error) error {
	return &Error{reason: reason, cause: cause}
}

type resource struct {
	id               domain.ResourceID
	name             string
	kind             domain.ResourceKind
	connector        string
	externalRef      string
	externalIdentity string
	revision         string
	configuration    domain.Snapshot
	requestedPolicy  domain.Snapshot
	onboardedAt      time.Time
	updatedAt        time.Time
}

type Store struct {
	mu        sync.RWMutex
	resources map[string]resource
	tasks     map[domain.TaskID]domain.Task
	history   map[domain.TaskID][]domain.TaskHistoryEntry
}

func New() *Store {
	return &Store{
		resources: make(map[string]resource),
		tasks:     make(map[domain.TaskID]domain.Task),
		history:   make(map[domain.TaskID][]domain.TaskHistoryEntry),
	}
}

func (s *Store) CreateTask(ctx context.Context, draft domain.TaskDraft) (domain.Task, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return domain.Task{}, storeError("task could not be submitted", err)
		}
	}
	if s == nil {
		return domain.Task{}, storeError("task store is unavailable", errors.New("store is nil"))
	}
	if err := validateDraft(draft); err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	configuration, err := domain.ParseSnapshot(draft.Resource.Configuration.Bytes())
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	requestedPolicy, err := domain.ParseSnapshot(draft.Resource.RequestedPolicy.Bytes())
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	input, err := domain.ParseSnapshot(draft.Input.Bytes())
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	taskConfiguration, err := domain.ParseSnapshot(draft.Configuration.Bytes())
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	policy, err := domain.ParseSnapshot(draft.Policy.Bytes())
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[draft.ID]; exists {
		return domain.Task{}, storeError("task could not be submitted", errors.New("duplicate task"))
	}
	for _, existing := range s.resources {
		if existing.name != draft.Resource.Name && existing.connector == draft.Resource.Connector && existing.externalIdentity == draft.Resource.ExternalIdentity {
			return domain.Task{}, storeError("resource conflicts with stored onboarding", ErrResourceConflict)
		}
	}
	storedResource, exists := s.resources[draft.Resource.Name]
	if exists {
		if storedResource.connector != draft.Resource.Connector || storedResource.externalIdentity != draft.Resource.ExternalIdentity {
			return domain.Task{}, storeError("resource conflicts with stored onboarding", ErrResourceConflict)
		}
		storedResource.kind = draft.Resource.Kind
		storedResource.externalRef = draft.Resource.ExternalRef
		storedResource.revision = draft.Resource.Revision
		storedResource.configuration = configuration
		storedResource.requestedPolicy = requestedPolicy
		storedResource.updatedAt = draft.CreatedAt
	} else {
		storedResource = resource{
			id:               draft.Resource.CandidateID,
			name:             draft.Resource.Name,
			kind:             draft.Resource.Kind,
			connector:        draft.Resource.Connector,
			externalRef:      draft.Resource.ExternalRef,
			externalIdentity: draft.Resource.ExternalIdentity,
			revision:         draft.Resource.Revision,
			configuration:    configuration,
			requestedPolicy:  requestedPolicy,
			onboardedAt:      draft.CreatedAt,
			updatedAt:        draft.CreatedAt,
		}
	}
	s.resources[draft.Resource.Name] = storedResource
	resourceSnapshot := domain.ResourceSnapshot{
		ID:        storedResource.id,
		Name:      storedResource.name,
		Kind:      storedResource.kind,
		Connector: storedResource.connector,
		Revision:  storedResource.revision,
	}
	created := domain.Task{
		ID:            draft.ID,
		Status:        draft.Status,
		CreatedAt:     draft.CreatedAt,
		Resource:      resourceSnapshot,
		Input:         input,
		Configuration: taskConfiguration,
		Policy:        policy,
	}
	s.tasks[draft.ID] = created
	s.history[draft.ID] = []domain.TaskHistoryEntry{draft.InitialHistory}
	return copyTask(created)
}

func (s *Store) Task(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return domain.Task{}, storeError("task store is unavailable", err)
		}
	}
	if s == nil {
		return domain.Task{}, storeError("task store is unavailable", errors.New("store is nil"))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.tasks[id]
	if !ok {
		return domain.Task{}, storeError("task not found", ErrTaskNotFound)
	}
	return copyTask(result)
}

func (s *Store) History(id domain.TaskID) []domain.TaskHistoryEntry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.TaskHistoryEntry(nil), s.history[id]...)
}

func validateDraft(draft domain.TaskDraft) error {
	if _, err := domain.ParseTaskID(string(draft.ID)); err != nil {
		return err
	}
	if draft.Status != domain.TaskStatusPending || draft.CreatedAt.IsZero() || draft.CreatedAt.Location() != time.UTC {
		return errors.New("invalid task metadata")
	}
	resource := draft.Resource
	if _, err := domain.ParseResourceID(string(resource.CandidateID)); err != nil {
		return err
	}
	if resource.Name == "" || resource.Kind == "" || resource.Connector == "" || resource.ExternalRef == "" || resource.ExternalIdentity == "" || resource.Revision == "" {
		return errors.New("incomplete resource metadata")
	}
	if string(resource.Configuration.Bytes()) != string(draft.Configuration.Bytes()) {
		return errors.New("configuration snapshots differ")
	}
	for _, snapshot := range []domain.Snapshot{resource.Configuration, resource.RequestedPolicy, draft.Input, draft.Configuration, draft.Policy} {
		if _, err := domain.ParseSnapshot(snapshot.Bytes()); err != nil {
			return err
		}
	}
	if draft.InitialHistory.Sequence != 1 || draft.InitialHistory.Status != draft.Status || !draft.InitialHistory.OccurredAt.Equal(draft.CreatedAt) || draft.InitialHistory.Reason != domain.HistoryReasonManualSubmission {
		return errors.New("invalid initial task history")
	}
	return nil
}

func copyTask(value domain.Task) (domain.Task, error) {
	input, err := domain.ParseSnapshot(value.Input.Bytes())
	if err != nil {
		return domain.Task{}, err
	}
	configuration, err := domain.ParseSnapshot(value.Configuration.Bytes())
	if err != nil {
		return domain.Task{}, err
	}
	policy, err := domain.ParseSnapshot(value.Policy.Bytes())
	if err != nil {
		return domain.Task{}, err
	}
	value.Input = input
	value.Configuration = configuration
	value.Policy = policy
	return value, nil
}
