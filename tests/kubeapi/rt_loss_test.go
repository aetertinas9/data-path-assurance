package kubeapi_test

// GKA-022 (b), 142, 143: the leadership loss window. After the leader gives up
// (its last successful renewal plus RenewDeadline, or the moment it sees another
// holder) it must issue no CRD, NodePathState or Node write, Run must return
// ErrLeadershipLost in time, and once a standby leads the old leader stays silent.
// Three ways to lose the Lease: another holder appears, every renewal fails at
// once, and Lease requests hang until their context ends while CRD and Node
// requests stay healthy. A test goroutine keeps editing a GPUDevice spec so that
// the leader has writes to make during the whole window.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// gkaRStartEditor edits spec.request.reason of a GPUDevice every 300 ms until the
// returned stop function (also registered with t.Cleanup) is called.
func gkaRStartEditor(t *testing.T, e *gkaEnvT, name string) (stop func()) {
	t.Helper()
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-quit:
				return
			case <-time.After(300 * time.Millisecond):
			}
			d := &v1alpha1.GPUDevice{}
			if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, d); err != nil {
				continue
			}
			d.Spec.Request.Reason = fmt.Sprintf("edit-%d", i)
			_ = e.Client.Update(context.Background(), d) // conflicts with the controller's reads are harmless
		}
	}()
	stop = sync.OnceFunc(func() {
		close(quit)
		wg.Wait()
	})
	t.Cleanup(stop)
	return stop
}

// gkaRLastLeasePUTBefore is the entry time of the newest Lease PUT before at.
func gkaRLastLeasePUTBefore(rec *gkaRecorder, at time.Time) time.Time {
	var last time.Time
	for _, en := range rec.Entries() {
		if en.Method == http.MethodPut && gkaRIsLease(en.Path) && en.At.Before(at) && en.At.After(last) {
			last = en.At
		}
	}
	return last
}

