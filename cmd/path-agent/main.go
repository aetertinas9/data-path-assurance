// Command path-agent is the node-side collector. With --fixture-root it reads a
// fixture tree and writes the offline snapshot artifact to stdout. With the live
// flags (and no --fixture-root) it collects the host and streams snapshot frames
// to the controller's ingest over mutual TLS until it is interrupted.
//
// Exit codes: 0 artifact written, help, or a live stream stopped by SIGINT or
// SIGTERM; 1 internal error; 2 usage error; 3 invalid fixture; 4 bound exceeded;
// 5 live mode with an option this build does not support; 6 live configuration
// that cannot be loaded.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"google.golang.org/grpc/grpclog"

	"github.com/aetertinas9/data-path-assurance/internal/agent"
	"github.com/aetertinas9/data-path-assurance/internal/agent/liveclient"
)

// Exit codes and their stderr tokens (GFO-011, GLI-080).
const (
	exitOK              = 0
	exitInternal        = 1
	exitUsage           = 2
	exitFixtureInvalid  = 3
	exitBoundExceeded   = 4
	exitLiveUnsupported = 5
	exitLiveConfig      = 6
)

const (
	flagFixtureRoot = "fixture-root"
	maxErrorMessage = 1024
)

// Live flag names (GFL-096, GLI-080).
const (
	flagController         = "controller"
	flagClusterID          = "cluster-id"
	flagNodeName           = "node-name"
	flagNodeUID            = "node-uid"
	flagSysfsRoot          = "sysfs-root"
	flagCAFile             = "ca-file"
	flagCertFile           = "cert-file"
	flagKeyFile            = "key-file"
	flagNVIDIASMI          = "nvidia-smi"
	flagPodResourcesSocket = "pod-resources-socket"
	flagBootIDFile         = "boot-id-file"
)

// isLiveFlag reports whether name is a flag of the live mode. Their values are
// never printed; a path value is opened only after the usage check, and
// --pod-resources-socket is never opened.
func isLiveFlag(name string) bool {
	switch name {
	case flagController, flagClusterID, flagNodeName, flagNodeUID, flagSysfsRoot,
		flagCAFile, flagCertFile, flagKeyFile, flagNVIDIASMI, flagPodResourcesSocket, flagBootIDFile:
		return true
	}
	return false
}

// requiredLiveFlags returns the live flags without which the live mode has no
// meaning (GLI-080), in the order a missing one is reported.
func requiredLiveFlags() []string {
	return []string{
		flagController, flagClusterID, flagNodeName, flagNodeUID,
		flagSysfsRoot, flagCAFile, flagCertFile, flagKeyFile,
	}
}

const usage = `usage: path-agent --fixture-root <dir>
       path-agent --controller <host:port> --cluster-id <id> --node-name <name>
                  --node-uid <uid> --sysfs-root <dir> --ca-file <file>
                  --cert-file <file> --key-file <file> [--nvidia-smi <path>]
                  [--boot-id-file <file>]

With --fixture-root, reads the offline fixture tree at <dir> and writes its
snapshot artifact to stdout. Without it, the live flags stream snapshot frames
to the controller until SIGINT or SIGTERM. --pod-resources-socket is not
supported by this build.
`

func main() {
	// grpc's default logger writes a format of its own to stderr; every stderr
	// line of path-agent is a start error line or slog text (GLI-005).
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(io.Discard, io.Discard, io.Discard))
	ignoreBrokenPipe()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run interprets args, performs the requested mode and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(stderr, usage)
		return exitOK
	}
	parsed := parseArgs(args)
	switch parsed.mode {
	case modeUsage:
		return fail(stderr, exitUsage, "usage", parsed.problem)
	case modeLive:
		return runLive(parsed.values, stderr)
	}

	root, err := agent.OpenFixtureRoot(parsed.fixtureRoot)
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

