package liveingestenv_test

// The path-controller binary with ingest enabled against a real API server
// (GLI-090..093, GLI-005): it leads, opens exactly one LISTEN socket, serves a
// hello that persists collectorSession through the Kubernetes adapter, ends with
// exit 0 within 10 s of SIGTERM while releasing the Lease, and ends with exit 1
// and the fixed `controller failed` line when it loses leadership. The binary is
// built once per test process (CGO off, GLI-120). The flag grammar and the
// start-up failure order without a cluster are in tests/liveingest/gli_binary_test.go.

import (
	"context"
	"errors"
	"fmt"
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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const glieMarkerDir = "GLIE-PATH-MARKER-62b0"

var (
	glieBinMu   sync.Mutex
	glieBinPath string
	glieBinErr  error
)

// glieControllerBinary builds ./cmd/path-controller once (CGO_ENABLED=0) into the
// repository's self-ignoring build directory.
func glieControllerBinary(t *testing.T) string {
	t.Helper()
	glieBinMu.Lock()
	defer glieBinMu.Unlock()
	if glieBinErr != nil {
		t.Fatalf("go build ./cmd/path-controller failed earlier: %v", glieBinErr)
	}
	if glieBinPath != "" {
		return glieBinPath
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	outDir := filepath.Join(root, "build", "gli-test-bin")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "build", ".gitignore")); err != nil {
		_ = os.WriteFile(filepath.Join(root, "build", ".gitignore"), []byte("*\n"), 0o644)
	}
	out := filepath.Join(outDir, "path-controller-envtest")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/path-controller")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		glieBinErr = fmt.Errorf("%w: %s", err, b)
		t.Fatalf("go build ./cmd/path-controller failed: %v\n%s", err, b)
	}
	glieBinPath = out
	return out
}

// glieKubeconfig writes a kubeconfig for the envtest API server.
func glieKubeconfig(t *testing.T, cfg *rest.Config) string {
	t.Helper()
	c := clientcmdapi.NewConfig()
	cluster := clientcmdapi.NewCluster()
	cluster.Server = cfg.Host
	cluster.CertificateAuthorityData = cfg.CAData
	cluster.InsecureSkipTLSVerify = cfg.Insecure
	user := clientcmdapi.NewAuthInfo()
	user.ClientCertificateData = cfg.CertData
	user.ClientKeyData = cfg.KeyData
	user.Token = cfg.BearerToken
	c.Clusters["glie"] = cluster
	c.AuthInfos["glie"] = user
	ctx := clientcmdapi.NewContext()
	ctx.Cluster, ctx.AuthInfo = "glie", "glie"
	c.Contexts["glie"] = ctx
	c.CurrentContext = "glie"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*c, path); err != nil {
		t.Fatalf("cannot write the kubeconfig: %v", err)
	}
	return path
}

type glieProc struct {
	cmd    *exec.Cmd
	stdout glieSyncBuf
	stderr glieSyncBuf
	done   chan struct{}
	err    error
}

func glieSpawn(t *testing.T, bin string, env []string, args ...string) *glieProc {
	t.Helper()
	p := &glieProc{done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = env
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("cannot start %s: %v", bin, err)
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

func (p *glieProc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Wait returns the exit code and whether the process exited within timeout.
func (p *glieProc) Wait(timeout time.Duration) (int, bool) {
	select {
	case <-p.done:
		if p.err == nil {
			return 0, true
		}
		var ee *exec.ExitError
		if errors.As(p.err, &ee) {
			return ee.ExitCode(), true
		}
		return -2, true
	case <-time.After(timeout):
		return 0, false
	}
}

func glieCleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "KUBECONFIG="), strings.HasPrefix(kv, "KUBERNETES_SERVICE_HOST="), strings.HasPrefix(kv, "KUBERNETES_SERVICE_PORT="), strings.HasPrefix(kv, "GRPC_GO_LOG"):
		default:
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// glieListenSockets counts the LISTEN TCP sockets of a process with lsof (or
// /proc); ok is false when the platform offers neither.
func glieListenSockets(pid int) (n int, how string, ok bool) {
	if path, err := exec.LookPath("lsof"); err == nil {
		out, err := exec.Command(path, "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN").Output()
		if err != nil && len(out) > 0 {
			return 0, "lsof", false
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return 0, "lsof", true
		}
		return len(lines) - 1, "lsof: " + strings.Join(lines, " | "), true
	}
	inodes := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			return 0, "", false
		}
		for i, line := range strings.Split(string(b), "\n") {
			fs := strings.Fields(line)
			if i == 0 || len(fs) < 10 || fs[3] != "0A" {
				continue
			}
			inodes[fs[9]] = true
		}
	}
	links, err := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", pid))
	if err != nil {
		return 0, "", false
	}
	count := 0
	for _, l := range links {
		target, err := os.Readlink(l)
		if err == nil && strings.HasPrefix(target, "socket:[") && inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
			count++
		}
	}
	return count, "/proc", true
}

