package kubeapi_test

// GKA-020 (public surface), GKA-021 (New validation), GKA-024 (sentinels),
// GKA-025 (field manager names), GKA-026 (LoadRESTConfig). None of these needs
// envtest: New performs no network I/O and LoadRESTConfig only reads files.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// ---------------------------------------------------------------------------
// GKA-020 / GKA-024 / GKA-025
// ---------------------------------------------------------------------------

func TestGKA020_PublicSurface(t *testing.T) {
	// Signatures (compile-time).
	var (
		_ func() controller.Clock                                  = controller.NewSystemClock
		_ func(controller.Options) (*controller.Controller, error) = controller.New
		_ func(string) (*rest.Config, error)                       = controller.LoadRESTConfig
		_ func(*controller.Controller, context.Context) error      = (*controller.Controller).Run
	)
	// Constants.
	if controller.DefaultResyncInterval != 30*time.Second || controller.MaxResyncInterval != 30*time.Second {
		t.Errorf("GKA-020: DefaultResyncInterval=%v MaxResyncInterval=%v, want 30s/30s", controller.DefaultResyncInterval, controller.MaxResyncInterval)
	}
	// Exact field sets of Options and LeaderElectionOptions.
	wantOpts := map[string]reflect.Type{
		"RESTConfig":     reflect.TypeOf((*rest.Config)(nil)),
		"Assessor":       reflect.TypeOf((*app.NodeAssessor)(nil)).Elem(),
		"Clock":          reflect.TypeOf((*controller.Clock)(nil)).Elem(),
		"ControllerID":   reflect.TypeOf(""),
		"ClusterID":      reflect.TypeOf(""),
		"ResyncInterval": reflect.TypeOf(time.Duration(0)),
		"LeaderElection": reflect.TypeOf(controller.LeaderElectionOptions{}),
		"Logger":         reflect.TypeOf((*slog.Logger)(nil)),
	}
	gkaRAssertFields(t, "controller.Options", reflect.TypeOf(controller.Options{}), wantOpts)
	wantLE := map[string]reflect.Type{
		"Namespace": reflect.TypeOf(""), "ID": reflect.TypeOf(""),
		"LeaseDuration": reflect.TypeOf(time.Duration(0)), "RenewDeadline": reflect.TypeOf(time.Duration(0)),
		"RetryPeriod": reflect.TypeOf(time.Duration(0)),
	}
	gkaRAssertFields(t, "controller.LeaderElectionOptions", reflect.TypeOf(controller.LeaderElectionOptions{}), wantLE)
}

func gkaRAssertFields(t *testing.T, what string, got reflect.Type, want map[string]reflect.Type) {
	t.Helper()
	if got.NumField() != len(want) {
		t.Errorf("GKA-020: %s has %d fields, want exactly %d", what, got.NumField(), len(want))
	}
	for name, typ := range want {
		f, ok := got.FieldByName(name)
		if !ok {
			t.Errorf("GKA-020: %s.%s missing", what, name)
			continue
		}
		if f.Type != typ {
			t.Errorf("GKA-020: %s.%s has type %v, want %v", what, name, f.Type, typ)
		}
	}
}

func TestGKA020_SystemClockIsWallClock(t *testing.T) {
	c := controller.NewSystemClock()
	if c == nil {
		t.Fatal("GKA-020: NewSystemClock returned nil")
	}
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("GKA-020: SystemClock.Now()=%s not within 1s of the wall clock [%s, %s]", got, before, after)
	}
	if again := c.Now(); again.Before(got.Add(-time.Second)) {
		t.Errorf("GKA-020: SystemClock went backwards: %s then %s", got, again)
	}
}

