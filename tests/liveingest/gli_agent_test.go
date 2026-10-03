package liveingest_test

// The live client library (internal/agent/liveclient, GLI-112) against a
// scripted ingest server: option validation, start-up configuration errors,
// the stream state machine of GLI-083 (hello, stop-and-wait acks, reconnect
// back-off and its reset, hello and ack timeouts, ResourceExhausted floor,
// cancellation), frame construction (GLI-081/082), certificate rotation
// (GLI-083 (1), GLI-085) and the boot ID file (GLI-084). Scenario group
// GLI-124 (f). Every wait is real time (the time seams of liveclient.Options are
// shortened) with at least 2 s of -race headroom and at most 20 s per wait.

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/agent/liveclient"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliBClock is a Clock the test moves by hand.
type gliBClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *gliBClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *gliBClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *gliBClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

var _ liveclient.Clock = (*gliBClock)(nil)

func gliBServe(t *testing.T, a *gliBAgent, h func(*gliBStream) error) *gliBServer {
	t.Helper()
	return gliBStartServer(t, gliBServerOpts{ServerCert: a.PKI.Server(t), ClientCAs: []*gliBPKI{a.PKI}}, h)
}

// gliBConnAt returns the i-th (0-based) server-side connection once it exists.
func gliBConnAt(srv *gliBServer, i int) (gliBConnInfo, bool) {
	cs := srv.Conns()
	if i < len(cs) {
		return cs[i], true
	}
	return gliBConnInfo{}, false
}

func gliBWaitConn(t *testing.T, srv *gliBServer, i int, timeout time.Duration, what string) gliBConnInfo {
	t.Helper()
	var info gliBConnInfo
	gliBEventually(t, timeout, what, func() bool {
		var ok bool
		info, ok = gliBConnAt(srv, i)
		return ok
	})
	return info
}

func gliBWaitFrames(t *testing.T, srv *gliBServer, i, n int, timeout time.Duration) gliBConnInfo {
	t.Helper()
	var info gliBConnInfo
	gliBEventually(t, timeout, fmt.Sprintf("connection %d has %d frames", i+1, n), func() bool {
		var ok bool
		info, ok = gliBConnAt(srv, i)
		return ok && len(info.Frames) >= n
	})
	return info
}

func gliBWaitClosed(t *testing.T, srv *gliBServer, i int, timeout time.Duration) gliBConnInfo {
	t.Helper()
	var info gliBConnInfo
	gliBEventually(t, timeout, fmt.Sprintf("connection %d is closed", i+1), func() bool {
		var ok bool
		info, ok = gliBConnAt(srv, i)
		return ok && !info.ClosedAt.IsZero()
	})
	return info
}

func gliBStillRunning(t *testing.T, c *gliBClient, what string) {
	t.Helper()
	if !c.Running() {
		t.Fatalf("liveclient.Run returned (%v) %s: network and server errors are never a reason to stop (GLI-083 (6))", c.err, what)
	}
}

// ---------------------------------------------------------------- GLI-112: ValidateFlags

func gliBHost253() string {
	l := strings.Repeat("a", 63)
	return l + "." + l + "." + l + "." + strings.Repeat("a", 61)
}

// GLI-112/080: ValidateFlags checks the value rules of GLI-080 and nothing else:
// it touches no file (a FIFO would block), no network and no process.
func TestGLI112_ValidateFlagsValueRules(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
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

	base := func() liveclient.Options {
		return liveclient.Options{
			Controller: "localhost:" + port, ClusterID: "lab-a", NodeName: "gpu-node-1", NodeUID: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
			SysfsRoot: "/nonexistent/sys", NVIDIASMI: "/nonexistent/nvidia-smi", BootIDFile: "/nonexistent/boot_id",
			TLS: liveclient.TLSFiles{CAFile: "/nonexistent/ca", CertFile: "/nonexistent/cert", KeyFile: "/nonexistent/key"},
		}
	}
	type tc struct {
		name  string
		mut   func(o *liveclient.Options)
		valid bool
	}
	var cases []tc
	add := func(valid bool, name string, mut func(o *liveclient.Options)) {
		cases = append(cases, tc{name, mut, valid})
	}
	ctl := func(v string) func(*liveclient.Options) { return func(o *liveclient.Options) { o.Controller = v } }
	cluster := func(v string) func(*liveclient.Options) { return func(o *liveclient.Options) { o.ClusterID = v } }
	nodeName := func(v string) func(*liveclient.Options) { return func(o *liveclient.Options) { o.NodeName = v } }
	nodeUID := func(v string) func(*liveclient.Options) { return func(o *liveclient.Options) { o.NodeUID = v } }
	smi := func(v string) func(*liveclient.Options) { return func(o *liveclient.Options) { o.NVIDIASMI = v } }

	add(true, "baseline", func(*liveclient.Options) {})
	add(true, "files that do not exist", func(o *liveclient.Options) {})
	add(true, "FIFO paths are not opened", func(o *liveclient.Options) {
		o.TLS = liveclient.TLSFiles{CAFile: fifo, CertFile: fifo, KeyFile: fifo}
		o.BootIDFile = fifo
		o.SysfsRoot = fifo
	})
	for _, v := range []string{"localhost:8443", "a:1", "a:65535", "a.b-c.example.com:8443", "a1.b2:80", "a.1b:8443", gliBHost253() + ":443"} {
		add(true, "controller "+gliXHead(v, 40), ctl(v))
	}
	for _, v := range []string{
		"", "localhost", "localhost:", ":8443", "localhost:0", "localhost:65536", "localhost:-1", "localhost:abc", "localhost:80:80",
		"LOCALHOST:8443", "Local_Host:8443", "a..b:8443", ".a:8443", "a.:8443", "-a:8443", "a-:8443",
		"127.0.0.1:8443", "1.2.3.4:5", "0.0.0.0:1", "[::1]:8443", "::1", "[::1]", "a.123:8443", "123:8443", "a b:8443", " localhost:8443",
		"http://localhost:8443", "localhost:8443/x", gliBHost253() + "a:443",
	} {
		add(false, "controller "+gliXHead(v, 40), ctl(v))
	}
	for _, v := range []string{"A.b_c-9", "c", strings.Repeat("a", 128), "."} {
		add(true, "cluster "+gliXHead(v, 20), cluster(v))
	}
	for _, v := range []string{"", strings.Repeat("a", 129), "a b", "a/b", "a@b", "caf\u00e9", "a:b", "a\tb"} {
		add(false, fmt.Sprintf("cluster %q", gliXHead(v, 20)), cluster(v))
	}
	for _, v := range []string{"n", strings.Repeat("a", 253), "node/with:odd~chars", "gpu-node-1.example.com"} {
		add(true, "node name "+gliXHead(v, 20), nodeName(v))
	}
	for _, v := range []string{"", strings.Repeat("a", 254), "has space", "tab\tx", "caf\u00e9", "new\nline", "del\x7f", "ctl\x01x"} {
		add(false, fmt.Sprintf("node name %q", gliXHead(v, 20)), nodeName(v))
	}
	for _, v := range []string{".", "..", "a", strings.Repeat("a", 128), "A.b_c-9"} {
		add(true, "node uid "+gliXHead(v, 20), nodeUID(v))
	}
	for _, v := range []string{"", strings.Repeat("a", 129), "a b", "a/b", "a@b", "caf\u00e9", "a:b"} {
		add(false, fmt.Sprintf("node uid %q", gliXHead(v, 20)), nodeUID(v))
	}
	add(false, "sysfs root empty", func(o *liveclient.Options) { o.SysfsRoot = "" })
	add(false, "ca file empty", func(o *liveclient.Options) { o.TLS.CAFile = "" })
	add(false, "cert file empty", func(o *liveclient.Options) { o.TLS.CertFile = "" })
	add(false, "key file empty", func(o *liveclient.Options) { o.TLS.KeyFile = "" })
	add(true, "nvidia-smi empty means no inventory", smi(""))
	add(true, "nvidia-smi absolute clean path", smi("/usr/bin/nvidia-smi"))
	add(true, "nvidia-smi path with a space", smi("/with space/nvidia-smi"))
	for _, v := range []string{"nvidia-smi", "./nvidia-smi", "/usr/bin/../bin/nvidia-smi", "/usr/bin/", "/a//b", "/a/./b"} {
		add(false, "nvidia-smi "+v, smi(v))
	}
	add(true, "boot ID file empty means the default path", func(o *liveclient.Options) { o.BootIDFile = "" })

	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			o := base()
			c.mut(&o)
			type result struct{ err error }
			ch := make(chan result, 1)
			go func() { ch <- result{o.ValidateFlags()} }()
			var err error
			select {
			case r := <-ch:
				err = r.err
			case <-time.After(3 * time.Second):
				t.Fatalf("ValidateFlags did not return within 3 s: it must not touch files (GLI-112)")
			}
			switch {
			case c.valid && err != nil:
				t.Errorf("GLI-080/112: ValidateFlags rejected a valid value: %v", err)
			case !c.valid && err == nil:
				t.Errorf("GLI-080/112: ValidateFlags accepted an invalid value")
			case !c.valid && !errors.Is(err, liveclient.ErrInvalidOptions):
				t.Errorf("GLI-112: the error does not match ErrInvalidOptions: %v", err)
			case !c.valid && errors.Is(err, liveclient.ErrLiveConfig):
				t.Errorf("GLI-112: a value-rule violation must not be ErrLiveConfig: %v", err)
			}
		})
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("GLI-112: ValidateFlags connected to the controller address %d time(s)", n)
	}
	if errors.Is(liveclient.ErrInvalidOptions, liveclient.ErrLiveConfig) || errors.Is(liveclient.ErrLiveConfig, liveclient.ErrInvalidOptions) {
		t.Errorf("GLI-112: ErrInvalidOptions and ErrLiveConfig must be different sentinels")
	}
}

