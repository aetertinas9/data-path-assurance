package kubeapi_test

// Shared fixtures and helpers for the CRD contract tests (GKA-040
// ..049). Every identifier here uses the prefix gkaC so it cannot collide with
// the shared harness (harness_test.go) or other test helpers.
//
// Objects are built as JSON-shaped maps and sent as unstructured objects so
// that invalid values (empty enums, oversize strings, missing required fields)
// can be expressed at all; the typed client would refuse to send several of
// them. Most negative cases use server-side dry-run, so nothing is persisted
// besides one base object per resource per test.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gkaCGroup      = "infrastructure.data-path-assurance.io"
	gkaCVersion    = "v1alpha1"
	gkaCAPIVersion = gkaCGroup + "/" + gkaCVersion

	gkaCTS   = "2026-09-30T00:00:00Z"
	gkaCTS30 = "2026-09-30T00:00:30Z"
	gkaCTS5m = "2026-09-30T00:05:30Z"

	gkaCNodeUID  = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	gkaCFleetUID = "5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"
)

// gkaCRes describes one CRD (GKA-040 table).
type gkaCRes struct {
	Kind, Plural, Singular, ListKind, File string
}

var (
	gkaCFleetRes = gkaCRes{"GPUFleet", "gpufleets", "gpufleet", "GPUFleetList", gkaCGroup + "_gpufleets.yaml"}
	gkaCDevRes   = gkaCRes{"GPUDevice", "gpudevices", "gpudevice", "GPUDeviceList", gkaCGroup + "_gpudevices.yaml"}
	gkaCNPSRes   = gkaCRes{"NodePathState", "nodepathstates", "nodepathstate", "NodePathStateList", gkaCGroup + "_nodepathstates.yaml"}
	gkaCAllRes   = []gkaCRes{gkaCFleetRes, gkaCDevRes, gkaCNPSRes}
)

// gkaCKit bundles the builders of one resource.
type gkaCKit struct {
	Res    gkaCRes
	Spec   func() map[string]any
	Status func() map[string]any
}

var gkaCKits = []gkaCKit{
	{gkaCFleetRes, gkaCFleetSpec, gkaCFleetStatus},
	{gkaCDevRes, gkaCDeviceSpec, gkaCDeviceStatus},
	{gkaCNPSRes, gkaCNPSSpec, gkaCNPSStatus},
}

func gkaCKitOf(plural string) gkaCKit {
	for _, k := range gkaCKits {
		if k.Res.Plural == plural {
			return k
		}
	}
	panic("gkaC: unknown resource " + plural)
}

// ---------------------------------------------------------------- JSON maps

// gkaCClone deep-copies a JSON-shaped value through encoding/json. The result
// only holds map[string]any, []any, string, float64, bool and nil, which is
// what unstructured objects and the path helpers below expect.
func gkaCClone(m map[string]any) map[string]any {
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("gkaC: marshal fixture: %v", err))
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		panic(fmt.Sprintf("gkaC: unmarshal fixture: %v", err))
	}
	return out
}

type gkaCSeg struct {
	key   string
	idx   int
	isIdx bool
}

// gkaCParse splits a dotted path. A segment "x[]" selects element 0 of list x
// and "x[3]" element 3.
func gkaCParse(path string) []gkaCSeg {
	var segs []gkaCSeg
	for _, p := range strings.Split(path, ".") {
		s := gkaCSeg{key: p}
		if i := strings.Index(p, "["); i >= 0 && strings.HasSuffix(p, "]") {
			s.key = p[:i]
			s.isIdx = true
			if n := p[i+1 : len(p)-1]; n != "" {
				v, err := strconv.Atoi(n)
				if err != nil {
					panic("gkaC: bad path " + path)
				}
				s.idx = v
			}
		}
		segs = append(segs, s)
	}
	return segs
}

// gkaCStep descends one segment.
func gkaCStep(cur any, s gkaCSeg) (any, bool) {
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[s.key]
	if !ok {
		return nil, false
	}
	if !s.isIdx {
		return v, true
	}
	l, ok := v.([]any)
	if !ok || s.idx >= len(l) {
		return nil, false
	}
	return l[s.idx], true
}

// gkaCGet returns the value at path.
func gkaCGet(root map[string]any, path string) (any, bool) {
	var cur any = root
	for _, s := range gkaCParse(path) {
		var ok bool
		if cur, ok = gkaCStep(cur, s); !ok {
			return nil, false
		}
	}
	return cur, true
}

