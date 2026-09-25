package agent

import "errors"

// Sentinels of the NVIDIA inventory runner and parser. Every error returned by
// RunNVIDIAQuery and ParseNVIDIAQueryOutput matches exactly one of them (or,
// for a caller context that ended first, that context's error) under
// errors.Is. None of their messages carries child output.
var (
	// ErrInvalidArgument reports a nil context, an executable that is not a
	// clean absolute path, or limits outside the GFL-030 bounds. No process is
	// started.
	ErrInvalidArgument = errors.New("agent: invalid argument")
	// ErrNVIDIAStart reports that the executable could not be started.
	ErrNVIDIAStart = errors.New("agent: nvidia-smi could not be started")
	// ErrNVIDIATimeout reports that the query did not finish within its timeout.
	ErrNVIDIATimeout = errors.New("agent: nvidia-smi timed out")
	// ErrNVIDIAOutputLimit reports output beyond its byte or row bound.
	ErrNVIDIAOutputLimit = errors.New("agent: nvidia-smi output exceeds its bound")
	// ErrNVIDIAExit reports a nonzero exit status or a signal termination.
	ErrNVIDIAExit = errors.New("agent: nvidia-smi did not exit successfully")
	// ErrNVIDIAEmpty reports output with no inventory row at all.
	ErrNVIDIAEmpty = errors.New("agent: nvidia-smi output is empty")
	// ErrNVIDIAMalformed reports a line outside the query output grammar.
	ErrNVIDIAMalformed = errors.New("agent: nvidia-smi output is malformed")
	// ErrNVIDIADuplicate reports a repeated UUID or a repeated canonical BDF.
	ErrNVIDIADuplicate = errors.New("agent: nvidia-smi output repeats a GPU")
)

// Sentinels of the offline artifact builder, used by path-agent to map an
// outcome to its exit code. They are not part of the Go API contract.
var (
	// ErrFixtureInvalid reports a fixture that violates the input contract:
	// the root, the manifest or a frame directory (exit 3).
	ErrFixtureInvalid = errors.New("agent: fixture invalid")
	// ErrBoundExceeded reports an artifact bound that cannot be met without
	// truncation (exit 4).
	ErrBoundExceeded = errors.New("agent: bound exceeded")
)
