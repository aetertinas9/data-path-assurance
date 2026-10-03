package liveingest_test

// GLI-111: NewServer configuration validation, DefaultTrustedSources, and the
// lifetime of Serve (ErrAlreadyServed, listener errors, cancellation, ownership
// of the listener, BundleSource before and after Serve).

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func gliwSource(name string) model.SourceRef {
	return model.SourceRef{Type: "agent", Name: "path-agent/" + name}
}

// gliwNewServerNoPanic calls NewServer and fails the test if it panics.
func gliwNewServerNoPanic(t *testing.T, cfg ingest.Config) (srv *ingest.Server, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewServer panicked: %v", r)
		}
	}()
	return ingest.NewServer(cfg)
}

func TestGLI111_NewServerRejectsInvalidConfiguration(t *testing.T) {
	foreign := gliForeignPKI(t)
	type mutation func(c *ingest.Config, b *gliwBase)
	long := func(c string, n int) string { return strings.Repeat(c, n) }
	writeIn := func(b *gliwBase, name string, data []byte) string {
		p := filepath.Join(b.PKI.Dir(), name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			b.t.Fatalf("write: %v", err)
		}
		return p
	}
	readFile := func(b *gliwBase, path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			b.t.Fatalf("read: %v", err)
		}
		return data
	}
	cases := []struct {
		name string
		mut  mutation
	}{
		// ClusterID
		{"ClusterID empty", func(c *ingest.Config, b *gliwBase) { c.ClusterID = "" }},
		{"ClusterID with space", func(c *ingest.Config, b *gliwBase) { c.ClusterID = "lab a" }},
		{"ClusterID with slash", func(c *ingest.Config, b *gliwBase) { c.ClusterID = "lab/a" }},
		{"ClusterID with plus", func(c *ingest.Config, b *gliwBase) { c.ClusterID = "lab+a" }},
		{"ClusterID non-ASCII", func(c *ingest.Config, b *gliwBase) { c.ClusterID = "lab-é" }},
		{"ClusterID 129 bytes", func(c *ingest.Config, b *gliwBase) { c.ClusterID = long("a", 129) }},
		// CollectorProfileID
		{"profile empty", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "" }},
		{"profile offline: prefix", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "offline:x" }},
		{"profile exactly offline:", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "offline:" }},
		{"profile unmatched-source", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "unmatched-source" }},
		{"profile with space", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "live default" }},
		{"profile non-ASCII", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "live:é" }},
		{"profile with control byte", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "live:\x01" }},
		{"profile 129 bytes", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = long("p", 129) }},
		// TrustedSources
		{"no trusted sources (nil)", func(c *ingest.Config, b *gliwBase) { c.TrustedSources = nil }},
		{"no trusted sources (empty)", func(c *ingest.Config, b *gliwBase) { c.TrustedSources = []fleet.TrustedSource{} }},
		{"duplicate (capability, source)", func(c *ingest.Config, b *gliwBase) {
			e := fleet.TrustedSource{Capability: fleet.TrustSysfsPCIeWidth, Source: gliwSource("sysfs-width")}
			c.TrustedSources = []fleet.TrustedSource{e, e}
		}},
		{"TrustExternalFence granted", func(c *ingest.Config, b *gliwBase) {
			c.TrustedSources = append(ingest.DefaultTrustedSources(),
				fleet.TrustedSource{Capability: fleet.TrustExternalFence, Source: gliwSource("fence")})
		}},
		// ports
		{"Nodes nil", func(c *ingest.Config, b *gliwBase) { c.Nodes = nil }},
		{"Sessions nil", func(c *ingest.Config, b *gliwBase) { c.Sessions = nil }},
		// TLS files
		{"CA path empty", func(c *ingest.Config, b *gliwBase) { c.TLS.CAFile = "" }},
		{"CA file missing", func(c *ingest.Config, b *gliwBase) { c.TLS.CAFile = filepath.Join(b.PKI.Dir(), "absent.pem") }},
		{"CA file is not PEM", func(c *ingest.Config, b *gliwBase) {
			c.TLS.CAFile = writeIn(b, "ca-garbage.pem", []byte("this is not a certificate"))
		}},
		{"CA file is empty", func(c *ingest.Config, b *gliwBase) { c.TLS.CAFile = writeIn(b, "ca-empty.pem", nil) }},
		{"CA file holds a key and no certificate", func(c *ingest.Config, b *gliwBase) {
			c.TLS.CAFile = writeIn(b, "ca-key-only.pem", readFile(b, b.PKI.Server.KeyFile))
		}},
		{"certificate path empty", func(c *ingest.Config, b *gliwBase) { c.TLS.CertFile = "" }},
		{"certificate file missing", func(c *ingest.Config, b *gliwBase) { c.TLS.CertFile = filepath.Join(b.PKI.Dir(), "absent.crt") }},
		{"certificate file is not PEM", func(c *ingest.Config, b *gliwBase) {
			c.TLS.CertFile = writeIn(b, "cert-garbage.crt", []byte("garbage"))
		}},
		{"key path empty", func(c *ingest.Config, b *gliwBase) { c.TLS.KeyFile = "" }},
		{"key file missing", func(c *ingest.Config, b *gliwBase) { c.TLS.KeyFile = filepath.Join(b.PKI.Dir(), "absent.key") }},
		{"key file is not PEM", func(c *ingest.Config, b *gliwBase) {
			c.TLS.KeyFile = writeIn(b, "key-garbage.key", []byte("garbage"))
		}},
		{"key does not belong to the certificate", func(c *ingest.Config, b *gliwBase) { c.TLS.KeyFile = foreign.Server.KeyFile }},
		{"certificate does not carry the service DNS", func(c *ingest.Config, b *gliwBase) { c.ServiceDNS = "ingest.example.org" }},
		// Limits
		{"FrameBurst 4 (wider than 3)", func(c *ingest.Config, b *gliwBase) { c.Limits.FrameBurst = 4 }},
		{"FrameBurst negative", func(c *ingest.Config, b *gliwBase) { c.Limits.FrameBurst = -1 }},
		{"HelloBurst 4 (wider than 3)", func(c *ingest.Config, b *gliwBase) { c.Limits.HelloBurst = 4 }},
		{"HelloBurst negative", func(c *ingest.Config, b *gliwBase) { c.Limits.HelloBurst = -1 }},
		{"FrameInterval 9s (faster than 10s)", func(c *ingest.Config, b *gliwBase) { c.Limits.FrameInterval = 9 * time.Second }},
		{"FrameInterval 9.999s", func(c *ingest.Config, b *gliwBase) { c.Limits.FrameInterval = 9999 * time.Millisecond }},
		{"FrameInterval negative", func(c *ingest.Config, b *gliwBase) { c.Limits.FrameInterval = -time.Second }},
		{"HelloInterval 9s (faster than 10s)", func(c *ingest.Config, b *gliwBase) { c.Limits.HelloInterval = 9 * time.Second }},
		{"HelloInterval negative", func(c *ingest.Config, b *gliwBase) { c.Limits.HelloInterval = -time.Second }},
		{"MaxStreams negative", func(c *ingest.Config, b *gliwBase) { c.Limits.MaxStreams = -1 }},
		{"HelloTimeout negative", func(c *ingest.Config, b *gliwBase) { c.Limits.HelloTimeout = -time.Second }},
		{"IdleTimeout negative", func(c *ingest.Config, b *gliwBase) { c.Limits.IdleTimeout = -time.Second }},
		{"NodeRetention negative", func(c *ingest.Config, b *gliwBase) { c.Limits.NodeRetention = -time.Hour }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := gliwNewBase(t)
			cfg := b.Config()
			tc.mut(&cfg, b)
			srv, err := gliwNewServerNoPanic(t, cfg)
			if !errors.Is(err, ingest.ErrInvalidConfig) {
				t.Fatalf("NewServer error = %v, want one wrapping ErrInvalidConfig", err)
			}
			if srv != nil {
				t.Fatalf("NewServer returned a non-nil *Server together with an error")
			}
			if errors.Is(err, ingest.ErrAlreadyServed) {
				t.Errorf("ErrInvalidConfig error also matches ErrAlreadyServed")
			}
			if strings.Contains(err.Error(), "PRIVATE KEY") {
				t.Errorf("the error text contains key material")
			}
		})
	}
}

