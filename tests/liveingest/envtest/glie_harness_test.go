// Package liveingestenv_test is the envtest suite of gpu-fleet-live-ingest
// (spec v1.0, scenario group GLI-124 (g)): the Kubernetes implementation of the
// session store, node directory and leader gate (GLI-032, GLI-092, GLI-113), its
// coupling with the S3a status controller (GLI-033), the agent -> ingest server
// -> S3a controller end-to-end run (L-READY) and the leader blip (L-LEADER-BLIP),
// and the path-controller binary against a real API server.
//
// This file is the shared harness. Its helpers use the prefix glie (the shared
// helpers of tests/liveingest are in another package and cannot be imported).
// Harness rules (GLI-120, GKA-170..177):
//   - TestMain starts envtest once. KUBEBUILDER_ASSETS unset: one explicit log
//     line naming it, every envtest test skips (a skip is not a PASS).
//     KUBEBUILDER_ASSETS set but kube-apiserver or etcd missing there: the run
//     FAILS. deploy/crds is installed as it is and the three CRDs must be
//     Established before any test runs.
//   - tests never call t.Parallel; every test calls glieEnv first, which also
//     registers the end-of-test cleanup (stop what the test started, delete every
//     GPUFleet, GPUDevice, NodePathState, Node and Lease carrying the test's
//     name prefix and wait until they are gone);
//   - every object a test creates is named through glieName(t, suffix);
//   - injected 409/5xx responses never carry Retry-After (GKA-175).
package liveingestenv_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

const (
	glieProbeEnv = "GLIE_HARNESS_PROBE"
	glieCluster  = "glie-cluster"
	glieGPUUUID  = "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	glieGPUBDF   = "0000:03:00.0"
	glieBootID   = "3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90"
	glieNS       = "default"
)

// glieT0 is a whole-second instant (GKA-071/074).
var glieT0 = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

var glieShared *glieEnvT

type glieEnvT struct {
	Config    *rest.Config // copy of the envtest config (unwrapped: the test's own requests are not recorded)
	Client    client.Client
	Clientset kubernetes.Interface
}

func TestMain(m *testing.M) {
	if kind := os.Getenv(glieProbeEnv); kind != "" {
		glieRunProbe(kind) // never returns
		return
	}
	os.Exit(glieMain(m))
}

func glieMain(m *testing.M) int {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		fmt.Fprintln(os.Stderr, "glie harness: KUBEBUILDER_ASSETS is not set: envtest tests are SKIPPED (a skip is not a PASS; gated runs must set KUBEBUILDER_ASSETS)")
		return m.Run()
	}
	for _, bin := range []string{"kube-apiserver", "etcd"} {
		fi, err := os.Stat(filepath.Join(assets, bin))
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			fmt.Fprintf(os.Stderr, "glie harness: FAIL: KUBEBUILDER_ASSETS=%q is set but an executable %s is not there (GKA-171: failure, not skip)\n", assets, bin)
			return 1
		}
	}
	crdDir, err := filepath.Abs(filepath.Join("..", "..", "..", "deploy", "crds"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "glie harness: FAIL: cannot resolve deploy/crds: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "glie harness: FAIL: envtest start (CRDs from %s): %v\n", crdDir, err)
		_ = env.Stop()
		return 1
	}
	stop := func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "glie harness: envtest stop: %v\n", err)
		}
	}
	shared, err := glieBuildShared(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "glie harness: FAIL: %v\n", err)
		stop()
		return 1
	}
	if err := glieVerifyEstablished(shared.Client); err != nil {
		fmt.Fprintf(os.Stderr, "glie harness: FAIL: %v\n", err)
		stop()
		return 1
	}
	glieShared = shared
	code := m.Run()
	stop()
	return code
}

func glieBuildShared(cfg *rest.Config) (*glieEnvT, error) {
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
	return &glieEnvT{Config: cfg, Client: c, Clientset: cs}, nil
}

