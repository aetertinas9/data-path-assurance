package kubeapi_test

// Lane T-2 object/world helpers (prefix gkaS). They create objects through the
// harness client and poll them through the harness gkaEventually.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

const (
	gkaSSelKey         = "gka-sem/sel"
	gkaSFinalizerHold  = "gka-sem/hold"
	gkaSForeignManager = "gka-sem-foreign"
)

func gkaSTO() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// gkaSWorld owns the objects of one test. All object names come from gkaName of
// the root test, so the harness cleanup of the root test finds them. Subtests
// use w.with(t) to report failures on their own *testing.T while sharing the
// root's names and bookkeeping.
type gkaSWorld struct {
	root *testing.T // naming and cleanup owner
	t    *testing.T // reporting
	e    *gkaEnvT
	sel  string // per-test label value that selects "this test's" nodes
	sh   *gkaSShared
}

type gkaSShared struct {
	mu        sync.Mutex
	finalized []string // GPUDevice names that carry the hold finalizer
}

func newGkaSWorld(t *testing.T) *gkaSWorld {
	t.Helper()
	e := gkaEnv(t)
	w := &gkaSWorld{root: t, t: t, e: e, sel: gkaName(t, "sel"), sh: &gkaSShared{}}
	// LIFO: finalizers are released first, then the harness deletes everything.
	t.Cleanup(func() { gkaCleanupAll(t, e) })
	t.Cleanup(w.releaseFinalizers)
	return w
}

// with returns the same world reporting on subtest t.
func (w *gkaSWorld) with(t *testing.T) *gkaSWorld {
	c := *w
	c.t = t
	return &c
}

func (w *gkaSWorld) name(suffix string) string { return gkaName(w.root, suffix) }

func (w *gkaSWorld) releaseFinalizers() {
	w.sh.mu.Lock()
	names := append([]string(nil), w.sh.finalized...)
	w.sh.mu.Unlock()
	for _, n := range names {
		_ = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			ctx, cancel := gkaSTO()
			defer cancel()
			d := &v1alpha1.GPUDevice{}
			if err := w.e.Client.Get(ctx, client.ObjectKey{Name: n}, d); err != nil {
				return client.IgnoreNotFound(err)
			}
			d.Finalizers = nil
			return client.IgnoreNotFound(w.e.Client.Update(ctx, d))
		})
	}
}

func (w *gkaSWorld) must(err error, what string) {
	w.t.Helper()
	if err != nil {
		w.t.Fatalf("%s: %v", what, err)
	}
}

// ---- object factories ------------------------------------------------------

func (w *gkaSWorld) node(suffix string, labels map[string]string) *corev1.Node {
	w.t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: w.name(suffix), Labels: labels}}
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(w.e.Client.Create(ctx, n), "create node "+n.Name)
	return n
}

// selNode is a Node carrying this test's selection label.
func (w *gkaSWorld) selNode(suffix string) *corev1.Node {
	w.t.Helper()
	return w.node(suffix, map[string]string{gkaSSelKey: w.sel})
}

func gkaSRequiredCoverage() []v1alpha1.FleetCoverageRequirement {
	return []v1alpha1.FleetCoverageRequirement{
		{Name: "pcie-parent", PathKind: "gpu-pcie-parent", Required: true},
		{Name: "pcie-root", PathKind: "gpu-pcie-root", Required: true},
		{Name: "pcie-width", PathKind: "gpu-pcie-link-width-normal", Required: true},
	}
}

func (w *gkaSWorld) fleet(suffix string, sel metav1.LabelSelector, mut ...func(*v1alpha1.GPUFleet)) *v1alpha1.GPUFleet {
	w.t.Helper()
	f := &v1alpha1.GPUFleet{
		ObjectMeta: metav1.ObjectMeta{Name: w.name(suffix)},
		Spec: v1alpha1.GPUFleetSpec{
			NodeSelector: sel, RequiredCoverage: gkaSRequiredCoverage(), FreshnessSeconds: 60, ReadyForSeconds: 30,
			Mode: v1alpha1.FleetMode("Audit"),
		},
	}
	for _, m := range mut {
		m(f)
	}
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(w.e.Client.Create(ctx, f), "create fleet "+f.Name)
	return f
}

