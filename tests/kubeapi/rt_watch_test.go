package kubeapi_test

// GKA-140: creating, changing or deleting a GPUFleet, GPUDevice, NodePathState or
// Node (label, UID, deletion) starts a pass without waiting for the tick. The
// ResyncInterval is 30 s, so every reaction below that arrives within 5 s cannot
// be tick driven; the whole scenario stays well under the first tick.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

func TestGKA140_WatchEventsStartPassesWithoutWaitingForTheTick(t *testing.T) {
	e := gkaEnv(t)
	ctx := context.Background()
	sel := gkaRSelector(t)
	n1 := gkaRNode(t, e, gkaName(t, "n1"), nil)
	n2 := gkaRNode(t, e, gkaName(t, "n2"), sel)
	fl := gkaRFleet(t, e, gkaName(t, "fl"), sel, v1alpha1.FleetModeAudit)

	rig := gkaRStart(t, e, nil, func(o *controller.Options) { o.ResyncInterval = 30 * time.Second })
	started := time.Now()
	// The first pass creates the NodePathState of the selected node.
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		_, ok := gkaRTryNPS(e, n2.Name)
		f := gkaRGetFleet(t, e, fl.Name)
		return ok && len(f.Status.Conditions) > 0, "first pass has not finished"
	})
	within := func(what string, cond func() (bool, string)) {
		t.Helper()
		gkaEventually(t, 5*time.Second, func() (bool, string) {
			ok, desc := cond()
			return ok, "GKA-140: " + what + " was not reflected within 5s: " + desc
		})
	}

	// Node label added.
	gkaRSetNodeLabels(t, e, n1.Name, sel)
	within("a Node label addition", func() (bool, string) {
		_, ok := gkaRTryNPS(e, n1.Name)
		return ok, "no NodePathState for the newly selected node"
	})

	// GPUDevice created.
	dev := gkaRDevice(t, e, gkaName(t, "n1-d0"), n1, fl)
	within("a GPUDevice creation", func() (bool, string) {
		return gkaRGetDevice(t, e, dev.Name).Status.ObservedRequestID != "", "the device has no status"
	})

	// NodePathState deleted: recreated with a new UID.
	old, _ := gkaRTryNPS(e, n1.Name)
	if err := e.Client.Delete(ctx, old); err != nil {
		t.Fatalf("delete NodePathState: %v", err)
	}
	within("a NodePathState deletion", func() (bool, string) {
		got, ok := gkaRTryNPS(e, n1.Name)
		return ok && got.UID != old.UID && len(got.Status.Conditions) > 0, fmt.Sprintf("NodePathState = %+v", got)
	})

	// NodePathState changed by someone else: the controller restores its status.
	gkaRRetry(t, "tamper with NodePathState status", func() error {
		nps, ok := gkaRTryNPS(e, n1.Name)
		if !ok {
			return fmt.Errorf("NodePathState missing")
		}
		nps.Status.EvidenceCompleteness = v1alpha1.SnapshotCompletenessPartial
		nps.Status.GraphRevision = gkaRp("tampered")
		return e.Client.Status().Update(ctx, nps)
	})
	within("a NodePathState change", func() (bool, string) {
		got, _ := gkaRTryNPS(e, n1.Name)
		return got != nil && got.Status.EvidenceCompleteness == v1alpha1.SnapshotCompletenessUnknown && got.Status.GraphRevision == nil, fmt.Sprintf("status = %+v", got)
	})

	// GPUFleet spec changed.
	gkaRRetry(t, "edit fleet", func() error {
		f := gkaRGetFleet(t, e, fl.Name)
		f.Spec.FreshnessSeconds = 61
		return e.Client.Update(ctx, f)
	})
	within("a GPUFleet update", func() (bool, string) {
		f := gkaRGetFleet(t, e, fl.Name)
		return f.Status.ObservedGeneration == 2, fmt.Sprintf("generation %d observedGeneration %d", f.Generation, f.Status.ObservedGeneration)
	})

	// GPUDevice deleted: no device is left on a selected node.
	if err := e.Client.Delete(ctx, gkaRGetDevice(t, e, dev.Name)); err != nil {
		t.Fatalf("delete GPUDevice: %v", err)
	}
	within("a GPUDevice deletion", func() (bool, string) {
		c := gkaRCond(gkaRGetFleet(t, e, fl.Name).Status.Conditions, "FleetReady")
		return c != nil && c.Reason == "NoMatchingDevices", fmt.Sprintf("FleetReady = %+v", c)
	})

	// Node label removed: the NodePathState goes away.
	gkaRSetNodeLabels(t, e, n1.Name, nil)
	within("a Node label removal", func() (bool, string) {
		_, ok := gkaRTryNPS(e, n1.Name)
		return !ok, "the NodePathState of the unselected node still exists"
	})

	// Node deleted.
	if err := e.Client.Delete(ctx, gkaRGetNode(t, e, n2.Name)); err != nil {
		t.Fatalf("delete Node: %v", err)
	}
	within("a Node deletion", func() (bool, string) {
		_, ok := gkaRTryNPS(e, n2.Name)
		return !ok, "the NodePathState of the deleted Node still exists"
	})

	if el := time.Since(started); el > 25*time.Second {
		t.Errorf("test premise: the scenario took %s, close to the 30s tick; the reactions cannot be attributed to watch events", el)
	}
	gkaRAssertVerbs(t, rig.Rec)
}
