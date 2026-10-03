package liveingestenv_test

// The Kubernetes session store and node directory (GLI-031, GLI-032, GLI-113)
// against a real API server: the minimal and the preserving status write, the
// resourceVersion-conditional PUT and its conflict rule, the deterministic
// transport scenarios of GLI-124 (g) (1)-(5), concurrency, leadership and
// cancellation discipline, error mapping without response bodies, and the
// adapter's option validation. Scenario group GLI-124 (g).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/ingestadapter"
)

// glieSessionEnv is a Node, its NodePathState (spec only, as S3a creates it), a
// Lease held by the adapter's controller ID and a running adapter that has
// become the leader. No S3a controller runs.
type glieSessionEnv struct {
	E      *glieEnvT
	Node   *corev1.Node
	Name   string
	Ad     *glieAdapter
	CtlID  string
	Lease  string
	Status string // "/nodepathstates/<name>/status"
}

func glieNewSessionEnv(t *testing.T, e *glieEnvT, mutate func(*glieAdapterOpts)) *glieSessionEnv {
	t.Helper()
	s := &glieSessionEnv{E: e, CtlID: glieName(t, "ctl"), Lease: glieName(t, "lease")}
	s.Node = glieNode(t, e, glieName(t, "node"), nil)
	s.Name = s.Node.Name
	glieNodePathState(t, e, s.Node)
	glieSeedLease(t, e, s.Lease, s.CtlID)
	o := glieAdapterOpts{ControllerID: s.CtlID, LeaseName: s.Lease}
	if mutate != nil {
		mutate(&o)
	}
	s.Ad = glieNewAdapter(t, e, o)
	if !o.NoPoll {
		s.Ad.WaitLeading(t, true, 10*time.Second)
	}
	s.Status = "/nodepathstates/" + s.Name + "/status"
	return s
}

func (s *glieSessionEnv) alloc(t testing.TB, after int64) (int64, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return s.Ad.Alloc(ctx, s.Node, after)
}

func (s *glieSessionEnv) mustAlloc(t testing.TB, after int64) int64 {
	t.Helper()
	got, err := s.alloc(t, after)
	if err != nil {
		t.Fatalf("AllocateSession(after=%d): %v", after, err)
	}
	return got
}

func (s *glieSessionEnv) stored(t testing.TB) *int64 {
	t.Helper()
	nps, ok := glieGetNPS(s.E, s.Name)
	if !ok {
		t.Fatalf("NodePathState %s does not exist", s.Name)
	}
	return nps.Status.CollectorSession
}

func glieMinimalStatus(nps *v1alpha1.NodePathState, session *int64) v1alpha1.NodePathStateStatus {
	return v1alpha1.NodePathStateStatus{
		ObservedGeneration: nps.Generation, NodeRef: nps.Spec.NodeRef, EvidenceCompleteness: v1alpha1.SnapshotCompletenessUnknown,
		DeviceSummaries: []v1alpha1.DeviceSummary{}, Conditions: []metav1.Condition{}, CollectorSession: session,
	}
}

func glieInt64(v int64) *int64 { return &v }

// glieRichStatus is a status S3a could have published: complete evidence, a
// graph revision, one device summary and one condition.
func glieRichStatus(nps *v1alpha1.NodePathState, session *int64) v1alpha1.NodePathStateStatus {
	return v1alpha1.NodePathStateStatus{
		ObservedGeneration: nps.Generation, NodeRef: nps.Spec.NodeRef,
		GraphRevision:        glieStr("7:2:" + strings.Repeat("c", 64)),
		EvidenceCompleteness: v1alpha1.SnapshotCompletenessComplete,
		DeviceSummaries: []v1alpha1.DeviceSummary{{
			Name: "gpu-a", UID: "gpu-a-uid", DesiredState: "InService", ObservedGeneration: 1, Qualification: "Qualified", LifecyclePhase: "Ready",
		}},
		Conditions: []metav1.Condition{{
			Type: "NodeEligible", Status: "True", Reason: "Ready", Message: "eligibility=Eligible qualification=Qualified",
			ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		}},
		CollectorSession: session,
	}
}

func glieHasStatusManager(nps *v1alpha1.NodePathState, manager string) bool {
	for _, m := range nps.ManagedFields {
		if m.Manager == manager && m.Operation == metav1.ManagedFieldsOperationUpdate && m.Subresource == "status" {
			return true
		}
	}
	return false
}

