package kubeapi_test

// API-server behavior of the CEL rules (GKA-044 spec rules, GKA-045 status
// rules, GKA-046 conditions rule). Each rule gets the minimal violating
// fixture (everything else from the normative base object, so no blocking
// schema error hides the CEL evaluation, GKA-044 last paragraph), plus the
// boundary cases just inside the rule.

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------- cold-start status

func gkaCColdFleetStatus() map[string]any {
	return map[string]any{
		"observedGeneration": 1, "selectedCount": 0, "readyCount": 0, "degradedCount": 0, "unknownCount": 0,
		"ownedNodeRefs": []any{},
		"conditions": []any{
			gkaCCond("FleetReady", "Unknown", "NoMatchingDevices", "selected=0 ready=0 degraded=0 unknown=0"),
		},
	}
}

// gkaCColdDeviceStatus is the cold-start GPUDevice status of spec section 12
// (GKA-072): no observation, so no graphRevision and no coverage rows.
func gkaCColdDeviceStatus() map[string]any {
	return map[string]any{
		"observedGeneration": 1, "observedRequestID": "enroll-1", "intentObservedAt": gkaCTS,
		"actualBinding": map[string]any{"state": "Unknown", "reason": "Validating"},
		"qualification": "Unknown", "lifecyclePhase": "Pending",
		"allocation": map[string]any{
			"state": "Unknown", "reason": "AllocationUnknown", "evidenceRefs": []any{}, "affectedWorkloads": []any{},
		},
		"coverage": []any{}, "findingRefs": []any{}, "evidenceRefs": []any{},
		"conditions": []any{
			gkaCCond("AllocationKnown", "Unknown", "AllocationUnknown", "allocation=Unknown; no_observation"),
			gkaCCond("DeviceQualified", "Unknown", "Validating", "qualification=Unknown phase=Pending; no_observation"),
			gkaCCond("IdentityBound", "Unknown", "Validating", "binding=Unknown; no_observation"),
			gkaCCond("LifecycleReady", "Unknown", "Validating", "phase=Pending; no_observation"),
		},
	}
}

func gkaCColdNPSStatus() map[string]any {
	return map[string]any{
		"observedGeneration": 1, "nodeRef": gkaCRef("gpu-node-1", gkaCNodeUID),
		"evidenceCompleteness": "Unknown", "deviceSummaries": []any{},
		"conditions": []any{
			gkaCCond("EvidenceFresh", "Unknown", "Validating", "completeness=Unknown; no_observation"),
			gkaCCond("NodeEligible", "Unknown", "Validating", "eligibility=Unknown qualification=Unknown; no_observation"),
		},
	}
}

// GKA-044/045, GFL-128: the normative examples of spec section 12, and the
// cold-start statuses S3a writes, pass a real API server. This is the control
// for every negative case below.
func TestGKA044_NormativeExamplesAccepted(t *testing.T) {
	e := gkaCStart(t)
	for _, c := range []struct {
		kit    gkaCKit
		spec   map[string]any
		status []map[string]any
	}{
		{gkaCKits[0], gkaCFleetSpec(), []map[string]any{gkaCColdFleetStatus(), gkaCFleetStatus()}},
		{gkaCKits[1], gkaCDeviceSpec(), []map[string]any{gkaCColdDeviceStatus(), gkaCDeviceStatus()}},
		{gkaCKits[2], gkaCNPSSpec(), []map[string]any{gkaCColdNPSStatus(), gkaCNPSStatus()}},
	} {
		obj := gkaCCreate(t, e, c.kit.Res, gkaName(t, c.kit.Res.Singular), c.spec)
		for i, st := range c.status {
			obj = gkaCStatusReal(t, e, obj, st)
			if got, _ := gkaCGet(obj.Object, "status.observedGeneration"); got == nil {
				t.Errorf("GKA-045 %s status #%d: the stored object lost its status", c.kit.Res.Kind, i)
			}
		}
	}
}

// ------------------------------------------------------------ GKA-044: fleet

func gkaCLabels(n int) map[string]any {
	m := map[string]any{}
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("k%02d", i)] = "v"
	}
	return m
}

func gkaCExprs(n int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"key": fmt.Sprintf("k%02d", i), "operator": "Exists"})
	}
	return out
}

func gkaCSel(labels map[string]any, exprs []any) map[string]any {
	m := map[string]any{}
	if labels != nil {
		m["matchLabels"] = labels
	}
	if exprs != nil {
		m["matchExpressions"] = exprs
	}
	return m
}

const (
	gkaCMsgF1 = "nodeSelector must have at least one matchLabels or matchExpressions entry"
	gkaCMsgF2 = "mode Enforce requires a canarySelector with at least one matchLabels or matchExpressions entry"
	gkaCMsgF3 = "selectors may have at most 64 matchLabels and 64 matchExpressions entries"
)