func TestGLI111_NewServerAcceptsValidConfiguration(t *testing.T) {
	type mutation func(c *ingest.Config, b *gliwBase)
	cases := []struct {
		name string
		mut  mutation
	}{
		{"defaults", func(c *ingest.Config, b *gliwBase) {}},
		{"optional fields nil", func(c *ingest.Config, b *gliwBase) { c.Leader, c.Clock, c.Logger = nil, nil, nil }},
		{"ServiceDNS empty skips the hostname check", func(c *ingest.Config, b *gliwBase) { c.ServiceDNS = "" }},
		{"ClusterID 128 bytes", func(c *ingest.Config, b *gliwBase) { c.ClusterID = strings.Repeat("c", 128) }},
		{"ClusterID with dots, dashes and underscores", func(c *ingest.Config, b *gliwBase) { c.ClusterID = "Lab_A-1.prod" }},
		{"profile 128 bytes", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = strings.Repeat("p", 128) }},
		{"profile offline- (no colon)", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "offline-live" }},
		{"profile with every printable ASCII byte class", func(c *ingest.Config, b *gliwBase) { c.CollectorProfileID = "live:A.b_c-d/e@f" }},
		{"same source under two capabilities", func(c *ingest.Config, b *gliwBase) {
			c.TrustedSources = []fleet.TrustedSource{
				{Capability: fleet.TrustSysfsPCIeWidth, Source: gliwSource("x")},
				{Capability: fleet.TrustSysfsPhysicalParent, Source: gliwSource("x")},
			}
		}},
		{"one capability, two sources", func(c *ingest.Config, b *gliwBase) {
			c.TrustedSources = []fleet.TrustedSource{
				{Capability: fleet.TrustSysfsPCIeWidth, Source: gliwSource("x")},
				{Capability: fleet.TrustSysfsPCIeWidth, Source: gliwSource("y")},
			}
		}},
		{"operator baseline capability is allowed", func(c *ingest.Config, b *gliwBase) {
			c.TrustedSources = append(ingest.DefaultTrustedSources(),
				fleet.TrustedSource{Capability: fleet.TrustOperatorBaseline, Source: gliwSource("baseline")})
		}},
		{"FrameBurst 3 and 1", func(c *ingest.Config, b *gliwBase) { c.Limits.FrameBurst, c.Limits.HelloBurst = 3, 1 }},
		{"intervals of exactly 10s", func(c *ingest.Config, b *gliwBase) {
			c.Limits.FrameInterval, c.Limits.HelloInterval = 10*time.Second, 10*time.Second
		}},
		{"intervals of one hour", func(c *ingest.Config, b *gliwBase) {
			c.Limits.FrameInterval, c.Limits.HelloInterval = time.Hour, time.Hour
		}},
		{"MaxStreams 1", func(c *ingest.Config, b *gliwBase) { c.Limits.MaxStreams = 1 }},
		{"MaxStreams 100000", func(c *ingest.Config, b *gliwBase) { c.Limits.MaxStreams = 100000 }},
		{"short positive timeouts", func(c *ingest.Config, b *gliwBase) {
			c.Limits.HelloTimeout, c.Limits.IdleTimeout, c.Limits.NodeRetention = time.Millisecond, time.Millisecond, time.Minute
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := gliwNewBase(t)
			cfg := b.Config()
			tc.mut(&cfg, b)
			srv, err := gliwNewServerNoPanic(t, cfg)
			if err != nil {
				t.Fatalf("NewServer rejected a valid configuration: %v", err)
			}
			if srv == nil {
				t.Fatal("NewServer returned a nil *Server without an error")
			}
		})
	}
	t.Run("zero Config is an error, not a panic", func(t *testing.T) {
		srv, err := gliwNewServerNoPanic(t, ingest.Config{})
		if !errors.Is(err, ingest.ErrInvalidConfig) || srv != nil {
			t.Fatalf("NewServer(Config{}) = %v, %v; want nil and ErrInvalidConfig", srv, err)
		}
	})
}

