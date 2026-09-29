package kubeapi_test

// GKA-020 (many controllers per process), GKA-022 (Run: cancel, leadership loss,
// ErrAlreadyRun), GKA-023 (Clock), GKA-141 (tick), GKA-146 (cache sync, API not
// available). Time bound cases keep the spec bound plus 1-2 s of scheduling slack
// (GKA-177) and say so where they do.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// gkaRForeignLease creates a Lease held by someone else for an hour.
func gkaRForeignLease(t *testing.T, e *gkaEnvT, ns, id, holder string) {
	t.Helper()
	now := metav1.NewMicroTime(time.Now())
	secs := int32(3600)
	l := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: ns},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &secs, AcquireTime: &now, RenewTime: &now},
	}
	if _, err := e.Clientset.CoordinationV1().Leases(ns).Create(context.Background(), l, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create foreign Lease %s/%s: %v", ns, id, err)
	}
}

// gkaRRunAsync starts c.Run(ctx) in a goroutine and returns its result channel.
func gkaRRunAsync(ctx context.Context, c *controller.Controller) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- c.Run(ctx) }()
	return ch
}

func gkaRWaitErr(t *testing.T, ch <-chan error, limit time.Duration, what string) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	select {
	case err := <-ch:
		return time.Since(start), err
	case <-time.After(limit):
		t.Fatalf("%s: Run did not return within %s", what, limit)
		return 0, nil
	}
}

// ---------------------------------------------------------------------------
// GKA-020: any number of controllers per process
// ---------------------------------------------------------------------------