// GLI-112: Run validates the value rules first (ErrInvalidOptions) before it
// reads a file, and does so without any I/O.
func TestGLI112_RunChecksValueRulesBeforeAnyIO(t *testing.T) {
	a := gliBNewAgent(t)
	srv := gliBServe(t, a, gliBAcceptAll(0, nil))
	fifo := filepath.Join(a.Dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		mut  func(o *liveclient.Options)
	}{
		{"ip literal controller", func(o *liveclient.Options) { o.Controller = srv.Addr }},
		{"empty cluster", func(o *liveclient.Options) { o.ClusterID = "" }},
		{"bad node uid with a FIFO ca", func(o *liveclient.Options) { o.NodeUID = "a b"; o.TLS.CAFile = fifo }},
		{"relative nvidia-smi", func(o *liveclient.Options) { o.NVIDIASMI = "nvidia-smi" }},
	} {
		o := a.Options(srv.Target())
		c.mut(&o)
		done := make(chan error, 1)
		go func() { done <- liveclient.Run(t.Context(), o) }()
		select {
		case err := <-done:
			if !errors.Is(err, liveclient.ErrInvalidOptions) || errors.Is(err, liveclient.ErrLiveConfig) {
				t.Errorf("GLI-112 %s: Run error = %v, want ErrInvalidOptions", c.name, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("GLI-112 %s: Run did not return within 3 s on an invalid option", c.name)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := srv.Count(); n != 0 {
		t.Errorf("GLI-112: Run connected %d time(s) although its options are invalid", n)
	}
}

// ---------------------------------------------------------------- GLI-112/080: start-up configuration errors

type gliBBreaker struct {
	name string
	fifo bool // the file is a FIFO: the failure must not wait for a writer
	set  func(t *testing.T, a *gliBAgent)
}

func gliBFifoAt(t *testing.T, path string) {
	t.Helper()
	_ = os.Remove(path)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
}

// gliBConfigBreakers are the start-up validation failures of GLI-080 (exit 6,
// ErrLiveConfig): CA, certificate and key files, the certificate identity
// against --cluster-id / --node-uid / role node, the sysfs root and the boot ID
// file.
func gliBConfigBreakers() []gliBBreaker {
	garbageDER := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}))
	node := func(cluster, uid string) func(t *testing.T, a *gliBAgent) {
		return func(t *testing.T, a *gliBAgent) { gliBInstall(t, a.CertFile, a.KeyFile, a.PKI.Node(t, cluster, uid)) }
	}
	write := func(path func(a *gliBAgent) string, content string) func(t *testing.T, a *gliBAgent) {
		return func(t *testing.T, a *gliBAgent) { gliBWrite(t, path(a), content, 0o644) }
	}
	ca := func(a *gliBAgent) string { return a.CAFile }
	cert := func(a *gliBAgent) string { return a.CertFile }
	key := func(a *gliBAgent) string { return a.KeyFile }
	boot := func(a *gliBAgent) string { return a.BootFile }
	return []gliBBreaker{
		{"ca missing", false, func(t *testing.T, a *gliBAgent) { a.CAFile = filepath.Join(a.Dir, "no-such-ca.pem") }},
		{"ca empty", false, write(ca, "")},
		{"ca not pem", false, write(ca, "this is not a pem file\n")},
		{"ca pem with garbage der", false, write(ca, garbageDER)},
		{"ca is a directory", false, func(t *testing.T, a *gliBAgent) { a.CAFile = a.Dir }},
		{"cert missing", false, func(t *testing.T, a *gliBAgent) { a.CertFile = filepath.Join(a.Dir, "no-such.crt") }},
		{"cert garbage", false, write(cert, "not a certificate\n")},
		{"cert pem with garbage der", false, write(cert, garbageDER)},
		{"cert is a directory", false, func(t *testing.T, a *gliBAgent) { a.CertFile = a.Dir }},
		{"key missing", false, func(t *testing.T, a *gliBAgent) { a.KeyFile = filepath.Join(a.Dir, "no-such.key") }},
		{"key garbage", false, write(key, "not a key\n")},
		{"key is a directory", false, func(t *testing.T, a *gliBAgent) { a.KeyFile = a.Dir }},
		{"key does not match the certificate", false, func(t *testing.T, a *gliBAgent) {
			other := a.PKI.Node(t, a.Cluster, a.NodeUID)
			b, err := os.ReadFile(other.Key)
			if err != nil {
				t.Fatal(err)
			}
			gliBWrite(t, a.KeyFile, string(b), 0o600)
		}},
		{"certificate and key files swapped", false, func(t *testing.T, a *gliBAgent) {
			c, err := os.ReadFile(a.CertFile)
			if err != nil {
				t.Fatal(err)
			}
			k, err := os.ReadFile(a.KeyFile)
			if err != nil {
				t.Fatal(err)
			}
			gliBWrite(t, a.CertFile, string(k), 0o644)
			gliBWrite(t, a.KeyFile, string(c), 0o600)
		}},
		{"san names another cluster", false, node("other-cluster", gliBNodeUID)},
		{"san names another node uid", false, node(gliBCluster, "another-node-uid")},
		{"san role fence", false, func(t *testing.T, a *gliBAgent) {
			gliBInstall(t, a.CertFile, a.KeyFile, a.PKI.Role(t, a.Cluster, "fence", a.NodeUID))
		}},
		{"san role viewer", false, func(t *testing.T, a *gliBAgent) {
			gliBInstall(t, a.CertFile, a.KeyFile, a.PKI.Role(t, a.Cluster, "viewer", a.NodeUID))
		}},
		{"san in another trust domain", false, func(t *testing.T, a *gliBAgent) {
			l := a.PKI.Node(t, a.Cluster, a.NodeUID, func(c *x509.Certificate) { c.URIs[0].Host = "other.local" })
			gliBInstall(t, a.CertFile, a.KeyFile, l)
		}},
		{"no uri san", false, func(t *testing.T, a *gliBAgent) {
			l := a.PKI.leaf(t, &x509.Certificate{
				Subject: pkix.Name{CommonName: "gli no san"}, DNSNames: []string{"node.example"},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			})
			gliBInstall(t, a.CertFile, a.KeyFile, l)
		}},
		{"sysfs root missing", false, func(t *testing.T, a *gliBAgent) { a.Sysfs = filepath.Join(a.Dir, "no-such-sysfs") }},
		{"sysfs root is a regular file", false, func(t *testing.T, a *gliBAgent) { a.Sysfs = a.BootFile }},
		{"boot id file missing", false, func(t *testing.T, a *gliBAgent) { a.BootFile = filepath.Join(a.Dir, "no-such-boot-id") }},
		{"boot id empty", false, write(boot, "")},
		{"boot id only whitespace", false, write(boot, "  \t\r\n")},
		{"boot id 129 bytes", false, write(boot, strings.Repeat("a", 129)+"\n")},
		{"boot id with an inner space", false, write(boot, "abc def\n")},
		{"boot id non-ASCII", false, write(boot, "caf\u00e9\n")},
		{"boot id control character", false, write(boot, "a\x01b\n")},
		{"boot id file is a directory", false, func(t *testing.T, a *gliBAgent) { a.BootFile = a.Dir }},
		{"ca file is a FIFO", true, func(t *testing.T, a *gliBAgent) { gliBFifoAt(t, a.CAFile) }},
		{"cert file is a FIFO", true, func(t *testing.T, a *gliBAgent) { gliBFifoAt(t, a.CertFile) }},
		{"key file is a FIFO", true, func(t *testing.T, a *gliBAgent) { gliBFifoAt(t, a.KeyFile) }},
		{"boot id file is a FIFO", true, func(t *testing.T, a *gliBAgent) { gliBFifoAt(t, a.BootFile) }},
	}
}

// GLI-112: Run's start-up I/O validation fails with ErrLiveConfig (never
// ErrInvalidOptions) and returns at once, without waiting on a FIFO and without
// dialing the controller.
func TestGLI112_RunStartupConfigErrorsAreErrLiveConfig(t *testing.T) {
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
	for _, b := range gliBConfigBreakers() {
		t.Run(strings.ReplaceAll(b.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			b.set(t, a)
			o := a.Options("localhost:" + port)
			ctx := t.Context()
			start := time.Now()
			done := make(chan error, 1)
			go func() { done <- liveclient.Run(ctx, o) }()
			select {
			case err := <-done:
				if !errors.Is(err, liveclient.ErrLiveConfig) || errors.Is(err, liveclient.ErrInvalidOptions) {
					t.Errorf("GLI-112/080: Run error = %v, want ErrLiveConfig", err)
				}
				if el := time.Since(start); b.fifo && el > 3*time.Second {
					t.Errorf("GLI-080: a FIFO input took %v to fail, want it to fail without waiting", el)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("GLI-080/112: Run did not return within 5 s (a FIFO must not be opened and waited on)")
			}
			for _, secret := range []string{gliBPathMarker} {
				if strings.Contains(a.Logs.String(), secret) {
					t.Errorf("GLI-085: the log carries a flag path: %q", gliXHead(a.Logs.String(), 300))
				}
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("GLI-080: Run dialed the controller %d time(s) although start-up validation failed", n)
	}
}

// ---------------------------------------------------------------- GLI-083: hello and frames

// GLI-083 (1)(3)/084/081/082: the agent connects with TLS 1.3 / h2 and the
// client leaf it was given, sends ClientHello with the pinned fields and the
// trimmed boot ID, and sends complete sequence 0 immediately after ServerHello
// (not after the first Interval) with a valid canonical digest, IDs and times.
func TestGLI083_HelloAndFirstFrame(t *testing.T) {
	a := gliBNewAgent(t)
	gliBWrite(t, a.BootFile, "  \t"+a.BootID+" \r\n", 0o644) // GLI-084: only ASCII whitespace around the value is trimmed
	srv := gliBServe(t, a, gliBAcceptAll(40, nil))
	o := a.Options(srv.Target())
	o.Interval = 10 * time.Second
	c := gliBRun(t, o)

	info := gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
	if h := info.Hello; h == nil || h.GetVersion() != "v1alpha1" || h.GetClusterId() != a.Cluster || h.GetNodeName() != a.NodeName ||
		h.GetNodeUid() != a.NodeUID || h.GetBootId() != a.BootID {
		t.Errorf("GLI-083 (1): ClientHello = %v, want version v1alpha1, cluster %q, node %q/%q, boot %q", h, a.Cluster, a.NodeName, a.NodeUID, a.BootID)
	}
	if info.TLSVersion != tls.VersionTLS13 || info.ALPN != "h2" {
		t.Errorf("GLI-020: negotiated TLS version 0x%04x ALPN %q, want TLS 1.3 and h2", info.TLSVersion, info.ALPN)
	}
	if info.Leaf == nil || info.Leaf.SerialNumber.Cmp(a.Leaf.Leaf.SerialNumber) != 0 {
		t.Errorf("GLI-083 (1): the server did not see the configured client leaf")
	}
	if el := info.FrameAt[0].Sub(info.HelloAt); el > 4*time.Second {
		t.Errorf("GLI-083 (3): frame 0 came %v after the hello although Interval is 10 s: cycle 0 must start at once", el)
	}
	gliBCheckFrame(t, a, 41, info.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
	gliBStillRunning(t, c, "while sending frames")
	if err := c.Stop(t, 5*time.Second); err != nil {
		t.Errorf("GLI-083: Run returned %v after the context was cancelled, want nil", err)
	}
}

// GLI-083 (4)/081 (i): stop-and-wait. No second frame is sent while the ack of
// the first is outstanding; ACCEPTED and DUPLICATE both continue the session;
// sequence grows by one per sent frame and observed_at strictly increases.
func TestGLI083_StopAndWaitSequenceAndDuplicate(t *testing.T) {
	a := gliBNewAgent(t)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := gliBServe(t, a, func(s *gliBStream) error {
		if _, err := s.RecvHello(); err != nil {
			return err
		}
		if err := s.SendHello(500, "live:test"); err != nil {
			return err
		}
		for i := 0; ; i++ {
			f, err := s.RecvFrame()
			if err != nil {
				return nil
			}
			if i == 0 {
				select {
				case <-release:
				case <-s.Ctx().Done():
					return nil
				}
			}
			code := ingestpb.AckCode_ACCEPTED
			if i == 2 {
				code = ingestpb.AckCode_DUPLICATE
			}
			if err := s.Ack(code, 500, f.GetSequence()+1); err != nil {
				return err
			}
		}
	})
	c := gliBRun(t, a.Options(srv.Target()))
	gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
	time.Sleep(900 * time.Millisecond) // six cycles at Interval 150 ms
	if n := len(srv.Conns()[0].Frames); n != 1 {
		t.Fatalf("GLI-083 (4): %d frames arrived while the first ack was outstanding, want exactly 1 (stop-and-wait)", n)
	}
	once.Do(func() { close(release) })
	info := gliBWaitFrames(t, srv, 0, 5, 15*time.Second)
	var prev time.Time
	for i, f := range info.Frames[:5] {
		gliBCheckFrame(t, a, 500, f, uint64(i), 300*time.Second, ingestpb.Completeness_COMPLETE, true)
		at := f.GetObservedAt().AsTime()
		if i > 0 && !at.After(prev) {
			t.Errorf("GLI-082: frame %d observed_at %v is not after the previous %v", i, at, prev)
		}
		prev = at
	}
	if n := srv.Count(); n != 1 {
		t.Errorf("GLI-083 (4): the agent opened %d streams; ACCEPTED and DUPLICATE both keep the stream", n)
	}
	gliBStillRunning(t, c, "after DUPLICATE")
}

// GLI-083 (4)/GLI-044: every ack code other than ACCEPTED and DUPLICATE (also the
// unspecified and an unknown value) makes the agent close the stream and start
// a new session with complete sequence 0 after the reconnect delay.
func TestGLI083_NonAcceptedAckStartsANewSession(t *testing.T) {
	for _, code := range []ingestpb.AckCode{
		ingestpb.AckCode_OUT_OF_ORDER, ingestpb.AckCode_GAP, ingestpb.AckCode_WRONG_SESSION, ingestpb.AckCode_CONFLICT,
		ingestpb.AckCode_INVALID, ingestpb.AckCode_ACK_CODE_UNSPECIFIED, ingestpb.AckCode(99),
	} {
		t.Run(code.String(), func(t *testing.T) {
			a := gliBNewAgent(t)
			srv := gliBServe(t, a, func(s *gliBStream) error {
				if _, err := s.RecvHello(); err != nil {
					return err
				}
				session := int64(s.N) * 10
				if err := s.SendHello(session, "live:test"); err != nil {
					return err
				}
				for {
					f, err := s.RecvFrame()
					if err != nil {
						return nil
					}
					if s.N == 1 {
						if err := s.Ack(code, session, 0); err != nil {
							return err
						}
						<-s.Ctx().Done() // the stream stays open until the agent ends it
						return nil
					}
					if err := s.Ack(ingestpb.AckCode_ACCEPTED, session, f.GetSequence()+1); err != nil {
						return err
					}
				}
			})
			o := a.Options(srv.Target())
			o.ReconnectInitial, o.ReconnectMax = 500*time.Millisecond, 500*time.Millisecond
			c := gliBRun(t, o)
			first := gliBWaitClosed(t, srv, 0, 15*time.Second)
			second := gliBWaitFrames(t, srv, 1, 1, 15*time.Second)
			if gap := second.StartAt.Sub(first.ClosedAt); gap < 350*time.Millisecond {
				t.Errorf("GLI-083 (5): the new stream started %v after the old one ended, want the reconnect delay (about 500 ms, jitter -20%%)", gap)
			}
			gliBCheckFrame(t, a, 20, second.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
			if len(first.Frames) != 1 {
				t.Errorf("GLI-083 (4): %d frames on the rejected stream, want 1", len(first.Frames))
			}
			gliBStillRunning(t, c, "after "+code.String())
		})
	}
}

// GLI-083 (6): no gRPC status code ends the agent; every one (and a clean OK
// close, before or after ServerHello) leads to a reconnect and a new session.
func TestGLI083_EveryStreamEndReconnects(t *testing.T) {
	type tc struct {
		name       string
		code       codes.Code
		ok         bool // return nil: the server ends the stream cleanly
		afterHello bool
	}
	var cases []tc
	for _, code := range []codes.Code{
		codes.Unauthenticated, codes.PermissionDenied, codes.FailedPrecondition, codes.Unavailable, codes.Internal,
		codes.InvalidArgument, codes.Aborted, codes.DeadlineExceeded, codes.Canceled, codes.ResourceExhausted, codes.Unimplemented,
	} {
		cases = append(cases, tc{name: code.String() + " after hello", code: code, afterHello: true}, tc{name: code.String() + " instead of hello", code: code})
	}
	cases = append(cases, tc{name: "OK after hello", ok: true, afterHello: true}, tc{name: "OK instead of hello", ok: true})
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			srv := gliBServe(t, a, func(s *gliBStream) error {
				if _, err := s.RecvHello(); err != nil {
					return err
				}
				if s.N > 1 {
					if err := s.SendHello(int64(s.N)*100, "live:test"); err != nil {
						return err
					}
					for {
						f, err := s.RecvFrame()
						if err != nil {
							return nil
						}
						if err := s.Ack(ingestpb.AckCode_ACCEPTED, int64(s.N)*100, f.GetSequence()+1); err != nil {
							return err
						}
					}
				}
				if c.afterHello {
					if err := s.SendHello(100, "live:test"); err != nil {
						return err
					}
				}
				if c.ok {
					return nil
				}
				return status.Error(c.code, "gli test")
			})
			o := a.Options(srv.Target())
			o.ExhaustedMin = 300 * time.Millisecond
			cl := gliBRun(t, o)
			second := gliBWaitFrames(t, srv, 1, 1, 20*time.Second)
			if second.Hello == nil {
				t.Errorf("GLI-083 (6): the second stream has no hello")
			}
			gliBCheckFrame(t, a, 200, second.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
			gliBStillRunning(t, cl, "after "+c.name)
		})
	}
}

// GLI-083 (2): an invalid ServerHello (session not positive, empty profile ID,
// an offline: prefix, more than 128 bytes, characters outside the identifier
// set) makes the agent close the stream without sending a frame and reconnect;
// the boundary values (session MaxInt64, a 128 byte profile ID) are accepted.
func TestGLI083_InvalidServerHelloIsRejected(t *testing.T) {
	type tc struct {
		name    string
		session int64
		profile string
		valid   bool
	}
	cases := []tc{
		{"session zero", 0, "live:x", false},
		{"session negative", -1, "live:x", false},
		{"session min int64", math.MinInt64, "live:x", false},
		{"empty profile id", 7, "", false},
		{"offline prefix", 7, "offline:lab", false},
		{"offline prefix only", 7, "offline:", false},
		{"profile 129 bytes", 7, strings.Repeat("a", 129), false},
		{"profile with a space", 7, "has space", false},
		{"profile non-ASCII", 7, "caf\u00e9", false},
		{"profile control character", 7, "ctl\x01x", false},
		{"session max int64", math.MaxInt64, "live:x", true},
		{"profile 128 bytes", 7, strings.Repeat("a", 128), true},
		{"session one", 1, "live:default", true},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			srv := gliBServe(t, a, func(s *gliBStream) error {
				if _, err := s.RecvHello(); err != nil {
					return err
				}
				session, profile := int64(s.N)*1000, "live:test"
				if s.N == 1 {
					session, profile = c.session, c.profile
				}
				if err := s.SendHello(session, profile); err != nil {
					return err
				}
				for {
					f, err := s.RecvFrame() // for an invalid hello this returns when the agent ends the stream; a frame is a violation
					if err != nil {
						return nil
					}
					if s.N == 1 && !c.valid {
						return nil
					}
					if err := s.Ack(ingestpb.AckCode_ACCEPTED, session, f.GetSequence()+1); err != nil {
						return err
					}
				}
			})
			cl := gliBRun(t, a.Options(srv.Target()))
			if c.valid {
				info := gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
				gliBCheckFrame(t, a, c.session, info.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
				gliBStillRunning(t, cl, "with a valid ServerHello")
				return
			}
			first := gliBWaitClosed(t, srv, 0, 15*time.Second)
			gliBWaitFrames(t, srv, 1, 1, 15*time.Second)
			if len(first.Frames) != 0 {
				t.Errorf("GLI-083 (2): the agent sent %d frame(s) after an invalid ServerHello", len(first.Frames))
			}
			gliBStillRunning(t, cl, "after an invalid ServerHello")
		})
	}
}

// GLI-083 (1)(4): HelloTimeout and AckTimeout (shortened through Options)
// close the stream at the configured time (neither early nor late) and the agent
// reconnects with a new session and sequence 0.
func TestGLI083_HelloAndAckTimeouts(t *testing.T) {
	const timeout = 600 * time.Millisecond
	t.Run("hello timeout", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			if s.N == 1 {
				<-s.Ctx().Done() // no ServerHello, ever
				return nil
			}
			return gliBAcceptAllAfterHello(s)
		})
		o := a.Options(srv.Target())
		o.HelloTimeout = timeout
		cl := gliBRun(t, o)
		first := gliBWaitClosed(t, srv, 0, 20*time.Second)
		if d := first.ClosedAt.Sub(first.HelloAt); d < timeout*8/10 || d > timeout+3*time.Second {
			t.Errorf("GLI-083 (1): the stream ended %v after the hello, want about HelloTimeout %v", d, timeout)
		}
		gliBWaitConn(t, srv, 1, 20*time.Second, "the agent reconnected after the hello timeout")
		gliBStillRunning(t, cl, "after a hello timeout")
	})
	t.Run("ack timeout", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			if s.N > 1 {
				if err := s.SendHello(int64(s.N)*10, "live:test"); err != nil {
					return err
				}
				for {
					f, err := s.RecvFrame()
					if err != nil {
						return nil
					}
					if err := s.Ack(ingestpb.AckCode_ACCEPTED, int64(s.N)*10, f.GetSequence()+1); err != nil {
						return err
					}
				}
			}
			if err := s.SendHello(10, "live:test"); err != nil {
				return err
			}
			if _, err := s.RecvFrame(); err != nil {
				return nil
			}
			<-s.Ctx().Done() // the frame is never acknowledged
			return nil
		})
		o := a.Options(srv.Target())
		o.AckTimeout = timeout
		cl := gliBRun(t, o)
		first := gliBWaitClosed(t, srv, 0, 20*time.Second)
		if len(first.FrameAt) != 1 {
			t.Fatalf("GLI-083 (4): the first stream carried %d frames, want 1 (stop-and-wait)", len(first.FrameAt))
		}
		if d := first.ClosedAt.Sub(first.FrameAt[0]); d < timeout*8/10 || d > timeout+3*time.Second {
			t.Errorf("GLI-083 (4): the stream ended %v after the unacknowledged frame, want about AckTimeout %v", d, timeout)
		}
		second := gliBWaitFrames(t, srv, 1, 1, 20*time.Second)
		gliBCheckFrame(t, a, 20, second.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
		gliBStillRunning(t, cl, "after an ack timeout")
	})
}

