package fleet

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const (
	reasonActiveFinding      = "Degraded"
	reasonBundleMismatch     = "BundleMismatch"
	reasonCoverageMissing    = "CoverageMissing"
	reasonCoverageUnknown    = "Validating"
	reasonFutureObservation  = "FutureObservation"
	reasonIdentityConflict   = "IdentityConflict"
	reasonMaintenancePending = "FenceMissing"
	reasonMaintenanceReady   = "Ready"
	reasonReady              = "Ready"
	reasonReadyWindow        = "Validating"
	reasonRetired            = "Ready"
	reasonRetiring           = "Validating"
	reasonUnadmittedSnapshot = "UnadmittedSnapshot"
	reasonUntrustedSource    = "UntrustedSource"
)

type qualificationEvidence struct {
	coverage                                             []CoverageAssessment
	evidenceIDs                                          []string
	findingIDs                                           []string
	minimum, maximum, validUntil                         time.Time
	allNormal, anyMissing, anyUnknown, activeBad, future bool
}

func EvaluateDevice(bundle AssessmentBundle, previous *DeviceDecision, now time.Time) (DeviceDecision, error) {
	if now.IsZero() {
		return DeviceDecision{}, invalidf("EvaluateDevice now is the zero time")
	}
	if err := bundle.Validate(); err != nil {
		return DeviceDecision{}, err
	}
	if previous != nil {
		if err := previous.Validate(); err != nil {
			return DeviceDecision{}, invalidf("EvaluateDevice previous: %v", err)
		}
	}
	b := cloneBundle(bundle)
	d := baseDecision(b, now)
	if !admissionMatches(b) {
		d.Reason = reasonUnadmittedSnapshot
		return d, nil
	}
	if !bundleMatches(b) {
		d.Reason = reasonBundleMismatch
		return d, nil
	}
	if b.Snapshot.Completeness == CompletenessPartial {
		return d, nil
	}
	if b.Snapshot.ObservedAt.After(now) {
		d.Reason = reasonFutureObservation
		return d, nil
	}

	state, binding, bindingReason, bindingDeadline, bindingFuture := resolveBinding(b, now)
	d.BindingState = state
	if state == BindingBound {
		d.Binding = cloneBinding(binding)
		d.BindingKey = bindingKey(binding)
		d.EvidenceIDs = []string{binding.EvidenceID}
	}
	if bindingFuture {
		d.Reason = reasonFutureObservation
		return d, nil
	}
	if state == BindingConflict {
		d.Reason = reasonIdentityConflict
		return d, nil
	}
	if state != BindingBound {
		d.Reason = bindingReason
		return d, nil
	}

	q := evaluateQualification(b, binding, now)
	d.Coverage = cloneCoverage(q.coverage)
	d.EvidenceIDs = sortedUnique(append(d.EvidenceIDs, q.evidenceIDs...))
	d.FindingIDs = sortedUnique(q.findingIDs)
	d.Allocation = evaluateAllocation(b, binding, now)
	if q.future || optionalEvidenceFuture(b, now) {
		d.Reason = reasonFutureObservation
		return d, nil
	}
	if q.anyUnknown {
		d.Reason = reasonCoverageUnknown
		return finishIntentPhase(d, b, binding, now), nil
	}
	if q.activeBad {
		d.Qualification = QualificationDisqualified
		d.Phase = phaseForBad(b.Intent.Desired)
		d.Reason = reasonActiveFinding
		return finishIntentPhase(d, b, binding, now), nil
	}
	if q.anyMissing {
		d.Qualification = QualificationDisqualified
		d.Phase = phaseForPending(b.Intent.Desired)
		d.Reason = reasonCoverageMissing
		return finishIntentPhase(d, b, binding, now), nil
	}
	if !q.allNormal {
		d.Reason = reasonCoverageUnknown
		return finishIntentPhase(d, b, binding, now), nil
	}

	d.ValidUntil = minTime(bindingDeadline, q.validUntil)
	d = advanceReadyWindow(d, previous, b, q)
	return finishIntentPhase(d, b, binding, now), nil
}

