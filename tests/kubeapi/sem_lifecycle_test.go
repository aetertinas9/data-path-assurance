package kubeapi_test

// GKA-100..105: NodePathState creation, ownerReference, deletion rows (a)(b)(c),
// Node replacement, and the field managers of create/ownerReference/status.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// gkaSOwnerRefProblem returns "" when the NodePathState carries exactly the one
// Node ownerReference of GKA-101 (controller/blockOwnerDeletion unset or false).
func gkaSOwnerRefProblem(p *v1alpha1.NodePathState, n *corev1.Node) string {
	if len(p.OwnerReferences) != 1 {
		return fmt.Sprintf("ownerReferences = %d entries, want exactly 1", len(p.OwnerReferences))
	}
	r := p.OwnerReferences[0]
	if r.APIVersion != "v1" || r.Kind != "Node" || r.Name != n.Name || r.UID != n.UID {
		return fmt.Sprintf("ownerReference = %s/%s %s %s, want v1/Node %s %s", r.APIVersion, r.Kind, r.Name, r.UID, n.Name, n.UID)
	}
	if (r.Controller != nil && *r.Controller) || (r.BlockOwnerDeletion != nil && *r.BlockOwnerDeletion) {
		return "ownerReference sets controller or blockOwnerDeletion to true"
	}
	return ""
}

// GKA-100 + GKA-101 + GKA-104 + GKA-105: a NodePathState is created (named like the
// Node, spec.nodeRef only) for a selected node, a node without devices and a Conflict
// node, but not for an unselected node; it has one ownerReference and no finalizer, the
// managers are dpa-nodepath-spec (create) and dpa-nodepath-status (status), a missing or
// wrong ownerReference is repaired, and nothing outside status is ever changed.
func TestGKA100_101_104_105_NodePathStateCreationOwnerReferenceAndManagers(t *testing.T) {
	w := newGkaSWorld(t)
	keyG := "gka-sem/g"
	nA := w.selNode("na")   // selected, has a device
	nB := w.selNode("nb")   // selected, no device
	nC := w.node("nc", nil) // selected by nobody
	nD := w.node("nd", map[string]string{gkaSSelKey: w.sel, keyG: w.sel})
	f := w.selFleet("f")
	w.fleet("g", metav1.LabelSelector{MatchLabels: map[string]string{keyG: w.sel}})
	w.device("da", nA, f)
	w.device("dd", nD, f)
	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	w.waitNPS(nA.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	w.waitNPS(nB.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "NoMatchingDevices") })
	w.waitNPS(nD.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Conflict") })
	gkaSSettle(3)

	for _, n := range []*corev1.Node{nA, nB, nD} {
		p := w.npsNow(n.Name)
		where := "GKA-100 " + n.Name
		if p.Spec.NodeRef != (v1alpha1.ObjectRef{Name: n.Name, UID: string(n.UID)}) {
			t.Errorf("%s: spec.nodeRef = %#v, want {%s %s}", where, p.Spec.NodeRef, n.Name, n.UID)
		}
		if why := gkaSOwnerRefProblem(p, n); why != "" {
			t.Errorf("GKA-101 %s: %s", n.Name, why)
		}
		if len(p.Finalizers) != 0 {
			t.Errorf("GKA-101 %s: finalizers = %v, want none", n.Name, p.Finalizers)
		}
		if !gkaSHasManaged(p, "dpa-nodepath-spec", "Update", "") {
			t.Errorf("GKA-104 %s: no managedFields entry {dpa-nodepath-spec Update} for the create: %v", n.Name, gkaSManagedFields(p))
		}
		if !gkaSHasManaged(p, "dpa-nodepath-status", "Update", "status") {
			t.Errorf("GKA-104/120 %s: no managedFields entry {dpa-nodepath-status Update status}: %v", n.Name, gkaSManagedFields(p))
		}
		if p.Generation != 1 || p.Status.ObservedGeneration != 1 {
			t.Errorf("GKA-104 %s: generation/observedGeneration = %d/%d, want 1/1 (spec is never updated)", n.Name, p.Generation, p.Status.ObservedGeneration)
		}
		if p.Status.CollectorSession != nil || p.Status.GateOwnership != nil {
			t.Errorf("GKA-105 %s: a new NodePathState has collectorSession/gateOwnership = %v/%v, want none", n.Name, p.Status.CollectorSession, p.Status.GateOwnership)
		}
	}
	if w.hasNPS(nC.Name) {
		t.Errorf("GKA-100: NodePathState created for the unselected node %s", nC.Name)
	}

	// GKA-101: a wrong and a missing ownerReference are repaired to the single Node reference (an extra
	// foreign entry is not tested: the spec does not say whether the repair removes it).
	repair := func(what string, mut func(p *v1alpha1.NodePathState)) {
		t.Helper()
		w.updateNPS(nA.Name, mut)
		w.waitNPS(nA.Name, func(p *v1alpha1.NodePathState) string { return gkaSOwnerRefProblem(p, nA) })
		if got := w.npsNow(nA.Name); got.Generation != 1 || got.Spec.NodeRef.UID != string(nA.UID) || !gkaSHasManaged(got, "dpa-nodepath-spec", "Update", "") {
			t.Errorf("GKA-101/104 %s: after the repair generation=%d nodeRef=%#v managers=%v", what, got.Generation, got.Spec.NodeRef, gkaSManagedFields(got))
		}
	}
	repair("wrong uid", func(p *v1alpha1.NodePathState) {
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: nA.Name, UID: "some-other-uid"}}
	})
	repair("missing", func(p *v1alpha1.NodePathState) { p.OwnerReferences = nil })
}

