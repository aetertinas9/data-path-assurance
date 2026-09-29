package kubeapi_test

// Static checks of the checked-in CRD manifests (GKA-040 .. GKA-048, GKA-170
// allows YAML checks next to the envtest suite). They never start envtest, so
// they run even when KUBEBUILDER_ASSETS is unset. YAML is decoded
// through k8s.io/apimachinery's YAML->JSON conversion into plain maps so this
// file needs no module beyond apimachinery.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
)

func gkaCRepoRoot(t testing.TB) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("cannot resolve the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

func gkaCCRDPath(t testing.TB, res gkaCRes) string {
	t.Helper()
	return filepath.Join(gkaCRepoRoot(t), "deploy", "crds", res.File)
}

// gkaCLoadCRD reads one CRD manifest as a JSON-shaped map.
func gkaCLoadCRD(t testing.TB, res gkaCRes) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(gkaCCRDPath(t, res))
	if err != nil {
		t.Fatalf("GKA-040: cannot read the CRD manifest %s: %v", res.File, err)
	}
	j, err := yaml.ToJSON(raw)
	if err != nil {
		t.Fatalf("GKA-040: %s is not valid YAML: %v", res.File, err)
	}
	var m map[string]any
	if err := json.Unmarshal(j, &m); err != nil {
		t.Fatalf("GKA-040: %s does not hold one object: %v", res.File, err)
	}
	return m
}

// gkaCSchemaOf returns spec.versions[0].schema.openAPIV3Schema.
func gkaCSchemaOf(t testing.TB, crd map[string]any) map[string]any {
	t.Helper()
	v, ok := gkaCGet(crd, "spec.versions[].schema.openAPIV3Schema")
	if !ok {
		t.Fatalf("GKA-041: the CRD has no spec.versions[0].schema.openAPIV3Schema")
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("GKA-041: openAPIV3Schema is not an object")
	}
	return m
}

// gkaCNode resolves a schema path ("x[]" = element schema of list x).
func gkaCNode(schema map[string]any, path string) (map[string]any, bool) {
	cur := schema
	if path == "" {
		return cur, true
	}
	for _, seg := range gkaCParse(path) {
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := props[seg.key].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
		if seg.isIdx {
			items, ok := cur["items"].(map[string]any)
			if !ok {
				return nil, false
			}
			cur = items
		}
	}
	return cur, true
}

// gkaCWalkSchema visits every schema node with its path in the same notation.
func gkaCWalkSchema(n map[string]any, path string, fn func(path string, n map[string]any)) {
	fn(path, n)
	if props, ok := n["properties"].(map[string]any); ok {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child, ok := props[k].(map[string]any)
			if !ok {
				continue
			}
			p := k
			if path != "" {
				p = path + "." + k
			}
			gkaCWalkSchema(child, p, fn)
		}
	}
	if items, ok := n["items"].(map[string]any); ok {
		gkaCWalkSchema(items, path+"[]", fn)
	}
	if ap, ok := n["additionalProperties"].(map[string]any); ok {
		gkaCWalkSchema(ap, path+"{}", fn)
	}
}

func gkaCStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func gkaCSameSet(a, b []string) bool {
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}

