package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// GroupName is the API group of every resource in this package.
	GroupName = "infrastructure.data-path-assurance.io"
	// Version is the only served and stored version of the group.
	Version = "v1alpha1"
)

// GroupVersion is the group and version of the kinds in this package. It is
// an immutable value: callers must not reassign it.
var GroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}

// AddToScheme registers GPUFleet, GPUDevice, NodePathState and their List
// kinds under GroupVersion and adds the common meta types (ListOptions,
// WatchEvent, ...) for that group version. Registering the same kinds twice
// in one scheme is harmless; a scheme this function is not called on is not
// affected.
func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&GPUFleet{}, &GPUFleetList{},
		&GPUDevice{}, &GPUDeviceList{},
		&NodePathState{}, &NodePathStateList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