// runLive validates the live flags and runs the stream client (GLI-080). The
// order is the priority 2 > 5 > 6 > run: usage problems first, then the
// unsupported --pod-resources-socket, which is decided by its presence alone,
// then the start-up validation inside liveclient.Run. Nothing is written to
// stdout, and nothing to stderr before the validation passes except the one
// error line.
func runLive(values map[string]string, stderr io.Writer) int {
	for _, name := range requiredLiveFlags() {
		if _, ok := values[name]; !ok {
			return fail(stderr, exitUsage, "usage", "missing required flag --"+name)
		}
	}
	for name, v := range values {
		if v == "" && name != flagPodResourcesSocket {
			return fail(stderr, exitUsage, "usage", "empty value for --"+name)
		}
	}
	opts := liveclient.Options{
		Controller: values[flagController],
		ClusterID:  values[flagClusterID],
		NodeName:   values[flagNodeName],
		NodeUID:    values[flagNodeUID],
		SysfsRoot:  values[flagSysfsRoot],
		NVIDIASMI:  values[flagNVIDIASMI],
		BootIDFile: values[flagBootIDFile],
		TLS: liveclient.TLSFiles{
			CAFile:   values[flagCAFile],
			CertFile: values[flagCertFile],
			KeyFile:  values[flagKeyFile],
		},
	}
	if err := opts.ValidateFlags(); err != nil {
		return fail(stderr, exitUsage, "usage", err.Error())
	}
	if _, ok := values[flagPodResourcesSocket]; ok {
		return fail(stderr, exitLiveUnsupported, "live_unsupported", "--pod-resources-socket is not supported by this build")
	}

	opts.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals()...)
	defer stop()
	err := liveclient.Run(ctx, opts)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, liveclient.ErrInvalidOptions):
		return fail(stderr, exitUsage, "usage", err.Error())
	case errors.Is(err, liveclient.ErrLiveConfig):
		return fail(stderr, exitLiveConfig, "live_config", err.Error())
	default:
		return fail(stderr, exitInternal, "internal", "the live client stopped unexpectedly")
	}
}

// argMode is what the arguments ask for.
type argMode int

const (
	modeUsage argMode = iota
	modeOffline
	modeLive
)

// parsedArgs is the result of parseArgs.
type parsedArgs struct {
	mode        argMode
	problem     string
	fixtureRoot string
	// values holds the live flags that were given, by name.
	values map[string]string
}

// usageProblem is the parse result of a usage error.
func usageProblem(problem string) parsedArgs {
	return parsedArgs{mode: modeUsage, problem: problem}
}

// parseArgs applies GFO-010 and GLI-080. Every flag takes a value, given as
// --name=value or as the next argument. Any malformed argument is a usage
// error, which outranks every other outcome; the problem text never quotes an
// argument. --fixture-root alone is the offline mode; --fixture-root with
// another flag is a usage error; live flags without --fixture-root are the live
// mode.
func parseArgs(args []string) parsedArgs {
	if len(args) == 0 {
		return usageProblem("no arguments; --fixture-root is required")
	}
	seen := map[string]bool{}
	values := map[string]string{}
	fixtureRoot := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return usageProblem("unexpected argument")
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		if name != flagFixtureRoot && !isLiveFlag(name) {
			return usageProblem("unknown flag")
		}
		if seen[name] {
			return usageProblem("flag given more than once")
		}
		seen[name] = true
		if !hasValue {
			if i+1 >= len(args) {
				return usageProblem("flag value missing")
			}
			i++
			value = args[i]
		}
		if name == flagFixtureRoot {
			if value == "" {
				return usageProblem("empty --fixture-root value")
			}
			fixtureRoot = value
			continue
		}
		values[name] = value
	}
	if !seen[flagFixtureRoot] {
		return parsedArgs{mode: modeLive, values: values}
	}
	if len(seen) > 1 {
		return usageProblem("--fixture-root cannot be combined with other flags")
	}
	return parsedArgs{mode: modeOffline, fixtureRoot: fixtureRoot}
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
