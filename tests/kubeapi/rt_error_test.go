package kubeapi_test

// GKA-125 (error table), GKA-126 (independence), GKA-127 (restart distrust),
// GKA-128 (verb set), GKA-027 (transport seam, no listener), GKA-034 (request
// validity and independence from assessor mutation).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// ---------------------------------------------------------------------------
// GKA-125
// ---------------------------------------------------------------------------

func TestGKA125_NotFoundIsSkippedWithoutFallbackAndRetriedOnLaterPasses(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	dev := sc.Devices[0]
	rig := gkaRNewRig(e)
	attempts := gkaRInjectStatusPUT(rig.Rec, "gpudevices", dev.Name, func(req *http.Request, n int64) *http.Response {
		if n <= 3 {
			return gkaRStatusResponse(req, http.StatusNotFound, metav1.StatusReasonNotFound, "gpudevices \""+dev.Name+"\" not found")
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	puts := gkaRStatusPUTs(rig.Rec, "gpudevices", dev.Name)
	if attempts.Load() < 4 || len(puts) < 4 {
		t.Fatalf("GKA-125: %d attempts; a NotFound is skipped and the next pass tries again (want the 4th attempt to succeed)", attempts.Load())
	}
	for i := 1; i < 4; i++ {
		if !gkaRPassBetween(rig.A, string(sc.Nodes[0].UID), puts[i-1].At, puts[i].At) {
			t.Errorf("GKA-125: attempt %d followed its NotFound inside the same pass; a NotFound is retried by a later pass only", i+1)
		}
	}
	for _, p := range puts {
		if bytes.Contains(p.Body, []byte("status write rejected")) {
			t.Errorf("GKA-125: a NotFound triggered the InternalError fallback")
		}
	}
	got := gkaRGetDevice(t, e, dev.Name)
	if got.Status.LifecyclePhase == v1alpha1.LifecyclePhaseUnknown {
		t.Errorf("GKA-125: the device ended in phase Unknown (InternalError) after transient NotFound answers")
	}
}

// 429, 5xx, connection errors and timeouts: no retry inside the pass (client-go
// retries only with Retry-After, which the harness forbids), a requeue capped at
// ResyncInterval, and no permanent-error fallback. One device per failure kind
// runs in the same scene; "no in-pass retry" means a new pass (an assessor call
// for the device's node) lies between consecutive attempts.
func TestGKA125_TransientErrorsRequeueWithoutInPassRetry(t *testing.T) {
	e := gkaEnv(t)
	kinds := []string{"500", "429", "connection error", "timeout"}
	sc := gkaRNewScene(t, e, len(kinds), 1, v1alpha1.FleetModeAudit)
	const failures = 4
	rig := gkaRNewRig(e)
	suffixes := make([]string, len(kinds))
	for i, d := range sc.Devices {
		suffixes[i] = gkaRPathFor("gpudevices", d.Name) + "/status"
	}
	counts := make([]atomic.Int64, len(kinds))
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut {
			return nil
		}
		for i, kind := range kinds {
			if !strings.HasSuffix(req.URL.Path, suffixes[i]) || (kind != "500" && kind != "429") {
				continue
			}
			if counts[i].Add(1) <= failures {
				if kind == "429" {
					return gkaRStatusResponse(req, http.StatusTooManyRequests, metav1.StatusReasonTooManyRequests, "injected 429")
				}
				return gkaRStatusResponse(req, http.StatusInternalServerError, metav1.StatusReasonInternalError, "injected 500")
			}
		}
		return nil
	})
	rig.Rec.InjectErr(func(req *http.Request) error {
		if req.Method != http.MethodPut {
			return nil
		}
		for i, kind := range kinds {
			if !strings.HasSuffix(req.URL.Path, suffixes[i]) || (kind != "connection error" && kind != "timeout") {
				continue
			}
			if counts[i].Add(1) <= failures {
				if kind == "timeout" {
					return fmt.Errorf("gka: injected request timeout: %w", os.ErrDeadlineExceeded)
				}
				return errors.New("gka: injected connection reset by peer")
			}
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	for i, kind := range kinds {
		dev, node := sc.Devices[i], sc.Nodes[i]
		puts := gkaRStatusPUTs(rig.Rec, "gpudevices", dev.Name)
		if len(puts) < failures+1 {
			t.Errorf("GKA-125 (%s): %d PUT attempts, want the failing ones plus a success", kind, len(puts))
			continue
		}
		for j := 1; j <= failures; j++ {
			if !gkaRPassBetween(rig.A, string(node.UID), puts[j-1].At, puts[j].At) {
				t.Errorf("GKA-125 (%s): attempt %d followed the failure inside the same pass; there is no retry inside the pass", kind, j+1)
			}
			if g := puts[j].At.Sub(puts[j-1].At); g > 1200*time.Millisecond {
				t.Errorf("GKA-125/141 (%s): attempt %d came %s after the failure; the requeue interval is capped at ResyncInterval (200ms)", kind, j+1, g)
			}
		}
		for _, p := range puts {
			if bytes.Contains(p.Body, []byte("status write rejected")) {
				t.Errorf("GKA-125 (%s): a transient error must not trigger the permanent-error fallback", kind)
			}
		}
		if got := gkaRGetDevice(t, e, dev.Name); got.Status.LifecyclePhase != v1alpha1.LifecyclePhasePending {
			t.Errorf("GKA-125 (%s): final phase %q, want Pending after the error cleared", kind, got.Status.LifecyclePhase)
		}
	}
}

// clusters groups entries whose gaps are at most maxGap.
func gkaRClusters(es []gkaRecEntry, maxGap time.Duration) [][]gkaRecEntry {
	var out [][]gkaRecEntry
	for i, e := range es {
		if i == 0 || e.At.Sub(es[i-1].At) > maxGap {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], e)
	}
	return out
}

func gkaRDeviceBodyOrFail(t *testing.T, body []byte) *v1alpha1.GPUDevice {
	t.Helper()
	d, err := gkaRDecodeDevice(body)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return d
}

// 401, 403 and 422 are permanent: no retry in the pass, one fallback write with
// the InternalError minimal status, and the object is tried again only by the next
// tick, never by event passes. One device per status code runs in the same scene.
func TestGKA125_PermanentErrorsFallBackOnceAndWaitForTheTick(t *testing.T) {
	e := gkaEnv(t)
	codes := []struct {
		code   int
		reason metav1.StatusReason
	}{
		{http.StatusUnprocessableEntity, metav1.StatusReasonInvalid},
		{http.StatusForbidden, metav1.StatusReasonForbidden},
		{http.StatusUnauthorized, metav1.StatusReasonUnauthorized},
	}
	sc := gkaRNewScene(t, e, len(codes), 1, v1alpha1.FleetModeAudit)
	unrelated := gkaRNode(t, e, gkaName(t, "unrelated"), nil) // label churn on it raises event passes
	const resync = time.Second
	markers := make([]string, len(codes))
	suffixes := make([]string, len(codes))
	for i, c := range codes {
		markers[i] = "GKA-" + strconv.Itoa(c.code) + "-MARKER-" + gkaRHash(t.Name(), 16)
		suffixes[i] = gkaRPathFor("gpudevices", sc.Devices[i].Name) + "/status"
	}
	rig := gkaRNewRig(e)
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut {
			return nil
		}
		for i, c := range codes {
			if strings.HasSuffix(req.URL.Path, suffixes[i]) {
				return gkaRStatusResponse(req, c.code, c.reason, "injected rejection "+markers[i])
			}
		}
		return nil
	})
	rig.Start(t, nil, func(o *controller.Options) { o.ResyncInterval = resync })
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		for i, d := range sc.Devices {
			if n := len(gkaRStatusPUTs(rig.Rec, "gpudevices", d.Name)); n < 2 {
				return false, fmt.Sprintf("device %d has only %d status PUT(s) so far", i, n)
			}
		}
		return true, ""
	})
	deadline := time.Now().Add(4 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		gkaRSetNodeLabels(t, e, unrelated.Name, map[string]string{"gka.churn": strconv.Itoa(i)})
		time.Sleep(250 * time.Millisecond)
	}

	logs := gkaRCtlLogs(rig.Ctl)
	for i, c := range codes {
		puts := gkaRStatusPUTs(rig.Rec, "gpudevices", sc.Devices[i].Name)
		clusters := gkaRClusters(puts, 300*time.Millisecond)
		if len(clusters) < 3 {
			t.Errorf("GKA-125 (%d): %d attempt cluster(s), want the initial one and at least two later ticks", c.code, len(clusters))
			continue
		}
		for j, cl := range clusters[:len(clusters)-1] { // the last cluster may still be in flight
			if len(cl) != 2 {
				t.Errorf("GKA-125 (%d): attempt cluster %d has %d PUTs, want exactly 2 (the write and one InternalError fallback, no retry)", c.code, j, len(cl))
				continue
			}
			if bytes.Contains(cl[0].Body, []byte("status write rejected")) || !bytes.Contains(cl[1].Body, []byte("status write rejected")) {
				t.Errorf("GKA-125/087 (%d): cluster %d is not (rejected write, InternalError fallback with class status write rejected)", c.code, j)
			}
		}
		// The first gap (start-up pass to the first tick) has no fixed length; later ticks are a full
		// ResyncInterval apart. Event passes (every 250ms here) must not add attempts.
		for j := 2; j < len(clusters); j++ {
			if gap := clusters[j][0].At.Sub(clusters[j-1][0].At); gap < 800*time.Millisecond {
				t.Errorf("GKA-125 (%d): the object was tried again after %s; only the next tick (ResyncInterval %s) may retry a permanent error, not event passes", c.code, gap, resync)
			}
		}
		if len(clusters[0]) >= 2 {
			fb := gkaRDeviceBodyOrFail(t, clusters[0][1].Body)
			if fb.Status.LifecyclePhase != v1alpha1.LifecyclePhaseUnknown || fb.Status.ActualBinding.Reason != "InternalError" || fb.Status.Allocation.Reason != "InternalError" || fb.Status.Qualification != v1alpha1.QualificationUnknown {
				t.Errorf("GKA-087/072 (%d): fallback status = phase %q binding reason %q allocation reason %q qualification %q", c.code, fb.Status.LifecyclePhase, fb.Status.ActualBinding.Reason, fb.Status.Allocation.Reason, fb.Status.Qualification)
			}
			for _, cd := range fb.Status.Conditions {
				if cd.Reason != "InternalError" || !strings.Contains(cd.Message, "internal error: status write rejected") {
					t.Errorf("GKA-087/074 (%d): fallback condition %s = %q / %q", c.code, cd.Type, cd.Reason, cd.Message)
				}
			}
		}
		// GKA-084/163: neither the response body nor an error text reaches a request body or the log.
		for _, p := range puts {
			if bytes.Contains(p.Body, []byte(markers[i])) {
				t.Errorf("GKA-074(e)/084 (%d): the API response text leaked into a status write", c.code)
			}
		}
		if strings.Contains(logs, markers[i]) {
			t.Errorf("GKA-084/163 (%d): the API response body reached the controller log", c.code)
		}
		// Other scopes keep being written while this object is rejected (GKA-126).
		if nps, ok := gkaRTryNPS(e, sc.Nodes[i].Name); !ok || len(nps.Status.Conditions) == 0 {
			t.Errorf("GKA-126 (%d): the NodePathState of the same node was not written while the device write was rejected", c.code)
		}
	}
	if !strings.Contains(logs, "level=ERROR") {
		t.Errorf("GKA-125: a failed permanent-error fallback must be logged at error level; log:\n%s", logs)
	}
}