// GKA-044 F1: nodeSelector needs at least one matchLabels or matchExpressions
// entry, on create and on update.
func TestGKA044_F1_NodeSelectorNotEmpty(t *testing.T) {
	e := gkaCStart(t)
	base := gkaCCreate(t, e, gkaCFleetRes, gkaName(t, "f1"), gkaCFleetSpec())
	dry := gkaName(t, "dry-f1")
	one := []any{map[string]any{"key": "a", "operator": "Exists"}}
	for _, c := range []struct {
		name string
		sel  map[string]any
		ok   bool
	}{
		{"empty object", gkaCSel(nil, nil), false},
		{"empty matchLabels", gkaCSel(map[string]any{}, nil), false},
		{"empty matchExpressions", gkaCSel(nil, []any{}), false},
		{"both empty", gkaCSel(map[string]any{}, []any{}), false},
		{"one label", gkaCSel(map[string]any{"a": "b"}, nil), true},
		{"one label with an empty value", gkaCSel(map[string]any{"a": ""}, nil), true},
		{"one expression", gkaCSel(nil, one), true},
		{"one expression In with values", gkaCSel(nil, []any{map[string]any{"key": "a", "operator": "In", "values": []any{"x", "y"}}}), true},
		{"empty labels plus one expression", gkaCSel(map[string]any{}, one), true},
		{"one label plus empty expressions", gkaCSel(map[string]any{"a": "b"}, []any{}), true},
		{"labels and expressions", gkaCSel(map[string]any{"a": "b"}, one), true},
	} {
		spec := gkaCFleetSpec()
		spec["nodeSelector"] = c.sel
		what := "GKA-044 F1 create: " + c.name
		if c.ok {
			gkaCOK(t, what, gkaCCreateDry(e, gkaCFleetRes, dry, spec))
		} else {
			gkaCInvalid(t, what, gkaCCreateDry(e, gkaCFleetRes, dry, spec), gkaCMsgF1)
		}
		err := gkaCSpecDry(e, base, func(s map[string]any) { s["nodeSelector"] = c.sel })
		what = "GKA-044 F1 update: " + c.name
		if c.ok {
			gkaCOK(t, what, err)
		} else {
			gkaCInvalid(t, what, err, gkaCMsgF1)
		}
	}
}

// GKA-044 F2: Enforce requires a non-empty canarySelector; Audit does not care.
func TestGKA044_F2_EnforceRequiresCanary(t *testing.T) {
	e := gkaCStart(t)
	dry := gkaName(t, "dry-f2")
	label := map[string]any{"matchLabels": map[string]any{"canary": "yes"}}
	expr := map[string]any{"matchExpressions": []any{map[string]any{"key": "canary", "operator": "Exists"}}}
	for _, c := range []struct {
		name   string
		mode   any // nil = key absent
		canary any // nil = key absent
		ok     bool
	}{
		{"Enforce without canary", "Enforce", nil, false},
		{"Enforce with empty canary object", "Enforce", map[string]any{}, false},
		{"Enforce with empty matchLabels", "Enforce", map[string]any{"matchLabels": map[string]any{}}, false},
		{"Enforce with empty matchExpressions", "Enforce", map[string]any{"matchExpressions": []any{}}, false},
		{"Enforce with both empty", "Enforce", map[string]any{"matchLabels": map[string]any{}, "matchExpressions": []any{}}, false},
		{"Enforce with a label", "Enforce", label, true},
		{"Enforce with an expression", "Enforce", expr, true},
		{"Enforce with labels and expressions", "Enforce", map[string]any{
			"matchLabels": map[string]any{"c": "y"}, "matchExpressions": []any{map[string]any{"key": "c", "operator": "Exists"}},
		}, true},
		{"Audit without canary", "Audit", nil, true},
		{"Audit with empty canary object", "Audit", map[string]any{}, true},
		{"Audit with a label canary", "Audit", label, true},
		{"mode absent (defaults to Audit) without canary", nil, nil, true},
	} {
		spec := gkaCFleetSpec()
		delete(spec, "mode")
		if c.mode != nil {
			spec["mode"] = c.mode
		}
		if c.canary != nil {
			spec["canarySelector"] = c.canary
		}
		what := "GKA-044 F2 create: " + c.name
		if c.ok {
			gkaCOK(t, what, gkaCCreateDry(e, gkaCFleetRes, dry, spec))
		} else {
			gkaCInvalid(t, what, gkaCCreateDry(e, gkaCFleetRes, dry, spec), gkaCMsgF2)
		}
	}
	// transitions on update
	audit := gkaCCreate(t, e, gkaCFleetRes, gkaName(t, "f2-audit"), gkaCFleetSpec())
	gkaCInvalid(t, "GKA-044 F2 update Audit->Enforce without canary",
		gkaCSpecDry(e, audit, func(s map[string]any) { s["mode"] = "Enforce" }), gkaCMsgF2)
	gkaCOK(t, "GKA-044 F2 update Audit->Enforce with canary",
		gkaCSpecDry(e, audit, func(s map[string]any) { s["mode"] = "Enforce"; s["canarySelector"] = label }))
	enfSpec := gkaCFleetSpec()
	enfSpec["mode"] = "Enforce"
	enfSpec["canarySelector"] = label
	enforce := gkaCCreate(t, e, gkaCFleetRes, gkaName(t, "f2-enforce"), enfSpec)
	gkaCInvalid(t, "GKA-044 F2 update Enforce: remove canary",
		gkaCSpecDry(e, enforce, func(s map[string]any) { delete(s, "canarySelector") }), gkaCMsgF2)
	gkaCInvalid(t, "GKA-044 F2 update Enforce: empty canary",
		gkaCSpecDry(e, enforce, func(s map[string]any) { s["canarySelector"] = map[string]any{} }), gkaCMsgF2)
	gkaCOK(t, "GKA-044 F2 update Enforce->Audit and remove canary",
		gkaCSpecDry(e, enforce, func(s map[string]any) { s["mode"] = "Audit"; delete(s, "canarySelector") }))
}