// selFleet selects this test's label.
func (w *gkaSWorld) selFleet(suffix string, mut ...func(*v1alpha1.GPUFleet)) *v1alpha1.GPUFleet {
	w.t.Helper()
	return w.fleet(suffix, metav1.LabelSelector{MatchLabels: map[string]string{gkaSSelKey: w.sel}}, mut...)
}

// gkaSEnforce turns a fleet Enforce (CEL F2 requires a non-empty canarySelector).
func gkaSEnforce(f *v1alpha1.GPUFleet) {
	f.Spec.Mode = v1alpha1.FleetMode("Enforce")
	f.Spec.CanarySelector = &metav1.LabelSelector{MatchLabels: map[string]string{"gka-sem/canary": "x"}}
}

func gkaSNodeRef(n *corev1.Node) v1alpha1.ObjectRef {
	return v1alpha1.ObjectRef{Name: n.Name, UID: string(n.UID)}
}

func gkaSFleetRef(f *v1alpha1.GPUFleet) v1alpha1.ObjectRef {
	return v1alpha1.ObjectRef{Name: f.Name, UID: string(f.UID)}
}

func gkaSCap(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// gkaSDeviceObject builds (without creating) a GPUDevice with the section 12
// shape. n and f may be nil when the mutator sets the references.
func (w *gkaSWorld) deviceObject(suffix string, n *corev1.Node, f *v1alpha1.GPUFleet, mut ...func(*v1alpha1.GPUDevice)) *v1alpha1.GPUDevice {
	name := w.name(suffix)
	d := &v1alpha1.GPUDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUDeviceSpec{
			InventoryClaim: v1alpha1.InventoryClaim{
				Vendor: "NVIDIA", UUID: gkaSPtr(gkaSCap("GPU-"+name, 128)), Source: "operator/asset-db", EvidenceID: gkaSCap("asset-db:"+name, 128),
			},
			DesiredState: v1alpha1.DesiredState("InService"),
			Request:      v1alpha1.LifecycleRequest{ID: "enroll-1", Reason: "initial enrollment"},
		},
	}
	if n != nil {
		d.Spec.NodeRef = gkaSNodeRef(n)
	}
	if f != nil {
		d.Spec.FleetRef = gkaSFleetRef(f)
	}
	for _, m := range mut {
		m(d)
	}
	return d
}

func (w *gkaSWorld) device(suffix string, n *corev1.Node, f *v1alpha1.GPUFleet, mut ...func(*v1alpha1.GPUDevice)) *v1alpha1.GPUDevice {
	w.t.Helper()
	d := w.deviceObject(suffix, n, f, mut...)
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(w.e.Client.Create(ctx, d), "create device "+d.Name)
	if len(d.Finalizers) > 0 {
		w.sh.mu.Lock()
		w.sh.finalized = append(w.sh.finalized, d.Name)
		w.sh.mu.Unlock()
	}
	return d
}

// gkaSWithClaim overrides the claim of a device.
func gkaSWithClaim(vendor string, uuid, serial *string) func(*v1alpha1.GPUDevice) {
	return func(d *v1alpha1.GPUDevice) {
		d.Spec.InventoryClaim.Vendor = vendor
		d.Spec.InventoryClaim.UUID = uuid
		d.Spec.InventoryClaim.Serial = serial
	}
}

func gkaSWithDesired(state string) func(*v1alpha1.GPUDevice) {
	return func(d *v1alpha1.GPUDevice) { d.Spec.DesiredState = v1alpha1.DesiredState(state) }
}

// ---- controller ------------------------------------------------------------

func gkaSStart(t *testing.T, cfg *rest.Config, a app.NodeAssessor, clk controller.Clock, mut ...func(*controller.Options)) *gkaCtl {
	t.Helper()
	return gkaStartController(t, cfg, a, clk, func(o *controller.Options) {
		o.ClusterID = gkaSClusterID
		o.ResyncInterval = gkaSResync
		for _, m := range mut {
			m(o)
		}
	})
}

func (w *gkaSWorld) start(a app.NodeAssessor, clk controller.Clock, mut ...func(*controller.Options)) *gkaCtl {
	w.t.Helper()
	return gkaSStart(w.t, w.e.Config, a, clk, mut...)
}

// ---- reads -------------------------------------------------------------------

