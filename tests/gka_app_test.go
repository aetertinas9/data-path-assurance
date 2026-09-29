package tests_test

// gpu-fleet-k8s-api section 2.3: the evaluation port in internal/app.
// GKA-030 (port and DTO surface, goroutine safety), GKA-031 (absence of a
// decision), GKA-032 (LiveAssessor: bundle validation, overwriting, previous
// memory, ForgetNode), GKA-033 (source port, "no observation" source).
//
// Expected values are computed with the same S1 API the assessor is specified to
// call (fleet.EvaluateDevice and fleet.AggregateNode), on bundles built by the
// existing fleetBundle helper, so nothing here re-derives S1 semantics by hand.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// ---------------------------------------------------------------------------
// GKA-030: surface
// ---------------------------------------------------------------------------

func gkaRAppFields(t *testing.T, name string, v any, want map[string]reflect.Type) {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.NumField() != len(want) {
		t.Errorf("GKA-030: %s has %d fields, want exactly %d", name, typ.NumField(), len(want))
	}
	for field, ft := range want {
		f, ok := typ.FieldByName(field)
		if !ok {
			t.Errorf("GKA-030: %s.%s is missing", name, field)
			continue
		}
		if f.Type != ft {
			t.Errorf("GKA-030: %s.%s has type %v, want %v", name, field, f.Type, ft)
		}
	}
}

