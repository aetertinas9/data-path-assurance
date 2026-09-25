// Command path-agent is the node-side collector. This build supports only the
// offline mode: it reads a fixture tree given by --fixture-root and writes the
// offline snapshot artifact to stdout. Live collection is not implemented yet.
//
// Exit codes: 0 artifact written (or help), 1 internal error, 2 usage error,
// 3 invalid fixture, 4 bound exceeded, 5 live mode unsupported.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/agent"
)

// Exit codes and their stderr tokens (GFO-011).
const (
	exitOK              = 0
	exitInternal        = 1
	exitUsage           = 2
	exitFixtureInvalid  = 3
	exitBoundExceeded   = 4
	exitLiveUnsupported = 5
)

const (
	flagFixtureRoot = "fixture-root"
	maxErrorMessage = 1024
)

// liveFlags are the live-mode flags of GFL-096. They are recognized only to be
// refused: their values are never opened, stat'ed or printed.
var liveFlags = map[string]bool{
	"controller":           true,
	"cluster-id":           true,
	"node-name":            true,
	"node-uid":             true,
	"sysfs-root":           true,
	"ca-file":              true,
	"cert-file":            true,
	"key-file":             true,
	"nvidia-smi":           true,
	"pod-resources-socket": true,
}

const usage = `usage: path-agent --fixture-root <dir>

Reads the offline fixture tree at <dir> and writes its snapshot artifact to
stdout. Live mode is not supported by this build.
`

func main() {
	ignoreBrokenPipe()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run interprets args, performs the requested mode and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(stderr, usage)
		return exitOK
	}
	fixtureRoot, mode, problem := parseArgs(args)
	switch mode {
	case modeUsage:
		return fail(stderr, exitUsage, "usage", problem)
	case modeLive:
		return fail(stderr, exitLiveUnsupported, "live_unsupported",
			"live mode is not implemented; use --fixture-root")
	}

	root, err := agent.OpenFixtureRoot(fixtureRoot)
	if err != nil {
		return fail(stderr, exitFixtureInvalid, "fixture_invalid", "fixture root is absent, not a directory, or cannot be opened")
	}
	defer func() { _ = root.Close() }()

	artifact, err := agent.CollectOffline(context.Background(), root)
	switch {
	case errors.Is(err, agent.ErrFixtureInvalid):
		return fail(stderr, exitFixtureInvalid, "fixture_invalid", err.Error())
	case errors.Is(err, agent.ErrBoundExceeded):
		return fail(stderr, exitBoundExceeded, "bound_exceeded", err.Error())
	case err != nil:
		return fail(stderr, exitInternal, "internal", err.Error())
	}
	if n, err := stdout.Write(artifact); err != nil || n != len(artifact) {
		return fail(stderr, exitInternal, "internal", "writing the artifact to stdout failed")
	}
	return exitOK
}

// argMode is what the arguments ask for.
type argMode int

const (
	modeUsage argMode = iota
	modeOffline
	modeLive
)

// parseArgs applies GFO-010. Every flag takes a value, given as --name=value
// or as the next argument. Any malformed argument is a usage error, which
// outranks a live-mode request; the problem text never quotes an argument.
func parseArgs(args []string) (fixtureRoot string, mode argMode, problem string) {
	if len(args) == 0 {
		return "", modeUsage, "no arguments; --fixture-root is required"
	}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return "", modeUsage, "unexpected argument"
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		if name != flagFixtureRoot && !liveFlags[name] {
			return "", modeUsage, "unknown flag"
		}
		if seen[name] {
			return "", modeUsage, "flag given more than once"
		}
		seen[name] = true
		if !hasValue {
			if i+1 >= len(args) {
				return "", modeUsage, "flag value missing"
			}
			i++
			value = args[i]
		}
		if name == flagFixtureRoot {
			if value == "" {
				return "", modeUsage, "empty --fixture-root value"
			}
			fixtureRoot = value
		}
	}
	if !seen[flagFixtureRoot] {
		return "", modeLive, ""
	}
	if len(seen) > 1 {
		return "", modeUsage, "--fixture-root cannot be combined with other flags"
	}
	return fixtureRoot, modeOffline, ""
}

// fail writes the single error line and returns code. The message is reduced
// to printable ASCII and bounded so stderr stays small.
func fail(stderr io.Writer, code int, token, message string) int {
	_, _ = fmt.Fprintf(stderr, "path-agent: error: %s: %s\n", token, printable(message))
	return code
}

// printable replaces bytes outside 0x20..0x7e and bounds the length.
func printable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxErrorMessage; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b.WriteByte(c)
	}
	return b.String()
}