func gkaCNum(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

// gkaCSchemaCache holds parsed schemas (tests in this package run serially).
var gkaCSchemaCache = map[string]map[string]any{}

// gkaCSchemaFor returns the schema of a CRD by plural.
func gkaCSchemaFor(t testing.TB, plural string) map[string]any {
	t.Helper()
	if s, ok := gkaCSchemaCache[plural]; ok {
		return s
	}
	for _, r := range gkaCAllRes {
		if r.Plural == plural {
			s := gkaCSchemaOf(t, gkaCLoadCRD(t, r))
			gkaCSchemaCache[plural] = s
			return s
		}
	}
	t.Fatalf("unknown resource %s", plural)
	return nil
}

// GKA-040: exactly the three generated files, with the names of the table.
func TestGKA040_ManifestFilesAndNames(t *testing.T) {
	dir := filepath.Join(gkaCRepoRoot(t), "deploy", "crds")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("GKA-040: cannot list deploy/crds: %v", err)
	}
	var got, want []string
	for _, e := range entries {
		got = append(got, e.Name())
		if e.IsDir() {
			t.Errorf("GKA-040: deploy/crds contains the directory %s", e.Name())
		}
	}
	for _, r := range gkaCAllRes {
		want = append(want, r.File)
	}
	if !gkaCSameSet(got, want) {
		t.Errorf("GKA-040: deploy/crds holds %v, want exactly %v", got, want)
	}
	for _, r := range gkaCAllRes {
		crd := gkaCLoadCRD(t, r)
		check := func(path string, wantVal any) {
			t.Helper()
			v, ok := gkaCGet(crd, path)
			if !ok || !reflect.DeepEqual(v, wantVal) {
				t.Errorf("GKA-040 %s: %s = %#v (present %v), want %#v", r.File, path, v, ok, wantVal)
			}
		}
		check("apiVersion", "apiextensions.k8s.io/v1")
		check("kind", "CustomResourceDefinition")
		check("metadata.name", r.Plural+"."+gkaCGroup)
		check("spec.group", gkaCGroup)
		check("spec.names.plural", r.Plural)
		check("spec.names.singular", r.Singular)
		check("spec.names.kind", r.Kind)
		check("spec.names.listKind", r.ListKind)
		if v, ok := gkaCGet(crd, "spec.names.shortNames"); ok && len(gkaCStrings(v)) != 0 {
			t.Errorf("GKA-040 %s: shortNames must be absent, got %v", r.File, v)
		}
		if v, ok := gkaCGet(crd, `metadata.annotations`); ok {
			if a, _ := v.(map[string]any); a["controller-gen.kubebuilder.io/version"] != "v0.20.1" {
				t.Errorf("GKA-040/GKA-191 %s: controller-gen annotation = %v, want v0.20.1 (the manifest must be generated, not hand written)", r.File, a["controller-gen.kubebuilder.io/version"])
			}
		} else {
			t.Errorf("GKA-040/GKA-191 %s: no controller-gen annotation; the manifest must be generated by controller-gen v0.20.1", r.File)
		}
	}
}

// GKA-041: cluster scope, one served+storage version, status subresource,
// structural pruning (no preserveUnknownFields anywhere).
func TestGKA041_ManifestVersionScopeSubresource(t *testing.T) {
	for _, r := range gkaCAllRes {
		crd := gkaCLoadCRD(t, r)
		if v, _ := gkaCGet(crd, "spec.scope"); v != "Cluster" {
			t.Errorf("GKA-041 %s: scope = %v, want Cluster", r.File, v)
		}
		versions, _ := gkaCGet(crd, "spec.versions")
		vl, _ := versions.([]any)
		if len(vl) != 1 {
			t.Errorf("GKA-041 %s: %d versions, want exactly one", r.File, len(vl))
			continue
		}
		ver, _ := vl[0].(map[string]any)
		if ver["name"] != "v1alpha1" || ver["served"] != true || ver["storage"] != true {
			t.Errorf("GKA-041 %s: version = %v, want v1alpha1 served=true storage=true", r.File, ver)
		}
		sub, ok := gkaCGet(crd, "spec.versions[].subresources")
		sm, _ := sub.(map[string]any)
		if !ok || sm == nil {
			t.Errorf("GKA-041 %s: no subresources", r.File)
		} else {
			if st, ok := sm["status"].(map[string]any); !ok || len(st) != 0 {
				t.Errorf("GKA-041 %s: subresources.status = %v, want {}", r.File, sm["status"])
			}
			if _, has := sm["scale"]; has {
				t.Errorf("GKA-041 %s: unexpected scale subresource", r.File)
			}
		}
		if v, ok := gkaCGet(crd, "spec.preserveUnknownFields"); ok && v == true {
			t.Errorf("GKA-041 %s: spec.preserveUnknownFields is true", r.File)
		}
		schema := gkaCSchemaOf(t, crd)
		if schema["type"] != "object" {
			t.Errorf("GKA-041 %s: root schema type = %v, want object", r.File, schema["type"])
		}
		gkaCWalkSchema(schema, "", func(path string, n map[string]any) {
			if n["x-kubernetes-preserve-unknown-fields"] == true {
				t.Errorf("GKA-041 %s: %q keeps unknown fields; unknown fields must be pruned", r.File, path)
			}
		})
	}
}

