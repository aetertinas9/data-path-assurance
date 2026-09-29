package kubeapi_test

// GKA-061 (overlap = Conflict), GKA-066 (invalid nodeSelector) and GKA-068
// (fail-closed hold for a fleet whose selector became invalid).

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// gkaSPrepareBystander gives a Node facts the controller must never change:
// an annotation, a taint and spec.unschedulable (GKA-061, GKA-121).
func gkaSPrepareBystander(w *gkaSWorld, name string) string {
	w.t.Helper()
	w.updateNode(name, func(n *corev1.Node) {
		if n.Annotations == nil {
			n.Annotations = map[string]string{}
		}
		n.Annotations["gka-sem/note"] = "keep"
		n.Spec.Unschedulable = true
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "gka-sem/t", Value: "v", Effect: corev1.TaintEffectNoSchedule})
	})
	return gkaSNodeFacts(w.nodeNow(name))
}

// GKA-061 (+GKA-100 for Conflict nodes, GKA-110/111/113 values): a node selected
// by two Audit fleets is Conflict: no assessor call, NodePathState Unknown/Conflict,
// counted unknown by both fleets, Node condition Unknown/Conflict naming both
// fleets, and nothing else on the Node changes. Deleting one fleet resolves it.
func TestGKA061_OverlapIsConflict(t *testing.T) {
	w := newGkaSWorld(t)
	keyA, keyB := "gka-sem/a", "gka-sem/b"
	nN := w.node("nn", map[string]string{keyA: w.sel, keyB: w.sel}) // selected by A and B
	nM := w.node("nm", map[string]string{keyA: w.sel})              // only A
	nP := w.node("np", map[string]string{keyB: w.sel})              // only B
	fA := w.fleet("fa", metav1.LabelSelector{MatchLabels: map[string]string{keyA: w.sel}})
	fB := w.fleet("fb", metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: keyB, Operator: metav1.LabelSelectorOpExists}}})
	dA := w.device("da", nN, fA)
	dB := w.device("db", nN, fB)
	dM := w.device("dm", nM, fA)
	dP := w.device("dp", nP, fB)
	factsBefore := gkaSPrepareBystander(w, nN.Name)

	a := gkaSNewAssessor(newGkaSScript(gkaSPlanReady))
	w.start(a, newGkaFakeClock(gkaST0))

	bothNames := gkaSNameList([]string{fA.Name, fB.Name})
	conflictCond := &gkaSC{gkaSNodeCondType, "Unknown", "Conflict", "fleet=" + bothNames}
	w.waitNode(nN.Name, func(n *corev1.Node) string { return gkaSNodeCondIs(n, conflictCond) })
	w.waitNode(nM.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name})
	})
	w.waitNode(nP.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fB.Name})
	})
	for _, f := range []*v1alpha1.GPUFleet{fA, fB} {
		w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Conflict") })
	}
	gkaSSettle(3)

	// NodePathState of the Conflict node: created (GKA-100), Unknown/Conflict, and the
	// union of every D(N, F) (GKA-076) with each device in its own Conflict scope.
	nps := w.waitNPS(nN.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	gkaSCheckNPS(t, "GKA-061 NodePathState", nps, gkaSNPSWant{Elig: "Unknown", Reason: "Conflict"})
	sums := gkaSSummaries(nps)
	if len(sums) != 2 || sums[dA.Name].UID != string(dA.UID) || sums[dB.Name].UID != string(dB.UID) {
		t.Errorf("GKA-076: Conflict node deviceSummaries = %#v, want the union {%s, %s}", nps.Status.DeviceSummaries, dA.Name, dB.Name)
	}
	for _, d := range []*v1alpha1.GPUDevice{dA, dB} {
		gkaSCheckNonDecision(t, "GKA-062 row 6 "+d.Name, w.deviceNow(d.Name), gkaSNonDec{SR: "Conflict", Phase: "Pending"})
	}
	// Fleets: A = {nN (Conflict), nM (Eligible)} -> row 5.
	gkaSCheckFleet(t, "GKA-061 fleet A", w.fleetNow(fA.Name), gkaSFleetWant{
		Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Conflict", Message: "selected=2 ready=1 degraded=0 unknown=1",
	})
	gkaSCheckFleet(t, "GKA-061 fleet B", w.fleetNow(fB.Name), gkaSFleetWant{
		Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Conflict", Message: "selected=2 ready=1 degraded=0 unknown=1",
	})
	// The assessor never saw the Conflict node, and never had a reason to forget it.
	if got := gkaSRequestsFor(a, nN.Name); len(got) != 0 {
		t.Errorf("GKA-061: assessor called %d times for the Conflict node", len(got))
	}
	for _, uid := range a.Forgotten() {
		if uid == string(nN.UID) {
			t.Errorf("GKA-070(e): ForgetNode(%s) for a node that was never assessable", uid)
		}
	}
	// Nothing but the condition changed on the Conflict node.
	if got := gkaSNodeFacts(w.nodeNow(nN.Name)); got != factsBefore {
		t.Errorf("GKA-061/121: node facts changed:\n before %s\n after  %s", factsBefore, got)
	}
	_, _ = dM, dP

	// Resolving the overlap (delete B): nN becomes assessable for A only; nP is now
	// selected by nobody, so its NodePathState and condition disappear (GKA-102(b), 113).
	w.deleteFleet(fB.Name)
	w.waitNPS(nN.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNPSGone(nP.Name)
	w.waitNode(nP.Name, func(n *corev1.Node) string { return gkaSNodeCondIs(n, nil) })
	w.waitNode(nN.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name})
	})
	reqs := gkaSRequestsFor(a, nN.Name)
	if len(reqs) == 0 || reqs[len(reqs)-1].Selection != fleet.SelectionComplete || strings.Join(gkaSIntentNames(reqs[len(reqs)-1]), ",") != dA.Name {
		t.Errorf("GKA-064: after the overlap ended, requests for the node = %#v, want Complete with intent %s", reqs, dA.Name)
	}
	w.waitDevice(dB.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "NoMatchingDevices") })
	w.waitFleet(fA.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "True", "Ready") })
	gkaSCheckFleet(t, "GKA-061 fleet A after resolve", w.fleetNow(fA.Name), gkaSFleetWant{
		Selected: 2, Ready: 2, Status: "True", Reason: "Ready", Message: "selected=2 ready=2 degraded=0 unknown=0", Revision: "*",
	})
}

