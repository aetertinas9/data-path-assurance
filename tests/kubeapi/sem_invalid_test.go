package kubeapi_test

// GKA-084 (InternalError rendering, error class, cause never recorded), GKA-085
// (assessor output validation (a)-(h)) and GKA-087 (minimal InternalError status).
// GKA-034 (invalid Policy/Intent) cannot be provoked through the API: every value
// the CRD admits builds a valid Policy and Intent, so that clause is not tested.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

func gkaSRep(c string, n int) string { return strings.Repeat(c, n) }

// gkaSAllocBase is a Ready decision that also carries an InUse allocation.
func gkaSAllocBase(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
	da := gkaSReadyAssessment(req, in)
	gkaSSetAllocation(&da, gkaSIDs("al", 2), gkaSWorkloads(1))
	return da
}

func gkaSDisqBase(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
	return gkaSDisqualifiedAssessment(req, in)
}

func gkaSUnknownBase(reason string) func(app.NodeAssessmentRequest, fleet.Intent) app.DeviceAssessment {
	return func(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
		return gkaSUnknownAssessment(req, in, reason)
	}
}

// gkaSRebind re-derives BindingKey after a claim/BDF field of the binding changed,
// so only the intended bound is violated.
func gkaSRebind(da *app.DeviceAssessment) {
	b := da.Decision.Binding
	da.Decision.BindingKey = strings.Join([]string{b.Claim.Vendor, b.Claim.UUID, b.Node.UID, b.BootID, b.BDF}, "\x00")
}

// gkaSBoundsMax is a valid decision whose every bounded string sits exactly on its
// GKA-042 maximum (and one summary on its minimum, empty).
func gkaSBoundsMax(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
	da := gkaSReadyAssessment(req, in)
	fid := gkaSRep("F", 128)
	da.Decision.FindingIDs = []string{fid}
	da.Findings = []app.FindingRecord{{ID: fid, Type: gkaSRep("T", 256), Severity: gkaSRep("s", 64), State: gkaSRep("a", 64)}}
	e1 := gkaSRep("E", 128)
	da.Decision.EvidenceIDs = []string{e1, "e-empty-summary"}
	da.Evidence = []app.EvidenceRecord{
		{ID: e1, Summary: gkaSRep("s", 512), ObservedAt: gkaSEvidenceAt, ExpiresAt: gkaSEvidenceAt.Add(time.Minute)},
		{ID: "e-empty-summary", Summary: "", ObservedAt: gkaSEvidenceAt, ExpiresAt: gkaSEvidenceAt.Add(time.Minute)},
	}
	gkaSSetAllocation(&da, []string{gkaSRep("R", 128)}, []app.WorkloadRecord{{
		Namespace: gkaSRep("n", 253), Name: gkaSRep("p", 253), UID: gkaSRep("u", 128), Container: gkaSRep("c", 253), CreatedAt: gkaSEvidenceAt,
	}})
	da.Allocation.Profile = gkaSRep("P", 128)
	da.Decision.Coverage[0].EvidenceIDs = []string{gkaSRep("C", 128)}
	da.Decision.Binding.Source.Name = gkaSRep("N", 256)
	da.Decision.Binding.EvidenceID = gkaSRep("B", 128)
	da.Decision.Binding.Claim.Vendor = gkaSRep("V", 128)
	gkaSRebind(&da)
	return da
}

type gkaSInvalidCase struct {
	name     string
	n        int // devices on the node (default 1)
	base     func(app.NodeAssessmentRequest, fleet.Intent) app.DeviceAssessment
	dev      func(req app.NodeAssessmentRequest, i int, in fleet.Intent, da *app.DeviceAssessment)
	node     func(req app.NodeAssessmentRequest, na *app.NodeAssessment)
	positive bool // must render normally
}

func (c gkaSInvalidCase) plan(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	base := c.base
	if base == nil {
		base = gkaSReadyAssessment
	}
	idx := map[string]int{}
	for i, in := range req.Intents {
		idx[in.Device.UID] = i
	}
	na := gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
		da := base(req, in)
		if c.dev != nil {
			c.dev(req, idx[in.Device.UID], in, &da)
		}
		return &da
	})
	if c.node != nil {
		c.node(req, &na)
	}
	return na, nil
}

