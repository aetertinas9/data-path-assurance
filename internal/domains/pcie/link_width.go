// Package pcie evaluates passive PCIe evidence without performing observation
// or changing the input window.
package pcie

import (
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"slices"
	"strings"
	"time"
)

// Link width signals, schema fields, and adapter-supplied baseline provenance.
const (
	SignalLinkWidthCurrent  model.SignalRef = "pcie.link.width.current"
	SignalLinkWidthExpected model.SignalRef = "pcie.link.width.expected"

	UnitLinkWidth string = "lanes"

	DimensionRootCanonical      string = "pcie.root.canonical"
	DimensionPeerCanonical      string = "pcie.peer.canonical"
	DimensionPeerKind           string = "pcie.peer.kind"
	DimensionExpectedProvenance string = "pcie.expected.provenance"

	ProvenanceAdjacentCapabilityMin  string = "adjacent_capability_min"
	ProvenanceOperatorVerifiedWiring string = "operator_verified_wiring"

	LinkWidthMinSamples  int           = 3
	LinkWidthMinDuration time.Duration = 30 * time.Second
	LinkWidthMaxGap      time.Duration = 30 * time.Second
)

// EvaluateLinkWidth returns persistent link-width degradation candidates from
// the retained observations. Each collector proves its own continuous history.
func EvaluateLinkWidth(w evidence.Window, now time.Time) []model.Finding {
	if now.IsZero() {
		return nil
	}
	now = now.Round(0).UTC()
	var findings []model.Finding
	for _, subject := range w.Subjects() {
		if subject.Kind != model.KindPCIeFunction {
			continue
		}
		if finding, ok := evaluateSubject(w, subject, now); ok {
			findings = append(findings, finding)
		}
	}
	slices.SortFunc(findings, func(a, b model.Finding) int { return strings.Compare(a.ID, b.ID) })
	return findings
}