func TestGLI111_SentinelsAreDistinct(t *testing.T) {
	if ingest.ErrInvalidConfig == nil || ingest.ErrAlreadyServed == nil {
		t.Fatal("sentinel errors must be set")
	}
	if errors.Is(ingest.ErrInvalidConfig, ingest.ErrAlreadyServed) || errors.Is(ingest.ErrAlreadyServed, ingest.ErrInvalidConfig) {
		t.Fatal("ErrInvalidConfig and ErrAlreadyServed must be different errors")
	}
}

// TestGLI111_DefaultTrustedSourcesContent: the default profile of GFO section 2.3 and no
// operator-baseline capability (fail closed); every call returns a fresh slice.
func TestGLI111_DefaultTrustedSourcesContent(t *testing.T) {
	want := map[fleet.TrustedSource]bool{
		{Capability: fleet.TrustSysfsPhysicalParent, Source: gliwSource("sysfs-parent")}: true,
		{Capability: fleet.TrustSysfsPCIeWidth, Source: gliwSource("sysfs-width")}:       true,
		{Capability: fleet.TrustNVIDIAUUIDBinding, Source: gliwSource("nvidia-smi")}:     true,
	}
	got := ingest.DefaultTrustedSources()
	if len(got) != len(want) {
		t.Fatalf("DefaultTrustedSources() has %d entries, want %d: %+v", len(got), len(want), got)
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("unexpected default trusted source %+v", e)
		}
		if e.Capability == fleet.TrustOperatorBaseline || e.Capability == fleet.TrustExternalFence {
			t.Errorf("default sources grant %v", e.Capability)
		}
	}
	got[0] = fleet.TrustedSource{}
	again := ingest.DefaultTrustedSources()
	for _, e := range again {
		if !want[e] {
			t.Errorf("the second call returned the mutated entry %+v: the slice must be fresh", e)
		}
	}
}

