package tests_test

// Build, dependency, Makefile and layout contract of the Kubernetes API feature
// (GKA-001 .. GKA-005, GKA-190 .. GKA-198). Tests that need the module cache or
// the network (controller-gen, go mod tidy) probe for it first and skip with an
// explicit reason when the environment cannot run them; a failure after the
// probe succeeded is a real failure.

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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	gkaCModule   = "github.com/aetertinas9/data-path-assurance"
	gkaCBaseline = "e9008ca" // product commit the feature is built on
)

func gkaCRoot(t testing.TB) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("cannot resolve the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

// gkaCRun runs a command and returns stdout, stderr and the error.
func gkaCRun(dir string, env []string, timeout time.Duration, name string, args ...string) (string, string, error) {
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

func gkaCEnv(extra ...string) []string {
	env := append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	return append(env, extra...)
}

// gkaCGo runs `go <args>` in dir with extra environment entries.
func gkaCGo(t testing.TB, dir string, extra []string, args ...string) string {
	t.Helper()
	stdout, stderr, err := gkaCRun(dir, gkaCEnv(extra...), 20*time.Minute, "go", args...)
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s%s", strings.Join(args, " "), err, gkaCHead(stdout, 1500), gkaCHead(stderr, 3000))
	}
	return stdout
}

func gkaCHead(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n...(truncated)"
	}
	return s
}

func gkaCExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// gkaCIsEnvFailure recognizes failures caused by a missing module cache or
// network rather than by the code under test.
func gkaCIsEnvFailure(out string) bool {
	for _, p := range []string{
		"dial tcp", "no such host", "i/o timeout", "connection refused", "Temporary failure in name resolution",
		"lookup ", "module lookup disabled", "GOPROXY=off", "cannot find module providing", "no required module provides",
		"failed to fetch", "unrecognized import path", "server misbehaving", "TLS handshake timeout", "proxyconnect",
		"missing go.sum entry", "verifying module", "reading https://", "reading file://", "timed out",
	} {
		if strings.Contains(out, p) {
			return true
		}
	}
	return false
}

type gkaCPkg struct {
	ImportPath string
	Dir        string
	Name       string
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Deps       []string
}

// gkaCListPkgs runs `go list -json` for the patterns (non-test view).
func gkaCListPkgs(t testing.TB, extraEnv []string, patterns ...string) []gkaCPkg {
	t.Helper()
	out := gkaCGo(t, gkaCRoot(t), extraEnv, append([]string{"list", "-json"}, patterns...)...)
	dec := json.NewDecoder(strings.NewReader(out))
	var pkgs []gkaCPkg
	for {
		var p gkaCPkg
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

func gkaCIsStd(imp string) bool {
	first := strings.SplitN(imp, "/", 2)[0]
	return !strings.Contains(first, ".")
}

func gkaCIsK8s(imp string) bool {
	return strings.HasPrefix(imp, "k8s.io/") || strings.HasPrefix(imp, "sigs.k8s.io/")
}

// gkaCSkipWalk lists top-level entries of the repository that are never copied
// or scanned.
func gkaCSkipTop(name string) bool {
	return strings.HasPrefix(name, ".") || name == "build" || name == "testdata"
}

// gkaCRepoCopy copies the repository into t.TempDir() (no dot-directories,
// build or testdata) so tests can modify it and run generators in it.
func gkaCRepoCopy(t *testing.T) string {
	t.Helper()
	root := gkaCRoot(t)
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
		if top := strings.Split(rel, string(filepath.Separator))[0]; gkaCSkipTop(top) {
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

// gkaCSnapshot maps every regular file below dir to its content.
func gkaCSnapshot(t testing.TB, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read %s: %v", path, err)
		}
		rel, _ := filepath.Rel(dir, path)
		m[rel] = string(b)
		return nil
	})
	return m
}

// gkaCExportedNames returns the exported top-level identifiers (types, funcs,
// vars, consts; methods excluded) declared by the non-test files of a package.
func gkaCExportedNames(t testing.TB, importPath string) map[string]bool {
	t.Helper()
	pkgs := gkaCListPkgs(t, nil, importPath)
	if len(pkgs) != 1 {
		t.Fatalf("package %s not found", importPath)
	}
	names := map[string]bool{}
	for _, file := range pkgs[0].GoFiles {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(pkgs[0].Dir, file), nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", file, err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						if sp.Name.IsExported() {
							names[sp.Name.Name] = true
						}
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							if n.IsExported() {
								names[n.Name] = true
							}
						}
					}
				}
			}
		}
	}
	return names
}

// ------------------------------------------------------------------ GKA-001

// GKA-001: every deliverable of section 1 exists in its place.
func TestGKA001_Deliverables(t *testing.T) {
	root := gkaCRoot(t)
	for pattern, name := range map[string]string{
		"./internal/kubernetes/api/v1alpha1": "v1alpha1",
		"./internal/kubernetes/controller":   "controller",
		"./internal/app":                     "app",
		"./cmd/path-controller":              "main",
	} {
		pkgs := gkaCListPkgs(t, nil, pattern)
		if len(pkgs) != 1 || pkgs[0].Name != name || len(pkgs[0].GoFiles) == 0 {
			t.Errorf("GKA-001: %s must be one package %q with Go files, got %+v", pattern, name, pkgs)
		}
	}
	for _, f := range []string{
		"deploy/crds/infrastructure.data-path-assurance.io_gpufleets.yaml",
		"deploy/crds/infrastructure.data-path-assurance.io_gpudevices.yaml",
		"deploy/crds/infrastructure.data-path-assurance.io_nodepathstates.yaml",
		"internal/kubernetes/api/v1alpha1/zz_generated.deepcopy.go",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("GKA-001: %s is missing: %v", f, err)
		}
	}
	// the Makefile targets and the go.mod dependencies of item (f).
	for _, target := range []string{"generate", "verify-generated", "envtest-assets", "test-envtest", "test", "all", "arch-check"} {
		stdout, stderr, err := gkaCRun(root, gkaCEnv(), time.Minute, "make", "-n", "--no-print-directory", target)
		if err != nil {
			t.Errorf("GKA-001: make -n %s failed: %v\n%s%s", target, err, gkaCHead(stdout, 500), gkaCHead(stderr, 500))
		}
	}
	// the new application files of internal/app.
	appNames := gkaCExportedNames(t, "./internal/app")
	for _, want := range []string{"NodeAssessor", "NewLiveAssessor", "NewNoObservationSource"} {
		if !appNames[want] {
			t.Errorf("GKA-001(d): internal/app does not export %s", want)
		}
	}
}

// ------------------------------------------------------------------ GKA-002

