package controller

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// errInvalidAssessment is wrapped by every GKA-085 violation. Its text never
// reaches a log line or a status; only the class does.
var errInvalidAssessment = errors.New("controller: invalid assessment")

func invalidAssessment(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidAssessment, fmt.Sprintf(format, args...))
}

// inV22 reports whether reason is one of the 22 condition reasons (GFL-071).
func inV22(reason string) bool {
	switch reason {
	case v1alpha1.ReasonReady, v1alpha1.ReasonValidating, v1alpha1.ReasonDegraded, v1alpha1.ReasonCoverageMissing,
		v1alpha1.ReasonEvidenceStale, v1alpha1.ReasonIdentityConflict, v1alpha1.ReasonUIDMismatch, v1alpha1.ReasonAllocationUnknown,
		v1alpha1.ReasonFenceMissing, v1alpha1.ReasonConflict, v1alpha1.ReasonNoMatchingDevices, v1alpha1.ReasonPartialSnapshot,
		v1alpha1.ReasonBundleMismatch, v1alpha1.ReasonFutureObservation, v1alpha1.ReasonUntrustedSource, v1alpha1.ReasonUnadmittedSnapshot,
		v1alpha1.ReasonTargetCapacityExceeded, v1alpha1.ReasonCleanupPending, v1alpha1.ReasonCleanupStable, v1alpha1.ReasonCleanupCompleted,
		v1alpha1.ReasonOwnershipConflict, v1alpha1.ReasonInternalError:
		return true
	}
	return false
}

var coverageNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// List bounds of the device status (GKA-043, GKA-082).
const (
	maxFindingRefs        = 16
	maxEvidenceRefs       = 32
	maxCoverageEvidence   = 16
	maxAllocationEvidence = 8
	maxAffectedWorkloads  = 256
	maxDeviceSummaries    = 256
)

// deviceProjection is the decision part of a device status (GKA-073) together
// with the values the conditions are built from.
type deviceProjection struct {
	dec           fleet.DeviceDecision
	graphRevision string
	qualification v1alpha1.Qualification
	phase         v1alpha1.LifecyclePhase
	binding       v1alpha1.ActualBinding
	allocation    v1alpha1.AllocationSummary
	coverage      []v1alpha1.CoverageSummary
	findings      []v1alpha1.FindingSummary
	evidence      []v1alpha1.EvidenceSummary
	truncQual     []string // truncated tokens of DeviceQualified
	truncAlloc    []string // truncated tokens of AllocationKnown
}

