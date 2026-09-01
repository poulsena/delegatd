package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
	_ "modernc.org/sqlite"
)

var (
	ErrTaskNotFound     = errors.New("task not found")
	ErrResourceConflict = errors.New("resource conflicts with stored onboarding")
	errStoreCorrupt     = errors.New("state store is corrupt")
	errStoreUnsupported = errors.New("state store schema is unsupported")
)

// Error is a safe store error with a wrapped cause for trusted diagnostics.
type Error struct {
	reason string
	cause  error
}

func (e *Error) Error() string {
	if e == nil || e.reason == "" {
		return "state store is unavailable"
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

type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

// Open opens the writable state database, creates the supported schema when
// needed, and runs migrations before returning the store.
func Open(ctx context.Context, cfg Config, dir string) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := resolveDatabasePath(cfg.Path, dir)
	if err != nil {
		return nil, storeError("state store is unavailable", err)
	}
	created, err := prepareDatabaseFile(path)
	if err != nil {
		return nil, storeError("state store is unavailable", err)
	}
	cleanup := func(cause error) (*Store, error) {
		if created {
			_ = os.Remove(path)
		}
		return nil, storeError("state store is unavailable", cause)
	}
	database, err := sql.Open("sqlite", writableDSN(path))
	if err != nil {
		return cleanup(err)
	}
	store := &Store{db: database, path: path}
	configureDatabase(database)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return cleanup(err)
	}
	if err := requireDeleteJournal(ctx, database); err != nil {
		_ = database.Close()
		return cleanup(err)
	}
	if err := migrate(ctx, database); err != nil {
		_ = database.Close()
		return cleanup(err)
	}
	if err := validateSchema(ctx, database); err != nil {
		_ = database.Close()
		return cleanup(err)
	}
	return store, nil
}