func TestGKA020_ManyControllersConcurrentlyAndSequentially(t *testing.T) {
	newCtl := func(i int) *controller.Controller {
		opts := gkaRBaseOpts()
		opts.RESTConfig = gkaRClosedPortConfig(t)
		opts.ControllerID = fmt.Sprintf("many-%d", i)
		opts.LeaderElection.ID = "many-lease" // same Lease ID: the instances race for one lock
		c, err := controller.New(opts)
		if err != nil {
			t.Fatalf("GKA-020: New #%d: %v", i, err)
		}
		return c
	}
	ctx, cancel := context.WithCancel(context.Background())
	var chans []<-chan error
	for i := 0; i < 4; i++ {
		chans = append(chans, gkaRRunAsync(ctx, newCtl(i)))
	}
	time.Sleep(700 * time.Millisecond)
	cancel()
	for i, ch := range chans {
		if el, err := gkaRWaitErr(t, ch, 12*time.Second, fmt.Sprintf("concurrent #%d", i)); err != nil || el >= 10*time.Second {
			t.Errorf("GKA-020/022: concurrent controller #%d Run = (%v after %s), want nil within 10s", i, err, el)
		}
	}
	for i := 10; i < 13; i++ {
		ctx2, cancel2 := context.WithCancel(context.Background())
		ch := gkaRRunAsync(ctx2, newCtl(i))
		time.Sleep(300 * time.Millisecond)
		cancel2()
		if _, err := gkaRWaitErr(t, ch, 12*time.Second, fmt.Sprintf("sequential #%d", i)); err != nil {
			t.Errorf("GKA-020: sequential controller #%d Run = %v, want nil", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// GKA-022 (d): Run is valid once
// ---------------------------------------------------------------------------

func TestGKA022_SecondRunReturnsErrAlreadyRunImmediately(t *testing.T) {
	opts := gkaRBaseOpts()
	opts.RESTConfig = gkaRClosedPortConfig(t)
	c, err := controller.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := gkaRRunAsync(ctx, c)
	time.Sleep(300 * time.Millisecond)

	// While the first Run is active.
	ctx2, cancel2 := context.WithCancel(context.Background())
	second := gkaRRunAsync(ctx2, c)
	select {
	case err := <-second:
		if !errors.Is(err, controller.ErrAlreadyRun) {
			t.Errorf("GKA-022(d): concurrent second Run = %v, want ErrAlreadyRun", err)
		}
	case <-time.After(3 * time.Second):
		cancel2()
		t.Fatal("GKA-022(d): second Run did not return immediately while the first is active")
	}
	cancel2()
	select {
	case err := <-first:
		t.Fatalf("GKA-022(d): the first Run returned (%v) because of the second call", err)
	case <-time.After(300 * time.Millisecond):
	}

	cancel()
	if _, err := gkaRWaitErr(t, first, 12*time.Second, "first"); err != nil {
		t.Errorf("GKA-022(a): first Run after cancel = %v, want nil", err)
	}
	// After the first Run returned: still ErrAlreadyRun, with a live or a cancelled ctx.
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	dead, cancelDead := context.WithCancel(context.Background())
	cancelDead()
	for name, cx := range map[string]context.Context{"live ctx": live, "cancelled ctx": dead} {
		ch := gkaRRunAsync(cx, c)
		select {
		case err := <-ch:
			if !errors.Is(err, controller.ErrAlreadyRun) {
				t.Errorf("GKA-022(d): Run after the first returned (%s) = %v, want ErrAlreadyRun", name, err)
			}
		case <-time.After(3 * time.Second):
			cancelLive()
			t.Fatalf("GKA-022(d): Run after the first returned (%s) did not return immediately", name)
		}
	}
}

func TestGKA022_ExactlyOneOfManyConcurrentRunsProceeds(t *testing.T) {
	opts := gkaRBaseOpts()
	opts.RESTConfig = gkaRClosedPortConfig(t)
	c, err := controller.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const n = 8
	res := make(chan error, n)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			res <- c.Run(ctx)
		}()
	}
	close(gate)
	for got := 0; got < n-1; got++ {
		select {
		case err := <-res:
			if !errors.Is(err, controller.ErrAlreadyRun) {
				t.Fatalf("GKA-022(d): a losing concurrent Run returned %v, want ErrAlreadyRun", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("GKA-022(d): only %d of %d losing Run calls returned ErrAlreadyRun", got, n-1)
		}
	}
	select {
	case err := <-res:
		t.Fatalf("GKA-022(d): a second Run proceeded (returned %v) instead of exactly one winner", err)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	if _, err := gkaRWaitErr(t, res, 12*time.Second, "winner"); err != nil {
		t.Errorf("GKA-022(a): the winning Run = %v after cancel, want nil", err)
	}
	wg.Wait()
}

func TestGKA022_RunWithAlreadyCancelledContextReturnsNil(t *testing.T) {
	opts := gkaRBaseOpts()
	opts.RESTConfig = gkaRClosedPortConfig(t)
	c, err := controller.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	el, err := gkaRWaitErr(t, gkaRRunAsync(ctx, c), 12*time.Second, "pre-cancelled")
	if err != nil || el >= 10*time.Second {
		t.Errorf("GKA-022(a): Run with a cancelled ctx = (%v after %s), want nil within 10s", err, el)
	}
	_, err = gkaRWaitErr(t, gkaRRunAsync(context.Background(), c), 5*time.Second, "second after cancelled")
	if !errors.Is(err, controller.ErrAlreadyRun) {
		t.Errorf("GKA-022(d): Run after a cancelled Run = %v, want ErrAlreadyRun", err)
	}
}

// ---------------------------------------------------------------------------
// GKA-022 (a)(c): cancellation
// ---------------------------------------------------------------------------

func TestGKA022_CancelWhileNotLeaderReturnsNilAndLeavesForeignLease(t *testing.T) {
	e := gkaEnv(t)
	leaseID := gkaName(t, "lease")
	gkaRForeignLease(t, e, "default", leaseID, "someone-else")
	rig := gkaRStart(t, e, nil, nil)
	time.Sleep(1800 * time.Millisecond) // > 3 x RetryPeriod of trying and failing to acquire
	if n := rig.A.AssessCount(); n != 0 {
		t.Errorf("GKA-022: a non-leader ran %d pass(es); it must not run any before being leader", n)
	}
	if w := rig.Rec.Writes(""); len(w) != 0 {
		t.Errorf("GKA-022/143: a non-leader wrote: %v", w)
	}
	if h, _ := gkaRLeaseHolder(e, "default", leaseID); h != "someone-else" {
		t.Errorf("GKA-142: holderIdentity = %q, the foreign holder must keep the Lease", h)
	}
	rig.Ctl.Cancel()
	el, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second)
	if err != nil || el >= 10*time.Second {
		t.Errorf("GKA-022(a): cancel while not leader: Run = (%v after %s), want nil within 10s", err, el)
	}
	if h, _ := gkaRLeaseHolder(e, "default", leaseID); h != "someone-else" {
		t.Errorf("GKA-142: after cancel holderIdentity = %q; a non-leader must not release another holder's Lease", h)
	}
	gkaRNoRequestsAfter(t, rig.Rec, gkaRCtlReturnedAt(rig.Ctl), "Run's return")
	gkaRAssertVerbs(t, rig.Rec)
}

func TestGKA022_CancelBeforeCacheSyncReturnsNil(t *testing.T) {
	e := gkaEnv(t)
	gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRNewRig(e)
	release, entered := gkaRHoldList(rig.Rec, "gpudevices")
	t.Cleanup(release)
	rig.Start(t, nil, nil)
	gkaEventually(t, 15*time.Second, func() (bool, string) {
		return entered.Load(), "neither the GPUDevice LIST nor the initial WATCH was requested"
	})
	gkaRWaitHolder(t, e, "default", gkaRCtlLease(rig.Ctl), gkaRCtlID(rig.Ctl), 15*time.Second)
	time.Sleep(600 * time.Millisecond)
	if rig.A.AssessCount() != 0 || len(rig.Rec.Writes("")) != 0 {
		t.Errorf("GKA-022/146: leader with an unsynced cache assessed %d time(s) and wrote %v", rig.A.AssessCount(), rig.Rec.Writes(""))
	}
	rig.Ctl.Cancel()
	el, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second)
	if err != nil || el >= 10*time.Second {
		t.Errorf("GKA-022(a): cancel before cache sync: Run = (%v after %s), want nil within 10s", err, el)
	}
	if w := rig.Rec.Writes(""); len(w) != 0 {
		t.Errorf("GKA-022/146: writes after a cancel before cache sync: %v", w)
	}
	if h, _ := gkaRLeaseHolder(e, "default", gkaRCtlLease(rig.Ctl)); h != "" {
		t.Errorf("GKA-142: the leader must release the Lease on cancel even before cache sync, holder = %q", h)
	}
}

func TestGKA022_CancelAbortsPassInFlight(t *testing.T) {
	e := gkaEnv(t)
	gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRNewRig(e)
	entered := make(chan struct{})
	sawCancel := make(chan struct{})
	var enterOnce, cancelOnce sync.Once
	rig.A.SetScriptCtx(func(ctx context.Context, req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			cancelOnce.Do(func() { close(sawCancel) })
			return app.NodeAssessment{}, ctx.Err()
		case <-time.After(25 * time.Second):
			return app.NodeAssessment{}, errors.New("assessor context was never cancelled")
		}
	})
	// A long ResyncInterval keeps the per-call deadline (= ResyncInterval, GKA-070(f)) out of the way.
	rig.Ctl = gkaStartController(t, rig.Cfg, rig.A, rig.Clk, func(o *controller.Options) { o.ResyncInterval = 30 * time.Second })
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the assessor was never called")
	}
	if log := rig.A.Log(); len(log) > 0 && (!log[0].HasDeadline || log[0].Remaining > 30*time.Second || log[0].Remaining < 20*time.Second) {
		t.Errorf("GKA-070(f): assessor ctx deadline = (%v, %s), want about ResyncInterval (30s)", log[0].HasDeadline, log[0].Remaining)
	}
	rig.Ctl.Cancel()
	tCancel := time.Now()
	select {
	case <-sawCancel:
	case <-time.After(10 * time.Second):
		t.Error("GKA-022(a): the in-flight assessor call never saw its context cancelled")
	}
	el, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second)
	if err != nil || el >= 10*time.Second {
		t.Errorf("GKA-022(a): cancel during a pass: Run = (%v after %s), want nil within 10s", err, el)
	}
	if late := gkaRWritesSince(rig.Rec, "", tCancel.Add(100*time.Millisecond)); len(late) != 0 {
		t.Errorf("GKA-144: write(s) started after the cancel: %v", late)
	}
	// The aborted call must not be rendered as an InternalError status after the cancel.
	fl := gkaRGetFleet(t, e, gkaName(t, "fl"))
	if c := gkaRCond(fl.Status.Conditions, "FleetReady"); c != nil && c.Reason == "InternalError" {
		t.Errorf("GKA-144: a cancelled pass wrote FleetReady InternalError")
	}
	gkaRNoRequestsAfter(t, rig.Rec, gkaRCtlReturnedAt(rig.Ctl), "Run's return")
}

