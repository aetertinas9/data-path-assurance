package kubeapi_test

// GKA-110 (which nodes get the condition), GKA-111 (values, name list) and GKA-113
// (removal, "published by itself", zero writes afterwards). The apply mechanics
// (GKA-112, GKA-114) belong to lane T-3.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// gkaSQuiet implements the GKA-175 negative window: wait until the recorder has seen no
// write under prefix for two resync intervals, then require none for 3 x resync + 2 s.
func gkaSQuiet(t *testing.T, rec *gkaRecorder, prefix, what string) {
	t.Helper()
	last := len(rec.Writes(prefix))
	stableSince := time.Now()
	giveUp := time.Now().Add(gkaSWaitMax)
	for time.Since(stableSince) < 2*gkaSResync+100*time.Millisecond {
		time.Sleep(50 * time.Millisecond)
		if n := len(rec.Writes(prefix)); n != last {
			last, stableSince = n, time.Now()
		}
		if time.Now().After(giveUp) {
			t.Fatalf("%s: writes under %s never settled", what, prefix)
		}
	}
	base := len(rec.Writes(prefix))
	time.Sleep(3*gkaSResync + 2*time.Second)
	if now := rec.Writes(prefix); len(now) != base {
		t.Errorf("%s: %d write(s) under %s in the steady state, want 0: %v", what, len(now)-base, prefix, now[base:])
	}
}

