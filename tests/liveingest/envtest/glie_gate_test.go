package liveingestenv_test

// Defaults and wiring of the leader gate and the session store that each rest on
// one explicit sentence of the specification (GLI-092, GLI-113, GLI-032 (c)(e)):
// the first poll starts with the RunLeaderPoll call, PollInterval 0 is 2 s,
// StaleAfter 0 is 3 x PollInterval, Serve's cancellation stops every request that
// depends on a pending hello, at most 16 AllocateSession calls run at once, and
// the conflict back-off adds at most 50% jitter. Every assertion is written so
// that a change of the sentence it checks fails it; time assertions leave at least
// 2 s of headroom for -race load.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/ingestadapter"
)

// glieLeaseGETs returns the recorded reads of one Lease.
func glieLeaseGETs(rec *glieRecorder, lease string) []glieRecEntry {
	var out []glieRecEntry
	for _, e := range rec.Entries() {
		if e.Method == http.MethodGet && !e.Watch && strings.Contains(e.Path, "/leases/"+lease) {
			out = append(out, e)
		}
	}
	return out
}

// glieMeasureStaleFlip injects sustained Lease read errors right after a poll
// that succeeded and returns the time from that last successful read to the moment
// Leading() turned false.
func glieMeasureStaleFlip(t *testing.T, s *glieSessionEnv, limit time.Duration) time.Duration {
	t.Helper()
	var sustained atomic.Bool
	s.Ad.Rec.Inject(func(r *http.Request) *http.Response {
		if sustained.Load() && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/leases/"+s.Lease) {
			return glieStatusResponse(r, 500, metav1.StatusReasonInternalError, "sustained")
		}
		return nil
	})
	n0 := len(glieLeaseGETs(s.Ad.Rec, s.Lease))
	glieEventually(t, 10*time.Second, func() (bool, string) {
		return len(glieLeaseGETs(s.Ad.Rec, s.Lease)) > n0, "waiting for the next Lease poll"
	})
	gets := glieLeaseGETs(s.Ad.Rec, s.Lease)
	lastOK := gets[len(gets)-1].At // this read passed: injection starts after it
	sustained.Store(true)
	if !s.Ad.Leading() {
		t.Fatalf("fixture: Leading() was already false before the errors started")
	}
	deadline := time.Now().Add(limit)
	for s.Ad.Leading() {
		if time.Now().After(deadline) {
			t.Fatalf("Leading() is still true %v after the last successful Lease read under sustained errors", time.Since(lastOK))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(lastOK)
}

// GLI-092/GLI-113: the first poll of RunLeaderPoll starts at once, not after the
// first PollInterval: with a 30 s interval and a Lease held by the controller ID the
// gate is true within a few seconds, and exactly one Lease read happened by then.
func TestGLI092_FirstPollStartsWithTheRunLeaderPollCall(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, func(o *glieAdapterOpts) {
		o.PollInterval, o.StaleAfter, o.NoPoll = 30*time.Second, 90*time.Second, true
	})
	if s.Ad.Leading() {
		t.Fatalf("GLI-092 (iii): Leading() is true before RunLeaderPoll ran")
	}
	start := time.Now()
	s.Ad.StartPoll(t)
	var took time.Duration
	glieEventually(t, 8*time.Second, func() (bool, string) {
		if s.Ad.Leading() {
			took = time.Since(start)
			return true, ""
		}
		return false, "Leading() is still false (PollInterval is 30 s: the first poll must not wait for it)"
	})
	if took > 6*time.Second {
		t.Errorf("GLI-092: the gate became true after %v with PollInterval 30 s, want the first poll to start with the RunLeaderPoll call", took)
	}
	if n := len(glieLeaseGETs(s.Ad.Rec, s.Lease)); n != 1 {
		t.Errorf("GLI-092: %d Lease read(s) in the first seconds of a 30 s interval, want exactly the one immediate first poll", n)
	}
}