// GKA-002: nothing outside the feature's scope was delivered: no RBAC or
// workload manifests, container files, protobuf, Helm chart, runbook or extra
// deploy content.
func TestGKA002_NoOutOfScopeDeliverables(t *testing.T) {
	root := gkaCRoot(t)
	workload := regexp.MustCompile(`(?m)^kind:\s*(ClusterRole|Role|ClusterRoleBinding|RoleBinding|Deployment|DaemonSet|StatefulSet|ServiceAccount|Service|Pod|ValidatingWebhookConfiguration|MutatingWebhookConfiguration)\s*$`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if gkaCSkipTop(top) || top == "tests" || top == "profiles" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		base := strings.ToLower(d.Name())
		slash := filepath.ToSlash(rel)
		switch {
		case strings.HasPrefix(base, "dockerfile"), strings.HasPrefix(base, "containerfile"):
			t.Errorf("GKA-002: %s is a container build file (S4a)", slash)
		case strings.HasSuffix(base, ".proto"):
			t.Errorf("GKA-002: %s is a protobuf file (S3b)", slash)
		case strings.Contains(base, "runbook"):
			t.Errorf("GKA-002: %s is a runbook (S4a)", slash)
		case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
			if strings.HasPrefix(slash, "deploy/crds/") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if workload.Match(b) {
				t.Errorf("GKA-002: %s is an RBAC or workload manifest (S4a)", slash)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "deploy"))
	if err != nil {
		t.Fatalf("GKA-002: cannot list deploy/: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "crds,helm" {
		t.Errorf("GKA-002: deploy/ contains %v, want exactly crds and helm (no RBAC, Deployment or chart)", names)
	}
	for _, dir := range []string{"deploy/helm", "api/proto"} {
		es, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Errorf("GKA-002/GKA-197: %s must stay as an empty directory holding only .gitkeep: %v", dir, err)
			continue
		}
		for _, e := range es {
			if e.Name() != ".gitkeep" {
				t.Errorf("GKA-002: %s/%s is out of scope (Helm chart is S4a, proto is S3b)", dir, e.Name())
			}
		}
	}
}

// ------------------------------------------------------------------ GKA-003

// GKA-003: the existing public identifiers of internal/app remain; with
// GKA_BASELINE_COMMIT set, no existing product file was modified or removed.
func TestGKA003_ExistingContractsUnchanged(t *testing.T) {
	root := gkaCRoot(t)
	appNames := gkaCExportedNames(t, "./internal/app")
	for _, name := range []string{
		"Explanation", "Optional", "SourceView", "Identity", "PathSegment", "FindingView", "EvidenceView", "ImpactView", "CoverageView",
		"AllocationView", "WorkloadView", "TotalCounts", "Limitation", "DeviceSummary", "TargetKind", "Request", "NodeIdentity",
		"Diagnostic", "Frame", "Replay", "ReplaySource", "ErrTargetNotFound", "ErrEvaluation", "PublicError", "ExplainFleet",
	} {
		if !appNames[name] {
			t.Errorf("GKA-003: the S2-2 identifier app.%s is no longer exported", name)
		}
	}

	t.Run("product_files_unchanged", func(t *testing.T) {
		// Opt-in: the comparison is against the commit the feature was built on,
		// so it only makes sense while this feature is the newest one. Set
		// GKA_BASELINE_COMMIT to the base commit to enable this check.
		base := os.Getenv("GKA_BASELINE_COMMIT")
		if base == "" {
			t.Skipf("GKA-003 (product files unchanged): set GKA_BASELINE_COMMIT=%s to compare the tree with the base commit (not run)", gkaCBaseline)
		}
		if _, _, err := gkaCRun(root, gkaCEnv(), 30*time.Second, "git", "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
			t.Skipf("GKA-003 (product files unchanged): git or the commit %s is not available here (%v)", base, err)
		}
		diff, stderr, err := gkaCRun(root, gkaCEnv(), time.Minute, "git", "diff", "--name-status", "--no-renames", base, "--", ".")
		if err != nil {
			t.Skipf("GKA-003 (product files unchanged): git diff failed: %v %s", err, stderr)
		}
		allowedChange := func(p string) bool {
			return p == "go.mod" || p == "go.sum" || p == "Makefile" || p == "README.md" || strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "tests/")
		}
		allowedDelete := map[string]bool{"api/v1alpha1/.gitkeep": true, "cmd/path-controller/.gitkeep": true}
		for _, line := range strings.Split(strings.TrimSpace(diff), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			status, path := f[0], f[1]
			switch {
			case status == "A":
			case status == "D" && allowedDelete[path]:
			case allowedChange(path):
			default:
				t.Errorf("GKA-003: %s changed (%s) since %s; existing product files, internal/fleet and the S2-2 internal/app API must not be modified", path, status, base)
			}
		}
	})
}

// ------------------------------------------------------------------ GKA-004