func TestGLI111_ServeTwiceReturnsErrAlreadyServed(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "twice")
	serveAgain := func() error {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = lis.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.Server.Serve(ctx, lis) }()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("the second Serve did not return at once")
			return nil
		}
	}
	if err := serveAgain(); !errors.Is(err, ingest.ErrAlreadyServed) {
		t.Fatalf("second Serve while running = %v, want ErrAlreadyServed", err)
	}
	// The running server is not disturbed by the refused call.
	if _, _, h := s.Connect(t, n); h.GetSession() != 1 {
		t.Fatalf("session after the refused second Serve = %d", h.GetSession())
	}
	if _, err := s.Stop(t); err != nil {
		t.Fatalf("Serve after cancel = %v, want nil", err)
	}
	if err := serveAgain(); !errors.Is(err, ingest.ErrAlreadyServed) {
		t.Fatalf("Serve after a completed Serve = %v, want ErrAlreadyServed", err)
	}
}

// gliwFailListener fails every Accept with a fixed error.
type gliwFailListener struct{ err error }

func (l gliwFailListener) Accept() (net.Conn, error) { return nil, l.err }
func (l gliwFailListener) Close() error              { return nil }
func (l gliwFailListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func TestGLI111_ServeListenerErrorIsWrapped(t *testing.T) {
	s := gliwBuildServer(t)
	sentinel := errors.New("gliw listener failure")
	done := make(chan error, 1)
	go func() { done <- s.Server.Serve(context.Background(), gliwFailListener{err: sentinel}) }()
	select {
	case err := <-done:
		if !errors.Is(err, sentinel) {
			t.Fatalf("Serve = %v, want an error wrapping the listener error (%%w)", err)
		}
		if errors.Is(err, ingest.ErrAlreadyServed) {
			t.Fatalf("a listener failure was reported as ErrAlreadyServed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the listener failed")
	}
}

func TestGLI111_ServeDoesNotPanicOnNilListener(t *testing.T) {
	s := gliwBuildServer(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Serve(ctx, nil) panicked: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Server.Serve(ctx, nil)
}

func TestGLI111_BundleSourceBeforeServe(t *testing.T) {
	s := gliwBuildServer(t)
	n := s.AddNode(t, "before")
	src := s.Server.BundleSource()
	if src == nil {
		t.Fatal("BundleSource() = nil before Serve")
	}
	_, err := src.NodeBundles(context.Background(), s.Query(n, false))
	if !errors.Is(err, app.ErrNoObservation) {
		t.Fatalf("NodeBundles before Serve = %v, want app.ErrNoObservation", err)
	}
}

func TestGLI111_ServeCancellation(t *testing.T) {
	t.Run("idle server stops promptly and releases the listener", func(t *testing.T) {
		s := gliStartServer(t)
		took, err := s.Stop(t)
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
		if took > 3*time.Second {
			t.Errorf("Serve returned %v after the cancel, want well under the 5 s graceful limit", took)
		}
		c, err := net.DialTimeout("tcp", s.Addr, 2*time.Second)
		if err == nil {
			_ = c.Close()
			t.Fatal("the listener still accepts connections after Serve returned (Serve owns it)")
		}
	})
	t.Run("active stream ends with Unavailable and the state stays readable", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "cancel1")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		took, err := s.Stop(t)
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
		if took > 8*time.Second {
			t.Errorf("Serve returned %v after the cancel, want within 5 s (+3 s load margin)", took)
		}
		var last error
		for {
			_, err := st.Recv()
			if err != nil {
				last = err
				break
			}
		}
		gliwWantCode(t, "stream at shutdown", last, codes.Unavailable)
		s.WantSnapshot(t, "after Serve ended (view survives until retention)", n, h.GetSession(), 0)
	})
	t.Run("pending hello is cancelled and Serve waits for the handler", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "cancel2")
		s.Sessions.SetDelay(time.Hour)
		st, _ := s.Stream(t, n.Cert)
		result := make(chan error, 1)
		go func() {
			_, err := gliwHello(st, s.HelloMsg(n))
			result <- err
		}()
		gliwEventually(t, 10*time.Second, "the hello reached the session store", func() bool { return s.Sessions.InFlight() == 1 })
		took, err := s.Stop(t)
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
		if took > 8*time.Second {
			t.Errorf("Serve returned %v after the cancel, want within 5 s (+3 s load margin)", took)
		}
		if got := s.Sessions.InFlight(); got != 0 {
			t.Errorf("%d store call(s) still running after Serve returned: the handler must be finished", got)
		}
		select {
		case err := <-result:
			gliwWantCode(t, "pending hello", err, codes.Unavailable)
		case <-time.After(5 * time.Second):
			t.Fatal("the pending hello never got an answer")
		}
		if got := s.Sessions.Stored(n.UID); got != 0 {
			t.Errorf("a cancelled hello stored session %d", got)
		}
		calls := len(s.Sessions.Calls())
		time.Sleep(200 * time.Millisecond)
		if got := len(s.Sessions.Calls()); got != calls {
			t.Errorf("the session store was called after Serve returned (%d -> %d calls)", calls, got)
		}
	})
	t.Run("hello after the cancel is refused without touching the store", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "cancel3")
		st, _ := s.Stream(t, n.Cert) // authenticated, no hello yet
		time.Sleep(300 * time.Millisecond)
		s.cancel()
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "hello after cancel", err, codes.Unavailable)
		if c := s.Sessions.Calls(); len(c) != 0 {
			t.Errorf("the store was called after the cancel: %+v", c)
		}
	})
}

