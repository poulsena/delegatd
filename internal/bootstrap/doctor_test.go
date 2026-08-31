package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/poulsena/delegatd/internal/config"
)

func TestLoadDoctorBuildsSupportedChecksInDeterministicCategoryOrder(t *testing.T) {
	path := writeConfig(t, `version: 1
store:
  kind: sqlite
  config:
    path: ./state.db
connectors:
  zeta:
    kind: github
    config: {}
  alpha:
    kind: github
    config: {}
workspace_providers:
  beta:
    kind: docker
    config: {}
  alpha:
    kind: docker
    config: {}
agent_runtimes:
  zeta:
    kind: omp
    config: {}
  alpha:
    kind: omp
    config: {}
`)
	document, checks, failure := LoadDoctor(path)
	if failure != nil {
		t.Fatalf("failure = %v", failure)
	}
	if document.Path != path {
		t.Fatalf("Path = %q, want %q", document.Path, path)
	}
	got := make([]string, len(checks))
	for index, check := range checks {
		got[index] = check.ID
	}
	want := []string{
		"connector.alpha",
		"connector.zeta",
		"workspace_provider.alpha",
		"workspace_provider.beta",
		"agent_runtime.alpha",
		"agent_runtime.zeta",
		"store.sqlite",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("check IDs = %v, want %v", got, want)
	}
}

func TestLoadDoctorRetainsIndependentChecksForUnsupportedAndBadAdapterConfig(t *testing.T) {
	path := writeConfig(t, `version: 1
store:
  kind: sqlite
  config: {}
connectors:
  unsupported:
    kind: future-connector
    config: {}
  valid:
    kind: github
    config:
      unknown: sentinel
workspace_providers:
  local:
    kind: docker
    config: {}
agent_runtimes:
  primary:
    kind: omp
    config: {}
`)
	_, checks, failure := LoadDoctor(path)
	if failure != nil {
		t.Fatalf("failure = %v", failure)
	}
	if len(checks) != 5 {
		t.Fatalf("checks = %d, want 5", len(checks))
	}
	for _, check := range checks {
		if check.ID == "connector.unsupported" || check.ID == "connector.valid" {
			_, checkFailure := check.Probe(context.Background())
			if checkFailure == nil {
				t.Fatalf("%s unexpectedly passed", check.ID)
			}
			if check.ID == "connector.unsupported" && checkFailure.Error() != "connector.unsupported adapter kind is unsupported" {
				t.Fatalf("unsupported failure = %q", checkFailure.Error())
			}
			if check.ID == "connector.valid" && checkFailure.Error() != "connector.valid configuration contains unknown or invalid fields" {
				t.Fatalf("config failure = %q", checkFailure.Error())
			}
		}
	}
}

func TestLoadDoctorNormalizesConfigurationFailureAndRunsNoChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "configuration-secret-path.yaml")
	if _, _, failure := LoadDoctor(path); failure == nil || failure.Error() != "configuration file is unreadable" {
		t.Fatalf("failure = %v", failure)
	}
	if failure := config.SafeReason(nil); failure != "" {
		t.Fatalf("SafeReason(nil) = %q", failure)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "delegatd.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}
