package kubeapi_test

// GKA-112..114 (Node condition by server-side apply), GKA-120..124 (status
// Update, spec and metadata untouched, semantic no-op, conflict retry, foreign
// entries). Error handling (125), independence (126), restart (127), verbs (128)
// and the transport seam (027) are in rt_error_test.go.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// gkaRPrepareNode gives a Node the kubelet-owned and operator-owned state the
// controller must never disturb: a Ready condition, an extra taint, an annotation
// and spec.unschedulable.
func gkaRPrepareNode(t *testing.T, e *gkaEnvT, name string) {
	t.Helper()
	gkaRSetNodeReady(t, e, name)
	gkaRRetry(t, "prepare node "+name, func() error {
		n := gkaRGetNode(t, e, name)
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "gka.io/custom", Value: "v", Effect: corev1.TaintEffectNoSchedule})
		n.Spec.Unschedulable = true
		if n.Annotations == nil {
			n.Annotations = map[string]string{}
		}
		n.Annotations["gka.io/note"] = "keep"
		return e.Client.Update(context.Background(), n)
	})
}

// gkaRNodeWithoutOurCondition returns the Node conditions minus DataPathGPUFleetReady.
func gkaRNodeWithoutOurCondition(n *corev1.Node) []corev1.NodeCondition {
	var out []corev1.NodeCondition
	for _, c := range n.Status.Conditions {
		if string(c.Type) != gkaRNodeCondType {
			out = append(out, c)
		}
	}
	return out
}

func gkaRTypesOf(conds []metav1.Condition) []string {
	var out []string
	for _, c := range conds {
		out = append(out, c.Type)
	}
	return out
}

// ---------------------------------------------------------------------------
// GKA-112 / 120 / 121
// ---------------------------------------------------------------------------