// GKA-042: property and required sets of every object node (required = every
// non-pointer field, `mode` excepted), the mode default, and the bound table.
func TestGKA042_ManifestShapesAndRequired(t *testing.T) {
	for _, s := range gkaCShapes {
		schema := gkaCSchemaFor(t, s.Plural)
		n, ok := gkaCNode(schema, s.Path)
		if !ok {
			t.Errorf("GKA-042 %s %q: schema node is missing", s.Plural, s.Path)
			continue
		}
		if n["type"] != "object" {
			t.Errorf("GKA-042 %s %q: type = %v, want object", s.Plural, s.Path, n["type"])
		}
		if s.Props != nil {
			props, _ := n["properties"].(map[string]any)
			var got []string
			for k := range props {
				got = append(got, k)
			}
			if !gkaCSameSet(got, s.Props) {
				t.Errorf("GKA-042 %s %q: properties %v, want exactly %v", s.Plural, s.Path, gkaCSortedCopy(got), gkaCSortedCopy(s.Props))
			}
		}
		if got := gkaCStrings(n["required"]); !gkaCSameSet(got, s.Required) {
			t.Errorf("GKA-042 %s %q: required %v, want exactly %v (every non-pointer field of GKA-012/013; mode excepted)", s.Plural, s.Path, gkaCSortedCopy(got), gkaCSortedCopy(s.Required))
		}
	}
	// conditions element schema: status enum and the observedGeneration member.
	for _, r := range gkaCAllRes {
		schema := gkaCSchemaOf(t, gkaCLoadCRD(t, r))
		n, ok := gkaCNode(schema, "status.conditions[]")
		if !ok {
			t.Errorf("GKA-042 %s: status.conditions[] is missing", r.Plural)
			continue
		}
		props, _ := n["properties"].(map[string]any)
		if _, ok := props["observedGeneration"]; !ok {
			t.Errorf("GKA-042/GKA-046 %s: conditions have no observedGeneration member", r.Plural)
		}
		if st, _ := props["status"].(map[string]any); !gkaCSameSet(gkaCStrings(st["enum"]), []string{"True", "False", "Unknown"}) {
			t.Errorf("GKA-046 %s: condition status enum = %v, want True|False|Unknown", r.Plural, st["enum"])
		}
	}
	// mode: enum, default Audit, not required.
	fleet := gkaCSchemaFor(t, "gpufleets")
	mode, ok := gkaCNode(fleet, "spec.mode")
	if !ok {
		t.Fatalf("GKA-042: spec.mode is missing")
	}
	if mode["default"] != "Audit" {
		t.Errorf("GKA-042/GKA-012: spec.mode default = %v, want Audit", mode["default"])
	}
}

func gkaCSortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestGKA042_ManifestBoundTable(t *testing.T) {
	const namePattern = "^[A-Za-z0-9._-]+$"
	for _, b := range gkaCBounds {
		schema := gkaCSchemaFor(t, b.Plural)
		n, ok := gkaCNode(schema, b.Path)
		if !ok {
			t.Errorf("GKA-042 %s %s: schema node is missing", b.Plural, b.Path)
			continue
		}
		if b.Int {
			if n["type"] != "integer" {
				t.Errorf("GKA-042 %s %s: type = %v, want integer", b.Plural, b.Path, n["type"])
			}
			if v, ok := gkaCNum(n["minimum"]); !ok || v != b.Min {
				t.Errorf("GKA-042 %s %s: minimum = %v, want %d", b.Plural, b.Path, n["minimum"], b.Min)
			}
			if b.NoMax {
				if _, has := n["maximum"]; has {
					t.Errorf("GKA-042 %s %s: maximum = %v, want none", b.Plural, b.Path, n["maximum"])
				}
			} else if v, ok := gkaCNum(n["maximum"]); !ok || v != b.Max {
				t.Errorf("GKA-042 %s %s: maximum = %v, want %d", b.Plural, b.Path, n["maximum"], b.Max)
			}
			wantFormat := "int32"
			if strings.HasSuffix(b.Path, "collectorSession") {
				wantFormat = "int64"
			}
			if n["format"] != wantFormat {
				t.Errorf("GKA-042 %s %s: format = %v, want %s", b.Plural, b.Path, n["format"], wantFormat)
			}
			continue
		}
		if n["type"] != "string" {
			t.Errorf("GKA-042 %s %s: type = %v, want string", b.Plural, b.Path, n["type"])
		}
		minL, hasMin := gkaCNum(n["minLength"])
		if b.Min == 0 {
			if hasMin && minL != 0 {
				t.Errorf("GKA-042 %s %s: minLength = %d, want 0 or unset", b.Plural, b.Path, minL)
			}
		} else if !hasMin || minL != b.Min {
			t.Errorf("GKA-042 %s %s: minLength = %v, want %d", b.Plural, b.Path, n["minLength"], b.Min)
		}
		if v, ok := gkaCNum(n["maxLength"]); !ok || v != b.Max {
			t.Errorf("GKA-042 %s %s: maxLength = %v, want %d", b.Plural, b.Path, n["maxLength"], b.Max)
		}
		if b.Pattern && n["pattern"] != namePattern {
			t.Errorf("GKA-042 %s %s: pattern = %v, want %s", b.Plural, b.Path, n["pattern"], namePattern)
		}
	}
}

func TestGKA042_ManifestEnumTable(t *testing.T) {
	for _, e := range gkaCEnums {
		schema := gkaCSchemaFor(t, e.Plural)
		n, ok := gkaCNode(schema, e.Path)
		if !ok {
			t.Errorf("GKA-042 %s %s: schema node is missing", e.Plural, e.Path)
			continue
		}
		if n["type"] != "string" {
			t.Errorf("GKA-042 %s %s: type = %v, want string", e.Plural, e.Path, n["type"])
		}
		if got := gkaCStrings(n["enum"]); !gkaCSameSet(got, e.Values) || len(got) != len(e.Values) {
			t.Errorf("GKA-042/GKA-014 %s %s: enum %v, want exactly %v", e.Plural, e.Path, got, e.Values)
		}
	}
}

// GKA-042: every string and integer member is bounded by the table (or is an
// enum or timestamp); no unbounded member slipped in.
func TestGKA042_ManifestNoUnboundedScalar(t *testing.T) {
	known := map[string]bool{}
	for _, b := range gkaCBounds {
		known[b.Plural+" "+b.Path] = true
	}
	for _, e := range gkaCEnums {
		known[e.Plural+" "+e.Path] = true
	}
	noBound := map[string]bool{"status.observedGeneration": true, "status.deviceSummaries[].observedGeneration": true}
	excluded := []string{"metadata", "apiVersion", "kind", "spec.nodeSelector", "spec.canarySelector", "status.conditions[]"}
	for _, r := range gkaCAllRes {
		schema := gkaCSchemaOf(t, gkaCLoadCRD(t, r))
		gkaCWalkSchema(schema, "", func(path string, n map[string]any) {
			for _, ex := range excluded {
				if path == ex || strings.HasPrefix(path, ex+".") || strings.HasPrefix(path, ex+"[") || strings.HasPrefix(path, ex+"{") {
					return
				}
			}
			switch n["type"] {
			case "string":
				if n["format"] == "date-time" || known[r.Plural+" "+path] {
					return
				}
				t.Errorf("GKA-042 %s: string member %q is not in the bound or enum table (every string is bounded)", r.Plural, path)
			case "integer":
				if known[r.Plural+" "+path] || noBound[path] {
					return
				}
				t.Errorf("GKA-042 %s: integer member %q is not in the bound table", r.Plural, path)
			}
		})
	}
}

