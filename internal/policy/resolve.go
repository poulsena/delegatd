package policy

import (
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/poulsena/delegatd/internal/domain"
)

var actionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// NormalizeRequest validates and deep-copies a policy request.
func NormalizeRequest(request domain.PolicyRequest) (domain.PolicyRequest, error) {
	actions := make(map[string]domain.PolicyDecision, len(request.Actions))
	for name, decision := range request.Actions {
		if !actionNamePattern.MatchString(name) {
			return domain.PolicyRequest{}, errors.New("invalid policy action name")
		}
		if decision != domain.PolicyAllow && decision != domain.PolicyDeny {
			return domain.PolicyRequest{}, errors.New("invalid policy decision")
		}
		actions[name] = decision
	}

	protectedPaths := make([]string, 0, len(request.ProtectedPaths))
	seen := make(map[string]struct{}, len(request.ProtectedPaths))
	for _, raw := range request.ProtectedPaths {
		pattern, err := NormalizeProtectedPath(raw)
		if err != nil {
			return domain.PolicyRequest{}, err
		}
		if _, exists := seen[pattern]; exists {
			return domain.PolicyRequest{}, errors.New("duplicate protected path")
		}
		seen[pattern] = struct{}{}
		protectedPaths = append(protectedPaths, pattern)
	}
	return domain.PolicyRequest{Actions: actions, ProtectedPaths: protectedPaths}, nil
}

// NormalizeInstructionPath validates a repository-relative literal path.
func NormalizeInstructionPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, "\\\x00*?[") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return "", errors.New("invalid instruction path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("invalid instruction path")
		}
	}
	return value, nil
}

// NormalizeProtectedPath validates a repository-relative slash glob. A double
// star is a complete segment and matches zero or more complete path segments.
func NormalizeProtectedPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return "", errors.New("invalid protected path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("invalid protected path")
		}
		if segment == "**" {
			continue
		}
		if strings.Contains(segment, "**") {
			return "", errors.New("invalid protected path")
		}
		if _, err := path.Match(segment, ""); err != nil {
			return "", errors.New("invalid protected path")
		}
	}
	return value, nil
}

// Resolve computes the effective policy intersection. Missing actions are
// denied and only actions explicitly granted by every layer are allowed.
func Resolve(operator, repository domain.PolicyRequest, taskAllowedActions map[string]struct{}) domain.EffectivePolicy {
	actions := make(map[string]domain.PolicyDecision, len(operator.Actions)+len(repository.Actions))
	for name := range operator.Actions {
		actions[name] = domain.PolicyDeny
	}
	for name := range repository.Actions {
		actions[name] = domain.PolicyDeny
	}
	for name := range actions {
		if operator.Actions[name] == domain.PolicyAllow &&
			repository.Actions[name] == domain.PolicyAllow {
			if _, allowed := taskAllowedActions[name]; allowed {
				actions[name] = domain.PolicyAllow
			}
		}
	}

	protected := make(map[string]struct{}, len(operator.ProtectedPaths)+len(repository.ProtectedPaths))
	for _, value := range operator.ProtectedPaths {
		protected[value] = struct{}{}
	}
	for _, value := range repository.ProtectedPaths {
		protected[value] = struct{}{}
	}
	protectedPaths := make([]string, 0, len(protected))
	for value := range protected {
		protectedPaths = append(protectedPaths, value)
	}
	sort.Strings(protectedPaths)
	return domain.EffectivePolicy{
		Version:        1,
		DefaultAction:  domain.PolicyDeny,
		Actions:        actions,
		ProtectedPaths: protectedPaths,
	}
}
