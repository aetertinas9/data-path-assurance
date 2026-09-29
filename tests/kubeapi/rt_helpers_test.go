package kubeapi_test

// Shared helpers for the run-time and write tests (prefix gkaR). Everything a run-time/write test needs:
// object builders per spec section 12, status readers, managedFields parsing,
// verb classification of recorded requests (GKA-128/175), the steady-state window
// (GKA-175) and a valid fake assessment builder (spec appendix 17).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

const (
	gkaRFleetLabelKey = "gka.data-path-assurance.io/fleet"
	gkaRNodeCondType  = "DataPathGPUFleetReady"
	gkaRGraphRev      = "7:2:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// gkaRT0 is a whole-second instant, so ceil and truncate agree (GKA-071/074).
var gkaRT0 = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

func gkaRHex64(ch byte) string { return strings.Repeat(string(ch), 64) }

// ---------------------------------------------------------------------------
// object builders (spec section 12 fixtures)
// ---------------------------------------------------------------------------

// gkaRSelector is the per-test node label selector map.
func gkaRSelector(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{gkaRFleetLabelKey: gkaName(t, "sel")}
}

func gkaRNode(t *testing.T, e *gkaEnvT, name string, labels map[string]string) *corev1.Node {
	t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := e.Client.Create(context.Background(), n); err != nil {
		t.Fatalf("create Node %s: %v", name, err)
	}
	return n
}

func gkaRFleet(t *testing.T, e *gkaEnvT, name string, selector map[string]string, mode v1alpha1.FleetMode) *v1alpha1.GPUFleet {
	t.Helper()
	fl := &v1alpha1.GPUFleet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUFleetSpec{
			NodeSelector: metav1.LabelSelector{MatchLabels: selector},
			RequiredCoverage: []v1alpha1.FleetCoverageRequirement{
				{Name: "pcie-parent", PathKind: v1alpha1.PathKindGPUPCIeParent, Required: true},
				{Name: "pcie-root", PathKind: v1alpha1.PathKindGPUPCIeRoot, Required: true},
				{Name: "pcie-width", PathKind: v1alpha1.PathKindGPUPCIeLinkWidthNormal, Required: true},
			},
			FreshnessSeconds: 60,
			ReadyForSeconds:  30,
			Mode:             mode,
		},
	}
	if mode == v1alpha1.FleetModeEnforce {
		fl.Spec.CanarySelector = &metav1.LabelSelector{MatchLabels: map[string]string{"gka.data-path-assurance.io/canary": "yes"}}
	}
	if err := e.Client.Create(context.Background(), fl); err != nil {
		t.Fatalf("create GPUFleet %s: %v", name, err)
	}
	return fl
}

func gkaRDevice(t *testing.T, e *gkaEnvT, name string, node *corev1.Node, fl *v1alpha1.GPUFleet) *v1alpha1.GPUDevice {
	t.Helper()
	uuid := "GPU-" + gkaRHash(name, 32)
	d := &v1alpha1.GPUDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUDeviceSpec{
			NodeRef:  v1alpha1.ObjectRef{Name: node.Name, UID: string(node.UID)},
			FleetRef: v1alpha1.ObjectRef{Name: fl.Name, UID: string(fl.UID)},
			InventoryClaim: v1alpha1.InventoryClaim{
				Vendor: "NVIDIA", UUID: &uuid, Source: "operator/asset-db", EvidenceID: "asset-db:" + name,
			},
			DesiredState: v1alpha1.DesiredStateInService,
			Request:      v1alpha1.LifecycleRequest{ID: "enroll-1", Reason: "initial enrollment"},
		},
	}
	if err := e.Client.Create(context.Background(), d); err != nil {
		t.Fatalf("create GPUDevice %s: %v", name, err)
	}
	return d
}

// gkaRScene is a fleet with nodes and devices, all created and labelled.
type gkaRScene struct {
	Selector map[string]string
	Fleet    *v1alpha1.GPUFleet
	Nodes    []*corev1.Node
	Devices  []*v1alpha1.GPUDevice // Devices[i*perNode+j] is device j of node i
}

