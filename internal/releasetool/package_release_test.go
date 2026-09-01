//go:build release_integration && !windows

package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPackageReleaseRejectsMissingEnvironment(t *testing.T) {
	base := packageValues(t)
	for _, name := range []string{"VERSION", "GOOS", "GOARCH", "SOURCE_DATE_EPOCH", "OUT_DIR"} {
		values := clonePackageValues(base)
		delete(values, name)
		if output, err := runPackage(t, values); err == nil {
			t.Fatalf("missing %s was accepted: %s", name, output)
		}
	}
}

func TestPackageReleaseRejectsVersionEpochAndTarget(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value string
	}{
		{name: "leading zero version", field: "VERSION", value: "01.0.0"},
		{name: "missing patch version", field: "VERSION", value: "1.0"},
		{name: "prerelease version", field: "VERSION", value: "1.0.0-beta"},
		{name: "negative epoch", field: "SOURCE_DATE_EPOCH", value: "-1"},
		{name: "fractional epoch", field: "SOURCE_DATE_EPOCH", value: "1.2"},
		{name: "non-decimal epoch", field: "SOURCE_DATE_EPOCH", value: "epoch"},
		{name: "unsupported operating system", field: "GOOS", value: "freebsd"},
		{name: "unsupported architecture", field: "GOARCH", value: "386"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := packageValues(t)
			values[testCase.field] = testCase.value
			if output, err := runPackage(t, values); err == nil {
				t.Fatalf("invalid %s was accepted: %s", testCase.field, output)
			}
		})
	}
}

func TestPackageReleaseRejectsSymlinkedOutputDirectory(t *testing.T) {
	realDirectory := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "out")
	if err := os.Symlink(realDirectory, link); err != nil {
		t.Fatal(err)
	}
	values := packageValues(t)
	values["OUT_DIR"] = link
	if output, err := runPackage(t, values); err == nil {
		t.Fatalf("symlinked OUT_DIR was accepted: %s", output)
	}
}

func TestPackageReleaseRejectsOutputCollisions(t *testing.T) {
	for _, name := range []string{
		"delegatd_0.0.0_windows_amd64.zip",
		"delegatd_0.0.0_windows_amd64.spdx.json",
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			if err := os.WriteFile(filepath.Join(out, name), []byte("existing"), 0o644); err != nil {
				t.Fatal(err)
			}
			values := packageValues(t)
			values["OUT_DIR"] = out
			if output, err := runPackage(t, values); err == nil {
				t.Fatalf("existing %s was accepted: %s", name, output)
			}
		})
	}
}

func TestPackageReleaseNamesWindowsBinary(t *testing.T) {
	out := t.TempDir()
	values := packageValues(t)
	values["OUT_DIR"] = out
	output, err := runPackage(t, values)
	if err != nil {
		t.Fatalf("package release failed: %v\n%s", err, output)
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
	}
	sort.Strings(gotNames)
	wantNames := []string{
		"delegatd_0.0.0_windows_amd64.spdx.json",
		"delegatd_0.0.0_windows_amd64.zip",
	}
	if strings.Join(gotNames, "\n") != strings.Join(wantNames, "\n") {
		t.Fatalf("unexpected package files: %v", gotNames)
	}

	archivePath := filepath.Join(out, "delegatd_0.0.0_windows_amd64.zip")
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 {
		t.Fatalf("expected one Windows archive entry, got %d", len(archive.File))
	}
	file := archive.File[0]
	if file.Name != "delegatd_0.0.0_windows_amd64.exe" {
		t.Fatalf("unexpected Windows archive entry: %q", file.Name)
	}
	reader, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 {
		t.Fatal("Windows archive entry is empty")
	}
}

func packageValues(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"VERSION":           "0.0.0",
		"GOOS":              "windows",
		"GOARCH":            "amd64",
		"SOURCE_DATE_EPOCH": "1700000000",
		"OUT_DIR":           t.TempDir(),
	}
}

func clonePackageValues(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func runPackage(t *testing.T, values map[string]string) ([]byte, error) {
	t.Helper()
	root := repositoryRoot(t)
	command := exec.Command(filepath.Join(root, "scripts", "package-release.sh"))
	command.Dir = root
	command.Env = packageEnvironment(values)
	return command.CombinedOutput()
}

var packageEnvironmentKeys = map[string]struct{}{
	"VERSION":           {},
	"GOOS":              {},
	"GOARCH":            {},
	"SOURCE_DATE_EPOCH": {},
	"OUT_DIR":           {},
}

func packageEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		separator := strings.IndexByte(entry, '=')
		if separator < 0 {
			continue
		}
		if _, packagingVariable := packageEnvironmentKeys[entry[:separator]]; packagingVariable {
			continue
		}
		environment = append(environment, entry)
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository root")
		}
		directory = parent
	}
}