// GKA-004: import rules of the new packages and of the rest of the module.
func TestGKA004_ImportRules(t *testing.T) {
	pkgs := gkaCListPkgs(t, nil, "./...")
	byPath := map[string]gkaCPkg{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	api := gkaCModule + "/internal/kubernetes/api/v1alpha1"
	ctl := gkaCModule + "/internal/kubernetes/controller"
	cmdPkg := gkaCModule + "/cmd/path-controller"
	for _, want := range []string{api, ctl, cmdPkg, gkaCModule + "/internal/app"} {
		if _, ok := byPath[want]; !ok {
			t.Fatalf("GKA-004: package %s does not exist", want)
		}
	}
	// (a) the API types: standard library and apimachinery only.
	for _, imp := range byPath[api].Imports {
		if !gkaCIsStd(imp) && !strings.HasPrefix(imp, "k8s.io/apimachinery/") {
			t.Errorf("GKA-004(a): v1alpha1 imports %s (standard library and k8s.io/apimachinery/... only)", imp)
		}
	}
	// (b) the controller and any sub-package of it.
	allowedModule := map[string]bool{
		gkaCModule + "/internal/app": true, gkaCModule + "/internal/fleet": true, gkaCModule + "/pkg/model": true, api: true,
	}
	for _, p := range pkgs {
		if p.ImportPath != ctl && !strings.HasPrefix(p.ImportPath, ctl+"/") {
			continue
		}
		for _, imp := range p.Imports {
			switch {
			case gkaCIsStd(imp), gkaCIsK8s(imp), imp == "github.com/go-logr/logr", allowedModule[imp]:
			case imp == ctl || strings.HasPrefix(imp, ctl+"/"):
			default:
				t.Errorf("GKA-004(b): %s imports %s (allowed: standard library, k8s.io/*, sigs.k8s.io/*, go-logr/logr, internal/app, internal/fleet, pkg/model, v1alpha1)", p.ImportPath, imp)
			}
			for _, bad := range []string{"/internal/agent", "/internal/offline", "/internal/cli", "/internal/nativepcie", "/cmd/"} {
				if strings.HasPrefix(imp, gkaCModule) && strings.Contains(imp, bad) {
					t.Errorf("GKA-004(b): %s imports the forbidden %s", p.ImportPath, imp)
				}
			}
		}
	}
	// (c) internal/app and the domain core never import Kubernetes libraries.
	core := regexp.MustCompile(`^` + regexp.QuoteMeta(gkaCModule) + `/(pkg/model|internal/(identity|graph|evidence|fleet|correlation|domains|impact|policy|app))(/|$)`)
	for _, p := range pkgs {
		if !core.MatchString(p.ImportPath) {
			continue
		}
		for _, imp := range p.Imports {
			if gkaCIsK8s(imp) {
				t.Errorf("GKA-004(c): domain core package %s imports %s", p.ImportPath, imp)
			}
		}
	}
	// (d) the binary: standard library, internal/app and the controller only.
	for _, imp := range byPath[cmdPkg].Imports {
		if !gkaCIsStd(imp) && imp != gkaCModule+"/internal/app" && imp != ctl {
			t.Errorf("GKA-004(d): cmd/path-controller imports %s (standard library, internal/app and internal/kubernetes/controller only)", imp)
		}
	}
	// (e) only internal/kubernetes/... imports k8s.io or sigs.k8s.io directly.
	for _, p := range pkgs {
		if strings.HasPrefix(p.ImportPath, gkaCModule+"/internal/kubernetes") {
			continue
		}
		for _, imp := range p.Imports {
			if gkaCIsK8s(imp) {
				t.Errorf("GKA-004(e): %s imports %s directly; only internal/kubernetes/... may", p.ImportPath, imp)
			}
		}
	}
}

// ------------------------------------------------------------------ GKA-005

var gkaCForbiddenVarTokens = []string{
	"NewScheme", "runtime.Scheme", "SchemeBuilder", "Registry", "prometheus.", "client.Client", "kubernetes.Clientset", "rest.Config", "http.Client",
}

// gkaCStaticFile applies the GKA-005 rules to one parsed file.
func gkaCStaticFile(t *testing.T, path string, mainPkg bool) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("GKA-005: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("GKA-005: cannot parse %s: %v", path, err)
	}
	where := func(p token.Pos) string { return fmt.Sprintf("%s:%d", filepath.Base(path), fset.Position(p).Line) }

	for _, is := range f.Imports {
		p, _ := strconv.Unquote(is.Path.Value)
		if is.Name != nil && is.Name.Name == "_" && p != "embed" {
			t.Errorf("GKA-005: %s blank import of %s (no import side effects)", where(is.Pos()), p)
		}
		if p == "log" {
			t.Errorf("GKA-005: %s imports the standard log package (logging uses log/slog)", where(is.Pos()))
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "init" {
				t.Errorf("GKA-005: %s declares func init()", where(d.Pos()))
			}
			if d.Body == nil {
				continue
			}
			allowedNow := d.Name.Name == "NewSystemClock" || (d.Recv != nil && d.Name.Name == "Now")
			ast.Inspect(d.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case id.Name == "time" && sel.Sel.Name == "Now" && !allowedNow:
					t.Errorf("GKA-005: %s calls time.Now outside NewSystemClock (time comes from the injected Clock)", where(sel.Pos()))
				case id.Name == "fmt" && (sel.Sel.Name == "Print" || sel.Sel.Name == "Printf" || sel.Sel.Name == "Println"):
					t.Errorf("GKA-005/GKA-162: %s writes to stdout with fmt.%s", where(sel.Pos()), sel.Sel.Name)
				case mainPkg && id.Name == "os" && sel.Sel.Name == "Exit" && d.Name.Name != "main":
					t.Errorf("GKA-164: %s calls os.Exit outside func main", where(sel.Pos()))
				}
				return true
			})
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				text := string(src[fset.Position(vs.Pos()).Offset:fset.Position(vs.End()).Offset])
				allBlank := true
				for _, n := range vs.Names {
					allBlank = allBlank && n.Name == "_"
				}
				if allBlank {
					continue
				}
				for _, tok := range gkaCForbiddenVarTokens {
					if strings.Contains(text, tok) {
						t.Errorf("GKA-005: %s package-level variable holds mutable global state (%s): %s", where(vs.Pos()), tok, strings.Split(text, "\n")[0])
					}
				}
				ast.Inspect(vs, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && sel.Sel.Name == "Now" {
							t.Errorf("GKA-005: %s package-level use of time.Now", where(sel.Pos()))
						}
					}
					return true
				})
			}
		}
	}
	// context.Context is always the first parameter.
	ast.Inspect(f, func(n ast.Node) bool {
		ft, ok := n.(*ast.FuncType)
		if !ok || ft.Params == nil {
			return true
		}
		idx := 0
		for _, field := range ft.Params.List {
			isCtx := false
			if sel, ok := field.Type.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "context" && sel.Sel.Name == "Context" {
					isCtx = true
				}
			}
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			if isCtx && idx > 0 {
				t.Errorf("GKA-005: %s takes context.Context as parameter %d (it must be the first)", where(field.Pos()), idx+1)
			}
			idx += count
		}
		return true
	})
}

// GKA-005: no init(), blank-import side effects, package-level mutable
// scheme/registry/client, stdlib log, direct time.Now, or context.Context that
// is not the first parameter, in the three new package trees.
func TestGKA005_StaticDiscipline(t *testing.T) {
	pkgs := gkaCListPkgs(t, nil, "./internal/kubernetes/...", "./cmd/path-controller")
	if len(pkgs) < 3 {
		t.Fatalf("GKA-005: expected the api, controller and cmd packages, got %d packages", len(pkgs))
	}
	for _, p := range pkgs {
		for _, name := range p.GoFiles {
			gkaCStaticFile(t, filepath.Join(p.Dir, name), p.Name == "main")
		}
	}
}

// ------------------------------------------------------------------ GKA-190

type gkaCModJSON struct {
	Module  struct{ Path string }
	Go      string
	Require []struct {
		Path     string
		Version  string
		Indirect bool
	}
	Replace []struct {
		Old struct{ Path string }
		New struct{ Path string }
	}
	Tool []struct{ Path string }
}

func gkaCLoadMod(t testing.TB, dir string) gkaCModJSON {
	t.Helper()
	out := gkaCGo(t, dir, nil, "mod", "edit", "-json")
	var m gkaCModJSON
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("cannot decode go mod edit -json: %v", err)
	}
	return m
}