func gkaSInvalidCases() []gkaSInvalidCase {
	at := gkaSEvidenceAt
	dev := func(f func(da *app.DeviceAssessment)) func(app.NodeAssessmentRequest, int, fleet.Intent, *app.DeviceAssessment) {
		return func(_ app.NodeAssessmentRequest, _ int, _ fleet.Intent, da *app.DeviceAssessment) { f(da) }
	}
	node := func(f func(na *app.NodeAssessment)) func(app.NodeAssessmentRequest, *app.NodeAssessment) {
		return func(_ app.NodeAssessmentRequest, na *app.NodeAssessment) { f(na) }
	}
	twoDevices := func(f func(na *app.NodeAssessment)) func(app.NodeAssessmentRequest, *app.NodeAssessment) {
		return node(func(na *app.NodeAssessment) {
			if len(na.Devices) == 2 {
				f(na)
			}
		})
	}
	return []gkaSInvalidCase{
		// (a) decision validity and correlation with the request.
		{name: "a1validate", dev: func(_ app.NodeAssessmentRequest, _ int, in fleet.Intent, da *app.DeviceAssessment) {
			*da = app.DeviceAssessment{Decision: fleet.DeviceDecision{DeviceUID: in.Device.UID}}
		}},
		{name: "a2uid", dev: dev(func(da *app.DeviceAssessment) { da.Decision.DeviceUID = "no-such-device" })},
		{name: "a3dup", n: 2, node: twoDevices(func(na *app.NodeAssessment) { na.Devices[1] = na.Devices[0] })},
		{name: "a4nodeuid", dev: dev(func(da *app.DeviceAssessment) { da.Decision.NodeUID = "other-node-uid" })},
		{name: "a5policy", dev: dev(func(da *app.DeviceAssessment) { da.Decision.PolicyRevision = "other-policy-revision" })},
		{name: "a6request", dev: dev(func(da *app.DeviceAssessment) { da.Decision.RequestID = "other-request" })},
		{name: "a7generation", dev: dev(func(da *app.DeviceAssessment) { da.Decision.MetadataGeneration++ })},
		{name: "a8desired", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Desired = fleet.DesiredMaintenance })},
		// (b) one Finding/Evidence record per referenced id.
		{name: "b1nofinding", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) { da.Findings = nil })},
		{name: "b2noevidence", dev: dev(func(da *app.DeviceAssessment) { da.Evidence = nil })},
		{name: "b3dupfinding", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) { da.Findings = append(da.Findings, da.Findings[0]) })},
		{name: "b4dupevidence", dev: dev(func(da *app.DeviceAssessment) { da.Evidence = append(da.Evidence, da.Evidence[0]) })},
		// (c) allocation record for Empty/InUse.
		{name: "c1norecord", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation = nil })},
		{name: "c2profile", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Profile = "" })},
		{name: "c3observed", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.ObservedAt = time.Time{} })},
		{name: "c4expires", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.ExpiresAt = time.Time{} })},
		{name: "c5norefs", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.EvidenceRefs = nil })},
		// (d) node decision validity.
		{name: "d1revision", node: node(func(na *app.NodeAssessment) { na.Node.AssessmentRevision = "not-a-hex-digest" })},
		{name: "d2nodeuid", node: node(func(na *app.NodeAssessment) { na.Node.NodeUID = "other-node-uid" })},
		{name: "d3fleetuid", node: node(func(na *app.NodeAssessment) { na.Node.FleetUID = "other-fleet-uid" })},
		// (e) observation <-> devices.
		{name: "e1noobs", node: node(func(na *app.NodeAssessment) { na.Observation = nil })},
		{name: "e2obsonly", node: node(func(na *app.NodeAssessment) { *na = app.NodeAssessment{Observation: na.Observation} })},
		{name: "e3nodeonly", node: node(func(na *app.NodeAssessment) { *na = app.NodeAssessment{Node: na.Node} })},
		{name: "e4emptyrev", node: node(func(na *app.NodeAssessment) { na.Observation.GraphRevision = "" })},
		{name: "e5unknowncomplete", node: node(func(na *app.NodeAssessment) { na.Observation.Completeness = fleet.CompletenessUnknown })},
		{name: "e6otherrev", node: node(func(na *app.NodeAssessment) { na.Observation.GraphRevision = gkaSGraphRev("some-other-node") })},
		// (f) the rendered status must satisfy the CRD bounds and conditional requirements.
		{name: "f1normalnoobserved", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Coverage[0].ObservedAt = time.Time{} })},
		{name: "f2normalnoevidence", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Coverage[0].EvidenceIDs = nil })},
		{name: "f3normalnolatest", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Coverage[0].LatestObservedAt = time.Time{} })},
		{name: "f4normalnoexpiry", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Coverage[0].ExpiresAt = time.Time{} })},
		{name: "f5reasonnotv22", base: gkaSUnknownBase("NotAV22Reason")},
		{name: "f6reasonempty", base: gkaSUnknownBase("")},
		{name: "f7reasonnonascii", base: gkaSUnknownBase("Valíd")},
		{name: "f8findingtype", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) { da.Findings[0].Type = gkaSRep("T", 257) })},
		{name: "f9findingseverity", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) { da.Findings[0].Severity = gkaSRep("s", 65) })},
		{name: "f10findingstate", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) { da.Findings[0].State = gkaSRep("a", 65) })},
		{name: "f11findingid", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) {
			id := gkaSRep("F", 129)
			da.Decision.FindingIDs = []string{id}
			da.Findings[0].ID = id
		})},
		{name: "f12evidenceid", dev: dev(func(da *app.DeviceAssessment) {
			id := gkaSRep("E", 129)
			da.Decision.EvidenceIDs = []string{id}
			da.Evidence = []app.EvidenceRecord{{ID: id, Summary: "s", ObservedAt: at, ExpiresAt: at.Add(time.Minute)}}
		})},
		{name: "f13evidencesummary", dev: dev(func(da *app.DeviceAssessment) { da.Evidence[0].Summary = gkaSRep("s", 513) })},
		{name: "f14profile", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Profile = gkaSRep("P", 129) })},
		{name: "f15workloadname", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Workloads[0].Name = gkaSRep("p", 254) })},
		{name: "f16workloadns", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Workloads[0].Namespace = gkaSRep("n", 254) })},
		{name: "f17workloadcontainer", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Workloads[0].Container = gkaSRep("c", 254) })},
		{name: "f18workloaduid", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Workloads[0].UID = gkaSRep("u", 129) })},
		{name: "f19allocref", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.EvidenceRefs = []string{gkaSRep("R", 129)} })},
		{name: "f20coverageref", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Coverage[0].EvidenceIDs = []string{gkaSRep("C", 129)} })},
		{name: "f21vendor", dev: dev(func(da *app.DeviceAssessment) {
			da.Decision.Binding.Claim.Vendor = gkaSRep("V", 129)
			gkaSRebind(da)
		})},
		{name: "f22bindingevidence", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Binding.EvidenceID = gkaSRep("B", 129) })},
		{name: "f23sourcename", dev: dev(func(da *app.DeviceAssessment) { da.Decision.Binding.Source.Name = gkaSRep("N", 257) })},
		// (g) node decision <-> selection/devices.
		{name: "g1completeshort", n: 2, node: twoDevices(func(na *app.NodeAssessment) {
			na.Devices = na.Devices[:1]
			na.Node.DeviceCount = 1 // Selection stays Complete although only 1 of 2 intents is decided
		})},
		{name: "g2partialfull", node: node(func(na *app.NodeAssessment) {
			na.Node.Selection = fleet.SelectionPartial
			na.Node.Eligibility, na.Node.Qualification, na.Node.ValidUntil = fleet.EligibilityUnknown, fleet.QualificationUnknown, time.Time{}
		})},
		{name: "g3devicecount", node: node(func(na *app.NodeAssessment) { na.Node.DeviceCount = 2 })},
		{name: "g4partialineligible", n: 2, node: twoDevices(func(na *app.NodeAssessment) {
			na.Devices = na.Devices[:1]
			na.Node.Selection, na.Node.DeviceCount = fleet.SelectionPartial, 1
			na.Node.Eligibility, na.Node.Qualification, na.Node.ValidUntil = fleet.EligibilityIneligible, fleet.QualificationUnknown, time.Time{}
		})},
		{name: "g5partialqualified", n: 2, node: twoDevices(func(na *app.NodeAssessment) {
			na.Devices = na.Devices[:1]
			na.Node.Selection, na.Node.DeviceCount = fleet.SelectionPartial, 1
			na.Node.Eligibility, na.Node.Qualification, na.Node.ValidUntil = fleet.EligibilityUnknown, fleet.QualificationQualified, gkaSEvidenceAt.Add(time.Minute)
		})},
		{name: "g6eligibledisq", node: node(func(na *app.NodeAssessment) {
			na.Node.Eligibility, na.Node.Qualification, na.Node.ValidUntil = fleet.EligibilityEligible, fleet.QualificationDisqualified, time.Time{}
		})},
		// (h) record times and strings.
		{name: "h1evidenceobserved", dev: dev(func(da *app.DeviceAssessment) { da.Evidence[0].ObservedAt = time.Time{} })},
		{name: "h2evidenceexpires", dev: dev(func(da *app.DeviceAssessment) { da.Evidence[0].ExpiresAt = time.Time{} })},
		{name: "h3workloadcreated", base: gkaSAllocBase, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.Workloads[0].CreatedAt = time.Time{} })},
		{name: "h4findingtype", base: gkaSDisqBase, dev: dev(func(da *app.DeviceAssessment) { da.Findings[0].Type = "" })},
		// Positive controls: these must render normally.
		{name: "p1boundsmax", base: gkaSBoundsMax, positive: true},
		{name: "p2extrarecords", positive: true, dev: dev(func(da *app.DeviceAssessment) {
			da.Findings = append(da.Findings, app.FindingRecord{ID: "unreferenced-finding", Type: "X", Severity: "low", State: "closed"})
			da.Evidence = append(da.Evidence, app.EvidenceRecord{ID: "unreferenced-evidence", Summary: "x", ObservedAt: at, ExpiresAt: at.Add(time.Minute)})
		})},
		{name: "p3allocrefs9", base: gkaSAllocBase, positive: true, dev: dev(func(da *app.DeviceAssessment) { da.Allocation.EvidenceRefs = gkaSIDs("al", 9) })},
		{name: "p4partialsubset", n: 2, positive: true, node: twoDevices(func(na *app.NodeAssessment) {
			// A subset of decisions with no node decision is a valid answer (GKA-031).
			na.Devices = na.Devices[:1]
			na.Node = nil
		})},
		{name: "p5farzonetime", positive: true, dev: dev(func(da *app.DeviceAssessment) {
			// The last representable instant viewed at UTC+14 has local year 10000;
			// the record time is still valid, so the location must not matter.
			da.Evidence[0].ExpiresAt = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC).In(time.FixedZone("UTC+14", 14*3600))
		})},
	}
}

