package model

import "time"

// PartitionKey groups work that must be processed together. How a key is
// derived from the graph is decided elsewhere; here it is only required to say
// something.
type PartitionKey string

// String returns the key as written.
func (p PartitionKey) String() string {
	return string(p)
}

// IsValid reports whether the key is non-empty.
func (p PartitionKey) IsValid() bool {
	return string(p) != ""
}

// EdgeRelation names how one asset stands to another in the physical graph. The
// zero value is not a relation.
type EdgeRelation int

// The edge relations v0.1 models.
const (
	RelLocatedIn EdgeRelation = iota + 1
	RelConnectedTo
	RelUpstreamOf
	RelDownstreamOf
	RelMemberOf
	RelAllocatedTo
	RelObservedBy
	RelIntendedToConnect
	RelSharesFailureDomainWith
)

var edgeRelationNames = []string{
	"LOCATED_IN",
	"CONNECTED_TO",
	"UPSTREAM_OF",
	"DOWNSTREAM_OF",
	"MEMBER_OF",
	"ALLOCATED_TO",
	"OBSERVED_BY",
	"INTENDED_TO_CONNECT",
	"SHARES_FAILURE_DOMAIN_WITH",
}

// String returns the relation's wire name, e.g. "CONNECTED_TO".
func (r EdgeRelation) String() string {
	return enumName("EdgeRelation", int(r), int(RelLocatedIn), edgeRelationNames)
}

// IsValid reports whether r is one of the enumerated relations. The zero value
// is not.
func (r EdgeRelation) IsValid() bool {
	return enumInRange(int(r), int(RelLocatedIn), len(edgeRelationNames))
}

// EdgeOrigin says where an edge came from: something the system saw, something
// an intent document declared, or something the system worked out. The zero
// value is not an origin.
type EdgeOrigin int

// The edge origins.
const (
	OriginObserved EdgeOrigin = iota + 1
	OriginIntended
	OriginDerived
)

var edgeOriginNames = []string{
	"Observed",
	"Intended",
	"Derived",
}

// String returns the origin's name, e.g. "Intended".
func (o EdgeOrigin) String() string {
	return enumName("EdgeOrigin", int(o), int(OriginObserved), edgeOriginNames)
}

// IsValid reports whether o is one of the enumerated origins. The zero value is
// not.
func (o EdgeOrigin) IsValid() bool {
	return enumInRange(int(o), int(OriginObserved), len(edgeOriginNames))
}

// Sample is one timestamped numeric reading with its labels, as collected from
// a metrics source.
type Sample struct {
	Timestamp time.Time
	Value     float64
	Labels    map[string]string
}

// Validate reports whether the sample is timestamped.
func (s Sample) Validate() error {
	if s.Timestamp.IsZero() {
		return invalidf("Sample.Timestamp is the zero time")
	}
	return nil
}

// ConditionStatus is the three-valued answer a condition carries. The zero
// value is not a status.
type ConditionStatus int

// The condition statuses.
const (
	ConditionTrue ConditionStatus = iota + 1
	ConditionFalse
	ConditionUnknown
)

var conditionStatusNames = []string{
	"True",
	"False",
	"Unknown",
}

// String returns the status's name, e.g. "Unknown".
func (s ConditionStatus) String() string {
	return enumName("ConditionStatus", int(s), int(ConditionTrue), conditionStatusNames)
}

// IsValid reports whether s is one of the enumerated statuses. The zero value
// is not.
func (s ConditionStatus) IsValid() bool {
	return enumInRange(int(s), int(ConditionTrue), len(conditionStatusNames))
}

// Condition reports one named aspect of an asset's state, why it stands that
// way, and when it last changed.
//
// Any domain prefix convention on Type is the reporting adapter's business; the
// model does not impose one.
type Condition struct {
	Type               string
	Status             ConditionStatus
	Reason             string
	Message            string
	LastTransitionTime time.Time
}

// Validate reports whether the condition names itself, carries a valid status
// and a reason, and records when it last changed. An empty Message is fine.
func (c Condition) Validate() error {
	if c.Type == "" {
		return invalidf("Condition.Type is empty")
	}
	if !c.Status.IsValid() {
		return invalidf("Condition.Status %s is not a condition status", c.Status)
	}
	if c.Reason == "" {
		return invalidf("Condition.Reason is empty")
	}
	if c.LastTransitionTime.IsZero() {
		return invalidf("Condition.LastTransitionTime is the zero time")
	}
	return nil
}
