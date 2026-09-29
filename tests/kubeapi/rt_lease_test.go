package kubeapi_test

// GKA-142 (Lease), GKA-143 (non-leader silence, failover), GKA-144 (no writes
// after cancel), GKA-145 (restart and leader switch start without memory).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// gkaRSetNodeLabels replaces the labels of a Node (retrying on conflicts).
func gkaRSetNodeLabels(t *testing.T, e *gkaEnvT, name string, labels map[string]string) {
	t.Helper()
	gkaEventually(t, 10*time.Second, func() (bool, string) {
		n := gkaRGetNode(t, e, name)
		n.Labels = labels
		err := e.Client.Update(context.Background(), n)
		return err == nil, fmt.Sprint(err)
	})
}

func gkaRDeleteLeaseQuietly(e *gkaEnvT, ns, id string) {
	_ = e.Clientset.CoordinationV1().Leases(ns).Delete(context.Background(), id, metav1.DeleteOptions{})
}

// ---------------------------------------------------------------------------
// GKA-142
// ---------------------------------------------------------------------------

func TestGKA142_LeaseHolderIsControllerIDRenewsAndIsReleasedOnCancel(t *testing.T) {
	e := gkaEnv(t)
	rig := gkaRStart(t, e, nil, nil)
	id, lease := gkaRCtlID(rig.Ctl), gkaRCtlLease(rig.Ctl)
	gkaRWaitHolder(t, e, "default", lease, id, 15*time.Second)

	l, ok := gkaRLease(e, "default", lease)
	if !ok {
		t.Fatal("GKA-142: the Lease does not exist")
	}
	if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity != id {
		t.Errorf("GKA-142: holderIdentity = %v, want exactly the ControllerID %q", l.Spec.HolderIdentity, id)
	}
	if l.Spec.LeaseDurationSeconds == nil || *l.Spec.LeaseDurationSeconds != 4 {
		t.Errorf("GKA-142: leaseDurationSeconds = %v, want 4 for LeaseDuration 4s", l.Spec.LeaseDurationSeconds)
	}
	if l.Spec.RenewTime == nil {
		t.Fatal("GKA-142: renewTime is unset")
	}
	first := l.Spec.RenewTime.Time
	time.Sleep(1500 * time.Millisecond)
	l2, _ := gkaRLease(e, "default", lease)
	if l2 == nil || l2.Spec.RenewTime == nil || !l2.Spec.RenewTime.After(first) {
		t.Errorf("GKA-142: the leader does not renew the Lease (renewTime did not advance from %s)", first)
	}

	rig.Ctl.Cancel()
	if el, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second); err != nil || el >= 10*time.Second {
		t.Errorf("GKA-022(a): Run = (%v after %s), want nil within 10s", err, el)
	}
	if h, found := gkaRLeaseHolder(e, "default", lease); !found || h != "" {
		t.Errorf("GKA-142: after a graceful stop holderIdentity = %q (found=%v), want nil or empty", h, found)
	}
	gkaRAssertVerbs(t, rig.Rec)
}

// The Lease records whole seconds (LeaseDuration 2.5s -> 2), the namespace comes
// from the options, and an empty ID means the Lease is called path-controller.
func TestGKA142_LeaseDurationTruncationNamespaceAndDefaultName(t *testing.T) {
	e := gkaEnv(t)
	const ns, name = "kube-system", "path-controller"
	gkaRDeleteLeaseQuietly(e, ns, name)
	t.Cleanup(func() { gkaRDeleteLeaseQuietly(e, ns, name) }) // runs after the controller stopped
	rig := gkaRStart(t, e, nil, func(o *controller.Options) {
		o.LeaderElection = controller.LeaderElectionOptions{
			Namespace: ns, ID: "", LeaseDuration: 2500 * time.Millisecond, RenewDeadline: 2 * time.Second, RetryPeriod: 500 * time.Millisecond,
		}
	})
	gkaRWaitHolder(t, e, ns, name, gkaRCtlID(rig.Ctl), 15*time.Second)
	l, _ := gkaRLease(e, ns, name)
	if l == nil || l.Spec.LeaseDurationSeconds == nil || *l.Spec.LeaseDurationSeconds != 2 {
		t.Errorf("GKA-020/142: leaseDurationSeconds = %v, want 2 (2.5s truncated, not rounded, to whole seconds)", l)
	}
	rig.Ctl.Cancel()
	if _, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
	if h, _ := gkaRLeaseHolder(e, ns, name); h != "" {
		t.Errorf("GKA-142: holderIdentity = %q after release", h)
	}
}