// The fallback write succeeds: the API server holds the InternalError status until
// a later pass renders the logical status again (checked on the recorded bodies,
// which do not depend on how long the fallback status stays visible).
func TestGKA125_FallbackStatusIsWrittenAndRecovers(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	dev := sc.Devices[0]
	rig := gkaRNewRig(e)
	gkaRInjectStatusPUT(rig.Rec, "gpudevices", dev.Name, func(req *http.Request, n int64) *http.Response {
		if n == 1 {
			return gkaRStatusResponse(req, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, "injected rejection")
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		puts := gkaRStatusPUTs(rig.Rec, "gpudevices", dev.Name)
		return len(puts) >= 3, fmt.Sprintf("%d status PUTs, want the rejected write, the fallback and the recovery", len(puts))
	})
	puts := gkaRStatusPUTs(rig.Rec, "gpudevices", dev.Name)
	fb := gkaRDeviceBodyOrFail(t, puts[1].Body)
	c := gkaRCond(fb.Status.Conditions, "DeviceQualified")
	if fb.Status.LifecyclePhase != v1alpha1.LifecyclePhaseUnknown || c == nil || c.Reason != "InternalError" || !strings.Contains(c.Message, "internal error: status write rejected") {
		t.Errorf("GKA-087/125: the second write is not the InternalError minimal status: phase %q condition %+v", fb.Status.LifecyclePhase, c)
	}
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		d := gkaRGetDevice(t, e, dev.Name)
		return d.Status.LifecyclePhase == v1alpha1.LifecyclePhasePending && d.Status.ActualBinding.Reason == "Validating", fmt.Sprintf("phase %q binding reason %q", d.Status.LifecyclePhase, d.Status.ActualBinding.Reason)
	})
}

