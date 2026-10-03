package liveingest_test

// The two binaries of gpu-fleet-live-ingest, built once per test process and
// observed through their command line, exit code, stdout/stderr, signals and the
// sockets they open (scenario group GLI-124 (f) and the cluster-free part of
// GLI-090..093):
//
//	path-agent      GLI-080 exit codes 0/2/5/6 and their priority 2 > 5 > 6, the
//	                one-line start-up error, signal exit within 10 s, GLI-086,
//	                GLI-101 secret hygiene, GLI-005 grpclog.
//	path-controller GLI-090 ingest flag usage, GLI-091 start-up failure order (no
//	                listener, no API request after an earlier step failed),
//	                GLI-093 listen-socket count and slog-only stderr.
//
// The agent behavior against a server (hello, frames, reconnect) is in
// gli_agent_test.go through the liveclient library seam; the binary is run
// against the scripted server here only where the process boundary matters.
// Behavior against a real API server (leader gate, status writes, controller
// failure exit) is in tests/liveingest/envtest.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const gliBSocketFlagValue = "/nonexistent/pod-resources.sock"

// ---------------------------------------------------------------- path-agent: usage (exit 2)

// GLI-080: every value rule and grammar violation is exit 2 within 1 s, with the
// one-line `path-agent: error: usage: ...` on stderr, nothing on stdout, and no
// flag value echoed; the usage error wins over --pod-resources-socket (2 > 5).
func TestGLI080_AgentUsageErrors(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	a := gliBNewAgent(t)
	const marker = "USAGE-MARKER-5e2f"
	type tc struct {
		name string
		args []string
		leak []string
	}
	var cases []tc
	add := func(name string, args []string, leak ...string) { cases = append(cases, tc{name, args, leak}) }
	flags := func(over map[string]string, extra ...string) []string {
		return a.Flags("localhost:8443", over, extra...)
	}

	for _, fl := range gliBRequiredFlags {
		add("missing "+fl, flags(map[string]string{fl: gliBDrop}))
		add("missing "+fl+" next to pod-resources-socket (2 beats 5)", flags(map[string]string{fl: gliBDrop}, "--pod-resources-socket", gliBSocketFlagValue))
	}
	badController := []string{
		"", "localhost", "localhost:", ":8443", "localhost:0", "localhost:65536", "localhost:-1", "localhost:abc", "localhost:80:80",
		"LOCALHOST-" + marker + ":8443", "Local_Host:8443", "a..b:8443", ".a:8443", "a.:8443", "-a:8443", "a-:8443",
		"127.0.0.1:8443", "1.2.3.4:5", "[::1]:8443", "::1", "a.123:8443", "123:8443", "a b:8443", "http://localhost:8443", gliBHost253() + "a:443",
	}
	for _, v := range badController {
		add("controller "+gliXHead(v, 30), flags(map[string]string{"controller": v}), marker)
		add("controller "+gliXHead(v, 30)+" with pod-resources-socket", flags(map[string]string{"controller": v}, "--pod-resources-socket", gliBSocketFlagValue))
	}
	for _, v := range []string{"", strings.Repeat("a", 129), "bad id " + marker, "a/b/" + marker, "caf\u00e9" + marker} {
		add("cluster-id "+gliXHead(v, 20), flags(map[string]string{"cluster-id": v}), marker)
	}
	for _, v := range []string{"", strings.Repeat("a", 254), "has space " + marker, "tab\t" + marker, "caf\u00e9" + marker} {
		add("node-name "+gliXHead(v, 20), flags(map[string]string{"node-name": v}), marker)
	}
	for _, v := range []string{"", strings.Repeat("a", 129), "a b " + marker, "a/b/" + marker, "caf\u00e9" + marker, "a@" + marker} {
		add("node-uid "+gliXHead(v, 20), flags(map[string]string{"node-uid": v}), marker)
	}
	for _, fl := range []string{"sysfs-root", "ca-file", "cert-file", "key-file"} {
		add(fl+" empty", flags(map[string]string{fl: ""}))
	}
	for _, v := range []string{"nvidia-smi-" + marker, "./nvidia-smi-" + marker, "/usr/bin/../bin/nvidia-smi-" + marker, "/usr/bin/" + marker + "/", "/a//b-" + marker, "/a/./b-" + marker} {
		add("nvidia-smi "+gliXHead(v, 30), flags(map[string]string{"nvidia-smi": v}), marker)
	}
	// grammar (GFO-010): repeated flags, positional arguments, unknown flags, a flag without its value.
	add("repeated --cluster-id", flags(nil, "--cluster-id", "other"))
	add("repeated --cluster-id=", flags(nil, "--cluster-id=other"))
	add("repeated --nvidia-smi", flags(nil, "--nvidia-smi", a.SMI))
	add("repeated --pod-resources-socket", flags(nil, "--pod-resources-socket", gliBSocketFlagValue, "--pod-resources-socket", gliBSocketFlagValue))
	add("positional argument", flags(nil, "positional-"+marker), marker)
	add("positional argument before the flags", append([]string{"positional-" + marker}, flags(nil)...), marker)
	add("unknown flag", flags(nil, "--bogus-"+marker, "x"), marker)
	add("unknown flag with value", flags(nil, "--bogus-"+marker+"=1"), marker)
	add("unknown flag with pod-resources-socket", flags(nil, "--pod-resources-socket", gliBSocketFlagValue, "--bogus"))
	add("flag without its value at the end", flags(nil, "--nvidia-smi"))
	add("--boot-id-file without its value at the end", append(flags(map[string]string{"boot-id-file": gliBDrop}), "--boot-id-file"))
	add("fixture-root with --boot-id-file", []string{"--fixture-root", t.TempDir(), "--boot-id-file", a.BootFile})
	add("fixture-root with a live flag", []string{"--fixture-root", t.TempDir(), "--controller", "localhost:8443"})

	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			res := gliBExec(t, bin, gliBCleanEnv(t), 10*time.Second, c.args...)
			if res.Killed || res.Exit != 2 {
				t.Fatalf("GLI-080: exit %d (killed %v), want 2; stderr %q", res.Exit, res.Killed, gliXHead(res.Stderr, 300))
			}
			if res.Elapsed > time.Second {
				t.Errorf("GLI-080: a usage error took %v, want at most 1 s (no file, network or process access)", res.Elapsed)
			}
			gliBStartLine(t, c.name, res, "path-agent", "usage", append(c.leak, a.Dir, gliBPathMarker, a.NodeUID))
		})
	}
}