// projectDecision maps one validated-shape DeviceAssessment to its status
// pieces and checks every GKA-085 condition that concerns the device: record
// completeness, allocation minimum, the CRD bounds and conditional fields.
func projectDecision(da app.DeviceAssessment) (*deviceProjection, error) {
	dec := da.Decision
	p := &deviceProjection{
		dec:           dec,
		graphRevision: dec.GraphRevision,
		qualification: v1alpha1.Qualification(dec.Qualification.String()),
		phase:         v1alpha1.LifecyclePhase(dec.Phase.String()),
	}
	if !okString(dec.GraphRevision, 1, 128) {
		return nil, invalidAssessment("graphRevision is outside its bound")
	}
	if !inV22(dec.Reason) || !okString(dec.Reason, 1, 512) {
		return nil, invalidAssessment("decision reason is not a condition reason")
	}

	// binding
	p.binding = v1alpha1.ActualBinding{State: v1alpha1.BindingState(dec.BindingState.String())}
	if dec.BindingState == fleet.BindingBound {
		b := dec.Binding
		p.binding.Reason = v1alpha1.ReasonReady
		p.binding.Vendor = strPtr(b.Claim.Vendor)
		p.binding.UUID = strPtr(b.Claim.UUID)
		if b.Claim.Serial != "" {
			p.binding.Serial = strPtr(b.Claim.Serial)
		}
		p.binding.NodeUID = strPtr(b.Node.UID)
		p.binding.BootID = strPtr(b.BootID)
		p.binding.BDF = strPtr(b.BDF)
		p.binding.FunctionKey = strPtr(b.Function.Key())
		p.binding.Source = &v1alpha1.SourceRef{Type: b.Source.Type, Name: b.Source.Name}
		p.binding.EvidenceID = strPtr(b.EvidenceID)
		if !validTime(b.ObservedAt) || !validTime(b.ExpiresAt) {
			return nil, invalidAssessment("binding time is not usable")
		}
		p.binding.ObservedAt = optTime(b.ObservedAt)
		p.binding.ExpiresAt = optTime(b.ExpiresAt)
		for _, f := range []struct {
			s      string
			lo, hi int
		}{
			{b.Claim.Vendor, 1, 128}, {b.Claim.UUID, 1, 128}, {b.Claim.Serial, 0, 128}, {b.Node.UID, 1, 128}, {b.BootID, 1, 128},
			{b.BDF, 1, 256}, {b.Function.Key(), 1, 256}, {b.Source.Type, 1, 256}, {b.Source.Name, 1, 256}, {b.EvidenceID, 1, 128},
		} {
			if !okString(f.s, f.lo, f.hi) {
				return nil, invalidAssessment("binding field is outside its bound")
			}
		}
	} else {
		p.binding.Reason = dec.Reason
	}

	// findings and evidence records
	findingByID := indexFindings(da.Findings)
	evidenceByID := indexEvidence(da.Evidence)
	ids := sortedUnique(dec.FindingIDs)
	for _, id := range ids {
		rec, ok := findingByID[id]
		if !ok {
			return nil, invalidAssessment("finding record is missing")
		}
		if !okString(rec.ID, 1, 128) || !okString(rec.Type, 1, 256) || !okString(rec.Severity, 1, 64) || !okString(rec.State, 1, 64) {
			return nil, invalidAssessment("finding record is outside its bound")
		}
		p.findings = append(p.findings, v1alpha1.FindingSummary{ID: rec.ID, Type: rec.Type, Severity: rec.Severity, State: rec.State})
	}
	if len(p.findings) > maxFindingRefs {
		p.findings = p.findings[:maxFindingRefs]
		p.truncQual = append(p.truncQual, "findingRefs")
	}
	evIDs := sortedUnique(dec.EvidenceIDs)
	for _, id := range evIDs {
		rec, ok := evidenceByID[id]
		if !ok {
			return nil, invalidAssessment("evidence record is missing")
		}
		if !okString(rec.ID, 1, 128) || !okString(rec.Summary, 0, 512) || !validTime(rec.ObservedAt) || !validTime(rec.ExpiresAt) {
			return nil, invalidAssessment("evidence record is outside its bound")
		}
		p.evidence = append(p.evidence, v1alpha1.EvidenceSummary{ID: rec.ID, Summary: rec.Summary, ObservedAt: metaTime(rec.ObservedAt), ExpiresAt: metaTime(rec.ExpiresAt)})
	}
	if len(p.evidence) > maxEvidenceRefs {
		p.evidence = p.evidence[:maxEvidenceRefs]
		p.truncQual = append(p.truncQual, "evidenceRefs")
	}

	// coverage
	cov := append([]fleet.CoverageAssessment(nil), dec.Coverage...)
	sort.Slice(cov, func(i, j int) bool { return cov[i].Name < cov[j].Name })
	covTruncated := false
	for _, c := range cov {
		if !okString(c.Name, 1, 253) || !coverageNamePattern.MatchString(c.Name) {
			return nil, invalidAssessment("coverage name is outside its bound")
		}
		reason := c.Reason
		if !okString(reason, 1, 512) || !isASCII(reason) {
			return nil, invalidAssessment("coverage reason is outside its bound")
		}
		refs := sortedUnique(c.EvidenceIDs)
		for _, r := range refs {
			if !okString(r, 1, 128) {
				return nil, invalidAssessment("coverage evidence id is outside its bound")
			}
		}
		if len(refs) > maxCoverageEvidence {
			refs = refs[:maxCoverageEvidence]
			covTruncated = true
		}
		if refs == nil {
			refs = []string{}
		}
		cs := v1alpha1.CoverageSummary{
			Name:             c.Name,
			PathKind:         c.PathKind,
			State:            v1alpha1.CoverageState(c.State.String()),
			Reason:           reason,
			EvidenceRefs:     refs,
			ObservedAt:       optTime(c.ObservedAt),
			LatestObservedAt: optTime(c.LatestObservedAt),
			ExpiresAt:        optTime(c.ExpiresAt),
		}
		if c.State == fleet.CoverageNormal {
			if cs.ObservedAt == nil || cs.LatestObservedAt == nil || cs.ExpiresAt == nil || len(refs) == 0 {
				return nil, invalidAssessment("normal coverage lacks times or evidence")
			}
			if !validTime(c.ObservedAt) || !validTime(c.LatestObservedAt) || !validTime(c.ExpiresAt) {
				return nil, invalidAssessment("coverage time is not usable")
			}
		}
		p.coverage = append(p.coverage, cs)
	}
	if covTruncated {
		p.truncQual = append(p.truncQual, "coverage.evidenceRefs")
	}
	if len(p.coverage) > 32 {
		return nil, invalidAssessment("coverage exceeds its bound")
	}

	// allocation
	p.allocation = v1alpha1.AllocationSummary{
		State:             v1alpha1.AllocationState(dec.Allocation.String()),
		Reason:            v1alpha1.ReasonAllocationUnknown,
		EvidenceRefs:      []string{},
		AffectedWorkloads: []v1alpha1.AffectedWorkload{},
	}
	if dec.Allocation == fleet.AllocationEmpty || dec.Allocation == fleet.AllocationInUse {
		rec := da.Allocation
		if rec == nil || rec.Profile == "" || !validTime(rec.ObservedAt) || !validTime(rec.ExpiresAt) || len(rec.EvidenceRefs) == 0 {
			return nil, invalidAssessment("allocation record is incomplete")
		}
		if !okString(rec.Profile, 1, 128) {
			return nil, invalidAssessment("allocation profile is outside its bound")
		}
		p.allocation.Reason = v1alpha1.ReasonReady
		p.allocation.Profile = strPtr(rec.Profile)
		p.allocation.ObservedAt = optTime(rec.ObservedAt)
		p.allocation.ExpiresAt = optTime(rec.ExpiresAt)
		refs := sortedUnique(rec.EvidenceRefs)
		for _, r := range refs {
			if !okString(r, 1, 128) {
				return nil, invalidAssessment("allocation evidence id is outside its bound")
			}
		}
		if len(refs) > maxAllocationEvidence {
			refs = refs[:maxAllocationEvidence]
			p.truncAlloc = append(p.truncAlloc, "allocation.evidenceRefs")
		}
		p.allocation.EvidenceRefs = refs
		ws, err := projectWorkloads(rec.Workloads)
		if err != nil {
			return nil, err
		}
		if len(ws) > maxAffectedWorkloads {
			ws = ws[:maxAffectedWorkloads]
			p.truncAlloc = append(p.truncAlloc, "affectedWorkloads")
		}
		p.allocation.AffectedWorkloads = ws
	}
	return p, nil
}