func TestGKA030_PortAndDTOSurface(t *testing.T) {
	var _ app.NodeAssessor = app.NewLiveAssessor(app.NewNoObservationSource())
	var _ func(app.LiveBundleSource) app.NodeAssessor = app.NewLiveAssessor
	var _ func() app.LiveBundleSource = app.NewNoObservationSource

	str, tm := reflect.TypeOf(""), reflect.TypeOf(time.Time{})
	gkaRAppFields(t, "NodeAssessmentRequest", app.NodeAssessmentRequest{}, map[string]reflect.Type{
		"Node": reflect.TypeOf(fleet.NodeRef{}), "FleetUID": str, "Policy": reflect.TypeOf(fleet.Policy{}),
		"Selection": reflect.TypeOf((*fleet.SelectionState)(nil)).Elem(), "Intents": reflect.TypeOf([]fleet.Intent(nil)), "Now": tm,
	})
	gkaRAppFields(t, "NodeAssessment", app.NodeAssessment{}, map[string]reflect.Type{
		"Devices": reflect.TypeOf([]app.DeviceAssessment(nil)), "Node": reflect.TypeOf((*fleet.NodeDecision)(nil)), "Observation": reflect.TypeOf((*app.NodeObservation)(nil)),
	})
	gkaRAppFields(t, "DeviceAssessment", app.DeviceAssessment{}, map[string]reflect.Type{
		"Decision": reflect.TypeOf(fleet.DeviceDecision{}), "Findings": reflect.TypeOf([]app.FindingRecord(nil)),
		"Evidence": reflect.TypeOf([]app.EvidenceRecord(nil)), "Allocation": reflect.TypeOf((*app.AllocationRecord)(nil)),
	})
	gkaRAppFields(t, "NodeObservation", app.NodeObservation{}, map[string]reflect.Type{
		"GraphRevision": str, "Completeness": reflect.TypeOf((*fleet.SnapshotCompleteness)(nil)).Elem(),
	})
	gkaRAppFields(t, "FindingRecord", app.FindingRecord{}, map[string]reflect.Type{"ID": str, "Type": str, "Severity": str, "State": str})
	gkaRAppFields(t, "EvidenceRecord", app.EvidenceRecord{}, map[string]reflect.Type{"ID": str, "Summary": str, "ObservedAt": tm, "ExpiresAt": tm})
	gkaRAppFields(t, "AllocationRecord", app.AllocationRecord{}, map[string]reflect.Type{
		"Profile": str, "ObservedAt": tm, "ExpiresAt": tm, "EvidenceRefs": reflect.TypeOf([]string(nil)), "Workloads": reflect.TypeOf([]app.WorkloadRecord(nil)),
	})
	gkaRAppFields(t, "WorkloadRecord", app.WorkloadRecord{}, map[string]reflect.Type{
		"Namespace": str, "Name": str, "UID": str, "Container": str, "CreatedAt": tm, "DeletedAt": tm,
	})
	gkaRAppFields(t, "LiveBundleQuery", app.LiveBundleQuery{}, map[string]reflect.Type{
		"Node": reflect.TypeOf(fleet.NodeRef{}), "Policy": reflect.TypeOf(fleet.Policy{}), "Intents": reflect.TypeOf([]fleet.Intent(nil)), "Now": tm,
	})
	gkaRAppFields(t, "LiveBundleSet", app.LiveBundleSet{}, map[string]reflect.Type{"Devices": reflect.TypeOf([]app.LiveDeviceBundle(nil))})
	gkaRAppFields(t, "LiveDeviceBundle", app.LiveDeviceBundle{}, map[string]reflect.Type{
		"DeviceUID": str, "Bundle": reflect.TypeOf(fleet.AssessmentBundle{}), "Findings": reflect.TypeOf([]app.FindingRecord(nil)),
		"Evidence": reflect.TypeOf([]app.EvidenceRecord(nil)), "Allocation": reflect.TypeOf((*app.AllocationRecord)(nil)),
	})
	if app.ErrNoObservation == nil || app.ErrNoObservation.Error() != "app: no observation" {
		t.Errorf("GKA-033: ErrNoObservation = %v, want the sentinel \"app: no observation\"", app.ErrNoObservation)
	}
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

type gkaRAppCtxKey struct{}

// gkaRAppSource is a LiveBundleSource fake: mutex protected, scripted, recording.
type gkaRAppSource struct {
	mu      sync.Mutex
	next    func(ctx context.Context, q app.LiveBundleQuery) (app.LiveBundleSet, error)
	queries []app.LiveBundleQuery
	marked  int
}

func (s *gkaRAppSource) set(fn func(ctx context.Context, q app.LiveBundleQuery) (app.LiveBundleSet, error)) {
	s.mu.Lock()
	s.next = fn
	s.mu.Unlock()
}

func (s *gkaRAppSource) setSet(set app.LiveBundleSet, err error) {
	s.set(func(context.Context, app.LiveBundleQuery) (app.LiveBundleSet, error) { return set, err })
}

func (s *gkaRAppSource) NodeBundles(ctx context.Context, q app.LiveBundleQuery) (app.LiveBundleSet, error) {
	cp := q
	cp.Policy.RequiredCoverage = append([]fleet.CoverageRequirement(nil), q.Policy.RequiredCoverage...)
	cp.Intents = append([]fleet.Intent(nil), q.Intents...)
	s.mu.Lock()
	s.queries = append(s.queries, cp)
	if ctx.Value(gkaRAppCtxKey{}) == "marker" {
		s.marked++
	}
	fn := s.next
	s.mu.Unlock()
	if fn == nil {
		return app.LiveBundleSet{}, app.ErrNoObservation
	}
	return fn(ctx, q)
}

// gkaRAppFixture holds three evaluation instants (t1 30 s and t2 40 s after t0). The source bundles
// carry a different policy revision, request id, desired state and device
// reference than the request: the assessor must overwrite them (GKA-032(c)).
type gkaRAppFixture struct {
	t0, t1, t2 time.Time
	b0, b1, b2 fleet.AssessmentBundle // what the source returns (sequence 0, 1 and 2)
	r0, r1, r2 app.NodeAssessmentRequest
}

func gkaRAppNewFixture(t *testing.T) *gkaRAppFixture {
	t.Helper()
	t0 := fleetT0.Add(10 * time.Minute)
	t1 := t0.Add(30 * time.Second)
	t2 := t1.Add(10 * time.Second)
	make1 := func(at time.Time, seq uint64) (fleet.AssessmentBundle, app.NodeAssessmentRequest) {
		src := fleetBundle(t, at, seq, fleet.DesiredMaintenance)
		src.Policy.Revision = "source-policy"
		src.Intent.RequestID = "source-request"
		src.Intent.Device = fleet.DeviceRef{Name: "garbage", UID: "garbage-uid"}
		real := fleetBundle(t, at, seq, fleet.DesiredInService)
		pol := real.Policy
		pol.Revision = "req-policy"
		in := real.Intent
		in.RequestID = "req-request"
		return src, app.NodeAssessmentRequest{
			Node: in.Node, FleetUID: "fleet-uid", Policy: pol, Selection: fleet.SelectionComplete, Intents: []fleet.Intent{in}, Now: at,
		}
	}
	fx := &gkaRAppFixture{t0: t0, t1: t1, t2: t2}
	fx.b0, fx.r0 = make1(t0, 0)
	fx.b1, fx.r1 = make1(t1, 1)
	fx.b2, fx.r2 = make1(t2, 2)
	return fx
}

func gkaRAppFindings() []app.FindingRecord {
	return []app.FindingRecord{{ID: "finding-1", Type: "PCIE_LINK_WIDTH_DEGRADED", Severity: "warning", State: "active"}}
}

func gkaRAppEvidence(at time.Time) []app.EvidenceRecord {
	return []app.EvidenceRecord{
		{ID: "binding-ev", Summary: "binding observed", ObservedAt: at, ExpiresAt: at.Add(time.Minute)},
		{ID: "path-parent", Summary: "parent observed", ObservedAt: at, ExpiresAt: at.Add(time.Minute)},
	}
}

// gkaRAppDevices builds a source set with one LiveDeviceBundle per device UID; the
// bundle for a device other than device-a is device-a's bundle re-labelled.
func gkaRAppDevices(b fleet.AssessmentBundle, uids ...string) app.LiveBundleSet {
	var set app.LiveBundleSet
	for _, uid := range uids {
		set.Devices = append(set.Devices, app.LiveDeviceBundle{
			DeviceUID: uid, Bundle: b, Findings: gkaRAppFindings(), Evidence: gkaRAppEvidence(b.Snapshot.ObservedAt),
		})
	}
	return set
}

// gkaRAppTwoIntents adds device-b to a request (intents stay in device UID order).
func gkaRAppTwoIntents(req app.NodeAssessmentRequest) app.NodeAssessmentRequest {
	second := req.Intents[0]
	second.Device = fleet.DeviceRef{Name: "gpu-b", UID: "device-b"}
	req.Intents = []fleet.Intent{req.Intents[0], second}
	return req
}

// gkaRAppExpect computes what AssessNode must return for one bundle per intent by
// calling the S1 functions the spec names: Policy and Intent overwritten, previous
// looked up per device and node.
func gkaRAppExpect(t *testing.T, bundles []fleet.AssessmentBundle, req app.NodeAssessmentRequest, prevDev []*fleet.DeviceDecision, prevNode *fleet.NodeDecision, selection fleet.SelectionState) ([]fleet.DeviceDecision, fleet.NodeDecision) {
	t.Helper()
	var decs []fleet.DeviceDecision
	var aggs []fleet.DeviceAggregate
	for i, b := range bundles {
		b.Policy = req.Policy
		b.Intent = req.Intents[i]
		var prev *fleet.DeviceDecision
		if i < len(prevDev) {
			prev = prevDev[i]
		}
		d, err := fleet.EvaluateDevice(b, prev, req.Now)
		if err != nil {
			t.Fatalf("oracle: EvaluateDevice: %v", err)
		}
		decs = append(decs, d)
		aggs = append(aggs, fleet.DeviceAggregate{
			DeviceUID: req.Intents[i].Device.UID, NodeUID: req.Node.UID, Desired: req.Intents[i].Desired,
			MetadataGeneration: req.Intents[i].MetadataGeneration, Decision: d,
		})
	}
	nd, err := fleet.AggregateNode(fleet.AggregateNodeInput{Node: req.Node, FleetUID: req.FleetUID, Selection: selection, Devices: aggs}, prevNode, req.Now)
	if err != nil {
		t.Fatalf("oracle: AggregateNode: %v", err)
	}
	return decs, nd
}

func gkaRAppAssess(t *testing.T, a app.NodeAssessor, req app.NodeAssessmentRequest) app.NodeAssessment {
	t.Helper()
	got, err := a.AssessNode(context.Background(), req)
	if err != nil {
		t.Fatalf("AssessNode: %v", err)
	}
	return got
}

func gkaRAppIsZero(a app.NodeAssessment) bool {
	return len(a.Devices) == 0 && a.Node == nil && a.Observation == nil
}

func gkaRAppSameDecision(t *testing.T, what string, got, want fleet.DeviceDecision) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: decision differs from the S1 oracle\n got  %+v\n want %+v", what, got, want)
	}
}

