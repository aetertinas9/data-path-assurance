package app

// Explanation is the offline explanation model. Its fields follow the
// explain response field order; every string is already rendered (enum
// names, timestamps, decimal numbers). A field that the response omits is
// either a nil pointer or an Optional that is not Set.
type Explanation struct {
	Kind TargetKind

	TargetName, TargetUID string
	NodeName, NodeUID     string
	// Identity is set for a GPU explanation only.
	Identity      *Identity
	GraphRevision Optional
	// Phase is the device lifecycle phase; GPU explanation only.
	Phase           string
	Qualification   string
	NodeEligibility string
	PathSegments    []PathSegment
	ActiveFindings  []FindingView
	Coverage        []CoverageView
	// Allocation is set for a GPU explanation only.
	Allocation             *AllocationView
	AffectedWorkloads      []WorkloadView
	ObservedAt             string
	EvaluatedAt            string
	Truncated              bool
	TotalCounts            TotalCounts
	Limitations            []Limitation
	NodeAssessmentRevision string
	DeviceSummaries        []DeviceSummary
	// Reason is the device decision reason (GPU explanation).
	Reason string
	// EligibilityReason is the node decision reason (node explanation).
	EligibilityReason string
}

// Optional is a string field that may be omitted.
type Optional struct {
	Value string
	Set   bool
}

func some(v string) Optional { return Optional{Value: v, Set: true} }

// SourceView names a collector source.
type SourceView struct {
	Type, Name string
}

// Identity is the actual binding of a GPU device.
type Identity struct {
	State, Reason                                           string
	Vendor, UUID, Serial, NodeUID, BootID, BDF, FunctionKey Optional
	Source                                                  *SourceView
	EvidenceID, ObservedAt, ExpiresAt                       Optional
}

// PathSegment is one trusted hop of the result frame topology with its
// provenance.
type PathSegment struct {
	From, Relation, To, Origin, Kind  string
	Source                            SourceView
	EvidenceID, ObservedAt, ExpiresAt string

	depth int
}

// FindingView is one active finding.
type FindingView struct {
	ID, Type, Severity, State, Confidence string
	Scope                                 []string
	Evidence                              []EvidenceView
	MissingInputs                         []string
	Affected                              []ImpactView
	FirstSeen, LastSeen                   string
	Explanation, SuggestedStep            string
}

// EvidenceView cites one observation of a finding.
type EvidenceView struct {
	ObservationID, Summary string
}

// ImpactView names an affected asset.
type ImpactView struct {
	Asset, Accuracy string
}

// CoverageView is one coverage entry. Device is set in a node explanation.
type CoverageView struct {
	Device                                  Optional
	Name, PathKind                          string
	Required                                bool
	State, Reason                           string
	EvidenceRefs                            []string
	ObservedAt, LatestObservedAt, ExpiresAt Optional

	deviceUID string
}

// AllocationView is the allocation summary of a GPU device.
type AllocationView struct {
	State, Reason                  string
	Profile, ObservedAt, ExpiresAt Optional
	EvidenceRefs                   []string
	AffectedWorkloads              []WorkloadView
}

// WorkloadView names a workload using a device.
type WorkloadView struct {
	Namespace, Name, UID, Container, CreatedAt string
	DeletedAt                                  Optional
}

// TotalCounts are the list lengths before truncation.
type TotalCounts struct {
	PathSegments, ActiveFindings, Coverage, AffectedWorkloads, DeviceSummaries, EvidenceRefs, Limitations int
}

// Limitation is one limitation code with its optional subject.
type Limitation struct {
	Code    string
	Subject Optional
}

// DeviceSummary summarizes one device of a node explanation.
type DeviceSummary struct {
	Name, UID, DesiredState, ObservedGeneration, Qualification, LifecyclePhase, Reason string
}
