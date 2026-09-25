package explain

import (
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/app"
)

// invocation is a parsed offline explain request.
type invocation struct {
	kind            app.TargetKind
	name            string
	artifact, fleet string
	output          string
}

const (
	flagArtifact = "artifact"
	flagFleet    = "fleet"
	flagOutput   = "output"
)

// transportFlags are the live transport flags. They are recognized only to be
// refused; their values are never opened, stat'ed or printed.
var transportFlags = map[string]bool{
	"server":     true,
	"cluster-id": true,
	"ca-file":    true,
	"cert-file":  true,
	"key-file":   true,
}

func knownFlag(name string) bool {
	return name == flagArtifact || name == flagFleet || name == flagOutput || transportFlags[name]
}

// parseArgs interprets `explain gpu|node <name>` followed by --name value or
// --name=value flags. Every syntax problem is a usage error, decided before
// any file is touched and ahead of the live-path refusal. The problem text
// never quotes an argument.
func parseArgs(args []string) (invocation, int, string) {
	var inv invocation
	if len(args) < 3 {
		return inv, ExitUsage, "expected: explain gpu|node <name> --artifact <file> --fleet <file>"
	}
	if args[0] != "explain" {
		return inv, ExitUsage, "the first argument must be explain"
	}
	switch args[1] {
	case "gpu":
		inv.kind = app.TargetGPU
	case "node":
		inv.kind = app.TargetNode
	default:
		return inv, ExitUsage, "the target kind must be gpu or node"
	}
	if !validName(args[2]) {
		return inv, ExitUsage, "the name must be 1-253 printable ASCII bytes not starting with -"
	}
	inv.name = args[2]

	values := map[string]string{}
	for i := 3; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return inv, ExitUsage, "unexpected argument; flags must be --name or --name=value"
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		if !knownFlag(name) {
			return inv, ExitUsage, "unknown flag"
		}
		if _, dup := values[name]; dup {
			return inv, ExitUsage, "a flag is given more than once"
		}
		if !hasValue {
			if i+1 >= len(args) {
				return inv, ExitUsage, "a flag value is missing"
			}
			i++
			value = args[i]
		}
		if value == "" {
			return inv, ExitUsage, "a flag value is empty"
		}
		if name == flagOutput && value != "text" && value != "json" {
			return inv, ExitUsage, "--output must be text or json"
		}
		values[name] = value
	}

	transport := false
	for name := range transportFlags {
		if _, ok := values[name]; ok {
			transport = true
		}
	}
	artifact, hasArtifact := values[flagArtifact]
	fleet, hasFleet := values[flagFleet]
	switch {
	case hasArtifact && hasFleet && transport:
		return inv, ExitUsage, "offline flags cannot be combined with transport flags"
	case hasArtifact && hasFleet:
	case hasArtifact || hasFleet:
		return inv, ExitUsage, "--artifact and --fleet must be given together"
	case transport:
		return inv, ExitLiveUnsupported, "the live transport path is not supported by this build; use --artifact and --fleet"
	default:
		return inv, ExitUsage, "--artifact and --fleet are required"
	}
	inv.artifact, inv.fleet = artifact, fleet
	inv.output = "text"
	if v, ok := values[flagOutput]; ok {
		inv.output = v
	}
	return inv, ExitOK, ""
}

// validName reports whether s is 1-253 bytes of 0x21-0x7E not starting
// with '-'.
func validName(s string) bool {
	if len(s) < 1 || len(s) > 253 || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