// GLI-083 (5): the reconnect delay doubles from ReconnectInitial to
// ReconnectMax with +-20% jitter, and returns to ReconnectInitial after a
// session that received at least one ACCEPTED.
func TestGLI083_ReconnectBackoffDoublesAndResets(t *testing.T) {
	const initial, max = 300 * time.Millisecond, 1200 * time.Millisecond
	gaps := func(cs []gliBConnInfo) []time.Duration {
		var out []time.Duration
		for i := 0; i+1 < len(cs); i++ {
			out = append(out, cs[i+1].StartAt.Sub(cs[i].ClosedAt))
		}
		return out
	}
	check := func(t *testing.T, g []time.Duration, nominal []time.Duration) {
		t.Helper()
		for i, n := range nominal {
			if i >= len(g) {
				t.Fatalf("only %d reconnect gaps observed, want %d", len(g), len(nominal))
			}
			lo, hi := n*8/10-60*time.Millisecond, n*12/10+1500*time.Millisecond
			if g[i] < lo || g[i] > hi {
				t.Errorf("GLI-083 (5): reconnect gap %d = %v, want within [%v, %v] (nominal %v, jitter +-20%%)", i+1, g[i], lo, hi, n)
			}
		}
	}
	t.Run("doubling_to_the_maximum", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			if err := s.SendHello(int64(s.N), "live:test"); err != nil {
				return err
			}
			return status.Error(codes.Unavailable, "gli test")
		})
		o := a.Options(srv.Target())
		o.ReconnectInitial, o.ReconnectMax = initial, max
		cl := gliBRun(t, o)
		gliBEventually(t, 20*time.Second, "six connections", func() bool { return srv.Count() >= 6 })
		gliBEventually(t, 5*time.Second, "the sixth connection ended", func() bool { return !srv.Conns()[5].ClosedAt.IsZero() })
		g := gaps(srv.Conns())
		check(t, g, []time.Duration{initial, 2 * initial, 4 * initial, max, max})
		if g[3] <= g[0] {
			t.Errorf("GLI-083 (5): the delay did not grow: gaps %v", g)
		}
		gliBStillRunning(t, cl, "while backing off")
	})
	t.Run("reset_after_an_accepted_frame", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			if err := s.SendHello(int64(s.N), "live:test"); err != nil {
				return err
			}
			if s.N == 4 { // one ACCEPTED, then the stream ends: the delay must start over
				f, err := s.RecvFrame()
				if err != nil {
					return nil
				}
				if err := s.Ack(ingestpb.AckCode_ACCEPTED, 4, f.GetSequence()+1); err != nil {
					return err
				}
			}
			return status.Error(codes.Unavailable, "gli test")
		})
		o := a.Options(srv.Target())
		o.ReconnectInitial, o.ReconnectMax = initial, 8*initial
		cl := gliBRun(t, o)
		gliBEventually(t, 20*time.Second, "five connections", func() bool { return srv.Count() >= 5 })
		gliBWaitClosed(t, srv, 3, 5*time.Second)
		g := gaps(srv.Conns())
		check(t, g[:3], []time.Duration{initial, 2 * initial, 4 * initial})
		lo, hi := initial*8/10-60*time.Millisecond, initial*12/10+500*time.Millisecond
		if g[3] < lo || g[3] > hi {
			t.Errorf("GLI-083 (5): after a session with an ACCEPTED the reconnect gap = %v, want about ReconnectInitial %v (within [%v, %v]); the unreset delay would be %v", g[3], initial, lo, hi, 8*initial)
		}
		gliBStillRunning(t, cl, "after the reset")
	})
}

