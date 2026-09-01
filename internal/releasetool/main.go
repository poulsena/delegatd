package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: releasetool archive|canonicalize-spdx|checksums")
	}
	switch args[0] {
	case "archive":
		return runArchive(args[1:])
	case "canonicalize-spdx":
		return runCanonicalizeSPDX(args[1:])
	case "checksums":
		return runChecksums(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runArchive(args []string) error {
	options, err := parseOptions(args, "--input", "--output", "--entry", "--format", "--epoch")
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	epoch, err := parseEpoch(options["--epoch"])
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if options["--format"] != "tar.gz" && options["--format"] != "zip" {
		return fmt.Errorf("archive: unsupported format %q", options["--format"])
	}
	if err := validateArchiveEntry(options["--entry"]); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	input, err := os.Open(options["--input"])
	if err != nil {
		return fmt.Errorf("archive: open input: %w", err)
	}
	defer input.Close()
	inputInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("archive: stat input: %w", err)
	}
	if !inputInfo.Mode().IsRegular() {
		return fmt.Errorf("archive: input is not a regular file")
	}

	return writeAtomic(options["--output"], func(output io.Writer) error {
		switch options["--format"] {
		case "tar.gz":
			return writeTarGz(output, input, inputInfo.Size(), options["--entry"], epoch)
		case "zip":
			return writeZip(output, input, options["--entry"], epoch)
		default:
			return errors.New("unsupported archive format")
		}
	})
}

func writeTarGz(output io.Writer, input io.Reader, size int64, entry string, epoch int64) error {
	gzipWriter := gzip.NewWriter(output)
	gzipWriter.Header.ModTime = time.Unix(epoch, 0).UTC()
	gzipWriter.Header.Name = ""
	gzipWriter.Header.Comment = ""
	gzipWriter.Header.OS = 255

	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name:       entry,
		Mode:       0o755,
		Size:       size,
		ModTime:    time.Unix(epoch, 0).UTC(),
		Typeflag:   tar.TypeReg,
		Uid:        0,
		Gid:        0,
		Uname:      "",
		Gname:      "",
		AccessTime: time.Time{},
		ChangeTime: time.Time{},
		Format:     tar.FormatUSTAR,
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.Copy(tarWriter, input); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gzipWriter.Close()
}

func writeZip(output io.Writer, input io.Reader, entry string, epoch int64) error {
	zipWriter := zip.NewWriter(output)
	fileHeader := &zip.FileHeader{
		Name:     entry,
		Method:   zip.Store,
		Modified: time.Unix(epoch, 0).UTC(),
		Extra:    nil,
		Comment:  "",
	}
	fileHeader.SetMode(0o755)
	fileHeader.CreatorVersion = (3 << 8) | 20
	fileHeader.ReaderVersion = 20
	fileHeader.Flags = 0
	fileWriter, err := zipWriter.CreateHeader(fileHeader)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fileWriter, input); err != nil {
		return err
	}
	return zipWriter.Close()
}

func runCanonicalizeSPDX(args []string) error {
	options, err := parseOptions(args, "--input", "--output", "--name", "--namespace", "--epoch")
	if err != nil {
		return fmt.Errorf("canonicalize-spdx: %w", err)
	}
	epoch, err := parseEpoch(options["--epoch"])
	if err != nil {
		return fmt.Errorf("canonicalize-spdx: %w", err)
	}
	input, err := os.Open(options["--input"])
	if err != nil {
		return fmt.Errorf("canonicalize-spdx: open input: %w", err)
	}
	defer input.Close()

	decoder := json.NewDecoder(input)
	decoder.UseNumber()
	var document map[string]interface{}
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("canonicalize-spdx: decode input: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("canonicalize-spdx: input contains more than one JSON value")
		}
		return fmt.Errorf("canonicalize-spdx: decode trailing input: %w", err)
	}
	creationInfo, ok := document["creationInfo"].(map[string]interface{})
	if !ok {
		return errors.New("canonicalize-spdx: creationInfo must be an object")
	}
	document["name"] = options["--name"]
	document["documentNamespace"] = options["--namespace"]
	creationInfo["created"] = time.Unix(epoch, 0).UTC().Format(time.RFC3339)

	return writeAtomic(options["--output"], func(output io.Writer) error {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(document)
	})
}

func runChecksums(args []string) error {
	options, err := parseOptions(args, "--dir", "--output")
	if err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	entries, err := os.ReadDir(options["--dir"])
	if err != nil {
		return fmt.Errorf("checksums: read directory: %w", err)
	}
	outputPath, err := filepath.Abs(options["--output"])
	if err != nil {
		return fmt.Errorf("checksums: resolve output: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == "SHA256SUMS" || !isChecksumInput(name) {
			continue
		}
		path := filepath.Join(options["--dir"], name)
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("checksums: resolve %s: %w", name, err)
		}
		if absolutePath == outputPath {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("checksums: stat %s: %w", name, err)
		}
		if info.Mode().IsRegular() {
			files = append(files, name)
		}
	}
	sort.Strings(files)

	return writeAtomic(options["--output"], func(output io.Writer) error {
		for _, name := range files {
			path := filepath.Join(options["--dir"], name)
			input, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open %s: %w", name, err)
			}
			digest := sha256.New()
			_, copyErr := io.Copy(digest, input)
			closeErr := input.Close()
			if copyErr != nil {
				return fmt.Errorf("hash %s: %w", name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", name, closeErr)
			}
			if _, err := fmt.Fprintf(output, "%x  %s\n", digest.Sum(nil), name); err != nil {
				return err
			}
		}
		return nil
	})
}

func isChecksumInput(name string) bool {
	return strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".spdx.json")
}

func parseOptions(args []string, required ...string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(required))
	for _, name := range required {
		allowed[name] = struct{}{}
	}
	if len(args)%2 != 0 {
		return nil, errors.New("every option requires one value")
	}
	options := make(map[string]string, len(required))
	for index := 0; index < len(args); index += 2 {
		name := args[index]
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("unknown option %q", name)
		}
		if _, duplicate := options[name]; duplicate {
			return nil, fmt.Errorf("duplicate option %q", name)
		}
		options[name] = args[index+1]
	}
	for _, name := range required {
		if _, ok := options[name]; !ok {
			return nil, fmt.Errorf("missing option %q", name)
		}
	}
	return options, nil
}

func parseEpoch(value string) (int64, error) {
	epoch, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("epoch must be a signed decimal integer: %w", err)
	}
	return epoch, nil
}

func validateArchiveEntry(entry string) error {
	if entry == "" || entry == "." || entry == ".." || strings.ContainsAny(entry, "/\\\x00") {
		return errors.New("entry must be one non-empty root name")
	}
	return nil
}

func writeAtomic(path string, write func(io.Writer) error) error {
	if path == "" {
		return errors.New("output path is required")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return fmt.Errorf("set output mode: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace output: %w", err)
	}
	removeTemporary = false
	return nil
}
