package kubeapi_test

// GKA-060 (label selection), GKA-064 (SelectionState split), GKA-065 (empty
// selection) and GKA-069 (claim duplicates).

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// GKA-060: matchLabels and every matchExpressions operator select by Node
// metadata.labels; label edits and node deletion move nodes in and out; the
// canarySelector is not evaluated (Enforce gate does not exist in S3a).
func TestGKA060_NodeSelectionByLabels(t *testing.T) {
	w := newGkaSWorld(t)
	tier, zone := "gka-sem/tier", "gka-sem/zone"
	n1 := w.node("n1", map[string]string{tier: "a", zone: "x"})
	n2 := w.node("n2", map[string]string{tier: "a", zone: "y"})
	n3 := w.node("n3", map[string]string{tier: "b"})
	n4 := w.node("n4", nil)

	req := func(key string, op metav1.LabelSelectorOperator, values ...string) metav1.LabelSelector {
		return metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: key, Operator: op, Values: values}}}
	}
	fMatch := w.fleet("fmatch", metav1.LabelSelector{MatchLabels: map[string]string{tier: "a"}})
	fIn := w.fleet("fin", req(zone, metav1.LabelSelectorOpIn, "x"))
	fNotIn := w.fleet("fnotin", req(zone, metav1.LabelSelectorOpNotIn, "x"))
	fExists := w.fleet("fexists", req(zone, metav1.LabelSelectorOpExists))
	fDNE := w.fleet("fdne", req(zone, metav1.LabelSelectorOpDoesNotExist))
	fAnd := w.fleet("fand", metav1.LabelSelector{
		MatchLabels:      map[string]string{tier: "a"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: zone, Operator: metav1.LabelSelectorOpIn, Values: []string{"y"}}},
	})
	// nodeSelector selects only n3; the canary would select n1 and n2 but must be ignored.
	fCanary := w.fleet("fcanary", metav1.LabelSelector{MatchLabels: map[string]string{tier: "b"}}, func(f *v1alpha1.GPUFleet) {
		gkaSEnforce(f)
		f.Spec.CanarySelector = &metav1.LabelSelector{MatchLabels: map[string]string{tier: "a"}}
	})
	fZero := w.fleet("fzero", metav1.LabelSelector{MatchLabels: map[string]string{tier: "none"}})

	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanNone)), newGkaFakeClock(gkaST0))

	expect := func(phase string, want map[*v1alpha1.GPUFleet]int32) {
		t.Helper()
		for f, n := range want {
			enforce := f.Spec.Mode == "Enforce"
			w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string {
				if o.Status.SelectedCount != n {
					return fmt.Sprintf("%s: selectedCount = %d, want %d", phase, o.Status.SelectedCount, n)
				}
				return gkaSFleetIs(o, "Unknown", "NoMatchingDevices")
			})
			tok := ""
			if enforce {
				tok = "gate_not_implemented"
			}
			gkaSCheckFleet(t, "GKA-060 "+phase+" "+f.Name, w.fleetNow(f.Name), gkaSFleetWant{
				Selected: n, Unknown: n, Status: "Unknown", Reason: "NoMatchingDevices",
				Message: gkaSMsg(fmt.Sprintf("selected=%d ready=0 degraded=0 unknown=%d", n, n), tok),
			})
		}
	}
	expect("initial", map[*v1alpha1.GPUFleet]int32{
		fMatch: 2, fIn: 1, fNotIn: 3, fExists: 2, fDNE: 2, fAnd: 1, fCanary: 1, fZero: 0,
	})

	w.updateNode(n4.Name, func(n *corev1.Node) { n.Labels = map[string]string{tier: "b"} })
	expect("n4 gains tier=b", map[*v1alpha1.GPUFleet]int32{
		fMatch: 2, fIn: 1, fNotIn: 3, fExists: 2, fDNE: 2, fAnd: 1, fCanary: 2, fZero: 0,
	})

	w.updateNode(n1.Name, func(n *corev1.Node) { delete(n.Labels, tier) })
	expect("n1 loses tier", map[*v1alpha1.GPUFleet]int32{
		fMatch: 1, fIn: 1, fNotIn: 3, fExists: 2, fDNE: 2, fAnd: 1, fCanary: 2, fZero: 0,
	})

	w.deleteNode(n2.Name)
	expect("n2 deleted", map[*v1alpha1.GPUFleet]int32{
		fMatch: 0, fIn: 1, fNotIn: 2, fExists: 1, fDNE: 2, fAnd: 0, fCanary: 2, fZero: 0,
	})
	_ = n3
}