// GKA-044 F3: at most 64 matchLabels and 64 matchExpressions per selector (each
// list separately, not their sum).
func TestGKA044_F3_SelectorSizeLimits(t *testing.T) {
	e := gkaCStart(t)
	dry := gkaName(t, "dry-f3")
	canary := func(labels map[string]any, exprs []any) map[string]any { return gkaCSel(labels, exprs) }
	cases := []struct {
		name string
		mut  func(spec map[string]any)
		ok   bool
	}{
		{"nodeSelector.matchLabels 64", func(s map[string]any) { s["nodeSelector"] = gkaCSel(gkaCLabels(64), nil) }, true},
		{"nodeSelector.matchLabels 65", func(s map[string]any) { s["nodeSelector"] = gkaCSel(gkaCLabels(65), nil) }, false},
		{"nodeSelector.matchExpressions 64", func(s map[string]any) { s["nodeSelector"] = gkaCSel(nil, gkaCExprs(64)) }, true},
		{"nodeSelector.matchExpressions 65", func(s map[string]any) { s["nodeSelector"] = gkaCSel(nil, gkaCExprs(65)) }, false},
		{"nodeSelector 64 labels + 64 expressions (limits are per list)", func(s map[string]any) {
			s["nodeSelector"] = gkaCSel(gkaCLabels(64), gkaCExprs(64))
		}, true},
		{"nodeSelector 65 labels + 1 expression", func(s map[string]any) { s["nodeSelector"] = gkaCSel(gkaCLabels(65), gkaCExprs(1)) }, false},
		{"nodeSelector 1 label + 65 expressions", func(s map[string]any) { s["nodeSelector"] = gkaCSel(gkaCLabels(1), gkaCExprs(65)) }, false},
		{"canarySelector.matchLabels 64", func(s map[string]any) { s["canarySelector"] = canary(gkaCLabels(64), nil) }, true},
		{"canarySelector.matchLabels 65", func(s map[string]any) { s["canarySelector"] = canary(gkaCLabels(65), nil) }, false},
		{"canarySelector.matchExpressions 64", func(s map[string]any) { s["canarySelector"] = canary(nil, gkaCExprs(64)) }, true},
		{"canarySelector.matchExpressions 65", func(s map[string]any) { s["canarySelector"] = canary(nil, gkaCExprs(65)) }, false},
		{"all four lists at 64", func(s map[string]any) {
			s["nodeSelector"] = gkaCSel(gkaCLabels(64), gkaCExprs(64))
			s["canarySelector"] = canary(gkaCLabels(64), gkaCExprs(64))
		}, true},
		{"Enforce with canary at 65 labels", func(s map[string]any) {
			s["mode"] = "Enforce"
			s["canarySelector"] = canary(gkaCLabels(65), nil)
		}, false},
	}
	for _, c := range cases {
		spec := gkaCFleetSpec()
		c.mut(spec)
		what := "GKA-044 F3: " + c.name
		if c.ok {
			gkaCOK(t, what, gkaCCreateDry(e, gkaCFleetRes, dry, spec))
		} else {
			gkaCInvalid(t, what, gkaCCreateDry(e, gkaCFleetRes, dry, spec), gkaCMsgF3)
		}
	}
}

// -------------------------------------------------------- GKA-044: GPUDevice

const (
	gkaCMsgD1 = "spec.nodeRef is immutable"
	gkaCMsgD2 = "spec.inventoryClaim is immutable"
	gkaCMsgD3 = "a Retired GPUDevice cannot leave Retired; register a new GPUDevice"
	gkaCMsgD4 = "changing desiredState requires a new request.id"
	gkaCMsgD5 = "inventoryClaim requires uuid or serial"
	gkaCMsgN1 = "spec.nodeRef is immutable"
)