func (w *gkaSWorld) seedNPS(name, nodeRefUID string) *v1alpha1.NodePathState {
	w.t.Helper()
	p := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: name, UID: nodeRefUID}},
	}
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(w.e.Client.Create(ctx, p), "seed nodepathstate "+name)
	p.Status = v1alpha1.NodePathStateStatus{
		ObservedGeneration: 1, NodeRef: p.Spec.NodeRef, GraphRevision: gkaSPtr("stale-revision"), EvidenceCompleteness: "Complete",
		DeviceSummaries: []v1alpha1.DeviceSummary{{Name: "stale-device", UID: "stale-device-uid", DesiredState: "InService", ObservedGeneration: 1, Qualification: "Qualified", LifecyclePhase: "Ready"}},
		Conditions: []metav1.Condition{{
			Type: "NodeEligible", Status: "True", Reason: "Ready", Message: "eligibility=Eligible qualification=Qualified",
			ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		}},
	}
	w.must(w.e.Client.Status().Update(ctx, p, client.FieldOwner(gkaSForeignManager)), "seed status of nodepathstate "+name)
	return p
}

// GKA-102 (rows a, b, c) + GKA-103: a NodePathState whose node is gone, whose node no
// fleet selects, or whose spec.nodeRef.uid is not the node's current UID is deleted, and a
// replacement node (same name, new UID) gets a brand new NodePathState (new uid, nodeRef,
// ownerReference) without any status carried over. Pre-existing objects are handled as soon
// as the caches are synced.
func TestGKA102_103_DeletionRowsAndNodeReplacement(t *testing.T) {
	w := newGkaSWorld(t)
	nStale := w.selNode("nstale")
	nUnsel := w.node("nunsel", nil)
	orphan := w.name("orphan") // no Node of this name exists
	f := w.selFleet("f")
	dStale := w.device("dstale", nStale, f)
	nDyn1, nDyn2, nDyn3 := w.selNode("ndyn1"), w.selNode("ndyn2"), w.selNode("ndyn3")
	w.device("ddyn1", nDyn1, f)
	w.device("ddyn2", nDyn2, f)
	dDyn3 := w.device("ddyn3", nDyn3, f)

	staleOld := w.seedNPS(nStale.Name, "stale-node-uid") // row (c): nodeRef.uid differs from the live Node
	w.seedNPS(nUnsel.Name, string(nUnsel.UID))           // row (b): no fleet selects the node
	w.seedNPS(orphan, "orphan-node-uid")                 // row (a): the Node does not exist

	w.start(gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	w.waitNPSGone(orphan)
	w.waitNPSGone(nUnsel.Name)
	fresh := w.waitNPS(nStale.Name, func(p *v1alpha1.NodePathState) string {
		if p.UID == staleOld.UID {
			return "still the stale object"
		}
		return gkaSNPSHas(p, "Ready")
	})
	if fresh.Spec.NodeRef.UID != string(nStale.UID) {
		t.Errorf("GKA-102(c): recreated spec.nodeRef.uid = %q, want %s", fresh.Spec.NodeRef.UID, nStale.UID)
	}
	if why := gkaSOwnerRefProblem(fresh, nStale); why != "" {
		t.Errorf("GKA-103: recreated NodePathState: %s", why)
	}
	if _, has := gkaSSummaries(fresh)["stale-device"]; has || (fresh.Status.GraphRevision != nil && *fresh.Status.GraphRevision == "stale-revision") {
		t.Errorf("GKA-103: the recreated NodePathState carries the stale status: %#v", fresh.Status)
	}
	if got := gkaSSummaries(fresh); len(got) != 1 || got[dStale.Name].UID != string(dStale.UID) {
		t.Errorf("GKA-103: recreated deviceSummaries = %#v, want the device %s", fresh.Status.DeviceSummaries, dStale.Name)
	}

	for _, n := range []*corev1.Node{nDyn1, nDyn2, nDyn3} {
		w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	}
	// Row (b): the label goes away, nothing selects the node any more.
	w.updateNode(nDyn1.Name, func(n *corev1.Node) { delete(n.Labels, gkaSSelKey) })
	w.waitNPSGone(nDyn1.Name)
	// Row (a): the Node is deleted (no garbage collector runs here: the controller deletes).
	w.deleteNode(nDyn2.Name)
	w.waitNPSGone(nDyn2.Name)
	// GKA-103: a replacement Node with a new UID.
	before := w.npsNow(nDyn3.Name)
	if got := gkaSSummaries(before); len(got) != 1 || got[dDyn3.Name].UID != string(dDyn3.UID) {
		t.Fatalf("test setup: the old NodePathState summarizes %#v", before.Status.DeviceSummaries)
	}
	w.deleteNode(nDyn3.Name)
	nNew := w.node("ndyn3", map[string]string{gkaSSelKey: w.sel})
	if nNew.UID == nDyn3.UID {
		t.Fatalf("test setup: the replacement node reused the UID")
	}
	dNew := w.device("dnew", nNew, f)
	after := w.waitNPS(nNew.Name, func(p *v1alpha1.NodePathState) string {
		if p.Spec.NodeRef.UID != string(nNew.UID) {
			return "spec.nodeRef.uid is still " + p.Spec.NodeRef.UID
		}
		// dDyn3 still names the old Node UID, so it is an X device: the new node is Partial.
		return gkaSNPSHas(p, "Validating")
	})
	if after.UID == before.UID {
		t.Errorf("GKA-103: the NodePathState of the replaced node was kept instead of being recreated")
	}
	if why := gkaSOwnerRefProblem(after, nNew); why != "" {
		t.Errorf("GKA-103: replacement NodePathState: %s", why)
	}
	if got := gkaSSummaries(after); len(got) != 1 || got[dNew.Name].UID != string(dNew.UID) {
		t.Errorf("GKA-103: replacement deviceSummaries = %#v, want only %s (old summaries are not copied)", after.Status.DeviceSummaries, dNew.Name)
	}
	// The device that still points at the old Node UID is a UIDMismatch now.
	w.waitDevice(dDyn3.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "UIDMismatch") })
}