// GKA-085 / GKA-084: every violated rule (a)-(h) turns the whole node into an
// InternalError with class "invalid assessment" (devices, NodePathState, Node
// condition), never keeps the last Ready, and never blocks the other nodes; the
// positive controls (limits exactly met, extra records, 9 allocation refs, a subset
// of decisions) render normally.
func TestGKA084_085_InvalidAssessmentsRenderInternalError(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	script := newGkaSScript(gkaSPlanReady)
	cases := gkaSInvalidCases()

	type made struct {
		c    gkaSInvalidCase
		n    *corev1.Node
		devs []*v1alpha1.GPUDevice
	}
	var all []made
	controlNode := w.selNode("ctl")
	controlDev := w.device("dctl", controlNode, f)
	for _, c := range cases {
		n := w.selNode("n" + c.name)
		count := c.n
		if count == 0 {
			count = 1
		}
		m := made{c: c, n: n}
		for i := 0; i < count; i++ {
			m.devs = append(m.devs, w.device(fmt.Sprintf("d%s%d", c.name, i), n, f))
		}
		script.SetNode(n.Name, c.plan)
		all = append(all, m)
	}
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	const class = "internal error: invalid assessment"
	invalid, positives := 0, 0
	for _, m := range all {
		if m.c.positive {
			positives++
			continue
		}
		invalid++
	}
	for _, m := range all {
		m := m
		if m.c.positive {
			wantReason := "Ready"
			if m.c.name == "p4partialsubset" {
				wantReason = "Validating" // a subset of decisions without a node decision
			}
			w.waitNPS(m.n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, wantReason) })
			continue
		}
		w.waitNPS(m.n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	}
	w.waitNPS(controlNode.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	gkaSSettle(4)

	for _, m := range all {
		where := "GKA-085 " + m.c.name
		if m.c.positive {
			for _, d := range m.devs {
				got := w.deviceNow(d.Name)
				c := gkaSCondOf(got.Status.Conditions, "DeviceQualified")
				if c == nil || c.Reason == "InternalError" {
					t.Errorf("%s: a valid assessment was rejected: DeviceQualified = %v", where, c)
				}
			}
			continue
		}
		for _, d := range m.devs {
			w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "InternalError") })
			gkaSCheckNonDecision(t, where+" "+d.Name, w.deviceNow(d.Name), gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{class}})
		}
		nps := w.npsNow(m.n.Name)
		gkaSCheckNPS(t, where, nps, gkaSNPSWant{Elig: "Unknown", Reason: "InternalError", NodeTail: []string{class}, FreshTail: []string{class}})
		for _, ds := range nps.Status.DeviceSummaries {
			if ds.Qualification != "Unknown" || ds.LifecyclePhase != "Unknown" {
				t.Errorf("%s: deviceSummaries carry %s/%s for a rejected assessment, want Unknown/Unknown", where, ds.Qualification, ds.LifecyclePhase)
			}
		}
		w.waitNode(m.n.Name, func(n *corev1.Node) string {
			return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "InternalError", gkaSMsg("fleet="+f.Name, class)})
		})
	}
	// The control node is untouched by its neighbours' failures (GKA-070(h), GKA-084).
	if got := w.deviceNow(controlDev.Name); got.Status.LifecyclePhase != "Ready" || got.Status.Qualification != "Qualified" {
		t.Errorf("GKA-084: the control device is %s/%s, want Ready/Qualified", got.Status.LifecyclePhase, got.Status.Qualification)
	}
	// Fleet: InternalError nodes present -> row 6 (no error token in the message).
	ready := 1 + positives - 1 // control + positives, minus p4 (Node == nil: Unknown)
	unknown := invalid + 1     // the invalid nodes and p4
	total := invalid + positives + 1
	gkaSCheckFleet(t, "GKA-078 row 6 with many rejected nodes", w.fleetNow(f.Name), gkaSFleetWant{
		Selected: int32(total), Ready: int32(ready), Unknown: int32(unknown), Status: "Unknown", Reason: "InternalError",
		Message: fmt.Sprintf("selected=%d ready=%d degraded=0 unknown=%d", total, ready, unknown),
	})
}