// gliwServerGoroutines counts goroutines currently executing code of the ingest
// adapter or the liveingest engine (frames named after those import paths).
func gliwServerGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "data-path-assurance/internal/ingest.") ||
			strings.Contains(g, "data-path-assurance/internal/app/liveingest.") {
			count++
		}
	}
	return count
}

// TestGLI005_ServerGoroutinesEndBeforeServeReturns: every goroutine owned by the
// server (handlers, timers, sweepers) is finished when Serve returns, also when
// streams are established, waiting for a hello, or blocked in the session store.
func TestGLI005_ServerGoroutinesEndBeforeServeReturns(t *testing.T) {
	base := gliwServerGoroutines()
	s := gliStartServer(t)
	n1, n2, n3 := s.AddNode(t, "gr1"), s.AddNode(t, "gr2"), s.AddNode(t, "gr3")
	st1, _, h1 := s.Connect(t, n1)
	s.Baseline(t, st1, n1, h1.GetSession())
	s.Sessions.SetDelay(time.Hour)
	st2, _ := s.Stream(t, n2.Cert)
	go func() { _, _ = gliwHello(st2, s.HelloMsg(n2)) }()
	_, _ = s.Stream(t, n3.Cert) // authenticated, waiting for a hello
	gliwEventually(t, 10*time.Second, "the second hello reached the store", func() bool { return s.Sessions.InFlight() == 1 })
	if during := gliwServerGoroutines(); during <= base {
		t.Fatalf("no server goroutine was found while streams were open (%d <= %d): the stack scan does not see the server", during, base)
	}
	if _, err := s.Stop(t); err != nil {
		t.Fatalf("Serve = %v, want nil", err)
	}
	gliwEventually(t, 2*time.Second, "server goroutines finished after Serve returned", func() bool {
		return gliwServerGoroutines() <= base
	})
}
