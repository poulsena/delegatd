package config

import (
	"reflect"
	"testing"

	"github.com/poulsena/delegatd/internal/domain"
)

func TestDecodeRepositoryNormalizesVersionOneConfiguration(t *testing.T) {
	data := []byte(`version: 1
agent:
  instructions:
    - AGENTS.md
policy:
  actions:
    change_request.open: allow
  protected_paths:
    - .github/workflows/**
validation:
  required:
    - name: tests
      run: printf '$HOME'
      timeout: 90s
`)
	got, err := DecodeRepository(data)
	if err != nil {
		t.Fatalf("DecodeRepository() error = %v", err)
	}
	if !reflect.DeepEqual(got.Agent.Instructions, []string{"AGENTS.md"}) {
		t.Fatalf("instructions = %#v", got.Agent.Instructions)
	}
	if got.Validation.Required[0].Timeout != "1m30s" {
		t.Fatalf("timeout = %q", got.Validation.Required[0].Timeout)
	}
	if got.Validation.Required[0].Run != "printf '$HOME'" {
		t.Fatalf("run = %q", got.Validation.Required[0].Run)
	}
	if got.Policy.Actions["change_request.open"] != domain.PolicyAllow {
		t.Fatalf("policy = %#v", got.Policy)
	}
}

func TestDecodeRepositoryMaterializesEmptySectionsAndRejectsUnsafeDocuments(t *testing.T) {
	got, err := DecodeRepository([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("DecodeRepository(empty) error = %v", err)
	}
	if got.Agent.Instructions == nil || got.Policy.Actions == nil || got.Policy.ProtectedPaths == nil || got.Validation.Required == nil {
		t.Fatalf("empty sections were not materialized: %#v", got)
	}
	for name, data := range map[string]string{
		"unknown field":        "version: 1\nunknown: value\n",
		"alias":                "version: &version 1\nagent: {instructions: [*version]}\n",
		"duplicate validation": "version: 1\nvalidation:\n  required:\n    - {name: tests, run: go test, timeout: 1m}\n    - {name: tests, run: go vet, timeout: 1m}\n",
		"unsafe instruction":   "version: 1\nagent:\n  instructions: [../AGENTS.md]\n",
		"bad timeout":          "version: 1\nvalidation:\n  required:\n    - {name: tests, run: go test, timeout: 0s}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRepository([]byte(data)); err == nil {
				t.Fatal("DecodeRepository() error = nil")
			}
		})
	}
}