func TestGKA112_120_121_WriteMethodsManagersAndUntouchedFields(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit)
	for _, n := range sc.Nodes {
		gkaRPrepareNode(t, e, n.Name)
	}
	beforeFleet := gkaRGetFleet(t, e, sc.Fleet.Name)
	var beforeDevs []*v1alpha1.GPUDevice
	for _, d := range sc.Devices {
		beforeDevs = append(beforeDevs, gkaRGetDevice(t, e, d.Name))
	}
	var beforeNodes []*corev1.Node
	for _, n := range sc.Nodes {
		beforeNodes = append(beforeNodes, gkaRGetNode(t, e, n.Name))
	}

	rig := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	for _, n := range sc.Nodes {
		name := n.Name
		gkaEventually(t, 20*time.Second, func() (bool, string) {
			return gkaRNodeCond(gkaRGetNode(t, e, name)) != nil, "node " + name + " has no DataPathGPUFleetReady condition yet"
		})
	}
	gkaRWaitQuiet(t, rig.Rec, "", 200*time.Millisecond)
	entries := rig.Rec.WriteEntries("")

	// Which (verb, resource) pairs were written (GKA-120/121/112).
	written := map[string]int{}
	for _, en := range entries {
		written[gkaRClassify(en.Method, en.Path, en.Watch).key()]++
	}
	for _, forbidden := range []string{"update gpufleets", "patch gpufleets", "update gpudevices", "patch gpudevices", "create gpudevices", "delete gpudevices",
		"update nodes", "patch nodes", "delete nodes", "create nodes", "update nodepathstates", "patch nodepathstates", "patch nodepathstates/status", "patch gpufleets/status", "patch gpudevices/status"} {
		if written[forbidden] != 0 {
			t.Errorf("GKA-112/120/121: the controller issued %d %q request(s)", written[forbidden], forbidden)
		}
	}
	for _, required := range []string{"update gpufleets/status", "update gpudevices/status", "create nodepathstates", "update nodepathstates/status", "patch nodes/status"} {
		if written[required] == 0 {
			t.Errorf("GKA-112/120: no %q request was recorded (writes: %v)", required, written)
		}
	}

	// GKA-120: status PUT with the read object's resourceVersion (optimistic concurrency).
	for _, d := range sc.Devices {
		puts := gkaRStatusPUTs(rig.Rec, "gpudevices", d.Name)
		if len(puts) == 0 {
			t.Errorf("GKA-120: no status PUT for device %s", d.Name)
		}
		for _, p := range puts {
			if gkaRBodyResourceVersion(p.Body) == "" {
				t.Errorf("GKA-120: status PUT for %s carries no metadata.resourceVersion", d.Name)
			}
		}
	}
	for _, p := range gkaRStatusPUTs(rig.Rec, "gpufleets", sc.Fleet.Name) {
		if gkaRBodyResourceVersion(p.Body) == "" {
			t.Errorf("GKA-120: fleet status PUT carries no metadata.resourceVersion")
		}
	}

	// GKA-112: every Node write is a force-less server-side apply of exactly one condition.
	for _, n := range sc.Nodes {
		nodeWrites := rig.Rec.WriteEntries(gkaRPathFor("nodes", n.Name))
		if len(nodeWrites) == 0 {
			t.Errorf("GKA-112: no write for Node %s", n.Name)
		}
		for _, w := range nodeWrites {
			if w.Method != http.MethodPatch || !strings.HasSuffix(w.Path, "/nodes/"+n.Name+"/status") {
				t.Errorf("GKA-112: Node write %s; only PATCH on nodes/status is allowed", w)
				continue
			}
			if !strings.HasPrefix(w.CT, "application/apply-patch+yaml") {
				t.Errorf("GKA-112: Node PATCH Content-Type = %q, want application/apply-patch+yaml", w.CT)
			}
			q, _ := url.ParseQuery(w.RawQuery)
			if q.Get("fieldManager") != "dpa-node-condition" {
				t.Errorf("GKA-112/025: fieldManager = %q, want dpa-node-condition", q.Get("fieldManager"))
			}
			if f := q.Get("force"); f != "" && f != "false" {
				t.Errorf("GKA-112: force=%q; the apply must not force", f)
			}
			body := gkaRJSONMap(w.Body)
			if got := gkaRRawKeys(body); !reflect.DeepEqual(got, []string{"apiVersion", "kind", "metadata", "status"}) {
				t.Errorf("GKA-112: apply body keys = %v, want apiVersion, kind, metadata, status", got)
			}
			md, _ := body["metadata"].(map[string]any)
			if got := gkaRRawKeys(md); !reflect.DeepEqual(got, []string{"name"}) || md["name"] != n.Name {
				t.Errorf("GKA-112: apply metadata = %v, want only name=%s", md, n.Name)
			}
			st, _ := body["status"].(map[string]any)
			conds, _ := st["conditions"].([]any)
			if got := gkaRRawKeys(st); !reflect.DeepEqual(got, []string{"conditions"}) || len(conds) != 1 {
				t.Errorf("GKA-112: apply status = %v, want conditions with exactly one entry", st)
			}
			if bytes.Contains(w.Body, []byte("resourceVersion")) {
				t.Errorf("GKA-112/123: the Node apply body must not carry a resourceVersion")
			}
		}
	}

	// GKA-120/121/112: managedFields.
	fleet := gkaRGetFleet(t, e, sc.Fleet.Name)
	if !gkaRHasManager(fleet.ManagedFields, "dpa-fleet-status", metav1.ManagedFieldsOperationUpdate, "status") {
		t.Errorf("GKA-120: GPUFleet managedFields lack dpa-fleet-status/Update/status: %v", gkaRManagerNames(fleet.ManagedFields))
	}
	for _, mf := range fleet.ManagedFields {
		if mf.Subresource == "" && strings.HasPrefix(mf.Manager, "dpa-") {
			t.Errorf("GKA-121: the controller wrote GPUFleet spec/metadata as %q", mf.Manager)
		}
	}
	if !reflect.DeepEqual(beforeFleet.Spec, fleet.Spec) || !reflect.DeepEqual(beforeFleet.Labels, fleet.Labels) || !reflect.DeepEqual(beforeFleet.Annotations, fleet.Annotations) ||
		!reflect.DeepEqual(beforeFleet.Finalizers, fleet.Finalizers) || !reflect.DeepEqual(beforeFleet.OwnerReferences, fleet.OwnerReferences) || beforeFleet.Generation != fleet.Generation {
		t.Errorf("GKA-121: GPUFleet spec or metadata changed")
	}
	for i, d := range sc.Devices {
		dev := gkaRGetDevice(t, e, d.Name)
		if !gkaRHasManager(dev.ManagedFields, "dpa-device-status", metav1.ManagedFieldsOperationUpdate, "status") {
			t.Errorf("GKA-120: GPUDevice managedFields lack dpa-device-status/Update/status: %v", gkaRManagerNames(dev.ManagedFields))
		}
		for _, mf := range dev.ManagedFields {
			if mf.Subresource == "" && strings.HasPrefix(mf.Manager, "dpa-") {
				t.Errorf("GKA-121: the controller wrote GPUDevice spec/metadata as %q", mf.Manager)
			}
		}
		b := beforeDevs[i]
		if !reflect.DeepEqual(b.Spec, dev.Spec) || !reflect.DeepEqual(b.Labels, dev.Labels) || !reflect.DeepEqual(b.Annotations, dev.Annotations) ||
			!reflect.DeepEqual(b.Finalizers, dev.Finalizers) || !reflect.DeepEqual(b.OwnerReferences, dev.OwnerReferences) || b.Generation != dev.Generation {
			t.Errorf("GKA-121: GPUDevice %s spec or metadata changed", d.Name)
		}
	}
	for i, n := range sc.Nodes {
		nps, ok := gkaRTryNPS(e, n.Name)
		if !ok {
			t.Fatalf("GKA-100: NodePathState %s missing", n.Name)
		}
		if !gkaRHasManager(nps.ManagedFields, "dpa-nodepath-spec", metav1.ManagedFieldsOperationUpdate, "") ||
			!gkaRHasManager(nps.ManagedFields, "dpa-nodepath-status", metav1.ManagedFieldsOperationUpdate, "status") {
			t.Errorf("GKA-104/120: NodePathState managers = %v, want dpa-nodepath-spec (create) and dpa-nodepath-status/Update/status", gkaRManagerNames(nps.ManagedFields))
		}
		want := v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: n.Name, UID: string(n.UID)}}
		if !reflect.DeepEqual(nps.Spec, want) || len(nps.Finalizers) != 0 {
			t.Errorf("GKA-100/101: NodePathState spec=%+v finalizers=%v, want %+v and none", nps.Spec, nps.Finalizers, want)
		}
		if len(nps.OwnerReferences) != 1 {
			t.Fatalf("GKA-101: ownerReferences = %v, want exactly one", nps.OwnerReferences)
		}
		or := nps.OwnerReferences[0]
		if or.APIVersion != "v1" || or.Kind != "Node" || or.Name != n.Name || or.UID != n.UID ||
			(or.Controller != nil && *or.Controller) || (or.BlockOwnerDeletion != nil && *or.BlockOwnerDeletion) {
			t.Errorf("GKA-101: ownerReference = %+v", or)
		}

		after := gkaRGetNode(t, e, n.Name)
		if !gkaRHasManager(after.ManagedFields, "dpa-node-condition", metav1.ManagedFieldsOperationApply, "status") || !gkaRManagerOwnsNodeCond(after, "dpa-node-condition") {
			t.Errorf("GKA-112: Node managedFields = %v, want dpa-node-condition/Apply/status owning the condition", gkaRManagerNames(after.ManagedFields))
		}
		b := beforeNodes[i]
		if !reflect.DeepEqual(b.Spec, after.Spec) || !reflect.DeepEqual(b.Labels, after.Labels) || !reflect.DeepEqual(b.Annotations, after.Annotations) ||
			!reflect.DeepEqual(b.Finalizers, after.Finalizers) || !reflect.DeepEqual(b.OwnerReferences, after.OwnerReferences) {
			t.Errorf("GKA-112/121: Node %s spec (taints, unschedulable) or metadata changed:\nbefore %+v\nafter  %+v", n.Name, b.Spec, after.Spec)
		}
		if !reflect.DeepEqual(b.Status.Conditions, gkaRNodeWithoutOurCondition(after)) {
			t.Errorf("GKA-112: Node %s other conditions (Ready, heartbeat) changed:\nbefore %+v\nafter  %+v", n.Name, b.Status.Conditions, after.Status.Conditions)
		}
	}
}

