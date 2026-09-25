package offline

import "errors"

// The input error classes. Every error LoadReplay returns for a rejected input
// wraps exactly one of them.
var (
	// ErrArtifactInvalid reports that the snapshot artifact cannot be opened
	// as a regular file or violates the artifact contract.
	ErrArtifactInvalid = errors.New("artifact invalid")
	// ErrFleetInvalid reports that the fleet file cannot be opened as a
	// regular file, violates the fleet file contract, or does not match the
	// artifact.
	ErrFleetInvalid = errors.New("fleet file invalid")
	// ErrBoundExceeded reports that an input file is larger than its bound.
	ErrBoundExceeded = errors.New("input size bound exceeded")
)

// InputError is a rejected input. Its detail names schema field paths and
// fixed phrases only; it never carries values read from the input, file
// paths or operating system error text, so it is safe to display.
type InputError struct {
	class  error
	detail string
}

// Error renders the class and the detail.
func (e *InputError) Error() string { return e.class.Error() + ": " + e.detail }

// Unwrap reports the error class.
func (e *InputError) Unwrap() error { return e.class }

// PublicMessage returns the detail, which is safe to display.
func (e *InputError) PublicMessage() string { return e.detail }

func inputError(class error, detail string) error {
	return &InputError{class: class, detail: detail}
}
