// Package kubeapi_test is the envtest black-box suite of gpu-fleet-k8s-api
// (spec v1.0, section 10).
//
// This file is the shared harness. Its package-level names (TestMain, gkaEnv,
// gkaName, gkaEventually, gkaCleanupAll, the fakes, the recorder and
// gkaStartController) are used by the other test files of the package: names,
// arguments and results must not change. Extra helpers use the gkaR prefix.
//
// Harness rules (GKA-170..178):
//   - tests never call t.Parallel; every test calls gkaEnv first, which also
//     registers the end-of-test cleanup (stop controllers started by the test,
//     then delete every GPUFleet, GPUDevice, NodePathState, Node and Lease whose
//     name carries the per-test prefix, and wait until they are gone);
//   - every object a test creates MUST be named through gkaName(t, suffix) so the
//     prefix-based cleanup can find it. A top-level test's prefix also covers the
//     objects its subtests create; a subtest's own prefix covers only itself;
//   - envtest starts once per package in TestMain. There is no garbage collector,
//     scheduler, kubelet or namespace controller (GKA-176).
package kubeapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

// gkaProbeEnv makes the test binary run one harness probe instead of the normal
// suite (used by the GKA-171 and GKA-177 self tests, which re-exec the test binary).
const gkaProbeEnv = "GKA_HARNESS_PROBE"

// gkaShared is the process-wide envtest handle. nil means envtest is unavailable
// (KUBEBUILDER_ASSETS unset), in which case gkaEnv skips.
var gkaShared *gkaEnvT

// gkaEnvT is what a test gets from gkaEnv.
type gkaEnvT struct {
	Config    *rest.Config // copy of the envtest config (unwrapped: the test's own requests are not recorded)
	Client    client.Client
	Clientset kubernetes.Interface
}

// TestMain starts envtest once when KUBEBUILDER_ASSETS is set (GKA-171/172).
//   - unset: one explicit log line naming KUBEBUILDER_ASSETS, then the suite runs and
//     every envtest test skips (a skip is not a PASS);
//   - set but kube-apiserver or etcd is missing there: the run FAILS (exit 1);
//   - set: deploy/crds is installed as is (ErrorIfCRDPathMissing) and the three CRDs
//     must reach Established before any test runs.
func TestMain(m *testing.M) {
	if kind := os.Getenv(gkaProbeEnv); kind != "" {
		gkaRunProbe(kind) // never returns
		return
	}
	os.Exit(gkaMain(m))
}

func gkaMain(m *testing.M) int {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		fmt.Fprintln(os.Stderr, "gka harness: KUBEBUILDER_ASSETS is not set: envtest tests are SKIPPED (a skip is not a PASS; gated runs must set KUBEBUILDER_ASSETS)")
		return m.Run()
	}
	for _, bin := range []string{"kube-apiserver", "etcd"} {
		fi, err := os.Stat(filepath.Join(assets, bin))
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			fmt.Fprintf(os.Stderr, "gka harness: FAIL: KUBEBUILDER_ASSETS=%q is set but an executable %s is not there (GKA-171: failure, not skip)\n", assets, bin)
			return 1
		}
	}
	crdDir, err := filepath.Abs(filepath.Join("..", "..", "deploy", "crds"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gka harness: FAIL: cannot resolve deploy/crds: %v\n", err)
		return 1
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:        []string{crdDir},
		ErrorIfCRDPathMissing:    true,
		BinaryAssetsDirectory:    assets,
		ControlPlaneStartTimeout: 60 * time.Second,
		ControlPlaneStopTimeout:  30 * time.Second,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gka harness: FAIL: envtest start (CRDs from %s): %v\n", crdDir, err)
		_ = env.Stop()
		return 1
	}
	stop := func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "gka harness: envtest stop: %v\n", err)
		}
	}
	shared, err := gkaBuildShared(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gka harness: FAIL: %v\n", err)
		stop()
		return 1
	}
	if err := gkaVerifyEstablished(shared.Client); err != nil {
		fmt.Fprintf(os.Stderr, "gka harness: FAIL: %v\n", err)
		stop()
		return 1
	}
	gkaShared = shared
	code := m.Run()
	stop()
	return code
}