// An assessor that ignores its context must not hold Run past the bound: the
// internal wait is capped at 10 s or less and Run still returns nil.
func TestGKA022_UnresponsiveAssessorDoesNotDelayReturnPastTheBound(t *testing.T) {
	e := gkaEnv(t)
	gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRNewRig(e)
	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var once, relOnce sync.Once
	release := func() { relOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(release)
	rig.A.SetScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		once.Do(func() { close(entered) })
		<-releaseCh // ignores every context
		return app.NodeAssessment{}, nil
	})
	rig.Ctl = gkaStartController(t, rig.Cfg, rig.A, rig.Clk, func(o *controller.Options) { o.ResyncInterval = 30 * time.Second })
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the assessor was never called")
	}
	rig.Ctl.Cancel()
	// Spec bound: 10 s. The 2 s of slack covers -race scheduling only (GKA-177).
	el, err := gkaRWaitDone(t, rig.Ctl, 15*time.Second)
	if err != nil {
		t.Errorf("GKA-022(a): Run = %v after the cleanup cap, want nil", err)
	}
	if el > 12*time.Second {
		t.Errorf("GKA-022(a): Run took %s to return with an unresponsive assessor, want the 10s bound", el)
	}
	release()
}

// ---------------------------------------------------------------------------
// GKA-022 (b): leadership loss
// ---------------------------------------------------------------------------