// glieVerbs is the set of (verb resource) pairs of the recorded requests,
// discovery excluded (GKA-175 (3)).
func glieVerbs(entries []glieRecEntry) []string {
	seen := map[string]bool{}
	for _, en := range entries {
		verb, res, discovery := glieClassify(en.Method, en.Path, en.Watch)
		if discovery {
			continue
		}
		seen[verb+" "+res] = true
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- GLI-032 (b): the status that is written

// GLI-032 (b): on an empty status the minimal status that passes the schema and
// CEL is written - observedGeneration = metadata.generation, nodeRef = spec.nodeRef,
// evidenceCompleteness Unknown, deviceSummaries [] and conditions [] (never null),
// collectorSession = session, graphRevision omitted - by a PUT of the whole object
// conditional on the resourceVersion it read, under the manager dpa-nodepath-status.
func TestGLI032_EmptyStatusGetsTheMinimalStatus(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	before := glieMustNPS(t, e, s.Name)
	if before.Status.NodeRef.Name != "" || before.Status.CollectorSession != nil {
		t.Fatalf("fixture: the NodePathState must start with an empty status, got %+v", before.Status)
	}
	session := s.mustAlloc(t, 0)
	if session < 1 {
		t.Fatalf("GLI-031: AllocateSession returned %d, want a positive session", session)
	}
	got := glieMustNPS(t, e, s.Name)
	if got.Status.ObservedGeneration != got.Generation || got.Generation < 1 {
		t.Errorf("GLI-032 (b): observedGeneration = %d, want metadata.generation %d", got.Status.ObservedGeneration, got.Generation)
	}
	if got.Status.NodeRef != got.Spec.NodeRef {
		t.Errorf("GLI-032 (b): status.nodeRef = %+v, want spec.nodeRef %+v", got.Status.NodeRef, got.Spec.NodeRef)
	}
	if got.Status.EvidenceCompleteness != v1alpha1.SnapshotCompletenessUnknown {
		t.Errorf("GLI-032 (b): evidenceCompleteness = %q, want Unknown", got.Status.EvidenceCompleteness)
	}
	if got.Status.CollectorSession == nil || *got.Status.CollectorSession != session {
		t.Errorf("GLI-032 (b): collectorSession = %v, want %d", got.Status.CollectorSession, session)
	}
	if got.Status.GraphRevision != nil {
		t.Errorf("GLI-032 (b): graphRevision = %q, want it omitted", *got.Status.GraphRevision)
	}
	raw := glieRawNPSStatus(t, e, s.Name)
	for _, k := range []string{"deviceSummaries", "conditions"} {
		v, ok := raw[k]
		list, isList := v.([]any)
		if !ok || !isList || len(list) != 0 {
			t.Errorf("GLI-032 (b)/GKA-013: stored status.%s = %#v, want the empty list [] (never null)", k, v)
		}
	}
	if !glieHasStatusManager(got, "dpa-nodepath-status") {
		t.Errorf("GLI-032 (b)/GFL-075: managedFields has no dpa-nodepath-status Update entry on status: %v", got.ManagedFields)
	}
	puts := s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)
	if len(puts) != 1 {
		t.Fatalf("GLI-032: %d PUT(s) to %s, want exactly 1 (no patch, no apply)", len(puts), s.Status)
	}
	var body map[string]any
	if err := json.Unmarshal(puts[0].Body, &body); err != nil {
		t.Fatalf("the PUT body is not JSON: %v", err)
	}
	md, _ := body["metadata"].(map[string]any)
	if rv, _ := md["resourceVersion"].(string); rv == "" {
		t.Errorf("GLI-032 (d) I1: the PUT body carries no resourceVersion (the write must be conditional on the object that was read)")
	}
	st, _ := body["status"].(map[string]any)
	for _, k := range []string{"deviceSummaries", "conditions"} {
		if l, ok := st[k].([]any); !ok || len(l) != 0 {
			t.Errorf("GLI-032 (b): the PUT body has status.%s = %#v, want [] (null serialization is forbidden)", k, st[k])
		}
	}
	for _, e := range s.Ad.Rec.Entries() {
		if e.Method == http.MethodPatch || (e.Method == http.MethodPut && !strings.HasSuffix(e.Path, s.Status)) || e.Method == http.MethodPost || e.Method == http.MethodDelete {
			t.Errorf("GLI-032 (d) I1/GLI-032 (h): unexpected write %s (only a PUT to nodepathstates/<n>/status is allowed)", e)
		}
	}
	if v := s.Ad.Rec.Violations(); len(v) > 0 {
		t.Errorf("harness: %v", v)
	}
}

// GLI-032 (b)(d) I3: on a status that is not empty every other field is written back
// as it was read and only collectorSession changes; fields owned by another
// manager (and that manager's managedFields entry) survive.
func TestGLI032_NonEmptyStatusIsPreservedExceptTheSession(t *testing.T) {
	for _, c := range []struct {
		name    string
		session *int64
	}{
		{"with a previous session", glieInt64(7)},
		{"without a session", nil},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			e := glieEnv(t)
			s := glieNewSessionEnv(t, e, nil)
			glieSeedStatus(t, e, s.Name, "glie-foreign", func(st *v1alpha1.NodePathStateStatus) {
				nps := glieMustNPS(t, e, s.Name)
				*st = glieRichStatus(nps, c.session)
			})
			before := glieMustNPS(t, e, s.Name)
			session := s.mustAlloc(t, 0)
			if c.session != nil && session <= *c.session {
				t.Errorf("GLI-031: session %d is not greater than the stored %d", session, *c.session)
			}
			after := glieMustNPS(t, e, s.Name)
			if after.Status.CollectorSession == nil || *after.Status.CollectorSession != session {
				t.Errorf("GLI-032: stored collectorSession = %v, want %d", after.Status.CollectorSession, session)
			}
			b, a := before.Status.DeepCopy(), after.Status.DeepCopy()
			b.CollectorSession, a.CollectorSession = nil, nil
			if !reflect.DeepEqual(b, a) {
				t.Errorf("GLI-032 (b)/I3: the status changed beyond collectorSession:\nbefore %+v\nafter  %+v", b, a)
			}
			if !reflect.DeepEqual(before.Spec, after.Spec) || before.Generation != after.Generation {
				t.Errorf("GLI-032: spec or generation changed: %+v/%d -> %+v/%d", before.Spec, before.Generation, after.Spec, after.Generation)
			}
			if !glieHasStatusManager(after, "glie-foreign") {
				t.Errorf("GLI-032 (d) I3: the other manager's status entry disappeared from managedFields: %v", after.ManagedFields)
			}
			if !glieHasStatusManager(after, "dpa-nodepath-status") {
				t.Errorf("GLI-032 (b): no dpa-nodepath-status entry in managedFields: %v", after.ManagedFields)
			}
		})
	}
}