func gkaBuildShared(cfg *rest.Config) (*gkaEnvT, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("clientgoscheme: %w", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("v1alpha1.AddToScheme: %w", err)
	}
	if err := coordinationv1.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("coordination AddToScheme: %w", err)
	}
	c, err := client.New(rest.CopyConfig(cfg), client.Options{Scheme: s})
	if err != nil {
		return nil, fmt.Errorf("client.New: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rest.CopyConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("kubernetes.NewForConfig: %w", err)
	}
	return &gkaEnvT{Config: cfg, Client: c, Clientset: cs}, nil
}

func gkaVerifyEstablished(c client.Client) error {
	names := []string{
		"gpufleets.infrastructure.data-path-assurance.io",
		"gpudevices.infrastructure.data-path-assurance.io",
		"nodepathstates.infrastructure.data-path-assurance.io",
	}
	for _, n := range names {
		ok, last := gkaRWait(30*time.Second, func() (bool, string) {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
			if err := c.Get(context.Background(), client.ObjectKey{Name: n}, u); err != nil {
				return false, err.Error()
			}
			conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
			for _, ci := range conds {
				cm, _ := ci.(map[string]interface{})
				if cm["type"] == "Established" && cm["status"] == "True" {
					return true, ""
				}
			}
			return false, "no Established=True condition yet"
		})
		if !ok {
			return fmt.Errorf("CRD %s did not become Established (GKA-172): %s", n, last)
		}
	}
	return nil
}

// gkaEnv returns the shared envtest handle and registers the end-of-test
// cleanup. Without KUBEBUILDER_ASSETS it skips with an explicit reason.
func gkaEnv(t *testing.T) *gkaEnvT {
	t.Helper()
	if gkaShared == nil {
		t.Skip("gka harness: envtest unavailable: KUBEBUILDER_ASSETS is not set (this skip is NOT a PASS)")
	}
	e := &gkaEnvT{Config: rest.CopyConfig(gkaShared.Config), Client: gkaShared.Client, Clientset: gkaShared.Clientset}
	gkaRRegisterCleanup(t, e)
	return e
}

var gkaRCleanupRegistered sync.Map // t.Name() -> true while a cleanup is pending

func gkaRRegisterCleanup(t *testing.T, e *gkaEnvT) {
	t.Helper()
	if _, loaded := gkaRCleanupRegistered.LoadOrStore(t.Name(), true); loaded {
		return
	}
	// start-of-test sweep: a previous run of the same test must not leave residue.
	gkaRSweep(e, gkaRCleanupPrefix(t.Name()), 30*time.Second)
	t.Cleanup(func() {
		gkaCleanupAll(t, e)
		gkaRCleanupRegistered.Delete(t.Name())
	})
}

// ---------------------------------------------------------------------------
// names
// ---------------------------------------------------------------------------

var gkaRClauseRE = regexp.MustCompile(`^TestGKA(\d{3})`)

func gkaRHash(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}

func gkaRTopName(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// gkaRPrefixes returns the per-top-level-test prefix and the per-test (subtest
// aware) prefix. Both are lowercase DNS-1123 fragments, at most 22 bytes.
func gkaRPrefixes(testName string) (top, full string) {
	topName := gkaRTopName(testName)
	digits := ""
	if m := gkaRClauseRE.FindStringSubmatch(topName); m != nil {
		digits = m[1]
	}
	top = "g" + digits + "-" + gkaRHash(topName, 8)
	full = top
	if topName != testName {
		full = top + "s" + gkaRHash(testName, 8)
	}
	return top, full
}

// gkaRCleanupPrefix is the prefix a test's cleanup deletes: the wide top-level
// prefix for a top-level test, the narrow one for a subtest.
func gkaRCleanupPrefix(testName string) string {
	top, full := gkaRPrefixes(testName)
	if strings.Contains(testName, "/") {
		return full
	}
	return top
}

func gkaRSanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "x"
	}
	return out
}

