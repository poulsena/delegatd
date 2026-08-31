package doctor

import (
	"context"
	"sync"
)

// Failure is a safe diagnostic failure. The underlying cause is retained for
// callers that need errors.Is/errors.As, but it is never part of Error.
type Failure struct {
	Reason string
	cause  error
}

// NewFailure creates a failure whose Reason is safe to show to an operator.
func NewFailure(reason string, cause error) *Failure {
	return &Failure{Reason: reason, cause: cause}
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	return f.Reason
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

// Check is one independent dependency diagnosis.
type Check struct {
	ID    string
	Probe func(context.Context) (string, *Failure)
}

// Result is the normalized outcome of one check.
type Result struct {
	ID      string
	Detail  string
	Failure *Failure
}

// Report contains results in the same order as the supplied checks.
type Report struct {
	Results []Result
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, result := range r.Results {
		if result.Failure != nil {
			return false
		}
	}
	return true
}

// Run executes all independent probes concurrently and preserves input order.
// Every probe is given the same command context. A nil probe is represented as
// a safe failure rather than panicking in the command process.
func Run(ctx context.Context, checks []Check) Report {
	results := make([]Result, len(checks))
	var wait sync.WaitGroup
	wait.Add(len(checks))
	for index := range checks {
		index := index
		check := checks[index]
		go func() {
			defer wait.Done()
			results[index].ID = check.ID
			if check.Probe == nil {
				results[index].Failure = NewFailure("diagnostic probe is unavailable", nil)
				return
			}
			results[index].Detail, results[index].Failure = check.Probe(ctx)
		}()
	}
	wait.Wait()
	return Report{Results: results}
}