// gkaCParent descends every segment but the last.
func gkaCParent(root map[string]any, path string) (map[string]any, gkaCSeg) {
	segs := gkaCParse(path)
	var cur any = root
	for _, s := range segs[:len(segs)-1] {
		var ok bool
		if cur, ok = gkaCStep(cur, s); !ok {
			panic("gkaC: path not found: " + path)
		}
	}
	pm, ok := cur.(map[string]any)
	if !ok {
		panic("gkaC: parent is not an object: " + path)
	}
	return pm, segs[len(segs)-1]
}

// gkaCSet sets the value at path (the parent must exist).
func gkaCSet(root map[string]any, path string, value any) {
	pm, last := gkaCParent(root, path)
	if !last.isIdx {
		pm[last.key] = value
		return
	}
	l, ok := pm[last.key].([]any)
	if !ok || last.idx >= len(l) {
		panic("gkaC: list element not found: " + path)
	}
	l[last.idx] = value
}

// gkaCDel removes the object member at path.
func gkaCDel(root map[string]any, path string) {
	pm, last := gkaCParent(root, path)
	if last.isIdx {
		panic("gkaC: cannot delete a list element: " + path)
	}
	delete(pm, last.key)
}

// gkaCErrPath renders a schema path the way API server errors do (list
// element 0 is "[0]").
func gkaCErrPath(path string) string {
	return strings.ReplaceAll(path, "[]", "[0]")
}

// -------------------------------------------------------------- base objects

func gkaCRef(name, uid string) map[string]any {
	return map[string]any{"name": name, "uid": uid}
}

func gkaCCoverageReq(name, kind string) map[string]any {
	return map[string]any{"name": name, "pathKind": kind, "required": true}
}

// gkaCFleetSpec is the normative GPUFleet of spec section 12.
func gkaCFleetSpec() map[string]any {
	return map[string]any{
		"nodeSelector": map[string]any{"matchLabels": map[string]any{"data-path-assurance.io/fleet": "lab-a"}},
		"requiredCoverage": []any{
			gkaCCoverageReq("pcie-parent", "gpu-pcie-parent"),
			gkaCCoverageReq("pcie-root", "gpu-pcie-root"),
			gkaCCoverageReq("pcie-width", "gpu-pcie-link-width-normal"),
		},
		"freshnessSeconds": 60,
		"readyForSeconds":  30,
		"mode":             "Audit",
	}
}

// gkaCDeviceSpec is the normative GPUDevice of spec section 12.
func gkaCDeviceSpec() map[string]any {
	return map[string]any{
		"nodeRef":  gkaCRef("gpu-node-1", gkaCNodeUID),
		"fleetRef": gkaCRef("lab-a-gpus", gkaCFleetUID),
		"inventoryClaim": map[string]any{
			"vendor": "NVIDIA", "uuid": "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
			"source": "operator/asset-db", "evidenceID": "asset-db:rack7-u12-gpu0",
		},
		"desiredState": "InService",
		"request":      map[string]any{"id": "enroll-1", "reason": "initial enrollment"},
	}
}

func gkaCNPSSpec() map[string]any {
	return map[string]any{"nodeRef": gkaCRef("gpu-node-1", gkaCNodeUID)}
}

func gkaCCond(typ, status, reason, msg string) map[string]any {
	return map[string]any{
		"type": typ, "status": status, "observedGeneration": 1,
		"lastTransitionTime": gkaCTS, "reason": reason, "message": msg,
	}
}

const gkaCGraphRev = "7:2:c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"

// gkaCFleetStatus is a fully populated, valid GPUFleet status.
func gkaCFleetStatus() map[string]any {
	return map[string]any{
		"observedGeneration": 1,
		"assessmentRevision": strings.Repeat("ab", 32),
		"selectedCount":      1, "readyCount": 1, "degradedCount": 0, "unknownCount": 0,
		"ownedNodeRefs": []any{gkaCRef("gpu-node-1", gkaCNodeUID)},
		"conditions": []any{
			gkaCCond("FleetReady", "True", "Ready", "selected=1 ready=1 degraded=0 unknown=0"),
		},
	}
}