// GLI-083 (5): after ResourceExhausted the wait is at least ExhaustedMin
// (whether the status ends the stream before or after a frame), while any other
// status waits only the normal back-off.
func TestGLI083_ResourceExhaustedWaitsAtLeastExhaustedMin(t *testing.T) {
	const floor = 1500 * time.Millisecond
	for _, c := range []struct {
		name        string
		code        codes.Code
		afterFrame  bool
		wantAtLeast bool
	}{
		{"exhausted after hello", codes.ResourceExhausted, false, true},
		{"exhausted after a frame", codes.ResourceExhausted, true, true},
		{"unavailable control", codes.Unavailable, false, false},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			srv := gliBServe(t, a, func(s *gliBStream) error {
				if _, err := s.RecvHello(); err != nil {
					return err
				}
				if s.N > 1 {
					return gliBAcceptAllAfterHello(s)
				}
				if err := s.SendHello(1, "live:test"); err != nil {
					return err
				}
				if c.afterFrame {
					if _, err := s.RecvFrame(); err != nil {
						return nil
					}
				}
				return status.Error(c.code, "gli test")
			})
			o := a.Options(srv.Target())
			o.ReconnectInitial, o.ReconnectMax, o.ExhaustedMin = 100*time.Millisecond, 200*time.Millisecond, floor
			cl := gliBRun(t, o)
			first := gliBWaitClosed(t, srv, 0, 15*time.Second)
			second := gliBWaitConn(t, srv, 1, 20*time.Second, "the agent reconnected")
			gap := second.StartAt.Sub(first.ClosedAt)
			switch {
			case c.wantAtLeast && gap < floor-30*time.Millisecond:
				t.Errorf("GLI-083 (5): reconnected %v after ResourceExhausted, want at least ExhaustedMin %v", gap, floor)
			case !c.wantAtLeast && gap > floor:
				t.Errorf("GLI-083 (5): reconnected %v after %v, want the normal back-off (<< ExhaustedMin %v)", gap, c.code, floor)
			}
			gliBStillRunning(t, cl, "after the status")
		})
	}
}

