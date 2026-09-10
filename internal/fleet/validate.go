package fleet

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}
func required(s, field string) error {
	if s == "" {
		return invalidf("%s is empty", field)
	}
	return nil
}
func requiredTime(t time.Time, field string) error {
	if t.IsZero() || t.Year() < 1 || t.Year() > 9999 {
		return invalidf("%s is not a valid required timestamp", field)
	}
	return nil
}
func optionalTime(t time.Time, field string) error {
	if !t.IsZero() && (t.Year() < 1 || t.Year() > 9999) {
		return invalidf("%s is outside the timestamp range", field)
	}
	return nil
}
func expiry(observed, expires time.Time, prefix string) error {
	if err := requiredTime(observed, prefix+".ObservedAt"); err != nil {
		return err
	}
	if err := requiredTime(expires, prefix+".ExpiresAt"); err != nil {
		return err
	}
	if !expires.After(observed) {
		return invalidf("%s.ExpiresAt is not after ObservedAt", prefix)
	}
	return nil
}
func zero[T any](v T) bool { return reflect.DeepEqual(v, *new(T)) }
func uniqueStrings(values []string, field string, nonempty bool) error {
	seen := map[string]struct{}{}
	for i, v := range values {
		if nonempty && v == "" {
			return invalidf("%s[%d] is empty", field, i)
		}
		if _, ok := seen[v]; ok {
			return invalidf("%s contains duplicate %q", field, v)
		}
		seen[v] = struct{}{}
	}
	return nil
}