// GKA-061/086/110/077: an Enforce fleet in the overlap: the Node gets no condition
// (Enforce is CRD-status only), NodeEligible carries gate_not_implemented when any
// fleet of S(N) is Enforce, the Audit fleet's own message does not, and no gate
// object is written. Switching the Enforce fleet to Audit publishes the condition.
func TestGKA061_086_ConflictWithEnforce(t *testing.T) {
	w := newGkaSWorld(t)
	keyA, keyE := "gka-sem/a", "gka-sem/e"
	nN := w.node("nn", map[string]string{keyA: w.sel, keyE: w.sel}) // Audit + Enforce
	nE := w.node("ne", map[string]string{keyE: w.sel})              // Enforce only
	fA := w.fleet("fa", metav1.LabelSelector{MatchLabels: map[string]string{keyA: w.sel}})
	fE := w.fleet("fe", metav1.LabelSelector{MatchLabels: map[string]string{keyE: w.sel}}, gkaSEnforce)
	dE := w.device("de", nE, fE)
	dA := w.device("da", nN, fA) // a D(N, A) member keeps fleet A out of GKA-078 row 3
	factsN := gkaSPrepareBystander(w, nN.Name)
	factsE := gkaSPrepareBystander(w, nE.Name)

	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	w.waitNPS(nN.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	w.waitNPS(nE.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitFleet(fE.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Conflict") })
	w.waitFleet(fA.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Conflict") })
	gkaSSettle(4)

	gkaSCheckNPS(t, "GKA-077 Audit+Enforce conflict node", w.npsNow(nN.Name), gkaSNPSWant{Elig: "Unknown", Reason: "Conflict", Enforce: true})
	gkaSCheckNPS(t, "GKA-077 Enforce node", w.npsNow(nE.Name), gkaSNPSWant{
		Elig: "True", Reason: "Ready", Qual: "Qualified", Enforce: true, FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: "*",
	})
	// GKA-110: neither node gets a Node condition while an Enforce fleet selects it.
	gkaSCheckNodeCond(t, "GKA-110 Audit+Enforce conflict node", w.nodeNow(nN.Name), nil)
	gkaSCheckNodeCond(t, "GKA-110 Enforce node", w.nodeNow(nE.Name), nil)
	// GKA-086: the Enforce fleet's own message carries the token, the Audit fleet's does not.
	gkaSCheckFleet(t, "GKA-086 Enforce fleet", w.fleetNow(fE.Name), gkaSFleetWant{
		Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Conflict", Message: "selected=2 ready=1 degraded=0 unknown=1; gate_not_implemented",
	})
	gkaSCheckFleet(t, "GKA-086 Audit fleet", w.fleetNow(fA.Name), gkaSFleetWant{
		Selected: 1, Unknown: 1, Status: "Unknown", Reason: "Conflict", Message: "selected=1 ready=0 degraded=0 unknown=1",
	})
	// No taint/annotation/finalizer/ownership is written (GKA-086, GKA-002).
	if got := gkaSNodeFacts(w.nodeNow(nN.Name)); got != factsN {
		t.Errorf("GKA-086: conflict node facts changed:\n before %s\n after  %s", factsN, got)
	}
	if got := gkaSNodeFacts(w.nodeNow(nE.Name)); got != factsE {
		t.Errorf("GKA-086: Enforce node facts changed:\n before %s\n after  %s", factsE, got)
	}
	for _, f := range []*v1alpha1.GPUFleet{w.fleetNow(fA.Name), w.fleetNow(fE.Name)} {
		if len(f.Finalizers) != 0 || len(f.Status.OwnedNodeRefs) != 0 {
			t.Errorf("GKA-086/105: fleet %s finalizers=%v ownedNodeRefs=%v, want none", f.Name, f.Finalizers, f.Status.OwnedNodeRefs)
		}
	}
	_, _ = dE, dA

	// Enforce -> Audit: the Enforce-only node now publishes; the overlap is Audit+Audit.
	w.updateFleet(fE.Name, func(f *v1alpha1.GPUFleet) { f.Spec.Mode = v1alpha1.FleetMode("Audit") })
	w.waitNode(nE.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fE.Name})
	})
	both := gkaSNameList([]string{fA.Name, fE.Name})
	w.waitNode(nN.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "Conflict", "fleet=" + both})
	})
	w.waitNPS(nN.Name, func(p *v1alpha1.NodePathState) string {
		c := gkaSCondOf(p.Status.Conditions, "NodeEligible")
		if c == nil || strings.Contains(c.Message, "gate_not_implemented") {
			return "NodeEligible still carries gate_not_implemented or is missing"
		}
		return ""
	})
	// And back to Enforce: both conditions are removed again (GKA-110, GKA-113).
	w.updateFleet(fE.Name, gkaSEnforce)
	w.waitNode(nE.Name, func(n *corev1.Node) string { return gkaSNodeCondIs(n, nil) })
	w.waitNode(nN.Name, func(n *corev1.Node) string { return gkaSNodeCondIs(n, nil) })
}

