package liveingest_test

// Build, dependency, generation and static-discipline rules of
// gpu-fleet-live-ingest (GLI-001..005, GLI-010..013, GLI-020, GLI-025,
// GLI-085, GLI-130..132; scenario group GLI-124 (h)).
//
// Helper names in this file start with gliX. Tests that need the module
// cache, the network or a tool download (verify-proto, go mod tidy) probe for
// it first and skip with an explicit "not run" reason when the environment
// cannot run them; a failure after the probe succeeded is a real failure.
// The cross-build matrix of GLI-132 (three binaries on darwin/arm64,
// linux/amd64 and linux/arm64, ./... on js/wasm, windows/amd64 and the plan9
// subset, go vet) is exercised by the existing tests/gka_build_test.go
// (TestGKA196_*), which cover the new packages automatically; this file adds
// the explicit grpc-package builds for the three non-host targets.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	gliXModule       = "github.com/aetertinas9/data-path-assurance"
	gliXBaselineRef  = "a5a8bb7" // product commit the feature is built on
	gliXBaselineEnv  = "GLI_BASELINE_COMMIT"
	gliXGrpcPath     = "google.golang.org/grpc"
	gliXProtobufPath = "google.golang.org/protobuf"
	gliXGrpclogPath  = "google.golang.org/grpc/grpclog"
)

func gliXRoot(t testing.TB) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("cannot resolve the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

// gliXRun runs a command and returns stdout, stderr and the error.
func gliXRun(dir string, env []string, timeout time.Duration, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
	}
	return stdout.String(), stderr.String(), err
}

func gliXEnv(extra ...string) []string {
	env := append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	return append(env, extra...)
}

func gliXHead(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n...(truncated)"
	}
	return s
}

func gliXExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// gliXGo runs `go <args>` in dir and fails the test on error.
func gliXGo(t testing.TB, dir string, extra []string, args ...string) string {
	t.Helper()
	stdout, stderr, err := gliXRun(dir, gliXEnv(extra...), 20*time.Minute, "go", args...)
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s%s", strings.Join(args, " "), err, gliXHead(stdout, 1500), gliXHead(stderr, 3000))
	}
	return stdout
}

// gliXIsEnvFailure recognizes failures caused by a missing module cache or
// network rather than by the code under test.
func gliXIsEnvFailure(out string) bool {
	for _, p := range []string{
		"dial tcp", "no such host", "i/o timeout", "connection refused", "Temporary failure in name resolution",
		"lookup ", "module lookup disabled", "GOPROXY=off", "cannot find module providing", "no required module provides",
		"failed to fetch", "unrecognized import path", "server misbehaving", "TLS handshake timeout", "proxyconnect",
		"missing go.sum entry", "verifying module", "reading https://", "reading file://", "timed out", "go: downloading",
	} {
		if strings.Contains(out, p) {
			return true
		}
	}
	return false
}

type gliXPkg struct {
	ImportPath string
	Dir        string
	Name       string
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Deps       []string
}

