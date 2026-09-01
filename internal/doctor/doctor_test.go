package doctor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPreservesInputOrderAndRunsEveryProbe(t *testing.T) {
	var calls atomic.Int32
	sentinel := errors.New("sentinel")
	checks := []Check{
		{ID: "slow", Probe: func(context.Context) (string, *Failure) {
			time.Sleep(20 * time.Millisecond)
			calls.Add(1)
			return "slow detail", nil
		}},
		{ID: "fast", Probe: func(context.Context) (string, *Failure) {
			calls.Add(1)
			return "fast detail", nil
		}},
		{ID: "failed", Probe: func(context.Context) (string, *Failure) {
			calls.Add(1)
			return "", NewFailure("dependency unavailable", sentinel)
		}},
	}

	report := Run(context.Background(), checks)
	if calls.Load() != int32(len(checks)) {
		t.Fatalf("calls = %d, want %d", calls.Load(), len(checks))
	}
	if len(report.Results) != len(checks) {
		t.Fatalf("results = %d, want %d", len(report.Results), len(checks))
	}
	for index, want := range []string{"slow", "fast", "failed"} {
		if report.Results[index].ID != want {
			t.Errorf("result[%d].ID = %q, want %q", index, report.Results[index].ID, want)
		}
	}
	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	if got := report.Results[2].Failure.Error(); got != "dependency unavailable" {
		t.Fatalf("failure error = %q", got)
	}
	if !errors.Is(report.Results[2].Failure, sentinel) {
		t.Fatal("errors.Is did not find the underlying cause")
	}
}

func TestFailureUnwrapRetainsUnderlyingCause(t *testing.T) {
	sentinel := errors.New("underlying")
	failure := NewFailure("safe reason", sentinel)
	if !errors.Is(failure, sentinel) {
		t.Fatal("errors.Is did not find the underlying cause")
	}
	if failure.Error() != "safe reason" {
		t.Fatalf("Error() = %q, want safe reason", failure.Error())
	}
}

func TestRunPassesCancellationToAllProbes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var observed atomic.Int32
	checks := []Check{
		{ID: "one", Probe: func(ctx context.Context) (string, *Failure) {
			<-ctx.Done()
			observed.Add(1)
			return "", NewFailure("cancelled", ctx.Err())
		}},
		{ID: "two", Probe: func(ctx context.Context) (string, *Failure) {
			<-ctx.Done()
			observed.Add(1)
			return "", NewFailure("cancelled", ctx.Err())
		}},
	}
	cancel()
	report := Run(ctx, checks)
	if observed.Load() != int32(len(checks)) {
		t.Fatalf("probes observing cancellation = %d, want %d", observed.Load(), len(checks))
	}
	if report.OK() {
		t.Fatal("cancelled report unexpectedly passed")
	}
}
