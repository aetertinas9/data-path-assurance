package kubeapi_test

// Lane T-2 (selection / projection / NodePathState lifetime / Node condition
// values) shared builders. Everything here is prefixed gkaS. The shared harness
// (gkaEnv, gkaStartController, fake assessor/clock, recorder) belongs to lane T-3
// and is only used, never defined, in the sem_* files.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const (
	gkaSClusterID = "gka-sem-cluster"
	gkaSResync    = 200 * time.Millisecond
	gkaSWaitMax   = 20 * time.Second
)

var (
	// gkaST0 is the fake clock origin (an exact UTC second).
	gkaST0 = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	// gkaSEvidenceAt is a fixed evidence time that does not depend on the fake
	// clock, so decisions stay byte-identical across passes.
	gkaSEvidenceAt = time.Date(2026, 9, 30, 0, 0, 30, 0, time.UTC)
)

func gkaSPtr[T any](v T) *T { return &v }

func gkaSHex(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// gkaSGraphRev has the GFL-083 shape <session>:<sequence>:<64 hex>.
func gkaSGraphRev(nodeUID string) string { return "7:2:" + gkaSHex("graph|"+nodeUID) }

// gkaSNodeRevision is the lowercase-hex-64 NodeDecision.AssessmentRevision the
// fake returns for a (node, fleet) pair.
func gkaSNodeRevision(nodeUID, fleetUID string) string {
	return gkaSHex("node-revision|" + nodeUID + "|" + fleetUID)
}

func gkaSBaselinePhase(d fleet.DesiredState) fleet.LifecyclePhase {
	switch d {
	case fleet.DesiredMaintenance:
		return fleet.PhaseMaintenancePending
	case fleet.DesiredRetired:
		return fleet.PhaseRetiring
	default:
		return fleet.PhasePending
	}
}

func gkaSBDF(deviceUID string) string { return "0000:" + gkaSHex("bdf|" + deviceUID)[:2] + ":00.0" }

func gkaSFunction(bdf string) model.AssetRef {
	id, err := model.NewTypedID(string(model.NamespacePCIBDF), bdf)
	if err != nil {
		panic(err)
	}
	ref, err := model.NewAssetRef(model.KindPCIeFunction, id)
	if err != nil {
		panic(err)
	}
	return ref
}

// gkaSBaseDecision is the shared skeleton of builders 1-3 below. It satisfies the
// spec section 17 item 2 Validate requirements for a non-qualified, non-bound
// decision and copies every correlation field GKA-085(a) checks from req/intent.
func gkaSBaseDecision(req app.NodeAssessmentRequest, in fleet.Intent) fleet.DeviceDecision {
	return fleet.DeviceDecision{
		Desired: in.Desired, Phase: gkaSBaselinePhase(in.Desired), Qualification: fleet.QualificationUnknown,
		BindingState: fleet.BindingUnknown, Allocation: fleet.AllocationUnknown,
		GraphRevision: gkaSGraphRev(req.Node.UID), EvaluatedAt: req.Now, Reason: "Validating",
		PolicyRevision: req.Policy.Revision, RequestID: in.RequestID, DeviceUID: in.Device.UID, NodeUID: req.Node.UID,
		BootID: "boot-1", TopologyDigest: gkaSHex("topology"), BaselineDigest: gkaSHex("baseline"),
		MetadataGeneration: in.MetadataGeneration, Session: 2, IntentObservedAt: in.ObservedAt,
	}
}

// gkaSUnknownAssessment (builder 1/4) is a valid "not enough evidence" decision:
// qualification/binding/allocation Unknown, baseline phase, no records.
func gkaSUnknownAssessment(req app.NodeAssessmentRequest, in fleet.Intent, reason string) app.DeviceAssessment {
	dec := gkaSBaseDecision(req, in)
	dec.Reason = reason
	return app.DeviceAssessment{Decision: dec}
}

// gkaSReadyAssessment (builder 2/4) is a valid Bound + Qualified + Ready decision
// with one Normal coverage row per policy entry, plus the matching Evidence
// records. The claim must carry a UUID (default gkaS devices do).
func gkaSReadyAssessment(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
	at := gkaSEvidenceAt
	exp := at.Add(5 * time.Minute)
	bdf := gkaSBDF(in.Device.UID)
	dec := gkaSBaseDecision(req, in)
	dec.Phase = fleet.PhaseReady
	dec.Qualification = fleet.QualificationQualified
	dec.BindingState = fleet.BindingBound
	dec.Binding = fleet.ObservedBinding{
		Node: req.Node, BootID: "boot-1", BDF: bdf, Function: gkaSFunction(bdf),
		Claim:  fleet.InventoryClaim{Vendor: in.Claim.Vendor, UUID: in.Claim.UUID, Serial: in.Claim.Serial, Source: in.Claim.Source, EvidenceID: in.Claim.EvidenceID},
		Source: model.SourceRef{Type: string(model.SourceTypeAgent), Name: "path-agent/nvidia-smi"}, EvidenceID: "nb:7:2:c0ffee",
		BundleRevision: "bundle-1", CollectorProfileID: "profile-1", ObservedAt: at, ExpiresAt: exp,
	}
	dec.BindingKey = strings.Join([]string{in.Claim.Vendor, in.Claim.UUID, req.Node.UID, "boot-1", bdf}, "\x00")
	dec.AcceptedNormalPoint = true
	dec.ValidUntil = exp
	dec.Reason = "Ready"
	dec.ReadyWindowStartedAt = at.Add(-time.Minute)
	dec.LastCompositeMin, dec.LastCompositeMax = at, at

	var evidence []app.EvidenceRecord
	for _, rc := range req.Policy.RequiredCoverage {
		eid := "pe:7:2:" + rc.Name
		dec.Coverage = append(dec.Coverage, fleet.CoverageAssessment{
			Name: rc.Name, PathKind: rc.PathKind, State: fleet.CoverageNormal, Reason: "Normal", EvidenceIDs: []string{eid},
			ObservedAt: at, LatestObservedAt: at, ExpiresAt: exp, AssessmentSequence: 4,
		})
		dec.CoverageCursors = append(dec.CoverageCursors, fleet.CoverageCursor{
			Name: rc.Name, ObservedAt: at, AssessmentSequence: 4, EvidenceDigest: gkaSHex("cursor|" + rc.Name),
		})
		dec.EvidenceIDs = append(dec.EvidenceIDs, eid)
		evidence = append(evidence, app.EvidenceRecord{ID: eid, Summary: "evidence " + eid, ObservedAt: at, ExpiresAt: exp})
	}
	return app.DeviceAssessment{Decision: dec, Evidence: evidence}
}

// gkaSDisqualifiedAssessment (builder 3/4) is a valid Disqualified + Degraded
// decision that keeps the binding and carries one active finding.
func gkaSDisqualifiedAssessment(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
	da := gkaSReadyAssessment(req, in)
	da.Decision.Phase = fleet.PhaseDegraded
	da.Decision.Qualification = fleet.QualificationDisqualified
	da.Decision.ValidUntil = time.Time{}
	da.Decision.AcceptedNormalPoint = false
	da.Decision.Reason = "Degraded"
	fid := "finding:" + gkaSHex(in.Device.UID)[:16]
	da.Decision.FindingIDs = []string{fid}
	da.Findings = []app.FindingRecord{{ID: fid, Type: "PCIE_LINK_WIDTH_DEGRADED", Severity: "high", State: "active"}}
	return da
}

// gkaSNodeDecision (builder 4/4) is a valid NodeDecision. Selection, Eligibility
// and Qualification must be consistent with what GKA-085(g) derives from the
// request; callers that want an intentionally inconsistent value edit the result.
func gkaSNodeDecision(req app.NodeAssessmentRequest, sel fleet.SelectionState, elig fleet.Eligibility, qual fleet.Qualification, reason string, count int) *fleet.NodeDecision {
	nd := fleet.NodeDecision{
		NodeUID: req.Node.UID, FleetUID: req.FleetUID, Qualification: qual, Eligibility: elig, Selection: sel,
		AssessmentRevision: gkaSNodeRevision(req.Node.UID, req.FleetUID), EvaluatedAt: req.Now, Reason: reason,
	}
	for i := 0; i < count; i++ {
		nd.DeviceCount++ // whatever integer type the public struct uses
	}
	if qual == fleet.QualificationQualified {
		nd.ValidUntil = gkaSEvidenceAt.Add(5 * time.Minute)
	}
	return &nd
}

// gkaSNodeFor mirrors the S1 aggregation described in spec section 17 item 4:
// non-Complete -> Unknown; any Disqualified -> Ineligible/Disqualified; all
// Qualified and every device InService+Ready -> Eligible/Qualified; all Qualified
// otherwise -> Ineligible/Qualified; anything else -> Unknown.
func gkaSNodeFor(req app.NodeAssessmentRequest, sel fleet.SelectionState, devs []app.DeviceAssessment) *fleet.NodeDecision {
	if sel != fleet.SelectionComplete {
		return gkaSNodeDecision(req, sel, fleet.EligibilityUnknown, fleet.QualificationUnknown, "Validating", len(devs))
	}
	anyDisqualified, allQualified, allReady := false, true, true
	for _, d := range devs {
		if d.Decision.Qualification == fleet.QualificationDisqualified {
			anyDisqualified = true
		}
		if d.Decision.Qualification != fleet.QualificationQualified {
			allQualified = false
		}
		if d.Decision.Desired != fleet.DesiredInService || d.Decision.Phase != fleet.PhaseReady {
			allReady = false
		}
	}
	switch {
	case anyDisqualified:
		return gkaSNodeDecision(req, sel, fleet.EligibilityIneligible, fleet.QualificationDisqualified, "Degraded", len(devs))
	case allQualified && allReady:
		return gkaSNodeDecision(req, sel, fleet.EligibilityEligible, fleet.QualificationQualified, "Ready", len(devs))
	case allQualified:
		return gkaSNodeDecision(req, sel, fleet.EligibilityIneligible, fleet.QualificationQualified, "Degraded", len(devs))
	default:
		return gkaSNodeDecision(req, sel, fleet.EligibilityUnknown, fleet.QualificationUnknown, "Validating", len(devs))
	}
}

// gkaSAssessment assembles the NodeAssessment for req. pick returns the assessment
// of each intent (nil = no decision for that device). No decision at all yields
// the "no observation" zero value (GKA-031).
func gkaSAssessment(req app.NodeAssessmentRequest, pick func(in fleet.Intent) *app.DeviceAssessment) app.NodeAssessment {
	var devs []app.DeviceAssessment
	for _, in := range req.Intents {
		if da := pick(in); da != nil {
			devs = append(devs, *da)
		}
	}
	if len(devs) == 0 {
		return app.NodeAssessment{}
	}
	sel := fleet.SelectionComplete
	if req.Selection != fleet.SelectionComplete || len(devs) < len(req.Intents) {
		sel = fleet.SelectionPartial
	}
	return app.NodeAssessment{
		Devices:     devs,
		Node:        gkaSNodeFor(req, sel, devs),
		Observation: &app.NodeObservation{GraphRevision: gkaSGraphRev(req.Node.UID), Completeness: fleet.CompletenessComplete},
	}
}

// gkaSPlan is one scripted answer of the fake assessor.
type gkaSPlan func(req app.NodeAssessmentRequest) (app.NodeAssessment, error)

func gkaSPlanNone(app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	return app.NodeAssessment{}, nil
}

func gkaSPlanReady(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	return gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
		da := gkaSReadyAssessment(req, in)
		return &da
	}), nil
}

