package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/bootstrap"
	"github.com/poulsena/delegatd/internal/config"
	"github.com/poulsena/delegatd/internal/doctor"
)

func TestRunDoctorSuccessUsesStableOutputAndExitCode(t *testing.T) {
	checks := []doctor.Check{
		{ID: "connector.github-main", Probe: func(context.Context) (string, *doctor.Failure) {
			return "GitHub App authenticated", nil
		}},
		{ID: "workspace_provider.docker-local", Probe: func(context.Context) (string, *doctor.Failure) {
			return "Docker server 29.7.2 (linux)", nil
		}},
		{ID: "agent_runtime.omp-primary", Probe: func(context.Context) (string, *doctor.Failure) {
			return "OMP RPC protocol 1", nil
		}},
		{ID: "store.sqlite", Probe: func(context.Context) (string, *doctor.Failure) {
			return "SQLite 3.51.0", nil
		}},
	}

	var stdout, stderr strings.Builder
	exit := run(context.Background(), []string{"doctor", "--config", "/tmp/devbox.yaml"}, &stdout, &stderr,
		func(path string) (config.Document, []doctor.Check, *doctor.Failure) {
			if path != "/tmp/devbox.yaml" {
				t.Fatalf("loader path = %q", path)
			}
			return config.Document{Config: config.Config{Version: 1}}, checks, nil
		})

	if exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}
	want := "PASS config: schema version 1\n" +
		"PASS connector.github-main: GitHub App authenticated\n" +
		"PASS workspace_provider.docker-local: Docker server 29.7.2 (linux)\n" +
		"PASS agent_runtime.omp-primary: OMP RPC protocol 1\n" +
		"PASS store.sqlite: SQLite 3.51.0\n" +
		"PASS doctor: 4 checks passed\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDoctorUsesRealConfigLoaderAndBootstrap(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "delegatd.yaml")
	configContents := `version: 1
store:
  kind: sqlite
  config:
    path: ./state.db
connectors:
  github-main:
    kind: github
    config:
      app_id: 123456
      private_key_file: ./github-private-key-secret.pem
workspace_providers:
  docker-local:
    kind: docker
    config: {}
agent_runtimes:
  omp-primary:
    kind: omp
    config: {}
`
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	exit := run(context.Background(), []string{
		"doctor", "--config", configPath, "--timeout", "100ms",
	}, &stdout, &stderr, bootstrap.LoadDoctor)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	want := "PASS config: schema version 1\n" +
		"FAIL connector.github-main: private key file is unreadable\n" +
		"FAIL workspace_provider.docker-local: docker executable was not found\n" +
		"FAIL agent_runtime.omp-primary: omp executable was not found\n" +
		"FAIL store.sqlite: SQLite database does not exist\n" +
		"FAIL doctor: 4 of 4 checks failed\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if strings.Contains(stdout.String(), "github-private-key-secret.pem") {
		t.Fatalf("stdout leaked the secret reference: %q", stdout.String())
	}

	invalidPath := filepath.Join(configDir, "invalid.yaml")
	if err := os.WriteFile(invalidPath, []byte("version: 1\nunknown: secret-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := run(context.Background(), []string{"doctor", "--config", invalidPath}, &stdout, &stderr, bootstrap.LoadDoctor); exit != 1 {
		t.Fatalf("invalid config exit = %d, want 1", exit)
	}
	if got, want := stdout.String(), "FAIL config: configuration YAML is invalid\nFAIL doctor: configuration invalid\n"; got != want {
		t.Fatalf("invalid config stdout = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String(), "secret-sentinel") {
		t.Fatalf("invalid config leaked YAML value: %q", stdout.String())
	}
}

func TestRunDoctorReportsAllFailuresWithoutLeakingCauses(t *testing.T) {
	var calls atomic.Int32
	checks := []doctor.Check{
		{ID: "connector.github-main", Probe: func(context.Context) (string, *doctor.Failure) {
			calls.Add(1)
			return "", doctor.NewFailure("dependency unavailable", errors.New("private-key-secret-sentinel"))
		}},
		{ID: "workspace_provider.docker-local", Probe: func(context.Context) (string, *doctor.Failure) {
			calls.Add(1)
			return "", doctor.NewFailure("dependency unavailable", errors.New("docker-stderr-sentinel"))
		}},
		{ID: "agent_runtime.omp-primary", Probe: func(context.Context) (string, *doctor.Failure) {
			calls.Add(1)
			return "", doctor.NewFailure("dependency unavailable", errors.New("omp-output-sentinel"))
		}},
		{ID: "store.sqlite", Probe: func(context.Context) (string, *doctor.Failure) {
			calls.Add(1)
			return "", doctor.NewFailure("dependency unavailable", errors.New("sqlite-dsn-sentinel"))
		}},
	}

	var stdout, stderr strings.Builder
	exit := run(context.Background(), []string{"doctor", "--config", "devbox.yaml"}, &stdout, &stderr,
		func(string) (config.Document, []doctor.Check, *doctor.Failure) {
			return config.Document{Config: config.Config{Version: 1}}, checks, nil
		})

	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if calls.Load() != int32(len(checks)) {
		t.Fatalf("probe calls = %d, want %d", calls.Load(), len(checks))
	}
	want := "PASS config: schema version 1\n" +
		"FAIL connector.github-main: dependency unavailable\n" +
		"FAIL workspace_provider.docker-local: dependency unavailable\n" +
		"FAIL agent_runtime.omp-primary: dependency unavailable\n" +
		"FAIL store.sqlite: dependency unavailable\n" +
		"FAIL doctor: 4 of 4 checks failed\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String()+stderr.String(), "sentinel") {
		t.Fatalf("diagnostics leaked an underlying cause: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunDoctorWaitsForCancellationAndReportsFailure(t *testing.T) {
	checks := []doctor.Check{
		{ID: "connector.github-main", Probe: func(ctx context.Context) (string, *doctor.Failure) {
			<-ctx.Done()
			return "", doctor.NewFailure("dependency unavailable", ctx.Err())
		}},
	}
	var stdout, stderr strings.Builder
	started := time.Now()
	exit := run(context.Background(), []string{"doctor", "--config", "devbox.yaml", "--timeout", "10ms"}, &stdout, &stderr,
		func(string) (config.Document, []doctor.Check, *doctor.Failure) {
			return config.Document{}, checks, nil
		})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if !strings.Contains(stdout.String(), "FAIL connector.github-main: dependency unavailable\n") {
		t.Fatalf("stdout = %q, want dependency failure", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunReportsConfigurationFailureWithoutProbing(t *testing.T) {
	var probes atomic.Int32
	var stdout, stderr strings.Builder
	exit := run(context.Background(), []string{"doctor", "--config", "secret-config.yaml"}, &stdout, &stderr,
		func(string) (config.Document, []doctor.Check, *doctor.Failure) {
			return config.Document{}, []doctor.Check{{
				ID: "should-not-run",
				Probe: func(context.Context) (string, *doctor.Failure) {
					probes.Add(1)
					return "", nil
				},
			}}, doctor.NewFailure("configuration file is unreadable", errors.New("config-secret-sentinel"))
		})
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if probes.Load() != 0 {
		t.Fatalf("probes = %d, want 0", probes.Load())
	}
	if got, want := stdout.String(), "FAIL config: configuration file is unreadable\nFAIL doctor: configuration invalid\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String(), "sentinel") || stderr.Len() != 0 {
		t.Fatalf("diagnostics leaked failure data: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidTimeout(t *testing.T) {
	loader := func(string) (config.Document, []doctor.Check, *doctor.Failure) {
		t.Fatal("loader should not run")
		return config.Document{}, nil, nil
	}
	for _, timeout := range []string{"0", "-1s", "not-a-duration"} {
		t.Run(timeout, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if got := run(context.Background(), []string{"doctor", "--config", "x", "--timeout", timeout}, &stdout, &stderr, loader); got != 2 {
				t.Fatalf("exit = %d, want 2", got)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), usage) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunDoctorCLIUsageAndHelp(t *testing.T) {
	loader := func(string) (config.Document, []doctor.Check, *doctor.Failure) {
		t.Fatal("loader should not run")
		return config.Document{}, nil, nil
	}

	for _, tc := range []struct {
		name string
		args []string
		exit int
	}{
		{name: "missing config", args: []string{"doctor"}, exit: 2},
		{name: "unknown subcommand", args: []string{"diagnose"}, exit: 2},
		{name: "positional argument", args: []string{"doctor", "--config", "x", "extra"}, exit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if got := run(context.Background(), tc.args, &stdout, &stderr, loader); got != tc.exit {
				t.Fatalf("exit = %d, want %d", got, tc.exit)
			}
			if !strings.Contains(stderr.String(), "usage: delegatd doctor --config FILE [--timeout DURATION]") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}

	var stdout, stderr strings.Builder
	if got := run(context.Background(), []string{"doctor", "--help"}, &stdout, &stderr, loader); got != 0 {
		t.Fatalf("help exit = %d, want 0", got)
	}
	if !strings.Contains(stdout.String(), "usage: delegatd doctor --config FILE [--timeout DURATION]") {
		t.Fatalf("help stdout = %q, want usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("help stderr = %q, want empty", stderr.String())
	}
}
