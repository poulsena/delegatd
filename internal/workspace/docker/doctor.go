package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"

	"github.com/poulsena/delegatd/internal/doctor"
)

const maxVersionOutputSize = 64 << 10

var dockerVersionPattern = regexp.MustCompile(`^[A-Za-z0-9.+_-]{1,64}$`)

// Config is intentionally empty for the version-one Docker diagnosis. Future
// workspace settings belong to the workspace profile, not this probe.
type Config struct{}

// NewDoctorCheck constructs a read-only Docker daemon diagnosis.
func NewDoctorCheck(name string, _ Config) doctor.Check {
	return doctor.Check{
		ID: "workspace_provider." + name,
		Probe: func(ctx context.Context) (string, *doctor.Failure) {
			return probe(ctx)
		},
	}
}

func probe(ctx context.Context) (string, *doctor.Failure) {
	executable, err := exec.LookPath("docker")
	if err != nil {
		return "", doctor.NewFailure("docker executable was not found", err)
	}

	probeContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var output limitedBuffer
	output.onLimit = cancel
	command := exec.CommandContext(probeContext, executable, "version", "--format", "{{json .Server.Version}} {{json .Server.Os}}")
	command.Stdout = &output
	command.Stderr = io.Discard
	runErr := command.Run()
	if output.tooLarge {
		return "", doctor.NewFailure("Docker version response is invalid", errOutputTooLarge)
	}
	if runErr != nil {
		return "", doctor.NewFailure("Docker daemon is unavailable", runErr)
	}

	version, operatingSystem, err := decodeVersionOutput(output.Bytes())
	if err != nil {
		return "", doctor.NewFailure("Docker version response is invalid", err)
	}
	if operatingSystem != "linux" {
		return "", doctor.NewFailure("Docker server OS is not linux", nil)
	}
	if !dockerVersionPattern.MatchString(version) {
		return "", doctor.NewFailure("Docker version response is invalid", errors.New("invalid server version"))
	}
	return "Docker server " + version + " (linux)", nil
}

func decodeVersionOutput(data []byte) (string, string, error) {
	decoder := json.NewDecoder(bufio.NewReader(bytes.NewReader(data)))
	var rawVersion, rawOperatingSystem json.RawMessage
	if err := decoder.Decode(&rawVersion); err != nil {
		return "", "", err
	}
	if err := decoder.Decode(&rawOperatingSystem); err != nil {
		return "", "", err
	}
	version, err := decodeJSONString(rawVersion)
	if err != nil {
		return "", "", err
	}
	operatingSystem, err := decodeJSONString(rawOperatingSystem)
	if err != nil {
		return "", "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", "", errors.New("extra JSON value")
		}
		return "", "", err
	}
	return version, operatingSystem, nil
}

func decodeJSONString(data json.RawMessage) (string, error) {
	if len(data) == 0 || data[0] != '"' {
		return "", errors.New("JSON value is not a string")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	return value, nil
}

type limitedBuffer struct {
	data     bytes.Buffer
	tooLarge bool
	onLimit  func()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxVersionOutputSize - b.data.Len()
	if remaining <= 0 {
		b.tooLarge = true
		if b.onLimit != nil {
			b.onLimit()
		}
		return 0, errOutputTooLarge
	}
	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.tooLarge = true
		if b.onLimit != nil {
			b.onLimit()
		}
		return remaining, errOutputTooLarge
	}
	return b.data.Write(p)
}
func (b *limitedBuffer) Bytes() []byte {
	return b.data.Bytes()
}

var errOutputTooLarge = errors.New("command output exceeded the diagnostic limit")