// GKA-064 (+GKA-062 row 8, K-T-19): the four SelectionState rows, which devices
// are Intents (D*) and which only make the node Partial (X*), and that devices
// whose fleetRef is another fleet never influence (N, F).
func TestGKA064_SelectionStateSplit(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	g := w.fleet("g", metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/none": "x"}}) // valid, selects nothing
	n := map[string]*corev1.Node{}
	for _, k := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7"} {
		n[k] = w.selNode(k)
	}
	wrongUID := func(r *v1alpha1.ObjectRef) { r.UID = "not-the-uid" }
	dup6 := gkaSPtr("GPU-" + w.name("dup6"))
	dup7 := gkaSPtr("GPU-" + w.name("dup7"))

	d1 := w.device("d1", n["n1"], f)
	g1 := w.device("g1", n["n1"], g) // fleetRef is another fleet: invisible to (n1, F)
	d2 := w.device("d2", n["n2"], f)
	x2 := w.device("x2", n["n2"], f, func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.NodeRef) })
	x3 := w.device("x3", n["n3"], f, func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.NodeRef) })
	d5 := w.device("d5", n["n5"], f)
	y5 := w.device("y5", n["n5"], f, func(d *v1alpha1.GPUDevice) { wrongUID(&d.Spec.FleetRef) })
	c61 := w.device("c61", n["n6"], f, gkaSWithClaim("NVIDIA", dup6, nil))
	c62 := w.device("c62", n["n6"], f, gkaSWithClaim("NVIDIA", dup6, nil))
	d6 := w.device("d6", n["n6"], f)
	c71 := w.device("c71", n["n7"], f, gkaSWithClaim("NVIDIA", dup7, nil))
	c72 := w.device("c72", n["n7"], f, gkaSWithClaim("NVIDIA", dup7, nil))

	a := gkaSNewAssessor(newGkaSScript(gkaSPlanReady))
	w.start(a, newGkaFakeClock(gkaST0))

	for _, k := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7"} {
		w.waitNPS(n[k].Name, func(p *v1alpha1.NodePathState) string {
			if len(p.Status.Conditions) == 0 {
				return "status not written yet"
			}
			return ""
		})
	}
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Validating") })
	w.waitDevice(g1.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "NoMatchingDevices") })
	gkaSSettle(3)

	type want struct {
		called    bool
		selection fleet.SelectionState
		intents   []string
	}
	rows := map[string]want{
		"n1": {true, fleet.SelectionComplete, []string{d1.Name}}, // D* != {} and X* = {}
		"n2": {true, fleet.SelectionPartial, []string{d2.Name}},  // D* != {} and X* != {}
		"n3": {false, 0, nil},                                    // D* = {} and X* != {}
		"n4": {false, 0, nil},                                    // D* = {} and X* = {}
		"n5": {true, fleet.SelectionPartial, []string{d5.Name}},  // fleetRef UID mismatch is X
		"n6": {true, fleet.SelectionPartial, []string{d6.Name}},  // IdentityConflict devices move from D to X*
		"n7": {false, 0, nil},                                    // only IdentityConflict devices: K-T-19
	}
	for k, wnt := range rows {
		reqs := gkaSRequestsFor(a, n[k].Name)
		if !wnt.called {
			if len(reqs) != 0 {
				t.Errorf("GKA-064 %s: assessor called %d times, want never", k, len(reqs))
			}
			continue
		}
		if len(reqs) == 0 {
			t.Errorf("GKA-064 %s: assessor never called", k)
			continue
		}
		got := reqs[len(reqs)-1]
		if got.Selection != wnt.selection {
			t.Errorf("GKA-064 %s: selection = %s, want %s", k, got.Selection.String(), wnt.selection.String())
		}
		if names := gkaSIntentNames(got); strings.Join(names, ",") != strings.Join(wnt.intents, ",") {
			t.Errorf("GKA-064 %s: intents = %v, want %v (D*)", k, names, wnt.intents)
		}
	}

	// Node scope rendering per row.
	nps := func(k string) *v1alpha1.NodePathState { return w.npsNow(n[k].Name) }
	gkaSCheckNPS(t, "GKA-064 n1 Complete", nps("n1"), gkaSNPSWant{
		Elig: "True", Reason: "Ready", Qual: "Qualified", FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: "*",
	})
	partialCalled := gkaSNPSWant{Elig: "Unknown", Reason: "Validating", Completeness: "Complete", GraphRevision: "*"}
	gkaSCheckNPS(t, "GKA-064 n2 Partial", nps("n2"), partialCalled)
	gkaSCheckNPS(t, "GKA-064 n5 Partial", nps("n5"), partialCalled)
	gkaSCheckNPS(t, "GKA-064 n6 Partial", nps("n6"), partialCalled)
	partialSkipped := gkaSNPSWant{Elig: "Unknown", Reason: "Validating"} // SelectionPartial without assessor call
	gkaSCheckNPS(t, "GKA-064 n3 Partial (no call)", nps("n3"), partialSkipped)
	gkaSCheckNPS(t, "GKA-064 n7 Partial (no call)", nps("n7"), partialSkipped)
	gkaSCheckNPS(t, "GKA-064 n4 NoDevices", nps("n4"), gkaSNPSWant{Elig: "Unknown", Reason: "NoMatchingDevices"})
	for k, wantCond := range map[string]*gkaSC{
		"n4": {gkaSNodeCondType, "Unknown", "NoMatchingDevices", "fleet=" + f.Name},
		"n3": {gkaSNodeCondType, "Unknown", "Validating", "fleet=" + f.Name},
	} {
		wantCond := wantCond
		w.waitNode(n[k].Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, wantCond) })
		gkaSCheckNodeCond(t, "GKA-064 "+k, w.nodeNow(n[k].Name), wantCond)
	}

	// deviceSummaries are D(N, F): x devices (UID mismatch) are absent, IdentityConflict
	// devices are present (GKA-076), all UID-ordered.
	sumNames := func(k string) []string {
		var out []string
		for _, ds := range nps(k).Status.DeviceSummaries {
			out = append(out, ds.Name)
		}
		sort.Strings(out)
		return out
	}
	for k, wantNames := range map[string][]string{
		"n1": {d1.Name}, "n2": {d2.Name}, "n3": nil, "n4": nil, "n5": {d5.Name},
		"n6": {c61.Name, c62.Name, d6.Name}, "n7": {c71.Name, c72.Name},
	} {
		sort.Strings(wantNames)
		if got := sumNames(k); strings.Join(got, ",") != strings.Join(wantNames, ",") {
			t.Errorf("GKA-076 %s: deviceSummaries = %v, want %v", k, got, wantNames)
		}
	}
	for _, k := range []string{"n1", "n2", "n5", "n6", "n7"} {
		sums := nps(k).Status.DeviceSummaries
		for i := 1; i < len(sums); i++ {
			if sums[i-1].UID >= sums[i].UID {
				t.Errorf("GKA-076 %s: deviceSummaries not in ascending UID order", k)
			}
		}
	}
	// A conflicting device is summarized with its scope status; the decided device with its decision.
	if ds := gkaSSummaries(nps("n6"))[c61.Name]; ds.Qualification != "Unknown" || ds.LifecyclePhase != "Pending" || ds.DesiredState != "InService" {
		t.Errorf("GKA-076 n6: %s summary = %#v, want Unknown/Pending/InService", c61.Name, ds)
	}
	if ds := gkaSSummaries(nps("n6"))[d6.Name]; ds.Qualification != "Qualified" || ds.LifecyclePhase != "Ready" || ds.ObservedGeneration != 1 || ds.UID != string(d6.UID) {
		t.Errorf("GKA-076 n6: %s summary = %#v, want Qualified/Ready generation 1 uid %s", d6.Name, ds, d6.UID)
	}

	// The cross-fleet device g1 is rendered by its own scope (GKA-062 row 5) and the
	// unresolved x devices by UIDMismatch.
	gkaSCheckNonDecision(t, "GKA-064 g1", w.deviceNow(g1.Name), gkaSNonDec{SR: "NoMatchingDevices", Phase: "Pending"})
	for _, x := range []*v1alpha1.GPUDevice{x2, x3, y5} {
		w.waitDevice(x.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "UIDMismatch") })
		gkaSCheckNonDecision(t, "GKA-064 "+x.Name, w.deviceNow(x.Name), gkaSNonDec{SR: "UIDMismatch", Phase: "Pending"})
	}

	// Fleet: 7 selected nodes, only n1 Eligible, none Disqualified -> row 7.
	gkaSCheckFleet(t, "GKA-064 fleet", w.fleetNow(f.Name), gkaSFleetWant{
		Selected: 7, Ready: 1, Unknown: 6, Status: "Unknown", Reason: "Validating", Message: "selected=7 ready=1 degraded=0 unknown=6",
	})
}

