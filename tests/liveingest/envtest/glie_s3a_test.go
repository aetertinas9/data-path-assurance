package liveingestenv_test

// The coupling of collectorSession with the S3a status controller (GLI-033 (i)-(iv),
// GKA-105/124): the session written by the ingest side survives S3a's passes and
// its own status writes, a hello does not erase what S3a published, a restart of
// the controller and adapter allocates a larger session, and a NodePathState
// that was deleted and re-created has an empty collectorSession until the next
// hello fills it. S3a runs in-process with the no-observation assessor.

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

type glieS3a struct {
	R   *glieRig
	Ad  *glieAdapter
	Ctl *glieCtlRig
}

// glieStartS3a creates a scene, runs the adapter (leader poll) and an S3a
// controller sharing one Lease, and waits until S3a published the node status.
func glieStartS3a(t *testing.T, e *glieEnvT) *glieS3a {
	t.Helper()
	r := glieNewRig(t, e)
	s := &glieS3a{R: r}
	s.start(t)
	return s
}

func (s *glieS3a) start(t *testing.T) {
	t.Helper()
	// GLI-091 order: controller.New (which installs the process-wide loggers) before any client-go
	// activity of the adapter's leader poll (GKA-005).
	s.Ad = glieNewAdapter(t, s.R.E, glieAdapterOpts{ControllerID: s.R.CtlID, LeaseName: s.R.Lease, NoPoll: true})
	s.Ctl = glieStartController(t, s.R.E, s.R.CtlID, s.R.Lease, newNoObservationAssessor(), s.R.Clk, nil)
	s.Ad.StartPoll(t)
	s.Ad.WaitLeading(t, true, 20*time.Second)
	s.waitPublished(t)
}

func (s *glieS3a) waitPublished(t *testing.T) *v1alpha1.NodePathState {
	t.Helper()
	var got *v1alpha1.NodePathState
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(s.R.E, s.R.Scene.Node.Name)
		got = n
		return ok && len(n.Status.Conditions) > 0, "S3a has not published the node status yet"
	})
	return got
}

// withoutSession is a status with collectorSession cleared, for comparing
// everything else.
func withoutSession(s v1alpha1.NodePathStateStatus) v1alpha1.NodePathStateStatus {
	cp := *s.DeepCopy()
	cp.CollectorSession = nil
	return cp
}

// GLI-033 (i)/GKA-105: after a hello the stored session stays through many S3a
// passes and through S3a's own status rewrites (a second device changes
// deviceSummaries); S3a never writes or drops it.
func TestGLI033_i_SessionSurvivesS3aPasses(t *testing.T) {
	e := glieEnv(t)
	s := glieStartS3a(t, e)
	node := s.R.Scene.Node
	session := glieMustAlloc(t, s.Ad, node, 0)
	end := time.Now().Add(2500 * time.Millisecond) // about a dozen passes at ResyncInterval 200 ms
	for time.Now().Before(end) {
		n := glieMustNPS(t, e, node.Name)
		if n.Status.CollectorSession == nil || *n.Status.CollectorSession != session {
			t.Fatalf("GLI-033 (i): collectorSession = %v during S3a passes, want %d kept", n.Status.CollectorSession, session)
		}
		if len(n.Status.Conditions) == 0 {
			t.Fatalf("GLI-033 (i): S3a's conditions disappeared: %+v", n.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	glieDeviceUUID(t, e, glieName(t, "dev2"), node, s.R.Scene.Fleet, "GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9")
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, node.Name)
		return ok && len(n.Status.DeviceSummaries) == 2, fmt.Sprintf("deviceSummaries %+v", n)
	})
	n := glieMustNPS(t, e, node.Name)
	if n.Status.CollectorSession == nil || *n.Status.CollectorSession != session {
		t.Errorf("GLI-033 (i)/GKA-124: S3a rewrote the status for a new device and collectorSession = %v, want %d preserved", n.Status.CollectorSession, session)
	}
}