func gkaRAppSameNode(t *testing.T, what string, got *fleet.NodeDecision, want fleet.NodeDecision) {
	t.Helper()
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("%s: node decision differs from the S1 oracle\n got  %+v\n want %+v", what, got, want)
	}
}

// gkaRAppTwoStep primes an assessor with call 1 and returns the oracle values of
// call 2 with and without the memory of call 1.
type gkaRAppTwoStep struct {
	first            fleet.DeviceDecision
	firstNode        fleet.NodeDecision
	withMem, noMem   fleet.DeviceDecision
	withMemN, noMemN fleet.NodeDecision
}

func gkaRAppPrepare(t *testing.T, fx *gkaRAppFixture) *gkaRAppTwoStep {
	t.Helper()
	d1, n1 := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b0}, fx.r0, nil, nil, fleet.SelectionComplete)
	withMem, withMemN := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b1}, fx.r1, []*fleet.DeviceDecision{&d1[0]}, &n1, fleet.SelectionComplete)
	noMem, noMemN := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b1}, fx.r1, nil, nil, fleet.SelectionComplete)
	if reflect.DeepEqual(withMem[0], noMem[0]) {
		t.Fatalf("test premise: with and without the previous decision, call 2 evaluates identically (%+v); the memory would be unobservable", withMem[0].Phase)
	}
	return &gkaRAppTwoStep{first: d1[0], firstNode: n1, withMem: withMem[0], noMem: noMem[0], withMemN: withMemN, noMemN: noMemN}
}

// ---------------------------------------------------------------------------
// GKA-032 (c)(d)(e)(g)
// ---------------------------------------------------------------------------

