package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InventoryClaim is the operator's claim about which hardware a GPUDevice is.
// It needs a uuid or a serial.
//
// +kubebuilder:validation:XValidation:rule="has(self.uuid) || has(self.serial)",message="inventoryClaim requires uuid or serial"
type InventoryClaim struct {
	// vendor is the GPU vendor.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Vendor string `json:"vendor"`
	// uuid is the GPU UUID.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UUID *string `json:"uuid,omitempty"`
	// serial is the GPU serial number.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Serial *string `json:"serial,omitempty"`
	// source names where the claim comes from.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Source string `json:"source"`
	// evidenceID identifies the inventory record backing the claim.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	EvidenceID string `json:"evidenceID"`
}

// LifecycleRequest identifies one operator request. Changing desiredState
// needs a new id.
type LifecycleRequest struct {
	// id identifies the request.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`
	// reason is the free-text reason of the request.
	//
	// +kubebuilder:validation:MaxLength=512
	Reason string `json:"reason"`
}

// GPUDeviceSpec is the operator's intent for one GPU.
//
// +kubebuilder:validation:XValidation:rule="self.nodeRef == oldSelf.nodeRef",message="spec.nodeRef is immutable"
// +kubebuilder:validation:XValidation:rule="self.inventoryClaim == oldSelf.inventoryClaim",message="spec.inventoryClaim is immutable"
// +kubebuilder:validation:XValidation:rule="oldSelf.desiredState != 'Retired' || self.desiredState == 'Retired'",message="a Retired GPUDevice cannot leave Retired; register a new GPUDevice"
// +kubebuilder:validation:XValidation:rule="self.desiredState == oldSelf.desiredState || self.request.id != oldSelf.request.id",message="changing desiredState requires a new request.id"
type GPUDeviceSpec struct {
	// nodeRef is the Node the GPU is installed in. It is immutable.
	NodeRef ObjectRef `json:"nodeRef"`
	// inventoryClaim is the claimed hardware identity. It is immutable.
	InventoryClaim InventoryClaim `json:"inventoryClaim"`
	// fleetRef is the GPUFleet the device belongs to.
	FleetRef ObjectRef `json:"fleetRef"`
	// desiredState is the lifecycle state the operator asks for.
	DesiredState DesiredState `json:"desiredState"`
	// request identifies the operator request behind desiredState.
	Request LifecycleRequest `json:"request"`
}

