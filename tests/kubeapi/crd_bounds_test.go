package kubeapi_test

// API-server behavior of the schema bounds (GKA-041, GKA-042). Negative cases
// use server-side dry-run against one persisted base object per resource, so
// each case costs one request and leaves nothing behind. Boundaries are tested
// exactly at the limit and one past it.

import (
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// gkaCTry validates the kit's base object with the member at path set to value
// (or removed when del). spec.* paths go through dry-run create, status.*
// paths through dry-run status update of base.
func gkaCTry(e *gkaEnvT, k gkaCKit, base *unstructured.Unstructured, dryName, path string, value any, del bool) error {
	if rest, ok := strings.CutPrefix(path, "spec."); ok {
		spec := k.Spec()
		if del {
			gkaCDel(spec, rest)
		} else {
			gkaCSet(spec, rest, value)
		}
		return gkaCCreateDry(e, k.Res, dryName, spec)
	}
	rest, ok := strings.CutPrefix(path, "status.")
	if !ok {
		panic("gkaC: path must start with spec. or status.: " + path)
	}
	st := k.Status()
	if del {
		gkaCDel(st, rest)
	} else {
		gkaCSet(st, rest, value)
	}
	return gkaCStatusDry(e, base, st)
}

func gkaCRows[T any](rows []T, plural func(T) string, want string) []T {
	var out []T
	for _, r := range rows {
		if plural(r) == want {
			out = append(out, r)
		}
	}
	return out
}

// GKA-042: every bounded scalar at exactly its limit and one past it.
func TestGKA042_BoundaryValues(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		gkaCOK(t, "GKA-042 control: base spec of "+k.Res.Kind, gkaCCreateDry(e, k.Res, dry, k.Spec()))
		for _, b := range gkaCRows(gkaCBounds, func(b gkaCBound) string { return b.Plural }, k.Res.Plural) {
			what := fmt.Sprintf("GKA-042 %s %s", k.Res.Plural, b.Path)
			ep := gkaCErrPath(b.Path)
			try := func(v any) error { return gkaCTry(e, k, base, dry, b.Path, v, false) }
			if b.Int {
				gkaCOK(t, what+" = minimum", try(b.Min))
				gkaCInvalid(t, what+" = minimum-1", try(b.Min-1), ep)
				switch {
				case !b.NoMax:
					gkaCOK(t, what+" = maximum", try(b.Max))
					gkaCInvalid(t, what+" = maximum+1", try(b.Max+1), ep)
				case !strings.HasSuffix(b.Path, "collectorSession"):
					// int32 members without a declared maximum: the largest int32 is accepted.
					// (The API server does not enforce the int32 range of a CRD integer, so
					// nothing is asserted beyond it: the spec declares only the minimum.)
					gkaCOK(t, what+" = 2147483647", try(int64(2147483647)))
				}
				continue
			}
			gkaCOK(t, fmt.Sprintf("%s at %d chars", what, b.Max), try(strings.Repeat("a", int(b.Max))))
			gkaCInvalid(t, fmt.Sprintf("%s at %d chars", what, b.Max+1), try(strings.Repeat("a", int(b.Max)+1)), ep)
			if b.Min >= 1 {
				gkaCInvalid(t, what+" empty", try(""), ep)
			} else {
				gkaCOK(t, what+" empty (minLength 0)", try(""))
			}
		}
	}
}

// GKA-042: maxLength and minLength count characters, not bytes.
func TestGKA042_LengthCountsCharacters(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpudevices")
	base := gkaCBaseObject(t, e, k)
	dry := gkaName(t, "dry-dev")
	for _, c := range []struct {
		path string
		max  int
	}{{"spec.request.reason", 512}, {"spec.nodeRef.uid", 128}, {"spec.inventoryClaim.source", 256}} {
		try := func(n int) error {
			return gkaCTry(e, k, base, dry, c.path, strings.Repeat("가", n), false) // U+AC00 is 3 bytes
		}
		gkaCOK(t, fmt.Sprintf("GKA-042 %s at %d multibyte characters", c.path, c.max), try(c.max))
		gkaCInvalid(t, fmt.Sprintf("GKA-042 %s at %d multibyte characters", c.path, c.max+1), try(c.max+1), gkaCErrPath(c.path))
	}
}

