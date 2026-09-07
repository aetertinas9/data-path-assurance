package pcie

import (
	"encoding/hex"
	"fmt"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"slices"
	"strconv"
	"strings"
	"time"
)

func makeWidthFinding(suffix []*widthBatch) (model.Finding, error) {
	first, head := suffix[0], suffix[len(suffix)-1]
	current := head.current.observation
	scope := []model.AssetRef{current.Subject, head.root}
	if head.peer.Key() != head.root.Key() {
		scope = append(scope, head.peer)
	}
	slices.SortFunc(scope, func(a, b model.AssetRef) int { return strings.Compare(a.Key(), b.Key()) })
	var refs []model.EvidenceRef
	for _, batch := range suffix {
		// Signals are distinct and current sorts before expected, so this is
		// the required (instant, signal, ID) order without deduplicating IDs.
		refs = append(refs,
			model.EvidenceRef{ObservationID: batch.current.observation.ID, Summary: string(SignalLinkWidthCurrent) + "=" + batch.currentWidth.String() + " lanes"},
			model.EvidenceRef{ObservationID: batch.expected.observation.ID, Summary: string(SignalLinkWidthExpected) + "=" + batch.expectedWidth.String() + " lanes"})
	}
	provenance := current.Dimensions[DimensionExpectedProvenance]
	basis := "adapter-supplied minimum of adjacent endpoint maximum widths"
	limitation := "capability does not establish verified wiring width"
	step := "Verify the adjacent link wiring baseline and inspect both link endpoints in audit mode."
	if provenance == ProvenanceOperatorVerifiedWiring {
		basis = "adapter-supplied operator-verified wiring width"
		limitation = "operator verification is asserted by the adapter and not revalidated by this rule"
		step = "Recheck the verified wiring baseline and inspect both link endpoints in audit mode."
	}
	encode := func(key string) string { return hex.EncodeToString([]byte(key)) }
	return model.NewFinding(model.Finding{
		ID:   "pcie-width-v1:" + encode(current.Subject.Key()) + ":" + encode(head.root.Key()) + ":" + encode(head.peer.Key()),
		Type: model.FindingPCIeLinkWidthDegraded, Scope: scope,
		Severity: model.SeverityWarning, Confidence: model.ConfidenceMedium, State: model.StateActive,
		Evidence: refs, FirstSeen: first.at, LastSeen: head.at, SuggestedStep: step,
		Explanation: fmt.Sprintf("PCIe link width degraded: subject=%s; peer=%s; root=%s; current=%s lanes; expected=%s lanes; samples=%d; first=%s; last=%s; source_type=%s; source_name=%s; provenance=%s; basis=%s; limitation=%s.",
			strconv.Quote(current.Subject.Key()), strconv.Quote(head.peer.Key()), strconv.Quote(head.root.Key()),
			head.currentWidth.String(), head.expectedWidth.String(), len(suffix),
			first.at.Format(time.RFC3339Nano), head.at.Format(time.RFC3339Nano),
			strconv.Quote(current.Source.Type), strconv.Quote(current.Source.Name), strconv.Quote(provenance), basis, limitation),
	})
}