// ---------------------------------------------------------------------------
// GKA-143
// ---------------------------------------------------------------------------

func gkaRLongLease(o *controller.Options) {
	o.LeaderElection.LeaseDuration = 12 * time.Second
	o.LeaderElection.RenewDeadline = 8 * time.Second
	o.LeaderElection.RetryPeriod = 500 * time.Millisecond
}

func TestGKA143_NonLeaderNeverWritesAndGracefulFailoverIsFast(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit)
	a := gkaRStart(t, e, nil, gkaRLongLease)
	gkaRWaitHolder(t, e, "default", gkaRCtlLease(a.Ctl), gkaRCtlID(a.Ctl), 15*time.Second)
	gkaRWaitSettled(t, e, sc)
	b := gkaRStart(t, e, nil, gkaRLongLease)

	// B is a non-leader while A lives, even under churn.
	time.Sleep(500 * time.Millisecond)
	gkaRSetNodeLabels(t, e, sc.Nodes[0].Name, map[string]string{gkaRFleetLabelKey: sc.Selector[gkaRFleetLabelKey], "gka.churn": "1"})
	time.Sleep(700 * time.Millisecond)
	gkaRSetNodeLabels(t, e, sc.Nodes[0].Name, sc.Selector)
	time.Sleep(1 * time.Second)
	if b.A.AssessCount() != 0 || len(b.A.Forgotten()) != 0 {
		t.Errorf("GKA-143: the non-leader ran passes (assess=%d forget=%v)", b.A.AssessCount(), b.A.Forgotten())
	}
	if w := b.Rec.Writes(""); len(w) != 0 {
		t.Errorf("GKA-143: the non-leader wrote: %v", w)
	}
	if h, _ := gkaRLeaseHolder(e, "default", gkaRCtlLease(a.Ctl)); h != gkaRCtlID(a.Ctl) {
		t.Fatalf("GKA-142: holder = %q, want the first instance %q", h, gkaRCtlID(a.Ctl))
	}

	// Graceful stop: the Lease is released and the standby takes over quickly.
	tCancel := time.Now()
	a.Ctl.Cancel()
	if _, err := gkaRWaitDone(t, a.Ctl, 12*time.Second); err != nil {
		t.Errorf("GKA-022(a): first instance Run = %v, want nil", err)
	}
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return b.A.AssessCount() > 0, "the standby has not started a pass yet"
	})
	tFirst := b.A.Log()[0].At
	// Spec: <= 3 x RetryPeriod (1.5s) + first pass time. The first pass includes cache sync; 4 s of
	// allowance keeps the bound far below LeaseDuration (12s), so an expiry-based takeover still fails.
	if el := tFirst.Sub(tCancel); el > 1500*time.Millisecond+4*time.Second {
		t.Errorf("GKA-143: graceful failover took %s, want about 3 x RetryPeriod + first pass (Lease released, LeaseDuration is 12s)", el)
	}
	if h, _ := gkaRLeaseHolder(e, "default", gkaRCtlLease(b.Ctl)); h != gkaRCtlID(b.Ctl) {
		t.Errorf("GKA-142: after failover holder = %q, want %q", h, gkaRCtlID(b.Ctl))
	}

	// The new leader works: a new device gets its status from B and nothing from A.
	late := gkaRDevice(t, e, gkaName(t, "n0-late"), sc.Nodes[0], sc.Fleet)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return gkaRGetDevice(t, e, late.Name).Status.ObservedRequestID != "", "the new leader has not written the device status"
	})
	if len(b.Rec.Writes(late.Name)) == 0 {
		t.Errorf("GKA-143: the new leader's own recorder saw no write for %s", late.Name)
	}
	if w := gkaRWritesSince(a.Rec, "", gkaRCtlReturnedAt(a.Ctl).Add(100*time.Millisecond)); len(w) != 0 {
		t.Errorf("GKA-144: the stopped instance wrote after Run returned: %v", w)
	}
	gkaRAssertVerbs(t, a.Rec)
	gkaRAssertVerbs(t, b.Rec)
}

