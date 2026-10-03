package ingestadapter

import (
	"errors"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
)

// storeError is an error of the ports. Its text is the fixed text of its kind:
// whatever the API server or the transport said (a response body, a request
// path, an object name) is reachable through Unwrap only, so that a log line
// or a status message built from Error() never repeats it.
type storeError struct {
	kind  error // one of the sentinels of internal/app/liveingest
	cause error // may be nil
}

func (e *storeError) Error() string { return e.kind.Error() }

// Unwrap exposes the kind and the cause, so errors.Is matches both.
func (e *storeError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.kind}
	}
	return []error{e.kind, e.cause}
}

// unavailable reports that the store could not be used. cause is the
// transport, API or context error that made it fail.
func unavailable(cause error) error {
	return &storeError{kind: liveingest.ErrStoreUnavailable, cause: cause}
}

// conflict reports that the retries ran out on resourceVersion conflicts.
func conflict(cause error) error {
	return &storeError{kind: liveingest.ErrSessionConflict, cause: cause}
}

// errPollRunning is returned by a RunLeaderPoll call while another one runs.
var errPollRunning = errors.New("ingestadapter: leader poll already running")

// errNotBuilt is returned by RunLeaderPoll on an Adapter that New did not
// build.
var errNotBuilt = errors.New("ingestadapter: adapter not built by New")