// ActualBinding is the observed binding of the claim to hardware. A Bound
// binding carries the full observed identity.
//
// +kubebuilder:validation:XValidation:rule="self.state != 'Bound' || (has(self.vendor) && has(self.uuid) && has(self.nodeUID) && has(self.bootID) && has(self.bdf) && has(self.functionKey) && has(self.source) && has(self.evidenceID) && has(self.observedAt) && has(self.expiresAt))",message="actualBinding.state Bound requires vendor, uuid, nodeUID, bootID, bdf, functionKey, source, evidenceID, observedAt and expiresAt"
type ActualBinding struct {
	// state is the binding state.
	State BindingState `json:"state"`
	// reason tells why the binding has this state.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Reason string `json:"reason"`
	// vendor is the observed GPU vendor.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Vendor *string `json:"vendor,omitempty"`
	// uuid is the observed GPU UUID.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UUID *string `json:"uuid,omitempty"`
	// serial is the observed GPU serial number.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Serial *string `json:"serial,omitempty"`
	// nodeUID is the UID of the Node the GPU was observed on.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	NodeUID *string `json:"nodeUID,omitempty"`
	// bootID is the boot ID of the Node during the observation.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	BootID *string `json:"bootID,omitempty"`
	// bdf is the PCI address of the GPU function.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	BDF *string `json:"bdf,omitempty"`
	// functionKey is the stable key of the GPU function.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	FunctionKey *string `json:"functionKey,omitempty"`
	// source is the evidence source of the observation.
	Source *SourceRef `json:"source,omitempty"`
	// evidenceID identifies the evidence of the observation.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	EvidenceID *string `json:"evidenceID,omitempty"`
	// observedAt is when the binding was observed.
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	// expiresAt is when the observation stops being fresh.
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// AffectedWorkload is a workload container that uses a device.
type AffectedWorkload struct {
	// namespace is the workload namespace.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Namespace string `json:"namespace"`
	// name is the workload name.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// uid is the workload UID.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
	// container is the container name.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Container string `json:"container"`
	// createdAt is when the workload was created.
	CreatedAt metav1.Time `json:"createdAt"`
	// deletedAt is when the workload was deleted, if it was.
	DeletedAt *metav1.Time `json:"deletedAt,omitempty"`
}

// AllocationSummary summarizes which workloads use the device. Empty and InUse
// need an observation.
//
// +kubebuilder:validation:XValidation:rule="self.state == 'Unknown' || (has(self.profile) && has(self.observedAt) && has(self.expiresAt) && size(self.evidenceRefs) > 0)",message="allocation.state Empty or InUse requires profile, observedAt, expiresAt and evidenceRefs"
type AllocationSummary struct {
	// state is the allocation state.
	State AllocationState `json:"state"`
	// reason tells why the allocation has this state.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Reason string `json:"reason"`
	// profile is the allocation profile of the observation.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Profile *string `json:"profile,omitempty"`
	// observedAt is when the allocation was observed.
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	// expiresAt is when the observation stops being fresh.
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// evidenceRefs are the IDs of the evidence behind the allocation.
	//
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	// +listType=atomic
	EvidenceRefs []string `json:"evidenceRefs"`
	// affectedWorkloads are the workloads that use the device.
	//
	// +kubebuilder:validation:MaxItems=256
	// +listType=map
	// +listMapKey=uid
	AffectedWorkloads []AffectedWorkload `json:"affectedWorkloads"`
}

// CoverageSummary is the state of one required coverage of a device. A Normal
// coverage carries its observation times and evidence.
//
// +kubebuilder:validation:XValidation:rule="self.state != 'Normal' || (has(self.observedAt) && has(self.latestObservedAt) && has(self.expiresAt) && size(self.evidenceRefs) > 0)",message="coverage.state Normal requires observedAt, latestObservedAt, expiresAt and evidenceRefs"
type CoverageSummary struct {
	// name is the name of the fleet requirement the coverage answers.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]+$`
	Name string `json:"name"`
	// pathKind is the kind of physical path the coverage describes.
	//
	// +kubebuilder:validation:Enum=gpu-pcie-parent;gpu-pcie-root;gpu-pcie-link-width-normal;gpu-nic-shared-ancestor;nic-lldp-remote
	PathKind string `json:"pathKind"`
	// state is the coverage state.
	State CoverageState `json:"state"`
	// reason tells why the coverage has this state.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Reason string `json:"reason"`
	// evidenceRefs are the IDs of the evidence behind the coverage.
	//
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	// +listType=atomic
	EvidenceRefs []string `json:"evidenceRefs"`
	// observedAt is when the coverage was observed.
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	// latestObservedAt is the latest observation time of the coverage.
	LatestObservedAt *metav1.Time `json:"latestObservedAt,omitempty"`
	// expiresAt is when the observation stops being fresh.
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// FindingSummary is a finding that concerns the device.
type FindingSummary struct {
	// id identifies the finding.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`
	// type is the finding type.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Type string `json:"type"`
	// severity is the finding severity.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Severity string `json:"severity"`
	// state is the finding state.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	State string `json:"state"`
}

// EvidenceSummary is an evidence item behind the device status.
type EvidenceSummary struct {
	// id identifies the evidence.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`
	// summary is a one-line description of the evidence.
	//
	// +kubebuilder:validation:MaxLength=512
	Summary string `json:"summary"`
	// observedAt is when the evidence was observed.
	ObservedAt metav1.Time `json:"observedAt"`
	// expiresAt is when the evidence stops being fresh.
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// GPUDeviceStatus is the observed state of one GPU.
//
// +kubebuilder:validation:XValidation:rule="self.qualification == 'Unknown' || has(self.graphRevision)",message="graphRevision is required when qualification is Qualified or Disqualified"
type GPUDeviceStatus struct {
	// observedGeneration is the metadata.generation the status was computed
	// for.
	ObservedGeneration int64 `json:"observedGeneration"`
	// observedRequestID is the spec.request.id the status was computed for.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ObservedRequestID string `json:"observedRequestID"`
	// intentObservedAt is when the controller first saw the current intent.
	IntentObservedAt metav1.Time `json:"intentObservedAt"`
	// actualBinding is the observed binding of the claim.
	ActualBinding ActualBinding `json:"actualBinding"`
	// graphRevision identifies the evidence graph revision of the assessment.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	GraphRevision *string `json:"graphRevision,omitempty"`
	// qualification is the path qualification verdict.
	Qualification Qualification `json:"qualification"`
	// lifecyclePhase is the lifecycle phase of the device.
	LifecyclePhase LifecyclePhase `json:"lifecyclePhase"`
	// allocation summarizes which workloads use the device.
	Allocation AllocationSummary `json:"allocation"`
	// coverage is the state of every required coverage.
	//
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	Coverage []CoverageSummary `json:"coverage"`
	// findingRefs are the findings that concern the device.
	//
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=id
	FindingRefs []FindingSummary `json:"findingRefs"`
	// evidenceRefs are the evidence items behind the status.
	//
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=id
	EvidenceRefs []EvidenceSummary `json:"evidenceRefs"`
	// conditions are the device conditions.
	//
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['DeviceQualified', 'LifecycleReady', 'EvidenceFresh', 'IdentityBound', 'AllocationKnown'] && c.reason in ['Ready', 'Validating', 'Degraded', 'CoverageMissing', 'EvidenceStale', 'IdentityConflict', 'UIDMismatch', 'AllocationUnknown', 'FenceMissing', 'Conflict', 'NoMatchingDevices', 'PartialSnapshot', 'BundleMismatch', 'FutureObservation', 'UntrustedSource', 'UnadmittedSnapshot', 'TargetCapacityExceeded', 'CleanupPending', 'CleanupStable', 'CleanupCompleted', 'OwnershipConflict', 'InternalError'] && has(c.observedGeneration) && size(c.message) >= 1 && size(c.message) <= 1024)",message="conditions must have an allowed type and reason, an observedGeneration, and a message of 1 to 1024 characters"
	Conditions []metav1.Condition `json:"conditions"`
}

// GPUDevice is the operator's registration of one GPU of a fleet together with
// the controller's observed result for it.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=gpudevices,singular=gpudevice
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="NODE",type=string,JSONPath=".spec.nodeRef.name"
// +kubebuilder:printcolumn:name="DESIRED",type=string,JSONPath=".spec.desiredState"
// +kubebuilder:printcolumn:name="PHASE",type=string,JSONPath=".status.lifecyclePhase"
// +kubebuilder:printcolumn:name="QUALIFIED",type=string,JSONPath=".status.qualification"
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=".metadata.creationTimestamp"
type GPUDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUDeviceSpec   `json:"spec"`
	Status GPUDeviceStatus `json:"status,omitempty"`
}

// GPUDeviceList is a list of GPUDevice.
//
// +kubebuilder:object:root=true
type GPUDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []GPUDevice `json:"items"`
}