func TestGKA032_OverwritesPolicyAndIntentAndMatchesTheS1Oracle(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	src := &gkaRAppSource{}
	src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
	a := app.NewLiveAssessor(src)
	before := gkaRCloneRequest(fx.r0)

	ctx := context.WithValue(context.Background(), gkaRAppCtxKey{}, "marker")
	got, err := a.AssessNode(ctx, fx.r0)
	if err != nil {
		t.Fatalf("GKA-032: AssessNode = %v", err)
	}
	if len(got.Devices) != 1 || got.Node == nil || got.Observation == nil {
		t.Fatalf("GKA-032: assessment = %+v, want one device, a node decision and an observation", got)
	}
	decs, nd := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b0}, fx.r0, nil, nil, fleet.SelectionComplete)
	gkaRAppSameDecision(t, "GKA-032(c)(d)", got.Devices[0].Decision, decs[0])
	gkaRAppSameNode(t, "GKA-032(e)", got.Node, nd)
	dec := got.Devices[0].Decision
	if dec.Desired != fleet.DesiredInService || dec.PolicyRevision != "req-policy" || dec.RequestID != "req-request" || dec.DeviceUID != "device-a" {
		t.Errorf("GKA-032(c): decision Desired=%v PolicyRevision=%q RequestID=%q DeviceUID=%q, want the request's values (source values are discarded)", dec.Desired, dec.PolicyRevision, dec.RequestID, dec.DeviceUID)
	}
	if want := (app.NodeObservation{GraphRevision: fx.b0.GraphRevision, Completeness: fx.b0.Snapshot.Completeness}); *got.Observation != want {
		t.Errorf("GKA-032(f): observation = %+v, want the common bundle values %+v", *got.Observation, want)
	}
	if !reflect.DeepEqual(got.Devices[0].Findings, gkaRAppFindings()) || !reflect.DeepEqual(got.Devices[0].Evidence, gkaRAppEvidence(fx.b0.Snapshot.ObservedAt)) {
		t.Errorf("GKA-032(g): records = %+v / %+v, want the source's values", got.Devices[0].Findings, got.Devices[0].Evidence)
	}

	// GKA-032(a) + section 2.3: the query carries the request's node, policy, intents and time.
	src.mu.Lock()
	q, marked := src.queries[0], src.marked
	src.mu.Unlock()
	if q.Node != fx.r0.Node || !reflect.DeepEqual(q.Policy, fx.r0.Policy) || !reflect.DeepEqual(q.Intents, fx.r0.Intents) || !q.Now.Equal(fx.r0.Now) {
		t.Errorf("GKA-032(a): source query = %+v, want the request's Node, Policy, Intents and Now", q)
	}
	if marked != 1 {
		t.Errorf("GKA-032(a): the caller's context did not reach the source")
	}
	// The assessor does not touch the request.
	if !reflect.DeepEqual(before, gkaRCloneRequest(fx.r0)) {
		t.Errorf("GKA-032/034: AssessNode modified its request")
	}
}

func gkaRCloneRequest(r app.NodeAssessmentRequest) app.NodeAssessmentRequest {
	r.Policy.RequiredCoverage = append([]fleet.CoverageRequirement(nil), r.Policy.RequiredCoverage...)
	r.Intents = append([]fleet.Intent(nil), r.Intents...)
	return r
}

// ---------------------------------------------------------------------------
// GKA-032 previous memory, ForgetNode
// ---------------------------------------------------------------------------

func TestGKA032_PreviousMemoryFeedsTheNextCall(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	p := gkaRAppPrepare(t, fx)
	src := &gkaRAppSource{}
	a := app.NewLiveAssessor(src)

	src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
	got1 := gkaRAppAssess(t, a, fx.r0)
	gkaRAppSameDecision(t, "call 1", got1.Devices[0].Decision, p.first)
	gkaRAppSameNode(t, "call 1", got1.Node, p.firstNode)

	src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
	got2 := gkaRAppAssess(t, a, fx.r1)
	gkaRAppSameDecision(t, "GKA-032(d): call 2 must use the decision of call 1 as previous", got2.Devices[0].Decision, p.withMem)
	gkaRAppSameNode(t, "GKA-032(e): call 2 must use the node decision of call 1 as previous", got2.Node, p.withMemN)
}

func TestGKA032_ForgetNodeDropsTheContinuityAndIsIdempotent(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	p := gkaRAppPrepare(t, fx)
	prime := func(t *testing.T) (app.NodeAssessor, *gkaRAppSource) {
		src := &gkaRAppSource{}
		a := app.NewLiveAssessor(src)
		src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
		gkaRAppAssess(t, a, fx.r0)
		src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
		return a, src
	}

	t.Run("forgotten node starts without continuity", func(t *testing.T) {
		a, _ := prime(t)
		a.ForgetNode(fx.r0.Node.UID)
		got := gkaRAppAssess(t, a, fx.r1)
		gkaRAppSameDecision(t, "GKA-032(h)", got.Devices[0].Decision, p.noMem)
		gkaRAppSameNode(t, "GKA-032(h)", got.Node, p.noMemN)
	})
	t.Run("forgetting twice, an unknown UID, or the empty UID is safe", func(t *testing.T) {
		a, _ := prime(t)
		for _, uid := range []string{fx.r0.Node.UID, fx.r0.Node.UID, "never-seen", ""} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("GKA-030: ForgetNode(%q) panicked: %v", uid, r)
					}
				}()
				a.ForgetNode(uid)
			}()
		}
		got := gkaRAppAssess(t, a, fx.r1)
		gkaRAppSameDecision(t, "after repeated ForgetNode", got.Devices[0].Decision, p.noMem)
	})
	t.Run("forgetting another node keeps this node's memory", func(t *testing.T) {
		a, _ := prime(t)
		a.ForgetNode("some-other-node")
		got := gkaRAppAssess(t, a, fx.r1)
		gkaRAppSameDecision(t, "GKA-032(h): other UID", got.Devices[0].Decision, p.withMem)
		gkaRAppSameNode(t, "GKA-032(h): other UID", got.Node, p.withMemN)
	})
	t.Run("forgetting on a fresh assessor is a no-op", func(t *testing.T) {
		src := &gkaRAppSource{}
		a := app.NewLiveAssessor(src)
		a.ForgetNode(fx.r0.Node.UID)
		src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
		got := gkaRAppAssess(t, a, fx.r1)
		gkaRAppSameDecision(t, "fresh assessor", got.Devices[0].Decision, p.noMem)
	})
}

