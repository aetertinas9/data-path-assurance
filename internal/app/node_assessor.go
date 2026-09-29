package app

import (
	"context"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// NodeAssessor is the evaluation port the Kubernetes controller calls once per
// assessable node in a pass. It knows only domain values: the node, the fleet
// policy, the device intents and the selection state the caller computed.
type NodeAssessor interface {
	// AssessNode evaluates the devices of one node. An empty result
	// (NodeAssessment{}) means "no observation"; it is the only way the port
	// says so.
	AssessNode(ctx context.Context, req NodeAssessmentRequest) (NodeAssessment, error)
	// ForgetNode drops the continuity state of that node UID. It is idempotent
	// and safe for an unknown UID (no error).
	ForgetNode(nodeUID string)
}

// NodeAssessmentRequest is one assessment call. The caller builds it fresh for
// every call, so the assessor may change its slices freely.
type NodeAssessmentRequest struct {
	// Node has ClusterID = the controller's cluster ID and Name/UID of the Node
	// object.
	Node     fleet.NodeRef
	FleetUID string
	Policy   fleet.Policy
	// Selection is the value the Kubernetes side computed. Only
	// SelectionComplete or SelectionPartial reach the assessor.
	Selection fleet.SelectionState
	// Intents are in device UID byte order, without duplicates, at least one.
	Intents []fleet.Intent
	Now     time.Time
}

// NodeAssessment is the result of one AssessNode call.
type NodeAssessment struct {
	// Devices holds the devices that have a decision, in device UID order. It is
	// nil or empty when there is none.
	Devices []DeviceAssessment
	Node    *fleet.NodeDecision
	// Observation is nil when nothing was observed.
	Observation *NodeObservation
}

// DeviceAssessment is one device decision with the detail records its status
// projection needs.
type DeviceAssessment struct {
	Decision fleet.DeviceDecision
	// Findings details Decision.FindingIDs.
	Findings []FindingRecord
	// Evidence details Decision.EvidenceIDs.
	Evidence []EvidenceRecord
	// Allocation is required when Decision.Allocation is Empty or InUse.
	Allocation *AllocationRecord
}

// NodeObservation describes the snapshot a node assessment was computed from.
type NodeObservation struct {
	GraphRevision string
	// Completeness is Complete or Partial.
	Completeness fleet.SnapshotCompleteness
}

// FindingRecord is the projected detail of one finding.
type FindingRecord struct {
	ID, Type, Severity, State string
}

// EvidenceRecord is the projected detail of one evidence item.
type EvidenceRecord struct {
	ID, Summary           string
	ObservedAt, ExpiresAt time.Time
}

// AllocationRecord is the projected detail of a device's allocation.
type AllocationRecord struct {
	Profile               string
	ObservedAt, ExpiresAt time.Time
	EvidenceRefs          []string
	Workloads             []WorkloadRecord
}

// WorkloadRecord is one workload that holds an allocated device.
type WorkloadRecord struct {
	Namespace, Name, UID, Container string
	// CreatedAt is required; a zero DeletedAt means the workload is not deleted.
	CreatedAt, DeletedAt time.Time
}