func TestGKA024_SentinelsAreDistinctAndTextsAreFixed(t *testing.T) {
	all := []struct {
		name string
		err  error
		text string
	}{
		{"ErrInvalidOptions", controller.ErrInvalidOptions, "controller: invalid options"},
		{"ErrLeadershipLost", controller.ErrLeadershipLost, "controller: leadership lost"},
		{"ErrAlreadyRun", controller.ErrAlreadyRun, "controller: already run"},
	}
	for i, a := range all {
		if a.err == nil || a.err.Error() != a.text {
			t.Errorf("GKA-024: %s = %v, want text %q", a.name, a.err, a.text)
		}
		for j, b := range all {
			if (i == j) != errors.Is(a.err, b.err) {
				t.Errorf("GKA-024: errors.Is(%s, %s) = %v", a.name, b.name, errors.Is(a.err, b.err))
			}
		}
	}
	// New wraps ErrInvalidOptions with %w and no other sentinel.
	_, err := controller.New(controller.Options{})
	if !errors.Is(err, controller.ErrInvalidOptions) || errors.Is(err, controller.ErrLeadershipLost) || errors.Is(err, controller.ErrAlreadyRun) {
		t.Errorf("GKA-024: New(Options{}) error %v must wrap exactly ErrInvalidOptions", err)
	}
}

