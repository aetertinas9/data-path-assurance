package liveingestenv_test

// The leader gate and its write discipline (GLI-092, GLI-030 (2), GLI-037) against
// a real API server: Leading() follows the Lease holder (fresh, released, expired,
// absent time fields), an error blip keeps it and sustained errors revoke it only
// after StaleAfter, the Lease poll does not share a client with the hello path,
// hellos are refused (Unavailable, no API write, no node or NodePathState read)
// while it is false, and cancelling Serve ends pending hellos and starts no new
// request. Scenario group GLI-124 (g).

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// glieSetLease rewrites a Lease: holder nil clears holderIdentity, renew nil
// removes renewTime, dur nil removes leaseDurationSeconds.
func glieSetLease(t testing.TB, e *glieEnvT, name string, holder *string, renew *time.Time, dur *int32) {
	t.Helper()
	glieEventually(t, 10*time.Second, func() (bool, string) {
		l, err := e.Clientset.CoordinationV1().Leases(glieNS).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		l.Spec.HolderIdentity = holder
		l.Spec.RenewTime = nil
		if renew != nil {
			mt := metav1.NewMicroTime(*renew)
			l.Spec.RenewTime = &mt
		}
		l.Spec.LeaseDurationSeconds = dur
		_, err = e.Clientset.CoordinationV1().Leases(glieNS).Update(context.Background(), l, metav1.UpdateOptions{})
		return err == nil, "update Lease: " + errString(err)
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func glieNowPtr() *time.Time { n := time.Now(); return &n }

// GLI-092 (i)/(iii): Leading() is true exactly while the Lease names the
// controller ID as holder and is not expired; a changed, emptied, absent or
// expired holder turns it false within a poll interval plus a margin and a
// restored Lease turns it true again. A Lease without renewTime and duration
// never expires.
func TestGLI092_LeaderGateFollowsTheLeaseHolder(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	other, empty := "another-controller", ""
	d3600, d10 := int32(3600), int32(10)
	hourAgo, tenAgo := time.Now().Add(-time.Hour), time.Now().Add(-10*time.Second)
	for _, st := range []struct {
		name   string
		holder *string
		renew  *time.Time
		dur    *int32
		want   bool
	}{
		{"held by the controller id", &s.CtlID, glieNowPtr(), &d3600, true},
		{"held by another controller", &other, glieNowPtr(), &d3600, false},
		{"held by the controller id again", &s.CtlID, glieNowPtr(), &d3600, true},
		{"released with an empty holder", &empty, glieNowPtr(), &d3600, false},
		{"held by the controller id once more", &s.CtlID, glieNowPtr(), &d3600, true},
		{"released with no holder at all", nil, glieNowPtr(), &d3600, false},
		{"held, without renewTime and duration (never expires)", &s.CtlID, nil, nil, true},
		{"held but expired (renewed an hour ago for ten seconds)", &s.CtlID, &hourAgo, &d10, false},
		{"held and renewed again", &s.CtlID, glieNowPtr(), &d3600, true},
		{"held, renewal plus duration reached exactly (ten seconds ago for ten seconds)", &s.CtlID, &tenAgo, &d10, false},
		{"held and renewed a last time", &s.CtlID, glieNowPtr(), &d3600, true},
	} {
		glieSetLease(t, e, s.Lease, st.holder, st.renew, st.dur)
		start := time.Now()
		glieEventually(t, 5*time.Second, func() (bool, string) {
			return s.Ad.Leading() == st.want, st.name + ": Leading() is " + map[bool]string{true: "true", false: "false"}[s.Ad.Leading()]
		})
		if el := time.Since(start); el > 3*time.Second {
			t.Errorf("GLI-092: %q took %v to be observed, want about a poll interval (100 ms)", st.name, el)
		}
	}
}

// GLI-092 (ii)(iii): a Lease read that fails (500, 429, 503, a transport error) keeps
// the previous value; only when the last successful read is older than StaleAfter
// is Leading() false. One blip never revokes it; sustained errors revoke it after
// about StaleAfter, not before; the next successful read restores it.
func TestGLI092_ErrorBlipsKeepLeadingAndSustainedErrorsRevokeAfterStaleAfter(t *testing.T) {
	e := glieEnv(t)
	const poll, stale = 100 * time.Millisecond, 1500 * time.Millisecond
	s := glieNewSessionEnv(t, e, func(o *glieAdapterOpts) { o.PollInterval, o.StaleAfter = poll, stale })
	isLeaseGet := func(r *http.Request) bool {
		return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/leases/"+s.Lease)
	}
	for _, b := range []struct {
		name string
		resp func(r *http.Request) *http.Response
		errf func(r *http.Request) error
	}{
		{name: "500", resp: func(r *http.Request) *http.Response {
			return glieStatusResponse(r, 500, metav1.StatusReasonInternalError, "blip")
		}},
		{name: "429", resp: func(r *http.Request) *http.Response {
			return glieStatusResponse(r, 429, metav1.StatusReasonTooManyRequests, "blip")
		}},
		{name: "503", resp: func(r *http.Request) *http.Response {
			return glieStatusResponse(r, 503, metav1.StatusReasonServiceUnavailable, "blip")
		}},
		{name: "transport error", errf: func(r *http.Request) error { return errors.New("injected connection reset") }},
	} {
		var fired atomic.Bool
		if b.errf != nil {
			s.Ad.Rec.InjectErr(func(r *http.Request) error {
				if isLeaseGet(r) && fired.CompareAndSwap(false, true) {
					return b.errf(r)
				}
				return nil
			})
		} else {
			s.Ad.Rec.Inject(func(r *http.Request) *http.Response {
				if isLeaseGet(r) && fired.CompareAndSwap(false, true) {
					return b.resp(r)
				}
				return nil
			})
		}
		glieEventually(t, 5*time.Second, func() (bool, string) { return fired.Load(), "the " + b.name + " blip was never injected" })
		end := time.Now().Add(8 * poll)
		for time.Now().Before(end) {
			if !s.Ad.Leading() {
				t.Fatalf("GLI-092 (ii): one %s blip on the Lease read turned Leading() false", b.name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	s.Ad.Rec.Inject(nil)
	s.Ad.Rec.InjectErr(nil)

	var sustained atomic.Bool
	s.Ad.Rec.Inject(func(r *http.Request) *http.Response {
		if sustained.Load() && isLeaseGet(r) {
			return glieStatusResponse(r, 500, metav1.StatusReasonInternalError, "sustained")
		}
		return nil
	})
	t0 := time.Now()
	sustained.Store(true)
	time.Sleep(stale * 6 / 10)
	if !s.Ad.Leading() {
		t.Fatalf("GLI-092 (ii): Leading() was false %v into the errors, before StaleAfter %v", time.Since(t0), stale)
	}
	var tFalse time.Duration
	glieEventually(t, 10*time.Second, func() (bool, string) {
		if !s.Ad.Leading() {
			tFalse = time.Since(t0)
			return true, ""
		}
		return false, "Leading() is still true under sustained Lease read errors"
	})
	if tFalse < stale-poll-150*time.Millisecond {
		t.Errorf("GLI-092 (ii): Leading() turned false after %v of errors, before StaleAfter %v", tFalse, stale)
	}
	if tFalse > stale+poll+3*time.Second {
		t.Errorf("GLI-092 (ii): Leading() turned false only after %v, want about StaleAfter %v", tFalse, stale)
	}
	sustained.Store(false)
	s.Ad.WaitLeading(t, true, 5*time.Second)
	if v := s.Ad.Rec.Violations(); len(v) > 0 {
		t.Errorf("harness: %v", v)
	}
}

// GLI-092: the Lease poll uses its own client with its own rate limiter, so a burst
// of hello-path requests (here: sixteen concurrent AllocateSession calls for sixteen
// nodes behind a 2 QPS limiter) cannot starve it: Leading() stays true and the Lease keeps being read.
func TestGLI092_LeasePollIsNotStarvedByHelloTraffic(t *testing.T) {
	e := glieEnv(t)
	base := rest.CopyConfig(e.Config)
	base.QPS, base.Burst = 2, 2
	s := glieNewSessionEnv(t, e, func(o *glieAdapterOpts) {
		o.Base, o.PollInterval, o.StaleAfter = base, 200*time.Millisecond, 3*time.Second
	})
	leaseGets := func() int {
		n := 0
		for _, en := range s.Ad.Rec.Entries() {
			if en.Method == http.MethodGet && strings.Contains(en.Path, "/leases/"+s.Lease) {
				n++
			}
		}
		return n
	}
	// sixteen different nodes: calls for one node are serialized by the adapter, so only different
	// nodes put several requests in flight at the 2 QPS limiter of the hello client.
	nodes := make([]*corev1.Node, 16)
	for i := range nodes {
		nodes[i] = glieNode(t, e, glieName(t, "starve-"+string(rune('a'+i))), nil)
		glieNodePathState(t, e, nodes[i])
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(n *corev1.Node) {
			defer wg.Done()
			_, _ = s.Ad.Alloc(ctx, n, 0)
		}(nodes[i])
	}
	t.Cleanup(func() { cancel(); wg.Wait() })
	before := leaseGets()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if !s.Ad.Leading() {
			t.Fatalf("GLI-092: Leading() turned false while hello-path requests were queued: the Lease poll shares a client or rate limiter with them")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := leaseGets() - before; n < 4 {
		t.Errorf("GLI-092: only %d Lease read(s) in 5 s under hello traffic, want the poll to keep its own pace (about 2 per second at the 2 QPS limit)", n)
	}
}

// GLI-092/GLI-030 (2): hello is answered Unavailable, before anything else, while
// the gate is false - no Node or NodePathState read, no write - both for a replica
// that never was leader and right after the Lease holder changed; once the gate is
// true again the hello succeeds and the one status PUT of a conflict-free hello is
// exactly one.
func TestGLI092_HelloIsRefusedWhileNotLeadingWithoutAnyAPIAccess(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	pki := glieNewPKI(t)
	srv := glieStartServer(t, s.Ad, pki, nil)
	leaf := pki.Node(t, glieCluster, string(s.Node.UID))
	hello := glieClientHello(s.Node)
	nonLease := func() int { return len(s.Ad.Rec.Since(time.Time{}, true)) }
	other := "another-controller"

	// not the leader from the start.
	glieSetLeaseHolder(t, e, s.Lease, &other)
	s.Ad.WaitLeading(t, false, 10*time.Second)
	before := nonLease()
	_, closeStream, err := glieHello(t, srv, leaf, hello)
	closeStream()
	if status.Code(err) != codes.Unavailable {
		t.Errorf("GLI-092/030 (2): hello to a non-leader = %v, want gRPC code Unavailable", err)
	}
	if n := nonLease() - before; n != 0 {
		t.Errorf("GLI-092/030: %d Node or NodePathState request(s) were made for a hello that the leader gate refused (the gate comes first)", n)
	}
	if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
		t.Errorf("GLI-092: %d status PUT(s) from a non-leader", n)
	}

	// the leader: one conflict-free hello is exactly one PUT.
	glieSetLeaseHolder(t, e, s.Lease, &s.CtlID)
	s.Ad.WaitLeading(t, true, 10*time.Second)
	sh, closeStream, err := glieHello(t, srv, leaf, hello)
	if err != nil || sh.GetSession() < 1 {
		t.Fatalf("GLI-030: hello to the leader = %v, %v, want a ServerHello with a positive session", sh, err)
	}
	if sh.GetCollectorProfileId() != "live:glie" {
		t.Errorf("GLI-030 (10): collector_profile_id = %q, want the configured live:glie", sh.GetCollectorProfileId())
	}
	closeStream()
	if got := s.stored(t); got == nil || *got != sh.GetSession() {
		t.Errorf("GLI-030/032: stored collectorSession = %v, want the ServerHello session %d (persisted before the hello answers)", got, sh.GetSession())
	}
	if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 1 {
		t.Errorf("GLI-124 (g)(5): a conflict-free hello made %d status PUT(s), want exactly 1", n)
	}

	// the gate turns false: the next hello is refused at once and nothing is written.
	glieSetLeaseHolder(t, e, s.Lease, &other)
	s.Ad.WaitLeading(t, false, 10*time.Second)
	putsBefore, reqBefore := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)), nonLease()
	_, closeStream, err = glieHello(t, srv, leaf, hello)
	closeStream()
	if status.Code(err) != codes.Unavailable {
		t.Errorf("GLI-092: hello right after the gate turned false = %v, want Unavailable", err)
	}
	if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)) - putsBefore; n != 0 {
		t.Errorf("GLI-092: %d NodePathState PUT(s) right after the leader toggled, want 0", n)
	}
	if n := nonLease() - reqBefore; n != 0 {
		t.Errorf("GLI-092: %d Node or NodePathState request(s) for a refused hello after the toggle", n)
	}
	glieSetLeaseHolder(t, e, s.Lease, &s.CtlID)
	s.Ad.WaitLeading(t, true, 10*time.Second)
	if sh2, closeStream, err := glieHello(t, srv, leaf, hello); err != nil || sh2.GetSession() <= sh.GetSession() {
		t.Errorf("GLI-030/031: hello after the gate recovered = %v, %v, want a session greater than %d", sh2, err, sh.GetSession())
	} else {
		closeStream()
	}
}

// GLI-092 (2)(3)/GLI-111: cancelling Serve while a hello waits for the API server
// ends that hello (the client sees Unavailable), makes Serve return, closes the
// listener, and no new API request enters the transport more than 100 ms after the
// cancellation (Lease polls excepted: they belong to RunLeaderPoll).
func TestGLI092_ServeCancelEndsPendingHellosAndStartsNoRequest(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	pki := glieNewPKI(t)
	srv := glieStartServer(t, s.Ad, pki, nil)
	leaf := pki.Node(t, glieCluster, string(s.Node.UID))
	entered := make(chan struct{})
	var once sync.Once
	s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodes/"+s.Node.Name) {
			once.Do(func() { close(entered) })
			<-req.Context().Done()
		}
		return nil
	})
	type result struct {
		err error
	}
	got := make(chan result, 1)
	go func() {
		_, closeStream, err := glieHello(t, srv, leaf, glieClientHello(s.Node))
		closeStream()
		got <- result{err}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the hello never reached the node directory")
	}
	cancelledAt := time.Now()
	if err := srv.Stop(t); err != nil {
		t.Errorf("GLI-111: Serve returned %v after its context was cancelled, want nil", err)
	}
	if el := time.Since(cancelledAt); el > 7*time.Second {
		t.Errorf("GLI-111/091: Serve took %v to return, want it to end within the 5 s graceful stop plus the forced stop", el)
	}
	select {
	case r := <-got:
		if status.Code(r.err) != codes.Unavailable {
			t.Errorf("GLI-100: the pending hello ended with %v, want Unavailable (server shutdown)", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the pending hello was not ended by the cancellation of Serve (GLI-092 (3): the hello's context is cancelled)")
	}
	time.Sleep(1200 * time.Millisecond)
	if late := s.Ad.Rec.Since(cancelledAt.Add(100*time.Millisecond), true); len(late) > 0 {
		var names []string
		for _, l := range late {
			names = append(names, l.String())
		}
		t.Errorf("GLI-092 (2)/GKA-175: %d request(s) entered the transport more than 100 ms after Serve was cancelled: %v", len(late), names)
	}
	if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
		t.Errorf("GLI-092 (3): %d status PUT(s) for a hello that was cancelled", n)
	}
	if c, err := net.DialTimeout("tcp", srv.Addr, time.Second); err == nil {
		_ = c.Close()
		t.Errorf("GLI-111: the listener still accepts connections after Serve returned")
	}
}