// GKA-110 (+GKA-113 removal, GKA-086): the condition exists exactly for nodes with
// S(N) != {} whose fleets are ALL Audit. An Enforce fleet anywhere in S(N) keeps it off
// (and removes one already published); flipping the mode adds/removes it, and a
// re-published condition starts a new lastTransitionTime.
func TestGKA110_PublicationRangeFollowsFleetModes(t *testing.T) {
	w := newGkaSWorld(t)
	keyA, keyE, keyA2 := "gka-sem/a", "gka-sem/e", "gka-sem/a2"
	n1 := w.node("n1", map[string]string{keyA: w.sel})               // Audit only
	n2 := w.node("n2", map[string]string{keyE: w.sel})               // Enforce only
	n3 := w.node("n3", map[string]string{keyA: w.sel, keyE: w.sel})  // Audit + Enforce
	n4 := w.node("n4", nil)                                          // nobody
	n5 := w.node("n5", map[string]string{keyA: w.sel, keyA2: w.sel}) // Audit + Audit
	fA := w.fleet("fa", metav1.LabelSelector{MatchLabels: map[string]string{keyA: w.sel}})
	fE := w.fleet("fe", metav1.LabelSelector{MatchLabels: map[string]string{keyE: w.sel}}, gkaSEnforce)
	fA2 := w.fleet("fa2", metav1.LabelSelector{MatchLabels: map[string]string{keyA2: w.sel}})
	w.device("d1", n1, fA)
	w.device("d2", n2, fE)
	clk := newGkaFakeClock(gkaST0)
	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), clk)

	both := func(a, b string) string { return "fleet=" + gkaSNameList([]string{a, b}) }
	published := map[*corev1.Node]*gkaSC{
		n1: {gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name},
		n5: {gkaSNodeCondType, "Unknown", "Conflict", both(fA.Name, fA2.Name)},
	}
	for n, c := range published {
		c := c
		w.waitNode(n.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, c) })
	}
	w.waitNPS(n2.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNPS(n3.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	w.waitNPS(n5.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	gkaSSettle(4)
	for _, n := range []*corev1.Node{n2, n3, n4} {
		gkaSCheckNodeCond(t, "GKA-110 initial "+n.Name, w.nodeNow(n.Name), nil)
	}
	if w.hasNPS(n4.Name) {
		t.Errorf("GKA-100: NodePathState created for the unselected node")
	}
	// Enforce nodes carry the token in NodeEligible only (GKA-077/086).
	gkaSCheckNPS(t, "GKA-077 Enforce-only node", w.npsNow(n2.Name), gkaSNPSWant{
		Elig: "True", Reason: "Ready", Qual: "Qualified", Enforce: true, FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: "*",
	})
	gkaSCheckNPS(t, "GKA-077 Audit+Audit conflict", w.npsNow(n5.Name), gkaSNPSWant{Elig: "Unknown", Reason: "Conflict"})
	gkaSCheckNPS(t, "GKA-077 Audit+Enforce conflict", w.npsNow(n3.Name), gkaSNPSWant{Elig: "Unknown", Reason: "Conflict", Enforce: true})
	gkaSCheckNodeCond(t, "GKA-110 n1", w.nodeNow(n1.Name), published[n1])

	// Audit -> Enforce: the conditions of every node that fleet A selects disappear.
	w.updateFleet(fA.Name, gkaSEnforce)
	w.waitNode(n1.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, nil) })
	w.waitNode(n5.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, nil) })
	gkaSCheckNPS(t, "GKA-077 n1 after Enforce", w.waitNPS(n1.Name, func(p *v1alpha1.NodePathState) string {
		c := gkaSCondOf(p.Status.Conditions, "NodeEligible")
		if c == nil || !strings.HasSuffix(c.Message, "gate_not_implemented") {
			return "NodeEligible has no gate_not_implemented yet"
		}
		return ""
	}), gkaSNPSWant{Elig: "True", Reason: "Ready", Qual: "Qualified", Enforce: true, FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: "*"})

	// Enforce -> Audit for both Enforce fleets, one minute and 40.5 s later: everything is published
	// again with a fresh lastTransitionTime (the previous entries were removed).
	clk.Set(gkaST0.Add(100*time.Second + 500*time.Millisecond))
	w.updateFleet(fA.Name, func(f *v1alpha1.GPUFleet) { f.Spec.Mode = v1alpha1.FleetMode("Audit") })
	w.updateFleet(fE.Name, func(f *v1alpha1.GPUFleet) { f.Spec.Mode = v1alpha1.FleetMode("Audit") })
	want := map[*corev1.Node]*gkaSC{
		n1: {gkaSNodeCondType, "True", "Ready", "fleet=" + fA.Name},
		n2: {gkaSNodeCondType, "True", "Ready", "fleet=" + fE.Name},
		n3: {gkaSNodeCondType, "Unknown", "Conflict", both(fA.Name, fE.Name)},
		n5: {gkaSNodeCondType, "Unknown", "Conflict", both(fA.Name, fA2.Name)},
	}
	for n, c := range want {
		c := c
		w.waitNode(n.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, c) })
	}
	gkaSSettle(3)
	for n, c := range want {
		node := w.nodeNow(n.Name)
		gkaSCheckNodeCond(t, "GKA-110 all Audit "+n.Name, node, c)
		if got := gkaSSec(gkaSNodeCond(node).LastTransitionTime); got != gkaSDay+"00:01:40Z" {
			t.Errorf("GKA-074(c) %s: republished lastTransitionTime = %s, want 00:01:40 (a removed entry starts over)", n.Name, got)
		}
	}
	gkaSCheckNodeCond(t, "GKA-110 n4", w.nodeNow(n4.Name), nil)
}

