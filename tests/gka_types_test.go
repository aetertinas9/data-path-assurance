package tests_test

// CRD Go types, scheme registration, constants and generated deepcopy
// (GKA-010 .. GKA-015). Everything is checked through the exported API and
// reflection; nothing here needs a cluster.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

var (
	_ runtime.Object = (*v1alpha1.GPUFleet)(nil)
	_ runtime.Object = (*v1alpha1.GPUFleetList)(nil)
	_ runtime.Object = (*v1alpha1.GPUDevice)(nil)
	_ runtime.Object = (*v1alpha1.GPUDeviceList)(nil)
	_ runtime.Object = (*v1alpha1.NodePathState)(nil)
	_ runtime.Object = (*v1alpha1.NodePathStateList)(nil)
)

const gkaCPkgSuffix = "internal/kubernetes/api/v1alpha1"

var gkaCKinds = []struct {
	kind string
	obj  runtime.Object
}{
	{"GPUFleet", &v1alpha1.GPUFleet{}}, {"GPUFleetList", &v1alpha1.GPUFleetList{}},
	{"GPUDevice", &v1alpha1.GPUDevice{}}, {"GPUDeviceList", &v1alpha1.GPUDeviceList{}},
	{"NodePathState", &v1alpha1.NodePathState{}}, {"NodePathStateList", &v1alpha1.NodePathStateList{}},
}

// GKA-010: constants, GroupVersion, and AddToScheme.
func TestGKA010_GroupVersionAndScheme(t *testing.T) {
	if v1alpha1.GroupName != "infrastructure.data-path-assurance.io" || v1alpha1.Version != "v1alpha1" {
		t.Errorf("GKA-010: GroupName/Version = %q/%q, want infrastructure.data-path-assurance.io/v1alpha1", v1alpha1.GroupName, v1alpha1.Version)
	}
	gv := v1alpha1.GroupVersion
	if gv != (schema.GroupVersion{Group: "infrastructure.data-path-assurance.io", Version: "v1alpha1"}) {
		t.Fatalf("GKA-010: GroupVersion = %v", gv)
	}

	untouched := runtime.NewScheme()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("GKA-010: AddToScheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Errorf("GKA-010: a second AddToScheme on the same scheme must return nil, got %v", err)
	}
	metaKinds := map[string]bool{"WatchEvent": true, "ListOptions": true, "GetOptions": true, "DeleteOptions": true, "CreateOptions": true, "UpdateOptions": true, "PatchOptions": true}
	for _, k := range gkaCKinds {
		gvk := gv.WithKind(k.kind)
		if !s.Recognizes(gvk) {
			t.Errorf("GKA-010: %s is not registered", gvk)
			continue
		}
		obj, err := s.New(gvk)
		if err != nil || reflect.TypeOf(obj) != reflect.TypeOf(k.obj) {
			t.Errorf("GKA-010: scheme.New(%s) = %T (%v), want %T", gvk, obj, err, k.obj)
		}
		kinds, _, err := s.ObjectKinds(k.obj)
		if err != nil || len(kinds) != 1 || kinds[0] != gvk {
			t.Errorf("GKA-010: ObjectKinds(%T) = %v (%v), want exactly [%s]", k.obj, kinds, err, gvk)
		}
		if untouched.Recognizes(gvk) {
			t.Errorf("GKA-010: a scheme AddToScheme was never called on knows %s", gvk)
		}
		metaKinds[k.kind] = true
	}
	for _, m := range []string{"WatchEvent", "ListOptions"} {
		if !s.Recognizes(gv.WithKind(m)) {
			t.Errorf("GKA-010: metav1.AddToGroupVersion was not applied (%s is not registered for %s)", m, gv)
		}
	}
	for gvk := range s.AllKnownTypes() {
		if gvk.GroupVersion() == gv && !metaKinds[gvk.Kind] {
			t.Errorf("GKA-010: unexpected kind %s registered for %s (only six kinds plus the metav1 group-version kinds)", gvk.Kind, gv)
		}
	}

	// registering next to the built-in kinds works in either order.
	for _, order := range []string{"builtin-first", "ours-first"} {
		sc := runtime.NewScheme()
		add := []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme}
		if order == "ours-first" {
			add[0], add[1] = add[1], add[0]
		}
		for _, f := range add {
			if err := f(sc); err != nil {
				t.Errorf("GKA-010: %s: AddToScheme: %v", order, err)
			}
		}
		if !sc.Recognizes(gv.WithKind("GPUFleet")) || !sc.Recognizes(schema.GroupVersionKind{Version: "v1", Kind: "Node"}) {
			t.Errorf("GKA-010: %s: the combined scheme lacks GPUFleet or Node", order)
		}
	}
}

// GKA-010: GVK <-> Go type round trip through JSON for every resource.
func TestGKA010_GVKRoundTripThroughJSON(t *testing.T) {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, k := range gkaCKinds {
		obj := reflect.New(reflect.TypeOf(k.obj).Elem()).Interface().(runtime.Object)
		gkaCFill(reflect.ValueOf(obj).Elem(), new(int))
		kinds, _, err := s.ObjectKinds(obj)
		if err != nil || len(kinds) != 1 {
			t.Fatalf("GKA-010: ObjectKinds(%T): %v %v", obj, kinds, err)
		}
		obj.GetObjectKind().SetGroupVersionKind(kinds[0])
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("GKA-010: marshal %T: %v", obj, err)
		}
		var head metav1.TypeMeta
		if err := json.Unmarshal(b, &head); err != nil {
			t.Fatal(err)
		}
		gvk := schema.FromAPIVersionAndKind(head.APIVersion, head.Kind)
		back, err := s.New(gvk)
		if err != nil || reflect.TypeOf(back) != reflect.TypeOf(obj) {
			t.Fatalf("GKA-010: %s does not come back as %T (%T, %v)", gvk, obj, back, err)
		}
		if err := json.Unmarshal(b, back); err != nil {
			t.Fatalf("GKA-010: unmarshal %s: %v", gvk, err)
		}
		b2, err := json.Marshal(back)
		if err != nil || string(b) != string(b2) {
			t.Errorf("GKA-010: %s does not survive a JSON round trip:\n%s\n%s", gvk, b, b2)
		}
	}
}