// GKA-102: the controller's delete of a NodePathState carries a UID precondition (the UID of the
// object it decided on), for every deletion row: (a) node missing, (b) no fleet selects the node,
// (c) spec.nodeRef.uid differs from the live Node. The DeleteOptions body is read on the wire.
func TestGKA102_DeleteCarriesUIDPrecondition(t *testing.T) {
	w := newGkaSWorld(t)
	nStale := w.selNode("nstale")
	nUnsel := w.node("nunsel", nil)
	orphan := w.name("orphan")
	f := w.selFleet("f")
	w.device("dstale", nStale, f)
	stale := w.seedNPS(nStale.Name, "stale-node-uid")   // (c)
	unsel := w.seedNPS(nUnsel.Name, string(nUnsel.UID)) // (b)
	orph := w.seedNPS(orphan, "orphan-node-uid")        // (a)

	cfg, rec := gkaRecordingConfig(w.e.Config)
	var mu sync.Mutex
	seen := map[string][]string{} // NodePathState name -> precondition UID of each DELETE ("<none>" if absent)
	rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodDelete || !strings.Contains(req.URL.Path, "/nodepathstates/") {
			return nil
		}
		var opts struct {
			Preconditions *struct {
				UID *string `json:"uid"`
			} `json:"preconditions"`
		}
		uid := "<none>"
		if body := gkaSBody(req); body != "" && json.Unmarshal([]byte(body), &opts) == nil && opts.Preconditions != nil && opts.Preconditions.UID != nil {
			uid = *opts.Preconditions.UID
		}
		mu.Lock()
		seen[path.Base(req.URL.Path)] = append(seen[path.Base(req.URL.Path)], uid)
		mu.Unlock()
		return nil // let the real delete through
	})
	gkaSStart(t, cfg, gkaSNewAssessor(newGkaSScript(gkaSPlanReady)), newGkaFakeClock(gkaST0))

	w.waitNPSGone(orphan)
	w.waitNPSGone(nUnsel.Name)
	w.waitNPS(nStale.Name, func(p *v1alpha1.NodePathState) string {
		if p.UID == stale.UID {
			return "still the stale object"
		}
		return ""
	})
	mu.Lock()
	defer mu.Unlock()
	for name, want := range map[string]string{orphan: string(orph.UID), nUnsel.Name: string(unsel.UID), nStale.Name: string(stale.UID)} {
		got := seen[name]
		if len(got) == 0 {
			t.Errorf("GKA-102: no DELETE of NodePathState %s was seen on the wire", name)
			continue
		}
		for _, uid := range got {
			if uid != want {
				t.Errorf("GKA-102: DELETE of %s carried preconditions.uid = %q, want the deleted object's uid %q", name, uid, want)
			}
		}
	}
}
