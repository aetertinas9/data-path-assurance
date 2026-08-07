package graph

import (
	"errors"
	"fmt"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// ErrStopped reports that a reducer was offered an event after its Run
// returned. It is the only sentinel this package adds: argument violations are
// reported with [model.ErrInvalid], which a caller working with pkg/model
// already tests for, and the domain outcomes a caller might expect an error for
// — a stale event, a gap, an observation the window refuses — are not errors at
// all but values of [ApplyOutcome].
var ErrStopped = errors.New("graph: reducer stopped")

// invalidf builds an error wrapping model.ErrInvalid, so that an argument
// violation here is tested for exactly as one from pkg/model is.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", model.ErrInvalid, fmt.Sprintf(format, args...))
}