// GLI-031/032: sessions are strictly greater than the stored value and than the
// engine's `after`, are never reused (also after the NodePathState is deleted and
// created again), and MaxInt64 is reachable once and then ends in
// ErrSessionOverflow without changing the stored value.
func TestGLI031_SessionsGrowAndOverflowIsRefused(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	s1 := s.mustAlloc(t, 0)
	s2 := s.mustAlloc(t, 0)
	if s1 < 1 || s2 <= s1 {
		t.Fatalf("GLI-031: consecutive sessions %d, %d are not strictly increasing", s1, s2)
	}
	s3 := s.mustAlloc(t, s2+100)
	if s3 <= s2+100 {
		t.Errorf("GLI-031: session %d is not greater than after=%d", s3, s2+100)
	}
	if got := s.stored(t); got == nil || *got != s3 {
		t.Errorf("GLI-031: stored session %v, want the last returned %d", got, s3)
	}
	s4 := s.mustAlloc(t, 1) // `after` below the stored value: the stored value wins
	if s4 <= s3 {
		t.Errorf("GLI-031: session %d with after=1 is not greater than the stored %d", s4, s3)
	}

	// the object is deleted and created again: the engine's `after` keeps the session from being reused.
	ctx := context.Background()
	old := glieMustNPS(t, e, s.Name)
	if err := e.Client.Delete(ctx, old); err != nil {
		t.Fatalf("delete NodePathState: %v", err)
	}
	glieNodePathState(t, e, s.Node)
	if got := s.stored(t); got != nil {
		t.Fatalf("fixture: the re-created NodePathState has collectorSession %v", *got)
	}
	s5 := s.mustAlloc(t, s4)
	if s5 <= s4 {
		t.Errorf("GLI-031: after a re-creation the session %d is not greater than after=%d", s5, s4)
	}

	// overflow.
	glieSeedStatus(t, e, s.Name, "glie-seed", func(st *v1alpha1.NodePathStateStatus) {
		*st = glieMinimalStatus(glieMustNPS(t, e, s.Name), glieInt64(math.MaxInt64-1))
	})
	last := s.mustAlloc(t, 0)
	if last != math.MaxInt64 {
		t.Errorf("GLI-031: from the stored MaxInt64-1 the session is %d, want MaxInt64", last)
	}
	for i := 0; i < 2; i++ {
		_, err := s.alloc(t, 0)
		if !errors.Is(err, liveingest.ErrSessionOverflow) {
			t.Errorf("GLI-031/036: with the stored MaxInt64 AllocateSession returned %v, want ErrSessionOverflow", err)
		}
		if got := s.stored(t); got == nil || *got != math.MaxInt64 {
			t.Errorf("GLI-031/036: the stored value changed to %v after an overflow", got)
		}
	}
}

// ---------------------------------------------------------------- GLI-032 (a)(c): not managed and store errors

// GLI-032 (a)(c): no NodePathState, a different spec.nodeRef.uid, or a 404 on the
// PUT is ErrNodeNotManaged (and the adapter never creates a NodePathState); API
// failures are ErrStoreUnavailable whose text carries no response body or other
// text from the server, and the node directory maps absent Nodes to ErrNodeNotFound.
func TestGLI032_NotManagedAndStoreErrors(t *testing.T) {
	e := glieEnv(t)
	const marker = "SERVER-BODY-MARKER-4c19"

	t.Run("no_node_path_state", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		if err := e.Client.Delete(context.Background(), glieMustNPS(t, e, s.Name)); err != nil {
			t.Fatal(err)
		}
		_, err := s.alloc(t, 0)
		if !errors.Is(err, liveingest.ErrNodeNotManaged) {
			t.Errorf("GLI-032 (a): err = %v, want ErrNodeNotManaged", err)
		}
		if _, ok := glieGetNPS(e, s.Name); ok {
			t.Errorf("GLI-032 (a): the adapter created a NodePathState (only S3a creates it)")
		}
		for _, en := range s.Ad.Rec.Entries() {
			if en.Method == http.MethodPost {
				t.Errorf("GLI-032 (a): the adapter sent %s", en)
			}
		}
	})
	t.Run("uid_mismatch", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		other := *s.Node
		other.UID = "another-node-uid"
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := s.Ad.Alloc(ctx, &other, 0)
		if !errors.Is(err, liveingest.ErrNodeNotManaged) {
			t.Errorf("GLI-032 (a): err = %v, want ErrNodeNotManaged when spec.nodeRef.uid differs from the node UID", err)
		}
		if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
			t.Errorf("GLI-032 (a): %d status PUT(s) for an unmanaged node", n)
		}
		if got := s.stored(t); got != nil {
			t.Errorf("GLI-032 (a): collectorSession = %d was written for a UID mismatch", *got)
		}
	})
	t.Run("not_found_on_put", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
			if req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, s.Status) {
				return glieStatusResponse(req, http.StatusNotFound, metav1.StatusReasonNotFound, marker)
			}
			return nil
		})
		_, err := s.alloc(t, 0)
		if !errors.Is(err, liveingest.ErrNodeNotManaged) {
			t.Errorf("GLI-032 (c): a 404 on the PUT gave %v, want ErrNodeNotManaged", err)
		}
		if err != nil && strings.Contains(err.Error(), marker) {
			t.Errorf("GLI-032 (c)/101: the error text carries the response body: %q", err.Error())
		}
	})
	for _, c := range []struct {
		name string
		when func(req *http.Request) bool
		resp func(req *http.Request) *http.Response
		errf func(req *http.Request) error
	}{
		{name: "500 on the node path state get",
			when: func(r *http.Request) bool {
				return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/nodepathstates/")
			},
			resp: func(r *http.Request) *http.Response {
				return glieStatusResponse(r, 500, metav1.StatusReasonInternalError, marker)
			}},
		{name: "403 on the node path state get", resp: func(r *http.Request) *http.Response {
			return glieStatusResponse(r, 403, metav1.StatusReasonForbidden, marker)
		}},
		{name: "503 on the status put", resp: func(r *http.Request) *http.Response {
			return glieStatusResponse(r, 503, metav1.StatusReasonServiceUnavailable, marker)
		}},
		{name: "transport error", errf: func(r *http.Request) error { return errors.New("connection reset " + marker) }},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			s := glieNewSessionEnv(t, e, nil)
			isTarget := func(r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/coordination.k8s.io/") {
					return false
				}
				if strings.Contains(c.name, "status put") {
					return r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, s.Status)
				}
				return strings.Contains(r.URL.Path, "/nodepathstates/"+s.Name) || strings.Contains(r.URL.Path, "/nodes/"+s.Name)
			}
			if c.errf != nil {
				s.Ad.Rec.InjectErr(func(r *http.Request) error {
					if isTarget(r) {
						return c.errf(r)
					}
					return nil
				})
			} else {
				s.Ad.Rec.Inject(func(r *http.Request) *http.Response {
					if isTarget(r) {
						return c.resp(r)
					}
					return nil
				})
			}
			_, err := s.alloc(t, 0)
			if !errors.Is(err, liveingest.ErrStoreUnavailable) {
				t.Errorf("GLI-032 (c): err = %v, want ErrStoreUnavailable", err)
			}
			if err != nil && strings.Contains(err.Error(), marker) {
				t.Errorf("GLI-032 (c)/GLI-101 (iv): the error text carries the response body or transport error text: %q", err.Error())
			}
			if errors.Is(err, liveingest.ErrNodeNotManaged) || errors.Is(err, liveingest.ErrSessionConflict) {
				t.Errorf("GLI-032 (c): err %v matches the wrong sentinel", err)
			}
			if got := s.stored(t); got != nil {
				t.Errorf("GLI-032: collectorSession %d was stored although the write failed", *got)
			}
		})
	}
	t.Run("node_directory", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := s.Ad.A.Nodes().GetNode(ctx, s.Node.Name)
		if err != nil || info.Name != s.Node.Name || info.UID != string(s.Node.UID) {
			t.Errorf("GetNode(existing) = %+v, %v, want {%s %s}", info, err, s.Node.Name, s.Node.UID)
		}
		if _, err := s.Ad.A.Nodes().GetNode(ctx, glieName(t, "ghost")); !errors.Is(err, liveingest.ErrNodeNotFound) {
			t.Errorf("GetNode(absent) error = %v, want ErrNodeNotFound", err)
		}
		s.Ad.Rec.Inject(func(r *http.Request) *http.Response {
			if strings.Contains(r.URL.Path, "/nodes/") {
				return glieStatusResponse(r, 500, metav1.StatusReasonInternalError, marker)
			}
			return nil
		})
		_, err = s.Ad.A.Nodes().GetNode(ctx, s.Node.Name)
		if err == nil || errors.Is(err, liveingest.ErrNodeNotFound) {
			t.Errorf("GetNode on an API failure = %v, want an error that is not ErrNodeNotFound (the server answers Unavailable, not PermissionDenied)", err)
		}
		if err != nil && strings.Contains(err.Error(), marker) {
			t.Errorf("GLI-101 (iv): the node directory error carries the response body: %q", err.Error())
		}
	})
}