func baseDecision(b AssessmentBundle, now time.Time) DeviceDecision {
	phase := phaseForPending(b.Intent.Desired)
	return DeviceDecision{Desired: b.Intent.Desired, Phase: phase, Qualification: QualificationUnknown, BindingState: BindingUnknown, Allocation: AllocationUnknown, Reason: reasonCoverageUnknown, EvaluatedAt: now, PolicyRevision: b.Policy.Revision, RequestID: b.Intent.RequestID, DeviceUID: b.Intent.Device.UID, NodeUID: b.Intent.Node.UID, BootID: b.Snapshot.BootID, TopologyDigest: b.TopologyDigest, BaselineDigest: b.BaselineDigest, MetadataGeneration: b.Intent.MetadataGeneration, Session: b.Snapshot.Session, IntentObservedAt: b.Intent.ObservedAt, GraphRevision: b.GraphRevision}
}
func phaseForPending(d DesiredState) LifecyclePhase {
	switch d {
	case DesiredMaintenance:
		return PhaseMaintenancePending
	case DesiredRetired:
		return PhaseRetiring
	default:
		return PhasePending
	}
}
func phaseForBad(d DesiredState) LifecyclePhase {
	if d == DesiredInService {
		return PhaseDegraded
	}
	return phaseForPending(d)
}

func admissionMatches(b AssessmentBundle) bool {
	a, s := b.Admitted, b.Snapshot
	return a.Baseline && a.NodeUID == s.NodeUID && a.BootID == s.BootID && a.PayloadDigest == s.PayloadDigest && a.BundleRevision == s.BundleRevision && a.Session == s.Session && a.Sequence == s.Sequence && a.Completeness == s.Completeness && a.ObservedAt.Equal(s.ObservedAt)
}
func bundleMatches(b AssessmentBundle) bool {
	if b.Snapshot.BundleRevision != b.GraphRevision || b.WindowRevision != b.GraphRevision || b.Topology == nil {
		return false
	}
	seq, ok := b.Topology.Sequence()
	if !ok || seq != b.Snapshot.Sequence || !b.Topology.Synced() {
		return false
	}
	expectedPartition := model.PartitionKey(model.KindKubernetesNode.String() + "/" + model.NamespaceKubernetesNodeUID + ":" + b.Snapshot.NodeUID)
	if b.Topology.Partition() != expectedPartition {
		return false
	}
	for _, p := range b.Provenance {
		if p.BundleRevision != b.GraphRevision {
			return false
		}
	}
	for _, v := range b.Bindings {
		if v.BundleRevision != b.GraphRevision {
			return false
		}
	}
	if b.Allocation != nil && b.Allocation.BundleRevision != b.GraphRevision {
		return false
	}
	return true
}

