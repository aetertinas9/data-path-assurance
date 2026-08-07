package evidence

import (
	"errors"
	"fmt"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// The sentinels this package reports. Argument violations are not among them:
// an argument is invalid by the model's rules plus a few of this package's own,
// so they are reported with [model.ErrInvalid], which a caller working with
// pkg/model already tests for.
var (
	// ErrConflict reports that the series already holds an observation at the
	// very same instant which is not equivalent to the one offered. Neither is
	// merged into the other and the retained one is never replaced.
	ErrConflict = errors.New("evidence: conflicting observation at the same instant")

	// ErrOutOfWindow reports that an observation is valid but cannot be
	// retained: it falls before the series horizon, or the capacity of the
	// series is already spent on later observations.
	ErrOutOfWindow = errors.New("evidence: observation falls outside the window")

	// ErrInsufficientSamples reports that a rate was asked of a series holding
	// fewer than the two samples a rate is made of.
	ErrInsufficientSamples = errors.New("evidence: not enough samples for a rate")
)

// invalidf builds an error wrapping model.ErrInvalid, so that an argument
// violation here is tested for exactly as one from pkg/model is.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", model.ErrInvalid, fmt.Sprintf(format, args...))
}