// GKA-042: names of requiredCoverage and coverage follow ^[A-Za-z0-9._-]+$.
func TestGKA042_CoverageNamePattern(t *testing.T) {
	e := gkaCStart(t)
	valid := []string{"a", "A", "0", ".", "_", "-", "pcie-parent", "A.b_c-D9", "..", "-.-"}
	invalid := []string{"a b", " a", "a ", "a/b", "a:b", "a@b", "가", "a\n", "a\tb", "a,b", "a*"}
	for _, b := range gkaCBounds {
		if !b.Pattern {
			continue
		}
		k := gkaCKitOf(b.Plural)
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		for _, v := range valid {
			gkaCOK(t, fmt.Sprintf("GKA-042 %s %s = %q", b.Plural, b.Path, v), gkaCTry(e, k, base, dry, b.Path, v, false))
		}
		for _, v := range invalid {
			gkaCInvalid(t, fmt.Sprintf("GKA-042 %s %s = %q", b.Plural, b.Path, v), gkaCTry(e, k, base, dry, b.Path, v, false), gkaCErrPath(b.Path))
		}
	}
}

// GKA-042: JSON types are enforced (no coercion of strings to numbers, no
// fractional integers, no null for required members).
func TestGKA042_ValueTypes(t *testing.T) {
	e := gkaCStart(t)
	k := gkaCKitOf("gpufleets")
	base := gkaCBaseObject(t, e, k)
	dry := gkaName(t, "dry-fleet")
	for _, c := range []struct {
		name  string
		path  string
		value any
	}{
		{"string for int32", "spec.freshnessSeconds", "60"},
		{"fractional for int32", "spec.freshnessSeconds", 60.5},
		{"bool for int32", "spec.freshnessSeconds", true},
		{"null for required int32", "spec.readyForSeconds", nil},
		{"object for int32", "spec.readyForSeconds", map[string]any{}},
		{"string for bool", "spec.requiredCoverage[].required", "true"},
		{"number for bool", "spec.requiredCoverage[].required", 1},
		{"number for string", "spec.requiredCoverage[].name", 7},
		{"list for object", "spec.nodeSelector", []any{}},
		{"object for list", "spec.requiredCoverage", map[string]any{}},
		{"string for object", "spec.nodeSelector", "x"},
		{"number for enum string", "spec.mode", 1},
	} {
		gkaCInvalid(t, "GKA-042 "+c.name, gkaCTry(e, k, base, dry, c.path, c.value, false))
	}
	gkaCInvalid(t, "GKA-042 status: string for int32", gkaCTry(e, k, base, dry, "status.selectedCount", "1", false), "status.selectedCount")
	gkaCInvalid(t, "GKA-042 status: fractional int", gkaCTry(e, k, base, dry, "status.selectedCount", 1.5, false), "status.selectedCount")
}

// GKA-042: every enum accepts its value set and rejects empty, case variants,
// a suffixed typo and a leading space.
func TestGKA042_EnumValues(t *testing.T) {
	e := gkaCStart(t)
	extra := map[string][]string{
		"spec.mode":                                               {"Cleanup", "audit "},
		"status.gateOwnership.effect":                             {"NoExecute", "PreferNoSchedule"},
		"spec.desiredState":                                       {"Retiring", "Unknown"},
		"status.gateOwnership.cleanupPhase":                       {"Cleanup", "none"},
		"status.evidenceCompleteness":                             {"Incomplete"},
		"status.actualBinding.state":                              {"BindingBound"},
		"status.allocation.state":                                 {"Known"},
		"status.lifecyclePhase":                                   {"Ready ", "Draining"},
		"status.qualification":                                    {"Ok"},
		"status.coverage[].state":                                 {"Degraded"},
		"spec.requiredCoverage[].pathKind":                        {"gpu-pcie-link-width", "gpu_pcie_parent"},
		"status.coverage[].pathKind":                              {"GPU-PCIE-PARENT"},
		"status.deviceSummaries[].desiredState":                   {"InService "},
		"status.deviceSummaries[].qualification":                  {"qualified"},
		"status.deviceSummaries[].lifecyclePhase":                 {"pending"},
		"status.gateOwnership.policy.requiredCoverage[].pathKind": {"nic-lldp"},
	}
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		for _, en := range gkaCRows(gkaCEnums, func(x gkaCEnum) string { return x.Plural }, k.Res.Plural) {
			valid := map[string]bool{}
			for _, v := range en.Values {
				valid[v] = true
				what := fmt.Sprintf("GKA-014/042 %s %s = %q", en.Plural, en.Path, v)
				if en.Path == "spec.mode" && v == "Enforce" {
					// Enforce is a valid value; rule F2 additionally wants a canarySelector.
					spec := k.Spec()
					spec["mode"] = v
					spec["canarySelector"] = map[string]any{"matchLabels": map[string]any{"canary": "yes"}}
					gkaCOK(t, what, gkaCCreateDry(e, k.Res, dry, spec))
					continue
				}
				gkaCOK(t, what, gkaCTry(e, k, base, dry, en.Path, v, false))
			}
			v0 := en.Values[0]
			bad := append([]string{"", strings.ToLower(v0), strings.ToUpper(v0), v0 + "x", " " + v0, "x" + v0}, extra[en.Path]...)
			for _, v := range bad {
				if valid[v] {
					continue // a variant that is itself a member of the set
				}
				gkaCInvalid(t, fmt.Sprintf("GKA-014/042 %s %s = %q", en.Plural, en.Path, v),
					gkaCTry(e, k, base, dry, en.Path, v, false), "Unsupported value")
			}
		}
	}
}