func resolveBinding(b AssessmentBundle, now time.Time) (BindingState, ObservedBinding, string, time.Time, bool) {
	if b.Intent.Claim.Vendor != "NVIDIA" || b.Intent.Claim.UUID == "" {
		return BindingUnknown, ObservedBinding{}, reasonUntrustedSource, time.Time{}, false
	}
	if b.CollectorTrust.ID == "" || b.CollectorTrust.ClusterID != b.Intent.Node.ClusterID || b.CollectorTrust.NodeUID != b.Intent.Node.UID || b.CollectorTrust.Session != b.Snapshot.Session {
		return BindingUnknown, ObservedBinding{}, reasonUntrustedSource, time.Time{}, false
	}
	var matches []ObservedBinding
	conflict := false
	for _, v := range b.Bindings {
		if v.Claim.Vendor != b.Intent.Claim.Vendor || v.Claim.UUID != b.Intent.Claim.UUID {
			continue
		}
		if !trusted(b.CollectorTrust, TrustNVIDIAUUIDBinding, v.Source) || v.CollectorProfileID != b.CollectorTrust.ID {
			continue
		}
		if v.ObservedAt.After(now) {
			return BindingUnknown, ObservedBinding{}, reasonFutureObservation, time.Time{}, true
		}
		if v.Node.ClusterID != b.Intent.Node.ClusterID || v.Node.UID != b.Intent.Node.UID || v.BootID != b.Snapshot.BootID {
			conflict = true
			continue
		}
		if v.ObservedAt.Before(b.Intent.ObservedAt) {
			continue
		}
		if !fresh(v.ObservedAt, v.ExpiresAt, b.Policy.Freshness, now) {
			continue
		}
		asset, err := b.Topology.Asset(v.Function)
		if err != nil || asset.Key() == "" || asset.Kind != model.KindPCIeFunction || v.Function.Canonical != model.NamespacePCIBDF+":"+v.BDF {
			continue
		}
		matches = append(matches, v)
	}
	if len(matches) > 1 {
		first := matches[0]
		for _, v := range matches[1:] {
			if v.BDF != first.BDF || v.Function.Key() != first.Function.Key() {
				conflict = true
			}
		}
	}
	if conflict {
		return BindingConflict, ObservedBinding{}, reasonIdentityConflict, time.Time{}, false
	}
	if len(matches) != 1 {
		return BindingUnknown, ObservedBinding{}, reasonUntrustedSource, time.Time{}, false
	}
	v := matches[0]
	return BindingBound, v, "", deadline(v.ObservedAt, v.ExpiresAt, b.Policy.Freshness), false
}
func bindingKey(b ObservedBinding) string {
	return b.Claim.Vendor + "\x00" + b.Claim.UUID + "\x00" + b.Node.UID + "\x00" + b.BootID + "\x00" + b.BDF
}
func trusted(p CollectorTrustProfile, capability TrustCapability, source model.SourceRef) bool {
	for _, s := range p.Sources {
		if s.Capability == capability && s.Source == source {
			return true
		}
	}
	return false
}
func fresh(observed, expires time.Time, d time.Duration, now time.Time) bool {
	return !observed.After(now) && now.Before(deadline(observed, expires, d))
}
func deadline(observed, expires time.Time, d time.Duration) time.Time {
	x := observed.Add(d)
	if expires.Before(x) {
		return expires
	}
	return x
}
func minTime(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func evaluateQualification(b AssessmentBundle, binding ObservedBinding, now time.Time) qualificationEvidence {
	q := qualificationEvidence{allNormal: true}
	requiredCount := 0
	findingsFresh := !b.FindingsEvaluatedAt.After(now) && now.Before(b.FindingsEvaluatedAt.Add(b.Policy.Freshness)) && b.FindingsGraphRevision == b.GraphRevision && findingsLinked(b, binding.Function)
	if b.FindingsEvaluatedAt.After(now) {
		q.future = true
		q.allNormal = false
	}
	if !findingsFresh {
		q.allNormal = false
	}
	for _, f := range b.Findings {
		if findingsFresh && f.State == model.StateActive && scopeContains(f.Scope, binding.Function) {
			q.activeBad = true
			q.findingIDs = append(q.findingIDs, f.ID)
			for _, e := range f.Evidence {
				q.evidenceIDs = append(q.evidenceIDs, e.ObservationID)
			}
		}
	}
	for _, req := range b.Policy.RequiredCoverage {
		if req.Required {
			requiredCount++
		}
		a := assessCoverage(b, binding, req, now)
		if req.Required && !findingsFresh {
			a.State = CoverageUnknown
			a.Reason = reasonBundleMismatch
			a.EvidenceIDs = nil
			a.ObservedAt = time.Time{}
			a.LatestObservedAt = time.Time{}
			a.ExpiresAt = time.Time{}
		}
		q.coverage = append(q.coverage, a)
		q.evidenceIDs = append(q.evidenceIDs, a.EvidenceIDs...)
		if a.State != CoverageNormal {
			q.allNormal = false
		}
		if a.State == CoverageMissing && req.Required {
			q.anyMissing = true
		}
		if req.Required && a.Reason == reasonFutureObservation {
			q.future = true
		}
		if req.Required && (a.State == CoverageUnknown || a.State == CoverageUnsupported) {
			q.anyUnknown = true
		}
		if a.State != CoverageNormal && req.Required {
			q.allNormal = false
		}
		if a.State == CoverageNormal && req.Required {
			if q.minimum.IsZero() || a.ObservedAt.Before(q.minimum) {
				q.minimum = a.ObservedAt
			}
			if a.LatestObservedAt.After(q.maximum) {
				q.maximum = a.LatestObservedAt
			}
			q.validUntil = minTime(q.validUntil, deadline(a.LatestObservedAt, a.ExpiresAt, b.Policy.Freshness))
		}
	}
	if requiredCount == 0 {
		q.allNormal = false
		q.anyUnknown = true
	}
	if findingsFresh {
		q.validUntil = minTime(q.validUntil, b.FindingsEvaluatedAt.Add(b.Policy.Freshness))
	}
	slices.SortFunc(q.coverage, func(a, c CoverageAssessment) int { return strings.Compare(a.Name, c.Name) })
	return q
}
func findingsLinked(b AssessmentBundle, function model.AssetRef) bool {
	ids := map[string]struct{}{}
	for _, p := range b.Provenance {
		ids[p.EvidenceID] = struct{}{}
	}
	for _, subject := range b.Window.Subjects() {
		signals, err := b.Window.Signals(subject)
		if err != nil {
			continue
		}
		for _, signal := range signals {
			series, err := b.Window.SeriesFor(subject, signal)
			if err != nil {
				continue
			}
			for _, s := range series {
				for _, o := range s.Observations() {
					ids[o.ID] = struct{}{}
				}
			}
		}
	}
	for _, f := range b.Findings {
		if f.State != model.StateActive || !scopeContains(f.Scope, function) {
			continue
		}
		for _, e := range f.Evidence {
			if _, ok := ids[e.ObservationID]; !ok {
				return false
			}
		}
	}
	return true
}
func scopeContains(scope []model.AssetRef, a model.AssetRef) bool {
	for _, s := range scope {
		if s.Key() == a.Key() {
			return true
		}
	}
	return false
}

func assessCoverage(b AssessmentBundle, binding ObservedBinding, req CoverageRequirement, now time.Time) CoverageAssessment {
	a := CoverageAssessment{Name: req.Name, PathKind: req.PathKind, State: CoverageUnknown, Reason: reasonCoverageUnknown, AssessmentSequence: b.Snapshot.Sequence}
	if b.Snapshot.Completeness != CompletenessComplete {
		return a
	}
	switch req.PathKind {
	case "gpu-pcie-parent":
		return assessPath(b, binding, req, now, false)
	case "gpu-pcie-root":
		return assessPath(b, binding, req, now, true)
	case "gpu-pcie-link-width-normal":
		return assessWidth(b, binding, req, now)
	case "gpu-nic-shared-ancestor":
		return assessSingleRelation(b, binding, req, now, model.RelSharesFailureDomainWith, TrustSysfsPhysicalParent)
	case "nic-lldp-remote":
		a.State = CoverageUnsupported
		a.Reason = "Unsupported"
		return a
	default:
		return a
	}
}
func assessPath(b AssessmentBundle, binding ObservedBinding, req CoverageRequirement, now time.Time, root bool) CoverageAssessment {
	a := CoverageAssessment{Name: req.Name, PathKind: req.PathKind, State: CoverageUnknown, Reason: reasonCoverageUnknown, AssessmentSequence: b.Snapshot.Sequence}
	if !hasCapability(b.CollectorTrust, TrustSysfsPhysicalParent) {
		a.Reason = reasonUntrustedSource
		return a
	}
	current := binding.Function
	seen := map[string]struct{}{current.Key(): {}}
	var proofs []EdgeProvenance
	for steps := 0; steps < 4096; steps++ {
		edges, _ := b.Topology.EdgesFrom(current)
		var observedParents []graph.Edge
		for _, e := range edges {
			if e.Relation == model.RelUpstreamOf && e.Origin == model.OriginObserved {
				observedParents = append(observedParents, e)
			}
		}
		if len(observedParents) > 1 {
			a.Reason = "TopologyConflict"
			return a
		}
		var parents []EdgeProvenance
		for _, e := range observedParents {
			if p, ok := findProvenance(b.Provenance, e, EdgeEvidenceObserved); ok {
				parents = append(parents, p)
			}
		}
		if len(observedParents) == 1 && len(parents) == 0 {
			a.Reason = reasonUntrustedSource
			return a
		}
		if len(parents) > 1 {
			a.Reason = "TopologyConflict"
			return a
		}
		if len(parents) == 0 {
			if len(proofs) == 0 || root {
				a.State = CoverageMissing
				a.Reason = reasonCoverageMissing
			}
			break
		}
		p := parents[0]
		if !trusted(b.CollectorTrust, TrustSysfsPhysicalParent, p.Source) || p.CollectorProfileID != b.CollectorTrust.ID {
			a.Reason = reasonUntrustedSource
			return a
		}
		if p.ObservedAt.After(now) {
			a.Reason = reasonFutureObservation
			return a
		}
		if p.ObservedAt.Before(b.Intent.ObservedAt) || !fresh(p.ObservedAt, p.ExpiresAt, b.Policy.Freshness, now) {
			return a
		}
		proofs = append(proofs, p)
		current = p.Edge.To
		if current.Kind != model.KindPCIeSwitch && current.Kind != model.KindPCIeRootPort {
			a.Reason = "TopologyConflict"
			return a
		}
		if _, ok := seen[current.Key()]; ok {
			a.Reason = "TopologyConflict"
			return a
		}
		seen[current.Key()] = struct{}{}
		if !root || current.Kind == model.KindPCIeRootPort {
			break
		}
	}
	if len(proofs) == 0 || (root && current.Kind != model.KindPCIeRootPort) {
		if a.State != CoverageMissing {
			a.State = CoverageMissing
			a.Reason = reasonCoverageMissing
		}
		return a
	}
	return normalCoverage(a, proofs, b.Policy.Freshness)
}
func assessSingleRelation(b AssessmentBundle, binding ObservedBinding, req CoverageRequirement, now time.Time, rel model.EdgeRelation, cap TrustCapability) CoverageAssessment {
	a := CoverageAssessment{Name: req.Name, PathKind: req.PathKind, State: CoverageUnknown, Reason: reasonCoverageUnknown, AssessmentSequence: b.Snapshot.Sequence}
	if !hasCapability(b.CollectorTrust, cap) {
		a.State = CoverageUnsupported
		a.Reason = "Unsupported"
		return a
	}
	edges, _ := b.Topology.EdgesFrom(binding.Function)
	foundRelation := false
	for _, e := range edges {
		if e.Relation != rel {
			continue
		}
		foundRelation = true
		p, ok := findProvenance(b.Provenance, e, EdgeEvidenceInferred)
		if !ok || !trusted(b.CollectorTrust, cap, p.Source) || p.CollectorProfileID != b.CollectorTrust.ID {
			continue
		}
		if p.ObservedAt.After(now) {
			a.Reason = reasonFutureObservation
			return a
		}
		if p.ObservedAt.Before(b.Intent.ObservedAt) || !fresh(p.ObservedAt, p.ExpiresAt, b.Policy.Freshness, now) {
			continue
		}
		return normalCoverage(a, []EdgeProvenance{p}, b.Policy.Freshness)
	}
	if foundRelation {
		a.Reason = reasonUntrustedSource
		return a
	}
	a.State = CoverageMissing
	a.Reason = reasonCoverageMissing
	return a
}
func findProvenance(all []EdgeProvenance, e graph.Edge, kind EdgeEvidenceKind) (EdgeProvenance, bool) {
	var found EdgeProvenance
	count := 0
	for _, p := range all {
		if sameEdge(p.Edge, e) && p.Kind == kind {
			found = p
			count++
		}
	}
	return found, count == 1
}
func sameEdge(a, b graph.Edge) bool {
	return a.From.Key() == b.From.Key() && a.To.Key() == b.To.Key() && a.Relation == b.Relation && a.Origin == b.Origin
}
func normalCoverage(a CoverageAssessment, p []EdgeProvenance, freshness time.Duration) CoverageAssessment {
	a.State = CoverageNormal
	a.Reason = "Normal"
	for _, v := range p {
		a.EvidenceIDs = append(a.EvidenceIDs, v.EvidenceID)
		if a.ObservedAt.IsZero() || v.ObservedAt.Before(a.ObservedAt) {
			a.ObservedAt = v.ObservedAt
		}
		if v.ObservedAt.After(a.LatestObservedAt) {
			a.LatestObservedAt = v.ObservedAt
		}
		a.ExpiresAt = minTime(a.ExpiresAt, deadline(v.ObservedAt, v.ExpiresAt, freshness))
	}
	a.EvidenceIDs = sortedUnique(a.EvidenceIDs)
	return a
}
func hasCapability(p CollectorTrustProfile, c TrustCapability) bool {
	for _, s := range p.Sources {
		if s.Capability == c {
			return true
		}
	}
	return false
}

func assessWidth(b AssessmentBundle, binding ObservedBinding, req CoverageRequirement, now time.Time) CoverageAssessment {
	a := CoverageAssessment{Name: req.Name, PathKind: req.PathKind, State: CoverageUnknown, Reason: reasonCoverageUnknown, AssessmentSequence: b.Snapshot.Sequence}
	if !hasCapability(b.CollectorTrust, TrustSysfsPCIeWidth) {
		a.State = CoverageUnsupported
		a.Reason = "Unsupported"
		return a
	}
	type reading struct{ o model.Observation }
	var currents, expected []reading
	for _, sig := range []model.SignalRef{pcie.SignalLinkWidthCurrent, pcie.SignalLinkWidthExpected} {
		series, err := b.Window.SeriesFor(binding.Function, sig)
		if err != nil {
			continue
		}
		for _, s := range series {
			if !trusted(b.CollectorTrust, TrustSysfsPCIeWidth, s.Source()) {
				continue
			}
			o, ok := s.Latest()
			if !ok {
				continue
			}
			if sig == pcie.SignalLinkWidthCurrent {
				currents = append(currents, reading{o})
			} else {
				expected = append(expected, reading{o})
			}
		}
	}
	for _, c := range currents {
		for _, e := range expected {
			if c.o.Source != e.o.Source || !c.o.ObservedAt.Equal(e.o.ObservedAt) || !mapsEqual(c.o.Dimensions, e.o.Dimensions) {
				continue
			}
			if c.o.Quality != model.QualityGood || e.o.Quality != model.QualityGood || c.o.Unit != pcie.UnitLinkWidth || e.o.Unit != pcie.UnitLinkWidth || len(c.o.Dimensions) != 4 {
				continue
			}
			if !validWidthDimensions(c.o.Dimensions) {
				continue
			}
			if c.o.ObservedAt.After(now) {
				a.Reason = reasonFutureObservation
				return a
			}
			if c.o.ObservedAt.Before(b.Intent.ObservedAt) || !fresh(c.o.ObservedAt, c.o.ExpiresAt, b.Policy.Freshness, now) || !fresh(e.o.ObservedAt, e.o.ExpiresAt, b.Policy.Freshness, now) {
				continue
			}
			provenance := c.o.Dimensions[pcie.DimensionExpectedProvenance]
			if provenance != pcie.ProvenanceAdjacentCapabilityMin && provenance != pcie.ProvenanceOperatorVerifiedWiring {
				continue
			}
			if provenance == pcie.ProvenanceOperatorVerifiedWiring && !trusted(b.CollectorTrust, TrustOperatorBaseline, e.o.Source) {
				a.Reason = reasonUntrustedSource
				return a
			}
			cv, cok := number(c.o.Value)
			ev, eok := number(e.o.Value)
			if !cok || !eok || cv < ev {
				a.State = CoverageMissing
				a.Reason = reasonCoverageMissing
				return a
			}
			a.State = CoverageNormal
			a.Reason = "Normal"
			a.EvidenceIDs = sortedUnique([]string{c.o.ID, e.o.ID})
			a.ObservedAt = c.o.ObservedAt
			a.LatestObservedAt = c.o.ObservedAt
			a.ExpiresAt = minTime(deadline(c.o.ObservedAt, c.o.ExpiresAt, b.Policy.Freshness), deadline(e.o.ObservedAt, e.o.ExpiresAt, b.Policy.Freshness))
			return a
		}
	}
	a.State = CoverageMissing
	a.Reason = reasonCoverageMissing
	return a
}
func validWidthDimensions(d map[string]string) bool {
	for _, key := range []string{pcie.DimensionRootCanonical, pcie.DimensionPeerCanonical, pcie.DimensionPeerKind, pcie.DimensionExpectedProvenance} {
		if d[key] == "" {
			return false
		}
	}
	return true
}
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func number(v model.Value) (float64, bool) {
	if n, ok := v.Int(); ok {
		return float64(n), true
	}
	return v.Float()
}

func advanceReadyWindow(d DeviceDecision, previous *DeviceDecision, b AssessmentBundle, q qualificationEvidence) DeviceDecision {
	cursors := make([]CoverageCursor, 0, len(d.Coverage))
	newPoint := true
	previousByName := map[string]CoverageCursor{}
	if previous != nil {
		for _, c := range previous.CoverageCursors {
			previousByName[c.Name] = c
		}
	}
	for _, a := range d.Coverage {
		if !requiredCoverage(b.Policy, a.Name) {
			continue
		}
		c := CoverageCursor{Name: a.Name, ObservedAt: a.ObservedAt, AssessmentSequence: a.AssessmentSequence, EvidenceDigest: coverageDigest(a)}
		if old, ok := previousByName[c.Name]; ok && !c.ObservedAt.After(old.ObservedAt) {
			newPoint = false
		}
		cursors = append(cursors, c)
	}
	stable := previous != nil && sameContinuity(*previous, d) && len(previous.CoverageCursors) == len(cursors)
	if newPoint {
		d.AcceptedNormalPoint = true
		d.LastCompositeMin = q.minimum
		d.LastCompositeMax = q.maximum
		d.CoverageCursors = cursors
		if !stable || !continuous(*previous, cursors, q.minimum, b.Policy.Freshness) {
			d.ReadyWindowStartedAt = q.minimum
		} else {
			d.ReadyWindowStartedAt = previous.ReadyWindowStartedAt
		}
	} else if stable {
		d.ReadyWindowStartedAt = previous.ReadyWindowStartedAt
		d.LastCompositeMin = previous.LastCompositeMin
		d.LastCompositeMax = previous.LastCompositeMax
		d.CoverageCursors = slices.Clone(previous.CoverageCursors)
	}
	if d.AcceptedNormalPoint && !d.ReadyWindowStartedAt.IsZero() && q.minimum.Sub(d.ReadyWindowStartedAt) >= b.Policy.ReadyFor {
		d.Phase = PhaseReady
		d.Qualification = QualificationQualified
		d.Reason = reasonReady
	} else {
		d.Phase = PhaseValidating
		d.Qualification = QualificationUnknown
		d.ValidUntil = time.Time{}
		d.Reason = reasonReadyWindow
	}
	return d
}
func requiredCoverage(p Policy, name string) bool {
	for _, r := range p.RequiredCoverage {
		if r.Name == name {
			return r.Required
		}
	}
	return false
}
func sameContinuity(p DeviceDecision, d DeviceDecision) bool {
	return p.BindingKey == d.BindingKey && p.NodeUID == d.NodeUID && p.BootID == d.BootID && p.Session == d.Session && p.TopologyDigest == d.TopologyDigest && p.BaselineDigest == d.BaselineDigest && p.PolicyRevision == d.PolicyRevision && p.RequestID == d.RequestID && p.MetadataGeneration == d.MetadataGeneration && p.Desired == d.Desired
}
func continuous(p DeviceDecision, cursors []CoverageCursor, min time.Time, freshness time.Duration) bool {
	if p.LastCompositeMin.IsZero() || min.Sub(p.LastCompositeMin) > freshness {
		return false
	}
	old := map[string]CoverageCursor{}
	for _, c := range p.CoverageCursors {
		old[c.Name] = c
	}
	for _, c := range cursors {
		v, ok := old[c.Name]
		if !ok || !c.ObservedAt.After(v.ObservedAt) || c.ObservedAt.Sub(v.ObservedAt) > freshness {
			return false
		}
	}
	return true
}
func coverageDigest(a CoverageAssessment) string {
	ids := slices.Clone(a.EvidenceIDs)
	slices.Sort(ids)
	h := sha256.New()
	for _, v := range append([]string{a.Name, a.PathKind}, ids...) {
		writeLength(h, v)
	}
	for _, t := range []time.Time{a.ObservedAt, a.LatestObservedAt, a.ExpiresAt} {
		writeLength(h, t.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type byteWriter interface{ Write([]byte) (int, error) }

func writeLength(w byteWriter, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len([]byte(s))))
	w.Write(b[:])
	w.Write([]byte(s))
}

func evaluateAllocation(b AssessmentBundle, binding ObservedBinding, now time.Time) AllocationState {
	a := b.Allocation
	if a == nil {
		return AllocationUnknown
	}
	if a.CollectorProfileID != b.CollectorTrust.ID || !hasCapability(b.CollectorTrust, TrustPodResourcesUUIDAllocation) || a.Profile != AllocationNVIDIAPodResourcesUUID || a.NodeUID != b.Snapshot.NodeUID || a.BootID != b.Snapshot.BootID || a.Session != b.Snapshot.Session || a.Sequence != b.Snapshot.Sequence || a.BundleRevision != b.GraphRevision || !a.Complete || a.ObservedAt.Before(b.Intent.ObservedAt) || !fresh(a.ObservedAt, a.ExpiresAt, b.Policy.Freshness, now) {
		return AllocationUnknown
	}
	for _, e := range a.Entries {
		if e.DeviceID == binding.Claim.UUID {
			if e.ResourceName != "nvidia.com/gpu" || a.ObservedAt.Before(e.Workload.CreatedAt) || (!e.Workload.DeletedAt.IsZero() && a.ObservedAt.After(e.Workload.DeletedAt)) {
				return AllocationUnknown
			}
			return AllocationInUse
		}
	}
	return AllocationEmpty
}
func optionalEvidenceFuture(b AssessmentBundle, now time.Time) bool {
	return (b.Allocation != nil && b.Allocation.ObservedAt.After(now)) || (b.Fence != nil && b.Fence.ObservedAt.After(now))
}
func finishIntentPhase(d DeviceDecision, b AssessmentBundle, binding ObservedBinding, now time.Time) DeviceDecision {
	if b.Intent.Desired == DesiredInService {
		return d
	}
	completed := d.Allocation == AllocationEmpty && fenceAccepted(b, now)
	pendingReason := reasonMaintenancePending
	if d.Allocation != AllocationEmpty {
		pendingReason = "AllocationUnknown"
	}
	if b.Intent.Desired == DesiredMaintenance {
		if completed {
			d.Phase = PhaseMaintenanceReady
			d.Reason = reasonMaintenanceReady
		} else {
			d.Phase = PhaseMaintenancePending
			d.Reason = pendingReason
		}
		return d
	}
	if completed && d.BindingState == BindingBound {
		d.Phase = PhaseRetired
		d.Reason = reasonRetired
	} else {
		d.Phase = PhaseRetiring
		d.Reason = pendingReason
	}
	return d
}
func fenceAccepted(b AssessmentBundle, now time.Time) bool {
	f, p := b.Fence, b.FenceTrust
	if f == nil || p == nil {
		return false
	}
	return f.State == FenceAcknowledged && f.Node.ClusterID == b.Intent.Node.ClusterID && f.Node.UID == b.Intent.Node.UID && f.DeviceUID == b.Intent.Device.UID && f.BootID == b.Snapshot.BootID && f.Session == b.Snapshot.Session && f.RequestID == b.Intent.RequestID && f.MetadataGeneration == b.Intent.MetadataGeneration && f.ObservedAt.Equal(f.ObservedAt) && !f.ObservedAt.Before(b.Intent.ObservedAt) && fresh(f.ObservedAt, f.ExpiresAt, b.Policy.Freshness, now) && f.TrustProfileID == p.ID && p.ClusterID == b.Intent.Node.ClusterID && f.Source == p.Source
}

func sortedUnique(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}
func cloneBundle(b AssessmentBundle) AssessmentBundle {
	b.Policy.RequiredCoverage = cloneCoverageRequirements(b.Policy.RequiredCoverage)
	b.Provenance = cloneProvenance(b.Provenance)
	b.Bindings = cloneBindings(b.Bindings)
	b.Findings = slices.Clone(b.Findings)
	b.Allocation = cloneAllocationBatch(b.Allocation)
	b.CollectorTrust.Sources = cloneTrusted(b.CollectorTrust.Sources)
	if b.Fence != nil {
		x := *b.Fence
		b.Fence = &x
	}
	if b.FenceTrust != nil {
		x := *b.FenceTrust
		b.FenceTrust = &x
	}
	return b
}