func TestGKA143_UncleanFailoverWithinBoundAndNewLeaderHasNoPassMemory(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit)
	a := gkaRStart(t, e, nil, nil)
	gkaRWaitHolder(t, e, "default", gkaRCtlLease(a.Ctl), gkaRCtlID(a.Ctl), 15*time.Second)
	gkaRWaitSettled(t, e, sc)
	b := gkaRStart(t, e, nil, nil)
	time.Sleep(1 * time.Second)

	// While B stands by, node 1 leaves the fleet: A (the leader) forgets it. B never saw it as assessable.
	gkaRSetNodeLabels(t, e, sc.Nodes[1].Name, nil)
	gkaEventually(t, 15*time.Second, func() (bool, string) {
		_, ok := gkaRTryNPS(e, sc.Nodes[1].Name)
		return !ok, "the leader has not removed the NodePathState of the unselected node"
	})

	// A stops renewing without releasing.
	a.Rec.InjectErr(func(req *http.Request) error {
		if req.Method == http.MethodPut && gkaRIsLease(req.URL.Path) {
			return errors.New("gka: injected connection failure")
		}
		return nil
	})
	t0 := time.Now()
	limit := 3*time.Second + 500*time.Millisecond + 2*time.Second
	select {
	case err := <-a.Ctl.Done:
		if !errors.Is(err, controller.ErrLeadershipLost) {
			t.Errorf("GKA-022(b): the instance that cannot renew returned %v, want ErrLeadershipLost", err)
		}
		if el := time.Since(t0); el > limit+time.Second {
			t.Errorf("GKA-022(b): losing leadership took %s, want <= %s", el, limit)
		}
	case <-time.After(limit + 8*time.Second):
		t.Fatal("GKA-022(b): the instance that cannot renew never returned")
	}
	gkaEventually(t, 20*time.Second, func() (bool, string) { return b.A.AssessCount() > 0, "the standby has not started a pass" })
	tFirst := b.A.Log()[0].At
	total := tFirst.Sub(t0)
	// Spec: <= LeaseDuration + 5 x RetryPeriod (6.5s) + first pass time (3 s allowance).
	if upper := 4*time.Second + 5*500*time.Millisecond + 3*time.Second; total > upper {
		t.Errorf("GKA-143: unclean failover took %s, want <= %s", total, upper)
	}
	// It must wait for the Lease to expire: the last renewal was at most one RetryPeriod before t0.
	if total < 3*time.Second {
		t.Errorf("GKA-143: the standby became active %s after the renewals stopped, before the Lease could have expired", total)
	}
	if w := gkaRWritesSince(a.Rec, "", tFirst.Add(100*time.Millisecond)); len(w) != 0 {
		t.Errorf("GKA-143/144: the old leader wrote after the new leader's first pass: %v", w)
	}

	// GKA-145: the new instance has no classification memory of node 1.
	time.Sleep(1 * time.Second)
	if f := b.A.Forgotten(); len(f) != 0 {
		t.Errorf("GKA-145: the new leader called ForgetNode(%v) although it never saw the node assessable", f)
	}
	for _, r := range b.A.Requests() {
		if r.Node.UID != string(sc.Nodes[0].UID) {
			t.Errorf("GKA-064: the new leader assessed node %q which is no longer selected", r.Node.Name)
		}
	}
	gkaRNoRequestsAfter(t, a.Rec, gkaRCtlReturnedAt(a.Ctl), "the old leader's return")
}

// ---------------------------------------------------------------------------
// GKA-144
// ---------------------------------------------------------------------------