func gkaSGetFleet(e *gkaEnvT, name string) (*v1alpha1.GPUFleet, error) {
	ctx, cancel := gkaSTO()
	defer cancel()
	o := &v1alpha1.GPUFleet{}
	return o, e.Client.Get(ctx, client.ObjectKey{Name: name}, o)
}

func gkaSGetDevice(e *gkaEnvT, name string) (*v1alpha1.GPUDevice, error) {
	ctx, cancel := gkaSTO()
	defer cancel()
	o := &v1alpha1.GPUDevice{}
	return o, e.Client.Get(ctx, client.ObjectKey{Name: name}, o)
}

func gkaSGetNPS(e *gkaEnvT, name string) (*v1alpha1.NodePathState, error) {
	ctx, cancel := gkaSTO()
	defer cancel()
	o := &v1alpha1.NodePathState{}
	return o, e.Client.Get(ctx, client.ObjectKey{Name: name}, o)
}

func gkaSGetNode(e *gkaEnvT, name string) (*corev1.Node, error) {
	ctx, cancel := gkaSTO()
	defer cancel()
	o := &corev1.Node{}
	return o, e.Client.Get(ctx, client.ObjectKey{Name: name}, o)
}

func (w *gkaSWorld) fleetNow(name string) *v1alpha1.GPUFleet {
	w.t.Helper()
	o, err := gkaSGetFleet(w.e, name)
	w.must(err, "get fleet "+name)
	return o
}

func (w *gkaSWorld) deviceNow(name string) *v1alpha1.GPUDevice {
	w.t.Helper()
	o, err := gkaSGetDevice(w.e, name)
	w.must(err, "get device "+name)
	return o
}

func (w *gkaSWorld) npsNow(name string) *v1alpha1.NodePathState {
	w.t.Helper()
	o, err := gkaSGetNPS(w.e, name)
	w.must(err, "get nodepathstate "+name)
	return o
}

func (w *gkaSWorld) nodeNow(name string) *corev1.Node {
	w.t.Helper()
	o, err := gkaSGetNode(w.e, name)
	w.must(err, "get node "+name)
	return o
}

// listNPS returns the NodePathStates whose name starts with prefix.
func (w *gkaSWorld) listNPS(prefix string) []v1alpha1.NodePathState {
	w.t.Helper()
	ctx, cancel := gkaSTO()
	defer cancel()
	list := &v1alpha1.NodePathStateList{}
	w.must(w.e.Client.List(ctx, list), "list nodepathstates")
	var out []v1alpha1.NodePathState
	for _, p := range list.Items {
		if strings.HasPrefix(p.Name, prefix) {
			out = append(out, p)
		}
	}
	return out
}

func (w *gkaSWorld) hasNPS(name string) bool {
	w.t.Helper()
	_, err := gkaSGetNPS(w.e, name)
	if err == nil {
		return true
	}
	if client.IgnoreNotFound(err) != nil {
		w.t.Fatalf("get nodepathstate %s: %v", name, err)
	}
	return false
}

// ---- waits (check returns "" once satisfied) --------------------------------

func gkaSWaitFleetFor(t *testing.T, e *gkaEnvT, d time.Duration, name string, check func(*v1alpha1.GPUFleet) string) *v1alpha1.GPUFleet {
	t.Helper()
	var last *v1alpha1.GPUFleet
	gkaEventually(t, d, func() (bool, string) {
		o, err := gkaSGetFleet(e, name)
		if err != nil {
			return false, "get fleet " + name + ": " + err.Error()
		}
		if why := check(o); why != "" {
			return false, "fleet " + name + ": " + why
		}
		last = o
		return true, ""
	})
	return last
}

func (w *gkaSWorld) waitFleet(name string, check func(*v1alpha1.GPUFleet) string) *v1alpha1.GPUFleet {
	w.t.Helper()
	return gkaSWaitFleetFor(w.t, w.e, gkaSWaitMax, name, check)
}

func (w *gkaSWorld) waitDevice(name string, check func(*v1alpha1.GPUDevice) string) *v1alpha1.GPUDevice {
	w.t.Helper()
	var last *v1alpha1.GPUDevice
	gkaEventually(w.t, gkaSWaitMax, func() (bool, string) {
		o, err := gkaSGetDevice(w.e, name)
		if err != nil {
			return false, "get device " + name + ": " + err.Error()
		}
		if why := check(o); why != "" {
			return false, "device " + name + ": " + why
		}
		last = o
		return true, ""
	})
	return last
}

