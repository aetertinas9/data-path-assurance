package kubeapi_test

// A minimal "world" (one Node, one GPUFleet selecting it, one GPUDevice on it)
// for the tests that need a controller or a real binary to have
// something to project. Object names all come from gkaName so the harness
// cleanup removes them.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type gkaCWorld struct {
	FleetName, NodeName, DeviceName string
	FleetUID, NodeUID, DeviceUID    string
}

// gkaCMakeWorld creates a Node labelled for a new Audit GPUFleet and one
// InService GPUDevice that references both by name and UID.
func gkaCMakeWorld(t *testing.T, e *gkaEnvT) *gkaCWorld {
	t.Helper()
	w := &gkaCWorld{FleetName: gkaName(t, "fleet"), NodeName: gkaName(t, "node"), DeviceName: gkaName(t, "device")}
	ctx, cancel := gkaCCtx()
	defer cancel()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: w.NodeName, Labels: map[string]string{"gka.test/fleet": w.FleetName}}}
	if err := e.Client.Create(ctx, node); err != nil {
		t.Fatalf("fixture: create Node: %v", err)
	}
	w.NodeUID = string(node.UID)

	fspec := gkaCFleetSpec()
	fspec["nodeSelector"] = map[string]any{"matchLabels": map[string]any{"gka.test/fleet": w.FleetName}}
	fleet := gkaCCreate(t, e, gkaCFleetRes, w.FleetName, fspec)
	w.FleetUID = string(fleet.GetUID())

	dspec := gkaCDeviceSpec()
	dspec["nodeRef"] = gkaCRef(w.NodeName, w.NodeUID)
	dspec["fleetRef"] = gkaCRef(w.FleetName, w.FleetUID)
	gkaCSet(dspec, "inventoryClaim.uuid", "GPU-"+w.DeviceName)
	dev := gkaCCreate(t, e, gkaCDevRes, w.DeviceName, dspec)
	w.DeviceUID = string(dev.GetUID())
	return w
}

// gkaCDeviceStatusMessage returns the message of the device's DeviceQualified
// condition and whether the controller has written a status at all.
func gkaCDeviceStatusMessage(t *testing.T, e *gkaEnvT, name string) (string, bool) {
	t.Helper()
	u := gkaCGetObj(t, e, gkaCDevRes, name)
	if _, has, _ := unstructured.NestedFieldNoCopy(u.Object, "status", "observedGeneration"); !has {
		return "", false
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "DeviceQualified" {
			msg, _ := m["message"].(string)
			return msg, true
		}
	}
	return "", true
}

// gkaCBuf is a goroutine-safe byte buffer for captured process output and logs.
type gkaCBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *gkaCBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *gkaCBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (b *gkaCBuf) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// gkaCLines returns the non-empty lines of s.
func gkaCLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// gkaCLeaseHolder reads the holderIdentity of a Lease ("" when absent).
func gkaCLeaseHolder(ctx context.Context, e *gkaEnvT, ns, name string) (holder string, durationSeconds int64, found bool) {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("coordination.k8s.io/v1")
	u.SetKind("Lease")
	if err := e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		return "", 0, false
	}
	holder, _, _ = unstructured.NestedString(u.Object, "spec", "holderIdentity")
	durationSeconds, _, _ = unstructured.NestedInt64(u.Object, "spec", "leaseDurationSeconds")
	return holder, durationSeconds, true
}

// gkaCMustJSON marshals v or fails the test.
func gkaCMustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