func TestGKA144_NoNewWritesAfterCancelAndNoRequestsAfterReturn(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 3, 6, v1alpha1.FleetModeAudit) // 18 devices: many writes to interrupt
	rig := gkaRNewRig(e)
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if gkaRIsWrite(req.Method) && !gkaRIsLease(req.URL.Path) {
			time.Sleep(120 * time.Millisecond) // stretch every write so the pass is long
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		n := len(rig.Rec.WriteEntries(""))
		return n >= 3, fmt.Sprintf("only %d writes so far", n)
	})
	rig.Ctl.Cancel()
	tCancel := time.Now()
	el, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second)
	if err != nil || el >= 10*time.Second {
		t.Errorf("GKA-022(a): Run = (%v after %s), want nil within 10s", err, el)
	}
	// In-flight requests may finish; nothing new may start 100 ms after the cancel returned.
	if late := gkaRWritesSince(rig.Rec, "", tCancel.Add(100*time.Millisecond)); len(late) != 0 {
		t.Errorf("GKA-144: %d write(s) started more than 100ms after cancel: %v", len(late), late[0])
	}
	with := 0
	for _, d := range sc.Devices {
		if gkaRGetDevice(t, e, d.Name).Status.ObservedRequestID != "" {
			with++
		}
	}
	if with == len(sc.Devices) {
		t.Errorf("GKA-144: every device already had a status at cancel time: the scenario did not interrupt a pass")
	}
	gkaRNoRequestsAfter(t, rig.Rec, gkaRCtlReturnedAt(rig.Ctl), "Run's return")
}

// ---------------------------------------------------------------------------
// GKA-145
// ---------------------------------------------------------------------------

func TestGKA145_NewInstanceStartsWithoutPassMemory(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	node := sc.Nodes[0]
	uid := string(node.UID)

	// Instance 1 assesses the node, then stops.
	r1 := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	if r1.A.AssessCount() == 0 || len(r1.A.Forgotten()) != 0 {
		t.Fatalf("GKA-145: instance 1 assess=%d forget=%v", r1.A.AssessCount(), r1.A.Forgotten())
	}
	r1.Ctl.Cancel()
	if _, err := gkaRWaitDone(t, r1.Ctl, 12*time.Second); err != nil {
		t.Fatalf("Run = %v", err)
	}

	// The node leaves the fleet while nobody is running; instance 2 must not "forget" it.
	gkaRSetNodeLabels(t, e, node.Name, nil)
	r2 := gkaRStart(t, e, nil, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		_, ok := gkaRTryNPS(e, node.Name)
		return !ok, "the NodePathState of the unselected node still exists"
	})
	time.Sleep(1 * time.Second)
	if f := r2.A.Forgotten(); len(f) != 0 {
		t.Errorf("GKA-145: a fresh instance called ForgetNode(%v): its pass classification memory must start empty", f)
	}
	if r2.A.AssessCount() != 0 {
		t.Errorf("GKA-145: a fresh instance assessed an unselected node %d time(s)", r2.A.AssessCount())
	}

	// Positive control (GKA-070(e)): the same instance forgets a node it saw assessable.
	gkaRSetNodeLabels(t, e, node.Name, sc.Selector)
	gkaEventually(t, 20*time.Second, func() (bool, string) { return r2.A.AssessCount() > 0, "the node was not assessed after being selected" })
	gkaRSetNodeLabels(t, e, node.Name, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		f := r2.A.Forgotten()
		return len(f) == 1 && f[0] == uid, fmt.Sprintf("ForgetNode calls = %v, want exactly [%s]", f, uid)
	})
	time.Sleep(1 * time.Second)
	if f := r2.A.Forgotten(); len(f) != 1 {
		t.Errorf("GKA-070(e): ForgetNode must be called once per assessable-to-non-assessable transition, got %v", f)
	}
	log := r2.A.Log()
	seenForget := false
	for _, c := range log {
		if c.Kind == "forget" {
			seenForget = true
		} else if seenForget && c.NodeUID == uid {
			t.Errorf("GKA-070(e): the node was assessed again after ForgetNode without being selected")
		}
	}
	gkaRAssertVerbs(t, r2.Rec)
}