// A delete that fails on its precondition is skipped and tried again on the next
// pass; it never becomes an InternalError, and the delete carries the UID precondition.
func TestGKA125_102_DeletePreconditionConflictIsSkippedAndRetried(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit) // node 1 stays selected: it marks every pass
	leaving, staying := sc.Nodes[0], sc.Nodes[1]
	rig := gkaRNewRig(e)
	var deletes atomic.Int64
	var blocking atomic.Bool
	blocking.Store(true)
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/nodepathstates/") && blocking.Load() {
			deletes.Add(1)
			return gkaRStatusResponse(req, http.StatusConflict, metav1.StatusReasonConflict, "Precondition failed: UID in precondition does not match")
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	before, ok := gkaRTryNPS(e, leaving.Name)
	if !ok {
		t.Fatal("NodePathState missing")
	}
	gkaRSetNodeLabels(t, e, leaving.Name, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return deletes.Load() >= 3, fmt.Sprintf("only %d delete attempts", deletes.Load())
	})
	var dels []gkaRecEntry
	for _, w := range rig.Rec.WriteEntries(leaving.Name) {
		if w.Method == http.MethodDelete {
			dels = append(dels, w)
		}
	}
	for i := 1; i < 3; i++ {
		if !gkaRPassBetween(rig.A, string(staying.UID), dels[i-1].At, dels[i].At) {
			t.Errorf("GKA-125: delete attempt %d followed a precondition conflict inside the same pass; the retry belongs to the next pass", i+1)
		}
	}
	// The body may be JSON or protobuf (client choice); the UID precondition must be in it either way.
	if !bytes.Contains(dels[0].Body, []byte(before.UID)) {
		t.Errorf("GKA-102: the delete carries no UID precondition for NodePathState %s (body %q)", before.UID, dels[0].Body)
	}
	if nps, ok := gkaRTryNPS(e, leaving.Name); !ok {
		t.Fatal("the NodePathState was deleted although the delete was rejected")
	} else if c := gkaRCond(nps.Status.Conditions, "NodeEligible"); c != nil && c.Reason == "InternalError" {
		t.Errorf("GKA-125: a delete precondition conflict was rendered as InternalError")
	}
	blocking.Store(false)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		_, ok := gkaRTryNPS(e, leaving.Name)
		return !ok, "the NodePathState still exists after the conflict cleared"
	})
}

