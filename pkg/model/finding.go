package model

import "time"

// FindingType names what a finding claims. The zero value is not a type.
type FindingType int

// The finding types v0.1 emits.
const (
	FindingPhysicalPeerMismatch FindingType = iota + 1
	FindingOpticalPathDegrading
	FindingPCIeLinkWidthDegraded
	FindingIdentityConflict
)

var findingTypeNames = []string{
	"PHYSICAL_PEER_MISMATCH",
	"OPTICAL_PATH_DEGRADING",
	"PCIE_LINK_WIDTH_DEGRADED",
	"IDENTITY_CONFLICT",
}

// String returns the type's wire name, e.g. "PCIE_LINK_WIDTH_DEGRADED".
func (t FindingType) String() string {
	return enumName("FindingType", int(t), int(FindingPhysicalPeerMismatch), findingTypeNames)
}

// IsValid reports whether t is one of the enumerated types. The zero value is
// not.
func (t FindingType) IsValid() bool {
	return enumInRange(int(t), int(FindingPhysicalPeerMismatch), len(findingTypeNames))
}

// Severity grades how much a finding matters. The zero value is not a severity.
type Severity int

// The severities.
const (
	SeverityInfo Severity = iota + 1
	SeverityWarning
	SeverityCritical
)

var severityNames = []string{
	"Info",
	"Warning",
	"Critical",
}

// String returns the severity's name, e.g. "Warning".
func (s Severity) String() string {
	return enumName("Severity", int(s), int(SeverityInfo), severityNames)
}

// IsValid reports whether s is one of the enumerated severities. The zero value
// is not.
func (s Severity) IsValid() bool {
	return enumInRange(int(s), int(SeverityInfo), len(severityNames))
}

// Confidence grades how sure the system is of a finding. The zero value is not
// a confidence.
type Confidence int

// The confidence levels.
const (
	ConfidenceLow Confidence = iota + 1
	ConfidenceMedium
	ConfidenceHigh
)

var confidenceNames = []string{
	"Low",
	"Medium",
	"High",
}

// String returns the confidence's name, e.g. "High".
func (c Confidence) String() string {
	return enumName("Confidence", int(c), int(ConfidenceLow), confidenceNames)
}

// IsValid reports whether c is one of the enumerated levels. The zero value is
// not.
func (c Confidence) IsValid() bool {
	return enumInRange(int(c), int(ConfidenceLow), len(confidenceNames))
}

// EvidenceLevel grades how directly a claim is supported: what was seen, what
// corroborated it, what was inferred, what is unknown, and what the system does
// not look at. The zero value is not a level.
type EvidenceLevel int

// The evidence levels.
const (
	LevelObserved EvidenceLevel = iota + 1
	LevelCorroborated
	LevelInferred
	LevelUnknown
	LevelOutOfScope
)

var evidenceLevelNames = []string{
	"Observed",
	"Corroborated",
	"Inferred",
	"Unknown",
	"OutOfScope",
}

// String returns the level's name, e.g. "Corroborated".
func (l EvidenceLevel) String() string {
	return enumName("EvidenceLevel", int(l), int(LevelObserved), evidenceLevelNames)
}

// IsValid reports whether l is one of the enumerated levels. The zero value is
// not.
func (l EvidenceLevel) IsValid() bool {
	return enumInRange(int(l), int(LevelObserved), len(evidenceLevelNames))
}

// FindingState is where a finding stands in its lifecycle. The zero value is
// not a state.
type FindingState int

// The finding states.
const (
	StateActive FindingState = iota + 1
	StateResolved
)

var findingStateNames = []string{
	"Active",
	"Resolved",
}

// String returns the state's name, e.g. "Active".
func (s FindingState) String() string {
	return enumName("FindingState", int(s), int(StateActive), findingStateNames)
}

// IsValid reports whether s is one of the enumerated states. The zero value is
// not.
func (s FindingState) IsValid() bool {
	return enumInRange(int(s), int(StateActive), len(findingStateNames))
}

// EvidenceRef cites one observation that supports a finding, optionally with a
// one-line human-readable summary of what it showed.
type EvidenceRef struct {
	ObservationID string
	Summary       string
}

// Validate reports whether the reference cites an observation. An empty Summary
// is fine.
func (e EvidenceRef) Validate() error {
	if e.ObservationID == "" {
		return invalidf("EvidenceRef.ObservationID is empty")
	}
	return nil
}

// ImpactAccuracy grades how well an affected asset is actually known to be
// affected. The zero value is not an accuracy.
type ImpactAccuracy int

// The impact accuracies.
const (
	ImpactConfirmed ImpactAccuracy = iota + 1
	ImpactMapped
	ImpactInferred
	ImpactUnknown
)

var impactAccuracyNames = []string{
	"Confirmed",
	"Mapped",
	"Inferred",
	"Unknown",
}

