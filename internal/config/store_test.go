package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStoreIgnoresUnrelatedDeploymentDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "show.yaml")
	data := `version: 1
store:
  kind: sqlite
  config:
    path: ./state.db
connectors:
  broken: [not-a-mapping]
resources:
  invalid: {unknown: value}
policy: [not-a-policy]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore() error = %v", err)
	}
	if got.Version != 1 || got.Dir != dir || got.Store.Kind != "sqlite" || got.Store.Config.Kind == 0 {
		t.Fatalf("store document = %#v", got)
	}
}

func TestLoadStoreRejectsInvalidStoreProjection(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"missing store":       "version: 1\n",
		"wrong version":       "version: 2\nstore: {kind: sqlite, config: {path: state.db}}\n",
		"unknown store field": "version: 1\nstore: {kind: sqlite, config: {path: state.db}, extra: value}\n",
		"interpolation":       "version: 1\nstore: {kind: sqlite, config: {path: ${STATE_DB}}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".yaml")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadStore(path); err == nil {
				t.Fatal("LoadStore() error = nil")
			}
		})
	}
}