func glieVerifyEstablished(c client.Client) error {
	names := []string{
		"gpufleets.infrastructure.data-path-assurance.io",
		"gpudevices.infrastructure.data-path-assurance.io",
		"nodepathstates.infrastructure.data-path-assurance.io",
	}
	for _, n := range names {
		ok, last := glieWait(30*time.Second, func() (bool, string) {
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

// glieEnv returns the shared envtest handle and registers the end-of-test
// cleanup. Without KUBEBUILDER_ASSETS it skips with an explicit reason.
func glieEnv(t *testing.T) *glieEnvT {
	t.Helper()
	if glieShared == nil {
		t.Skip("glie harness: envtest unavailable: KUBEBUILDER_ASSETS is not set (this skip is NOT a PASS)")
	}
	e := &glieEnvT{Config: rest.CopyConfig(glieShared.Config), Client: glieShared.Client, Clientset: glieShared.Clientset}
	glieRegisterCleanup(t, e)
	return e
}

var glieCleanupRegistered sync.Map

func glieRegisterCleanup(t *testing.T, e *glieEnvT) {
	t.Helper()
	if _, loaded := glieCleanupRegistered.LoadOrStore(t.Name(), true); loaded {
		return
	}
	glieSweep(e, glieCleanupPrefix(t.Name()), 30*time.Second) // start-of-test sweep: a previous run leaves no residue
	t.Cleanup(func() {
		prefix := glieCleanupPrefix(t.Name())
		if left := glieSweep(e, prefix, 30*time.Second); len(left) > 0 {
			t.Errorf("glieCleanup: objects with prefix %q still exist after 30s: %v", prefix, left)
		}
		glieCleanupRegistered.Delete(t.Name())
	})
}

// ---------------------------------------------------------------------------
// names and waiting
// ---------------------------------------------------------------------------

var glieClauseRE = regexp.MustCompile(`^TestGLI(\d{3})`)

func glieHash(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}

func glieTopName(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// gliePrefixes returns the per-top-level-test and the per-test (subtest aware)
// name prefixes: lowercase DNS-1123 fragments of at most 22 bytes.
func gliePrefixes(testName string) (top, full string) {
	topName := glieTopName(testName)
	digits := ""
	if m := glieClauseRE.FindStringSubmatch(topName); m != nil {
		digits = m[1]
	}
	top = "l" + digits + "-" + glieHash(topName, 8)
	full = top
	if topName != testName {
		full = top + "s" + glieHash(testName, 8)
	}
	return top, full
}

func glieCleanupPrefix(testName string) string {
	top, full := gliePrefixes(testName)
	if strings.Contains(testName, "/") {
		return full
	}
	return top
}

func glieSanitize(s string) string {
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

// glieName returns a lowercase DNS-1123 name (at most 63 bytes) unique to the
// test and the suffix.
func glieName(t *testing.T, suffix string) string {
	t.Helper()
	top, full := gliePrefixes(t.Name())
	base := top
	if strings.Contains(t.Name(), "/") {
		base = full
	}
	s := glieSanitize(suffix)
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
	return base + "-" + cut + "-" + glieHash(suffix, 8)
}

// glieWait polls cond every 50 ms until it reports true or the timeout expires.
func glieWait(timeout time.Duration, cond func() (bool, string)) (bool, string) {
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

// glieEventually polls cond every 50 ms and fails the test with the last
// description when the timeout (at most 20 s, GLI-125) expires.
func glieEventually(t testing.TB, timeout time.Duration, cond func() (bool, string)) {
	t.Helper()
	if ok, last := glieWait(timeout, cond); !ok {
		t.Fatalf("glieEventually: condition not met within %s; last state: %s", timeout, last)
	}
}

func glieSweep(e *glieEnvT, prefix string, timeout time.Duration) []string {
	var left []string
	glieWait(timeout, func() (bool, string) {
		left = glieDeleteMatching(e, prefix)
		return len(left) == 0, fmt.Sprint(left)
	})
	return left
}

func glieDeleteMatching(e *glieEnvT, prefix string) []string {
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
// fake clock
// ---------------------------------------------------------------------------

// glieClock only moves when the test says so (controller.Clock,
// liveingest.Clock and liveclient.Clock are all `Now() time.Time`).
type glieClock struct {
	mu  sync.Mutex
	now time.Time
}

func newGlieClock(t0 time.Time) *glieClock { return &glieClock{now: t0} }

func (c *glieClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *glieClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// request recorder (GKA-175 observation seam)
// ---------------------------------------------------------------------------

type glieRecEntry struct {
	At       time.Time
	Method   string
	Path     string
	RawQuery string
	Watch    bool
	Body     []byte // write requests only (first 1 MiB)
}

func (e glieRecEntry) String() string { return e.Method + " " + e.Path }

func glieIsWrite(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func glieIsLease(path string) bool { return strings.Contains(path, "/apis/coordination.k8s.io/") }

// glieRecorder records every request that enters the transport of a config made
// by glieRecordingConfig and can answer requests with injected responses.
type glieRecorder struct {
	mu         sync.Mutex
	entries    []glieRecEntry
	inject     func(req *http.Request) *http.Response
	injectErr  func(req *http.Request) error
	violations []string
}

// glieRecordingConfig copies base and wraps its transport (chaining an existing
// WrapTransport). The recorder records a request before it decides to inject or
// forward it.
func glieRecordingConfig(base *rest.Config) (*rest.Config, *glieRecorder) {
	rec := &glieRecorder{}
	cfg := rest.CopyConfig(base)
	cfg.WrapTransport = transport.Wrappers(base.WrapTransport, func(rt http.RoundTripper) http.RoundTripper {
		return &glieRecordingRT{rec: rec, next: rt}
	})
	return cfg, rec
}

type glieRecordingRT struct {
	rec  *glieRecorder
	next http.RoundTripper
}

func (rt *glieRecordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	q := req.URL.Query()
	e := glieRecEntry{
		At: time.Now(), Method: req.Method, Path: req.URL.Path, RawQuery: req.URL.RawQuery,
		Watch: q.Get("watch") == "true" || q.Get("watch") == "1",
	}
	if glieIsWrite(req.Method) {
		e.Body = glieReqBody(req)
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

// glieReqBody reads a request body without consuming req.Body (client-go sets GetBody).
func glieReqBody(req *http.Request) []byte {
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

// Inject installs fn as the response injector: a non-nil result answers the
// request without contacting the server, nil lets it through. fn runs outside
// the recorder lock and may block or call the API with another client.
func (r *glieRecorder) Inject(fn func(req *http.Request) *http.Response) {
	r.mu.Lock()
	r.inject = fn
	r.mu.Unlock()
}

// InjectErr installs a transport-level error injector; nil removes it.
func (r *glieRecorder) InjectErr(fn func(req *http.Request) error) {
	r.mu.Lock()
	r.injectErr = fn
	r.mu.Unlock()
}

func (r *glieRecorder) Entries() []glieRecEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]glieRecEntry(nil), r.entries...)
}

func (r *glieRecorder) Violations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.violations...)
}

// StatusPUTs returns the recorded PUTs to <resource>/<name>/status.
func (r *glieRecorder) StatusPUTs(resource, name string) []glieRecEntry {
	suffix := "/" + resource + "/" + name + "/status"
	var out []glieRecEntry
	for _, e := range r.Entries() {
		if e.Method == http.MethodPut && strings.HasSuffix(e.Path, suffix) {
			out = append(out, e)
		}
	}
	return out
}

// SingleGETs returns the recorded non-watch GETs of exactly <resource>/<name>.
func (r *glieRecorder) SingleGETs(resource, name string) []glieRecEntry {
	suffix := "/" + resource + "/" + name
	var out []glieRecEntry
	for _, e := range r.Entries() {
		if e.Method == http.MethodGet && !e.Watch && strings.HasSuffix(e.Path, suffix) {
			out = append(out, e)
		}
	}
	return out
}

// Since returns the entries that entered the transport after at; Lease
// requests are left out when withoutLease is set.
func (r *glieRecorder) Since(at time.Time, withoutLease bool) []glieRecEntry {
	var out []glieRecEntry
	for _, e := range r.Entries() {
		if e.At.After(at) && !(withoutLease && glieIsLease(e.Path)) {
			out = append(out, e)
		}
	}
	return out
}

// glieStatusResponse builds an injectable metav1.Status error response without
// Retry-After and with the given message.
func glieStatusResponse(req *http.Request, code int, reason metav1.StatusReason, message string) *http.Response {
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

// glieClassify maps a request to (verb, resource[/subresource]) like GKA-175 (3):
// collection GET = list, watch=true GET = watch, single GET = get.
func glieClassify(method, path string, watch bool) (verb, resource string, discovery bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "", "", true
	}
	var rest []string
	switch segs[0] {
	case "version", "openapi", "healthz", "livez", "readyz":
		return "", "", true
	case "api":
		if len(segs) <= 2 {
			return "", "", true
		}
		rest = segs[2:]
	case "apis":
		if len(segs) <= 3 {
			return "", "", true
		}
		rest = segs[3:]
	default:
		return method, path, false
	}
	if rest[0] == "namespaces" && len(rest) >= 3 {
		rest = rest[2:]
	}
	resource = rest[0]
	name := ""
	if len(rest) > 1 {
		name = rest[1]
	}
	if len(rest) > 2 {
		resource += "/" + rest[2]
	}
	switch method {
	case http.MethodGet:
		switch {
		case watch:
			verb = "watch"
		case name == "":
			verb = "list"
		default:
			verb = "get"
		}
	case http.MethodPost:
		verb = "create"
	case http.MethodPut:
		verb = "update"
	case http.MethodPatch:
		verb = "patch"
	case http.MethodDelete:
		verb = "delete"
	default:
		verb = strings.ToLower(method)
	}
	return verb, resource, false
}

// ---------------------------------------------------------------------------
// PKI (ECDSA P-256, files in t.TempDir(), never printed)
// ---------------------------------------------------------------------------

type gliePKI struct {
	dir    string
	key    *ecdsa.PrivateKey
	cert   *x509.Certificate
	CAFile string
	CAPEM  []byte
	serial atomic.Int64
	files  atomic.Int64
}

type glieLeaf struct {
	Cert, Key string
	Leaf      *x509.Certificate
}

func glieWriteFile(t testing.TB, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func glieNewPKI(t *testing.T) *gliePKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "glie test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	p := &gliePKI{dir: t.TempDir(), key: key, cert: cert}
	p.serial.Store(1000)
	p.CAPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	p.CAFile = filepath.Join(p.dir, "ca.pem")
	glieWriteFile(t, p.CAFile, string(p.CAPEM), 0o644)
	return p
}

func (p *gliePKI) leaf(t *testing.T, base *x509.Certificate, mods ...func(*x509.Certificate)) glieLeaf {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	base.SerialNumber = big.NewInt(p.serial.Add(1))
	if base.NotBefore.IsZero() {
		base.NotBefore = time.Now().Add(-time.Hour)
	}
	if base.NotAfter.IsZero() {
		base.NotAfter = time.Now().AddDate(10, 0, 0) // far beyond anything a fake clock reaches (GLI-121)
	}
	base.KeyUsage = x509.KeyUsageDigitalSignature
	for _, m := range mods {
		m(base)
	}
	der, err := x509.CreateCertificate(rand.Reader, base, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	n := p.files.Add(1)
	l := glieLeaf{Cert: filepath.Join(p.dir, fmt.Sprintf("leaf-%d.crt", n)), Key: filepath.Join(p.dir, fmt.Sprintf("leaf-%d.key", n)), Leaf: parsed}
	glieWriteFile(t, l.Cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), 0o644)
	glieWriteFile(t, l.Key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), 0o600)
	return l
}

func (p *gliePKI) Server(t *testing.T, dns ...string) glieLeaf {
	t.Helper()
	if len(dns) == 0 {
		dns = []string{"localhost"}
	}
	return p.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "glie ingest server"}, DNSNames: dns, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
}

// Node issues a node client leaf: URI SAN spiffe://data-path-assurance.local/cluster/<cluster>/node/<uid> (GLI-021).
func (p *gliePKI) Node(t *testing.T, cluster, uid string) glieLeaf {
	t.Helper()
	u := &url.URL{Scheme: "spiffe", Host: "data-path-assurance.local", Path: "/cluster/" + cluster + "/node/" + uid}
	return p.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "glie node"}, URIs: []*url.URL{u}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
}

func glieInstall(t testing.TB, dstCert, dstKey string, l glieLeaf) {
	t.Helper()
	for _, c := range [][2]string{{l.Cert, dstCert}, {l.Key, dstKey}} {
		b, err := os.ReadFile(c[0])
		if err != nil {
			t.Fatalf("read %s: %v", c[0], err)
		}
		glieWriteFile(t, c[1], string(b), 0o600)
	}
}

// ---------------------------------------------------------------------------
// Kubernetes objects
// ---------------------------------------------------------------------------

func glieNode(t *testing.T, e *glieEnvT, name string, labels map[string]string) *corev1.Node {
	t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := e.Client.Create(context.Background(), n); err != nil {
		t.Fatalf("create Node %s: %v", name, err)
	}
	return n
}

// glieFleet creates an Audit GPUFleet selecting nodes by labels (the normative
// coverage, freshness 60 s and ready-for 30 s).
func glieFleet(t *testing.T, e *glieEnvT, name string, selector map[string]string) *v1alpha1.GPUFleet {
	t.Helper()
	fl := &v1alpha1.GPUFleet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUFleetSpec{
			NodeSelector: metav1.LabelSelector{MatchLabels: selector},
			RequiredCoverage: []v1alpha1.FleetCoverageRequirement{
				{Name: "pcie-parent", PathKind: v1alpha1.PathKindGPUPCIeParent, Required: true},
				{Name: "pcie-root", PathKind: v1alpha1.PathKindGPUPCIeRoot, Required: true},
				{Name: "pcie-width", PathKind: v1alpha1.PathKindGPUPCIeLinkWidthNormal, Required: true},
			},
			FreshnessSeconds: 60,
			ReadyForSeconds:  30,
			Mode:             v1alpha1.FleetModeAudit,
		},
	}
	if err := e.Client.Create(context.Background(), fl); err != nil {
		t.Fatalf("create GPUFleet %s: %v", name, err)
	}
	return fl
}

// glieDevice creates an InService GPUDevice on node whose inventory claim is the
// UUID the fake nvidia-smi reports.
func glieDevice(t *testing.T, e *glieEnvT, name string, node *corev1.Node, fl *v1alpha1.GPUFleet) *v1alpha1.GPUDevice {
	t.Helper()
	return glieDeviceUUID(t, e, name, node, fl, glieGPUUUID)
}

// glieDeviceUUID is glieDevice with another inventory UUID claim.
func glieDeviceUUID(t *testing.T, e *glieEnvT, name string, node *corev1.Node, fl *v1alpha1.GPUFleet, uuid string) *v1alpha1.GPUDevice {
	t.Helper()
	d := &v1alpha1.GPUDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUDeviceSpec{
			NodeRef:  v1alpha1.ObjectRef{Name: node.Name, UID: string(node.UID)},
			FleetRef: v1alpha1.ObjectRef{Name: fl.Name, UID: string(fl.UID)},
			InventoryClaim: v1alpha1.InventoryClaim{
				Vendor: "NVIDIA", UUID: &uuid, Source: "operator/asset-db", EvidenceID: "asset-db:" + name,
			},
			DesiredState: v1alpha1.DesiredStateInService,
			Request:      v1alpha1.LifecycleRequest{ID: "enroll-1", Reason: "initial enrollment"},
		},
	}
	if err := e.Client.Create(context.Background(), d); err != nil {
		t.Fatalf("create GPUDevice %s: %v", name, err)
	}
	return d
}

// glieNodePathState creates a NodePathState (spec.nodeRef only, owned by the
// Node) the way S3a does (GKA-100/101), without S3a running.
func glieNodePathState(t *testing.T, e *glieEnvT, node *corev1.Node) *v1alpha1.NodePathState {
	t.Helper()
	nps := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{
			Name:            node.Name,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}},
		},
		Spec: v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: node.Name, UID: string(node.UID)}},
	}
	if err := e.Client.Create(context.Background(), nps); err != nil {
		t.Fatalf("create NodePathState %s: %v", node.Name, err)
	}
	return nps
}