func TestGKA025_FieldManagerNames(t *testing.T) {
	for got, want := range map[string]string{
		controller.FieldManagerFleetStatus:    "dpa-fleet-status",
		controller.FieldManagerDeviceStatus:   "dpa-device-status",
		controller.FieldManagerNodePathSpec:   "dpa-nodepath-spec",
		controller.FieldManagerNodePathStatus: "dpa-nodepath-status",
		controller.FieldManagerNodeCondition:  "dpa-node-condition",
	} {
		if got != want {
			t.Errorf("GKA-025: field manager constant = %q, want %q", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// GKA-021 New validation
// ---------------------------------------------------------------------------

func gkaRBaseOpts() controller.Options {
	return controller.Options{
		RESTConfig:   &rest.Config{Host: "https://127.0.0.1:1"},
		Assessor:     newGkaFakeAssessor(),
		ControllerID: "controller-1",
		ClusterID:    "cluster-1",
		LeaderElection: controller.LeaderElectionOptions{
			Namespace: "default",
		},
	}
}

func gkaRRepeat(ch string, n int) string { return strings.Repeat(ch, n) }

// gkaRSubdomain builds a valid DNS-1123 subdomain of exactly n bytes (63-byte labels).
func gkaRSubdomain(n int) string {
	var labels []string
	total := 0
	for total < n {
		room := n - total
		if len(labels) > 0 {
			room-- // separating dot
		}
		l := room
		if l > 63 {
			l = 63
		}
		labels = append(labels, gkaRRepeat("a", l))
		total = len(strings.Join(labels, "."))
	}
	return strings.Join(labels, ".")
}

func TestGKA021_NewValidationTable(t *testing.T) {
	id128 := gkaRRepeat("a", 128)
	id129 := gkaRRepeat("a", 129)
	le := func(mut func(*controller.LeaderElectionOptions)) func(*controller.Options) {
		return func(o *controller.Options) { mut(&o.LeaderElection) }
	}
	dur := func(lease, renew, retry time.Duration) func(*controller.Options) {
		return le(func(l *controller.LeaderElectionOptions) {
			l.LeaseDuration, l.RenewDeadline, l.RetryPeriod = lease, renew, retry
		})
	}
	cases := []struct {
		name  string
		mut   func(*controller.Options)
		valid bool
	}{
		{"baseline (all defaults)", func(*controller.Options) {}, true},
		{"nil RESTConfig", func(o *controller.Options) { o.RESTConfig = nil }, false},
		{"nil Assessor", func(o *controller.Options) { o.Assessor = nil }, false},
		{"nil Clock uses the system clock", func(o *controller.Options) { o.Clock = nil }, true},
		{"nil Logger uses slog default", func(o *controller.Options) { o.Logger = nil }, true},

		{"ClusterID empty", func(o *controller.Options) { o.ClusterID = "" }, false},
		{"ClusterID 128", func(o *controller.Options) { o.ClusterID = id128 }, true},
		{"ClusterID 129", func(o *controller.Options) { o.ClusterID = id129 }, false},
		{"ClusterID mixed allowed chars", func(o *controller.Options) { o.ClusterID = "A.b_c-9" }, true},
		{"ClusterID single dash", func(o *controller.Options) { o.ClusterID = "-" }, true},
		{"ClusterID space", func(o *controller.Options) { o.ClusterID = "a b" }, false},
		{"ClusterID slash", func(o *controller.Options) { o.ClusterID = "a/b" }, false},
		{"ClusterID colon", func(o *controller.Options) { o.ClusterID = "a:b" }, false},
		{"ClusterID non-ASCII", func(o *controller.Options) { o.ClusterID = "clusteré" }, false},
		{"ClusterID trailing newline", func(o *controller.Options) { o.ClusterID = "abc\n" }, false},
		{"ClusterID NUL", func(o *controller.Options) { o.ClusterID = "a\x00b" }, false},

		{"ControllerID empty", func(o *controller.Options) { o.ControllerID = "" }, false},
		{"ControllerID 128", func(o *controller.Options) { o.ControllerID = id128 }, true},
		{"ControllerID 129", func(o *controller.Options) { o.ControllerID = id129 }, false},
		{"ControllerID mixed allowed chars", func(o *controller.Options) { o.ControllerID = "Host.name_1-a" }, true},
		{"ControllerID space", func(o *controller.Options) { o.ControllerID = "a b" }, false},
		{"ControllerID plus", func(o *controller.Options) { o.ControllerID = "a+b" }, false},
		{"ControllerID trailing newline", func(o *controller.Options) { o.ControllerID = "abc\n" }, false},

		{"Resync zero means default", func(o *controller.Options) { o.ResyncInterval = 0 }, true},
		{"Resync 1ns", func(o *controller.Options) { o.ResyncInterval = 1 }, true},
		{"Resync 200ms", func(o *controller.Options) { o.ResyncInterval = 200 * time.Millisecond }, true},
		{"Resync exactly 30s", func(o *controller.Options) { o.ResyncInterval = 30 * time.Second }, true},
		{"Resync 30s+1ns", func(o *controller.Options) { o.ResyncInterval = 30*time.Second + 1 }, false},
		{"Resync 31s", func(o *controller.Options) { o.ResyncInterval = 31 * time.Second }, false},
		{"Resync -1ns", func(o *controller.Options) { o.ResyncInterval = -1 }, false},

		{"Namespace empty", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "" }), false},
		{"Namespace one char", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "a" }), true},
		{"Namespace digits only", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "123" }), true},
		{"Namespace 63", le(func(l *controller.LeaderElectionOptions) { l.Namespace = gkaRRepeat("a", 63) }), true},
		{"Namespace 64", le(func(l *controller.LeaderElectionOptions) { l.Namespace = gkaRRepeat("a", 64) }), false},
		{"Namespace uppercase", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "Default" }), false},
		{"Namespace with dot (label, not subdomain)", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "a.b" }), false},
		{"Namespace underscore", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "a_b" }), false},
		{"Namespace leading dash", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "-a" }), false},
		{"Namespace trailing dash", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "a-" }), false},
		{"Namespace inner dash", le(func(l *controller.LeaderElectionOptions) { l.Namespace = "kube-system" }), true},

		{"LeaseID empty means path-controller", le(func(l *controller.LeaderElectionOptions) { l.ID = "" }), true},
		{"LeaseID subdomain a.b", le(func(l *controller.LeaderElectionOptions) { l.ID = "a.b" }), true},
		{"LeaseID subdomain digits", le(func(l *controller.LeaderElectionOptions) { l.ID = "0.1" }), true},
		{"LeaseID 253", le(func(l *controller.LeaderElectionOptions) { l.ID = gkaRSubdomain(253) }), true},
		{"LeaseID 254", le(func(l *controller.LeaderElectionOptions) { l.ID = gkaRSubdomain(254) }), false},
		{"LeaseID label of 63", le(func(l *controller.LeaderElectionOptions) { l.ID = gkaRRepeat("a", 63) }), true},
		// A DNS-1123 subdomain (apimachinery IsDNS1123Subdomain) limits only the total length (253); labels have no 63 limit.
		{"LeaseID single label of 64 (valid: only the total is limited)", le(func(l *controller.LeaderElectionOptions) { l.ID = gkaRRepeat("a", 64) }), true},
		{"LeaseID single label of 253", le(func(l *controller.LeaderElectionOptions) { l.ID = gkaRRepeat("a", 253) }), true},
		{"LeaseID single label of 254", le(func(l *controller.LeaderElectionOptions) { l.ID = gkaRRepeat("a", 254) }), false},
		{"LeaseID 100-byte labels, 253 in total", le(func(l *controller.LeaderElectionOptions) {
			l.ID = gkaRRepeat("a", 100) + "." + gkaRRepeat("b", 100) + "." + gkaRRepeat("c", 51)
		}), true},
		{"LeaseID 100-byte labels, 254 in total", le(func(l *controller.LeaderElectionOptions) {
			l.ID = gkaRRepeat("a", 100) + "." + gkaRRepeat("b", 100) + "." + gkaRRepeat("c", 52)
		}), false},
		{"LeaseID label ending with a dash", le(func(l *controller.LeaderElectionOptions) { l.ID = "a-.b" }), false},
		{"LeaseID uppercase", le(func(l *controller.LeaderElectionOptions) { l.ID = "Path-Controller" }), false},
		{"LeaseID empty label", le(func(l *controller.LeaderElectionOptions) { l.ID = "a..b" }), false},
		{"LeaseID leading dot", le(func(l *controller.LeaderElectionOptions) { l.ID = ".a" }), false},
		{"LeaseID trailing dot", le(func(l *controller.LeaderElectionOptions) { l.ID = "a." }), false},
		{"LeaseID underscore", le(func(l *controller.LeaderElectionOptions) { l.ID = "a_b" }), false},
		{"LeaseID leading dash", le(func(l *controller.LeaderElectionOptions) { l.ID = "-a" }), false},
		{"LeaseID slash", le(func(l *controller.LeaderElectionOptions) { l.ID = "a/b" }), false},

		{"durations all defaults", dur(0, 0, 0), true},
		{"negative LeaseDuration", dur(-1, 3*time.Second, 500*time.Millisecond), false},
		{"negative RenewDeadline", dur(4*time.Second, -1, 500*time.Millisecond), false},
		{"negative RetryPeriod", dur(4*time.Second, 3*time.Second, -1), false},
		{"LeaseDuration 999ms (below 1s, no default applies)", dur(999*time.Millisecond, 500*time.Millisecond, 100*time.Millisecond), false},
		{"LeaseDuration 1s-1ns", dur(time.Second-1, 500*time.Millisecond, 100*time.Millisecond), false},
		{"LeaseDuration exactly 1s", dur(time.Second, 900*time.Millisecond, 500*time.Millisecond), true},
		{"LeaseDuration == RenewDeadline (strict)", dur(4*time.Second, 4*time.Second, 500*time.Millisecond), false},
		{"LeaseDuration = RenewDeadline+1ns", dur(3*time.Second+1, 3*time.Second, 500*time.Millisecond), true},
		{"LeaseDuration < RenewDeadline", dur(3*time.Second, 4*time.Second, 500*time.Millisecond), false},
		{"RenewDeadline == 1.2 x RetryPeriod (strict, 600ms/500ms)", dur(4*time.Second, 600*time.Millisecond, 500*time.Millisecond), false},
		{"RenewDeadline = 601ms, RetryPeriod 500ms", dur(4*time.Second, 601*time.Millisecond, 500*time.Millisecond), true},
		{"RenewDeadline == 1.2 x RetryPeriod (3s/2.5s)", dur(4*time.Second, 3*time.Second, 2500*time.Millisecond), false},
		{"RenewDeadline 3s+1ns over RetryPeriod 2.5s", dur(4*time.Second, 3*time.Second+1, 2500*time.Millisecond), true},
		{"RetryPeriod > RenewDeadline", dur(4*time.Second, 3*time.Second, 5*time.Second), false},
		{"defaults applied first: Lease 5s vs default Renew 10s", dur(5*time.Second, 0, 0), false},
		{"defaults applied first: default Lease 15s vs Renew 20s", dur(0, 20*time.Second, 0), false},
		{"defaults applied first: default Renew 10s vs Retry 9s", dur(0, 0, 9*time.Second), false},
		{"defaults applied first: Retry 8s keeps 10s > 9.6s", dur(0, 0, 8*time.Second), true},
		{"defaults applied first: Lease 20s alone", dur(20*time.Second, 0, 0), true},
		{"defaults applied first: Renew 14s under default Lease 15s", dur(0, 14*time.Second, 0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := gkaRBaseOpts()
			tc.mut(&opts)
			start := time.Now()
			ctl, err := controller.New(opts)
			if el := time.Since(start); el > 5*time.Second {
				t.Errorf("GKA-021: New took %s: it must not touch the network", el)
			}
			if tc.valid {
				if err != nil || ctl == nil {
					t.Fatalf("GKA-021: New = (%v, %v), want a controller and nil error", ctl, err)
				}
				return
			}
			if ctl != nil {
				t.Errorf("GKA-021: New returned a non-nil *Controller together with an error")
			}
			if !errors.Is(err, controller.ErrInvalidOptions) {
				t.Fatalf("GKA-021/024: err = %v, want errors.Is(err, ErrInvalidOptions)", err)
			}
			if errors.Is(err, controller.ErrLeadershipLost) || errors.Is(err, controller.ErrAlreadyRun) {
				t.Errorf("GKA-024: New error also matches another sentinel: %v", err)
			}
		})
	}
}

