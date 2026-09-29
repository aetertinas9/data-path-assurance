// Package controller is the Kubernetes adapter of the GPU fleet lifecycle: it
// watches GPUFleet, GPUDevice, NodePathState and Node objects, asks an
// app.NodeAssessor for the domain decisions, and projects them into CRD
// status, NodePathState objects and one Node condition.
//
// Work happens in passes. A pass reads every watched object from the informer
// caches, classifies each fleet, node and device into a scope, calls the
// assessor once per assessable node and writes only what changed. Passes run
// on one goroutine, only on the elected leader and only after every cache has
// synced. The package opens no listener, emits no Event objects and keeps no
// decision state beyond the assessor's own memory.
package controller