// gliBAcceptAllAfterHello is gliBAcceptAll for a stream whose hello was read.
func gliBAcceptAllAfterHello(s *gliBStream) error {
	session := int64(s.N) * 100
	if err := s.SendHello(session, "live:test"); err != nil {
		return err
	}
	for {
		f, err := s.RecvFrame()
		if err != nil {
			return nil
		}
		if err := s.Ack(ingestpb.AckCode_ACCEPTED, session, f.GetSequence()+1); err != nil {
			return err
		}
	}
}

// GLI-083: cancelling the context ends Run with nil in every state - waiting
// for the ServerHello, waiting for an ack, sleeping between attempts, streaming,
// and before any connection exists - promptly (long timeouts and delays are
// configured so that only the cancellation can end the wait) and no connection
// is opened afterwards.
func TestGLI083_ContextCancelEndsEveryWait(t *testing.T) {
	cancelAndCheck := func(t *testing.T, cl *gliBClient, srv *gliBServer) {
		t.Helper()
		start := time.Now()
		if err := cl.Stop(t, 5*time.Second); err != nil {
			t.Errorf("GLI-083: Run returned %v on cancellation, want nil", err)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("GLI-083: Run took %v to return after the cancellation, want a prompt return", el)
		}
		if srv != nil {
			n := srv.Count()
			time.Sleep(700 * time.Millisecond)
			if srv.Count() != n {
				t.Errorf("GLI-083: a new connection was opened after the cancellation")
			}
		}
	}
	t.Run("waiting_for_server_hello", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			<-s.Ctx().Done()
			return nil
		})
		o := a.Options(srv.Target())
		o.HelloTimeout = time.Minute
		cl := gliBRun(t, o)
		gliBEventually(t, 15*time.Second, "the hello arrived", func() bool { i, ok := gliBConnAt(srv, 0); return ok && i.Hello != nil })
		cancelAndCheck(t, cl, srv)
		gliBWaitClosed(t, srv, 0, 5*time.Second)
	})
	t.Run("waiting_for_an_ack", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
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
		})
		o := a.Options(srv.Target())
		o.AckTimeout = time.Minute
		cl := gliBRun(t, o)
		gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
		cancelAndCheck(t, cl, srv)
		gliBWaitClosed(t, srv, 0, 5*time.Second)
	})
	t.Run("sleeping_between_attempts", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, func(s *gliBStream) error {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			return status.Error(codes.Unavailable, "gli test")
		})
		o := a.Options(srv.Target())
		o.ReconnectInitial, o.ReconnectMax = 30*time.Second, 30*time.Second
		cl := gliBRun(t, o)
		gliBWaitClosed(t, srv, 0, 15*time.Second)
		time.Sleep(400 * time.Millisecond) // the agent is now in its 24-36 s back-off sleep
		cancelAndCheck(t, cl, srv)
	})
	t.Run("streaming", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, gliBAcceptAll(0, nil))
		cl := gliBRun(t, a.Options(srv.Target()))
		gliBWaitFrames(t, srv, 0, 2, 15*time.Second)
		cancelAndCheck(t, cl, srv)
		gliBWaitClosed(t, srv, 0, 5*time.Second)
	})
	t.Run("before_any_connection", func(t *testing.T) {
		a := gliBNewAgent(t)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		cl := gliBRun(t, a.Options(fmt.Sprintf("localhost:%d", port)))
		time.Sleep(500 * time.Millisecond)
		gliBStillRunning(t, cl, "while the controller is unreachable")
		cancelAndCheck(t, cl, nil)
	})
}

