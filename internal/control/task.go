package control

import (
	"context"
	"errors"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/policy"
)

// TaskStore is the application-owned durable task boundary.
type TaskStore interface {
	CreateTask(context.Context, domain.TaskDraft) (domain.Task, error)
	Task(context.Context, domain.TaskID) (domain.Task, error)
}

// RepositorySource is the narrow connector result needed by this repository
// task slice. Connector credentials and transport types stay outside control.
type RepositorySource interface {
	Snapshot(context.Context) (domain.RepositoryMaterial, error)
}

type TaskService struct {
	store TaskStore
	newID func(string) string
	now   func() time.Time
}

func NewTaskService(store TaskStore, newID func(string) string, now func() time.Time) *TaskService {
	if newID == nil {
		newID = domain.NewID
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &TaskService{store: store, newID: newID, now: now}
}

// SubmitManualRepository captures the source, policy, and input snapshots once
// and persists a pending generic task. It does not start any execution path.
func (s *TaskService) SubmitManualRepository(ctx context.Context, resourceName, connectorName string, input domain.TaskInput, operator domain.PolicyRequest, source RepositorySource) (domain.Task, error) {
	if err := contextError(ctx); err != nil {
		return domain.Task{}, NewFailure("task cancelled", err)
	}
	if s == nil || s.store == nil || source == nil || resourceName == "" || connectorName == "" {
		return domain.Task{}, NewFailure("task failed", errors.New("task service is not configured"))
	}
	material, err := source.Snapshot(ctx)
	if err != nil {
		return domain.Task{}, failureFromCause(ctx, err)
	}
	if material.ExternalRef == "" || material.ExternalIdentity == "" || material.Revision == "" || material.Configuration.Version != 1 {
		return domain.Task{}, NewFailure("repository configuration is invalid", errors.New("repository material is incomplete"))
	}

	repositoryConfiguration := cloneRepositoryConfiguration(material.Configuration)
	repositoryPolicy, err := policy.NormalizeRequest(repositoryConfiguration.Policy)
	if err != nil {
		return domain.Task{}, NewFailure("repository configuration is invalid", err)
	}
	repositoryConfiguration.Policy = repositoryPolicy
	operatorPolicy, err := policy.NormalizeRequest(operator)
	if err != nil {
		return domain.Task{}, NewFailure("task failed", err)
	}
	input = domain.TaskInput{Version: input.Version, Source: input.Source, Instructions: input.Instructions}
	if input.Version != 1 || input.Source != domain.TaskSourceManual || input.Instructions == "" {
		return domain.Task{}, NewFailure("task input is invalid", errors.New("invalid normalized task input"))
	}

	configurationSnapshot, err := domain.NewSnapshot(repositoryConfiguration)
	if err != nil {
		return domain.Task{}, NewFailure("repository configuration is invalid", err)
	}
	requestedPolicySnapshot, err := domain.NewSnapshot(repositoryPolicy)
	if err != nil {
		return domain.Task{}, NewFailure("repository configuration is invalid", err)
	}
	inputSnapshot, err := domain.NewSnapshot(input)
	if err != nil {
		return domain.Task{}, NewFailure("task input is invalid", err)
	}
	effectivePolicy := policy.Resolve(operatorPolicy, repositoryPolicy, nil)
	policySnapshot, err := domain.NewSnapshot(effectivePolicy)
	if err != nil {
		return domain.Task{}, NewFailure("task failed", err)
	}

	createdAt := s.now()
	if createdAt.IsZero() {
		return domain.Task{}, NewFailure("task failed", errors.New("task clock returned zero"))
	}
	createdAt = createdAt.UTC()
	taskID := domain.TaskID(s.newID("task_"))
	resourceID := domain.ResourceID(s.newID("resource_"))
	draft := domain.TaskDraft{
		ID:     taskID,
		Status: domain.TaskStatusPending,
		Resource: domain.ResourceDraft{
			CandidateID:      resourceID,
			Name:             resourceName,
			Kind:             domain.ResourceKindRepository,
			Connector:        connectorName,
			ExternalRef:      material.ExternalRef,
			ExternalIdentity: material.ExternalIdentity,
			Revision:         material.Revision,
			Configuration:    configurationSnapshot,
			RequestedPolicy:  requestedPolicySnapshot,
		},
		Input:         inputSnapshot,
		Configuration: configurationSnapshot,
		Policy:        policySnapshot,
		CreatedAt:     createdAt,
		InitialHistory: domain.TaskHistoryEntry{
			Sequence:   1,
			Status:     domain.TaskStatusPending,
			OccurredAt: createdAt,
			Reason:     domain.HistoryReasonManualSubmission,
		},
	}
	if _, err := domain.ParseTaskID(string(draft.ID)); err != nil {
		return domain.Task{}, NewFailure("task failed", err)
	}
	if _, err := domain.ParseResourceID(string(draft.Resource.CandidateID)); err != nil {
		return domain.Task{}, NewFailure("task failed", err)
	}
	created, err := s.store.CreateTask(ctx, draft)
	if err != nil {
		return domain.Task{}, failureFromCause(ctx, err)
	}
	return created, nil
}

// Show reads only the persisted task projection.
func (s *TaskService) Show(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	if err := contextError(ctx); err != nil {
		return domain.Task{}, NewFailure("task cancelled", err)
	}
	if s == nil || s.store == nil {
		return domain.Task{}, NewFailure("state store is unavailable", errors.New("task store is not configured"))
	}
	if _, err := domain.ParseTaskID(string(id)); err != nil {
		return domain.Task{}, NewFailure("task not found", err)
	}
	result, err := s.store.Task(ctx, id)
	if err != nil {
		return domain.Task{}, failureFromCause(ctx, err)
	}
	return result, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func failureFromCause(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return NewFailure("task cancelled", ctx.Err())
	}
	var reasonProvider interface{ SafeReason() string }
	if errors.As(err, &reasonProvider) && reasonProvider.SafeReason() != "" {
		return NewFailure(reasonProvider.SafeReason(), err)
	}
	return NewFailure("task failed", err)
}

func cloneRepositoryConfiguration(configuration domain.RepositoryConfiguration) domain.RepositoryConfiguration {
	clone := configuration
	clone.Agent.Instructions = append([]string{}, configuration.Agent.Instructions...)
	clone.Policy.Actions = make(map[string]domain.PolicyDecision, len(configuration.Policy.Actions))
	for name, decision := range configuration.Policy.Actions {
		clone.Policy.Actions[name] = decision
	}
	clone.Policy.ProtectedPaths = append([]string{}, configuration.Policy.ProtectedPaths...)
	clone.Validation.Required = append([]domain.ValidationCommand{}, configuration.Validation.Required...)
	return clone
}