// New must give the same verdict for a config whose port is closed and for a
// listener that would count connections, and must not open any connection.
func TestGKA021_NewMakesNoNetworkCalls(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var conns atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			_ = c.Close()
		}
	}()
	defer func() {
		_ = ln.Close()
		<-done
	}()

	valid := gkaRBaseOpts()
	valid.RESTConfig = &rest.Config{Host: "http://" + ln.Addr().String()}
	invalid := valid
	invalid.ClusterID = ""
	for i := 0; i < 3; i++ {
		if ctl, err := controller.New(valid); err != nil || ctl == nil {
			t.Fatalf("GKA-021: New(valid) = (%v, %v)", ctl, err)
		}
		if _, err := controller.New(invalid); !errors.Is(err, controller.ErrInvalidOptions) {
			t.Fatalf("GKA-021: New(invalid) err = %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := conns.Load(); n != 0 {
		t.Errorf("GKA-021: New opened %d connection(s); it must perform no network I/O", n)
	}

	// Closed port: the verdict is identical.
	closed := gkaRBaseOpts()
	closed.RESTConfig = &rest.Config{Host: "https://127.0.0.1:1"}
	if ctl, err := controller.New(closed); err != nil || ctl == nil {
		t.Errorf("GKA-021: closed-port New = (%v, %v), want success", ctl, err)
	}
}

// The caller's RESTConfig is copied: New and a short Run leave it untouched, and
// zero QPS/Burst/Timeout stay zero in the caller's copy (defaults go to the copy).
func TestGKA021_CallerRESTConfigIsNotMutated(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:1", BearerToken: "not-a-secret", UserAgent: "gka-test"}
	before := rest.CopyConfig(cfg)
	opts := gkaRBaseOpts()
	opts.RESTConfig = cfg
	ctl, err := controller.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !reflect.DeepEqual(before, cfg) || cfg.QPS != 0 || cfg.Burst != 0 || cfg.Timeout != 0 {
		t.Errorf("GKA-021: New changed the caller's RESTConfig: %+v", cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ctl.Run(ctx) }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after cancel = %v, want nil", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !reflect.DeepEqual(before, cfg) || cfg.QPS != 0 || cfg.Burst != 0 || cfg.Timeout != 0 {
		t.Errorf("GKA-021: Run changed the caller's RESTConfig: %+v", cfg)
	}
}

// A non-zero caller Timeout is honoured and a zero Timeout gets a large default:
// a server that never answers sees the client give up after about 400 ms in the
// first case and not within 2.5 s in the second.
func TestGKA021_CallerTimeoutIsRespected(t *testing.T) {
	observe := func(timeout time.Duration, wait time.Duration) (cancelled bool, after time.Duration) {
		var firstNS, cancelNS atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			firstNS.CompareAndSwap(0, time.Now().UnixNano())
			select {
			case <-r.Context().Done():
				cancelNS.CompareAndSwap(0, time.Now().UnixNano())
			case <-time.After(8 * time.Second):
			}
		}))
		defer srv.Close()
		opts := gkaRBaseOpts()
		opts.RESTConfig = &rest.Config{Host: srv.URL, Timeout: timeout}
		ctl, err := controller.New(opts)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- ctl.Run(ctx) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("Run did not return after cancel")
			}
		}()
		gkaEventually(t, 10*time.Second, func() (bool, string) { return firstNS.Load() != 0, "no request reached the hanging server" })
		time.Sleep(wait)
		if c := cancelNS.Load(); c != 0 {
			return true, time.Duration(c - firstNS.Load())
		}
		return false, 0
	}
	if ok, after := observe(400*time.Millisecond, 3*time.Second); !ok || after < 300*time.Millisecond || after > 3*time.Second {
		t.Errorf("GKA-021: caller Timeout 400ms: request cancelled=%v after %s, want cancelled after about 400ms", ok, after)
	}
	if ok, after := observe(0, 2500*time.Millisecond); ok {
		t.Errorf("GKA-021/026: zero Timeout must default to a large value (30s), but the request was abandoned after %s", after)
	}
}