func gkaRLoseLeadership(t *testing.T, how string) {
	t.Helper()
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	lease := gkaRCtlLease(rig.Ctl)
	gkaRWaitHolder(t, e, "default", lease, gkaRCtlID(rig.Ctl), 10*time.Second)

	switch how {
	case "holder change":
		gkaEventually(t, 10*time.Second, func() (bool, string) {
			l, ok := gkaRLease(e, "default", lease)
			if !ok {
				return false, "lease missing"
			}
			thief, secs := "thief", int32(3600)
			now := metav1.NewMicroTime(time.Now())
			l.Spec.HolderIdentity, l.Spec.LeaseDurationSeconds, l.Spec.RenewTime, l.Spec.AcquireTime = &thief, &secs, &now, &now
			_, err := e.Clientset.CoordinationV1().Leases("default").Update(context.Background(), l, metav1.UpdateOptions{})
			return err == nil, fmt.Sprint(err)
		})
	case "renew failure":
		rig.Rec.InjectErr(func(req *http.Request) error {
			if req.Method == http.MethodPut && gkaRIsLease(req.URL.Path) {
				return errors.New("gka: injected connection failure")
			}
			return nil
		})
	}
	tLoss := time.Now()
	limit := 3*time.Second + 500*time.Millisecond + 2*time.Second // RenewDeadline + RetryPeriod + 2s
	var err error
	select {
	case err = <-rig.Ctl.Done:
	case <-time.After(limit + 8*time.Second):
		t.Fatalf("GKA-022(b): Run did not return after the loss (%s)", how)
	}
	if el := time.Since(tLoss); el > limit+time.Second { // 1 s scheduling slack
		t.Errorf("GKA-022(b): leadership loss (%s) took %s, want <= RenewDeadline+RetryPeriod+2s = %s", how, el, limit)
	}
	if !errors.Is(err, controller.ErrLeadershipLost) {
		t.Errorf("GKA-022(b)/024: Run = %v, want errors.Is(err, ErrLeadershipLost)", err)
	}
	if how == "holder change" {
		if h, _ := gkaRLeaseHolder(e, "default", lease); h != "thief" {
			t.Errorf("GKA-142: the ex-leader changed the Lease holder to %q; it must leave the new holder alone", h)
		}
	}
	gkaRNoRequestsAfter(t, rig.Rec, gkaRCtlReturnedAt(rig.Ctl), "Run's return after the loss")
	gkaRAssertVerbs(t, rig.Rec)
}