// GLI-113: PollInterval 0 is 2 s (the Lease is read about every 2 s, the first read
// at once) and StaleAfter 0 is 3 x PollInterval (sustained read errors turn the gate
// false about 6 s after the last successful read, not much earlier and not much
// later); with another PollInterval the default StaleAfter follows it.
func TestGLI113_PollIntervalAndStaleAfterDefaults(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time scenario of about 20 s (skipped with -short)")
	}
	e := glieEnv(t)
	zero := func(o *glieAdapterOpts) {
		o.Mutate = func(op *ingestadapter.Options) { op.PollInterval, op.StaleAfter = 0, 0 }
	}
	t.Run("default_poll_interval_and_stale_after", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, zero)
		time.Sleep(7 * time.Second)
		gets := glieLeaseGETs(s.Ad.Rec, s.Lease)
		if len(gets) < 3 || len(gets) > 5 {
			t.Errorf("GLI-113: %d Lease reads in 7 s with PollInterval 0, want 3..5 (a 2 s interval, the first read at once)", len(gets))
		}
		for i := 1; i < len(gets); i++ {
			if gap := gets[i].At.Sub(gets[i-1].At); gap < 1500*time.Millisecond || gap > 4*time.Second {
				t.Errorf("GLI-113: the gap between Lease reads %d and %d is %v, want about the default 2 s", i, i+1, gap)
			}
		}
		flip := glieMeasureStaleFlip(t, s, 15*time.Second)
		if flip < 5500*time.Millisecond || flip > 10*time.Second {
			t.Errorf("GLI-113: Leading() turned false %v after the last successful read with StaleAfter 0, want about 3 x PollInterval = 6 s (between 5.5 s and 10 s)", flip)
		}
	})
	t.Run("default_stale_after_follows_the_poll_interval", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, func(o *glieAdapterOpts) {
			o.Mutate = func(op *ingestadapter.Options) { op.PollInterval, op.StaleAfter = 300*time.Millisecond, 0 }
		})
		flip := glieMeasureStaleFlip(t, s, 15*time.Second)
		if flip < 800*time.Millisecond || flip > 3200*time.Millisecond {
			t.Errorf("GLI-113: Leading() turned false %v after the last successful read with PollInterval 300 ms and StaleAfter 0, want about 3 x 300 ms = 0.9 s (between 0.8 s and 3.2 s)", flip)
		}
	})
}

// GLI-092 (3): Serve's cancellation cancels the context of a pending hello, so a
// hello that was waiting for the Node read when Serve was cancelled starts no
// NodePathState request when that read finally returns. The Node read is held in the
// transport regardless of the request context and released after the cancellation;
// 100 ms after the cancel call there is no NodePathState GET or PUT, no session was
// stored, and the client sees Unavailable.
func TestGLI092_ServeCancelStopsRequestsOfAHelloWhoseNodeReadIsStillPending(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	pki := glieNewPKI(t)
	srv := glieStartServer(t, s.Ad, pki, nil)
	leaf := pki.Node(t, glieCluster, string(s.Node.UID))
	entered, release := make(chan struct{}), make(chan struct{})
	var once, relOnce sync.Once
	t.Cleanup(func() { relOnce.Do(func() { close(release) }) })
	s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/nodes/"+s.Node.Name) {
			return nil
		}
		first := false
		once.Do(func() {
			first = true
			close(entered)
		})
		if !first {
			return nil
		}
		<-release // held whatever happens to the request context
		node, err := e.Clientset.CoreV1().Nodes().Get(context.Background(), s.Node.Name, metav1.GetOptions{})
		if err != nil {
			return glieStatusResponse(req, 500, metav1.StatusReasonInternalError, "fixture: cannot read the node")
		}
		node.TypeMeta = metav1.TypeMeta{Kind: "Node", APIVersion: "v1"}
		body, _ := json.Marshal(node)
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h,
			Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: req,
		}
	})
	got := make(chan error, 1)
	go func() {
		_, closeStream, err := glieHello(t, srv, leaf, glieClientHello(s.Node))
		closeStream()
		got <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the hello never reached the node directory")
	}
	srv.cancel() // the cancel call returns at once; what follows is measured from here
	cancelledAt := time.Now()
	time.Sleep(300 * time.Millisecond)
	relOnce.Do(func() { close(release) })
	if err := srv.Stop(t); err != nil {
		t.Errorf("GLI-111: Serve returned %v, want nil", err)
	}
	select {
	case err := <-got:
		if status.Code(err) != codes.Unavailable {
			t.Errorf("GLI-100: the pending hello ended with %v, want Unavailable (server shutdown)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the pending hello did not end")
	}
	time.Sleep(1200 * time.Millisecond)
	var late []string
	for _, en := range s.Ad.Rec.Since(cancelledAt.Add(100*time.Millisecond), true) {
		if strings.Contains(en.Path, "/nodepathstates/") {
			late = append(late, en.String())
		}
	}
	if len(late) > 0 {
		t.Errorf("GLI-092 (2)(3): %d NodePathState request(s) entered the transport more than 100 ms after Serve was cancelled: %v (the hello's context must be cancelled with Serve's)", len(late), late)
	}
	if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
		t.Errorf("GLI-092 (3): %d status PUT(s) for a hello that Serve's cancellation ended", n)
	}
	if got := s.stored(t); got != nil {
		t.Errorf("GLI-092 (3): collectorSession %d was stored for a cancelled hello", *got)
	}
}