// ---------------------------------------------------------------------------
// GKA-026 LoadRESTConfig
// ---------------------------------------------------------------------------

func gkaRKubeconfig(server, token, extraUser string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c1
  cluster:
    server: %s
    insecure-skip-tls-verify: true
users:
- name: u1
  user:
    token: %s
%s
contexts:
- name: ctx1
  context: {cluster: c1, user: u1}
current-context: ctx1
`, server, token, extraUser)
}

func gkaRWriteFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func gkaRIsPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func TestGKA026_LoadRESTConfigFromKubeconfigFillsDefaults(t *testing.T) {
	marker := "GKA-SECRET-" + gkaRHash(t.Name(), 24)
	path := gkaRWriteFile(t, filepath.Join(t.TempDir(), "kubeconfig"), gkaRKubeconfig("https://127.0.0.1:1", marker, ""))
	cfg, err := controller.LoadRESTConfig(path)
	if err != nil || cfg == nil {
		t.Fatalf("GKA-026: LoadRESTConfig(valid) = (%v, %v)", cfg, err)
	}
	if cfg.Host != "https://127.0.0.1:1" || cfg.BearerToken != marker {
		t.Errorf("GKA-026: Host=%q token-matches=%v, want the kubeconfig values", cfg.Host, cfg.BearerToken == marker)
	}
	if cfg.QPS != 50 || cfg.Burst != 100 || cfg.Timeout != 30*time.Second {
		t.Errorf("GKA-026: QPS=%v Burst=%v Timeout=%v, want 50/100/30s defaults", cfg.QPS, cfg.Burst, cfg.Timeout)
	}
}

func TestGKA026_LoadRESTConfigUsesCurrentContext(t *testing.T) {
	content := `apiVersion: v1
kind: Config
clusters:
- name: a
  cluster: {server: "https://127.0.0.1:11", insecure-skip-tls-verify: true}
- name: b
  cluster: {server: "https://127.0.0.1:22", insecure-skip-tls-verify: true}
users:
- name: u
  user: {token: tok}
contexts:
- name: ca
  context: {cluster: a, user: u}
- name: cb
  context: {cluster: b, user: u}
current-context: cb
`
	cfg, err := controller.LoadRESTConfig(gkaRWriteFile(t, filepath.Join(t.TempDir(), "kc"), content))
	if err != nil || cfg == nil {
		t.Fatalf("LoadRESTConfig = (%v, %v)", cfg, err)
	}
	if cfg.Host != "https://127.0.0.1:22" {
		t.Errorf("GKA-026: Host = %q, want the current-context cluster", cfg.Host)
	}
}

func TestGKA026_LoadRESTConfigFailuresDoNotLeakSecrets(t *testing.T) {
	marker := "GKA-SECRET-" + gkaRHash(t.Name(), 24)
	dir := t.TempDir()
	file := func(name, content string) string { return gkaRWriteFile(t, filepath.Join(dir, name), content) }
	cases := []struct {
		name string
		path string
	}{
		{"missing file", filepath.Join(dir, "does-not-exist-"+marker)},
		{"malformed yaml carrying the marker", file("broken-"+marker, "apiVersion: v1\nusers:\n- name: u\n  user:\n    token: "+marker+"\n\t- : [unterminated\n")},
		{"dangling current-context", file("dangling", strings.Replace(gkaRKubeconfig("https://127.0.0.1:1", marker, ""), "current-context: ctx1", "current-context: nope-"+marker, 1))},
		{"invalid certificate data", file("badcert", gkaRKubeconfig("https://127.0.0.1:1", marker, "    client-certificate-data: "+marker+"\n    client-key-data: "+marker))},
		{"empty file", file("empty", "")},
		{"a directory", dir},
	}
	fixed := ""
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := controller.LoadRESTConfig(tc.path)
			if err == nil {
				t.Fatalf("GKA-026: LoadRESTConfig(%s) succeeded, want a failure", tc.name)
			}
			if cfg != nil {
				t.Errorf("GKA-026: failure returned a non-nil config")
			}
			base := filepath.Base(tc.path)
			for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%q", err)} {
				if strings.Contains(rendered, marker) || strings.Contains(rendered, dir) || (len(base) >= 8 && strings.Contains(rendered, base)) {
					t.Errorf("GKA-026/163: error text leaks the token, the file path or content: %q", rendered)
				}
			}
			if msg := err.Error(); msg == "" || !gkaRIsPrintableASCII(msg) {
				t.Errorf("GKA-026: Error() = %q, want a non-empty fixed ASCII phrase", msg)
			}
			if errors.Unwrap(err) == nil {
				t.Errorf("GKA-026: the cause must be reachable through Unwrap()")
			}
			if tc.name == "a directory" {
				return
			}
			if fixed == "" {
				fixed = err.Error()
			} else if err.Error() != fixed {
				t.Errorf("GKA-026: Error() = %q differs from the fixed phrase %q used for other failures", err.Error(), fixed)
			}
		})
	}
}

func TestGKA026_InClusterFailureIsFixedPhrase(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	cfg, err := controller.LoadRESTConfig("")
	if err == nil || cfg != nil {
		t.Fatalf("GKA-026: LoadRESTConfig(\"\") outside a cluster = (%v, %v), want an error", cfg, err)
	}
	if msg := err.Error(); msg == "" || !gkaRIsPrintableASCII(msg) {
		t.Errorf("GKA-026: Error() = %q, want a non-empty ASCII phrase", msg)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("GKA-026: the cause must be reachable through Unwrap()")
	}
}