// Another manager owns the condition: the force-less apply conflicts (409
// FieldManagerConflict), the controller does not overwrite it, tries once per pass
// and takes over the moment the other manager lets go.
func TestGKA112_124_ForeignOwnedNodeConditionIsNotOverwritten(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	node := sc.Nodes[0]
	gkaRSetNodeReady(t, e, node.Name)
	if err := gkaRApplyNodeCond(e, node.Name, "gka-other", "True", "Other", "other-owned", false); err != nil {
		t.Fatalf("seed the foreign condition: %v", err)
	}
	rig := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	time.Sleep(1500 * time.Millisecond)

	n := gkaRGetNode(t, e, node.Name)
	c := gkaRNodeCond(n)
	if c == nil || c.Status != corev1.ConditionTrue || c.Reason != "Other" || c.Message != "other-owned" {
		t.Fatalf("GKA-112/124: the foreign-owned condition was overwritten: %+v", c)
	}
	if gkaRManagerOwnsNodeCond(n, "dpa-node-condition") || !gkaRManagerOwnsNodeCond(n, "gka-other") {
		t.Errorf("GKA-124: ownership changed: %v", gkaRManagerNames(n.ManagedFields))
	}
	patches := len(rig.Rec.WriteEntries(gkaRPathFor("nodes", node.Name)))
	assessed := rig.A.AssessCount() // read after patches: each pass patches at most once after its assessor call
	if patches < 2 {
		t.Errorf("GKA-112: only %d apply attempt(s); the controller must retry on later passes", patches)
	}
	if patches > assessed {
		t.Errorf("GKA-112/125: %d apply attempts in %d passes; a FieldManagerConflict is tried once per pass", patches, assessed)
	}
	for _, w := range rig.Rec.WriteEntries(gkaRPathFor("nodes", node.Name)) {
		if w.Method != http.MethodPatch {
			t.Errorf("GKA-112: unexpected Node write %s", w)
		}
	}
	// The other manager lets go: the controller's next pass publishes its own entry.
	if err := gkaRApplyNodeCond(e, node.Name, "gka-other", "", "", "", false); err != nil {
		t.Fatalf("release the foreign condition: %v", err)
	}
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		n := gkaRGetNode(t, e, node.Name)
		return gkaRManagerOwnsNodeCond(n, "dpa-node-condition") && gkaRNodeCond(n) != nil && gkaRNodeCond(n).Reason != "Other",
			fmt.Sprintf("managers %v cond %+v", gkaRManagerNames(n.ManagedFields), gkaRNodeCond(n))
	})
}

// ---------------------------------------------------------------------------
// GKA-113
// ---------------------------------------------------------------------------

