package kubeapi_test

// GKA-071 (intent observation time), GKA-072 (non-decision status), GKA-073
// (decision status) and the spec section 12 normative examples, including the
// GKA-074(c) lastTransitionTime rules and GKA-083 omit rules.

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

const (
	gkaSDay   = "2026-09-30T"
	gkaSAt0   = gkaSDay + "00:00:00Z"
	gkaSAt30  = gkaSDay + "00:00:30Z"
	gkaSAt530 = gkaSDay + "00:05:30Z"
)

// GKA-072 / spec section 12 "cold-start GPUDevice status": the S3a product state
// (assessor knows nothing). Every field of the four resources and the Node
// condition, the GKA-083 omissions, the "[]" lists, and lastTransitionTime.
func TestGKA072_083_ColdStartMatchesSpecExample(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	d := w.device("d", n, f)
	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanNone)), newGkaFakeClock(gkaST0))

	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Validating") })
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Validating") })
	w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	nodeCond := &gkaSC{gkaSNodeCondType, "Unknown", "Validating", "fleet=" + f.Name + "; no_observation"}
	w.waitNode(n.Name, func(o *corev1.Node) string { return gkaSNodeCondIs(o, nodeCond) })
	gkaSSettle(3)

	dev := w.deviceNow(d.Name)
	gkaSCheckNonDecision(t, "GKA-072 device", dev, gkaSNonDec{SR: "Validating", Phase: "Pending", Tail: []string{"no_observation"}, Intent: gkaSAt0})
	for _, typ := range []string{"AllocationKnown", "DeviceQualified", "IdentityBound", "LifecycleReady"} {
		gkaSLTT(t, "GKA-074(c) device", dev.Status.Conditions, typ, gkaSAt0)
	}
	if dev.Status.ObservedGeneration != 1 || dev.Status.ObservedRequestID != "enroll-1" {
		t.Errorf("GKA-071: observedGeneration/observedRequestID = %d/%q, want 1/enroll-1", dev.Status.ObservedGeneration, dev.Status.ObservedRequestID)
	}

	nps := w.npsNow(n.Name)
	gkaSCheckNPS(t, "GKA-076/077 cold NodePathState", nps, gkaSNPSWant{
		Elig: "Unknown", Reason: "Validating", NodeTail: []string{"no_observation"}, FreshTail: []string{"no_observation"},
	})
	for _, typ := range []string{"EvidenceFresh", "NodeEligible"} {
		gkaSLTT(t, "GKA-074(c) NodePathState", nps.Status.Conditions, typ, gkaSAt0)
	}
	if ds := gkaSSummaries(nps)[d.Name]; len(nps.Status.DeviceSummaries) != 1 || ds.UID != string(d.UID) || ds.DesiredState != "InService" ||
		ds.ObservedGeneration != 1 || ds.Qualification != "Unknown" || ds.LifecyclePhase != "Pending" {
		t.Errorf("GKA-076: deviceSummaries = %#v, want the device as InService/gen 1/Unknown/Pending", nps.Status.DeviceSummaries)
	}
	if nps.Status.CollectorSession != nil || nps.Status.GateOwnership != nil || nps.Status.GraphRevision != nil {
		t.Errorf("GKA-105/083: collectorSession/gateOwnership/graphRevision = %v/%v/%v, want omitted", nps.Status.CollectorSession, nps.Status.GateOwnership, nps.Status.GraphRevision)
	}

	fl := w.fleetNow(f.Name)
	gkaSCheckFleet(t, "GKA-078 cold fleet", fl, gkaSFleetWant{
		Selected: 1, Unknown: 1, Status: "Unknown", Reason: "Validating", Message: "selected=1 ready=0 degraded=0 unknown=1",
	})
	gkaSLTT(t, "GKA-074(c) fleet", fl.Status.Conditions, "FleetReady", gkaSAt0)

	node := w.nodeNow(n.Name)
	gkaSCheckNodeCond(t, "GKA-111 cold node condition", node, nodeCond)
	if c := gkaSNodeCond(node); c != nil && gkaSSec(c.LastTransitionTime) != gkaSAt0 {
		t.Errorf("GKA-074(c): node condition lastTransitionTime = %s, want %s", gkaSSec(c.LastTransitionTime), gkaSAt0)
	}

	// Raw JSON: required lists are present and empty ("[]"), optional revision/session
	// fields are absent (GKA-013, GKA-083, GKA-105).
	rawDev := w.raw("GPUDevice", d.Name)
	for _, path := range [][]string{
		{"status", "coverage"}, {"status", "findingRefs"}, {"status", "evidenceRefs"}, {"status", "conditions"},
		{"status", "allocation", "evidenceRefs"}, {"status", "allocation", "affectedWorkloads"},
	} {
		if arr, ok := gkaSRawArray(rawDev, path...); !ok || (path[len(path)-1] != "conditions" && len(arr) != 0) {
			t.Errorf("GKA-072: %v = %v (present=%v), want a present list", path, arr, ok)
		}
	}
	if st, _ := rawDev["status"].(map[string]any); st != nil {
		if _, has := st["graphRevision"]; has {
			t.Errorf("GKA-083: device status carries graphRevision without a decision")
		}
	}
	if arr, ok := gkaSRawArray(w.raw("GPUFleet", f.Name), "status", "ownedNodeRefs"); !ok || len(arr) != 0 {
		t.Errorf("GKA-105: fleet ownedNodeRefs = %v (present=%v), want []", arr, ok)
	}
	if st, _ := w.raw("GPUFleet", f.Name)["status"].(map[string]any); st != nil {
		if _, has := st["assessmentRevision"]; has {
			t.Errorf("GKA-079/083: fleet status carries assessmentRevision without a complete decision")
		}
	}
	rawNPS := w.raw("NodePathState", n.Name)
	if arr, ok := gkaSRawArray(rawNPS, "status", "deviceSummaries"); !ok || len(arr) != 1 {
		t.Errorf("GKA-076: raw deviceSummaries = %v (present=%v)", arr, ok)
	}
	if st, _ := rawNPS["status"].(map[string]any); st != nil {
		for _, k := range []string{"collectorSession", "gateOwnership", "graphRevision"} {
			if _, has := st[k]; has {
				t.Errorf("GKA-105/083: NodePathState status carries %s", k)
			}
		}
	}
}