// ---------------------------------------------------------------------------
// GKA-031 / GKA-032(a): no decision
// ---------------------------------------------------------------------------

func TestGKA031_032_NoObservationClearsTheNodeMemoryAndReturnsTheZeroAssessment(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	p := gkaRAppPrepare(t, fx)
	variants := []struct {
		name string
		set  app.LiveBundleSet
		err  error
	}{
		{"ErrNoObservation", app.LiveBundleSet{}, app.ErrNoObservation},
		{"wrapped ErrNoObservation", app.LiveBundleSet{}, fmt.Errorf("source says: %w", app.ErrNoObservation)},
		{"empty Devices and nil error", app.LiveBundleSet{}, nil},
		{"empty non-nil Devices slice", app.LiveBundleSet{Devices: []app.LiveDeviceBundle{}}, nil},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			src := &gkaRAppSource{}
			a := app.NewLiveAssessor(src)
			src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
			gkaRAppAssess(t, a, fx.r0)

			src.setSet(v.set, v.err)
			got, err := a.AssessNode(context.Background(), fx.r1)
			if err != nil {
				t.Fatalf("GKA-032(a): AssessNode = %v, want nil error for %s", err, v.name)
			}
			if !gkaRAppIsZero(got) {
				t.Errorf("GKA-031/032(a): assessment = %+v, want the zero NodeAssessment", got)
			}

			src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
			after := gkaRAppAssess(t, a, fx.r1)
			gkaRAppSameDecision(t, "GKA-032(a): the node's memory is erased by "+v.name, after.Devices[0].Decision, p.noMem)
			gkaRAppSameNode(t, "GKA-032(a)", after.Node, p.noMemN)
		})
	}
}

func TestGKA032_OtherSourceErrorsAreWrappedAndKeepTheMemory(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	p := gkaRAppPrepare(t, fx)
	src := &gkaRAppSource{}
	a := app.NewLiveAssessor(src)
	src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
	gkaRAppAssess(t, a, fx.r0)

	boom := errors.New("source exploded")
	src.setSet(app.LiveBundleSet{}, boom)
	if _, err := a.AssessNode(context.Background(), fx.r1); !errors.Is(err, boom) || errors.Is(err, app.ErrNoObservation) {
		t.Fatalf("GKA-032(a): err = %v, want a %%w wrap of the source error and not ErrNoObservation", err)
	}
	src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
	got := gkaRAppAssess(t, a, fx.r1)
	gkaRAppSameDecision(t, "GKA-032(a)(f): memory must be unchanged by a source error", got.Devices[0].Decision, p.withMem)
	gkaRAppSameNode(t, "GKA-032(a)(f)", got.Node, p.withMemN)
}

// ---------------------------------------------------------------------------
// GKA-032 (b)(f): invalid bundle sets
// ---------------------------------------------------------------------------

