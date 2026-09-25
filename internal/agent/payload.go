package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Fixed sources of the collector (GFO §2.3). They never come from a manifest.
var (
	parentSource = model.SourceRef{Type: model.SourceTypeAgent, Name: "path-agent/sysfs-parent"}
	widthSource  = model.SourceRef{Type: model.SourceTypeAgent, Name: "path-agent/sysfs-width"}
	nvidiaSource = model.SourceRef{Type: model.SourceTypeAgent, Name: "path-agent/nvidia-smi"}
)

// Class observation signal and unit (GFO-064).
const (
	signalClassCode model.SignalRef = "pcie.function.class_code"
	unitPCIClass    string          = "pci_class"
)

// Deterministic ID prefixes and tags (GFO-084).
const (
	idPrefixEdgeEvidence = "pe"
	idPrefixObservation  = "ob"
	idPrefixGPUBinding   = "nb"
	idTagEdgeEvidence    = "dpa.edge-evidence.v1"
	idTagObservation     = "dpa.observation.v1"
	idTagGPUBinding      = "dpa.gpu-binding.v1"
	canonicalPayloadTag  = "dpa.HostSnapshotV1.canonical.v1"
)

// Per-frame payload bounds (GFO-095).
const (
	maxPayloadAssets       = 4096
	maxPayloadEdges        = 8192
	maxPayloadObservations = 32768
	maxPayloadBindings     = 256
	maxAssetAliases        = 64
	maxDimensions          = 64
	maxCanonicalBytes      = 16 << 20
	maxDiagnostics         = 256
)

// gpuBinding is one NVIDIA UUID bound to a GPU function of the frame.
type gpuBinding struct {
	uuid, bdf, evidenceID string
}

// payload is one frame's HostSnapshotV1 content, every collection sorted.
type payload struct {
	assets       []model.AssetRef
	edges        []hop
	evidenceIDs  []string // edgeEvidence, one per edge, by edge index
	observations []model.Observation
	bindings     []gpuBinding
}

// frameContext is what every ID and timestamp of one frame is derived from.
type frameContext struct {
	session  int64
	sequence uint64
	times    frameTimes
}

