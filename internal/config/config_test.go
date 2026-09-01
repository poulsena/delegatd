package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const validConfiguration = `version: 1
store:
  kind: sqlite
  config:
    path: ./state.db
connectors:
  github-main:
    kind: github
    config:
      app_id: 123456
      private_key_file: ./github-app.pem
workspace_providers:
  docker-local:
    kind: docker
    config: {}
agent_runtimes:
  omp-primary:
    kind: omp
    config: {}
`

func TestLoadResolvesDocumentPathAndDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "delegatd.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfiguration(t, path, validConfiguration)

	document, err := Load(filepath.Join(dir, "nested", ".", "delegatd.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.Path != absolute {
		t.Fatalf("Path = %q, want %q", document.Path, absolute)
	}
	if document.Dir != filepath.Dir(absolute) {
		t.Fatalf("Dir = %q, want %q", document.Dir, filepath.Dir(absolute))
	}
	if document.Config.Store.Config.Kind != yaml.MappingNode {
		t.Fatalf("store config kind = %d, want mapping", document.Config.Store.Config.Kind)
	}
	if got := document.Config.Connectors["github-main"].Config.Kind; got != yaml.MappingNode {
		t.Fatalf("connector config kind = %d, want mapping", got)
	}
}

func TestLoadRejectsSchemaFailuresWithSafeReasons(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "wrong version", data: strings.Replace(validConfiguration, "version: 1", "version: 2", 1), want: "version must be 1"},
		{name: "missing store", data: strings.Replace(validConfiguration, "store:\n  kind: sqlite\n  config:\n    path: ./state.db\n", "", 1), want: "store is required"},
		{name: "missing connectors", data: strings.Replace(validConfiguration, "connectors:\n  github-main:\n    kind: github\n    config:\n      app_id: 123456\n      private_key_file: ./github-app.pem\n", "", 1), want: "connectors is required"},
		{name: "empty workspace providers", data: strings.Replace(validConfiguration, "workspace_providers:\n  docker-local:\n    kind: docker\n    config: {}", "workspace_providers: {}", 1), want: "workspace_providers must contain at least one instance"},
		{name: "empty runtimes", data: strings.Replace(validConfiguration, "agent_runtimes:\n  omp-primary:\n    kind: omp\n    config: {}", "agent_runtimes: {}", 1), want: "agent_runtimes must contain at least one instance"},
		{name: "invalid connector name", data: strings.Replace(validConfiguration, "github-main:", "GitHub-main:", 1), want: "connectors contains an invalid instance name"},
		{name: "invalid kind", data: strings.Replace(validConfiguration, "kind: github", "kind: GitHub", 1), want: "connector.github-main kind is missing or invalid"},
		{name: "unknown top-level field", data: "extra: secret-sentinel\n" + validConfiguration, want: "configuration YAML is invalid"},
		{name: "unknown instance field", data: strings.Replace(validConfiguration, "kind: github\n", "kind: github\n      extra: secret-sentinel\n", 1), want: "configuration YAML is invalid"},
		{name: "duplicate document", data: validConfiguration + "---\n" + validConfiguration, want: "configuration must contain exactly one document"},
		{name: "duplicate key", data: strings.Replace(validConfiguration, "version: 1", "version: 1\nversion: 1", 1), want: "configuration YAML is invalid"},
		{name: "scalar root", data: "not-a-mapping\n", want: "configuration YAML is invalid"},
		{name: "environment interpolation", data: strings.Replace(validConfiguration, "./state.db", "${STATE_DB}", 1), want: "configuration YAML is invalid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "delegatd.yaml")
			writeConfiguration(t, path, tc.data)
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want failure")
			}
			got := err.Error()
			if got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "secret-sentinel") || strings.Contains(got, path) {
				t.Fatalf("error leaked sensitive input: %q", got)
			}
		})
	}
}

func TestLoadRejectsUnreadableAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "missing.yaml"))
	if err == nil || err.Error() != "configuration file is unreadable" {
		t.Fatalf("missing error = %v, want unreadable", err)
	}

	path := filepath.Join(dir, "large.yaml")
	writeConfiguration(t, path, strings.Repeat("x", maxConfigurationSize+1))
	_, err = Load(path)
	if err == nil || err.Error() != "configuration exceeds 1 MiB" {
		t.Fatalf("oversized error = %v, want size failure", err)
	}
}

func TestDecodeRequiresMappingAndRejectsUnknownFields(t *testing.T) {
	type adapterConfig struct {
		Path string `yaml:"path"`
	}
	var mapping yaml.Node
	if err := yaml.Unmarshal([]byte("path: ./state.db\n"), &mapping); err != nil {
		t.Fatal(err)
	}
	var decoded adapterConfig
	if err := Decode(*mapping.Content[0], &decoded); err != nil {
		t.Fatalf("Decode(valid) error = %v", err)
	}
	if decoded.Path != "./state.db" {
		t.Fatalf("Path = %q", decoded.Path)
	}

	var unknown yaml.Node
	if err := yaml.Unmarshal([]byte("path: ./state.db\nsecret: sentinel\n"), &unknown); err != nil {
		t.Fatal(err)
	}
	if err := Decode(*unknown.Content[0], &decoded); err == nil {
		t.Fatal("Decode(unknown) error = nil")
	}
	var scalar yaml.Node
	if err := yaml.Unmarshal([]byte("not-a-map\n"), &scalar); err != nil {
		t.Fatal(err)
	}
	if err := Decode(*scalar.Content[0], &decoded); err == nil {
		t.Fatal("Decode(scalar) error = nil")
	}
}

func TestSafeReasonNormalizesNonValidationErrors(t *testing.T) {
	if got := SafeReason(errors.New("path=/tmp/secret")); got != "configuration YAML is invalid" {
		t.Fatalf("SafeReason() = %q", got)
	}
}

func writeConfiguration(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidatesConfiguredResources(t *testing.T) {
	validResource := validConfiguration + `resources:
  api-service:
    kind: repository
    connector: github-main
    config: {}
`
	path := filepath.Join(t.TempDir(), "delegatd.yaml")
	writeConfiguration(t, path, validResource)
	if document, err := Load(path); err != nil || document.Config.Resources["api-service"].Connector != "github-main" {
		t.Fatalf("Load(valid resource) = %#v, %v", document.Config.Resources, err)
	}
	for name, replacement := range map[string]string{
		"invalid resource name": strings.Replace(validResource, "api-service:", "Api-service:", 1),
		"unknown connector":     strings.Replace(validResource, "connector: github-main", "connector: missing", 1),
		"scalar config":         strings.Replace(validResource, "    connector: github-main\n    config: {}\n", "    connector: github-main\n    config: invalid\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "delegatd.yaml")
			writeConfiguration(t, candidate, replacement)
			if _, err := Load(candidate); err == nil {
				t.Fatal("Load() accepted invalid resource configuration")
			}
		})
	}
}