// GLI-080: values at the edge of every rule pass the usage stage: with
// --pod-resources-socket the process ends with exit 5 (a usage error would be
// exit 2), without opening, stat-ing or dialing anything. A FIFO named by
// --pod-resources-socket or any other path flag is never opened.
func TestGLI080_AgentBoundaryValuesPassUsageAndExit5(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	a := gliBNewAgent(t)
	fifo := filepath.Join(a.Dir, "socket.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(a.Dir, "nvidia-smi-ran")
	a.SMI = gliBScript(t, a.Dir, "nvidia-smi-marker", "touch '"+marker+"'\n")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepted atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	type tc struct {
		name string
		over map[string]string
	}
	cases := []tc{
		{"controller a:1", map[string]string{"controller": "a:1"}},
		{"controller a:65535", map[string]string{"controller": "a:65535"}},
		{"controller 253 byte host", map[string]string{"controller": gliBHost253() + ":443"}},
		{"controller last label not all digits", map[string]string{"controller": "a.1b:8443"}},
		{"controller localhost with a live port", map[string]string{"controller": "localhost:" + port}},
		{"cluster-id 128 bytes", map[string]string{"cluster-id": strings.Repeat("a", 128)}},
		{"cluster-id all allowed characters", map[string]string{"cluster-id": "A.b_c-9"}},
		{"node-name 253 bytes", map[string]string{"node-name": strings.Repeat("a", 253)}},
		{"node-name with odd printable characters", map[string]string{"node-name": "node/with:odd~chars"}},
		{"node-uid 128 bytes", map[string]string{"node-uid": strings.Repeat("a", 128)}},
		{"node-uid a dot", map[string]string{"node-uid": "."}},
		{"node-uid two dots", map[string]string{"node-uid": ".."}},
		{"every path flag a FIFO", map[string]string{"sysfs-root": fifo, "ca-file": fifo, "cert-file": fifo, "key-file": fifo, "boot-id-file": fifo, "nvidia-smi": gliBDrop}},
		{"flags written as --name=value", nil},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			var args []string
			if c.name == "flags written as --name=value" {
				args = []string{
					"--controller=localhost:" + port, "--cluster-id=" + a.Cluster, "--node-name=" + a.NodeName, "--node-uid=" + a.NodeUID,
					"--sysfs-root=" + fifo, "--ca-file=" + fifo, "--cert-file=" + fifo, "--key-file=" + fifo, "--nvidia-smi=" + a.SMI,
					"--pod-resources-socket=" + fifo,
				}
			} else {
				args = a.Flags("localhost:8443", c.over, "--pod-resources-socket", fifo)
			}
			res := gliBExec(t, bin, gliBCleanEnv(t), 10*time.Second, args...)
			if res.Killed || res.Exit != 5 {
				t.Fatalf("GLI-080: exit %d (killed %v), want 5 (usage passed, PodResources unsupported); stderr %q", res.Exit, res.Killed, gliXHead(res.Stderr, 300))
			}
			if res.Elapsed > time.Second {
				t.Errorf("GLI-080/GFO-013: exit 5 took %v, want at most 1 s", res.Elapsed)
			}
			gliBStartLine(t, c.name, res, "path-agent", "live_unsupported", []string{a.Dir, gliBPathMarker, fifo})
		})
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("GLI-080: the --nvidia-smi executable ran although every run ended at the usage or exit 5 stage")
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("GLI-080: the agent connected to --controller %d time(s) although it ended with exit 5", n)
	}
}

// ---------------------------------------------------------------- path-agent: live config (exit 6)

