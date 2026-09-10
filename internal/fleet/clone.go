package fleet

import (
	"slices"

	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func cloneTypedIDs(in []model.TypedID) []model.TypedID {
	out := make([]model.TypedID, len(in))
	for i, v := range in {
		out[i] = v
		out[i].Raw = slices.Clone(v.Raw)
	}
	return out
}
func cloneAsset(a model.AssetRef) model.AssetRef { a.Aliases = cloneTypedIDs(a.Aliases); return a }
func cloneEdge(e graph.Edge) graph.Edge {
	e.From = cloneAsset(e.From)
	e.To = cloneAsset(e.To)
	return e
}
func cloneBinding(b ObservedBinding) ObservedBinding { b.Function = cloneAsset(b.Function); return b }
func cloneCoverageRequirements(in []CoverageRequirement) []CoverageRequirement {
	return slices.Clone(in)
}
func cloneCoverage(in []CoverageAssessment) []CoverageAssessment {
	out := make([]CoverageAssessment, len(in))
	for i, v := range in {
		out[i] = v
		out[i].EvidenceIDs = slices.Clone(v.EvidenceIDs)
	}
	return out
}
func cloneProvenance(in []EdgeProvenance) []EdgeProvenance {
	out := make([]EdgeProvenance, len(in))
	for i, v := range in {
		out[i] = v
		out[i].Edge = cloneEdge(v.Edge)
	}
	return out
}
func cloneBindings(in []ObservedBinding) []ObservedBinding {
	out := make([]ObservedBinding, len(in))
	for i, v := range in {
		out[i] = cloneBinding(v)
	}
	return out
}
func cloneTrusted(in []TrustedSource) []TrustedSource { return slices.Clone(in) }
func cloneAllocationBatch(in *AllocationBatch) *AllocationBatch {
	if in == nil {
		return nil
	}
	out := *in
	out.EvidenceRefs = slices.Clone(in.EvidenceRefs)
	out.Entries = slices.Clone(in.Entries)
	return &out
}
func cloneDecision(d DeviceDecision) DeviceDecision {
	d.Binding = cloneBinding(d.Binding)
	d.Coverage = cloneCoverage(d.Coverage)
	d.EvidenceIDs = slices.Clone(d.EvidenceIDs)
	d.FindingIDs = slices.Clone(d.FindingIDs)
	d.CoverageCursors = slices.Clone(d.CoverageCursors)
	return d
}
func cloneOwnership(o GateOwnership) GateOwnership {
	o.Policy.RequiredCoverage = cloneCoverageRequirements(o.Policy.RequiredCoverage)
	return o
}

func NewPolicy(p Policy) (Policy, error) {
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	p.RequiredCoverage = cloneCoverageRequirements(p.RequiredCoverage)
	return p, nil
}
