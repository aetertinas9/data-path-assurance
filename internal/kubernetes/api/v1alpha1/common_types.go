package v1alpha1

// ObjectRef identifies a Kubernetes object by name and UID. Both parts are
// compared: a reference whose UID differs from the live object's UID does not
// refer to that object.
type ObjectRef struct {
	// name is the object name.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// uid is the object UID.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

// OwnedNodeRef identifies a Node whose gate a fleet owns.
type OwnedNodeRef struct {
	// name is the Node name.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// uid is the Node UID.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

// SourceRef identifies the evidence source that produced an observation.
type SourceRef struct {
	// type is the source type.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Type string `json:"type"`
	// name is the source name.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`
}

// FleetCoverageRequirement names one evidence coverage a fleet requires from
// every device.
type FleetCoverageRequirement struct {
	// name identifies the requirement inside its list.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._-]+$`
	Name string `json:"name"`
	// pathKind is the kind of physical path the coverage describes.
	//
	// +kubebuilder:validation:Enum=gpu-pcie-parent;gpu-pcie-root;gpu-pcie-link-width-normal;gpu-nic-shared-ancestor;nic-lldp-remote
	PathKind string `json:"pathKind"`
	// required tells whether missing coverage disqualifies the device.
	Required bool `json:"required"`
}