// gkaName returns a lowercase DNS-1123 name (at most 63 bytes) unique to the
// test and the suffix: <per-test prefix>-<suffix>. Long suffixes are truncated and
// carry a hash of the original so distinct suffixes stay distinct.
func gkaName(t *testing.T, suffix string) string {
	t.Helper()
	return gkaRNameFor(t.Name(), suffix)
}

func gkaRNameFor(testName, suffix string) string {
	top, full := gkaRPrefixes(testName)
	base := top
	if strings.Contains(testName, "/") {
		base = full
	}
	s := gkaRSanitize(suffix)
	name := base + "-" + s
	if len(name) <= 63 {
		return name
	}
	room := 63 - len(base) - 1 - 9
	if room < 1 {
		room = 1
	}
	cut := strings.Trim(s[:room], "-")
	if cut == "" {
		cut = "x"
	}
	return base + "-" + cut + "-" + gkaRHash(suffix, 8)
}

// ---------------------------------------------------------------------------
// waiting
// ---------------------------------------------------------------------------

// gkaRWait polls cond every 50 ms until it reports true or the timeout expires.
func gkaRWait(timeout time.Duration, cond func() (bool, string)) (bool, string) {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		ok, desc := cond()
		if ok {
			return true, desc
		}
		last = desc
		if !time.Now().Before(deadline) {
			return false, last
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// gkaEventually polls cond every 50 ms (at most 100 ms) and fails the test with
// the last description when the timeout expires. Timeouts are at most 20 s
// (90 s only for the 1025-Node capacity test) per GKA-177.
func gkaEventually(t *testing.T, timeout time.Duration, cond func() (bool, string)) {
	t.Helper()
	if ok, last := gkaRWait(timeout, cond); !ok {
		t.Fatalf("gkaEventually: condition not met within %s; last state: %s", timeout, last)
	}
}

// ---------------------------------------------------------------------------
// cleanup
// ---------------------------------------------------------------------------

// gkaCleanupAll stops the controllers this test started, then deletes every
// GPUFleet, GPUDevice, NodePathState, Node and Lease carrying the test's name
// prefix (finalizers are cleared) and waits until none is left (GKA-173).
func gkaCleanupAll(t *testing.T, e *gkaEnvT) {
	t.Helper()
	gkaRStopControllersOf(t)
	prefix := gkaRCleanupPrefix(t.Name())
	if left := gkaRSweep(e, prefix, 30*time.Second); len(left) > 0 {
		t.Errorf("gkaCleanupAll: objects with prefix %q still exist after 30s: %v", prefix, left)
	}
}

// gkaRSweep deletes matching objects until none remain; it returns what is left
// when the timeout expires.
func gkaRSweep(e *gkaEnvT, prefix string, timeout time.Duration) []string {
	var left []string
	gkaRWait(timeout, func() (bool, string) {
		left = gkaRDeleteMatching(e, prefix)
		return len(left) == 0, fmt.Sprint(left)
	})
	return left
}

func gkaRDeleteMatching(e *gkaEnvT, prefix string) []string {
	ctx := context.Background()
	var left []string
	del := func(o client.Object, kind string) {
		left = append(left, kind+"/"+o.GetName())
		if len(o.GetFinalizers()) > 0 {
			o.SetFinalizers(nil)
			_ = e.Client.Update(ctx, o)
		}
		_ = e.Client.Delete(ctx, o) // NotFound and conflicts are re-evaluated by the next poll
	}
	var fleets v1alpha1.GPUFleetList
	if err := e.Client.List(ctx, &fleets); err == nil {
		for i := range fleets.Items {
			if strings.HasPrefix(fleets.Items[i].Name, prefix) {
				del(&fleets.Items[i], "GPUFleet")
			}
		}
	}
	var devices v1alpha1.GPUDeviceList
	if err := e.Client.List(ctx, &devices); err == nil {
		for i := range devices.Items {
			if strings.HasPrefix(devices.Items[i].Name, prefix) {
				del(&devices.Items[i], "GPUDevice")
			}
		}
	}
	var states v1alpha1.NodePathStateList
	if err := e.Client.List(ctx, &states); err == nil {
		for i := range states.Items {
			if strings.HasPrefix(states.Items[i].Name, prefix) {
				del(&states.Items[i], "NodePathState")
			}
		}
	}
	var nodes corev1.NodeList
	if err := e.Client.List(ctx, &nodes); err == nil {
		for i := range nodes.Items {
			if strings.HasPrefix(nodes.Items[i].Name, prefix) {
				del(&nodes.Items[i], "Node")
			}
		}
	}
	var leases coordinationv1.LeaseList
	if err := e.Client.List(ctx, &leases); err == nil {
		for i := range leases.Items {
			if strings.HasPrefix(leases.Items[i].Name, prefix) {
				del(&leases.Items[i], "Lease")
			}
		}
	}
	return left
}

// ---------------------------------------------------------------------------
// fake assessor and fake clock (GKA-174)
// ---------------------------------------------------------------------------

// gkaFakeCall is one ordered entry of a fake assessor's call log.
type gkaFakeCall struct {
	Kind        string // "assess" or "forget"
	At          time.Time
	NodeUID     string
	Req         app.NodeAssessmentRequest // deep copy taken before the script ran ("assess" only)
	HasDeadline bool
	Remaining   time.Duration // ctx deadline minus call time at entry
}

// gkaFakeAssessor implements app.NodeAssessor. Every access is mutex protected;
// the script runs outside the lock.
type gkaFakeAssessor struct {
	mu        sync.Mutex
	script    func(req app.NodeAssessmentRequest) (app.NodeAssessment, error)
	scriptCtx func(ctx context.Context, req app.NodeAssessmentRequest) (app.NodeAssessment, error)
	calls     []gkaFakeCall
}

func newGkaFakeAssessor() *gkaFakeAssessor { return &gkaFakeAssessor{} }

// SetScript installs the scripted result (nil restores "no observation").
func (f *gkaFakeAssessor) SetScript(fn func(req app.NodeAssessmentRequest) (app.NodeAssessment, error)) {
	f.mu.Lock()
	f.script = fn
	f.mu.Unlock()
}

// SetScriptCtx installs a script that also sees the call context; it takes
// precedence over SetScript (nil removes it).
func (f *gkaFakeAssessor) SetScriptCtx(fn func(ctx context.Context, req app.NodeAssessmentRequest) (app.NodeAssessment, error)) {
	f.mu.Lock()
	f.scriptCtx = fn
	f.mu.Unlock()
}

func gkaRCopyReq(req app.NodeAssessmentRequest) app.NodeAssessmentRequest {
	cp := req
	cp.Policy.RequiredCoverage = append([]fleet.CoverageRequirement(nil), req.Policy.RequiredCoverage...)
	cp.Intents = append([]fleet.Intent(nil), req.Intents...)
	return cp
}

// AssessNode records the call and returns the scripted assessment (zero value
// when no script is set).
func (f *gkaFakeAssessor) AssessNode(ctx context.Context, req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
	call := gkaFakeCall{Kind: "assess", At: time.Now(), NodeUID: req.Node.UID, Req: gkaRCopyReq(req)}
	if dl, ok := ctx.Deadline(); ok {
		call.HasDeadline = true
		call.Remaining = time.Until(dl)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	fn, fnCtx := f.script, f.scriptCtx
	f.mu.Unlock()
	if fnCtx != nil {
		return fnCtx(ctx, req)
	}
	if fn == nil {
		return app.NodeAssessment{}, nil
	}
	return fn(req)
}

// ForgetNode records the call.
func (f *gkaFakeAssessor) ForgetNode(nodeUID string) {
	f.mu.Lock()
	f.calls = append(f.calls, gkaFakeCall{Kind: "forget", At: time.Now(), NodeUID: nodeUID})
	f.mu.Unlock()
}

// Requests returns copies of the AssessNode arguments in call order.
func (f *gkaFakeAssessor) Requests() []app.NodeAssessmentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []app.NodeAssessmentRequest
	for _, c := range f.calls {
		if c.Kind == "assess" {
			out = append(out, gkaRCopyReq(c.Req))
		}
	}
	return out
}

// Forgotten returns the ForgetNode arguments in call order.
func (f *gkaFakeAssessor) Forgotten() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.Kind == "forget" {
			out = append(out, c.NodeUID)
		}
	}
	return out
}