// GKA-043: maxItems, list type and list-map keys.
func TestGKA043_ManifestListBoundsAndTypes(t *testing.T) {
	for _, l := range gkaCLists {
		schema := gkaCSchemaFor(t, l.Plural)
		n, ok := gkaCNode(schema, l.Path)
		if !ok {
			t.Errorf("GKA-043 %s %s: schema node is missing", l.Plural, l.Path)
			continue
		}
		if n["type"] != "array" {
			t.Errorf("GKA-043 %s %s: type = %v, want array", l.Plural, l.Path, n["type"])
		}
		if v, ok := gkaCNum(n["maxItems"]); !ok || v != int64(l.Max) {
			t.Errorf("GKA-043 %s %s: maxItems = %v, want %d", l.Plural, l.Path, n["maxItems"], l.Max)
		}
		wantType := "map"
		if l.Atomic {
			wantType = "atomic"
		}
		if n["x-kubernetes-list-type"] != wantType {
			t.Errorf("GKA-043 %s %s: x-kubernetes-list-type = %v, want %s", l.Plural, l.Path, n["x-kubernetes-list-type"], wantType)
		}
		if !l.Atomic {
			if keys := gkaCStrings(n["x-kubernetes-list-map-keys"]); !reflect.DeepEqual(keys, []string{l.Key}) {
				t.Errorf("GKA-043 %s %s: list-map-keys = %v, want [%s]", l.Plural, l.Path, keys, l.Key)
			}
			items, _ := n["items"].(map[string]any)
			found := false
			for _, req := range gkaCStrings(items["required"]) {
				found = found || req == l.Key
			}
			if !found {
				t.Errorf("GKA-043 %s %s: key %s is not required in the element schema", l.Plural, l.Path, l.Key)
			}
		}
	}
	for _, r := range gkaCAllRes {
		schema := gkaCSchemaOf(t, gkaCLoadCRD(t, r))
		n, ok := gkaCNode(schema, "status.conditions")
		if !ok {
			t.Errorf("GKA-043 %s: status.conditions is missing", r.Plural)
			continue
		}
		if v, ok := gkaCNum(n["maxItems"]); !ok || v != 32 {
			t.Errorf("GKA-043 %s conditions: maxItems = %v, want 32", r.Plural, n["maxItems"])
		}
		if n["x-kubernetes-list-type"] != "map" || !reflect.DeepEqual(gkaCStrings(n["x-kubernetes-list-map-keys"]), []string{"type"}) {
			t.Errorf("GKA-043 %s conditions: list type %v keys %v, want map/[type]", r.Plural, n["x-kubernetes-list-type"], n["x-kubernetes-list-map-keys"])
		}
	}
}

// ------------------------------------------------------------------ CEL text

type gkaCRule struct {
	ID, Plural, Path, Rule, Message string
}