// GKA-044 D1, D2: nodeRef and inventoryClaim are immutable; fleetRef and the
// request reason are not.
func TestGKA044_D1D2_Immutability(t *testing.T) {
	e := gkaCStart(t)
	base := gkaCCreate(t, e, gkaCDevRes, gkaName(t, "d12"), gkaCDeviceSpec())
	set := func(path string, v any) func(map[string]any) {
		return func(s map[string]any) { gkaCSet(s, path, v) }
	}
	for _, c := range []struct {
		name string
		mut  func(map[string]any)
		want string // "" = accepted
	}{
		{"D1 nodeRef.name", set("nodeRef.name", "other-node"), gkaCMsgD1},
		{"D1 nodeRef.uid", set("nodeRef.uid", "other-uid"), gkaCMsgD1},
		{"D1 nodeRef.name and uid", func(s map[string]any) { s["nodeRef"] = gkaCRef("other", "other") }, gkaCMsgD1},
		{"D2 inventoryClaim.vendor", set("inventoryClaim.vendor", "AMD"), gkaCMsgD2},
		{"D2 inventoryClaim.uuid", set("inventoryClaim.uuid", "GPU-other"), gkaCMsgD2},
		{"D2 inventoryClaim.serial added", set("inventoryClaim.serial", "SN-1"), gkaCMsgD2},
		{"D2 inventoryClaim.source", set("inventoryClaim.source", "other-source"), gkaCMsgD2},
		{"D2 inventoryClaim.evidenceID", set("inventoryClaim.evidenceID", "other-evidence"), gkaCMsgD2},
		{"unchanged spec", func(map[string]any) {}, ""},
		{"fleetRef.name may change", set("fleetRef.name", "other-fleet"), ""},
		{"fleetRef.uid may change", set("fleetRef.uid", "other-fleet-uid"), ""},
		{"request.reason may change", set("request.reason", "edited"), ""},
	} {
		err := gkaCSpecDry(e, base, c.mut)
		if c.want == "" {
			gkaCOK(t, "GKA-044 "+c.name, err)
		} else {
			gkaCInvalid(t, "GKA-044 "+c.name, err, c.want)
		}
	}
}

// GKA-044 D3, D4: Retired is terminal; a desiredState change needs a new id.
func TestGKA044_D3D4_DesiredStateTransitions(t *testing.T) {
	e := gkaCStart(t)
	live := gkaCCreate(t, e, gkaCDevRes, gkaName(t, "d4-live"), gkaCDeviceSpec())
	retiredSpec := gkaCDeviceSpec()
	retiredSpec["desiredState"] = "Retired"
	retiredSpec["request"] = map[string]any{"id": "retire-1", "reason": "decommissioned"}
	retired := gkaCCreate(t, e, gkaCDevRes, gkaName(t, "d3-retired"), retiredSpec)

	change := func(state, id string) func(map[string]any) {
		return func(s map[string]any) {
			s["desiredState"] = state
			gkaCSet(s, "request.id", id)
		}
	}
	for _, c := range []struct {
		name    string
		retired bool // the persisted Retired device instead of the InService one
		mut     func(map[string]any)
		want    []string // nil = accepted
	}{
		{"D4 InService->Maintenance with the same request.id", false, change("Maintenance", "enroll-1"), []string{gkaCMsgD4}},
		{"D4 InService->Retired with the same request.id", false, change("Retired", "enroll-1"), []string{gkaCMsgD4}},
		{"D4 InService->Maintenance with a new request.id", false, change("Maintenance", "maint-1"), nil},
		{"D4 InService->Retired with a new request.id", false, change("Retired", "retire-2"), nil},
		{"D4 same state with a new request.id", false, change("InService", "enroll-2"), nil},
		{"D4 same state and same request.id", false, change("InService", "enroll-1"), nil},
		{"D3 Retired->InService with a new request.id", true, change("InService", "back-1"), []string{gkaCMsgD3}},
		{"D3 Retired->Maintenance with a new request.id", true, change("Maintenance", "back-2"), []string{gkaCMsgD3}},
		{"D3+D4 Retired->InService with the same request.id", true, change("InService", "retire-1"), []string{gkaCMsgD3, gkaCMsgD4}},
		{"Retired stays Retired with a new request.id", true, change("Retired", "retire-2"), nil},
		{"Retired stays Retired, reason edited", true, func(s map[string]any) { gkaCSet(s, "request.reason", "edited") }, nil},
	} {
		base := live
		if c.retired {
			base = retired
		}
		err := gkaCSpecDry(e, base, c.mut)
		if c.want == nil {
			gkaCOK(t, "GKA-044 "+c.name, err)
		} else {
			gkaCInvalid(t, "GKA-044 "+c.name, err, c.want...)
		}
	}
}

// GKA-044 D5: an inventory claim needs a uuid or a serial.
func TestGKA044_D5_ClaimNeedsUUIDOrSerial(t *testing.T) {
	e := gkaCStart(t)
	dry := gkaName(t, "dry-d5")
	for _, c := range []struct {
		name         string
		uuid, serial any
		ok           bool
	}{
		{"neither uuid nor serial", nil, nil, false},
		{"uuid only", "GPU-1", nil, true},
		{"serial only", nil, "SN-1", true},
		{"uuid and serial", "GPU-1", "SN-1", true},
	} {
		spec := gkaCDeviceSpec()
		claim := spec["inventoryClaim"].(map[string]any)
		delete(claim, "uuid")
		if c.uuid != nil {
			claim["uuid"] = c.uuid
		}
		if c.serial != nil {
			claim["serial"] = c.serial
		}
		what := "GKA-044 D5: " + c.name
		if c.ok {
			gkaCOK(t, what, gkaCCreateDry(e, gkaCDevRes, dry, spec))
		} else {
			gkaCInvalid(t, what, gkaCCreateDry(e, gkaCDevRes, dry, spec), gkaCMsgD5)
		}
	}
}