// GLI-032 (e): the adapter runs at most 16 AllocateSession calls at once (calls for
// different nodes run in parallel, the 17th waits). With twenty nodes and every
// NodePathState request delayed in the transport, the number of NodePathState
// requests in flight reaches 16 and never exceeds it; every call still succeeds.
func TestGLI032_AtMostSixteenCallsRunAtOnce(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	const calls = 20
	nodes := []*corev1.Node{s.Node}
	for i := 1; i < calls; i++ {
		n := glieNode(t, e, glieName(t, "cap-"+string(rune('a'+i))), nil)
		glieNodePathState(t, e, n)
		nodes = append(nodes, n)
	}
	var inflight, peak atomic.Int64
	s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if (req.Method == http.MethodGet && req.URL.Query().Get("watch") != "") || !strings.Contains(req.URL.Path, "/nodepathstates/") {
			return nil
		}
		cur := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		select {
		case <-time.After(700 * time.Millisecond):
		case <-req.Context().Done():
		}
		return nil
	})
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, errs[i] = s.Ad.Alloc(ctx, nodes[i], 0)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("GLI-032 (e): call %d failed: %v (calls beyond the limit wait, they do not fail)", i, err)
		}
	}
	if got := peak.Load(); got != 16 {
		t.Errorf("GLI-032 (e): the peak of NodePathState requests in flight is %d with %d concurrent calls for different nodes, want exactly the limit 16", got, calls)
	}
}

// glieMedian is the median of a non-empty sample.
func glieMedian(ds []time.Duration) time.Duration {
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	if len(cp)%2 == 1 {
		return cp[len(cp)/2]
	}
	return (cp[len(cp)/2-1] + cp[len(cp)/2]) / 2
}

