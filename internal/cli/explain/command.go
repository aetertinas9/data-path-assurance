// Package explain is the pathctl explain command: argument parsing, exit
// codes, the single stderr error line, atomic stdout output, and the JSON and
// text renderings of an app.Explanation.
//
// This build supports the offline path only (--artifact and --fleet). The
// live transport flags are recognized so that they can be refused without
// touching the network or the files they name.
package explain

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/offline"
)

// Exit codes and their stderr tokens.
const (
	ExitOK              = 0
	ExitInternal        = 1
	ExitUsage           = 2
	ExitNotFound        = 4
	ExitLiveUnsupported = 5
	ExitArtifactInvalid = 6
	ExitFleetInvalid    = 7
	ExitBoundExceeded   = 8
)

var exitTokens = map[int]string{
	ExitInternal:        "internal",
	ExitUsage:           "usage",
	ExitNotFound:        "not_found",
	ExitLiveUnsupported: "live_unsupported",
	ExitArtifactInvalid: "artifact_invalid",
	ExitFleetInvalid:    "fleet_invalid",
	ExitBoundExceeded:   "bound_exceeded",
}

// Output bounds.
const (
	maxStdoutBytes = 16 << 20
	maxStderrBytes = 8 << 10
)

// ExplainFunc evaluates a replay and returns the explanation of a request.
type ExplainFunc func(ctx context.Context, src app.ReplaySource, req app.Request) (app.Explanation, error)

// SourceFunc returns the replay source reading the given artifact and fleet
// file paths.
type SourceFunc func(artifactPath, fleetPath string) app.ReplaySource

// Command runs pathctl explain.
type Command struct {
	explain ExplainFunc
	source  SourceFunc
	stdout  io.Writer
	stderr  io.Writer
}

// NewCommand wires the command to its use case, its replay source factory and
// its output streams.
func NewCommand(explain ExplainFunc, source SourceFunc, stdout, stderr io.Writer) *Command {
	return &Command{explain: explain, source: source, stdout: stdout, stderr: stderr}
}

const usageText = `usage: pathctl explain gpu <device-name> --artifact <file> --fleet <file> [--output text|json]
       pathctl explain node <node-name> --artifact <file> --fleet <file> [--output text|json]

Explains a GPU device or its node from an offline snapshot artifact written by
path-agent --fixture-root and an offline fleet file. The live transport flags
(--server, --cluster-id, --ca-file, --cert-file, --key-file) are not supported
by this build.

Exit codes: 0 explanation written, 1 internal error, 2 usage error, 4 not
found, 5 live path unsupported, 6 invalid artifact, 7 invalid fleet file,
8 size bound exceeded.
`

// Run executes the command with args (without the program name) and returns
// the process exit code. Nothing is written to stdout unless the whole
// explanation was built successfully.
func (c *Command) Run(ctx context.Context, args []string) int {
	ignoreBrokenPipe()
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(c.stderr, usageText)
		return ExitOK
	}
	inv, code, problem := parseArgs(args)
	if code != ExitOK {
		return c.fail(code, problem)
	}
	if c.explain == nil || c.source == nil {
		return c.fail(ExitInternal, "the command is not wired")
	}
	exp, err := c.explain(ctx, c.source(inv.artifact, inv.fleet), app.Request{Kind: inv.kind, Name: inv.name})
	if err != nil {
		code := classify(err)
		return c.fail(code, publicMessage(err, code))
	}
	body, err := EncodeJSON(exp)
	if err != nil {
		return c.fail(ExitInternal, "the explanation holds a character outside printable ASCII")
	}
	if inv.output == "text" {
		body = RenderText(exp)
	}
	if len(body) > maxStdoutBytes {
		return c.fail(ExitBoundExceeded, "the explanation is larger than 16777216 bytes")
	}
	n, err := c.stdout.Write(body)
	if err != nil || n != len(body) {
		return c.fail(ExitInternal, "writing the explanation to stdout failed")
	}
	return ExitOK
}

func classify(err error) int {
	switch {
	case errors.Is(err, offline.ErrBoundExceeded):
		return ExitBoundExceeded
	case errors.Is(err, offline.ErrArtifactInvalid):
		return ExitArtifactInvalid
	case errors.Is(err, offline.ErrFleetInvalid):
		return ExitFleetInvalid
	case errors.Is(err, app.ErrTargetNotFound):
		return ExitNotFound
	default:
		return ExitInternal
	}
}

// publicMessage returns the safe detail an error carries, or a fixed phrase.
// Error text from elsewhere is never shown: it may quote input values, paths
// or operating system messages.
func publicMessage(err error, code int) string {
	var pm interface{ PublicMessage() string }
	if errors.As(err, &pm) {
		return pm.PublicMessage()
	}
	switch code {
	case ExitArtifactInvalid:
		return "the artifact is invalid"
	case ExitFleetInvalid:
		return "the fleet file is invalid"
	case ExitBoundExceeded:
		return "an input exceeds its size bound"
	case ExitNotFound:
		return "the target was not found"
	default:
		return "unexpected internal error"
	}
}

// fail writes the single error line and returns code.
func (c *Command) fail(code int, message string) int {
	prefix := "pathctl: error: " + exitTokens[code] + ": "
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < len(message) && b.Len() < maxStderrBytes-1; i++ {
		ch := message[i]
		if ch < 0x20 || ch > 0x7e {
			ch = '?'
		}
		b.WriteByte(ch)
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(c.stderr, b.String())
	return code
}
