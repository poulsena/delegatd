package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestArchiveIsDeterministicAndNormalized(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.bin")
	payload := []byte("release payload\n")
	if err := os.WriteFile(input, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	epoch := int64(1700000001)
	modTime := time.Unix(epoch, 0).UTC()

	for _, format := range []string{"tar.gz", "zip"} {
		first := filepath.Join(root, "first."+format)
		second := filepath.Join(root, "second."+format)
		args := []string{
			"archive", "--input", input, "--entry", "delegatd", "--format", format,
			"--epoch", fmt.Sprint(epoch),
		}
		runTool(t, append(append([]string{}, args...), "--output", first)...)
		runTool(t, append(append([]string{}, args...), "--output", second)...)
		firstBytes, err := os.ReadFile(first)
		if err != nil {
			t.Fatal(err)
		}
		secondBytes, err := os.ReadFile(second)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("%s archive changed between identical runs", format)
		}

		switch format {
		case "tar.gz":
			checkTarGz(t, firstBytes, payload, modTime)
		case "zip":
			checkZip(t, firstBytes, payload, modTime)
		}
	}
}

func TestCanonicalizeSPDXReplacesOnlyVolatileFields(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "raw.spdx.json")
	output := filepath.Join(root, "canonical.spdx.json")
	inputJSON := `{"spdxVersion":"SPDX-2.3","creationInfo":{"created":"old","creators":["Tool"],"comment":"keep"},"name":"old","documentNamespace":"old","packages":[{"downloadLocation":"NOASSERTION","versionInfo":1.25}],"documentDescribes":["x"]}`
	if err := os.WriteFile(input, []byte(inputJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	runTool(t, "canonicalize-spdx", "--input", input, "--output", output, "--name", "delegatd_0.0.0_linux_amd64", "--namespace", "https://example.test/sbom/linux-amd64", "--epoch", "1700000000")
	canonical, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "creationInfo": {
    "comment": "keep",
    "created": "2023-11-14T22:13:20Z",
    "creators": [
      "Tool"
    ]
  },
  "documentDescribes": [
    "x"
  ],
  "documentNamespace": "https://example.test/sbom/linux-amd64",
  "name": "delegatd_0.0.0_linux_amd64",
  "packages": [
    {
      "downloadLocation": "NOASSERTION",
      "versionInfo": 1.25
    }
  ],
  "spdxVersion": "SPDX-2.3"
}
`
	if string(canonical) != want {
		t.Fatalf("canonical SPDX mismatch:\n%s", canonical)
	}

	var got map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "delegatd_0.0.0_linux_amd64" || got["documentNamespace"] != "https://example.test/sbom/linux-amd64" {
		t.Fatalf("volatile top-level fields not replaced: %#v", got)
	}
	packages := got["packages"].([]interface{})
	version := packages[0].(map[string]interface{})["versionInfo"]
	if !reflect.DeepEqual(version, json.Number("1.25")) {
		t.Fatalf("JSON number was not preserved: %#v", version)
	}
}

func TestChecksumsAreStableAndSorted(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"z.zip":       []byte("zip"),
		"a.tar.gz":    []byte("tar"),
		"m.spdx.json": []byte(`{"name":"sbom"}`),
		"README.txt":  []byte("ignore"),
		"SHA256SUMS":  []byte("stale checksum\n"),
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := filepath.Join(root, "checksums-first")
	second := filepath.Join(root, "checksums-second")
	runTool(t, "checksums", "--dir", root, "--output", first)
	runTool(t, "checksums", "--dir", root, "--output", second)
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("checksums changed between identical runs")
	}

	var want strings.Builder
	for _, name := range []string{"a.tar.gz", "m.spdx.json", "z.zip"} {
		digest := sha256.Sum256(files[name])
		fmt.Fprintf(&want, "%x  %s\n", digest, name)
	}
	if string(firstBytes) != want.String() {
		t.Fatalf("checksum ordering or format mismatch:\n%s", firstBytes)
	}
}

func TestCommandsRejectUnknownArguments(t *testing.T) {
	if _, err := runToolRaw(t, "checksums", "--dir", t.TempDir(), "--unknown", "value"); err == nil {
		t.Fatal("unknown argument was accepted")
	}
}

func TestCommandsRejectMissingAndInvalidArguments(t *testing.T) {
	cases := [][]string{
		nil,
		{"unknown"},
		{"archive", "--input", "input"},
		{"canonicalize-spdx", "--input", "input"},
		{"checksums", "--dir", t.TempDir()},
		{"archive", "--input", "input", "--output", "output", "--entry", "entry", "--format", "invalid", "--epoch", "0"},
		{"archive", "--input", "input", "--output", "output", "--entry", "entry", "--format", "zip", "--epoch", "not-decimal"},
		{"checksums", "--dir", t.TempDir(), "--output", "output", "--dir", t.TempDir()},
	}
	for index, args := range cases {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			if _, err := runToolRaw(t, args...); err == nil {
				t.Fatal("invalid command arguments were accepted")
			}
		})
	}
}

func TestArchiveAndCanonicalizeRejectUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.bin")
	if err := os.WriteFile(input, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runToolRaw(t, "archive", "--input", input, "--output", filepath.Join(root, "archive.zip"), "--entry", "../escape", "--format", "zip", "--epoch", "0"); err == nil {
		t.Fatal("unsafe archive entry was accepted")
	}
	badJSON := filepath.Join(root, "bad.json")
	if err := os.WriteFile(badJSON, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runToolRaw(t, "canonicalize-spdx", "--input", badJSON, "--output", filepath.Join(root, "canonical.json"), "--name", "name", "--namespace", "https://example.test", "--epoch", "0"); err == nil {
		t.Fatal("invalid SPDX document was accepted")
	}
	multipleJSON := filepath.Join(root, "multiple.json")
	if err := os.WriteFile(multipleJSON, []byte(`{"creationInfo":{}} {"creationInfo":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runToolRaw(t, "canonicalize-spdx", "--input", multipleJSON, "--output", filepath.Join(root, "multiple-canonical.json"), "--name", "name", "--namespace", "https://example.test", "--epoch", "0"); err == nil {
		t.Fatal("multiple SPDX values were accepted")
	}
}

func TestChecksumsRejectsMissingDirectory(t *testing.T) {
	if _, err := runToolRaw(t, "checksums", "--dir", filepath.Join(t.TempDir(), "missing"), "--output", filepath.Join(t.TempDir(), "checksums")); err == nil {
		t.Fatal("missing checksum directory was accepted")
	}
}

func TestAtomicOutputRemainsAbsentAfterWriteFailure(t *testing.T) {
	output := filepath.Join(t.TempDir(), "output")
	err := writeAtomic(output, func(writer io.Writer) error {
		if _, err := io.WriteString(writer, "partial"); err != nil {
			return err
		}
		return errors.New("forced write failure")
	})
	if err == nil {
		t.Fatal("writeAtomic() error = nil")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed atomic output exists: %v", err)
	}
}

func runTool(t *testing.T, args ...string) []byte {
	t.Helper()
	output, err := runToolRaw(t, args...)
	if err != nil {
		t.Fatalf("release tool %v failed: %v\n%s", args, err, output)
	}
	return output
}

func runToolRaw(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	err := execute(args)
	if err != nil {
		return []byte(err.Error()), err
	}
	return nil, nil
}

func checkTarGz(t *testing.T, archive, payload []byte, modTime time.Time) {
	t.Helper()
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if !gzipReader.ModTime.Equal(modTime) || gzipReader.Name != "" || gzipReader.Comment != "" || gzipReader.OS != 255 {
		t.Fatalf("non-normalized gzip header: %#v", gzipReader.Header)
	}
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "delegatd" || header.Mode != 0o755 || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || !header.ModTime.Equal(modTime) || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() {
		t.Fatalf("non-normalized tar header: %#v", header)
	}
	gotPayload, err := io.ReadAll(tarReader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("tar payload mismatch: %q", gotPayload)
	}
	if _, err := tarReader.Next(); err != io.EOF {
		t.Fatalf("expected exactly one tar entry, got %v", err)
	}
}

func checkZip(t *testing.T, archive, payload []byte, modTime time.Time) {
	t.Helper()
	zipReader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zipReader.File) != 1 {
		t.Fatalf("expected exactly one zip entry, got %d", len(zipReader.File))
	}
	file := zipReader.File[0]
	if file.Name != "delegatd" || file.Mode().Perm() != 0o755 || !file.Modified.Equal(modTime) || file.Comment != "" {
		t.Fatalf("non-normalized zip header: %#v", file.FileHeader)
	}
	expectedExtra := make([]byte, 9)
	binary.LittleEndian.PutUint16(expectedExtra[0:2], 0x5455)
	binary.LittleEndian.PutUint16(expectedExtra[2:4], 5)
	expectedExtra[4] = 1
	binary.LittleEndian.PutUint32(expectedExtra[5:9], uint32(modTime.Unix()))
	if !bytes.Equal(file.Extra, expectedExtra) {
		t.Fatalf("unexpected ZIP extended timestamp: %#v", file.Extra)
	}
	reader, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	gotPayload, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("zip payload mismatch: %q", gotPayload)
	}
}
