package kubeapi_test

// GKA-070: pass model as observable through the assessor port: one Clock read per
// pass, AssessNode once per assessable node in Node UID byte order, ForgetNode before
// any AssessNode of the pass that dropped a node (once per transition), and the call
// deadline equal to ResyncInterval with a timeout rendered as one node's InternalError.

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

type gkaSEvent struct {
	Kind string // "assess" or "forget"
	Node string // node name (assess)
	UID  string // node UID
	Now  time.Time
}

// gkaSTraceAssessor implements app.NodeAssessor with ONE ordered log of AssessNode and
// ForgetNode calls (the harness fake keeps them in separate lists) and records the
// remaining call deadline. It is used only where relative order matters.
type gkaSTraceAssessor struct {
	mu        sync.Mutex
	events    []gkaSEvent
	remaining []time.Duration
	plan      gkaSPlan
	hook      func(ctx context.Context, req app.NodeAssessmentRequest) (handled bool, na app.NodeAssessment, err error)
}

func newGkaSTrace(plan gkaSPlan) *gkaSTraceAssessor { return &gkaSTraceAssessor{plan: plan} }

func (a *gkaSTraceAssessor) AssessNode(ctx context.Context, req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	a.mu.Lock()
	a.events = append(a.events, gkaSEvent{Kind: "assess", Node: req.Node.Name, UID: req.Node.UID, Now: req.Now})
	if dl, ok := ctx.Deadline(); ok {
		a.remaining = append(a.remaining, time.Until(dl))
	} else {
		a.remaining = append(a.remaining, -1)
	}
	hook, plan := a.hook, a.plan
	a.mu.Unlock()
	if hook != nil {
		if handled, na, err := hook(ctx, req); handled {
			return na, err
		}
	}
	return plan(req)
}

func (a *gkaSTraceAssessor) ForgetNode(uid string) {
	a.mu.Lock()
	a.events = append(a.events, gkaSEvent{Kind: "forget", UID: uid})
	a.mu.Unlock()
}

func (a *gkaSTraceAssessor) Events() []gkaSEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]gkaSEvent(nil), a.events...)
}

func (a *gkaSTraceAssessor) Remaining() []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]time.Duration(nil), a.remaining...)
}

func gkaSSortedByUID(nodes ...*corev1.Node) []*corev1.Node {
	out := append([]*corev1.Node(nil), nodes...)
	sort.Slice(out, func(i, j int) bool { return string(out[i].UID) < string(out[j].UID) })
	return out
}

// GKA-070(c)(f) + GKA-023: every pass reads the Clock exactly once and calls AssessNode
// exactly once per assessable node in Node UID byte order. The first call of each pass
// moves the fake clock by 10 s: a second read in the same pass would show up as a
// different req.Now for the later nodes.
func TestGKA070_OnePassOneClockReadNodesInUIDOrder(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	nodes := []*corev1.Node{w.selNode("n1"), w.selNode("n2"), w.selNode("n3")}
	for i, n := range nodes {
		w.device("d"+string(rune('a'+i)), n, f)
	}
	sorted := gkaSSortedByUID(nodes...)
	clk := newGkaFakeClock(gkaST0)
	tr := newGkaSTrace(gkaSPlanNone)
	tr.hook = func(_ context.Context, req app.NodeAssessmentRequest) (bool, app.NodeAssessment, error) {
		if req.Node.UID == string(sorted[0].UID) {
			clk.Advance(10 * time.Second) // the first node of each pass
		}
		return false, app.NodeAssessment{}, nil
	}
	w.start(tr, clk)

	gkaEventually(t, gkaSWaitMax, func() (bool, string) { return len(tr.Events()) >= 12, "waiting for four passes" })
	ev := tr.Events()[:12]
	for i, e := range ev {
		if e.Kind != "assess" || e.UID != string(sorted[i%3].UID) {
			t.Fatalf("GKA-070(f): call %d = %s %s, want assess of %s (once per node per pass, Node UID byte order): %+v", i, e.Kind, e.UID, sorted[i%3].UID, ev)
		}
	}
	for p := 0; p < 4; p++ {
		nows := []time.Time{ev[3*p].Now, ev[3*p+1].Now, ev[3*p+2].Now}
		if !nows[0].Equal(nows[1]) || !nows[1].Equal(nows[2]) {
			t.Errorf("GKA-070(c): pass %d saw Now = %v, want one value for the whole pass", p, nows)
		}
		if p > 0 && !ev[3*p].Now.After(ev[3*(p-1)].Now) {
			t.Errorf("GKA-070(c): pass %d Now %v is not after pass %d Now %v", p, ev[3*p].Now, p-1, ev[3*(p-1)].Now)
		}
	}
}