// GKA-066 (+GKA-084/087 InternalError rendering): a nodeSelector that does not
// convert (In without values, unknown operator, invalid label value, invalid key)
// makes the fleet select nothing and render InternalError with the minimal status;
// its devices reach row 4; other fleets are unaffected in the same pass; no
// NodePathState or Node condition is created for its node. The "both lists empty"
// branch is unreachable through the API (CEL F1 rejects it) and is not tested.
func TestGKA066_InvalidNodeSelector(t *testing.T) {
	w := newGkaSWorld(t)
	nX := w.node("nx", nil) // referenced by the devices of the broken fleets only
	nV := w.selNode("nv")
	valid := w.selFleet("valid")
	dV := w.device("dv", nV, valid)
	sel := func(key string, op metav1.LabelSelectorOperator, vals ...string) metav1.LabelSelector {
		return metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: key, Operator: op, Values: vals}}}
	}
	type variant struct {
		name    string
		sel     metav1.LabelSelector
		mut     func(*v1alpha1.GPUFleet)
		enforce bool
	}
	variants := []variant{
		{name: "in-no-values", sel: sel("gka-sem/k", metav1.LabelSelectorOpIn)},
		{name: "notin-no-values", sel: sel("gka-sem/k", metav1.LabelSelectorOpNotIn)},
		{name: "bogus-operator", sel: sel("gka-sem/k", metav1.LabelSelectorOperator("Bogus"), "v")},
		{name: "exists-with-values", sel: sel("gka-sem/k", metav1.LabelSelectorOpExists, "v")},
		{name: "invalid-value", sel: metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/k": "not a valid value!"}}},
		{name: "invalid-key", sel: sel("not a valid key", metav1.LabelSelectorOpExists)},
		{name: "enforce", sel: sel("gka-sem/k", metav1.LabelSelectorOpIn), mut: gkaSEnforce, enforce: true},
	}
	type made struct {
		v variant
		f *v1alpha1.GPUFleet
		d *v1alpha1.GPUDevice
	}
	var all []made
	for _, v := range variants {
		var muts []func(*v1alpha1.GPUFleet)
		if v.mut != nil {
			muts = append(muts, v.mut)
		}
		f := w.fleet("b-"+v.name, v.sel, muts...)
		all = append(all, made{v, f, w.device("d-"+v.name, nX, f)})
	}

	a := gkaSNewAssessor(newGkaSScript(gkaSPlanReady))
	w.start(a, newGkaFakeClock(gkaST0))

	for _, m := range all {
		w.waitFleet(m.f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
		w.waitDevice(m.d.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "InternalError") })
	}
	w.waitFleet(valid.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "True", "Ready") })
	gkaSSettle(3)

	for _, m := range all {
		msg := "selected=0 ready=0 degraded=0 unknown=0; internal error: invalid nodeSelector"
		if m.v.enforce {
			msg += "; gate_not_implemented" // GKA-087 / GKA-074 token order
		}
		gkaSCheckFleet(t, "GKA-066/087 "+m.v.name, w.fleetNow(m.f.Name), gkaSFleetWant{Status: "Unknown", Reason: "InternalError", Message: msg})
		gkaSCheckNonDecision(t, "GKA-062 row 4 "+m.v.name, w.deviceNow(m.d.Name), gkaSNonDec{
			SR: "InternalError", Phase: "Unknown", Tail: []string{"internal error: invalid nodeSelector"},
		})
	}
	// The broken fleets select nothing: no NodePathState, no Node condition for nX.
	if w.hasNPS(nX.Name) {
		t.Errorf("GKA-066/068: NodePathState created for %s, a node only broken fleets point at", nX.Name)
	}
	gkaSCheckNodeCond(t, "GKA-066", w.nodeNow(nX.Name), nil)
	// The valid fleet is rendered normally in the same passes (GKA-070(h), GKA-084).
	gkaSCheckFleet(t, "GKA-084 valid fleet", w.fleetNow(valid.Name), gkaSFleetWant{
		Selected: 1, Ready: 1, Status: "True", Reason: "Ready", Message: "selected=1 ready=1 degraded=0 unknown=0", Revision: "*",
	})
	if got := gkaSRequestsFor(a, nX.Name); len(got) != 0 {
		t.Errorf("GKA-066: assessor called %d times for a node no valid fleet selects", len(got))
	}
	_ = dV
}