// GKA-074 name-list notation in a Node condition: fleet names byte-ordered, at most 8
// listed with ",+N" for the rest; and a list that would exceed 1024 characters is cut
// from the back with the remainder counted in +N.
func TestGKA111_NodeConditionFleetNameList(t *testing.T) {
	w := newGkaSWorld(t)
	keyTen, keyLong := "gka-sem/ten", "gka-sem/long"
	nTen := w.node("nten", map[string]string{keyTen: w.sel})
	nLong := w.node("nlong", map[string]string{keyLong: w.sel})
	var ten, long []string
	for i := 0; i < 10; i++ {
		f := w.fleet(fmt.Sprintf("t%02d", i), metav1.LabelSelector{MatchLabels: map[string]string{keyTen: w.sel}})
		ten = append(ten, f.Name)
	}
	longBase := w.name("lf")
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("%s%s-%02d", longBase, strings.Repeat("y", 170), i)
		f := w.fleet("lf", metav1.LabelSelector{MatchLabels: map[string]string{keyLong: w.sel}}, func(f *v1alpha1.GPUFleet) { f.Name = name })
		long = append(long, f.Name)
	}
	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanNone)), newGkaFakeClock(gkaST0))

	wantTen := &gkaSC{gkaSNodeCondType, "Unknown", "Conflict", "fleet=" + gkaSNameList(ten)}
	w.waitNode(nTen.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, wantTen) })
	gkaSCheckNodeCond(t, "GKA-074 ten fleets", w.nodeNow(nTen.Name), wantTen)
	if !strings.HasSuffix(wantTen.Message, ",+2") {
		t.Fatalf("test setup: the expected list %q does not end with ,+2", wantTen.Message)
	}

	n := w.waitNode(nLong.Name, func(o *corev1.Node) string {
		if gkaSNodeCond(o) == nil {
			return "no condition yet"
		}
		return ""
	})
	c := gkaSNodeCond(n)
	if string(c.Status) != "Unknown" || c.Reason != "Conflict" {
		t.Errorf("GKA-111: long-name conflict condition = {%s %s}, want Unknown/Conflict", c.Status, c.Reason)
	}
	gkaSCheckPeerList(t, "GKA-074 six ~236-character fleet names", &metav1.Condition{Message: c.Message}, "fleet=", long)
}