// gkaSMarker is carried by every scripted error text; it must never reach a status message or a log record.
const gkaSMarker = "GKA-LEAK-MARKER-31337"

// GKA-084 (+GKA-070(f)): assessor errors of any kind (errors.Is does not matter) are
// rendered as class "assessor failed" without the cause text; the last Ready is not
// kept and comes back when the assessor recovers; slog carries no error text.
func TestGKA084_AssessorErrorsRenderInternalErrorWithoutCause(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	script := newGkaSScript(gkaSPlanReady)
	kinds := map[string]error{
		"plain":    errors.New("boom " + gkaSMarker),
		"wrapped":  fmt.Errorf("wrapped %s: %w", gkaSMarker, fleet.ErrInvalidInput),
		"noobs":    app.ErrNoObservation, // a real assessor never returns it to the controller: still an error here
		"deadline": context.DeadlineExceeded,
	}
	type made struct {
		n *corev1.Node
		d *v1alpha1.GPUDevice
	}
	errNodes := map[string]made{}
	for k, e := range kinds {
		n := w.selNode("n" + k)
		d := w.device("d"+k, n, f)
		errNodes[k] = made{n, d}
		script.SetNode(n.Name, gkaSPlanErr(e))
	}
	nFlip := w.selNode("nflip")
	dFlip := w.device("dflip", nFlip, f)
	nOK := w.selNode("nok")
	dOK := w.device("dok", nOK, f)
	logs := newGkaSLog()
	clk := newGkaFakeClock(gkaST0)
	w.start(gkaSNewAssessor(script), clk, func(o *controller.Options) { o.Logger = logs.Logger() })

	const class = "internal error: assessor failed"
	for k, m := range errNodes {
		w.waitDevice(m.d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "InternalError") })
		gkaSCheckNonDecision(t, "GKA-084 "+k, w.deviceNow(m.d.Name), gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{class}})
		w.waitNPS(m.n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
		gkaSCheckNPS(t, "GKA-084 "+k, w.npsNow(m.n.Name), gkaSNPSWant{Elig: "Unknown", Reason: "InternalError", NodeTail: []string{class}, FreshTail: []string{class}})
		w.waitNode(m.n.Name, func(n *corev1.Node) string {
			return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "InternalError", gkaSMsg("fleet="+f.Name, class)})
		})
	}
	// Healthy neighbours are rendered normally in the same passes.
	w.waitDevice(dOK.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Ready") })
	w.waitDevice(dFlip.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Ready") })
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
	gkaSCheckFleet(t, "GKA-078 row 6", w.fleetNow(f.Name), gkaSFleetWant{
		Selected: 6, Ready: 2, Unknown: 4, Status: "Unknown", Reason: "InternalError", Message: "selected=6 ready=2 degraded=0 unknown=4",
	})

	// The last Ready is not kept: the flip node turns InternalError at its next pass, then recovers.
	clk.Set(gkaST0.Add(10 * time.Second))
	script.SetNode(nFlip.Name, gkaSPlanErr(errors.New("flip failure "+gkaSMarker)))
	w.waitDevice(dFlip.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "InternalError") })
	dv := w.deviceNow(dFlip.Name)
	gkaSCheckNonDecision(t, "GKA-084 flip device", dv, gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{class}})
	gkaSLTT(t, "GKA-074(c)", dv.Status.Conditions, "DeviceQualified", gkaSDay+"00:00:10Z")
	w.waitNode(nFlip.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "InternalError", gkaSMsg("fleet="+f.Name, class)})
	})
	clk.Set(gkaST0.Add(20 * time.Second))
	script.SetNode(nFlip.Name, gkaSPlanReady)
	w.waitDevice(dFlip.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Ready") })
	w.waitNode(nFlip.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + f.Name})
	})
	gkaSLTT(t, "GKA-074(c)", w.deviceNow(dFlip.Name).Status.Conditions, "DeviceQualified", gkaSDay+"00:00:20Z")
	gkaSSettle(3)

	// Cause text never appears in logs or in any published message (GKA-084, GKA-074(e)).
	lines := logs.Lines()
	if len(lines) == 0 {
		t.Errorf("GKA-084: the controller logged nothing about the failing nodes")
	}
	for _, l := range lines {
		if strings.Contains(l, gkaSMarker) || strings.Contains(l, "boom") || strings.Contains(l, "flip failure") {
			t.Errorf("GKA-084: a log record carries the error text: %s", l)
		}
	}
	for _, m := range errNodes {
		for _, msg := range []string{gkaSCondOf(w.deviceNow(m.d.Name).Status.Conditions, "DeviceQualified").Message, gkaSNodeCond(w.nodeNow(m.n.Name)).Message} {
			if strings.Contains(msg, gkaSMarker) {
				t.Errorf("GKA-074(e): a published message carries the error text: %q", msg)
			}
		}
	}
}

