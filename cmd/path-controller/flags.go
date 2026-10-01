package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
)

// config is the validated command line.
type config struct {
	kubeconfig    string
	clusterID     string
	controllerID  string
	namespace     string
	leaderID      string
	leaseDuration time.Duration
	renewDeadline time.Duration
	retryPeriod   time.Duration
	resync        time.Duration
	logLevel      string
}

// The fixed usage messages (GKA-162). None carries a flag name or value.
const (
	msgUnknownFlag   = "unknown flag"
	msgMissingFlag   = "missing required flag"
	msgInvalidValue  = "invalid flag value"
	msgBadRelation   = "invalid duration relation"
	msgUnexpectedArg = "unexpected argument"
	msgParseError    = "flag parse error"
)

// usageError is a command line error; msg is one of the fixed messages.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// errHelp reports a -h, -help or --help request.
var errHelp = errors.New("help requested")

const (
	defaultLeaderID      = "path-controller"
	defaultLeaseDuration = 15 * time.Second
	defaultRenewDeadline = 10 * time.Second
	defaultRetryPeriod   = 2 * time.Second
	defaultResync        = 30 * time.Second
	maxResync            = 30 * time.Second
	logLevelDefault      = "info"
)

var (
	identifierRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	dnsLabelRe   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// dnsSubdomainRe is the pattern of validation.IsDNS1123Subdomain.
	dnsSubdomainRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

func isDNSLabel(s string) bool { return len(s) <= 63 && dnsLabelRe.MatchString(s) }

// isDNSSubdomain is the Kubernetes DNS-1123 subdomain (apimachinery
// validation.IsDNS1123Subdomain): at most 253 characters matching the pattern,
// with no limit on the length of the single labels. controller.New applies the
// same definition.
func isDNSSubdomain(s string) bool {
	return len(s) <= 253 && dnsSubdomainRe.MatchString(s)
}

// durationFlag sets *dst from a positive Go duration within [lo, hi] (hi 0
// means no upper bound).
func durationFlag(dst func(*config) *time.Duration, lo, hi time.Duration) func(*config, string) bool {
	return func(c *config, v string) bool {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d < lo || (hi > 0 && d > hi) {
			return false
		}
		*dst(c) = d
		return true
	}
}

// flagSpec is one accepted flag; set validates the value and stores it.
type flagSpec struct {
	name  string
	value string // usage placeholder
	desc  string
	set   func(c *config, v string) bool
}

func flagTable() []flagSpec {
	return []flagSpec{
		{"kubeconfig", "path", "kubeconfig file; empty means in-cluster configuration",
			func(c *config, v string) bool { c.kubeconfig = v; return true }},
		{"cluster-id", "id", "cluster identifier, [A-Za-z0-9._-]{1,128} (required)",
			func(c *config, v string) bool { c.clusterID = v; return identifierRe.MatchString(v) }},
		{"controller-id", "id", "controller identifier, also the Lease holder; default <hostname>-<8 hex>",
			func(c *config, v string) bool { c.controllerID = v; return identifierRe.MatchString(v) }},
		{"leader-election-namespace", "namespace", "namespace of the leader election Lease (required)",
			func(c *config, v string) bool { c.namespace = v; return isDNSLabel(v) }},
		{"leader-election-id", "name", "name of the leader election Lease (default " + defaultLeaderID + ")",
			func(c *config, v string) bool { c.leaderID = v; return isDNSSubdomain(v) }},
		{"leader-election-lease-duration", "duration", "lease duration, at least 1s (default 15s)",
			durationFlag(func(c *config) *time.Duration { return &c.leaseDuration }, time.Second, 0)},
		{"leader-election-renew-deadline", "duration", "renew deadline (default 10s)",
			durationFlag(func(c *config) *time.Duration { return &c.renewDeadline }, 0, 0)},
		{"leader-election-retry-period", "duration", "retry period (default 2s)",
			durationFlag(func(c *config) *time.Duration { return &c.retryPeriod }, 0, 0)},
		{"resync-interval", "duration", "interval of the periodic full pass, above 0 and at most 30s (default 30s)",
			durationFlag(func(c *config) *time.Duration { return &c.resync }, 0, maxResync)},
		{"log-level", "level", "debug, info, warn or error (default " + logLevelDefault + ")",
			func(c *config, v string) bool {
				switch v {
				case "debug", "info", "warn", "error":
					c.logLevel = v
					return true
				}
				return false
			}},
	}
}

// parseArgs parses and validates the command line with the syntax of Go's flag
// package (-x, --x, -x=v, --x v). It touches neither the network nor files.
// It returns errHelp for a help request and a *usageError for every other
// problem.
func parseArgs(args []string) (config, error) {
	cfg := config{logLevel: logLevelDefault}
	table := flagTable()
	raw := map[string]string{} // the last value of each flag (a repeated flag: the last one wins)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			return cfg, &usageError{msgUnexpectedArg}
		}
		name := a[1:]
		if name[0] == '-' {
			name = name[1:]
			if name == "" { // "--" ends the flags; anything after it is positional
				if i+1 < len(args) {
					return cfg, &usageError{msgUnexpectedArg}
				}
				break
			}
		}
		if name == "" || name[0] == '-' || name[0] == '=' {
			return cfg, &usageError{msgParseError}
		}
		value, hasValue := "", false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value, hasValue = name[:eq], name[eq+1:], true
		}
		if name == "h" || name == "help" {
			return cfg, errHelp
		}
		var spec *flagSpec
		for j := range table {
			if table[j].name == name {
				spec = &table[j]
				break
			}
		}
		if spec == nil {
			return cfg, &usageError{msgUnknownFlag}
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return cfg, &usageError{msgParseError}
			}
			value = args[i]
		}
		raw[spec.name] = value
	}
	// Value rules apply to the final value of every flag, in table order.
	for _, spec := range table {
		if v, ok := raw[spec.name]; ok && !spec.set(&cfg, v) {
			return cfg, &usageError{msgInvalidValue}
		}
	}
	if cfg.clusterID == "" || cfg.namespace == "" {
		return cfg, &usageError{msgMissingFlag}
	}
	if cfg.leaderID == "" {
		cfg.leaderID = defaultLeaderID
	}
	if cfg.leaseDuration == 0 {
		cfg.leaseDuration = defaultLeaseDuration
	}
	if cfg.renewDeadline == 0 {
		cfg.renewDeadline = defaultRenewDeadline
	}
	if cfg.retryPeriod == 0 {
		cfg.retryPeriod = defaultRetryPeriod
	}
	if cfg.resync == 0 {
		cfg.resync = defaultResync
	}
	// The relation of controller.New: LeaseDuration > RenewDeadline > 1.2 x RetryPeriod.
	if cfg.leaseDuration <= cfg.renewDeadline || cfg.renewDeadline <= jitteredRetry(cfg.retryPeriod) {
		return cfg, &usageError{msgBadRelation}
	}
	if cfg.controllerID == "" {
		cfg.controllerID = defaultControllerID()
	}
	return cfg, nil
}

// jitteredRetry returns 1.2 x retry, saturated at the largest Duration.
// Converting an out-of-range float64 to an integer is implementation-dependent
// in Go (amd64 yields the minimum int64), which would let a huge retry period
// pass the relation check.
func jitteredRetry(retry time.Duration) time.Duration {
	f := 1.2 * float64(retry)
	if f >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return time.Duration(f)
}

// defaultControllerID is "<hostname>-<8 hex>": characters of the hostname
// outside the identifier set become '-', the hostname is cut to 119
// characters, and the suffix is fresh for every process.
func defaultControllerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "path-controller"
	}
	var b strings.Builder
	for _, r := range host {
		if r < 0x80 && (r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 119 {
		name = name[:119]
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to a
		// constant so that the id still matches the pattern.
		suffix = [4]byte{}
	}
	return name + "-" + hex.EncodeToString(suffix[:])
}

// writeUsage prints every flag with its meaning.
func writeUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: path-controller --cluster-id <id> --leader-election-namespace <namespace> [flags]")
	for _, f := range flagTable() {
		_, _ = fmt.Fprintf(w, "  --%s <%s>\n      %s\n", f.name, f.value, f.desc)
	}
}