// GLI-032 (c)/GKA-123: the conflict back-off doubles from 10 ms and adds 0..50%
// jitter. A single wait cannot show a wider jitter (a 0..100% jitter stays below
// the cost of one attempt for the short waits), so the assertions are on a sample:
// over 60 trials with four injected 409s each, every wait is at least its nominal
// value, and for the fourth wait (nominal 80 ms; the gap between the PUTs also
// holds the cost of one attempt, which varies with the machine) two assertions
// hold together:
//
//	(a) median - min <= 0.25 x nominal + 10 ms        the spread of the jitter
//	(b) median <= 1.5 x nominal + c + 10 ms           the level of the wait
//
// where c is the median cost of one conflict-free attempt measured on 40 calls.
// Expected values (nominal 80 ms, jitter uniform on [0, 0.5] x nominal, e the
// cost of an attempt in the retry path, which is a few ms above c):
//
//	specified 0..50%      (a) ~20 ms + a few ms of cost spread = 22..26 ms <= 30 ms
//	                      (b) ~100 ms + c + e-bias (5..7 ms)  = c + 105..107 ms <= c + 130 ms
//	wide 0..100%          (a) ~40 ms + spread = 44 ms > 30 ms        -> fails by (a)
//	                      (b) ~120 ms + c + 7 ms = c + 127 ms (passes (b); (a) catches it)
//	fixed +75% / +100%    (a) ~0 + spread (passes (a))
//	                      (b) c + 140 + 7 = c + 147 ms / c + 167 ms > c + 130 ms -> fails by (b)
//
// (a) alone would let a fixed extra wait through and (b) alone a 0..100% jitter,
// so neither may be dropped. (a) is independent of c, which cancels in
// median - min together with every constant bias of the gap measurement; the
// median is used instead of the mean because it is not moved by the occasional
// slow attempt of a loaded machine.
func TestGLI032_ConflictBackoffAddsAtMostFiftyPercentJitter(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)

	// the cost of one attempt without a conflict: the time from the read to the write.
	var costs []time.Duration
	for i := 0; i < 40; i++ {
		before := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name))
		getsBefore := len(s.Ad.Rec.SingleGETs("nodepathstates", s.Name))
		s.mustAlloc(t, 0)
		puts := s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)
		gets := s.Ad.Rec.SingleGETs("nodepathstates", s.Name)
		if len(puts) != before+1 || len(gets) != getsBefore+1 {
			t.Fatalf("fixture: a conflict-free call made %d PUT(s) and %d read(s)", len(puts)-before, len(gets)-getsBefore)
		}
		costs = append(costs, puts[before].At.Sub(gets[getsBefore].At))
	}
	cost := glieMedian(costs)

	var conflicts atomic.Int64
	var armed atomic.Bool
	s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if armed.Load() && req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, s.Status) && conflicts.Add(1) <= 4 {
			return glieStatusResponse(req, http.StatusConflict, metav1.StatusReasonConflict, "injected conflict")
		}
		return nil
	})
	const trials = 60
	var fourth []time.Duration
	for trial := 0; trial < trials; trial++ {
		before := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name))
		conflicts.Store(0)
		armed.Store(true)
		s.mustAlloc(t, 0)
		armed.Store(false)
		puts := s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)[before:]
		if len(puts) != 5 {
			t.Fatalf("GKA-123: %d PUTs for four conflicts, want 5", len(puts))
		}
		for i := 1; i < len(puts); i++ {
			nominal := time.Duration(10<<(i-1)) * time.Millisecond
			gap := puts[i].At.Sub(puts[i-1].At)
			if gap < nominal-3*time.Millisecond {
				t.Errorf("GKA-123: trial %d wait %d = %v, want at least the nominal %v", trial, i, gap, nominal)
			}
		}
		fourth = append(fourth, puts[4].At.Sub(puts[3].At))
	}
	nominal := 80 * time.Millisecond
	median := glieMedian(fourth)
	smallest := fourth[0]
	for _, d := range fourth {
		if d < smallest {
			smallest = d
		}
	}
	if spread, limit := median-smallest, nominal/4+10*time.Millisecond; spread > limit {
		t.Errorf("GKA-123: over %d trials the fourth retry (nominal %v) has median %v and minimum %v, a spread of %v, want at most 0.25 x nominal + 10 ms = %v: the jitter is wider than 0..50%%", trials, nominal, median, smallest, spread, limit)
	}
	if limit := nominal*3/2 + cost + 10*time.Millisecond; median > limit {
		t.Errorf("GKA-123: over %d trials the median of the fourth retry (nominal %v) is %v, want at most 1.5 x nominal + the median attempt cost %v + 10 ms = %v: the wait carries more than the 0..50%% jitter", trials, nominal, median, cost, limit)
	}
}
