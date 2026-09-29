package kubeapi_test

// Contract tables of the CRD schema (GKA-042, GKA-043), written from the spec
// text only. The same tables drive the static manifest checks
// (crd_manifest_test.go) and the API-server behavior checks
// (crd_bounds_test.go, crd_lists_test.go).

import "fmt"

// gkaCShape is one schema object node: its exact property set and required set
// (GKA-042: required = every non-pointer field of GKA-012/013, `mode` excepted).
// Path "" is the resource root; "x[]" is the element schema of list x.
type gkaCShape struct {
	Plural   string
	Path     string
	Props    []string
	Required []string
}

var gkaCShapes = []gkaCShape{
	// GPUFleet
	{"gpufleets", "", []string{"apiVersion", "kind", "metadata", "spec", "status"}, []string{"spec"}},
	{"gpufleets", "spec", []string{"nodeSelector", "requiredCoverage", "freshnessSeconds", "readyForSeconds", "mode", "canarySelector"},
		[]string{"nodeSelector", "requiredCoverage", "freshnessSeconds", "readyForSeconds"}},
	{"gpufleets", "spec.requiredCoverage[]", []string{"name", "pathKind", "required"}, []string{"name", "pathKind", "required"}},
	{"gpufleets", "status", []string{"observedGeneration", "assessmentRevision", "selectedCount", "readyCount", "degradedCount", "unknownCount", "ownedNodeRefs", "conditions"},
		[]string{"observedGeneration", "selectedCount", "readyCount", "degradedCount", "unknownCount", "ownedNodeRefs", "conditions"}},
	{"gpufleets", "status.ownedNodeRefs[]", []string{"name", "uid"}, []string{"name", "uid"}},
	{"gpufleets", "status.conditions[]", nil, []string{"lastTransitionTime", "message", "reason", "status", "type"}},

	// GPUDevice
	{"gpudevices", "", []string{"apiVersion", "kind", "metadata", "spec", "status"}, []string{"spec"}},
	{"gpudevices", "spec", []string{"nodeRef", "inventoryClaim", "fleetRef", "desiredState", "request"},
		[]string{"nodeRef", "inventoryClaim", "fleetRef", "desiredState", "request"}},
	{"gpudevices", "spec.nodeRef", []string{"name", "uid"}, []string{"name", "uid"}},
	{"gpudevices", "spec.fleetRef", []string{"name", "uid"}, []string{"name", "uid"}},
	{"gpudevices", "spec.inventoryClaim", []string{"vendor", "uuid", "serial", "source", "evidenceID"}, []string{"vendor", "source", "evidenceID"}},
	{"gpudevices", "spec.request", []string{"id", "reason"}, []string{"id", "reason"}},
	{"gpudevices", "status", []string{"observedGeneration", "observedRequestID", "intentObservedAt", "actualBinding", "graphRevision", "qualification", "lifecyclePhase", "allocation", "coverage", "findingRefs", "evidenceRefs", "conditions"},
		[]string{"observedGeneration", "observedRequestID", "intentObservedAt", "actualBinding", "qualification", "lifecyclePhase", "allocation", "coverage", "findingRefs", "evidenceRefs", "conditions"}},
	{"gpudevices", "status.actualBinding", []string{"state", "reason", "vendor", "uuid", "serial", "nodeUID", "bootID", "bdf", "functionKey", "source", "evidenceID", "observedAt", "expiresAt"},
		[]string{"state", "reason"}},
	{"gpudevices", "status.actualBinding.source", []string{"type", "name"}, []string{"type", "name"}},
	{"gpudevices", "status.allocation", []string{"state", "reason", "profile", "observedAt", "expiresAt", "evidenceRefs", "affectedWorkloads"},
		[]string{"state", "reason", "evidenceRefs", "affectedWorkloads"}},
	{"gpudevices", "status.allocation.affectedWorkloads[]", []string{"namespace", "name", "uid", "container", "createdAt", "deletedAt"},
		[]string{"namespace", "name", "uid", "container", "createdAt"}},
	{"gpudevices", "status.coverage[]", []string{"name", "pathKind", "state", "reason", "evidenceRefs", "observedAt", "latestObservedAt", "expiresAt"},
		[]string{"name", "pathKind", "state", "reason", "evidenceRefs"}},
	{"gpudevices", "status.findingRefs[]", []string{"id", "type", "severity", "state"}, []string{"id", "type", "severity", "state"}},
	{"gpudevices", "status.evidenceRefs[]", []string{"id", "summary", "observedAt", "expiresAt"}, []string{"id", "summary", "observedAt", "expiresAt"}},
	{"gpudevices", "status.conditions[]", nil, []string{"lastTransitionTime", "message", "reason", "status", "type"}},

	// NodePathState
	{"nodepathstates", "", []string{"apiVersion", "kind", "metadata", "spec", "status"}, []string{"spec"}},
	{"nodepathstates", "spec", []string{"nodeRef"}, []string{"nodeRef"}},
	{"nodepathstates", "spec.nodeRef", []string{"name", "uid"}, []string{"name", "uid"}},
	{"nodepathstates", "status", []string{"observedGeneration", "nodeRef", "graphRevision", "collectorSession", "evidenceCompleteness", "deviceSummaries", "gateOwnership", "conditions"},
		[]string{"observedGeneration", "nodeRef", "evidenceCompleteness", "deviceSummaries", "conditions"}},
	{"nodepathstates", "status.nodeRef", []string{"name", "uid"}, []string{"name", "uid"}},
	{"nodepathstates", "status.deviceSummaries[]", []string{"name", "uid", "desiredState", "observedGeneration", "qualification", "lifecyclePhase"},
		[]string{"name", "uid", "desiredState", "observedGeneration", "qualification", "lifecyclePhase"}},
	{"nodepathstates", "status.gateOwnership", []string{"ownerFleetUID", "nodeUID", "key", "value", "effect", "policy", "cleanupPhase", "cleanupRequestedAt", "recoveryStartedAt", "lastRecoveryPointAt", "recoveryControllerID", "lastRecoveryDigest"},
		[]string{"ownerFleetUID", "nodeUID", "key", "value", "effect", "policy", "cleanupPhase", "cleanupRequestedAt"}},
	{"nodepathstates", "status.gateOwnership.policy", []string{"policyVersion", "requiredCoverage", "freshnessSeconds", "readyForSeconds"},
		[]string{"policyVersion", "requiredCoverage", "freshnessSeconds", "readyForSeconds"}},
	{"nodepathstates", "status.gateOwnership.policy.requiredCoverage[]", []string{"name", "pathKind", "required"}, []string{"name", "pathKind", "required"}},
	{"nodepathstates", "status.conditions[]", nil, []string{"lastTransitionTime", "message", "reason", "status", "type"}},
}