func TestGKA032_InvalidBundleSetsFailWithErrInvalidInputAndKeepTheMemory(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	p := gkaRAppPrepare(t, fx)
	two := gkaRAppTwoIntents(fx.r1)
	withDeviceB := func(edit func(*fleet.AssessmentBundle)) app.LiveBundleSet {
		set := gkaRAppDevices(fx.b1, "device-a", "device-b")
		edit(&set.Devices[1].Bundle)
		return set
	}
	cases := []struct {
		name string
		req  app.NodeAssessmentRequest
		set  app.LiveBundleSet
	}{
		{"device not among the request intents", func() app.NodeAssessmentRequest {
			r := fx.r1
			r.Intents = append([]fleet.Intent(nil), fx.r1.Intents...)
			r.Intents[0].Device = fleet.DeviceRef{Name: "gpu-z", UID: "device-z"}
			return r
		}(), gkaRAppDevices(fx.b1, "device-a")},
		{"request without intents", func() app.NodeAssessmentRequest { r := fx.r1; r.Intents = nil; return r }(), gkaRAppDevices(fx.b1, "device-a")},
		{"duplicate device bundle", fx.r1, gkaRAppDevices(fx.b1, "device-a", "device-a")},
		{"snapshot node UID differs from the request node", fx.r1, func() app.LiveBundleSet {
			set := gkaRAppDevices(fx.b1, "device-a")
			set.Devices[0].Bundle.Snapshot.NodeUID = "another-node-uid"
			return set
		}()},
		{"graph revision differs between devices", two, withDeviceB(func(b *fleet.AssessmentBundle) { b.GraphRevision = "bundle-other" })},
		{"completeness differs between devices", two, withDeviceB(func(b *fleet.AssessmentBundle) { b.Snapshot.Completeness = fleet.CompletenessPartial })},
		{"session differs between devices", two, withDeviceB(func(b *fleet.AssessmentBundle) { b.Snapshot.Session = fx.b1.Snapshot.Session + 1 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &gkaRAppSource{}
			a := app.NewLiveAssessor(src)
			src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
			gkaRAppAssess(t, a, fx.r0)

			src.setSet(tc.set, nil)
			if _, err := a.AssessNode(context.Background(), tc.req); !errors.Is(err, fleet.ErrInvalidInput) {
				t.Fatalf("GKA-032(b): err = %v, want errors.Is(err, fleet.ErrInvalidInput)", err)
			}
			src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
			got := gkaRAppAssess(t, a, fx.r1)
			gkaRAppSameDecision(t, "GKA-032(f): a failed call must not change the memory", got.Devices[0].Decision, p.withMem)
			gkaRAppSameNode(t, "GKA-032(f)", got.Node, p.withMemN)
		})
	}
}

// ---------------------------------------------------------------------------
// GKA-031 / 032 (e): partial decisions
// ---------------------------------------------------------------------------

func TestGKA031_032_FewerDecisionsThanIntentsAggregateAsPartial(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	two := gkaRAppTwoIntents(fx.r0)
	bundles2 := []fleet.AssessmentBundle{fx.b0, fx.b0}

	t.Run("complete selection with a subset of decisions is Partial", func(t *testing.T) {
		src := &gkaRAppSource{}
		src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
		got := gkaRAppAssess(t, app.NewLiveAssessor(src), two)
		if len(got.Devices) != 1 || got.Node == nil {
			t.Fatalf("GKA-031: assessment = %+v, want the decision of device-a and a node decision", got)
		}
		decs, nd := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b0}, two, nil, nil, fleet.SelectionPartial)
		gkaRAppSameDecision(t, "device-a", got.Devices[0].Decision, decs[0])
		gkaRAppSameNode(t, "GKA-032(e): SelectionPartial when decisions < intents", got.Node, nd)
		if got.Node.Selection != fleet.SelectionPartial || got.Node.DeviceCount != 1 || got.Node.Eligibility != fleet.EligibilityUnknown || got.Node.Qualification != fleet.QualificationUnknown {
			t.Errorf("GKA-032(e)/085(g): node = %+v, want Partial with DeviceCount 1 and Unknown eligibility and qualification", got.Node)
		}
	})
	t.Run("complete selection with all decisions is Complete", func(t *testing.T) {
		src := &gkaRAppSource{}
		src.setSet(gkaRAppDevices(fx.b0, "device-a", "device-b"), nil)
		got := gkaRAppAssess(t, app.NewLiveAssessor(src), two)
		decs, nd := gkaRAppExpect(t, bundles2, two, nil, nil, fleet.SelectionComplete)
		if len(got.Devices) != 2 {
			t.Fatalf("GKA-032: %d device assessments, want 2", len(got.Devices))
		}
		gkaRAppSameDecision(t, "device-a", got.Devices[0].Decision, decs[0])
		gkaRAppSameDecision(t, "device-b", got.Devices[1].Decision, decs[1])
		gkaRAppSameNode(t, "GKA-032(e): Complete", got.Node, nd)
		if got.Node.Selection != fleet.SelectionComplete || got.Node.DeviceCount != 2 {
			t.Errorf("GKA-032(e): node = %+v, want Complete with DeviceCount 2", got.Node)
		}
	})
	t.Run("a Partial request selection stays Partial", func(t *testing.T) {
		req := two
		req.Selection = fleet.SelectionPartial
		src := &gkaRAppSource{}
		src.setSet(gkaRAppDevices(fx.b0, "device-a", "device-b"), nil)
		got := gkaRAppAssess(t, app.NewLiveAssessor(src), req)
		_, nd := gkaRAppExpect(t, bundles2, req, nil, nil, fleet.SelectionPartial)
		gkaRAppSameNode(t, "GKA-032(e): request Selection Partial", got.Node, nd)
		if got.Node.Selection != fleet.SelectionPartial {
			t.Errorf("GKA-032(e): node selection = %v, want Partial", got.Node.Selection)
		}
	})
}

// ---------------------------------------------------------------------------
// GKA-032 (g): order and copies
// ---------------------------------------------------------------------------

func TestGKA032_DevicesAreSortedByUIDAndRecordsAreIndependentCopies(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	two := gkaRAppTwoIntents(fx.r0)
	src := &gkaRAppSource{}
	set := gkaRAppDevices(fx.b0, "device-b", "device-a") // reversed on purpose
	alloc := &app.AllocationRecord{
		Profile: "profile-x", ObservedAt: fx.t0, ExpiresAt: fx.t0.Add(time.Minute), EvidenceRefs: []string{"e1", "e2"},
		Workloads: []app.WorkloadRecord{{Namespace: "ns", Name: "pod", UID: "uid-1", Container: "c", CreatedAt: fx.t0}},
	}
	set.Devices[1].Allocation = alloc
	src.setSet(set, nil)
	a := app.NewLiveAssessor(src)
	got := gkaRAppAssess(t, a, two)
	if len(got.Devices) != 2 || got.Devices[0].Decision.DeviceUID != "device-a" || got.Devices[1].Decision.DeviceUID != "device-b" {
		t.Fatalf("GKA-032(g): devices are not in device UID order: %+v", got.Devices)
	}
	wantFindings, wantEvidence := gkaRAppFindings(), gkaRAppEvidence(fx.b0.Snapshot.ObservedAt)
	if !reflect.DeepEqual(got.Devices[0].Findings, wantFindings) || !reflect.DeepEqual(got.Devices[0].Evidence, wantEvidence) {
		t.Errorf("GKA-032(g): records = %+v / %+v, want the source's values", got.Devices[0].Findings, got.Devices[0].Evidence)
	}

	// Mutating the source's storage afterwards must not change what was returned.
	set.Devices[1].Findings[0].ID = "mutated-in-source"
	set.Devices[1].Evidence[0].Summary = "mutated-in-source"
	alloc.EvidenceRefs[0] = "mutated-in-source"
	alloc.Workloads[0].Name = "mutated-in-source"
	if !reflect.DeepEqual(got.Devices[0].Findings, wantFindings) || !reflect.DeepEqual(got.Devices[0].Evidence, wantEvidence) {
		t.Errorf("GKA-032(g): returned records share storage with the source")
	}
	if out := got.Devices[0].Allocation; out != nil {
		if out == alloc {
			t.Errorf("GKA-032(g): the returned Allocation is the source's pointer, not a copy")
		} else if out.EvidenceRefs[0] != "e1" || out.Workloads[0].Name != "pod" {
			t.Errorf("GKA-032(g): the returned Allocation shares slices with the source: %+v", out)
		}
	}
	// Mutating the returned records must not corrupt the assessor or later results.
	got.Devices[0].Findings[0].ID = "mutated-by-caller"
	got.Devices[0].Evidence[0].ID = "mutated-by-caller"
	fresh := gkaRAppDevices(fx.b0, "device-a", "device-b")
	src.setSet(fresh, nil)
	again := gkaRAppAssess(t, a, two)
	if !reflect.DeepEqual(again.Devices[0].Findings, wantFindings) {
		t.Errorf("GKA-032(g): a caller mutation of an earlier result leaked into a later result: %+v", again.Devices[0].Findings)
	}
}