func (w *gkaSWorld) waitNPS(name string, check func(*v1alpha1.NodePathState) string) *v1alpha1.NodePathState {
	w.t.Helper()
	var last *v1alpha1.NodePathState
	gkaEventually(w.t, gkaSWaitMax, func() (bool, string) {
		o, err := gkaSGetNPS(w.e, name)
		if err != nil {
			return false, "get nodepathstate " + name + ": " + err.Error()
		}
		if why := check(o); why != "" {
			return false, "nodepathstate " + name + ": " + why
		}
		last = o
		return true, ""
	})
	return last
}

func (w *gkaSWorld) waitNode(name string, check func(*corev1.Node) string) *corev1.Node {
	w.t.Helper()
	var last *corev1.Node
	gkaEventually(w.t, gkaSWaitMax, func() (bool, string) {
		o, err := gkaSGetNode(w.e, name)
		if err != nil {
			return false, "get node " + name + ": " + err.Error()
		}
		if why := check(o); why != "" {
			return false, "node " + name + ": " + why
		}
		last = o
		return true, ""
	})
	return last
}

// waitNPSGone waits until the NodePathState no longer exists.
func (w *gkaSWorld) waitNPSGone(name string) {
	w.t.Helper()
	gkaEventually(w.t, gkaSWaitMax, func() (bool, string) {
		_, err := gkaSGetNPS(w.e, name)
		if err == nil {
			return false, "nodepathstate " + name + " still exists"
		}
		if client.IgnoreNotFound(err) != nil {
			return false, "get nodepathstate " + name + ": " + err.Error()
		}
		return true, ""
	})
}

// settle sleeps for n resync intervals: enough passes for a negative assertion
// ("never called", "not created") that has already synchronized on a positive
// event.
func gkaSSettle(n int) { time.Sleep(time.Duration(n) * gkaSResync) }

// ---- mutations (retry on conflict; the controller writes statuses concurrently)

func (w *gkaSWorld) updateFleet(name string, mut func(*v1alpha1.GPUFleet)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &v1alpha1.GPUFleet{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Update(ctx, o)
	}), "update fleet "+name)
}

func (w *gkaSWorld) updateDevice(name string, mut func(*v1alpha1.GPUDevice)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &v1alpha1.GPUDevice{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Update(ctx, o)
	}), "update device "+name)
}

func (w *gkaSWorld) updateNode(name string, mut func(*corev1.Node)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &corev1.Node{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Update(ctx, o)
	}), "update node "+name)
}

func (w *gkaSWorld) updateNPS(name string, mut func(*v1alpha1.NodePathState)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &v1alpha1.NodePathState{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Update(ctx, o)
	}), "update nodepathstate "+name)
}

// statusAs writes a status through a manager other than the controller's.
func (w *gkaSWorld) fleetStatusAsForeign(name string, mut func(*v1alpha1.GPUFleet)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &v1alpha1.GPUFleet{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Status().Update(ctx, o, client.FieldOwner(gkaSForeignManager))
	}), "foreign status update fleet "+name)
}

func (w *gkaSWorld) deviceStatusAsForeign(name string, mut func(*v1alpha1.GPUDevice)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &v1alpha1.GPUDevice{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Status().Update(ctx, o, client.FieldOwner(gkaSForeignManager))
	}), "foreign status update device "+name)
}

func (w *gkaSWorld) npsStatusAsForeign(name string, mut func(*v1alpha1.NodePathState)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &v1alpha1.NodePathState{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Status().Update(ctx, o, client.FieldOwner(gkaSForeignManager))
	}), "foreign status update nodepathstate "+name)
}

func (w *gkaSWorld) nodeStatusAsForeign(name string, mut func(*corev1.Node)) {
	w.t.Helper()
	w.must(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ctx, cancel := gkaSTO()
		defer cancel()
		o := &corev1.Node{}
		if err := w.e.Client.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
			return err
		}
		mut(o)
		return w.e.Client.Status().Update(ctx, o, client.FieldOwner(gkaSForeignManager))
	}), "foreign status update node "+name)
}

// deleteNode deletes a Node (no finalizers, so it disappears immediately).
func (w *gkaSWorld) deleteNode(name string) {
	w.t.Helper()
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(client.IgnoreNotFound(w.e.Client.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})), "delete node "+name)
}

