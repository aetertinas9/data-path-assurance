package liveingestenv_test

// End-to-end scenarios of GLI-140 against a real API server: the agent library
// client sends frames of a BASE sysfs fixture and a fake nvidia-smi to the ingest
// server, the Kubernetes adapter persists sessions and gates leadership, and the
// S3a controller (NewLiveAssessor over the server's bundle source) projects the
// result into the GPUDevice and NodePathState status.
//
//	L-READY        GPUDevice Validating -> Ready, collectorSession = graphRevision session
//	L-RESTART      new process state: larger session, no observation before the baseline
//	L-LEADER-BLIP  one Lease read error changes nothing; sustained errors drop the
//	               observation and refuse hellos; recovery needs a new baseline
//
// Clock rules (GLI-127): server, controller and agent share one fake Clock, moved
// by 15 s for every frame S3a has published (never less than the 10 s frame
// interval, never a stalled clock); the adapter keeps the system clock; the
// controller resync is 200 ms.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

func gliePhaseReady(d *v1alpha1.GPUDevice) bool {
	return d.Status.LifecyclePhase == v1alpha1.LifecyclePhaseReady && d.Status.Qualification == v1alpha1.QualificationQualified
}

// conditionMessage returns the message of the device condition of the type.
func conditionMessage(conds []metav1.Condition, typ string) string {
	for _, c := range conds {
		if c.Type == typ {
			return c.Message
		}
	}
	return ""
}

// L-READY: agent -> ingest server -> S3a controller. Observed: the GPUDevice is
// first not ready, then Ready/Qualified once the frames span the ready-for
// window; collectorSession is the session of the ServerHello and the first
// component of the published graphRevision; the evidence is Complete.
func TestGLI140_LReady_AgentToServerToControllerEndToEnd(t *testing.T) {
	e := glieEnv(t)
	r := glieNewRig(t, e)
	r.StartCore(t, nil)
	r.StartAgent(t, nil)
	sawNotReady := false
	r.Drive(t, 20*time.Second, func() (bool, string) {
		d := r.Device(t)
		if !gliePhaseReady(d) {
			sawNotReady = true
		}
		return gliePhaseReady(d), fmt.Sprintf("device phase %q qualification %q, %d frames published, conditions %+v", d.Status.LifecyclePhase, d.Status.Qualification, r.Frames, d.Status.Conditions)
	})
	if !sawNotReady {
		t.Errorf("L-READY: the device was Ready at the first look; it must pass through Validating before ReadyFor has elapsed (GFL-042)")
	}
	nps := r.NPS()
	if nps == nil || nps.Status.GraphRevision == nil || nps.Status.CollectorSession == nil {
		t.Fatalf("L-READY: NodePathState status after Ready: %+v", nps)
	}
	session, seq, ok := glieRevision(*nps.Status.GraphRevision)
	if !ok {
		t.Fatalf("L-READY: graphRevision %q is not <session>:<sequence>:<digest>", *nps.Status.GraphRevision)
	}
	if *nps.Status.CollectorSession != session || session < 1 {
		t.Errorf("L-READY/GLI-033: collectorSession = %d, graphRevision session = %d, want the same positive ServerHello session", *nps.Status.CollectorSession, session)
	}
	if seq < 2 {
		t.Errorf("L-READY: Ready after sequence %d; the 30 s ready window needs frames at 0, 15 and 30 s", seq)
	}
	if nps.Status.EvidenceCompleteness != v1alpha1.SnapshotCompletenessComplete {
		t.Errorf("L-READY: evidenceCompleteness = %q, want Complete", nps.Status.EvidenceCompleteness)
	}
	if !r.Files.Running() {
		t.Errorf("GLI-083: the agent stopped by itself")
	}
	if v := r.Ad.Rec.Violations(); len(v) > 0 {
		t.Errorf("harness: %v", v)
	}
	if devs, err := r.Probe(t); err != nil || devs != 1 {
		t.Errorf("GLI-075: NodeBundles for the node returned %d device(s), %v; want the one claimed device", devs, err)
	}
}

