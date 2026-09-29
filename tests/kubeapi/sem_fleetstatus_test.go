package kubeapi_test

// GKA-078 (GPUFleet counts and FleetReady table), GKA-079 (assessmentRevision),
// GKA-080 (Policy) and GKA-081 (Intent assembly).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

type gkaSNodeKind int

const (
	gkaSKReady    gkaSNodeKind = iota // InService device, Qualified/Ready decision -> node Eligible
	gkaSKDisq                         // Disqualified/Degraded decision -> node Ineligible/Disqualified
	gkaSKMaint                        // Maintenance device with a Ready decision -> node Ineligible/Qualified
	gkaSKNone                         // no observation -> node Unknown
	gkaSKErr                          // assessor error -> InternalError
	gkaSKConflict                     // second fleet selects the node -> Conflict
)

// GKA-078: every row of the FleetReady table with the counts A' (node unit: ready =
// Eligible, degraded = Disqualified, unknown = the rest, sum = selectedCount), the
// row order (4 before 4b before 5 before 6 before 7), the message tokens
// (qualified_not_eligible, gate_not_implemented, no internal-error token on row 6).
func TestGKA078_FleetReadyTableAllRows(t *testing.T) {
	w := newGkaSWorld(t)
	script := newGkaSScript(gkaSPlanReady)
	a := gkaSNewAssessor(script)

	type scen struct {
		id      string
		enforce bool
		nodes   []gkaSNodeKind
		want    gkaSFleetWant
	}
	scens := []scen{
		// Row 4: any Disqualified node beats everything below it.
		{"d1", false, []gkaSNodeKind{gkaSKDisq, gkaSKReady}, gkaSFleetWant{Selected: 2, Ready: 1, Degraded: 1, Status: "False", Reason: "Degraded", Message: "selected=2 ready=1 degraded=1 unknown=0"}},
		{"d2", false, []gkaSNodeKind{gkaSKDisq, gkaSKConflict}, gkaSFleetWant{Selected: 2, Degraded: 1, Unknown: 1, Status: "False", Reason: "Degraded", Message: "selected=2 ready=0 degraded=1 unknown=1"}},
		{"d3", false, []gkaSNodeKind{gkaSKDisq, gkaSKErr}, gkaSFleetWant{Selected: 2, Degraded: 1, Unknown: 1, Status: "False", Reason: "Degraded", Message: "selected=2 ready=0 degraded=1 unknown=1"}},
		{"d4", false, []gkaSNodeKind{gkaSKDisq}, gkaSFleetWant{Selected: 1, Degraded: 1, Status: "False", Reason: "Degraded", Message: "selected=1 ready=0 degraded=1 unknown=0"}},
		// Row 4b: every selected node Qualified, none Disqualified, not all Eligible.
		{"q1", false, []gkaSNodeKind{gkaSKReady, gkaSKMaint}, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Validating", Message: "selected=2 ready=1 degraded=0 unknown=1; qualified_not_eligible=1"}},
		{"q2", false, []gkaSNodeKind{gkaSKMaint, gkaSKMaint}, gkaSFleetWant{Selected: 2, Unknown: 2, Status: "Unknown", Reason: "Validating", Message: "selected=2 ready=0 degraded=0 unknown=2; qualified_not_eligible=2"}},
		{"q3", true, []gkaSNodeKind{gkaSKReady, gkaSKMaint}, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Validating", Message: "selected=2 ready=1 degraded=0 unknown=1; qualified_not_eligible=1; gate_not_implemented"}},
		// A Conflict node is not Qualified, so 4b does not apply and row 5 wins.
		{"q4", false, []gkaSNodeKind{gkaSKMaint, gkaSKConflict}, gkaSFleetWant{Selected: 2, Unknown: 2, Status: "Unknown", Reason: "Conflict", Message: "selected=2 ready=0 degraded=0 unknown=2"}},
		// Row 5 and its precedence over row 6.
		{"c1", false, []gkaSNodeKind{gkaSKReady, gkaSKConflict}, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Conflict", Message: "selected=2 ready=1 degraded=0 unknown=1"}},
		{"c2", false, []gkaSNodeKind{gkaSKReady, gkaSKConflict, gkaSKErr}, gkaSFleetWant{Selected: 3, Ready: 1, Unknown: 2, Status: "Unknown", Reason: "Conflict", Message: "selected=3 ready=1 degraded=0 unknown=2"}},
		// Row 6: the internal-error token is only for the fleet's own InternalError (row 2).
		{"e1", false, []gkaSNodeKind{gkaSKReady, gkaSKErr}, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "InternalError", Message: "selected=2 ready=1 degraded=0 unknown=1"}},
		{"e2", true, []gkaSNodeKind{gkaSKReady, gkaSKErr}, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "InternalError", Message: "selected=2 ready=1 degraded=0 unknown=1; gate_not_implemented"}},
		// Row 7: some node undecided.
		{"u1", false, []gkaSNodeKind{gkaSKReady, gkaSKNone}, gkaSFleetWant{Selected: 2, Ready: 1, Unknown: 1, Status: "Unknown", Reason: "Validating", Message: "selected=2 ready=1 degraded=0 unknown=1"}},
		// Row 8: every node Eligible, assessmentRevision published (CEL S6).
		{"k1", false, []gkaSNodeKind{gkaSKReady, gkaSKReady}, gkaSFleetWant{Selected: 2, Ready: 2, Status: "True", Reason: "Ready", Message: "selected=2 ready=2 degraded=0 unknown=0"}},
		{"k2", false, []gkaSNodeKind{gkaSKReady}, gkaSFleetWant{Selected: 1, Ready: 1, Status: "True", Reason: "Ready", Message: "selected=1 ready=1 degraded=0 unknown=0"}},
		{"k3", true, []gkaSNodeKind{gkaSKReady, gkaSKReady}, gkaSFleetWant{Selected: 2, Ready: 2, Status: "True", Reason: "Ready", Message: "selected=2 ready=2 degraded=0 unknown=0; gate_not_implemented"}},
	}
	fleets := map[string]*v1alpha1.GPUFleet{}
	nodesOf := map[string][]*corev1.Node{}
	for _, sc := range scens {
		val := w.name("g" + sc.id)
		var muts []func(*v1alpha1.GPUFleet)
		if sc.enforce {
			muts = append(muts, gkaSEnforce)
		}
		f := w.fleet("f"+sc.id, metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/g78": val}}, muts...)
		fleets[sc.id] = f
		helper := ""
		for i, kind := range sc.nodes {
			labels := map[string]string{"gka-sem/g78": val}
			if kind == gkaSKConflict {
				helper = w.name("h" + sc.id)
				labels["gka-sem/h78"] = helper
			}
			n := w.node(fmt.Sprintf("n%s%d", sc.id, i), labels)
			nodesOf[sc.id] = append(nodesOf[sc.id], n)
			desired := "InService"
			if kind == gkaSKMaint {
				desired = "Maintenance"
			}
			w.device(fmt.Sprintf("d%s%d", sc.id, i), n, f, gkaSWithDesired(desired))
			switch kind {
			case gkaSKDisq:
				script.SetNode(n.Name, gkaSPlanDisqualified)
			case gkaSKNone:
				script.SetNode(n.Name, gkaSPlanNone)
			case gkaSKErr:
				script.SetNode(n.Name, gkaSPlanErr(errors.New("scripted assessor failure")))
			}
		}
		if helper != "" { // a second (Audit) fleet selecting the Conflict node
			w.fleet("h"+sc.id, metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/h78": helper}})
		}
	}
	w.start(a, newGkaFakeClock(gkaST0))

	for _, sc := range scens {
		f := fleets[sc.id]
		w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string {
			c := gkaSFleetReadyOf(o)
			if c == nil || c.Message != sc.want.Message || string(c.Status) != sc.want.Status || c.Reason != sc.want.Reason || o.Status.ObservedGeneration != o.Generation {
				return fmt.Sprintf("%s: FleetReady = %v, want {%s %s %q}", sc.id, c, sc.want.Status, sc.want.Reason, sc.want.Message)
			}
			return ""
		})
	}
	gkaSSettle(3)
	for _, sc := range scens {
		want := sc.want
		// GKA-079, independent of the FleetReady row: the revision is published exactly when every selected
		// node has a decision (Node != nil) with a Complete selection, i.e. no node is Conflict, undecided or failed.
		publish := len(sc.nodes) > 0
		pairs := map[string]string{}
		for i, kind := range sc.nodes {
			if kind == gkaSKConflict || kind == gkaSKNone || kind == gkaSKErr {
				publish = false
			}
			uid := string(nodesOf[sc.id][i].UID)
			pairs[uid] = gkaSNodeRevision(uid, string(fleets[sc.id].UID))
		}
		if publish {
			want.Revision = gkaSFleetRevisionOf(pairs)
		}
		gkaSCheckFleet(t, "GKA-078/079 "+sc.id, w.fleetNow(fleets[sc.id].Name), want)
	}
	// Conflict nodes were never assessed (GKA-061) and no scenario node of an Enforce fleet got a condition (GKA-110).
	for _, sc := range scens {
		if !sc.enforce {
			continue
		}
		for i := range sc.nodes {
			n := w.nodeNow(w.name(fmt.Sprintf("n%s%d", sc.id, i)))
			gkaSCheckNodeCond(t, "GKA-110 "+sc.id, n, nil)
		}
	}
}

// fleetRevisionOf recomputes the GKA-079 digest from the (node UID, node revision) pairs.
func gkaSFleetRevisionOf(pairs map[string]string) string {
	uids := make([]string, 0, len(pairs))
	for uid := range pairs {
		uids = append(uids, uid)
	}
	sort.Strings(uids) // byte order of the Node UID
	h := sha256.New()
	h.Write([]byte("dpa.FleetAssessment.v1"))
	h.Write([]byte{0})
	for _, uid := range uids {
		h.Write([]byte(uid))
		h.Write([]byte{0})
		h.Write([]byte(pairs[uid]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// GKA-079: assessmentRevision is SHA-256("dpa.FleetAssessment.v1" 0x00 then, per
// selected node in Node UID byte order, UID 0x00 node revision 0x00), lowercase hex;
// it changes with any node revision and is omitted unless every selected node has a
// decision with a Complete selection.
func TestGKA079_AssessmentRevisionFormulaAndOmission(t *testing.T) {
	w := newGkaSWorld(t)
	n1, n2 := w.selNode("n1"), w.selNode("n2")
	f := w.selFleet("f")
	w.device("d1", n1, f)
	w.device("d2", n2, f)
	script := newGkaSScript(gkaSPlanReady)
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	rev := func(n *corev1.Node) string { return gkaSNodeRevision(string(n.UID), string(f.UID)) }
	expect := func(what string, pairs map[string]string) {
		t.Helper()
		want := gkaSFleetRevisionOf(pairs)
		w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string {
			if o.Status.AssessmentRevision == nil || *o.Status.AssessmentRevision != want {
				return fmt.Sprintf("%s: assessmentRevision = %v, want %s", what, o.Status.AssessmentRevision, want)
			}
			return gkaSFleetIs(o, "True", "Ready")
		})
	}
	omitted := func(what string) {
		t.Helper()
		w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string {
			if o.Status.AssessmentRevision != nil {
				return fmt.Sprintf("%s: assessmentRevision = %s, want omitted", what, *o.Status.AssessmentRevision)
			}
			if c := gkaSFleetReadyOf(o); c == nil || c.Status == "True" {
				return what + ": FleetReady is still True"
			}
			return ""
		})
	}

	expect("both nodes decided", map[string]string{string(n1.UID): rev(n1), string(n2.UID): rev(n2)})

	// Any node's revision participates in the digest.
	changed := gkaSHex("a different node revision")
	script.SetNode(n1.Name, gkaSEdit(gkaSPlanReady, func(req app.NodeAssessmentRequest, na *app.NodeAssessment) {
		na.Node.AssessmentRevision = changed
	}))
	expect("n1 revision changed", map[string]string{string(n1.UID): changed, string(n2.UID): rev(n2)})

	// One node without any decision: omitted (and FleetReady cannot be True).
	script.SetNode(n2.Name, gkaSPlanNone)
	omitted("n2 has no decision")
	script.SetNode(n1.Name, gkaSPlanReady)
	script.SetNode(n2.Name, gkaSPlanReady)
	expect("restored", map[string]string{string(n1.UID): rev(n1), string(n2.UID): rev(n2)})

	// A Partial selection on one node (an unresolved same-name device makes X* non-empty).
	x := w.device("x2", n2, f, func(d *v1alpha1.GPUDevice) { d.Spec.NodeRef.UID = "not-the-uid" })
	omitted("n2 is Partial")
	w.deleteDevice(x.Name)
	expect("Partial resolved", map[string]string{string(n1.UID): rev(n1), string(n2.UID): rev(n2)})
}

// GKA-080: the fleet.Policy handed to the assessor: coverage sorted by name bytes,
// Freshness/ReadyFor from the spec seconds, and a Revision (1-128 ASCII) that is a
// deterministic function of exactly (fleet UID, freshnessSeconds, readyForSeconds,
// name-ordered requiredCoverage): unaffected by mode, selectors, order, generation.
func TestGKA080_PolicyRevisionObservationContract(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	coverage := func() []v1alpha1.FleetCoverageRequirement {
		return []v1alpha1.FleetCoverageRequirement{
			{Name: "beta", PathKind: "gpu-pcie-link-width-normal", Required: true},
			{Name: "alpha", PathKind: "gpu-pcie-root", Required: false},
			{Name: "Alpha", PathKind: "gpu-pcie-parent", Required: true},
		}
	}
	mk := func(f *v1alpha1.GPUFleet) {
		f.Spec.RequiredCoverage = coverage()
		f.Spec.FreshnessSeconds = 61
		f.Spec.ReadyForSeconds = 31
	}
	f := w.selFleet("f", mk)
	d := w.device("d", n, f)
	a := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
	var p0, pLast string

	policyNow := func(t *testing.T, w *gkaSWorld) (fleet.Policy, app.NodeAssessmentRequest) {
		t.Helper()
		cur := w.fleetNow(f.Name)
		w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string {
			if o.Status.ObservedGeneration != cur.Generation {
				return fmt.Sprintf("observedGeneration = %d, want %d", o.Status.ObservedGeneration, cur.Generation)
			}
			return ""
		})
		reqs := gkaSRequestsFor(a, n.Name)
		if len(reqs) == 0 {
			t.Fatalf("GKA-080: assessor never called")
		}
		last := reqs[len(reqs)-1]
		return last.Policy, last
	}
	update := func(t *testing.T, w *gkaSWorld, mut func(*v1alpha1.GPUFleet)) (fleet.Policy, app.NodeAssessmentRequest) {
		t.Helper()
		w.updateFleet(f.Name, mut)
		return policyNow(t, w)
	}

	t.Run("first-controller", func(t *testing.T) {
		w := w.with(t)
		w.start(a, newGkaFakeClock(gkaST0))
		p, req := policyNow(t, w)
		p0 = p.Revision
		if p.Revision == "" || len(p.Revision) > 128 || !gkaSASCII(p.Revision) {
			t.Errorf("GKA-080: Revision = %q, want 1-128 ASCII characters", p.Revision)
		}
		if p.Freshness != 61*time.Second || p.ReadyFor != 31*time.Second {
			t.Errorf("GKA-080: Freshness/ReadyFor = %s/%s, want 1m1s/31s", p.Freshness, p.ReadyFor)
		}
		wantCov := []fleet.CoverageRequirement{
			{Name: "Alpha", PathKind: "gpu-pcie-parent", Required: true},
			{Name: "alpha", PathKind: "gpu-pcie-root", Required: false},
			{Name: "beta", PathKind: "gpu-pcie-link-width-normal", Required: true},
		}
		if fmt.Sprint(p.RequiredCoverage) != fmt.Sprint(wantCov) {
			t.Errorf("GKA-080: RequiredCoverage = %v, want %v (name byte order)", p.RequiredCoverage, wantCov)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("GKA-034/080: policy does not validate: %v", err)
		}
		if req.FleetUID != string(f.UID) {
			t.Errorf("GKA-081: FleetUID = %q, want %s", req.FleetUID, f.UID)
		}
		seen := map[string]string{p0: "initial"}
		mustSame := func(what string, mut func(*v1alpha1.GPUFleet)) {
			t.Helper()
			if got, _ := update(t, w, mut); got.Revision != p0 {
				t.Errorf("GKA-080: %s changed the Revision %q -> %q, want unchanged", what, p0, got.Revision)
			}
		}
		mustDiffer := func(what string, mut func(*v1alpha1.GPUFleet)) {
			t.Helper()
			got, _ := update(t, w, mut)
			if prev, dup := seen[got.Revision]; dup {
				t.Errorf("GKA-080: %s produced Revision %q, already produced by %s, want a new value", what, got.Revision, prev)
			}
			seen[got.Revision] = what
		}
		// Not part of the Revision.
		mustSame("mode + canarySelector", gkaSEnforce)
		mustSame("canarySelector", func(f *v1alpha1.GPUFleet) {
			f.Spec.CanarySelector = &metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/canary": "y"}}
		})
		mustSame("nodeSelector", func(f *v1alpha1.GPUFleet) {
			f.Spec.NodeSelector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: gkaSSelKey, Operator: metav1.LabelSelectorOpExists}}
		})
		mustSame("requiredCoverage order", func(f *v1alpha1.GPUFleet) {
			c := coverage()
			f.Spec.RequiredCoverage = []v1alpha1.FleetCoverageRequirement{c[2], c[0], c[1]}
		})
		// Each of the four inputs.
		mustDiffer("freshnessSeconds", func(f *v1alpha1.GPUFleet) { f.Spec.FreshnessSeconds = 62 })
		mustDiffer("readyForSeconds", func(f *v1alpha1.GPUFleet) { f.Spec.ReadyForSeconds = 32 })
		mustDiffer("required flag", func(f *v1alpha1.GPUFleet) {
			for i := range f.Spec.RequiredCoverage {
				if f.Spec.RequiredCoverage[i].Name == "alpha" {
					f.Spec.RequiredCoverage[i].Required = true
				}
			}
		})
		mustDiffer("pathKind", func(f *v1alpha1.GPUFleet) {
			for i := range f.Spec.RequiredCoverage {
				if f.Spec.RequiredCoverage[i].Name == "beta" {
					f.Spec.RequiredCoverage[i].PathKind = "gpu-pcie-root"
				}
			}
		})
		mustDiffer("added coverage", func(f *v1alpha1.GPUFleet) {
			f.Spec.RequiredCoverage = append(f.Spec.RequiredCoverage, v1alpha1.FleetCoverageRequirement{Name: "gamma", PathKind: "gpu-nic-shared-ancestor", Required: false})
		})
		// Back to the original four inputs (different order, mode, selector): the original value again.
		mustSame("restoring the original inputs", func(f *v1alpha1.GPUFleet) {
			c := coverage()
			f.Spec.RequiredCoverage = []v1alpha1.FleetCoverageRequirement{c[1], c[2], c[0]}
			f.Spec.FreshnessSeconds, f.Spec.ReadyForSeconds = 61, 31
			f.Spec.Mode = v1alpha1.FleetMode("Audit")
		})

		// Same inputs, different fleet UID: a different Revision.
		w.deleteFleet(f.Name)
		f2 := w.fleet("f", metav1.LabelSelector{MatchLabels: map[string]string{gkaSSelKey: w.sel}}, mk)
		if f2.UID == f.UID {
			t.Fatalf("test setup: recreated fleet reused the UID")
		}
		w.updateDevice(d.Name, func(o *v1alpha1.GPUDevice) { o.Spec.FleetRef.UID = string(f2.UID) })
		gkaEventually(t, gkaSWaitMax, func() (bool, string) {
			reqs := gkaSRequestsFor(a, n.Name)
			if len(reqs) == 0 || reqs[len(reqs)-1].FleetUID != string(f2.UID) {
				return false, "no request for the recreated fleet yet"
			}
			return true, ""
		})
		reqs := gkaSRequestsFor(a, n.Name)
		pLast = reqs[len(reqs)-1].Policy.Revision
		if pLast == p0 {
			t.Errorf("GKA-080: a different fleet UID produced the same Revision %q", pLast)
		}
	})

	t.Run("restarted-controller", func(t *testing.T) {
		w := w.with(t)
		a2 := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
		w.start(a2, newGkaFakeClock(gkaST0))
		gkaEventually(t, gkaSWaitMax, func() (bool, string) {
			return len(gkaSRequestsFor(a2, n.Name)) > 0, "the restarted controller has not assessed the node yet"
		})
		if got := gkaSRequestsFor(a2, n.Name)[0].Policy.Revision; got != pLast {
			t.Errorf("GKA-080: Revision after a restart = %q, want the same %q for the same inputs", got, pLast)
		}
	})
}

// GKA-081: the NodeAssessmentRequest and every Intent are assembled exactly from the
// objects: ClusterID from Options, names/UIDs, desired state, request id, generation,
// ObservedAt = intentObservedAt, claim fields (nil uuid/serial -> ""), FleetUID,
// Selection, Now, UID-ordered intents; a spec edit bumps MetadataGeneration.
func TestGKA081_IntentAssembly(t *testing.T) {
	w := newGkaSWorld(t)
	n1, n2 := w.selNode("n1"), w.selNode("n2")
	f := w.selFleet("f")
	dU := w.device("du", n1, f)
	dS := w.device("ds", n1, f, gkaSWithClaim("AMD", nil, gkaSPtr("SN-"+w.name("ds"))))
	dB := w.device("db", n1, f, gkaSWithDesired("Maintenance"), func(d *v1alpha1.GPUDevice) {
		d.Spec.Request = v1alpha1.LifecycleRequest{ID: "maint-7", Reason: "swap"}
		d.Spec.InventoryClaim.Serial = gkaSPtr("SN-" + w.name("db"))
		d.Spec.InventoryClaim.Source = "operator/other-source"
		d.Spec.InventoryClaim.EvidenceID = "asset-db:custom-evidence"
	})
	dV := w.device("dv", n2, f)
	a := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
	clk := newGkaFakeClock(gkaST0)
	w.start(a, clk)

	gkaEventually(t, gkaSWaitMax, func() (bool, string) {
		return len(gkaSRequestsFor(a, n1.Name)) > 0 && len(gkaSRequestsFor(a, n2.Name)) > 0, "waiting for both nodes to be assessed"
	})
	desired := map[string]fleet.DesiredState{dU.Name: fleet.DesiredInService, dS.Name: fleet.DesiredInService, dB.Name: fleet.DesiredMaintenance, dV.Name: fleet.DesiredInService}
	check := func(req app.NodeAssessmentRequest, node *corev1.Node, devs []*v1alpha1.GPUDevice, generations map[string]int64) {
		t.Helper()
		wantNode := fleet.NodeRef{ClusterID: gkaSClusterID, Name: node.Name, UID: string(node.UID)}
		if req.Node != wantNode || req.FleetUID != string(f.UID) || req.Selection != fleet.SelectionComplete || !req.Now.Equal(gkaST0) {
			t.Errorf("GKA-081: request = node %+v fleetUID %q selection %s now %s, want %+v %s Complete %s",
				req.Node, req.FleetUID, req.Selection.String(), req.Now.UTC(), wantNode, f.UID, gkaST0)
		}
		if err := req.Policy.Validate(); err != nil {
			t.Errorf("GKA-081: request policy invalid: %v", err)
		}
		if len(req.Intents) != len(devs) {
			t.Fatalf("GKA-081: %d intents, want %d", len(req.Intents), len(devs))
		}
		uids := make([]string, len(req.Intents))
		for i, in := range req.Intents {
			uids[i] = in.Device.UID
		}
		if !sort.StringsAreSorted(uids) {
			t.Errorf("GKA-081: intents are not in device UID byte order: %v", uids)
		}
		byName := map[string]*v1alpha1.GPUDevice{}
		for _, d := range devs {
			byName[d.Name] = d
		}
		for _, in := range req.Intents {
			d := byName[in.Device.Name]
			if d == nil {
				t.Errorf("GKA-081: intent for unexpected device %q", in.Device.Name)
				continue
			}
			uuid, serial := "", ""
			if d.Spec.InventoryClaim.UUID != nil {
				uuid = *d.Spec.InventoryClaim.UUID
			}
			if d.Spec.InventoryClaim.Serial != nil {
				serial = *d.Spec.InventoryClaim.Serial
			}
			wantIntent := fleet.Intent{
				Device: fleet.DeviceRef{Name: d.Name, UID: string(d.UID)}, Node: wantNode, Desired: desired[d.Name],
				RequestID: d.Spec.Request.ID, MetadataGeneration: in.MetadataGeneration, ObservedAt: in.ObservedAt,
				Claim: fleet.InventoryClaim{Vendor: d.Spec.InventoryClaim.Vendor, UUID: uuid, Serial: serial, Source: d.Spec.InventoryClaim.Source, EvidenceID: d.Spec.InventoryClaim.EvidenceID},
			}
			if in != wantIntent {
				t.Errorf("GKA-081: intent for %s = %+v, want %+v", d.Name, in, wantIntent)
			}
			if int64(in.MetadataGeneration) != generations[d.Name] {
				t.Errorf("GKA-081: intent %s MetadataGeneration = %d, want metadata.generation %d", d.Name, int64(in.MetadataGeneration), generations[d.Name])
			}
			if !in.ObservedAt.Equal(gkaST0) {
				t.Errorf("GKA-081/071: intent %s ObservedAt = %s, want %s (exact-second pass time)", d.Name, in.ObservedAt.UTC(), gkaST0)
			}
			if err := in.Validate(); err != nil {
				t.Errorf("GKA-081: intent %s does not validate: %v", d.Name, err)
			}
		}
	}
	last := func(node *corev1.Node) app.NodeAssessmentRequest {
		reqs := gkaSRequestsFor(a, node.Name)
		return reqs[len(reqs)-1]
	}
	gens := map[string]int64{dU.Name: 1, dS.Name: 1, dB.Name: 1, dV.Name: 1}
	check(last(n1), n1, []*v1alpha1.GPUDevice{dU, dS, dB}, gens)
	check(last(n2), n2, []*v1alpha1.GPUDevice{dV}, gens)

	// A spec edit (reason only) raises the generation the assessor sees.
	w.updateDevice(dB.Name, func(d *v1alpha1.GPUDevice) { d.Spec.Request.Reason = "swap done" })
	gkaEventually(t, gkaSWaitMax, func() (bool, string) {
		for _, in := range last(n1).Intents {
			if in.Device.Name == dB.Name && in.MetadataGeneration == 2 {
				return true, ""
			}
		}
		return false, "the assessor has not seen generation 2 yet"
	})
	gens[dB.Name] = 2
	dB2 := w.deviceNow(dB.Name)
	gkaSSettle(2)
	check(last(n1), n1, []*v1alpha1.GPUDevice{dU, dS, dB2}, gens)
}