// gkaCBound is one bounded scalar (GKA-042 table). String rows use
// minLength/maxLength; integer rows use minimum/maximum. NoMax rows have no
// declared maximum (int32 range only).
type gkaCBound struct {
	Plural  string
	Path    string
	Int     bool
	Min     int64
	Max     int64
	NoMax   bool
	Pattern bool // requires ^[A-Za-z0-9._-]+$
}

func gkaCStr(plural, path string, min, max int64) gkaCBound {
	return gkaCBound{Plural: plural, Path: path, Min: min, Max: max}
}

func gkaCName(plural, path string, min, max int64) gkaCBound {
	return gkaCBound{Plural: plural, Path: path, Min: min, Max: max, Pattern: true}
}

func gkaCInt(plural, path string, min, max int64) gkaCBound {
	return gkaCBound{Plural: plural, Path: path, Int: true, Min: min, Max: max}
}

func gkaCIntNoMax(plural, path string, min int64) gkaCBound {
	return gkaCBound{Plural: plural, Path: path, Int: true, Min: min, NoMax: true}
}

var gkaCBounds = []gkaCBound{
	// GPUFleet spec/status
	gkaCName("gpufleets", "spec.requiredCoverage[].name", 1, 253),
	gkaCInt("gpufleets", "spec.freshnessSeconds", 1, 86400),
	gkaCInt("gpufleets", "spec.readyForSeconds", 1, 86400),
	gkaCStr("gpufleets", "status.assessmentRevision", 1, 128),
	gkaCStr("gpufleets", "status.ownedNodeRefs[].name", 1, 253),
	gkaCStr("gpufleets", "status.ownedNodeRefs[].uid", 1, 128),
	gkaCIntNoMax("gpufleets", "status.selectedCount", 0),
	gkaCIntNoMax("gpufleets", "status.readyCount", 0),
	gkaCIntNoMax("gpufleets", "status.degradedCount", 0),
	gkaCIntNoMax("gpufleets", "status.unknownCount", 0),

	// GPUDevice spec
	gkaCStr("gpudevices", "spec.nodeRef.name", 1, 253),
	gkaCStr("gpudevices", "spec.nodeRef.uid", 1, 128),
	gkaCStr("gpudevices", "spec.fleetRef.name", 1, 253),
	gkaCStr("gpudevices", "spec.fleetRef.uid", 1, 128),
	gkaCStr("gpudevices", "spec.inventoryClaim.vendor", 1, 128),
	gkaCStr("gpudevices", "spec.inventoryClaim.uuid", 1, 128),
	gkaCStr("gpudevices", "spec.inventoryClaim.serial", 1, 128),
	gkaCStr("gpudevices", "spec.inventoryClaim.source", 1, 256),
	gkaCStr("gpudevices", "spec.inventoryClaim.evidenceID", 1, 128),
	gkaCStr("gpudevices", "spec.request.id", 1, 128),
	gkaCStr("gpudevices", "spec.request.reason", 0, 512),

	// GPUDevice status
	gkaCStr("gpudevices", "status.observedRequestID", 1, 128),
	gkaCStr("gpudevices", "status.graphRevision", 1, 128),
	gkaCStr("gpudevices", "status.actualBinding.reason", 1, 512),
	gkaCStr("gpudevices", "status.actualBinding.vendor", 1, 128),
	gkaCStr("gpudevices", "status.actualBinding.uuid", 1, 128),
	gkaCStr("gpudevices", "status.actualBinding.serial", 1, 128),
	gkaCStr("gpudevices", "status.actualBinding.nodeUID", 1, 128),
	gkaCStr("gpudevices", "status.actualBinding.bootID", 1, 128),
	gkaCStr("gpudevices", "status.actualBinding.bdf", 1, 256),
	gkaCStr("gpudevices", "status.actualBinding.functionKey", 1, 256),
	gkaCStr("gpudevices", "status.actualBinding.source.type", 1, 256),
	gkaCStr("gpudevices", "status.actualBinding.source.name", 1, 256),
	gkaCStr("gpudevices", "status.actualBinding.evidenceID", 1, 128),
	gkaCStr("gpudevices", "status.allocation.reason", 1, 512),
	gkaCStr("gpudevices", "status.allocation.profile", 1, 128),
	gkaCStr("gpudevices", "status.allocation.evidenceRefs[]", 1, 128),
	gkaCStr("gpudevices", "status.allocation.affectedWorkloads[].namespace", 1, 253),
	gkaCStr("gpudevices", "status.allocation.affectedWorkloads[].name", 1, 253),
	gkaCStr("gpudevices", "status.allocation.affectedWorkloads[].container", 1, 253),
	gkaCStr("gpudevices", "status.allocation.affectedWorkloads[].uid", 1, 128),
	gkaCName("gpudevices", "status.coverage[].name", 1, 253),
	gkaCStr("gpudevices", "status.coverage[].reason", 1, 512),
	gkaCStr("gpudevices", "status.coverage[].evidenceRefs[]", 1, 128),
	gkaCStr("gpudevices", "status.findingRefs[].id", 1, 128),
	gkaCStr("gpudevices", "status.findingRefs[].type", 1, 256),
	gkaCStr("gpudevices", "status.findingRefs[].severity", 1, 64),
	gkaCStr("gpudevices", "status.findingRefs[].state", 1, 64),
	gkaCStr("gpudevices", "status.evidenceRefs[].id", 1, 128),
	gkaCStr("gpudevices", "status.evidenceRefs[].summary", 0, 512),

	// NodePathState
	gkaCStr("nodepathstates", "spec.nodeRef.name", 1, 253),
	gkaCStr("nodepathstates", "spec.nodeRef.uid", 1, 128),
	gkaCStr("nodepathstates", "status.nodeRef.name", 1, 253),
	gkaCStr("nodepathstates", "status.nodeRef.uid", 1, 128),
	gkaCStr("nodepathstates", "status.graphRevision", 1, 128),
	gkaCIntNoMax("nodepathstates", "status.collectorSession", 1), // int64, minimum 1, no declared maximum
	gkaCStr("nodepathstates", "status.deviceSummaries[].name", 1, 253),
	gkaCStr("nodepathstates", "status.deviceSummaries[].uid", 1, 128),
	gkaCStr("nodepathstates", "status.gateOwnership.ownerFleetUID", 1, 128),
	gkaCStr("nodepathstates", "status.gateOwnership.nodeUID", 1, 128),
	gkaCStr("nodepathstates", "status.gateOwnership.key", 1, 256),
	gkaCStr("nodepathstates", "status.gateOwnership.value", 1, 128),
	gkaCStr("nodepathstates", "status.gateOwnership.recoveryControllerID", 1, 128),
	gkaCStr("nodepathstates", "status.gateOwnership.lastRecoveryDigest", 1, 128),
	gkaCStr("nodepathstates", "status.gateOwnership.policy.policyVersion", 1, 128),
	gkaCName("nodepathstates", "status.gateOwnership.policy.requiredCoverage[].name", 1, 253),
	gkaCInt("nodepathstates", "status.gateOwnership.policy.freshnessSeconds", 1, 86400),
	gkaCInt("nodepathstates", "status.gateOwnership.policy.readyForSeconds", 1, 86400),
}