func TestGKA113_UnselectedNodeConditionIsRemovedOnlyWhenSelfPublished(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit)
	for _, n := range sc.Nodes {
		gkaRSetNodeReady(t, e, n.Name)
	}
	foreignNode := gkaRNode(t, e, gkaName(t, "foreign"), nil) // never selected
	if err := gkaRApplyNodeCond(e, foreignNode.Name, "gka-other", "True", "Other", "other-owned", false); err != nil {
		t.Fatalf("seed the foreign condition: %v", err)
	}
	plainNode := gkaRNode(t, e, gkaName(t, "plain"), nil) // never selected, no condition

	rig := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	for _, n := range sc.Nodes {
		name := n.Name
		gkaEventually(t, 20*time.Second, func() (bool, string) {
			return gkaRNodeCond(gkaRGetNode(t, e, name)) != nil, "no condition on " + name
		})
	}
	// Never-selected nodes are left alone, whoever owns a same-typed entry.
	gkaRWaitQuiet(t, rig.Rec, "", 200*time.Millisecond)
	for _, n := range []*corev1.Node{foreignNode, plainNode} {
		if w := rig.Rec.Writes(gkaRPathFor("nodes", n.Name)); len(w) != 0 {
			t.Errorf("GKA-113: the controller wrote to never-selected Node %s: %v", n.Name, w)
		}
	}
	fn := gkaRGetNode(t, e, foreignNode.Name)
	if c := gkaRNodeCond(fn); c == nil || c.Reason != "Other" || !gkaRManagerOwnsNodeCond(fn, "gka-other") {
		t.Errorf("GKA-113: another manager's condition on an unselected node was touched: %+v", c)
	}

	// Node 1 leaves the fleet: the self-published entry is removed with an empty apply; Ready stays.
	victim := sc.Nodes[1]
	tUnselect := time.Now()
	gkaRSetNodeLabels(t, e, victim.Name, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		n := gkaRGetNode(t, e, victim.Name)
		return gkaRNodeCond(n) == nil && !gkaRManagerOwnsNodeCond(n, "dpa-node-condition"), fmt.Sprintf("cond=%+v managers=%v", gkaRNodeCond(n), gkaRManagerNames(n.ManagedFields))
	})
	var removal *gkaRecEntry
	for _, w := range rig.Rec.WriteEntries(gkaRPathFor("nodes", victim.Name)) {
		if w.At.After(tUnselect) && w.Method == http.MethodPatch {
			w := w
			removal = &w
		}
	}
	if removal == nil {
		t.Fatal("GKA-113: no PATCH was recorded for the removal")
	}
	st, _ := gkaRJSONMap(removal.Body)["status"].(map[string]any)
	if conds, ok := st["conditions"].([]any); !ok || len(conds) != 0 {
		t.Errorf("GKA-113: the removal must apply conditions: [], got %v", st)
	}
	if q, _ := url.ParseQuery(removal.RawQuery); q.Get("fieldManager") != "dpa-node-condition" {
		t.Errorf("GKA-113: the removal uses manager %q, want dpa-node-condition", q.Get("fieldManager"))
	}
	after := gkaRGetNode(t, e, victim.Name)
	ready := false
	for _, c := range after.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Errorf("GKA-113: removing the condition also removed Ready: %+v", after.Status.Conditions)
	}
	// Steady state after the removal: no write for the node (its NodePathState delete came first).
	gkaRWaitQuiet(t, rig.Rec, victim.Name, 200*time.Millisecond)
	start := time.Now()
	time.Sleep(3*200*time.Millisecond + 2*time.Second)
	if w := gkaRWritesSince(rig.Rec, victim.Name, start); len(w) != 0 {
		t.Errorf("GKA-113/175: writes for the unselected node in the steady-state window: %v", w)
	}

	// GKA-110: a fleet switched to Enforce no longer publishes and withdraws its condition.
	keep := sc.Nodes[0]
	gkaRRetry(t, "switch fleet to Enforce", func() error {
		fl := gkaRGetFleet(t, e, sc.Fleet.Name)
		fl.Spec.Mode = v1alpha1.FleetModeEnforce
		fl.Spec.CanarySelector = &metav1.LabelSelector{MatchLabels: map[string]string{"gka.data-path-assurance.io/canary": "yes"}}
		return e.Client.Update(context.Background(), fl)
	})
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		n := gkaRGetNode(t, e, keep.Name)
		return gkaRNodeCond(n) == nil, fmt.Sprintf("Enforce fleet: condition still present %+v", gkaRNodeCond(n))
	})
	gkaRRetry(t, "switch fleet back to Audit", func() error {
		fl := gkaRGetFleet(t, e, sc.Fleet.Name)
		fl.Spec.Mode = v1alpha1.FleetModeAudit
		return e.Client.Update(context.Background(), fl)
	})
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return gkaRNodeCond(gkaRGetNode(t, e, keep.Name)) != nil, "Audit fleet: condition not published again"
	})
}

// ---------------------------------------------------------------------------
// GKA-114
// ---------------------------------------------------------------------------