// GLI-080: every start-up validation failure of the credential files, the
// certificate identity, the sysfs root and the boot ID file is exit 6
// (`live_config`) - never a connection, never a run of nvidia-smi, never a wait
// on a FIFO (1 s) - and --pod-resources-socket still takes precedence (5 > 6).
func TestGLI080_AgentLiveConfigErrors(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepted atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ranMarkers := t.TempDir()
	for _, b := range gliBConfigBreakers() {
		t.Run(strings.ReplaceAll(b.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			ran := filepath.Join(ranMarkers, strings.ReplaceAll(b.name, " ", "_"))
			a.SMI = gliBScript(t, a.Dir, "nvidia-smi-marker", "touch '"+ran+"'\n")
			b.set(t, a)
			forbidden := []string{a.Dir, gliBPathMarker, "BEGIN CERTIFICATE", "PRIVATE KEY", "spiffe://", a.NodeUID}
			res := gliBExec(t, bin, gliBCleanEnv(t), 10*time.Second, a.Flags("localhost:"+port, nil)...)
			if res.Killed || res.Exit != 6 {
				t.Fatalf("GLI-080: exit %d (killed %v), want 6; stderr %q", res.Exit, res.Killed, gliXHead(res.Stderr, 300))
			}
			if b.fifo && res.Elapsed > time.Second {
				t.Errorf("GLI-080: a FIFO input took %v to fail, want at most 1 s without waiting for a writer", res.Elapsed)
			}
			gliBStartLine(t, b.name, res, "path-agent", "live_config", forbidden)
			if _, err := os.Stat(ran); err == nil {
				t.Errorf("GLI-080: nvidia-smi ran before the start-up validation ended")
			}
			// the socket flag wins over the configuration failure: exit 5, same invariants.
			res5 := gliBExec(t, bin, gliBCleanEnv(t), 10*time.Second, a.Flags("localhost:"+port, nil, "--pod-resources-socket", gliBSocketFlagValue)...)
			if res5.Killed || res5.Exit != 5 {
				t.Errorf("GLI-080: with --pod-resources-socket the exit is %d (killed %v), want 5 (5 beats 6)", res5.Exit, res5.Killed)
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("GLI-080: the agent connected to --controller %d time(s) although start-up validation failed", n)
	}
}

// ---------------------------------------------------------------- path-agent: running, signals

// GLI-080/086: a running agent exits 0 within 10 s of SIGTERM or SIGINT in every
// state - streaming, waiting for the ServerHello, waiting for an ack, backing off
// - with nothing on stdout and only slog text on stderr; the first frame it
// sends is the complete, valid sequence 0.
func TestGLI080_AgentRunsAndExitsZeroOnSignals(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	type state struct {
		name    string
		handler func(s *gliBStream) error
		ready   func(t *testing.T, srv *gliBServer)
	}
	states := []state{
		{"streaming", gliBAcceptAll(90, nil), func(t *testing.T, srv *gliBServer) { gliBWaitFrames(t, srv, 0, 1, 20*time.Second) }},
		{"waiting for the server hello", func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			<-s.Ctx().Done()
			return nil
		}, func(t *testing.T, srv *gliBServer) {
			gliBEventually(t, 20*time.Second, "the hello arrived", func() bool { i, ok := gliBConnAt(srv, 0); return ok && i.Hello != nil })
		}},
		{"waiting for an ack", func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			if err := s.SendHello(1, "live:test"); err != nil {
				return err
			}
			if _, err := s.RecvFrame(); err != nil {
				return nil
			}
			<-s.Ctx().Done()
			return nil
		}, func(t *testing.T, srv *gliBServer) { gliBWaitFrames(t, srv, 0, 1, 20*time.Second) }},
		{"backing off", func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			return status.Error(codes.Unavailable, "gli test")
		}, func(t *testing.T, srv *gliBServer) {
			gliBEventually(t, 20*time.Second, "two connections", func() bool { return srv.Count() >= 2 })
		}},
	}
	for _, st := range states {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
			t.Run(strings.ReplaceAll(st.name, " ", "_")+"_"+sig.String(), func(t *testing.T) {
				a := gliBNewAgent(t)
				srv := gliBServe(t, a, st.handler)
				p := gliBSpawn(t, bin, gliBCleanEnv(t), a.Flags(srv.Target(), nil)...)
				st.ready(t, srv)
				if p.Exited() {
					code, _ := p.Wait(0)
					t.Fatalf("GLI-083 (6): the agent exited with %d instead of running; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
				}
				if st.name == "streaming" {
					info := srv.Conns()[0]
					if h := info.Hello; h == nil || h.GetVersion() != "v1alpha1" || h.GetClusterId() != a.Cluster || h.GetNodeUid() != a.NodeUID || h.GetBootId() != a.BootID {
						t.Errorf("GLI-083: the binary's ClientHello = %v", h)
					}
					gliBCheckFrame(t, a, 91, info.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
				}
				p.Signal(t, sig)
				code, ok := p.Wait(12 * time.Second) // the 10 s of GLI-086 plus the 2 s load margin
				if !ok {
					t.Fatalf("GLI-080/086: still running 12 s after %s; stderr:\n%s", sig, gliXHead(p.Stderr.String(), 1500))
				}
				if code != 0 {
					t.Errorf("GLI-080: exit %d after %s, want 0; stderr:\n%s", code, sig, gliXHead(p.Stderr.String(), 1500))
				}
				if out := p.Stdout.String(); out != "" {
					t.Errorf("GLI-080: stdout is not empty: %q", gliXHead(out, 200))
				}
				gliBSlogLines(t, "GLI-080", p.Stderr.String(), "")
				for _, bad := range []string{a.Dir, gliBPathMarker} {
					if strings.Contains(p.Stderr.String(), bad) {
						t.Errorf("GLI-101: stderr contains a flag path: %q", gliXHead(p.Stderr.String(), 300))
					}
				}
			})
		}
	}
}

// GLI-086: SIGTERM does not wait for a hanging nvidia-smi child.
func TestGLI086_AgentTerminatesWhileNvidiaSmiHangs(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	a := gliBNewAgent(t)
	pidFile := filepath.Join(a.Dir, "smi-pid")
	a.SMI = gliBScript(t, a.Dir, "nvidia-smi-hang", "echo $$ > '"+pidFile+"'\nexec sleep 60\n")
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &pid); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	srv := gliBServe(t, a, gliBAcceptAll(0, nil))
	p := gliBSpawn(t, bin, gliBCleanEnv(t), a.Flags(srv.Target(), nil)...)
	gliBEventually(t, 20*time.Second, "the hanging nvidia-smi started", func() bool { _, err := os.Stat(pidFile); return err == nil })
	p.Signal(t, syscall.SIGTERM)
	start := time.Now()
	code, ok := p.Wait(12 * time.Second)
	if !ok {
		t.Fatalf("GLI-086: still running 12 s after SIGTERM with a hanging nvidia-smi child; stderr:\n%s", gliXHead(p.Stderr.String(), 1500))
	}
	if code != 0 {
		t.Errorf("GLI-086: exit %d, want 0; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("GLI-086: took %v to exit, want at most 10 s", el)
	}
}

// GLI-101: unique markers in the nvidia-smi stderr and output and in the path of
// every flag file, plus a credential file damaged while the agent runs, never
// reach stderr, which carries slog text only.
func TestGLI101_AgentOutputNeverCarriesSecrets(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	a := gliBNewAgent(t)
	const smiErr, smiCSV = "SMI-STDERR-MARKER-31c7", "SMI-CSV-MARKER-77ab"
	a.SMI = gliBScript(t, a.Dir, "nvidia-smi-noisy", "echo '"+smiErr+"' >&2\necho '"+smiCSV+", 0000:03:00.0'\nexit 3\n")
	orig := a.Leaf
	var damageErr atomic.Value
	srv := gliBServe(t, a, nil)
	srv.SetHandler(func(s *gliBStream) error {
		if _, err := s.RecvHello(); err != nil {
			return err
		}
		if s.N > 1 {
			return gliBAcceptAllAfterHello(s)
		}
		if err := s.SendHello(1, "live:test"); err != nil {
			return err
		}
		f, err := s.RecvFrame()
		if err != nil {
			return nil
		}
		if err := s.Ack(ingestpb.AckCode_ACCEPTED, 1, f.GetSequence()+1); err != nil {
			return err
		}
		if err := os.WriteFile(a.CertFile, []byte("garbage "+smiCSV), 0o600); err != nil { // damage while the agent runs
			damageErr.Store(err.Error())
		}
		return status.Error(codes.Unavailable, "damaged")
	})
	p := gliBSpawn(t, bin, gliBCleanEnv(t), a.Flags(srv.Target(), nil)...)
	gliBWaitClosed(t, srv, 0, 20*time.Second)
	time.Sleep(3 * time.Second) // reconnect attempts with the damaged certificate
	if v := damageErr.Load(); v != nil {
		t.Fatalf("fixture: %v", v)
	}
	if p.Exited() {
		code, _ := p.Wait(0)
		t.Fatalf("GLI-085: the agent exited with %d because a credential file was damaged after start; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
	}
	gliBInstall(t, a.CertFile, a.KeyFile, orig)
	gliBWaitFrames(t, srv, 1, 1, 30*time.Second)
	p.Signal(t, syscall.SIGTERM)
	if code, ok := p.Wait(12 * time.Second); !ok || code != 0 {
		t.Fatalf("GLI-080: exit %d (exited %v) after SIGTERM, want 0", code, ok)
	}
	if out := p.Stdout.String(); out != "" {
		t.Errorf("GLI-080: stdout is not empty: %q", gliXHead(out, 200))
	}
	for _, bad := range []string{smiErr, smiCSV, gliBPathMarker, a.Dir, "PRIVATE KEY", "BEGIN CERTIFICATE", "spiffe://", "data-path-assurance.local"} {
		if strings.Contains(p.Stderr.String(), bad) {
			t.Errorf("GLI-101: stderr contains %q:\n%s", bad, gliXHead(p.Stderr.String(), 800))
		}
	}
	gliBSlogLines(t, "GLI-080", p.Stderr.String(), "")
}

// GLI-005: with GRPC_GO_LOG_SEVERITY_LEVEL=info and a high verbosity the
// agent's stderr still holds slog text only: the composition root silences
// grpc's own logger. The server presents a certificate of another CA so that grpc
// has handshake failures to report.
func TestGLI005_AgentStderrStaysSlogUnderGrpcLogging(t *testing.T) {
	bin := gliBBinary(t, "path-agent")
	a := gliBNewAgent(t)
	foreign := gliBNewPKI(t)
	srv := gliBStartServer(t, gliBServerOpts{ServerCert: foreign.Server(t), ClientCAs: []*gliBPKI{a.PKI}}, gliBAcceptAll(0, nil))
	env := gliBCleanEnv(t, "GRPC_GO_LOG_SEVERITY_LEVEL=info", "GRPC_GO_LOG_VERBOSITY_LEVEL=99")
	p := gliBSpawn(t, bin, env, a.Flags(srv.Target(), nil)...)
	time.Sleep(3500 * time.Millisecond) // connection attempts and one back-off
	if p.Exited() {
		code, _ := p.Wait(0)
		t.Fatalf("the agent exited with %d against an untrusted server instead of retrying; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
	}
	p.Signal(t, syscall.SIGTERM)
	if code, ok := p.Wait(12 * time.Second); !ok || code != 0 {
		t.Fatalf("exit %d (exited %v) after SIGTERM, want 0", code, ok)
	}
	if n := srv.Count(); n != 0 {
		t.Errorf("GLI-083 (1): %d stream(s) reached a server with an untrusted certificate", n)
	}
	gliBSlogLines(t, "GLI-005", p.Stderr.String(), "")
	if out := p.Stdout.String(); out != "" {
		t.Errorf("GLI-080: stdout is not empty: %q", gliXHead(out, 200))
	}
}

// ---------------------------------------------------------------- path-controller (no cluster)

type gliBCtl struct {
	Dir        string
	PKI        *gliBPKI
	Server     gliBLeaf
	CAFile     string
	CertFile   string
	KeyFile    string
	Kubeconfig string
}

// gliBKubeconfigFor writes a kubeconfig whose API server is 127.0.0.1:port
// (https, certificate checks off): with a port that nothing answers it parses
// but never connects.
func gliBKubeconfigFor(t testing.TB, port int) string {
	t.Helper()
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: gli
  cluster:
    server: https://127.0.0.1:%d
    insecure-skip-tls-verify: true
users:
- name: gli
  user:
    token: gli-token
contexts:
- name: gli
  context: {cluster: gli, user: gli}
current-context: gli
`, port)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	gliBWrite(t, path, body, 0o600)
	return path
}

// gliBNewCtl prepares a server certificate (DNS SAN localhost) and CA below a
// directory named with the path marker, and a kubeconfig for apiPort.
func gliBNewCtl(t *testing.T, apiPort int) *gliBCtl {
	t.Helper()
	c := &gliBCtl{Dir: filepath.Join(t.TempDir(), gliBPathMarker), PKI: gliBNewPKI(t)}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c.Server = c.PKI.Server(t)
	c.CAFile = filepath.Join(c.Dir, "ca.pem")
	c.CertFile = filepath.Join(c.Dir, "server.crt")
	c.KeyFile = filepath.Join(c.Dir, "server.key")
	gliBWrite(t, c.CAFile, string(c.PKI.CAPEM), 0o644)
	gliBInstall(t, c.CertFile, c.KeyFile, c.Server)
	c.Kubeconfig = gliBKubeconfigFor(t, apiPort)
	return c
}

var gliBCtlFlagOrder = []string{
	"kubeconfig", "cluster-id", "leader-election-namespace", "resync-interval",
	"ingest-listen", "ingest-service-dns", "ingest-cert-file", "ingest-key-file", "ingest-ca-file",
}

// Args are the controller flags with ingest enabled on listen; an override of
// gliBDrop removes a flag.
func (c *gliBCtl) Args(listen string, over map[string]string, extra ...string) []string {
	vals := map[string]string{
		"kubeconfig": c.Kubeconfig, "cluster-id": "gli-cluster", "leader-election-namespace": "default", "resync-interval": "300ms",
		"ingest-listen": listen, "ingest-service-dns": "localhost",
		"ingest-cert-file": c.CertFile, "ingest-key-file": c.KeyFile, "ingest-ca-file": c.CAFile,
	}
	var args []string
	for _, fl := range gliBCtlFlagOrder {
		v := vals[fl]
		if o, ok := over[fl]; ok {
			v = o
		}
		if v != gliBDrop {
			args = append(args, "--"+fl, v)
		}
	}
	return append(args, extra...)
}

// GLI-090: the ingest flag rules are usage errors (exit 2, one fixed line, before
// any file or network access; the kubeconfig named here does not exist), and the
// values at the edge of each rule are accepted (they get as far as the kubeconfig,
// exit 1 `cannot load kubeconfig`).
func TestGLI090_ControllerIngestFlagRules(t *testing.T) {
	bin := gliBBinary(t, "path-controller")
	c := gliBNewCtl(t, 1)
	absent := filepath.Join(t.TempDir(), "no-such-dir-gli", "kubeconfig")
	const marker = "CTL-MARKER-8a41"
	usage := []string{"invalid flag value", "missing required flag"}
	type tc struct {
		name string
		args []string
		leak string
		msgs []string
	}
	var bad []tc
	addBad := func(name string, args []string, leak string, msgs ...string) {
		if len(msgs) == 0 {
			msgs = usage
		}
		bad = append(bad, tc{name, args, leak, msgs})
	}
	args := func(listen string, over map[string]string, extra ...string) []string {
		if over == nil {
			over = map[string]string{}
		}
		over["kubeconfig"] = absent
		return c.Args(listen, over, extra...)
	}
	const listen = "127.0.0.1:18443"

	// without --ingest-listen no other ingest flag may be present (profile id alone included).
	addBad("profile id alone", args(gliBDrop, map[string]string{"ingest-service-dns": gliBDrop, "ingest-cert-file": gliBDrop, "ingest-key-file": gliBDrop, "ingest-ca-file": gliBDrop}, "--ingest-profile-id", "live:x"), "")
	for _, fl := range []string{"ingest-service-dns", "ingest-cert-file", "ingest-key-file", "ingest-ca-file"} {
		only := map[string]string{"ingest-service-dns": gliBDrop, "ingest-cert-file": gliBDrop, "ingest-key-file": gliBDrop, "ingest-ca-file": gliBDrop}
		delete(only, fl)
		addBad("only "+fl+" without listen", args(gliBDrop, only), "")
		addBad("listen without "+fl, args(listen, map[string]string{fl: gliBDrop}), "")
	}
	addBad("an empty --ingest-listen is no listen: the other ingest flags then violate the rule", args("", nil), "")
	for _, v := range []string{":0", "127.0.0.1:0", "localhost:0", "localhost:65536", "localhost:-1", "localhost", "8443", ":", "localhost:abc", "[::1]", "localhost:80:80"} {
		addBad("listen "+v, args(v, nil), "")
	}
	for _, v := range []string{"UPPER." + marker, "a_b." + marker, "a..b", "-a", "a.", strings.Repeat("a", 254), "", " "} {
		addBad("service dns "+gliXHead(v, 20), args(listen, map[string]string{"ingest-service-dns": v}), marker)
	}
	for _, fl := range []string{"ingest-cert-file", "ingest-key-file", "ingest-ca-file"} {
		addBad(fl+" empty", args(listen, map[string]string{fl: ""}), "")
	}
	for _, v := range []string{"", "offline:" + marker, "offline:", "unmatched-source", strings.Repeat("a", 129), "has space " + marker, "caf\u00e9" + marker, "ctl\x01" + marker} {
		addBad("profile id "+gliXHead(v, 20), args(listen, nil, "--ingest-profile-id", v), marker)
	}
	addBad("the last --ingest-listen wins (invalid last)", args(listen, nil, "--ingest-listen", ":0"), "")
	addBad("unknown ingest flag", args(listen, nil, "--ingest-addr", ":9090"), "ingest-addr", "unknown flag")
	addBad("unknown ingest flag with value", args(listen, nil, "--ingest-addr=:9090"), "ingest-addr", "unknown flag")

	for _, b := range bad {
		t.Run(strings.ReplaceAll(b.name, " ", "_"), func(t *testing.T) {
			res := gliBExec(t, bin, gliBCleanEnv(t), 20*time.Second, b.args...)
			if res.Killed || res.Exit != 2 {
				t.Fatalf("GLI-090: exit %d (killed %v), want 2; stderr %q", res.Exit, res.Killed, gliXHead(res.Stderr, 300))
			}
			forbidden := []string{c.Dir, gliBPathMarker, absent, "no-such-dir-gli"}
			if b.leak != "" {
				forbidden = append(forbidden, b.leak)
			}
			gliBStartLine(t, b.name, res, "path-controller", "usage", forbidden, b.msgs...)
		})
	}

	good := []tc{}
	addGood := func(name string, a []string) { good = append(good, tc{name: name, args: a}) }
	addGood("all flags valid", args(listen, nil))
	addGood("listen with an empty host", args(":8443", nil))
	addGood("listen port 1", args("127.0.0.1:1", nil))
	addGood("listen port 65535", args("127.0.0.1:65535", nil))
	addGood("default profile id", args(listen, nil, "--ingest-profile-id", "live:default"))
	addGood("profile id 128 bytes", args(listen, nil, "--ingest-profile-id", strings.Repeat("a", 128)))
	addGood("profile id with another case of the offline prefix", args(listen, nil, "--ingest-profile-id", "Offline:x"))
	addGood("profile id with a colon and dots", args(listen, nil, "--ingest-profile-id", "live:lab-a.cluster_1"))
	addGood("service dns 253 bytes", args(listen, map[string]string{"ingest-service-dns": gliBHost253()}))
	addGood("service dns a single label", args(listen, map[string]string{"ingest-service-dns": "ingest"}))
	addGood("the last --ingest-listen wins (valid last)", args(listen, nil, "--ingest-listen", ":8443"))
	addGood("invalid listen first, valid last", args("localhost:0", nil, "--ingest-listen", ":8443"))
	addGood("ingest disabled: no ingest flag at all", args(gliBDrop, map[string]string{"ingest-service-dns": gliBDrop, "ingest-cert-file": gliBDrop, "ingest-key-file": gliBDrop, "ingest-ca-file": gliBDrop}))
	for _, g := range good {
		t.Run("accepted_"+strings.ReplaceAll(g.name, " ", "_"), func(t *testing.T) {
			res := gliBExec(t, bin, gliBCleanEnv(t), 20*time.Second, g.args...)
			if res.Killed || res.Exit != 1 {
				t.Fatalf("GLI-090: exit %d (killed %v), want 1 `cannot load kubeconfig` (the flags are valid; start-up stops at the missing kubeconfig); stderr %q", res.Exit, res.Killed, gliXHead(res.Stderr, 300))
			}
			gliBStartLine(t, g.name, res, "path-controller", "config", []string{c.Dir, gliBPathMarker, absent}, "cannot load kubeconfig")
		})
	}
}

// GLI-091: a failure at an early step ends the process with exit 1 and the one
// fixed line `invalid ingest options` and nothing after it runs - no API request
// (the kubeconfig points at a socket this test watches), no listener (the
// listening port stays free).
func TestGLI091_ControllerIngestStartupFailuresStopBeforeAnythingRuns(t *testing.T) {
	bin := gliBBinary(t, "path-controller")
	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = apiLn.Close() }()
	var apiConns atomic.Int64
	go func() {
		for {
			conn, err := apiLn.Accept()
			if err != nil {
				return
			}
			apiConns.Add(1)
			_ = conn.Close()
		}
	}()
	apiPort := apiLn.Addr().(*net.TCPAddr).Port

	type tc struct {
		name string
		prep func(t *testing.T, c *gliBCtl, listen *string) map[string]string
	}
	garbage := "this is not a pem file\n"
	cases := []tc{
		{"certificate garbage", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			gliBWrite(t, c.CertFile, garbage, 0o644)
			return nil
		}},
		{"key garbage", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			gliBWrite(t, c.KeyFile, garbage, 0o600)
			return nil
		}},
		{"certificate missing", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			return map[string]string{"ingest-cert-file": filepath.Join(c.Dir, "no-such.crt")}
		}},
		{"key missing", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			return map[string]string{"ingest-key-file": filepath.Join(c.Dir, "no-such.key")}
		}},
		{"ca missing", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			return map[string]string{"ingest-ca-file": filepath.Join(c.Dir, "no-such-ca.pem")}
		}},
		{"ca garbage", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			gliBWrite(t, c.CAFile, garbage, 0o644)
			return nil
		}},
		{"ca empty", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			gliBWrite(t, c.CAFile, "", 0o644)
			return nil
		}},
		{"certificate is a directory", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			return map[string]string{"ingest-cert-file": c.Dir}
		}},
		{"key does not match the certificate", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			other := c.PKI.Server(t)
			b, err := os.ReadFile(other.Key)
			if err != nil {
				t.Fatal(err)
			}
			gliBWrite(t, c.KeyFile, string(b), 0o600)
			return nil
		}},
		{"service dns is not in the certificate", func(t *testing.T, c *gliBCtl, _ *string) map[string]string {
			return map[string]string{"ingest-service-dns": "other.example"}
		}},
		{"the listen port is in use", func(t *testing.T, c *gliBCtl, listen *string) map[string]string {
			busy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = busy.Close() })
			*listen = busy.Addr().String()
			return nil
		}},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			ctl := gliBNewCtl(t, apiPort)
			listen := fmt.Sprintf("127.0.0.1:%d", gliBFreePort(t))
			over := c.prep(t, ctl, &listen)
			before := apiConns.Load()
			res := gliBExec(t, bin, gliBCleanEnv(t), 30*time.Second, ctl.Args(listen, over)...)
			if res.Killed || res.Exit != 1 {
				t.Fatalf("GLI-091: exit %d (killed %v), want 1; stderr %q", res.Exit, res.Killed, gliXHead(res.Stderr, 400))
			}
			gliBStartLine(t, c.name, res, "path-controller", "config", []string{ctl.Dir, gliBPathMarker, "PRIVATE KEY", "BEGIN CERTIFICATE", "gli-token"}, "invalid ingest options")
			time.Sleep(150 * time.Millisecond)
			if n := apiConns.Load() - before; n != 0 {
				t.Errorf("GLI-091: %d connection(s) reached the API server although an earlier step failed (no API request may follow a failed step)", n)
			}
			if !strings.Contains(c.name, "in use") {
				if l, err := net.Listen("tcp", listen); err != nil {
					t.Errorf("GLI-091: the ingest port %s is not free after the failed start (a listener was opened or left behind): %v", listen, err)
				} else {
					_ = l.Close()
				}
			}
		})
	}
}

// GLI-093/091/005: with ingest enabled the process has exactly one LISTEN socket
// (the ingest listener; no metrics, health or pprof), a node-role mTLS client
// that reaches it while no Lease is readable gets Unavailable (not the leader:
// fail-closed), SIGTERM ends it with exit 0 within 10 s, and stderr holds slog
// text only even with grpc logging switched on and a junk TCP connection to the
// listener. Without ingest there is no LISTEN socket at all.
func TestGLI093_ControllerIngestListenSocketAndSignal(t *testing.T) {
	bin := gliBBinary(t, "path-controller")
	apiPort := gliBFreePort(t) // nothing listens: the API server is unreachable
	env := gliBCleanEnv(t, "GRPC_GO_LOG_SEVERITY_LEVEL=info", "GRPC_GO_LOG_VERBOSITY_LEVEL=99")

	t.Run("ingest_enabled", func(t *testing.T) {
		ctl := gliBNewCtl(t, apiPort)
		listen := fmt.Sprintf("127.0.0.1:%d", gliBFreePort(t))
		p := gliBSpawn(t, bin, env, ctl.Args(listen, nil)...)
		gliBEventually(t, 20*time.Second, "the ingest listener accepts connections", func() bool {
			if p.Exited() {
				code, _ := p.Wait(0)
				t.Fatalf("the controller exited with %d instead of running; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
			}
			c, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
			if err != nil {
				return false
			}
			_ = c.Close()
			return true
		})
		if n, how, ok := gliBListenSockets(p.Pid()); !ok {
			t.Logf("GLI-093: cannot enumerate listening sockets on this platform (not run)")
		} else if n != 1 {
			t.Errorf("GLI-093: the process has %d LISTEN socket(s) (%s), want exactly 1 (the ingest listener; metrics, health and pprof are off)", n, how)
		}
		// a junk connection: grpc's own handshake-failure log must not appear on stderr.
		if c, err := net.DialTimeout("tcp", listen, time.Second); err == nil {
			_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n\x00\x01\x02 junk"))
			_ = c.Close()
		}
		// a node-role client: the identity is valid, the process is not the leader.
		node := ctl.PKI.Node(t, "gli-cluster", gliBNodeUID)
		cert, err := tls.LoadX509KeyPair(node.Cert, node.Key)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ctl.PKI.CAPEM)
		conn, err := grpc.NewClient(listen, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: "localhost", MinVersion: tls.VersionTLS13,
		})))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		sctx, scancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer scancel()
		stream, err := ingestpb.NewIngestClient(conn).Stream(sctx)
		if err != nil {
			t.Fatalf("GLI-093: cannot open a stream to the ingest listener: %v", err)
		}
		if err := stream.Send(&ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: &ingestpb.ClientHello{
			Version: "v1alpha1", ClusterId: "gli-cluster", NodeName: gliBNodeName, NodeUid: gliBNodeUID, BootId: gliBBootID,
		}}}); err != nil {
			// This controller is deliberately not the leader, so the server ends the stream with Unavailable
			// before it reads the hello (GLI-030 (2)); when that end arrives before Send, grpc reports io.EOF
			// from Send and the real status is only available from Recv.
			if !errors.Is(err, io.EOF) {
				t.Fatalf("GLI-093: sending the hello: %v", err)
			}
		}
		if m, err := stream.Recv(); status.Code(err) != codes.Unavailable {
			t.Errorf("GLI-030/092: hello to a controller that cannot read its Lease = %v / %v, want gRPC code Unavailable (fail-closed leader gate)", m, err)
		}
		p.Signal(t, syscall.SIGTERM)
		code, ok := p.Wait(12 * time.Second) // GLI-091: 10 s plus a 2 s load margin
		if !ok {
			t.Fatalf("GLI-091: still running 12 s after SIGTERM; stderr:\n%s", gliXHead(p.Stderr.String(), 1500))
		}
		if code != 0 {
			t.Errorf("GLI-091: exit %d after SIGTERM, want 0; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
		}
		if out := p.Stdout.String(); out != "" {
			t.Errorf("GLI-093/GKA-162: stdout is not empty: %q", gliXHead(out, 200))
		}
		gliBSlogLines(t, "GLI-005/GKA-162", p.Stderr.String(), "")
		for _, bad := range []string{ctl.Dir, gliBPathMarker, "PRIVATE KEY", "BEGIN CERTIFICATE", "spiffe://", "gli-token"} {
			if strings.Contains(p.Stderr.String(), bad) {
				t.Errorf("GLI-101: stderr contains %q:\n%s", bad, gliXHead(p.Stderr.String(), 800))
			}
		}
	})

	t.Run("ingest_disabled_is_the_s3a_binary", func(t *testing.T) {
		kc := gliBKubeconfigFor(t, apiPort)
		p := gliBSpawn(t, bin, env, "--kubeconfig", kc, "--cluster-id", "gli-cluster", "--leader-election-namespace", "default", "--resync-interval", "300ms")
		time.Sleep(2500 * time.Millisecond)
		if p.Exited() {
			code, _ := p.Wait(0)
			t.Fatalf("GLI-093: the controller exited with %d on an unreachable API server instead of retrying; stderr:\n%s", code, gliXHead(p.Stderr.String(), 1500))
		}
		if n, how, ok := gliBListenSockets(p.Pid()); ok && n != 0 {
			t.Errorf("GLI-093/GKA-027: %d LISTEN socket(s) (%s) with ingest disabled, want 0", n, how)
		}
		p.Signal(t, syscall.SIGTERM)
		if code, ok := p.Wait(12 * time.Second); !ok || code != 0 {
			t.Fatalf("GLI-093: exit %d (exited %v) after SIGTERM, want 0; stderr:\n%s", code, ok, gliXHead(p.Stderr.String(), 1500))
		}
		if out := p.Stdout.String(); out != "" {
			t.Errorf("GLI-093: stdout is not empty: %q", gliXHead(out, 200))
		}
		gliBSlogLines(t, "GLI-093/GKA-162", p.Stderr.String(), "")
	})
}