// ---------------------------------------------------------------------------
// GKA-033
// ---------------------------------------------------------------------------

func TestGKA033_NoObservationSourceAlwaysReportsErrNoObservation(t *testing.T) {
	src := app.NewNoObservationSource()
	if src == nil {
		t.Fatal("GKA-033: NewNoObservationSource returned nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	fx := gkaRAppNewFixture(t)
	queries := []app.LiveBundleQuery{
		{},
		{Node: fx.r0.Node, Policy: fx.r0.Policy, Intents: fx.r0.Intents, Now: fx.t0},
		{Intents: append(append([]fleet.Intent(nil), fx.r0.Intents...), fx.r1.Intents...)},
	}
	for i, q := range queries {
		for _, ctx := range []context.Context{context.Background(), cancelled} {
			set, err := src.NodeBundles(ctx, q)
			if !errors.Is(err, app.ErrNoObservation) || len(set.Devices) != 0 {
				t.Errorf("GKA-033: query %d: NodeBundles = (%+v, %v), want an empty set and ErrNoObservation", i, set, err)
			}
		}
	}
	// Repeated calls stay the same (no state).
	for i := 0; i < 3; i++ {
		if set, err := src.NodeBundles(context.Background(), queries[1]); !errors.Is(err, app.ErrNoObservation) || len(set.Devices) != 0 {
			t.Errorf("GKA-033: call %d = (%+v, %v)", i, set, err)
		}
	}
}

func TestGKA031_033_LiveAssessorOverTheNoObservationSourceReturnsTheZeroAssessment(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	a := app.NewLiveAssessor(app.NewNoObservationSource())
	two := gkaRAppTwoIntents(fx.r0)
	partial := fx.r0
	partial.Selection = fleet.SelectionPartial
	for name, req := range map[string]app.NodeAssessmentRequest{
		"one device": fx.r0, "two devices": two, "partial selection": partial, "zero request": {},
	} {
		got, err := a.AssessNode(context.Background(), req)
		if err != nil || !gkaRAppIsZero(got) {
			t.Errorf("GKA-031/033 (%s): AssessNode = (%+v, %v), want the zero NodeAssessment and a nil error", name, got, err)
		}
	}
	a.ForgetNode(fx.r0.Node.UID) // still safe with nothing remembered
}

// ---------------------------------------------------------------------------
// GKA-030: goroutine safety
// ---------------------------------------------------------------------------

// Concurrent AssessNode and ForgetNode on one assessor: race free (meaningful under
// -race) and every returned assessment is structurally valid.
func TestGKA030_LiveAssessorIsSafeForConcurrentUse(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	src := &gkaRAppSource{}
	src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
	a := app.NewLiveAssessor(src)
	const workers, iterations = 8, 30
	var wg sync.WaitGroup
	errs := make(chan string, workers*iterations)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (w + i) % 3 {
				case 0:
					a.ForgetNode(fx.r1.Node.UID)
				default:
					got, err := a.AssessNode(context.Background(), fx.r1)
					if err != nil {
						errs <- fmt.Sprintf("AssessNode: %v", err)
						continue
					}
					if len(got.Devices) != 1 || got.Node == nil || got.Observation == nil {
						errs <- fmt.Sprintf("incomplete assessment: %+v", got)
						continue
					}
					if err := got.Devices[0].Decision.Validate(); err != nil {
						errs <- fmt.Sprintf("invalid decision: %v", err)
					}
					if err := got.Node.Validate(); err != nil {
						errs <- fmt.Sprintf("invalid node decision: %v", err)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("GKA-030: %s", e)
	}
}

// ---------------------------------------------------------------------------
// GKA-032 (e)(f): scope of the memory replacement and the prevNode key
// ---------------------------------------------------------------------------

// After a successful call the node's memory is exactly that call's decisions: a
// device that dropped out (the request no longer lists it, or the source returned
// no bundle for it) has no previous when it comes back, while the device that stayed
// keeps its continuity.
func TestGKA032_MemoryIsReplacedNotMergedBySuccessfulCalls(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	two0, two2 := gkaRAppTwoIntents(fx.r0), gkaRAppTwoIntents(fx.r2)
	cases := []struct {
		name string
		req2 app.NodeAssessmentRequest
		sel2 fleet.SelectionState // the selection the assessor must use for call 2
	}{
		{"the second request lists fewer devices", fx.r1, fleet.SelectionComplete},
		{"the source omits a requested device", gkaRAppTwoIntents(fx.r1), fleet.SelectionPartial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &gkaRAppSource{}
			a := app.NewLiveAssessor(src)

			// Call 1: both devices, so the node memory holds device-a and device-b.
			src.setSet(gkaRAppDevices(fx.b0, "device-a", "device-b"), nil)
			gkaRAppAssess(t, a, two0)
			decs1, n1 := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b0, fx.b0}, two0, nil, nil, fleet.SelectionComplete)

			// Call 2: only device-a is decided; the memory becomes device-a alone.
			src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
			got2 := gkaRAppAssess(t, a, tc.req2)
			decs2, n2 := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b1}, tc.req2, []*fleet.DeviceDecision{&decs1[0]}, &n1, tc.sel2)
			if len(got2.Devices) != 1 {
				t.Fatalf("GKA-032: call 2 returned %d device assessments, want 1", len(got2.Devices))
			}
			gkaRAppSameDecision(t, "call 2 device-a", got2.Devices[0].Decision, decs2[0])
			gkaRAppSameNode(t, "call 2", got2.Node, n2)

			// Call 3: device-b returns. Its previous is gone (cold start); device-a continues from call 2.
			src.setSet(gkaRAppDevices(fx.b2, "device-a", "device-b"), nil)
			got3 := gkaRAppAssess(t, a, two2)
			cold, n3 := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b2, fx.b2}, two2, []*fleet.DeviceDecision{&decs2[0], nil}, &n2, fleet.SelectionComplete)
			stale, _ := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b2, fx.b2}, two2, []*fleet.DeviceDecision{&decs2[0], &decs1[1]}, &n2, fleet.SelectionComplete)
			if reflect.DeepEqual(cold[1], stale[1]) {
				t.Fatalf("test premise: device-b evaluates identically with and without its call-1 decision (phase %v); the replacement scope would be unobservable", cold[1].Phase)
			}
			if len(got3.Devices) != 2 {
				t.Fatalf("GKA-032: call 3 returned %d device assessments, want 2", len(got3.Devices))
			}
			gkaRAppSameDecision(t, "GKA-032(f): device-a keeps its call-2 continuity", got3.Devices[0].Decision, cold[0])
			gkaRAppSameDecision(t, "GKA-032(f): device-b, absent from call 2, starts cold", got3.Devices[1].Decision, cold[1])
			gkaRAppSameNode(t, "call 3", got3.Node, n3)
		})
	}
}