// Log returns the ordered AssessNode and ForgetNode call log (copies).
func (f *gkaFakeAssessor) Log() []gkaFakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]gkaFakeCall, len(f.calls))
	for i, c := range f.calls {
		c.Req = gkaRCopyReq(c.Req)
		out[i] = c
	}
	return out
}

// AssessCount is the number of AssessNode calls so far.
func (f *gkaFakeAssessor) AssessCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Kind == "assess" {
			n++
		}
	}
	return n
}

// gkaFakeClock implements controller.Clock; it only moves when the test says so.
type gkaFakeClock struct {
	mu    sync.Mutex
	now   time.Time
	calls int64
}

func newGkaFakeClock(t0 time.Time) *gkaFakeClock { return &gkaFakeClock{now: t0} }

// Now returns the current fake time and counts the call.
func (c *gkaFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.now
}

// Set moves the clock to t.
func (c *gkaFakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// Advance moves the clock forward by d.
func (c *gkaFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Calls is the number of Now() calls so far.
func (c *gkaFakeClock) Calls() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

var _ controller.Clock = (*gkaFakeClock)(nil)
var _ app.NodeAssessor = (*gkaFakeAssessor)(nil)

// ---------------------------------------------------------------------------
// request recorder (GKA-175 observation seam)
// ---------------------------------------------------------------------------

// gkaRecEntry is one request that entered the wrapped transport.
type gkaRecEntry struct {
	At       time.Time
	Method   string
	Path     string
	RawQuery string
	Watch    bool
	CT       string // Content-Type request header
	Body     []byte // write requests only (first 1 MiB)
	BodyName string // metadata.name found in a write body (POST create has no name in the URL)
}

func (e gkaRecEntry) String() string { return e.Method + " " + e.Path }

func gkaRIsWrite(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func gkaRIsLease(path string) bool { return strings.Contains(path, "/apis/coordination.k8s.io/") }

// gkaRecorder records every request that enters the transport of a config made
// by gkaRecordingConfig, and can answer requests with injected responses.
type gkaRecorder struct {
	mu         sync.Mutex
	entries    []gkaRecEntry
	inject     func(req *http.Request) *http.Response
	injectErr  func(req *http.Request) error
	violations []string
	inflight   atomic.Int64 // requests inside RoundTrip (a watch counts until its headers arrive)
}

// gkaRecordingConfig copies base and wraps its transport (chaining an existing
// WrapTransport). The recorder is the outermost wrapper: it records a request
// before deciding to inject or forward it.
func gkaRecordingConfig(base *rest.Config) (*rest.Config, *gkaRecorder) {
	rec := &gkaRecorder{}
	cfg := rest.CopyConfig(base)
	cfg.WrapTransport = transport.Wrappers(base.WrapTransport, func(rt http.RoundTripper) http.RoundTripper {
		return &gkaRecordingRT{rec: rec, next: rt}
	})
	return cfg, rec
}

type gkaRecordingRT struct {
	rec  *gkaRecorder
	next http.RoundTripper
}

func (rt *gkaRecordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.rec.inflight.Add(1)
	defer rt.rec.inflight.Add(-1)
	q := req.URL.Query()
	e := gkaRecEntry{
		At: time.Now(), Method: req.Method, Path: req.URL.Path, RawQuery: req.URL.RawQuery,
		Watch: q.Get("watch") == "true" || q.Get("watch") == "1", CT: req.Header.Get("Content-Type"),
	}
	if gkaRIsWrite(req.Method) {
		e.Body = gkaRReqBody(req)
		e.BodyName = gkaRBodyName(e.Body)
	}
	rt.rec.mu.Lock()
	rt.rec.entries = append(rt.rec.entries, e)
	inj, injErr := rt.rec.inject, rt.rec.injectErr
	rt.rec.mu.Unlock()

	if injErr != nil {
		if err := injErr(req); err != nil {
			return nil, err
		}
	}
	if inj != nil {
		if resp := inj(req); resp != nil {
			if resp.Header != nil && resp.Header.Get("Retry-After") != "" {
				rt.rec.mu.Lock()
				rt.rec.violations = append(rt.rec.violations, "injected response for "+e.String()+" carries Retry-After (forbidden by GKA-175)")
				rt.rec.mu.Unlock()
				resp.Header.Del("Retry-After")
			}
			return resp, nil
		}
	}
	return rt.next.RoundTrip(req)
}

// gkaRReqBody reads a request body without consuming req.Body (client-go sets GetBody).
func gkaRReqBody(req *http.Request) []byte {
	if req.GetBody == nil {
		return nil
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
	return b
}

func gkaRBodyName(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	return v.Metadata.Name
}

// Inject installs fn as the response injector: a non-nil result answers the
// request without contacting the server, nil lets it through. The function runs
// outside the recorder lock and may sleep or call the API with another client.
// Injected responses must not carry Retry-After (GKA-175): the recorder strips it
// and records a violation. Inject(nil) removes the injector.
func (r *gkaRecorder) Inject(fn func(req *http.Request) *http.Response) {
	r.mu.Lock()
	r.inject = fn
	r.mu.Unlock()
}

// InjectErr installs a transport-level error injector (connection errors,
// timeouts): a non-nil error is returned from RoundTrip. nil removes it.
func (r *gkaRecorder) InjectErr(fn func(req *http.Request) error) {
	r.mu.Lock()
	r.injectErr = fn
	r.mu.Unlock()
}

// Entries returns a copy of every recorded request.
func (r *gkaRecorder) Entries() []gkaRecEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gkaRecEntry(nil), r.entries...)
}

// InFlight is the number of requests currently inside the transport.
func (r *gkaRecorder) InFlight() int64 { return r.inflight.Load() }

// Violations lists harness-rule violations seen by the recorder.
func (r *gkaRecorder) Violations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.violations...)
}

