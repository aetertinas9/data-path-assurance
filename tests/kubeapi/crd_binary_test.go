package kubeapi_test

// The path-controller binary against a real API server (GKA-160 defaults,
// GKA-161 exit codes, GKA-162 stderr, GKA-163 secrets, GKA-164 wiring). The
// binary is built from the repository and run as a child process; everything
// it does is observed through the API server, its exit code and its output.
// Flag grammar and other no-cluster behavior is in tests/gka_binary_test.go.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const gkaCFinalLine = "path-controller: error: internal: controller failed"

// gkaCBuildController builds ./cmd/path-controller (CGO off) into t.TempDir().
func gkaCBuildController(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "path-controller")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/path-controller")
	cmd.Dir = gkaCRepoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GKA-164: go build ./cmd/path-controller failed: %v\n%s", err, out)
	}
	return bin
}

// gkaCBinEnv is the process environment without anything that could make the
// binary find a cluster on its own.
func gkaCBinEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "KUBECONFIG="), strings.HasPrefix(kv, "KUBERNETES_SERVICE_HOST="), strings.HasPrefix(kv, "KUBERNETES_SERVICE_PORT="):
		default:
			env = append(env, kv)
		}
	}
	return env
}

type gkaCProc struct {
	cmd     *exec.Cmd
	stdout  gkaCBuf
	stderr  gkaCBuf
	done    chan struct{}
	waitErr error
}

func gkaCSpawn(t *testing.T, bin string, args ...string) *gkaCProc {
	t.Helper()
	p := &gkaCProc{done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = gkaCBinEnv()
	p.cmd.Stdout = &p.stdout
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("cannot start %s: %v", bin, err)
	}
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *gkaCProc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Wait returns the exit code and whether the process exited within timeout.
func (p *gkaCProc) Wait(timeout time.Duration) (int, bool) {
	select {
	case <-p.done:
		if p.waitErr == nil {
			return 0, true
		}
		var ee *exec.ExitError
		if errors.As(p.waitErr, &ee) {
			return ee.ExitCode(), true
		}
		return -2, true
	case <-time.After(timeout):
		return 0, false
	}
}

// gkaCKubeconfig writes a kubeconfig for the envtest API server; token, when
// set, is added next to the client certificate.
func gkaCKubeconfig(t *testing.T, cfg *rest.Config, token string) string {
	t.Helper()
	if token != "" && cfg.BearerToken != "" {
		t.Skip("envtest authenticates with a bearer token here, so a marker token cannot be added without breaking the connection")
	}
	c := clientcmdapi.NewConfig()
	cluster := clientcmdapi.NewCluster()
	cluster.Server = cfg.Host
	cluster.CertificateAuthorityData = cfg.CAData
	cluster.InsecureSkipTLSVerify = cfg.Insecure
	user := clientcmdapi.NewAuthInfo()
	user.ClientCertificateData = cfg.CertData
	user.ClientKeyData = cfg.KeyData
	user.Token = cfg.BearerToken
	if token != "" {
		user.Token = token
	}
	c.Clusters["gka"] = cluster
	c.AuthInfos["gka"] = user
	ctx := clientcmdapi.NewContext()
	ctx.Cluster, ctx.AuthInfo = "gka", "gka"
	c.Contexts["gka"] = ctx
	c.CurrentContext = "gka"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*c, path); err != nil {
		t.Fatalf("cannot write the kubeconfig: %v", err)
	}
	return path
}

// gkaCArgs is the full flag set of a test run (GKA-173 timings); extra flags
// come last, so a repeated flag overrides (GKA-160: the last value wins).
func gkaCArgs(kubeconfig, controllerID, leaseID string, extra ...string) []string {
	args := []string{
		"--kubeconfig", kubeconfig, "--cluster-id", "gka-cluster", "--leader-election-namespace", "default",
		"--leader-election-id", leaseID, "--leader-election-lease-duration", "4s", "--leader-election-renew-deadline", "3s",
		"--leader-election-retry-period", "500ms", "--resync-interval", "300ms", "--log-level", "debug",
	}
	if controllerID != "" {
		args = append(args, "--controller-id", controllerID)
	}
	return append(args, extra...)
}

func gkaCWaitLeader(t *testing.T, e *gkaEnvT, p *gkaCProc, lease, holder string) {
	t.Helper()
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		if p.Exited() {
			return false, fmt.Sprintf("the binary exited early; stderr:\n%s", p.stderr.String())
		}
		ctx, cancel := gkaCCtx()
		defer cancel()
		h, _, found := gkaCLeaseHolder(ctx, e, "default", lease)
		return found && h == holder, fmt.Sprintf("Lease default/%s holder %q (found %v), want %q", lease, h, found, holder)
	})
}