// prevNode is looked up by (node UID, fleet UID): a call for another fleet on the
// same node must not use the node decision remembered for the first fleet, while
// the device continuity (keyed by node and device) still applies.
func TestGKA032_PreviousNodeDecisionIsKeyedByFleetUID(t *testing.T) {
	fx := gkaRAppNewFixture(t)
	src := &gkaRAppSource{}
	a := app.NewLiveAssessor(src)
	src.setSet(gkaRAppDevices(fx.b0, "device-a"), nil)
	gkaRAppAssess(t, a, fx.r0)
	decs1, _ := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b0}, fx.r0, nil, nil, fleet.SelectionComplete)

	other := fx.r1
	other.FleetUID = "another-fleet-uid"
	src.setSet(gkaRAppDevices(fx.b1, "device-a"), nil)
	got := gkaRAppAssess(t, a, other) // an error here means the other fleet's call consumed fleet-uid's node decision
	decs2, n2 := gkaRAppExpect(t, []fleet.AssessmentBundle{fx.b1}, other, []*fleet.DeviceDecision{&decs1[0]}, nil, fleet.SelectionComplete)
	gkaRAppSameDecision(t, "GKA-032(d): device continuity is keyed by node and device, not by fleet", got.Devices[0].Decision, decs2[0])
	gkaRAppSameNode(t, "GKA-032(e): no previous node decision for a fleet never seen on this node", got.Node, n2)
	if got.Node.FleetUID != "another-fleet-uid" {
		t.Errorf("GKA-081: node decision FleetUID = %q, want the request's", got.Node.FleetUID)
	}
	// The first fleet keeps working: a call for it after the other fleet's call is still valid.
	src.setSet(gkaRAppDevices(fx.b2, "device-a"), nil)
	again := gkaRAppAssess(t, a, fx.r2)
	if again.Node == nil || again.Node.FleetUID != fx.r2.FleetUID || again.Node.NodeUID != fx.r2.Node.UID {
		t.Errorf("GKA-032(e): call for the first fleet returned node decision %+v", again.Node)
	}
}