// gkaRMatch reports whether a recorded request belongs to pathPrefix: the URL
// path contains it, or a write body names an object starting with it (a POST
// create carries the name only in the body). Lease requests never match unless
// pathPrefix itself names coordination.k8s.io: Lease traffic is counted separately.
func gkaRMatch(e gkaRecEntry, pathPrefix string) bool {
	if gkaRIsLease(e.Path) && !strings.Contains(pathPrefix, "coordination.k8s.io") {
		return false
	}
	if pathPrefix == "" {
		return true
	}
	return strings.Contains(e.Path, pathPrefix) || (e.BodyName != "" && strings.HasPrefix(e.BodyName, pathPrefix))
}

// WriteEntries returns the write requests (POST, PUT, PATCH, DELETE) matching pathPrefix.
func (r *gkaRecorder) WriteEntries(pathPrefix string) []gkaRecEntry {
	var out []gkaRecEntry
	for _, e := range r.Entries() {
		if gkaRIsWrite(e.Method) && gkaRMatch(e, pathPrefix) {
			out = append(out, e)
		}
	}
	return out
}

// Writes returns "METHOD path" for the write requests matching pathPrefix.
// Matching: the URL path contains pathPrefix (a URL prefix and an object-name
// prefix both work), or a write body names an object with that name prefix. Lease
// requests are excluded (see LeaseRequests).
func (r *gkaRecorder) Writes(pathPrefix string) []string {
	var out []string
	for _, e := range r.WriteEntries(pathPrefix) {
		out = append(out, e.String())
	}
	return out
}