// ---------------------------------------------------------------------------
// GKA-126
// ---------------------------------------------------------------------------

func TestGKA126_AssessorErrorOfOneNodeDoesNotAffectTheOthers(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 3, 1, v1alpha1.FleetModeAudit)
	marker := "ASSESSOR-SECRET-" + gkaRHash(t.Name(), 16)
	failing := sc.Nodes[0]
	rig := gkaRStart(t, e, gkaRFailNode(string(failing.UID), errors.New(marker), nil), nil)
	gkaRWaitSettled(t, e, sc)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		d := gkaRGetDevice(t, e, sc.Devices[0].Name)
		c := gkaRCond(d.Status.Conditions, "DeviceQualified")
		return c != nil && c.Reason == "InternalError", fmt.Sprintf("failing node device conditions = %+v", d.Status.Conditions)
	})
	bad := gkaRGetDevice(t, e, sc.Devices[0].Name)
	if bad.Status.LifecyclePhase != v1alpha1.LifecyclePhaseUnknown {
		t.Errorf("GKA-084: the failed node's device phase = %q, want Unknown (the last Ready is not kept)", bad.Status.LifecyclePhase)
	}
	if c := gkaRCond(bad.Status.Conditions, "DeviceQualified"); !strings.Contains(c.Message, "internal error: assessor failed") {
		t.Errorf("GKA-084: message = %q, want the class assessor failed", c.Message)
	}
	for i := 1; i < 3; i++ {
		good := gkaRGetDevice(t, e, sc.Devices[i].Name)
		if good.Status.LifecyclePhase != v1alpha1.LifecyclePhasePending || good.Status.ActualBinding.Reason != "Validating" {
			t.Errorf("GKA-126: device %d of an unaffected node has phase %q reason %q, want the normal cold-start status", i, good.Status.LifecyclePhase, good.Status.ActualBinding.Reason)
		}
		nps, ok := gkaRTryNPS(e, sc.Nodes[i].Name)
		if !ok {
			t.Fatalf("NodePathState %d missing", i)
		}
		if c := gkaRCond(nps.Status.Conditions, "NodeEligible"); c == nil || c.Reason != "Validating" {
			t.Errorf("GKA-126: unaffected node %d NodeEligible = %+v, want Validating", i, c)
		}
	}
	nps, _ := gkaRTryNPS(e, failing.Name)
	if c := gkaRCond(nps.Status.Conditions, "NodeEligible"); c == nil || c.Reason != "InternalError" {
		t.Errorf("GKA-084/076: failing node NodeEligible = %+v, want InternalError", c)
	}
	// The error text reaches neither status nor log (GKA-074(e), GKA-084).
	all := fmt.Sprintf("%+v %+v %s", bad.Status, nps.Status, gkaRCtlLogs(rig.Ctl))
	if strings.Contains(all, marker) {
		t.Errorf("GKA-074(e)/084: the assessor's error text leaked into a status or the log")
	}
	if !strings.Contains(gkaRCtlLogs(rig.Ctl), "level=") {
		t.Errorf("GKA-084: nothing was logged about the failed assessment")
	}
}