// gkaCDeviceStatus is a fully populated, valid GPUDevice status (every
// optional member present, one element in every list).
func gkaCDeviceStatus() map[string]any {
	return map[string]any{
		"observedGeneration": 1, "observedRequestID": "enroll-1", "intentObservedAt": gkaCTS,
		"actualBinding": map[string]any{
			"state": "Bound", "reason": "Ready", "vendor": "NVIDIA",
			"uuid": "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d", "serial": "SN-0001",
			"nodeUID": gkaCNodeUID, "bootID": "3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90",
			"bdf": "0000:03:00.0", "functionKey": "PCIeFunction/pci-bdf:0000:03:00.0",
			"source":     map[string]any{"type": "agent", "name": "path-agent/nvidia-smi"},
			"evidenceID": "nb:7:2:c0ffee", "observedAt": gkaCTS30, "expiresAt": gkaCTS5m,
		},
		"graphRevision": gkaCGraphRev,
		"qualification": "Qualified", "lifecyclePhase": "Ready",
		"allocation": map[string]any{
			"state": "InUse", "reason": "Ready", "profile": "default",
			"observedAt": gkaCTS30, "expiresAt": gkaCTS5m,
			"evidenceRefs": []any{"al:7:2:1"},
			"affectedWorkloads": []any{map[string]any{
				"namespace": "ml", "name": "trainer-0", "uid": "wl-uid-0", "container": "main",
				"createdAt": gkaCTS, "deletedAt": gkaCTS30,
			}},
		},
		"coverage": []any{map[string]any{
			"name": "pcie-parent", "pathKind": "gpu-pcie-parent", "state": "Normal", "reason": "Normal",
			"evidenceRefs": []any{"pe:7:2:a1"},
			"observedAt":   gkaCTS30, "latestObservedAt": gkaCTS30, "expiresAt": gkaCTS5m,
		}},
		"findingRefs": []any{map[string]any{
			"id": "f-1", "type": "PCIE_LINK_WIDTH_DEGRADED", "severity": "warning", "state": "open",
		}},
		"evidenceRefs": []any{map[string]any{
			"id": "pe:7:2:a1", "summary": "PCIeFunction LOCATED_IN PCIeSwitch observed",
			"observedAt": gkaCTS30, "expiresAt": gkaCTS5m,
		}},
		"conditions": []any{
			gkaCCond("AllocationKnown", "True", "Ready", "allocation=InUse"),
			gkaCCond("DeviceQualified", "True", "Ready", "qualification=Qualified phase=Ready"),
			gkaCCond("IdentityBound", "True", "Ready", "binding=Bound"),
			gkaCCond("LifecycleReady", "True", "Ready", "phase=Ready"),
		},
	}
}

// gkaCNPSStatus is a fully populated, valid NodePathState status including the
// S4b-only gateOwnership shape.
func gkaCNPSStatus() map[string]any {
	return map[string]any{
		"observedGeneration":   1,
		"nodeRef":              gkaCRef("gpu-node-1", gkaCNodeUID),
		"graphRevision":        gkaCGraphRev,
		"collectorSession":     1,
		"evidenceCompleteness": "Complete",
		"deviceSummaries": []any{map[string]any{
			"name": "gpu-node-1-gpu0", "uid": "dev-uid-0", "desiredState": "InService",
			"observedGeneration": 1, "qualification": "Qualified", "lifecyclePhase": "Ready",
		}},
		"gateOwnership": map[string]any{
			"ownerFleetUID": gkaCFleetUID, "nodeUID": gkaCNodeUID,
			"key": "data-path-assurance.io/gate", "value": "fleet-1", "effect": "NoSchedule",
			"policy": map[string]any{
				"policyVersion":    "pv-1",
				"requiredCoverage": []any{gkaCCoverageReq("pcie-parent", "gpu-pcie-parent")},
				"freshnessSeconds": 60, "readyForSeconds": 30,
			},
			"cleanupPhase": "None", "cleanupRequestedAt": gkaCTS,
			"recoveryStartedAt": gkaCTS, "lastRecoveryPointAt": gkaCTS30,
			"recoveryControllerID": "controller-1", "lastRecoveryDigest": "digest-1",
		},
		"conditions": []any{
			gkaCCond("EvidenceFresh", "True", "Ready", "completeness=Complete"),
			gkaCCond("NodeEligible", "True", "Ready", "eligibility=Eligible qualification=Qualified"),
		},
	}
}

// ---------------------------------------------------------- API interaction

func gkaCCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// gkaCStart returns the shared envtest environment and registers the cleanup
// (harness rule GKA-173: delete everything this test created).
func gkaCStart(t *testing.T) *gkaEnvT {
	t.Helper()
	e := gkaEnv(t)
	t.Cleanup(func() { gkaCleanupAll(t, e) })
	return e
}

