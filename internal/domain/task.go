package domain

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxManualInputSize = 1 << 20
	maxSnapshotSize    = 16 << 20

	TaskSourceManual              = "manual"
	HistoryReasonManualSubmission = "manual_submission"
)

var (
	taskIDPattern     = regexp.MustCompile(`^task_[A-Z2-7]{26,128}$`)
	resourceIDPattern = regexp.MustCompile(`^resource_[A-Z2-7]{26,128}$`)
)

type TaskID string
type ResourceID string
type TaskStatus string
type ResourceKind string
type PolicyDecision string

const (
	TaskStatusPending      TaskStatus     = "pending"
	ResourceKindRepository ResourceKind   = "repository"
	PolicyAllow            PolicyDecision = "allow"
	PolicyDeny             PolicyDecision = "deny"
)

// NewID returns a cryptographically random identifier with the supplied prefix.
func NewID(prefix string) string {
	return prefix + rand.Text()
}

// ParseTaskID validates the public task identifier grammar.
func ParseTaskID(value string) (TaskID, error) {
	if !taskIDPattern.MatchString(value) {
		return "", errors.New("invalid task ID")
	}
	return TaskID(value), nil
}

// ParseResourceID validates the durable resource identifier grammar.
func ParseResourceID(value string) (ResourceID, error) {
	if !resourceIDPattern.MatchString(value) {
		return "", errors.New("invalid resource ID")
	}
	return ResourceID(value), nil
}

// TaskInput is the normalized input envelope for a manual task.
type TaskInput struct {
	Version      int    `json:"version"`
	Source       string `json:"source"`
	Instructions string `json:"instructions"`
}

type AgentConfiguration struct {
	Instructions []string `json:"instructions" yaml:"instructions"`
}

type PolicyRequest struct {
	Actions        map[string]PolicyDecision `json:"actions" yaml:"actions"`
	ProtectedPaths []string                  `json:"protected_paths" yaml:"protected_paths"`
}

type ValidationCommand struct {
	Name    string `json:"name" yaml:"name"`
	Run     string `json:"run" yaml:"run"`
	Timeout string `json:"timeout" yaml:"timeout"`
}

type ValidationConfiguration struct {
	Required []ValidationCommand `json:"required" yaml:"required"`
}

type RepositoryConfiguration struct {
	Version    int                     `json:"version" yaml:"version"`
	Agent      AgentConfiguration      `json:"agent" yaml:"agent"`
	Policy     PolicyRequest           `json:"policy" yaml:"policy"`
	Validation ValidationConfiguration `json:"validation" yaml:"validation"`
}

type EffectivePolicy struct {
	Version        int                       `json:"version"`
	DefaultAction  PolicyDecision            `json:"default_action"`
	Actions        map[string]PolicyDecision `json:"actions"`
	ProtectedPaths []string                  `json:"protected_paths"`
}

// RepositoryMaterial is the trusted connector result used by the repository
// task submission path. External values remain opaque to the rest of the core.
type RepositoryMaterial struct {
	ExternalRef      string
	ExternalIdentity string
	Revision         string
	Configuration    RepositoryConfiguration
}

type ResourceSnapshot struct {
	ID        ResourceID   `json:"id"`
	Name      string       `json:"name"`
	Kind      ResourceKind `json:"kind"`
	Connector string       `json:"connector"`
	Revision  string       `json:"revision"`
}

type Snapshot struct {
	data json.RawMessage
}

// NewSnapshot validates and canonicalizes a JSON-serializable value.
func NewSnapshot(value any) (Snapshot, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, err
	}
	return ParseSnapshot(data)
}

// ParseSnapshot validates and canonicalizes one JSON value.
func ParseSnapshot(data []byte) (Snapshot, error) {
	canonical, err := canonicalJSON(data)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{data: canonical}, nil
}

// Bytes returns an independent copy of the canonical JSON representation.
func (s Snapshot) Bytes() []byte {
	return append([]byte(nil), s.data...)
}

// MarshalJSON embeds the canonical value instead of quoting it.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	if len(s.data) == 0 {
		return nil, errors.New("empty snapshot")
	}
	return s.Bytes(), nil
}

func canonicalJSON(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty JSON snapshot")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("snapshot contains multiple JSON values")
		}
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	canonical := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	if len(canonical) > maxSnapshotSize {
		return nil, errors.New("snapshot exceeds 16 MiB")
	}
	return append([]byte(nil), canonical...), nil
}

// NormalizeManualInput converts user input to the version-one manual envelope.
func NormalizeManualInput(raw []byte) (TaskInput, error) {
	if len(raw) > maxManualInputSize {
		return TaskInput{}, errors.New("manual input exceeds 1 MiB")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return TaskInput{}, errors.New("manual input is invalid")
	}
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	instructions := strings.Join(lines[start:end], "\n")
	if instructions == "" {
		return TaskInput{}, errors.New("manual input is empty")
	}
	if len([]byte(instructions)) > maxManualInputSize {
		return TaskInput{}, errors.New("manual input exceeds 1 MiB")
	}
	return TaskInput{Version: 1, Source: TaskSourceManual, Instructions: instructions}, nil
}

type ResourceDraft struct {
	CandidateID      ResourceID
	Name             string
	Kind             ResourceKind
	Connector        string
	ExternalRef      string
	ExternalIdentity string
	Revision         string
	Configuration    Snapshot
	RequestedPolicy  Snapshot
}

type TaskHistoryEntry struct {
	Sequence   int
	Status     TaskStatus
	OccurredAt time.Time
	Reason     string
}

type TaskDraft struct {
	ID             TaskID
	Status         TaskStatus
	Resource       ResourceDraft
	Input          Snapshot
	Configuration  Snapshot
	Policy         Snapshot
	CreatedAt      time.Time
	InitialHistory TaskHistoryEntry
}

type Task struct {
	ID            TaskID           `json:"id"`
	Status        TaskStatus       `json:"status"`
	CreatedAt     time.Time        `json:"created_at"`
	Resource      ResourceSnapshot `json:"resource"`
	Input         Snapshot         `json:"input"`
	Configuration Snapshot         `json:"configuration"`
	Policy        Snapshot         `json:"policy"`
}

func (s Snapshot) String() string {
	return string(s.data)
}

func (s TaskStatus) String() string {
	return string(s)
}

func (k ResourceKind) String() string {
	return string(k)
}

func (d PolicyDecision) String() string {
	return string(d)
}
