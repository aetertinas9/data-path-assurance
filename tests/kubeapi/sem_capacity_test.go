package kubeapi_test

// GKA-063 (capacity), GKA-062 row 7, GKA-067 row 4, GKA-078 row 1, GKA-110/113
// interaction with capacity. One test owns the 1025 nodes: 1025 -> 1024 -> 1025
// -> Enforce, so the expensive fixture is created once (GKA-177: 90 s budget).

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

func TestGKA063_CapacityBoundary1024And1025(t *testing.T) {
	if testing.Short() {
		t.Skip("GKA-063 capacity test creates 1025 Nodes")
	}
	w := newGkaSWorld(t)
	fast := gkaSFastClient(t, w.e)
	const total = 1025

	names := make([]string, total)
	inSet := map[string]bool{}
	for i := range names {
		names[i] = w.name(fmt.Sprintf("cap%04d", i))
		inSet[names[i]] = true
	}
	extraName := w.name("capxtra")
	inSet[extraName] = true

	// Bulk cleanup with the fast client. Registered after the world cleanups and
	// before the controller starts, so it runs after the controller stopped and
	// before the sequential harness cleanup (which would take minutes for 2000 objects).
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		list := &v1alpha1.NodePathStateList{}
		if err := fast.List(ctx, list); err == nil {
			var doomed []string
			for _, p := range list.Items {
				if inSet[p.Name] {
					doomed = append(doomed, p.Name)
				}
			}
			_ = gkaSParallel(len(doomed), 32, func(i int) error {
				return client.IgnoreNotFound(fast.Delete(ctx, &v1alpha1.NodePathState{ObjectMeta: metav1.ObjectMeta{Name: doomed[i]}}))
			})
		}
		all := append(append([]string(nil), names...), extraName)
		_ = gkaSParallel(len(all), 32, func(i int) error {
			return client.IgnoreNotFound(fast.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: all[i]}}))
		})
	})

	createNode := func(name string) (*corev1.Node, error) {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{gkaSSelKey: w.sel}}}
		ctx, cancel := gkaSTO()
		defer cancel()
		return n, fast.Create(ctx, n)
	}
	nodes := make([]*corev1.Node, total)
	if err := gkaSParallel(total, 32, func(i int) error {
		n, err := createNode(names[i])
		nodes[i] = n
		return err
	}); err != nil {
		t.Fatalf("creating %d nodes: %v", total, err)
	}
	f := w.selFleet("f")
	node0, last := nodes[0], nodes[total-1]
	dcap := w.device("dcap", node0, f)
	// A device with a wrong fleet UID stays UIDMismatch (row 1) even at capacity (row 7).
	dwrong := w.device("dwrong", nodes[1], f, func(d *v1alpha1.GPUDevice) { d.Spec.FleetRef.UID = "not-the-uid" })

	a := gkaSNewAssessor(newGkaSScript(gkaSPlanReady))
	cfg := rest.CopyConfig(w.e.Config)
	cfg.QPS, cfg.Burst = 1000, 2000
	gkaSStart(t, cfg, a, newGkaFakeClock(gkaST0), func(o *controller.Options) {
		o.ResyncInterval = 2 * time.Second
		o.LeaderElection.LeaseDuration = 15 * time.Second
		o.LeaderElection.RenewDeadline = 10 * time.Second
		o.LeaderElection.RetryPeriod = 2 * time.Second
	})

	condNodes := func() (published []string) {
		ctx, cancel := gkaSTO()
		defer cancel()
		list := &corev1.NodeList{}
		if err := fast.List(ctx, list); err != nil {
			t.Fatalf("list nodes: %v", err)
		}
		for i := range list.Items {
			if inSet[list.Items[i].Name] && gkaSNodeCond(&list.Items[i]) != nil {
				published = append(published, list.Items[i].Name)
			}
		}
		return published
	}
	ownNPS := func() (out []string) {
		for _, p := range w.listNPS("") {
			if inSet[p.Name] {
				out = append(out, p.Name)
			}
		}
		return out
	}
	exceeded := func(o *v1alpha1.GPUFleet) string {
		if o.Status.SelectedCount != total {
			return fmt.Sprintf("selectedCount = %d, want %d", o.Status.SelectedCount, total)
		}
		return gkaSFleetIs(o, "Unknown", "TargetCapacityExceeded")
	}

	// 1025 selected nodes: the whole fleet is over capacity (GKA-063, GKA-078 row 1).
	fl := gkaSWaitFleetFor(t, w.e, 60*time.Second, f.Name, exceeded)
	gkaSCheckFleet(t, "GKA-063 1025", fl, gkaSFleetWant{
		Selected: total, Unknown: total, Status: "Unknown", Reason: "TargetCapacityExceeded",
		Message: "selected=1025 ready=0 degraded=0 unknown=1025",
	})
	w.waitDevice(dcap.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "TargetCapacityExceeded") })
	gkaSCheckNonDecision(t, "GKA-062 row 7", w.deviceNow(dcap.Name), gkaSNonDec{SR: "TargetCapacityExceeded", Phase: "Pending"})
	w.waitDevice(dwrong.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "UIDMismatch") })
	gkaSCheckNonDecision(t, "GKA-062 row 1 beats row 7", w.deviceNow(dwrong.Name), gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"})
	time.Sleep(5 * time.Second) // >= two 2 s ticks
	if got := len(a.Requests()); got != 0 {
		t.Errorf("GKA-063: assessor called %d times for a fleet over capacity, want 0", got)
	}
	if got := ownNPS(); len(got) != 0 {
		t.Errorf("GKA-063: %d NodePathStates were created for a fleet over capacity, want none", len(got))
	}
	if got := condNodes(); len(got) != 0 {
		t.Errorf("GKA-063/110: %d Node conditions were published for a fleet over capacity, want none (no new publication)", len(got))
	}

	// Overlap beats capacity (GKA-067 row 3 before row 4, GKA-062 row 6 before row 7): a second fleet also
	// selects one node of the 1025-node fleet. That node is Conflict: its NodePathState is created although the
	// fleet is over capacity, the condition names both fleets, its devices are Conflict, nothing is assessed.
	nOv := nodes[2]
	w.updateNode(nOv.Name, func(o *corev1.Node) { o.Labels["gka-sem/ov"] = w.sel })
	g := w.fleet("g", metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/ov": w.sel}})
	dOvF := w.device("dovf", nOv, f)
	dOvG := w.device("dovg", nOv, g)
	w.waitNPS(nOv.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	gkaSCheckNPS(t, "GKA-067 row 3 beats row 4", w.npsNow(nOv.Name), gkaSNPSWant{Elig: "Unknown", Reason: "Conflict"})
	w.waitNode(nOv.Name, func(o *corev1.Node) string {
		return gkaSNodeCondIs(o, &gkaSC{gkaSNodeCondType, "Unknown", "Conflict", "fleet=" + gkaSNameList([]string{f.Name, g.Name})})
	})
	for _, d := range []*v1alpha1.GPUDevice{dOvF, dOvG} {
		w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Conflict") })
		gkaSCheckNonDecision(t, "GKA-062 row 6 beats row 7 "+d.Name, w.deviceNow(d.Name), gkaSNonDec{SR: "Conflict", Phase: "Pending"})
	}
	w.waitFleet(g.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Conflict") })
	gkaSCheckFleet(t, "GKA-061 the overlapping fleet", w.fleetNow(g.Name), gkaSFleetWant{
		Selected: 1, Unknown: 1, Status: "Unknown", Reason: "Conflict", Message: "selected=1 ready=0 degraded=0 unknown=1",
	})
	if o := w.fleetNow(f.Name); exceeded(o) != "" {
		t.Errorf("GKA-063: the big fleet left row 1 because of the overlap: %s", exceeded(o))
	}
	if got := len(a.Requests()); got != 0 {
		t.Errorf("GKA-061/063: assessor called %d times while the only assessable-looking node is a Conflict node", got)
	}
	// The overlap ends: the node falls back to capacity (device row 7); its published condition is updated by GKA-063.
	w.deleteFleet(g.Name)
	w.waitDevice(dOvF.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "TargetCapacityExceeded") })
	w.waitDevice(dOvG.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "NoMatchingDevices") })
	w.waitNPS(nOv.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "TargetCapacityExceeded") })
	w.waitNode(nOv.Name, func(o *corev1.Node) string {
		return gkaSNodeCondIs(o, &gkaSC{gkaSNodeCondType, "Unknown", "TargetCapacityExceeded", "fleet=" + f.Name})
	})

	// Exactly 1024 is within capacity: assessed, state created, condition published.
	w.deleteNode(last.Name)
	gkaSWaitFleetFor(t, w.e, 60*time.Second, f.Name, func(o *v1alpha1.GPUFleet) string {
		if o.Status.SelectedCount != total-1 {
			return fmt.Sprintf("selectedCount = %d, want %d", o.Status.SelectedCount, total-1)
		}
		c := gkaSFleetReadyOf(o)
		if c == nil || c.Reason == "TargetCapacityExceeded" {
			return "FleetReady still TargetCapacityExceeded at exactly 1024 nodes"
		}
		return ""
	})
	w.waitNPS(node0.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNode(node0.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + f.Name})
	})
	w.waitDevice(dcap.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "Ready") })
	if len(gkaSRequestsFor(a, node0.Name)) == 0 {
		t.Errorf("GKA-063: assessor never called at exactly 1024 selected nodes")
	}

	// Back to 1025: no new NodePathState, existing ones flip to Unknown/TargetCapacityExceeded
	// (never deleted), existing conditions flip too, the assessor is not called any more.
	if _, err := createNode(extraName); err != nil {
		t.Fatalf("creating the 1025th node again: %v", err)
	}
	gkaSWaitFleetFor(t, w.e, 60*time.Second, f.Name, exceeded)
	var existing []string
	gkaEventually(t, 60*time.Second, func() (bool, string) {
		existing = ownNPS()
		if len(existing) == 0 {
			return false, "no NodePathState exists yet"
		}
		for _, name := range existing {
			p, err := gkaSGetNPS(w.e, name)
			if err != nil {
				return false, "get " + name + ": " + err.Error()
			}
			if why := gkaSNPSHas(p, "TargetCapacityExceeded"); why != "" {
				return false, name + ": " + why
			}
		}
		return true, ""
	})
	gkaEventually(t, 60*time.Second, func() (bool, string) {
		for _, name := range condNodes() {
			n, err := gkaSGetNode(w.e, name)
			if err != nil {
				return false, err.Error()
			}
			if why := gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "TargetCapacityExceeded", "fleet=" + f.Name}); why != "" {
				return false, name + ": " + why
			}
		}
		return true, ""
	})
	publishedThen := condNodes()
	callsThen := len(a.Requests())
	time.Sleep(5 * time.Second)
	if now := ownNPS(); len(now) != len(existing) {
		t.Errorf("GKA-063: NodePathState count changed %d -> %d while over capacity (create or delete)", len(existing), len(now))
	}
	if w.hasNPS(extraName) {
		t.Errorf("GKA-063: a NodePathState exists for the node that pushed the fleet over capacity")
	}
	if now := condNodes(); len(now) != len(publishedThen) {
		t.Errorf("GKA-063/110: published Node conditions changed %d -> %d while over capacity", len(publishedThen), len(now))
	}
	if callsNow := len(a.Requests()); callsNow != callsThen {
		t.Errorf("GKA-063: assessor calls grew %d -> %d while over capacity", callsThen, callsNow)
	}
	gkaSCheckNPS(t, "GKA-063 existing NodePathState", w.npsNow(node0.Name), gkaSNPSWant{Elig: "Unknown", Reason: "TargetCapacityExceeded"})
	gkaSCheckNonDecision(t, "GKA-062 row 7 again", w.deviceNow(dcap.Name), gkaSNonDec{SR: "TargetCapacityExceeded", Phase: "Pending"})
	gkaSCheckFleet(t, "GKA-063 1025 again", w.fleetNow(f.Name), gkaSFleetWant{
		Selected: total, Unknown: total, Status: "Unknown", Reason: "TargetCapacityExceeded", Message: "selected=1025 ready=0 degraded=0 unknown=1025",
	})

	// Enforce: FleetReady row 1 is mode independent (+ the token), existing NodePathStates
	// carry the token in NodeEligible, and every published Node condition is removed (GKA-110).
	w.updateFleet(f.Name, gkaSEnforce)
	gkaSWaitFleetFor(t, w.e, 60*time.Second, f.Name, func(o *v1alpha1.GPUFleet) string {
		if why := exceeded(o); why != "" {
			return why
		}
		if c := gkaSFleetReadyOf(o); c.Message != "selected=1025 ready=0 degraded=0 unknown=1025; gate_not_implemented" {
			return fmt.Sprintf("message = %q", c.Message)
		}
		return ""
	})
	gkaEventually(t, 60*time.Second, func() (bool, string) {
		if got := condNodes(); len(got) != 0 {
			return false, fmt.Sprintf("%d Node conditions still published for an Enforce fleet", len(got))
		}
		return true, ""
	})
	gkaSCheckNPS(t, "GKA-063/077 Enforce", w.waitNPS(node0.Name, func(p *v1alpha1.NodePathState) string {
		c := gkaSCondOf(p.Status.Conditions, "NodeEligible")
		if c == nil || c.Message != "eligibility=Unknown qualification=Unknown; gate_not_implemented" {
			return "NodeEligible message does not carry gate_not_implemented yet"
		}
		return ""
	}), gkaSNPSWant{Elig: "Unknown", Reason: "TargetCapacityExceeded", Enforce: true})
}