// gkaRNewScene creates nodes (labelled into the fleet), one Audit fleet and
// perNode devices per node. Nothing is running yet: no controller has started.
func gkaRNewScene(t *testing.T, e *gkaEnvT, nodes, perNode int, mode v1alpha1.FleetMode) *gkaRScene {
	t.Helper()
	sc := &gkaRScene{Selector: gkaRSelector(t)}
	for i := 0; i < nodes; i++ {
		sc.Nodes = append(sc.Nodes, gkaRNode(t, e, gkaName(t, fmt.Sprintf("n%d", i)), sc.Selector))
	}
	sc.Fleet = gkaRFleet(t, e, gkaName(t, "fl"), sc.Selector, mode)
	for i, n := range sc.Nodes {
		for j := 0; j < perNode; j++ {
			sc.Devices = append(sc.Devices, gkaRDevice(t, e, gkaName(t, fmt.Sprintf("n%d-d%d", i, j)), n, sc.Fleet))
		}
	}
	return sc
}

// ---------------------------------------------------------------------------
// readers
// ---------------------------------------------------------------------------

func gkaRGetNode(t *testing.T, e *gkaEnvT, name string) *corev1.Node {
	t.Helper()
	n := &corev1.Node{}
	if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, n); err != nil {
		t.Fatalf("get Node %s: %v", name, err)
	}
	return n
}

func gkaRGetFleet(t *testing.T, e *gkaEnvT, name string) *v1alpha1.GPUFleet {
	t.Helper()
	o := &v1alpha1.GPUFleet{}
	if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, o); err != nil {
		t.Fatalf("get GPUFleet %s: %v", name, err)
	}
	return o
}

func gkaRGetDevice(t *testing.T, e *gkaEnvT, name string) *v1alpha1.GPUDevice {
	t.Helper()
	o := &v1alpha1.GPUDevice{}
	if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, o); err != nil {
		t.Fatalf("get GPUDevice %s: %v", name, err)
	}
	return o
}

// gkaRTryNPS returns the NodePathState and whether it exists.
func gkaRTryNPS(e *gkaEnvT, name string) (*v1alpha1.NodePathState, bool) {
	o := &v1alpha1.NodePathState{}
	if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, o); err != nil {
		return nil, false
	}
	return o, true
}

func gkaRCond(conds []metav1.Condition, typ string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == typ {
			return &conds[i]
		}
	}
	return nil
}

func gkaRNodeCond(n *corev1.Node) *corev1.NodeCondition {
	for i := range n.Status.Conditions {
		if string(n.Status.Conditions[i].Type) == gkaRNodeCondType {
			return &n.Status.Conditions[i]
		}
	}
	return nil
}

// gkaRWaitSettled waits until every device has a status of the current request,
// every node has a NodePathState with conditions, and the fleet has conditions.
func gkaRWaitSettled(t *testing.T, e *gkaEnvT, sc *gkaRScene) {
	t.Helper()
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		for _, d := range sc.Devices {
			got := &v1alpha1.GPUDevice{}
			if err := e.Client.Get(context.Background(), client.ObjectKey{Name: d.Name}, got); err != nil {
				return false, err.Error()
			}
			if got.Status.ObservedRequestID != d.Spec.Request.ID || len(got.Status.Conditions) == 0 {
				return false, "device " + d.Name + " has no status yet"
			}
		}
		for _, n := range sc.Nodes {
			nps, ok := gkaRTryNPS(e, n.Name)
			if !ok || len(nps.Status.Conditions) == 0 {
				return false, "NodePathState " + n.Name + " has no status yet"
			}
		}
		fl := &v1alpha1.GPUFleet{}
		if err := e.Client.Get(context.Background(), client.ObjectKey{Name: sc.Fleet.Name}, fl); err != nil {
			return false, err.Error()
		}
		return len(fl.Status.Conditions) > 0, "fleet has no conditions yet"
	})
}

// gkaRSetNodeReady writes a kubelet-like Ready condition (GKA-176: no kubelet).
func gkaRSetNodeReady(t *testing.T, e *gkaEnvT, name string) {
	t.Helper()
	n := gkaRGetNode(t, e, name)
	hb := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{
		Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: hb, LastTransitionTime: hb,
		Reason: "KubeletReady", Message: "gka fixture",
	})
	if err := e.Client.Status().Update(context.Background(), n); err != nil {
		t.Fatalf("set Node %s Ready: %v", name, err)
	}
}