func gkaSPlanDisqualified(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	return gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
		da := gkaSDisqualifiedAssessment(req, in)
		return &da
	}), nil
}

func gkaSPlanUnknown(reason string) gkaSPlan {
	return func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		return gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
			da := gkaSUnknownAssessment(req, in, reason)
			return &da
		}), nil
	}
}

// gkaSEdit wraps a plan and lets the test corrupt or adjust its successful output.
func gkaSEdit(base gkaSPlan, edit func(req app.NodeAssessmentRequest, na *app.NodeAssessment)) gkaSPlan {
	return func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		na, err := base(req)
		if err == nil {
			edit(req, &na)
		}
		return na, err
	}
}

// gkaSPlanErr fails every call with err.
func gkaSPlanErr(err error) gkaSPlan {
	return func(app.NodeAssessmentRequest) (app.NodeAssessment, error) { return app.NodeAssessment{}, err }
}

// gkaSScript routes the single fake-assessor script to a plan per node name and
// lets tests swap plans while the controller runs (mutex protected).
type gkaSScript struct {
	mu     sync.Mutex
	def    gkaSPlan
	byNode map[string]gkaSPlan
}

func newGkaSScript(def gkaSPlan) *gkaSScript {
	return &gkaSScript{def: def, byNode: map[string]gkaSPlan{}}
}

