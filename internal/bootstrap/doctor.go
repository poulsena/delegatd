package bootstrap

import (
	"context"
	"sort"

	"github.com/poulsena/delegatd/internal/config"
	"github.com/poulsena/delegatd/internal/connector/github"
	"github.com/poulsena/delegatd/internal/doctor"
	"github.com/poulsena/delegatd/internal/runtime/omp"
	"github.com/poulsena/delegatd/internal/store/sqlite"
	"github.com/poulsena/delegatd/internal/workspace/docker"
)

// LoadDoctor loads the deployment document and constructs one check for every
// configured named adapter instance. Adapter-owned configuration failures are
// represented as failed checks so independent probes still run.
func LoadDoctor(path string) (config.Document, []doctor.Check, *doctor.Failure) {
	document, err := config.Load(path)
	if err != nil {
		return document, nil, doctor.NewFailure(config.SafeReason(err), err)
	}

	checks := make([]doctor.Check, 0, 1+len(document.Config.Connectors)+len(document.Config.WorkspaceProviders)+len(document.Config.AgentRuntimes))
	for _, name := range sortedNames(document.Config.Connectors) {
		instance := document.Config.Connectors[name]
		checkID := "connector." + name
		switch instance.Kind {
		case "github":
			var adapterConfig github.Config
			if err := config.Decode(instance.Config, &adapterConfig); err != nil {
				checks = append(checks, failedCheck(checkID, checkID+" configuration contains unknown or invalid fields", err))
				continue
			}
			checks = append(checks, github.NewDoctorCheck(name, adapterConfig, document.Dir))
		default:
			checks = append(checks, failedCheck(checkID, checkID+" adapter kind is unsupported", nil))
		}
	}
	for _, name := range sortedNames(document.Config.WorkspaceProviders) {
		instance := document.Config.WorkspaceProviders[name]
		checkID := "workspace_provider." + name
		switch instance.Kind {
		case "docker":
			var adapterConfig docker.Config
			if err := config.Decode(instance.Config, &adapterConfig); err != nil {
				checks = append(checks, failedCheck(checkID, checkID+" configuration contains unknown or invalid fields", err))
				continue
			}
			checks = append(checks, docker.NewDoctorCheck(name, adapterConfig))
		default:
			checks = append(checks, failedCheck(checkID, checkID+" adapter kind is unsupported", nil))
		}
	}
	for _, name := range sortedNames(document.Config.AgentRuntimes) {
		instance := document.Config.AgentRuntimes[name]
		checkID := "agent_runtime." + name
		switch instance.Kind {
		case "omp":
			var adapterConfig omp.Config
			if err := config.Decode(instance.Config, &adapterConfig); err != nil {
				checks = append(checks, failedCheck(checkID, checkID+" configuration contains unknown or invalid fields", err))
				continue
			}
			checks = append(checks, omp.NewDoctorCheck(name, adapterConfig))
		default:
			checks = append(checks, failedCheck(checkID, checkID+" adapter kind is unsupported", nil))
		}
	}

	storeID := "store." + document.Config.Store.Kind
	if document.Config.Store.Kind == "sqlite" {
		var adapterConfig sqlite.Config
		if err := config.Decode(document.Config.Store.Config, &adapterConfig); err != nil {
			checks = append(checks, failedCheck(storeID, storeID+" configuration contains unknown or invalid fields", err))
		} else {
			checks = append(checks, sqlite.NewDoctorCheck(adapterConfig, document.Dir))
		}
	} else {
		checks = append(checks, failedCheck(storeID, storeID+" adapter kind is unsupported", nil))
	}
	return document, checks, nil
}

func sortedNames(instances map[string]config.Instance) []string {
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func failedCheck(id, reason string, cause error) doctor.Check {
	return doctor.Check{
		ID: id,
		Probe: func(_ context.Context) (string, *doctor.Failure) {
			return "", doctor.NewFailure(reason, cause)
		},
	}
}