// ---------------------------------------------------------------- GLI-124 (g): the deterministic transport scenarios

// GLI-124 (g) (1)/GLI-032 (d) I2: another writer changes conditions and graphRevision
// just before the adapter's first status PUT arrives. The PUT conflicts, the adapter
// reads the object again and writes only its own field: the final object has the
// other writer's fields and the new session, after exactly two PUTs and at most
// two reads.
func TestGLI124g1_InterleavedWriterFieldsSurvive(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	glieSeedStatus(t, e, s.Name, "glie-foreign", func(st *v1alpha1.NodePathStateStatus) {
		*st = glieRichStatus(glieMustNPS(t, e, s.Name), glieInt64(3))
	})
	injected := glieStr("9:1:" + strings.Repeat("d", 64))
	var fired atomic.Int64
	var injErr atomic.Value
	s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut || !strings.HasSuffix(req.URL.Path, s.Status) || fired.Add(1) != 1 {
			return nil
		}
		for attempt := 0; attempt < 10; attempt++ { // the competing writer, with its own client
			nps, ok := glieGetNPS(e, s.Name)
			if !ok {
				injErr.Store("NodePathState vanished")
				return nil
			}
			nps.Status.GraphRevision = injected
			nps.Status.Conditions = []metav1.Condition{{
				Type: "EvidenceFresh", Status: "True", Reason: "Ready", Message: "injected by the competing writer",
				ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)),
			}}
			if err := e.Client.Status().Update(context.Background(), nps); err == nil {
				return nil
			}
		}
		injErr.Store("the competing write never succeeded")
		return nil
	})
	session := s.mustAlloc(t, 0)
	if v := injErr.Load(); v != nil {
		t.Fatalf("fixture: %v", v)
	}
	got := glieMustNPS(t, e, s.Name)
	if got.Status.CollectorSession == nil || *got.Status.CollectorSession != session || session <= 3 {
		t.Errorf("GLI-124 (g)(1): collectorSession = %v (session %d), want the new session greater than 3", got.Status.CollectorSession, session)
	}
	if got.Status.GraphRevision == nil || *got.Status.GraphRevision != *injected {
		t.Errorf("GLI-124 (g)(1)/I2: graphRevision = %v, want the competing writer's %q", got.Status.GraphRevision, *injected)
	}
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Message != "injected by the competing writer" {
		t.Errorf("GLI-124 (g)(1)/I3: conditions = %+v, want the competing writer's single condition", got.Status.Conditions)
	}
	if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 2 {
		t.Errorf("GLI-124 (g)(1): %d status PUT(s), want 2 (the conflicting one and the retry)", n)
	}
	if n := len(s.Ad.Rec.SingleGETs("nodepathstates", s.Name)); n != 2 {
		t.Errorf("GLI-032 (c): %d reads of the NodePathState, want 2 (the first and the one after the 409; reads are direct, never cached)", n)
	}
}

