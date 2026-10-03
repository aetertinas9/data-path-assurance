package liveingest

import "time"

// WireVersion is the value of ClientHello.version.
const WireVersion = "v1alpha1"

// The raw values of the wire enums, mirrored from ingest.proto. The neutral
// types carry enum fields as raw int32 so that a value this package does not
// know stays representable and is rejected by the checks instead of being
// folded into a known one.
const (
	// CompletenessUnspecified is the zero value of the wire enum Completeness.
	CompletenessUnspecified int32 = 0
	// CompletenessComplete is Completeness.COMPLETE.
	CompletenessComplete int32 = 1
	// CompletenessPartial is Completeness.PARTIAL.
	CompletenessPartial int32 = 2

	// AllocationProfileUnspecified is the zero value of the wire enum
	// AllocationProfile.
	AllocationProfileUnspecified int32 = 0
	// AllocationProfileNVIDIAPodResourcesUUID is
	// AllocationProfile.NVIDIA_PODRESOURCES_UUID.
	AllocationProfileNVIDIAPodResourcesUUID int32 = 1
	// AllocationProfileUnsupported is AllocationProfile.UNSUPPORTED.
	AllocationProfileUnsupported int32 = 2
)

// Timestamp is a wire timestamp as the message carries it: whether it is
// present at all, whole seconds since the Unix epoch and nanoseconds. It is
// kept raw so that converting a protobuf Timestamp to a Timestamp and back
// loses nothing; Valid and Time give the checked meaning.
type Timestamp struct {
	Present bool
	Seconds int64
	Nanos   int32
}

// The valid range of a wire timestamp: 0001-01-01T00:00:00Z through
// 9999-12-31T23:59:59Z in seconds, nanoseconds in [0, 1e9).
const (
	minTimestampSeconds int64 = -62135596800
	maxTimestampSeconds int64 = 253402300799
)

// TimestampOf returns the present timestamp of t. The zero Timestamp is the
// absent one.
func TimestampOf(t time.Time) Timestamp {
	return Timestamp{Present: true, Seconds: t.Unix(), Nanos: int32(t.Nanosecond())}
}

// Valid reports whether the timestamp is present, in the protobuf timestamp
// range and not the zero instant (0001-01-01T00:00:00Z with no nanoseconds).
// Together these are the instants the contract accepts: UTC
// 0001-01-01T00:00:00.000000001Z through 9999-12-31T23:59:59.999999999Z.
func (t Timestamp) Valid() bool {
	if !t.Present || t.Nanos < 0 || t.Nanos >= 1_000_000_000 {
		return false
	}
	if t.Seconds < minTimestampSeconds || t.Seconds > maxTimestampSeconds {
		return false
	}
	return t.Seconds != minTimestampSeconds || t.Nanos != 0
}

// Time returns the instant of the timestamp in UTC. The result is meaningful
// only when Valid reports true.
func (t Timestamp) Time() time.Time {
	return time.Unix(t.Seconds, int64(t.Nanos)).UTC()
}

// compare orders two timestamps as instants.
func (t Timestamp) compare(u Timestamp) int {
	switch {
	case t.Seconds < u.Seconds:
		return -1
	case t.Seconds > u.Seconds:
		return 1
	case t.Nanos < u.Nanos:
		return -1
	case t.Nanos > u.Nanos:
		return 1
	}
	return 0
}

// Frame is one SnapshotFrame of the wire, converted without interpretation.
type Frame struct {
	NodeUID, BootID string
	Session         int64
	Sequence        uint64
	// Completeness is the raw wire enum value (CompletenessComplete and
	// CompletenessPartial are the valid ones).
	Completeness   int32
	ObservedAt     Timestamp
	Payload        *Payload
	PayloadDigest  []byte
	BundleRevision string
	// UnknownFields reports that the ClientFrame or SnapshotFrame message
	// carries unknown fields outside the payload.
	UnknownFields bool
}

// Payload is a HostSnapshotV1 of the wire.
type Payload struct {
	Assets       []Asset
	Edges        []Edge
	EdgeEvidence []EdgeEvidence
	Observations []Observation
	GPUBindings  []GPUBinding
	// Allocation is nil when the message has no allocation batch.
	Allocation *AllocationBatch
	// UnknownFields reports that the payload message, or any message nested in
	// it (timestamps and the allocation batch included), carries unknown
	// fields.
	UnknownFields bool
}

// Alias is an additional identifier of an asset.
type Alias struct{ Namespace, Value string }

// Asset is a reference to an asset: the kind name, the canonical identifier
// (<namespace>:<value>) and the aliases.
type Asset struct {
	Kind, Canonical string
	Aliases         []Alias
}

// Key renders the asset as <kind>/<canonical>, the form edges use to name
// their endpoints.
func (a Asset) Key() string { return a.Kind + "/" + a.Canonical }

// Edge is a relation between two assets named by key.
type Edge struct{ FromKey, Relation, ToKey, Origin string }

// EdgeEvidence is the provenance of the edge at EdgeIndex.
type EdgeEvidence struct {
	EdgeIndex              uint32
	Kind                   string
	SourceType, SourceName string
	ObservedAt, ExpiresAt  Timestamp
	EvidenceID             string
}

// SourceRef names where an observation came from.
type SourceRef struct{ Type, Name string }

// ValueKind selects the alternative a Value holds.
type ValueKind uint8

// The alternatives of a Value. ValueNone means that no alternative is set,
// which is invalid.
const (
	ValueNone ValueKind = iota
	ValueInt
	ValueFloat
	ValueBool
	ValueString
)

// Value is the tagged union of an observation value: Kind says which of the
// other fields is meaningful.
type Value struct {
	Kind  ValueKind
	Int   int64
	Float float64
	Bool  bool
	Str   string
}

// Dimension is one key/value dimension of an observation.
type Dimension struct{ Key, Value string }

// Observation is one measurement. Dimensions are in key order.
type Observation struct {
	ID         string
	Source     SourceRef
	Subject    Asset
	Signal     string
	Value      Value
	Unit       string
	Dimensions []Dimension
	ObservedAt Timestamp
	ReceivedAt Timestamp
	// ExpiresAt is the only optional timestamp: absent means the observation
	// does not expire.
	ExpiresAt Timestamp
	Sequence  uint64
	Quality   string
	RawDigest string
}

// GPUBinding binds an NVIDIA GPU UUID to a PCIe function.
type GPUBinding struct {
	UUID, Serial, BDF      string
	SourceType, SourceName string
	EvidenceID             string
	ObservedAt, ExpiresAt  Timestamp
}

// AllocationEntry is one device allocation of an allocation batch.
type AllocationEntry struct {
	ResourceName, DeviceID, PodNamespace, PodName, ContainerName string
}

// AllocationBatch is the allocation evidence of a frame. This package decodes,
// validates and digests it; evaluating it is not part of live ingest.
type AllocationBatch struct {
	NodeUID, BootID       string
	Session               int64
	Sequence              uint64
	ObservedAt, ExpiresAt Timestamp
	Complete              bool
	EvidenceRefs          []string
	// Profile is the raw wire enum value (AllocationProfileNVIDIAPodResourcesUUID
	// and AllocationProfileUnsupported are the valid ones).
	Profile int32
	Entries []AllocationEntry
}