func (s *gkaSScript) SetDefault(p gkaSPlan) {
	s.mu.Lock()
	s.def = p
	s.mu.Unlock()
}

func (s *gkaSScript) SetNode(name string, p gkaSPlan) {
	s.mu.Lock()
	s.byNode[name] = p
	s.mu.Unlock()
}

func (s *gkaSScript) run(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	s.mu.Lock()
	p, ok := s.byNode[req.Node.Name]
	if !ok {
		p = s.def
	}
	s.mu.Unlock()
	return p(req)
}

// gkaSNewAssessor builds the harness fake assessor driven by s.
func gkaSNewAssessor(s *gkaSScript) *gkaFakeAssessor {
	a := newGkaFakeAssessor()
	a.SetScript(s.run)
	return a
}

// gkaSRequestsFor returns the recorded requests of one node, in call order.
func gkaSRequestsFor(a *gkaFakeAssessor, nodeName string) []app.NodeAssessmentRequest {
	var out []app.NodeAssessmentRequest
	for _, r := range a.Requests() {
		if r.Node.Name == nodeName {
			out = append(out, r)
		}
	}
	return out
}

func gkaSIntentNames(req app.NodeAssessmentRequest) []string {
	names := make([]string, 0, len(req.Intents))
	for _, in := range req.Intents {
		names = append(names, in.Device.Name)
	}
	return names
}