// gkaCEnum is one enum-typed field (GKA-014 value sets).
type gkaCEnum struct {
	Plural string
	Path   string
	Values []string
}

var (
	gkaCPathKinds    = []string{"gpu-pcie-parent", "gpu-pcie-root", "gpu-pcie-link-width-normal", "gpu-nic-shared-ancestor", "nic-lldp-remote"}
	gkaCDesired      = []string{"InService", "Maintenance", "Retired"}
	gkaCQualif       = []string{"Qualified", "Disqualified", "Unknown"}
	gkaCPhases       = []string{"Pending", "Validating", "Ready", "Degraded", "MaintenancePending", "MaintenanceReady", "Retiring", "Retired", "Unknown"}
	gkaCBindingState = []string{"Bound", "Unknown", "Conflict"}
	gkaCAllocState   = []string{"Empty", "InUse", "Unknown"}
	gkaCCovState     = []string{"Normal", "Missing", "Unknown", "Unsupported"}
	gkaCCompleteness = []string{"Unknown", "Complete", "Partial"}
	gkaCCleanup      = []string{"None", "Pending", "Stable", "Completed", "Orphaned", "OwnershipConflict"}
)

var gkaCEnums = []gkaCEnum{
	{"gpufleets", "spec.mode", []string{"Audit", "Enforce"}},
	{"gpufleets", "spec.requiredCoverage[].pathKind", gkaCPathKinds},
	{"gpudevices", "spec.desiredState", gkaCDesired},
	{"gpudevices", "status.actualBinding.state", gkaCBindingState},
	{"gpudevices", "status.qualification", gkaCQualif},
	{"gpudevices", "status.lifecyclePhase", gkaCPhases},
	{"gpudevices", "status.allocation.state", gkaCAllocState},
	{"gpudevices", "status.coverage[].state", gkaCCovState},
	{"gpudevices", "status.coverage[].pathKind", gkaCPathKinds},
	{"nodepathstates", "status.evidenceCompleteness", gkaCCompleteness},
	{"nodepathstates", "status.deviceSummaries[].desiredState", gkaCDesired},
	{"nodepathstates", "status.deviceSummaries[].qualification", gkaCQualif},
	{"nodepathstates", "status.deviceSummaries[].lifecyclePhase", gkaCPhases},
	{"nodepathstates", "status.gateOwnership.effect", []string{"NoSchedule"}},
	{"nodepathstates", "status.gateOwnership.cleanupPhase", gkaCCleanup},
	{"nodepathstates", "status.gateOwnership.policy.requiredCoverage[].pathKind", gkaCPathKinds},
}