// ------------------------------------------------------------------ helpers

var (
	gkaCTimeType   = reflect.TypeOf(metav1.Time{})
	gkaCFieldsType = reflect.TypeOf(metav1.FieldsV1{})
)

// gkaCFill sets every reachable field of v to a distinct non-zero value: every
// pointer allocated, every slice and map given two elements.
func gkaCFill(v reflect.Value, seed *int) {
	*seed++
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("s%d", *seed))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*seed))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*seed))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*seed))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		gkaCFill(v.Elem(), seed)
	case reflect.Slice:
		sl := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			gkaCFill(sl.Index(i), seed)
		}
		v.Set(sl)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		for i := 0; i < 2; i++ {
			k := reflect.New(v.Type().Key()).Elem()
			gkaCFill(k, seed)
			e := reflect.New(v.Type().Elem()).Elem()
			gkaCFill(e, seed)
			m.SetMapIndex(k, e)
		}
		v.Set(m)
	case reflect.Struct:
		if v.Type() == gkaCTimeType {
			v.Set(reflect.ValueOf(metav1.NewTime(time.Date(2026, 9, 30, 0, 0, *seed%60, 0, time.UTC))))
			return
		}
		if v.Type() == gkaCFieldsType { // Raw must stay valid JSON
			v.Set(reflect.ValueOf(metav1.FieldsV1{Raw: []byte(`{"f:a":{}}`)}))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				gkaCFill(v.Field(i), seed)
			}
		}
	}
}

// gkaCScramble changes every reachable leaf of v in place (through pointers,
// slices and maps).
func gkaCScramble(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "!")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() + 1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Pointer:
		if !v.IsNil() {
			gkaCScramble(v.Elem())
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			gkaCScramble(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			gkaCScramble(e)
			v.SetMapIndex(k, e)
		}
	case reflect.Struct:
		if v.Type() == gkaCTimeType {
			v.Set(reflect.ValueOf(metav1.NewTime(time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))))
			return
		}
		if v.Type() == gkaCFieldsType {
			v.Set(reflect.ValueOf(metav1.FieldsV1{Raw: []byte(`{"f:b":{}}`)}))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				gkaCScramble(v.Field(i))
			}
		}
	}
}

func gkaCJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

type gkaCField struct {
	Go, JSON string
	Omit     bool
	Type     string
}

func gkaCF(goName, jsonName string, omit bool, typ string) gkaCField {
	return gkaCField{goName, jsonName, omit, typ}
}

