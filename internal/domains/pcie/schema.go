package pcie

import (
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"maps"
	"math"
	"math/big"
	"strings"
	"time"
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