var gkaCSpecRules = []gkaCRule{
	{"F1", "gpufleets", "spec",
		`(has(self.nodeSelector.matchLabels) && size(self.nodeSelector.matchLabels) > 0) || (has(self.nodeSelector.matchExpressions) && size(self.nodeSelector.matchExpressions) > 0)`,
		`nodeSelector must have at least one matchLabels or matchExpressions entry`},
	{"F2", "gpufleets", "spec",
		`self.mode != 'Enforce' || (has(self.canarySelector) && ((has(self.canarySelector.matchLabels) && size(self.canarySelector.matchLabels) > 0) || (has(self.canarySelector.matchExpressions) && size(self.canarySelector.matchExpressions) > 0)))`,
		`mode Enforce requires a canarySelector with at least one matchLabels or matchExpressions entry`},
	{"F3", "gpufleets", "spec",
		`(!has(self.nodeSelector.matchLabels) || size(self.nodeSelector.matchLabels) <= 64) && (!has(self.nodeSelector.matchExpressions) || size(self.nodeSelector.matchExpressions) <= 64) && (!has(self.canarySelector) || ((!has(self.canarySelector.matchLabels) || size(self.canarySelector.matchLabels) <= 64) && (!has(self.canarySelector.matchExpressions) || size(self.canarySelector.matchExpressions) <= 64)))`,
		`selectors may have at most 64 matchLabels and 64 matchExpressions entries`},
	{"D1", "gpudevices", "spec", `self.nodeRef == oldSelf.nodeRef`, `spec.nodeRef is immutable`},
	{"D2", "gpudevices", "spec", `self.inventoryClaim == oldSelf.inventoryClaim`, `spec.inventoryClaim is immutable`},
	{"D3", "gpudevices", "spec", `oldSelf.desiredState != 'Retired' || self.desiredState == 'Retired'`, `a Retired GPUDevice cannot leave Retired; register a new GPUDevice`},
	{"D4", "gpudevices", "spec", `self.desiredState == oldSelf.desiredState || self.request.id != oldSelf.request.id`, `changing desiredState requires a new request.id`},
	{"D5", "gpudevices", "spec.inventoryClaim", `has(self.uuid) || has(self.serial)`, `inventoryClaim requires uuid or serial`},
	{"N1", "nodepathstates", "spec", `self.nodeRef == oldSelf.nodeRef`, `spec.nodeRef is immutable`},
}

var gkaCStatusRules = []gkaCRule{
	{"S1", "gpudevices", "status.actualBinding",
		`self.state != 'Bound' || (has(self.vendor) && has(self.uuid) && has(self.nodeUID) && has(self.bootID) && has(self.bdf) && has(self.functionKey) && has(self.source) && has(self.evidenceID) && has(self.observedAt) && has(self.expiresAt))`,
		`actualBinding.state Bound requires vendor, uuid, nodeUID, bootID, bdf, functionKey, source, evidenceID, observedAt and expiresAt`},
	{"S2", "gpudevices", "status.allocation",
		`self.state == 'Unknown' || (has(self.profile) && has(self.observedAt) && has(self.expiresAt) && size(self.evidenceRefs) > 0)`,
		`allocation.state Empty or InUse requires profile, observedAt, expiresAt and evidenceRefs`},
	{"S3", "gpudevices", "status.coverage[]",
		`self.state != 'Normal' || (has(self.observedAt) && has(self.latestObservedAt) && has(self.expiresAt) && size(self.evidenceRefs) > 0)`,
		`coverage.state Normal requires observedAt, latestObservedAt, expiresAt and evidenceRefs`},
	{"S4", "gpudevices", "status", `self.qualification == 'Unknown' || has(self.graphRevision)`,
		`graphRevision is required when qualification is Qualified or Disqualified`},
	{"S5", "nodepathstates", "status", `self.evidenceCompleteness == 'Unknown' || has(self.graphRevision)`,
		`graphRevision is required when evidenceCompleteness is Complete or Partial`},
	{"S6", "gpufleets", "status",
		`!self.conditions.exists(c, c.type == 'FleetReady' && c.status == 'True') || has(self.assessmentRevision)`,
		`assessmentRevision is required when FleetReady is True`},
}

var gkaCSpace = regexp.MustCompile(`\s+`)

// gkaCNormCEL makes a rule comparable: whitespace runs collapse to one space
// and both CEL quote styles read the same.
func gkaCNormCEL(s string) string {
	return strings.TrimSpace(gkaCSpace.ReplaceAllString(strings.ReplaceAll(s, `"`, `'`), " "))
}

