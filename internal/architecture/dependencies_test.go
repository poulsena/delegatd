package architecture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/poulsena/delegatd"

var concreteAdapterPrefixes = []string{
	modulePath + "/internal/connector/",
	modulePath + "/internal/runtime/",
	modulePath + "/internal/workspace/",
	modulePath + "/internal/store/",
}

type packageMetadata struct {
	ImportPath string
	Imports    []string
}

func validatePackageGraph(packages []packageMetadata) error {
	for _, pkg := range packages {
		for _, imported := range pkg.Imports {
			adapterRoot, concrete := concreteAdapterRoot(imported)
			if !concrete || permittedAdapterImporter(pkg.ImportPath, adapterRoot) {
				continue
			}
			return fmt.Errorf("forbidden concrete adapter import: importer %s imports %s", pkg.ImportPath, imported)
		}
	}
	return nil
}

func concreteAdapterRoot(importPath string) (string, bool) {
	for _, prefix := range concreteAdapterPrefixes {
		if !strings.HasPrefix(importPath, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(importPath, prefix)
		if remainder == "" {
			continue
		}
		if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
			remainder = remainder[:slash]
		}
		return prefix + remainder, true
	}
	return "", false
}

func permittedAdapterImporter(importer, adapterRoot string) bool {
	if importer == modulePath+"/internal/bootstrap" || importer == modulePath+"/cmd/delegatd" {
		return true
	}
	return importer == adapterRoot || strings.HasPrefix(importer, adapterRoot+"/")
}

func TestConcreteAdaptersStayOutsideCoreRejectsForbiddenImport(t *testing.T) {
	importer := modulePath + "/internal/domain"
	forbidden := modulePath + "/internal/connector/github"
	err := validatePackageGraph([]packageMetadata{{
		ImportPath: importer,
		Imports:    []string{forbidden},
	}})
	if err == nil {
		t.Fatal("expected a forbidden adapter import to be rejected")
	}
	if !strings.Contains(err.Error(), importer) || !strings.Contains(err.Error(), forbidden) {
		t.Fatalf("error %q does not name importer and forbidden import", err)
	}
}

func TestConcreteAdaptersStayOutsideCore(t *testing.T) {
	root := repositoryRoot(t)
	command := exec.Command("go", "list", "-json", "./...")
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, stderr.String())
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	packages := make([]packageMetadata, 0)
	for {
		var pkg packageMetadata
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		packages = append(packages, pkg)
	}
	if err := validatePackageGraph(packages); err != nil {
		t.Fatal(err)
	}
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