func glieGetNPS(e *glieEnvT, name string) (*v1alpha1.NodePathState, bool) {
	o := &v1alpha1.NodePathState{}
	if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, o); err != nil {
		return nil, false
	}
	return o, true
}

func glieMustNPS(t *testing.T, e *glieEnvT, name string) *v1alpha1.NodePathState {
	t.Helper()
	o, ok := glieGetNPS(e, name)
	if !ok {
		t.Fatalf("NodePathState %s does not exist", name)
	}
	return o
}

// glieRawNPSStatus returns the raw JSON status map of a NodePathState (to tell
// [] from null from absent).
func glieRawNPSStatus(t *testing.T, e *glieEnvT, name string) map[string]any {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("infrastructure.data-path-assurance.io/v1alpha1")
	u.SetKind("NodePathState")
	if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, u); err != nil {
		t.Fatalf("get NodePathState %s: %v", name, err)
	}
	st, _, _ := unstructured.NestedMap(u.Object, "status")
	return st
}

// glieSeedStatus writes a status as manager (an update of the status
// subresource, retried on conflicts).
func glieSeedStatus(t *testing.T, e *glieEnvT, name, manager string, mutate func(s *v1alpha1.NodePathStateStatus)) {
	t.Helper()
	glieEventually(t, 10*time.Second, func() (bool, string) {
		nps, ok := glieGetNPS(e, name)
		if !ok {
			return false, "NodePathState missing"
		}
		mutate(&nps.Status)
		err := e.Client.Status().Update(context.Background(), nps, client.FieldOwner(manager))
		return err == nil, fmt.Sprintf("seed status: %v", err)
	})
}