// GKA-042: `required` is every non-pointer field; a missing one is rejected.
func TestGKA042_RequiredFields(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		// root: spec is required.
		noSpec := gkaCUnstructured(map[string]any{
			"apiVersion": gkaCAPIVersion, "kind": k.Res.Kind, "metadata": map[string]any{"name": dry},
		})
		ctx, cancel := gkaCCtx()
		err := e.Client.Create(ctx, noSpec, client.DryRunAll)
		cancel()
		gkaCInvalid(t, "GKA-042 "+k.Res.Kind+" without spec", err, "spec", "Required value")
		for _, s := range gkaCRows(gkaCShapes, func(s gkaCShape) string { return s.Plural }, k.Res.Plural) {
			if s.Path == "" {
				continue
			}
			for _, f := range s.Required {
				path := s.Path + "." + f
				gkaCInvalid(t, fmt.Sprintf("GKA-042 %s without required %s", k.Res.Plural, path),
					gkaCTry(e, k, base, dry, path, nil, true), "Required value")
			}
		}
	}
}

// GKA-042/K-T-04: optional members that no rule constrains may be absent.
func TestGKA042_OptionalFieldsMayBeAbsent(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		for _, o := range gkaCOptional {
			if o.Plural != k.Res.Plural {
				continue
			}
			gkaCOK(t, fmt.Sprintf("GKA-042 %s without optional %s", o.Plural, o.Path), gkaCTry(e, k, base, dry, o.Path, nil, true))
		}
	}
}

// GKA-012/042: a typed client that leaves Mode empty gets the server default
// Audit; so do an absent and a null mode; an empty string is rejected.
func TestGKA042_ModeDefaultsToAudit(t *testing.T) {
	e := gkaCStart(t)
	typedName := gkaName(t, "typed")
	typed := &v1alpha1.GPUFleet{
		ObjectMeta: metav1.ObjectMeta{Name: typedName},
		Spec: v1alpha1.GPUFleetSpec{
			NodeSelector:     metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}},
			RequiredCoverage: []v1alpha1.FleetCoverageRequirement{{Name: "c", PathKind: "gpu-pcie-parent", Required: true}},
			FreshnessSeconds: 60,
			ReadyForSeconds:  30,
		},
	}
	ctx, cancel := gkaCCtx()
	defer cancel()
	if err := e.Client.Create(ctx, typed); err != nil {
		t.Fatalf("GKA-012: typed create with an empty Mode must be accepted (mode is omitted and defaulted): %v", err)
	}
	got := &v1alpha1.GPUFleet{}
	if err := e.Client.Get(ctx, client.ObjectKey{Name: typedName}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Mode != "Audit" {
		t.Errorf("GKA-012/042: typed create with empty Mode stored mode %q, want Audit", got.Spec.Mode)
	}

	for _, c := range []struct {
		name string
		mut  func(spec map[string]any)
	}{
		{"absent", func(spec map[string]any) { delete(spec, "mode") }},
		{"null", func(spec map[string]any) { spec["mode"] = nil }},
	} {
		name := gkaName(t, "m-"+c.name)
		spec := gkaCFleetSpec()
		c.mut(spec)
		u := gkaCCreate(t, e, gkaCFleetRes, name, spec)
		if mode, _, _ := unstructured.NestedString(u.Object, "spec", "mode"); mode != "Audit" {
			t.Errorf("GKA-042: %s mode stored as %q, want the default Audit", c.name, mode)
		}
	}
	spec := gkaCFleetSpec()
	spec["mode"] = ""
	gkaCInvalid(t, "GKA-042 mode: empty string", gkaCCreateDry(e, gkaCFleetRes, gkaName(t, "dry-empty"), spec), "Unsupported value")
	spec["mode"] = "Enforce"
	spec["canarySelector"] = map[string]any{"matchLabels": map[string]any{"canary": "yes"}}
	u := gkaCCreate(t, e, gkaCFleetRes, gkaName(t, "enforce"), spec)
	if mode, _, _ := unstructured.NestedString(u.Object, "spec", "mode"); mode != "Enforce" {
		t.Errorf("GKA-042: an explicit Enforce must be kept, got %q", mode)
	}
}