type glieBinWorld struct {
	R          *glieRig
	Listen     string
	CtlID      string
	LeaseID    string
	Args       []string
	Server     *glieServerRig // only Addr and PKI: it dials the binary's listener
	Kubeconfig string
	Dir        string
}

// glieNewBinWorld prepares the scene (Node, fleet, device), the PKI and files below
// a directory named with the path marker, and the full flag set of a binary with
// ingest enabled on a free loopback port.
func glieNewBinWorld(t *testing.T, e *glieEnvT) *glieBinWorld {
	t.Helper()
	r := glieNewRig(t, e)
	w := &glieBinWorld{R: r, CtlID: r.CtlID, LeaseID: r.Lease, Dir: filepath.Join(t.TempDir(), glieMarkerDir)}
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := r.PKI.Server(t)
	ca, cert, key := filepath.Join(w.Dir, "ca.pem"), filepath.Join(w.Dir, "server.crt"), filepath.Join(w.Dir, "server.key")
	glieWriteFile(t, ca, string(r.PKI.CAPEM), 0o644)
	glieInstall(t, cert, key, server)
	w.Kubeconfig = glieKubeconfig(t, e.Config)
	w.Listen = fmt.Sprintf("127.0.0.1:%d", glieFreePort(t))
	w.Server = &glieServerRig{Addr: w.Listen, PKI: r.PKI}
	w.Args = []string{
		"--kubeconfig", w.Kubeconfig, "--cluster-id", glieCluster, "--controller-id", w.CtlID,
		"--leader-election-namespace", glieNS, "--leader-election-id", w.LeaseID,
		"--leader-election-lease-duration", "4s", "--leader-election-renew-deadline", "3s", "--leader-election-retry-period", "500ms",
		"--resync-interval", "300ms", "--log-level", "debug",
		"--ingest-listen", w.Listen, "--ingest-service-dns", "localhost",
		"--ingest-cert-file", cert, "--ingest-key-file", key, "--ingest-ca-file", ca,
	}
	return w
}

// glieHolder returns the holderIdentity of a Lease ("" when nil) and whether the Lease exists.
func glieHolder(e *glieEnvT, name string) (string, bool) {
	l, err := e.Clientset.CoordinationV1().Leases(glieNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	if l.Spec.HolderIdentity == nil {
		return "", true
	}
	return *l.Spec.HolderIdentity, true
}

// GLI-091/093/005: with ingest enabled the binary becomes the leader, opens exactly
// one LISTEN socket, answers a node-role hello with a ServerHello of the default
// profile and persists the session in the NodePathState, ends with exit 0 within
// 10 s of SIGTERM and releases its Lease; stdout is empty and stderr is slog text
// only (also with grpc logging switched on and a junk TCP connection to the port).
func TestGLI091_ControllerBinaryWithIngestLeadsServesAndStopsOnSIGTERM(t *testing.T) {
	e := glieEnv(t)
	bin := glieControllerBinary(t)
	w := glieNewBinWorld(t, e)
	p := glieSpawn(t, bin, glieCleanEnv("GRPC_GO_LOG_SEVERITY_LEVEL=info", "GRPC_GO_LOG_VERBOSITY_LEVEL=99"), w.Args...)
	glieEventually(t, 20*time.Second, func() (bool, string) {
		if p.Exited() {
			code, _ := p.Wait(0)
			t.Fatalf("the binary exited with %d instead of running; stderr:\n%s", code, p.stderr.String())
		}
		h, ok := glieHolder(e, w.LeaseID)
		return ok && h == w.CtlID, fmt.Sprintf("Lease %s holder %q (found %v), want %q", w.LeaseID, h, ok, w.CtlID)
	})
	glieEventually(t, 20*time.Second, func() (bool, string) {
		c, err := net.DialTimeout("tcp", w.Listen, 200*time.Millisecond)
		if err != nil {
			return false, err.Error()
		}
		_ = c.Close()
		return true, ""
	})
	if n, how, ok := glieListenSockets(p.cmd.Process.Pid); !ok {
		t.Logf("GLI-093: cannot enumerate listening sockets on this platform (not run)")
	} else if n != 1 {
		t.Errorf("GLI-093: the process has %d LISTEN socket(s) (%s), want exactly 1 (the ingest listener)", n, how)
	}
	if c, err := net.DialTimeout("tcp", w.Listen, time.Second); err == nil { // junk: grpc's own handshake-failure log must stay silent
		_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n\x00\x01\x02 junk"))
		_ = c.Close()
	}
	// S3a creates the NodePathState; then the hello persists the session.
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, w.R.Scene.Node.Name)
		return ok && len(n.Status.Conditions) > 0, "S3a has not published the node status yet"
	})
	leaf := w.R.PKI.Node(t, glieCluster, string(w.R.Scene.Node.UID))
	// right after the Lease holder became the controller the ingest gate may still read the old holder
	// (GLI-092: poll interval 2 s plus a margin), so a hello is retried until the ServerHello comes.
	var sh *ingestpb.ServerHello
	var closeStream func()
	glieEventually(t, 20*time.Second, func() (bool, string) {
		h, c, err := glieHello(t, w.Server, leaf, glieClientHello(w.R.Scene.Node))
		if err != nil || h.GetSession() < 1 {
			c()
			return false, fmt.Sprintf("hello = %v, %v (retried while the leader gate polls; Unavailable is expected until then)", h, err)
		}
		sh, closeStream = h, c
		return true, ""
	})
	defer closeStream()
	if sh.GetCollectorProfileId() != "live:default" {
		t.Errorf("GLI-090/030 (10): collector_profile_id = %q, want the default --ingest-profile-id live:default", sh.GetCollectorProfileId())
	}
	glieEventually(t, 10*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, w.R.Scene.Node.Name)
		return ok && n.Status.CollectorSession != nil && *n.Status.CollectorSession == sh.GetSession(), fmt.Sprintf("status %+v", n)
	})
	if p.Exited() {
		t.Fatalf("the binary exited while serving; stderr:\n%s", p.stderr.String())
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	code, ok := p.Wait(12 * time.Second) // GLI-091: 10 s plus a 2 s load margin
	if !ok {
		t.Fatalf("GLI-091: still running 12 s after SIGTERM; stderr:\n%s", p.stderr.String())
	}
	if code != 0 {
		t.Errorf("GLI-091: exit %d after SIGTERM, want 0; stderr:\n%s", code, p.stderr.String())
	}
	if h, found := glieHolder(e, w.LeaseID); !found || h != "" {
		t.Errorf("GKA-142/GLI-091: after a clean exit the Lease holder is %q (found %v), want it released", h, found)
	}
	if out := p.stdout.String(); out != "" {
		t.Errorf("GLI-093/GKA-162: stdout is not empty: %q", out)
	}
	for i, l := range strings.Split(strings.TrimRight(p.stderr.String(), "\n"), "\n") {
		if strings.TrimSpace(l) != "" && !strings.Contains(l, "level=") {
			t.Errorf("GLI-005/GKA-162: stderr line %d is not a slog text record: %q", i+1, l)
		}
	}
	for _, bad := range []string{w.Dir, glieMarkerDir, "PRIVATE KEY", "BEGIN CERTIFICATE", "spiffe://"} {
		if strings.Contains(p.stderr.String(), bad) {
			t.Errorf("GLI-101: stderr contains %q", bad)
		}
	}
	if c, err := net.DialTimeout("tcp", w.Listen, 500*time.Millisecond); err == nil {
		_ = c.Close()
		t.Errorf("GLI-091: the ingest port still accepts connections after the process exited")
	}
}