func TestGKA126_WriteFailureOfOneDeviceDoesNotBlockTheOthers(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 3, 1, v1alpha1.FleetModeAudit)
	stuck := sc.Devices[1]
	rig := gkaRNewRig(e)
	var failing atomic.Bool
	failing.Store(true)
	suffix := gkaRPathFor("gpudevices", stuck.Name) + "/status"
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if failing.Load() && req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, suffix) {
			return gkaRStatusResponse(req, http.StatusInternalServerError, metav1.StatusReasonInternalError, "injected")
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		for i, d := range sc.Devices {
			if i == 1 {
				continue
			}
			if gkaRGetDevice(t, e, d.Name).Status.ObservedRequestID == "" {
				return false, "device " + d.Name + " has no status"
			}
		}
		for _, n := range sc.Nodes {
			if nps, ok := gkaRTryNPS(e, n.Name); !ok || len(nps.Status.Conditions) == 0 {
				return false, "NodePathState " + n.Name + " has no status"
			}
		}
		return len(gkaRGetFleet(t, e, sc.Fleet.Name).Status.Conditions) > 0, "fleet has no status"
	})
	time.Sleep(600 * time.Millisecond)
	if gkaRGetDevice(t, e, stuck.Name).Status.ObservedRequestID != "" {
		t.Fatal("test premise: the injected failure did not block the stuck device")
	}
	failing.Store(false)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return gkaRGetDevice(t, e, stuck.Name).Status.ObservedRequestID != "", "the stuck device never recovered"
	})
}

// ---------------------------------------------------------------------------
// GKA-127
// ---------------------------------------------------------------------------