// GKA-041: unknown fields are pruned on create and on status update; strict
// field validation names them.
func TestGKA041_UnknownFieldsArePruned(t *testing.T) {
	e := gkaCStart(t)
	name := gkaName(t, "prune")
	spec := gkaCFleetSpec()
	spec["bogus"] = "x"
	spec["nodeSelector"].(map[string]any)["bogusInSelector"] = 1
	obj := gkaCObject(gkaCFleetRes, name, spec)
	obj["extraTopLevel"] = true
	u := gkaCUnstructured(obj)
	ctx, cancel := gkaCCtx()
	defer cancel()
	if err := e.Client.Create(ctx, u); err != nil {
		t.Fatalf("GKA-041: create with unknown fields must succeed (pruned): %v", err)
	}
	got := gkaCGetObj(t, e, gkaCFleetRes, name)
	if _, has, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "bogus"); has {
		t.Errorf("GKA-041: spec.bogus was stored; unknown fields must be pruned")
	}
	if _, has, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "nodeSelector", "bogusInSelector"); has {
		t.Errorf("GKA-041: spec.nodeSelector.bogusInSelector was stored; unknown fields must be pruned")
	}
	if _, has := got.Object["extraTopLevel"]; has {
		t.Errorf("GKA-041: a top-level unknown field was stored")
	}
	st := gkaCFleetStatus()
	st["bogusStatus"] = "x"
	st["conditions"].([]any)[0].(map[string]any)["bogusCondition"] = 1
	after := gkaCStatusReal(t, e, got, st)
	if _, has, _ := unstructured.NestedFieldNoCopy(after.Object, "status", "bogusStatus"); has {
		t.Errorf("GKA-041: status.bogusStatus was stored")
	}
	if l, _, _ := unstructured.NestedSlice(after.Object, "status", "conditions"); len(l) == 1 {
		if _, has := l[0].(map[string]any)["bogusCondition"]; has {
			t.Errorf("GKA-041: an unknown condition member was stored")
		}
	}

	strict := gkaCUnstructured(gkaCObject(gkaCFleetRes, gkaName(t, "strict"), spec))
	err := e.Client.Create(ctx, strict, client.FieldValidation("Strict"), client.DryRunAll)
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("GKA-041: strict field validation must reject the unknown spec.bogus, got %v", err)
	}
}