// GKA-068: fail-closed hold. A typo in a selector must not look like "released":
// the node that a device of the broken fleet points at keeps its NodePathState and
// Node condition, both flipped to Unknown/InternalError, while unrelated nodes are
// released normally. Node deletion and UID replacement still delete the state.
func TestGKA068_InvalidSelectorHoldsExistingState(t *testing.T) {
	w := newGkaSWorld(t)
	nH1 := w.selNode("h1") // a device of the fleet points here
	nH2 := w.selNode("h2") // no device points here
	fA := w.selFleet("fa")
	d1 := w.device("d1", nH1, fA)
	script := newGkaSScript(gkaSPlanReady)
	a := gkaSNewAssessor(script)
	w.start(a, newGkaFakeClock(gkaST0))

	invalidSel := func(f *v1alpha1.GPUFleet) {
		f.Spec.NodeSelector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: gkaSSelKey, Operator: metav1.LabelSelectorOpIn}}}
	}
	validSel := func(f *v1alpha1.GPUFleet) {
		f.Spec.NodeSelector = metav1.LabelSelector{MatchLabels: map[string]string{gkaSSelKey: w.sel}}
	}
	const internal = "internal error: invalid nodeSelector"

	// Phase 1: healthy.
	w.waitNPS(nH1.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNPS(nH2.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "NoMatchingDevices") })
	w.waitNode(nH1.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name})
	})
	w.waitNode(nH2.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "NoMatchingDevices", "fleet=" + fA.Name})
	})
	uid1 := w.npsNow(nH1.Name).UID

	// Phase 2: the selector becomes invalid.
	w.updateFleet(fA.Name, invalidSel)
	w.waitFleet(fA.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
	w.waitNPSGone(nH2.Name) // released normally (GKA-070(h)): no device of the broken fleet points here
	w.waitNode(nH2.Name, func(n *corev1.Node) string { return gkaSNodeCondIs(n, nil) })
	w.waitNPS(nH1.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	w.waitNode(nH1.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "InternalError", gkaSMsg("fleet="+fA.Name, internal)})
	})
	w.waitDevice(d1.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "InternalError") })
	gkaSSettle(5)
	holdNPS := w.npsNow(nH1.Name)
	if holdNPS.UID != uid1 {
		t.Errorf("GKA-068: NodePathState was deleted and recreated during the hold (uid %s -> %s)", uid1, holdNPS.UID)
	}
	gkaSCheckNPS(t, "GKA-068 hold", holdNPS, gkaSNPSWant{
		Elig: "Unknown", Reason: "InternalError", NodeTail: []string{internal}, FreshTail: []string{internal},
	})
	if ds := gkaSSummaries(holdNPS)[d1.Name]; len(holdNPS.Status.DeviceSummaries) != 1 || ds.Qualification != "Unknown" || ds.LifecyclePhase != "Unknown" || ds.DesiredState != "InService" {
		t.Errorf("GKA-076: hold deviceSummaries = %#v, want the device with qualification/phase Unknown", holdNPS.Status.DeviceSummaries)
	}
	gkaSCheckFleet(t, "GKA-066 broken fleet", w.fleetNow(fA.Name), gkaSFleetWant{
		Status: "Unknown", Reason: "InternalError", Message: "selected=0 ready=0 degraded=0 unknown=0; " + internal,
	})
	gkaSCheckNonDecision(t, "GKA-062 row 4 hold device", w.deviceNow(d1.Name), gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{internal}})

	// Phase 3: the selector is fixed: everything returns; nH2 is created afresh.
	w.updateFleet(fA.Name, validSel)
	w.waitNPS(nH1.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNPS(nH2.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "NoMatchingDevices") })
	w.waitNode(nH1.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name})
	})
	if w.npsNow(nH1.Name).UID != uid1 {
		t.Errorf("GKA-068: the held NodePathState was replaced when the selector became valid again")
	}

	// Phase 4: invalid again, then the node disappears: deletion is not held (GKA-102(a)).
	w.updateFleet(fA.Name, invalidSel)
	w.waitNPS(nH1.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	w.deleteNode(nH1.Name)
	w.waitNPSGone(nH1.Name)
	// The same name comes back with a new UID: still invalid selector, still no NodePathState
	// is created (hold never creates) and no condition is published; the device sees row 2.
	nH1b := w.node("h1", map[string]string{gkaSSelKey: w.sel})
	if nH1b.UID == nH1.UID {
		t.Fatalf("test setup: the recreated node reused the UID")
	}
	w.waitDevice(d1.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "UIDMismatch") })
	gkaSSettle(5)
	if w.hasNPS(nH1.Name) {
		t.Errorf("GKA-068: a NodePathState was created for %s under a broken selector", nH1.Name)
	}
	gkaSCheckNodeCond(t, "GKA-068 recreated node", w.nodeNow(nH1.Name), nil)

	// Phase 5 (GKA-068 "B's mode does not matter"): the stale device goes away, a fresh
	// device on the recreated node is published under a valid selector, and then the
	// fleet turns Enforce with a broken selector: the condition is still updated.
	w.deleteDevice(d1.Name)
	w.updateFleet(fA.Name, validSel)
	d2 := w.device("d2", nH1b, fA)
	w.waitNode(nH1b.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name})
	})
	w.updateFleet(fA.Name, func(f *v1alpha1.GPUFleet) {
		invalidSel(f)
		gkaSEnforce(f)
	})
	w.waitNode(nH1b.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "InternalError", gkaSMsg("fleet="+fA.Name, internal)})
	})
	w.waitFleet(fA.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
	gkaSCheckFleet(t, "GKA-087 Enforce broken fleet", w.fleetNow(fA.Name), gkaSFleetWant{
		Status: "Unknown", Reason: "InternalError", Message: "selected=0 ready=0 degraded=0 unknown=0; " + internal + "; gate_not_implemented",
	})
	w.waitDevice(d2.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "InternalError") })
}