// GKA-044 N1: NodePathState.spec.nodeRef is immutable.
func TestGKA044_N1_NodePathStateNodeRefImmutable(t *testing.T) {
	e := gkaCStart(t)
	base := gkaCCreate(t, e, gkaCNPSRes, gkaName(t, "n1"), gkaCNPSSpec())
	for _, c := range []struct {
		name string
		mut  func(map[string]any)
		ok   bool
	}{
		{"nodeRef.name", func(s map[string]any) { gkaCSet(s, "nodeRef.name", "other") }, false},
		{"nodeRef.uid", func(s map[string]any) { gkaCSet(s, "nodeRef.uid", "other") }, false},
		{"unchanged", func(map[string]any) {}, true},
	} {
		err := gkaCSpecDry(e, base, c.mut)
		if c.ok {
			gkaCOK(t, "GKA-044 N1 "+c.name, err)
		} else {
			gkaCInvalid(t, "GKA-044 N1 "+c.name, err, gkaCMsgN1)
		}
	}
	// nodeRef is free at create time: N1 is a transition rule.
	spec := gkaCNPSSpec()
	gkaCSet(spec, "nodeRef.name", "any-node")
	gkaCOK(t, "GKA-044 N1: create with an arbitrary nodeRef", gkaCCreateDry(e, gkaCNPSRes, gkaName(t, "dry-n1"), spec))
}

// ------------------------------------------------------ GKA-045: status rules

const (
	gkaCMsgS1 = "actualBinding.state Bound requires vendor, uuid, nodeUID, bootID, bdf, functionKey, source, evidenceID, observedAt and expiresAt"
	gkaCMsgS2 = "allocation.state Empty or InUse requires profile, observedAt, expiresAt and evidenceRefs"
	gkaCMsgS3 = "coverage.state Normal requires observedAt, latestObservedAt, expiresAt and evidenceRefs"
	gkaCMsgS4 = "graphRevision is required when qualification is Qualified or Disqualified"
	gkaCMsgS5 = "graphRevision is required when evidenceCompleteness is Complete or Partial"
	gkaCMsgS6 = "assessmentRevision is required when FleetReady is True"
)

// GKA-045 S1: a Bound binding carries all of its ten members.
func TestGKA045_S1_BoundBindingNeedsIdentity(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpudevices")
	base := gkaCBaseObject(t, e, k)
	dry := gkaName(t, "dry-dev")
	for _, f := range []string{"vendor", "uuid", "nodeUID", "bootID", "bdf", "functionKey", "source", "evidenceID", "observedAt", "expiresAt"} {
		gkaCInvalid(t, "GKA-045 S1 Bound without "+f,
			gkaCTry(e, k, base, dry, "status.actualBinding."+f, nil, true), gkaCMsgS1)
	}
	gkaCOK(t, "GKA-045 S1 Bound without serial (serial is not required)", gkaCTry(e, k, base, dry, "status.actualBinding.serial", nil, true))
	minimal := func(state string) map[string]any { return map[string]any{"state": state, "reason": "Validating"} }
	gkaCInvalid(t, "GKA-045 S1 Bound with state and reason only", gkaCTry(e, k, base, dry, "status.actualBinding", minimal("Bound"), false), gkaCMsgS1)
	for _, s := range []string{"Unknown", "Conflict"} {
		gkaCOK(t, "GKA-045 S1 "+s+" with state and reason only", gkaCTry(e, k, base, dry, "status.actualBinding", minimal(s), false))
	}
	for _, s := range []string{"Unknown", "Conflict"} {
		gkaCOK(t, "GKA-045 S1 "+s+" with every member present", gkaCTry(e, k, base, dry, "status.actualBinding.state", s, false))
	}
}

// GKA-045 S2: Empty and InUse allocations carry profile, times and evidence.
func TestGKA045_S2_KnownAllocationNeedsDetail(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpudevices")
	base := gkaCBaseObject(t, e, k)
	dry := gkaName(t, "dry-dev")
	for _, state := range []string{"Empty", "InUse"} {
		for _, f := range []string{"profile", "observedAt", "expiresAt"} {
			st := k.Status()
			gkaCSet(st, "allocation.state", state)
			gkaCDel(st, "allocation."+f)
			gkaCInvalid(t, fmt.Sprintf("GKA-045 S2 %s without %s", state, f), gkaCStatusDry(e, base, st), gkaCMsgS2)
		}
		st := k.Status()
		gkaCSet(st, "allocation.state", state)
		gkaCSet(st, "allocation.evidenceRefs", []any{})
		gkaCInvalid(t, "GKA-045 S2 "+state+" with empty evidenceRefs", gkaCStatusDry(e, base, st), gkaCMsgS2)
		gkaCOK(t, "GKA-045 S2 "+state+" with every member", gkaCTry(e, k, base, dry, "status.allocation.state", state, false))
	}
	unknown := map[string]any{"state": "Unknown", "reason": "AllocationUnknown", "evidenceRefs": []any{}, "affectedWorkloads": []any{}}
	gkaCOK(t, "GKA-045 S2 Unknown without profile, times and evidence", gkaCTry(e, k, base, dry, "status.allocation", unknown, false))
}

