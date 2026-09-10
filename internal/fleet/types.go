package fleet

import (
	"errors"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

var ErrInvalidInput = errors.New("fleet: invalid input")
var ErrConflict = errors.New("fleet: conflict")

type NodeRef struct{ ClusterID, Name, UID string }
type InventoryClaim struct{ Vendor, UUID, Serial, Source, EvidenceID string }
type ObservedBinding struct {
	Node                                           NodeRef
	BootID, BDF                                    string
	Function                                       model.AssetRef
	Claim                                          InventoryClaim
	Source                                         model.SourceRef
	EvidenceID, BundleRevision, CollectorProfileID string
	ObservedAt, ExpiresAt                          time.Time
}
type SnapshotCursor struct {
	NodeUID, BootID, PayloadDigest, BundleRevision string
	Session                                        int64
	Sequence                                       uint64
	Completeness                                   SnapshotCompleteness
	ObservedAt                                     time.Time
	Baseline                                       bool
}
type SnapshotEnvelope struct {
	NodeUID, BootID, PayloadDigest, BundleRevision string
	Session                                        int64
	Sequence                                       uint64
	Completeness                                   SnapshotCompleteness
	ObservedAt                                     time.Time
}
type DeviceRef struct{ Name, UID string }
type EdgeProvenance struct {
	Edge                                           graph.Edge
	EvidenceID, BundleRevision, CollectorProfileID string
	Kind                                           EdgeEvidenceKind
	Source                                         model.SourceRef
	ObservedAt, ExpiresAt                          time.Time
}
type CoverageRequirement struct {
	Name     string
	PathKind string
	Required bool
}
type CoverageAssessment struct {
	Name, PathKind                          string
	State                                   CoverageState
	EvidenceIDs                             []string
	Reason                                  string
	ObservedAt, LatestObservedAt, ExpiresAt time.Time
	AssessmentSequence                      uint64
}
type TrustedSource struct {
	Capability TrustCapability
	Source     model.SourceRef
}
type CollectorTrustProfile struct {
	ID                 string
	Mode               TrustMode
	ClusterID, NodeUID string
	Session            int64
	Sources            []TrustedSource
}
type FenceTrustProfile struct {
	ID, ClusterID, Issuer string
	Source                model.SourceRef
}
type WorkloadRef struct {
	Namespace, Name, UID, Container string
	CreatedAt, DeletedAt            time.Time
}
type AllocationEntry struct {
	ResourceName, DeviceID string
	Workload               WorkloadRef
}
type AllocationBatch struct {
	NodeUID, BootID, BundleRevision, EvidenceDigest string
	Session                                         int64
	Sequence                                        uint64
	ObservedAt, ExpiresAt                           time.Time
	Complete                                        bool
	EvidenceRefs                                    []string
	Profile                                         AllocationProfile
	Entries                                         []AllocationEntry
	CollectorProfileID                              string
}
type FenceAcknowledgement struct {
	Node                  NodeRef
	DeviceUID, BootID     string
	Session               int64
	RequestID, EvidenceID string
	Source                model.SourceRef
	MetadataGeneration    int64
	State                 FenceState
	ObservedAt, ExpiresAt time.Time
	TrustProfileID        string
}
type Policy struct {
	Revision            string
	RequiredCoverage    []CoverageRequirement
	Freshness, ReadyFor time.Duration
}
type Intent struct {
	Device             DeviceRef
	Node               NodeRef
	Desired            DesiredState
	RequestID          string
	MetadataGeneration int64
	ObservedAt         time.Time
	Claim              InventoryClaim
}
type AssessmentBundle struct {
	Policy                                                        Policy
	Intent                                                        Intent
	Snapshot                                                      SnapshotEnvelope
	Admitted                                                      SnapshotCursor
	GraphRevision, WindowRevision, TopologyDigest, BaselineDigest string
	Topology                                                      *graph.Snapshot
	Window                                                        evidence.Window
	Provenance                                                    []EdgeProvenance
	Bindings                                                      []ObservedBinding
	Findings                                                      []model.Finding
	FindingsEvaluatedAt                                           time.Time
	FindingsGraphRevision                                         string
	Allocation                                                    *AllocationBatch
	Fence                                                         *FenceAcknowledgement
	CollectorTrust                                                CollectorTrustProfile
	FenceTrust                                                    *FenceTrustProfile
}
type CoverageCursor struct {
	Name               string
	ObservedAt         time.Time
	AssessmentSequence uint64
	EvidenceDigest     string
}
type DeviceDecision struct {
	Desired                                                  DesiredState
	Phase                                                    LifecyclePhase
	Qualification                                            Qualification
	BindingState                                             BindingState
	Binding                                                  ObservedBinding
	Allocation                                               AllocationState
	AcceptedNormalPoint                                      bool
	GraphRevision                                            string
	Coverage                                                 []CoverageAssessment
	EvidenceIDs, FindingIDs                                  []string
	Reason, Message                                          string
	EvaluatedAt, ValidUntil                                  time.Time
	PolicyRevision, RequestID, DeviceUID, NodeUID, BootID    string
	BindingKey, TopologyDigest, BaselineDigest               string
	MetadataGeneration                                       int64
	Session                                                  int64
	IntentObservedAt                                         time.Time
	ReadyWindowStartedAt, LastCompositeMin, LastCompositeMax time.Time
	CoverageCursors                                          []CoverageCursor
}
type DeviceAggregate struct {
	DeviceUID, NodeUID string
	Desired            DesiredState
	MetadataGeneration int64
	Decision           DeviceDecision
}
type AggregateNodeInput struct {
	Node      NodeRef
	FleetUID  string
	Selection SelectionState
	Devices   []DeviceAggregate
}
type NodeDecision struct {
	NodeUID, FleetUID, Reason, AssessmentRevision string
	Qualification                                 Qualification
	Eligibility                                   Eligibility
	Selection                                     SelectionState
	DeviceCount                                   int
	NormalPointAt                                 time.Time
	NormalPointDigest                             string
	EvaluatedAt, ValidUntil                       time.Time
}
type CleanupPolicySnapshot struct {
	PolicyVersion       string
	RequiredCoverage    []CoverageRequirement
	Freshness, ReadyFor time.Duration
}
type GateOwnership struct {
	OwnerFleetUID, NodeUID, Key, Value, Effect                 string
	Policy                                                     CleanupPolicySnapshot
	Phase                                                      CleanupPhase
	CleanupRequestedAt, RecoveryStartedAt, LastRecoveryPointAt time.Time
	RecoveryControllerID, LastRecoveryDigest                   string
}
type GateInput struct {
	Mode                   GateMode
	FleetUID, ControllerID string
	Node                   NodeRef
	FleetConflict          bool
	Policy                 CleanupPolicySnapshot
	NodeDecision           NodeDecision
	Ownership              GateOwnership
}
type GateDecision struct {
	Action      GateAction
	Ownership   GateOwnership
	Reason      string
	EvaluatedAt time.Time
}