// GKA-113 (+GKA-112 preservation): the condition is removed when the node stops being
// selected (label removed, fleet deleted); only an entry that the controller itself
// published (managedFields {dpa-node-condition Apply status}) is ever removed or touched,
// other conditions and foreign owners of the same type are left alone; after the removal
// the controller writes nothing more to that node.
func TestGKA113_RemovalOnlyOfOwnConditionAndQuietAfterwards(t *testing.T) {
	w := newGkaSWorld(t)
	keyX := "gka-sem/x"
	nS := w.selNode("ns")
	nG := w.selNode("ng")                              // selected, but the type is owned by someone else
	nX := w.node("nx", map[string]string{keyX: w.sel}) // selected by the second fleet
	nF := w.node("nf", nil)                            // unselected, type owned by someone else
	fS := w.selFleet("fs")
	fX := w.fleet("fx", metav1.LabelSelector{MatchLabels: map[string]string{keyX: w.sel}})
	w.device("ds", nS, fS)
	w.device("dg", nG, fS)
	w.device("dx", nX, fX)

	hb := metav1.NewTime(time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))
	kubelet := corev1.NodeCondition{
		Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "KubeletReady", Message: "kubelet is posting ready status",
		LastHeartbeatTime: hb, LastTransitionTime: hb,
	}
	custom := corev1.NodeCondition{Type: "GkaSemCustom", Status: corev1.ConditionFalse, Reason: "Custom", Message: "someone else's condition", LastHeartbeatTime: hb, LastTransitionTime: hb}
	foreignFleetReady := corev1.NodeCondition{
		Type: corev1.NodeConditionType(gkaSNodeCondType), Status: corev1.ConditionFalse, Reason: "Foreign", Message: "owned by another manager",
		LastHeartbeatTime: hb, LastTransitionTime: hb,
	}
	seed := func(n *corev1.Node, conds ...corev1.NodeCondition) {
		w.nodeStatusAsForeign(n.Name, func(o *corev1.Node) { o.Status.Conditions = append(o.Status.Conditions, conds...) })
	}
	seed(nS, kubelet, custom)
	seed(nX, kubelet)
	seed(nG, foreignFleetReady)
	seed(nF, foreignFleetReady)

	cfg, rec := gkaRecordingConfig(w.e.Config)
	gkaSStart(t, cfg, gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	mine := func(fleetName string) *gkaSC { return &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + fleetName} }
	w.waitNode(nS.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, mine(fS.Name)) })
	w.waitNode(nX.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, mine(fX.Name)) })
	w.waitNPS(nG.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	gkaSSettle(5)

	sameCond := func(where string, n *corev1.Node, want corev1.NodeCondition) {
		t.Helper()
		var got *corev1.NodeCondition
		for i := range n.Status.Conditions {
			if n.Status.Conditions[i].Type == want.Type {
				got = &n.Status.Conditions[i]
			}
		}
		if got == nil {
			t.Errorf("%s: condition %s of node %s is gone", where, want.Type, n.Name)
			return
		}
		if got.Status != want.Status || got.Reason != want.Reason || got.Message != want.Message ||
			!got.LastHeartbeatTime.Equal(&want.LastHeartbeatTime) || !got.LastTransitionTime.Equal(&want.LastTransitionTime) {
			t.Errorf("%s: condition %s of node %s = %+v, want it untouched %+v", where, want.Type, n.Name, *got, want)
		}
	}
	node := w.nodeNow(nS.Name)
	sameCond("GKA-112 after publishing", node, kubelet)
	sameCond("GKA-112 after publishing", node, custom)
	if !gkaSHasManaged(node, "dpa-node-condition", "Apply", "status") {
		t.Errorf("GKA-112/113: no managedFields entry {dpa-node-condition Apply status} on the published node: %v", gkaSManagedFields(node))
	}
	// A condition of the same type owned by another manager is neither overwritten nor removed.
	sameCond("GKA-112/124 selected node, foreign owner", w.nodeNow(nG.Name), foreignFleetReady)
	sameCond("GKA-113 unselected node, foreign owner", w.nodeNow(nF.Name), foreignFleetReady)

	// Removal 1: the label goes away.
	w.updateNode(nS.Name, func(o *corev1.Node) { delete(o.Labels, gkaSSelKey) })
	w.waitNPSGone(nS.Name)
	w.waitNode(nS.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, nil) })
	node = w.nodeNow(nS.Name)
	sameCond("GKA-113 after removal", node, kubelet)
	sameCond("GKA-113 after removal", node, custom)
	gkaSQuiet(t, rec, "/api/v1/nodes/"+nS.Name, "GKA-113 steady state of the released node")

	// Removal 2: the fleet is deleted.
	w.deleteFleet(fX.Name)
	w.waitNPSGone(nX.Name)
	w.waitNode(nX.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, nil) })
	sameCond("GKA-113 after fleet deletion", w.nodeNow(nX.Name), kubelet)
	gkaSQuiet(t, rec, "/api/v1/nodes/"+nX.Name, "GKA-113 steady state after the fleet was deleted")

	// The foreign entries were never touched by any of this.
	sameCond("GKA-113 end", w.nodeNow(nG.Name), foreignFleetReady)
	sameCond("GKA-113 end", w.nodeNow(nF.Name), foreignFleetReady)
}