func TestGKA127_RestartDistrustsStatusSummariesButReusesIntentAndTransitionTimes(t *testing.T) {
	e := gkaEnv(t)
	ctx := context.Background()
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	node, dev, fl := sc.Nodes[0], sc.Devices[0], sc.Fleet
	const old = "2026-01-01T00:00:00Z"
	const oldLater = "2026-01-01T00:00:30Z"

	// What a previous run left behind: everything looks Ready and Qualified.
	observed, expires := gkaRTime(oldLater), gkaRTime("2026-01-01T00:05:30Z")
	cond := func(typ string, status metav1.ConditionStatus, reason, msg, at string) metav1.Condition {
		return metav1.Condition{Type: typ, Status: status, ObservedGeneration: 1, LastTransitionTime: gkaRTime(at), Reason: reason, Message: msg}
	}
	d := gkaRGetDevice(t, e, dev.Name)
	d.Status = gkaRColdDeviceStatus(d, old, []metav1.Condition{
		cond("AllocationKnown", metav1.ConditionUnknown, "AllocationUnknown", "allocation=Unknown", old),
		cond("DeviceQualified", metav1.ConditionTrue, "Ready", "qualification=Qualified phase=Ready", oldLater),
		cond("IdentityBound", metav1.ConditionTrue, "Ready", "binding=Bound", oldLater),
		cond("LifecycleReady", metav1.ConditionTrue, "Ready", "phase=Ready", oldLater),
	})
	d.Status.ActualBinding = v1alpha1.ActualBinding{
		State: v1alpha1.BindingStateBound, Reason: "Ready", Vendor: gkaRp("NVIDIA"), UUID: d.Spec.InventoryClaim.UUID, NodeUID: gkaRp(string(node.UID)),
		BootID: gkaRp("boot-a"), BDF: gkaRp("0000:03:00.0"), FunctionKey: gkaRp("PCIeFunction/pci-bdf:0000:03:00.0"),
		Source: &v1alpha1.SourceRef{Type: "agent", Name: "path-agent/nvidia-smi"}, EvidenceID: gkaRp("nb:7:2:c0ffee"), ObservedAt: &observed, ExpiresAt: &expires,
	}
	d.Status.GraphRevision = gkaRp(gkaRGraphRev)
	d.Status.Qualification = v1alpha1.QualificationQualified
	d.Status.LifecyclePhase = v1alpha1.LifecyclePhaseReady
	if err := e.Client.Status().Update(ctx, d); err != nil {
		t.Fatalf("seed the previous device status: %v", err)
	}
	fresh := gkaRGetFleet(t, e, fl.Name)
	fresh.Status = v1alpha1.GPUFleetStatus{
		ObservedGeneration: 1, AssessmentRevision: gkaRp(gkaRHex64('d')), SelectedCount: 1, ReadyCount: 1, OwnedNodeRefs: []v1alpha1.OwnedNodeRef{},
		Conditions: []metav1.Condition{cond("FleetReady", metav1.ConditionTrue, "Ready", "selected=1 ready=1 degraded=0 unknown=0", oldLater)},
	}
	if err := e.Client.Status().Update(ctx, fresh); err != nil {
		t.Fatalf("seed the previous fleet status: %v", err)
	}
	nps := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{
			Name:            node.Name,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}},
		},
		Spec: v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: node.Name, UID: string(node.UID)}},
	}
	if err := e.Client.Create(ctx, nps); err != nil {
		t.Fatalf("create the previous NodePathState: %v", err)
	}
	nps.Status = v1alpha1.NodePathStateStatus{
		ObservedGeneration: 1, NodeRef: nps.Spec.NodeRef, GraphRevision: gkaRp(gkaRGraphRev), EvidenceCompleteness: v1alpha1.SnapshotCompletenessComplete,
		DeviceSummaries: []v1alpha1.DeviceSummary{{
			Name: dev.Name, UID: string(dev.UID), DesiredState: v1alpha1.DesiredStateInService, ObservedGeneration: 1,
			Qualification: v1alpha1.QualificationQualified, LifecyclePhase: v1alpha1.LifecyclePhaseReady,
		}},
		Conditions: []metav1.Condition{
			cond("EvidenceFresh", metav1.ConditionTrue, "Ready", "completeness=Complete", oldLater),
			cond("NodeEligible", metav1.ConditionTrue, "Ready", "eligibility=Eligible qualification=Qualified", oldLater),
		},
	}
	if err := e.Client.Status().Update(ctx, nps); err != nil {
		t.Fatalf("seed the previous NodePathState status: %v", err)
	}

	// A new controller with an assessor that has nothing to say.
	rig := gkaRStart(t, e, nil, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		got := gkaRGetDevice(t, e, dev.Name)
		return got.Status.LifecyclePhase == v1alpha1.LifecyclePhasePending, fmt.Sprintf("device phase = %q", got.Status.LifecyclePhase)
	})
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		f := gkaRGetFleet(t, e, fl.Name)
		c := gkaRCond(f.Status.Conditions, "FleetReady")
		n, _ := gkaRTryNPS(e, node.Name)
		return c != nil && c.Status == metav1.ConditionUnknown && n != nil && n.Status.EvidenceCompleteness == v1alpha1.SnapshotCompletenessUnknown,
			fmt.Sprintf("fleet FleetReady=%+v nps=%+v", c, n)
	})

	got := gkaRGetDevice(t, e, dev.Name)
	if got.Status.Qualification != v1alpha1.QualificationUnknown || got.Status.ActualBinding.State != v1alpha1.BindingStateUnknown || got.Status.ActualBinding.Reason != "Validating" || got.Status.GraphRevision != nil {
		t.Errorf("GKA-127: the previous summary was reused: qualification %q binding %+v graphRevision %v", got.Status.Qualification, got.Status.ActualBinding, got.Status.GraphRevision)
	}
	if !got.Status.IntentObservedAt.UTC().Equal(gkaRTime(old).UTC()) || got.Status.ObservedRequestID != "enroll-1" {
		t.Errorf("GKA-071/127: intentObservedAt=%s observedRequestID=%q, want the stored %s / enroll-1 reused", got.Status.IntentObservedAt, got.Status.ObservedRequestID, old)
	}
	if c := gkaRCond(got.Status.Conditions, "AllocationKnown"); c == nil || !c.LastTransitionTime.UTC().Equal(gkaRTime(old).UTC()) {
		t.Errorf("GKA-074(c)/127: AllocationKnown lastTransitionTime = %+v, want the stored %s (status unchanged)", c, old)
	}
	if c := gkaRCond(got.Status.Conditions, "DeviceQualified"); c == nil || c.Status != metav1.ConditionUnknown || !c.LastTransitionTime.UTC().Equal(gkaRT0) {
		t.Errorf("GKA-074(c)/127: DeviceQualified = %+v, want Unknown with lastTransitionTime = the new pass time %s", c, gkaRT0)
	}
	f := gkaRGetFleet(t, e, fl.Name)
	if f.Status.AssessmentRevision != nil || f.Status.ReadyCount != 0 || f.Status.UnknownCount != 1 || f.Status.SelectedCount != 1 {
		t.Errorf("GKA-127/079: fleet status = revision %v ready %d unknown %d selected %d; the stored Ready summary must not survive", f.Status.AssessmentRevision, f.Status.ReadyCount, f.Status.UnknownCount, f.Status.SelectedCount)
	}
	if c := gkaRCond(f.Status.Conditions, "FleetReady"); c == nil || c.Reason != "Validating" || !c.LastTransitionTime.UTC().Equal(gkaRT0) {
		t.Errorf("GKA-078/127: FleetReady = %+v, want Unknown/Validating transitioned at %s", c, gkaRT0)
	}
	n, _ := gkaRTryNPS(e, node.Name)
	if n.Status.GraphRevision != nil || len(n.Status.DeviceSummaries) != 1 || n.Status.DeviceSummaries[0].Qualification != v1alpha1.QualificationUnknown {
		t.Errorf("GKA-127/076: NodePathState still shows the stored summary: %+v", n.Status)
	}
	if c := gkaRCond(n.Status.Conditions, "NodeEligible"); c == nil || c.Status != metav1.ConditionUnknown || c.Reason != "Validating" {
		t.Errorf("GKA-127/076: NodeEligible = %+v, want Unknown/Validating", c)
	}
	reqs := rig.A.Requests()
	if len(reqs) == 0 || len(reqs[0].Intents) != 1 || !reqs[0].Intents[0].ObservedAt.Equal(gkaRTime(old).UTC()) {
		t.Errorf("GKA-071/081: the first request must carry the stored intentObservedAt %s, got %+v", old, reqs)
	}
}