// ---------------------------------------------------------------------------
// managedFields
// ---------------------------------------------------------------------------

func gkaRHasManager(mf []metav1.ManagedFieldsEntry, manager string, op metav1.ManagedFieldsOperationType, subresource string) bool {
	for _, m := range mf {
		if m.Manager == manager && m.Operation == op && m.Subresource == subresource {
			return true
		}
	}
	return false
}

// gkaRManagerOwnsNodeCond reports whether a managedFields entry of manager
// (operation Apply, subresource status) owns the DataPathGPUFleetReady list key.
func gkaRManagerOwnsNodeCond(n *corev1.Node, manager string) bool {
	key := `k:{"type":"` + gkaRNodeCondType + `"}`
	for _, m := range n.ManagedFields {
		if m.Manager != manager || m.Operation != metav1.ManagedFieldsOperationApply || m.Subresource != "status" || m.FieldsV1 == nil {
			continue
		}
		var root map[string]map[string]map[string]json.RawMessage
		if json.Unmarshal(m.FieldsV1.Raw, &root) != nil {
			continue
		}
		if _, ok := root["f:status"]["f:conditions"][key]; ok {
			return true
		}
	}
	return false
}

func gkaRManagerNames(mf []metav1.ManagedFieldsEntry) []string {
	var out []string
	for _, m := range mf {
		out = append(out, fmt.Sprintf("%s/%s/%q", m.Manager, m.Operation, m.Subresource))
	}
	return out
}

// gkaRApplyNodeCond server-side applies one DataPathGPUFleetReady entry as
// manager on nodes/status (no force unless force is true). status "" applies
// conditions: [] instead (removes the manager's own entry).
func gkaRApplyNodeCond(e *gkaEnvT, node, manager, status, reason, message string, force bool) error {
	conds := []map[string]any{}
	if status != "" {
		conds = append(conds, map[string]any{
			"type": gkaRNodeCondType, "status": status, "reason": reason, "message": message,
			"lastTransitionTime": "2026-01-01T00:00:00Z",
		})
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": node},
		"status": map[string]any{"conditions": conds},
	})
	if err != nil {
		return err
	}
	_, err = e.Clientset.CoreV1().Nodes().Patch(context.Background(), node, types.ApplyPatchType, body,
		metav1.PatchOptions{FieldManager: manager, Force: &force}, "status")
	return err
}

// ---------------------------------------------------------------------------
// verb classification (GKA-128, GKA-175 (3))
// ---------------------------------------------------------------------------

type gkaRReq struct {
	Verb      string
	Resource  string // resource or resource/subresource
	Name      string
	Discovery bool
	Unknown   bool
}

func (r gkaRReq) key() string { return r.Verb + " " + r.Resource }

// gkaRClassify maps a request to (verb, resource): collection GET = list,
// watch=true GET = watch, single GET = get; discovery paths are flagged.
func gkaRClassify(method, path string, watch bool) gkaRReq {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	disc := gkaRReq{Discovery: true}
	if len(segs) == 0 || segs[0] == "" {
		return disc
	}
	var rest []string
	switch segs[0] {
	case "version", "openapi", "healthz", "livez", "readyz":
		return disc
	case "api":
		if len(segs) <= 2 {
			return disc
		}
		rest = segs[2:]
	case "apis":
		if len(segs) <= 3 {
			return disc
		}
		rest = segs[3:]
	default:
		return gkaRReq{Verb: method, Resource: path, Unknown: true}
	}
	if rest[0] == "namespaces" && len(rest) >= 3 {
		rest = rest[2:]
	}
	r := gkaRReq{Resource: rest[0]}
	if len(rest) > 1 {
		r.Name = rest[1]
	}
	if len(rest) > 2 {
		r.Resource += "/" + rest[2]
	}
	switch method {
	case http.MethodGet:
		switch {
		case watch:
			r.Verb = "watch"
		case r.Name == "":
			r.Verb = "list"
		default:
			r.Verb = "get"
		}
	case http.MethodPost:
		r.Verb = "create"
	case http.MethodPut:
		r.Verb = "update"
	case http.MethodPatch:
		r.Verb = "patch"
	case http.MethodDelete:
		r.Verb = "delete"
		if r.Name == "" {
			r.Verb = "deletecollection"
		}
	default:
		r.Verb = strings.ToLower(method)
	}
	return r
}