// deterministicID renders <prefix>:<session>:<sequence>:<H>, where H is the
// first 16 bytes, in lowercase hex, of the SHA-256 of the length-prefixed
// parts.
func (fc frameContext) deterministicID(prefix string, parts ...string) string {
	h := sha256.New()
	var n [4]byte
	for _, p := range parts {
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	return prefix + ":" + strconv.FormatInt(fc.session, 10) + ":" + strconv.FormatUint(fc.sequence, 10) + ":" + hex.EncodeToString(sum[:16])
}

// buildPayload assembles and sorts one frame's payload.
func buildPayload(fc frameContext, t *topology, classes map[string]uint32, selected []string, pairs []widthPair, inventory []GPUInventoryEntry) (payload, error) {
	var p payload
	for _, a := range t.assets {
		p.assets = append(p.assets, a)
	}
	slices.SortFunc(p.assets, func(x, y model.AssetRef) int { return strings.Compare(x.Key(), y.Key()) })

	for _, h := range t.hops {
		p.edges = append(p.edges, h)
	}
	slices.SortFunc(p.edges, compareHops)
	relation, origin := model.RelLocatedIn.String(), model.OriginObserved.String()
	for _, e := range p.edges {
		p.evidenceIDs = append(p.evidenceIDs,
			fc.deterministicID(idPrefixEdgeEvidence, idTagEdgeEvidence, e.from.Key(), relation, e.to.Key(), origin))
	}

	for _, f := range selected {
		o, err := fc.observation(parentSource, pciAsset(model.KindPCIeFunction, f), signalClassCode,
			int64(classes[f]), unitPCIClass, map[string]string{})
		if err != nil {
			return payload{}, err
		}
		p.observations = append(p.observations, o)
	}
	for _, w := range pairs {
		dims := func() map[string]string {
			return map[string]string{
				pcie.DimensionRootCanonical:      w.rootPort.Canonical,
				pcie.DimensionPeerCanonical:      w.peer.Canonical,
				pcie.DimensionPeerKind:           w.peer.Kind.String(),
				pcie.DimensionExpectedProvenance: w.provenance,
			}
		}
		cur, err := fc.observation(widthSource, w.function, pcie.SignalLinkWidthCurrent, w.current, pcie.UnitLinkWidth, dims())
		if err != nil {
			return payload{}, err
		}
		exp, err := fc.observation(widthSource, w.function, pcie.SignalLinkWidthExpected, w.expected, pcie.UnitLinkWidth, dims())
		if err != nil {
			return payload{}, err
		}
		p.observations = append(p.observations, cur, exp)
	}
	slices.SortFunc(p.observations, compareObservations)

	for _, e := range inventory {
		p.bindings = append(p.bindings, gpuBinding{
			uuid:       e.UUID,
			bdf:        e.BDF,
			evidenceID: fc.deterministicID(idPrefixGPUBinding, idTagGPUBinding, e.UUID, e.BDF),
		})
	}
	slices.SortFunc(p.bindings, func(x, y gpuBinding) int {
		if c := strings.Compare(x.bdf, y.bdf); c != 0 {
			return c
		}
		return strings.Compare(x.uuid, y.uuid)
	})
	return p, p.checkBounds()
}

// observation builds one observation of the frame and holds it to the model's
// own validation.
func (fc frameContext) observation(src model.SourceRef, subject model.AssetRef, signal model.SignalRef, value int64, unit string, dims map[string]string) (model.Observation, error) {
	o := model.Observation{
		ID:         fc.deterministicID(idPrefixObservation, idTagObservation, subject.Key(), string(signal)),
		Source:     src,
		Subject:    subject,
		Signal:     signal,
		Value:      model.NewIntValue(value),
		Unit:       unit,
		Dimensions: dims,
		ObservedAt: fc.times.observedAt,
		ReceivedAt: fc.times.observedAt,
		ExpiresAt:  fc.times.expiresAt,
		Sequence:   fc.sequence,
		Quality:    model.QualityGood,
	}
	if err := o.Validate(); err != nil {
		return model.Observation{}, fmt.Errorf("agent: building observation: %w", err)
	}
	return o, nil
}

// checkBounds applies the per-frame payload bounds; exceeding one is fatal to
// the whole run rather than a reason to truncate.
func (p payload) checkBounds() error {
	switch {
	case len(p.assets) > maxPayloadAssets:
		return fmt.Errorf("%w: %d assets in one frame", ErrBoundExceeded, len(p.assets))
	case len(p.edges) > maxPayloadEdges:
		return fmt.Errorf("%w: %d edges in one frame", ErrBoundExceeded, len(p.edges))
	case len(p.observations) > maxPayloadObservations:
		return fmt.Errorf("%w: %d observations in one frame", ErrBoundExceeded, len(p.observations))
	case len(p.bindings) > maxPayloadBindings:
		return fmt.Errorf("%w: %d GPU bindings in one frame", ErrBoundExceeded, len(p.bindings))
	}
	for _, a := range p.assets {
		if len(a.Aliases) > maxAssetAliases {
			return fmt.Errorf("%w: asset aliases", ErrBoundExceeded)
		}
	}
	for _, o := range p.observations {
		if len(o.Subject.Aliases) > maxAssetAliases || len(o.Dimensions) > maxDimensions {
			return fmt.Errorf("%w: observation aliases or dimensions", ErrBoundExceeded)
		}
	}
	return nil
}

// compareHops is the graph edge order: From key, To key, relation, origin.
// Relation and origin are the same for every hop.
func compareHops(x, y hop) int {
	if c := strings.Compare(x.from.Key(), y.from.Key()); c != 0 {
		return c
	}
	return strings.Compare(x.to.Key(), y.to.Key())
}

// compareObservations is the artifact observation order (GFO-085): subject
// key, signal, source type, source name, dimensions, observedAt, ID.
func compareObservations(x, y model.Observation) int {
	if c := strings.Compare(x.Subject.Key(), y.Subject.Key()); c != 0 {
		return c
	}
	if c := strings.Compare(string(x.Signal), string(y.Signal)); c != 0 {
		return c
	}
	if c := strings.Compare(x.Source.Type, y.Source.Type); c != 0 {
		return c
	}
	if c := strings.Compare(x.Source.Name, y.Source.Name); c != 0 {
		return c
	}
	if c := compareDimensions(x.Dimensions, y.Dimensions); c != 0 {
		return c
	}
	if c := x.ObservedAt.Compare(y.ObservedAt); c != 0 {
		return c
	}
	return strings.Compare(x.ID, y.ID)
}

// compareDimensions compares the key-ordered (key, value) sequences
// lexicographically, key before value, a proper prefix first.
func compareDimensions(x, y map[string]string) int {
	xk, yk := sortedKeys(x), sortedKeys(y)
	for i := 0; i < len(xk) && i < len(yk); i++ {
		if c := strings.Compare(xk[i], yk[i]); c != 0 {
			return c
		}
		if c := strings.Compare(x[xk[i]], y[yk[i]]); c != 0 {
			return c
		}
	}
	return len(xk) - len(yk)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// canonicalBytes is the format-independent payload encoding the digest is
// computed over (GFO-086).
type canonicalBytes struct {
	b []byte
}

func (c *canonicalBytes) str(s string) {
	c.b = binary.BigEndian.AppendUint32(c.b, uint32(len(s)))
	c.b = append(c.b, s...)
}

func (c *canonicalBytes) u32(v uint32) { c.b = binary.BigEndian.AppendUint32(c.b, v) }
func (c *canonicalBytes) u64(v uint64) { c.b = binary.BigEndian.AppendUint64(c.b, v) }

// timestamp writes i64be(floor Unix seconds) followed by u32be(nanoseconds).
func (c *canonicalBytes) timestamp(t time.Time) {
	c.u64(uint64(t.Unix()))
	c.u32(uint32(t.Nanosecond()))
}

// optionalTimestamp writes 0x00 for the zero time and 0x01 then the time
// otherwise.
func (c *canonicalBytes) optionalTimestamp(t time.Time) {
	if t.IsZero() {
		c.b = append(c.b, 0)
		return
	}
	c.b = append(c.b, 1)
	c.timestamp(t)
}

func (c *canonicalBytes) asset(a model.AssetRef) {
	c.str(a.Kind.String())
	c.str(a.Canonical)
	c.u32(uint32(len(a.Aliases)))
	for _, alias := range a.Aliases {
		c.str(alias.Namespace)
		c.str(alias.Value)
	}
}

// digest returns the lowercase hex SHA-256 of the payload's canonical bytes,
// or ErrBoundExceeded when those bytes exceed 16 MiB.
func (p payload) digest(fc frameContext) (string, error) {
	var c canonicalBytes
	c.str(canonicalPayloadTag)

	c.u32(uint32(len(p.assets)))
	for _, a := range p.assets {
		c.asset(a)
	}

	relation, origin := model.RelLocatedIn.String(), model.OriginObserved.String()
	c.u32(uint32(len(p.edges)))
	for _, e := range p.edges {
		c.str(e.from.Key())
		c.str(relation)
		c.str(e.to.Key())
		c.str(origin)
	}

	c.u32(uint32(len(p.edges)))
	for i := range p.edges {
		c.u32(uint32(i))
		c.str(fleet.EdgeEvidenceObserved.String())
		c.str(parentSource.Type)
		c.str(parentSource.Name)
		c.timestamp(fc.times.observedAt)
		c.timestamp(fc.times.expiresAt)
		c.str(p.evidenceIDs[i])
	}

	c.u32(uint32(len(p.observations)))
	for _, o := range p.observations {
		c.str(o.ID)
		c.str(o.Source.Type)
		c.str(o.Source.Name)
		c.asset(o.Subject)
		c.str(string(o.Signal))
		v, _ := o.Value.Int()
		c.str(o.Value.Kind().String())
		c.u64(uint64(v))
		c.str(o.Unit)
		keys := sortedKeys(o.Dimensions)
		c.u32(uint32(len(keys)))
		for _, k := range keys {
			c.str(k)
			c.str(o.Dimensions[k])
		}
		c.timestamp(o.ObservedAt)
		c.timestamp(o.ReceivedAt)
		c.optionalTimestamp(o.ExpiresAt)
		c.u64(o.Sequence)
		c.str(o.Quality.String())
		c.str(o.RawDigest)
	}

	c.u32(uint32(len(p.bindings)))
	for _, b := range p.bindings {
		c.str(b.uuid)
		c.str("")
		c.str(b.bdf)
		c.str(nvidiaSource.Type)
		c.str(nvidiaSource.Name)
		c.str(b.evidenceID)
		c.timestamp(fc.times.observedAt)
		c.timestamp(fc.times.expiresAt)
	}

	// No allocation batch.
	c.b = append(c.b, 0)

	if len(c.b) > maxCanonicalBytes {
		return "", fmt.Errorf("%w: frame canonical payload of %d bytes", ErrBoundExceeded, len(c.b))
	}
	sum := sha256.Sum256(c.b)
	return hex.EncodeToString(sum[:]), nil
}