// GKA-070(e): ForgetNode(uid) is called exactly once when a node that was assessable in
// the previous pass is not assessable now (overlap -> Conflict), when its Node object is
// deleted (old UID), and when it is replaced by a new UID; the call precedes every
// AssessNode of that pass, and a node that becomes assessable again is forgotten again
// only after it stops being assessable again.
func TestGKA070_ForgetNodeComesBeforeAnyAssessNodeOfThatPass(t *testing.T) {
	w := newGkaSWorld(t)
	keyB := "gka-sem/b"
	f := w.selFleet("f")
	suffix := map[string]string{}
	var all []*corev1.Node
	for _, sfx := range []string{"n1", "n2", "n3"} {
		n := w.selNode(sfx)
		suffix[n.Name] = sfx
		all = append(all, n)
		w.device("d"+sfx, n, f)
	}
	s := gkaSSortedByUID(all...)
	n0, n1, n2 := s[0], s[1], s[2] // Node UID byte order
	tr := newGkaSTrace(gkaSPlanReady)
	w.start(tr, newGkaFakeClock(gkaST0))

	indexOf := func(kind, uid string, from int) int {
		ev := tr.Events()
		for i := from; i >= 0 && i < len(ev); i++ {
			if ev[i].Kind == kind && ev[i].UID == uid {
				return i
			}
		}
		return -1
	}
	count := func(kind, uid string) int {
		n := 0
		for _, e := range tr.Events() {
			if e.Kind == kind && e.UID == uid {
				n++
			}
		}
		return n
	}
	lastAssess := func(uid string) int {
		ev := tr.Events()
		for i := len(ev) - 1; i >= 0; i-- {
			if ev[i].Kind == "assess" && ev[i].UID == uid {
				return i
			}
		}
		return -1
	}
	// expectDropOrder: after the last AssessNode of the dropped node the rest of that pass (the
	// nodes with a larger UID) is assessed, then the next pass starts with ForgetNode(dropped).
	expectDropOrder := func(what string, dropped *corev1.Node, later []*corev1.Node, nextFirst *corev1.Node) {
		t.Helper()
		ev := tr.Events()
		l := lastAssess(string(dropped.UID))
		i := l + 1
		for _, n := range later {
			if i >= len(ev) || ev[i].Kind != "assess" || ev[i].UID != string(n.UID) {
				t.Errorf("GKA-070(e) %s: after the last AssessNode of %s the same pass continues with %s, got %+v", what, dropped.Name, n.Name, ev[l+1:])
				return
			}
			i++
		}
		if i >= len(ev) || ev[i].Kind != "forget" || ev[i].UID != string(dropped.UID) {
			t.Errorf("GKA-070(e) %s: the next pass must start with ForgetNode(%s), got %+v", what, dropped.UID, ev[l+1:])
			return
		}
		if i+1 >= len(ev) || ev[i+1].Kind != "assess" || ev[i+1].UID != string(nextFirst.UID) {
			t.Errorf("GKA-070(e) %s: ForgetNode must be followed by AssessNode of %s, got %+v", what, nextFirst.Name, ev[i:])
		}
	}

	gkaEventually(t, gkaSWaitMax, func() (bool, string) { return len(tr.Events()) >= 6, "waiting for two full passes" })
	for _, n := range s {
		if got := count("forget", string(n.UID)); got != 0 {
			t.Fatalf("GKA-070(e): ForgetNode(%s) before any node stopped being assessable: %+v", n.UID, tr.Events())
		}
	}

	// Phase A: a second fleet selects n0 -> Conflict. n0 is assessed for the last time, then forgotten once.
	w.updateNode(n0.Name, func(o *corev1.Node) { o.Labels[keyB] = w.sel })
	fB := w.fleet("fb", metav1.LabelSelector{MatchLabels: map[string]string{keyB: w.sel}})
	gkaEventually(t, gkaSWaitMax, func() (bool, string) { return indexOf("forget", string(n0.UID), 0) >= 0, "waiting for ForgetNode(n0)" })
	gkaSSettle(5)
	expectDropOrder("conflict", n0, []*corev1.Node{n1, n2}, n1)
	if got := count("forget", string(n0.UID)); got != 1 {
		t.Errorf("GKA-070(e): ForgetNode(n0) called %d times for one transition, want 1", got)
	}
	// It becomes assessable again: no further ForgetNode by itself.
	w.deleteFleet(fB.Name)
	gkaEventually(t, gkaSWaitMax, func() (bool, string) {
		return indexOf("assess", string(n0.UID), indexOf("forget", string(n0.UID), 0)) >= 0, "waiting until n0 is assessed again"
	})
	gkaSSettle(3)
	if got := count("forget", string(n0.UID)); got != 1 {
		t.Errorf("GKA-070(e): ForgetNode(n0) = %d calls after n0 was assessable again, want still 1", got)
	}

	// Phase B: the Node object of n1 (middle UID) is deleted: its old UID is forgotten first.
	w.deleteNode(n1.Name)
	gkaEventually(t, gkaSWaitMax, func() (bool, string) { return indexOf("forget", string(n1.UID), 0) >= 0, "waiting for ForgetNode(n1)" })
	gkaSSettle(3)
	expectDropOrder("node deleted", n1, []*corev1.Node{n2}, n0)
	if got := count("forget", string(n1.UID)); got != 1 {
		t.Errorf("GKA-070(e): ForgetNode(n1) called %d times, want 1", got)
	}

	// Phase C: n2 (largest UID) is replaced by a node of the same name and a new UID, with a device of its own.
	w.deleteNode(n2.Name)
	repl := w.node(suffix[n2.Name], map[string]string{gkaSSelKey: w.sel})
	if repl.UID == n2.UID {
		t.Fatalf("test setup: the replacement reused the UID")
	}
	w.device("dnew", repl, f)
	gkaEventually(t, gkaSWaitMax, func() (bool, string) {
		return indexOf("forget", string(n2.UID), 0) >= 0 && indexOf("assess", string(repl.UID), 0) >= 0, "waiting for the replacement to be assessed"
	})
	fi, ai := indexOf("forget", string(n2.UID), 0), indexOf("assess", string(repl.UID), 0)
	if fi > ai {
		t.Errorf("GKA-070(e): the old UID was forgotten (event %d) after the replacement was assessed (event %d)", fi, ai)
	}
	if got := indexOf("assess", string(n2.UID), fi); got >= 0 {
		t.Errorf("GKA-070(e): the old UID was assessed again after ForgetNode (event %d)", got)
	}
}

