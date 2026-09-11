package fleet

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
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
	if b.Snapshot.NodeUID != b.Intent.Node.UID || b.Snapshot.BundleRevision != b.GraphRevision || b.WindowRevision != b.GraphRevision || b.Topology == nil {
		return false
	}
	seq, ok := b.Topology.Sequence()
	if !ok || seq != b.Snapshot.Sequence || !b.Topology.Synced() {
		return false
	}
	nodeAnchor := model.AssetRef{Kind: model.KindKubernetesNode, Canonical: model.NamespaceKubernetesNodeUID + ":" + b.Snapshot.NodeUID}
	expectedPartition, err := graph.PartitionFor(nodeAnchor)
	if err != nil {
		return false
	}
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
	if b.CollectorTrust.ID == "" || b.CollectorTrust.ClusterID != b.Intent.Node.ClusterID || b.CollectorTrust.NodeUID != b.Intent.Node.UID || b.CollectorTrust.NodeUID != b.Snapshot.NodeUID || b.CollectorTrust.Session != b.Snapshot.Session {
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
		if len(f.Evidence) == 0 {
			return false
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
		return a
	case "nic-lldp-remote":
		if b.CollectorTrust.Mode == TrustModeLive {
			return a
		}
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
			if e.Relation == model.RelLocatedIn && e.Origin == model.OriginObserved {
				observedParents = append(observedParents, e)
			}
		}
		if len(observedParents) > 1 {
			a.Reason = "TopologyConflict"
			a.EvidenceIDs = provenanceIDsForEdges(b.Provenance, observedParents)
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
		stored, err := b.Topology.Asset(current)
		if err != nil || stored.Key() == "" || stored.Key() != current.Key() {
			a.Reason = "TopologyConflict"
			a.EvidenceIDs = provenanceIDs(proofs)
			return a
		}
		if current.Kind != model.KindPCIeSwitch && current.Kind != model.KindPCIeRootPort {
			a.Reason = "TopologyConflict"
			a.EvidenceIDs = provenanceIDs(proofs)
			return a
		}
		if _, ok := seen[current.Key()]; ok {
			a.Reason = "TopologyConflict"
			a.EvidenceIDs = provenanceIDs(proofs)
			return a
		}
		seen[current.Key()] = struct{}{}
		if current.Kind == model.KindPCIeRootPort && rootAncestryContradicts(b, current, seen) {
			a.Reason = "TopologyConflict"
			a.EvidenceIDs = sortedUnique(append(provenanceIDs(proofs), observedEvidenceIDsFrom(b, current)...))
			return a
		}
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
func rootAncestryContradicts(b AssessmentBundle, root model.AssetRef, seen map[string]struct{}) bool {
	edges, _ := b.Topology.EdgesFrom(root)
	var parents []graph.Edge
	for _, e := range edges {
		if e.Relation == model.RelLocatedIn && e.Origin == model.OriginObserved {
			parents = append(parents, e)
		}
	}
	if len(parents) > 1 {
		return true
	}
	if len(parents) == 0 {
		return false
	}
	parent := parents[0].To
	if _, ok := seen[parent.Key()]; ok {
		return true
	}
	if parent.Kind != model.KindKubernetesNode {
		return true
	}
	want := model.AssetRef{Kind: model.KindKubernetesNode, Canonical: model.NamespaceKubernetesNodeUID + ":" + b.Intent.Node.UID}
	return parent.Key() != want.Key()
}
func observedEvidenceIDsFrom(b AssessmentBundle, from model.AssetRef) []string {
	var ids []string
	for _, p := range b.Provenance {
		if p.Kind == EdgeEvidenceObserved && p.Edge.From.Key() == from.Key() && p.Edge.Relation == model.RelLocatedIn && p.Edge.Origin == model.OriginObserved {
			ids = append(ids, p.EvidenceID)
		}
	}
	return sortedUnique(ids)
}
func provenanceIDs(proofs []EdgeProvenance) []string {
	ids := make([]string, 0, len(proofs))
	for _, p := range proofs {
		ids = append(ids, p.EvidenceID)
	}
	return sortedUnique(ids)
}
func provenanceIDsForEdges(all []EdgeProvenance, edges []graph.Edge) []string {
	var ids []string
	for _, e := range edges {
		for _, p := range all {
			if p.Kind == EdgeEvidenceObserved && sameEdge(p.Edge, e) {
				ids = append(ids, p.EvidenceID)
			}
		}
	}
	return sortedUnique(ids)
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
	type reading struct {
		o      model.Observation
		series evidence.Series
	}
	var readings []reading
	for _, sig := range []model.SignalRef{pcie.SignalLinkWidthCurrent, pcie.SignalLinkWidthExpected} {
		series, err := b.Window.SeriesFor(binding.Function, sig)
		if err != nil {
			continue
		}
		for _, s := range series {
			o, ok := s.Latest()
			if !ok {
				continue
			}
			readings = append(readings, reading{o: o, series: s})
		}
	}
	if len(readings) == 0 {
		return a
	}
	latest := readings[0].o.ObservedAt
	for _, r := range readings[1:] {
		if r.o.ObservedAt.After(latest) {
			latest = r.o.ObservedAt
		}
	}
	if latest.After(now) {
		a.Reason = reasonFutureObservation
		return a
	}
	bySource := map[model.SourceRef][]reading{}
	for _, r := range readings {
		bySource[r.o.Source] = append(bySource[r.o.Source], r)
	}
	sources := make([]model.SourceRef, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(x, y model.SourceRef) int {
		if c := strings.Compare(x.Type, y.Type); c != 0 {
			return c
		}
		return strings.Compare(x.Name, y.Name)
	})
	var expectedWidth *big.Int
	var expectedDimensions map[string]string
	var referenceNormal *bool
	latestPairFound := false
	for _, source := range sources {
		sourceLatest := bySource[source][0].o.ObservedAt
		for _, r := range bySource[source][1:] {
			if r.o.ObservedAt.After(sourceLatest) {
				sourceLatest = r.o.ObservedAt
			}
		}
		atGlobalLatest := sourceLatest.Equal(latest)
		if !trusted(b.CollectorTrust, TrustSysfsPCIeWidth, source) {
			if atGlobalLatest {
				return widthUnknown(a, reasonUntrustedSource)
			}
			continue
		}
		var batch []reading
		for _, r := range bySource[source] {
			if r.o.ObservedAt.Equal(sourceLatest) {
				batch = append(batch, r)
			}
		}
		if len(batch) != 2 {
			if atGlobalLatest {
				return widthUnknown(a, reasonBundleMismatch)
			}
			continue
		}
		var current, expected reading
		var currentCount, expectedCount int
		for _, r := range batch {
			switch r.o.Signal {
			case pcie.SignalLinkWidthCurrent:
				current = r
				currentCount++
			case pcie.SignalLinkWidthExpected:
				expected = r
				expectedCount++
			}
		}
		if currentCount != 1 || expectedCount != 1 || !mapsEqual(current.o.Dimensions, expected.o.Dimensions) {
			if atGlobalLatest {
				return widthUnknown(a, reasonBundleMismatch)
			}
			continue
		}
		if !validWidthDimensions(current.o.Dimensions, binding.Function) {
			if atGlobalLatest {
				return widthUnknown(a, reasonCoverageUnknown)
			}
			continue
		}
		pathProofs, pathReason := widthPathProofs(b, binding.Function, current.o.Dimensions, now)
		if pathReason != "" {
			if atGlobalLatest {
				return widthUnknown(a, pathReason)
			}
			continue
		}
		if current.o.Dimensions[pcie.DimensionExpectedProvenance] == pcie.ProvenanceOperatorVerifiedWiring && !trusted(b.CollectorTrust, TrustOperatorBaseline, source) {
			if atGlobalLatest {
				return widthUnknown(a, reasonUntrustedSource)
			}
			continue
		}
		cv, ev, ok := validateWidthPair(current, expected, b, now)
		if !ok {
			if atGlobalLatest {
				return widthUnknown(a, reasonCoverageUnknown)
			}
			continue
		}
		if expectedWidth == nil {
			expectedWidth = new(big.Int).Set(ev)
			expectedDimensions = current.o.Dimensions
		} else if expectedWidth.Cmp(ev) != 0 || !mapsEqual(expectedDimensions, current.o.Dimensions) {
			return widthUnknown(a, reasonBundleMismatch)
		}
		thisNormal := cv.Cmp(ev) >= 0
		if referenceNormal == nil {
			x := thisNormal
			referenceNormal = &x
		} else if *referenceNormal != thisNormal {
			return widthUnknown(a, reasonBundleMismatch)
		}
		if atGlobalLatest {
			latestPairFound = true
			a.EvidenceIDs = append(a.EvidenceIDs, current.o.ID, expected.o.ID)
			a.ExpiresAt = minTime(a.ExpiresAt, minTime(deadline(current.o.ObservedAt, current.o.ExpiresAt, b.Policy.Freshness), deadline(expected.o.ObservedAt, expected.o.ExpiresAt, b.Policy.Freshness)))
			for _, proof := range pathProofs {
				a.EvidenceIDs = append(a.EvidenceIDs, proof.EvidenceID)
				a.ExpiresAt = minTime(a.ExpiresAt, deadline(proof.ObservedAt, proof.ExpiresAt, b.Policy.Freshness))
				if a.ObservedAt.IsZero() || proof.ObservedAt.Before(a.ObservedAt) {
					a.ObservedAt = proof.ObservedAt
				}
				if proof.ObservedAt.After(a.LatestObservedAt) {
					a.LatestObservedAt = proof.ObservedAt
				}
			}
		}
	}
	if !latestPairFound || referenceNormal == nil {
		return a
	}
	if !*referenceNormal {
		a.State = CoverageMissing
		a.Reason = reasonCoverageMissing
		a.EvidenceIDs = sortedUnique(a.EvidenceIDs)
		if a.ObservedAt.IsZero() || latest.Before(a.ObservedAt) {
			a.ObservedAt = latest
		}
		if latest.After(a.LatestObservedAt) {
			a.LatestObservedAt = latest
		}
		return a
	}
	a.State = CoverageNormal
	a.Reason = "Normal"
	a.EvidenceIDs = sortedUnique(a.EvidenceIDs)
	if a.ObservedAt.IsZero() || latest.Before(a.ObservedAt) {
		a.ObservedAt = latest
	}
	if latest.After(a.LatestObservedAt) {
		a.LatestObservedAt = latest
	}
	return a
}
func widthUnknown(a CoverageAssessment, reason string) CoverageAssessment {
	a.State = CoverageUnknown
	a.Reason = reason
	a.EvidenceIDs = nil
	a.ObservedAt = time.Time{}
	a.LatestObservedAt = time.Time{}
	a.ExpiresAt = time.Time{}
	return a
}
func validateWidthPair(current, expected struct {
	o      model.Observation
	series evidence.Series
}, b AssessmentBundle, now time.Time) (*big.Int, *big.Int, bool) {
	for _, r := range []struct {
		o      model.Observation
		series evidence.Series
	}{current, expected} {
		if r.o.Validate() != nil || r.o.Quality != model.QualityGood || r.o.Unit != pcie.UnitLinkWidth || !r.o.ExpiresAt.After(r.o.ObservedAt) || r.o.ObservedAt.Before(b.Intent.ObservedAt) || !fresh(r.o.ObservedAt, r.o.ExpiresAt, b.Policy.Freshness, now) {
			return nil, nil, false
		}
		ok, err := r.series.Fresh(now)
		if err != nil || !ok {
			return nil, nil, false
		}
	}
	cv, ok := positiveWidth(current.o.Value)
	if !ok {
		return nil, nil, false
	}
	ev, ok := positiveWidth(expected.o.Value)
	if !ok {
		return nil, nil, false
	}
	return cv, ev, true
}
func validWidthDimensions(d map[string]string, subject model.AssetRef) bool {
	if len(d) != 4 {
		return false
	}
	for _, key := range []string{pcie.DimensionRootCanonical, pcie.DimensionPeerCanonical, pcie.DimensionPeerKind, pcie.DimensionExpectedProvenance} {
		if d[key] == "" {
			return false
		}
	}
	root := model.AssetRef{Kind: model.KindPCIeRootPort, Canonical: d[pcie.DimensionRootCanonical]}
	if root.Validate() != nil {
		return false
	}
	var kind model.AssetKind
	switch d[pcie.DimensionPeerKind] {
	case "PCIeRootPort":
		kind = model.KindPCIeRootPort
	case "PCIeSwitch":
		kind = model.KindPCIeSwitch
	case "PCIeFunction":
		kind = model.KindPCIeFunction
	default:
		return false
	}
	peer := model.AssetRef{Kind: kind, Canonical: d[pcie.DimensionPeerCanonical]}
	if peer.Validate() != nil || peer.Key() == subject.Key() {
		return false
	}
	if kind == model.KindPCIeRootPort && peer.Canonical != root.Canonical {
		return false
	}
	p := d[pcie.DimensionExpectedProvenance]
	return p == pcie.ProvenanceAdjacentCapabilityMin || p == pcie.ProvenanceOperatorVerifiedWiring
}
func widthPathProofs(b AssessmentBundle, function model.AssetRef, d map[string]string, now time.Time) ([]EdgeProvenance, string) {
	if !hasCapability(b.CollectorTrust, TrustSysfsPhysicalParent) {
		return nil, reasonUntrustedSource
	}
	wantRoot := model.AssetRef{Kind: model.KindPCIeRootPort, Canonical: d[pcie.DimensionRootCanonical]}
	peerKind, ok := widthPeerKind(d[pcie.DimensionPeerKind])
	if !ok {
		return nil, reasonCoverageUnknown
	}
	wantPeer := model.AssetRef{Kind: peerKind, Canonical: d[pcie.DimensionPeerCanonical]}
	current := function
	seen := map[string]struct{}{current.Key(): {}}
	var proofs []EdgeProvenance
	var peer model.AssetRef
	for steps := 0; steps < 4096; steps++ {
		edges, _ := b.Topology.EdgesFrom(current)
		var parents []graph.Edge
		for _, edge := range edges {
			if edge.Relation == model.RelLocatedIn && edge.Origin == model.OriginObserved {
				parents = append(parents, edge)
			}
		}
		if len(parents) != 1 {
			return nil, reasonCoverageUnknown
		}
		proof, found := findProvenance(b.Provenance, parents[0], EdgeEvidenceObserved)
		if !found {
			return nil, reasonCoverageUnknown
		}
		if !trusted(b.CollectorTrust, TrustSysfsPhysicalParent, proof.Source) || proof.CollectorProfileID != b.CollectorTrust.ID {
			return nil, reasonUntrustedSource
		}
		if proof.ObservedAt.After(now) {
			return nil, reasonFutureObservation
		}
		if proof.ObservedAt.Before(b.Intent.ObservedAt) || !fresh(proof.ObservedAt, proof.ExpiresAt, b.Policy.Freshness, now) {
			return nil, reasonCoverageUnknown
		}
		proofs = append(proofs, proof)
		current = proof.Edge.To
		stored, err := b.Topology.Asset(current)
		if err != nil || stored.Key() != current.Key() || (current.Kind != model.KindPCIeSwitch && current.Kind != model.KindPCIeRootPort) {
			return nil, reasonCoverageUnknown
		}
		if len(proofs) == 1 {
			peer = current
		}
		if _, exists := seen[current.Key()]; exists {
			return nil, reasonCoverageUnknown
		}
		seen[current.Key()] = struct{}{}
		if current.Kind == model.KindPCIeRootPort {
			if rootAncestryContradicts(b, current, seen) {
				return nil, reasonCoverageUnknown
			}
			if current.Key() != wantRoot.Key() || peer.Key() != wantPeer.Key() {
				return nil, reasonCoverageUnknown
			}
			return proofs, ""
		}
	}
	return nil, reasonCoverageUnknown
}
func widthPeerKind(value string) (model.AssetKind, bool) {
	switch value {
	case "PCIeRootPort":
		return model.KindPCIeRootPort, true
	case "PCIeSwitch":
		return model.KindPCIeSwitch, true
	case "PCIeFunction":
		return model.KindPCIeFunction, true
	default:
		return 0, false
	}
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
func positiveWidth(v model.Value) (*big.Int, bool) {
	if n, ok := v.Int(); ok {
		if n <= 0 {
			return nil, false
		}
		return big.NewInt(n), true
	}
	f, ok := v.Float()
	if !ok || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
		return nil, false
	}
	n, accuracy := new(big.Float).SetFloat64(f).Int(nil)
	return n, accuracy == big.Exact
}

func advanceReadyWindow(d DeviceDecision, previous *DeviceDecision, b AssessmentBundle, q qualificationEvidence) DeviceDecision {
	cursors := make([]CoverageCursor, 0, len(d.Coverage))
	newPoint := true
	stable := previous != nil && sameContinuity(*previous, d)
	previousByName := map[string]CoverageCursor{}
	if stable {
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
	stable = stable && len(previous.CoverageCursors) == len(cursors)
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
	} else if !d.AcceptedNormalPoint && stable && previous.Phase == PhaseReady && previous.Qualification == QualificationQualified && previous.ValidUntil.After(d.EvaluatedAt) && d.ValidUntil.After(d.EvaluatedAt) {
		d.Phase = PhaseReady
		d.Qualification = QualificationQualified
		d.ValidUntil = minTime(previous.ValidUntil, d.ValidUntil)
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
	return p.DeviceUID == d.DeviceUID && p.BindingKey == d.BindingKey && p.NodeUID == d.NodeUID && p.BootID == d.BootID && p.Session == d.Session && p.TopologyDigest == d.TopologyDigest && p.BaselineDigest == d.BaselineDigest && p.PolicyRevision == d.PolicyRevision && p.RequestID == d.RequestID && p.MetadataGeneration == d.MetadataGeneration && p.Desired == d.Desired
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
		if e.ResourceName != "nvidia.com/gpu" || a.ObservedAt.Before(e.Workload.CreatedAt) || (!e.Workload.DeletedAt.IsZero() && a.ObservedAt.After(e.Workload.DeletedAt)) {
			return AllocationUnknown
		}
	}
	for _, e := range a.Entries {
		if e.DeviceID == binding.Claim.UUID {
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
	fenceOK, fenceReason := fenceAssessment(b, now)
	completed := d.Allocation == AllocationEmpty && fenceOK
	pendingReason := reasonMaintenancePending
	if d.Allocation != AllocationEmpty {
		pendingReason = "AllocationUnknown"
	} else if !fenceOK {
		pendingReason = fenceReason
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
func fenceAssessment(b AssessmentBundle, now time.Time) (bool, string) {
	f, p := b.Fence, b.FenceTrust
	if f == nil || p == nil {
		return false, "FenceMissing"
	}
	if f.TrustProfileID != p.ID || f.Source != p.Source || p.ClusterID != b.Intent.Node.ClusterID || f.Node.ClusterID != b.Intent.Node.ClusterID || f.Node.UID != b.Intent.Node.UID {
		return false, reasonUntrustedSource
	}
	if f.DeviceUID != b.Intent.Device.UID || f.BootID != b.Snapshot.BootID || f.Session != b.Snapshot.Session || f.RequestID != b.Intent.RequestID || f.MetadataGeneration != b.Intent.MetadataGeneration || f.ObservedAt.Before(b.Intent.ObservedAt) || f.State != FenceAcknowledged {
		return false, "FenceMissing"
	}
	if !fresh(f.ObservedAt, f.ExpiresAt, b.Policy.Freshness, now) {
		return false, "EvidenceStale"
	}
	return true, reasonReady
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
	b.Findings = cloneFindings(b.Findings)
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