func gkaCObject(res gkaCRes, name string, spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": gkaCAPIVersion, "kind": res.Kind,
		"metadata": map[string]any{"name": name},
		"spec":     spec,
	}
}

func gkaCUnstructured(m map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: gkaCClone(m)}
}

// gkaCCreate persists an object (metadata.name must come from gkaName) and
// returns the server's copy.
func gkaCCreate(t *testing.T, e *gkaEnvT, res gkaCRes, name string, spec map[string]any) *unstructured.Unstructured {
	t.Helper()
	u := gkaCUnstructured(gkaCObject(res, name, spec))
	ctx, cancel := gkaCCtx()
	defer cancel()
	if err := e.Client.Create(ctx, u); err != nil {
		t.Fatalf("fixture: create %s %s: %v", res.Kind, name, err)
	}
	return u
}

// gkaCCreateDry validates a create without persisting it.
func gkaCCreateDry(e *gkaEnvT, res gkaCRes, name string, spec map[string]any) error {
	u := gkaCUnstructured(gkaCObject(res, name, spec))
	ctx, cancel := gkaCCtx()
	defer cancel()
	return e.Client.Create(ctx, u, client.DryRunAll)
}

// gkaCStatusDry validates a status update of base without persisting it.
func gkaCStatusDry(e *gkaEnvT, base *unstructured.Unstructured, status map[string]any) error {
	cp := base.DeepCopy()
	cp.Object["status"] = gkaCClone(status)
	ctx, cancel := gkaCCtx()
	defer cancel()
	return e.Client.Status().Update(ctx, cp, client.DryRunAll)
}

// gkaCSpecDry validates a spec update of base (mutate edits a copy of the spec)
// without persisting it.
func gkaCSpecDry(e *gkaEnvT, base *unstructured.Unstructured, mutate func(spec map[string]any)) error {
	cp := base.DeepCopy()
	spec := gkaCClone(cp.Object["spec"].(map[string]any))
	mutate(spec)
	cp.Object["spec"] = spec
	ctx, cancel := gkaCCtx()
	defer cancel()
	return e.Client.Update(ctx, cp, client.DryRunAll)
}

// gkaCStatusReal persists a status update.
func gkaCStatusReal(t *testing.T, e *gkaEnvT, base *unstructured.Unstructured, status map[string]any) *unstructured.Unstructured {
	t.Helper()
	cp := base.DeepCopy()
	cp.Object["status"] = gkaCClone(status)
	ctx, cancel := gkaCCtx()
	defer cancel()
	if err := e.Client.Status().Update(ctx, cp); err != nil {
		t.Fatalf("fixture: update status of %s %s: %v", cp.GetKind(), cp.GetName(), err)
	}
	return cp
}

// gkaCGetObj reads an object fresh from the API server.
func gkaCGetObj(t *testing.T, e *gkaEnvT, res gkaCRes, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(gkaCAPIVersion)
	u.SetKind(res.Kind)
	ctx, cancel := gkaCCtx()
	defer cancel()
	if err := e.Client.Get(ctx, client.ObjectKey{Name: name}, u); err != nil {
		t.Fatalf("fixture: get %s %s: %v", res.Kind, name, err)
	}
	return u
}

// gkaCBaseObject creates the persisted base object of a kit under a unique
// name and proves the kit's base status is valid (the control for every
// negative case that follows).
func gkaCBaseObject(t *testing.T, e *gkaEnvT, k gkaCKit) *unstructured.Unstructured {
	t.Helper()
	base := gkaCCreate(t, e, k.Res, gkaName(t, k.Res.Singular), k.Spec())
	if err := gkaCStatusDry(e, base, k.Status()); err != nil {
		t.Fatalf("fixture: the base %s status must be accepted by the API server (control case): %v", k.Res.Kind, err)
	}
	return base
}

// ------------------------------------------------------------- assertions

func gkaCOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: want accepted by the API server, got %v", what, err)
	}
}

func gkaCInvalid(t *testing.T, what string, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: want 422 Invalid, but the API server accepted the object", what)
		return
	}
	if !apierrors.IsInvalid(err) {
		t.Errorf("%s: want a 422 Invalid error, got %v", what, err)
		return
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%s: error does not contain %q: %v", what, w, err)
		}
	}
}
