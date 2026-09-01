package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorCheckUsesVersionOnlyAndValidatesLinuxServer(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
printf '%s' '"29.7.2" "linux"'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	check := NewDoctorCheck("docker-local", Config{})
	detail, failure := check.Probe(context.Background())
	if failure != nil {
		t.Fatalf("failure = %v", failure)
	}
	if detail != "Docker server 29.7.2 (linux)" {
		t.Fatalf("detail = %q", detail)
	}
}

func TestDoctorCheckMapsExecutableProcessAndResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{name: "missing executable", script: "", want: "docker executable was not found"},
		{name: "non-linux", script: "#!/bin/sh\nprintf '%s' '\"29.7.2\" \"darwin\"'\n", want: "Docker server OS is not linux"},
		{name: "malformed", script: "#!/bin/sh\nprintf '%s' 'not-json'\n", want: "Docker version response is invalid"},
		{name: "nonzero", script: "#!/bin/sh\nprintf '%s' 'docker-stderr-sentinel' >&2\nexit 1\n", want: "Docker daemon is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			if tc.script != "" {
				writeExecutable(t, filepath.Join(binDir, "docker"), tc.script)
			}
			t.Setenv("PATH", binDir)
			check := NewDoctorCheck("local", Config{})
			_, failure := check.Probe(context.Background())
			if failure == nil || failure.Error() != tc.want {
				t.Fatalf("failure = %v, want %q", failure, tc.want)
			}
			if strings.Contains(failure.Error(), "sentinel") {
				t.Fatalf("failure leaked child stderr: %q", failure.Error())
			}
		})
	}
}

func TestDoctorCheckBoundsOutputAndHonorsCancellation(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "docker"), `#!/bin/sh
printf '%s' 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
exit 0
`)
	// The script above is intentionally valid-size but malformed; the bounded
	// writer is exercised with a generated oversized script below.
	t.Setenv("PATH", binDir)
	check := NewDoctorCheck("local", Config{})
	if _, failure := check.Probe(context.Background()); failure == nil || failure.Error() != "Docker version response is invalid" {
		t.Fatalf("malformed failure = %v", failure)
	}

	largeOutput := filepath.Join(binDir, "large-output")
	if err := os.WriteFile(largeOutput, []byte(strings.Repeat("x", maxVersionOutputSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "docker"), "#!/bin/sh\n/bin/cat "+largeOutput+"\n")
	if _, failure := check.Probe(context.Background()); failure == nil || failure.Error() != "Docker version response is invalid" {
		t.Fatalf("oversized failure = %v", failure)
	}

	writeExecutable(t, filepath.Join(binDir, "docker"), "#!/bin/sh\nexec /bin/sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, failure := check.Probe(ctx); failure == nil || failure.Error() != "Docker daemon is unavailable" {
		t.Fatalf("timeout failure = %v", failure)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