// GLI-033 (ii): once S3a has published, a hello changes collectorSession and
// nothing else of the status.
func TestGLI033_ii_HelloKeepsWhatS3aPublished(t *testing.T) {
	e := glieEnv(t)
	s := glieStartS3a(t, e)
	node := s.R.Scene.Node
	s.waitPublished(t)
	time.Sleep(600 * time.Millisecond)
	before := glieMustNPS(t, e, node.Name)
	session := glieMustAlloc(t, s.Ad, node, 0)
	time.Sleep(900 * time.Millisecond) // more S3a passes after the hello
	after := glieMustNPS(t, e, node.Name)
	if after.Status.CollectorSession == nil || *after.Status.CollectorSession != session {
		t.Fatalf("GLI-033 (ii): collectorSession = %v, want %d", after.Status.CollectorSession, session)
	}
	b, a := withoutSession(before.Status), withoutSession(after.Status)
	if !reflect.DeepEqual(b, a) {
		t.Errorf("GLI-033 (ii)/I3: the hello changed what S3a published:\nbefore %+v\nafter  %+v", b, a)
	}
	if len(a.Conditions) == 0 || a.EvidenceCompleteness != v1alpha1.SnapshotCompletenessUnknown {
		t.Errorf("GLI-033 (ii): unexpected S3a status after the hello: %+v", a)
	}
}

// GLI-033 (iii)/GLI-035/GFL-127: a restart of the controller and the adapter loses
// every in-memory value but not the stored session: it is still there, and the next
// allocation is larger.
func TestGLI033_iii_RestartAllocatesALargerSession(t *testing.T) {
	e := glieEnv(t)
	s := glieStartS3a(t, e)
	node := s.R.Scene.Node
	first := glieMustAlloc(t, s.Ad, node, 0)
	if err := s.Ctl.Stop(t); err != nil {
		t.Logf("controller returned %v", err)
	}
	if err := s.Ad.StopPoll(t); err != nil {
		t.Errorf("RunLeaderPoll returned %v", err)
	}
	if n := glieMustNPS(t, e, node.Name); n.Status.CollectorSession == nil || *n.Status.CollectorSession != first {
		t.Fatalf("GLI-033 (iii): the stored session is %v after the controller stopped, want %d", n.Status.CollectorSession, first)
	}
	s.start(t) // a new adapter (no memory of `after`) and a new controller on the same Lease
	n := glieMustNPS(t, e, node.Name)
	if n.Status.CollectorSession == nil || *n.Status.CollectorSession != first {
		t.Errorf("GLI-033 (iii)/GKA-105: after the restart S3a changed collectorSession to %v, want %d", n.Status.CollectorSession, first)
	}
	second := glieMustAlloc(t, s.Ad, node, 0)
	if second <= first {
		t.Errorf("GLI-033 (iii): the first session after a restart is %d, want one greater than the stored %d", second, first)
	}
	time.Sleep(800 * time.Millisecond)
	if n := glieMustNPS(t, e, node.Name); n.Status.CollectorSession == nil || *n.Status.CollectorSession != second {
		t.Errorf("GLI-033 (iii): collectorSession = %v after more S3a passes, want %d", n.Status.CollectorSession, second)
	}
}

// GLI-033 (iv): a NodePathState that is deleted is re-created by S3a (a new
// object, no status carried over); between that and the node's next hello S3a has
// published conditions while collectorSession is empty; the next hello (with the
// engine's `after`) fills it with a session that was never issued before.
func TestGLI033_iv_RecreatedObjectHasAnEmptySessionUntilTheNextHello(t *testing.T) {
	e := glieEnv(t)
	s := glieStartS3a(t, e)
	node := s.R.Scene.Node
	old := glieMustNPS(t, e, node.Name)
	issued := glieMustAlloc(t, s.Ad, node, 0)
	if err := e.Client.Delete(context.Background(), old); err != nil {
		t.Fatalf("delete NodePathState: %v", err)
	}
	var fresh *v1alpha1.NodePathState
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, node.Name)
		fresh = n
		return ok && n.UID != old.UID && len(n.Status.Conditions) > 0, "S3a has not re-created and published the NodePathState yet"
	})
	if fresh.Status.CollectorSession != nil {
		t.Errorf("GLI-033 (iv)/GKA-103/105: the re-created NodePathState has collectorSession %d, want it empty until the next hello (no status is carried over)", *fresh.Status.CollectorSession)
	}
	next := glieMustAlloc(t, s.Ad, node, issued)
	if next <= issued {
		t.Errorf("GLI-031/033 (iv): the session after the re-creation is %d, want one greater than %d that was issued before the deletion", next, issued)
	}
	glieEventually(t, 10*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, node.Name)
		return ok && n.Status.CollectorSession != nil && *n.Status.CollectorSession == next && len(n.Status.Conditions) > 0,
			fmt.Sprintf("status %+v", n)
	})
}