// GKA-190: dependency set and versions, no replace/tool, go.sum committed,
// nothing uses cgo.
func TestGKA190_Dependencies(t *testing.T) {
	root := gkaCRoot(t)
	m := gkaCLoadMod(t, root)
	if m.Module.Path != gkaCModule || m.Go != "1.26.0" {
		t.Errorf("GKA-190: module %q go %q, want %s and go 1.26.0", m.Module.Path, m.Go, gkaCModule)
	}
	req := map[string]struct {
		version  string
		indirect bool
	}{}
	for _, r := range m.Require {
		req[r.Path] = struct {
			version  string
			indirect bool
		}{r.Version, r.Indirect}
	}
	for _, p := range []string{"k8s.io/api", "k8s.io/apimachinery", "k8s.io/client-go", "k8s.io/apiextensions-apiserver"} {
		r, ok := req[p]
		if !ok {
			t.Errorf("GKA-190: go.mod does not require %s", p)
			continue
		}
		if r.version != "v0.35.3" {
			t.Errorf("GKA-190: %s is %s, want v0.35.3 (one patch for the four k8s.io modules)", p, r.version)
		}
	}
	if r, ok := req["sigs.k8s.io/controller-runtime"]; !ok || r.version != "v0.23.3" {
		t.Errorf("GKA-190: controller-runtime = %v, want v0.23.3", r.version)
	}
	for _, p := range []string{"github.com/go-logr/logr", "sigs.k8s.io/controller-runtime", "k8s.io/api", "k8s.io/apimachinery", "k8s.io/client-go"} {
		if r, ok := req[p]; ok && r.indirect {
			t.Errorf("GKA-190: %s is marked // indirect but the code imports it directly", p)
		}
	}
	if _, ok := req["github.com/go-logr/logr"]; !ok {
		t.Errorf("GKA-190: github.com/go-logr/logr must be a direct requirement")
	}
	for p := range req {
		if strings.HasPrefix(p, "sigs.k8s.io/controller-tools") {
			t.Errorf("GKA-190: controller-tools must not be a requirement (%s); controller-gen runs through go run @version", p)
		}
	}
	if len(m.Replace) != 0 {
		t.Errorf("GKA-190: go.mod has replace directives: %+v", m.Replace)
	}
	if len(m.Tool) != 0 {
		t.Errorf("GKA-190: go.mod has tool directives: %+v", m.Tool)
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil || len(sum) == 0 {
		t.Errorf("GKA-190: go.sum must be committed: %v", err)
	} else {
		for _, want := range []string{"k8s.io/client-go v0.35.3 h1:", "sigs.k8s.io/controller-runtime v0.23.3 h1:"} {
			if !strings.Contains(string(sum), want) {
				t.Errorf("GKA-190: go.sum has no entry %q", want)
			}
		}
	}
	for _, p := range gkaCListPkgs(t, nil, "./internal/kubernetes/...", "./cmd/path-controller", "./internal/app") {
		if len(p.CgoFiles) != 0 {
			t.Errorf("GKA-190: %s uses cgo: %v", p.ImportPath, p.CgoFiles)
		}
	}
}

// GKA-190: `go mod tidy` leaves go.mod and go.sum unchanged (module cache or
// network required).
func TestGKA190_ModTidyIsClean(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	repo := gkaCRepoCopy(t)
	before := gkaCSnapshot(t, repo)
	stdout, stderr, err := gkaCRun(repo, gkaCEnv(), 10*time.Minute, "go", "mod", "tidy")
	if err != nil {
		if gkaCIsEnvFailure(stdout + stderr) {
			t.Skipf("GKA-190: go mod tidy needs the module cache or network, which are not available (not run): %s", gkaCHead(stdout+stderr, 400))
		}
		t.Fatalf("GKA-190: go mod tidy failed: %v\n%s%s", err, stdout, stderr)
	}
	after := gkaCSnapshot(t, repo)
	for _, f := range []string{"go.mod", "go.sum"} {
		if before[f] != after[f] {
			t.Errorf("GKA-190: go mod tidy changed %s; commit the tidy result:\n%s", f, gkaCLineDiff(before[f], after[f]))
		}
	}
}

// gkaCLineDiff shows the lines that differ between a and b (set difference).
func gkaCLineDiff(a, b string) string {
	seenA, seenB := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(a, "\n") {
		seenA[l] = true
	}
	var out []string
	for _, l := range strings.Split(b, "\n") {
		seenB[l] = true
		if !seenA[l] {
			out = append(out, "+ "+l)
		}
	}
	for _, l := range strings.Split(a, "\n") {
		if !seenB[l] {
			out = append(out, "- "+l)
		}
	}
	if len(out) > 40 {
		out = append(out[:40], "...")
	}
	return strings.Join(out, "\n")
}

// ------------------------------------------------------------------ GKA-191

const (
	gkaCGenPkg     = "sigs.k8s.io/controller-tools/cmd/controller-gen"
	gkaCGenVersion = "v0.20.1"
)

// gkaCGeneratorOrSkip probes that controller-gen v0.20.1 can be run here.
func gkaCGeneratorOrSkip(t *testing.T) {
	t.Helper()
	stdout, stderr, err := gkaCRun(t.TempDir(), gkaCEnv(), 10*time.Minute, "go", "run", gkaCGenPkg+"@"+gkaCGenVersion, "--version")
	if err != nil {
		if gkaCIsEnvFailure(stdout+stderr) || strings.Contains(stdout+stderr, "go: downloading") {
			t.Skipf("controller-gen %s cannot be fetched or built here (module cache or network needed; not run): %s", gkaCGenVersion, gkaCHead(stdout+stderr, 400))
		}
		t.Skipf("controller-gen %s does not run in this environment (not run): %v %s", gkaCGenVersion, err, gkaCHead(stdout+stderr, 400))
	}
	if !strings.Contains(stdout+stderr, gkaCGenVersion) {
		t.Skipf("controller-gen --version printed %q, expected %s (not run)", strings.TrimSpace(stdout+stderr), gkaCGenVersion)
	}
}

// GKA-191: `make generate` pins controller-gen v0.20.1 and the package carries
// the prescribed markers.
func TestGKA191_GenerateTargetAndMarkers(t *testing.T) {
	root := gkaCRoot(t)
	stdout, stderr, err := gkaCRun(root, gkaCEnv(), time.Minute, "make", "-n", "--no-print-directory", "generate")
	if err != nil {
		t.Fatalf("GKA-191: make -n generate: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, gkaCGenPkg+"@"+gkaCGenVersion) {
		t.Errorf("GKA-191: make generate does not run `go run %s@%s`:\n%s", gkaCGenPkg, gkaCGenVersion, stdout)
	}
	if strings.Contains(stdout, "@latest") || regexp.MustCompile(`controller-gen@v0\.(1[0-9]|20\.0|20\.[2-9]|2[1-9])`).MatchString(stdout) {
		t.Errorf("GKA-191: make generate must pin exactly %s (no @latest or other version):\n%s", gkaCGenVersion, stdout)
	}
	if !strings.Contains(stdout, "deploy/crds") {
		t.Errorf("GKA-191: make generate does not write the CRDs to deploy/crds:\n%s", stdout)
	}

	pkgs := gkaCListPkgs(t, nil, "./internal/kubernetes/api/v1alpha1")
	if len(pkgs) != 1 {
		t.Fatalf("GKA-191: v1alpha1 package not found")
	}
	var all strings.Builder
	for _, name := range pkgs[0].GoFiles {
		if name == "zz_generated.deepcopy.go" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(pkgs[0].Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	src := all.String()
	for _, want := range []string{"+groupName=infrastructure.data-path-assurance.io", "+kubebuilder:object:generate=true"} {
		if !strings.Contains(src, want) {
			t.Errorf("GKA-191: the package lacks the marker %s", want)
		}
	}
	for _, res := range [][2]string{{"gpufleets", "gpufleet"}, {"gpudevices", "gpudevice"}, {"nodepathstates", "nodepathstate"}} {
		want := "+kubebuilder:resource:scope=Cluster,path=" + res[0] + ",singular=" + res[1]
		if !strings.Contains(src, want) {
			t.Errorf("GKA-191: the package lacks the marker %s", want)
		}
	}
	if n := strings.Count(src, "+kubebuilder:object:root=true"); n < 6 {
		t.Errorf("GKA-191: %d object:root markers, want at least 6 (three resources and three lists)", n)
	}
	if n := strings.Count(src, "+kubebuilder:subresource:status"); n < 3 {
		t.Errorf("GKA-191: %d subresource:status markers, want at least 3", n)
	}
	if n := strings.Count(src, "+kubebuilder:printcolumn"); n < 14 {
		t.Errorf("GKA-191/GKA-048: %d printcolumn markers, want at least 14 (5+5+4)", n)
	}
	if !strings.Contains(src, "+kubebuilder:default=Audit") {
		t.Errorf("GKA-191/GKA-012: the Mode default marker +kubebuilder:default=Audit is missing")
	}
}

// GKA-191: running `make generate` in a copy reproduces the checked-in files
// byte for byte and creates nothing else.
func TestGKA191_GenerateReproducesCheckedInFiles(t *testing.T) {
	gkaCGeneratorOrSkip(t)
	repo := gkaCRepoCopy(t)
	before := gkaCSnapshot(t, repo)
	stdout, stderr, err := gkaCRun(repo, gkaCEnv(), 15*time.Minute, "make", "--no-print-directory", "generate")
	if err != nil {
		t.Fatalf("GKA-191: make generate failed: %v\n%s%s", err, gkaCHead(stdout, 2000), gkaCHead(stderr, 3000))
	}
	after := gkaCSnapshot(t, repo)
	for path, old := range before {
		if strings.HasPrefix(path, "deploy/crds/") || strings.HasPrefix(path, "internal/kubernetes/api/v1alpha1/") {
			if after[path] != old {
				t.Errorf("GKA-191/192: make generate changed the checked-in %s (the committed generated files are stale)", path)
			}
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("GKA-191/040: make generate created the unexpected file %s (only the four generated files are allowed)", path)
		}
	}
}

// ------------------------------------------------------------------ GKA-192

// GKA-192: verify-generated passes on a clean tree without git or writes, and
// fails - leaving the tampered file as it is - on a changed CRD or deepcopy.
func TestGKA192_VerifyGenerated(t *testing.T) {
	gkaCGeneratorOrSkip(t)
	repo := gkaCRepoCopy(t) // no .git: the target must not need git
	run := func() (string, int) {
		stdout, stderr, err := gkaCRun(repo, gkaCEnv(), 15*time.Minute, "make", "--no-print-directory", "verify-generated")
		return gkaCHead(stdout+stderr, 3000), gkaCExitCode(err)
	}
	checkedIn := func() map[string]string {
		m := map[string]string{}
		for k, v := range gkaCSnapshot(t, filepath.Join(repo, "deploy")) {
			m["deploy/"+k] = v
		}
		for k, v := range gkaCSnapshot(t, filepath.Join(repo, "internal", "kubernetes", "api", "v1alpha1")) {
			m["v1alpha1/"+k] = v
		}
		return m
	}
	before := checkedIn()
	if out, code := run(); code != 0 {
		t.Fatalf("GKA-192(a): make verify-generated on a clean tree exited %d:\n%s", code, out)
	}
	after := checkedIn()
	if len(before) != len(after) {
		t.Errorf("GKA-192: verify-generated added or removed files in the checked-in tree (%d -> %d files)", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("GKA-192: verify-generated wrote to the checked-in tree (%s changed)", k)
		}
	}

	tamper := func(rel string, mutate func([]byte) []byte, what string) {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		orig, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("GKA-192 %s: %v", what, err)
		}
		changed := mutate(append([]byte(nil), orig...))
		if bytes.Equal(changed, orig) {
			t.Fatalf("GKA-192 %s: the fixture change did not change %s", what, rel)
		}
		if err := os.WriteFile(path, changed, 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := run()
		if code == 0 {
			t.Errorf("GKA-192 %s: make verify-generated exited 0 although %s differs from the generated output", what, rel)
		}
		now, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(now, changed) {
			t.Errorf("GKA-192 %s: the gate rewrote %s; it must leave the tampered file as it is (%v)", what, rel, err)
		}
		if code != 0 && !gkaCMentions(out, filepath.Base(rel), "differ", "drift", "stale", "generate") {
			t.Logf("GKA-192 %s: exit %d without a hint of the drift in its output:\n%s", what, code, out)
		}
		if err := os.WriteFile(path, orig, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := run(); code != 0 {
			t.Errorf("GKA-192 %s: after restoring %s the gate still fails (%d), so the earlier failure was not the drift:\n%s", what, rel, code, out)
		}
	}
	tamper("deploy/crds/infrastructure.data-path-assurance.io_gpufleets.yaml", func(b []byte) []byte {
		return bytes.Replace(b, []byte(gkaCGenVersion), []byte("v0.20.2"), 1)
	}, "(b) one character of a CRD")
	tamper("internal/kubernetes/api/v1alpha1/zz_generated.deepcopy.go", func(b []byte) []byte {
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "// DeepCopyInto is an autogenerated deepcopy function") {
				return []byte(strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"))
			}
		}
		return b
	}, "(c) one line of zz_generated.deepcopy.go")
}

func gkaCMentions(s string, words ...string) bool {
	for _, a := range words {
		if strings.Contains(s, a) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ GKA-193 / 194

// gkaCShim writes a fake `go` first on PATH that forwards `go env` to the real
// toolchain and records every other invocation (arguments and the value of
// KUBEBUILDER_ASSETS) into a log.
func gkaCShim(t *testing.T) (pathEnv, logPath string) {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	binDir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "calls.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"env\" ]; then exec '" + realGo + "' \"$@\"; fi\n" +
		"{ printf 'ARGS:'; for a in \"$@\"; do printf ' [%s]' \"$a\"; done; printf '\\n';\n" +
		"  if [ \"${KUBEBUILDER_ASSETS+set}\" = set ]; then printf 'KBA=[%s]\\n' \"$KUBEBUILDER_ASSETS\"; else printf 'KBA=<unset>\\n'; fi; } >> '" + logPath + "'\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"), logPath
}

type gkaCCall struct {
	Args []string
	KBA  string // "<unset>" when the variable was not exported
}

func gkaCReadCalls(t *testing.T, logPath string) []gkaCCall {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	var calls []gkaCCall
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		args := regexp.MustCompile(`\[([^\]]*)\]`).FindAllStringSubmatch(strings.TrimPrefix(lines[i], "ARGS:"), -1)
		var a []string
		for _, m := range args {
			a = append(a, m[1])
		}
		kba := strings.TrimSuffix(strings.TrimPrefix(lines[i+1], "KBA="), "")
		kba = strings.TrimSuffix(strings.TrimPrefix(kba, "["), "]")
		calls = append(calls, gkaCCall{a, kba})
	}
	return calls
}

// gkaCEnvWithout returns the environment without the named variables.
func gkaCEnvWithout(names ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		drop := false
		for _, n := range names {
			drop = drop || strings.HasPrefix(kv, n+"=")
		}
		if !drop {
			env = append(env, kv)
		}
	}
	return env
}

func gkaCHostTarget(t *testing.T) string {
	t.Helper()
	out, _, err := gkaCRun(gkaCRoot(t), gkaCEnv(), time.Minute, "go", "env", "GOOS", "GOARCH")
	f := strings.Fields(out)
	if err != nil || len(f) != 2 {
		t.Fatalf("go env GOOS GOARCH: %v %q", err, out)
	}
	return f[0] + "-" + f[1]
}

func gkaCMakeEnvtestDir(t *testing.T, repo, target string, files ...string) string {
	t.Helper()
	dir := filepath.Join(repo, "build", "envtest", "k8s", "1.35.0-"+target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func gkaCNeedMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler: the native prerequisite of `make test` cannot be built here (not run)")
	}
}

// GKA-193: make envtest-assets runs the pinned setup-envtest, and `make test`
// never fetches assets.
func TestGKA193_EnvtestAssetsTarget(t *testing.T) {
	gkaCNeedMake(t)
	repo := gkaCRepoCopy(t)
	pathEnv, logPath := gkaCShim(t)
	env := append(gkaCEnvWithout("KUBEBUILDER_ASSETS", "PATH"), pathEnv)
	stdout, stderr, err := gkaCRun(repo, env, 5*time.Minute, "make", "--no-print-directory", "envtest-assets")
	if err != nil {
		t.Fatalf("GKA-193: make envtest-assets failed: %v\n%s%s", err, stdout, stderr)
	}
	want := []string{"run", "sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.0.0-20260305142021-f9589b9f2b9d", "use", "1.35.0", "--bin-dir", "build/envtest", "-p", "path"}
	found := false
	for _, c := range gkaCReadCalls(t, logPath) {
		if len(c.Args) > 1 && strings.Contains(c.Args[1], "setup-envtest") {
			found = true
			if strings.Join(c.Args, " ") != strings.Join(want, " ") {
				t.Errorf("GKA-193: envtest-assets ran `go %s`, want `go %s`", strings.Join(c.Args, " "), strings.Join(want, " "))
			}
		}
	}
	if !found {
		t.Errorf("GKA-193: make envtest-assets did not run go run ...setup-envtest (calls: %v)", gkaCReadCalls(t, logPath))
	}
}

// GKA-193: make test-envtest fails with one guidance line when the assets are
// missing, and otherwise exports the absolute directory (overriding any
// inherited value) and runs the envtest suite with -race -count=1.
func TestGKA193_TestEnvtestTarget(t *testing.T) {
	gkaCNeedMake(t)
	target := gkaCHostTarget(t)
	pathEnv, logPath := gkaCShim(t)
	env := func(extra ...string) []string {
		return append(append(gkaCEnvWithout("KUBEBUILDER_ASSETS", "PATH"), pathEnv), extra...)
	}
	run := func(repo string, e []string) (string, string, int) {
		stdout, stderr, err := gkaCRun(repo, e, 5*time.Minute, "make", "-s", "--no-print-directory", "test-envtest")
		return stdout, stderr, gkaCExitCode(err)
	}
	countTests := func() int {
		n := 0
		for _, c := range gkaCReadCalls(t, logPath) {
			if len(c.Args) > 0 && c.Args[0] == "test" {
				n++
			}
		}
		return n
	}

	// missing assets directory
	repo := gkaCRepoCopy(t)
	_, stderr, code := run(repo, env())
	if code == 0 {
		t.Errorf("GKA-193: make test-envtest succeeded without the envtest assets")
	}
	if n := strings.Count(stderr, "make envtest-assets"); n != 1 {
		t.Errorf("GKA-193: stderr mentions `make envtest-assets` %d times, want exactly one guidance line:\n%s", n, stderr)
	}
	if countTests() != 0 {
		t.Errorf("GKA-193: go test ran although the assets are missing")
	}

	// one of the two executables missing
	for _, only := range []string{"kube-apiserver", "etcd"} {
		repo := gkaCRepoCopy(t)
		gkaCMakeEnvtestDir(t, repo, target, only)
		if _, stderr, code := run(repo, env()); code == 0 {
			t.Errorf("GKA-193: make test-envtest succeeded with only %s present:\n%s", only, stderr)
		}
	}
	if countTests() != 0 {
		t.Errorf("GKA-193: go test ran with an incomplete assets directory")
	}

	// complete directory: export the absolute path, override an inherited value.
	repo = gkaCRepoCopy(t)
	dir := gkaCMakeEnvtestDir(t, repo, target, "kube-apiserver", "etcd")
	_, stderr, code = run(repo, env("KUBEBUILDER_ASSETS=/inherited/not/used"))
	if code != 0 {
		t.Fatalf("GKA-193: make test-envtest with complete assets exited %d:\n%s", code, stderr)
	}
	var got *gkaCCall
	for _, c := range gkaCReadCalls(t, logPath) {
		if len(c.Args) > 0 && c.Args[0] == "test" {
			c := c
			got = &c
		}
	}
	if got == nil {
		t.Fatalf("GKA-193: make test-envtest did not run go test")
	}
	for _, want := range []string{"-race", "-count=1", "./tests/kubeapi/..."} {
		if !gkaCHas(got.Args, want) {
			t.Errorf("GKA-193: test-envtest ran `go %s`, want it to include %s (go test -race -count=1 ./tests/kubeapi/...)", strings.Join(got.Args, " "), want)
		}
	}
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(got.KBA)
	if !filepath.IsAbs(got.KBA) || gotDir != wantDir {
		t.Errorf("GKA-193: KUBEBUILDER_ASSETS = %q, want the absolute path %q (an inherited value must not win)", got.KBA, dir)
	}
}

// GKA-194: make test honors an existing KUBEBUILDER_ASSETS, otherwise exports
// the assets directory when it exists, otherwise leaves the variable unset;
// it never downloads; the existing targets are intact.
func TestGKA194_MakeTestIntegration(t *testing.T) {
	gkaCNeedMake(t)
	target := gkaCHostTarget(t)
	pathEnv, logPath := gkaCShim(t)
	env := func(extra ...string) []string {
		return append(append(gkaCEnvWithout("KUBEBUILDER_ASSETS", "PATH"), pathEnv), extra...)
	}
	lastTest := func() *gkaCCall {
		var got *gkaCCall
		for _, c := range gkaCReadCalls(t, logPath) {
			if len(c.Args) > 0 && c.Args[0] == "test" {
				c := c
				got = &c
			}
		}
		return got
	}
	run := func(repo string, e []string) {
		t.Helper()
		if stdout, stderr, err := gkaCRun(repo, e, 10*time.Minute, "make", "--no-print-directory", "test"); err != nil {
			t.Fatalf("GKA-194: make test failed: %v\n%s%s", err, gkaCHead(stdout, 1500), gkaCHead(stderr, 1500))
		}
	}

	// (A) an inherited value is respected, with or without the directory.
	repo := gkaCRepoCopy(t)
	gkaCMakeEnvtestDir(t, repo, target, "kube-apiserver", "etcd")
	run(repo, env("KUBEBUILDER_ASSETS=/preset/dir"))
	if c := lastTest(); c == nil || c.KBA != "/preset/dir" {
		t.Errorf("GKA-194: with KUBEBUILDER_ASSETS preset, go test saw %+v, want /preset/dir", c)
	}
	// (B) directory present, variable unset: exported as the directory.
	repo = gkaCRepoCopy(t)
	dir := gkaCMakeEnvtestDir(t, repo, target, "kube-apiserver", "etcd")
	run(repo, env())
	c := lastTest()
	if c == nil {
		t.Fatalf("GKA-194: make test did not run go test")
	}
	if !strings.Contains(strings.Join(c.Args, " "), "./...") {
		t.Errorf("GKA-194: make test ran `go %s`, want go test ./...", strings.Join(c.Args, " "))
	}
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(c.KBA)
	if !filepath.IsAbs(c.KBA) || gotDir != wantDir {
		t.Errorf("GKA-194: with the assets directory present, KUBEBUILDER_ASSETS = %q, want the absolute path %q", c.KBA, dir)
	}
	// (C) no directory, variable unset: not exported (envtest tests then skip explicitly).
	repo = gkaCRepoCopy(t)
	run(repo, env())
	if c := lastTest(); c == nil || (c.KBA != "<unset>" && c.KBA != "") {
		t.Errorf("GKA-194: without assets, go test saw KUBEBUILDER_ASSETS=%+v, want it unset", c)
	}
	for _, call := range gkaCReadCalls(t, logPath) {
		if len(call.Args) > 1 && strings.Contains(strings.Join(call.Args, " "), "setup-envtest") {
			t.Errorf("GKA-194/193: make test downloaded envtest assets: go %s", strings.Join(call.Args, " "))
		}
	}
}

// GKA-194: the pre-existing targets keep their recipes.
func TestGKA194_ExistingTargetsUnchanged(t *testing.T) {
	root := gkaCRoot(t)
	for _, target := range []string{"native", "native-asan", "native-example", "native-test", "native-test-sanitize", "clean-native", "arch-check", "fmt", "vet", "build", "all", "test"} {
		if _, stderr, err := gkaCRun(root, gkaCEnv(), time.Minute, "make", "-n", "--no-print-directory", target); err != nil {
			t.Errorf("GKA-194: make -n %s failed: %v %s", target, err, gkaCHead(stderr, 300))
		}
	}
	all, _, err := gkaCRun(root, gkaCEnv(), time.Minute, "make", "-n", "--no-print-directory", "all")
	if err != nil {
		t.Fatalf("GKA-194: make -n all: %v", err)
	}
	for _, want := range []string{"go build ./...", "go vet ./...", "arch-check"} {
		if !strings.Contains(all, want) {
			t.Errorf("GKA-194: make all no longer runs %q:\n%s", want, gkaCHead(all, 1500))
		}
	}
	for _, bad := range []string{"go test", "KUBEBUILDER", "setup-envtest"} {
		if strings.Contains(all, bad) {
			t.Errorf("GKA-194: make all now involves %q (the existing `all` must stay build, vet, arch-check):\n%s", bad, gkaCHead(all, 1500))
		}
	}
}

// ------------------------------------------------------------------ GKA-195

// gkaCArchFixture builds a tiny module that uses the repository Makefile and a
// stub k8s-like dependency, and runs `make arch-check` in it.
func gkaCArchFixture(t *testing.T, depPath string, files map[string]string) (string, int) {
	t.Helper()
	root := gkaCRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	const module = "example.com/gka-arch-fixture"
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", string(makefile))
	write("go.mod", "module "+module+"\n\ngo 1.26.0\n\nrequire "+depPath+" v0.0.0\nreplace "+depPath+" => ./stubs/dep\n")
	write("stubs/dep/go.mod", "module "+depPath+"\n\ngo 1.26.0\n")
	write("stubs/dep/dep.go", "package dep\n")
	for rel, content := range files {
		write(rel, strings.ReplaceAll(content, "MODULE", module))
	}
	env := append(os.Environ(), "GOWORK=off", "GO111MODULE=on", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off", "GOFLAGS=", "MAKEFLAGS=", "MFLAGS=")
	stdout, stderr, err := gkaCRun(dir, env, 2*time.Minute, "make", "--no-print-directory", "arch-check")
	return stdout + stderr, gkaCExitCode(err)
}

// GKA-195: the repository passes arch-check, the forbidden pattern covers
// sigs.k8s.io, the domain core has no Kubernetes dependency (direct or
// transitive), and a violation is caught.
func TestGKA195_ArchCheck(t *testing.T) {
	root := gkaCRoot(t)
	stdout, stderr, err := gkaCRun(root, gkaCEnv(), 5*time.Minute, "make", "--no-print-directory", "arch-check")
	if err != nil || !strings.Contains(stdout, "arch-check: OK") || strings.Contains(stdout, "arch-check: FAIL") {
		t.Errorf("GKA-195: make arch-check on the repository: %v\n%s%s", err, stdout, stderr)
	}
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var pattern string
	for _, l := range strings.Split(string(mk), "\n") {
		if strings.HasPrefix(l, "FORBIDDEN_IMPORTS :=") {
			pattern = strings.TrimSpace(strings.TrimPrefix(l, "FORBIDDEN_IMPORTS :="))
		}
	}
	re, err := regexp.Compile(strings.ReplaceAll(pattern, "$$", "$"))
	if err != nil || pattern == "" {
		t.Fatalf("GKA-195: cannot read FORBIDDEN_IMPORTS from the Makefile: %q %v", pattern, err)
	}
	for _, imp := range []string{"k8s.io/api/core/v1", "sigs.k8s.io/controller-runtime", "sigs.k8s.io/yaml", "runtime/cgo", gkaCModule + "/internal/nativepcie"} {
		if !re.MatchString(imp) {
			t.Errorf("GKA-195: FORBIDDEN_IMPORTS does not match %s", imp)
		}
	}
	if !strings.Contains(string(mk), "internal/app") {
		t.Errorf("GKA-195: DOMAIN_CORE must keep internal/app")
	}
	coreRE := regexp.MustCompile(`^` + regexp.QuoteMeta(gkaCModule) + `/(pkg/model|internal/(identity|graph|evidence|fleet|correlation|domains|impact|policy|app))(/|$)`)
	checked := 0
	for _, p := range gkaCListPkgs(t, nil, "./...") {
		if !coreRE.MatchString(p.ImportPath) {
			continue
		}
		checked++
		for _, dep := range p.Deps {
			if gkaCIsK8s(dep) {
				t.Errorf("GKA-195: domain core package %s reaches %s transitively", p.ImportPath, dep)
			}
		}
	}
	if checked == 0 {
		t.Errorf("GKA-195: no domain core package found")
	}

	// the checker itself: a violation is reported, a clean fixture passes.
	out, code := gkaCArchFixture(t, "sigs.k8s.io/controller-runtime", map[string]string{
		"internal/app/app.go": "package app\n\nimport _ \"sigs.k8s.io/controller-runtime\"\n",
	})
	if code == 0 || !strings.Contains(out, "arch-check: FAIL") || !strings.Contains(out, "sigs.k8s.io/controller-runtime") {
		t.Errorf("GKA-195: arch-check accepted internal/app importing sigs.k8s.io directly (exit %d):\n%s", code, out)
	}
	out, code = gkaCArchFixture(t, "sigs.k8s.io/controller-runtime", map[string]string{
		"internal/app/app.go":                  "package app\n\nimport _ \"MODULE/internal/kubernetes/helper\"\n",
		"internal/kubernetes/helper/helper.go": "package helper\n\nimport _ \"sigs.k8s.io/controller-runtime\"\n",
	})
	if code == 0 || !strings.Contains(out, "arch-check: FAIL") || !strings.Contains(out, "sigs.k8s.io/controller-runtime") {
		t.Errorf("GKA-195: arch-check accepted internal/app reaching sigs.k8s.io transitively (exit %d):\n%s", code, out)
	}
	out, code = gkaCArchFixture(t, "sigs.k8s.io/controller-runtime", map[string]string{
		"internal/app/app.go":                  "package app\n",
		"internal/kubernetes/helper/helper.go": "package helper\n\nimport _ \"sigs.k8s.io/controller-runtime\"\n",
	})
	if code != 0 || !strings.Contains(out, "arch-check: OK") {
		t.Errorf("GKA-195: arch-check rejected a fixture where only internal/kubernetes imports sigs.k8s.io (exit %d):\n%s", code, out)
	}
}

// GKA-195: link rules for every host target and both CGO settings.
func TestGKA195_LinkRules(t *testing.T) {
	targets := [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}
	list := func(goos, goarch, cgo string, pkgs ...string) []string {
		p := gkaCListPkgs(t, []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=" + cgo}, append([]string{"-deps"}, pkgs...)...)
		var out []string
		for _, x := range p {
			out = append(out, x.ImportPath)
		}
		return out
	}
	for _, tg := range targets {
		for _, cgo := range []string{"0", "1"} {
			name := fmt.Sprintf("%s_%s_cgo%s", tg[0], tg[1], cgo)
			t.Run(name, func(t *testing.T) {
				for _, dep := range list(tg[0], tg[1], cgo, "./cmd/pathctl", "./cmd/path-agent") {
					if gkaCIsK8s(dep) {
						t.Errorf("GKA-195 %s: pathctl/path-agent depend on %s", name, dep)
					}
				}
				deps := list(tg[0], tg[1], cgo, "./cmd/path-controller")
				for _, dep := range deps {
					if strings.HasSuffix(dep, "/internal/nativepcie") {
						t.Errorf("GKA-195 %s: path-controller depends on %s", name, dep)
					}
					if cgo == "0" && dep == "runtime/cgo" {
						t.Errorf("GKA-195 %s: path-controller depends on runtime/cgo with CGO_ENABLED=0", name)
					}
				}
			})
		}
	}
}

// ------------------------------------------------------------------ GKA-196

func gkaCCrossBuild(t *testing.T, goos, goarch string, pkgs ...string) {
	t.Helper()
	root := gkaCRoot(t)
	args := append([]string{"build"}, pkgs...)
	stdout, stderr, err := gkaCRun(root, gkaCEnv("CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch), 30*time.Minute, "go", args...)
	if err != nil {
		t.Errorf("GKA-196: CGO_ENABLED=0 GOOS=%s GOARCH=%s go build %s failed: %v\n%s%s",
			goos, goarch, strings.Join(pkgs, " "), err, gkaCHead(stdout, 800), gkaCHead(stderr, 2500))
	}
}

// GKA-196: the three binaries cross-build for the three host targets; every
// package builds for js/wasm, windows/amd64, darwin/arm64, linux/amd64 and
// linux/arm64; on plan9/amd64 only the Kubernetes packages are excluded.
func TestGKA196_CrossBuilds(t *testing.T) {
	bins := []string{"./cmd/path-controller", "./cmd/pathctl", "./cmd/path-agent"}
	for _, tg := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		t.Run("binaries_"+tg[0]+"_"+tg[1], func(t *testing.T) { gkaCCrossBuild(t, tg[0], tg[1], bins...) })
	}
	for _, tg := range [][2]string{{"js", "wasm"}, {"windows", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		t.Run("all_"+tg[0]+"_"+tg[1], func(t *testing.T) { gkaCCrossBuild(t, tg[0], tg[1], "./...") })
	}
	t.Run("all_plan9_amd64_without_kubernetes", func(t *testing.T) {
		var keep []string
		for _, p := range gkaCListPkgs(t, nil, "./...") {
			if len(p.GoFiles)+len(p.CgoFiles) == 0 {
				continue
			}
			if p.ImportPath == gkaCModule+"/cmd/path-controller" || strings.HasPrefix(p.ImportPath, gkaCModule+"/internal/kubernetes") {
				continue
			}
			importsK8sPackages := false
			for _, d := range p.Deps {
				importsK8sPackages = importsK8sPackages || strings.HasPrefix(d, gkaCModule+"/internal/kubernetes")
			}
			if !importsK8sPackages {
				keep = append(keep, p.ImportPath)
			}
		}
		if len(keep) == 0 {
			t.Fatalf("GKA-196: no package is left for plan9/amd64")
		}
		gkaCCrossBuild(t, "plan9", "amd64", keep...)
	})
}

// GKA-196: go vet passes on the host and for linux/amd64 and linux/arm64
// (CGO off). Run in a copy so the native artifacts `make vet` prepares do not
// touch the working tree.
func TestGKA196_Vet(t *testing.T) {
	repo := gkaCRepoCopy(t)
	for _, tg := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}} {
		t.Run("vet_"+tg[0]+"_"+tg[1], func(t *testing.T) {
			stdout, stderr, err := gkaCRun(repo, gkaCEnv("CGO_ENABLED=0", "GOOS="+tg[0], "GOARCH="+tg[1]), 30*time.Minute, "go", "vet", "./...")
			if err != nil {
				t.Errorf("GKA-196: CGO_ENABLED=0 GOOS=%s GOARCH=%s go vet ./... failed: %v\n%s%s", tg[0], tg[1], err, gkaCHead(stdout, 800), gkaCHead(stderr, 3000))
			}
		})
	}
	t.Run("vet_host", func(t *testing.T) {
		if _, err := exec.LookPath("make"); err != nil {
			t.Skip("make is not on PATH")
		}
		if _, err := exec.LookPath("cc"); err != nil {
			t.Skip("no C compiler for the native prerequisite of make vet (not run)")
		}
		stdout, stderr, err := gkaCRun(repo, gkaCEnv(), 30*time.Minute, "make", "--no-print-directory", "vet")
		if err != nil {
			t.Errorf("GKA-196: host go vet ./... (make vet) failed: %v\n%s%s", err, gkaCHead(stdout, 800), gkaCHead(stderr, 3000))
		}
	})
}

// ------------------------------------------------------------ GKA-197 / 198

// GKA-197: the empty api/v1alpha1 placeholder is gone; the other placeholders
// stay; cmd/path-controller has no .gitkeep once it has content.
func TestGKA197_PlaceholderDirectories(t *testing.T) {
	root := gkaCRoot(t)
	if _, err := os.Stat(filepath.Join(root, "api", "v1alpha1")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("GKA-197: api/v1alpha1 must not exist in the working tree (the .gitkeep and the directory are removed), stat: %v", err)
	}
	for _, keep := range []string{"api/proto/.gitkeep", "deploy/helm/.gitkeep"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(keep))); err != nil {
			t.Errorf("GKA-197: %s must stay untouched: %v", keep, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "cmd", "path-controller"))
	if err != nil {
		t.Fatalf("GKA-197: cmd/path-controller: %v", err)
	}
	hasGo := false
	for _, e := range entries {
		hasGo = hasGo || strings.HasSuffix(e.Name(), ".go")
	}
	if hasGo {
		if _, err := os.Stat(filepath.Join(root, "cmd", "path-controller", ".gitkeep")); err == nil {
			t.Errorf("GKA-197: cmd/path-controller/.gitkeep must be deleted once the directory has content")
		}
	}
}

// GKA-198: this feature creates no documentation or README file: none in the
// directories it owns (updating the existing user documentation is the
// maintainer's job after convergence and is deliberately not policed here).
func TestGKA198_NoNewDocumentationFiles(t *testing.T) {
	root := gkaCRoot(t)
	isDoc := func(name string) bool {
		l := strings.ToLower(name)
		return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".markdown") || strings.HasSuffix(l, ".rst") || strings.HasSuffix(l, ".adoc") ||
			strings.HasSuffix(l, ".txt") || strings.HasPrefix(l, "readme") || strings.HasPrefix(l, "changelog") || strings.HasPrefix(l, "notes")
	}
	for _, dir := range []string{"internal/kubernetes", "cmd/path-controller", "deploy", "api", "tests/kubeapi"} {
		_ = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && isDoc(d.Name()) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("GKA-198: %s is a new documentation file (user documentation is updated by the maintainer after convergence)", filepath.ToSlash(rel))
			}
			return nil
		})
	}
}