// gkaCShapes is GKA-012 / GKA-013 verbatim: Go name, JSON name, omitempty, Go type.
var gkaCTypeShapes = []struct {
	typ    reflect.Type
	fields []gkaCField
}{
	{reflect.TypeOf(v1alpha1.ObjectRef{}), []gkaCField{gkaCF("Name", "name", false, "string"), gkaCF("UID", "uid", false, "string")}},
	{reflect.TypeOf(v1alpha1.OwnedNodeRef{}), []gkaCField{gkaCF("Name", "name", false, "string"), gkaCF("UID", "uid", false, "string")}},
	{reflect.TypeOf(v1alpha1.SourceRef{}), []gkaCField{gkaCF("Type", "type", false, "string"), gkaCF("Name", "name", false, "string")}},
	{reflect.TypeOf(v1alpha1.FleetCoverageRequirement{}), []gkaCField{
		gkaCF("Name", "name", false, "string"), gkaCF("PathKind", "pathKind", false, "string"), gkaCF("Required", "required", false, "bool")}},
	{reflect.TypeOf(v1alpha1.GPUFleetSpec{}), []gkaCField{
		gkaCF("NodeSelector", "nodeSelector", false, "v1.LabelSelector"),
		gkaCF("RequiredCoverage", "requiredCoverage", false, "[]v1alpha1.FleetCoverageRequirement"),
		gkaCF("FreshnessSeconds", "freshnessSeconds", false, "int32"), gkaCF("ReadyForSeconds", "readyForSeconds", false, "int32"),
		gkaCF("Mode", "mode", true, "v1alpha1.FleetMode"), gkaCF("CanarySelector", "canarySelector", true, "*v1.LabelSelector")}},
	{reflect.TypeOf(v1alpha1.InventoryClaim{}), []gkaCField{
		gkaCF("Vendor", "vendor", false, "string"), gkaCF("UUID", "uuid", true, "*string"), gkaCF("Serial", "serial", true, "*string"),
		gkaCF("Source", "source", false, "string"), gkaCF("EvidenceID", "evidenceID", false, "string")}},
	{reflect.TypeOf(v1alpha1.LifecycleRequest{}), []gkaCField{gkaCF("ID", "id", false, "string"), gkaCF("Reason", "reason", false, "string")}},
	{reflect.TypeOf(v1alpha1.GPUDeviceSpec{}), []gkaCField{
		gkaCF("NodeRef", "nodeRef", false, "v1alpha1.ObjectRef"), gkaCF("InventoryClaim", "inventoryClaim", false, "v1alpha1.InventoryClaim"),
		gkaCF("FleetRef", "fleetRef", false, "v1alpha1.ObjectRef"), gkaCF("DesiredState", "desiredState", false, "v1alpha1.DesiredState"),
		gkaCF("Request", "request", false, "v1alpha1.LifecycleRequest")}},
	{reflect.TypeOf(v1alpha1.NodePathStateSpec{}), []gkaCField{gkaCF("NodeRef", "nodeRef", false, "v1alpha1.ObjectRef")}},

	{reflect.TypeOf(v1alpha1.GPUFleetStatus{}), []gkaCField{
		gkaCF("ObservedGeneration", "observedGeneration", false, "int64"), gkaCF("AssessmentRevision", "assessmentRevision", true, "*string"),
		gkaCF("SelectedCount", "selectedCount", false, "int32"), gkaCF("ReadyCount", "readyCount", false, "int32"),
		gkaCF("DegradedCount", "degradedCount", false, "int32"), gkaCF("UnknownCount", "unknownCount", false, "int32"),
		gkaCF("OwnedNodeRefs", "ownedNodeRefs", false, "[]v1alpha1.OwnedNodeRef"), gkaCF("Conditions", "conditions", false, "[]v1.Condition")}},
	{reflect.TypeOf(v1alpha1.ActualBinding{}), []gkaCField{
		gkaCF("State", "state", false, "v1alpha1.BindingState"), gkaCF("Reason", "reason", false, "string"),
		gkaCF("Vendor", "vendor", true, "*string"), gkaCF("UUID", "uuid", true, "*string"), gkaCF("Serial", "serial", true, "*string"),
		gkaCF("NodeUID", "nodeUID", true, "*string"), gkaCF("BootID", "bootID", true, "*string"), gkaCF("BDF", "bdf", true, "*string"),
		gkaCF("FunctionKey", "functionKey", true, "*string"), gkaCF("Source", "source", true, "*v1alpha1.SourceRef"),
		gkaCF("EvidenceID", "evidenceID", true, "*string"), gkaCF("ObservedAt", "observedAt", true, "*v1.Time"), gkaCF("ExpiresAt", "expiresAt", true, "*v1.Time")}},
	{reflect.TypeOf(v1alpha1.AffectedWorkload{}), []gkaCField{
		gkaCF("Namespace", "namespace", false, "string"), gkaCF("Name", "name", false, "string"), gkaCF("UID", "uid", false, "string"),
		gkaCF("Container", "container", false, "string"), gkaCF("CreatedAt", "createdAt", false, "v1.Time"), gkaCF("DeletedAt", "deletedAt", true, "*v1.Time")}},
	{reflect.TypeOf(v1alpha1.AllocationSummary{}), []gkaCField{
		gkaCF("State", "state", false, "v1alpha1.AllocationState"), gkaCF("Reason", "reason", false, "string"), gkaCF("Profile", "profile", true, "*string"),
		gkaCF("ObservedAt", "observedAt", true, "*v1.Time"), gkaCF("ExpiresAt", "expiresAt", true, "*v1.Time"),
		gkaCF("EvidenceRefs", "evidenceRefs", false, "[]string"), gkaCF("AffectedWorkloads", "affectedWorkloads", false, "[]v1alpha1.AffectedWorkload")}},
	{reflect.TypeOf(v1alpha1.CoverageSummary{}), []gkaCField{
		gkaCF("Name", "name", false, "string"), gkaCF("PathKind", "pathKind", false, "string"), gkaCF("State", "state", false, "v1alpha1.CoverageState"),
		gkaCF("Reason", "reason", false, "string"), gkaCF("EvidenceRefs", "evidenceRefs", false, "[]string"),
		gkaCF("ObservedAt", "observedAt", true, "*v1.Time"), gkaCF("LatestObservedAt", "latestObservedAt", true, "*v1.Time"), gkaCF("ExpiresAt", "expiresAt", true, "*v1.Time")}},
	{reflect.TypeOf(v1alpha1.FindingSummary{}), []gkaCField{
		gkaCF("ID", "id", false, "string"), gkaCF("Type", "type", false, "string"), gkaCF("Severity", "severity", false, "string"), gkaCF("State", "state", false, "string")}},
	{reflect.TypeOf(v1alpha1.EvidenceSummary{}), []gkaCField{
		gkaCF("ID", "id", false, "string"), gkaCF("Summary", "summary", false, "string"),
		gkaCF("ObservedAt", "observedAt", false, "v1.Time"), gkaCF("ExpiresAt", "expiresAt", false, "v1.Time")}},
	{reflect.TypeOf(v1alpha1.GPUDeviceStatus{}), []gkaCField{
		gkaCF("ObservedGeneration", "observedGeneration", false, "int64"), gkaCF("ObservedRequestID", "observedRequestID", false, "string"),
		gkaCF("IntentObservedAt", "intentObservedAt", false, "v1.Time"), gkaCF("ActualBinding", "actualBinding", false, "v1alpha1.ActualBinding"),
		gkaCF("GraphRevision", "graphRevision", true, "*string"), gkaCF("Qualification", "qualification", false, "v1alpha1.Qualification"),
		gkaCF("LifecyclePhase", "lifecyclePhase", false, "v1alpha1.LifecyclePhase"), gkaCF("Allocation", "allocation", false, "v1alpha1.AllocationSummary"),
		gkaCF("Coverage", "coverage", false, "[]v1alpha1.CoverageSummary"), gkaCF("FindingRefs", "findingRefs", false, "[]v1alpha1.FindingSummary"),
		gkaCF("EvidenceRefs", "evidenceRefs", false, "[]v1alpha1.EvidenceSummary"), gkaCF("Conditions", "conditions", false, "[]v1.Condition")}},
	{reflect.TypeOf(v1alpha1.DeviceSummary{}), []gkaCField{
		gkaCF("Name", "name", false, "string"), gkaCF("UID", "uid", false, "string"), gkaCF("DesiredState", "desiredState", false, "v1alpha1.DesiredState"),
		gkaCF("ObservedGeneration", "observedGeneration", false, "int64"), gkaCF("Qualification", "qualification", false, "v1alpha1.Qualification"),
		gkaCF("LifecyclePhase", "lifecyclePhase", false, "v1alpha1.LifecyclePhase")}},
	{reflect.TypeOf(v1alpha1.GateOwnershipPolicy{}), []gkaCField{
		gkaCF("PolicyVersion", "policyVersion", false, "string"), gkaCF("RequiredCoverage", "requiredCoverage", false, "[]v1alpha1.FleetCoverageRequirement"),
		gkaCF("FreshnessSeconds", "freshnessSeconds", false, "int32"), gkaCF("ReadyForSeconds", "readyForSeconds", false, "int32")}},
	{reflect.TypeOf(v1alpha1.GateOwnershipStatus{}), []gkaCField{
		gkaCF("OwnerFleetUID", "ownerFleetUID", false, "string"), gkaCF("NodeUID", "nodeUID", false, "string"), gkaCF("Key", "key", false, "string"),
		gkaCF("Value", "value", false, "string"), gkaCF("Effect", "effect", false, "string"), gkaCF("Policy", "policy", false, "v1alpha1.GateOwnershipPolicy"),
		gkaCF("CleanupPhase", "cleanupPhase", false, "v1alpha1.CleanupPhase"), gkaCF("CleanupRequestedAt", "cleanupRequestedAt", false, "v1.Time"),
		gkaCF("RecoveryStartedAt", "recoveryStartedAt", true, "*v1.Time"), gkaCF("LastRecoveryPointAt", "lastRecoveryPointAt", true, "*v1.Time"),
		gkaCF("RecoveryControllerID", "recoveryControllerID", true, "*string"), gkaCF("LastRecoveryDigest", "lastRecoveryDigest", true, "*string")}},
	{reflect.TypeOf(v1alpha1.NodePathStateStatus{}), []gkaCField{
		gkaCF("ObservedGeneration", "observedGeneration", false, "int64"), gkaCF("NodeRef", "nodeRef", false, "v1alpha1.ObjectRef"),
		gkaCF("GraphRevision", "graphRevision", true, "*string"), gkaCF("CollectorSession", "collectorSession", true, "*int64"),
		gkaCF("EvidenceCompleteness", "evidenceCompleteness", false, "v1alpha1.SnapshotCompleteness"),
		gkaCF("DeviceSummaries", "deviceSummaries", false, "[]v1alpha1.DeviceSummary"),
		gkaCF("GateOwnership", "gateOwnership", true, "*v1alpha1.GateOwnershipStatus"), gkaCF("Conditions", "conditions", false, "[]v1.Condition")}},
}