// gliXList runs `go list -json` for the patterns (non-test view).
func gliXList(t testing.TB, extraEnv []string, patterns ...string) []gliXPkg {
	t.Helper()
	out := gliXGo(t, gliXRoot(t), extraEnv, append([]string{"list", "-json"}, patterns...)...)
	dec := json.NewDecoder(strings.NewReader(out))
	var pkgs []gliXPkg
	for {
		var p gliXPkg
		if err := dec.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("cannot decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func gliXIsStd(imp string) bool {
	first := strings.SplitN(imp, "/", 2)[0]
	return !strings.Contains(first, ".")
}

func gliXIsK8s(imp string) bool {
	return strings.HasPrefix(imp, "k8s.io/") || strings.HasPrefix(imp, "sigs.k8s.io/")
}

// gliXUnder reports whether imp is base or a package below it.
func gliXUnder(imp, base string) bool { return imp == base || strings.HasPrefix(imp, base+"/") }

func gliXSet(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

// gliXPkgByPath returns the package with the module-relative path rel, failing
// the test when it does not exist.
func gliXPkgByPath(t testing.TB, all []gliXPkg, rel string) gliXPkg {
	t.Helper()
	for _, p := range all {
		if p.ImportPath == gliXModule+"/"+rel {
			return p
		}
	}
	t.Fatalf("package %s does not exist", rel)
	return gliXPkg{}
}

// gliXProductPkgs is every package of the module except the test packages.
func gliXProductPkgs(t testing.TB) []gliXPkg {
	t.Helper()
	var out []gliXPkg
	for _, p := range gliXList(t, nil, "./...") {
		if p.ImportPath == gliXModule+"/tests" || strings.HasPrefix(p.ImportPath, gliXModule+"/tests/") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// gliXRepoCopy copies the repository into t.TempDir() (no dot entries, build
// or testdata) so tests can modify it and run generators in it.
func gliXRepoCopy(t *testing.T) string {
	t.Helper()
	root := gliXRoot(t)
	dst := filepath.Join(t.TempDir(), "repo")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if strings.HasPrefix(top, ".") || top == "build" || top == "testdata" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case !d.Type().IsRegular():
			return nil
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
	})
	if err != nil {
		t.Fatalf("fixture: cannot copy the repository: %v", err)
	}
	return dst
}

// gliXSnapshot maps every regular file below dir to its content.
func gliXSnapshot(t testing.TB, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		m[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return m
}

func gliXReadFile(t testing.TB, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(gliXRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("cannot read %s: %v", rel, err)
	}
	return string(b)
}

// ---------------------------------------------------------------- GLI-001/002/003

// GLI-001: every deliverable of section 1 exists where the spec puts it.
func TestGLI001_Deliverables(t *testing.T) {
	root := gliXRoot(t)
	all := gliXProductPkgs(t)
	for rel, name := range map[string]string{
		"internal/transport/mtls":           "mtls",
		"internal/app/framecore":            "framecore",
		"internal/app/liveingest":           "liveingest",
		"internal/ingest":                   "ingest",
		"internal/ingest/ingestpb":          "ingestpb",
		"internal/kubernetes/ingestadapter": "ingestadapter",
		"internal/agent/liveclient":         "liveclient",
		"cmd/path-agent":                    "main",
		"cmd/path-controller":               "main",
	} {
		p := gliXPkgByPath(t, all, rel)
		if p.Name != name || len(p.GoFiles) == 0 {
			t.Errorf("GLI-001: %s must be package %q with Go files, got %q with %d files", rel, name, p.Name, len(p.GoFiles))
		}
	}
	for _, f := range []string{
		"api/proto/dpa/ingest/v1alpha1/ingest.proto", "api/proto/buf.yaml", "api/proto/buf.gen.yaml", "api/proto/.gitkeep",
		"internal/ingest/ingestpb/ingest.pb.go", "internal/ingest/ingestpb/ingest_grpc.pb.go",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("GLI-001/012: %s is missing: %v", f, err)
		}
	}
	// the generated package is exactly the two generated files (GLI-012).
	entries, err := os.ReadDir(filepath.Join(root, "internal", "ingest", "ingestpb"))
	if err != nil {
		t.Fatalf("GLI-012: cannot list internal/ingest/ingestpb: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "ingest.pb.go,ingest_grpc.pb.go" {
		t.Errorf("GLI-012: internal/ingest/ingestpb holds %q, want exactly ingest.pb.go and ingest_grpc.pb.go", got)
	}
	// the Makefile targets of item (i).
	for _, target := range []string{"generate-proto", "verify-proto", "all", "arch-check", "test"} {
		stdout, stderr, err := gliXRun(root, gliXEnv(), time.Minute, "make", "-n", "--no-print-directory", target)
		if err != nil {
			t.Errorf("GLI-001: make -n %s failed: %v\n%s%s", target, err, gliXHead(stdout, 500), gliXHead(stderr, 500))
		}
	}
}

// GLI-002: nothing out of scope was delivered: exactly one proto file (S3b-2
// adds fence.proto and S3b-3 explain.proto), no kubelet dependency, no
// PodResources or fence or explain-server package, no pathctl live transport.
func TestGLI002_NoOutOfScopeDeliverables(t *testing.T) {
	root := gliXRoot(t)
	var protos []string
	_ = filepath.WalkDir(filepath.Join(root, "api"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".proto") {
			rel, _ := filepath.Rel(root, path)
			protos = append(protos, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(protos) != 1 || protos[0] != "api/proto/dpa/ingest/v1alpha1/ingest.proto" {
		t.Errorf("GLI-002/010: the proto files are %v, want exactly api/proto/dpa/ingest/v1alpha1/ingest.proto (fence.proto is S3b-2, explain.proto S3b-3)", protos)
	}
	mod := gliXReadFile(t, "go.mod")
	if strings.Contains(mod, "k8s.io/kubelet") {
		t.Errorf("GLI-002: go.mod requires k8s.io/kubelet (the PodResources adapter is S3b-2)")
	}
	for _, p := range gliXProductPkgs(t) {
		low := strings.ToLower(p.ImportPath)
		for _, bad := range []string{"podresources", "fence", "explainapi", "explain/server", "/runbook"} {
			if strings.Contains(low, bad) {
				t.Errorf("GLI-002: package %s belongs to a later feature (%s)", p.ImportPath, bad)
			}
		}
	}
	for _, rel := range []string{"deploy/helm", "deploy/crds"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("GLI-002: %s must stay: %v", rel, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "deploy"))
	if err != nil {
		t.Fatalf("GLI-002: cannot list deploy/: %v", err)
	}
	var deploy []string
	for _, e := range entries {
		deploy = append(deploy, e.Name())
	}
	sort.Strings(deploy)
	if got := strings.Join(deploy, ","); got != "crds,helm" {
		t.Errorf("GLI-002: deploy/ holds %q, want exactly crds and helm (RBAC, manifests and containers are S4a)", got)
	}
}

// GLI-003: opt-in comparison with the base commit (GLI_BASELINE_COMMIT=a5a8bb7):
// the ratified domain packages, the CRD manifests, the S3a controller and
// pathctl are byte-identical; internal/app only changes where the framecore
// extraction (GLI-071) rewrote call sites and only adds files elsewhere.
func TestGLI003_ProtectedFilesUnchanged(t *testing.T) {
	base := os.Getenv(gliXBaselineEnv)
	if base == "" {
		t.Skipf("GLI-003: set %s=%s to compare the tree with the base commit (not run)", gliXBaselineEnv, gliXBaselineRef)
	}
	root := gliXRoot(t)
	if _, _, err := gliXRun(root, gliXEnv(), 30*time.Second, "git", "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		t.Skipf("GLI-003: git or the commit %s is not available here (%v) (not run)", base, err)
	}
	protected := []string{
		"internal/fleet", "pkg/model", "internal/graph", "internal/evidence", "internal/domains/pcie", "internal/identity",
		"deploy/crds", "internal/kubernetes/controller", "internal/kubernetes/api/v1alpha1",
		"cmd/pathctl", "internal/cli", "internal/offline", "internal/nativepcie",
	}
	diff, stderr, err := gliXRun(root, gliXEnv(), time.Minute, "git", append([]string{"diff", "--name-status", "--no-renames", base, "--"}, protected...)...)
	if err != nil {
		t.Skipf("GLI-003: git diff failed (not run): %v %s", err, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(diff), "\n") {
		if strings.TrimSpace(line) != "" {
			t.Errorf("GLI-003: a protected product file changed since %s: %s", base, line)
		}
	}
	untracked, _, err := gliXRun(root, gliXEnv(), time.Minute, "git", append([]string{"ls-files", "--others", "--exclude-standard", "--"}, protected...)...)
	if err == nil {
		for _, l := range strings.Split(strings.TrimSpace(untracked), "\n") {
			if strings.TrimSpace(l) != "" {
				t.Errorf("GLI-003: untracked file inside a protected tree: %s", l)
			}
		}
	}
}

// ---------------------------------------------------------------- GLI-004

type gliXImportRule struct {
	clause string
	rel    string
	allow  func(imp string) bool
	hint   string
}

// GLI-004 (a)..(g): the direct import rules of every new package, the
// transitive absence for the two domain-core packages, and the global
// grpc/protobuf/k8s importer sets.
func TestGLI004_ImportRules(t *testing.T) {
	all := gliXProductPkgs(t)
	m := gliXModule
	ratified := gliXSet(m+"/pkg/model", m+"/internal/identity", m+"/internal/graph", m+"/internal/evidence", m+"/internal/domains/pcie", m+"/internal/fleet")
	forbiddenStd := gliXSet("encoding/json", "io", "io/fs", "io/ioutil", "os", "os/exec", "path/filepath", "log", "log/slog", "net", "net/http", "syscall", "unsafe", "crypto/tls")
	stdOK := func(imp string) bool { return gliXIsStd(imp) && !forbiddenStd[imp] }

	rules := []gliXImportRule{
		{"GLI-004(a)", "internal/app/framecore", func(i string) bool { return stdOK(i) || ratified[i] }, "standard library (minus the GFX-005(d) list and crypto/tls) and the ratified packages only; framecore never imports internal/app"},
		{"GLI-004(a)", "internal/app/liveingest", func(i string) bool { return stdOK(i) || ratified[i] || gliXUnder(i, m+"/internal/app") }, "standard library (minus the GFX-005(d) list and crypto/tls), the ratified packages and internal/app/..."},
		{"GLI-004(b)", "internal/transport/mtls", gliXIsStd, "the standard library only"},
		{"GLI-004(c)", "internal/ingest", func(i string) bool {
			return gliXIsStd(i) || gliXUnder(i, gliXGrpcPath) || gliXUnder(i, gliXProtobufPath) ||
				i == m+"/internal/ingest/ingestpb" || i == m+"/internal/app/liveingest" || i == m+"/internal/transport/mtls" ||
				i == m+"/internal/fleet" || i == m+"/internal/app" || i == m+"/pkg/model"
		}, "the standard library, grpc, protobuf, ingestpb, liveingest, mtls, internal/fleet, internal/app and pkg/model"},
		{"GLI-004(d)", "internal/kubernetes/ingestadapter", func(i string) bool {
			return gliXIsStd(i) || gliXIsK8s(i) || i == m+"/internal/app/liveingest" || i == m+"/internal/fleet" || i == m+"/internal/kubernetes/api/v1alpha1"
		}, "the standard library, k8s.io/*, sigs.k8s.io/*, liveingest, internal/fleet and the v1alpha1 types"},
		{"GLI-004(e)", "internal/agent/liveclient", func(i string) bool {
			return gliXIsStd(i) || gliXUnder(i, gliXGrpcPath) || gliXUnder(i, gliXProtobufPath) ||
				i == m+"/internal/ingest/ingestpb" || i == m+"/internal/transport/mtls" || i == m+"/internal/agent" ||
				i == m+"/internal/app/liveingest" || i == m+"/internal/identity" || i == m+"/internal/graph" || i == m+"/internal/fleet" || i == m+"/pkg/model"
		}, "the standard library, grpc, protobuf, ingestpb, mtls, internal/agent, liveingest, identity, graph, fleet and pkg/model (no Kubernetes, GFL-080)"},
	}
	for _, r := range rules {
		p := gliXPkgByPath(t, all, r.rel)
		for _, imp := range p.Imports {
			if imp == "C" {
				continue
			}
			if !r.allow(imp) {
				t.Errorf("%s: %s imports %s (allowed: %s)", r.clause, r.rel, imp, r.hint)
			}
		}
	}

	// (a) transitive: the two domain-core packages reach no google.golang.org, k8s.io or sigs.k8s.io package.
	for _, rel := range []string{"internal/app/framecore", "internal/app/liveingest"} {
		p := gliXPkgByPath(t, all, rel)
		for _, dep := range p.Deps {
			if strings.HasPrefix(dep, "google.golang.org/") || gliXIsK8s(dep) {
				t.Errorf("GLI-004(a): %s reaches %s transitively", rel, dep)
			}
		}
	}
	// (a) the sharing: internal/app and liveingest import framecore, liveingest imports internal/app, framecore does not.
	fc := m + "/internal/app/framecore"
	contains := func(list []string, want string) bool {
		for _, x := range list {
			if x == want {
				return true
			}
		}
		return false
	}
	if !contains(gliXPkgByPath(t, all, "internal/app").Imports, fc) {
		t.Errorf("GLI-004(a)/071: internal/app (S2-2) does not import internal/app/framecore (copy instead of extraction?)")
	}
	live := gliXPkgByPath(t, all, "internal/app/liveingest")
	if !contains(live.Imports, fc) {
		t.Errorf("GLI-004(a)/071: internal/app/liveingest does not import internal/app/framecore")
	}
	if !contains(live.Imports, m+"/internal/app") {
		t.Errorf("GLI-004(a): internal/app/liveingest must import internal/app (port and ErrNoObservation)")
	}

	// (f) the two binaries: standard library and this module, plus google.golang.org/grpc/grpclog and nothing else outside.
	for _, rel := range []string{"cmd/path-agent", "cmd/path-controller"} {
		p := gliXPkgByPath(t, all, rel)
		sawGrpclog := false
		for _, imp := range p.Imports {
			switch {
			case gliXIsStd(imp), strings.HasPrefix(imp, m+"/"):
			case imp == gliXGrpclogPath:
				sawGrpclog = true
			default:
				t.Errorf("GLI-004(f)/(g): %s imports %s (a binary may import only google.golang.org/grpc/grpclog outside the standard library and this module)", rel, imp)
			}
			if gliXIsK8s(imp) || strings.HasPrefix(imp, m+"/internal/offline") || strings.HasPrefix(imp, m+"/internal/cli") {
				t.Errorf("GLI-004(f): %s imports %s", rel, imp)
			}
		}
		if !sawGrpclog {
			t.Errorf("GLI-004(f)/005: %s does not import google.golang.org/grpc/grpclog (the composition root silences grpc's own logger)", rel)
		}
	}
	for _, imp := range gliXPkgByPath(t, all, "cmd/path-agent").Imports {
		if strings.HasPrefix(imp, m+"/internal/kubernetes") {
			t.Errorf("GLI-004(e)/GFL-080: cmd/path-agent imports %s (the agent has no Kubernetes dependency)", imp)
		}
	}

	// (g) who may import what directly (test packages excluded).
	grpcAllowed := gliXSet(m+"/internal/ingest", m+"/internal/ingest/ingestpb", m+"/internal/agent/liveclient")
	protoAllowed := gliXSet(m+"/internal/ingest", m+"/internal/ingest/ingestpb", m+"/internal/agent/liveclient")
	for _, p := range all {
		for _, imp := range p.Imports {
			switch {
			case gliXIsK8s(imp):
				if p.ImportPath != m+"/internal/kubernetes" && !strings.HasPrefix(p.ImportPath, m+"/internal/kubernetes/") {
					t.Errorf("GLI-004(g): %s imports %s directly; only internal/kubernetes/... may", p.ImportPath, imp)
				}
			case gliXUnder(imp, gliXGrpcPath):
				cmdGrpclog := (p.ImportPath == m+"/cmd/path-agent" || p.ImportPath == m+"/cmd/path-controller") && imp == gliXGrpclogPath
				if !grpcAllowed[p.ImportPath] && !cmdGrpclog {
					t.Errorf("GLI-004(g): %s imports %s directly (allowed: internal/ingest, ingestpb, liveclient, and google.golang.org/grpc/grpclog in the two binaries)", p.ImportPath, imp)
				}
			case gliXUnder(imp, gliXProtobufPath):
				if !protoAllowed[p.ImportPath] {
					t.Errorf("GLI-004(c)(e): %s imports %s directly (only internal/ingest, ingestpb and liveclient may)", p.ImportPath, imp)
				}
			}
		}
	}
}

// ---------------------------------------------------------------- GLI-005 / 020 / 025 / 085 static

func gliXParse(t testing.TB, path string) (*token.FileSet, *ast.File, []byte) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("cannot parse %s: %v", path, err)
	}
	return fset, f, src
}

func gliXSelector(n ast.Node) (pkg, name string, ok bool) {
	sel, isSel := n.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isID := sel.X.(*ast.Ident)
	if !isID {
		return "", "", false
	}
	return id.Name, sel.Sel.Name, true
}

// gliXAllowedGlobalVar reports whether a package-level var initializer is a
// sentinel error or a precompiled pattern (GLI-005: constants and sentinel
// errors only).
func gliXAllowedGlobalVar(v ast.Expr) bool {
	call, ok := v.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkg, name, ok := gliXSelector(call.Fun)
	if !ok {
		return false
	}
	switch pkg + "." + name {
	case "errors.New", "fmt.Errorf", "regexp.MustCompile":
		return true
	}
	return false
}

var gliXStaticPackages = []string{
	"internal/app/framecore", "internal/app/liveingest", "internal/transport/mtls", "internal/ingest",
	"internal/kubernetes/ingestadapter", "internal/agent/liveclient", "cmd/path-agent", "cmd/path-controller",
}

// GLI-005: the new packages have no init(), blank import, package-level
// mutable state or direct time.Now outside a system clock; context.Context is
// the first parameter; no stdlib log; the controller writes nothing to stdout and
// calls os.Exit only in main. Generated code (ingestpb) is exempt.
func TestGLI005_StaticDiscipline(t *testing.T) {
	all := gliXProductPkgs(t)
	for _, rel := range gliXStaticPackages {
		p := gliXPkgByPath(t, all, rel)
		isMain := p.Name == "main"
		isController := p.ImportPath == gliXModule+"/cmd/path-controller"
		for _, name := range p.GoFiles {
			fset, f, src := gliXParse(t, filepath.Join(p.Dir, name))
			where := func(pos token.Pos) string { return fmt.Sprintf("%s/%s:%d", rel, name, fset.Position(pos).Line) }
			for _, is := range f.Imports {
				path, _ := strconv.Unquote(is.Path.Value)
				if is.Name != nil && is.Name.Name == "_" {
					t.Errorf("GLI-005: %s blank import of %s (no import side effects)", where(is.Pos()), path)
				}
				if path == "log" {
					t.Errorf("GLI-005: %s imports the standard log package (logging uses log/slog)", where(is.Pos()))
				}
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && d.Name.Name == "init" {
						t.Errorf("GLI-005: %s declares func init()", where(d.Pos()))
					}
					if d.Body == nil {
						continue
					}
					lowName := strings.ToLower(d.Name.Name)
					allowNow := d.Name.Name == "Now" || strings.Contains(lowName, "systemclock")
					ast.Inspect(d.Body, func(n ast.Node) bool {
						pkg, sel, ok := gliXSelector(n)
						if !ok {
							return true
						}
						switch {
						case pkg == "time" && sel == "Now" && !isMain && !allowNow:
							t.Errorf("GLI-005: %s calls time.Now outside a system clock implementation (time comes from the injected Clock)", where(n.Pos()))
						case isController && pkg == "fmt" && (sel == "Print" || sel == "Printf" || sel == "Println"):
							t.Errorf("GLI-005/GKA-162: %s writes to stdout with fmt.%s (the controller writes nothing to stdout)", where(n.Pos()), sel)
						case isController && pkg == "os" && sel == "Exit" && d.Name.Name != "main":
							t.Errorf("GLI-005/GKA-164: %s calls os.Exit outside func main", where(n.Pos()))
						case pkg == "gzip" && sel == "SetLevel":
							t.Errorf("GLI-005: %s calls gzip.SetLevel (process-global compressor settings are forbidden)", where(n.Pos()))
						}
						return true
					})
				case *ast.GenDecl:
					if d.Tok != token.VAR {
						continue
					}
					for _, spec := range d.Specs {
						vs := spec.(*ast.ValueSpec)
						allBlank := true
						for _, n := range vs.Names {
							allBlank = allBlank && n.Name == "_"
						}
						if allBlank {
							continue
						}
						ok := len(vs.Values) == len(vs.Names) && len(vs.Values) > 0
						for _, v := range vs.Values {
							ok = ok && gliXAllowedGlobalVar(v)
						}
						if !ok {
							text := string(src[fset.Position(vs.Pos()).Offset:fset.Position(vs.End()).Offset])
							t.Errorf("GLI-005: %s package-level variable is not a sentinel error or constant: %s", where(vs.Pos()), strings.Split(text, "\n")[0])
						}
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				ft, ok := n.(*ast.FuncType)
				if !ok || ft.Params == nil {
					return true
				}
				idx := 0
				for _, field := range ft.Params.List {
					if pkg, sel, ok := gliXSelector(field.Type); ok && pkg == "context" && sel == "Context" && idx > 0 {
						t.Errorf("GLI-005: %s takes context.Context as parameter %d (it must be the first)", where(field.Pos()), idx+1)
					}
					count := len(field.Names)
					if count == 0 {
						count = 1
					}
					idx += count
				}
				return true
			})
		}
	}
}

// GLI-005 (sole global-setting exception): each binary calls
// grpclog.SetLoggerV2 exactly once, with three io.Discard writers, before any
// call into the ingest, adapter, controller or live client packages; no other
// package touches grpc's global logger.
func TestGLI005_GrpclogIsSetOnlyByTheCompositionRoots(t *testing.T) {
	all := gliXProductPkgs(t)
	m := gliXModule
	for _, p := range all {
		if p.ImportPath == m+"/internal/ingest/ingestpb" || p.Name == "main" && !strings.HasPrefix(p.ImportPath, m+"/cmd/") {
			continue
		}
		isCmd := p.ImportPath == m+"/cmd/path-agent" || p.ImportPath == m+"/cmd/path-controller"
		calls := 0
		for _, name := range p.GoFiles {
			fset, f, _ := gliXParse(t, filepath.Join(p.Dir, name))
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				var setPos token.Pos
				discards := 0
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					pkg, sel, ok := gliXSelector(call.Fun)
					if !ok || pkg != "grpclog" {
						return true
					}
					if sel == "SetLoggerV2" || sel == "SetLogger" || sel == "SetLoggerV2WithVerbosity" {
						calls++
						if !isCmd {
							t.Errorf("GLI-005: %s/%s:%d calls grpclog.%s; library packages never touch grpc's global logger", p.ImportPath, name, fset.Position(call.Pos()).Line, sel)
							return true
						}
						setPos = call.Pos()
						ast.Inspect(call, func(x ast.Node) bool {
							if pk, nm, ok := gliXSelector(x); ok && pk == "io" && nm == "Discard" {
								discards++
							}
							return true
						})
					}
					return true
				})
				if setPos.IsValid() {
					if discards < 3 {
						t.Errorf("GLI-005: %s: grpclog.SetLoggerV2 must receive three io.Discard writers (found %d)", p.ImportPath, discards)
					}
					// nothing that talks to grpc may run earlier in the same function.
					ast.Inspect(fd.Body, func(n ast.Node) bool {
						call, ok := n.(*ast.CallExpr)
						if !ok || call.Pos() >= setPos {
							return true
						}
						if pkg, sel, ok := gliXSelector(call.Fun); ok {
							switch pkg {
							case "ingest", "ingestadapter", "controller", "liveclient":
								t.Errorf("GLI-005: %s: %s.%s is called before grpclog.SetLoggerV2 (it must be the first thing main does)", p.ImportPath, pkg, sel)
							}
						}
						return true
					})
				}
			}
		}
		if isCmd && calls != 1 {
			t.Errorf("GLI-005: %s calls grpclog.SetLoggerV2 %d times, want exactly once", p.ImportPath, calls)
		}
	}
}

// GLI-020 / GLI-025 / GLI-085: no verification-bypass option, no insecure
// credentials, no extra gRPC service, no Kubernetes credential in the agent.
func TestGLI020_025_085_StaticSurface(t *testing.T) {
	all := gliXProductPkgs(t)
	m := gliXModule
	bypass := regexp.MustCompile(`(?i)insecure|skip[-_ ]?verify|skip[-_ ]?tls`)
	extraServices := []string{"reflection", "health", "channelz", "admin", "profiling", "binarylog", "orca"}
	for _, p := range all {
		if p.ImportPath == m+"/internal/ingest/ingestpb" {
			continue
		}
		scoped := strings.HasPrefix(p.ImportPath, m+"/internal/ingest")
		for _, rel := range gliXStaticPackages {
			scoped = scoped || p.ImportPath == m+"/"+rel
		}
		for _, name := range p.GoFiles {
			fset, f, _ := gliXParse(t, filepath.Join(p.Dir, name))
			where := func(pos token.Pos) string {
				return fmt.Sprintf("%s/%s:%d", p.ImportPath, name, fset.Position(pos).Line)
			}
			for _, is := range f.Imports {
				path, _ := strconv.Unquote(is.Path.Value)
				if path == "google.golang.org/grpc/credentials/insecure" {
					t.Errorf("GLI-020: %s imports %s (no plaintext listener or dial path exists)", where(is.Pos()), path)
				}
				if strings.HasPrefix(p.ImportPath, m+"/internal/ingest") || p.Name == "main" {
					for _, svc := range extraServices {
						if gliXUnder(path, gliXGrpcPath+"/"+svc) {
							t.Errorf("GLI-025: %s imports %s (the listener serves the Ingest service only)", where(is.Pos()), path)
						}
					}
				}
				if strings.HasPrefix(p.ImportPath, m+"/internal/agent/liveclient") || p.ImportPath == m+"/cmd/path-agent" {
					if gliXIsK8s(path) || strings.HasPrefix(path, m+"/internal/kubernetes") {
						t.Errorf("GLI-085: %s imports %s (the agent has no Kubernetes client)", where(is.Pos()), path)
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Ident:
					if n.Name == "InsecureSkipVerify" || n.Name == "WithInsecure" {
						t.Errorf("GLI-020: %s uses %s (no verification-bypass option exists)", where(n.Pos()), n.Name)
					}
				case *ast.BasicLit:
					if n.Kind != token.STRING {
						return true
					}
					v, err := strconv.Unquote(n.Value)
					if err != nil {
						return true
					}
					if scoped && bypass.MatchString(v) {
						t.Errorf("GLI-020: %s string literal %q looks like a verification-bypass option, flag or environment variable", where(n.Pos()), v)
					}
					if strings.HasPrefix(p.ImportPath, m+"/internal/agent/liveclient") || p.ImportPath == m+"/cmd/path-agent" {
						low := strings.ToLower(v)
						if strings.Contains(low, "serviceaccount") || strings.Contains(low, "kubernetes_service") || strings.Contains(low, "secrets/kubernetes.io") || strings.Contains(low, "kubeconfig") {
							t.Errorf("GLI-085: %s string literal %q names a Kubernetes credential source (the agent uses none)", where(n.Pos()), v)
						}
					}
				}
				return true
			})
		}
	}
}

// ---------------------------------------------------------------- GLI-010 / 012

var gliXProtoFieldRE = regexp.MustCompile(`(repeated\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s+([a-z_][a-z0-9_]*)\s*=\s*([0-9]+)\s*;`)
var gliXProtoEnumValueRE = regexp.MustCompile(`([A-Z][A-Z0-9_]*)\s*=\s*([0-9]+)\s*;`)
var gliXProtoBlockRE = regexp.MustCompile(`(?m)\b(message|enum|service)\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)

func gliXStripProtoComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, " ")
	return regexp.MustCompile(`//[^\n]*`).ReplaceAllString(src, "")
}

// gliXParseProto returns the fields of every message ("[repeated ]type name=N",
// sorted) and the values of every enum ("NAME=N", sorted) of a proto3 file.
func gliXParseProto(src string) (messages, enums map[string][]string) {
	src = gliXStripProtoComments(src)
	messages, enums = map[string][]string{}, map[string][]string{}
	for _, loc := range gliXProtoBlockRE.FindAllStringSubmatchIndex(src, -1) {
		kind, name := src[loc[2]:loc[3]], src[loc[4]:loc[5]]
		depth, end := 1, loc[1]
		for end < len(src) && depth > 0 {
			switch src[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
			end++
		}
		body := src[loc[1] : end-1]
		var items []string
		switch kind {
		case "message":
			for _, f := range gliXProtoFieldRE.FindAllStringSubmatch(body, -1) {
				rep := ""
				if strings.TrimSpace(f[1]) != "" {
					rep = "repeated "
				}
				items = append(items, rep+f[2]+" "+f[3]+"="+f[4])
			}
			sort.Strings(items)
			messages[name] = items
		case "enum":
			for _, v := range gliXProtoEnumValueRE.FindAllStringSubmatch(body, -1) {
				items = append(items, v[1]+"="+v[2])
			}
			sort.Strings(items)
			enums[name] = items
		}
	}
	return messages, enums
}

func gliXSorted(items ...string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

// GLI-010: the proto file declares the wire format exactly (messages, field
// numbers, enums, the one RPC, package and go_package).
func TestGLI010_ProtoDeclaresTheWireFormat(t *testing.T) {
	src := gliXReadFile(t, "api/proto/dpa/ingest/v1alpha1/ingest.proto")
	norm := strings.Join(strings.Fields(gliXStripProtoComments(src)), " ")
	for _, want := range []string{
		`syntax = "proto3";`,
		`package dpa.ingest.v1alpha1;`,
		`option go_package = "github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb";`,
		`import "google/protobuf/timestamp.proto";`,
		`service Ingest { rpc Stream(stream ClientFrame) returns (stream ServerFrame); }`,
	} {
		if !strings.Contains(norm, want) {
			t.Errorf("GLI-010: the proto file lacks %q", want)
		}
	}
	wantMsgs := map[string][]string{
		"ClientFrame":     gliXSorted("ClientHello hello=1", "SnapshotFrame snapshot=2"),
		"ServerFrame":     gliXSorted("ServerHello hello=1", "SnapshotAck ack=2"),
		"ClientHello":     gliXSorted("string version=1", "string cluster_id=2", "string node_name=3", "string node_uid=4", "string boot_id=5"),
		"ServerHello":     gliXSorted("int64 session=1", "string collector_profile_id=2"),
		"SnapshotFrame":   gliXSorted("string node_uid=1", "string boot_id=2", "int64 session=3", "uint64 sequence=4", "Completeness completeness=5", "google.protobuf.Timestamp observed_at=6", "HostSnapshotV1 payload=7", "bytes payload_digest=8", "string bundle_revision=9"),
		"SnapshotAck":     gliXSorted("AckCode code=1", "int64 accepted_session=2", "uint64 expected_next_sequence=3", "string message=4"),
		"HostSnapshotV1":  gliXSorted("repeated Asset assets=1", "repeated Edge edges=2", "repeated EdgeEvidence edge_evidence=3", "repeated Observation observations=4", "repeated GPUBinding gpu_bindings=5", "AllocationBatch allocation_batch=6"),
		"Alias":           gliXSorted("string namespace=1", "string value=2"),
		"Asset":           gliXSorted("string kind=1", "string canonical=2", "repeated Alias aliases=3"),
		"Edge":            gliXSorted("string from_key=1", "string relation=2", "string to_key=3", "string origin=4"),
		"EdgeEvidence":    gliXSorted("uint32 edge_index=1", "string kind=2", "string source_type=3", "string source_name=4", "google.protobuf.Timestamp observed_at=5", "google.protobuf.Timestamp expires_at=6", "string evidence_id=7"),
		"SourceRef":       gliXSorted("string type=1", "string name=2"),
		"Value":           gliXSorted("int64 int_value=1", "double float_value=2", "bool bool_value=3", "string string_value=4"),
		"Dimension":       gliXSorted("string key=1", "string value=2"),
		"Observation":     gliXSorted("string id=1", "SourceRef source=2", "Asset subject=3", "string signal=4", "Value value=5", "string unit=6", "repeated Dimension dimensions=7", "google.protobuf.Timestamp observed_at=8", "google.protobuf.Timestamp received_at=9", "google.protobuf.Timestamp expires_at=10", "uint64 sequence=11", "string quality=12", "string raw_digest=13"),
		"GPUBinding":      gliXSorted("string uuid=1", "string serial=2", "string bdf=3", "string source_type=4", "string source_name=5", "string evidence_id=6", "google.protobuf.Timestamp observed_at=7", "google.protobuf.Timestamp expires_at=8"),
		"AllocationEntry": gliXSorted("string resource_name=1", "string device_id=2", "string pod_namespace=3", "string pod_name=4", "string container_name=5"),
		"AllocationBatch": gliXSorted("string node_uid=1", "string boot_id=2", "int64 session=3", "uint64 sequence=4", "google.protobuf.Timestamp observed_at=5", "google.protobuf.Timestamp expires_at=6", "bool complete=7", "repeated string evidence_refs=8", "AllocationProfile profile=9", "repeated AllocationEntry entries=10"),
	}
	wantEnums := map[string][]string{
		"Completeness":      gliXSorted("COMPLETENESS_UNSPECIFIED=0", "COMPLETE=1", "PARTIAL=2"),
		"AckCode":           gliXSorted("ACK_CODE_UNSPECIFIED=0", "ACCEPTED=1", "DUPLICATE=2", "OUT_OF_ORDER=3", "GAP=4", "WRONG_SESSION=5", "CONFLICT=6", "INVALID=7"),
		"AllocationProfile": gliXSorted("ALLOCATION_PROFILE_UNSPECIFIED=0", "NVIDIA_PODRESOURCES_UUID=1", "UNSUPPORTED=2"),
	}
	msgs, enums := gliXParseProto(src)
	for name, want := range wantMsgs {
		got, ok := msgs[name]
		if !ok {
			t.Errorf("GLI-010: message %s is not declared", name)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GLI-010: message %s fields = %v, want %v (a deployed field number is never reused or renumbered)", name, got, want)
		}
	}
	for name := range msgs {
		if _, ok := wantMsgs[name]; !ok {
			t.Errorf("GLI-010: message %s is not part of the wire format", name)
		}
	}
	for name, want := range wantEnums {
		got, ok := enums[name]
		if !ok {
			t.Errorf("GLI-010: enum %s is not declared", name)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GLI-010: enum %s values = %v, want %v", name, got, want)
		}
	}
	for name := range enums {
		if _, ok := wantEnums[name]; !ok {
			t.Errorf("GLI-010: enum %s is not part of the wire format", name)
		}
	}
}

// GLI-012: the generation setup is pinned: buf, protoc-gen-go and
// protoc-gen-go-grpc run through `go run <module>@<version>` with
// GOTOOLCHAIN=local, the plugin options are as specified, the generated
// headers carry the pinned versions, and none of the tools is a go.mod
// requirement or tool directive.
func TestGLI012_GenerationSetupIsPinned(t *testing.T) {
	bufGen := gliXReadFile(t, "api/proto/buf.gen.yaml")
	normGen := strings.Join(strings.Fields(bufGen), " ")
	for _, want := range []string{
		"version: v2",
		"google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.8",
		"google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2",
		"module=github.com/aetertinas9/data-path-assurance",
		"out: .",
	} {
		if !strings.Contains(normGen, want) {
			t.Errorf("GLI-012: api/proto/buf.gen.yaml lacks %q", want)
		}
	}
	for _, bad := range []string{"paths=source_relative", "default_api_level", "protoc:", "remote:", "buf.build/"} {
		if strings.Contains(bufGen, bad) {
			t.Errorf("GLI-012: api/proto/buf.gen.yaml contains %q", bad)
		}
	}
	if !strings.Contains(gliXReadFile(t, "api/proto/buf.yaml"), "version:") {
		t.Errorf("GLI-012: api/proto/buf.yaml does not declare a version")
	}
	root := gliXRoot(t)
	stdout, stderr, err := gliXRun(root, gliXEnv(), time.Minute, "make", "-n", "--no-print-directory", "generate-proto")
	if err != nil {
		t.Fatalf("GLI-012: make -n generate-proto failed: %v\n%s%s", err, stdout, stderr)
	}
	for _, want := range []string{"GOTOOLCHAIN=local", "github.com/bufbuild/buf/cmd/buf@v1.72.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("GLI-012: the generate-proto recipe does not contain %q:\n%s", want, gliXHead(stdout, 1500))
		}
	}
	if regexp.MustCompile(`(^|[\s;&|(])protoc(\s|$)`).MatchString(stdout) {
		t.Errorf("GLI-012: the generate-proto recipe calls a system protoc:\n%s", gliXHead(stdout, 1500))
	}
	pb := gliXReadFile(t, "internal/ingest/ingestpb/ingest.pb.go")
	head := pb
	if len(head) > 2000 {
		head = head[:2000]
	}
	if !strings.Contains(head, "protoc-gen-go v1.36.8") || !regexp.MustCompile(`protoc\s+\(unknown\)`).MatchString(head) {
		t.Errorf("GLI-012: ingest.pb.go header lacks `protoc-gen-go v1.36.8` and `protoc (unknown)`:\n%s", gliXHead(head, 600))
	}
	if !strings.HasPrefix(pb, "// Code generated by protoc-gen-go. DO NOT EDIT.") {
		t.Errorf("GLI-012: ingest.pb.go does not start with the generated-code marker")
	}
	grpcPB := gliXReadFile(t, "internal/ingest/ingestpb/ingest_grpc.pb.go")
	if !strings.Contains(grpcPB, "grpc.SupportPackageIsVersion9") || !strings.Contains(grpcPB, "protoc-gen-go-grpc v1.6.2") {
		t.Errorf("GLI-012: ingest_grpc.pb.go lacks grpc.SupportPackageIsVersion9 or the protoc-gen-go-grpc v1.6.2 header")
	}
	mod := gliXReadFile(t, "go.mod")
	for _, bad := range []string{"bufbuild", "protoc-gen-go", "protoc-gen-go-grpc"} {
		if strings.Contains(mod, bad) {
			t.Errorf("GLI-012: go.mod mentions %q (generation tools are neither require nor tool)", bad)
		}
	}
	if regexp.MustCompile(`(?m)^tool\b`).MatchString(mod) {
		t.Errorf("GLI-012: go.mod has a tool directive")
	}
}

// ---------------------------------------------------------------- GLI-013

type gliXVerifyResult struct {
	out  string
	code int
}

func gliXMakeVerifyProto(dir string) gliXVerifyResult {
	stdout, stderr, err := gliXRun(dir, gliXEnv("GOTOOLCHAIN=local"), 15*time.Minute, "make", "--no-print-directory", "verify-proto")
	return gliXVerifyResult{out: stdout + stderr, code: gliXExitCode(err)}
}

// GLI-013 (a)(b)(c): verify-proto passes on a clean tree without writing it,
// fails when one character of a generated file changes (the file stays
// changed), and fails when the .proto gains a field that was not regenerated.
// Needs the module cache or network and the buf toolchain (about 37 s and 1 GB
// on the first run): outside the GLI-125 time budget; skipped with an explicit
// reason when the environment cannot run it. Every tamper happens in a copy.
func TestGLI013_VerifyProto(t *testing.T) {
	repo := gliXRepoCopy(t)
	before := gliXSnapshot(t, repo)
	clean := gliXMakeVerifyProto(repo)
	if clean.code != 0 {
		if gliXIsEnvFailure(clean.out) {
			t.Skipf("GLI-013: make verify-proto needs the module cache or network for buf/protoc-gen-go (not run): %s", gliXHead(clean.out, 600))
		}
		t.Fatalf("GLI-013 (a): make verify-proto on a clean tree exited %d, want 0:\n%s", clean.code, gliXHead(clean.out, 3000))
	}
	after := gliXSnapshot(t, repo)
	for path, content := range before {
		if got, ok := after[path]; !ok || got != content {
			t.Errorf("GLI-013: verify-proto changed or removed the checked-in file %s (it must only compare)", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("GLI-013: verify-proto left the new file %s in the tree (it works in a temporary location)", path)
		}
	}

	t.Run("generated_file_tamper", func(t *testing.T) {
		repo := gliXRepoCopy(t)
		path := filepath.Join(repo, "internal", "ingest", "ingestpb", "ingest.pb.go")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		i := bytes.Index(b, []byte("package ingestpb"))
		if i < 0 {
			t.Fatalf("fixture: ingest.pb.go has no package clause")
		}
		tampered := append([]byte(nil), b...)
		tampered[i+len("package ingestpb")-1] = 'c' // package ingestpc: one character
		if err := os.WriteFile(path, tampered, 0o644); err != nil {
			t.Fatal(err)
		}
		res := gliXMakeVerifyProto(repo)
		if res.code == 0 {
			t.Errorf("GLI-013 (b): verify-proto accepted a generated file with one changed character:\n%s", gliXHead(res.out, 1500))
		}
		if gliXIsEnvFailure(res.out) {
			t.Errorf("GLI-013 (b): the failure looks like an environment problem, not a drift report (the clean run passed in the same environment):\n%s", gliXHead(res.out, 1500))
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, tampered) {
			t.Errorf("GLI-013 (b): the tampered file must stay as it was after verify-proto (read error %v)", err)
		}
	})
	t.Run("proto_field_added_without_regeneration", func(t *testing.T) {
		repo := gliXRepoCopy(t)
		path := filepath.Join(repo, "api", "proto", "dpa", "ingest", "v1alpha1", "ingest.proto")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`(message\s+ClientHello\s*\{)`)
		if !re.Match(b) {
			t.Fatalf("fixture: no `message ClientHello {` in the proto file")
		}
		changed := re.ReplaceAll(b, []byte("${1} string gli_added_field = 99;"))
		if err := os.WriteFile(path, changed, 0o644); err != nil {
			t.Fatal(err)
		}
		res := gliXMakeVerifyProto(repo)
		if res.code == 0 {
			t.Errorf("GLI-013 (c): verify-proto accepted a .proto change that was not regenerated:\n%s", gliXHead(res.out, 1500))
		}
		if gliXIsEnvFailure(res.out) {
			t.Errorf("GLI-013 (c): the failure looks like an environment problem, not a drift report:\n%s", gliXHead(res.out, 1500))
		}
	})
}

// ---------------------------------------------------------------- GLI-130

// GLI-130: grpc v1.72.2 and protobuf v1.36.8 are direct requirements, nothing
// else changed in how go.mod is shaped, go.sum carries the new modules, this
// feature brings no cgo (the ratified internal/nativepcie is not its concern)
// and `go mod tidy` leaves both files unchanged.
func TestGLI130_Dependencies(t *testing.T) {
	root := gliXRoot(t)
	out := gliXGo(t, root, nil, "mod", "edit", "-json")
	var m struct {
		Module  struct{ Path string }
		Go      string
		Require []struct {
			Path     string
			Version  string
			Indirect bool
		}
		Replace []struct{ Old struct{ Path string } }
		Tool    []struct{ Path string }
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("cannot decode go mod edit -json: %v", err)
	}
	if m.Module.Path != gliXModule || m.Go != "1.26.0" {
		t.Errorf("GLI-130: module %q go %q, want %s and go 1.26.0", m.Module.Path, m.Go, gliXModule)
	}
	type req struct {
		version  string
		indirect bool
	}
	reqs := map[string]req{}
	for _, r := range m.Require {
		reqs[r.Path] = req{r.Version, r.Indirect}
	}
	for path, want := range map[string]string{gliXGrpcPath: "v1.72.2", gliXProtobufPath: "v1.36.8"} {
		r, ok := reqs[path]
		switch {
		case !ok:
			t.Errorf("GLI-130: go.mod does not require %s", path)
		case r.version != want:
			t.Errorf("GLI-130: %s is %s, want %s", path, r.version, want)
		case r.indirect:
			t.Errorf("GLI-130: %s is marked // indirect but the code imports it directly", path)
		}
	}
	if len(m.Replace) != 0 || len(m.Tool) != 0 {
		t.Errorf("GLI-130: go.mod has replace (%d) or tool (%d) directives", len(m.Replace), len(m.Tool))
	}
	sum := gliXReadFile(t, "go.sum")
	for _, want := range []string{"google.golang.org/grpc v1.72.2 h1:", "google.golang.org/protobuf v1.36.8 h1:", "google.golang.org/genproto/googleapis/rpc "} {
		if !strings.Contains(sum, want) {
			t.Errorf("GLI-130: go.sum has no entry %q", want)
		}
	}
	// "no cgo" means this feature brings none: internal/nativepcie is a ratified cgo package (native-pcie-observer)
	// and stays out of scope. (1) the new packages have no cgo files - listed with CGO_ENABLED=1 so that a file
	// importing "C" would show up in CgoFiles; (2) the CGO_ENABLED=0 closure of each of the three binaries has no
	// runtime/cgo and no package of this module with cgo files.
	for _, rel := range []string{
		"internal/ingest", "internal/ingest/ingestpb", "internal/transport/mtls", "internal/app/liveingest", "internal/app/framecore",
		"internal/kubernetes/ingestadapter", "internal/agent/liveclient",
	} {
		pkgs := gliXList(t, []string{"CGO_ENABLED=1"}, "./"+rel)
		if len(pkgs) != 1 {
			t.Errorf("GLI-130: ./%s is %d packages, want 1", rel, len(pkgs))
			continue
		}
		if len(pkgs[0].CgoFiles) != 0 {
			t.Errorf("GLI-130: %s uses cgo: %v (this feature brings no cgo)", pkgs[0].ImportPath, pkgs[0].CgoFiles)
		}
	}
	for _, bin := range []string{"./cmd/path-agent", "./cmd/path-controller", "./cmd/pathctl"} {
		for _, p := range gliXList(t, []string{"CGO_ENABLED=0"}, "-deps", bin) {
			if p.ImportPath == "runtime/cgo" {
				t.Errorf("GLI-130: the CGO_ENABLED=0 closure of %s holds runtime/cgo", bin)
			}
			if gliXUnder(p.ImportPath, gliXModule) && len(p.CgoFiles) != 0 {
				t.Errorf("GLI-130: %s (in the CGO_ENABLED=0 closure of %s) has cgo files: %v", p.ImportPath, bin, p.CgoFiles)
			}
		}
	}

	t.Run("mod_tidy_is_clean", func(t *testing.T) {
		repo := gliXRepoCopy(t)
		before := gliXSnapshot(t, repo)
		stdout, stderr, err := gliXRun(repo, gliXEnv(), 10*time.Minute, "go", "mod", "tidy")
		if err != nil {
			if gliXIsEnvFailure(stdout + stderr) {
				t.Skipf("GLI-130: go mod tidy needs the module cache or network (not run): %s", gliXHead(stdout+stderr, 400))
			}
			t.Fatalf("GLI-130: go mod tidy failed: %v\n%s%s", err, stdout, stderr)
		}
		after := gliXSnapshot(t, repo)
		for _, f := range []string{"go.mod", "go.sum"} {
			if before[f] != after[f] {
				t.Errorf("GLI-130: go mod tidy changed %s; commit the tidy result", f)
			}
		}
	})
}

// ---------------------------------------------------------------- GLI-131

// gliXArchCheck runs `make arch-check` in dir and returns the combined output
// and the exit code.
func gliXArchCheck(dir string) (string, int) {
	stdout, stderr, err := gliXRun(dir, gliXEnv(), 10*time.Minute, "make", "--no-print-directory", "arch-check")
	return stdout + stderr, gliXExitCode(err)
}

func gliXPackageClause(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	re := regexp.MustCompile(`(?m)^package\s+([A-Za-z_][A-Za-z0-9_]*)`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if m := re.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	t.Fatalf("fixture: no Go file with a package clause in %s", dir)
	return ""
}

// GLI-131 (a)(b): FORBIDDEN_IMPORTS also catches protobuf, an unmodified copy
// passes arch-check, and internal/app/liveingest importing protobuf or grpc
// (directly or through ingestpb) makes arch-check fail.
func TestGLI131_ArchCheck(t *testing.T) {
	mk := gliXReadFile(t, "Makefile")
	var pattern string
	for _, l := range strings.Split(mk, "\n") {
		if strings.HasPrefix(l, "FORBIDDEN_IMPORTS :=") {
			pattern = strings.TrimSpace(strings.TrimPrefix(l, "FORBIDDEN_IMPORTS :="))
		}
	}
	re, err := regexp.Compile(strings.ReplaceAll(pattern, "$$", "$"))
	if err != nil || pattern == "" {
		t.Fatalf("GLI-131: cannot read FORBIDDEN_IMPORTS from the Makefile: %q %v", pattern, err)
	}
	for _, imp := range []string{
		"google.golang.org/protobuf/proto", "google.golang.org/protobuf/types/known/timestamppb", "google.golang.org/grpc", "google.golang.org/grpc/codes",
		"k8s.io/api/core/v1", "sigs.k8s.io/yaml", "runtime/cgo", gliXModule + "/internal/nativepcie",
	} {
		if !re.MatchString(imp) {
			t.Errorf("GLI-131: FORBIDDEN_IMPORTS does not match %s", imp)
		}
	}
	for _, imp := range []string{"fmt", "sort", gliXModule + "/internal/fleet", "github.com/google/go-cmp/cmp"} {
		if re.MatchString(imp) {
			t.Errorf("GLI-131: FORBIDDEN_IMPORTS matches the allowed package %s", imp)
		}
	}

	diagnostics := []string{"cannot determine module path", "go list ./... failed", "arch-check: go list -deps", "no required module provides package", "syntax error", "build constraints exclude all Go files", "found packages"}
	check := func(t *testing.T, out string, code int, mustName ...string) {
		t.Helper()
		if code == 0 || !strings.Contains(out, "arch-check: FAIL") || strings.Contains(out, "arch-check: OK") {
			t.Errorf("GLI-131 (b): arch-check accepted the injected import (exit %d):\n%s", code, gliXHead(out, 2500))
		}
		for _, n := range mustName {
			if !strings.Contains(out, n) {
				t.Errorf("GLI-131 (b): the arch-check report does not name %s:\n%s", n, gliXHead(out, 2500))
			}
		}
		for _, d := range diagnostics {
			if strings.Contains(out, d) {
				t.Errorf("GLI-131 (b): the failure is a tool or fixture error (%q), not an architecture rejection:\n%s", d, gliXHead(out, 2500))
			}
		}
	}

	t.Run("clean_copy_passes", func(t *testing.T) {
		repo := gliXRepoCopy(t)
		out, code := gliXArchCheck(repo)
		if code != 0 || !strings.Contains(out, "arch-check: OK") || strings.Contains(out, "arch-check: FAIL") {
			t.Fatalf("GLI-131 (a): make arch-check on an unmodified copy exited %d:\n%s", code, gliXHead(out, 2500))
		}
		// the negative cases below are only meaningful when the clean copy passes.
		inject := func(t *testing.T, repo, body string) {
			t.Helper()
			dir := filepath.Join(repo, "internal", "app", "liveingest")
			pkg := gliXPackageClause(t, dir)
			src := "package " + pkg + "\n\n" + body
			if err := os.WriteFile(filepath.Join(dir, "zz_gli_arch_probe.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct {
			name, body string
			names      []string
		}{
			{"protobuf_direct", "import _ \"google.golang.org/protobuf/proto\"\n", []string{"google.golang.org/protobuf", "internal/app/liveingest"}},
			{"ingestpb_transitive", "import _ \"" + gliXModule + "/internal/ingest/ingestpb\"\n", []string{"google.golang.org/protobuf", "google.golang.org/grpc", "internal/app/liveingest"}},
			{"grpc_direct", "import _ \"google.golang.org/grpc/codes\"\n", []string{"google.golang.org/grpc", "internal/app/liveingest"}},
		} {
			c := c
			t.Run(c.name, func(t *testing.T) {
				repo := gliXRepoCopy(t)
				inject(t, repo, c.body)
				out, code := gliXArchCheck(repo)
				if gliXIsEnvFailure(out) && !strings.Contains(out, "arch-check: FAIL") {
					t.Skipf("GLI-131 (b): the injected module is not available here (not run): %s", gliXHead(out, 400))
				}
				check(t, out, code, c.names...)
			})
		}
	})
}

// GLI-131 (c)(d)(e): the link rules, with go list -deps on the three host
// targets and both CGO settings (exact package paths, never a substring
// pattern such as net/http, which also matches golang.org/x/net/http2).
func TestGLI131_LinkRules(t *testing.T) {
	m := gliXModule
	deps := func(t *testing.T, goos, goarch, cgo string, pkg string) []string {
		t.Helper()
		var out []string
		for _, p := range gliXList(t, []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=" + cgo}, "-deps", pkg) {
			out = append(out, p.ImportPath)
		}
		return out
	}
	for _, tg := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		for _, cgo := range []string{"0", "1"} {
			name := fmt.Sprintf("%s_%s_cgo%s", tg[0], tg[1], cgo)
			t.Run(name, func(t *testing.T) {
				// (c) path-agent: no Kubernetes, no native PCIe, no runtime/cgo without cgo.
				agent := deps(t, tg[0], tg[1], cgo, "./cmd/path-agent")
				sawGrpc := false
				for _, d := range agent {
					switch {
					case gliXIsK8s(d), gliXUnder(d, m+"/internal/kubernetes"), gliXUnder(d, m+"/internal/nativepcie"):
						t.Errorf("GLI-131(c) %s: path-agent depends on %s", name, d)
					case cgo == "0" && d == "runtime/cgo":
						t.Errorf("GLI-131(c) %s: path-agent depends on runtime/cgo with CGO_ENABLED=0", name)
					}
					sawGrpc = sawGrpc || gliXUnder(d, gliXGrpcPath)
				}
				if !sawGrpc {
					t.Errorf("GLI-131(c) %s: path-agent does not depend on grpc: the live client is not linked", name)
				}
				// (d) pathctl: no grpc, no protobuf, no agent, no ingest (until S3b-3 amends GFX-100(b)).
				for _, d := range deps(t, tg[0], tg[1], cgo, "./cmd/pathctl") {
					if gliXUnder(d, gliXGrpcPath) || gliXUnder(d, gliXProtobufPath) || gliXUnder(d, m+"/internal/agent") || gliXUnder(d, m+"/internal/ingest") {
						t.Errorf("GLI-131(d) %s: pathctl depends on %s", name, d)
					}
				}
				// (e) path-controller: no native PCIe; no runtime/cgo without cgo.
				for _, d := range deps(t, tg[0], tg[1], cgo, "./cmd/path-controller") {
					if gliXUnder(d, m+"/internal/nativepcie") {
						t.Errorf("GLI-131(e) %s: path-controller depends on %s", name, d)
					}
					if cgo == "0" && d == "runtime/cgo" {
						t.Errorf("GLI-131(e) %s: path-controller depends on runtime/cgo with CGO_ENABLED=0", name)
					}
				}
			})
		}
	}
}

// ---------------------------------------------------------------- GLI-132

// GLI-132: the packages that import grpc build on the targets outside the host
// triple (js/wasm, windows/amd64, plan9/amd64). The three binaries on the three
// host targets, ./... on js/wasm and windows/amd64, the plan9 subset and go vet
// are TestGKA196_* of tests/gka_build_test.go. A failure here on plan9 is a
// matter for the specification (extend the GKA-196 plan9 exclusion list), not a
// code fix.
func TestGLI132_GrpcPackagesBuildOffHost(t *testing.T) {
	root := gliXRoot(t)
	for _, tg := range [][2]string{{"js", "wasm"}, {"windows", "amd64"}, {"plan9", "amd64"}} {
		t.Run(tg[0]+"_"+tg[1], func(t *testing.T) {
			stdout, stderr, err := gliXRun(root, gliXEnv("CGO_ENABLED=0", "GOOS="+tg[0], "GOARCH="+tg[1]), 30*time.Minute,
				"go", "build", "./internal/ingest/...", "./internal/agent/...", "./cmd/path-agent")
			if err != nil {
				hint := ""
				if tg[0] == "plan9" {
					hint = " (plan9: failing here means GKA-196's exclusion list must be extended by a spec revision, not by a code fix)"
				}
				t.Errorf("GLI-132: CGO_ENABLED=0 GOOS=%s GOARCH=%s go build ./internal/ingest/... ./internal/agent/... ./cmd/path-agent failed%s: %v\n%s%s",
					tg[0], tg[1], hint, err, gliXHead(stdout, 800), gliXHead(stderr, 2500))
			}
		})
	}
	// the new packages must not import internal/kubernetes: otherwise the plan9 subset of TestGKA196 would skip them silently.
	for _, p := range gliXList(t, nil, "./internal/ingest/...", "./internal/agent/...", "./cmd/path-agent", "./internal/transport/...", "./internal/app/...") {
		for _, d := range p.Deps {
			if gliXUnder(d, gliXModule+"/internal/kubernetes") {
				t.Errorf("GLI-132: %s depends on %s, so TestGKA196 would exclude it from the plan9 build", p.ImportPath, d)
			}
		}
	}
}