// LeaseRequests returns "METHOD path" for every Lease request (reads and writes).
func (r *gkaRecorder) LeaseRequests() []string {
	var out []string
	for _, e := range r.Entries() {
		if gkaRIsLease(e.Path) {
			out = append(out, e.String())
		}
	}
	return out
}

// All returns "METHOD path" for every request, reads and watches included.
func (r *gkaRecorder) All() []string {
	var out []string
	for _, e := range r.Entries() {
		out = append(out, e.String())
	}
	return out
}

// gkaRStatusResponse builds an injectable metav1.Status error response with no
// Retry-After header.
func gkaRStatusResponse(req *http.Request, code int, reason metav1.StatusReason, message string) *http.Response {
	st := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Message: message, Reason: reason, Code: int32(code), //nolint:gosec // HTTP codes fit int32
	}
	b, _ := json.Marshal(st)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h,
		Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b)), Request: req,
	}
}

// ---------------------------------------------------------------------------
// controller runner (GKA-173)
// ---------------------------------------------------------------------------

// gkaCtl is what gkaStartController returns. Done receives Run's result once
// (buffered) and is then closed.
type gkaCtl struct {
	Cancel func()
	Done   <-chan error
}

// gkaSyncBuf is a mutex protected log sink.
type gkaSyncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *gkaSyncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *gkaSyncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type gkaRCtlInfo struct {
	TestName     string
	ControllerID string
	LeaseID      string
	Namespace    string
	Logs         *gkaSyncBuf
	Options      controller.Options
	cancel       context.CancelFunc
	finished     chan struct{}
	returnedAt   atomic.Int64 // UnixNano when Run returned
}