// gkaCValidations returns the x-kubernetes-validations of a node as
// (rule, message) pairs.
func gkaCValidations(n map[string]any) [][2]string {
	l, _ := n["x-kubernetes-validations"].([]any)
	var out [][2]string
	for _, e := range l {
		m, _ := e.(map[string]any)
		rule, _ := m["rule"].(string)
		msg, _ := m["message"].(string)
		out = append(out, [2]string{rule, msg})
	}
	return out
}

func gkaCCheckRules(t *testing.T, clause string, rules []gkaCRule) {
	t.Helper()
	for _, r := range rules {
		schema := gkaCSchemaFor(t, r.Plural)
		n, ok := gkaCNode(schema, r.Path)
		if !ok {
			t.Errorf("%s %s: schema node %s %q is missing", clause, r.ID, r.Plural, r.Path)
			continue
		}
		matches := 0
		for _, v := range gkaCValidations(n) {
			if gkaCNormCEL(v[0]) != gkaCNormCEL(r.Rule) {
				continue
			}
			matches++
			if v[1] != r.Message {
				t.Errorf("%s %s: message = %q, want %q", clause, r.ID, v[1], r.Message)
			}
		}
		if matches != 1 {
			t.Errorf("%s %s: %d validations at %s %q carry the rule %q, want exactly one", clause, r.ID, matches, r.Plural, r.Path, r.Rule)
		}
	}
}

// GKA-044: the spec-level rules, rule text and message verbatim.
func TestGKA044_ManifestSpecRulesText(t *testing.T) {
	gkaCCheckRules(t, "GKA-044", gkaCSpecRules)
}

// GKA-045: the status conditional-required rules, verbatim.
func TestGKA045_ManifestStatusRulesText(t *testing.T) {
	gkaCCheckRules(t, "GKA-045", gkaCStatusRules)
}

var (
	gkaCTypeIn   = regexp.MustCompile(`c\.type in \[([^\]]*)\]`)
	gkaCReasonIn = regexp.MustCompile(`c\.reason in \[([^\]]*)\]`)
	gkaCQuoted   = regexp.MustCompile(`'([^']*)'`)
)