// GKA-076 (hold row) + GKA-068: the deviceSummaries of a held NodePathState list every device that
// points at the node with the broken fleet's name and show phase and qualification Unknown for ALL of
// them, also for devices whose own scope is not InternalError (UIDMismatch, IdentityConflict), whose
// own status keeps the baseline phase.
func TestGKA068_HoldDeviceSummariesOverrideOtherScopes(t *testing.T) {
	w := newGkaSWorld(t)
	nH := w.selNode("h")
	fA := w.selFleet("fa")
	dOK := w.device("dok", nH, fA)
	dU := w.device("du", nH, fA, func(d *v1alpha1.GPUDevice) { d.Spec.FleetRef.UID = "not-the-uid" }) // scope UIDMismatch
	dupC := gkaSPtr("GPU-" + w.name("dupc"))
	dI1 := w.device("di1", nH, fA, gkaSWithClaim("NVIDIA", dupC, nil)) // scope IdentityConflict
	dI2 := w.device("di2", nH, fA, gkaSWithClaim("NVIDIA", dupC, nil))
	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	// Before the selector breaks the node is Partial (X* is not empty) and summarizes D(N, F) only.
	w.waitNPS(nH.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	before := gkaSSummaries(w.npsNow(nH.Name))
	if _, has := before[dU.Name]; has || len(before) != 3 {
		t.Fatalf("test setup: the healthy NodePathState summarizes %#v, want D(N, F) = {%s, %s, %s}", before, dOK.Name, dI1.Name, dI2.Name)
	}

	w.updateFleet(fA.Name, func(f *v1alpha1.GPUFleet) {
		f.Spec.NodeSelector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: gkaSSelKey, Operator: metav1.LabelSelectorOpIn}}}
	})
	w.waitFleet(fA.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
	w.waitNPS(nH.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	for _, d := range []*v1alpha1.GPUDevice{dOK, dU, dI1, dI2} {
		w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string {
			if o.Status.Conditions == nil {
				return "no status"
			}
			return ""
		})
	}
	gkaSSettle(4)
	hold := w.npsNow(nH.Name)

	// The devices' own statuses: row 4 for the healthy one, rows 1 and 3 keep their baseline phase.
	own := map[string]gkaSNonDec{
		dOK.Name: {SR: "InternalError", Phase: "Unknown", Tail: []string{"internal error: invalid nodeSelector"}},
		dU.Name:  {SR: "UIDMismatch", Phase: "Pending"},
		dI1.Name: {SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + dI2.Name},
		dI2.Name: {SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + dI1.Name},
	}
	for name, x := range own {
		gkaSCheckNonDecision(t, "GKA-062 "+name, w.deviceNow(name), x)
	}
	// The summaries override every one of them with Unknown/Unknown.
	sums := gkaSSummaries(hold)
	if len(hold.Status.DeviceSummaries) != 4 {
		t.Errorf("GKA-076: hold deviceSummaries = %#v, want the 4 devices pointing at the node with the broken fleet's name", hold.Status.DeviceSummaries)
	}
	for _, d := range []*v1alpha1.GPUDevice{dOK, dU, dI1, dI2} {
		s, ok := sums[d.Name]
		if !ok {
			t.Errorf("GKA-076: hold deviceSummaries lacks %s", d.Name)
			continue
		}
		if s.Qualification != "Unknown" || s.LifecyclePhase != "Unknown" || s.DesiredState != "InService" || s.ObservedGeneration != 1 || s.UID != string(d.UID) {
			t.Errorf("GKA-076: hold summary of %s = %#v, want Unknown/Unknown/InService/gen 1/uid %s (whatever its own scope says)", d.Name, s, d.UID)
		}
	}
	for i := 1; i < len(hold.Status.DeviceSummaries); i++ {
		if hold.Status.DeviceSummaries[i-1].UID >= hold.Status.DeviceSummaries[i].UID {
			t.Errorf("GKA-076: hold deviceSummaries are not in ascending UID order")
			break
		}
	}
}