// GLI-124 (g) (3)/GLI-032 (c)/GKA-123: k injected 409s (k = 0..4) end in success with
// k+1 PUTs, at most k+1 direct reads and waits of at least 10 ms * (2^k - 1) in
// total (10 ms doubling per attempt); five 409s end in ErrSessionConflict with
// five PUTs and an unchanged stored value.
func TestGLI124g3_ConflictRetriesFollowTheBackoffRule(t *testing.T) {
	e := glieEnv(t)
	for k := 0; k <= 5; k++ {
		t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
			s := glieNewSessionEnv(t, e, nil)
			glieSeedStatus(t, e, s.Name, "glie-seed", func(st *v1alpha1.NodePathStateStatus) {
				*st = glieMinimalStatus(glieMustNPS(t, e, s.Name), glieInt64(5))
			})
			var puts atomic.Int64
			s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
				if req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, s.Status) && puts.Add(1) <= int64(k) {
					return glieStatusResponse(req, http.StatusConflict, metav1.StatusReasonConflict, "injected conflict")
				}
				return nil
			})
			start := time.Now()
			session, err := s.alloc(t, 0)
			elapsed := time.Since(start)
			entries := s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)
			gets := s.Ad.Rec.SingleGETs("nodepathstates", s.Name)
			if k == 5 {
				if !errors.Is(err, liveingest.ErrSessionConflict) {
					t.Fatalf("GLI-124 (g)(3): five 409s gave %v, want ErrSessionConflict", err)
				}
				if len(entries) != 5 {
					t.Errorf("GKA-123: %d PUTs after five conflicts, want 5 attempts including the first", len(entries))
				}
				if got := s.stored(t); got == nil || *got != 5 {
					t.Errorf("GLI-032 (c): the stored session is %v after ErrSessionConflict, want the unchanged 5", got)
				}
			} else {
				if err != nil {
					t.Fatalf("GLI-124 (g)(3): %d conflicts must still succeed, got %v", k, err)
				}
				if session <= 5 {
					t.Errorf("GLI-031: session %d is not greater than the stored 5", session)
				}
				if got := s.stored(t); got == nil || *got != session {
					t.Errorf("stored session %v, want %d", got, session)
				}
				if len(entries) != k+1 {
					t.Errorf("GLI-124 (g)(3): %d PUTs after %d conflicts, want %d", len(entries), k, k+1)
				}
			}
			if len(gets) > k+1 || len(gets) < 1 {
				t.Errorf("GLI-032 (c): %d direct reads of the NodePathState, want at most %d (one first read and one after each conflict)", len(gets), k+1)
			}
			waits := k
			if k == 5 {
				waits = 4
			}
			want := time.Duration(10*((1<<waits)-1)) * time.Millisecond
			if elapsed < want {
				t.Errorf("GKA-123: the call took %v, want at least the back-off sum %v (10 ms doubling per attempt)", elapsed, want)
			}
			for i := 1; i < len(entries); i++ {
				gap := entries[i].At.Sub(entries[i-1].At)
				need := time.Duration(10<<(i-1)) * time.Millisecond
				if gap < need-3*time.Millisecond {
					t.Errorf("GKA-123: the gap before attempt %d is %v, want at least %v", i+1, gap, need)
				}
			}
			if elapsed > 10*time.Second {
				t.Errorf("GKA-123: the call took %v, the back-off must stay short", elapsed)
			}
		})
	}
}

// GLI-124 (g) (4)(5)/GLI-033: the adapter and the S3a controller write the same
// NodePathState with separate transports; the writer-specific PUT counts are
// observable, a conflict-free hello-sized call is exactly one PUT, and the two
// writers never erase each other's fields.
func TestGLI124g4_TwoWritersWithSeparateTransports(t *testing.T) {
	e := glieEnv(t)
	r := glieNewRig(t, e)
	// GLI-091 order: controller.New (process-wide loggers, GKA-005) before the adapter's leader poll.
	ad := glieNewAdapter(t, e, glieAdapterOpts{ControllerID: r.CtlID, LeaseName: r.Lease, NoPoll: true})
	ctl := glieStartController(t, e, r.CtlID, r.Lease, newNoObservationAssessor(), r.Clk, nil)
	ad.StartPoll(t)
	ad.WaitLeading(t, true, 20*time.Second)
	name := r.Scene.Node.Name
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, name)
		return ok && len(n.Status.Conditions) > 0, "S3a has not published the node status yet"
	})
	ctlPuts := len(ctl.Rec.StatusPUTs("nodepathstates", name))
	if ctlPuts < 1 {
		t.Fatalf("fixture: S3a's own transport saw %d status PUTs, want at least 1", ctlPuts)
	}
	if n := len(ad.Rec.StatusPUTs("nodepathstates", name)); n != 0 {
		t.Fatalf("fixture: the adapter's transport saw %d status PUTs before any call", n)
	}
	const calls = 5
	var last int64
	for i := 0; i < calls; i++ {
		session := glieMustAlloc(t, ad, r.Scene.Node, last)
		if session <= last {
			t.Errorf("GLI-031: session %d is not greater than %d", session, last)
		}
		last = session
		time.Sleep(250 * time.Millisecond) // let S3a pass in between
	}
	glieEventually(t, 10*time.Second, func() (bool, string) {
		n := glieMustNPS(t, e, name)
		return n.Status.CollectorSession != nil && *n.Status.CollectorSession == last && len(n.Status.Conditions) > 0, fmt.Sprintf("status %+v", n.Status)
	})
	if n := len(ad.Rec.StatusPUTs("nodepathstates", name)); n < calls {
		t.Errorf("GLI-124 (g)(4): the adapter's transport saw %d status PUTs for %d calls, want at least one each", n, calls)
	}
	for _, en := range ad.Rec.Entries() {
		if en.Method == http.MethodPut && strings.Contains(en.Path, "/nodepathstates/") && !strings.HasSuffix(en.Path, "/status") {
			t.Errorf("GLI-032: the adapter wrote the object (not its status): %s", en)
		}
	}
	// S3a keeps publishing: more passes must not lose the session.
	time.Sleep(1500 * time.Millisecond)
	final := glieMustNPS(t, e, name)
	if final.Status.CollectorSession == nil || *final.Status.CollectorSession != last {
		t.Errorf("GLI-033: after more S3a passes collectorSession = %v, want %d", final.Status.CollectorSession, last)
	}
	if err := ctl.Stop(t); err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("controller returned %v", err)
	}
}