// GLI-083 (1): the server certificate is verified against the configured CA and
// the DNS name of --controller with TLS 1.3 as the floor: a certificate from
// another CA, one that names another DNS name, an expired one, one without the
// ServerAuth usage, and a TLS 1.2-only server never get a hello (and the agent
// keeps retrying, it does not stop); the correct certificate does.
func TestGLI083_UntrustedServersNeverReceiveAHello(t *testing.T) {
	a := gliBNewAgent(t)
	foreign := gliBNewPKI(t)
	expired := func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-48 * time.Hour)
		c.NotAfter = time.Now().Add(-24 * time.Hour)
	}
	type tc struct {
		name      string
		cert      gliBLeaf
		min, max  uint16
		wantHello bool
	}
	cases := []tc{
		{name: "control: trusted certificate", cert: a.PKI.Server(t), wantHello: true},
		{name: "certificate of another CA", cert: foreign.Server(t)},
		{name: "certificate for another DNS name", cert: a.PKI.Server(t, "other.example")},
		{name: "expired certificate", cert: a.PKI.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "expired"}, DNSNames: []string{"localhost"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, expired)},
		{name: "certificate without the ServerAuth usage", cert: a.PKI.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "client only"}, DNSNames: []string{"localhost"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})},
		{name: "TLS 1.2 only server", cert: a.PKI.Server(t), min: tls.VersionTLS12, max: tls.VersionTLS12},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			srv := gliBStartServer(t, gliBServerOpts{ServerCert: c.cert, ClientCAs: []*gliBPKI{a.PKI}, MinVersion: c.min, MaxVersion: c.max}, gliBAcceptAll(0, nil))
			cl := gliBRun(t, a.Options(srv.Target()))
			if c.wantHello {
				info := gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
				if info.TLSVersion != tls.VersionTLS13 {
					t.Errorf("GLI-020: negotiated 0x%04x, want TLS 1.3", info.TLSVersion)
				}
				return
			}
			time.Sleep(2500 * time.Millisecond)
			if n := srv.Count(); n != 0 {
				t.Errorf("GLI-083 (1): the agent opened %d stream(s) to a server it must not trust (no hello may reach it)", n)
			}
			gliBStillRunning(t, cl, "against an untrusted server")
		})
	}
}

// GLI-083 (1)/GLI-085: CA, certificate and key are read again for every
// connection attempt: a rotated client leaf is what the server sees next, and a
// rotated CA (the server switches to a certificate of a second PKI) is trusted
// on the next attempt.
func TestGLI083_CertificatesAndCAAreReadPerAttempt(t *testing.T) {
	a := gliBNewAgent(t)
	pki2 := gliBNewPKI(t)
	leafB := a.PKI.Node(t, a.Cluster, a.NodeUID)
	leaf2 := pki2.Server(t)
	server2, err := tls.LoadX509KeyPair(leaf2.Cert, leaf2.Key)
	if err != nil {
		t.Fatal(err)
	}
	srv := gliBStartServer(t, gliBServerOpts{ServerCert: a.PKI.Server(t), ClientCAs: []*gliBPKI{a.PKI, pki2}}, nil)
	var rotateErr atomic.Value
	srv.SetHandler(func(s *gliBStream) error {
		if s.N > 1 {
			if _, err := s.RecvHello(); err != nil {
				return err
			}
			return gliBAcceptAllAfterHello(s)
		}
		if _, err := s.RecvHello(); err != nil {
			return err
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
		// rotate everything before the stream ends: the client leaf and key, the CA file, the server certificate.
		for _, p := range [][2]string{{leafB.Cert, a.CertFile}, {leafB.Key, a.KeyFile}, {pki2.CAFile, a.CAFile}} {
			b, err := os.ReadFile(p[0])
			if err == nil {
				err = os.WriteFile(p[1]+".new", b, 0o600)
			}
			if err == nil {
				err = os.Rename(p[1]+".new", p[1])
			}
			if err != nil {
				rotateErr.Store(err.Error())
			}
		}
		srv.cert.Store(&server2)
		return status.Error(codes.Unavailable, "rotate")
	})
	o := a.Options(srv.Target())
	o.ReconnectInitial, o.ReconnectMax = 400*time.Millisecond, 400*time.Millisecond
	cl := gliBRun(t, o)
	first := gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
	second := gliBWaitFrames(t, srv, 1, 1, 20*time.Second)
	if v := rotateErr.Load(); v != nil {
		t.Fatalf("fixture: the rotation failed: %v", v)
	}
	if first.Leaf == nil || second.Leaf == nil || first.Leaf.SerialNumber.Cmp(a.Leaf.Leaf.SerialNumber) != 0 {
		t.Fatalf("fixture: the first connection did not present the original leaf")
	}
	if second.Leaf.SerialNumber.Cmp(leafB.Leaf.SerialNumber) != 0 {
		t.Errorf("GLI-083 (1): the second connection presented serial %v, want the rotated leaf %v: the client certificate must be re-read for every attempt", second.Leaf.SerialNumber, leafB.Leaf.SerialNumber)
	}
	gliBStillRunning(t, cl, "after the rotation")
}

// GLI-085: a read or parse failure of the credential files after start is not a
// reason to stop: the agent logs the class, retries with back-off and connects
// again as soon as the files are valid; the log never carries a path, a
// certificate or a key.
func TestGLI085_FilesDamagedAfterStartAreRetried(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(a *gliBAgent) error
	}{
		{"certificate garbage", func(a *gliBAgent) error { return os.WriteFile(a.CertFile, []byte("garbage"), 0o600) }},
		{"key garbage", func(a *gliBAgent) error { return os.WriteFile(a.KeyFile, []byte("garbage"), 0o600) }},
		{"ca removed", func(a *gliBAgent) error { return os.Remove(a.CAFile) }},
		{"certificate removed", func(a *gliBAgent) error { return os.Remove(a.CertFile) }},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			orig := a.Leaf
			caCopy := string(a.PKI.CAPEM)
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
				if err := c.damage(a); err != nil {
					damageErr.Store(err.Error())
				}
				return status.Error(codes.Unavailable, "damaged")
			})
			cl := gliBRun(t, a.Options(srv.Target()))
			gliBWaitClosed(t, srv, 0, 15*time.Second)
			time.Sleep(1500 * time.Millisecond)
			if v := damageErr.Load(); v != nil {
				t.Fatalf("fixture: %v", v)
			}
			gliBStillRunning(t, cl, "although its credential files are damaged")
			if n := srv.Count(); n != 1 {
				t.Errorf("GLI-085: %d connection(s) were made with damaged credential files, want none", n-1)
			}
			// restore valid files: the next attempt connects.
			gliBWrite(t, a.CAFile, caCopy, 0o644)
			gliBInstall(t, a.CertFile, a.KeyFile, orig)
			gliBWaitFrames(t, srv, 1, 1, 20*time.Second)
			logs := a.Logs.String()
			for _, bad := range []string{gliBPathMarker, "PRIVATE KEY", "BEGIN CERTIFICATE", "spiffe://", "data-path-assurance.local"} {
				if strings.Contains(logs, bad) {
					t.Errorf("GLI-085/101: the log contains %q: %q", bad, gliXHead(logs, 400))
				}
			}
		})
	}
}