// GKA-065: FleetReady is Unknown/NoMatchingDevices when the fleet selects no node
// or when no selected node has a D(N, F) member (even if X members exist), but a
// single node with devices keeps the fleet in row 7 (Validating).
func TestGKA065_EmptySelectionFleetReady(t *testing.T) {
	w := newGkaSWorld(t)
	keyNoDev, keyMixed, keyX := "gka-sem/nodev", "gka-sem/mixed", "gka-sem/xonly"
	nd1 := w.node("nd1", map[string]string{keyNoDev: w.sel})
	nd2 := w.node("nd2", map[string]string{keyNoDev: w.sel})
	m1 := w.node("m1", map[string]string{keyMixed: w.sel})
	m2 := w.node("m2", map[string]string{keyMixed: w.sel})
	x1 := w.node("x1", map[string]string{keyX: w.sel})
	sel := func(k string) metav1.LabelSelector {
		return metav1.LabelSelector{MatchLabels: map[string]string{k: w.sel}}
	}
	fZero := w.fleet("fzero", metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/never": "x"}})
	fNoDev := w.fleet("fnodev", sel(keyNoDev))
	fMixed := w.fleet("fmixed", sel(keyMixed))
	fX := w.fleet("fx", sel(keyX))
	// A node whose only devices are an IdentityConflict pair: they are members of D(N, F), so the
	// fleet is NOT "empty" (row 7 Validating), although the node itself is Partial.
	keyC := "gka-sem/confonly"
	c1 := w.node("c1", map[string]string{keyC: w.sel})
	fC := w.fleet("fc", sel(keyC))
	dupC := gkaSPtr("GPU-" + w.name("dupc"))
	w.device("cc1", c1, fC, gkaSWithClaim("NVIDIA", dupC, nil))
	w.device("cc2", c1, fC, gkaSWithClaim("NVIDIA", dupC, nil))
	w.device("dm1", m1, fMixed)
	// The only device of fX has a wrong fleetRef UID: it is X, never D.
	w.device("dx", x1, fX, func(d *v1alpha1.GPUDevice) { d.Spec.FleetRef.UID = "not-the-uid" })

	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	cases := []struct {
		f    *v1alpha1.GPUFleet
		want gkaSFleetWant
	}{
		{fZero, gkaSFleetWant{Status: "Unknown", Reason: "NoMatchingDevices", Message: "selected=0 ready=0 degraded=0 unknown=0"}},
		{fNoDev, gkaSFleetWant{Selected: 2, Unknown: 2, Status: "Unknown", Reason: "NoMatchingDevices", Message: "selected=2 ready=0 degraded=0 unknown=2"}},
		{fX, gkaSFleetWant{Selected: 1, Unknown: 1, Status: "Unknown", Reason: "NoMatchingDevices", Message: "selected=1 ready=0 degraded=0 unknown=1"}},
		{fC, gkaSFleetWant{Selected: 1, Unknown: 1, Status: "Unknown", Reason: "Validating", Message: "selected=1 ready=0 degraded=0 unknown=1"}},
		// One node with a device (Eligible), one without: row 7, not row 3 ("some nodes only NoDevices").
		{fMixed, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Validating", Message: "selected=2 ready=1 degraded=0 unknown=1"}},
	}
	for _, c := range cases {
		w.waitFleet(c.f.Name, func(o *v1alpha1.GPUFleet) string {
			if o.Status.SelectedCount != c.want.Selected {
				return fmt.Sprintf("selectedCount = %d, want %d", o.Status.SelectedCount, c.want.Selected)
			}
			return gkaSFleetIs(o, c.want.Status, c.want.Reason)
		})
		gkaSCheckFleet(t, "GKA-065 "+c.f.Name, w.fleetNow(c.f.Name), c.want)
	}
	// The X-only node is Partial, not NoDevices: NodePathState says Validating.
	w.waitNPS(x1.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	w.waitNPS(nd1.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "NoMatchingDevices") })
	w.waitNPS(m2.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "NoMatchingDevices") })
	_, _ = nd2, m2
}