// L-RESTART: the controller side restarts (new adapter, server, S3a controller on
// the same Lease). A hello allocates a session greater than the stored one, and
// before the new complete sequence 0 NodeBundles has no observation and the status
// is Validating with no_observation; the new agent then establishes a baseline of a
// still later session.
func TestGLI140_LRestart_NewSessionAndNoObservationBeforeTheBaseline(t *testing.T) {
	e := glieEnv(t)
	r := glieNewRig(t, e)
	r.StartCore(t, nil)
	r.StartAgent(t, nil)
	r.Drive(t, 20*time.Second, func() (bool, string) {
		s, q, ok := r.Published()
		return ok && q >= 1, fmt.Sprintf("published revision session %d sequence %d ok %v", s, q, ok)
	})
	s1, _, _ := r.Published()
	if nps := r.NPS(); nps.Status.CollectorSession == nil || *nps.Status.CollectorSession != s1 {
		t.Fatalf("fixture: collectorSession %v, graphRevision session %d", nps.Status.CollectorSession, s1)
	}

	// everything on the controller side stops; so does the agent.
	if err := r.Files.Stop(t); err != nil {
		t.Errorf("liveclient.Run returned %v on cancellation, want nil", err)
	}
	if err := r.Srv.Stop(t); err != nil {
		t.Errorf("Serve returned %v", err)
	}
	if err := r.Ctl.Stop(t); err != nil {
		t.Logf("controller returned %v", err)
	}
	if err := r.Ad.StopPoll(t); err != nil {
		t.Errorf("RunLeaderPoll returned %v", err)
	}

	r.StartCore(t, nil) // new process state: empty memory everywhere, the stored session stays
	leaf := r.Files.Leaf
	sh, closeStream, err := glieHello(t, r.Srv, leaf, glieClientHello(r.Scene.Node))
	if err != nil || sh.GetSession() <= s1 {
		t.Fatalf("L-RESTART: hello after the restart = %v, %v, want a session greater than the stored %d", sh, err, s1)
	}
	defer closeStream()
	glieEventually(t, 20*time.Second, func() (bool, string) {
		_, ok := r.Tee.Last()
		return ok, "the new controller has not queried the bundle source yet"
	})
	if devs, err := r.Probe(t); !errors.Is(err, app.ErrNoObservation) || devs != 0 {
		t.Errorf("L-RESTART/GLI-035: NodeBundles after the hello and before any frame = %d device(s), %v, want app.ErrNoObservation", devs, err)
	}
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n := r.NPS()
		d := r.Device(t)
		msg := conditionMessage(d.Status.Conditions, "DeviceQualified")
		ok := n != nil && n.Status.CollectorSession != nil && *n.Status.CollectorSession == sh.GetSession() &&
			n.Status.GraphRevision == nil && n.Status.EvidenceCompleteness == v1alpha1.SnapshotCompletenessUnknown && strings.Contains(msg, "no_observation")
		return ok, fmt.Sprintf("collectorSession %v graphRevision %v completeness %q DeviceQualified %q", n.Status.CollectorSession, n.Status.GraphRevision, n.Status.EvidenceCompleteness, msg)
	})
	if d := r.Device(t); gliePhaseReady(d) {
		t.Errorf("L-RESTART: the device is Ready after a restart without any observation (GFL-127)")
	}
	closeStream()

	// the agent comes back: a later session and a new baseline.
	r.Files = glieNewAgentFiles(t, r.PKI, r.Scene.Node)
	r.StartAgent(t, nil)
	r.Drive(t, 20*time.Second, func() (bool, string) {
		s, q, ok := r.Published()
		return ok && s > sh.GetSession() && q >= 0, fmt.Sprintf("published session %d sequence %d ok %v, want a session greater than %d", s, q, ok, sh.GetSession())
	})
	if devs, err := r.Probe(t); err != nil || devs != 1 {
		t.Errorf("L-RESTART: NodeBundles after the new baseline = %d device(s), %v, want the observation back", devs, err)
	}
}