// GLI-124 (g) (2)/GLI-033: the S3a controller's first status PUT is preceded by an
// AllocateSession. The controller's PUT conflicts, it reads the object again and
// keeps collectorSession while publishing its own fields.
func TestGLI124g2_SessionWrittenJustBeforeS3aStatusPutSurvives(t *testing.T) {
	e := glieEnv(t)
	r := glieNewRig(t, e)
	// GLI-091 order: controller.New (process-wide loggers, GKA-005) before the adapter's leader poll.
	ad := glieNewAdapter(t, e, glieAdapterOpts{ControllerID: r.CtlID, LeaseName: r.Lease, NoPoll: true})
	name := r.Scene.Node.Name

	cfg, rec := glieRecordingConfig(e.Config)
	var fired atomic.Int64
	var allocated atomic.Int64
	var injErr atomic.Value
	rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut || !strings.HasSuffix(req.URL.Path, "/nodepathstates/"+name+"/status") || fired.Add(1) != 1 {
			return nil
		}
		// S3a's first status PUT of this node: the ingest side writes collectorSession first.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var session int64
		var err error
		for attempt := 0; attempt < 50; attempt++ { // the adapter's gate may not have polled the controller's Lease yet
			if session, err = ad.Alloc(ctx, r.Scene.Node, 0); err == nil || !errors.Is(err, liveingest.ErrStoreUnavailable) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			injErr.Store(err.Error())
			return nil
		}
		allocated.Store(session)
		return nil
	})
	ctl := glieStartController(t, e, r.CtlID, r.Lease, newNoObservationAssessor(), r.Clk, func(o *controller.Options) { o.RESTConfig = cfg })
	ad.StartPoll(t)
	ad.WaitLeading(t, true, 20*time.Second)
	glieEventually(t, 20*time.Second, func() (bool, string) {
		n, ok := glieGetNPS(e, name)
		return ok && len(n.Status.Conditions) > 0 && n.Status.CollectorSession != nil, "S3a has not published over the session yet"
	})
	if v := injErr.Load(); v != nil {
		t.Fatalf("fixture: AllocateSession inside the transport failed: %v", v)
	}
	if allocated.Load() == 0 {
		t.Fatalf("fixture: the first S3a status PUT was never intercepted")
	}
	got := glieMustNPS(t, e, name)
	if *got.Status.CollectorSession != allocated.Load() {
		t.Errorf("GLI-124 (g)(2)/GKA-105: collectorSession = %d, want the session %d written just before S3a's PUT", *got.Status.CollectorSession, allocated.Load())
	}
	if got.Status.NodeRef != got.Spec.NodeRef || got.Status.EvidenceCompleteness != v1alpha1.SnapshotCompletenessUnknown || len(got.Status.Conditions) == 0 {
		t.Errorf("GLI-124 (g)(2): S3a's own fields are not reflected: %+v", got.Status)
	}
	if n := len(rec.StatusPUTs("nodepathstates", name)); n < 2 {
		t.Errorf("GLI-124 (g)(2): S3a sent %d status PUT(s), want at least 2 (the conflicting first one and its retry)", n)
	}
	if err := ctl.Stop(t); err != nil {
		t.Logf("controller returned %v", err)
	}
}

// ---------------------------------------------------------------- GLI-032 (e)(f)(g)(h): concurrency, budget, leadership, verbs

func glieMustAlloc(t testing.TB, ad *glieAdapter, node *corev1.Node, after int64) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	got, err := ad.Alloc(ctx, node, after)
	if err != nil {
		t.Fatalf("AllocateSession: %v", err)
	}
	return got
}

// GLI-032 (e): sixteen concurrent calls for one node on one adapter all succeed with
// distinct sessions (the adapter serializes per node, so no ErrSessionConflict); two
// adapters with eight callers each end with distinct successful sessions and only
// ErrSessionConflict as a failure, the stored value being the largest one.
func TestGLI032_ConcurrentCallsAreSerializedPerNode(t *testing.T) {
	e := glieEnv(t)
	type res struct {
		session int64
		err     error
	}
	run := func(t *testing.T, s *glieSessionEnv, adapters []*glieAdapter, per int) []res {
		t.Helper()
		var wg sync.WaitGroup
		out := make([]res, len(adapters)*per)
		for i := range out {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				session, err := adapters[i/per].Alloc(ctx, s.Node, 0)
				out[i] = res{session, err}
			}(i)
		}
		wg.Wait()
		return out
	}
	t.Run("one_adapter_sixteen_callers", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		out := run(t, s, []*glieAdapter{s.Ad}, 16)
		seen := map[int64]bool{}
		var max int64
		for _, r := range out {
			if r.err != nil {
				t.Errorf("GLI-032 (e): a concurrent call on one adapter failed: %v (ErrSessionConflict only arises from other writers)", r.err)
				continue
			}
			if r.session < 1 || seen[r.session] {
				t.Errorf("GLI-032 (e)/GLI-031: session %d is not positive and unique", r.session)
			}
			seen[r.session] = true
			if r.session > max {
				max = r.session
			}
		}
		if len(seen) != 16 {
			t.Errorf("GLI-032 (e): %d distinct sessions for 16 calls, want 16", len(seen))
		}
		if got := s.stored(t); got == nil || *got != max {
			t.Errorf("GLI-031: stored session %v, want the largest returned %d", got, max)
		}
	})
	t.Run("two_adapters_eight_callers_each", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		second := glieNewAdapter(t, e, glieAdapterOpts{ControllerID: s.CtlID, LeaseName: s.Lease})
		second.WaitLeading(t, true, 10*time.Second)
		out := run(t, s, []*glieAdapter{s.Ad, second}, 8)
		seen := map[int64]bool{}
		var max int64
		ok := 0
		for _, r := range out {
			switch {
			case r.err == nil:
				ok++
				if r.session < 1 || seen[r.session] {
					t.Errorf("GLI-031: session %d returned to two callers or not positive", r.session)
				}
				seen[r.session] = true
				if r.session > max {
					max = r.session
				}
			case !errors.Is(r.err, liveingest.ErrSessionConflict):
				t.Errorf("GLI-032 (e): a call failed with %v, want only ErrSessionConflict as a failure between adapters", r.err)
			}
		}
		if ok == 0 {
			t.Fatalf("GLI-032 (e): no call succeeded")
		}
		if got := s.stored(t); got == nil || *got != max {
			t.Errorf("GLI-031: stored session %v, want the largest successful session %d", got, max)
		}
	})
}

