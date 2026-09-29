package app

import (
	"slices"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// The live assessor keeps the decisions it computed as the next call's
// previous state and also hands them to its caller, so every value that
// crosses that boundary is deep-copied. The helpers keep nil and empty slices
// as they are, so a copy compares equal to its original.

func liveCloneDecision(d fleet.DeviceDecision) fleet.DeviceDecision {
	aliases := slices.Clone(d.Binding.Function.Aliases)
	for i := range aliases {
		aliases[i].Raw = slices.Clone(aliases[i].Raw)
	}
	d.Binding.Function.Aliases = aliases
	coverage := slices.Clone(d.Coverage)
	for i := range coverage {
		coverage[i].EvidenceIDs = slices.Clone(coverage[i].EvidenceIDs)
	}
	d.Coverage = coverage
	d.EvidenceIDs = slices.Clone(d.EvidenceIDs)
	d.FindingIDs = slices.Clone(d.FindingIDs)
	d.CoverageCursors = slices.Clone(d.CoverageCursors)
	return d
}

func liveClonePolicy(p fleet.Policy) fleet.Policy {
	p.RequiredCoverage = slices.Clone(p.RequiredCoverage)
	return p
}

func liveCloneFindings(in []FindingRecord) []FindingRecord { return slices.Clone(in) }

func liveCloneEvidence(in []EvidenceRecord) []EvidenceRecord { return slices.Clone(in) }

func liveCloneAllocation(in *AllocationRecord) *AllocationRecord {
	if in == nil {
		return nil
	}
	out := *in
	out.EvidenceRefs = slices.Clone(in.EvidenceRefs)
	out.Workloads = slices.Clone(in.Workloads)
	return &out
}