// GKA-069: claim duplicates become IdentityConflict regardless of fleet or node
// (cluster wide), compared byte-exactly on (vendor, uuid) or (vendor, serial when
// there is no uuid); Retired peers, terminating peers, and the peer= list rules.
func TestGKA069_ClaimDuplicateIdentityConflict(t *testing.T) {
	w := newGkaSWorld(t)
	ghostF, ghostN := w.name("ghostf"), w.name("ghostn")
	ghost := func(d *v1alpha1.GPUDevice) {
		d.Spec.NodeRef = v1alpha1.ObjectRef{Name: ghostN, UID: "u"}
		d.Spec.FleetRef = v1alpha1.ObjectRef{Name: ghostF, UID: "u"}
	}
	// Every device points at a nonexistent fleet/node, so a non-conflicting device
	// renders NoMatchingDevices and only the GKA-069 rule can change that.
	mk := func(suffix string, mut ...func(*v1alpha1.GPUDevice)) *v1alpha1.GPUDevice {
		return w.device(suffix, nil, nil, append([]func(*v1alpha1.GPUDevice){ghost}, mut...)...)
	}
	uuid := func(s string) *string { return gkaSPtr("GPU-" + w.name(s)) }
	conflict := map[string][]string{} // device -> peer names
	plain := map[string]string{}      // device -> phase of a non-conflicting device
	pair := func(a, b *v1alpha1.GPUDevice) {
		conflict[a.Name] = append(conflict[a.Name], b.Name)
		conflict[b.Name] = append(conflict[b.Name], a.Name)
	}

	// G1: identical (vendor, uuid), different fleets/nodes are irrelevant.
	u1 := uuid("u1")
	a1 := mk("a1", gkaSWithClaim("NVIDIA", u1, nil))
	a2 := mk("a2", gkaSWithClaim("NVIDIA", u1, nil))
	pair(a1, a2)
	// G2: same uuid, different vendor: no conflict.
	u2 := uuid("u2")
	plain[mk("b1", gkaSWithClaim("NVIDIA", u2, nil)).Name] = "Pending"
	plain[mk("b2", gkaSWithClaim("AMD", u2, nil)).Name] = "Pending"
	// G3: byte-exact comparison: case and trailing space differ.
	base := "GPU-" + w.name("case")
	plain[mk("c1", gkaSWithClaim("NVIDIA", gkaSPtr(base), nil)).Name] = "Pending"
	plain[mk("c2", gkaSWithClaim("NVIDIA", gkaSPtr(strings.ToUpper(base)), nil)).Name] = "Pending"
	plain[mk("c3", gkaSWithClaim("NVIDIA", gkaSPtr(base+" "), nil)).Name] = "Pending"
	// G4: no uuid, same (vendor, serial): conflict.
	s4 := gkaSPtr("SN-" + w.name("s4"))
	d1 := mk("d1", gkaSWithClaim("NVIDIA", nil, s4))
	d2 := mk("d2", gkaSWithClaim("NVIDIA", nil, s4))
	pair(d1, d2)
	// G5: uuids differ although serials are equal: the uuid is the identity, no conflict.
	s5 := gkaSPtr("SN-" + w.name("s5"))
	plain[mk("e1", gkaSWithClaim("NVIDIA", uuid("u5a"), s5)).Name] = "Pending"
	plain[mk("e2", gkaSWithClaim("NVIDIA", uuid("u5b"), s5)).Name] = "Pending"
	// G6: a uuid equal to another device's serial string is a different kind of key.
	x6 := "X6-" + w.name("x6")
	plain[mk("f1", gkaSWithClaim("NVIDIA", gkaSPtr(x6), nil)).Name] = "Pending"
	plain[mk("f2", gkaSWithClaim("NVIDIA", nil, gkaSPtr(x6))).Name] = "Pending"
	// G7: Retired holder + one live claimant: only the live device conflicts.
	u7 := uuid("u7")
	g1 := mk("g1", gkaSWithClaim("NVIDIA", u7, nil), gkaSWithDesired("Retired"))
	g2 := mk("g2", gkaSWithClaim("NVIDIA", u7, nil))
	conflict[g2.Name] = []string{g1.Name}
	plain[g1.Name] = "Retiring"
	// G8: two Retired devices with one claim: nobody conflicts.
	u8 := uuid("u8")
	plain[mk("h1", gkaSWithClaim("NVIDIA", u8, nil), gkaSWithDesired("Retired")).Name] = "Retiring"
	plain[mk("h2", gkaSWithClaim("NVIDIA", u8, nil), gkaSWithDesired("Retired")).Name] = "Retiring"
	// G9: Retired holder + two live claimants: both live devices conflict, peers include the Retired one.
	u9 := uuid("u9")
	i1 := mk("i1", gkaSWithClaim("NVIDIA", u9, nil), gkaSWithDesired("Retired"))
	i2 := mk("i2", gkaSWithClaim("NVIDIA", u9, nil))
	i3 := mk("i3", gkaSWithClaim("NVIDIA", u9, nil))
	conflict[i2.Name] = []string{i1.Name, i3.Name}
	conflict[i3.Name] = []string{i1.Name, i2.Name}
	plain[i1.Name] = "Retiring"
	// G10: terminating devices are not peers.
	hold := func(d *v1alpha1.GPUDevice) { d.Finalizers = []string{gkaSFinalizerHold} }
	u10 := uuid("u10")
	plain[mk("j1", gkaSWithClaim("NVIDIA", u10, nil)).Name] = "Pending"
	j2 := mk("j2", gkaSWithClaim("NVIDIA", u10, nil), hold)
	u11 := uuid("u11")
	k1 := mk("k1", gkaSWithClaim("NVIDIA", u11, nil))
	k2 := mk("k2", gkaSWithClaim("NVIDIA", u11, nil))
	k3 := mk("k3", gkaSWithClaim("NVIDIA", u11, nil), hold)
	pair(k1, k2)
	w.deleteDevice(j2.Name)
	w.deleteDevice(k3.Name)
	// G11: 11 identical claims: each has 10 peers, listed as 8 names plus ",+2".
	u12 := uuid("u12")
	var g11 []*v1alpha1.GPUDevice
	for i := 0; i < 11; i++ {
		g11 = append(g11, mk(fmt.Sprintf("m%02d", i), gkaSWithClaim("NVIDIA", u12, nil)))
	}
	for _, x := range g11 {
		for _, y := range g11 {
			if x != y {
				conflict[x.Name] = append(conflict[x.Name], y.Name)
			}
		}
	}
	// G12: six devices with ~236-character names: the peer list must be cut to fit 1024.
	u13 := uuid("u13")
	longBase := w.name("lg")
	var g12 []*v1alpha1.GPUDevice
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("%s%s-%02d", longBase, strings.Repeat("y", 170), i)
		g12 = append(g12, mk("lg", gkaSWithClaim("NVIDIA", u13, nil), func(d *v1alpha1.GPUDevice) { d.Name = name }))
	}

	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanNone)), newGkaFakeClock(gkaST0))

	for name := range conflict {
		w.waitDevice(name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "IdentityConflict") })
	}
	for name := range plain {
		w.waitDevice(name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "NoMatchingDevices") })
	}
	for _, x := range g12 {
		w.waitDevice(x.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "IdentityConflict") })
	}
	gkaSSettle(3)

	for name, peers := range conflict {
		d := w.deviceNow(name)
		gkaSCheckNonDecision(t, "GKA-069 "+name, d, gkaSNonDec{
			SR: "IdentityConflict", Phase: "Pending", Binding: "Conflict", Peer: "peer=" + gkaSNameList(peers),
		})
	}
	for name, phase := range plain {
		gkaSCheckNonDecision(t, "GKA-069 plain "+name, w.deviceNow(name), gkaSNonDec{SR: "NoMatchingDevices", Phase: phase})
	}
	// Peer list of the 6 long names: consistent prefix of the sorted peer names, ",+N" for the rest, <= 1024.
	var names12 []string
	for _, x := range g12 {
		names12 = append(names12, x.Name)
	}
	for _, x := range g12 {
		d := w.deviceNow(x.Name)
		c := gkaSCondOf(d.Status.Conditions, "IdentityBound")
		var peers []string
		for _, n := range names12 {
			if n != x.Name {
				peers = append(peers, n)
			}
		}
		gkaSCheckPeerList(t, "GKA-074 long peers "+x.Name[:12], c, "binding=Conflict; peer=", peers)
	}
	_ = g11
}