// String returns the accuracy's name, e.g. "Mapped".
func (a ImpactAccuracy) String() string {
	return enumName("ImpactAccuracy", int(a), int(ImpactConfirmed), impactAccuracyNames)
}

// IsValid reports whether a is one of the enumerated accuracies. The zero value
// is not.
func (a ImpactAccuracy) IsValid() bool {
	return enumInRange(int(a), int(ImpactConfirmed), len(impactAccuracyNames))
}

// ImpactRef names an asset a finding affects, together with how well that is
// known.
type ImpactRef struct {
	Asset    AssetRef
	Accuracy ImpactAccuracy
}

// Validate reports whether the asset reference and the accuracy are both valid.
func (i ImpactRef) Validate() error {
	if err := i.Asset.Validate(); err != nil {
		return invalidf("ImpactRef.Asset: %s", err)
	}
	if !i.Accuracy.IsValid() {
		return invalidf("ImpactRef.Accuracy %s is not an impact accuracy", i.Accuracy)
	}
	return nil
}

func (i ImpactRef) clone() ImpactRef {
	c := i
	c.Asset = i.Asset.clone()
	return c
}

// Finding is one explainable claim about the data path: what is wrong, which
// assets it is about, how sure the system is, what supports the claim, what the
// system could not see, and what to do next.
//
// A finding always says something about its own basis: either it cites evidence
// or it declares which inputs were missing. One with neither cannot exist.
type Finding struct {
	ID            string
	Type          FindingType
	Scope         []AssetRef
	Severity      Severity
	Confidence    Confidence
	State         FindingState
	Evidence      []EvidenceRef
	MissingInputs []SignalRef
	Affected      []ImpactRef
	FirstSeen     time.Time
	LastSeen      time.Time
	Explanation   string
	SuggestedStep string
}

// NewFinding validates f and returns a defensive copy of it. Mutating the
// slices reachable from f afterwards does not affect the returned finding,
// whose Validate is nil.
func NewFinding(f Finding) (Finding, error) {
	if err := f.Validate(); err != nil {
		return Finding{}, err
	}
	return f.clone(), nil
}

// Validate reports whether the finding carries an ID, an enumerated type, a
// non-empty scope of valid asset references, an enumerated severity, confidence
// and state, both timestamps in order, an explanation, valid entries in every
// collection it carries, and either evidence or a declaration of missing
// inputs.
func (f Finding) Validate() error {
	if f.ID == "" {
		return invalidf("Finding.ID is empty")
	}
	if !f.Type.IsValid() {
		return invalidf("Finding.Type %s is not a finding type", f.Type)
	}
	if len(f.Scope) == 0 {
		return invalidf("Finding.Scope is empty")
	}
	for i, asset := range f.Scope {
		if err := asset.Validate(); err != nil {
			return invalidf("Finding.Scope[%d]: %s", i, err)
		}
	}
	if !f.Severity.IsValid() {
		return invalidf("Finding.Severity %s is not a severity", f.Severity)
	}
	if !f.Confidence.IsValid() {
		return invalidf("Finding.Confidence %s is not a confidence", f.Confidence)
	}
	if !f.State.IsValid() {
		return invalidf("Finding.State %s is not a finding state", f.State)
	}
	if f.FirstSeen.IsZero() {
		return invalidf("Finding.FirstSeen is the zero time")
	}
	if f.LastSeen.IsZero() {
		return invalidf("Finding.LastSeen is the zero time")
	}
	if f.LastSeen.Before(f.FirstSeen) {
		return invalidf("Finding.LastSeen is before Finding.FirstSeen")
	}
	if f.Explanation == "" {
		return invalidf("Finding.Explanation is empty")
	}
	for i, evidence := range f.Evidence {
		if err := evidence.Validate(); err != nil {
			return invalidf("Finding.Evidence[%d]: %s", i, err)
		}
	}
	for i, signal := range f.MissingInputs {
		if !signal.IsValid() {
			return invalidf("Finding.MissingInputs[%d] %q is not a dotted lowercase signal name", i, string(signal))
		}
	}
	for i, impact := range f.Affected {
		if err := impact.Validate(); err != nil {
			return invalidf("Finding.Affected[%d]: %s", i, err)
		}
	}
	if len(f.Evidence) == 0 && len(f.MissingInputs) == 0 {
		return invalidf("Finding cites no evidence and declares no missing inputs")
	}
	return nil
}

func (f Finding) clone() Finding {
	c := f
	c.Scope = cloneAssetRefs(f.Scope)
	c.Evidence = cloneEvidenceRefs(f.Evidence)
	c.MissingInputs = cloneSignalRefs(f.MissingInputs)
	c.Affected = cloneImpactRefs(f.Affected)
	return c
}
