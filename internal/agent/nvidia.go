package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Bounds of one NVIDIA inventory query (GFL-030).
const (
	NVIDIAQueryTimeout   = 5 * time.Second
	NVIDIAMaxStdoutBytes = 65536
	NVIDIAMaxStderrBytes = 4096
	NVIDIAMaxRows        = 256
)

// nvidiaWaitDelay bounds how long Wait keeps reading output after the process
// has exited (or been killed) while a descendant still holds a pipe open.
const nvidiaWaitDelay = 200 * time.Millisecond

// nvidiaKillGrace bounds how long an aborted query waits for the killed
// process to be reaped before returning anyway.
const nvidiaKillGrace = 500 * time.Millisecond

// NVIDIALimits narrows the bounds of one query. Each field must be positive and
// no larger than its GFL-030 bound.
type NVIDIALimits struct {
	Timeout        time.Duration
	MaxStdoutBytes int
	MaxStderrBytes int
}

// GPUInventoryEntry is one inventory row: the GPU UUID as reported and the
// canonical PCI address of the GPU.
type GPUInventoryEntry struct {
	UUID string
	BDF  string
}

// DefaultNVIDIALimits returns the GFL-030 bounds themselves: 5s, 64 KiB of
// stdout and 4 KiB of stderr.
func DefaultNVIDIALimits() NVIDIALimits {
	return NVIDIALimits{
		Timeout:        NVIDIAQueryTimeout,
		MaxStdoutBytes: NVIDIAMaxStdoutBytes,
		MaxStderrBytes: NVIDIAMaxStderrBytes,
	}
}

// NVIDIAQueryArgs returns the query arguments, excluding argv[0], as a new
// slice on every call.
func NVIDIAQueryArgs() []string {
	return []string{"--query-gpu=uuid,pci.bus_id", "--format=csv,noheader,nounits"}
}

// nvidiaRowPattern is one query output line: UUID, a comma and pci.bus_id,
// with optional blanks around each field.
var nvidiaRowPattern = regexp.MustCompile(
	`^[ \t]*(GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})[ \t]*,[ \t]*([0-9A-Fa-f]{4}(?:[0-9A-Fa-f]{4})?:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}\.[0-7])[ \t]*$`)

// ParseNVIDIAQueryOutput parses the stdout of the inventory query. It checks,
// in order: size, emptiness, the grammar of every line, the row count, and
// duplicate UUIDs or canonical addresses. On success the rows are returned in
// input order with each address canonicalized; on failure the result is nil
// and the error matches one sentinel. Error messages never contain the input.
func ParseNVIDIAQueryOutput(stdout []byte) ([]GPUInventoryEntry, error) {
	if len(stdout) > NVIDIAMaxStdoutBytes {
		return nil, fmt.Errorf("%w: %d bytes of stdout", ErrNVIDIAOutputLimit, len(stdout))
	}
	text := string(stdout)
	if text == "" || text == "\n" || text == "\r\n" {
		return nil, ErrNVIDIAEmpty
	}
	if trimmed, found := strings.CutSuffix(text, "\n"); found {
		text = strings.TrimSuffix(trimmed, "\r")
	}
	lines := strings.Split(text, "\n")
	entries := make([]GPUInventoryEntry, 0, min(len(lines), NVIDIAMaxRows+1))
	rows := 0
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		m := nvidiaRowPattern.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("%w: line %d", ErrNVIDIAMalformed, i+1)
		}
		bdf, ok := canonicalBusID(m[2])
		if !ok {
			return nil, fmt.Errorf("%w: line %d", ErrNVIDIAMalformed, i+1)
		}
		rows++
		if len(entries) <= NVIDIAMaxRows {
			entries = append(entries, GPUInventoryEntry{UUID: m[1], BDF: bdf})
		}
	}
	if rows > NVIDIAMaxRows {
		return nil, fmt.Errorf("%w: %d rows", ErrNVIDIAOutputLimit, rows)
	}
	uuids := make(map[string]struct{}, len(entries))
	bdfs := make(map[string]struct{}, len(entries))
	for i, e := range entries {
		if _, dup := uuids[e.UUID]; dup {
			return nil, fmt.Errorf("%w: UUID repeated at row %d", ErrNVIDIADuplicate, i+1)
		}
		if _, dup := bdfs[e.BDF]; dup {
			return nil, fmt.Errorf("%w: address repeated at row %d", ErrNVIDIADuplicate, i+1)
		}
		uuids[e.UUID] = struct{}{}
		bdfs[e.BDF] = struct{}{}
	}
	return entries, nil
}