// indexFindings maps records by ID. An ID that appears twice is left out, so a
// decision referencing it fails the "exactly one record" rule (GKA-085(b)).
func indexFindings(recs []app.FindingRecord) map[string]app.FindingRecord {
	m := make(map[string]app.FindingRecord, len(recs))
	dup := map[string]bool{}
	for _, r := range recs {
		if _, ok := m[r.ID]; ok {
			dup[r.ID] = true
		}
		m[r.ID] = r
	}
	for id := range dup {
		delete(m, id)
	}
	return m
}

// indexEvidence is indexFindings for evidence records.
func indexEvidence(recs []app.EvidenceRecord) map[string]app.EvidenceRecord {
	m := make(map[string]app.EvidenceRecord, len(recs))
	dup := map[string]bool{}
	for _, r := range recs {
		if _, ok := m[r.ID]; ok {
			dup[r.ID] = true
		}
		m[r.ID] = r
	}
	for id := range dup {
		delete(m, id)
	}
	return m
}

// projectWorkloads orders workloads by uid, keeps the first container (byte
// order) of a repeated uid and checks the bounds.
func projectWorkloads(ws []app.WorkloadRecord) ([]v1alpha1.AffectedWorkload, error) {
	sorted := append([]app.WorkloadRecord(nil), ws...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].UID != sorted[j].UID {
			return sorted[i].UID < sorted[j].UID
		}
		return sorted[i].Container < sorted[j].Container
	})
	out := make([]v1alpha1.AffectedWorkload, 0, len(sorted))
	for i, w := range sorted {
		if i > 0 && w.UID == sorted[i-1].UID {
			continue
		}
		if !okString(w.Namespace, 1, 253) || !okString(w.Name, 1, 253) || !okString(w.UID, 1, 128) || !okString(w.Container, 1, 253) || !validTime(w.CreatedAt) {
			return nil, invalidAssessment("workload record is outside its bound")
		}
		out = append(out, v1alpha1.AffectedWorkload{
			Namespace: w.Namespace, Name: w.Name, UID: w.UID, Container: w.Container,
			CreatedAt: metaTime(w.CreatedAt), DeletedAt: optTime(w.DeletedAt),
		})
	}
	return out, nil
}