// ---------------------------------------------------------------- GLI-082 / 081 / 084 / 086

// GLI-082/081 (h): observed_at is the clock at the start of the cycle and every
// time of the frame follows it; EvidenceTTL sets the expiry; a cycle whose
// time is not strictly later than the previous frame's sends nothing - four
// skipped cycles keep the session, five in a row end it - and the new session's
// sequence 0 is exempt (it may carry an earlier or equal time).
func TestGLI082_ClockTTLAndNonMonotonicCycles(t *testing.T) {
	a := gliBNewAgent(t)
	t0 := time.Date(2026, 10, 3, 0, 0, 0, 123456789, time.UTC)
	clk := &gliBClock{now: t0}
	srv := gliBServe(t, a, gliBAcceptAll(60, nil))
	o := a.Options(srv.Target())
	o.Clock = clk
	o.Interval = 100 * time.Millisecond
	o.EvidenceTTL = 90 * time.Second
	cl := gliBRun(t, o)

	first := gliBWaitFrames(t, srv, 0, 1, 15*time.Second)
	gliBCheckFrame(t, a, 61, first.Frames[0], 0, 90*time.Second, ingestpb.Completeness_COMPLETE, true)
	if !first.Frames[0].GetObservedAt().AsTime().Equal(t0) {
		t.Errorf("GLI-082: observed_at = %v, want the clock value %v", first.Frames[0].GetObservedAt().AsTime(), t0)
	}
	time.Sleep(250 * time.Millisecond) // two or three skipped cycles: fewer than five
	if n := len(srv.Conns()[0].Frames); n != 1 || srv.Count() != 1 {
		t.Fatalf("GLI-082: a stopped clock produced %d frames on %d streams, want 1 frame and the same stream (no frame without a later time)", n, srv.Count())
	}
	clk.Advance(time.Second)
	second := gliBWaitFrames(t, srv, 0, 2, 15*time.Second)
	gliBCheckFrame(t, a, 61, second.Frames[1], 1, 90*time.Second, ingestpb.Completeness_COMPLETE, true)
	if got := second.Frames[1].GetObservedAt().AsTime(); !got.Equal(t0.Add(time.Second)) {
		t.Errorf("GLI-082: observed_at of frame 1 = %v, want %v", got, t0.Add(time.Second))
	}
	if srv.Count() != 1 {
		t.Errorf("GLI-083 (3): fewer than five skipped cycles must not end the session")
	}

	// five skipped cycles in a row: the stream is closed and a new session starts at sequence 0 with an equal time.
	conn2 := gliBWaitFrames(t, srv, 1, 1, 20*time.Second)
	gliBCheckFrame(t, a, 62, conn2.Frames[0], 0, 90*time.Second, ingestpb.Completeness_COMPLETE, true)
	if got := conn2.Frames[0].GetObservedAt().AsTime(); !got.Equal(t0.Add(time.Second)) {
		t.Errorf("GLI-082: the new session's sequence 0 carries %v, want the unchanged clock %v (the monotonic rule does not apply to sequence 0)", got, t0.Add(time.Second))
	}

	// a clock that moved backwards behaves the same way.
	clk.Set(t0)
	conn3 := gliBWaitFrames(t, srv, 2, 1, 20*time.Second)
	gliBCheckFrame(t, a, 63, conn3.Frames[0], 0, 90*time.Second, ingestpb.Completeness_COMPLETE, true)
	if got := conn3.Frames[0].GetObservedAt().AsTime(); !got.Equal(t0) {
		t.Errorf("GLI-082: after the clock moved back the new session's sequence 0 carries %v, want %v", got, t0)
	}
	gliBStillRunning(t, cl, "after clock changes")
}

// GLI-081 (b)(f)/GFO-050: a partial sysfs read sends nothing before the baseline
// (five failed cycles end the stream), complete sequence 0 follows when the
// fixture is whole again; after the baseline a PARTIAL frame is sent in
// sequence, and the next complete cycle sends COMPLETE again, all on one
// session.
func TestGLI081_PartialCollectionBeforeAndAfterTheBaseline(t *testing.T) {
	t.Run("before_the_baseline", func(t *testing.T) {
		a := gliBNewAgent(t)
		gitkeep := filepath.Join(a.Sysfs, "bus", "pci", "devices", ".gitkeep")
		gliBWrite(t, gitkeep, "", 0o644) // GFO-041: a non-canonical name makes the frame PARTIAL
		srv := gliBServe(t, a, gliBAcceptAll(70, nil))
		cl := gliBRun(t, a.Options(srv.Target()))
		gliBWaitConn(t, srv, 1, 20*time.Second, "five failed cycles end the stream and the agent reconnects")
		first := gliBWaitClosed(t, srv, 0, 5*time.Second)
		if len(first.Frames) != 0 {
			t.Errorf("GLI-083 (3): %d frame(s) were sent while every collection was PARTIAL, want none (sequence 0 must be complete)", len(first.Frames))
		}
		if err := os.Remove(gitkeep); err != nil {
			t.Fatal(err)
		}
		var info gliBConnInfo
		gliBEventually(t, 20*time.Second, "a complete sequence 0 after the fixture is whole", func() bool {
			for _, c := range srv.Conns() {
				if len(c.Frames) > 0 {
					info = c
					return true
				}
			}
			return false
		})
		gliBCheckFrame(t, a, 70+int64(info.N), info.Frames[0], 0, 300*time.Second, ingestpb.Completeness_COMPLETE, true)
		gliBStillRunning(t, cl, "after the partial phase")
	})
	t.Run("after_the_baseline", func(t *testing.T) {
		a := gliBNewAgent(t)
		srv := gliBServe(t, a, gliBAcceptAll(70, nil))
		cl := gliBRun(t, a.Options(srv.Target()))
		gliBWaitFrames(t, srv, 0, 2, 15*time.Second)
		gitkeep := filepath.Join(a.Sysfs, "bus", "pci", "devices", ".gitkeep")
		gliBWrite(t, gitkeep, "", 0o644)
		var partial int
		gliBEventually(t, 15*time.Second, "a PARTIAL frame", func() bool {
			for i, f := range srv.Conns()[0].Frames {
				if f.GetCompleteness() == ingestpb.Completeness_PARTIAL {
					partial = i
					return true
				}
			}
			return false
		})
		if err := os.Remove(gitkeep); err != nil {
			t.Fatal(err)
		}
		info := gliBWaitFrames(t, srv, 0, partial+3, 15*time.Second)
		gliBCheckEnvelope(t, a, 71, info.Frames[partial], uint64(partial), ingestpb.Completeness_PARTIAL)
		sawComplete := false
		for i, f := range info.Frames {
			if i > partial && f.GetCompleteness() == ingestpb.Completeness_COMPLETE {
				sawComplete = true
			}
			if f.GetSequence() != uint64(i) || f.GetSession() != 71 {
				t.Errorf("GLI-083 (4): frame %d has session %d sequence %d: a PARTIAL frame after the baseline is sent in sequence on the same session", i, f.GetSession(), f.GetSequence())
			}
		}
		if !sawComplete {
			t.Errorf("GLI-043: no COMPLETE frame follows the partial one after the fixture is whole again")
		}
		if srv.Count() != 1 {
			t.Errorf("GLI-083 (4): the agent reconnected %d time(s) although every frame was acknowledged", srv.Count()-1)
		}
		gliBStillRunning(t, cl, "after the partial frame")
	})
}