// RunNVIDIAQuery runs executable directly, without a shell or a PATH search,
// with exactly the query arguments and an empty stdin, and parses its stdout.
//
// The process is killed and the query fails when stdout or stderr outgrows its
// limit (ErrNVIDIAOutputLimit), when limits.Timeout passes after start
// (ErrNVIDIATimeout), or when ctx ends (an error matching ctx.Err()); whichever
// is observed first decides. In each case the call returns promptly even if
// the process ignores SIGTERM or a descendant keeps the output pipes open.
// Otherwise a nonzero exit or a signal is ErrNVIDIAExit, and a clean exit
// yields ParseNVIDIAQueryOutput of stdout.
func RunNVIDIAQuery(ctx context.Context, executable string, limits NVIDIALimits) ([]GPUInventoryEntry, error) {
	if err := checkRunArguments(ctx, executable, limits); err != nil {
		return nil, err
	}

	// tripped is closed by whichever output buffer first outgrows its bound.
	tripped := make(chan struct{})
	var once sync.Once
	trip := func() { once.Do(func() { close(tripped) }) }
	stdout := &boundedBuffer{max: limits.MaxStdoutBytes, trip: trip}
	stderr := &boundedBuffer{max: limits.MaxStderrBytes, trip: trip}

	cmd := &exec.Cmd{
		Path:      executable,
		Args:      append([]string{executable}, NVIDIAQueryArgs()...),
		Stdout:    stdout,
		Stderr:    stderr,
		WaitDelay: nvidiaWaitDelay,
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNVIDIAStart, err)
	}

	// The wait goroutine owns cmd.Wait. It ends once the process has exited
	// and the output copies have stopped, which WaitDelay bounds after exit;
	// waitDone is buffered so it never blocks after an abort.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	timer := time.NewTimer(limits.Timeout)
	defer timer.Stop()

	var cause error
	select {
	case waitErr := <-waitDone:
		return finishNVIDIAQuery(cmd, waitErr, stdout, stderr)
	case <-tripped:
		cause = ErrNVIDIAOutputLimit
	case <-timer.C:
		cause = ErrNVIDIATimeout
	case <-ctx.Done():
		cause = ctx.Err()
	}

	// SIGKILL cannot be ignored. A failure here means the process is already
	// gone, which is the state wanted.
	_ = cmd.Process.Kill()
	grace := time.NewTimer(nvidiaKillGrace)
	defer grace.Stop()
	select {
	case <-waitDone:
	case <-grace.C:
	}
	return nil, fmt.Errorf("agent: nvidia query aborted: %w", cause)
}

// checkRunArguments rejects every argument RunNVIDIAQuery must not act on.
func checkRunArguments(ctx context.Context, executable string, limits NVIDIALimits) error {
	switch {
	case ctx == nil:
		return fmt.Errorf("%w: nil context", ErrInvalidArgument)
	case executable == "":
		return fmt.Errorf("%w: empty executable", ErrInvalidArgument)
	case !filepath.IsAbs(executable):
		return fmt.Errorf("%w: executable is not an absolute path", ErrInvalidArgument)
	case filepath.Clean(executable) != executable:
		return fmt.Errorf("%w: executable is not a clean path", ErrInvalidArgument)
	case limits.Timeout <= 0 || limits.Timeout > NVIDIAQueryTimeout:
		return fmt.Errorf("%w: timeout outside (0, %s]", ErrInvalidArgument, NVIDIAQueryTimeout)
	case limits.MaxStdoutBytes <= 0 || limits.MaxStdoutBytes > NVIDIAMaxStdoutBytes:
		return fmt.Errorf("%w: stdout limit outside (0, %d]", ErrInvalidArgument, NVIDIAMaxStdoutBytes)
	case limits.MaxStderrBytes <= 0 || limits.MaxStderrBytes > NVIDIAMaxStderrBytes:
		return fmt.Errorf("%w: stderr limit outside (0, %d]", ErrInvalidArgument, NVIDIAMaxStderrBytes)
	}
	return nil
}

// finishNVIDIAQuery decides the outcome of a process that ended on its own:
// an output bound beats the exit status, which beats parsing.
func finishNVIDIAQuery(cmd *exec.Cmd, waitErr error, stdout, stderr *boundedBuffer) ([]GPUInventoryEntry, error) {
	if stdout.over || stderr.over {
		return nil, ErrNVIDIAOutputLimit
	}
	state := cmd.ProcessState
	if state == nil {
		return nil, fmt.Errorf("%w: no process state", ErrNVIDIAExit)
	}
	if !state.Success() {
		if code := state.ExitCode(); code >= 0 {
			return nil, fmt.Errorf("%w: exit status %d", ErrNVIDIAExit, code)
		}
		return nil, fmt.Errorf("%w: terminated by a signal", ErrNVIDIAExit)
	}
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return nil, fmt.Errorf("%w: output could not be collected", ErrNVIDIAExit)
	}
	return ParseNVIDIAQueryOutput(stdout.data)
}

// errOutputBound stops the copy of an output stream that outgrew its bound.
var errOutputBound = errors.New("agent: output bound reached")

// boundedBuffer keeps at most max bytes of one output stream. Its fields are
// written only by the single exec copy goroutine of that stream and read only
// after Wait has returned, which orders the two.
type boundedBuffer struct {
	max  int
	data []byte
	over bool
	trip func()
}

// Write appends p, or marks the buffer over its bound, signals the trip and
// fails once the total would exceed max. Exactly max bytes are accepted.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.over {
		return 0, errOutputBound
	}
	if len(p) > b.max-len(b.data) {
		b.over = true
		b.trip()
		return 0, errOutputBound
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
