package kubeapi_test

// GKA-125 (requeue schedule): a status write that fails with a transient error is
// requeued starting at min(100ms, ResyncInterval), doubling per failure, capped at
// ResyncInterval, and the schedule starts over after a success. The trigger is a
// tampered device status, so that the failing PUT is the only write in the pass:
// no other object changes and no other watch event can start an earlier retry.
// With ResyncInterval 30 s no tick falls into the first 25 seconds after Run started.
// Interval assertions are lower bounds with load tolerance plus ordering; exact
// values are never asserted.

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// gkaRTamperDeviceStatus changes actualBinding.reason behind the controller's back.
func gkaRTamperDeviceStatus(t *testing.T, e *gkaEnvT, name, reason string) {
	t.Helper()
	gkaRRetry(t, "tamper with the device status", func() error {
		d := &v1alpha1.GPUDevice{}
		if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, d); err != nil {
			return err
		}
		d.Status.ActualBinding.Reason = reason
		return e.Client.Status().Update(context.Background(), d)
	})
}

// gkaRRequeueRig runs a settled controller whose device status PUT fails with a 500
// while failing is true.
type gkaRRequeueRig struct {
	rig     *gkaRRig
	dev     string
	failing atomic.Bool
}

func gkaRNewRequeueRig(t *testing.T, resync time.Duration) (*gkaRRequeueRig, *gkaEnvT) {
	t.Helper()
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	r := &gkaRRequeueRig{dev: sc.Devices[0].Name}
	r.rig = gkaRNewRig(e)
	gkaRInjectStatusPUT(r.rig.Rec, "gpudevices", r.dev, func(req *http.Request, n int64) *http.Response {
		if r.failing.Load() {
			return gkaRStatusResponse(req, http.StatusInternalServerError, metav1.StatusReasonInternalError, "injected 500")
		}
		return nil
	})
	r.rig.Start(t, nil, func(o *controller.Options) { o.ResyncInterval = resync })
	gkaRWaitSettled(t, e, sc)
	gkaRWaitQuiet(t, r.rig.Rec, "", 200*time.Millisecond)
	return r, e
}

// episode makes the device PUT fail, tampers with the status so that a write is due,
// waits for n failing attempts, and returns their entries.
func (r *gkaRRequeueRig) episode(t *testing.T, e *gkaEnvT, n int, reason string) []gkaRecEntry {
	t.Helper()
	start := time.Now()
	r.failing.Store(true)
	gkaRTamperDeviceStatus(t, e, r.dev, reason)
	var got []gkaRecEntry
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		got = got[:0]
		for _, p := range gkaRStatusPUTs(r.rig.Rec, "gpudevices", r.dev) {
			if p.At.After(start) {
				got = append(got, p)
			}
		}
		return len(got) >= n, fmt.Sprintf("%d failing status PUTs so far, want %d", len(got), n)
	})
	r.failing.Store(false)
	return got[:n]
}

func (r *gkaRRequeueRig) recovered(t *testing.T, e *gkaEnvT) {
	t.Helper()
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		d := gkaRGetDevice(t, e, r.dev)
		return d.Status.ActualBinding.Reason == "Validating", "actualBinding.reason = " + d.Status.ActualBinding.Reason
	})
}

func TestGKA125_RequeueBacksOffFromOneHundredMillisecondsAndResetsAfterSuccess(t *testing.T) {
	r, e := gkaRNewRequeueRig(t, 30*time.Second)
	started := time.Now()

	es := r.episode(t, e, 7, "stale-1")
	for i := 1; i <= 6; i++ {
		delay := (100 * time.Millisecond) << (i - 1) // 100ms, 200ms, ... 3.2s
		gap := es[i].At.Sub(es[i-1].At)
		if float64(gap) < 0.85*float64(delay) {
			t.Errorf("GKA-125: retry %d came %s after the failure, want at least the requeue delay %s (100ms doubling)", i, gap, delay)
		}
		if gap > 2*delay+2*time.Second {
			t.Errorf("GKA-125: retry %d came %s after the failure, far beyond the requeue delay %s", i, gap, delay)
		}
		if i >= 2 && gap <= es[i-1].At.Sub(es[i-2].At) {
			t.Errorf("GKA-125: the retry interval did not grow (%s after %s)", gap, es[i-1].At.Sub(es[i-2].At))
		}
	}

	// A success resets the schedule: the next failure is retried after about 100 ms, not after
	// the 6.4 s the previous episode had reached. The second tamper starts a pass that succeeds.
	gkaRTamperDeviceStatus(t, e, r.dev, "stale-2")
	r.recovered(t, e)
	es2 := r.episode(t, e, 3, "stale-3")
	if gap := es2[1].At.Sub(es2[0].At); gap < 85*time.Millisecond || gap > 2*time.Second {
		t.Errorf("GKA-125: after a success the first retry came %s later, want about 100ms (the schedule starts over)", gap)
	}
	if gap := es2[2].At.Sub(es2[1].At); float64(gap) < 0.85*float64(200*time.Millisecond) {
		t.Errorf("GKA-125: after a success the second retry came %s later, want at least 200ms", gap)
	}
	if el := time.Since(started); el > 25*time.Second {
		t.Errorf("test premise: the scenario took %s, so a 30s tick may have retried earlier than the requeue", el)
	}
}

// The doubling stops at ResyncInterval: with a 1 s interval no retry interval exceeds
// it by more than scheduling slack, however many failures have accumulated.
func TestGKA125_RequeueIntervalIsCappedAtResyncInterval(t *testing.T) {
	const resync = time.Second
	r, e := gkaRNewRequeueRig(t, resync)
	es := r.episode(t, e, 9, "stale-cap")
	for i := 1; i < len(es); i++ {
		if gap := es[i].At.Sub(es[i-1].At); gap > resync+1500*time.Millisecond {
			t.Errorf("GKA-125/141: retry %d came %s after the failure, want at most ResyncInterval %s (+1.5s slack)", i, gap, resync)
		}
	}
	if span := es[len(es)-1].At.Sub(es[0].At); span > 9*(resync+1500*time.Millisecond) {
		t.Errorf("GKA-125: nine attempts took %s", span)
	}
}