// TestGKA031_SemBuildersProduceValidDecisions is an envtest-free sanity check of
// the four builders: a builder bug would otherwise surface everywhere as an
// "invalid assessment" InternalError. It also pins that a decision synthesized
// without evidence is invalid (GKA-031).
func TestGKA031_SemBuildersProduceValidDecisions(t *testing.T) {
	policy := fleet.Policy{
		Revision: "policy-rev-1", Freshness: time.Minute, ReadyFor: 30 * time.Second,
		RequiredCoverage: []fleet.CoverageRequirement{
			{Name: "pcie-parent", PathKind: "gpu-pcie-parent", Required: true},
			{Name: "pcie-root", PathKind: "gpu-pcie-root", Required: true},
			{Name: "pcie-width", PathKind: "gpu-pcie-link-width-normal", Required: true},
		},
	}
	node := fleet.NodeRef{ClusterID: gkaSClusterID, Name: "node-a", UID: "node-uid-a"}
	for _, desired := range []fleet.DesiredState{fleet.DesiredInService, fleet.DesiredMaintenance, fleet.DesiredRetired} {
		in := fleet.Intent{
			Device: fleet.DeviceRef{Name: "gpu-a", UID: "device-uid-a"}, Node: node, Desired: desired, RequestID: "enroll-1",
			MetadataGeneration: 1, ObservedAt: gkaST0.Add(time.Second),
			Claim: fleet.InventoryClaim{Vendor: "NVIDIA", UUID: "GPU-a", Source: "operator/asset-db", EvidenceID: "asset-db:a"},
		}
		req := app.NodeAssessmentRequest{Node: node, FleetUID: "fleet-uid-a", Policy: policy, Selection: fleet.SelectionComplete, Intents: []fleet.Intent{in}, Now: gkaST0}
		for name, da := range map[string]app.DeviceAssessment{
			"unknown":      gkaSUnknownAssessment(req, in, "Validating"),
			"ready":        gkaSReadyAssessment(req, in),
			"disqualified": gkaSDisqualifiedAssessment(req, in),
		} {
			if err := da.Decision.Validate(); err != nil {
				t.Errorf("GKA-031: %s/%s builder decision invalid: %v", desired.String(), name, err)
			}
		}
		na, err := gkaSPlanReady(req)
		if err != nil || na.Node == nil || na.Observation == nil || len(na.Devices) != 1 {
			t.Fatalf("GKA-031: ready plan = %#v / %v", na, err)
		}
		if err := na.Node.Validate(); err != nil {
			t.Errorf("GKA-031: %s node builder invalid: %v", desired.String(), err)
		}
		if got, err := gkaSPlanNone(req); err != nil || got.Node != nil || got.Observation != nil || len(got.Devices) != 0 {
			t.Errorf("GKA-031: none plan = %#v / %v, want the zero (no observation) value", got, err)
		}
	}
	synthesized := fleet.DeviceDecision{DeviceUID: "device-uid-a"}
	if err := synthesized.Validate(); err == nil {
		t.Error("GKA-031: a decision synthesized without evidence validated")
	}
}
