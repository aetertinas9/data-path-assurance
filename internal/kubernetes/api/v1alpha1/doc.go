// Package v1alpha1 defines the infrastructure.data-path-assurance.io/v1alpha1
// Kubernetes API of the GPU fleet lifecycle: the cluster-scoped GPUFleet,
// GPUDevice and NodePathState custom resources.
//
// The types are the source of the CRD manifests in deploy/crds and of the
// generated deep copy functions; both are produced by `make generate`
// (controller-gen) from the markers in this package and must not be edited by
// hand. The package imports only the standard library and
// k8s.io/apimachinery, and it registers nothing globally: a scheme learns the
// kinds only through an explicit AddToScheme call.
//
// +kubebuilder:object:generate=true
// +groupName=infrastructure.data-path-assurance.io
package v1alpha1