// gkaRAllowedVerbs is the GKA-128 verb set (S4a RBAC input).
var gkaRAllowedVerbs = map[string]bool{
	"get gpufleets": true, "list gpufleets": true, "watch gpufleets": true,
	"update gpufleets/status": true,
	"get gpudevices":          true, "list gpudevices": true, "watch gpudevices": true,
	"update gpudevices/status": true,
	"get nodepathstates":       true, "list nodepathstates": true, "watch nodepathstates": true,
	"create nodepathstates": true, "update nodepathstates": true, "delete nodepathstates": true,
	"update nodepathstates/status": true,
	"get nodes":                    true, "list nodes": true, "watch nodes": true,
	"patch nodes/status": true,
	"get leases":         true, "create leases": true, "update leases": true,
}

// gkaRVerbViolations lists every recorded request whose (verb, resource) is not
// in GKA-128 (discovery excluded) and every Event request.
func gkaRVerbViolations(entries []gkaRecEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, en := range entries {
		r := gkaRClassify(en.Method, en.Path, en.Watch)
		if r.Discovery {
			continue
		}
		k := r.key()
		if r.Unknown {
			k = "UNKNOWN " + en.Method + " " + en.Path
		}
		if strings.HasPrefix(r.Resource, "events") || strings.Contains(en.Path, "/events") {
			k = "EVENT " + en.Method + " " + en.Path
		} else if gkaRAllowedVerbs[k] {
			continue
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func gkaRAssertVerbs(t *testing.T, rec *gkaRecorder) {
	t.Helper()
	if v := gkaRVerbViolations(rec.Entries()); len(v) > 0 {
		t.Errorf("GKA-027/128/142: requests outside the allowed (verb, resource) set or Event requests: %v", v)
	}
}

// ---------------------------------------------------------------------------
// steady-state window (GKA-175)
// ---------------------------------------------------------------------------

// gkaRLastWrite is the time of the newest write matching prefix (zero if none).
func gkaRLastWrite(rec *gkaRecorder, prefix string) time.Time {
	var last time.Time
	for _, e := range rec.WriteEntries(prefix) {
		if e.At.After(last) {
			last = e.At
		}
	}
	return last
}

// gkaRWritesSince returns the matching writes that entered the transport after at.
func gkaRWritesSince(rec *gkaRecorder, prefix string, at time.Time) []gkaRecEntry {
	var out []gkaRecEntry
	for _, e := range rec.WriteEntries(prefix) {
		if e.At.After(at) {
			out = append(out, e)
		}
	}
	return out
}

// gkaRQuietWindow implements the GKA-175 zero-write assertion: wait until the last
// matching write is at least 2 x resync old, observe for 3 x resync + 2 s, and fail
// when any matching write entered the transport in that window.
func gkaRQuietWindow(t *testing.T, rec *gkaRecorder, prefix string, resync time.Duration) {
	t.Helper()
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		last := gkaRLastWrite(rec, prefix)
		return last.IsZero() || time.Since(last) >= 2*resync, "last write at " + last.String()
	})
	start := time.Now()
	time.Sleep(3*resync + 2*time.Second)
	if w := gkaRWritesSince(rec, prefix, start); len(w) > 0 {
		var got []string
		for _, e := range w {
			got = append(got, e.String())
		}
		t.Errorf("GKA-122/175: %d write request(s) in the steady-state window (prefix %q): %v", len(w), prefix, got)
	}
}

// ---------------------------------------------------------------------------
// leases
// ---------------------------------------------------------------------------

func gkaRLease(e *gkaEnvT, ns, id string) (*coordinationv1.Lease, bool) {
	l, err := e.Clientset.CoordinationV1().Leases(ns).Get(context.Background(), id, metav1.GetOptions{})
	if err != nil {
		return nil, false
	}
	return l, true
}

// gkaRLeaseHolder returns the Lease holderIdentity ("" when nil or absent).
func gkaRLeaseHolder(e *gkaEnvT, ns, id string) (string, bool) {
	l, ok := gkaRLease(e, ns, id)
	if !ok {
		return "", false
	}
	if l.Spec.HolderIdentity == nil {
		return "", true
	}
	return *l.Spec.HolderIdentity, true
}

func gkaRWaitHolder(t *testing.T, e *gkaEnvT, ns, id, holder string, timeout time.Duration) {
	t.Helper()
	gkaEventually(t, timeout, func() (bool, string) {
		h, ok := gkaRLeaseHolder(e, ns, id)
		return ok && h == holder, fmt.Sprintf("lease %s/%s holder=%q found=%v, want %q", ns, id, h, ok, holder)
	})
}

// ---------------------------------------------------------------------------
// fake assessments (spec appendix 17: valid decisions computed from the request)
// ---------------------------------------------------------------------------

// gkaRReadyAssessment builds a valid Qualified/Ready DeviceAssessment for the
// intent, derived from the request so GKA-085 (a)(d) hold. now must be a whole
// second (GKA-071 ceil equals the clock value).
func gkaRReadyAssessment(req app.NodeAssessmentRequest, in fleet.Intent, idx int) (app.DeviceAssessment, error) {
	now := req.Now
	bdf := fmt.Sprintf("0000:%02x:00.0", 0x65+idx)
	tid, err := model.NewTypedID(string(model.NamespacePCIBDF), bdf)
	if err != nil {
		return app.DeviceAssessment{}, err
	}
	fn, err := model.NewAssetRef(model.KindPCIeFunction, tid)
	if err != nil {
		return app.DeviceAssessment{}, err
	}
	binding := fleet.ObservedBinding{
		Node: req.Node, BootID: "boot-a", BDF: bdf, Function: fn, Claim: in.Claim,
		Source:     model.SourceRef{Type: string(model.SourceTypeAgent), Name: "path-agent/nvidia-smi"},
		EvidenceID: "binding-ev", BundleRevision: gkaRGraphRev, CollectorProfileID: "profile-1",
		ObservedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	var coverage []fleet.CoverageAssessment
	var cursors []fleet.CoverageCursor
	for _, c := range req.Policy.RequiredCoverage {
		coverage = append(coverage, fleet.CoverageAssessment{
			Name: c.Name, PathKind: c.PathKind, State: fleet.CoverageNormal, Reason: "Normal",
			EvidenceIDs: []string{"path-ev"}, ObservedAt: now, LatestObservedAt: now, ExpiresAt: now.Add(time.Minute), AssessmentSequence: 4,
		})
		cursors = append(cursors, fleet.CoverageCursor{Name: c.Name, ObservedAt: now, AssessmentSequence: 4, EvidenceDigest: gkaRHex64('c')})
	}
	dec := fleet.DeviceDecision{
		Desired: in.Desired, Phase: fleet.PhaseReady, Qualification: fleet.QualificationQualified,
		BindingState: fleet.BindingBound, Binding: binding, Allocation: fleet.AllocationUnknown,
		AcceptedNormalPoint: true, GraphRevision: gkaRGraphRev, Reason: "Ready",
		Coverage: coverage, EvidenceIDs: []string{"binding-ev", "path-ev"}, EvaluatedAt: now, ValidUntil: now.Add(time.Minute),
		PolicyRevision: req.Policy.Revision, RequestID: in.RequestID, DeviceUID: in.Device.UID, NodeUID: req.Node.UID, BootID: "boot-a",
		BindingKey:     in.Claim.Vendor + "\x00" + in.Claim.UUID + "\x00" + req.Node.UID + "\x00boot-a\x00" + bdf,
		TopologyDigest: gkaRHex64('a'), BaselineDigest: gkaRHex64('b'), MetadataGeneration: in.MetadataGeneration, Session: 2,
		IntentObservedAt: in.ObservedAt, ReadyWindowStartedAt: in.ObservedAt, LastCompositeMin: now, LastCompositeMax: now,
		CoverageCursors: cursors,
	}
	ev := []app.EvidenceRecord{
		{ID: "binding-ev", Summary: "binding observed", ObservedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "path-ev", Summary: "path observed", ObservedAt: now, ExpiresAt: now.Add(time.Minute)},
	}
	return app.DeviceAssessment{Decision: dec, Evidence: ev}, nil
}

// gkaRReadyScript answers every request with Ready decisions for all intents and
// the node aggregate computed by fleet.AggregateNode.
func gkaRReadyScript(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	var devs []app.DeviceAssessment
	var aggs []fleet.DeviceAggregate
	for i, in := range req.Intents {
		da, err := gkaRReadyAssessment(req, in, i)
		if err != nil {
			return app.NodeAssessment{}, err
		}
		devs = append(devs, da)
		aggs = append(aggs, fleet.DeviceAggregate{
			DeviceUID: in.Device.UID, NodeUID: req.Node.UID, Desired: in.Desired,
			MetadataGeneration: in.MetadataGeneration, Decision: da.Decision,
		})
	}
	nd, err := fleet.AggregateNode(fleet.AggregateNodeInput{Node: req.Node, FleetUID: req.FleetUID, Selection: req.Selection, Devices: aggs}, nil, req.Now)
	if err != nil {
		return app.NodeAssessment{}, err
	}
	return app.NodeAssessment{
		Devices: devs, Node: &nd,
		Observation: &app.NodeObservation{GraphRevision: gkaRGraphRev, Completeness: fleet.CompletenessComplete},
	}, nil
}

// gkaRFailNode wraps a script: requests for nodeUID fail with err.
func gkaRFailNode(nodeUID string, err error, next func(app.NodeAssessmentRequest) (app.NodeAssessment, error)) func(app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	return func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		if req.Node.UID == nodeUID {
			return app.NodeAssessment{}, err
		}
		if next == nil {
			return app.NodeAssessment{}, nil
		}
		return next(req)
	}
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

// gkaRAssessTimes returns the AssessNode call times for one node UID.
func gkaRAssessTimes(f *gkaFakeAssessor, nodeUID string) []time.Time {
	var out []time.Time
	for _, c := range f.Log() {
		if c.Kind == "assess" && c.NodeUID == nodeUID {
			out = append(out, c.At)
		}
	}
	return out
}

// gkaRPathFor returns the substring identifying one object's URL path.
func gkaRPathFor(resource, name string) string { return "/" + resource + "/" + name }

// gkaRStatusPUTs returns the recorded PUTs to <resource>/<name>/status.
func gkaRStatusPUTs(rec *gkaRecorder, resource, name string) []gkaRecEntry {
	suffix := gkaRPathFor(resource, name) + "/status"
	var out []gkaRecEntry
	for _, e := range rec.Entries() {
		if e.Method == http.MethodPut && strings.HasSuffix(e.Path, suffix) {
			out = append(out, e)
		}
	}
	return out
}

// gkaRSingleGETs returns recorded non-watch GETs of exactly <resource>/<name>.
func gkaRSingleGETs(rec *gkaRecorder, resource, name string) []gkaRecEntry {
	suffix := gkaRPathFor(resource, name)
	var out []gkaRecEntry
	for _, e := range rec.Entries() {
		if e.Method == http.MethodGet && !e.Watch && strings.HasSuffix(e.Path, suffix) {
			out = append(out, e)
		}
	}
	return out
}

// gkaRJSONMap decodes a JSON object body.
func gkaRJSONMap(body []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	return m
}

// ---------------------------------------------------------------------------
// rig: recording config + fake assessor + fake clock + running controller
// ---------------------------------------------------------------------------

// gkaRRig is a running controller together with its observation points.
type gkaRRig struct {
	E   *gkaEnvT
	Cfg *rest.Config
	Rec *gkaRecorder
	A   *gkaFakeAssessor
	Clk *gkaFakeClock
	Ctl *gkaCtl
}

// gkaRNewRig prepares the observation points without starting anything, so a
// test can install injectors before the controller's first request.
func gkaRNewRig(e *gkaEnvT) *gkaRRig {
	cfg, rec := gkaRecordingConfig(e.Config)
	return &gkaRRig{E: e, Cfg: cfg, Rec: rec, A: newGkaFakeAssessor(), Clk: newGkaFakeClock(gkaRT0)}
}

// Start runs the controller with the assessor script (nil = no observation).
func (r *gkaRRig) Start(t *testing.T, script func(app.NodeAssessmentRequest) (app.NodeAssessment, error), mutate func(*controller.Options)) *gkaRRig {
	t.Helper()
	r.A.SetScript(script)
	r.Ctl = gkaStartController(t, r.Cfg, r.A, r.Clk, mutate)
	return r
}

// gkaRStart starts a controller with a recording config, a fake assessor (script
// may be nil = no observation) and a fake clock frozen at gkaRT0.
func gkaRStart(t *testing.T, e *gkaEnvT, script func(app.NodeAssessmentRequest) (app.NodeAssessment, error), mutate func(*controller.Options)) *gkaRRig {
	t.Helper()
	return gkaRNewRig(e).Start(t, script, mutate)
}

// gkaRWaitDone waits for Run to return and returns its result.
func gkaRWaitDone(t *testing.T, c *gkaCtl, limit time.Duration) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	select {
	case err := <-c.Done:
		return time.Since(start), err
	case <-time.After(limit):
		t.Fatalf("Run did not return within %s", limit)
		return 0, nil
	}
}

// gkaRNoRequestsAfter asserts that no request (reads included) entered the
// transport more than 100 ms after at, during a 1.5 s observation (GKA-175).
func gkaRNoRequestsAfter(t *testing.T, rec *gkaRecorder, at time.Time, what string) {
	t.Helper()
	if n := rec.InFlight(); n != 0 {
		t.Errorf("GKA-022(c): %d request(s) still in flight when the check started after %s", n, what)
	}
	time.Sleep(1500 * time.Millisecond)
	cut := at.Add(100 * time.Millisecond)
	var late []string
	for _, e := range rec.Entries() {
		if e.At.After(cut) {
			late = append(late, e.String())
		}
	}
	if len(late) > 0 {
		t.Errorf("GKA-022(c)/144: %d request(s) entered the transport after %s: %v", len(late), what, late)
	}
}

// gkaRClosedPortConfig is a REST config whose port nothing listens on.
func gkaRClosedPortConfig(t *testing.T) *rest.Config {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return &rest.Config{Host: "http://" + addr}
}

// gkaRTickClock returns base + n*step on the n-th Now() call.
type gkaRTickClock struct {
	mu   sync.Mutex
	n    int64
	base time.Time
	step time.Duration
}

func (c *gkaRTickClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.base.Add(time.Duration(c.n) * c.step)
	c.n++
	return v
}

// Calls is the number of Now() reads so far.
func (c *gkaRTickClock) Calls() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

var _ controller.Clock = (*gkaRTickClock)(nil)

// gkaRHoldList blocks the initial synchronisation requests of one resource until
// release is called (or the request context ends): the collection LIST and the
// initial WATCH (client-go 0.35 informers may receive the initial list through a
// WatchList watch=true&sendInitialEvents=true instead of a LIST, GKA-175 verb
// mapping). Later ordinary watches pass. entered reports whether a request was held.
func gkaRHoldList(rec *gkaRecorder, resource string) (release func(), entered *atomic.Bool) {
	ch := make(chan struct{})
	var once sync.Once
	entered = &atomic.Bool{}
	rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.URL.Path, "/"+resource) {
			return nil
		}
		q := req.URL.Query()
		isList := q.Get("watch") == ""
		isInitialWatch := (q.Get("watch") == "true" || q.Get("watch") == "1") && q.Get("sendInitialEvents") == "true"
		if isList || isInitialWatch {
			entered.Store(true)
			select {
			case <-ch:
			case <-req.Context().Done():
			}
		}
		return nil
	})
	return func() { once.Do(func() { close(ch) }) }, entered
}

