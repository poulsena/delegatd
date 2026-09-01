package control

import "errors"

// Failure is a safe application-facing failure. The underlying cause remains
// available to trusted callers through errors.Is/errors.As but is never part of
// Error's operator-facing text.
type Failure struct {
	Reason string
	cause  error
}

func NewFailure(reason string, cause error) *Failure {
	return &Failure{Reason: reason, cause: cause}
}

func (f *Failure) Error() string {
	if f == nil || f.Reason == "" {
		return "task failed"
	}
	return f.Reason
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

func (f *Failure) SafeReason() string {
	if f == nil {
		return ""
	}
	return f.Error()
}

// SafeReason returns the stable public reason for an application failure.
func SafeReason(err error) string {
	if err == nil {
		return ""
	}
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Error()
	}
	var reasonProvider interface{ SafeReason() string }
	if errors.As(err, &reasonProvider) && reasonProvider.SafeReason() != "" {
		return reasonProvider.SafeReason()
	}
	return "task failed"
}