func TestGKA022_LeadershipLostWhenHolderChanges(t *testing.T) { gkaRLoseLeadership(t, "holder change") }

func TestGKA022_LeadershipLostWhenRenewalFails(t *testing.T) { gkaRLoseLeadership(t, "renew failure") }

// ---------------------------------------------------------------------------
// GKA-023 Clock
// ---------------------------------------------------------------------------

// The clock is read exactly once per pass: with a frozen clock its Now() call
// count equals the number of passes, and with a stepping clock all requests of one
// pass carry the same value while consecutive passes differ by exactly one step.
func TestGKA023_ClockIsReadOncePerPass(t *testing.T) {
	e := gkaEnv(t)
	gkaRNewScene(t, e, 3, 1, v1alpha1.FleetModeAudit)

	cfg, _ := gkaRecordingConfig(e.Config)
	a := newGkaFakeAssessor()
	tick := &gkaRTickClock{base: gkaRT0, step: time.Second}
	gkaStartController(t, cfg, a, tick, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return a.AssessCount() >= 18, fmt.Sprintf("only %d assess calls", a.AssessCount())
	})
	// At a pass boundary the assessor count is 3 x the number of clock reads.
	gkaEventually(t, 5*time.Second, func() (bool, string) {
		reads := tick.Calls()
		calls := int64(a.AssessCount())
		return calls == 3*reads, fmt.Sprintf("clock reads=%d assess calls=%d: Now() must be read exactly once per pass", reads, calls)
	})
	reqs := a.Requests()
	full := len(reqs) / 3 * 3
	for i := 0; i < full; i += 3 {
		chunk := reqs[i : i+3]
		for j := 1; j < 3; j++ {
			if !chunk[j].Now.Equal(chunk[0].Now) {
				t.Fatalf("GKA-023: requests %d and %d of one pass carry different Now values %s / %s: the clock must be read once per pass", i, i+j, chunk[0].Now, chunk[j].Now)
			}
			if chunk[j-1].Node.UID >= chunk[j].Node.UID {
				t.Errorf("GKA-070(f): pass at request %d calls nodes out of UID byte order: %q then %q", i, chunk[j-1].Node.UID, chunk[j].Node.UID)
			}
		}
		if i >= 3 {
			if d := chunk[0].Now.Sub(reqs[i-3].Now); d != time.Second {
				t.Errorf("GKA-023: consecutive passes differ by %s, want exactly one clock step (1s): Now() must be read exactly once per pass", d)
			}
		}
	}
}