// ---------------------------------------------------------------------------
// GKA-128 / GKA-027
// ---------------------------------------------------------------------------

func TestGKA128_AllRequestsStayWithinTheDeclaredVerbSet(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 1, v1alpha1.FleetModeAudit)
	rig := gkaRNewRig(e)
	var conflicted atomic.Bool
	suffix := gkaRPathFor("gpudevices", sc.Devices[0].Name) + "/status"
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, suffix) && conflicted.CompareAndSwap(false, true) {
			return gkaRConflict(req) // makes the retry issue a direct GET
		}
		return nil
	})
	rig.Start(t, nil, nil)
	gkaRWaitSettled(t, e, sc)
	// Exercise delete, re-create and the Node condition withdrawal and re-publication.
	gkaRSetNodeLabels(t, e, sc.Nodes[1].Name, nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		_, ok := gkaRTryNPS(e, sc.Nodes[1].Name)
		return !ok, "NodePathState of the unselected node still exists"
	})
	gkaRSetNodeLabels(t, e, sc.Nodes[1].Name, sc.Selector)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		nps, ok := gkaRTryNPS(e, sc.Nodes[1].Name)
		return ok && len(nps.Status.Conditions) > 0, "NodePathState of the re-selected node is not back"
	})
	rig.Ctl.Cancel()
	if _, err := gkaRWaitDone(t, rig.Ctl, 12*time.Second); err != nil {
		t.Errorf("Run = %v", err)
	}
	// GKA-128/175: the observed (verb, resource) set is a subset of the declared set (discovery
	// excluded) and no Event is requested. Which of the declared verbs occur is not required.
	t.Run("observed verbs are a subset of the declared set", func(t *testing.T) {
		gkaRAssertVerbs(t, rig.Rec)
	})
	// The scenario forces these writes, so their absence means the scenario did not run as designed.
	t.Run("the scenario exercised the core write verbs", func(t *testing.T) {
		seen := map[string]bool{}
		for _, en := range rig.Rec.Entries() {
			if r := gkaRClassify(en.Method, en.Path, en.Watch); !r.Discovery {
				seen[r.key()] = true
			}
		}
		for _, want := range []string{
			"update gpufleets/status", "update gpudevices/status", "create nodepathstates", "update nodepathstates/status", "delete nodepathstates", "patch nodes/status",
		} {
			if !seen[want] {
				t.Errorf("GKA-128: the scenario never issued %q (issued: %v)", want, seen)
			}
		}
	})
}

type gkaRCountingRT struct {
	next http.RoundTripper
	mu   sync.Mutex
	seen []string
}

func (c *gkaRCountingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.seen = append(c.seen, req.Method+" "+req.URL.Path)
	c.mu.Unlock()
	return c.next.RoundTrip(req)
}

func (c *gkaRCountingRT) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// A config that carries a complete Transport (not just WrapTransport) is honoured
// for every request: all traffic goes through it, none around it.
func TestGKA027_CustomTransportCarriesAllRequests(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	base, err := rest.TransportFor(e.Config)
	if err != nil {
		t.Fatalf("rest.TransportFor: %v", err)
	}
	crt := &gkaRCountingRT{next: base}
	cfg := &rest.Config{Host: e.Config.Host, Transport: crt}
	a := newGkaFakeAssessor()
	gkaStartController(t, cfg, a, newGkaFakeClock(gkaRT0), nil)
	gkaRWaitSettled(t, e, sc)
	seen := crt.all()
	has := func(method, substr string) bool {
		for _, s := range seen {
			if strings.HasPrefix(s, method+" ") && strings.Contains(s, substr) {
				return true
			}
		}
		return false
	}
	for _, w := range []struct{ method, substr string }{
		{"GET", "/apis/coordination.k8s.io/v1/namespaces/default/leases/"}, {"GET", "/gpudevices"}, {"GET", "/gpufleets"}, {"GET", "/nodes"},
		{"PUT", "/gpudevices/" + sc.Devices[0].Name + "/status"}, {"PATCH", "/nodes/" + sc.Nodes[0].Name + "/status"}, {"POST", "/nodepathstates"},
	} {
		if !has(w.method, w.substr) {
			t.Errorf("GKA-027: no %s %s went through the caller's Transport (%d requests seen)", w.method, w.substr, len(seen))
		}
	}
}