func gkaCQuotedItems(s string) []string {
	var out []string
	for _, m := range gkaCQuoted.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// GKA-046: the conditions rule of each resource.
func TestGKA046_ManifestConditionsRule(t *testing.T) {
	for _, r := range gkaCAllRes {
		schema := gkaCSchemaOf(t, gkaCLoadCRD(t, r))
		n, ok := gkaCNode(schema, "status.conditions")
		if !ok {
			t.Errorf("GKA-046 %s: status.conditions is missing", r.Plural)
			continue
		}
		vs := gkaCValidations(n)
		if len(vs) != 1 {
			t.Errorf("GKA-046 %s: %d conditions validations, want exactly one", r.Plural, len(vs))
			continue
		}
		rule := gkaCNormCEL(vs[0][0])
		if vs[0][1] != gkaCCondMessage {
			t.Errorf("GKA-046 %s: message = %q, want %q", r.Plural, vs[0][1], gkaCCondMessage)
		}
		if !strings.HasPrefix(rule, "self.all(c,") {
			t.Errorf("GKA-046 %s: rule %q must start with self.all(c,", r.Plural, rule)
		}
		if m := gkaCTypeIn.FindStringSubmatch(rule); m == nil {
			t.Errorf("GKA-046 %s: rule has no `c.type in [...]` clause: %s", r.Plural, rule)
		} else if got := gkaCQuotedItems(m[1]); !gkaCSameSet(got, gkaCCondTypes[r.Plural]) || len(got) != len(gkaCCondTypes[r.Plural]) {
			t.Errorf("GKA-046 %s: allowed types %v, want exactly %v", r.Plural, got, gkaCCondTypes[r.Plural])
		}
		if m := gkaCReasonIn.FindStringSubmatch(rule); m == nil {
			t.Errorf("GKA-046 %s: rule has no `c.reason in [...]` clause: %s", r.Plural, rule)
		} else if got := gkaCQuotedItems(m[1]); !gkaCSameSet(got, gkaCV22) || len(got) != 22 {
			t.Errorf("GKA-046 %s: allowed reasons %v, want exactly the 22 V22 reasons %v", r.Plural, got, gkaCV22)
		}
		for _, frag := range []string{"has(c.observedGeneration)", "size(c.message) >= 1", "size(c.message) <= 1024"} {
			if !strings.Contains(rule, frag) {
				t.Errorf("GKA-046 %s: rule lacks %q: %s", r.Plural, frag, rule)
			}
		}
	}
}

var gkaCMacro = regexp.MustCompile(`\.(all|exists|exists_one|map|filter)\(`)

// GKA-047: no rule walks an unbounded list; only conditions (<= 32) may be
// traversed.
func TestGKA047_ManifestNoUnboundedTraversal(t *testing.T) {
	total := 0
	for _, r := range gkaCAllRes {
		schema := gkaCSchemaOf(t, gkaCLoadCRD(t, r))
		gkaCWalkSchema(schema, "", func(path string, n map[string]any) {
			for _, v := range gkaCValidations(n) {
				total++
				rule := gkaCNormCEL(v[0])
				for _, m := range gkaCMacro.FindAllStringIndex(rule, -1) {
					before := rule[:m[0]]
					onConditions := strings.HasSuffix(before, "self.conditions") ||
						(path == "status.conditions" && before == "self")
					if !onConditions {
						t.Errorf("GKA-047 %s %q: rule walks a list other than conditions (%s...): %s", r.Plural, path, rule[m[0]:m[1]], rule)
					}
				}
				if path == "spec" && r == gkaCFleetRes && gkaCMacro.MatchString(rule) {
					t.Errorf("GKA-047 %s: selector rule %q must use only has/size/equality", r.Plural, rule)
				}
			}
		})
	}
	// spec rules + status rules + one conditions rule per resource.
	if want := len(gkaCSpecRules) + len(gkaCStatusRules) + 3; total < want {
		t.Errorf("GKA-047: only %d CEL rules found across the manifests, want at least %d", total, want)
	}
}

type gkaCColumn struct{ Name, JSONPath, Type string }

var gkaCColumns = map[string][]gkaCColumn{
	"gpufleets": {
		{"MODE", ".spec.mode", "string"}, {"READY", ".status.readyCount", "integer"},
		{"DEGRADED", ".status.degradedCount", "integer"}, {"UNKNOWN", ".status.unknownCount", "integer"},
		{"AGE", ".metadata.creationTimestamp", "date"},
	},
	"gpudevices": {
		{"NODE", ".spec.nodeRef.name", "string"}, {"DESIRED", ".spec.desiredState", "string"},
		{"PHASE", ".status.lifecyclePhase", "string"}, {"QUALIFIED", ".status.qualification", "string"},
		{"AGE", ".metadata.creationTimestamp", "date"},
	},
	"nodepathstates": {
		{"NODE", ".spec.nodeRef.name", "string"}, {"REVISION", ".status.graphRevision", "string"},
		{"COMPLETE", ".status.evidenceCompleteness", "string"}, {"AGE", ".metadata.creationTimestamp", "date"},
	},
}

// GKA-048: printer columns in order.
func TestGKA048_ManifestPrinterColumns(t *testing.T) {
	for _, r := range gkaCAllRes {
		crd := gkaCLoadCRD(t, r)
		v, _ := gkaCGet(crd, "spec.versions[].additionalPrinterColumns")
		l, _ := v.([]any)
		var got []gkaCColumn
		for _, e := range l {
			m, _ := e.(map[string]any)
			name, _ := m["name"].(string)
			jp, _ := m["jsonPath"].(string)
			typ, _ := m["type"].(string)
			got = append(got, gkaCColumn{name, jp, typ})
		}
		if !reflect.DeepEqual(got, gkaCColumns[r.Plural]) {
			t.Errorf("GKA-048 %s: additionalPrinterColumns = %v, want %v (this order)", r.Plural, got, gkaCColumns[r.Plural])
		}
	}
}