// GKA-072: without a decision the phase is the baseline of the desired state
// (InService->Pending, Maintenance->MaintenancePending, Retired->Retiring), and
// the desired state reaches the assessor as fleet.DesiredState (GKA-081).
func TestGKA072_BaselinePhaseFollowsDesiredState(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	dIn := w.device("din", n, f)
	dMa := w.device("dma", n, f, gkaSWithDesired("Maintenance"))
	dRe := w.device("dre", n, f, gkaSWithDesired("Retired"))
	a := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
	w.start(a, newGkaFakeClock(gkaST0))

	cases := []struct {
		d     *v1alpha1.GPUDevice
		phase string
		want  fleet.DesiredState
	}{
		{dIn, "Pending", fleet.DesiredInService},
		{dMa, "MaintenancePending", fleet.DesiredMaintenance},
		{dRe, "Retiring", fleet.DesiredRetired},
	}
	for _, c := range cases {
		w.waitDevice(c.d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Validating") })
		gkaSCheckNonDecision(t, "GKA-072 "+c.d.Name, w.deviceNow(c.d.Name), gkaSNonDec{
			SR: "Validating", Phase: c.phase, Tail: []string{"no_observation"},
		})
	}
	reqs := gkaSRequestsFor(a, n.Name)
	if len(reqs) == 0 {
		t.Fatalf("GKA-081: assessor never called")
	}
	got := map[string]fleet.DesiredState{}
	for _, in := range reqs[len(reqs)-1].Intents {
		got[in.Device.Name] = in.Desired
	}
	for _, c := range cases {
		if got[c.d.Name] != c.want {
			t.Errorf("GKA-081: intent Desired for %s = %s, want %s", c.d.Name, got[c.d.Name].String(), c.want.String())
		}
	}
	// No observation at all: the node is Unknown/Validating whatever the desired states are.
	gkaSCheckNPS(t, "GKA-076 node with baseline devices", w.npsNow(n.Name), gkaSNPSWant{
		Elig: "Unknown", Reason: "Validating", NodeTail: []string{"no_observation"}, FreshTail: []string{"no_observation"},
	})
}

// GKA-073 + section 12 "observed GPUDevice status" + GKA-074(c): one Unknown->Qualified
// transition and back. The lastTransitionTime is the pass time truncated to the second
// when the status flips (00:00:30.7 -> 00:00:30, 00:01:00.9 -> 00:01:00), and is kept
// when only reason or message change (AllocationKnown never flips; DeviceQualified
// changes reason Validating -> CoverageMissing at the same Unknown status).
func TestGKA073_DecisionStatusMatchesSpecExample(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	d := w.device("d", n, f)
	// A second device whose claim carries a serial as well: serial is copied only when non-empty.
	dS := w.device("ds", n, f, gkaSWithClaim("NVIDIA", gkaSPtr("GPU-"+w.name("ds")), gkaSPtr("SN-"+w.name("ds"))))
	script := newGkaSScript(gkaSPlanNone)
	clk := newGkaFakeClock(gkaST0)
	w.start(gkaSNewAssessor(script), clk)

	// Phase 1: no observation at 00:00:00.
	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Validating") })
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Validating") })

	// Phase 2: observed Qualified/Ready at 00:00:30.7.
	clk.Set(gkaST0.Add(30*time.Second + 700*time.Millisecond))
	script.SetDefault(gkaSPlanReady)
	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string {
		if o.Status.LifecyclePhase != "Ready" {
			return "phase = " + string(o.Status.LifecyclePhase)
		}
		return ""
	})
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "True", "Ready") })
	w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNode(n.Name, func(o *corev1.Node) string {
		return gkaSNodeCondIs(o, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + f.Name})
	})
	gkaSSettle(3)
	dev := w.deviceNow(d.Name)
	nodeUID := string(n.UID)
	bdf := gkaSBDF(string(d.UID))

	s := dev.Status
	if s.ObservedGeneration != 1 || s.ObservedRequestID != "enroll-1" || gkaSSec(s.IntentObservedAt) != gkaSAt0 {
		t.Errorf("GKA-071: generation/requestID/intentObservedAt = %d/%q/%s, want 1/enroll-1/%s (kept across passes)",
			s.ObservedGeneration, s.ObservedRequestID, gkaSSec(s.IntentObservedAt), gkaSAt0)
	}
	if s.GraphRevision == nil || *s.GraphRevision != gkaSGraphRev(nodeUID) {
		t.Errorf("GKA-073: graphRevision = %v, want %s", s.GraphRevision, gkaSGraphRev(nodeUID))
	}
	if s.Qualification != "Qualified" || s.LifecyclePhase != "Ready" {
		t.Errorf("GKA-073: qualification/phase = %s/%s, want Qualified/Ready", s.Qualification, s.LifecyclePhase)
	}
	ab := s.ActualBinding
	if ab.State != "Bound" || ab.Reason != "Ready" {
		t.Errorf("GKA-073: actualBinding state/reason = %s/%s, want Bound/Ready", ab.State, ab.Reason)
	}
	strEq := func(what string, got *string, want string) {
		if got == nil || *got != want {
			t.Errorf("GKA-073: actualBinding.%s = %v, want %q", what, got, want)
		}
	}
	strEq("vendor", ab.Vendor, "NVIDIA")
	strEq("uuid", ab.UUID, *d.Spec.InventoryClaim.UUID)
	strEq("nodeUID", ab.NodeUID, nodeUID)
	strEq("bootID", ab.BootID, "boot-1")
	strEq("bdf", ab.BDF, bdf)
	strEq("functionKey", ab.FunctionKey, gkaSFunction(bdf).Key())
	strEq("evidenceID", ab.EvidenceID, "nb:7:2:c0ffee")
	if want := "PCIeFunction/pci-bdf:" + bdf; ab.FunctionKey != nil && *ab.FunctionKey != want {
		t.Errorf("GKA-073/section 12: functionKey = %q, want %q", *ab.FunctionKey, want)
	}
	if ab.Serial != nil {
		t.Errorf("GKA-073: actualBinding.serial = %q for an empty claim serial, want omitted", *ab.Serial)
	}
	if ab.Source == nil || ab.Source.Type != "agent" || ab.Source.Name != "path-agent/nvidia-smi" {
		t.Errorf("GKA-073: actualBinding.source = %#v, want {agent path-agent/nvidia-smi}", ab.Source)
	}
	if ab.ObservedAt == nil || gkaSSec(*ab.ObservedAt) != gkaSAt30 || ab.ExpiresAt == nil || gkaSSec(*ab.ExpiresAt) != gkaSAt530 {
		t.Errorf("GKA-073: actualBinding observedAt/expiresAt = %v/%v, want %s/%s", ab.ObservedAt, ab.ExpiresAt, gkaSAt30, gkaSAt530)
	}
	al := s.Allocation
	if al.State != "Unknown" || al.Reason != "AllocationUnknown" || al.Profile != nil || al.ObservedAt != nil || al.ExpiresAt != nil ||
		al.EvidenceRefs == nil || len(al.EvidenceRefs) != 0 || al.AffectedWorkloads == nil || len(al.AffectedWorkloads) != 0 {
		t.Errorf("GKA-073: allocation = %#v, want Unknown/AllocationUnknown with empty lists", al)
	}
	if len(s.Coverage) != 3 {
		t.Fatalf("GKA-073: coverage = %#v, want 3 rows", s.Coverage)
	}
	for i, rc := range gkaSRequiredCoverage() { // already byte sorted: pcie-parent < pcie-root < pcie-width
		c := s.Coverage[i]
		eid := "pe:7:2:" + rc.Name
		if c.Name != rc.Name || c.PathKind != rc.PathKind || c.State != "Normal" || c.Reason != "Normal" || len(c.EvidenceRefs) != 1 || c.EvidenceRefs[0] != eid ||
			c.ObservedAt == nil || gkaSSec(*c.ObservedAt) != gkaSAt30 || c.LatestObservedAt == nil || gkaSSec(*c.LatestObservedAt) != gkaSAt30 ||
			c.ExpiresAt == nil || gkaSSec(*c.ExpiresAt) != gkaSAt530 {
			t.Errorf("GKA-073: coverage[%d] = %#v, want %s Normal with %s and 00:00:30/00:05:30", i, c, rc.Name, eid)
		}
	}
	if s.FindingRefs == nil || len(s.FindingRefs) != 0 {
		t.Errorf("GKA-073: findingRefs = %#v, want []", s.FindingRefs)
	}
	if len(s.EvidenceRefs) != 3 {
		t.Fatalf("GKA-073: evidenceRefs = %#v, want 3 entries", s.EvidenceRefs)
	}
	for i, rc := range gkaSRequiredCoverage() {
		e := s.EvidenceRefs[i]
		eid := "pe:7:2:" + rc.Name
		if e.ID != eid || e.Summary != "evidence "+eid || gkaSSec(e.ObservedAt) != gkaSAt30 || gkaSSec(e.ExpiresAt) != gkaSAt530 {
			t.Errorf("GKA-073: evidenceRefs[%d] = %#v, want %s (id order)", i, e, eid)
		}
	}
	gkaSCheckConds(t, "GKA-075 decision conditions", s.Conditions, 1, []gkaSC{
		{"AllocationKnown", "Unknown", "AllocationUnknown", "allocation=Unknown"},
		{"DeviceQualified", "True", "Ready", "qualification=Qualified phase=Ready"},
		{"IdentityBound", "True", "Ready", "binding=Bound"},
		{"LifecycleReady", "True", "Ready", "phase=Ready"},
	})
	// section 12: AllocationKnown kept 00:00:00 (status never changed); the others flipped at 00:00:30.
	gkaSLTT(t, "GKA-074(c)", s.Conditions, "AllocationKnown", gkaSAt0)
	for _, typ := range []string{"DeviceQualified", "IdentityBound", "LifecycleReady"} {
		gkaSLTT(t, "GKA-074(c)", s.Conditions, typ, gkaSAt30)
	}
	// The claim serial is copied when non-empty.
	if ds := w.deviceNow(dS.Name); ds.Status.ActualBinding.Serial == nil || *ds.Status.ActualBinding.Serial != *dS.Spec.InventoryClaim.Serial {
		t.Errorf("GKA-073: actualBinding.serial = %v, want %q", ds.Status.ActualBinding.Serial, *dS.Spec.InventoryClaim.Serial)
	}
	// Fleet, NodePathState and Node condition flipped at 00:00:30 too.
	fl := w.fleetNow(f.Name)
	gkaSCheckFleet(t, "GKA-078 observed fleet", fl, gkaSFleetWant{
		Selected: 1, Ready: 1, Status: "True", Reason: "Ready", Message: "selected=1 ready=1 degraded=0 unknown=0", Revision: "*",
	})
	gkaSLTT(t, "GKA-074(c) fleet", fl.Status.Conditions, "FleetReady", gkaSAt30)
	nps := w.npsNow(n.Name)
	gkaSCheckNPS(t, "GKA-077 observed NodePathState", nps, gkaSNPSWant{
		Elig: "True", Reason: "Ready", Qual: "Qualified", FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: gkaSGraphRev(nodeUID),
	})
	for _, typ := range []string{"EvidenceFresh", "NodeEligible"} {
		gkaSLTT(t, "GKA-074(c) NodePathState", nps.Status.Conditions, typ, gkaSAt30)
	}
	if c := gkaSNodeCond(w.nodeNow(n.Name)); c == nil || gkaSSec(c.LastTransitionTime) != gkaSAt30 {
		t.Errorf("GKA-074(c): node condition = %#v, want lastTransitionTime %s", c, gkaSAt30)
	}

	// Phase 3: at 00:01:00.9 the decision becomes Unknown/Validating: statuses flip, times truncate.
	clk.Set(gkaST0.Add(time.Minute + 900*time.Millisecond))
	script.SetDefault(gkaSPlanUnknown("Validating"))
	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Validating") })
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Validating") })
	gkaSSettle(3)
	dev = w.deviceNow(d.Name)
	gkaSCheckConds(t, "GKA-075 unknown decision", dev.Status.Conditions, 1, []gkaSC{
		{"AllocationKnown", "Unknown", "AllocationUnknown", "allocation=Unknown"},
		{"DeviceQualified", "Unknown", "Validating", "qualification=Unknown phase=Pending"},
		{"IdentityBound", "Unknown", "Validating", "binding=Unknown"},
		{"LifecycleReady", "Unknown", "Validating", "phase=Pending"},
	})
	gkaSLTT(t, "GKA-074(c)", dev.Status.Conditions, "AllocationKnown", gkaSAt0)
	for _, typ := range []string{"DeviceQualified", "IdentityBound", "LifecycleReady"} {
		gkaSLTT(t, "GKA-074(c)", dev.Status.Conditions, typ, gkaSDay+"00:01:00Z")
	}
	if dev.Status.ActualBinding.State != "Unknown" || dev.Status.ActualBinding.Reason != "Validating" || dev.Status.ActualBinding.Vendor != nil || dev.Status.ActualBinding.BDF != nil {
		t.Errorf("GKA-073: actualBinding for an Unknown decision = %#v, want {Unknown Validating} only", dev.Status.ActualBinding)
	}
	if dev.Status.GraphRevision == nil || *dev.Status.GraphRevision != gkaSGraphRev(nodeUID) {
		t.Errorf("GKA-073: an Unknown decision still carries dec.GraphRevision, got %v", dev.Status.GraphRevision)
	}
	if len(dev.Status.Coverage) != 0 || len(dev.Status.EvidenceRefs) != 0 {
		t.Errorf("GKA-073: Unknown decision without coverage/evidence renders coverage=%v evidenceRefs=%v", dev.Status.Coverage, dev.Status.EvidenceRefs)
	}

	// Phase 4: only the reason changes (Unknown -> Unknown): lastTransitionTime is kept.
	clk.Set(gkaST0.Add(90 * time.Second))
	script.SetDefault(gkaSPlanUnknown("CoverageMissing"))
	dev = w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "CoverageMissing") })
	gkaSCheckConds(t, "GKA-075 reason-only change", dev.Status.Conditions, 1, []gkaSC{
		{"AllocationKnown", "Unknown", "AllocationUnknown", "allocation=Unknown"},
		{"DeviceQualified", "Unknown", "CoverageMissing", "qualification=Unknown phase=Pending"},
		{"IdentityBound", "Unknown", "CoverageMissing", "binding=Unknown"},
		{"LifecycleReady", "Unknown", "CoverageMissing", "phase=Pending"},
	})
	gkaSLTT(t, "GKA-074(c)", dev.Status.Conditions, "AllocationKnown", gkaSAt0)
	for _, typ := range []string{"DeviceQualified", "IdentityBound", "LifecycleReady"} {
		gkaSLTT(t, "GKA-074(c) reason-only", dev.Status.Conditions, typ, gkaSDay+"00:01:00Z")
	}
	if dev.Status.ActualBinding.Reason != "CoverageMissing" {
		t.Errorf("GKA-073: actualBinding.reason = %q, want dec.Reason CoverageMissing", dev.Status.ActualBinding.Reason)
	}
	if gkaSSec(dev.Status.IntentObservedAt) != gkaSAt0 {
		t.Errorf("GKA-071: intentObservedAt drifted to %s across passes", gkaSSec(dev.Status.IntentObservedAt))
	}
}