// gkaCList is one bounded list (GKA-043 table). Make builds element i with a
// distinct key (or a distinct string for atomic lists).
type gkaCList struct {
	Plural string
	Path   string
	Max    int
	Atomic bool
	Key    string // list-map key for map lists
	Make   func(i int) any
}

func gkaCStrElem(i int) any { return fmt.Sprintf("ref-%04d", i) }

var gkaCLists = []gkaCList{
	{"gpufleets", "spec.requiredCoverage", 32, false, "name", func(i int) any {
		return gkaCCoverageReq(fmt.Sprintf("c%02d", i), "gpu-pcie-parent")
	}},
	{"gpufleets", "status.ownedNodeRefs", 1024, false, "uid", func(i int) any {
		return gkaCRef("gpu-node-1", fmt.Sprintf("u%04d", i))
	}},
	{"gpudevices", "status.coverage", 32, false, "name", func(i int) any {
		return map[string]any{
			"name": fmt.Sprintf("c%02d", i), "pathKind": "gpu-pcie-parent", "state": "Missing",
			"reason": "CoverageMissing", "evidenceRefs": []any{},
		}
	}},
	{"gpudevices", "status.findingRefs", 16, false, "id", func(i int) any {
		return map[string]any{"id": fmt.Sprintf("f%04d", i), "type": "T", "severity": "warning", "state": "open"}
	}},
	{"gpudevices", "status.evidenceRefs", 32, false, "id", func(i int) any {
		return map[string]any{"id": fmt.Sprintf("e%04d", i), "summary": "", "observedAt": gkaCTS30, "expiresAt": gkaCTS5m}
	}},
	{"gpudevices", "status.allocation.evidenceRefs", 8, true, "", gkaCStrElem},
	{"gpudevices", "status.coverage[].evidenceRefs", 16, true, "", gkaCStrElem},
	{"gpudevices", "status.allocation.affectedWorkloads", 256, false, "uid", func(i int) any {
		return map[string]any{
			"namespace": "ml", "name": "w", "uid": fmt.Sprintf("w%04d", i), "container": "main", "createdAt": gkaCTS,
		}
	}},
	{"nodepathstates", "status.deviceSummaries", 256, false, "uid", func(i int) any {
		return map[string]any{
			"name": "d", "uid": fmt.Sprintf("d%04d", i), "desiredState": "InService",
			"observedGeneration": 1, "qualification": "Unknown", "lifecyclePhase": "Pending",
		}
	}},
	{"nodepathstates", "status.gateOwnership.policy.requiredCoverage", 32, false, "name", func(i int) any {
		return gkaCCoverageReq(fmt.Sprintf("c%02d", i), "gpu-pcie-parent")
	}},
}