// GKA-087 (+GKA-084/125 rendering): when the API server permanently rejects a status
// write (422), the same object gets the minimal InternalError status with class
// "status write rejected", and neither the response body nor the object's error text
// reaches the log.
func TestGKA087_StatusWriteRejectedFallbackRendering(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	d := w.device("d", n, f)
	cfg, rec := gkaRecordingConfig(w.e.Config)
	logs := newGkaSLog()
	rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut || !strings.HasSuffix(req.URL.Path, "/status") {
			return nil
		}
		p := req.URL.Path
		if !strings.Contains(p, "/gpudevices/"+d.Name+"/") && !strings.Contains(p, "/nodepathstates/"+n.Name+"/") && !strings.Contains(p, "/gpufleets/"+f.Name+"/") {
			return nil
		}
		if strings.Contains(gkaSBody(req), "status write rejected") {
			return nil // the fallback write is allowed through
		}
		return gkaSStatusResponse(req, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, "the object is invalid: "+gkaSMarker)
	})
	gkaSStart(t, cfg, gkaSNewAssessor(newGkaSScript(gkaSPlanNone)), newGkaFakeClock(gkaST0), func(o *controller.Options) { o.Logger = logs.Logger() })

	const class = "internal error: status write rejected"
	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "InternalError") })
	w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
	gkaSSettle(6) // several ticks: the rejected normal writes are retried on each tick, the fallback is not repeated

	gkaSCheckNonDecision(t, "GKA-087 device", w.deviceNow(d.Name), gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{class}})
	gkaSCheckNPS(t, "GKA-087 nodepathstate", w.npsNow(n.Name), gkaSNPSWant{Elig: "Unknown", Reason: "InternalError", NodeTail: []string{class}, FreshTail: []string{class}})
	gkaSCheckFleet(t, "GKA-087 fleet", w.fleetNow(f.Name), gkaSFleetWant{
		Status: "Unknown", Reason: "InternalError", Message: "selected=0 ready=0 degraded=0 unknown=0; " + class,
	})
	for _, l := range logs.Lines() {
		if strings.Contains(l, gkaSMarker) || strings.Contains(l, "the object is invalid") {
			t.Errorf("GKA-084/163: a log record carries the API response body: %s", l)
		}
	}
	if len(logs.Lines()) == 0 {
		t.Errorf("GKA-125: the permanent write failure was not logged")
	}
	if len(rec.Writes("/apis/"+gkaSGroup+"/")) == 0 {
		t.Errorf("GKA-120: the recorder saw no status write")
	}
}
