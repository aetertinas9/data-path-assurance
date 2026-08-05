package model

import (
	"errors"
	"fmt"
)

// ErrInvalid is the sentinel reported for every invariant violation in this
// package. Errors returned by constructors and by Validate wrap it, so callers
// test with errors.Is(err, ErrInvalid) rather than by comparing messages.
var ErrInvalid = errors.New("model: invalid value")

// invalidf builds an ErrInvalid-wrapping error describing one violation.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
