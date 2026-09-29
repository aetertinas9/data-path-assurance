package kubeapi_test

// GKA-062 (device scope resolution order) and GKA-067 (node scope table).

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// GKA-062: every row of the device scope table, and the "first match wins"
// precedence between rows (UID mismatch before claim duplicates before invalid
// selector before missing target before Conflict). GKA-069 duplicates are
// cluster wide, so a UIDMismatch device still counts as a peer.
func TestGKA062_DeviceScopeResolutionRows(t *testing.T) {
	w := newGkaSWorld(t)
	sel2 := w.name("sel2")
	nA := w.selNode("a")                                                                          // selected by F only
	nB := w.node("b", nil)                                                                        // selected by nobody
	nC := w.node("c", map[string]string{gkaSSelKey: w.sel, "gka-sem/sel2": sel2})                 // selected by F and G
	f := w.selFleet("f")                                                                          // valid, selects w.sel
	g := w.fleet("g", metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/sel2": sel2}}) // valid, selects nC
	bad := w.fleet("bad", metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "gka-sem/bad", Operator: metav1.LabelSelectorOpIn}, // In without values: not convertible
	}})
	ghostFleet, ghostNode := w.name("ghostf"), w.name("ghostn")
	wrongUID := func(r *v1alpha1.ObjectRef) { r.UID = "not-the-uid" }

	dup56 := gkaSPtr("GPU-" + w.name("dup56"))
	dupCC := gkaSPtr("GPU-" + w.name("dupcc"))
	dupQQ := gkaSPtr("GPU-" + w.name("dupqq"))

	r1 := w.device("r1", nA, f, func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.FleetRef) })
	r2 := w.device("r2", nA, f, func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.NodeRef) })
	r2b := w.device("r2b", nA, nil, func(d *v1alpha1.GPUDevice) {
		wrongUID(&d.Spec.NodeRef)
		d.Spec.FleetRef = v1alpha1.ObjectRef{Name: ghostFleet, UID: "u"}
	})
	e5 := w.device("e5", nA, f, gkaSWithClaim("NVIDIA", dup56, nil), func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.FleetRef) })
	e6 := w.device("e6", nA, f, gkaSWithClaim("NVIDIA", dup56, nil))
	c1 := w.device("c1", nA, f, gkaSWithClaim("NVIDIA", dupCC, nil))
	c2 := w.device("c2", nA, f, gkaSWithClaim("NVIDIA", dupCC, nil))
	q1 := w.device("q1", nA, bad, gkaSWithClaim("NVIDIA", dupQQ, nil))
	q2 := w.device("q2", nA, bad, gkaSWithClaim("NVIDIA", dupQQ, nil))
	p1 := w.device("p1", nA, bad)
	p2 := w.device("p2", nA, bad, func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.NodeRef) })
	p3 := w.device("p3", nil, bad, func(d *v1alpha1.GPUDevice) { d.Spec.NodeRef = v1alpha1.ObjectRef{Name: ghostNode, UID: "u"} })
	v5a := w.device("v5a", nA, nil, func(d *v1alpha1.GPUDevice) { d.Spec.FleetRef = v1alpha1.ObjectRef{Name: ghostFleet, UID: "u"} })
	v5b := w.device("v5b", nil, f, func(d *v1alpha1.GPUDevice) { d.Spec.NodeRef = v1alpha1.ObjectRef{Name: ghostNode, UID: "u"} })
	v5c := w.device("v5c", nB, f)
	v6a := w.device("v6a", nC, f)
	v6b := w.device("v6b", nC, g)
	v8 := w.device("v8", nA, f)

	script := newGkaSScript(gkaSPlanNone)
	a := gkaSNewAssessor(script)
	w.start(a, newGkaFakeClock(gkaST0))

	invalid := "internal error: invalid nodeSelector"
	cases := []struct {
		d *v1alpha1.GPUDevice
		x gkaSNonDec
	}{
		{r1, gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"}},                                                    // row 1
		{r2, gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"}},                                                    // row 2
		{r2b, gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"}},                                                   // row 2 beats row 5 (OD-04)
		{e5, gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"}},                                                    // row 1 beats row 3
		{e6, gkaSNonDec{SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + e5.Name}}, // UIDMismatch peer still counts
		{c1, gkaSNonDec{SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + c2.Name}}, // row 3
		{c2, gkaSNonDec{SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + c1.Name}},
		{q1, gkaSNonDec{SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + q2.Name}}, // row 3 beats row 4
		{q2, gkaSNonDec{SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + q1.Name}},
		{p1, gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{invalid}}}, // row 4
		{p2, gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"}},                            // row 2 beats row 4
		{p3, gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{invalid}}}, // row 4 beats row 5
		{v5a, gkaSNonDec{SR: "NoMatchingDevices", Phase: "Pending"}},                     // row 5: fleet missing
		{v5b, gkaSNonDec{SR: "NoMatchingDevices", Phase: "Pending"}},                     // row 5: node missing
		{v5c, gkaSNonDec{SR: "NoMatchingDevices", Phase: "Pending"}},                     // row 5: selector does not select the node
		{v6a, gkaSNonDec{SR: "Conflict", Phase: "Pending"}},                              // row 6
		{v6b, gkaSNonDec{SR: "Conflict", Phase: "Pending"}},
		{v8, gkaSNonDec{SR: "Validating", Phase: "Pending", Tail: []string{"no_observation"}}}, // row 8: assessable
	}
	for _, c := range cases {
		sr := c.x.SR
		w.waitDevice(c.d.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, sr) })
	}
	// nC is a Conflict node: sync on its NodePathState before asserting "never assessed".
	w.waitNPS(nC.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	w.waitNPS(nA.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	for _, c := range cases {
		gkaSCheckNonDecision(t, "GKA-062 "+c.d.Name, w.deviceNow(c.d.Name), c.x)
	}

	// Row 8 is the only assessable device: the assessor sees exactly D* = {v8}
	// (GKA-064) for (nA, F) and is never called for the Conflict node nC or the
	// unselected nB.
	reqs := gkaSRequestsFor(a, nA.Name)
	if len(reqs) == 0 {
		t.Fatalf("GKA-062 row 8: assessor was never called for %s", nA.Name)
	}
	if got := gkaSIntentNames(reqs[len(reqs)-1]); len(got) != 1 || got[0] != v8.Name {
		t.Errorf("GKA-062/064: intents = %v, want only %s (every other device has a non-assessable scope)", got, v8.Name)
	}
	if reqs[len(reqs)-1].Selection != fleet.SelectionPartial {
		t.Errorf("GKA-064: selection = %s, want Partial (X* is not empty)", reqs[len(reqs)-1].Selection.String())
	}
	for _, n := range []string{nB.Name, nC.Name} {
		if got := gkaSRequestsFor(a, n); len(got) != 0 {
			t.Errorf("GKA-062/067: assessor called %d times for non-assessable node %s", len(got), n)
		}
	}
	if w.hasNPS(nB.Name) {
		t.Errorf("GKA-067 row 2: NodePathState created for node %s that no fleet selects", nB.Name)
	}
}

// GKA-067: node scope rows 2 (no fleet selects), 3 (overlap = Conflict) and 5
// (single fleet -> GKA-064 table). Rows 1 and 4 are covered by the GKA-068 hold
// test and the GKA-063 capacity test.
func TestGKA067_NodeScopeTableRows(t *testing.T) {
	w := newGkaSWorld(t)
	selB := w.name("selb")
	nNone := w.node("none", nil)
	nConf := w.node("conf", map[string]string{gkaSSelKey: w.sel, "gka-sem/selb": selB})
	nOK := w.selNode("ok")
	fa := w.selFleet("fa")
	fb := w.fleet("fb", metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/selb": selB}})
	dOK := w.device("dok", nOK, fa)
	dConf := w.device("dconf", nConf, fa)

	script := newGkaSScript(gkaSPlanReady)
	a := gkaSNewAssessor(script)
	w.start(a, newGkaFakeClock(gkaST0))

	w.waitNPS(nOK.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	conf := w.waitNPS(nConf.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	gkaSCheckNPS(t, "GKA-067 row 3", conf, gkaSNPSWant{Elig: "Unknown", Reason: "Conflict"})
	gkaSSettle(5)

	// Row 3: Conflict node -> assessor is not called, even with a ready script.
	if got := gkaSRequestsFor(a, nConf.Name); len(got) != 0 {
		t.Errorf("GKA-067 row 3: assessor called %d times for Conflict node", len(got))
	}
	// Row 2: nobody selects nNone -> no NodePathState, no condition.
	if w.hasNPS(nNone.Name) {
		t.Errorf("GKA-067 row 2: NodePathState exists for an unselected node")
	}
	gkaSCheckNodeCond(t, "GKA-067 row 2", w.nodeNow(nNone.Name), nil)
	// Row 5: single fleet -> assessed with the GKA-064 selection.
	reqs := gkaSRequestsFor(a, nOK.Name)
	if len(reqs) == 0 || reqs[0].Selection != fleet.SelectionComplete || len(reqs[0].Intents) != 1 || reqs[0].Intents[0].Device.Name != dOK.Name {
		t.Errorf("GKA-067 row 5: requests for %s = %#v, want Complete with intent %s", nOK.Name, reqs, dOK.Name)
	}
	// The Conflict node's device is rendered as Conflict scope, and both fleets
	// count the node as unknown (GKA-061).
	w.waitDevice(dConf.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "Conflict") })
	gkaSCheckNonDecision(t, "GKA-067 conflict device", w.deviceNow(dConf.Name), gkaSNonDec{SR: "Conflict", Phase: "Pending"})
	_ = fb
}