// GKA-074(c) + GKA-111: the Node condition's lastTransitionTime follows the status only.
// A message-only change and a reason-only change at an unchanged status keep it, however
// far the clock has moved; a real status change takes the pass time truncated to the second.
func TestGKA111_NodeConditionLastTransitionKeptWhenOnlyReasonOrMessageChange(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	w.device("d", n, f)
	script := newGkaSScript(gkaSPlanNone)
	clk := newGkaFakeClock(gkaST0)
	w.start(gkaSNewAssessor(script), clk)

	expect := func(what string, want gkaSC, ltt string) {
		t.Helper()
		w.waitNode(n.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, &want) })
		gkaSSettle(2)
		node := w.nodeNow(n.Name)
		gkaSCheckNodeCond(t, what, node, &want)
		if got := gkaSSec(gkaSNodeCond(node).LastTransitionTime); got != ltt {
			t.Errorf("GKA-074(c)/111 %s: lastTransitionTime = %s, want %s", what, got, ltt)
		}
	}
	// No observation at 00:00:00.
	expect("cold", gkaSC{gkaSNodeCondType, "Unknown", "Validating", "fleet=" + f.Name + "; no_observation"}, gkaSAt0)
	// Message only (the no_observation token goes away), status and reason unchanged, 100.5 s later.
	clk.Set(gkaST0.Add(100*time.Second + 500*time.Millisecond))
	script.SetDefault(gkaSPlanUnknown("Validating"))
	expect("message only", gkaSC{gkaSNodeCondType, "Unknown", "Validating", "fleet=" + f.Name}, gkaSAt0)
	// Reason only (same status, same message), 200.5 s after the start.
	clk.Set(gkaST0.Add(200*time.Second + 500*time.Millisecond))
	script.SetDefault(gkaSEdit(gkaSPlanUnknown("Validating"), func(_ app.NodeAssessmentRequest, na *app.NodeAssessment) { na.Node.Reason = "EvidenceStale" }))
	expect("reason only", gkaSC{gkaSNodeCondType, "Unknown", "EvidenceStale", "fleet=" + f.Name}, gkaSAt0)
	// The status finally changes at 300.5 s: the time is truncated, not rounded.
	clk.Set(gkaST0.Add(300*time.Second + 500*time.Millisecond))
	script.SetDefault(gkaSPlanReady)
	expect("status change", gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + f.Name}, gkaSDay+"00:05:00Z")
}

// GKA-114 (+GKA-074(c)): a Node condition whose status and reason stay but whose message changes
// (a third Audit fleet joins an overlap: fleet=a,b -> fleet=a,b,c) IS written, and the new message
// is published while lastTransitionTime is kept.
func TestGKA114_MessageOnlyChangeOfNodeConditionIsWritten(t *testing.T) {
	w := newGkaSWorld(t)
	key := "gka-sem/ov"
	n := w.node("n", map[string]string{key: w.sel})
	sel := metav1.LabelSelector{MatchLabels: map[string]string{key: w.sel}}
	fa, fb := w.fleet("fa", sel), w.fleet("fb", sel)
	cfg, rec := gkaRecordingConfig(w.e.Config)
	clk := newGkaFakeClock(gkaST0)
	gkaSStart(t, cfg, gkaSNewAssessor(newGkaSScript(gkaSPlanNone)), clk)

	two := &gkaSC{gkaSNodeCondType, "Unknown", "Conflict", "fleet=" + gkaSNameList([]string{fa.Name, fb.Name})}
	w.waitNode(n.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, two) })
	gkaSSettle(4)
	prefix := "/api/v1/nodes/" + n.Name
	before := len(rec.Writes(prefix))
	if before == 0 {
		t.Fatalf("test setup: the recorder saw no write to %s although the condition was published", prefix)
	}

	clk.Set(gkaST0.Add(100*time.Second + 500*time.Millisecond))
	fc := w.fleet("fc", sel)
	three := &gkaSC{gkaSNodeCondType, "Unknown", "Conflict", "fleet=" + gkaSNameList([]string{fa.Name, fb.Name, fc.Name})}
	w.waitNode(n.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, three) })
	if after := len(rec.Writes(prefix)); after <= before {
		t.Errorf("GKA-114: %d writes to %s before and %d after the message changed, want a new write", before, prefix, after)
	}
	node := w.nodeNow(n.Name)
	gkaSCheckNodeCond(t, "GKA-114 three fleets", node, three)
	if got := gkaSSec(gkaSNodeCond(node).LastTransitionTime); got != gkaSAt0 {
		t.Errorf("GKA-074(c): lastTransitionTime = %s after a message-only change, want %s (status unchanged)", got, gkaSAt0)
	}
}
