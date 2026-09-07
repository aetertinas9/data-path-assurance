// Package pcie evaluates passive PCIe evidence without performing observation
// or changing the input window.
package pcie

import (
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// widthReading retains the originating series for the head freshness check.
type widthReading struct {
	observation model.Observation
	series      evidence.Series
}

type widthBatch struct {
	at                          time.Time
	readings                    []widthReading
	current, expected           widthReading
	currentWidth, expectedWidth *big.Int
	root, peer                  model.AssetRef
	valid                       bool
}

func (b *widthBatch) degraded() bool {
	return b.valid && b.currentWidth.Cmp(b.expectedWidth) < 0
}

func (b *widthBatch) fresh(now time.Time) (bool, error) {
	if !b.valid || b.at.After(now) {
		return false, nil
	}
	for _, r := range []widthReading{b.current, b.expected} {
		fresh, err := r.series.Fresh(now)
		if err != nil || !fresh {
			return false, err
		}
	}
	return true, nil
}

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

func evaluateSubject(w evidence.Window, subject model.AssetRef, now time.Time) (model.Finding, bool) {
	histories := make(map[model.SourceRef]map[time.Time]*widthBatch)
	var latest time.Time
	for _, signal := range []model.SignalRef{SignalLinkWidthCurrent, SignalLinkWidthExpected} {
		series, err := w.SeriesFor(subject, signal)
		if err != nil {
			return model.Finding{}, false
		}
		for _, s := range series {
			for _, o := range s.Observations() {
				at := o.ObservedAt.Round(0).UTC()
				if latest.IsZero() || at.After(latest) {
					latest = at
				}
				if histories[o.Source] == nil {
					histories[o.Source] = make(map[time.Time]*widthBatch)
				}
				if histories[o.Source][at] == nil {
					histories[o.Source][at] = &widthBatch{at: at}
				}
				batch := histories[o.Source][at]
				batch.readings = append(batch.readings, widthReading{o, s})
			}
		}
	}
	if latest.IsZero() || latest.After(now) {
		return model.Finding{}, false
	}
	sources := make([]model.SourceRef, 0, len(histories))
	for source := range histories {
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b model.SourceRef) int {
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	var agreed *widthBatch
	var selected []*widthBatch
	for _, source := range sources {
		batches := make([]*widthBatch, 0, len(histories[source]))
		for _, batch := range histories[source] {
			batch.validate()
			batches = append(batches, batch)
		}
		slices.SortFunc(batches, func(a, b *widthBatch) int { return a.at.Compare(b.at) })
		head := batches[len(batches)-1]
		fresh, err := head.fresh(now)
		if err != nil || (head.at.Equal(latest) && !fresh) {
			return model.Finding{}, false
		}
		if !fresh {
			continue
		}
		if agreed != nil && (!sameBaseline(agreed, head) || agreed.degraded() != head.degraded()) {
			return model.Finding{}, false
		}
		agreed = head
		if selected != nil || !head.at.Equal(latest) || !head.degraded() {
			continue
		}
		start := len(batches) - 1
		for start > 0 {
			previous, next := batches[start-1], batches[start]
			if !previous.degraded() || !sameBaseline(previous, next) || next.at.Sub(previous.at) > LinkWidthMaxGap {
				break
			}
			start--
		}
		suffix := batches[start:]
		if len(suffix) >= LinkWidthMinSamples && head.at.Sub(suffix[0].at) >= LinkWidthMinDuration {
			selected = suffix
		}
	}
	if selected == nil {
		return model.Finding{}, false
	}
	finding, err := makeWidthFinding(selected)
	return finding, err == nil
}

func sameBaseline(a, b *widthBatch) bool {
	return a.valid && b.valid && a.expectedWidth.Cmp(b.expectedWidth) == 0 &&
		maps.Equal(a.current.observation.Dimensions, b.current.observation.Dimensions)
}

func (b *widthBatch) validate() {
	if len(b.readings) != 2 {
		return
	}
	for _, reading := range b.readings {
		switch reading.observation.Signal {
		case SignalLinkWidthCurrent:
			b.current = reading
		case SignalLinkWidthExpected:
			b.expected = reading
		}
	}
	c, e := b.current.observation, b.expected.observation
	if c.Signal != SignalLinkWidthCurrent || e.Signal != SignalLinkWidthExpected ||
		c.Validate() != nil || e.Validate() != nil ||
		c.Quality != model.QualityGood || e.Quality != model.QualityGood ||
		c.Unit != UnitLinkWidth || e.Unit != UnitLinkWidth || !maps.Equal(c.Dimensions, e.Dimensions) {
		return
	}
	b.currentWidth, b.expectedWidth = positiveInteger(c.Value), positiveInteger(e.Value)
	if b.currentWidth == nil || b.expectedWidth == nil {
		return
	}
	d := c.Dimensions
	if len(d) != 4 {
		return
	}
	for _, key := range []string{DimensionRootCanonical, DimensionPeerCanonical, DimensionPeerKind, DimensionExpectedProvenance} {
		if _, exists := d[key]; !exists {
			return
		}
	}
	if d[DimensionExpectedProvenance] != ProvenanceAdjacentCapabilityMin &&
		d[DimensionExpectedProvenance] != ProvenanceOperatorVerifiedWiring {
		return
	}
	b.root = model.AssetRef{Kind: model.KindPCIeRootPort, Canonical: d[DimensionRootCanonical]}
	b.peer.Canonical = d[DimensionPeerCanonical]
	switch d[DimensionPeerKind] {
	case "PCIeRootPort":
		b.peer.Kind = model.KindPCIeRootPort
	case "PCIeSwitch":
		b.peer.Kind = model.KindPCIeSwitch
	case "PCIeFunction":
		b.peer.Kind = model.KindPCIeFunction
	default:
		return
	}
	if !validPathAsset(b.root) || !validPathAsset(b.peer) || b.peer.Key() == c.Subject.Key() {
		return
	}
	if b.peer.Kind == model.KindPCIeRootPort && b.peer.Canonical != b.root.Canonical {
		return
	}
	b.valid = true
}

func validPathAsset(asset model.AssetRef) bool {
	namespace, value, found := strings.Cut(asset.Canonical, ":")
	return found && namespace != "" && value != "" && asset.Validate() == nil
}

// big.Int preserves the exact integer represented by either numeric kind,
// including floating-point integers beyond int64 and float64's exact-int range.
func positiveInteger(value model.Value) *big.Int {
	if i, ok := value.Int(); ok {
		if i > 0 {
			return big.NewInt(i)
		}
		return nil
	}
	f, ok := value.Float()
	if !ok || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
		return nil
	}
	i, accuracy := new(big.Float).SetFloat64(f).Int(nil)
	if accuracy != big.Exact {
		return nil
	}
	return i
}

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