func gkaRp(s string) *string { return &s }

func gkaRTime(s string) metav1.Time {
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return metav1.NewTime(tm)
}

// gkaRColdDeviceStatus is a valid observation-less device status (spec section
// 12 cold start example) for the device at generation gen.
func gkaRColdDeviceStatus(d *v1alpha1.GPUDevice, at string, conds []metav1.Condition) v1alpha1.GPUDeviceStatus {
	return v1alpha1.GPUDeviceStatus{
		ObservedGeneration: d.Generation,
		ObservedRequestID:  d.Spec.Request.ID,
		IntentObservedAt:   gkaRTime(at),
		ActualBinding:      v1alpha1.ActualBinding{State: v1alpha1.BindingStateUnknown, Reason: "Validating"},
		Qualification:      v1alpha1.QualificationUnknown,
		LifecyclePhase:     v1alpha1.LifecyclePhasePending,
		Allocation: v1alpha1.AllocationSummary{
			State: v1alpha1.AllocationStateUnknown, Reason: "AllocationUnknown",
			EvidenceRefs: []string{}, AffectedWorkloads: []v1alpha1.AffectedWorkload{},
		},
		Coverage: []v1alpha1.CoverageSummary{}, FindingRefs: []v1alpha1.FindingSummary{}, EvidenceRefs: []v1alpha1.EvidenceSummary{},
		Conditions: conds,
	}
}

