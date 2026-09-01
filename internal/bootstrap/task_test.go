package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/poulsena/delegatd/internal/connector/github"
	"github.com/poulsena/delegatd/internal/control"
	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/store/sqlite"
)

func TestSubmitAndShowTaskUseSeparateBootstrapPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "delegatd.yaml")
	statePath := filepath.Join(dir, "state.db")
	writeTaskConfig(t, configPath, filepath.Base(statePath))
	input, err := domain.NormalizeManualInput([]byte("\r\nrepair README\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	var sourceCalls int
	options := defaultTaskOptions()
	options.newRepositorySource = func(github.Config, github.RepositoryConfig, string) (control.RepositorySource, error) {
		return repositorySourceFunc(func(context.Context) (domain.RepositoryMaterial, error) {
			sourceCalls++
			return domain.RepositoryMaterial{
				ExternalRef:      "acme/api",
				ExternalIdentity: "123",
				Revision:         "revision-v1",
				Configuration: domain.RepositoryConfiguration{
					Version: 1,
					Agent:   domain.AgentConfiguration{Instructions: []string{"repo-config-v1"}},
				},
			}, nil
		}), nil
	}
	created, err := submitTask(context.Background(), configPath, "api-service", input, options)
	if err != nil {
		t.Fatalf("submitTask() error = %v", err)
	}
	if sourceCalls != 1 || created.Status != domain.TaskStatusPending {
		t.Fatalf("source calls/task = %d/%#v", sourceCalls, created)
	}

	showConfigPath := filepath.Join(dir, "show.yaml")
	showConfig := "version: 1\nstore:\n  kind: sqlite\n  config:\n    path: " + filepath.Base(statePath) + "\nconnectors: [invalid]\nresources: [invalid]\npolicy: [invalid]\n"
	if err := osWriteFile(showConfigPath, []byte(showConfig)); err != nil {
		t.Fatal(err)
	}
	showOptions := defaultTaskOptions()
	showOptions.newRepositorySource = func(github.Config, github.RepositoryConfig, string) (control.RepositorySource, error) {
		return nil, errors.New("show constructed connector")
	}
	shown, err := showTask(context.Background(), showConfigPath, created.ID, showOptions)
	if err != nil {
		t.Fatalf("showTask() error = %v", err)
	}
	if shown.ID != created.ID || shown.Configuration.String() != created.Configuration.String() || shown.Policy.String() != created.Policy.String() {
		t.Fatalf("shown = %#v, want %#v", shown, created)
	}
	if sourceCalls != 1 {
		t.Fatalf("source calls after show = %d, want 1", sourceCalls)
	}
}

func TestSubmitTaskRejectsUnknownResourceBeforeOpeningStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "delegatd.yaml")
	writeTaskConfig(t, configPath, "state.db")
	opened := false
	options := defaultTaskOptions()
	options.openStore = func(context.Context, sqlite.Config, string) (*sqlite.Store, error) {
		opened = true
		return nil, errors.New("store should not open")
	}
	input, err := domain.NormalizeManualInput([]byte("instructions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submitTask(context.Background(), configPath, "missing", input, options); err == nil || err.Error() != "resource is not configured" {
		t.Fatalf("error = %v", err)
	}
	if opened {
		t.Fatal("store opened for unknown resource")
	}
}

func writeTaskConfig(t *testing.T, path, statePath string) {
	t.Helper()
	data := "version: 1\n" +
		"store:\n  kind: sqlite\n  config:\n    path: " + statePath + "\n" +
		"connectors:\n  github-main:\n    kind: github\n    config:\n      app_id: 1\n      private_key_file: missing.pem\n" +
		"workspace_providers:\n  docker-local:\n    kind: docker\n    config: {}\n" +
		"agent_runtimes:\n  omp-primary:\n    kind: omp\n    config: {}\n" +
		"resources:\n  api-service:\n    kind: repository\n    connector: github-main\n    config:\n      external_ref: acme/api\n" +
		"policy:\n  actions:\n    change_request.open: deny\n  protected_paths: []\n"
	if err := osWriteFile(path, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

type repositorySourceFunc func(context.Context) (domain.RepositoryMaterial, error)

func (f repositorySourceFunc) Snapshot(ctx context.Context) (domain.RepositoryMaterial, error) {
	return f(ctx)
}

func TestExportedTaskWrappersReturnSafeConfigurationFailures(t *testing.T) {
	input := domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "instructions"}
	if _, err := SubmitTask(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), "api-service", input); err == nil || control.SafeReason(err) != "configuration file is unreadable" {
		t.Fatalf("SubmitTask() error = %v", err)
	}
	if _, err := ShowTask(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), domain.TaskID("task_"+strings.Repeat("A", 26))); err == nil || control.SafeReason(err) != "configuration file is unreadable" {
		t.Fatalf("ShowTask() error = %v", err)
	}
}

func TestExportedSubmitTaskRejectsInvalidConfiguration(t *testing.T) {
	input := domain.TaskInput{Version: 1, Source: domain.TaskSourceManual, Instructions: "instructions"}
	cases := []struct {
		name     string
		resource string
		mutate   func(string) string
		want     string
	}{
		{name: "unknown resource", resource: "missing", want: "resource is not configured"},
		{name: "unsupported resource kind", resource: "api-service", mutate: func(data string) string { return strings.Replace(data, "kind: repository", "kind: unsupported", 1) }, want: "resource is unsupported"},
		{name: "unsupported connector kind", resource: "api-service", mutate: func(data string) string { return strings.Replace(data, "kind: github", "kind: other", 1) }, want: "resource is unsupported"},
		{name: "invalid connector configuration", resource: "api-service", mutate: func(data string) string { return strings.Replace(data, "app_id: 1", "app_id: invalid", 1) }, want: "connector configuration is invalid"},
		{name: "invalid resource configuration", resource: "api-service", mutate: func(data string) string {
			return strings.Replace(data, "external_ref: acme/api", "external_ref: [invalid]", 1)
		}, want: "resource configuration is invalid"},
		{name: "invalid store configuration", resource: "api-service", mutate: func(data string) string { return strings.Replace(data, "path: state.db", "path: [invalid]", 1) }, want: "state store is unavailable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "delegatd.yaml")
			writeTaskConfig(t, configPath, "state.db")
			if testCase.mutate != nil {
				data, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := osWriteFile(configPath, []byte(testCase.mutate(string(data)))); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SubmitTask(context.Background(), configPath, testCase.resource, input); err == nil || control.SafeReason(err) != testCase.want {
				t.Fatalf("SubmitTask() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestExportedShowTaskRejectsUnsupportedStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "show.yaml")
	if err := osWriteFile(configPath, []byte("version: 1\nstore:\n  kind: memory\n  config: {}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := ShowTask(context.Background(), configPath, domain.TaskID("task_"+strings.Repeat("A", 26))); err == nil || control.SafeReason(err) != "state store is unavailable" {
		t.Fatalf("ShowTask() error = %v", err)
	}
}

func TestExportedShowTaskReportsUnavailableStateStore(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "show.yaml")
	if err := osWriteFile(configPath, []byte("version: 1\nstore:\n  kind: sqlite\n  config:\n    path: missing.db\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := ShowTask(context.Background(), configPath, domain.TaskID("task_"+strings.Repeat("A", 26))); err == nil || control.SafeReason(err) != "state store is unavailable" {
		t.Fatalf("ShowTask() error = %v", err)
	}
}