// GLI-081 (b)/GFO-020..026: nvidia-smi runs with the exact argv for every frame;
// an absent, failing, malformed or duplicate-row inventory gives binding 0 and
// does not change completeness.
func TestGLI081_NvidiaInventoryIsPerFrameAndOptional(t *testing.T) {
	t.Run("argv_and_one_run_per_frame", func(t *testing.T) {
		a := gliBNewAgent(t)
		counter := filepath.Join(a.Dir, "smi-runs")
		a.SMI = gliBScript(t, a.Dir, "nvidia-smi-counting", "echo \"$@\" >> '"+counter+"'\nprintf '%s, %s\\n' '"+gliBGPUUUID+"' '00000000:03:00.0'\n")
		srv := gliBServe(t, a, gliBAcceptAll(80, nil))
		cl := gliBRun(t, a.Options(srv.Target()))
		info := gliBWaitFrames(t, srv, 0, 4, 15*time.Second)
		for i, f := range info.Frames[:4] {
			gliBCheckFrame(t, a, 81, f, uint64(i), 300*time.Second, ingestpb.Completeness_COMPLETE, true)
		}
		b, err := os.ReadFile(counter)
		if err != nil {
			t.Fatalf("the fake nvidia-smi never ran: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) < 4 {
			t.Errorf("GLI-081 (b): nvidia-smi ran %d time(s) for at least 4 frames, want once per frame", len(lines))
		}
		for _, l := range lines {
			if l != "--query-gpu=uuid,pci.bus_id --format=csv,noheader,nounits" {
				t.Errorf("GFO-022: nvidia-smi argv = %q", l)
			}
		}
		gliBStillRunning(t, cl, "while querying nvidia-smi")
	})
	for _, c := range []struct {
		name, body string
		absent     bool
	}{
		{name: "absent", absent: true},
		{name: "exit status 1", body: "echo broken >&2\nexit 1\n"},
		{name: "malformed output", body: "echo 'not,a,valid,row'\n"},
		{name: "empty output", body: "exit 0\n"},
		{name: "duplicate uuid rows", body: "printf '%s, %s\\n%s, %s\\n' '" + gliBGPUUUID + "' '00000000:03:00.0' '" + gliBGPUUUID + "' '00000000:04:00.0'\n"},
		{name: "uppercase uuid", body: "printf 'GPU-5F0B1C2D-3E4F-4A5B-8C6D-7E8F9A0B1C2D, 00000000:03:00.0\\n'\n"},
	} {
		t.Run("no_binding_"+strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			a := gliBNewAgent(t)
			if c.absent {
				a.SMI = ""
			} else {
				a.SMI = gliBScript(t, a.Dir, "nvidia-smi-bad", c.body)
			}
			srv := gliBServe(t, a, gliBAcceptAll(80, nil))
			cl := gliBRun(t, a.Options(srv.Target()))
			info := gliBWaitFrames(t, srv, 0, 2, 15*time.Second)
			for i, f := range info.Frames[:2] {
				gliBCheckFrame(t, a, 81, f, uint64(i), 300*time.Second, ingestpb.Completeness_COMPLETE, false)
			}
			gliBStillRunning(t, cl, "without an inventory")
		})
	}
}

// GLI-084: the boot ID file is read again for every connection (a rotated file
// is picked up), surrounding ASCII whitespace is trimmed, and 128 bytes are
// accepted.
func TestGLI084_BootIDIsReadForEveryConnection(t *testing.T) {
	a := gliBNewAgent(t)
	long := strings.Repeat("b", 128)
	var writeErr atomic.Value
	srv := gliBServe(t, a, nil)
	srv.SetHandler(func(s *gliBStream) error {
		if _, err := s.RecvHello(); err != nil {
			return err
		}
		if err := s.SendHello(int64(s.N), "live:test"); err != nil {
			return err
		}
		f, err := s.RecvFrame()
		if err != nil {
			return nil
		}
		if err := s.Ack(ingestpb.AckCode_ACCEPTED, int64(s.N), f.GetSequence()+1); err != nil {
			return err
		}
		next := map[int]string{1: "\t boot-two \r\n", 2: long + "\n"}[s.N]
		if next != "" {
			if err := os.WriteFile(a.BootFile, []byte(next), 0o644); err != nil {
				writeErr.Store(err.Error())
			}
			return status.Error(codes.Unavailable, "rotate the boot id")
		}
		return gliBAcceptAllAfterFirst(s, int64(s.N))
	})
	o := a.Options(srv.Target())
	o.ReconnectInitial, o.ReconnectMax = 300*time.Millisecond, 300*time.Millisecond
	cl := gliBRun(t, o)
	c3 := gliBWaitFrames(t, srv, 2, 1, 20*time.Second)
	if v := writeErr.Load(); v != nil {
		t.Fatalf("fixture: %v", v)
	}
	conns := srv.Conns()
	for i, want := range []string{a.BootID, "boot-two", long} {
		if got := conns[i].Hello.GetBootId(); got != want {
			t.Errorf("GLI-084: hello %d boot_id = %q, want %q", i+1, gliXHead(got, 40), gliXHead(want, 40))
		}
		if got := conns[i].Frames[0].GetBootId(); got != want {
			t.Errorf("GLI-084: frame 0 of connection %d boot_id = %q, want %q", i+1, gliXHead(got, 40), gliXHead(want, 40))
		}
	}
	_ = c3
	gliBStillRunning(t, cl, "after the boot ID rotation")
}

// gliBAcceptAllAfterFirst continues acknowledging a stream whose first frame was
// already acknowledged.
func gliBAcceptAllAfterFirst(s *gliBStream, session int64) error {
	for {
		f, err := s.RecvFrame()
		if err != nil {
			return nil
		}
		if err := s.Ack(ingestpb.AckCode_ACCEPTED, session, f.GetSequence()+1); err != nil {
			return err
		}
	}
}

// GLI-086: cancelling Run does not wait for a running nvidia-smi child: Run
// returns promptly although the child sleeps for a minute.
func TestGLI086_CancelDoesNotWaitForAHangingNvidiaSmi(t *testing.T) {
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
	cl := gliBRun(t, a.Options(srv.Target()))
	gliBEventually(t, 15*time.Second, "the fake nvidia-smi started", func() bool { _, err := os.Stat(pidFile); return err == nil })
	start := time.Now()
	if err := cl.Stop(t, 8*time.Second); err != nil {
		t.Errorf("GLI-086: Run returned %v, want nil", err)
	}
	if el := time.Since(start); el > 2500*time.Millisecond {
		t.Errorf("GLI-086: Run took %v to return with a hanging nvidia-smi child, want it not to wait for the child (GFO-024: within 1 s)", el)
	}
}

// GLI-101 (i)-(iv): unique markers in the nvidia-smi stderr and output, in the
// path of every flag file and in the GPU serial position never reach the log.
func TestGLI101_LibraryLogsCarryNoMarker(t *testing.T) {
	a := gliBNewAgent(t)
	const smiErr, smiCSV = "SMI-STDERR-MARKER-31c7", "SMI-CSV-MARKER-77ab"
	a.SMI = gliBScript(t, a.Dir, "nvidia-smi-noisy", "echo '"+smiErr+"' >&2\necho '"+smiCSV+", 0000:03:00.0'\nexit 3\n")
	srv := gliBServe(t, a, gliBAcceptAll(0, nil))
	o := a.Options(srv.Target())
	cl := gliBRun(t, o)
	gliBWaitFrames(t, srv, 0, 2, 15*time.Second)
	if err := cl.Stop(t, 5*time.Second); err != nil {
		t.Errorf("Run returned %v", err)
	}
	logs := a.Logs.String()
	for _, bad := range []string{smiErr, smiCSV, gliBPathMarker, "PRIVATE KEY", "BEGIN CERTIFICATE", "spiffe://"} {
		if strings.Contains(logs, bad) {
			t.Errorf("GLI-101: the log contains %q:\n%s", bad, gliXHead(logs, 500))
		}
	}
	if logs == "" {
		t.Logf("GLI-101: the client logged nothing at debug level; the marker check is vacuous for this run")
	}
}
