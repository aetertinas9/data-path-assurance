package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodePathStateSpec identifies the Node a NodePathState describes.
//
// +kubebuilder:validation:XValidation:rule="self.nodeRef == oldSelf.nodeRef",message="spec.nodeRef is immutable"
type NodePathStateSpec struct {
	// nodeRef is the described Node. It is immutable.
	NodeRef ObjectRef `json:"nodeRef"`
}

// DeviceSummary is the short result of one GPUDevice on the node.
type DeviceSummary struct {
	// name is the GPUDevice name.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// uid is the GPUDevice UID.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
	// desiredState is the lifecycle state the operator asks for.
	DesiredState DesiredState `json:"desiredState"`
	// observedGeneration is the GPUDevice generation the result is for.
	ObservedGeneration int64 `json:"observedGeneration"`
	// qualification is the path qualification verdict of the device.
	Qualification Qualification `json:"qualification"`
	// lifecyclePhase is the lifecycle phase of the device.
	LifecyclePhase LifecyclePhase `json:"lifecyclePhase"`
}

// GateOwnershipPolicy is the policy a fleet held when it took a node gate.
type GateOwnershipPolicy struct {
	// policyVersion identifies the policy.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PolicyVersion string `json:"policyVersion"`
	// requiredCoverage is the coverage list of the policy.
	//
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	RequiredCoverage []FleetCoverageRequirement `json:"requiredCoverage"`
	// freshnessSeconds is how long an observation counts as fresh.
	//
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	FreshnessSeconds int32 `json:"freshnessSeconds"`
	// readyForSeconds is how long a device must stay qualified before it is
	// ready.
	//
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	ReadyForSeconds int32 `json:"readyForSeconds"`
}

// GateOwnershipStatus records the ownership of a node gate. The shape is
// reserved for the Enforce gate; this API version does not populate it.
type GateOwnershipStatus struct {
	// ownerFleetUID is the UID of the owning GPUFleet.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OwnerFleetUID string `json:"ownerFleetUID"`
	// nodeUID is the UID of the gated Node.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	NodeUID string `json:"nodeUID"`
	// key is the taint key of the gate.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Key string `json:"key"`
	// value is the taint value of the gate.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Value string `json:"value"`
	// effect is the taint effect of the gate.
	//
	// +kubebuilder:validation:Enum=NoSchedule
	Effect string `json:"effect"`
	// policy is the policy the fleet held when it took the gate.
	Policy GateOwnershipPolicy `json:"policy"`
	// cleanupPhase is the phase of the gate cleanup.
	CleanupPhase CleanupPhase `json:"cleanupPhase"`
	// cleanupRequestedAt is when the cleanup was requested.
	CleanupRequestedAt metav1.Time `json:"cleanupRequestedAt"`
	// recoveryStartedAt is when the recovery started, if it did.
	RecoveryStartedAt *metav1.Time `json:"recoveryStartedAt,omitempty"`
	// lastRecoveryPointAt is when the last recovery point was recorded.
	LastRecoveryPointAt *metav1.Time `json:"lastRecoveryPointAt,omitempty"`
	// recoveryControllerID identifies the controller that runs the recovery.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	RecoveryControllerID *string `json:"recoveryControllerID,omitempty"`
	// lastRecoveryDigest is the digest of the last recovery point.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	LastRecoveryDigest *string `json:"lastRecoveryDigest,omitempty"`
}

// NodePathStateStatus is the observed path state of one node.
//
// +kubebuilder:validation:XValidation:rule="self.evidenceCompleteness == 'Unknown' || has(self.graphRevision)",message="graphRevision is required when evidenceCompleteness is Complete or Partial"
type NodePathStateStatus struct {
	// observedGeneration is the metadata.generation the status was computed
	// for.
	ObservedGeneration int64 `json:"observedGeneration"`
	// nodeRef is the described Node.
	NodeRef ObjectRef `json:"nodeRef"`
	// graphRevision identifies the evidence graph revision of the assessment.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	GraphRevision *string `json:"graphRevision,omitempty"`
	// collectorSession is the collector session of the observation.
	//
	// +kubebuilder:validation:Minimum=1
	CollectorSession *int64 `json:"collectorSession,omitempty"`
	// evidenceCompleteness tells how complete the evidence snapshot is.
	EvidenceCompleteness SnapshotCompleteness `json:"evidenceCompleteness"`
	// deviceSummaries are the short results of the GPUDevices on the node.
	//
	// +kubebuilder:validation:MaxItems=256
	// +listType=map
	// +listMapKey=uid
	DeviceSummaries []DeviceSummary `json:"deviceSummaries"`
	// gateOwnership records the ownership of the node gate.
	GateOwnership *GateOwnershipStatus `json:"gateOwnership,omitempty"`
	// conditions are the node path conditions.
	//
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['NodeEligible', 'EvidenceFresh', 'GateOwned', 'CleanupReady'] && c.reason in ['Ready', 'Validating', 'Degraded', 'CoverageMissing', 'EvidenceStale', 'IdentityConflict', 'UIDMismatch', 'AllocationUnknown', 'FenceMissing', 'Conflict', 'NoMatchingDevices', 'PartialSnapshot', 'BundleMismatch', 'FutureObservation', 'UntrustedSource', 'UnadmittedSnapshot', 'TargetCapacityExceeded', 'CleanupPending', 'CleanupStable', 'CleanupCompleted', 'OwnershipConflict', 'InternalError'] && has(c.observedGeneration) && size(c.message) >= 1 && size(c.message) <= 1024)",message="conditions must have an allowed type and reason, an observedGeneration, and a message of 1 to 1024 characters"
	Conditions []metav1.Condition `json:"conditions"`
}

// NodePathState is the path state of one Node, owned by that Node.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=nodepathstates,singular=nodepathstate
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="NODE",type=string,JSONPath=".spec.nodeRef.name"
// +kubebuilder:printcolumn:name="REVISION",type=string,JSONPath=".status.graphRevision"
// +kubebuilder:printcolumn:name="COMPLETE",type=string,JSONPath=".status.evidenceCompleteness"
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=".metadata.creationTimestamp"
type NodePathState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodePathStateSpec   `json:"spec"`
	Status NodePathStateStatus `json:"status,omitempty"`
}

// NodePathStateList is a list of NodePathState.
//
// +kubebuilder:object:root=true
type NodePathStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NodePathState `json:"items"`
}
