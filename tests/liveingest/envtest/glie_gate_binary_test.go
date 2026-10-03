package liveingestenv_test

// The wiring of the leader gate's StaleAfter in the path-controller binary
// (GLI-091 (3), GLI-092): StaleAfter is the value of --leader-election-renew-deadline,
// not a default derived from the poll interval and not the lease duration. The
// binary is pointed at the API server through a small HTTP proxy that can fail the
// Lease reads (and only those): the controller's own renewal keeps working, so the
// process keeps running while the ingest gate's reads fail, and the moment a hello
// turns from a ServerHello into Unavailable is the StaleAfter of the binary.

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// glieSetFlag replaces the value of a flag in an argument list (or appends it).
func glieSetFlag(args []string, name, value string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == name {
			out[i+1] = value
			return out
		}
	}
	return append(out, name, value)
}

// GLI-091 (3)/GLI-092: with --leader-election-renew-deadline 12s (lease duration
// 20 s, retry period 1 s, default poll interval 2 s) and every Lease read failing
// from some instant on, a hello still gets its ServerHello 9 s later (so StaleAfter
// is neither the 6 s that 3 x PollInterval would give nor the 1 s retry period) and
// is Unavailable by 16.5 s (so StaleAfter is not the 20 s lease duration). The
// process keeps running throughout. This test waits in real time (about 30 s).
func TestGLI091_ControllerBinaryStaleAfterIsTheRenewDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time scenario of about 30 s (skipped with -short)")
	}
	e := glieEnv(t)
	bin := glieControllerBinary(t)
	w := glieNewBinWorld(t, e)

	rt, err := rest.TransportFor(e.Config)
	if err != nil {
		t.Fatalf("fixture: transport for the envtest API server: %v", err)
	}
	target, err := url.Parse(e.Config.Host)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = rt
	proxy.FlushInterval = -1 // watches stream
	var failLeaseReads atomic.Bool
	leasePath := "/leases/" + w.LeaseID
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			if failLeaseReads.Load() && req.Method == http.MethodGet && strings.Contains(req.URL.Path, leasePath) && req.URL.Query().Get("watch") == "" {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusInternalServerError)
				_, _ = rw.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"InternalError","code":500,"message":"injected"}`))
				return
			}
			proxy.ServeHTTP(rw, req)
		}),
	}
	go func() { _ = api.Serve(ln) }()
	t.Cleanup(func() { _ = api.Close() })
	kubeconfig := glieKubeconfig(t, &rest.Config{Host: "http://" + ln.Addr().String()})

	args := glieSetFlag(w.Args, "--kubeconfig", kubeconfig)
	args = glieSetFlag(args, "--leader-election-lease-duration", "20s")
	args = glieSetFlag(args, "--leader-election-renew-deadline", "12s")
	args = glieSetFlag(args, "--leader-election-retry-period", "1s")
	p := glieSpawn(t, bin, glieCleanEnv(), args...)

	glieEventually(t, 20*time.Second, func() (bool, string) {
		if p.Exited() {
			code, _ := p.Wait(0)
			t.Fatalf("the binary exited with %d instead of running; stderr:\n%s", code, p.stderr.String())
		}
		h, ok := glieHolder(e, w.LeaseID)
		return ok && h == w.CtlID, fmt.Sprintf("Lease holder %q (found %v), want %q", h, ok, w.CtlID)
	})
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, w.R.Scene.Node.Name)
		return ok && len(n.Status.Conditions) > 0, "S3a has not published the node status yet"
	})
	leaf := w.R.PKI.Node(t, glieCluster, string(w.R.Scene.Node.UID))
	hello := func() (*ingestpb.ServerHello, error) {
		h, closeStream, err := glieHello(t, w.Server, leaf, glieClientHello(w.R.Scene.Node))
		closeStream()
		return h, err
	}
	glieEventually(t, 20*time.Second, func() (bool, string) {
		h, err := hello()
		return err == nil && h.GetSession() >= 1, fmt.Sprintf("hello = %v, %v (retried until the ingest gate has polled the Lease)", h, err)
	})

	failLeaseReads.Store(true)
	t0 := time.Now()
	time.Sleep(9 * time.Second)
	if p.Exited() {
		t.Fatalf("fixture: the binary exited while only the Lease reads fail (its own renewal does not read the Lease); stderr:\n%s", p.stderr.String())
	}
	if h, err := hello(); err != nil || h.GetSession() < 1 {
		t.Errorf("GLI-091 (3): a hello %v after the Lease reads began to fail = %v, %v, want the ServerHello: StaleAfter must be the renew deadline 12 s, not a shorter value", time.Since(t0), h, err)
	}
	var unavailableAt time.Duration
	glieEventually(t, 8*time.Second, func() (bool, string) {
		_, err := hello()
		if status.Code(err) == codes.Unavailable {
			unavailableAt = time.Since(t0)
			return true, ""
		}
		return false, fmt.Sprintf("hello = %v (the gate must be false by 16.5 s after the Lease reads began to fail)", err)
	})
	if unavailableAt > 16500*time.Millisecond {
		t.Errorf("GLI-091 (3): the hello became Unavailable only %v after the Lease reads began to fail, want within renew deadline 12 s + poll interval 2 s + headroom (16.5 s): StaleAfter is not the renew deadline", unavailableAt)
	}
	if p.Exited() {
		t.Errorf("fixture: the binary exited before the gate turned false (its own leadership was lost); stderr:\n%s", p.stderr.String())
	}
	failLeaseReads.Store(false)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code, ok := p.Wait(12 * time.Second); !ok || code != 0 {
		t.Errorf("GLI-091: exit %d (exited %v) after SIGTERM, want 0; stderr:\n%s", code, ok, p.stderr.String())
	}
}