// GKA-045 S3: a Normal coverage row carries its times and evidence.
func TestGKA045_S3_NormalCoverageNeedsEvidence(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpudevices")
	base := gkaCBaseObject(t, e, k)
	dry := gkaName(t, "dry-dev")
	for _, f := range []string{"observedAt", "latestObservedAt", "expiresAt"} {
		gkaCInvalid(t, "GKA-045 S3 Normal without "+f, gkaCTry(e, k, base, dry, "status.coverage[]."+f, nil, true), gkaCMsgS3)
	}
	gkaCInvalid(t, "GKA-045 S3 Normal with empty evidenceRefs",
		gkaCTry(e, k, base, dry, "status.coverage[].evidenceRefs", []any{}, false), gkaCMsgS3)
	for _, state := range []string{"Missing", "Unknown", "Unsupported"} {
		row := map[string]any{"name": "pcie-parent", "pathKind": "gpu-pcie-parent", "state": state, "reason": "CoverageMissing", "evidenceRefs": []any{}}
		gkaCOK(t, "GKA-045 S3 "+state+" without times and evidence", gkaCTry(e, k, base, dry, "status.coverage", []any{row}, false))
	}
	gkaCOK(t, "GKA-045 S3 Normal with every member", gkaCTry(e, k, base, dry, "status.coverage[].state", "Normal", false))
	// the rule applies to every element, not only the first.
	second := map[string]any{"name": "pcie-root", "pathKind": "gpu-pcie-root", "state": "Normal", "reason": "Normal", "evidenceRefs": []any{}}
	first, _ := gkaCGet(k.Status(), "coverage[]")
	gkaCInvalid(t, "GKA-045 S3 second element Normal without evidence",
		gkaCTry(e, k, base, dry, "status.coverage", []any{first, second}, false), gkaCMsgS3)
}

// GKA-045 S4, S5: graphRevision accompanies any Qualified/Disqualified device
// and any Complete/Partial node.
func TestGKA045_S4S5_GraphRevisionRequired(t *testing.T) {
	e := gkaCStart(t)
	dev := gkaCKitOf("gpudevices")
	devBase := gkaCBaseObject(t, e, dev)
	dry := gkaName(t, "dry-x")
	for _, q := range []string{"Qualified", "Disqualified"} {
		st := dev.Status()
		st["qualification"] = q
		gkaCDel(st, "graphRevision")
		gkaCInvalid(t, "GKA-045 S4 "+q+" without graphRevision", gkaCStatusDry(e, devBase, st), gkaCMsgS4)
		gkaCOK(t, "GKA-045 S4 "+q+" with graphRevision", gkaCTry(e, dev, devBase, dry, "status.qualification", q, false))
	}
	st := dev.Status()
	st["qualification"] = "Unknown"
	gkaCDel(st, "graphRevision")
	gkaCOK(t, "GKA-045 S4 Unknown without graphRevision", gkaCStatusDry(e, devBase, st))

	nps := gkaCKitOf("nodepathstates")
	npsBase := gkaCBaseObject(t, e, nps)
	for _, c := range []string{"Complete", "Partial"} {
		st := nps.Status()
		st["evidenceCompleteness"] = c
		gkaCDel(st, "graphRevision")
		gkaCInvalid(t, "GKA-045 S5 "+c+" without graphRevision", gkaCStatusDry(e, npsBase, st), gkaCMsgS5)
		gkaCOK(t, "GKA-045 S5 "+c+" with graphRevision", gkaCTry(e, nps, npsBase, dry, "status.evidenceCompleteness", c, false))
	}
	st = nps.Status()
	st["evidenceCompleteness"] = "Unknown"
	gkaCDel(st, "graphRevision")
	gkaCOK(t, "GKA-045 S5 Unknown without graphRevision", gkaCStatusDry(e, npsBase, st))
}

