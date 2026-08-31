package omp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/poulsena/delegatd/internal/doctor"
)

const maxReadyFrameSize = 1 << 20

// Config is intentionally empty for the version-one OMP diagnosis. Runtime
// invocation settings are adapter-owned configuration for a later tracer.
type Config struct{}

// NewDoctorCheck constructs a no-session OMP RPC protocol diagnosis.
func NewDoctorCheck(name string, _ Config) doctor.Check {
	return doctor.Check{
		ID: "agent_runtime." + name,
		Probe: func(ctx context.Context) (string, *doctor.Failure) {
			return probe(ctx)
		},
	}
}

func probe(ctx context.Context) (string, *doctor.Failure) {
	executable, err := exec.LookPath("omp")
	if err != nil {
		return "", doctor.NewFailure("omp executable was not found", err)
	}

	probeContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(
		probeContext,
		executable,
		"--mode", "rpc",
		"--no-session",
		"--no-extensions",
		"--no-skills",
		"--no-rules",
		"--no-tools",
		"--no-lsp",
		"--no-pty",
		"--cwd", os.TempDir(),
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", doctor.NewFailure("OMP RPC did not become ready", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return "", doctor.NewFailure("OMP RPC did not become ready", err)
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return "", doctor.NewFailure("OMP RPC did not become ready", err)
	}

	reader := bufio.NewReader(stdout)
	frame, frameErr := readReadyFrame(reader)
	if frameErr != nil {
		cancel()
		_ = command.Wait()
		return "", doctor.NewFailure("OMP RPC did not become ready", frameErr)
	}
	ready, protocolErr := readyFrame(frame)
	if protocolErr != nil {
		cancel()
		_ = command.Wait()
		if errors.Is(protocolErr, errProtocolUnavailable) {
			return "", doctor.NewFailure("OMP RPC protocol 1 is unavailable", protocolErr)
		}
		return "", doctor.NewFailure("OMP RPC did not become ready", protocolErr)
	}
	if !ready {
		cancel()
		_ = command.Wait()
		return "", doctor.NewFailure("OMP RPC protocol 1 is unavailable", nil)
	}

	if err := stdin.Close(); err != nil {
		cancel()
		_ = command.Wait()
		return "", doctor.NewFailure("OMP RPC did not shut down cleanly", err)
	}
	drainDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		drainDone <- err
	}()
	waitErr := command.Wait()
	drainErr := <-drainDone
	if waitErr != nil || drainErr != nil {
		if waitErr != nil {
			return "", doctor.NewFailure("OMP RPC did not shut down cleanly", waitErr)
		}
		return "", doctor.NewFailure("OMP RPC did not shut down cleanly", drainErr)
	}
	return "OMP RPC protocol 1", nil
}

func readReadyFrame(reader *bufio.Reader) ([]byte, error) {
	var frame bytes.Buffer
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			if frame.Len()+len(part) > maxReadyFrameSize {
				return nil, errFrameTooLarge
			}
			_, _ = frame.Write(part)
		}
		if err == nil {
			data := frame.Bytes()
			return bytes.TrimSuffix(data, []byte{'\n'}), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func readyFrame(data []byte) (bool, error) {
	var frame struct {
		Type     string `json:"type"`
		Protocol *int   `json:"protocolVersion"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return false, err
	}
	if frame.Type != "ready" {
		return false, errors.New("ready frame was not advertised")
	}
	if frame.Protocol == nil {
		return false, errProtocolUnavailable
	}
	return *frame.Protocol == 1, nil
}

var (
	errFrameTooLarge       = errors.New("OMP ready frame exceeded the diagnostic limit")
	errProtocolUnavailable = errors.New("OMP ready frame did not advertise protocol 1")
)