func gkaCTagOf(f reflect.StructField) (name string, omit, inline bool) {
	parts := strings.Split(f.Tag.Get("json"), ",")
	name = parts[0]
	for _, o := range parts[1:] {
		omit = omit || o == "omitempty"
		inline = inline || o == "inline"
	}
	return name, omit, inline
}

func gkaCKeys(t *testing.T, v any) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(gkaCJSON(t, v)), &m); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GKA-011: the three resources and their lists have the prescribed shape and
// implement runtime.Object through pointer receivers.
func TestGKA011_ResourceTypes(t *testing.T) {
	for _, r := range []struct {
		name, spec, status string
		res, list          reflect.Type
	}{
		{"GPUFleet", "GPUFleetSpec", "GPUFleetStatus", reflect.TypeOf(v1alpha1.GPUFleet{}), reflect.TypeOf(v1alpha1.GPUFleetList{})},
		{"GPUDevice", "GPUDeviceSpec", "GPUDeviceStatus", reflect.TypeOf(v1alpha1.GPUDevice{}), reflect.TypeOf(v1alpha1.GPUDeviceList{})},
		{"NodePathState", "NodePathStateSpec", "NodePathStateStatus", reflect.TypeOf(v1alpha1.NodePathState{}), reflect.TypeOf(v1alpha1.NodePathStateList{})},
	} {
		want := []struct {
			goName, json string
			omit, inline bool
			typ          string
		}{
			{"TypeMeta", "", false, true, "v1.TypeMeta"}, {"ObjectMeta", "metadata", true, false, "v1.ObjectMeta"},
			{"Spec", "spec", false, false, "v1alpha1." + r.spec}, {"Status", "status", true, false, "v1alpha1." + r.status},
		}
		if r.res.NumField() != len(want) {
			t.Errorf("GKA-011: %s has %d fields, want %d", r.name, r.res.NumField(), len(want))
			continue
		}
		for i, w := range want {
			f := r.res.Field(i)
			name, omit, inline := gkaCTagOf(f)
			if f.Name != w.goName || name != w.json || omit != w.omit || inline != w.inline || f.Type.String() != w.typ {
				t.Errorf("GKA-011: %s field %d = %s %s json=%q, want %s %s json=%q omitempty=%v inline=%v",
					r.name, i, f.Name, f.Type, f.Tag.Get("json"), w.goName, w.typ, w.json, w.omit, w.inline)
			}
			if (w.goName == "TypeMeta" || w.goName == "ObjectMeta") && !f.Anonymous {
				t.Errorf("GKA-011: %s.%s must be an embedded field", r.name, w.goName)
			}
		}
		wantList := []struct {
			goName, json string
			omit, inline bool
			typ          string
		}{
			{"TypeMeta", "", false, true, "v1.TypeMeta"}, {"ListMeta", "metadata", true, false, "v1.ListMeta"},
			{"Items", "items", false, false, "[]v1alpha1." + r.name},
		}
		if r.list.NumField() != len(wantList) {
			t.Errorf("GKA-011: %s has %d fields, want %d", r.list.Name(), r.list.NumField(), len(wantList))
			continue
		}
		for i, w := range wantList {
			f := r.list.Field(i)
			name, omit, inline := gkaCTagOf(f)
			if f.Name != w.goName || name != w.json || omit != w.omit || inline != w.inline || f.Type.String() != w.typ {
				t.Errorf("GKA-011: %s field %d = %s %s json=%q, want %s %s json=%q", r.list.Name(), i, f.Name, f.Type, f.Tag.Get("json"), w.goName, w.typ, w.json)
			}
		}
		// pointer receiver only: the value type does not implement runtime.Object.
		if !reflect.PointerTo(r.res).Implements(reflect.TypeOf((*runtime.Object)(nil)).Elem()) {
			t.Errorf("GKA-011: *%s does not implement runtime.Object", r.name)
		}
		if r.res.Implements(reflect.TypeOf((*runtime.Object)(nil)).Elem()) {
			t.Errorf("GKA-011: %s (value) implements runtime.Object; DeepCopyObject must have a pointer receiver", r.name)
		}
	}
}