// GKA-045 S6: FleetReady=True needs assessmentRevision; other statuses and
// other types do not.
func TestGKA045_S6_FleetReadyNeedsAssessmentRevision(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpufleets")
	base := gkaCBaseObject(t, e, k)
	withConds := func(revision bool, conds ...map[string]any) map[string]any {
		st := k.Status()
		if !revision {
			gkaCDel(st, "assessmentRevision")
		}
		l := make([]any, 0, len(conds))
		for _, c := range conds {
			l = append(l, c)
		}
		st["conditions"] = l
		return st
	}
	ready := func(status string) map[string]any { return gkaCCond("FleetReady", status, "Ready", "m") }
	gkaCInvalid(t, "GKA-045 S6 FleetReady=True without assessmentRevision", gkaCStatusDry(e, base, withConds(false, ready("True"))), gkaCMsgS6)
	gkaCInvalid(t, "GKA-045 S6 FleetReady=True among other conditions without assessmentRevision",
		gkaCStatusDry(e, base, withConds(false, gkaCCond("CleanupReady", "False", "CleanupPending", "m"), ready("True"))), gkaCMsgS6)
	gkaCOK(t, "GKA-045 S6 FleetReady=True with assessmentRevision", gkaCStatusDry(e, base, withConds(true, ready("True"))))
	gkaCOK(t, "GKA-045 S6 FleetReady=False without assessmentRevision", gkaCStatusDry(e, base, withConds(false, ready("False"))))
	gkaCOK(t, "GKA-045 S6 FleetReady=Unknown without assessmentRevision", gkaCStatusDry(e, base, withConds(false, ready("Unknown"))))
	gkaCOK(t, "GKA-045 S6 GateOwned=True without assessmentRevision", gkaCStatusDry(e, base, withConds(false, gkaCCond("GateOwned", "True", "Ready", "m"))))
	gkaCOK(t, "GKA-045 S6 no conditions without assessmentRevision", gkaCStatusDry(e, base, withConds(false)))
}

// -------------------------------------------------- GKA-046: conditions rule

func gkaCCondStatus(k gkaCKit, conds ...map[string]any) map[string]any {
	st := k.Status()
	l := make([]any, 0, len(conds))
	for _, c := range conds {
		l = append(l, c)
	}
	st["conditions"] = l
	return st
}

// GKA-046: per resource, only its own condition types are accepted.
func TestGKA046_ConditionTypesPerResource(t *testing.T) {
	e := gkaCStart(t)
	union := map[string]bool{}
	for _, ts := range gkaCCondTypes {
		for _, x := range ts {
			union[x] = true
		}
	}
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		own := map[string]bool{}
		for _, ty := range gkaCCondTypes[k.Res.Plural] {
			own[ty] = true
			gkaCOK(t, fmt.Sprintf("GKA-046 %s allows type %s", k.Res.Kind, ty),
				gkaCStatusDry(e, base, gkaCCondStatus(k, gkaCCond(ty, "Unknown", "Validating", "m"))))
		}
		var others []string
		for ty := range union {
			if !own[ty] {
				others = append(others, ty)
			}
		}
		others = append(others, "Ready", "DataPathGPUFleetReady", "fleetready", "FleetReady2", "Type00")
		for _, ty := range others {
			gkaCInvalid(t, fmt.Sprintf("GKA-046 %s rejects type %s", k.Res.Kind, ty),
				gkaCStatusDry(e, base, gkaCCondStatus(k, gkaCCond(ty, "Unknown", "Validating", "m"))), gkaCCondMessage)
		}
		// one bad entry among good ones rejects the whole list.
		good := gkaCCond(gkaCCondTypes[k.Res.Plural][0], "Unknown", "Validating", "m")
		bad := gkaCCond("NotAType", "Unknown", "Validating", "m")
		gkaCInvalid(t, "GKA-046 "+k.Res.Kind+" one disallowed type among allowed ones",
			gkaCStatusDry(e, base, gkaCCondStatus(k, good, bad)), gkaCCondMessage)
	}
}

// GKA-046: reasons are exactly the 22 V22 values.
func TestGKA046_ConditionReasonsAreV22(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		ty := gkaCCondTypes[k.Res.Plural][0]
		if ty == "FleetReady" {
			ty = "GateOwned" // FleetReady=True has an extra rule (S6); use a neutral type here
		}
		for _, r := range gkaCV22 {
			gkaCOK(t, fmt.Sprintf("GKA-046 %s reason %s", k.Res.Kind, r),
				gkaCStatusDry(e, base, gkaCCondStatus(k, gkaCCond(ty, "Unknown", r, "m"))))
		}
		for _, r := range []string{"Foo", "ready", "READY", "Normal", "TopologyConflict", "Unsupported", "Ready2", "NotAReason", "Validating2", "internalerror"} {
			gkaCInvalid(t, fmt.Sprintf("GKA-046 %s reason %s", k.Res.Kind, r),
				gkaCStatusDry(e, base, gkaCCondStatus(k, gkaCCond(ty, "Unknown", r, "m"))), gkaCCondMessage)
		}
	}
}