// GKA-041: the CRDs are installed, Established, cluster scoped and serve only
// v1alpha1; namespaced access does not exist.
func TestGKA041_InstalledEstablishedClusterScoped(t *testing.T) {
	e := gkaCStart(t)
	for _, r := range gkaCAllRes {
		crd := &unstructured.Unstructured{}
		crd.SetAPIVersion("apiextensions.k8s.io/v1")
		crd.SetKind("CustomResourceDefinition")
		ctx, cancel := gkaCCtx()
		err := e.Client.Get(ctx, client.ObjectKey{Name: r.Plural + "." + gkaCGroup}, crd)
		cancel()
		if err != nil {
			t.Errorf("GKA-041/GKA-172: CRD %s is not installed: %v", r.Plural, err)
			continue
		}
		established := false
		conds, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]any)
			if m["type"] == "Established" && m["status"] == "True" {
				established = true
			}
		}
		if !established {
			t.Errorf("GKA-041/GKA-172: CRD %s has no Established=True condition", r.Plural)
		}
		if scope, _, _ := unstructured.NestedString(crd.Object, "spec", "scope"); scope != "Cluster" {
			t.Errorf("GKA-041: CRD %s scope %q, want Cluster", r.Plural, scope)
		}
		vers, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
		if len(vers) != 1 {
			t.Errorf("GKA-041: CRD %s has %d versions, want 1", r.Plural, len(vers))
		}
	}
	// A namespaced path for a cluster-scoped resource is not served.
	_, err := e.Clientset.Discovery().RESTClient().Get().
		AbsPath("/apis/" + gkaCGroup + "/" + gkaCVersion + "/namespaces/default/gpufleets").DoRaw(t.Context())
	if err == nil {
		t.Errorf("GKA-041: GET .../namespaces/default/gpufleets succeeded; the resources are cluster scoped")
	}
}

// GKA-041: the status subresource separates the two writers: /status changes
// only status, the main resource changes only spec, and metadata.generation
// moves with spec changes alone (GKA-071 and observedGeneration rely on it).
func TestGKA041_StatusSubresourceSemantics(t *testing.T) {
	e := gkaCStart(t)
	name := gkaName(t, "sub")
	obj := gkaCCreate(t, e, gkaCFleetRes, name, gkaCFleetSpec())
	gen0 := obj.GetGeneration()
	if gen0 != 1 {
		t.Errorf("GKA-041: a new object has generation %d, want 1", gen0)
	}
	ctx, cancel := gkaCCtx()
	defer cancel()

	// /status: status is written, a spec edit sent along is ignored, generation stays.
	viaStatus := obj.DeepCopy()
	viaStatus.Object["status"] = gkaCClone(gkaCFleetStatus())
	if err := unstructured.SetNestedField(viaStatus.Object, int64(90), "spec", "freshnessSeconds"); err != nil {
		t.Fatal(err)
	}
	if err := e.Client.Status().Update(ctx, viaStatus); err != nil {
		t.Fatalf("GKA-041: status update: %v", err)
	}
	got := gkaCGetObj(t, e, gkaCFleetRes, name)
	if v, _, _ := unstructured.NestedInt64(got.Object, "spec", "freshnessSeconds"); v != 60 {
		t.Errorf("GKA-041: a status update changed spec.freshnessSeconds to %d; /status must only write status", v)
	}
	if v, _, _ := unstructured.NestedInt64(got.Object, "status", "selectedCount"); v != 1 {
		t.Errorf("GKA-041: the status update did not store status.selectedCount (got %d)", v)
	}
	if got.GetGeneration() != gen0 {
		t.Errorf("GKA-041: a status update moved metadata.generation %d -> %d", gen0, got.GetGeneration())
	}

	// main resource: spec is written, a status edit sent along is ignored, generation increments.
	viaSpec := got.DeepCopy()
	if err := unstructured.SetNestedField(viaSpec.Object, int64(45), "spec", "readyForSeconds"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(viaSpec.Object, int64(9), "status", "selectedCount"); err != nil {
		t.Fatal(err)
	}
	if err := e.Client.Update(ctx, viaSpec); err != nil {
		t.Fatalf("GKA-041: spec update: %v", err)
	}
	got = gkaCGetObj(t, e, gkaCFleetRes, name)
	if v, _, _ := unstructured.NestedInt64(got.Object, "spec", "readyForSeconds"); v != 45 {
		t.Errorf("GKA-041: the spec update was not stored (readyForSeconds %d)", v)
	}
	if v, _, _ := unstructured.NestedInt64(got.Object, "status", "selectedCount"); v != 1 {
		t.Errorf("GKA-041: a main-resource update changed status.selectedCount to %d; status is written through /status only", v)
	}
	if got.GetGeneration() != gen0+1 {
		t.Errorf("GKA-041: a spec update moved metadata.generation to %d, want %d", got.GetGeneration(), gen0+1)
	}
}
