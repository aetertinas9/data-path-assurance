package liveingest

import "errors"

// ErrInvalidFrame is matched, with errors.Is, by every error that reports a
// frame as invalid: a violated payload rule, a digest or revision that does
// not match the recomputed value, or a canonical encoding over the size bound.
// A caller maps it to the INVALID acknowledgement.
var ErrInvalidFrame = errors.New("liveingest: invalid frame")

// ErrInvalidContext is returned when the caller itself passes an unusable
// FrameContext. It is a programming or configuration error, not an invalid
// frame.
var ErrInvalidContext = errors.New("liveingest: invalid frame context")

// frameError is an invalid-frame error. Its text is a fixed vocabulary word
// (the class): it never repeats a value taken from a frame, so it is safe to
// log.
type frameError struct{ class string }

func (e *frameError) Error() string { return "liveingest: invalid frame: " + e.class }

// Is makes every frameError match ErrInvalidFrame.
func (e *frameError) Is(target error) bool { return target == ErrInvalidFrame }

func invalid(class string) error { return &frameError{class: class} }

// Class returns the fixed class word of an invalid-frame error, such as
// "payload_order" or "digest", and "" for any other error. It is meant for
// logs and metrics.
func Class(err error) string {
	var fe *frameError
	if errors.As(err, &fe) {
		return fe.class
	}
	return ""
}