func gkaRLossWindow(t *testing.T, how string) {
	t.Helper()
	const (
		renewDeadline = 3 * time.Second // gkaStartController default
		retryPeriod   = 500 * time.Millisecond
	)
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	dev := sc.Devices[0].Name
	a := gkaRStart(t, e, nil, nil)
	lease := gkaRCtlLease(a.Ctl)
	gkaRWaitHolder(t, e, "default", lease, gkaRCtlID(a.Ctl), 15*time.Second)
	gkaRWaitSettled(t, e, sc)
	b := gkaRStart(t, e, nil, nil) // the standby
	time.Sleep(700 * time.Millisecond)

	// Lease requests of the old leader hang (variant "lease requests hang") until the flag is
	// raised; the hang ends with the request context, the test cleanup, or after 30 s.
	var hang atomic.Bool
	releaseHang := make(chan struct{})
	var relOnce sync.Once
	a.Rec.Inject(func(req *http.Request) *http.Response {
		if hang.Load() && gkaRIsLease(req.URL.Path) {
			select {
			case <-req.Context().Done():
			case <-releaseHang:
			case <-time.After(30 * time.Second):
			}
		}
		return nil
	})
	gkaRStartEditor(t, e, dev)
	t.Cleanup(func() { relOnce.Do(func() { close(releaseHang) }) }) // runs before the controllers are stopped

	// Writes flow before the loss.
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		n := len(a.Rec.WriteEntries(gkaRPathFor("gpudevices", dev)))
		return n >= 4, fmt.Sprintf("only %d device writes before the loss", n)
	})

	var t0 time.Time // when the loss starts
	switch how {
	case "holder change":
		// The thief's Lease outlives the old leader's give-up time but expires before the standby's test deadline.
		gkaRRetry(t, "take the Lease", func() error {
			l, ok := gkaRLease(e, "default", lease)
			if !ok {
				return errors.New("lease missing")
			}
			thief, secs := "thief", int32(6)
			now := metav1.NewMicroTime(time.Now())
			l.Spec.HolderIdentity, l.Spec.LeaseDurationSeconds, l.Spec.RenewTime, l.Spec.AcquireTime = &thief, &secs, &now, &now
			_, err := e.Clientset.CoordinationV1().Leases("default").Update(context.Background(), l, metav1.UpdateOptions{})
			return err
		})
		t0 = time.Now()
	case "renew failure":
		a.Rec.InjectErr(func(req *http.Request) error {
			if req.Method == http.MethodPut && gkaRIsLease(req.URL.Path) {
				return errors.New("gka: injected connection failure")
			}
			return nil
		})
		t0 = time.Now()
	case "lease requests hang":
		hang.Store(true)
		t0 = time.Now()
	default:
		t.Fatalf("unknown variant %q", how)
	}
	lastRenew := gkaRLastLeasePUTBefore(a.Rec, t0)
	if lastRenew.IsZero() {
		t.Fatal("test premise: the old leader renewed the Lease before the loss")
	}
	// The give-up time: last successful renewal + RenewDeadline (client-go notices at the next
	// retry tick, so one RetryPeriod more).
	judge := lastRenew.Add(renewDeadline + retryPeriod)

	// Run returns ErrLeadershipLost within RenewDeadline + RetryPeriod + 2s (+ 2s of load slack).
	limit := renewDeadline + retryPeriod + 2*time.Second + 2*time.Second
	var err error
	select {
	case err = <-a.Ctl.Done:
	case <-time.After(limit + 10*time.Second):
		t.Fatalf("GKA-022(b): Run did not return after the loss (%s)", how)
	}
	ret := gkaRCtlReturnedAt(a.Ctl)
	if el := ret.Sub(t0); el > limit {
		t.Errorf("GKA-022(b): the loss (%s) took %s to end Run, want <= RenewDeadline+RetryPeriod+2s (+2s slack) = %s", how, el, limit)
	}
	if !errors.Is(err, controller.ErrLeadershipLost) {
		t.Errorf("GKA-022(b)/024: Run = %v, want errors.Is(err, ErrLeadershipLost)", err)
	}

	// No CRD, NodePathState or Node write enters the transport after the give-up time (0.5 s margin).
	wroteBefore := false
	for _, w := range a.Rec.WriteEntries("") {
		if w.At.After(t0.Add(-3*time.Second)) && w.At.Before(t0) {
			wroteBefore = true
		}
	}
	if !wroteBefore {
		t.Error("test premise: the old leader made no write in the 3s before the loss")
	}
	var late []string
	for _, w := range a.Rec.WriteEntries("") {
		if w.At.After(judge.Add(500 * time.Millisecond)) {
			late = append(late, fmt.Sprintf("%s (+%s after the give-up time)", w, w.At.Sub(judge).Round(time.Millisecond)))
		}
	}
	if len(late) > 0 {
		t.Errorf("GKA-022(b)/142/143 (%s): %d write(s) entered the transport after leadership was lost: %v", how, len(late), late)
	}
	gkaRNoRequestsAfter(t, a.Rec, ret, "Run's return after the loss")

	// The standby takes over; from its first pass on, the old leader writes nothing.
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return b.A.AssessCount() > 0, "the standby has not started a pass"
	})
	tB := b.A.Log()[0].At
	if w := gkaRWritesSince(a.Rec, "", tB.Add(100*time.Millisecond)); len(w) != 0 {
		t.Errorf("GKA-143/144 (%s): the old leader wrote after the new leader's first pass: %v", how, w)
	}
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		n := len(gkaRWritesSince(b.Rec, "", tB))
		return n > 0, fmt.Sprintf("the new leader made %d write(s) since its first pass", n)
	})
	if how == "holder change" {
		if h, _ := gkaRLeaseHolder(e, "default", lease); h == "thief" {
			t.Errorf("GKA-142: the Lease is still held by the thief long after it expired; the standby did not take it")
		}
	}
	gkaRAssertVerbs(t, a.Rec)
	gkaRAssertVerbs(t, b.Rec)
}

func TestGKA022_LossWindowHolderChangeStopsWritesAndFailsOver(t *testing.T) {
	gkaRLossWindow(t, "holder change")
}

func TestGKA022_LossWindowRenewFailureStopsWritesAndFailsOver(t *testing.T) {
	gkaRLossWindow(t, "renew failure")
}

func TestGKA022_LossWindowLeaseRequestsHangStopWritesAndFailOver(t *testing.T) {
	gkaRLossWindow(t, "lease requests hang")
}