// GKA-198 (maintainer follow-up): the controller and its custom resources ship
// in the source tree, so no user document may tell readers they are absent. A
// sentence that announces something missing must not name them while
// cmd/path-controller and deploy/crds exist.
func TestGKA198_UserDocsDoNotDenyShippedController(t *testing.T) {
	root := gkaCRoot(t)
	for _, p := range []string{"cmd/path-controller/main.go", "deploy/crds"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			t.Fatalf("GKA-198 precondition: %s: %v", p, err)
		}
	}
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs = append(docs, filepath.Join(root, "README.md"))
	containsAny := func(s string, words []string) bool {
		for _, w := range words {
			if strings.Contains(s, w) {
				return true
			}
		}
		return false
	}
	absence := []string{"do not expect", "does not yet provide", "do not yet provide", "not present in", "absent from"}
	shipped := []string{"a controller", "the controller", "kubernetes resources", "custom resources", "crds"}
	for _, p := range docs {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(strings.Join(strings.Fields(string(b)), " "))
		for sentence := range strings.SplitSeq(text, ". ") {
			if containsAny(sentence, absence) && containsAny(sentence, shipped) {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("GKA-198: %s says the shipped controller or custom resources are missing: %q", filepath.ToSlash(rel), sentence)
			}
		}
	}
}