// GKA-012, GKA-013: Go names, JSON names, omitempty and types of every spec and
// status struct, and the JSON keys of zero and fully populated values.
func TestGKA012_013_SpecAndStatusShapes(t *testing.T) {
	for _, s := range gkaCTypeShapes {
		name := s.typ.Name()
		if s.typ.NumField() != len(s.fields) {
			t.Errorf("GKA-012/013: %s has %d fields, want exactly %d", name, s.typ.NumField(), len(s.fields))
			continue
		}
		var nonOmit, all []string
		for i, w := range s.fields {
			f := s.typ.Field(i)
			jn, omit, _ := gkaCTagOf(f)
			if f.Name != w.Go || jn != w.JSON || omit != w.Omit || f.Type.String() != w.Type {
				t.Errorf("GKA-012/013: %s field %d = %s %s json=%q, want %s %s json=%q omitempty=%v",
					name, i, f.Name, f.Type, f.Tag.Get("json"), w.Go, w.Type, w.JSON, w.Omit)
			}
			all = append(all, w.JSON)
			if !w.Omit {
				nonOmit = append(nonOmit, w.JSON)
			}
		}
		sort.Strings(all)
		sort.Strings(nonOmit)
		zero := reflect.New(s.typ).Elem().Interface()
		if got := gkaCKeys(t, zero); !reflect.DeepEqual(got, nonOmit) {
			t.Errorf("GKA-012/013: zero %s marshals the keys %v, want %v (only omitempty members are left out)", name, got, nonOmit)
		}
		full := reflect.New(s.typ)
		gkaCFill(full.Elem(), new(int))
		if got := gkaCKeys(t, full.Interface()); !reflect.DeepEqual(got, all) {
			t.Errorf("GKA-012/013: populated %s marshals the keys %v, want %v", name, got, all)
		}
	}
}

// GKA-012: an unset Mode leaves `mode` out of the JSON (so the server default
// Audit applies); a set Mode is written; the canarySelector is left out unless set.
func TestGKA012_ModeOmittedWhenEmpty(t *testing.T) {
	var spec v1alpha1.GPUFleetSpec
	if keys := gkaCKeys(t, spec); gkaCHas(keys, "mode") || gkaCHas(keys, "canarySelector") {
		t.Errorf("GKA-012: an empty spec marshals %v; mode and canarySelector must be omitted", keys)
	}
	spec.Mode = v1alpha1.FleetModeEnforce
	b := gkaCJSON(t, spec)
	if !strings.Contains(b, `"mode":"Enforce"`) {
		t.Errorf("GKA-012: an Enforce spec marshals %s, want mode Enforce", b)
	}
	claim := gkaCJSON(t, v1alpha1.InventoryClaim{Vendor: "v", Source: "s", EvidenceID: "e"})
	if strings.Contains(claim, "uuid") || strings.Contains(claim, "serial") {
		t.Errorf("GKA-012: a claim without uuid/serial marshals %s; nil pointers are omitted", claim)
	}
	empty := ""
	claim = gkaCJSON(t, v1alpha1.InventoryClaim{Vendor: "v", UUID: &empty, Source: "s", EvidenceID: "e"})
	if !strings.Contains(claim, `"uuid":""`) {
		t.Errorf("GKA-012: a pointer to an empty string must still be written (%s): presence and emptiness differ", claim)
	}
	req := gkaCJSON(t, v1alpha1.LifecycleRequest{ID: "r"})
	if !strings.Contains(req, `"reason":""`) {
		t.Errorf("GKA-012: an empty request reason must still be written (%s): reason has no omitempty", req)
	}
}

func gkaCHas(s []string, x string) bool {
	for _, e := range s {
		if e == x {
			return true
		}
	}
	return false
}

// gkaCConst is one exported constant of GKA-014.
type gkaCConst struct {
	name, typ, val string
	got            any
}