// gkaRRawKeys returns the sorted key set of a decoded JSON object.
func gkaRRawKeys(m map[string]any) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// gkaRRetry retries fn (optimistic-concurrency writes made by the test itself).
func gkaRRetry(t *testing.T, what string, fn func() error) {
	t.Helper()
	gkaEventually(t, 10*time.Second, func() (bool, string) {
		err := fn()
		return err == nil, fmt.Sprintf("%s: %v", what, err)
	})
}

// gkaRWaitQuiet waits until the newest matching write is at least 2 x resync old.
func gkaRWaitQuiet(t *testing.T, rec *gkaRecorder, prefix string, resync time.Duration) {
	t.Helper()
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		last := gkaRLastWrite(rec, prefix)
		return last.IsZero() || time.Since(last) >= 2*resync, "last matching write at " + last.String()
	})
}

// gkaRInjectStatusPUT installs an injector for PUTs to <resource>/<name>/status.
// mk receives the 1-based attempt number and returns a response or nil (pass).
func gkaRInjectStatusPUT(rec *gkaRecorder, resource, name string, mk func(req *http.Request, attempt int64) *http.Response) *atomic.Int64 {
	var attempts atomic.Int64
	suffix := gkaRPathFor(resource, name) + "/status"
	rec.Inject(func(req *http.Request) *http.Response {
		if req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, suffix) {
			return mk(req, attempts.Add(1))
		}
		return nil
	})
	return &attempts
}