// GKA-070(f): the AssessNode context carries a deadline of ResyncInterval; a call that
// runs into it renders that node as InternalError "assessor failed" while the other
// node of the same pass is rendered normally (GKA-084, GKA-126).
func TestGKA070_AssessNodeDeadlineIsResyncInterval(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	nSlow, nFast := w.selNode("slow"), w.selNode("fast")
	dSlow := w.device("dslow", nSlow, f)
	dFast := w.device("dfast", nFast, f)
	tr := newGkaSTrace(gkaSPlanReady)
	tr.hook = func(ctx context.Context, req app.NodeAssessmentRequest) (bool, app.NodeAssessment, error) {
		if req.Node.Name == nSlow.Name {
			<-ctx.Done()
			return true, app.NodeAssessment{}, ctx.Err()
		}
		return false, app.NodeAssessment{}, nil
	}
	const resync = 2 * time.Second
	w.start(tr, newGkaFakeClock(gkaST0), func(o *controller.Options) { o.ResyncInterval = resync })

	const class = "internal error: assessor failed"
	w.waitDevice(dSlow.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "InternalError") })
	w.waitDevice(dFast.Name, func(d *v1alpha1.GPUDevice) string { return gkaSDeviceHas(d, "Ready") })
	gkaSCheckNonDecision(t, "GKA-070(f) slow node", w.deviceNow(dSlow.Name), gkaSNonDec{SR: "InternalError", Phase: "Unknown", Tail: []string{class}})
	w.waitNPS(nSlow.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	w.waitNode(nSlow.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "Unknown", "InternalError", gkaSMsg("fleet="+f.Name, class)})
	})
	w.waitNode(nFast.Name, func(n *corev1.Node) string {
		return gkaSNodeCondIs(n, &gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + f.Name})
	})
	rem := tr.Remaining()
	if len(rem) == 0 {
		t.Fatalf("GKA-070(f): the assessor was never called")
	}
	for _, r := range rem {
		if r < 0 {
			t.Errorf("GKA-070(f): AssessNode context has no deadline")
			break
		}
		if r > resync || r < resync-1500*time.Millisecond {
			t.Errorf("GKA-070(f): remaining deadline at call = %s, want within (%s, %s]", r, resync-1500*time.Millisecond, resync)
			break
		}
	}
}