// gkaCOptional lists optional members that no CEL rule constrains: deleting
// them from the base object must stay valid.
var gkaCOptional = []struct{ Plural, Path string }{
	{"gpufleets", "spec.mode"},
	{"gpufleets", "spec.canarySelector"},
	{"gpudevices", "status.actualBinding.serial"},
	{"gpudevices", "status.allocation.affectedWorkloads[].deletedAt"},
	{"nodepathstates", "status.collectorSession"}, // K-T-04: not enforced by the manifest
	{"nodepathstates", "status.gateOwnership"},
	{"nodepathstates", "status.gateOwnership.recoveryStartedAt"},
	{"nodepathstates", "status.gateOwnership.lastRecoveryPointAt"},
	{"nodepathstates", "status.gateOwnership.recoveryControllerID"},
	{"nodepathstates", "status.gateOwnership.lastRecoveryDigest"},
}

// V22 (GFL-071 condition reasons) and the per-resource condition types
// (GKA-046).
var gkaCV22 = []string{
	"Ready", "Validating", "Degraded", "CoverageMissing", "EvidenceStale", "IdentityConflict", "UIDMismatch",
	"AllocationUnknown", "FenceMissing", "Conflict", "NoMatchingDevices", "PartialSnapshot", "BundleMismatch",
	"FutureObservation", "UntrustedSource", "UnadmittedSnapshot", "TargetCapacityExceeded", "CleanupPending",
	"CleanupStable", "CleanupCompleted", "OwnershipConflict", "InternalError",
}

var gkaCCondTypes = map[string][]string{
	"gpufleets":      {"FleetReady", "GateOwned", "CleanupReady"},
	"gpudevices":     {"DeviceQualified", "LifecycleReady", "EvidenceFresh", "IdentityBound", "AllocationKnown"},
	"nodepathstates": {"NodeEligible", "EvidenceFresh", "GateOwned", "CleanupReady"},
}

const gkaCCondMessage = "conditions must have an allowed type and reason, an observedGeneration, and a message of 1 to 1024 characters"