func TestGKA114_EqualConditionIsNotRewrittenAndChangesKeepTransitionTime(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	node := sc.Nodes[0]
	rig := gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	var first *corev1.NodeCondition
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		first = gkaRNodeCond(gkaRGetNode(t, e, node.Name))
		return first != nil, "no condition yet"
	})
	before := gkaRGetNode(t, e, node.Name)

	// Equal condition: no write and an unchanged Node resourceVersion.
	gkaRQuietWindow(t, rig.Rec, gkaRPathFor("nodes", node.Name), 200*time.Millisecond)
	if after := gkaRGetNode(t, e, node.Name); after.ResourceVersion != before.ResourceVersion {
		t.Errorf("GKA-114: the Node resourceVersion changed from %s to %s although the condition did not", before.ResourceVersion, after.ResourceVersion)
	}

	// A second fleet selecting the same node changes reason and message but not the status.
	gkaRFleet(t, e, gkaName(t, "fl2"), sc.Selector, v1alpha1.FleetModeAudit)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		c := gkaRNodeCond(gkaRGetNode(t, e, node.Name))
		return c != nil && c.Reason == "Conflict", fmt.Sprintf("condition = %+v", c)
	})
	c := gkaRNodeCond(gkaRGetNode(t, e, node.Name))
	if c.Status != first.Status {
		t.Fatalf("test premise: status changed from %s to %s", first.Status, c.Status)
	}
	if !c.LastTransitionTime.Equal(&first.LastTransitionTime) {
		t.Errorf("GKA-114/074(c): lastTransitionTime changed (%s -> %s) although only reason and message changed", first.LastTransitionTime, c.LastTransitionTime)
	}
	gkaRQuietWindow(t, rig.Rec, gkaRPathFor("nodes", node.Name), 200*time.Millisecond)
}

// ---------------------------------------------------------------------------
// GKA-122
// ---------------------------------------------------------------------------

func TestGKA122_SemanticNoOpSteadyStateAndGenerationChange(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 2, v1alpha1.FleetModeAudit)
	for _, n := range sc.Nodes {
		gkaRSetNodeReady(t, e, n.Name)
	}
	rig := gkaRStart(t, e, gkaRReadyScript, nil)
	gkaRWaitSettled(t, e, sc)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		fl := gkaRGetFleet(t, e, sc.Fleet.Name)
		c := gkaRCond(fl.Status.Conditions, "FleetReady")
		return c != nil && c.Status == metav1.ConditionTrue, fmt.Sprintf("fleet conditions = %+v (a Ready fake decision must render FleetReady True)", fl.Status.Conditions)
	})
	top, _ := gkaRPrefixes(t.Name())

	snapshot := func() map[string]string {
		rv := map[string]string{}
		fl := gkaRGetFleet(t, e, sc.Fleet.Name)
		rv["fleet"] = fl.ResourceVersion
		for _, d := range sc.Devices {
			rv["device/"+d.Name] = gkaRGetDevice(t, e, d.Name).ResourceVersion
		}
		for _, n := range sc.Nodes {
			nps, _ := gkaRTryNPS(e, n.Name)
			if nps != nil {
				rv["nps/"+n.Name] = nps.ResourceVersion
			}
			rv["node/"+n.Name] = gkaRGetNode(t, e, n.Name).ResourceVersion
		}
		return rv
	}
	gkaRWaitQuiet(t, rig.Rec, top, 200*time.Millisecond)
	before := snapshot()
	c0 := rig.A.AssessCount()
	gkaRQuietWindow(t, rig.Rec, top, 200*time.Millisecond)
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("GKA-122: resourceVersions changed in the steady state:\nbefore %v\nafter  %v", before, after)
	}
	if rig.A.AssessCount()-c0 < 16 {
		t.Errorf("GKA-141: only %d assessor calls in the steady-state window; ticks must keep re-assessing", rig.A.AssessCount()-c0)
	}

	// observedGeneration is a semantic change: only the edited device is written.
	target := sc.Devices[0]
	gkaRRetry(t, "edit device", func() error {
		d := gkaRGetDevice(t, e, target.Name)
		d.Spec.Request.Reason = "changed reason"
		return e.Client.Update(context.Background(), d)
	})
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		d := gkaRGetDevice(t, e, target.Name)
		return d.Generation == 2 && d.Status.ObservedGeneration == 2, fmt.Sprintf("generation %d observedGeneration %d", d.Generation, d.Status.ObservedGeneration)
	})
	gkaRWaitQuiet(t, rig.Rec, top, 200*time.Millisecond)
	for _, d := range sc.Devices[1:] {
		if rv := gkaRGetDevice(t, e, d.Name).ResourceVersion; rv != before["device/"+d.Name] {
			t.Errorf("GKA-122: device %s was rewritten (resourceVersion %s -> %s) although its status did not change", d.Name, before["device/"+d.Name], rv)
		}
	}
}