func TestGKA023_RequestNowAndConditionTimesComeFromTheClock(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	frac := gkaRT0.Add(700*time.Millisecond + 123)
	rig := gkaRNewRig(e)
	rig.Clk.Set(frac)
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)

	if reqs := rig.A.Requests(); len(reqs) == 0 {
		t.Fatal("no assessor request")
	} else {
		for i, r := range reqs {
			if !r.Now.Equal(frac) {
				t.Fatalf("GKA-023: request %d Now = %s, want exactly the clock value %s (nanoseconds kept)", i, r.Now.Format(time.RFC3339Nano), frac.Format(time.RFC3339Nano))
			}
			if want := gkaRT0.Add(time.Second); len(r.Intents) != 1 || !r.Intents[0].ObservedAt.Equal(want) {
				t.Errorf("GKA-071: Intent.ObservedAt = %v, want the clock value rounded up to the second (%s)", r.Intents, want)
			}
		}
	}
	dev := gkaRGetDevice(t, e, sc.Devices[0].Name)
	if got, want := dev.Status.IntentObservedAt.UTC(), gkaRT0.Add(time.Second); !got.Equal(want) {
		t.Errorf("GKA-071: intentObservedAt = %s, want %s (ceil of the clock value)", got, want)
	}
	for _, c := range dev.Status.Conditions {
		if got := c.LastTransitionTime.UTC(); !got.Equal(gkaRT0) {
			t.Errorf("GKA-074(c): device condition %s lastTransitionTime = %s, want the clock value truncated to the second (%s)", c.Type, got, gkaRT0)
		}
	}
	nps, ok := gkaRTryNPS(e, sc.Nodes[0].Name)
	if !ok {
		t.Fatal("NodePathState missing")
	}
	for _, c := range nps.Status.Conditions {
		if got := c.LastTransitionTime.UTC(); !got.Equal(gkaRT0) {
			t.Errorf("GKA-074(c): NodePathState condition %s lastTransitionTime = %s, want %s", c.Type, got, gkaRT0)
		}
	}
	gkaEventually(t, 10*time.Second, func() (bool, string) {
		n := gkaRGetNode(t, e, sc.Nodes[0].Name)
		c := gkaRNodeCond(n)
		return c != nil && c.LastTransitionTime.UTC().Equal(gkaRT0), fmt.Sprintf("node condition = %+v", c)
	})

	// Advancing the clock moves the next passes' Now while unchanged statuses keep their old times.
	rig.Clk.Advance(time.Hour)
	want := frac.Add(time.Hour)
	gkaEventually(t, 10*time.Second, func() (bool, string) {
		reqs := rig.A.Requests()
		last := reqs[len(reqs)-1].Now
		return last.Equal(want), fmt.Sprintf("last request Now = %s, want %s", last.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	})
	dev = gkaRGetDevice(t, e, sc.Devices[0].Name)
	if got := dev.Status.IntentObservedAt.UTC(); !got.Equal(gkaRT0.Add(time.Second)) {
		t.Errorf("GKA-071: intentObservedAt changed to %s after the clock advanced; it is fixed per generation and request", got)
	}
	for _, c := range dev.Status.Conditions {
		if got := c.LastTransitionTime.UTC(); !got.Equal(gkaRT0) {
			t.Errorf("GKA-074(c): condition %s lastTransitionTime = %s after the clock advanced with an unchanged status", c.Type, got)
		}
	}
}

func TestGKA023_NilClockUsesTheWallClock(t *testing.T) {
	e := gkaEnv(t)
	gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	cfg, _ := gkaRecordingConfig(e.Config)
	a := newGkaFakeAssessor()
	gkaStartController(t, cfg, a, nil, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) { return a.AssessCount() >= 2, "no assessor call" })
	before := time.Now()
	for _, r := range a.Requests() {
		if d := before.Sub(r.Now); d < -time.Second || d > 30*time.Second {
			t.Errorf("GKA-020/023: Clock=nil: request Now %s is not the wall clock (now %s)", r.Now, before)
		}
	}
}

// ---------------------------------------------------------------------------
// GKA-141 tick
// ---------------------------------------------------------------------------

// The tick is a real timer: a frozen clock and no events still produce passes
// (assessor calls), no writes, and no hot loop.
func TestGKA141_TickRunsWithFrozenClockAndNoEvents(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	top, _ := gkaRPrefixes(t.Name())
	gkaRWaitQuiet(t, rig.Rec, top, 200*time.Millisecond)

	start, c0, r0 := time.Now(), rig.A.AssessCount(), rig.Clk.Calls()
	time.Sleep(2 * time.Second)
	passes := rig.A.AssessCount() - c0 // one node: one assessor call per pass
	// About ten passes in two seconds at 200ms; loose bounds: a working tick and no hot loop.
	if passes < 6 || passes > 40 {
		t.Errorf("GKA-141: %d passes in 2s at ResyncInterval 200ms, want about 10", passes)
	}
	if reads := rig.Clk.Calls() - r0; reads < 6 {
		t.Errorf("GKA-141/023: %d clock reads in 2s: the tick must keep running while the clock is frozen", reads)
	}
	if w := gkaRWritesSince(rig.Rec, top, start); len(w) != 0 {
		t.Errorf("GKA-122/141: %d write(s) during ticks with unchanged input", len(w))
	}
}

func TestGKA141_ZeroResyncIntervalMeansThirtySeconds(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRStart(t, e, nil, func(o *controller.Options) { o.ResyncInterval = 0 })
	gkaRWaitSettled(t, e, sc)
	// Wait until the event-driven passes caused by our own writes are over.
	last, lastAt := -1, time.Now()
	gkaEventually(t, 15*time.Second, func() (bool, string) {
		n := rig.A.AssessCount()
		if n != last {
			last, lastAt = n, time.Now()
		}
		return time.Since(lastAt) >= 1200*time.Millisecond, fmt.Sprintf("assess calls still changing (%d)", n)
	})
	before := rig.A.AssessCount()
	time.Sleep(2 * time.Second)
	if after := rig.A.AssessCount(); after != before {
		t.Errorf("GKA-141: ResyncInterval 0 must mean 30s, but %d extra pass(es) ran within 2s", after-before)
	}
}

