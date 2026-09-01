package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/config"
	"github.com/poulsena/delegatd/internal/control"
	"github.com/poulsena/delegatd/internal/doctor"
	"github.com/poulsena/delegatd/internal/domain"
)

func TestRunTaskSubmitNormalizesInlineFileAndStdinInputs(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(filePath, []byte("\r\nfile input\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		args      []string
		stdin     string
		wantInput string
	}{
		{name: "inline", args: []string{"--config", "config.yaml", "--resource", "api-service", "--input", "\r\ninline input\r\n"}, wantInput: "inline input"},
		{name: "file", args: []string{"--config", "config.yaml", "--resource", "api-service", "--input-file", filePath}, wantInput: "file input"},
		{name: "stdin", args: []string{"--config", "config.yaml", "--resource", "api-service", "--input-file", "-"}, stdin: "\r\nstdin input\r\n", wantInput: "stdin input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got domain.TaskInput
			dependencies := commandDependencies{
				submitTask: func(_ context.Context, _ string, _ string, input domain.TaskInput) (domain.Task, error) {
					got = input
					return domain.Task{ID: domain.TaskID("task_" + strings.Repeat("A", 26))}, nil
				},
				loadDoctor: func(string) (config.Document, []doctor.Check, *doctor.Failure) { return config.Document{}, nil, nil },
			}
			var stdout, stderr strings.Builder
			exit := run(context.Background(), append([]string{"task", "submit"}, tc.args...), io.NopCloser(strings.NewReader(tc.stdin)), &stdout, &stderr, dependencies)
			if exit != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if got.Version != 1 || got.Source != domain.TaskSourceManual || got.Instructions != tc.wantInput {
				t.Fatalf("input = %#v, want instructions %q", got, tc.wantInput)
			}
			if stdout.String() != `{"task_id":"task_AAAAAAAAAAAAAAAAAAAAAAAAAA"}`+"\n" {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestRunTaskRejectsAmbiguousInputAndRedactsFailures(t *testing.T) {
	called := false
	dependencies := commandDependencies{
		submitTask: func(context.Context, string, string, domain.TaskInput) (domain.Task, error) {
			called = true
			return domain.Task{}, nil
		},
	}
	var stdout, stderr strings.Builder
	if exit := run(context.Background(), []string{"task", "submit", "--config", "config.yaml", "--resource", "api-service", "--input", "one", "--input", "two"}, io.NopCloser(strings.NewReader("")), &stdout, &stderr, dependencies); exit != 2 {
		t.Fatalf("duplicate input exit = %d", exit)
	}
	if called || stdout.Len() != 0 || stderr.String() != taskSubmitUsage+"\n" {
		t.Fatalf("duplicate input called=%v stdout=%q stderr=%q", called, stdout.String(), stderr.String())
	}

	called = false
	stdout.Reset()
	stderr.Reset()
	secret := errors.New("github-token-secret-sentinel")
	dependencies.submitTask = func(context.Context, string, string, domain.TaskInput) (domain.Task, error) {
		called = true
		return domain.Task{}, control.NewFailure("repository is unavailable", secret)
	}
	if exit := run(context.Background(), []string{"task", "submit", "--config", "config.yaml", "--resource", "api-service", "--input", "input"}, io.NopCloser(strings.NewReader("")), &stdout, &stderr, dependencies); exit != 1 {
		t.Fatalf("failure exit = %d", exit)
	}
	if !called || stdout.String() != "FAIL task: repository is unavailable\n" || stderr.Len() != 0 || strings.Contains(stdout.String(), "secret-sentinel") {
		t.Fatalf("failure called=%v stdout=%q stderr=%q", called, stdout.String(), stderr.String())
	}
}

func TestRunTaskShowRendersPersistedTaskJSON(t *testing.T) {
	input, err := domain.NewSnapshot(domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "show input"})
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := domain.NewSnapshot(map[string]any{"version": 1, "marker": "repo-config-v1"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := domain.NewSnapshot(map[string]any{"version": 1, "default_action": "deny", "marker": "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.TaskID("task_" + strings.Repeat("A", 26))
	task := domain.Task{ID: id, Status: domain.TaskStatusPending, CreatedAt: time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC), Resource: domain.ResourceSnapshot{ID: domain.ResourceID("resource_" + strings.Repeat("B", 26)), Name: "api-service", Kind: domain.ResourceKindRepository, Connector: "github-main", Revision: "revision-v1"}, Input: input, Configuration: configuration, Policy: policy}
	called := false
	dependencies := commandDependencies{showTask: func(context.Context, string, domain.TaskID) (domain.Task, error) {
		called = true
		return task, nil
	}}
	var stdout, stderr strings.Builder
	if exit := run(context.Background(), []string{"task", "show", "--config", "show.yaml", string(id)}, io.NopCloser(strings.NewReader("")), &stdout, &stderr, dependencies); exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	if !called || stderr.Len() != 0 || !json.Valid(bytes.TrimSpace([]byte(stdout.String()))) {
		t.Fatalf("called=%v stderr=%q stdout=%q", called, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "repo-config-v1") || !strings.Contains(stdout.String(), "policy-v1") || !strings.Contains(stdout.String(), `"status":"pending"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestReadTaskInputCancellationClosesReader(t *testing.T) {
	reader := &blockingReadCloser{closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readTaskInput(ctx, "", "-", reader)
	if err == nil || control.SafeReason(err) != "task cancelled" {
		t.Fatalf("error = %v", err)
	}
	select {
	case <-reader.closed:
	default:
		t.Fatal("cancellation did not close stdin")
	}
}

type blockingReadCloser struct {
	once   sync.Once
	closed chan struct{}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}
