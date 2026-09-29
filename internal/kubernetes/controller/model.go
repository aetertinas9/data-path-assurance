package controller

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// maxFleetNodes is the largest number of Nodes one fleet may select; a fleet
// selecting more is not evaluated at all (GKA-063).
const maxFleetNodes = 1024

// Message classes of an "internal error: <class>" token (GKA-084).
const (
	classAssessorFailed     = "assessor failed"
	classInvalidAssessment  = "invalid assessment"
	classInvalidIntent      = "invalid intent or policy"
	classInvalidSelector    = "invalid nodeSelector"
	classStatusWriteRefused = "status write rejected"
)

// deviceScope is the scope a GPUDevice is rendered in (GKA-062).
type deviceScope int

const (
	deviceAssessable deviceScope = iota
	deviceNoMatching
	deviceUIDMismatch
	deviceIdentityConflict
	deviceInternalError
	deviceConflict
	deviceCapacity
)

// nodeScope is the scope a Node is rendered in (GKA-067).
type nodeScope int

const (
	nodeNotSelected nodeScope = iota
	nodeHeld
	nodeConflict
	nodeCapacity
	nodeSelected
)

// fleetState is one GPUFleet as a pass sees it.
type fleetState struct {
	obj        *v1alpha1.GPUFleet
	validSel   bool
	selected   []*nodeState
	overCap    bool
	policy     fleet.Policy
	policyErr  error
	enforce    bool
	anyDevices bool // union of D(N,F) over the selected nodes is not empty
}

// deviceState is one GPUDevice as a pass sees it.
type deviceState struct {
	obj              *v1alpha1.GPUDevice
	scope            deviceScope
	class            string // internal error class when scope is deviceInternalError
	peers            []string
	fleet            *fleetState
	node             *nodeState
	intentObservedAt time.Time
	noObservation    bool
	proj             *deviceProjection // set when the assessor decided this device
}

// nodeState is one Node as a pass sees it.
type nodeState struct {
	obj        *corev1.Node
	fleets     []*fleetState // S(N)
	scope      nodeScope
	sel        fleet.SelectionState // for nodeSelected
	holdFleets []string             // invalid fleet names for nodeHeld
	d          []*deviceState       // D(N,F) of the single fleet (nodeSelected, nodeCapacity)
	dUnion     []*deviceState       // union of D over S(N) (nodeConflict)
	dStar      []*deviceState
	xStar      int
	existing   *v1alpha1.NodePathState
	// assessment results
	assessed bool
	eval     nodeEval
}

// nodeEval is the node verdict NE (GKA-076) and the values derived from it.
type nodeEval struct {
	eligibility   fleet.Eligibility
	qualification fleet.Qualification
	reason        string
	class         string // internal error class ("" when none)
	noObservation bool
	obs           *app.NodeObservation
	decision      *fleet.NodeDecision // validated, may be nil
}

func newNodeEval(reason string) nodeEval {
	return nodeEval{eligibility: fleet.EligibilityUnknown, qualification: fleet.QualificationUnknown, reason: reason}
}

// world is the classification of one pass.
type world struct {
	now       time.Time
	fleets    []*fleetState
	fleetBy   map[string]*fleetState
	nodes     []*nodeState
	nodeBy    map[string]*nodeState
	devices   []*deviceState
	devicesBy map[string][]*deviceState // by nodeRef.name
	paths     map[string]*v1alpha1.NodePathState
}
