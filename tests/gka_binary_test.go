package tests_test

// The path-controller binary without a cluster (GKA-160 flag grammar, GKA-161
// exit codes, GKA-162 stdout/stderr, GKA-163 secrets, GKA-164 wiring). The
// binary is built from the repository and only its command line, exit code and
// output are observed. Behavior against a real API server (leader election,
// projection, exit code 1 on lost leadership) is in tests/kubeapi/crd_binary_test.go.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var gkaCFlagNames = []string{
	"kubeconfig", "cluster-id", "controller-id", "leader-election-namespace", "leader-election-id",
	"leader-election-lease-duration", "leader-election-renew-deadline", "leader-election-retry-period",
	"resync-interval", "log-level",
}

var gkaCUsageMessages = []string{
	"unknown flag", "missing required flag", "invalid flag value", "invalid duration relation", "unexpected argument", "flag parse error",
}

// gkaCBinBuild builds ./cmd/path-controller (CGO off) into t.TempDir().
func gkaCBinBuild(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "path-controller")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/path-controller")
	cmd.Dir = gkaCRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GKA-164: go build ./cmd/path-controller failed: %v\n%s", err, out)
	}
	return bin
}

// gkaCBinEnvClean is the environment without anything that could make the
// binary find a cluster on its own, and with a private HOME.
func gkaCBinEnvClean(t *testing.T, extra ...string) []string {
	t.Helper()
	env := gkaCEnvWithout("KUBECONFIG", "KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT", "HOME")
	env = append(env, "HOME="+t.TempDir())
	return append(env, extra...)
}

type gkaCSafeBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *gkaCSafeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *gkaCSafeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

type gkaCBinResult struct {
	Code           int
	Stdout, Stderr string
	TimedOut       bool
}

// gkaCBinExec runs the binary to completion (it must exit by itself).
func gkaCBinExec(t *testing.T, bin string, env []string, args ...string) gkaCBinResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var out, errb gkaCSafeBuf
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start the binary: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return gkaCBinResult{Code: gkaCExitCode(err), Stdout: out.String(), Stderr: errb.String()}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return gkaCBinResult{Code: -1, Stdout: out.String(), Stderr: errb.String(), TimedOut: true}
	}
}

// gkaCStartupLine checks the GKA-162 shape of a startup failure: exactly one
// line, fixed prefix, token and message, printable ASCII only.
func gkaCStartupLine(t *testing.T, what, stderr, token string, messages ...string) {
	t.Helper()
	for _, b := range []byte(stderr) {
		if b != '\n' && (b < 0x20 || b > 0x7e) {
			t.Errorf("%s: stderr holds the byte 0x%02x (only 0x20-0x7E and newline are allowed): %q", what, b, stderr)
			return
		}
	}
	if strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") {
		t.Errorf("%s: stderr must be exactly one line ending in a newline, got %q", what, stderr)
		return
	}
	prefix := "path-controller: error: " + token + ": "
	if !strings.HasPrefix(stderr, prefix) {
		t.Errorf("%s: stderr %q does not start with %q", what, stderr, prefix)
		return
	}
	msg := strings.TrimSuffix(strings.TrimPrefix(stderr, prefix), "\n")
	ok := false
	for _, m := range messages {
		ok = ok || m == msg
	}
	if !ok {
		t.Errorf("%s: message %q, want one of %v", what, msg, messages)
	}
}

func gkaCRepeat(s string, n int) string { return strings.Repeat(s, n) }

// gkaCSubdomain253 is a valid 253-character DNS-1123 subdomain.
func gkaCSubdomain253() string {
	l := gkaCRepeat("a", 63)
	return l + "." + l + "." + l + "." + gkaCRepeat("a", 61)
}

func gkaCClosedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// gkaCWriteKubeconfig writes a kubeconfig for a closed local port.
func gkaCWriteKubeconfig(t *testing.T, token string) string {
	t.Helper()
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: gka
  cluster:
    server: https://127.0.0.1:%d
    insecure-skip-tls-verify: true
users:
- name: gka
  user:
    token: %s
contexts:
- name: gka
  context: {cluster: gka, user: gka}
current-context: gka
`, gkaCClosedPort(t), token)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ------------------------------------------------------------------ GKA-160

type gkaCFlagCase struct {
	name string
	args func(kc string) []string
	code int      // 1 = flags accepted (then the missing kubeconfig fails), 2 = usage error
	msgs []string // for code 2: allowed messages
	leak string   // must not appear on stderr
	env  []string
}

func gkaCWith(extra ...string) func(string) []string {
	return func(kc string) []string {
		return append([]string{"--kubeconfig", kc, "--cluster-id", "c1", "--leader-election-namespace", "ns"}, extra...)
	}
}

func gkaCRaw(args ...string) func(string) []string { return func(string) []string { return args } }

func gkaCOKCase(name string, extra ...string) gkaCFlagCase {
	return gkaCFlagCase{name: name, args: gkaCWith(extra...), code: 1}
}

func gkaCBadCase(name string, msg, leak string, extra ...string) gkaCFlagCase {
	return gkaCFlagCase{name: name, args: gkaCWith(extra...), code: 2, msgs: []string{msg}, leak: leak}
}

func gkaCBadEither(name, leak string, extra ...string) gkaCFlagCase {
	return gkaCFlagCase{name: name, args: gkaCWith(extra...), code: 2, msgs: []string{"invalid flag value", "flag parse error"}, leak: leak}
}

// GKA-160: the flag grammar, value rules, defaults and duration relation.
func TestGKA160_FlagGrammar(t *testing.T) {
	bin := gkaCBinBuild(t)
	const badValue, relation = "invalid flag value", "invalid duration relation"
	var cases []gkaCFlagCase

	// accepted spellings and boundary values
	for _, form := range [][]string{
		{"-cluster-id", "c1"}, {"--cluster-id", "c1"}, {"--cluster-id=c1"}, {"-cluster-id=c1"},
	} {
		form := form
		cases = append(cases, gkaCFlagCase{name: "form " + strings.Join(form, " "), code: 1,
			args: func(kc string) []string {
				return append(append([]string{"--kubeconfig", kc}, form...), "--leader-election-namespace", "ns")
			}})
	}
	cases = append(cases,
		gkaCOKCase("minimal"),
		gkaCOKCase("cluster-id 128 chars", "--cluster-id", gkaCRepeat("a", 128)),
		gkaCOKCase("cluster-id all allowed characters", "--cluster-id", "A.b_c-9"),
		gkaCOKCase("controller-id 128 chars", "--controller-id", gkaCRepeat("a", 128)),
		gkaCOKCase("controller-id all allowed characters", "--controller-id", "A.b_c-9"),
		gkaCOKCase("namespace 63 chars", "--leader-election-namespace", gkaCRepeat("a", 63)),
		gkaCOKCase("namespace with a dash and digits", "--leader-election-namespace", "a-b1"),
		gkaCOKCase("namespace starting with a digit", "--leader-election-namespace", "1a"),
		gkaCOKCase("lease id a.b", "--leader-election-id", "a.b"),
		gkaCOKCase("lease id a-b.c", "--leader-election-id", "a-b.c"),
		gkaCOKCase("lease id 253 chars", "--leader-election-id", gkaCSubdomain253()),
		gkaCOKCase("durations 1s/800ms/500ms", "--leader-election-lease-duration", "1s", "--leader-election-renew-deadline", "800ms", "--leader-election-retry-period", "500ms"),
		gkaCOKCase("renew just above 1.2 x retry", "--leader-election-lease-duration", "5s", "--leader-election-renew-deadline", "601ms", "--leader-election-retry-period", "500ms"),
		gkaCOKCase("lease just above renew", "--leader-election-lease-duration", "10000000001ns"),
		gkaCOKCase("only lease-duration above the default renew", "--leader-election-lease-duration", "11s"),
		gkaCOKCase("only retry-period 8s against the default renew 10s", "--leader-election-retry-period", "8s"),
		gkaCOKCase("only retry-period 100ms", "--leader-election-retry-period", "100ms"),
		gkaCOKCase("resync 30s", "--resync-interval", "30s"),
		gkaCOKCase("resync 1ns", "--resync-interval", "1ns"),
		gkaCOKCase("resync 1ms", "--resync-interval", "1ms"),
		gkaCOKCase("resync just under 30s", "--resync-interval", "29999999999ns"),
		gkaCOKCase("log-level debug", "--log-level", "debug"),
		gkaCOKCase("log-level info", "--log-level=info"),
		gkaCOKCase("log-level warn", "--log-level=warn"),
		gkaCOKCase("log-level error", "--log-level=error"),
		gkaCOKCase("repeated cluster-id: the last value wins (valid last)", "--cluster-id", "bad id", "--cluster-id", "c2"),
		gkaCOKCase("repeated log-level: the last value wins (valid last)", "--log-level", "bogus", "--log-level", "info"),
		gkaCOKCase("repeated resync-interval: the last value wins", "--resync-interval", "99s", "--resync-interval", "1s"),
		gkaCFlagCase{name: "empty --kubeconfig selects in-cluster (which fails outside a cluster)", code: 1,
			args: gkaCRaw("--kubeconfig=", "--cluster-id", "c1", "--leader-election-namespace", "ns")},
		gkaCFlagCase{name: "no --kubeconfig selects in-cluster (which fails outside a cluster)", code: 1,
			args: gkaCRaw("--cluster-id", "c1", "--leader-election-namespace", "ns")},
	)

	// required flags
	cases = append(cases,
		gkaCFlagCase{name: "no arguments", args: gkaCRaw(), code: 2, msgs: []string{"missing required flag"}},
		gkaCFlagCase{name: "cluster-id missing", args: gkaCRaw("--leader-election-namespace", "ns"), code: 2, msgs: []string{"missing required flag"}},
		gkaCFlagCase{name: "namespace missing", args: gkaCRaw("--cluster-id", "c1"), code: 2, msgs: []string{"missing required flag"}},
		gkaCFlagCase{name: "only optional flags", args: gkaCRaw("--log-level", "info"), code: 2, msgs: []string{"missing required flag"}},
	)

	// value rules
	cases = append(cases,
		gkaCBadCase("cluster-id empty", badValue, "", "--cluster-id", ""),
		gkaCBadCase("cluster-id 129 chars", badValue, gkaCRepeat("a", 129), "--cluster-id", gkaCRepeat("a", 129)),
		gkaCBadCase("cluster-id with a space", badValue, "bad id gka", "--cluster-id", "bad id gka"),
		gkaCBadCase("cluster-id with a slash", badValue, "a/b/gka", "--cluster-id", "a/b/gka"),
		gkaCBadCase("cluster-id with an at sign", badValue, "a@gka", "--cluster-id", "a@gka"),
		gkaCBadCase("cluster-id non-ASCII", badValue, "\uac00\ub098", "--cluster-id", "\uac00\ub098"),
		gkaCBadCase("controller-id empty", badValue, "", "--controller-id", ""),
		gkaCBadCase("controller-id 129 chars", badValue, gkaCRepeat("b", 129), "--controller-id", gkaCRepeat("b", 129)),
		gkaCBadCase("controller-id with a space", badValue, "bad ctl gka", "--controller-id", "bad ctl gka"),
		gkaCBadCase("namespace empty", badValue, "", "--leader-election-namespace", ""),
		gkaCBadCase("namespace upper case", badValue, "Upper", "--leader-election-namespace", "Upper"),
		gkaCBadCase("namespace underscore", badValue, "a_b_gka", "--leader-election-namespace", "a_b_gka"),
		gkaCBadCase("namespace leading dash", badValue, "-lead", "--leader-election-namespace", "-lead"),
		gkaCBadCase("namespace trailing dash", badValue, "trail-", "--leader-election-namespace", "trail-"),
		gkaCBadCase("namespace 64 chars", badValue, gkaCRepeat("a", 64), "--leader-election-namespace", gkaCRepeat("a", 64)),
		gkaCBadCase("namespace with a dot (a label, not a subdomain)", badValue, "a.b.gka", "--leader-election-namespace", "a.b.gka"),
		gkaCBadCase("lease id upper case", badValue, "Upper", "--leader-election-id", "Upper"),
		gkaCBadCase("lease id underscore", badValue, "a_b_gka", "--leader-election-id", "a_b_gka"),
		gkaCBadCase("lease id leading dash", badValue, "-lead", "--leader-election-id", "-lead"),
		gkaCBadCase("lease id trailing dash", badValue, "trail-", "--leader-election-id", "trail-"),
		gkaCBadCase("lease id empty label", badValue, "a..b.gka", "--leader-election-id", "a..b.gka"),
		gkaCBadCase("lease id leading dot", badValue, ".lead", "--leader-election-id", ".lead"),
		gkaCBadCase("lease id trailing dot", badValue, "trail.", "--leader-election-id", "trail."),
		gkaCBadCase("lease id 254 chars", badValue, gkaCSubdomain253()+"a", "--leader-election-id", gkaCSubdomain253()+"a"),
		// DNS-1123 subdomain is the Kubernetes definition: only the 253-character total is limited, not each label.
		gkaCOKCase("lease id with a 64 char label is a valid subdomain", "--leader-election-id", gkaCRepeat("a", 64)+".b"),
		gkaCBadCase("log-level upper case", badValue, "INFO", "--log-level", "INFO"),
		gkaCBadCase("log-level trace", badValue, "trace", "--log-level", "trace"),
		gkaCBadCase("log-level empty", badValue, "", "--log-level", ""),
		gkaCBadCase("log-level warning", badValue, "warning", "--log-level", "warning"),
		gkaCBadCase("log-level Debug", badValue, "Debug", "--log-level", "Debug"),
		gkaCBadCase("repeated cluster-id: the last value wins (invalid last)", badValue, "bad id last", "--cluster-id", "c2", "--cluster-id", "bad id last"),
		gkaCBadCase("repeated log-level: the last value wins (invalid last)", badValue, "bogus-last", "--log-level", "info", "--log-level", "bogus-last"),
	)

	// durations: value rules may surface as a value or a parse error.
	for _, c := range []struct{ name, flag, value string }{
		{"lease-duration abc", "--leader-election-lease-duration", "abc"},
		{"lease-duration without a unit", "--leader-election-lease-duration", "10"},
		{"lease-duration empty", "--leader-election-lease-duration", ""},
		{"lease-duration negative", "--leader-election-lease-duration", "-1s"},
		{"lease-duration zero", "--leader-election-lease-duration", "0s"},
		{"renew-deadline abc", "--leader-election-renew-deadline", "abc"},
		{"renew-deadline zero", "--leader-election-renew-deadline", "0s"},
		{"renew-deadline negative", "--leader-election-renew-deadline", "-5s"},
		{"retry-period abc", "--leader-election-retry-period", "abc"},
		{"retry-period zero", "--leader-election-retry-period", "0s"},
		{"retry-period negative", "--leader-election-retry-period", "-1s"},
		{"resync-interval abc", "--resync-interval", "abc"},
		{"resync-interval zero", "--resync-interval", "0"},
		{"resync-interval 0s", "--resync-interval", "0s"},
		{"resync-interval negative", "--resync-interval", "-1s"},
		{"resync-interval 30s+1ns", "--resync-interval", "30000000001ns"},
		{"resync-interval 31s", "--resync-interval", "31s"},
		{"resync-interval empty", "--resync-interval", ""},
	} {
		cases = append(cases, gkaCBadEither(c.name, "", c.flag, c.value))
	}
	cases = append(cases,
		gkaCFlagCase{name: "lease-duration under 1s (relation holds)", code: 2, msgs: []string{badValue, relation}, args: gkaCWith(
			"--leader-election-lease-duration", "999ms", "--leader-election-renew-deadline", "800ms", "--leader-election-retry-period", "500ms")},
		gkaCBadCase("lease equal to renew", relation, "", "--leader-election-lease-duration", "10s", "--leader-election-renew-deadline", "10s"),
		gkaCBadCase("lease 1s against the default renew 10s", relation, "", "--leader-election-lease-duration", "1s"),
		gkaCBadCase("renew 15s equals the default lease 15s", relation, "", "--leader-election-renew-deadline", "15s"),
		gkaCBadCase("retry 9s makes renew 10s <= 1.2 x retry", relation, "", "--leader-election-retry-period", "9s"),
		gkaCBadCase("renew exactly 1.2 x retry", relation, "", "--leader-election-lease-duration", "5s", "--leader-election-renew-deadline", "600ms", "--leader-election-retry-period", "500ms"),
		gkaCBadCase("renew equals retry", relation, "", "--leader-election-lease-duration", "5s", "--leader-election-renew-deadline", "500ms", "--leader-election-retry-period", "500ms"),
		// 1.2 x retry beyond the Duration range must still violate the relation on every GOARCH.
		gkaCBadCase("retry 2400000h makes 1.2 x retry overflow", relation, "", "--leader-election-lease-duration", "20s", "--leader-election-renew-deadline", "10s", "--leader-election-retry-period", "2400000h"),
		gkaCBadCase("retry at the Duration maximum", relation, "", "--leader-election-lease-duration", "20s", "--leader-election-renew-deadline", "10s", "--leader-election-retry-period", "9223372036854775807ns"),
	)

	// unknown flags, positional arguments, missing values
	cases = append(cases,
		gkaCBadCase("unknown flag with value", "unknown flag", "bogus-flag-gka", "--bogus-flag-gka=secretvalue"),
		gkaCBadCase("unknown flag single dash", "unknown flag", "zzgka", "-zzgka"),
		gkaCBadCase("unknown flag without value", "unknown flag", "bogus-gka", "--bogus-gka"),
		gkaCBadCase("positional argument", "unexpected argument", "positional-gka", "positional-gka"),
		gkaCBadCase("positional argument after --", "unexpected argument", "afterdash-gka", "--", "afterdash-gka"),
		gkaCFlagCase{name: "flag without its value at the end", code: 2, msgs: []string{"flag parse error"}, args: gkaCRaw("--leader-election-namespace", "ns", "--cluster-id")},
		gkaCFlagCase{name: "duration flag without its value at the end", code: 2, msgs: []string{"flag parse error"}, args: gkaCWith("--resync-interval")},
		gkaCFlagCase{name: "validation precedes file access", code: 2, msgs: []string{badValue}, leak: "no-such-dir-gka",
			args: gkaCRaw("--kubeconfig", "/no-such-dir-gka/kubeconfig", "--cluster-id", "bad id", "--leader-election-namespace", "ns")},
	)
	// flags of other frameworks and of later features are not accepted.
	for _, f := range []string{
		"--listen=:8080", "--ingest-addr=:9090", "--tls-cert=x", "--tls-key=x", "--metrics-bind-address=:8080", "--health-probe-bind-address=:8081",
		"--leader-elect", "--namespace=ns", "--kubeconfig-context=x", "--version", "-v=2", "--logtostderr", "--alsologtostderr", "--stderrthreshold=INFO",
		"--vmodule=x=1", "--log_dir=/x", "--zap-log-level=debug", "--enable-pprof", "--pprof-bind-address=:6060", "--cluster=x", "--config=x", "--output=json",
	} {
		leak := strings.TrimLeft(strings.SplitN(f, "=", 2)[0], "-")
		if len(leak) < 3 {
			leak = ""
		}
		cases = append(cases, gkaCBadCase("rejects "+f, "unknown flag", leak, f))
	}

	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			kc := filepath.Join(t.TempDir(), "absent", "kubeconfig")
			res := gkaCBinExec(t, bin, gkaCBinEnvClean(t, c.env...), c.args(kc)...)
			what := "GKA-160/161/162 " + c.name
			if res.TimedOut {
				t.Fatalf("%s: the binary did not exit", what)
			}
			if res.Code != c.code {
				t.Errorf("%s: exit %d, want %d; stderr %q", what, res.Code, c.code, res.Stderr)
			}
			if res.Stdout != "" {
				t.Errorf("%s: stdout is not empty: %q", what, res.Stdout)
			}
			switch c.code {
			case 1:
				gkaCStartupLine(t, what, res.Stderr, "config", "cannot load kubeconfig")
			case 2:
				gkaCStartupLine(t, what, res.Stderr, "usage", c.msgs...)
			}
			if c.leak != "" && strings.Contains(res.Stderr, c.leak) {
				t.Errorf("%s: stderr repeats the argument %q: %q", what, c.leak, res.Stderr)
			}
			if strings.Contains(res.Stderr, kc) || strings.Contains(res.Stderr, "absent") {
				t.Errorf("%s: stderr contains the kubeconfig path: %q", what, res.Stderr)
			}
		})
	}
}

// ------------------------------------------------------------------ GKA-161

// GKA-161: -h/-help/--help exit 0 with the usage on stderr (all flag names).
func TestGKA161_HelpExitsZero(t *testing.T) {
	bin := gkaCBinBuild(t)
	for _, args := range [][]string{{"-h"}, {"-help"}, {"--help"}, {"--cluster-id", "c1", "--help"}} {
		res := gkaCBinExec(t, bin, gkaCBinEnvClean(t), args...)
		what := "GKA-161 " + strings.Join(args, " ")
		if res.TimedOut || res.Code != 0 {
			t.Errorf("%s: exit %d (timed out %v), want 0; stderr:\n%s", what, res.Code, res.TimedOut, res.Stderr)
		}
		if res.Stdout != "" {
			t.Errorf("GKA-162 %s: stdout is not empty: %q", what, res.Stdout)
		}
		for _, name := range gkaCFlagNames {
			if !strings.Contains(res.Stderr, "-"+name) {
				t.Errorf("GKA-162 %s: the usage on stderr does not name the flag -%s:\n%s", what, name, res.Stderr)
			}
		}
		if strings.Contains(res.Stderr, "path-controller: error:") {
			t.Errorf("GKA-162 %s: help must not print an error line:\n%s", what, res.Stderr)
		}
	}
}

// GKA-161: runtime and configuration failures exit 1 with the fixed line, for
// every way a kubeconfig can be unusable.
func TestGKA161_ConfigFailuresExitOne(t *testing.T) {
	bin := gkaCBinBuild(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const marker = "GKA161-CONTENT-MARKER"
	cases := map[string]string{
		"absent file":                   filepath.Join(dir, "does-not-exist"),
		"a directory":                   dir,
		"an empty file":                 write("empty", ""),
		"not yaml":                      write("garbage", "\x00\x01 {{{ "+marker),
		"yaml without clusters":         write("noclusters", "apiVersion: v1\nkind: Config\n# "+marker+"\n"),
		"no current context":            write("noctx", "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster: {server: 'https://127.0.0.1:1'}\nusers:\n- name: a\n  user: {token: "+marker+"}\n"),
		"invalid certificate authority": write("badca", "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster: {server: 'https://127.0.0.1:1', certificate-authority-data: '"+marker+"!!'}\nusers:\n- name: a\n  user: {token: x}\ncontexts:\n- name: a\n  context: {cluster: a, user: a}\ncurrent-context: a\n"),
	}
	for name, path := range cases {
		res := gkaCBinExec(t, bin, gkaCBinEnvClean(t), "--kubeconfig", path, "--cluster-id", "c1", "--leader-election-namespace", "ns")
		what := "GKA-161/163 " + name
		if res.TimedOut {
			t.Errorf("%s: the binary did not exit", what)
			continue
		}
		if res.Code != 1 {
			t.Errorf("%s: exit %d, want 1; stderr %q", what, res.Code, res.Stderr)
		}
		gkaCStartupLine(t, what, res.Stderr, "config", "cannot load kubeconfig")
		if res.Stdout != "" {
			t.Errorf("%s: stdout is not empty", what)
		}
		if strings.Contains(res.Stderr, marker) || strings.Contains(res.Stderr, path) {
			t.Errorf("%s: stderr exposes kubeconfig content or path: %q", what, res.Stderr)
		}
	}
}

// GKA-020/026/161: with no --kubeconfig only the in-cluster configuration is
// read: neither $KUBECONFIG nor the default file can make the binary start.
func TestGKA161_NoKubeconfigMeansInClusterOnly(t *testing.T) {
	bin := gkaCBinBuild(t)
	valid := gkaCWriteKubeconfig(t, "x")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(valid)
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(gkaCEnvWithout("KUBECONFIG", "KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT", "HOME"), "KUBECONFIG="+valid, "HOME="+home)
	res := gkaCBinExec(t, bin, env, "--cluster-id", "c1", "--leader-election-namespace", "ns")
	if res.TimedOut {
		t.Fatalf("GKA-161: without --kubeconfig the binary kept running: it must not read $KUBECONFIG or ~/.kube/config")
	}
	if res.Code != 1 {
		t.Errorf("GKA-161: exit %d without --kubeconfig outside a cluster, want 1; stderr %q", res.Code, res.Stderr)
	}
	gkaCStartupLine(t, "GKA-161 in-cluster only", res.Stderr, "config", "cannot load kubeconfig")
}

type gkaCLive struct {
	cmd            *exec.Cmd
	stdout, stderr gkaCSafeBuf
	done           chan struct{}
	err            error
}

func gkaCSpawn(t *testing.T, bin string, env []string, args ...string) *gkaCLive {
	t.Helper()
	p := &gkaCLive{done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = env
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("cannot start the binary: %v", err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *gkaCLive) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *gkaCLive) wait(timeout time.Duration) (int, bool) {
	select {
	case <-p.done:
		return gkaCExitCode(p.err), true
	case <-time.After(timeout):
		return 0, false
	}
}

// GKA-161, GKA-146, GKA-164: pointed at a closed port the binary does not give
// up (Run stays alive), and SIGTERM and SIGINT stop it with exit code 0 within
// 10 seconds; nothing is written to stdout and every stderr line is slog text.
func TestGKA161_SignalsStopWithExitZero(t *testing.T) {
	bin := gkaCBinBuild(t)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			kc := gkaCWriteKubeconfig(t, "x")
			p := gkaCSpawn(t, bin, gkaCBinEnvClean(t),
				"--kubeconfig", kc, "--cluster-id", "c1", "--leader-election-namespace", "ns", "--resync-interval", "300ms", "--log-level", "debug")
			time.Sleep(2500 * time.Millisecond)
			if p.exited() {
				code, _ := p.wait(0)
				t.Fatalf("GKA-146: the binary exited with %d on an unreachable API server instead of retrying; stderr:\n%s", code, p.stderr.String())
			}
			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			code, ok := p.wait(12 * time.Second) // the 10 s of GKA-022(a) plus the 2 s load margin
			if !ok {
				t.Fatalf("GKA-161/164: still running 12 s after %s; stderr:\n%s", sig, p.stderr.String())
			}
			if code != 0 {
				t.Errorf("GKA-161: exit %d after %s, want 0; stderr:\n%s", code, sig, p.stderr.String())
			}
			if out := p.stdout.String(); out != "" {
				t.Errorf("GKA-162: stdout is not empty: %q", out)
			}
			for i, l := range strings.Split(strings.TrimRight(p.stderr.String(), "\n"), "\n") {
				if strings.TrimSpace(l) != "" && !strings.Contains(l, "level=") {
					t.Errorf("GKA-162: stderr line %d is not a slog text record (no level=): %q", i+1, l)
				}
			}
		})
	}
}

// ------------------------------------------------------------------ GKA-162

// GKA-162: every startup failure is one fixed line; no flag value, unknown flag
// name or path is echoed (the 160 table checks each case); here the shape of
// the whole message set is pinned once more over hostile input.
func TestGKA162_HostileArgumentsAreNeverEchoed(t *testing.T) {
	bin := gkaCBinBuild(t)
	const marker = "GKA162-ECHO-MARKER"
	for _, args := range [][]string{
		{"--cluster-id", marker + " bad id", "--leader-election-namespace", "ns"},
		{"--cluster-id", "c1", "--leader-election-namespace", marker + "_Bad"},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--" + marker + "=1"},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", marker},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--log-level", marker},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--resync-interval", marker},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--leader-election-id", marker},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--kubeconfig", "/" + marker + "/x", "--controller-id", marker + " x"},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--kubeconfig", "/" + marker + "/x"},
		{"--cluster-id", "c1", "--leader-election-namespace", "ns", "--\x01" + marker},
	} {
		res := gkaCBinExec(t, bin, gkaCBinEnvClean(t), args...)
		if res.TimedOut {
			t.Errorf("GKA-162 %q: the binary did not exit", args)
			continue
		}
		if strings.Contains(res.Stderr, marker) || res.Stdout != "" {
			t.Errorf("GKA-162 %q: the argument was echoed or stdout used: stderr %q stdout %q", args, res.Stderr, res.Stdout)
		}
		if res.Code != 1 && res.Code != 2 {
			t.Errorf("GKA-162 %q: exit %d, want 1 or 2", args, res.Code)
		}
		token := map[int]string{1: "config", 2: "usage"}[res.Code]
		msgs := append([]string{"cannot load kubeconfig"}, gkaCUsageMessages...)
		gkaCStartupLine(t, fmt.Sprintf("GKA-162 %q", args), res.Stderr, token, msgs...)
	}
}

// ------------------------------------------------------------------ GKA-163

// GKA-163: kubeconfig secrets never reach stdout or stderr: a marker token with
// an unreachable server, an unusable file, and a usage error that names the
// kubeconfig path.
func TestGKA163_SecretsNeverPrinted(t *testing.T) {
	bin := gkaCBinBuild(t)
	const marker = "GKA163-TOKEN-MARKER-77c2"

	// connection failure: the binary keeps running; stop it and inspect the output.
	kc := gkaCWriteKubeconfig(t, marker)
	p := gkaCSpawn(t, bin, gkaCBinEnvClean(t), "--kubeconfig", kc, "--cluster-id", "c1", "--leader-election-namespace", "ns",
		"--resync-interval", "300ms", "--log-level", "debug")
	time.Sleep(3 * time.Second)
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	if _, ok := p.wait(12 * time.Second); !ok {
		t.Fatalf("GKA-163: the binary did not stop after SIGTERM")
	}
	for name, out := range map[string]string{"stdout": p.stdout.String(), "stderr": p.stderr.String()} {
		if strings.Contains(out, marker) {
			t.Errorf("GKA-163: the token marker is printed on %s while connecting to an unreachable server", name)
		}
	}

	// unusable files that contain the marker.
	dir := t.TempDir()
	badFiles := map[string]string{
		"garbage":  "\x00 {{{ " + marker,
		"badca":    "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster: {server: 'https://127.0.0.1:1', certificate-authority-data: '" + marker + "!!'}\nusers:\n- name: a\n  user: {token: " + marker + "}\ncontexts:\n- name: a\n  context: {cluster: a, user: a}\ncurrent-context: a\n",
		"nocert":   "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster: {server: 'https://127.0.0.1:1', insecure-skip-tls-verify: true}\nusers:\n- name: a\n  user: {client-certificate-data: " + "R0tBMTYzLUNFUlQtTUFSS0VS" + ", client-key-data: R0tBMTYzLUtFWS1NQVJLRVI=, token: " + marker + "}\ncontexts:\n- name: a\n  context: {cluster: a, user: a}\ncurrent-context: a\n",
		"marker!!": "not a kubeconfig " + marker,
	}
	for name, body := range badFiles {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		p := gkaCSpawn(t, bin, gkaCBinEnvClean(t), "--kubeconfig", path, "--cluster-id", "c1", "--leader-election-namespace", "ns", "--log-level", "debug")
		code, exited := p.wait(3 * time.Second)
		if !exited {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
			p.wait(12 * time.Second)
		}
		for out, text := range map[string]string{"stdout": p.stdout.String(), "stderr": p.stderr.String()} {
			if strings.Contains(text, marker) || strings.Contains(text, "R0tBMTYz") || strings.Contains(text, "GKA163-CERT") {
				t.Errorf("GKA-163 %s (exit %d, exited %v): secret content on %s:\n%s", name, code, exited, out, text)
			}
			if strings.Contains(text, path) {
				t.Errorf("GKA-163 %s: the kubeconfig path is printed on %s:\n%s", name, out, text)
			}
		}
	}

	// the kubeconfig path is not part of a usage error either.
	res := gkaCBinExec(t, bin, gkaCBinEnvClean(t), "--kubeconfig", "/"+marker+"/kubeconfig")
	if res.Code != 2 || strings.Contains(res.Stderr, marker) || res.Stdout != "" {
		t.Errorf("GKA-163: usage error with a kubeconfig path: exit %d stderr %q stdout %q", res.Code, res.Stderr, res.Stdout)
	}
}

// ------------------------------------------------------------------ GKA-164

// GKA-164: main only wires: it calls controller.LoadRESTConfig,
// app.NewLiveAssessor(app.NewNoObservationSource()), controller.New,
// signal.NotifyContext and Run, and exits through os.Exit in main alone.
func TestGKA164_MainIsThinWiring(t *testing.T) {
	pkgs := gkaCListPkgs(t, nil, "./cmd/path-controller")
	if len(pkgs) != 1 {
		t.Fatalf("GKA-164: cmd/path-controller not found")
	}
	names := map[string]bool{}
	appUse := map[string]bool{}
	mainFound, exitInMain := false, false
	runCall := false
	for _, file := range pkgs[0].GoFiles {
		path := filepath.Join(pkgs[0].Dir, file)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		alias := map[string]string{} // local name -> import path
		for _, is := range f.Imports {
			p, _ := strconv.Unquote(is.Path.Value)
			local := p[strings.LastIndex(p, "/")+1:]
			if is.Name != nil {
				local = is.Name.Name
			}
			alias[local] = p
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			isMain := fd.Recv == nil && fd.Name.Name == "main"
			mainFound = mainFound || isMain
			if fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "Run" {
					runCall = true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch p := alias[id.Name]; {
				case strings.HasSuffix(p, "/internal/app"):
					appUse[sel.Sel.Name] = true
				case strings.HasSuffix(p, "/internal/kubernetes/controller"):
					names["controller."+sel.Sel.Name] = true
				case p == "os/signal":
					names["signal."+sel.Sel.Name] = true
				case p == "os" && sel.Sel.Name == "Exit":
					if isMain {
						exitInMain = true
					}
				}
				return true
			})
		}
	}
	if !mainFound {
		t.Fatalf("GKA-164: no func main in cmd/path-controller")
	}
	for _, want := range []string{"controller.LoadRESTConfig", "controller.New", "signal.NotifyContext"} {
		if !names[want] {
			t.Errorf("GKA-164: the binary never calls %s", want)
		}
	}
	if !runCall {
		t.Errorf("GKA-164: the binary never calls Run")
	}
	if !appUse["NewLiveAssessor"] || !appUse["NewNoObservationSource"] {
		t.Errorf("GKA-164: the binary must wire app.NewLiveAssessor(app.NewNoObservationSource()); app identifiers used: %v", appUse)
	}
	for id := range appUse {
		if strings.HasPrefix(id, "New") && id != "NewLiveAssessor" && id != "NewNoObservationSource" {
			t.Errorf("GKA-164/033: the binary uses app.%s; S3a wires only NewLiveAssessor(NewNoObservationSource())", id)
		}
	}
	if !exitInMain {
		t.Errorf("GKA-164: main must terminate the process with os.Exit(code)")
	}
}