func (w *gkaSWorld) deleteFleet(name string) {
	w.t.Helper()
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(client.IgnoreNotFound(w.e.Client.Delete(ctx, &v1alpha1.GPUFleet{ObjectMeta: metav1.ObjectMeta{Name: name}})), "delete fleet "+name)
}

func (w *gkaSWorld) deleteDevice(name string) {
	w.t.Helper()
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(client.IgnoreNotFound(w.e.Client.Delete(ctx, &v1alpha1.GPUDevice{ObjectMeta: metav1.ObjectMeta{Name: name}})), "delete device "+name)
}

// ---- raw JSON, managedFields ------------------------------------------------

var gkaSGroup = "infrastructure.data-path-assurance.io"

func (w *gkaSWorld) raw(kind, name string) map[string]any {
	w.t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: gkaSGroup, Version: "v1alpha1", Kind: kind})
	ctx, cancel := gkaSTO()
	defer cancel()
	w.must(w.e.Client.Get(ctx, client.ObjectKey{Name: name}, u), "get raw "+kind+"/"+name)
	return u.Object
}

// gkaSRawList returns status.<field> of a raw object and whether it was present
// as a JSON array (present-and-empty is the "[]" the spec requires, GKA-013).
func gkaSRawArray(obj map[string]any, path ...string) ([]any, bool) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	arr, ok := cur.([]any)
	return arr, ok
}

type gkaSManaged struct {
	Manager, Operation, Subresource string
}

func gkaSManagedFields(o metav1.Object) []gkaSManaged {
	var out []gkaSManaged
	for _, m := range o.GetManagedFields() {
		out = append(out, gkaSManaged{Manager: m.Manager, Operation: string(m.Operation), Subresource: m.Subresource})
	}
	return out
}

func gkaSHasManaged(o metav1.Object, manager, operation, subresource string) bool {
	for _, m := range gkaSManagedFields(o) {
		if m.Manager == manager && m.Operation == operation && m.Subresource == subresource {
			return true
		}
	}
	return false
}

// ---- logging capture -------------------------------------------------------

// gkaSLog is a slog.Handler that keeps every record as one formatted line.
type gkaSLog struct {
	mu    *sync.Mutex
	lines *[]string
	attrs string
}

func newGkaSLog() *gkaSLog {
	return &gkaSLog{mu: &sync.Mutex{}, lines: &[]string{}}
}

func (h *gkaSLog) Enabled(context.Context, slog.Level) bool { return true }

func (h *gkaSLog) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Level.String())
	b.WriteString(" ")
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	h.mu.Lock()
	*h.lines = append(*h.lines, b.String())
	h.mu.Unlock()
	return nil
}

func (h *gkaSLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	for _, a := range attrs {
		c.attrs += " " + a.Key + "=" + a.Value.String()
	}
	return &c
}

func (h *gkaSLog) WithGroup(string) slog.Handler { return h }

func (h *gkaSLog) Lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), *h.lines...)
}

func (h *gkaSLog) Logger() *slog.Logger { return slog.New(h) }

// ---- injected HTTP responses (no Retry-After, GKA-175) ----------------------

func gkaSStatusResponse(req *http.Request, code int, reason metav1.StatusReason, message string) *http.Response {
	st := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Code: int32(code), Reason: reason, Message: message,
	}
	body, _ := json.Marshal(st)
	return &http.Response{
		StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}
}

// gkaSBody returns the request body and restores it for the real transport.
func gkaSBody(req *http.Request) string {
	if req.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(b))
	return string(b)
}

// ---- bulk helpers for the capacity test --------------------------------------

func gkaSFastClient(t *testing.T, e *gkaEnvT) client.Client {
	t.Helper()
	cfg := rest.CopyConfig(e.Config)
	cfg.QPS = 2000
	cfg.Burst = 4000
	c, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	if err != nil {
		t.Fatalf("fast client: %v", err)
	}
	return c
}

// gkaSParallel runs fn(0..n-1) on `workers` goroutines and returns the first error.
func gkaSParallel(n, workers int, fn func(i int) error) error {
	var next atomic.Int64
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				mu.Lock()
				failed := first != nil
				mu.Unlock()
				if i >= n || failed {
					return
				}
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}