func gkaCConsts() []gkaCConst {
	c := []gkaCConst{
		{"FleetModeAudit", "FleetMode", "Audit", v1alpha1.FleetModeAudit},
		{"FleetModeEnforce", "FleetMode", "Enforce", v1alpha1.FleetModeEnforce},
		{"DesiredStateInService", "DesiredState", "InService", v1alpha1.DesiredStateInService},
		{"DesiredStateMaintenance", "DesiredState", "Maintenance", v1alpha1.DesiredStateMaintenance},
		{"DesiredStateRetired", "DesiredState", "Retired", v1alpha1.DesiredStateRetired},
		{"QualificationQualified", "Qualification", "Qualified", v1alpha1.QualificationQualified},
		{"QualificationDisqualified", "Qualification", "Disqualified", v1alpha1.QualificationDisqualified},
		{"QualificationUnknown", "Qualification", "Unknown", v1alpha1.QualificationUnknown},
		{"LifecyclePhasePending", "LifecyclePhase", "Pending", v1alpha1.LifecyclePhasePending},
		{"LifecyclePhaseValidating", "LifecyclePhase", "Validating", v1alpha1.LifecyclePhaseValidating},
		{"LifecyclePhaseReady", "LifecyclePhase", "Ready", v1alpha1.LifecyclePhaseReady},
		{"LifecyclePhaseDegraded", "LifecyclePhase", "Degraded", v1alpha1.LifecyclePhaseDegraded},
		{"LifecyclePhaseMaintenancePending", "LifecyclePhase", "MaintenancePending", v1alpha1.LifecyclePhaseMaintenancePending},
		{"LifecyclePhaseMaintenanceReady", "LifecyclePhase", "MaintenanceReady", v1alpha1.LifecyclePhaseMaintenanceReady},
		{"LifecyclePhaseRetiring", "LifecyclePhase", "Retiring", v1alpha1.LifecyclePhaseRetiring},
		{"LifecyclePhaseRetired", "LifecyclePhase", "Retired", v1alpha1.LifecyclePhaseRetired},
		{"LifecyclePhaseUnknown", "LifecyclePhase", "Unknown", v1alpha1.LifecyclePhaseUnknown},
		{"BindingStateBound", "BindingState", "Bound", v1alpha1.BindingStateBound},
		{"BindingStateUnknown", "BindingState", "Unknown", v1alpha1.BindingStateUnknown},
		{"BindingStateConflict", "BindingState", "Conflict", v1alpha1.BindingStateConflict},
		{"AllocationStateEmpty", "AllocationState", "Empty", v1alpha1.AllocationStateEmpty},
		{"AllocationStateInUse", "AllocationState", "InUse", v1alpha1.AllocationStateInUse},
		{"AllocationStateUnknown", "AllocationState", "Unknown", v1alpha1.AllocationStateUnknown},
		{"CoverageStateNormal", "CoverageState", "Normal", v1alpha1.CoverageStateNormal},
		{"CoverageStateMissing", "CoverageState", "Missing", v1alpha1.CoverageStateMissing},
		{"CoverageStateUnknown", "CoverageState", "Unknown", v1alpha1.CoverageStateUnknown},
		{"CoverageStateUnsupported", "CoverageState", "Unsupported", v1alpha1.CoverageStateUnsupported},
		{"SnapshotCompletenessUnknown", "SnapshotCompleteness", "Unknown", v1alpha1.SnapshotCompletenessUnknown},
		{"SnapshotCompletenessComplete", "SnapshotCompleteness", "Complete", v1alpha1.SnapshotCompletenessComplete},
		{"SnapshotCompletenessPartial", "SnapshotCompleteness", "Partial", v1alpha1.SnapshotCompletenessPartial},
		{"CleanupPhaseNone", "CleanupPhase", "None", v1alpha1.CleanupPhaseNone},
		{"CleanupPhasePending", "CleanupPhase", "Pending", v1alpha1.CleanupPhasePending},
		{"CleanupPhaseStable", "CleanupPhase", "Stable", v1alpha1.CleanupPhaseStable},
		{"CleanupPhaseCompleted", "CleanupPhase", "Completed", v1alpha1.CleanupPhaseCompleted},
		{"CleanupPhaseOrphaned", "CleanupPhase", "Orphaned", v1alpha1.CleanupPhaseOrphaned},
		{"CleanupPhaseOwnershipConflict", "CleanupPhase", "OwnershipConflict", v1alpha1.CleanupPhaseOwnershipConflict},
	}
	plain := func(name, val string, got any) { c = append(c, gkaCConst{name, "string", val, got}) }
	plain("ConditionTypeFleetReady", "FleetReady", v1alpha1.ConditionTypeFleetReady)
	plain("ConditionTypeDeviceQualified", "DeviceQualified", v1alpha1.ConditionTypeDeviceQualified)
	plain("ConditionTypeLifecycleReady", "LifecycleReady", v1alpha1.ConditionTypeLifecycleReady)
	plain("ConditionTypeEvidenceFresh", "EvidenceFresh", v1alpha1.ConditionTypeEvidenceFresh)
	plain("ConditionTypeIdentityBound", "IdentityBound", v1alpha1.ConditionTypeIdentityBound)
	plain("ConditionTypeAllocationKnown", "AllocationKnown", v1alpha1.ConditionTypeAllocationKnown)
	plain("ConditionTypeNodeEligible", "NodeEligible", v1alpha1.ConditionTypeNodeEligible)
	plain("ConditionTypeGateOwned", "GateOwned", v1alpha1.ConditionTypeGateOwned)
	plain("ConditionTypeCleanupReady", "CleanupReady", v1alpha1.ConditionTypeCleanupReady)
	plain("ReasonReady", "Ready", v1alpha1.ReasonReady)
	plain("ReasonValidating", "Validating", v1alpha1.ReasonValidating)
	plain("ReasonDegraded", "Degraded", v1alpha1.ReasonDegraded)
	plain("ReasonCoverageMissing", "CoverageMissing", v1alpha1.ReasonCoverageMissing)
	plain("ReasonEvidenceStale", "EvidenceStale", v1alpha1.ReasonEvidenceStale)
	plain("ReasonIdentityConflict", "IdentityConflict", v1alpha1.ReasonIdentityConflict)
	plain("ReasonUIDMismatch", "UIDMismatch", v1alpha1.ReasonUIDMismatch)
	plain("ReasonAllocationUnknown", "AllocationUnknown", v1alpha1.ReasonAllocationUnknown)
	plain("ReasonFenceMissing", "FenceMissing", v1alpha1.ReasonFenceMissing)
	plain("ReasonConflict", "Conflict", v1alpha1.ReasonConflict)
	plain("ReasonNoMatchingDevices", "NoMatchingDevices", v1alpha1.ReasonNoMatchingDevices)
	plain("ReasonPartialSnapshot", "PartialSnapshot", v1alpha1.ReasonPartialSnapshot)
	plain("ReasonBundleMismatch", "BundleMismatch", v1alpha1.ReasonBundleMismatch)
	plain("ReasonFutureObservation", "FutureObservation", v1alpha1.ReasonFutureObservation)
	plain("ReasonUntrustedSource", "UntrustedSource", v1alpha1.ReasonUntrustedSource)
	plain("ReasonUnadmittedSnapshot", "UnadmittedSnapshot", v1alpha1.ReasonUnadmittedSnapshot)
	plain("ReasonTargetCapacityExceeded", "TargetCapacityExceeded", v1alpha1.ReasonTargetCapacityExceeded)
	plain("ReasonCleanupPending", "CleanupPending", v1alpha1.ReasonCleanupPending)
	plain("ReasonCleanupStable", "CleanupStable", v1alpha1.ReasonCleanupStable)
	plain("ReasonCleanupCompleted", "CleanupCompleted", v1alpha1.ReasonCleanupCompleted)
	plain("ReasonOwnershipConflict", "OwnershipConflict", v1alpha1.ReasonOwnershipConflict)
	plain("ReasonInternalError", "InternalError", v1alpha1.ReasonInternalError)
	plain("PathKindGPUPCIeParent", "gpu-pcie-parent", v1alpha1.PathKindGPUPCIeParent)
	plain("PathKindGPUPCIeRoot", "gpu-pcie-root", v1alpha1.PathKindGPUPCIeRoot)
	plain("PathKindGPUPCIeLinkWidthNormal", "gpu-pcie-link-width-normal", v1alpha1.PathKindGPUPCIeLinkWidthNormal)
	plain("PathKindGPUNICSharedAncestor", "gpu-nic-shared-ancestor", v1alpha1.PathKindGPUNICSharedAncestor)
	plain("PathKindNICLLDPRemote", "nic-lldp-remote", v1alpha1.PathKindNICLLDPRemote)
	plain("NodeConditionTypeGPUFleetReady", "DataPathGPUFleetReady", v1alpha1.NodeConditionTypeGPUFleetReady)
	return c
}

