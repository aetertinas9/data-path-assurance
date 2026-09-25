package app

import (
	"slices"
	"strings"
)

// Output bounds of an explanation.
const (
	maxPathSegments      = 512
	maxActiveFindings    = 64
	maxCoverage          = 32
	maxAffectedWorkloads = 256
	maxDeviceSummaries   = 256
	maxEvidenceRefs      = 256
	maxLimitations       = 2048
)

// cut shortens list to bound and reports whether it did.
func cut[T any](list []T, bound int) ([]T, bool) {
	if len(list) <= bound {
		return list, false
	}
	return list[:bound], true
}

// finish orders every list by its key, truncates the lists over their bounds
// and records the lengths before truncation. Judgements were made before this
// point on the full lists; truncation only shortens what is shown.
func finish(e *Explanation) {
	slices.SortFunc(e.DeviceSummaries, func(x, y DeviceSummary) int { return strings.Compare(x.UID, y.UID) })
	slices.SortFunc(e.AffectedWorkloads, func(x, y WorkloadView) int { return strings.Compare(x.UID, y.UID) })
	if e.Kind == TargetNode {
		slices.SortStableFunc(e.Coverage, func(x, y CoverageView) int {
			if c := strings.Compare(x.deviceUID, y.deviceUID); c != 0 {
				return c
			}
			return strings.Compare(x.Name, y.Name)
		})
	} else {
		slices.SortStableFunc(e.Coverage, func(x, y CoverageView) int { return strings.Compare(x.Name, y.Name) })
	}

	t := &e.TotalCounts
	t.PathSegments = len(e.PathSegments)
	t.ActiveFindings = len(e.ActiveFindings)
	t.Coverage = len(e.Coverage)
	t.AffectedWorkloads = len(e.AffectedWorkloads)
	t.DeviceSummaries = len(e.DeviceSummaries)
	t.Limitations = len(e.Limitations)

	truncated := false
	mark := func(did bool) {
		if did {
			truncated = true
		}
	}
	var did bool
	e.PathSegments, did = cut(e.PathSegments, maxPathSegments)
	mark(did)
	e.ActiveFindings, did = cut(e.ActiveFindings, maxActiveFindings)
	mark(did)
	e.Coverage, did = cut(e.Coverage, maxCoverage)
	mark(did)
	e.AffectedWorkloads, did = cut(e.AffectedWorkloads, maxAffectedWorkloads)
	mark(did)
	e.DeviceSummaries, did = cut(e.DeviceSummaries, maxDeviceSummaries)
	mark(did)
	e.Limitations, did = cut(e.Limitations, maxLimitations)
	mark(did)

	for i := range e.ActiveFindings {
		f := &e.ActiveFindings[i]
		t.EvidenceRefs += len(f.Evidence)
		f.Evidence, did = cut(f.Evidence, maxEvidenceRefs)
		mark(did)
	}
	for i := range e.Coverage {
		c := &e.Coverage[i]
		t.EvidenceRefs += len(c.EvidenceRefs)
		c.EvidenceRefs, did = cut(c.EvidenceRefs, maxEvidenceRefs)
		mark(did)
	}
	if e.Allocation != nil {
		t.EvidenceRefs += len(e.Allocation.EvidenceRefs)
		e.Allocation.EvidenceRefs, did = cut(e.Allocation.EvidenceRefs, maxEvidenceRefs)
		mark(did)
		e.Allocation.AffectedWorkloads, did = cut(e.Allocation.AffectedWorkloads, maxAffectedWorkloads)
		mark(did)
	}
	e.Truncated = truncated
}