// glieSeedLease creates a Lease whose holder is holder, renewed now, valid for
// an hour.
func glieSeedLease(t *testing.T, e *glieEnvT, name, holder string) {
	t.Helper()
	dur := int32(3600)
	now := metav1.NewMicroTime(time.Now())
	l := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: glieNS},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &dur, RenewTime: &now, AcquireTime: &now},
	}
	if err := e.Client.Create(context.Background(), l); err != nil {
		t.Fatalf("create Lease %s: %v", name, err)
	}
}

// glieSetLeaseHolder rewrites the holder of a Lease and renews it (retrying on
// conflicts); holder nil clears holderIdentity (a released Lease).
func glieSetLeaseHolder(t testing.TB, e *glieEnvT, name string, holder *string) {
	t.Helper()
	glieEventually(t, 10*time.Second, func() (bool, string) {
		l, err := e.Clientset.CoordinationV1().Leases(glieNS).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		l.Spec.HolderIdentity = holder
		now := metav1.NewMicroTime(time.Now())
		l.Spec.RenewTime = &now
		dur := int32(3600)
		l.Spec.LeaseDurationSeconds = &dur
		_, err = e.Clientset.CoordinationV1().Leases(glieNS).Update(context.Background(), l, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("update Lease: %v", err)
	})
}

func glieStr(s string) *string { return &s }

var glieRevisionRE = regexp.MustCompile(`^([0-9]+):([0-9]+):([0-9a-f]{64})$`)

// glieRevision parses a graphRevision `<session>:<sequence>:<digest>`.
func glieRevision(rev string) (session, sequence int64, ok bool) {
	m := glieRevisionRE.FindStringSubmatch(rev)
	if m == nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscan(m[1], &session); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscan(m[2], &sequence); err != nil {
		return 0, 0, false
	}
	return session, sequence, true
}

// ---------------------------------------------------------------------------
// shared logger and listening helpers
// ---------------------------------------------------------------------------

type glieSyncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *glieSyncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *glieSyncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func glieLogger(buf *glieSyncBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func glieFreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// ---------------------------------------------------------------------------
// harness self tests: the probes re-execute this test binary (GKA-171, GLI-120)
// ---------------------------------------------------------------------------

func glieRunProbe(kind string) {
	var tests []testing.InternalTest
	switch kind {
	case "env-skip":
		tests = []testing.InternalTest{{Name: "ProbeEnvSkip", F: func(t *testing.T) {
			_ = glieEnv(t)
			t.Log("PROBE-NOT-SKIPPED")
		}}}
	default:
		fmt.Fprintf(os.Stderr, "glie harness: unknown probe %q\n", kind)
		os.Exit(3)
	}
	//nolint:staticcheck // SA1019: the probe needs a real *testing.T outside the normal suite
	testing.Main(func(pat, str string) (bool, error) { return true, nil }, tests, nil, nil)
	os.Exit(2)
}

func glieRunSelf(t *testing.T, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, args...)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KUBEBUILDER_ASSETS=") || strings.HasPrefix(kv, glieProbeEnv+"=") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	case err != nil:
		t.Fatalf("run %s: %v", exe, err)
	}
	return string(out), 0
}

func glieFakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // a fake executable on purpose
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// GLI-120 (GKA-171): KUBEBUILDER_ASSETS unset -> the package still runs and every
// envtest test skips with a log naming the variable; set but without
// kube-apiserver or etcd -> the run FAILS and says why.
func TestGLI120_UnsetAssetsSkipWithAnExplicitLog(t *testing.T) {
	out, code := glieRunSelf(t, nil, "-test.run=^$", "-test.v")
	if code != 0 {
		t.Errorf("GLI-120: with KUBEBUILDER_ASSETS unset the package must still run (envtest tests skip); exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "KUBEBUILDER_ASSETS") || !strings.Contains(out, "SKIPPED") {
		t.Errorf("GLI-120: the unset case must log that envtest tests are skipped and name KUBEBUILDER_ASSETS; output:\n%s", out)
	}
	out, code = glieRunSelf(t, []string{glieProbeEnv + "=env-skip"}, "-test.v")
	if code != 0 || !strings.Contains(out, "SKIP") || !strings.Contains(out, "KUBEBUILDER_ASSETS") || strings.Contains(out, "PROBE-NOT-SKIPPED") {
		t.Errorf("GLI-120: glieEnv must t.Skip with a reason naming KUBEBUILDER_ASSETS when unset; exit %d:\n%s", code, out)
	}
}

func TestGLI120_AssetsSetButBinariesMissingFail(t *testing.T) {
	empty := t.TempDir()
	onlyAPIServer := t.TempDir()
	glieFakeBinary(t, onlyAPIServer, "kube-apiserver")
	onlyEtcd := t.TempDir()
	glieFakeBinary(t, onlyEtcd, "etcd")
	cases := map[string]string{
		"empty directory":      empty,
		"kube-apiserver only":  onlyAPIServer,
		"etcd only":            onlyEtcd,
		"directory is missing": filepath.Join(empty, "does-not-exist"),
		"a file, not a folder": filepath.Join(onlyEtcd, "etcd"),
	}
	for name, dir := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			out, code := glieRunSelf(t, []string{"KUBEBUILDER_ASSETS=" + dir}, "-test.run=^$", "-test.v")
			if code == 0 {
				t.Errorf("GLI-120: KUBEBUILDER_ASSETS=%s (%s) must FAIL the run, got exit 0:\n%s", dir, name, out)
			}
			if !strings.Contains(out, "KUBEBUILDER_ASSETS") {
				t.Errorf("GLI-120: the failure must say what is wrong (KUBEBUILDER_ASSETS); output:\n%s", out)
			}
			if strings.Contains(out, "SKIP") {
				t.Errorf("GLI-120: a missing binary must never end as a skip:\n%s", out)
			}
		})
	}
}

// GLI-120/121: the suite's own sources never name the repository fixture
// directory and never call t.Parallel (envtest tests share one API server).
func TestGLI120_SuiteSourcesFollowTheHarnessRules(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test sources found: %v", err)
	}
	forbiddenDir := "test" + "data"
	parallel := "t.Para" + "llel("
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(b)), forbiddenDir+"/") || strings.Contains(strings.ToLower(string(b)), "\""+forbiddenDir) {
			t.Errorf("GLI-120: %s names the repository fixture directory; inputs come from code or t.TempDir()", f)
		}
		if strings.Contains(string(b), parallel) {
			t.Errorf("GLI-120: %s calls t.Parallel; envtest tests never run in parallel", f)
		}
	}
}