func gkaCSlogLines(t *testing.T, clause, stderr, allowedLast string) {
	t.Helper()
	lines := gkaCLines(stderr)
	for i, l := range lines {
		if allowedLast != "" && i == len(lines)-1 && l == allowedLast {
			continue
		}
		if !strings.Contains(l, "level=") {
			t.Errorf("%s: stderr line %d is not a slog text record (no level=): %q", clause, i+1, l)
		}
	}
}

func gkaCTerminate(t *testing.T, p *gkaCProc, sig syscall.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("cannot signal the binary: %v", err)
	}
}

// GKA-164: with a valid kubeconfig the binary becomes leader (Lease holder =
// --controller-id), projects status through the no-observation assessor, and
// exits 0 within 10 seconds of SIGTERM, releasing the Lease. GKA-162: nothing
// on stdout and every stderr line is slog text.
func TestGKA164_BinaryLeadsProjectsAndStopsOnSIGTERM(t *testing.T) {
	e := gkaCStart(t)
	w := gkaCMakeWorld(t, e)
	bin := gkaCBuildController(t)
	kc := gkaCKubeconfig(t, e.Config, "")
	lease, ctl := gkaName(t, "lease"), gkaName(t, "ctl")
	p := gkaCSpawn(t, bin, gkaCArgs(kc, ctl, lease)...)
	gkaCWaitLeader(t, e, p, lease, ctl)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		msg, written := gkaCDeviceStatusMessage(t, e, w.DeviceName)
		return written && strings.Contains(msg, "no_observation"), fmt.Sprintf("device DeviceQualified message %q (written %v); the product wires NewLiveAssessor(NewNoObservationSource())", msg, written)
	})
	gkaEventually(t, 10*time.Second, func() (bool, string) {
		ctx, cancel := gkaCCtx()
		defer cancel()
		nps := &unstructured.Unstructured{}
		nps.SetAPIVersion(gkaCAPIVersion)
		nps.SetKind("NodePathState")
		err := e.Client.Get(ctx, client.ObjectKey{Name: w.NodeName}, nps)
		return err == nil, fmt.Sprintf("NodePathState %s: %v", w.NodeName, err)
	})
	gkaCTerminate(t, p, syscall.SIGTERM)
	code, ok := p.Wait(12 * time.Second) // 10 s bound of GKA-164 plus the 2 s load margin
	if !ok {
		t.Fatalf("GKA-164: the binary was still running 12 s after SIGTERM; stderr:\n%s", p.stderr.String())
	}
	if code != 0 {
		t.Errorf("GKA-161/164: exit code after SIGTERM = %d, want 0; stderr:\n%s", code, p.stderr.String())
	}
	ctx, cancel := gkaCCtx()
	defer cancel()
	if h, _, found := gkaCLeaseHolder(ctx, e, "default", lease); !found || h != "" {
		t.Errorf("GKA-142/164: after a clean exit the Lease holder is %q (found %v), want it released (empty)", h, found)
	}
	if n := p.stdout.Len(); n != 0 {
		t.Errorf("GKA-162: %d bytes on stdout, want 0: %q", n, p.stdout.String())
	}
	gkaCSlogLines(t, "GKA-162", p.stderr.String(), "")
}

// GKA-162: --log-level filters (the flag is honored, last value wins), and
// every stderr line stays slog text at each level.
func TestGKA162_LogLevelFlagFiltersSlogOutput(t *testing.T) {
	e := gkaCStart(t)
	w := gkaCMakeWorld(t, e)
	bin := gkaCBuildController(t)
	kc := gkaCKubeconfig(t, e.Config, "")
	for _, level := range []string{"error", "warn", "info", "debug"} {
		lease, ctl := gkaName(t, "lease-"+level), gkaName(t, "ctl-"+level)
		// the second --log-level overrides the default one of gkaCArgs.
		p := gkaCSpawn(t, bin, gkaCArgs(kc, ctl, lease, "--log-level="+level)...)
		gkaCWaitLeader(t, e, p, lease, ctl)
		gkaEventually(t, 20*time.Second, func() (bool, string) {
			_, written := gkaCDeviceStatusMessage(t, e, w.DeviceName)
			return written, "no device status yet"
		})
		time.Sleep(time.Second)
		gkaCTerminate(t, p, syscall.SIGTERM)
		if code, ok := p.Wait(12 * time.Second); !ok || code != 0 {
			t.Fatalf("GKA-162 level %s: exit %d (exited %v), want 0; stderr:\n%s", level, code, ok, p.stderr.String())
		}
		gkaCSlogLines(t, "GKA-162 level "+level, p.stderr.String(), "")
		if p.stdout.Len() != 0 {
			t.Errorf("GKA-162 level %s: stdout is not empty: %q", level, p.stdout.String())
		}
		text := p.stderr.String()
		below := map[string][]string{
			"error": {"level=WARN", "level=INFO", "level=DEBUG"},
			"warn":  {"level=INFO", "level=DEBUG"},
			"info":  {"level=DEBUG"},
			"debug": nil,
		}[level]
		for _, lv := range below {
			if strings.Contains(text, lv) {
				t.Errorf("GKA-160/162 --log-level=%s still logged %s records", level, lv)
			}
		}
	}
}