// GKA-071: intentObservedAt is the pass time rounded UP to the second (an exact
// second stays), while lastTransitionTime is the same time truncated; the value is
// the assessor's Intent.ObservedAt and is at most 1 s after req.Now. It is kept while
// generation and request.id are unchanged (also across a controller restart and for
// metadata-only edits) and renewed when either changes.
func TestGKA071_IntentObservedAtRoundsUpAndIsPreserved(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	a := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
	clk := newGkaFakeClock(gkaST0.Add(400 * time.Millisecond))
	type boundary struct {
		suffix string
		clock  time.Time
		want   string // intentObservedAt
		ltt    string // lastTransitionTime
	}
	boundaries := []boundary{
		{"b400", gkaST0.Add(400 * time.Millisecond), gkaSDay + "00:00:01Z", gkaSAt0},
		{"bexact", gkaST0.Add(time.Minute), gkaSDay + "00:01:00Z", gkaSDay + "00:01:00Z"},
		{"b1ns", gkaST0.Add(2*time.Minute + time.Nanosecond), gkaSDay + "00:02:01Z", gkaSDay + "00:02:00Z"},
		{"b999", gkaST0.Add(3*time.Minute + 999999999*time.Nanosecond), gkaSDay + "00:03:01Z", gkaSDay + "00:03:00Z"},
	}
	type before struct {
		rv     string
		intent string
	}
	saved := map[string]before{}
	var devices []*v1alpha1.GPUDevice

	t.Run("first-controller", func(t *testing.T) {
		w := w.with(t)
		w.start(a, clk)
		for _, b := range boundaries {
			clk.Set(b.clock)
			d := w.device(b.suffix, n, f)
			devices = append(devices, d)
			got := w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Validating") })
			if gkaSSec(got.Status.IntentObservedAt) != b.want {
				t.Errorf("GKA-071 %s: intentObservedAt = %s for pass time %s, want %s (round up)", b.suffix, gkaSSec(got.Status.IntentObservedAt), b.clock.Format(time.RFC3339Nano), b.want)
			}
			gkaSLTT(t, "GKA-074(c) "+b.suffix, got.Status.Conditions, "DeviceQualified", b.ltt)
			// The assessor received the same value, at most 1 s after req.Now (GKA-071, GKA-023).
			found := false
			for _, r := range gkaSRequestsFor(a, n.Name) {
				for _, in := range r.Intents {
					if in.Device.Name != d.Name {
						continue
					}
					found = true
					if gkaSSec(metav1.NewTime(in.ObservedAt)) != b.want || !r.Now.Equal(b.clock) {
						t.Errorf("GKA-071 %s: Intent.ObservedAt/req.Now = %s/%s, want %s/%s", b.suffix, in.ObservedAt.UTC(), r.Now.UTC(), b.want, b.clock.UTC())
					}
					if lag := in.ObservedAt.Sub(r.Now); lag < 0 || lag > time.Second {
						t.Errorf("GKA-071 %s: Intent.ObservedAt - req.Now = %s, want within [0, 1s]", b.suffix, lag)
					}
					break
				}
				if found {
					break
				}
			}
			if !found {
				t.Errorf("GKA-071 %s: assessor never received an intent for %s", b.suffix, d.Name)
			}
			saved[d.Name] = before{rv: got.ResourceVersion, intent: b.want}
		}

		// Preservation: the clock jumps an hour; nothing about these devices changed.
		clk.Set(gkaST0.Add(time.Hour))
		start := len(a.Requests())
		gkaEventually(t, gkaSWaitMax, func() (bool, string) { return len(a.Requests()) >= start+3, "waiting for more passes" })
		gkaSSettle(3)
		for _, d := range devices {
			got := w.deviceNow(d.Name)
			if gkaSSec(got.Status.IntentObservedAt) != saved[d.Name].intent {
				t.Errorf("GKA-071: %s intentObservedAt changed to %s after the clock moved", d.Name, gkaSSec(got.Status.IntentObservedAt))
			}
			if got.ResourceVersion != saved[d.Name].rv {
				t.Errorf("GKA-071/122: %s was rewritten (resourceVersion %s -> %s) although nothing changed", d.Name, saved[d.Name].rv, got.ResourceVersion)
			}
		}

		// A metadata-only edit does not change generation: intentObservedAt stays.
		target := devices[0].Name
		w.updateDevice(target, func(d *v1alpha1.GPUDevice) { d.Labels = map[string]string{"gka-sem/edit": "label-only"} })
		gkaSSettle(4)
		if got := w.deviceNow(target); gkaSSec(got.Status.IntentObservedAt) != saved[target].intent || got.Status.ObservedGeneration != 1 {
			t.Errorf("GKA-071: label-only edit changed intentObservedAt/observedGeneration to %s/%d", gkaSSec(got.Status.IntentObservedAt), got.Status.ObservedGeneration)
		}

		// Only request.reason changes: generation 2, same request id -> renewed at the new pass time.
		clk.Set(gkaST0.Add(2*time.Hour + 250*time.Millisecond))
		w.updateDevice(target, func(d *v1alpha1.GPUDevice) { d.Spec.Request.Reason = "changed reason" })
		got := w.waitDevice(target, func(d *v1alpha1.GPUDevice) string {
			if d.Generation != 2 || d.Status.ObservedGeneration != 2 {
				return fmt.Sprintf("generation/observedGeneration = %d/%d, want 2/2", d.Generation, d.Status.ObservedGeneration)
			}
			return ""
		})
		if gkaSSec(got.Status.IntentObservedAt) != gkaSDay+"02:00:01Z" || got.Status.ObservedRequestID != "enroll-1" {
			t.Errorf("GKA-071: after a generation change intentObservedAt/requestID = %s/%s, want 02:00:01/enroll-1", gkaSSec(got.Status.IntentObservedAt), got.Status.ObservedRequestID)
		}
		// Only request.id changes (desiredState kept, CEL D4 allows it): renewed, id follows.
		clk.Set(gkaST0.Add(3*time.Hour + 500*time.Millisecond))
		w.updateDevice(target, func(d *v1alpha1.GPUDevice) { d.Spec.Request.ID = "enroll-2" })
		got = w.waitDevice(target, func(d *v1alpha1.GPUDevice) string {
			if d.Status.ObservedRequestID != "enroll-2" {
				return "observedRequestID = " + d.Status.ObservedRequestID
			}
			return ""
		})
		if gkaSSec(got.Status.IntentObservedAt) != gkaSDay+"03:00:01Z" || got.Status.ObservedGeneration != 3 {
			t.Errorf("GKA-071: after a request.id change intentObservedAt/observedGeneration = %s/%d, want 03:00:01/3", gkaSSec(got.Status.IntentObservedAt), got.Status.ObservedGeneration)
		}
		// desiredState + new request id: the phase baseline follows (MaintenancePending).
		clk.Set(gkaST0.Add(4*time.Hour + 100*time.Millisecond))
		w.updateDevice(target, func(d *v1alpha1.GPUDevice) {
			d.Spec.DesiredState = v1alpha1.DesiredState("Maintenance")
			d.Spec.Request.ID = "maint-1"
		})
		got = w.waitDevice(target, func(d *v1alpha1.GPUDevice) string {
			if d.Status.ObservedRequestID != "maint-1" || d.Status.LifecyclePhase != "MaintenancePending" {
				return fmt.Sprintf("requestID/phase = %s/%s", d.Status.ObservedRequestID, d.Status.LifecyclePhase)
			}
			return ""
		})
		if gkaSSec(got.Status.IntentObservedAt) != gkaSDay+"04:00:01Z" {
			t.Errorf("GKA-071: after a desiredState change intentObservedAt = %s, want 04:00:01", gkaSSec(got.Status.IntentObservedAt))
		}
		saved[target] = before{rv: got.ResourceVersion, intent: gkaSSec(got.Status.IntentObservedAt)}
		for _, d := range devices[1:] {
			saved[d.Name] = before{rv: w.deviceNow(d.Name).ResourceVersion, intent: saved[d.Name].intent}
		}
	})

	t.Run("restarted-controller", func(t *testing.T) {
		w := w.with(t)
		clk2 := newGkaFakeClock(gkaST0.Add(24 * time.Hour))
		a2 := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
		w.start(a2, clk2)
		gkaEventually(t, gkaSWaitMax, func() (bool, string) {
			return len(a2.Requests()) >= 3, "waiting for passes of the restarted controller"
		})
		gkaSSettle(3)
		for _, d := range devices {
			got := w.deviceNow(d.Name)
			if gkaSSec(got.Status.IntentObservedAt) != saved[d.Name].intent {
				t.Errorf("GKA-071: %s intentObservedAt = %s after restart, want the stored %s", d.Name, gkaSSec(got.Status.IntentObservedAt), saved[d.Name].intent)
			}
			if got.ResourceVersion != saved[d.Name].rv {
				t.Errorf("GKA-071/127: %s rewritten by the restarted controller (resourceVersion %s -> %s)", d.Name, saved[d.Name].rv, got.ResourceVersion)
			}
			// The restarted assessor is given the stored value, not the new clock.
			for _, r := range gkaSRequestsFor(a2, n.Name) {
				for _, in := range r.Intents {
					if in.Device.Name == d.Name && gkaSSec(metav1.NewTime(in.ObservedAt)) != saved[d.Name].intent {
						t.Errorf("GKA-071: restarted Intent.ObservedAt for %s = %s, want %s", d.Name, in.ObservedAt.UTC(), saved[d.Name].intent)
					}
				}
			}
		}
	})
}