var (
	gkaRCtlMu    sync.Mutex
	gkaRCtlByPtr = map[*gkaCtl]*gkaRCtlInfo{}
	gkaRCtlAll   []*gkaRCtlInfo
	gkaRCtlSeq   = map[string]int{}
)

// gkaStartController builds a controller with the GKA-173 defaults (ResyncInterval
// 200 ms, Lease 4 s / 3 s / 500 ms in namespace default, a per-test Lease ID, a
// per-instance ControllerID, ClusterID "gka-cluster", a mutex protected slog
// logger) applies mutate, and runs it in a goroutine. t.Cleanup cancels it and
// waits for Run to return (registered after gkaEnv's cleanup, so it runs first).
func gkaStartController(t *testing.T, cfg *rest.Config, a app.NodeAssessor, c controller.Clock, mutate func(*controller.Options)) *gkaCtl {
	t.Helper()
	gkaRAssertNoForeignLive(t)
	gkaRCtlMu.Lock()
	gkaRCtlSeq[t.Name()]++
	seq := gkaRCtlSeq[t.Name()]
	gkaRCtlMu.Unlock()

	logs := &gkaSyncBuf{}
	opts := controller.Options{
		RESTConfig:     cfg,
		Assessor:       a,
		Clock:          c,
		ControllerID:   gkaName(t, fmt.Sprintf("c%d", seq)),
		ClusterID:      "gka-cluster",
		ResyncInterval: 200 * time.Millisecond,
		LeaderElection: controller.LeaderElectionOptions{
			Namespace:     "default",
			ID:            gkaName(t, "lease"),
			LeaseDuration: 4 * time.Second,
			RenewDeadline: 3 * time.Second,
			RetryPeriod:   500 * time.Millisecond,
		},
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mutate != nil {
		mutate(&opts)
	}
	ctl, err := controller.New(opts)
	if err != nil {
		t.Fatalf("gkaStartController: controller.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	info := &gkaRCtlInfo{
		TestName: t.Name(), ControllerID: opts.ControllerID, LeaseID: opts.LeaderElection.ID,
		Namespace: opts.LeaderElection.Namespace, Logs: logs, Options: opts,
		cancel: cancel, finished: make(chan struct{}),
	}
	handle := &gkaCtl{Cancel: cancel, Done: done}
	gkaRCtlMu.Lock()
	gkaRCtlByPtr[handle] = info
	gkaRCtlAll = append(gkaRCtlAll, info)
	gkaRCtlMu.Unlock()
	go func() {
		err := ctl.Run(ctx)
		info.returnedAt.Store(time.Now().UnixNano())
		done <- err
		close(done)
		close(info.finished)
	}()
	t.Cleanup(func() { gkaRStop(t, info) })
	return handle
}

func gkaRStop(t *testing.T, info *gkaRCtlInfo) {
	t.Helper()
	info.cancel()
	select {
	case <-info.finished:
	case <-time.After(15 * time.Second):
		t.Errorf("controller %s: Run did not return within 15s after cancel (GKA-022(a) allows 10s)", info.ControllerID)
	}
}

func gkaRStopControllersOf(t *testing.T) {
	t.Helper()
	top := gkaRTopName(t.Name())
	topLevel := top == t.Name()
	gkaRCtlMu.Lock()
	infos := append([]*gkaRCtlInfo(nil), gkaRCtlAll...)
	gkaRCtlMu.Unlock()
	for _, info := range infos {
		if info.TestName == t.Name() || (topLevel && gkaRTopName(info.TestName) == top) {
			gkaRStop(t, info)
		}
	}
}

// gkaRAssertNoForeignLive fails when a controller from another top-level test is
// still running (GKA-173: controllers never overlap across tests).
func gkaRAssertNoForeignLive(t *testing.T) {
	t.Helper()
	top := gkaRTopName(t.Name())
	gkaRCtlMu.Lock()
	infos := append([]*gkaRCtlInfo(nil), gkaRCtlAll...)
	gkaRCtlMu.Unlock()
	for _, info := range infos {
		if gkaRTopName(info.TestName) == top {
			continue
		}
		select {
		case <-info.finished:
		default:
			t.Fatalf("gkaStartController: controller %s of test %s is still running", info.ControllerID, info.TestName)
		}
	}
}

func gkaRInfoOf(c *gkaCtl) *gkaRCtlInfo {
	gkaRCtlMu.Lock()
	defer gkaRCtlMu.Unlock()
	return gkaRCtlByPtr[c]
}

// gkaRCtlID is the ControllerID (Lease holderIdentity) of a started controller.
func gkaRCtlID(c *gkaCtl) string { return gkaRInfoOf(c).ControllerID }

// gkaRCtlLease is the Lease ID of a started controller.
func gkaRCtlLease(c *gkaCtl) string { return gkaRInfoOf(c).LeaseID }

// gkaRCtlLogs is everything the controller's slog logger wrote so far.
func gkaRCtlLogs(c *gkaCtl) string { return gkaRInfoOf(c).Logs.String() }

// gkaRCtlReturnedAt is the moment Run returned (zero while it is running).
func gkaRCtlReturnedAt(c *gkaCtl) time.Time {
	ns := gkaRInfoOf(c).returnedAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// ---------------------------------------------------------------------------
// probes: the harness self tests re-exec the test binary in probe mode
// ---------------------------------------------------------------------------

func gkaRunProbe(kind string) {
	var tests []testing.InternalTest
	switch kind {
	case "eventually-fail":
		tests = []testing.InternalTest{{Name: "ProbeEventuallyFail", F: func(t *testing.T) {
			gkaEventually(t, 300*time.Millisecond, func() (bool, string) { return false, "PROBE-LAST-DESC" })
		}}}
	case "eventually-ok":
		tests = []testing.InternalTest{{Name: "ProbeEventuallyOK", F: func(t *testing.T) {
			n := 0
			gkaEventually(t, 5*time.Second, func() (bool, string) { n++; return n >= 3, "polling" })
			t.Log("PROBE-EVENTUALLY-OK")
		}}}
	case "env-skip":
		tests = []testing.InternalTest{{Name: "ProbeEnvSkip", F: func(t *testing.T) {
			_ = gkaEnv(t)
			t.Log("PROBE-NOT-SKIPPED")
		}}}
	default:
		fmt.Fprintf(os.Stderr, "gka harness: unknown probe %q\n", kind)
		os.Exit(3)
	}
	//nolint:staticcheck // SA1019: the probe needs a real *testing.T outside the normal suite
	testing.Main(func(pat, str string) (bool, error) { return true, nil }, tests, nil, nil)
	os.Exit(2)
}
