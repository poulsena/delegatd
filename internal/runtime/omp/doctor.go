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
	"sync"
	"time"

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
	// An explicit pipe lets Wait observe the main process independently of
	// descendants that inherit stdout. StdoutPipe's implicit close can race
	// with the drain, while waiting for EOF first can block forever.
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return "", doctor.NewFailure("OMP RPC did not become ready", err)
	}
	command.Stdout = stdoutWriter
	// nil connects stderr directly to the null device. Using io.Discard would
	// make os/exec copy through a pipe that an orphaned child could keep open.
	command.Stderr = nil
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return "", doctor.NewFailure("OMP RPC did not become ready", err)
	}
	if err := command.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return "", doctor.NewFailure("OMP RPC did not become ready", err)
	}
	// The child owns its duplicate of the write end after Start.
	_ = stdoutWriter.Close()

	var closeReaderOnce sync.Once
	closeReader := func() {
		closeReaderOnce.Do(func() {
			_ = stdoutReader.Close()
		})
	}
	watchStop := make(chan struct{})
	defer close(watchStop)
	defer closeReader()
	go func() {
		select {
		case <-probeContext.Done():
			closeReader()
		case <-watchStop:
		}
	}()
	stopBeforeReady := func(reason string, cause error) (string, *doctor.Failure) {
		cancel()
		_ = command.Wait()
		closeReader()
		return "", doctor.NewFailure(reason, cause)
	}

	reader := bufio.NewReader(stdoutReader)
	frame, frameErr := readReadyFrame(reader)
	if frameErr != nil {
		return stopBeforeReady("OMP RPC did not become ready", frameErr)
	}
	ready, protocolErr := readyFrame(frame)
	if protocolErr != nil {
		if errors.Is(protocolErr, errProtocolUnavailable) {
			return stopBeforeReady("OMP RPC protocol 1 is unavailable", protocolErr)
		}
		return stopBeforeReady("OMP RPC did not become ready", protocolErr)
	}
	if !ready {
		return stopBeforeReady("OMP RPC protocol 1 is unavailable", nil)
	}

	if err := stdin.Close(); err != nil {
		return stopBeforeReady("OMP RPC did not shut down cleanly", err)
	}
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(drainDone)
	}()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()
	waitErr := <-waitDone
	// A descendant may still own the write end after the main process exits.
	// Set an immediate deadline before closing our read end so the drain
	// cannot keep the diagnosis blocked.
	_ = stdoutReader.SetReadDeadline(time.Now())
	closeReader()
	<-drainDone
	if waitErr != nil {
		return "", doctor.NewFailure("OMP RPC did not shut down cleanly", waitErr)
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