func (n NodeRef) Validate() error {
	if err := required(n.ClusterID, "NodeRef.ClusterID"); err != nil {
		return err
	}
	if err := required(n.Name, "NodeRef.Name"); err != nil {
		return err
	}
	return required(n.UID, "NodeRef.UID")
}
func (d DeviceRef) Validate() error {
	if err := required(d.Name, "DeviceRef.Name"); err != nil {
		return err
	}
	return required(d.UID, "DeviceRef.UID")
}
func (c InventoryClaim) Validate() error {
	if err := required(c.Vendor, "InventoryClaim.Vendor"); err != nil {
		return err
	}
	if c.UUID == "" && c.Serial == "" {
		return invalidf("InventoryClaim has neither UUID nor Serial")
	}
	if err := required(c.Source, "InventoryClaim.Source"); err != nil {
		return err
	}
	return required(c.EvidenceID, "InventoryClaim.EvidenceID")
}
func (b ObservedBinding) Validate() error {
	if err := b.Node.Validate(); err != nil {
		return invalidf("ObservedBinding.Node: %v", err)
	}
	if err := required(b.BootID, "ObservedBinding.BootID"); err != nil {
		return err
	}
	if err := required(b.BDF, "ObservedBinding.BDF"); err != nil {
		return err
	}
	if err := b.Function.Validate(); err != nil {
		return invalidf("ObservedBinding.Function: %v", err)
	}
	if err := b.Claim.Validate(); err != nil {
		return invalidf("ObservedBinding.Claim: %v", err)
	}
	if err := b.Source.Validate(); err != nil {
		return invalidf("ObservedBinding.Source: %v", err)
	}
	for v, n := range map[string]string{b.EvidenceID: "ObservedBinding.EvidenceID", b.BundleRevision: "ObservedBinding.BundleRevision", b.CollectorProfileID: "ObservedBinding.CollectorProfileID"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	return expiry(b.ObservedAt, b.ExpiresAt, "ObservedBinding")
}
func (s SnapshotEnvelope) Validate() error {
	for v, n := range map[string]string{s.NodeUID: "SnapshotEnvelope.NodeUID", s.BootID: "SnapshotEnvelope.BootID", s.PayloadDigest: "SnapshotEnvelope.PayloadDigest", s.BundleRevision: "SnapshotEnvelope.BundleRevision"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if s.Session < 1 {
		return invalidf("SnapshotEnvelope.Session is not positive")
	}
	if s.Completeness != CompletenessComplete && s.Completeness != CompletenessPartial {
		return invalidf("SnapshotEnvelope.Completeness is invalid")
	}
	return requiredTime(s.ObservedAt, "SnapshotEnvelope.ObservedAt")
}
func (s SnapshotCursor) Validate() error {
	e := SnapshotEnvelope{NodeUID: s.NodeUID, BootID: s.BootID, PayloadDigest: s.PayloadDigest, BundleRevision: s.BundleRevision, Session: s.Session, Sequence: s.Sequence, Completeness: s.Completeness, ObservedAt: s.ObservedAt}
	if err := e.Validate(); err != nil {
		return invalidf("SnapshotCursor: %v", err)
	}
	return nil
}
func (p EdgeProvenance) Validate() error {
	if err := p.Edge.Validate(); err != nil {
		return invalidf("EdgeProvenance.Edge: %v", err)
	}
	for v, n := range map[string]string{p.EvidenceID: "EdgeProvenance.EvidenceID", p.BundleRevision: "EdgeProvenance.BundleRevision", p.CollectorProfileID: "EdgeProvenance.CollectorProfileID"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if !p.Kind.IsValid() {
		return invalidf("EdgeProvenance.Kind is invalid")
	}
	if err := p.Source.Validate(); err != nil {
		return invalidf("EdgeProvenance.Source: %v", err)
	}
	return expiry(p.ObservedAt, p.ExpiresAt, "EdgeProvenance")
}

var pathKinds = map[string]struct{}{"gpu-pcie-parent": {}, "gpu-pcie-root": {}, "gpu-pcie-link-width-normal": {}, "gpu-nic-shared-ancestor": {}, "nic-lldp-remote": {}}

func (r CoverageRequirement) Validate() error {
	if err := required(r.Name, "CoverageRequirement.Name"); err != nil {
		return err
	}
	if _, ok := pathKinds[r.PathKind]; !ok {
		return invalidf("CoverageRequirement.PathKind %q is unsupported", r.PathKind)
	}
	return nil
}
func (a CoverageAssessment) Validate() error {
	if err := (CoverageRequirement{Name: a.Name, PathKind: a.PathKind}).Validate(); err != nil {
		return invalidf("CoverageAssessment: %v", err)
	}
	if !a.State.IsValid() {
		return invalidf("CoverageAssessment.State is invalid")
	}
	if err := uniqueStrings(a.EvidenceIDs, "CoverageAssessment.EvidenceIDs", true); err != nil {
		return err
	}
	if a.State == CoverageNormal {
		if len(a.EvidenceIDs) == 0 {
			return invalidf("normal CoverageAssessment has no EvidenceIDs")
		}
		if err := expiry(a.ObservedAt, a.ExpiresAt, "CoverageAssessment"); err != nil {
			return err
		}
		if err := requiredTime(a.LatestObservedAt, "CoverageAssessment.LatestObservedAt"); err != nil {
			return err
		}
		if a.LatestObservedAt.Before(a.ObservedAt) || a.ExpiresAt.Before(a.LatestObservedAt) {
			return invalidf("CoverageAssessment timestamps are out of order")
		}
	} else {
		for t, n := range map[time.Time]string{a.ObservedAt: "CoverageAssessment.ObservedAt", a.LatestObservedAt: "CoverageAssessment.LatestObservedAt", a.ExpiresAt: "CoverageAssessment.ExpiresAt"} {
			if err := optionalTime(t, n); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s TrustedSource) Validate() error {
	if !s.Capability.IsValid() {
		return invalidf("TrustedSource.Capability is invalid")
	}
	if err := s.Source.Validate(); err != nil {
		return invalidf("TrustedSource.Source: %v", err)
	}
	return nil
}
func (p CollectorTrustProfile) Validate() error {
	if err := required(p.ID, "CollectorTrustProfile.ID"); err != nil {
		return err
	}
	if !p.Mode.IsValid() {
		return invalidf("CollectorTrustProfile.Mode is invalid")
	}
	if p.Mode == TrustModeOffline && !strings.HasPrefix(p.ID, "offline:") {
		return invalidf("offline CollectorTrustProfile.ID lacks offline: prefix")
	}
	for v, n := range map[string]string{p.ClusterID: "CollectorTrustProfile.ClusterID", p.NodeUID: "CollectorTrustProfile.NodeUID"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if p.Session < 1 {
		return invalidf("CollectorTrustProfile.Session is not positive")
	}
	seen := map[string]struct{}{}
	for i, s := range p.Sources {
		if err := s.Validate(); err != nil {
			return invalidf("CollectorTrustProfile.Sources[%d]: %v", i, err)
		}
		k := fmt.Sprintf("%d\x00%s\x00%s", s.Capability, s.Source.Type, s.Source.Name)
		if _, ok := seen[k]; ok {
			return invalidf("CollectorTrustProfile.Sources contains duplicate capability/source")
		}
		seen[k] = struct{}{}
		if s.Capability == TrustExternalFence {
			return invalidf("collector profile grants external fence capability")
		}
	}
	return nil
}
func (p FenceTrustProfile) Validate() error {
	for v, n := range map[string]string{p.ID: "FenceTrustProfile.ID", p.ClusterID: "FenceTrustProfile.ClusterID", p.Issuer: "FenceTrustProfile.Issuer"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if err := p.Source.Validate(); err != nil {
		return invalidf("FenceTrustProfile.Source: %v", err)
	}
	return nil
}
func (w WorkloadRef) Validate() error {
	for v, n := range map[string]string{w.Namespace: "WorkloadRef.Namespace", w.Name: "WorkloadRef.Name", w.UID: "WorkloadRef.UID", w.Container: "WorkloadRef.Container"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if err := requiredTime(w.CreatedAt, "WorkloadRef.CreatedAt"); err != nil {
		return err
	}
	if err := optionalTime(w.DeletedAt, "WorkloadRef.DeletedAt"); err != nil {
		return err
	}
	if !w.DeletedAt.IsZero() && w.DeletedAt.Before(w.CreatedAt) {
		return invalidf("WorkloadRef.DeletedAt is before CreatedAt")
	}
	return nil
}
func (e AllocationEntry) Validate() error {
	if err := required(e.ResourceName, "AllocationEntry.ResourceName"); err != nil {
		return err
	}
	if err := required(e.DeviceID, "AllocationEntry.DeviceID"); err != nil {
		return err
	}
	if err := e.Workload.Validate(); err != nil {
		return invalidf("AllocationEntry.Workload: %v", err)
	}
	return nil
}
func (b AllocationBatch) Validate() error {
	for v, n := range map[string]string{b.NodeUID: "AllocationBatch.NodeUID", b.BootID: "AllocationBatch.BootID", b.BundleRevision: "AllocationBatch.BundleRevision", b.EvidenceDigest: "AllocationBatch.EvidenceDigest", b.CollectorProfileID: "AllocationBatch.CollectorProfileID"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if b.Session < 1 {
		return invalidf("AllocationBatch.Session is not positive")
	}
	if err := expiry(b.ObservedAt, b.ExpiresAt, "AllocationBatch"); err != nil {
		return err
	}
	if !b.Profile.IsValid() {
		return invalidf("AllocationBatch.Profile is invalid")
	}
	if err := uniqueStrings(b.EvidenceRefs, "AllocationBatch.EvidenceRefs", true); err != nil {
		return err
	}
	for i, e := range b.Entries {
		if err := e.Validate(); err != nil {
			return invalidf("AllocationBatch.Entries[%d]: %v", i, err)
		}
	}
	return nil
}
func (f FenceAcknowledgement) Validate() error {
	if err := f.Node.Validate(); err != nil {
		return invalidf("FenceAcknowledgement.Node: %v", err)
	}
	for v, n := range map[string]string{f.DeviceUID: "FenceAcknowledgement.DeviceUID", f.BootID: "FenceAcknowledgement.BootID", f.RequestID: "FenceAcknowledgement.RequestID", f.EvidenceID: "FenceAcknowledgement.EvidenceID", f.TrustProfileID: "FenceAcknowledgement.TrustProfileID"} {
		if err := required(v, n); err != nil {
			return err
		}
	}
	if f.Session < 1 || f.MetadataGeneration < 1 {
		return invalidf("FenceAcknowledgement session/generation is not positive")
	}
	if !f.State.IsValid() {
		return invalidf("FenceAcknowledgement.State is invalid")
	}
	if err := f.Source.Validate(); err != nil {
		return invalidf("FenceAcknowledgement.Source: %v", err)
	}
	return expiry(f.ObservedAt, f.ExpiresAt, "FenceAcknowledgement")
}
func (p Policy) Validate() error {
	if err := required(p.Revision, "Policy.Revision"); err != nil {
		return err
	}
	if p.Freshness <= 0 || p.ReadyFor <= 0 {
		return invalidf("Policy freshness/ready window is not positive")
	}
	if len(p.RequiredCoverage) > 32 {
		return invalidf("Policy.RequiredCoverage exceeds 32")
	}
	seen := map[string]struct{}{}
	for i, r := range p.RequiredCoverage {
		if err := r.Validate(); err != nil {
			return invalidf("Policy.RequiredCoverage[%d]: %v", i, err)
		}
		if _, ok := seen[r.Name]; ok {
			return invalidf("Policy.RequiredCoverage contains duplicate name %q", r.Name)
		}
		seen[r.Name] = struct{}{}
	}
	return nil
}
func (i Intent) Validate() error {
	if err := i.Device.Validate(); err != nil {
		return invalidf("Intent.Device: %v", err)
	}
	if err := i.Node.Validate(); err != nil {
		return invalidf("Intent.Node: %v", err)
	}
	if !i.Desired.IsValid() {
		return invalidf("Intent.Desired is invalid")
	}
	if err := required(i.RequestID, "Intent.RequestID"); err != nil {
		return err
	}
	if i.MetadataGeneration < 1 {
		return invalidf("Intent.MetadataGeneration is less than one")
	}
	if err := requiredTime(i.ObservedAt, "Intent.ObservedAt"); err != nil {
		return err
	}
	if err := i.Claim.Validate(); err != nil {
		return invalidf("Intent.Claim: %v", err)
	}
	return nil
}

func (b AssessmentBundle) Validate() error {
	if err := b.Policy.Validate(); err != nil {
		return invalidf("AssessmentBundle.Policy: %v", err)
	}
	if err := b.Intent.Validate(); err != nil {
		return invalidf("AssessmentBundle.Intent: %v", err)
	}
	if err := b.Snapshot.Validate(); err != nil {
		return invalidf("AssessmentBundle.Snapshot: %v", err)
	}
	if err := b.Admitted.Validate(); err != nil {
		return invalidf("AssessmentBundle.Admitted: %v", err)
	}
	if b.GraphRevision == "" || b.WindowRevision == "" {
		return invalidf("AssessmentBundle revision is empty")
	}
	if !digestPattern.MatchString(b.TopologyDigest) || !digestPattern.MatchString(b.BaselineDigest) {
		return invalidf("AssessmentBundle topology/baseline digest is invalid")
	}
	for i, p := range b.Provenance {
		if err := p.Validate(); err != nil {
			return invalidf("AssessmentBundle.Provenance[%d]: %v", i, err)
		}
	}
	for i, v := range b.Bindings {
		if err := v.Validate(); err != nil {
			return invalidf("AssessmentBundle.Bindings[%d]: %v", i, err)
		}
	}
	for i, f := range b.Findings {
		if err := f.Validate(); err != nil {
			return invalidf("AssessmentBundle.Findings[%d]: %v", i, err)
		}
	}
	if err := requiredTime(b.FindingsEvaluatedAt, "AssessmentBundle.FindingsEvaluatedAt"); err != nil {
		return err
	}
	if b.FindingsGraphRevision == "" {
		return invalidf("AssessmentBundle.FindingsGraphRevision is empty")
	}
	if b.Allocation != nil {
		if err := b.Allocation.Validate(); err != nil {
			return invalidf("AssessmentBundle.Allocation: %v", err)
		}
	}
	if (b.Fence == nil) != (b.FenceTrust == nil) {
		return invalidf("AssessmentBundle.Fence and FenceTrust must both be present or absent")
	}
	if b.Fence != nil {
		if err := b.Fence.Validate(); err != nil {
			return invalidf("AssessmentBundle.Fence: %v", err)
		}
		if err := b.FenceTrust.Validate(); err != nil {
			return invalidf("AssessmentBundle.FenceTrust: %v", err)
		}
	}
	if err := b.CollectorTrust.Validate(); err != nil {
		return invalidf("AssessmentBundle.CollectorTrust: %v", err)
	}
	return nil
}

func (c CoverageCursor) Validate() error {
	if c.Name == "" {
		return invalidf("CoverageCursor.Name is empty")
	}
	if err := requiredTime(c.ObservedAt, "CoverageCursor.ObservedAt"); err != nil {
		return err
	}
	if !digestPattern.MatchString(c.EvidenceDigest) {
		return invalidf("CoverageCursor.EvidenceDigest is invalid")
	}
	return nil
}

func (d DeviceDecision) Validate() error {
	if !d.Desired.IsValid() || !d.Phase.IsValid() || !d.Qualification.IsValid() || !d.BindingState.IsValid() || !d.Allocation.IsValid() {
		return invalidf("DeviceDecision contains an invalid enum")
	}
	if d.BindingState == BindingBound {
		if err := d.Binding.Validate(); err != nil {
			return invalidf("DeviceDecision.Binding: %v", err)
		}
	} else if !zero(d.Binding) {
		return invalidf("DeviceDecision.Binding must be zero unless Bound")
	}
	for value, name := range map[string]string{d.PolicyRevision: "DeviceDecision.PolicyRevision", d.RequestID: "DeviceDecision.RequestID", d.DeviceUID: "DeviceDecision.DeviceUID", d.NodeUID: "DeviceDecision.NodeUID", d.BootID: "DeviceDecision.BootID", d.GraphRevision: "DeviceDecision.GraphRevision", d.TopologyDigest: "DeviceDecision.TopologyDigest", d.BaselineDigest: "DeviceDecision.BaselineDigest"} {
		if err := required(value, name); err != nil {
			return err
		}
	}
	if !digestPattern.MatchString(d.TopologyDigest) || !digestPattern.MatchString(d.BaselineDigest) {
		return invalidf("DeviceDecision topology/baseline digest is invalid")
	}
	if d.BindingState == BindingBound && d.BindingKey == "" {
		return invalidf("bound DeviceDecision.BindingKey is empty")
	}
	if d.BindingState != BindingBound && d.BindingKey != "" {
		return invalidf("unbound DeviceDecision.BindingKey is nonempty")
	}
	if d.MetadataGeneration < 1 || d.Session < 1 {
		return invalidf("DeviceDecision generation/session is not positive")
	}
	if err := requiredTime(d.EvaluatedAt, "DeviceDecision.EvaluatedAt"); err != nil {
		return err
	}
	if err := requiredTime(d.IntentObservedAt, "DeviceDecision.IntentObservedAt"); err != nil {
		return err
	}
	if d.Qualification == QualificationQualified {
		if err := requiredTime(d.ValidUntil, "DeviceDecision.ValidUntil"); err != nil {
			return err
		}
	} else if err := optionalTime(d.ValidUntil, "DeviceDecision.ValidUntil"); err != nil {
		return err
	}
	if err := uniqueStrings(d.EvidenceIDs, "DeviceDecision.EvidenceIDs", true); err != nil {
		return err
	}
	if err := uniqueStrings(d.FindingIDs, "DeviceDecision.FindingIDs", true); err != nil {
		return err
	}
	names := map[string]struct{}{}
	for i, c := range d.Coverage {
		if err := c.Validate(); err != nil {
			return invalidf("DeviceDecision.Coverage[%d]: %v", i, err)
		}
		if _, ok := names[c.Name]; ok {
			return invalidf("DeviceDecision.Coverage contains duplicate name")
		}
		names[c.Name] = struct{}{}
	}
	cursorNames := map[string]struct{}{}
	for i, c := range d.CoverageCursors {
		if err := c.Validate(); err != nil {
			return invalidf("DeviceDecision.CoverageCursors[%d]: %v", i, err)
		}
		if _, ok := cursorNames[c.Name]; ok {
			return invalidf("DeviceDecision.CoverageCursors contains duplicate name")
		}
		cursorNames[c.Name] = struct{}{}
	}
	if len(d.CoverageCursors) > 32 {
		return invalidf("DeviceDecision.CoverageCursors exceeds 32")
	}
	return nil
}

func (a DeviceAggregate) Validate() error {
	if a.DeviceUID == "" || a.NodeUID == "" {
		return invalidf("DeviceAggregate identity is empty")
	}
	if !a.Desired.IsValid() || a.MetadataGeneration < 1 {
		return invalidf("DeviceAggregate desired/generation is invalid")
	}
	if err := a.Decision.Validate(); err != nil {
		return invalidf("DeviceAggregate.Decision: %v", err)
	}
	return nil
}
func (a AggregateNodeInput) Validate() error {
	if err := a.Node.Validate(); err != nil {
		return invalidf("AggregateNodeInput.Node: %v", err)
	}
	if a.FleetUID == "" || !a.Selection.IsValid() {
		return invalidf("AggregateNodeInput fleet/selection is invalid")
	}
	for i, d := range a.Devices {
		if err := d.Validate(); err != nil {
			return invalidf("AggregateNodeInput.Devices[%d]: %v", i, err)
		}
	}
	return nil
}
func (n NodeDecision) Validate() error {
	if n.NodeUID == "" || n.FleetUID == "" || !digestPattern.MatchString(n.AssessmentRevision) {
		return invalidf("NodeDecision identity/reason/revision is invalid")
	}
	if !n.Qualification.IsValid() || !n.Eligibility.IsValid() || !n.Selection.IsValid() || n.DeviceCount < 0 {
		return invalidf("NodeDecision enum/count is invalid")
	}
	if err := requiredTime(n.EvaluatedAt, "NodeDecision.EvaluatedAt"); err != nil {
		return err
	}
	if n.Qualification == QualificationQualified {
		if err := requiredTime(n.ValidUntil, "NodeDecision.ValidUntil"); err != nil {
			return err
		}
	} else if err := optionalTime(n.ValidUntil, "NodeDecision.ValidUntil"); err != nil {
		return err
	}
	if (n.NormalPointAt.IsZero()) != (n.NormalPointDigest == "") {
		return invalidf("NodeDecision normal point fields differ in presence")
	}
	if n.NormalPointDigest != "" && !digestPattern.MatchString(n.NormalPointDigest) {
		return invalidf("NodeDecision.NormalPointDigest is invalid")
	}
	return nil
}
func (p CleanupPolicySnapshot) Validate() error {
	return (Policy{Revision: p.PolicyVersion, RequiredCoverage: p.RequiredCoverage, Freshness: p.Freshness, ReadyFor: p.ReadyFor}).Validate()
}
func (o GateOwnership) Validate() error {
	if o.OwnerFleetUID == "" || o.NodeUID == "" || o.Key == "" || o.Value == "" || o.Effect == "" {
		return invalidf("GateOwnership target is incomplete")
	}
	if err := o.Policy.Validate(); err != nil {
		return invalidf("GateOwnership.Policy: %v", err)
	}
	if !o.Phase.IsValid() {
		return invalidf("GateOwnership.Phase is invalid")
	}
	for t, n := range map[time.Time]string{o.CleanupRequestedAt: "GateOwnership.CleanupRequestedAt", o.RecoveryStartedAt: "GateOwnership.RecoveryStartedAt", o.LastRecoveryPointAt: "GateOwnership.LastRecoveryPointAt"} {
		if err := optionalTime(t, n); err != nil {
			return err
		}
	}
	if !o.RecoveryStartedAt.IsZero() && !o.LastRecoveryPointAt.IsZero() && o.LastRecoveryPointAt.Before(o.RecoveryStartedAt) {
		return invalidf("GateOwnership recovery timestamps are out of order")
	}
	return nil
}
func (g GateInput) Validate() error {
	if !g.Mode.IsValid() {
		return invalidf("GateInput.Mode is invalid")
	}
	if err := g.Node.Validate(); err != nil {
		return invalidf("GateInput.Node: %v", err)
	}
	if g.ControllerID == "" {
		return invalidf("GateInput.ControllerID is empty")
	}
	if err := g.NodeDecision.Validate(); err != nil {
		return invalidf("GateInput.NodeDecision: %v", err)
	}
	switch g.Mode {
	case GateModeAudit:
		if !zero(g.Ownership) {
			return invalidf("audit GateInput has ownership")
		}
	case GateModeEnforce:
		if g.FleetUID == "" {
			return invalidf("enforce GateInput.FleetUID is empty")
		}
		if err := g.Policy.Validate(); err != nil {
			return invalidf("GateInput.Policy: %v", err)
		}
	case GateModeCleanup:
		if zero(g.Ownership) {
			return invalidf("cleanup GateInput lacks ownership")
		}
		if err := g.Ownership.Validate(); err != nil {
			return invalidf("GateInput.Ownership: %v", err)
		}
	}
	return nil
}
func (g GateDecision) Validate() error {
	if !g.Action.IsValid() {
		return invalidf("GateDecision action is invalid")
	}
	if err := requiredTime(g.EvaluatedAt, "GateDecision.EvaluatedAt"); err != nil {
		return err
	}
	if g.Action == GateActionNone {
		if !zero(g.Ownership) {
			return invalidf("none GateDecision has ownership")
		}
	} else if err := g.Ownership.Validate(); err != nil {
		return invalidf("GateDecision.Ownership: %v", err)
	}
	return nil
}