// GLI-032 (f)(g)/GLI-092: a call whose adapter stops being the leader between the
// read and the write does not write (ErrStoreUnavailable, zero PUTs); a cancelled
// caller context ends the call at once and no further request enters the
// transport; a call that cannot make progress ends at its own 20 s budget.
func TestGLI032_LeadershipAndCancellationDiscipline(t *testing.T) {
	e := glieEnv(t)

	t.Run("not_leading_at_the_put", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		entered, release := make(chan struct{}), make(chan struct{})
		var once, relOnce sync.Once
		s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
			if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodepathstates/"+s.Name) {
				once.Do(func() { close(entered) })
				select {
				case <-release:
				case <-req.Context().Done():
				}
			}
			return nil
		})
		t.Cleanup(func() { relOnce.Do(func() { close(release) }) })
		done := make(chan error, 1)
		go func() {
			_, err := s.alloc(t, 0)
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("the adapter never read the NodePathState")
		}
		other := "someone-else"
		glieSetLeaseHolder(t, e, s.Lease, &other)
		s.Ad.WaitLeading(t, false, 10*time.Second)
		relOnce.Do(func() { close(release) })
		select {
		case err := <-done:
			if !errors.Is(err, liveingest.ErrStoreUnavailable) {
				t.Errorf("GLI-032 (g): err = %v, want ErrStoreUnavailable when Leader() is false at the PUT", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("AllocateSession did not return")
		}
		if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
			t.Errorf("GLI-032 (g)/GLI-092: %d status PUT(s) from a non-leading adapter, want 0", n)
		}
		if got := s.stored(t); got != nil {
			t.Errorf("GLI-092: a session %d was stored by a non-leader", *got)
		}
	})
	t.Run("not_leading_at_all", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		other := "someone-else"
		glieSetLeaseHolder(t, e, s.Lease, &other)
		s.Ad.WaitLeading(t, false, 10*time.Second)
		if _, err := s.alloc(t, 0); !errors.Is(err, liveingest.ErrStoreUnavailable) {
			t.Errorf("GLI-032 (g): err = %v, want ErrStoreUnavailable", err)
		}
		if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
			t.Errorf("GLI-092: %d status PUT(s) from a non-leading adapter, want 0", n)
		}
		glieSetLeaseHolder(t, e, s.Lease, &s.CtlID)
		s.Ad.WaitLeading(t, true, 10*time.Second)
		s.mustAlloc(t, 0)
	})
	t.Run("cancelled_context", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		entered := make(chan struct{})
		var once sync.Once
		s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
			if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodepathstates/"+s.Name) {
				once.Do(func() { close(entered) })
				<-req.Context().Done()
			}
			return nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := s.Ad.Alloc(ctx, s.Node, 0)
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("the adapter never read the NodePathState")
		}
		cancel()
		cut := time.Now()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("GLI-032 (g): a cancelled call returned no error")
			}
			if errors.Is(err, liveingest.ErrSessionConflict) || errors.Is(err, liveingest.ErrNodeNotManaged) {
				t.Errorf("GLI-032 (g): a cancelled call returned %v, want ErrStoreUnavailable or the context error", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("GLI-032 (g): the call did not return within 5 s of the cancellation")
		}
		time.Sleep(1200 * time.Millisecond)
		var late []string
		for _, en := range s.Ad.Rec.Since(cut.Add(100*time.Millisecond), true) {
			late = append(late, en.String())
		}
		if len(late) > 0 {
			t.Errorf("GLI-032 (g)/GLI-092 (2): %d request(s) entered the transport more than 100 ms after the cancellation: %v", len(late), late)
		}
		if n := len(s.Ad.Rec.StatusPUTs("nodepathstates", s.Name)); n != 0 {
			t.Errorf("GLI-032 (g): %d status PUT(s) after a cancellation", n)
		}
	})
}

// GLI-032 (f): the whole call is bounded to 20 s (shorter than the agent's 30 s
// ServerHello wait): a request that never answers is cancelled and the call returns
// ErrStoreUnavailable at about 20 s. This test waits in real time (about 20 s).
func TestGLI032_TheWholeCallIsBoundedTo20Seconds(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	s.Ad.Rec.Inject(func(req *http.Request) *http.Response {
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodepathstates/"+s.Name) {
			<-req.Context().Done() // never answers
		}
		return nil
	})
	start := time.Now()
	_, err := s.Ad.Alloc(context.Background(), s.Node, 0)
	el := time.Since(start)
	if !errors.Is(err, liveingest.ErrStoreUnavailable) {
		t.Errorf("GLI-032 (f): err = %v, want ErrStoreUnavailable when the call budget is exhausted", err)
	}
	if el < 18*time.Second || el > 25*time.Second {
		t.Errorf("GLI-032 (f): the call ended after %v, want about 20 s", el)
	}
}