// GKA-014: every constant exists with its exact string value; enum constants
// have their enum type (a `type X string`), the others are plain strings.
func TestGKA014_EnumsAndConstants(t *testing.T) {
	consts := gkaCConsts()
	if len(consts) != 36+9+22+5+1 {
		t.Fatalf("test table: %d constants", len(consts))
	}
	seenName := map[string]bool{}
	for _, c := range consts {
		if seenName[c.name] {
			t.Errorf("test table: duplicate %s", c.name)
		}
		seenName[c.name] = true
		rt := reflect.TypeOf(c.got)
		if rt.Kind() != reflect.String {
			t.Errorf("GKA-014: %s has kind %s, want string", c.name, rt.Kind())
			continue
		}
		if c.typ == "string" {
			if rt.Name() != "string" || rt.PkgPath() != "" {
				t.Errorf("GKA-014: %s has type %s.%s, want an untyped/plain string constant", c.name, rt.PkgPath(), rt.Name())
			}
		} else if rt.Name() != c.typ || !strings.HasSuffix(rt.PkgPath(), gkaCPkgSuffix) {
			t.Errorf("GKA-014: %s has type %s.%s, want %s.%s (type %s string)", c.name, rt.PkgPath(), rt.Name(), gkaCPkgSuffix, c.typ, c.typ)
		}
		if got := reflect.ValueOf(c.got).String(); got != c.val {
			t.Errorf("GKA-014: %s = %q, want %q", c.name, got, c.val)
		}
	}
	// value uniqueness inside each value set.
	byType := map[string]map[string]string{}
	for _, c := range consts {
		if byType[c.typ] == nil {
			byType[c.typ] = map[string]string{}
		}
		if prev, dup := byType[c.typ][c.val]; dup && c.typ != "string" {
			t.Errorf("GKA-014: %s and %s share the value %q", prev, c.name, c.val)
		}
		byType[c.typ][c.val] = c.name
	}
	// the untyped string constants: condition types, V22 reasons, pathKinds are
	// pairwise distinct within their family.
	family := func(prefix string) map[string]bool {
		m := map[string]bool{}
		for _, c := range consts {
			if c.typ == "string" && strings.HasPrefix(c.name, prefix) {
				if m[c.val] {
					t.Errorf("GKA-014: duplicate value %q in %s*", c.val, prefix)
				}
				m[c.val] = true
			}
		}
		return m
	}
	if n := len(family("ConditionType")); n != 9 {
		t.Errorf("GKA-014: %d condition types, want 9", n)
	}
	if n := len(family("Reason")); n != 22 {
		t.Errorf("GKA-014: %d reasons, want the 22 of V22", n)
	}
	if n := len(family("PathKind")); n != 5 {
		t.Errorf("GKA-014: %d path kinds, want 5", n)
	}
}

