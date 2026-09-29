package kubeapi_test

// GKA-170..178: the harness rules themselves. Static rules (naming, no
// t.Parallel, Eventually bounds, no repository test-data directory) are checked on
// the sources of this directory with go/parser, so they bind every test file of this package.
// The KUBEBUILDER_ASSETS gate (GKA-171) and the failure path of gkaEventually
// (GKA-177) are exercised by re-executing this test binary.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// static source rules
// ---------------------------------------------------------------------------

func gkaRParseSources(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	names, err := filepath.Glob("*_test.go")
	if err != nil || len(names) == 0 {
		t.Fatalf("no test sources found in the package directory: %v", err)
	}
	files := map[string]*ast.File{}
	for _, n := range names {
		f, err := parser.ParseFile(fset, n, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		files[n] = f
	}
	return fset, files
}

func TestGKA170_FilesNamesAndHelperPrefixFollowTheHarnessRules(t *testing.T) {
	_, files := gkaRParseSources(t)
	testName := regexp.MustCompile(`^TestGKA[0-9]{3}_[A-Za-z0-9_]+$`)
	for name, f := range files {
		if f.Name.Name != "kubeapi_test" {
			t.Errorf("GKA-170: %s is in package %q, want kubeapi_test", name, f.Name.Name)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil {
					continue
				}
				n := d.Name.Name
				switch {
				case n == "TestMain":
				case strings.HasPrefix(n, "Test"):
					if !testName.MatchString(n) {
						t.Errorf("GKA-170: %s: test %q does not match TestGKA0NN_..", name, n)
					}
				case !strings.HasPrefix(n, "gka") && !strings.HasPrefix(n, "newGka"): // newGka*: constructors named by the harness API
					t.Errorf("GKA-170: %s: helper func %q lacks the gka prefix", name, n)
				}
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				for _, sp := range d.Specs {
					var names []*ast.Ident
					switch sp := sp.(type) {
					case *ast.TypeSpec:
						names = append(names, sp.Name)
					case *ast.ValueSpec:
						names = append(names, sp.Names...)
					}
					for _, id := range names {
						if id.Name != "_" && !strings.HasPrefix(id.Name, "gka") {
							t.Errorf("GKA-170: %s: top-level identifier %q lacks the gka prefix", name, id.Name)
						}
					}
				}
			}
		}
	}
}

