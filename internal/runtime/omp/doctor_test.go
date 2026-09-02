//go:build !windows

package omp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/poulsena/delegatd/internal/doctor"
)

func TestDoctorCheckRequiresReadyProtocolAndDrainsNotifications(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "omp"), `#!/bin/sh
printf '%s\n' '{"type":"ready","protocolVersion":1}'
cat >/dev/null
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	check := NewDoctorCheck("omp-primary", Config{})
	if detail, failure := check.Probe(context.Background()); failure != nil || detail != "OMP RPC protocol 1" {
		t.Fatalf("detail=%q failure=%v", detail, failure)
	}

	flood := filepath.Join(binDir, "notifications")
	if err := os.WriteFile(flood, []byte(strings.Repeat("notification\n", 200000)), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "omp"), "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"ready\",\"protocolVersion\":1}'\n/bin/cat "+flood+"\ncat >/dev/null\n")
	started := time.Now()
	if detail, failure := check.Probe(context.Background()); failure != nil || detail != "OMP RPC protocol 1" {
		t.Fatalf("flood detail=%q failure=%v", detail, failure)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("flood probe took %s", elapsed)
	}
}
func TestDoctorCheckDoesNotWaitForDescendantHoldingStdout(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "omp"), `#!/bin/sh
printf '%s\n' '{"type":"ready","protocolVersion":1}'
(/bin/sleep 5) &
exit 0
`)
	t.Setenv("PATH", binDir)
	check := NewDoctorCheck("omp-primary", Config{})
	result := make(chan struct {
		detail  string
		failure *doctor.Failure
	}, 1)
	go func() {
		detail, failure := check.Probe(context.Background())
		result <- struct {
			detail  string
			failure *doctor.Failure
		}{detail: detail, failure: failure}
	}()
	select {
	case got := <-result:
		if got.failure != nil || got.detail != "OMP RPC protocol 1" {
			t.Fatalf("detail=%q failure=%v", got.detail, got.failure)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe waited for descendant stdout to close")
	}
}

func TestDoctorCheckMapsOMPFailuresWithoutChildOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{name: "missing executable", script: "", want: "omp executable was not found"},
		{name: "malformed ready", script: "#!/bin/sh\nprintf '%s\\n' 'output-secret-sentinel'\n", want: "OMP RPC did not become ready"},
		{name: "unsupported protocol", script: "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"ready\",\"protocolVersion\":2}'\n", want: "OMP RPC protocol 1 is unavailable"},
		{name: "nonzero shutdown", script: "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"ready\",\"protocolVersion\":1}'\nexit 1\n", want: "OMP RPC did not shut down cleanly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			if tc.script != "" {
				writeExecutable(t, filepath.Join(binDir, "omp"), tc.script)
			}
			t.Setenv("PATH", binDir)
			check := NewDoctorCheck("primary", Config{})
			_, failure := check.Probe(context.Background())
			if failure == nil || failure.Error() != tc.want {
				t.Fatalf("failure = %v, want %q", failure, tc.want)
			}
			if strings.Contains(failure.Error(), "sentinel") {
				t.Fatalf("failure leaked child output: %q", failure.Error())
			}
		})
	}
}

func TestDoctorCheckKillsHungOMPOnContextCancellation(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "omp"), "#!/bin/sh\nexec /bin/sleep 10\n")
	t.Setenv("PATH", binDir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	check := NewDoctorCheck("primary", Config{})
	if _, failure := check.Probe(ctx); failure == nil || failure.Error() != "OMP RPC did not become ready" {
		t.Fatalf("failure = %v", failure)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