// The controller listens on no TCP port (metrics, health, pprof and webhook are off).
func TestGKA027_ControllerOpensNoListenSocket(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	gkaRStart(t, e, nil, nil)
	gkaRWaitSettled(t, e, sc)
	time.Sleep(500 * time.Millisecond)
	n, how, err := gkaRListenSockets()
	if err != nil {
		t.Skipf("GKA-027: cannot enumerate listening sockets (%v)", err)
	}
	if n != 0 {
		t.Errorf("GKA-027: the process has %d listening TCP socket(s) (%s), want 0 while the controller runs", n, how)
	}
}

// gkaRListenSockets counts this process's LISTEN sockets with lsof, falling back to /proc.
func gkaRListenSockets() (int, string, error) {
	if path, err := exec.LookPath("lsof"); err == nil {
		out, err := exec.Command(path, "-nP", "-a", "-p", strconv.Itoa(os.Getpid()), "-iTCP", "-sTCP:LISTEN").Output()
		if err != nil && len(out) > 0 {
			return 0, "lsof", fmt.Errorf("lsof: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return 0, "lsof", nil
		}
		return len(lines) - 1, "lsof: " + strings.Join(lines, " | "), nil // first line is the header
	}
	inodes := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			return 0, "", err
		}
		for i, line := range strings.Split(string(b), "\n") {
			fs := strings.Fields(line)
			if i == 0 || len(fs) < 10 || fs[3] != "0A" {
				continue
			}
			inodes[fs[9]] = true
		}
	}
	links, err := filepath.Glob("/proc/self/fd/*")
	if err != nil {
		return 0, "", err
	}
	n := 0
	for _, l := range links {
		target, err := os.Readlink(l)
		if err == nil && strings.HasPrefix(target, "socket:[") && inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
			n++
		}
	}
	return n, "/proc", nil
}

// ---------------------------------------------------------------------------
// GKA-034
// ---------------------------------------------------------------------------

// Every request handed to the assessor is valid, and an assessor that scribbles over
// its request cannot influence later requests: the request is built fresh per call.
func TestGKA034_RequestsAreValidAndBuiltFreshPerCall(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 2, 2, v1alpha1.FleetModeAudit)
	rig := gkaRNewRig(e)
	rig.A.SetScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		for i := range req.Intents {
			req.Intents[i].RequestID = "scribbled"
			req.Intents[i].Claim.Vendor = "scribbled"
		}
		if len(req.Policy.RequiredCoverage) > 0 {
			req.Policy.RequiredCoverage[0].Name = "scribbled"
		}
		req.Intents = req.Intents[:0]
		return app.NodeAssessment{}, nil
	})
	rig.Ctl = gkaStartController(t, rig.Cfg, rig.A, rig.Clk, nil)
	gkaRWaitSettled(t, e, sc)
	gkaEventually(t, 20*time.Second, func() (bool, string) { return rig.A.AssessCount() >= 12, "fewer than 12 assessor calls" })

	fleetUID := string(sc.Fleet.UID)
	for i, r := range rig.A.Requests() {
		if _, err := fleet.NewPolicy(r.Policy); err != nil {
			t.Errorf("GKA-034: request %d Policy is invalid: %v", i, err)
		}
		if len(r.Intents) != 2 {
			t.Errorf("GKA-034: request %d has %d intents, want 2 (a previous call's scribbling leaked?)", i, len(r.Intents))
		}
		for j, in := range r.Intents {
			if err := in.Validate(); err != nil {
				t.Errorf("GKA-034: request %d intent %d is invalid: %v", i, j, err)
			}
			if in.RequestID != "enroll-1" || in.Claim.Vendor != "NVIDIA" {
				t.Errorf("GKA-034: request %d intent %d = %+v: an earlier assessor mutation leaked into a later request", i, j, in)
			}
			if j > 0 && r.Intents[j-1].Device.UID >= in.Device.UID {
				t.Errorf("GKA-030: request %d intents are not in device UID byte order without duplicates", i)
			}
		}
		if len(r.Policy.RequiredCoverage) != 3 || r.Policy.RequiredCoverage[0].Name != "pcie-parent" {
			t.Errorf("GKA-034/080: request %d policy coverage = %+v: an earlier mutation leaked", i, r.Policy.RequiredCoverage)
		}
		if r.Selection != fleet.SelectionComplete && r.Selection != fleet.SelectionPartial {
			t.Errorf("GKA-030: request %d Selection = %v, only Complete or Partial may be passed", i, r.Selection)
		}
		if r.Node.ClusterID != "gka-cluster" || r.FleetUID != fleetUID || r.Node.Name == "" || r.Node.UID == "" {
			t.Errorf("GKA-081: request %d Node=%+v FleetUID=%q, want ClusterID gka-cluster and the fleet UID %q", i, r.Node, r.FleetUID, fleetUID)
		}
	}
}
