package model

import (
	"strings"
	"time"
)

// Well-known observation source types. The set is open.
const (
	SourceTypePrometheus = "prometheus"
	SourceTypeGNMI       = "gnmi"
	SourceTypeAgent      = "agent"
)

// SourceRef names where an observation came from: a source type and the
// concrete instance of it.
type SourceRef struct {
	Type string
	Name string
}

// Validate reports whether both parts of the reference are present.
func (s SourceRef) Validate() error {
	if s.Type == "" {
		return invalidf("SourceRef.Type is empty")
	}
	if s.Name == "" {
		return invalidf("SourceRef.Name is empty")
	}
	return nil
}

// SignalRef names what was measured, as two or more lowercase segments joined
// by dots, e.g. "fabric.port.fec.corrected_rate".
type SignalRef string

// String returns the signal name as written.
func (s SignalRef) String() string {
	return string(s)
}

// IsValid reports whether the name matches ^[a-z0-9_]+(\.[a-z0-9_]+)+$ — two or
// more non-empty segments of lowercase letters, digits and underscores, joined
// by dots.
func (s SignalRef) IsValid() bool {
	segments := strings.Split(string(s), ".")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			switch {
			case c >= 'a' && c <= 'z':
			case c >= '0' && c <= '9':
			case c == '_':
			default:
				return false
			}
		}
	}
	return true
}

// ValueKind tells which of the four kinds a Value holds. The zero value is not
// a kind.
type ValueKind int

// The kinds a Value can hold.
const (
	ValueFloat ValueKind = iota + 1
	ValueInt
	ValueBool
	ValueString
)

var valueKindNames = []string{
	"Float",
	"Int",
	"Bool",
	"String",
}

// String returns the kind's name, e.g. "Float".
func (k ValueKind) String() string {
	return enumName("ValueKind", int(k), int(ValueFloat), valueKindNames)
}

// IsValid reports whether k is one of the enumerated kinds. The zero value is
// not.
func (k ValueKind) IsValid() bool {
	return enumInRange(int(k), int(ValueFloat), len(valueKindNames))
}

// Value is an opaque measured value of one of four kinds. It is read through
// Kind and the accessors, each of which reports false when asked for a kind the
// value does not hold.
//
// The zero Value holds nothing: its Kind is not a valid kind and every accessor
// reports false.
type Value struct {
	kind    ValueKind
	float   float64
	integer int64
	boolean bool
	str     string
}

// NewFloatValue returns a Value holding v.
func NewFloatValue(v float64) Value {
	return Value{kind: ValueFloat, float: v}
}

// NewIntValue returns a Value holding v.
func NewIntValue(v int64) Value {
	return Value{kind: ValueInt, integer: v}
}

// NewBoolValue returns a Value holding v.
func NewBoolValue(v bool) Value {
	return Value{kind: ValueBool, boolean: v}
}

// NewStringValue returns a Value holding v.
func NewStringValue(v string) Value {
	return Value{kind: ValueString, str: v}
}

// Kind reports which kind the value holds.
func (v Value) Kind() ValueKind {
	return v.kind
}

// Float returns the float the value holds, or (0, false) if it holds another
// kind.
func (v Value) Float() (float64, bool) {
	if v.kind != ValueFloat {
		return 0, false
	}
	return v.float, true
}

// Int returns the integer the value holds, or (0, false) if it holds another
// kind.
func (v Value) Int() (int64, bool) {
	if v.kind != ValueInt {
		return 0, false
	}
	return v.integer, true
}

// Bool returns the boolean the value holds, or (false, false) if it holds
// another kind.
func (v Value) Bool() (bool, bool) {
	if v.kind != ValueBool {
		return false, false
	}
	return v.boolean, true
}

// Str returns the string the value holds, or ("", false) if it holds another
// kind.
func (v Value) Str() (string, bool) {
	if v.kind != ValueString {
		return "", false
	}
	return v.str, true
}

// EvidenceQuality grades how much an observation can be leaned on.
//
// Unlike every other enumeration here, its zero value is meaningful and valid:
// an adapter that says nothing about quality is saying QualityUnknown.
type EvidenceQuality int

// The evidence qualities. QualityUnknown is the zero value and is valid.
const (
	QualityUnknown EvidenceQuality = iota
	QualityGood
	QualityDegraded
)

var evidenceQualityNames = []string{
	"Unknown",
	"Good",
	"Degraded",
}

// String returns the quality's name, e.g. "Degraded".
func (q EvidenceQuality) String() string {
	return enumName("EvidenceQuality", int(q), int(QualityUnknown), evidenceQualityNames)
}

// IsValid reports whether q is one of the enumerated qualities. The zero value,
// QualityUnknown, is.
func (q EvidenceQuality) IsValid() bool {
	return enumInRange(int(q), int(QualityUnknown), len(evidenceQualityNames))
}

// Observation is one measurement of one signal about one asset: the atom
// evidence is built from.
//
// All three timestamps are supplied by the caller. No ordering between
// ObservedAt and ReceivedAt is imposed here — clock skew is the concern of the
// quality and evidence layers. ExpiresAt is optional; the zero time means the
// observation does not expire.
type Observation struct {
	ID         string
	Source     SourceRef
	Subject    AssetRef
	Signal     SignalRef
	Value      Value
	Unit       string
	Dimensions map[string]string
	ObservedAt time.Time
	ReceivedAt time.Time
	ExpiresAt  time.Time
	Sequence   uint64
	Quality    EvidenceQuality
	RawDigest  string
}

// NewObservation validates o and returns a defensive copy of it. Mutating the
// maps or slices reachable from o afterwards does not affect the returned
// observation, whose Validate is nil.
func NewObservation(o Observation) (Observation, error) {
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	return o.clone(), nil
}

// Validate reports whether the observation carries an ID, a valid source,
// subject, signal, value and quality, and both an ObservedAt and a ReceivedAt;
// and whether ExpiresAt, when set, is after ObservedAt.
func (o Observation) Validate() error {
	if o.ID == "" {
		return invalidf("Observation.ID is empty")
	}
	if err := o.Source.Validate(); err != nil {
		return invalidf("Observation.Source: %s", err)
	}
	if err := o.Subject.Validate(); err != nil {
		return invalidf("Observation.Subject: %s", err)
	}
	if !o.Signal.IsValid() {
		return invalidf("Observation.Signal %q is not a dotted lowercase signal name", string(o.Signal))
	}
	if !o.Value.Kind().IsValid() {
		return invalidf("Observation.Value holds no value")
	}
	if o.ObservedAt.IsZero() {
		return invalidf("Observation.ObservedAt is the zero time")
	}
	if o.ReceivedAt.IsZero() {
		return invalidf("Observation.ReceivedAt is the zero time")
	}
	if !o.Quality.IsValid() {
		return invalidf("Observation.Quality %s is not an evidence quality", o.Quality)
	}
	if !o.ExpiresAt.IsZero() && !o.ExpiresAt.After(o.ObservedAt) {
		return invalidf("Observation.ExpiresAt is not after Observation.ObservedAt")
	}
	return nil
}

func (o Observation) clone() Observation {
	c := o
	c.Subject = o.Subject.clone()
	c.Dimensions = cloneStringMap(o.Dimensions)
	return c
}