// gkaRDecodeDevice decodes a recorded PUT body into a GPUDevice (safe to call
// from injector goroutines: it never touches *testing.T).
func gkaRDecodeDevice(body []byte) (*v1alpha1.GPUDevice, error) {
	d := &v1alpha1.GPUDevice{}
	if err := json.Unmarshal(body, d); err != nil {
		return nil, fmt.Errorf("decode GPUDevice body: %w", err)
	}
	return d, nil
}

// gkaRBodyResourceVersion returns metadata.resourceVersion of a JSON body.
func gkaRBodyResourceVersion(body []byte) string {
	md, _ := gkaRJSONMap(body)["metadata"].(map[string]any)
	rv, _ := md["resourceVersion"].(string)
	return rv
}

// gkaRGaps returns the time gaps between consecutive entries.
func gkaRGaps(es []gkaRecEntry) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(es); i++ {
		out = append(out, es[i].At.Sub(es[i-1].At))
	}
	return out
}

// gkaRPassBetween reports whether the fake assessor was called for the node
// strictly between a and b: a new pass began (in-pass retries never call it).
func gkaRPassBetween(f *gkaFakeAssessor, nodeUID string, a, b time.Time) bool {
	for _, at := range gkaRAssessTimes(f, nodeUID) {
		if at.After(a) && at.Before(b) {
			return true
		}
	}
	return false
}

// gkaRCondEqual compares two conditions field by field; the time is compared with
// Equal (a parsed UTC time and a deserialised one differ in Location).
func gkaRCondEqual(a, b metav1.Condition) bool {
	return a.Type == b.Type && a.Status == b.Status && a.ObservedGeneration == b.ObservedGeneration &&
		a.Reason == b.Reason && a.Message == b.Message && a.LastTransitionTime.Equal(&b.LastTransitionTime)
}