// ---------------------------------------------------------------------------
// GKA-146 cache sync and API not available
// ---------------------------------------------------------------------------

func TestGKA146_ClosedPortAndUnavailableCRDsNeverReturnBeforeCancel(t *testing.T) {
	e := gkaEnv(t)
	// Case 1: a closed port.
	closedCtl := gkaStartController(t, gkaRClosedPortConfig(t), newGkaFakeAssessor(), nil, func(o *controller.Options) {
		o.ControllerID = gkaName(t, "closed")
		o.LeaderElection.ID = gkaName(t, "lease-closed")
	})
	// Case 2: an API server on which the CRD group answers NotFound (the CRDs behave as uninstalled).
	rig := gkaRNewRig(e)
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if strings.HasPrefix(req.URL.Path, "/apis/"+v1alpha1.GroupName) {
			return gkaRStatusResponse(req, http.StatusNotFound, metav1.StatusReasonNotFound, "the server could not find the requested resource")
		}
		return nil
	})
	rig.Start(t, nil, nil)

	time.Sleep(10500 * time.Millisecond)
	for name, c := range map[string]*gkaCtl{"closed port": closedCtl, "CRD group NotFound": rig.Ctl} {
		select {
		case err := <-c.Done:
			t.Errorf("GKA-146: Run returned (%v) within 10s with %s; it must keep waiting", err, name)
		default:
		}
	}
	if w := rig.Rec.Writes(""); len(w) != 0 {
		t.Errorf("GKA-146: with the CRDs unavailable the controller wrote %v (only Lease writes are allowed)", w)
	}
	if rig.A.AssessCount() != 0 {
		t.Errorf("GKA-146: %d pass(es) ran without synchronised caches", rig.A.AssessCount())
	}
	closedCtl.Cancel()
	rig.Ctl.Cancel()
	for name, c := range map[string]*gkaCtl{"closed port": closedCtl, "CRD group NotFound": rig.Ctl} {
		el, err := gkaRWaitDone(t, c, 12*time.Second)
		if err != nil || el >= 10*time.Second {
			t.Errorf("GKA-146/022(a): cancel with %s: Run = (%v after %s), want nil within 10s", name, err, el)
		}
	}
}

// No create/update/delete/apply happens before every cache has synced, above all
// not the GKA-102 delete of an orphan NodePathState; the delete follows the sync.
func TestGKA146_NoWritesBeforeAllCachesSynced(t *testing.T) {
	e := gkaEnv(t)
	orphan := gkaRNode(t, e, gkaName(t, "orphan"), nil) // selected by no fleet
	nps := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{Name: orphan.Name},
		Spec:       v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: orphan.Name, UID: string(orphan.UID)}},
	}
	if err := e.Client.Create(context.Background(), nps); err != nil {
		t.Fatalf("create orphan NodePathState: %v", err)
	}
	rig := gkaRNewRig(e)
	release, entered := gkaRHoldList(rig.Rec, "gpudevices")
	t.Cleanup(release)
	rig.Start(t, nil, nil)
	gkaEventually(t, 15*time.Second, func() (bool, string) {
		return entered.Load(), "neither the GPUDevice LIST nor the initial WATCH was requested"
	})
	gkaRWaitHolder(t, e, "default", gkaRCtlLease(rig.Ctl), gkaRCtlID(rig.Ctl), 15*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if w := rig.Rec.Writes(""); len(w) != 0 {
		t.Errorf("GKA-146: writes while a cache had not synced: %v", w)
	}
	if _, ok := gkaRTryNPS(e, orphan.Name); !ok {
		t.Fatal("GKA-102/146: the orphan NodePathState was deleted before the caches synced")
	}
	release()
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		_, ok := gkaRTryNPS(e, orphan.Name)
		return !ok, "the orphan NodePathState still exists after the caches synced"
	})
}