// GKA-161: losing leadership ends the process with exit code 1 and the fixed
// last line; earlier lines are slog text.
func TestGKA161_LeadershipLostExitsWithCode1(t *testing.T) {
	e := gkaCStart(t)
	bin := gkaCBuildController(t)
	kc := gkaCKubeconfig(t, e.Config, "")
	lease, ctl := gkaName(t, "lease"), gkaName(t, "ctl")
	p := gkaCSpawn(t, bin, gkaCArgs(kc, ctl, lease)...)
	gkaCWaitLeader(t, e, p, lease, ctl)

	stolen := false
	for attempt := 0; attempt < 20 && !stolen; attempt++ {
		ctx, cancel := gkaCCtx()
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("coordination.k8s.io/v1")
		u.SetKind("Lease")
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: lease}, u); err != nil {
			cancel()
			t.Fatalf("fixture: read the Lease: %v", err)
		}
		_ = unstructured.SetNestedField(u.Object, "thief-"+ctl, "spec", "holderIdentity")
		_ = unstructured.SetNestedField(u.Object, int64(3600), "spec", "leaseDurationSeconds")
		_ = unstructured.SetNestedField(u.Object, time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00"), "spec", "renewTime")
		err := e.Client.Update(ctx, u)
		cancel()
		switch {
		case err == nil:
			stolen = true
		case apierrors.IsConflict(err):
			time.Sleep(50 * time.Millisecond)
		default:
			t.Fatalf("fixture: take over the Lease: %v", err)
		}
	}
	if !stolen {
		t.Fatalf("fixture: could not take over the Lease in 20 attempts")
	}
	// RenewDeadline 3s + RetryPeriod 500ms + 2s (GKA-022(b)) plus a 2s load margin.
	code, ok := p.Wait(9 * time.Second)
	if !ok {
		t.Fatalf("GKA-022/161: the binary kept running 9 s after another holder took the Lease; stderr:\n%s", p.stderr.String())
	}
	if code != 1 {
		t.Errorf("GKA-161: exit code after losing leadership = %d, want 1; stderr:\n%s", code, p.stderr.String())
	}
	lines := gkaCLines(p.stderr.String())
	if len(lines) == 0 || lines[len(lines)-1] != gkaCFinalLine {
		t.Errorf("GKA-162: last stderr line must be %q, stderr:\n%s", gkaCFinalLine, p.stderr.String())
	}
	gkaCSlogLines(t, "GKA-162", p.stderr.String(), gkaCFinalLine)
	if p.stdout.Len() != 0 {
		t.Errorf("GKA-162: stdout is not empty: %q", p.stdout.String())
	}
}

var gkaCHolderRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}-[0-9a-f]{8}$`)

// GKA-160: without --controller-id the holder is <sanitized hostname>-<8 hex>,
// new for every process; without --leader-election-id the Lease is
// default/path-controller with the default 15 s duration; a fractional lease
// duration is recorded truncated to whole seconds.
func TestGKA160_DefaultsReachTheLease(t *testing.T) {
	e := gkaCStart(t)
	bin := gkaCBuildController(t)
	kc := gkaCKubeconfig(t, e.Config, "")
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skipf("no usable hostname (%v)", err)
	}
	prefix := regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(host, "-")
	if len(prefix) > 119 {
		prefix = prefix[:119]
	}

	deleteDefaultLease := func() {
		ctx, cancel := gkaCCtx()
		defer cancel()
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("coordination.k8s.io/v1")
		u.SetKind("Lease")
		u.SetNamespace("default")
		u.SetName("path-controller")
		_ = e.Client.Delete(ctx, u)
	}
	deleteDefaultLease()
	t.Cleanup(deleteDefaultLease)

	// run 1: every default (no controller-id, lease id, durations).
	p1 := gkaCSpawn(t, bin, "--kubeconfig", kc, "--cluster-id", "gka-cluster", "--leader-election-namespace", "default", "--resync-interval", "300ms")
	var holder1 string
	var dur1 int64
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		if p1.Exited() {
			return false, "the binary exited early; stderr:\n" + p1.stderr.String()
		}
		ctx, cancel := gkaCCtx()
		defer cancel()
		h, d, found := gkaCLeaseHolder(ctx, e, "default", "path-controller")
		holder1, dur1 = h, d
		return found && h != "", fmt.Sprintf("Lease default/path-controller found=%v holder=%q", found, h)
	})
	if !gkaCHolderRE.MatchString(holder1) {
		t.Errorf("GKA-160: default controller-id %q does not match ^[A-Za-z0-9._-]{1,128}-<8 hex>$", holder1)
	}
	if !strings.HasPrefix(holder1, prefix+"-") || len(holder1) != len(prefix)+9 {
		t.Errorf("GKA-160: default controller-id %q, want %q-<8 hex> (hostname with characters outside the pattern replaced by '-')", holder1, prefix)
	}
	if dur1 != 15 {
		t.Errorf("GKA-160/020: default lease duration recorded as %d s, want 15", dur1)
	}
	gkaCTerminate(t, p1, syscall.SIGTERM)
	if code, ok := p1.Wait(12 * time.Second); !ok || code != 0 {
		t.Fatalf("GKA-161: first run exit %d (exited %v), want 0; stderr:\n%s", code, ok, p1.stderr.String())
	}

	// run 2: default controller-id again (must differ), 4.5 s lease duration.
	lease := gkaName(t, "lease")
	p2 := gkaCSpawn(t, bin, "--kubeconfig", kc, "--cluster-id", "gka-cluster", "--leader-election-namespace", "default",
		"--leader-election-id", lease, "--leader-election-lease-duration", "4500ms", "--leader-election-renew-deadline", "3s",
		"--leader-election-retry-period", "500ms", "--resync-interval", "300ms")
	var holder2 string
	var dur2 int64
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		if p2.Exited() {
			return false, "the binary exited early; stderr:\n" + p2.stderr.String()
		}
		ctx, cancel := gkaCCtx()
		defer cancel()
		h, d, found := gkaCLeaseHolder(ctx, e, "default", lease)
		holder2, dur2 = h, d
		return found && h != "", fmt.Sprintf("Lease default/%s found=%v holder=%q", lease, found, h)
	})
	if holder2 == holder1 || !gkaCHolderRE.MatchString(holder2) {
		t.Errorf("GKA-160: second process controller-id %q must be new (first %q) and match the pattern", holder2, holder1)
	}
	if dur2 != 4 {
		t.Errorf("GKA-020: --leader-election-lease-duration=4500ms recorded as %d s, want 4 (truncated to whole seconds)", dur2)
	}
	gkaCTerminate(t, p2, syscall.SIGTERM)
	if code, ok := p2.Wait(12 * time.Second); !ok || code != 0 {
		t.Fatalf("GKA-161: second run exit %d (exited %v), want 0; stderr:\n%s", code, ok, p2.stderr.String())
	}
}

// GKA-163: a bearer token placed in the kubeconfig never reaches stdout or
// stderr, even at debug level, while the binary runs against a real API server.
func TestGKA163_TokenNeverPrintedWhileRunning(t *testing.T) {
	e := gkaCStart(t)
	w := gkaCMakeWorld(t, e)
	const marker = "GKA163-TOKEN-MARKER-5b1e7c"
	bin := gkaCBuildController(t)
	kc := gkaCKubeconfig(t, e.Config, marker)
	lease, ctl := gkaName(t, "lease"), gkaName(t, "ctl")
	p := gkaCSpawn(t, bin, gkaCArgs(kc, ctl, lease)...)
	time.Sleep(4 * time.Second) // connection attempts, cache sync, a few passes
	gkaCTerminate(t, p, syscall.SIGTERM)
	if _, ok := p.Wait(12 * time.Second); !ok {
		t.Fatalf("GKA-163: the binary was still running 12 s after SIGTERM")
	}
	for name, out := range map[string]string{"stdout": p.stdout.String(), "stderr": p.stderr.String()} {
		if strings.Contains(out, marker) {
			t.Errorf("GKA-163: the kubeconfig token marker appears on %s", name)
		}
	}
	_ = w
}