// GKA-015: every struct has DeepCopy/DeepCopyInto, copies are deep for every
// pointer/slice/map/time/selector, nil stays nil, and the file is generated.
func TestGKA015_DeepCopyIsDeepForEveryType(t *testing.T) {
	types := []reflect.Type{}
	for _, s := range gkaCTypeShapes {
		types = append(types, s.typ)
	}
	for _, k := range gkaCKinds {
		types = append(types, reflect.TypeOf(k.obj).Elem())
	}
	if len(types) != 27 {
		t.Fatalf("test table: %d types, want 21 helper structs + 6 resources/lists", len(types))
	}
	for _, rt := range types {
		name := rt.Name()
		ptr := reflect.PointerTo(rt)
		dc, ok1 := ptr.MethodByName("DeepCopy")
		dci, ok2 := ptr.MethodByName("DeepCopyInto")
		if !ok1 || !ok2 {
			t.Errorf("GKA-015: *%s lacks DeepCopy or DeepCopyInto", name)
			continue
		}
		if dc.Type.NumOut() != 1 || dc.Type.Out(0) != ptr {
			t.Errorf("GKA-015: %s.DeepCopy returns %v, want *%s", name, dc.Type, name)
			continue
		}
		if dci.Type.NumIn() != 2 || dci.Type.In(1) != ptr {
			t.Errorf("GKA-015: %s.DeepCopyInto takes %v, want *%s", name, dci.Type, name)
			continue
		}

		orig := reflect.New(rt)
		gkaCFill(orig.Elem(), new(int))
		before := gkaCJSON(t, orig.Interface())

		// DeepCopy
		cp := dc.Func.Call([]reflect.Value{orig})[0]
		if cp.IsNil() || cp.Pointer() == orig.Pointer() {
			t.Errorf("GKA-015: %s.DeepCopy returned nil or the receiver itself", name)
			continue
		}
		if got := gkaCJSON(t, cp.Interface()); got != before {
			t.Errorf("GKA-015: %s.DeepCopy changed the content", name)
		}
		gkaCScramble(cp.Elem())
		if got := gkaCJSON(t, orig.Interface()); got != before {
			t.Errorf("GKA-015: changing the copy of %s changed the original (a slice, pointer, map, time or selector is shared)", name)
		}
		if gkaCJSON(t, cp.Interface()) == before {
			t.Errorf("test self-check: the scramble left %s unchanged", name)
		}

		// DeepCopyInto
		out := reflect.New(rt)
		dci.Func.Call([]reflect.Value{orig, out})
		if got := gkaCJSON(t, out.Interface()); got != before {
			t.Errorf("GKA-015: %s.DeepCopyInto changed the content", name)
		}
		gkaCScramble(out.Elem())
		if got := gkaCJSON(t, orig.Interface()); got != before {
			t.Errorf("GKA-015: changing the DeepCopyInto target of %s changed the original", name)
		}

		// zero value: nil pointers and nil slices stay nil.
		zero := reflect.New(rt)
		zcp := dc.Func.Call([]reflect.Value{zero})[0]
		if !reflect.DeepEqual(zero.Interface(), zcp.Interface()) {
			t.Errorf("GKA-015: %s.DeepCopy of the zero value is not equal to it (nil must stay nil): %#v", name, zcp.Interface())
		}
		// nil receiver.
		if nilCopy := dc.Func.Call([]reflect.Value{reflect.Zero(ptr)})[0]; !nilCopy.IsNil() {
			t.Errorf("GKA-015: (*%s)(nil).DeepCopy() must return nil", name)
		}
	}
}

// GKA-015: DeepCopyObject of the six runtime.Objects returns a distinct copy of
// the same dynamic type.
func TestGKA015_DeepCopyObject(t *testing.T) {
	for _, k := range gkaCKinds {
		obj := reflect.New(reflect.TypeOf(k.obj).Elem())
		gkaCFill(obj.Elem(), new(int))
		before := gkaCJSON(t, obj.Interface())
		cp := obj.Interface().(runtime.Object).DeepCopyObject()
		if reflect.TypeOf(cp) != obj.Type() || reflect.ValueOf(cp).Pointer() == obj.Pointer() {
			t.Errorf("GKA-015: %s.DeepCopyObject returned %T (same pointer: %v)", k.kind, cp, reflect.ValueOf(cp).Pointer() == obj.Pointer())
			continue
		}
		gkaCScramble(reflect.ValueOf(cp).Elem())
		if gkaCJSON(t, obj.Interface()) != before {
			t.Errorf("GKA-015: changing the DeepCopyObject result of %s changed the original", k.kind)
		}
	}
}

// GKA-015: the deepcopy file is generated output (zz_generated.deepcopy.go,
// controller-gen header) and part of the package.
func TestGKA015_DeepCopyFileIsGenerated(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-f", `{{join .GoFiles "\n"}}`, "./internal/kubernetes/api/v1alpha1")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("GKA-015: go list ./internal/kubernetes/api/v1alpha1: %v", err)
	}
	files := strings.Fields(string(out))
	if !gkaCHas(files, "zz_generated.deepcopy.go") {
		t.Errorf("GKA-015/GKA-191: the package files %v lack zz_generated.deepcopy.go", files)
	}
	head, err := gkaCReadHead(filepath.Join(root, "internal", "kubernetes", "api", "v1alpha1", "zz_generated.deepcopy.go"), 400)
	if err != nil {
		t.Fatalf("GKA-015: %v", err)
	}
	if !strings.Contains(head, "Code generated by controller-gen. DO NOT EDIT.") {
		t.Errorf("GKA-015/GKA-191: zz_generated.deepcopy.go does not start with the controller-gen generated-code header:\n%s", head)
	}
}

func gkaCReadHead(path string, n int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) > n {
		b = b[:n]
	}
	return string(b), nil
}