// GLI-032 (h)/(a): the adapter uses only get nodepathstates, update
// nodepathstates/status, get nodes and get leases - no list, watch, create, patch or
// delete, no Event, no cache - so the RBAC input does not grow.
func TestGLI032_UsesOnlyTheDocumentedAPIVerbs(t *testing.T) {
	e := glieEnv(t)
	s := glieNewSessionEnv(t, e, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.Ad.A.Nodes().GetNode(ctx, s.Node.Name); err != nil {
		t.Fatal(err)
	}
	s.mustAlloc(t, 0)
	s.mustAlloc(t, 0)
	time.Sleep(400 * time.Millisecond) // a few Lease polls
	allowed := map[string]bool{"get nodepathstates": true, "update nodepathstates/status": true, "get nodes": true, "get leases": true}
	for _, v := range glieVerbs(s.Ad.Rec.Entries()) {
		if !allowed[v] {
			t.Errorf("GLI-032 (h): the adapter used %q, outside the documented set %v", v, allowed)
		}
	}
	if n := len(s.Ad.Rec.SingleGETs("nodepathstates", s.Name)); n < 2 {
		t.Errorf("GLI-032 (a): %d direct reads for two calls, want at least one per call (the NodePathState is read from the API server, not a cache)", n)
	}
	if v := s.Ad.Rec.Violations(); len(v) > 0 {
		t.Errorf("harness: %v", v)
	}
}

// ---------------------------------------------------------------- GLI-113: the adapter's options

// GLI-113: New validates its options without any network I/O and copies the REST
// config (the caller's value is not changed and later changes to it are not seen);
// a non-zero Timeout of the config is respected; every request goes through the
// copy's transport wrapper; Leader() is false until the first successful poll;
// RunLeaderPoll blocks until the context is cancelled, returns nil even when the
// API server cannot be reached, and sends nothing after it returned.
func TestGLI113_NewValidatesOptionsWithoutIOAndCopiesTheConfig(t *testing.T) {
	e := glieEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepted atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	probe := &rest.Config{Host: "http://" + ln.Addr().String()}
	good := func() ingestadapter.Options {
		return ingestadapter.Options{RESTConfig: probe, ControllerID: "c1", LeaseNamespace: "default", LeaseName: "l1"}
	}
	for _, c := range []struct {
		name  string
		mut   func(*ingestadapter.Options)
		valid bool
	}{
		{"minimal", func(*ingestadapter.Options) {}, true},
		{"all optional values", func(o *ingestadapter.Options) { o.PollInterval = time.Second; o.StaleAfter = 5 * time.Second }, true},
		{"nil rest config", func(o *ingestadapter.Options) { o.RESTConfig = nil }, false},
		{"empty controller id", func(o *ingestadapter.Options) { o.ControllerID = "" }, false},
		{"empty lease namespace", func(o *ingestadapter.Options) { o.LeaseNamespace = "" }, false},
		{"empty lease name", func(o *ingestadapter.Options) { o.LeaseName = "" }, false},
		{"negative poll interval", func(o *ingestadapter.Options) { o.PollInterval = -time.Nanosecond }, false},
		{"negative stale after", func(o *ingestadapter.Options) { o.StaleAfter = -time.Nanosecond }, false},
	} {
		o := good()
		c.mut(&o)
		ad, err := ingestadapter.New(o)
		switch {
		case c.valid && (err != nil || ad == nil):
			t.Errorf("GLI-113 %s: New = %v, %v, want success", c.name, ad, err)
		case !c.valid && !errors.Is(err, ingestadapter.ErrInvalidOptions):
			t.Errorf("GLI-113 %s: New error = %v, want ErrInvalidOptions", c.name, err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("GLI-113: New connected to the API server %d time(s); it validates without I/O", n)
	}
	if probe.QPS != 0 || probe.Burst != 0 || probe.Timeout != 0 {
		t.Errorf("GLI-113: New changed the caller's config to QPS %v Burst %d Timeout %v; defaults belong to the copy", probe.QPS, probe.Burst, probe.Timeout)
	}

	t.Run("the config is copied", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		cfg, rec := glieRecordingConfig(e.Config)
		ad, err := ingestadapter.New(ingestadapter.Options{RESTConfig: cfg, ControllerID: s.CtlID, LeaseNamespace: glieNS, LeaseName: s.Lease, PollInterval: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Host = "https://127.0.0.1:1" // a later change must not be seen by the adapter
		cfg.WrapTransport = nil
		r := &glieAdapter{A: ad, Rec: rec, Cfg: cfg}
		r.StartPoll(t)
		t.Cleanup(func() { _ = r.StopPoll(t) })
		r.WaitLeading(t, true, 10*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := r.Alloc(ctx, s.Node, 0); err != nil {
			t.Errorf("GLI-113: the adapter does not work after the caller changed its config: %v (the config must be copied)", err)
		}
		if len(rec.Entries()) == 0 {
			t.Errorf("GLI-113: the copy's WrapTransport did not see the adapter's requests")
		}
	})
	t.Run("the config timeout is respected", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, nil)
		base := rest.CopyConfig(e.Config)
		base.Timeout = 300 * time.Millisecond
		cfg, rec := glieRecordingConfig(base)
		rec.Inject(func(req *http.Request) *http.Response {
			if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodepathstates/"+s.Name) {
				<-req.Context().Done()
			}
			return nil
		})
		ad, err := ingestadapter.New(ingestadapter.Options{RESTConfig: cfg, ControllerID: s.CtlID, LeaseNamespace: glieNS, LeaseName: s.Lease, PollInterval: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		r := &glieAdapter{A: ad, Rec: rec, Cfg: cfg}
		r.StartPoll(t)
		t.Cleanup(func() { _ = r.StopPoll(t) })
		r.WaitLeading(t, true, 10*time.Second)
		start := time.Now()
		_, err = r.Alloc(context.Background(), s.Node, 0)
		if !errors.Is(err, liveingest.ErrStoreUnavailable) {
			t.Errorf("GLI-113: err = %v, want ErrStoreUnavailable", err)
		}
		if el := time.Since(start); el > 8*time.Second {
			t.Errorf("GLI-113: a request that never answers took %v with Timeout 300 ms, want the config's non-zero timeout to be respected (the defaults 30 s apply only to 0)", el)
		}
	})
	t.Run("Leader is false before a poll and RunLeaderPoll stops cleanly", func(t *testing.T) {
		s := glieNewSessionEnv(t, e, func(o *glieAdapterOpts) { o.NoPoll = true })
		time.Sleep(500 * time.Millisecond)
		if s.Ad.Leading() {
			t.Errorf("GLI-113/092 (iii): Leader() is true before RunLeaderPoll ran, with a valid Lease held by the controller ID")
		}
		s.Ad.StartPoll(t)
		s.Ad.WaitLeading(t, true, 10*time.Second)
		if err := s.Ad.StopPoll(t); err != nil {
			t.Errorf("GLI-113: RunLeaderPoll returned %v after its context was cancelled, want nil", err)
		}
		cut := time.Now()
		time.Sleep(1200 * time.Millisecond)
		if late := s.Ad.Rec.Since(cut.Add(100*time.Millisecond), false); len(late) > 0 {
			t.Errorf("GLI-113: %d request(s) after RunLeaderPoll returned: %v", len(late), late)
		}
	})
	t.Run("RunLeaderPoll returns nil when the API server is unreachable", func(t *testing.T) {
		port := glieFreePort(t)
		ad, err := ingestadapter.New(ingestadapter.Options{
			RESTConfig: &rest.Config{Host: fmt.Sprintf("http://127.0.0.1:%d", port)}, ControllerID: "c1", LeaseNamespace: glieNS, LeaseName: "l1",
			PollInterval: 100 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := &glieAdapter{A: ad}
		r.StartPoll(t)
		time.Sleep(1200 * time.Millisecond)
		if r.Leading() {
			t.Errorf("GLI-092 (iii): Leader() is true although no poll ever succeeded (fail-closed)")
		}
		if err := r.StopPoll(t); err != nil {
			t.Errorf("GLI-113: RunLeaderPoll returned %v, want nil (errors are retried, never returned)", err)
		}
	})
}