// OpenReadOnly opens only an existing supported state database for inspection.
func OpenReadOnly(ctx context.Context, cfg Config, dir string) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := resolveDatabasePath(cfg.Path, dir)
	if err != nil {
		return nil, storeError("state store is unavailable", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("state store path is not a regular file")
		}
		return nil, storeError("state store is unavailable", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, storeError("state store is unavailable", errors.New("state store permissions are too broad"))
	}
	database, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, storeError("state store is unavailable", err)
	}
	store := &Store{db: database, path: path, readOnly: true}
	configureDatabase(database)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, storeError("state store is unavailable", err)
	}
	if err := requireDeleteJournal(ctx, database); err != nil {
		_ = database.Close()
		return nil, storeError("state store is unavailable", err)
	}
	if err := validateSchema(ctx, database); err != nil {
		_ = database.Close()
		return nil, storeError("state store is unavailable", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateTask(ctx context.Context, draft domain.TaskDraft) (domain.Task, error) {
	if s == nil || s.db == nil || s.readOnly {
		return domain.Task{}, storeError("state store is unavailable", errors.New("store is read-only or closed"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateTaskDraft(draft); err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	configuration := draft.Configuration.Bytes()
	requestedPolicy := draft.Resource.RequestedPolicy.Bytes()
	input := draft.Input.Bytes()
	effectivePolicy := draft.Policy.Bytes()

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	defer transaction.Rollback()

	var resourceID string
	var existingConnector, existingIdentity, existingKind string
	err = transaction.QueryRowContext(ctx, `SELECT id, connector_instance, external_identity, kind FROM resources WHERE name = ?`, draft.Resource.Name).Scan(&resourceID, &existingConnector, &existingIdentity, &existingKind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var owner string
		identityErr := transaction.QueryRowContext(ctx, `SELECT name FROM resources WHERE connector_instance = ? AND external_identity = ?`, draft.Resource.Connector, draft.Resource.ExternalIdentity).Scan(&owner)
		if identityErr == nil {
			return domain.Task{}, storeError("resource conflicts with stored onboarding", ErrResourceConflict)
		}
		if !errors.Is(identityErr, sql.ErrNoRows) {
			return domain.Task{}, storeError("task could not be submitted", identityErr)
		}
		resourceID = string(draft.Resource.CandidateID)
		createdAt := formatTime(draft.CreatedAt)
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO resources (
				id, name, kind, connector_instance, external_ref, external_identity,
				revision, configuration_json, policy_request_json, onboarded_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			resourceID, draft.Resource.Name, string(draft.Resource.Kind), draft.Resource.Connector,
			draft.Resource.ExternalRef, draft.Resource.ExternalIdentity, draft.Resource.Revision,
			configuration, requestedPolicy, createdAt, createdAt); err != nil {
			return domain.Task{}, storeError("task could not be submitted", err)
		}
	case err != nil:
		return domain.Task{}, storeError("task could not be submitted", err)
	default:
		if existingConnector != draft.Resource.Connector || existingIdentity != draft.Resource.ExternalIdentity {
			return domain.Task{}, storeError("resource conflicts with stored onboarding", ErrResourceConflict)
		}
		if _, err := domain.ParseResourceID(resourceID); err != nil || existingKind == "" {
			return domain.Task{}, storeError("state store is unavailable", errStoreCorrupt)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE resources SET kind = ?, external_ref = ?, revision = ?,
			configuration_json = ?, policy_request_json = ?, updated_at = ?
			WHERE id = ?`,
			string(draft.Resource.Kind), draft.Resource.ExternalRef, draft.Resource.Revision,
			configuration, requestedPolicy, formatTime(draft.CreatedAt), resourceID); err != nil {
			return domain.Task{}, storeError("task could not be submitted", err)
		}
	}

	resourceSnapshot := domain.ResourceSnapshot{
		ID:        domain.ResourceID(resourceID),
		Name:      draft.Resource.Name,
		Kind:      draft.Resource.Kind,
		Connector: draft.Resource.Connector,
		Revision:  draft.Resource.Revision,
	}
	resourceJSON, err := domain.NewSnapshot(resourceSnapshot)
	if err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO tasks (
			id, resource_id, status, resource_json, input_json,
			configuration_json, policy_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(draft.ID), resourceID, string(draft.Status), resourceJSON.Bytes(), input,
		configuration, effectivePolicy, formatTime(draft.CreatedAt)); err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO task_history (task_id, sequence, status, occurred_at, reason)
		VALUES (?, ?, ?, ?, ?)`,
		string(draft.ID), draft.InitialHistory.Sequence, string(draft.InitialHistory.Status),
		formatTime(draft.InitialHistory.OccurredAt), draft.InitialHistory.Reason); err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	if err := transaction.Commit(); err != nil {
		return domain.Task{}, storeError("task could not be submitted", err)
	}
	return domain.Task{
		ID:            draft.ID,
		Status:        draft.Status,
		CreatedAt:     draft.CreatedAt,
		Resource:      resourceSnapshot,
		Input:         mustSnapshot(input),
		Configuration: mustSnapshot(configuration),
		Policy:        mustSnapshot(effectivePolicy),
	}, nil
}

func (s *Store) Task(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	if s == nil || s.db == nil {
		return domain.Task{}, storeError("state store is unavailable", errors.New("store is closed"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := domain.ParseTaskID(string(id)); err != nil {
		return domain.Task{}, storeError("task not found", ErrTaskNotFound)
	}
	var status string
	var resourceJSON, inputJSON, configurationJSON, policyJSON []byte
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT status, resource_json, input_json, configuration_json, policy_json, created_at
		FROM tasks WHERE id = ?`, string(id)).Scan(&status, &resourceJSON, &inputJSON, &configurationJSON, &policyJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, storeError("task not found", ErrTaskNotFound)
	}
	if err != nil {
		return domain.Task{}, storeError("state store is unavailable", err)
	}
	if status != string(domain.TaskStatusPending) {
		return domain.Task{}, storeError("state store is unavailable", errStoreCorrupt)
	}
	var resource domain.ResourceSnapshot
	if err := decodeResourceSnapshot(resourceJSON, &resource); err != nil {
		return domain.Task{}, storeError("state store is unavailable", err)
	}
	if _, err := domain.ParseResourceID(string(resource.ID)); err != nil || resource.Name == "" || resource.Kind == "" || resource.Connector == "" || resource.Revision == "" {
		return domain.Task{}, storeError("state store is unavailable", errStoreCorrupt)
	}
	created, err := parseStoredTime(createdAt)
	if err != nil {
		return domain.Task{}, storeError("state store is unavailable", err)
	}
	input, err := domain.ParseSnapshot(inputJSON)
	if err != nil {
		return domain.Task{}, storeError("state store is unavailable", err)
	}
	configuration, err := domain.ParseSnapshot(configurationJSON)
	if err != nil {
		return domain.Task{}, storeError("state store is unavailable", err)
	}
	policy, err := domain.ParseSnapshot(policyJSON)
	if err != nil {
		return domain.Task{}, storeError("state store is unavailable", err)
	}
	return domain.Task{
		ID:            id,
		Status:        domain.TaskStatus(status),
		CreatedAt:     created,
		Resource:      resource,
		Input:         input,
		Configuration: configuration,
		Policy:        policy,
	}, nil
}

func resolveDatabasePath(configuredPath, dir string) (string, error) {
	if configuredPath == "" {
		return "", errors.New("state store path is required")
	}
	path := configuredPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Abs(path)
}

func prepareDatabaseFile(path string) (bool, error) {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("state store parent is not a directory")
		}
		return false, err
	}
	info, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return false, createErr
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(path)
			return false, closeErr
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("state store path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false, errors.New("state store permissions are too broad")
	}
	return false, nil
}

func configureDatabase(database *sql.DB) {
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
}

func writableDSN(path string) string {
	return "file:" + url.PathEscape(filepath.ToSlash(path)) + "?mode=rwc&_foreign_keys=1&_busy_timeout=5000&_txlock=immediate"
}

func readOnlyDSN(path string) string {
	return "file:" + url.PathEscape(filepath.ToSlash(path)) + "?mode=ro&immutable=1&_pragma=query_only(1)&_foreign_keys=1"
}

func requireDeleteJournal(ctx context.Context, database *sql.DB) error {
	var journal string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return err
	}
	if !strings.EqualFold(journal, "delete") {
		return errors.New("state store journal mode is unsupported")
	}
	return nil
}

func validateTaskDraft(draft domain.TaskDraft) error {
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
	if !bytes.Equal(resource.Configuration.Bytes(), draft.Configuration.Bytes()) {
		return errors.New("configuration snapshots differ")
	}
	for _, snapshot := range []domain.Snapshot{resource.Configuration, resource.RequestedPolicy, draft.Input, draft.Configuration, draft.Policy} {
		if _, err := domain.ParseSnapshot(snapshot.Bytes()); err != nil {
			return err
		}
	}
	history := draft.InitialHistory
	if history.Sequence != 1 || history.Status != draft.Status || history.OccurredAt != draft.CreatedAt || history.Reason != domain.HistoryReasonManualSubmission {
		return errors.New("invalid initial task history")
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		if err == nil {
			err = errors.New("stored timestamp is not UTC")
		}
		return time.Time{}, err
	}
	return parsed, nil
}

func decodeResourceSnapshot(data []byte, destination *domain.ResourceSnapshot) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("resource snapshot contains multiple values")
		}
		return err
	}
	return nil
}

func mustSnapshot(data []byte) domain.Snapshot {
	snapshot, err := domain.ParseSnapshot(data)
	if err != nil {
		panic(err)
	}
	return snapshot
}