// GLI-091 (6): losing leadership ends the process with exit 1 and, because the
// controller ended first, the fixed last line `controller failed`; everything
// before it is slog text, and the ingest listener is gone with the process.
func TestGLI091_ControllerBinaryLosingLeadershipExitsWithControllerFailed(t *testing.T) {
	e := glieEnv(t)
	bin := glieControllerBinary(t)
	w := glieNewBinWorld(t, e)
	p := glieSpawn(t, bin, glieCleanEnv(), w.Args...)
	glieEventually(t, 20*time.Second, func() (bool, string) {
		if p.Exited() {
			code, _ := p.Wait(0)
			t.Fatalf("the binary exited with %d instead of running; stderr:\n%s", code, p.stderr.String())
		}
		h, ok := glieHolder(e, w.LeaseID)
		return ok && h == w.CtlID, fmt.Sprintf("Lease holder %q (found %v), want %q", h, ok, w.CtlID)
	})
	thief := "thief-" + w.CtlID
	glieSetLeaseHolder(t, e, w.LeaseID, &thief)
	code, ok := p.Wait(9 * time.Second) // RenewDeadline 3s + RetryPeriod 500ms + 2s (GKA-022(b)) plus a 2 s load margin
	if !ok {
		t.Fatalf("GKA-022/161: the binary kept running 9 s after another holder took the Lease; stderr:\n%s", p.stderr.String())
	}
	if code != 1 {
		t.Errorf("GKA-161: exit code after losing leadership = %d, want 1; stderr:\n%s", code, p.stderr.String())
	}
	lines := strings.Split(strings.TrimRight(p.stderr.String(), "\n"), "\n")
	const final = "path-controller: error: internal: controller failed"
	if lines[len(lines)-1] != final {
		t.Errorf("GLI-091/GKA-162: the last stderr line is %q, want %q (the controller ended first)", lines[len(lines)-1], final)
	}
	for i, l := range lines[:len(lines)-1] {
		if strings.TrimSpace(l) != "" && !strings.Contains(l, "level=") {
			t.Errorf("GKA-162: stderr line %d is not a slog text record: %q", i+1, l)
		}
	}
	if p.stdout.String() != "" {
		t.Errorf("GKA-162: stdout is not empty: %q", p.stdout.String())
	}
	if c, err := net.DialTimeout("tcp", w.Listen, 500*time.Millisecond); err == nil {
		_ = c.Close()
		t.Errorf("GLI-091: the ingest port still accepts connections after the process exited")
	}
}