func TestGKA173_NoTestCallsTParallel(t *testing.T) {
	fset, files := gkaRParseSources(t)
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Parallel" {
					t.Errorf("GKA-173: %s calls .Parallel() at %s; tests/kubeapi never runs tests in parallel", name, fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
}

// gkaRDurationLiteral evaluates N * time.Second (or Millisecond/Minute).
func gkaRDurationLiteral(e ast.Expr) (time.Duration, bool) {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.MUL {
		return 0, false
	}
	lit, ok := bin.X.(*ast.BasicLit)
	sel, ok2 := bin.Y.(*ast.SelectorExpr)
	if !ok || !ok2 || (lit.Kind != token.INT && lit.Kind != token.FLOAT) {
		return 0, false
	}
	n, err := strconv.ParseFloat(lit.Value, 64)
	if err != nil {
		return 0, false
	}
	unit := map[string]time.Duration{"Second": time.Second, "Millisecond": time.Millisecond, "Minute": time.Minute}[sel.Sel.Name]
	if unit == 0 {
		return 0, false
	}
	return time.Duration(n * float64(unit)), true
}

func TestGKA177_EventuallyTimeoutsStayWithinTheBudget(t *testing.T) {
	fset, files := gkaRParseSources(t)
	checked := 0
	for name, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			limit := 20 * time.Second
			if strings.Contains(fd.Name.Name, "GKA063") {
				limit = 90 * time.Second // the 1025-Node capacity test only
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "gkaEventually" || len(call.Args) < 2 {
					return true
				}
				checked++
				if d, ok := gkaRDurationLiteral(call.Args[1]); ok && d > limit {
					t.Errorf("GKA-177: %s: gkaEventually timeout %s at %s exceeds the %s budget", name, d, fset.Position(call.Pos()), limit)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Error("GKA-177: no gkaEventually call found to check")
	}
}

func TestGKA178_NoTestReadsTheRepositoryTestDataDirectory(t *testing.T) {
	fset, files := gkaRParseSources(t)
	forbidden := "test" + "data"
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(strings.ToLower(lit.Value), forbidden) {
				t.Errorf("GKA-178: %s: string literal at %s names the repository fixture directory; inputs come from code or t.TempDir()", name, fset.Position(lit.Pos()))
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// GKA-171: KUBEBUILDER_ASSETS gate (re-executes this test binary)
// ---------------------------------------------------------------------------

func gkaRRunSelf(t *testing.T, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, args...)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KUBEBUILDER_ASSETS=") || strings.HasPrefix(kv, gkaProbeEnv+"=") {
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

func gkaRFakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // a fake executable on purpose
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func TestGKA171_UnsetAssetsSkipWithAnExplicitLog(t *testing.T) {
	out, code := gkaRRunSelf(t, nil, "-test.run=^$", "-test.v")
	if code != 0 {
		t.Errorf("GKA-171: with KUBEBUILDER_ASSETS unset the package must still run (envtest tests skip); exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "KUBEBUILDER_ASSETS") || !strings.Contains(out, "SKIPPED") {
		t.Errorf("GKA-171: the unset case must log that envtest tests are skipped and name KUBEBUILDER_ASSETS; output:\n%s", out)
	}
	out, code = gkaRRunSelf(t, []string{gkaProbeEnv + "=env-skip"}, "-test.v")
	if code != 0 || !strings.Contains(out, "SKIP") || !strings.Contains(out, "KUBEBUILDER_ASSETS") || strings.Contains(out, "PROBE-NOT-SKIPPED") {
		t.Errorf("GKA-171: gkaEnv must t.Skip with a reason naming KUBEBUILDER_ASSETS when unset; exit %d:\n%s", code, out)
	}
}

func TestGKA171_AssetsSetButBinariesMissingFail(t *testing.T) {
	empty := t.TempDir()
	onlyAPIServer := t.TempDir()
	gkaRFakeBinary(t, onlyAPIServer, "kube-apiserver")
	onlyEtcd := t.TempDir()
	gkaRFakeBinary(t, onlyEtcd, "etcd")
	cases := map[string]string{
		"empty directory":      empty,
		"kube-apiserver only":  onlyAPIServer,
		"etcd only":            onlyEtcd,
		"directory is missing": filepath.Join(empty, "does-not-exist"),
		"a file, not a folder": filepath.Join(onlyEtcd, "etcd"),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			out, code := gkaRRunSelf(t, []string{"KUBEBUILDER_ASSETS=" + dir}, "-test.run=^$", "-test.v")
			if code == 0 {
				t.Errorf("GKA-171: KUBEBUILDER_ASSETS=%s (%s) must FAIL the run, got exit 0:\n%s", dir, name, out)
			}
			if !strings.Contains(out, "KUBEBUILDER_ASSETS") {
				t.Errorf("GKA-171: the failure must say what is wrong (KUBEBUILDER_ASSETS); output:\n%s", out)
			}
			if strings.Contains(out, "SKIP") {
				t.Errorf("GKA-171: a missing binary must never end as a skip:\n%s", out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GKA-172
// ---------------------------------------------------------------------------

func TestGKA172_CRDsFromDeployCRDsAreEstablishedFirst(t *testing.T) {
	e := gkaEnv(t)
	dir := filepath.Join("..", "..", "deploy", "crds")
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("GKA-172: no checked-in CRD files under %s (%v)", dir, err)
	}
	for _, name := range []string{
		"gpufleets.infrastructure.data-path-assurance.io", "gpudevices.infrastructure.data-path-assurance.io", "nodepathstates.infrastructure.data-path-assurance.io",
	} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
		if err := e.Client.Get(context.Background(), client.ObjectKey{Name: name}, u); err != nil {
			t.Errorf("GKA-172: CRD %s is not installed: %v", name, err)
			continue
		}
		established := false
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, ci := range conds {
			if cm, _ := ci.(map[string]interface{}); cm["type"] == "Established" && cm["status"] == "True" {
				established = true
			}
		}
		if !established {
			t.Errorf("GKA-172: CRD %s is not Established", name)
		}
	}
	// The harness installs the checked-in directory and fails when it is missing.
	src, err := os.ReadFile("harness_test.go")
	if err != nil {
		t.Fatalf("read harness: %v", err)
	}
	if !regexp.MustCompile(`ErrorIfCRDPathMissing:\s+true`).Match(src) || !strings.Contains(string(src), `"deploy", "crds"`) {
		t.Errorf("GKA-172: the harness must install deploy/crds with ErrorIfCRDPathMissing")
	}
}

// ---------------------------------------------------------------------------
// GKA-173
// ---------------------------------------------------------------------------

func TestGKA173_NamesAreDNS1123UniquePerTestAndSuffix(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	tests := []string{"TestGKA120_A", "TestGKA120_B", "TestGKA121_A", "TestOther", "TestGKA120_A/sub1", "TestGKA120_A/sub2", "TestGKA120_A/sub1/deeper", "Test" + strings.Repeat("X", 200)}
	suffixes := []string{"n", "lease", "fl", "UPPER.Case", strings.Repeat("s", 200), strings.Repeat("s", 199) + "t", "a-very-long-suffix-that-clearly-exceeds-the-sixty-three-character-limit-of-a-name"}
	seen := map[string]string{}
	for _, tn := range tests {
		for _, sfx := range suffixes {
			name := gkaRNameFor(tn, sfx)
			key := tn + "|" + sfx
			switch {
			case len(name) > 63:
				t.Errorf("GKA-173: %s -> %q is %d bytes, want <= 63", key, name, len(name))
			case !re.MatchString(name):
				t.Errorf("GKA-173: %s -> %q is not a lowercase DNS-1123 label", key, name)
			}
			if prev, dup := seen[name]; dup {
				t.Errorf("GKA-173: %s and %s both map to %q", prev, key, name)
			}
			seen[name] = key
			if again := gkaRNameFor(tn, sfx); again != name {
				t.Errorf("GKA-173: gkaName is not deterministic: %q then %q", name, again)
			}
		}
	}
	top, full := gkaRPrefixes("TestGKA120_A/sub1")
	if !strings.HasPrefix(gkaRNameFor("TestGKA120_A/sub1", "n"), full) || !strings.HasPrefix(full, top) {
		t.Errorf("GKA-173: subtest names must carry the top-level prefix %q and their own prefix %q", top, full)
	}
	if gkaRCleanupPrefix("TestGKA120_A") != top || gkaRCleanupPrefix("TestGKA120_A/sub1") != full {
		t.Errorf("GKA-173: cleanup prefixes: a top-level test sweeps %q, a subtest only %q", top, full)
	}
	for _, other := range []string{"TestGKA120_B", "TestGKA121_A", "TestOther"} {
		if o, _ := gkaRPrefixes(other); strings.HasPrefix(gkaRNameFor(other, "n"), top) || o == top {
			t.Errorf("GKA-173: %s shares the sweep prefix of TestGKA120_A", other)
		}
	}
}

func TestGKA173_StartControllerDefaultsAndIsolation(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	rig := gkaRStart(t, e, nil, nil)
	id, lease := gkaRCtlID(rig.Ctl), gkaRCtlLease(rig.Ctl)
	if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(lease) || len(lease) > 253 {
		t.Errorf("GKA-173: Lease ID %q must be lowercase [a-z0-9-] and a valid subdomain", lease)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`).MatchString(id) {
		t.Errorf("GKA-173: ControllerID %q violates the option pattern", id)
	}
	gkaRWaitHolder(t, e, "default", lease, id, 15*time.Second)
	l, _ := gkaRLease(e, "default", lease)
	if l == nil || l.Spec.LeaseDurationSeconds == nil || *l.Spec.LeaseDurationSeconds != 4 {
		t.Errorf("GKA-173: default LeaseDuration must be 4s, Lease = %+v", l)
	}
	gkaRWaitSettled(t, e, sc)
	// ResyncInterval 200ms: about ten passes in two seconds.
	c0 := rig.A.AssessCount()
	time.Sleep(2 * time.Second)
	if n := rig.A.AssessCount() - c0; n < 5 || n > 20 {
		t.Errorf("GKA-173: %d passes in 2s with the default 200ms ResyncInterval, want about 10", n)
	}
	// A second controller of the same test shares the Lease and has its own identity.
	second := gkaRStart(t, e, nil, nil)
	if gkaRCtlID(second.Ctl) == id || gkaRCtlLease(second.Ctl) != lease {
		t.Errorf("GKA-173: second controller has ID %q lease %q, want a distinct ID on the same Lease %q", gkaRCtlID(second.Ctl), gkaRCtlLease(second.Ctl), lease)
	}
}

func TestGKA173_CleanupAllRemovesOnlyThisTestsObjectsAndStopsControllers(t *testing.T) {
	e := gkaEnv(t)
	ctx := context.Background()
	other := gkaRNode(t, e, "zzother-"+gkaRHash(t.Name(), 8), nil) // not created through gkaName
	t.Cleanup(func() { _ = e.Client.Delete(ctx, other) })

	n := gkaRNode(t, e, gkaName(t, "n"), nil)
	gkaRRetry(t, "add a finalizer", func() error {
		cur := gkaRGetNode(t, e, n.Name)
		cur.Finalizers = []string{"gka.io/hold"}
		return e.Client.Update(ctx, cur)
	})
	fl := gkaRFleet(t, e, gkaName(t, "fl"), map[string]string{"gka/x": "y"}, v1alpha1.FleetModeAudit)
	gkaRDevice(t, e, gkaName(t, "d"), n, fl)
	nps := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{Name: gkaName(t, "nps")},
		Spec:       v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: n.Name, UID: string(n.UID)}},
	}
	if err := e.Client.Create(ctx, nps); err != nil {
		t.Fatalf("create NodePathState: %v", err)
	}
	gkaRForeignLease(t, e, "default", gkaName(t, "lease"), "someone")
	ctl := gkaStartController(t, gkaRClosedPortConfig(t), newGkaFakeAssessor(), nil, nil)

	gkaCleanupAll(t, e)
	select {
	case err := <-ctl.Done:
		if err != nil {
			t.Errorf("GKA-173: the controller stopped by cleanup returned %v, want nil", err)
		}
	default:
		t.Errorf("GKA-173: gkaCleanupAll must stop the controllers the test started before deleting anything")
	}
	prefix := gkaRCleanupPrefix(t.Name())
	if left := gkaRDeleteMatching(e, prefix); len(left) != 0 {
		t.Errorf("GKA-173: objects survived gkaCleanupAll: %v", left)
	}
	if err := e.Client.Get(ctx, client.ObjectKey{Name: other.Name}, &corev1.Node{}); err != nil {
		t.Errorf("GKA-173: an object outside the test's prefix was deleted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GKA-174 fakes
// ---------------------------------------------------------------------------

func gkaRSampleRequest(uid string) app.NodeAssessmentRequest {
	return app.NodeAssessmentRequest{
		Node:    fleet.NodeRef{ClusterID: "c", Name: "node-" + uid, UID: uid},
		Policy:  fleet.Policy{Revision: "rev", RequiredCoverage: []fleet.CoverageRequirement{{Name: "cov-a"}, {Name: "cov-b"}}},
		Intents: []fleet.Intent{{RequestID: "r1"}, {RequestID: "r2"}},
	}
}

func TestGKA174_FakeAssessorRecordsInOrderScriptsAndCopies(t *testing.T) {
	f := newGkaFakeAssessor()
	ctx := context.Background()

	got, err := f.AssessNode(ctx, gkaRSampleRequest("u1"))
	if err != nil || got.Node != nil || got.Observation != nil || len(got.Devices) != 0 {
		t.Fatalf("GKA-174: default script = (%+v, %v), want the zero NodeAssessment (no observation) and nil", got, err)
	}
	f.ForgetNode("u1")
	boom := errors.New("scripted failure")
	f.SetScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		if req.Node.UID == "bad" {
			return app.NodeAssessment{}, boom
		}
		return app.NodeAssessment{Observation: &app.NodeObservation{GraphRevision: "g-" + req.Node.UID}}, nil
	})
	if got, err := f.AssessNode(ctx, gkaRSampleRequest("u2")); err != nil || got.Observation == nil || got.Observation.GraphRevision != "g-u2" {
		t.Errorf("GKA-174: scripted result = (%+v, %v)", got, err)
	}
	if _, err := f.AssessNode(ctx, gkaRSampleRequest("bad")); !errors.Is(err, boom) {
		t.Errorf("GKA-174: scripted error = %v, want %v", err, boom)
	}
	f.ForgetNode("u2")
	f.ForgetNode("u2")
	f.ForgetNode("")

	log := f.Log()
	var kinds []string
	for _, c := range log {
		kinds = append(kinds, c.Kind+":"+c.NodeUID)
	}
	want := []string{"assess:u1", "forget:u1", "assess:u2", "assess:bad", "forget:u2", "forget:u2", "forget:"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("GKA-174: ordered call log = %v, want %v", kinds, want)
	}
	if fg := f.Forgotten(); strings.Join(fg, ",") != "u1,u2,u2," || f.AssessCount() != 3 {
		t.Errorf("GKA-174: Forgotten=%v AssessCount=%d", fg, f.AssessCount())
	}

	// Requests are deep copies in both directions.
	req := gkaRSampleRequest("u3")
	if _, err := f.AssessNode(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Intents[0].RequestID = "changed-by-caller"
	req.Policy.RequiredCoverage[0].Name = "changed-by-caller"
	recorded := f.Requests()
	last := recorded[len(recorded)-1]
	if last.Intents[0].RequestID != "r1" || last.Policy.RequiredCoverage[0].Name != "cov-a" {
		t.Errorf("GKA-174: a caller mutation after the call changed the recorded request: %+v", last)
	}
	last.Intents[0].RequestID = "changed-by-reader"
	if again := f.Requests(); again[len(again)-1].Intents[0].RequestID != "r1" {
		t.Errorf("GKA-174: Requests() returned storage shared with the fake")
	}

	// Context deadline is recorded; SetScriptCtx takes precedence and sees the context.
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	f.SetScriptCtx(func(c context.Context, req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		if _, ok := c.Deadline(); !ok {
			return app.NodeAssessment{}, errors.New("no deadline")
		}
		return app.NodeAssessment{}, nil
	})
	if _, err := f.AssessNode(dctx, gkaRSampleRequest("u4")); err != nil {
		t.Errorf("GKA-174: SetScriptCtx script = %v", err)
	}
	if lg := f.Log(); !lg[len(lg)-1].HasDeadline || lg[len(lg)-1].Remaining > 5*time.Second || lg[len(lg)-1].Remaining < 3*time.Second {
		t.Errorf("GKA-174: deadline entry = %+v", lg[len(lg)-1])
	}
	f.SetScriptCtx(nil)
	f.SetScript(nil)
	if got, err := f.AssessNode(ctx, gkaRSampleRequest("u5")); err != nil || got.Observation != nil {
		t.Errorf("GKA-174: SetScript(nil) must restore the no-observation default, got (%+v, %v)", got, err)
	}
}

// Concurrent use is race free (this test is only meaningful under -race).
func TestGKA174_FakesAreSafeForConcurrentUse(t *testing.T) {
	f := newGkaFakeAssessor()
	clk := newGkaFakeClock(gkaRT0)
	const workers, iterations = 16, 60
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (w + i) % 5 {
				case 0:
					_, _ = f.AssessNode(context.Background(), gkaRSampleRequest("u"+strconv.Itoa(w)))
				case 1:
					f.ForgetNode("u" + strconv.Itoa(w))
				case 2:
					_ = f.Requests()
					_ = f.Forgotten()
					_ = f.Log()
				case 3:
					f.SetScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) { return app.NodeAssessment{}, nil })
				case 4:
					clk.Advance(time.Millisecond)
					_ = clk.Now()
					_ = clk.Calls()
				}
			}
		}(w)
	}
	wg.Wait()
	if got, want := f.AssessCount(), workers*iterations/5; got != want {
		t.Errorf("GKA-174: %d assess calls recorded, want %d", got, want)
	}
	if got, want := len(f.Forgotten()), workers*iterations/5; got != want {
		t.Errorf("GKA-174: %d forget calls recorded, want %d", got, want)
	}
}

func TestGKA174_FakeClockMovesOnlyWhenToldTo(t *testing.T) {
	c := newGkaFakeClock(gkaRT0)
	if first, second := c.Now(), c.Now(); !first.Equal(gkaRT0) || !second.Equal(gkaRT0) { // two reads: it does not move between them
		t.Errorf("GKA-174: a fake clock must not move by itself (%s, %s)", first, second)
	}
	c.Advance(90 * time.Minute)
	if want := gkaRT0.Add(90 * time.Minute); !c.Now().Equal(want) {
		t.Errorf("GKA-174: after Advance Now = %s, want %s", c.Now(), want)
	}
	back := gkaRT0.Add(-time.Hour)
	c.Set(back)
	if !c.Now().Equal(back) {
		t.Errorf("GKA-174: Set must move the clock to an arbitrary instant")
	}
	if c.Calls() != 4 {
		t.Errorf("GKA-174: Calls = %d, want the 4 Now() reads above", c.Calls())
	}
	var wg sync.WaitGroup
	base := c.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Advance(time.Second)
				_ = c.Now()
			}
		}()
	}
	wg.Wait()
	if want := base.Add(800 * time.Second); !c.Now().Equal(want) {
		t.Errorf("GKA-174: concurrent Advance lost updates: %s, want %s", c.Now(), want)
	}
}

// ---------------------------------------------------------------------------
// GKA-175 observation method
// ---------------------------------------------------------------------------

func TestGKA175_VerbClassification(t *testing.T) {
	const grp = "/apis/infrastructure.data-path-assurance.io/v1alpha1"
	cases := []struct {
		method, path string
		watch        bool
		want         string
		discovery    bool
	}{
		{"GET", "/api/v1/nodes", false, "list nodes", false},
		{"GET", "/api/v1/nodes", true, "watch nodes", false},
		{"GET", "/api/v1/nodes/n1", false, "get nodes", false},
		{"PATCH", "/api/v1/nodes/n1/status", false, "patch nodes/status", false},
		{"PUT", "/api/v1/nodes/n1", false, "update nodes", false},
		{"PUT", grp + "/gpudevices/d/status", false, "update gpudevices/status", false},
		{"GET", grp + "/gpufleets", false, "list gpufleets", false},
		{"POST", grp + "/nodepathstates", false, "create nodepathstates", false},
		{"DELETE", grp + "/nodepathstates/n", false, "delete nodepathstates", false},
		{"DELETE", grp + "/nodepathstates", false, "deletecollection nodepathstates", false},
		{"GET", "/apis/coordination.k8s.io/v1/namespaces/default/leases/l", false, "get leases", false},
		{"PUT", "/apis/coordination.k8s.io/v1/namespaces/default/leases/l", false, "update leases", false},
		{"POST", "/apis/coordination.k8s.io/v1/namespaces/default/leases", false, "create leases", false},
		{"POST", "/api/v1/namespaces/default/events", false, "create events", false},
		{"GET", "/api", false, "", true},
		{"GET", "/apis", false, "", true},
		{"GET", "/api/v1", false, "", true},
		{"GET", grp, false, "", true},
		{"GET", "/apis/coordination.k8s.io/v1", false, "", true},
		{"GET", "/version", false, "", true},
		{"GET", "/openapi/v3", false, "", true},
	}
	for _, tc := range cases {
		got := gkaRClassify(tc.method, tc.path, tc.watch)
		if got.Discovery != tc.discovery || (!tc.discovery && got.key() != tc.want) {
			t.Errorf("GKA-175 (3): %s %s watch=%v -> %+v, want %q discovery=%v", tc.method, tc.path, tc.watch, got, tc.want, tc.discovery)
		}
	}
	if r := gkaRClassify("GET", "/somewhere/else", false); !r.Unknown {
		t.Errorf("GKA-175: an unrecognised path must be reported as unknown, got %+v", r)
	}
	entries := []gkaRecEntry{
		{Method: "GET", Path: "/api/v1/nodes"}, {Method: "GET", Path: "/apis"},
		{Method: "PUT", Path: "/api/v1/nodes/n1"}, {Method: "POST", Path: "/api/v1/namespaces/default/events"},
	}
	if v := gkaRVerbViolations(entries); len(v) != 2 {
		t.Errorf("GKA-128/175: violations = %v, want the Node update and the Event create only", v)
	}
}

func TestGKA175_RecorderRecordsFiltersAndInjects(t *testing.T) {
	var hits sync.Map
	var hitCount int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitCount++
		mu.Unlock()
		hits.Store(r.Method+" "+r.URL.Path, r.Header.Get("X-Gka-Base"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()
	served := func() int64 {
		mu.Lock()
		defer mu.Unlock()
		return hitCount
	}

	// A base WrapTransport that is already there stays in the chain.
	base := &rest.Config{Host: srv.URL}
	base.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return gkaRRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			r.Header.Set("X-Gka-Base", "1")
			return rt.RoundTrip(r)
		})
	}
	cfg, rec := gkaRecordingConfig(base)
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatalf("HTTPClientFor: %v", err)
	}
	do := func(method, path, body string) *http.Response {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	const grp = "/apis/infrastructure.data-path-assurance.io/v1alpha1"
	do("GET", "/api/v1/nodes", "")
	do("GET", "/api/v1/nodes?watch=true", "")
	do("GET", "/api/v1/nodes/x", "")
	do("PUT", grp+"/gpudevices/abc/status", `{"metadata":{"name":"abc"}}`)
	do("PATCH", "/api/v1/nodes/x/status", `{"kind":"Node"}`)
	do("POST", grp+"/nodepathstates", `{"metadata":{"name":"gka-body-name"}}`)
	do("DELETE", grp+"/nodepathstates/n", "")
	do("PUT", "/apis/coordination.k8s.io/v1/namespaces/default/leases/lease1", `{"spec":{}}`)
	do("GET", "/apis", "")

	if got := len(rec.All()); got != 9 || rec.All()[0] != "GET /api/v1/nodes" || rec.All()[8] != "GET /apis" {
		t.Errorf("GKA-175: All() = %v, want the nine requests in order", rec.All())
	}
	if w := rec.Writes(""); len(w) != 4 || w[0] != "PUT "+grp+"/gpudevices/abc/status" || w[3] != "DELETE "+grp+"/nodepathstates/n" {
		t.Errorf("GKA-175: Writes(\"\") = %v, want the four non-Lease writes", w)
	}
	if w := rec.Writes("gpudevices"); len(w) != 1 {
		t.Errorf("GKA-175: Writes by URL fragment = %v", w)
	}
	if w := rec.Writes("gka-body-name"); len(w) != 1 || !strings.HasPrefix(w[0], "POST ") {
		t.Errorf("GKA-175: Writes by the object name in a create body = %v", w)
	}
	if w := rec.Writes("/apis/coordination.k8s.io"); len(w) != 1 {
		t.Errorf("GKA-175: naming coordination.k8s.io includes Lease writes, got %v", w)
	}
	if l := rec.LeaseRequests(); len(l) != 1 {
		t.Errorf("GKA-175: LeaseRequests = %v, want the single Lease PUT", l)
	}
	var sawWatch bool
	for _, e := range rec.Entries() {
		if e.Watch && e.Path == "/api/v1/nodes" {
			sawWatch = true
		}
		if e.Method == "PUT" && strings.Contains(e.Path, "gpudevices") && (e.BodyName != "abc" || !strings.Contains(string(e.Body), "abc")) {
			t.Errorf("GKA-175: write body not captured: %+v", e)
		}
		if e.At.IsZero() {
			t.Errorf("GKA-175: entry without a timestamp: %+v", e)
		}
	}
	if !sawWatch {
		t.Errorf("GKA-175: watch=true GET was not flagged as a watch")
	}
	if v, _ := hits.Load("GET /api/v1/nodes"); v != "1" {
		t.Errorf("GKA-175: the base WrapTransport was dropped from the chain")
	}
	if base.WrapTransport == nil {
		t.Errorf("GKA-175: gkaRecordingConfig must not modify the base config")
	}

	// Injection answers without contacting the server, never leaks Retry-After, and is removable.
	before := served()
	rec.Inject(func(req *http.Request) *http.Response {
		if strings.HasSuffix(req.URL.Path, "/injected") {
			resp := gkaRStatusResponse(req, http.StatusTooManyRequests, metav1.StatusReasonTooManyRequests, "slow down")
			resp.Header.Set("Retry-After", "1")
			return resp
		}
		return nil
	})
	if resp := do("GET", "/api/v1/injected", ""); resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "" {
		t.Errorf("GKA-175: injected response = %d Retry-After=%q, want 429 without Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if served() != before {
		t.Errorf("GKA-175: an injected request reached the server")
	}
	if v := rec.Violations(); len(v) != 1 {
		t.Errorf("GKA-175: Violations = %v, want one for the injected Retry-After", v)
	}
	do("GET", "/api/v1/passthrough", "")
	if served() != before+1 {
		t.Errorf("GKA-175: a request the injector declined must reach the server")
	}
	sentinel := errors.New("gka injected transport failure")
	rec.InjectErr(func(req *http.Request) error { return sentinel })
	if _, err := hc.Get(srv.URL + "/api/v1/nodes"); !errors.Is(err, sentinel) {
		t.Errorf("GKA-175: InjectErr error = %v, want the sentinel", err)
	}
	rec.InjectErr(nil)
	rec.Inject(nil)
	do("GET", "/api/v1/after", "")
	if served() != before+2 {
		t.Errorf("GKA-175: after removing the injectors requests must pass through")
	}
}

type gkaRRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f gkaRRoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// GKA-176
// ---------------------------------------------------------------------------

func TestGKA176_EnvtestHasNoGCAndAddsTheNotReadyTaint(t *testing.T) {
	e := gkaEnv(t)
	ctx := context.Background()
	n := gkaRNode(t, e, gkaName(t, "n"), nil)
	got := gkaRGetNode(t, e, n.Name)
	taint := false
	for _, ta := range got.Spec.Taints {
		if ta.Key == "node.kubernetes.io/not-ready" && ta.Effect == corev1.TaintEffectNoSchedule {
			taint = true
		}
	}
	if !taint {
		t.Errorf("GKA-176: a created Node must carry the not-ready:NoSchedule taint (TaintNodesByCondition); taints = %+v", got.Spec.Taints)
	}
	gkaRSetNodeReady(t, e, n.Name)
	ready := false
	for _, c := range gkaRGetNode(t, e, n.Name).Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Errorf("GKA-176: the test must be able to set Ready through the status subresource")
	}

	// No garbage collector: an ownerReference does not cascade.
	nps := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{
			Name:            gkaName(t, "nps"),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: n.Name, UID: n.UID}},
		},
		Spec: v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: n.Name, UID: string(n.UID)}},
	}
	if err := e.Client.Create(ctx, nps); err != nil {
		t.Fatalf("create NodePathState: %v", err)
	}
	if err := e.Client.Delete(ctx, gkaRGetNode(t, e, n.Name)); err != nil {
		t.Fatalf("delete Node: %v", err)
	}
	time.Sleep(2 * time.Second)
	if _, ok := gkaRTryNPS(e, nps.Name); !ok {
		t.Errorf("GKA-176: the NodePathState disappeared with its owner: envtest has no garbage collector, so tests must not rely on cascading deletes")
	}
}

// ---------------------------------------------------------------------------
// GKA-177
// ---------------------------------------------------------------------------

func TestGKA177_EventuallyPollsFastAndFailsWithTheLastDescription(t *testing.T) {
	// In-process: the condition is evaluated at least every 100 ms (with scheduling margin).
	var times []time.Time
	start := time.Now()
	gkaEventually(t, 5*time.Second, func() (bool, string) {
		times = append(times, time.Now())
		return time.Since(start) >= 500*time.Millisecond, "waiting"
	})
	if len(times) < 5 {
		t.Errorf("GKA-177: only %d evaluations in 500ms, want polling at most every 100ms", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap > 200*time.Millisecond {
			t.Errorf("GKA-177: poll gap %s exceeds 100ms (+ scheduling margin)", gap)
		}
	}
	// Re-executed: a condition that never holds fails with the timeout and the last description.
	t0 := time.Now()
	out, code := gkaRRunSelf(t, []string{gkaProbeEnv + "=eventually-fail"}, "-test.v")
	if code == 0 || !strings.Contains(out, "PROBE-LAST-DESC") || !strings.Contains(out, "300ms") {
		t.Errorf("GKA-177: gkaEventually must fail with the timeout and the last description; exit %d:\n%s", code, out)
	}
	if el := time.Since(t0); el > 30*time.Second {
		t.Errorf("GKA-177: the failing probe took %s", el)
	}
	out, code = gkaRRunSelf(t, []string{gkaProbeEnv + "=eventually-ok"}, "-test.v")
	if code != 0 || !strings.Contains(out, "PROBE-EVENTUALLY-OK") {
		t.Errorf("GKA-177: a condition that becomes true must pass; exit %d:\n%s", code, out)
	}
}