// gkaSCheckPeerList checks the GKA-074 name-list rule for a message that had to
// be shortened to fit 1024 characters: the listed names are a prefix of the byte
// sorted names, the remainder is ",+N", and nothing is dropped silently.
func gkaSCheckPeerList(t *testing.T, where string, c *metav1.Condition, prefix string, all []string) {
	t.Helper()
	if c == nil {
		t.Errorf("%s: condition missing", where)
		return
	}
	msg := c.Message
	if len(msg) > 1024 || !gkaSASCII(msg) {
		t.Errorf("%s: message is %d chars (ASCII=%v), want <= 1024 ASCII (GKA-074)", where, len(msg), gkaSASCII(msg))
	}
	if !strings.HasPrefix(msg, prefix) {
		t.Errorf("%s: message %q lacks prefix %q", where, msg, prefix)
		return
	}
	sorted := append([]string(nil), all...)
	sort.Strings(sorted)
	list := strings.Split(strings.TrimPrefix(msg, prefix), ",")
	listed := 0
	remaining := 0
	for _, tok := range list {
		if strings.HasPrefix(tok, "+") {
			if _, err := fmt.Sscanf(tok, "+%d", &remaining); err != nil {
				t.Errorf("%s: bad remainder token %q", where, tok)
			}
			continue
		}
		if listed >= len(sorted) || tok != sorted[listed] {
			t.Errorf("%s: listed name %d = %.40q, want the sorted peer %.40q", where, listed, tok, sorted[min(listed, len(sorted)-1)])
			return
		}
		listed++
	}
	if listed < 1 {
		t.Errorf("%s: no peer name listed although the first one fits", where)
	}
	if listed+remaining != len(sorted) {
		t.Errorf("%s: listed %d + remainder %d != %d peers", where, listed, remaining, len(sorted))
	}
	if listed < len(sorted) && remaining == 0 {
		t.Errorf("%s: names were dropped without a +N remainder", where)
	}
}