// GKA-046: observedGeneration must be present; message is 1..1024 characters.
func TestGKA046_ConditionGenerationAndMessage(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		ty := gkaCCondTypes[k.Res.Plural][0]
		if ty == "FleetReady" {
			ty = "GateOwned"
		}
		try := func(mut func(c map[string]any)) error {
			c := gkaCCond(ty, "Unknown", "Validating", "m")
			mut(c)
			return gkaCStatusDry(e, base, gkaCCondStatus(k, c))
		}
		what := "GKA-046 " + k.Res.Kind
		gkaCInvalid(t, what+" without observedGeneration", try(func(c map[string]any) { delete(c, "observedGeneration") }), gkaCCondMessage)
		gkaCOK(t, what+" observedGeneration 0", try(func(c map[string]any) { c["observedGeneration"] = 0 }))
		gkaCOK(t, what+" observedGeneration 7", try(func(c map[string]any) { c["observedGeneration"] = 7 }))
		gkaCInvalid(t, what+" empty message", try(func(c map[string]any) { c["message"] = "" }), gkaCCondMessage)
		gkaCOK(t, what+" message of 1 char", try(func(c map[string]any) { c["message"] = "x" }))
		gkaCOK(t, what+" message of 1024 chars", try(func(c map[string]any) { c["message"] = strings.Repeat("a", 1024) }))
		gkaCInvalid(t, what+" message of 1025 chars", try(func(c map[string]any) { c["message"] = strings.Repeat("a", 1025) }), gkaCCondMessage)
		gkaCOK(t, what+" message of 1024 multibyte chars", try(func(c map[string]any) { c["message"] = strings.Repeat("가", 1024) }))
		gkaCInvalid(t, what+" message of 1025 multibyte chars", try(func(c map[string]any) { c["message"] = strings.Repeat("가", 1025) }), gkaCCondMessage)
		gkaCInvalid(t, what+" status Maybe", try(func(c map[string]any) { c["status"] = "Maybe" }), "Unsupported value")
		gkaCInvalid(t, what+" status true (lower case)", try(func(c map[string]any) { c["status"] = "true" }), "Unsupported value")
		gkaCInvalid(t, what+" empty reason", try(func(c map[string]any) { c["reason"] = "" }))
		gkaCInvalid(t, what+" reason with a space", try(func(c map[string]any) { c["reason"] = "Not Ready" }))
		gkaCInvalid(t, what+" lastTransitionTime not a timestamp", try(func(c map[string]any) { c["lastTransitionTime"] = "yesterday" }))
		for _, f := range []string{"type", "status", "lastTransitionTime", "reason", "message"} {
			f := f
			gkaCInvalid(t, what+" without "+f, try(func(c map[string]any) { delete(c, f) }), "Required value")
		}
	}
}

// GKA-045/GKA-046: the rules also apply to the status subresource of a fresh
// object created through the real (non-dry) path, and a rejected update leaves
// the stored object unchanged.
func TestGKA045_RejectedStatusUpdateChangesNothing(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpufleets")
	base := gkaCBaseObject(t, e, k)
	bad := k.Status()
	gkaCDel(bad, "assessmentRevision") // FleetReady=True without a revision violates S6
	cp := base.DeepCopy()
	cp.Object["status"] = gkaCClone(bad)
	ctx, cancel := gkaCCtx()
	defer cancel()
	gkaCInvalid(t, "GKA-045 S6 real status update", e.Client.Status().Update(ctx, cp), gkaCMsgS6)
	after := gkaCGetObj(t, e, k.Res, base.GetName())
	if after.GetResourceVersion() != base.GetResourceVersion() {
		t.Errorf("GKA-045: a rejected status update changed resourceVersion %s -> %s", base.GetResourceVersion(), after.GetResourceVersion())
	}
	if _, has := after.Object["status"]; has {
		t.Errorf("GKA-045: a rejected status update left a status behind: %v", after.Object["status"])
	}
}

// GKA-044/K-T-16: the manifest does not police the inside of a label selector
// (operator, values, key format). A selector the API server accepts but
// LabelSelectorAsSelector rejects is a runtime case (GKA-066 InternalError), so
// the CRD must let it through as long as the entry-count rules hold.
func TestGKA044_SelectorContentIsNotPolicedByTheManifest(t *testing.T) {
	e := gkaCStart(t)
	dry := gkaName(t, "dry-sel")
	expr := func(key, op string, values ...any) map[string]any {
		m := map[string]any{"key": key, "operator": op}
		if values != nil {
			m["values"] = values
		}
		return m
	}
	for _, c := range []struct {
		name string
		sel  map[string]any
	}{
		{"unknown operator", gkaCSel(nil, []any{expr("a", "Bogus")})},
		{"In without values", gkaCSel(nil, []any{expr("a", "In")})},
		{"Exists with values", gkaCSel(nil, []any{expr("a", "Exists", "x")})},
		{"lower-case operator", gkaCSel(nil, []any{expr("a", "in", "x")})},
		{"key that is not a valid label key", gkaCSel(nil, []any{expr("not a key!", "Exists")})},
		{"empty key", gkaCSel(nil, []any{expr("", "Exists")})},
		{"matchLabels value that is not a valid label value", gkaCSel(map[string]any{"a": "has spaces and !"}, nil)},
		{"matchLabels key that is not a valid label key", gkaCSel(map[string]any{"bad key": "v"}, nil)},
		{"very long value", gkaCSel(map[string]any{"a": strings.Repeat("v", 300)}, nil)},
	} {
		spec := gkaCFleetSpec()
		spec["nodeSelector"] = c.sel
		gkaCOK(t, "GKA-044/K-T-16 nodeSelector with "+c.name, gkaCCreateDry(e, gkaCFleetRes, dry, spec))
	}
}
