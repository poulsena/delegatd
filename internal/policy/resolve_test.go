package policy

import (
	"reflect"
	"testing"

	"github.com/poulsena/delegatd/internal/domain"
)

func TestNormalizeRequestCopiesAndValidatesPolicy(t *testing.T) {
	request := domain.PolicyRequest{
		Actions: map[string]domain.PolicyDecision{
			"change_request.open": domain.PolicyAllow,
		},
		ProtectedPaths: []string{" infra/** ", "infra/**"},
	}
	got, err := NormalizeRequest(request)
	if err == nil {
		t.Fatal("NormalizeRequest() error = nil for duplicate path")
	}

	request.ProtectedPaths = []string{" infra/** ", "src/*.go"}
	got, err = NormalizeRequest(request)
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	want := []string{"infra/**", "src/*.go"}
	if !reflect.DeepEqual(got.ProtectedPaths, want) {
		t.Fatalf("protected paths = %#v, want %#v", got.ProtectedPaths, want)
	}
	request.ProtectedPaths[0] = "changed"
	if got.ProtectedPaths[0] != "infra/**" {
		t.Fatal("NormalizeRequest() retained caller slice")
	}
}

func TestResolveIntersectsActionsAndSortsProtectedPaths(t *testing.T) {
	got := Resolve(
		domain.PolicyRequest{
			Actions:        map[string]domain.PolicyDecision{"a.action": domain.PolicyAllow, "b.action": domain.PolicyAllow},
			ProtectedPaths: []string{"z/**", "shared"},
		},
		domain.PolicyRequest{
			Actions:        map[string]domain.PolicyDecision{"a.action": domain.PolicyAllow, "b.action": domain.PolicyDeny, "c.action": domain.PolicyAllow},
			ProtectedPaths: []string{"a/**", "shared"},
		},
		map[string]struct{}{"a.action": {}},
	)
	wantActions := map[string]domain.PolicyDecision{
		"a.action": domain.PolicyAllow,
		"b.action": domain.PolicyDeny,
		"c.action": domain.PolicyDeny,
	}
	if !reflect.DeepEqual(got.Actions, wantActions) {
		t.Fatalf("actions = %#v, want %#v", got.Actions, wantActions)
	}
	if !reflect.DeepEqual(got.ProtectedPaths, []string{"a/**", "shared", "z/**"}) {
		t.Fatalf("protected paths = %#v", got.ProtectedPaths)
	}
	if got.DefaultAction != domain.PolicyDeny || got.Version != 1 {
		t.Fatalf("effective policy = %#v", got)
	}
}

func TestNormalizeInstructionPathRejectsUnsafePaths(t *testing.T) {
	if got, err := NormalizeInstructionPath(" docs/AGENTS.md "); err != nil || got != "docs/AGENTS.md" {
		t.Fatalf("valid instruction path = %q, %v", got, err)
	}
	for _, value := range []string{"", "/absolute", "../parent", "docs//file", "docs/./file", "docs/../file", "docs\\file", "docs/*.md", "docs\x00file", "docs/"} {
		if _, err := NormalizeInstructionPath(value); err == nil {
			t.Fatalf("NormalizeInstructionPath(%q) error = nil", value)
		}
	}
}

func TestNormalizeRequestRejectsInvalidActionsDecisionsAndPaths(t *testing.T) {
	for name, request := range map[string]domain.PolicyRequest{
		"action name": {Actions: map[string]domain.PolicyDecision{"Invalid": domain.PolicyAllow}},
		"decision":    {Actions: map[string]domain.PolicyDecision{"task.run": "unknown"}},
		"path":        {ProtectedPaths: []string{"src//file"}},
		"duplicate":   {ProtectedPaths: []string{"src/**", "src/**"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeRequest(request); err == nil {
				t.Fatal("invalid policy request was accepted")
			}
		})
	}
}
