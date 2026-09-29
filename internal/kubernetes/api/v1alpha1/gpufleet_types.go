package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GPUFleetSpec is the desired policy of a GPU fleet: which nodes it covers,
// which evidence it requires and how strictly the result is applied.
//
// +kubebuilder:validation:XValidation:rule="(has(self.nodeSelector.matchLabels) && size(self.nodeSelector.matchLabels) > 0) || (has(self.nodeSelector.matchExpressions) && size(self.nodeSelector.matchExpressions) > 0)",message="nodeSelector must have at least one matchLabels or matchExpressions entry"
// +kubebuilder:validation:XValidation:rule="self.mode != 'Enforce' || (has(self.canarySelector) && ((has(self.canarySelector.matchLabels) && size(self.canarySelector.matchLabels) > 0) || (has(self.canarySelector.matchExpressions) && size(self.canarySelector.matchExpressions) > 0)))",message="mode Enforce requires a canarySelector with at least one matchLabels or matchExpressions entry"
// +kubebuilder:validation:XValidation:rule="(!has(self.nodeSelector.matchLabels) || size(self.nodeSelector.matchLabels) <= 64) && (!has(self.nodeSelector.matchExpressions) || size(self.nodeSelector.matchExpressions) <= 64) && (!has(self.canarySelector) || ((!has(self.canarySelector.matchLabels) || size(self.canarySelector.matchLabels) <= 64) && (!has(self.canarySelector.matchExpressions) || size(self.canarySelector.matchExpressions) <= 64)))",message="selectors may have at most 64 matchLabels and 64 matchExpressions entries"
type GPUFleetSpec struct {
	// nodeSelector selects the Nodes the fleet covers by their labels. It must
	// have at least one matchLabels or matchExpressions entry.
	NodeSelector metav1.LabelSelector `json:"nodeSelector"`
	// requiredCoverage lists the evidence coverages every device of the fleet
	// must provide.
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
	// mode is Audit (report only, the default) or Enforce (Enforce requires a
	// canarySelector).
	//
	// +kubebuilder:default=Audit
	// +optional
	Mode FleetMode `json:"mode,omitempty"`
	// canarySelector selects the canary Nodes of an Enforce fleet.
	CanarySelector *metav1.LabelSelector `json:"canarySelector,omitempty"`
}

// GPUFleetStatus is the observed aggregate result of a GPU fleet.
//
// +kubebuilder:validation:XValidation:rule="!self.conditions.exists(c, c.type == 'FleetReady' && c.status == 'True') || has(self.assessmentRevision)",message="assessmentRevision is required when FleetReady is True"
type GPUFleetStatus struct {
	// observedGeneration is the metadata.generation the status was computed
	// for.
	ObservedGeneration int64 `json:"observedGeneration"`
	// assessmentRevision identifies the assessment the counts come from.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	AssessmentRevision *string `json:"assessmentRevision,omitempty"`
	// selectedCount is the number of Nodes the fleet selects.
	//
	// +kubebuilder:validation:Minimum=0
	SelectedCount int32 `json:"selectedCount"`
	// readyCount is the number of selected Nodes that are eligible.
	//
	// +kubebuilder:validation:Minimum=0
	ReadyCount int32 `json:"readyCount"`
	// degradedCount is the number of selected Nodes whose path is
	// disqualified.
	//
	// +kubebuilder:validation:Minimum=0
	DegradedCount int32 `json:"degradedCount"`
	// unknownCount is the number of selected Nodes that are neither ready nor
	// degraded.
	//
	// +kubebuilder:validation:Minimum=0
	UnknownCount int32 `json:"unknownCount"`
	// ownedNodeRefs lists the Nodes whose gate the fleet owns.
	//
	// +kubebuilder:validation:MaxItems=1024
	// +listType=map
	// +listMapKey=uid
	OwnedNodeRefs []OwnedNodeRef `json:"ownedNodeRefs"`
	// conditions are the fleet conditions.
	//
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['FleetReady', 'GateOwned', 'CleanupReady'] && c.reason in ['Ready', 'Validating', 'Degraded', 'CoverageMissing', 'EvidenceStale', 'IdentityConflict', 'UIDMismatch', 'AllocationUnknown', 'FenceMissing', 'Conflict', 'NoMatchingDevices', 'PartialSnapshot', 'BundleMismatch', 'FutureObservation', 'UntrustedSource', 'UnadmittedSnapshot', 'TargetCapacityExceeded', 'CleanupPending', 'CleanupStable', 'CleanupCompleted', 'OwnershipConflict', 'InternalError'] && has(c.observedGeneration) && size(c.message) >= 1 && size(c.message) <= 1024)",message="conditions must have an allowed type and reason, an observedGeneration, and a message of 1 to 1024 characters"
	Conditions []metav1.Condition `json:"conditions"`
}

// GPUFleet is a cluster-scoped group of GPU nodes that share one qualification
// policy.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=gpufleets,singular=gpufleet
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="MODE",type=string,JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="READY",type=integer,JSONPath=".status.readyCount"
// +kubebuilder:printcolumn:name="DEGRADED",type=integer,JSONPath=".status.degradedCount"
// +kubebuilder:printcolumn:name="UNKNOWN",type=integer,JSONPath=".status.unknownCount"
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=".metadata.creationTimestamp"
type GPUFleet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUFleetSpec   `json:"spec"`
	Status GPUFleetStatus `json:"status,omitempty"`
}

// GPUFleetList is a list of GPUFleet.
//
// +kubebuilder:object:root=true
type GPUFleetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []GPUFleet `json:"items"`
}