// During a 409 another actor writes exactly what the controller wanted to write:
// the retry re-reads the object (a direct GET) and must not write again.
func TestGKA122_123_RetryFindsSameContentWrittenByAnotherActor(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	dev := sc.Devices[0]
	rig := gkaRNewRig(e)
	attempts := gkaRInjectStatusPUT(rig.Rec, "gpudevices", dev.Name, func(req *http.Request, n int64) *http.Response {
		if n != 1 {
			return nil
		}
		want, err := gkaRDecodeDevice(gkaRReqBody(req))
		cur := &v1alpha1.GPUDevice{}
		if err == nil {
			err = e.Client.Get(context.Background(), client.ObjectKey{Name: dev.Name}, cur)
		}
		if err == nil {
			cur.Status = want.Status
			err = e.Client.Status().Update(context.Background(), cur)
		}
		if err != nil {
			t.Errorf("the other actor's write failed: %v", err)
		}
		return gkaRConflict(req)
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	gkaRWaitQuiet(t, rig.Rec, "", 200*time.Millisecond)
	time.Sleep(1500 * time.Millisecond)
	if n := attempts.Load(); n != 1 {
		t.Errorf("GKA-122: %d status PUT attempts; after the other actor wrote the same content no further write is needed", n)
	}
	if len(gkaRSingleGETs(rig.Rec, "gpudevices", dev.Name)) == 0 {
		t.Errorf("GKA-123: the retry did not GET the latest object from the API server")
	}
	got := gkaRGetDevice(t, e, dev.Name)
	if gkaRHasManager(got.ManagedFields, "dpa-device-status", metav1.ManagedFieldsOperationUpdate, "status") {
		t.Errorf("GKA-122: dpa-device-status wrote although the other actor already had: %v", gkaRManagerNames(got.ManagedFields))
	}
	if got.Status.ObservedRequestID != dev.Spec.Request.ID {
		t.Errorf("status = %+v", got.Status)
	}
}

// ---------------------------------------------------------------------------
// GKA-123
// ---------------------------------------------------------------------------

func gkaRConflict(req *http.Request) *http.Response {
	return gkaRStatusResponse(req, http.StatusConflict, metav1.StatusReasonConflict, "Operation cannot be fulfilled: the object has been modified; please apply your changes to the latest version and try again")
}

// Two devices in one scene: device 0 conflicts on its first four PUTs (the fifth
// attempt of the same pass succeeds), device 1 on its first five (the pass gives up,
// a later pass succeeds).
func TestGKA123_125_ConflictRetryBoundsBackoffAndDirectGET(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit)
	fourth, fifth := sc.Devices[0], sc.Devices[1]
	rig := gkaRNewRig(e)
	var n4, n5 atomic.Int64
	suffix4, suffix5 := gkaRPathFor("gpudevices", fourth.Name)+"/status", gkaRPathFor("gpudevices", fifth.Name)+"/status"
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut {
			return nil
		}
		switch {
		case strings.HasSuffix(req.URL.Path, suffix4):
			if n4.Add(1) <= 4 {
				return gkaRConflict(req)
			}
		case strings.HasSuffix(req.URL.Path, suffix5):
			if n5.Add(1) <= 5 {
				return gkaRConflict(req)
			}
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	gkaRWaitQuiet(t, rig.Rec, "", 200*time.Millisecond)

	// Device 0: exactly five attempts, all inside one pass.
	puts := gkaRStatusPUTs(rig.Rec, "gpudevices", fourth.Name)
	if len(puts) != 5 || n4.Load() != 5 {
		t.Fatalf("GKA-123: %d PUT attempts recorded (%d seen by the injector), want exactly 5 in the retrying pass", len(puts), n4.Load())
	}
	base := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond}
	var total time.Duration
	for i, g := range gkaRGaps(puts) {
		total += g
		if float64(g) < 0.9*float64(base[i]) {
			t.Errorf("GKA-123: gap before attempt %d is %s, want at least the backoff %s (10ms doubling, plus jitter)", i+2, g, base[i])
		}
	}
	if total < 135*time.Millisecond {
		t.Errorf("GKA-123: the waits between five attempts sum to %s, want >= 150ms", total)
	}
	if total > 5*time.Second {
		t.Errorf("GKA-123: the retries took %s", total)
	}
	gets := 0 // direct GETs (not the cache) between the attempts
	for _, g := range gkaRSingleGETs(rig.Rec, "gpudevices", fourth.Name) {
		if g.At.After(puts[0].At) && g.At.Before(puts[4].At) {
			gets++
		}
	}
	if gets < 4 {
		t.Errorf("GKA-123: %d direct GETs of the object between the five attempts, want >= 4", gets)
	}
	if gkaRPassBetween(rig.A, string(sc.Nodes[0].UID), puts[0].At, puts[4].At) {
		t.Errorf("GKA-123: a new pass started between the retries of one write")
	}

	// Device 1: five conflicts end the pass; a later pass writes it.
	puts5 := gkaRStatusPUTs(rig.Rec, "gpudevices", fifth.Name)
	if len(puts5) != 6 {
		t.Fatalf("GKA-123: %d PUT attempts for the always-conflicting write, want 5 in the first pass and 1 in a later pass", len(puts5))
	}
	if gkaRPassBetween(rig.A, string(sc.Nodes[1].UID), puts5[0].At, puts5[4].At) {
		t.Errorf("GKA-123: the five attempts were not made in one pass")
	}
	if !gkaRPassBetween(rig.A, string(sc.Nodes[1].UID), puts5[4].At, puts5[5].At) {
		t.Errorf("GKA-123/125: the sixth attempt was made in the same pass; at most five attempts per pass are allowed")
	}
	if got := gkaRGetDevice(t, e, fifth.Name); got.Status.ObservedRequestID == "" {
		t.Errorf("GKA-123: the object never got its status")
	}
}

// While a PUT conflicts, another manager adds an entry the controller does not own:
// the retry re-reads the object and keeps it.
func TestGKA123_124_ForeignEntryAddedDuringConflictIsPreserved(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	dev := sc.Devices[0]
	foreign := metav1.Condition{
		Type: "EvidenceFresh", Status: metav1.ConditionTrue, ObservedGeneration: 1, LastTransitionTime: gkaRTime("2026-01-01T00:00:00Z"),
		Reason: "Ready", Message: "foreign device entry",
	}
	rig := gkaRNewRig(e)
	gkaRInjectStatusPUT(rig.Rec, "gpudevices", dev.Name, func(req *http.Request, n int64) *http.Response {
		if n != 1 {
			return nil
		}
		want, err := gkaRDecodeDevice(gkaRReqBody(req))
		cur := &v1alpha1.GPUDevice{}
		if err == nil {
			err = e.Client.Get(context.Background(), client.ObjectKey{Name: dev.Name}, cur)
		}
		if err == nil {
			cur.Status = want.Status
			cur.Status.ActualBinding.Reason = "stale-reason" // differs from what the controller wants
			cur.Status.Conditions = append(append([]metav1.Condition(nil), want.Status.Conditions...), foreign)
			err = e.Client.Status().Update(context.Background(), cur)
		}
		if err != nil {
			t.Errorf("the other actor's write failed: %v", err)
		}
		return gkaRConflict(req)
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		got := gkaRGetDevice(t, e, dev.Name)
		return got.Status.ActualBinding.Reason == "Validating", "actualBinding.reason = " + got.Status.ActualBinding.Reason
	})
	got := gkaRGetDevice(t, e, dev.Name)
	c := gkaRCond(got.Status.Conditions, "EvidenceFresh")
	if c == nil || !gkaRCondEqual(*c, foreign) {
		t.Errorf("GKA-123/124: the foreign condition was lost or changed: %+v", c)
	}
	types := gkaRTypesOf(got.Status.Conditions)
	if !sort.StringsAreSorted(types) {
		t.Errorf("GKA-074(a): conditions are not in type byte order: %v", types)
	}
}

func TestGKA123_AlreadyExistsOnCreateCountsAsSuccess(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	node := sc.Nodes[0]
	rig := gkaRNewRig(e)
	var injected atomic.Bool
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/nodepathstates") && injected.CompareAndSwap(false, true) {
			nps := &v1alpha1.NodePathState{
				ObjectMeta: metav1.ObjectMeta{
					Name:            node.Name,
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}},
				},
				Spec: v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: node.Name, UID: string(node.UID)}},
			}
			if err := e.Client.Create(context.Background(), nps); err != nil {
				t.Errorf("the racing creator failed: %v", err)
			}
			return gkaRStatusResponse(req, http.StatusConflict, metav1.StatusReasonAlreadyExists, "nodepathstates.infrastructure.data-path-assurance.io \""+node.Name+"\" already exists")
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	gkaRWaitQuiet(t, rig.Rec, "", 200*time.Millisecond)
	posts := 0
	for _, w := range rig.Rec.WriteEntries("") {
		if w.Method == http.MethodPost && strings.HasSuffix(w.Path, "/nodepathstates") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("GKA-123/125: %d NodePathState create attempts, want 1 (AlreadyExists is success and the object is re-read)", posts)
	}
	nps, ok := gkaRTryNPS(e, node.Name)
	if !ok || len(nps.Status.Conditions) == 0 {
		t.Fatalf("GKA-123: the NodePathState did not get its status after AlreadyExists: %+v", nps)
	}
	for _, c := range nps.Status.Conditions {
		if c.Reason == "InternalError" {
			t.Errorf("GKA-125: AlreadyExists was rendered as InternalError: %+v", c)
		}
	}
	for _, c := range gkaRGetDevice(t, e, sc.Devices[0].Name).Status.Conditions {
		if c.Reason == "InternalError" {
			t.Errorf("GKA-125: AlreadyExists made the device InternalError: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// GKA-124
// ---------------------------------------------------------------------------

func TestGKA124_ForeignConditionsAndPreservedFieldsSurviveEveryWrite(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	node, dev := sc.Nodes[0], sc.Devices[0]
	ctx := context.Background()

	fleetForeign := metav1.Condition{
		Type: "GateOwned", Status: metav1.ConditionFalse, ObservedGeneration: 1, LastTransitionTime: gkaRTime("2026-01-01T00:00:00Z"),
		Reason: "CleanupPending", Message: "foreign fleet entry",
	}
	owned := []v1alpha1.OwnedNodeRef{{Name: node.Name, UID: string(node.UID)}}
	fl := gkaRGetFleet(t, e, sc.Fleet.Name)
	fl.Status = v1alpha1.GPUFleetStatus{ObservedGeneration: 1, OwnedNodeRefs: owned, Conditions: []metav1.Condition{fleetForeign}}
	if err := e.Client.Status().Update(ctx, fl); err != nil {
		t.Fatalf("seed the fleet status: %v", err)
	}
	devForeign := metav1.Condition{
		Type: "EvidenceFresh", Status: metav1.ConditionTrue, ObservedGeneration: 1, LastTransitionTime: gkaRTime("2026-01-01T00:00:00Z"),
		Reason: "Ready", Message: "foreign device entry",
	}
	d := gkaRGetDevice(t, e, dev.Name)
	d.Status = gkaRColdDeviceStatus(d, "2026-01-01T00:00:00Z", []metav1.Condition{devForeign})
	if err := e.Client.Status().Update(ctx, d); err != nil {
		t.Fatalf("seed the device status: %v", err)
	}

	gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		f := gkaRGetFleet(t, e, sc.Fleet.Name)
		return gkaRCond(f.Status.Conditions, "FleetReady") != nil, "FleetReady not published yet"
	})
	f := gkaRGetFleet(t, e, sc.Fleet.Name)
	if types := gkaRTypesOf(f.Status.Conditions); !reflect.DeepEqual(types, []string{"FleetReady", "GateOwned"}) {
		t.Errorf("GKA-074(a)/124: fleet condition types = %v, want [FleetReady GateOwned] in byte order", types)
	}
	if c := gkaRCond(f.Status.Conditions, "GateOwned"); c == nil || !gkaRCondEqual(*c, fleetForeign) {
		t.Errorf("GKA-124: the foreign GateOwned entry changed: %+v", c)
	}
	if !reflect.DeepEqual(f.Status.OwnedNodeRefs, owned) {
		t.Errorf("GKA-105/124: ownedNodeRefs = %+v, want the value that was there %+v", f.Status.OwnedNodeRefs, owned)
	}
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return gkaRCond(gkaRGetDevice(t, e, dev.Name).Status.Conditions, "DeviceQualified") != nil, "the controller has not written the device conditions yet"
	})
	gd := gkaRGetDevice(t, e, dev.Name)
	if types := gkaRTypesOf(gd.Status.Conditions); !reflect.DeepEqual(types, []string{"AllocationKnown", "DeviceQualified", "EvidenceFresh", "IdentityBound", "LifecycleReady"}) {
		t.Errorf("GKA-074(a)/124: device condition types = %v", types)
	}
	if c := gkaRCond(gd.Status.Conditions, "EvidenceFresh"); c == nil || !gkaRCondEqual(*c, devForeign) {
		t.Errorf("GKA-124: the foreign EvidenceFresh entry changed: %+v", c)
	}

	// NodePathState: another manager sets collectorSession and a GateOwned entry; a later
	// controller write (new device -> new deviceSummaries) must keep both.
	session := int64(7)
	npsForeign := metav1.Condition{
		Type: "GateOwned", Status: metav1.ConditionFalse, ObservedGeneration: 1, LastTransitionTime: gkaRTime("2026-01-01T00:00:00Z"),
		Reason: "CleanupPending", Message: "foreign node entry",
	}
	gkaRRetry(t, "seed NodePathState status", func() error {
		nps, ok := gkaRTryNPS(e, node.Name)
		if !ok {
			return fmt.Errorf("NodePathState missing")
		}
		nps.Status.CollectorSession = &session
		nps.Status.Conditions = append(nps.Status.Conditions, npsForeign)
		return e.Client.Status().Update(ctx, nps)
	})
	second := gkaRDevice(t, e, gkaName(t, "n0-d1"), node, sc.Fleet)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		nps, ok := gkaRTryNPS(e, node.Name)
		return ok && len(nps.Status.DeviceSummaries) == 2, fmt.Sprintf("deviceSummaries after adding %s: %+v", second.Name, nps)
	})
	nps, _ := gkaRTryNPS(e, node.Name)
	if nps.Status.CollectorSession == nil || *nps.Status.CollectorSession != 7 {
		t.Errorf("GKA-105/124: collectorSession = %v, want 7 preserved", nps.Status.CollectorSession)
	}
	if c := gkaRCond(nps.Status.Conditions, "GateOwned"); c == nil || !gkaRCondEqual(*c, npsForeign) {
		t.Errorf("GKA-124: the foreign NodePathState entry changed: %+v", c)
	}
	if types := gkaRTypesOf(nps.Status.Conditions); !reflect.DeepEqual(types, []string{"EvidenceFresh", "GateOwned", "NodeEligible"}) {
		t.Errorf("GKA-074(a)/124: NodePathState condition types = %v", types)
	}
}