// L-LEADER-BLIP: (A) one failed Lease read changes nothing: Leading() stays true,
// the observation stays (NodeBundles answers, the status stays Complete with its
// graphRevision, it never returns to no_observation); (B) sustained failures
// revoke the gate after StaleAfter: NodeBundles answers ErrNoObservation, a new
// hello is Unavailable, and S3a returns to no_observation; (C) when the Lease can
// be read again the gate is true, but there is still no observation until a new
// hello and a new complete sequence 0.
func TestGLI140_LLeaderBlip_OneErrorChangesNothingSustainedErrorsDropTheObservation(t *testing.T) {
	e := glieEnv(t)
	r := glieNewRig(t, e)
	r.StartCore(t, nil)
	r.StartAgent(t, nil)
	stopDrive := r.DriveBackground(t)
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n := r.NPS()
		_, err := r.Probe(t)
		return n != nil && n.Status.EvidenceCompleteness == v1alpha1.SnapshotCompletenessComplete && err == nil, fmt.Sprintf("status %+v probe %v", n, err)
	})
	leaseName := r.Lease
	isLeaseGet := func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/leases/"+leaseName)
	}

	// (A) a single blip.
	var fired atomic.Bool
	r.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if isLeaseGet(req) && fired.CompareAndSwap(false, true) {
			return glieStatusResponse(req, 500, metav1.StatusReasonInternalError, "one blip")
		}
		return nil
	})
	glieEventually(t, 5*time.Second, func() (bool, string) { return fired.Load(), "the blip was never injected" })
	end := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(end) {
		if !r.Ad.Leading() {
			t.Fatalf("L-LEADER-BLIP: one failed Lease read turned Leading() false")
		}
		if _, err := r.Probe(t); err != nil {
			t.Fatalf("L-LEADER-BLIP: NodeBundles = %v after one failed Lease read, want the observation kept", err)
		}
		n := r.NPS()
		if n.Status.EvidenceCompleteness != v1alpha1.SnapshotCompletenessComplete || n.Status.GraphRevision == nil ||
			strings.Contains(conditionMessage(n.Status.Conditions, "NodeEligible"), "no_observation") {
			t.Fatalf("L-LEADER-BLIP: the status went back to no_observation after one failed Lease read: %+v", n.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.Ad.Rec.Inject(nil)

	// (B) sustained failures.
	var sustained atomic.Bool
	r.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if sustained.Load() && isLeaseGet(req) {
			return glieStatusResponse(req, 500, metav1.StatusReasonInternalError, "sustained")
		}
		return nil
	})
	sustained.Store(true)
	r.Ad.WaitLeading(t, false, 15*time.Second)
	if devs, err := r.Probe(t); !errors.Is(err, app.ErrNoObservation) || devs != 0 {
		t.Errorf("GLI-037/L-LEADER-BLIP: NodeBundles with Leading() false = %d device(s), %v, want app.ErrNoObservation", devs, err)
	}
	_, closeStream, err := glieHello(t, r.Srv, r.Files.Leaf, glieClientHello(r.Scene.Node))
	closeStream()
	if status.Code(err) != codes.Unavailable {
		t.Errorf("L-LEADER-BLIP/GLI-030 (2): a hello while the gate is false = %v, want Unavailable", err)
	}
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n := r.NPS()
		return n != nil && n.Status.GraphRevision == nil && n.Status.EvidenceCompleteness == v1alpha1.SnapshotCompletenessUnknown,
			fmt.Sprintf("status %+v", n.Status)
	})

	// (C) the Lease can be read again; the agent is held back so that no hello can arrive first.
	stopDrive()
	if err := r.Files.Stop(t); err != nil {
		t.Errorf("liveclient.Run returned %v on cancellation, want nil", err)
	}
	sustained.Store(false)
	r.Ad.WaitLeading(t, true, 10*time.Second)
	if devs, err := r.Probe(t); !errors.Is(err, app.ErrNoObservation) || devs != 0 {
		t.Errorf("L-LEADER-BLIP/GLI-037: with Leading() true again and no new hello NodeBundles = %d device(s), %v, want app.ErrNoObservation", devs, err)
	}
	sh, closeStream, err := glieHello(t, r.Srv, r.Files.Leaf, glieClientHello(r.Scene.Node))
	if err != nil || sh.GetSession() < 1 {
		t.Fatalf("L-LEADER-BLIP: hello after the recovery = %v, %v", sh, err)
	}
	if devs, err := r.Probe(t); !errors.Is(err, app.ErrNoObservation) || devs != 0 {
		t.Errorf("GLI-035/037: after the new hello and before complete sequence 0 NodeBundles = %d device(s), %v, want app.ErrNoObservation", devs, err)
	}
	closeStream()
	r.Files = glieNewAgentFiles(t, r.PKI, r.Scene.Node)
	r.StartAgent(t, nil)
	r.DriveBackground(t)
	glieEventually(t, 20*time.Second, func() (bool, string) {
		devs, err := r.Probe(t)
		return err == nil && devs == 1, fmt.Sprintf("NodeBundles = %d device(s), %v", devs, err)
	})
}
