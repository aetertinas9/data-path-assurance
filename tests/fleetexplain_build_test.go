package tests_test

// Products, scope, dependencies, placement, determinism, build targets, the
// verification seam and test-input hygiene (GFX-001..005, GFX-100, GFX-101,
// GFX-104).

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGFX001_OfflineExplainProducts: pathctl explains gpu and node offline in
// both output forms.
func TestGFX001_OfflineExplainProducts(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSWidth(t)), gfxDefaultFleet())
	g := c.Check("gpu", gfxDevName, "GFX-001 gpu")
	n := c.Check("node", gfoNodeName, "GFX-001 node")
	gfxWant(t, "GFX-001", "kinds", g.Kind+"/"+n.Kind, "GPUExplanation/NodeExplanation")
}

// TestGFX002_OfflineOnlyClaims: results are marked offline and never live;
// live transport is refused; no workload or allocation evidence is claimed.
func TestGFX002_OfflineOnlyClaims(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), gfxDefaultFleet())
	for _, kind := range []string{"gpu", "node"} {
		name := map[string]string{"gpu": gfxDevName, "node": gfoNodeName}[kind]
		e := c.Check(kind, name, "GFX-002 "+kind)
		gfxWantLims(t, "GFX-002 "+kind, e, []string{"offline", "offline_trust_not_live " + gfxProfileWidth, "traffic_path_unverified"}, nil)
		if len(e.Workloads) != 0 {
			t.Errorf("GFX-002: %s claims affected workloads", kind)
		}
		text := gfxOK(t, c.Run(kind, name, "text"), "GFX-002 text")
		if first := strings.SplitN(string(text), "\n", 2)[0]; !strings.HasSuffix(first, " mode=offline") || strings.Contains(string(text), "mode=live") {
			t.Errorf("GFX-002/X-T-13: first line %q, want mode=offline", first)
		}
	}
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, "explain", "gpu", gfxDevName, "--server", "127.0.0.1:1"), 5, "GFX-002 live transport")
}

// gfxGo runs a go command in the repository root.
func gfxGo(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = gfoRepoRoot(t)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestGFX003_NoNewRequirements: go.mod keeps the module line and no require.
func TestGFX003_NoNewRequirements(t *testing.T) {
	t.Parallel()
	mod, err := os.ReadFile(filepath.Join(gfoRepoRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module "+gfoModule+"\n") {
		t.Errorf("GFX-003: go.mod module line changed")
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if l := strings.TrimSpace(line); l == "require" || strings.HasPrefix(l, "require ") || strings.HasPrefix(l, "require(") {
			t.Errorf("GFX-003/GFX-100 (c): go.mod has a require directive: %q", l)
		}
	}
}

// TestGFX005_PackagePlacement: the four responsibilities exist in their
// packages with the direct imports GFX-005 allows.
func TestGFX005_PackagePlacement(t *testing.T) {
	t.Parallel()
	imports := func(pkg string) []string {
		out, err := gfxGo(t, nil, "list", "-f", `{{join .Imports "\n"}}`, pkg)
		if err != nil {
			t.Fatalf("GFX-005: go list %s: %v\n%s", pkg, err, out)
		}
		var list []string
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l != "" {
				list = append(list, l)
			}
		}
		return list
	}
	m := gfoModule
	allowedCmd := gfxSet("context", "os", m+"/internal/app", m+"/internal/offline", m+"/internal/cli/explain")
	for _, imp := range imports("./cmd/pathctl") {
		if !allowedCmd[imp] {
			t.Errorf("GFX-005 (a): cmd/pathctl imports %s", imp)
		}
	}
	for _, imp := range imports("./internal/offline") {
		if strings.HasPrefix(imp, m+"/internal/agent") || strings.HasPrefix(imp, m+"/internal/nativepcie") {
			t.Errorf("GFX-005 (c): internal/offline imports %s", imp)
		}
	}
	imports("./internal/cli/explain")
	out, err := gfxGo(t, nil, "list", "-f", "{{.ImportPath}}", "./internal/app/...")
	if err != nil {
		t.Fatalf("GFX-005 (d): go list ./internal/app/...: %v\n%s", err, out)
	}
	forbiddenStd := gfxSet("encoding/json", "io", "io/fs", "io/ioutil", "os", "os/exec", "path/filepath", "log", "log/slog", "net", "net/http", "syscall", "unsafe")
	ratified := gfxSet(m+"/pkg/model", m+"/internal/identity", m+"/internal/graph", m+"/internal/evidence", m+"/internal/domains/pcie", m+"/internal/fleet")
	for _, pkg := range strings.Fields(out) {
		for _, imp := range imports(pkg) {
			switch {
			case forbiddenStd[imp]:
				t.Errorf("GFX-005 (d): %s imports the excluded standard package %s", pkg, imp)
			case strings.HasPrefix(imp, m+"/"):
				if !ratified[imp] && imp != m+"/internal/app" && !strings.HasPrefix(imp, m+"/internal/app/") {
					t.Errorf("GFX-005 (d): %s imports %s (only ratified packages and internal/app are allowed)", pkg, imp)
				}
			case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
				t.Errorf("GFX-005 (d)/GFX-003: %s imports the third-party package %s", pkg, imp)
			}
		}
	}
}

// TestGFX004_Determinism: identical input bytes under other paths, working
// directories and environments give identical stdout, stderr and exit codes.
func TestGFX004_Determinism(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSWidth(t))
	fleetJSON := gfxDefaultFleet().JSON()
	in1 := gfxWriteInputs(t, a.Raw, fleetJSON)
	dir2 := filepath.Join(t.TempDir(), "other place", "deeper")
	in2 := gfxInputs{Dir: dir2, Artifact: filepath.Join(dir2, "x.snapshot"), Fleet: filepath.Join(dir2, "y")}
	gfxWriteFile(t, in2.Artifact, a.Raw)
	gfxWriteFile(t, in2.Fleet, []byte(fleetJSON))
	bad := strings.Replace(fleetJSON, `"freshnessSeconds":60`, `"freshnessSeconds":0`, 1)
	badIn1 := gfxWriteInputs(t, a.Raw, bad)
	badIn2 := gfxWriteInputs(t, a.Raw, bad)
	envs := [][]string{
		nil,
		append(gfxEnv(), "TZ=Asia/Seoul", "LANG=ko_KR.UTF-8", "LC_ALL=ko_KR.UTF-8", "GOMAXPROCS=1"),
		append(gfxEnv(), "TZ=America/St_Johns", "LANG=C", "GOMAXPROCS=7", "HOSTNAME=elsewhere"),
	}
	type run struct {
		kind, name, output string
		in                 gfxInputs
		dir                string
		env                []string
	}
	for _, req := range [][3]string{{"gpu", gfxDevName, "json"}, {"gpu", gfxDevName, "text"}, {"node", gfoNodeName, "json"}, {"node", gfoNodeName, "text"}} {
		var ref gfoResult
		for i, r := range []run{
			{req[0], req[1], req[2], in1, "", envs[0]},
			{req[0], req[1], req[2], in2, dir2, envs[1]},
			{req[0], req[1], req[2], in1, t.TempDir(), envs[2]},
			{req[0], req[1], req[2], in2, "", envs[0]},
		} {
			res := gfxRun(t, bins.Pathctl, gfoRunOpt{Env: r.env, Dir: r.dir}, gfxArgs(r.kind, r.name, r.in, r.output)...)
			if i == 0 {
				ref = res
				gfxOK(t, res, "GFX-004 reference "+strings.Join(req[:], " "))
				continue
			}
			if res.Exit != ref.Exit || !bytes.Equal(res.Stdout, ref.Stdout) || !bytes.Equal(res.Stderr, ref.Stderr) {
				t.Errorf("GFX-004 %v run %d: exit/stdout/stderr differ from the reference (exit %d vs %d)", req, i, res.Exit, ref.Exit)
			}
		}
	}
	r1 := gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, badIn1, "json")...)
	r2 := gfxRun(t, bins.Pathctl, gfoRunOpt{Env: envs[1], Dir: t.TempDir()}, gfxArgs("gpu", gfxDevName, badIn2, "json")...)
	gfxFail(t, r1, 7, "GFX-004 failing reference")
	if r1.Exit != r2.Exit || !bytes.Equal(r1.Stderr, r2.Stderr) {
		t.Errorf("GFX-004/GFX-012: failing runs under different paths differ: %q vs %q", r1.Stderr, r2.Stderr)
	}
}

// TestGFX100_BuildAndDependencies: CGO-free cross builds of pathctl, the
// dependency closure (CGO 0 and 1), make arch-check and the repository-wide
// build targets.
func TestGFX100_BuildAndDependencies(t *testing.T) {
	t.Parallel()
	makefile, err := os.ReadFile(filepath.Join(gfoRepoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var forbidden []string
	sc := bufio.NewScanner(bytes.NewReader(makefile))
	for sc.Scan() {
		if l := sc.Text(); strings.HasPrefix(l, "FORBIDDEN_IMPORTS :=") {
			for _, p := range strings.Split(strings.TrimSpace(strings.TrimPrefix(l, "FORBIDDEN_IMPORTS :=")), "|") {
				p = strings.NewReplacer(`\.`, ".", "$$", "", "^", "").Replace(p)
				forbidden = append(forbidden, p)
			}
		}
	}
	if len(forbidden) == 0 {
		t.Fatalf("GFX-100: FORBIDDEN_IMPORTS not found in the Makefile")
	}
	for _, cgo := range []string{"0", "1"} {
		out, err := gfxGo(t, []string{"CGO_ENABLED=" + cgo}, "list", "-deps", "-f", "{{.ImportPath}}|{{.Standard}}|{{len .CgoFiles}}", "./cmd/pathctl")
		if err != nil {
			t.Fatalf("GFX-100 (b): go list -deps ./cmd/pathctl: %v\n%s", err, out)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			parts := strings.Split(line, "|")
			if len(parts) != 3 {
				t.Fatalf("GFX-100: unexpected go list line %q", line)
			}
			path, std, cgoFiles := parts[0], parts[1], parts[2]
			for _, bad := range []string{gfoModule + "/internal/nativepcie", gfoModule + "/internal/agent"} {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("GFX-100 (b) CGO_ENABLED=%s: pathctl depends on %s", cgo, path)
				}
			}
			if path == "runtime/cgo" {
				t.Errorf("GFX-100 (b) CGO_ENABLED=%s: pathctl depends on runtime/cgo", cgo)
			}
			for _, p := range forbidden {
				if p != "" && p != "runtime/cgo" && strings.Contains(path, p) {
					t.Errorf("GFX-100 (b) CGO_ENABLED=%s: %s matches FORBIDDEN_IMPORTS pattern %q", cgo, path, p)
				}
			}
			if std == "true" {
				continue
			}
			if path != gfoModule && !strings.HasPrefix(path, gfoModule+"/") {
				t.Errorf("GFX-100 (b) CGO_ENABLED=%s: non-standard, non-module dependency %s", cgo, path)
			}
			if cgoFiles != "0" {
				t.Errorf("GFX-100 (b) CGO_ENABLED=%s: %s has %s cgo files", cgo, path, cgoFiles)
			}
		}
	}
	cmd := exec.Command("make", "--no-print-directory", "arch-check")
	cmd.Dir = gfoRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "arch-check: OK") {
		t.Errorf("GFX-100 (d): make arch-check: %v\n%s", err, out)
	}
	if out, err := gfxGo(t, nil, "list", "./internal/app/..."); err != nil || !strings.Contains(out, gfoModule+"/internal/app") {
		t.Errorf("GFX-100 (d): internal/app is not present for arch-check: %v %s", err, out)
	}
	targets := []struct{ goos, goarch, pkg string }{
		{"darwin", "arm64", "./cmd/pathctl"}, {"linux", "amd64", "./cmd/pathctl"}, {"linux", "arm64", "./cmd/pathctl"},
		{"darwin", "arm64", "./..."}, {"linux", "amd64", "./..."}, {"linux", "arm64", "./..."},
		{"js", "wasm", "./..."}, {"plan9", "amd64", "./..."}, {"windows", "amd64", "./..."},
	}
	for _, tg := range targets {
		tg := tg
		name := tg.goos + "_" + tg.goarch + "_all"
		if tg.pkg == "./cmd/pathctl" {
			name = tg.goos + "_" + tg.goarch + "_pathctl"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			args := []string{"build"}
			if tg.pkg == "./cmd/pathctl" {
				args = append(args, "-o", filepath.Join(t.TempDir(), "pathctl"))
			}
			args = append(args, tg.pkg)
			if out, err := gfxGo(t, []string{"CGO_ENABLED=0", "GOOS=" + tg.goos, "GOARCH=" + tg.goarch}, args...); err != nil {
				head := out
				if len(head) > 1500 {
					head = head[:1500] + "..."
				}
				t.Errorf("GFX-100 (a)/(e): CGO_ENABLED=0 GOOS=%s GOARCH=%s go build %s failed: %v\n%s", tg.goos, tg.goarch, tg.pkg, err, head)
			}
		})
	}
}

// TestGFX101_VerificationSeam: the binary contract is the only surface; the
// oracle computed through the ratified APIs agrees with pathctl on a
// multi-device fleet mixing Ready, unbound and maintenance devices.
func TestGFX101_VerificationSeam(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	f := gfxSReady(t)
	gpu2 := "0000:03:00.1"
	uuid2 := "GPU-00000000-0000-4000-8000-000000000002"
	for i := 0; i < 3; i++ {
		f.Dev(i, gfxPCI(append(append([]string{}, gfoPathSWD...), gpu2), "0x030200", "0x10de", "16", "16"))
		f.Write(gfoCSV(i), gfxUUID+", 00000000:03:00.0\n"+uuid2+", 0000:03:00.1\n")
	}
	fl := gfxTwoDevices()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.EvidenceID, d.Desired, d.Generation = "gpu-node-1-gpu1", "zz-uid-gpu1", uuid2, "asset-db:gpu1", "Maintenance", 3
	fl.Devices = append(fl.Devices, d)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
	for _, name := range []string{gfxDevName, "gpu-node-1-spare", "gpu-node-1-gpu1"} {
		c.Check("gpu", name, "GFX-101 "+name)
	}
	c.Check("node", gfoNodeName, "GFX-101 node")
}

// TestGFX104_InputsAreTemporary: the tests of this feature never read the
// repository test data directory; every input is created under t.TempDir().
func TestGFX104_InputsAreTemporary(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join(gfoRepoRoot(t), "tests", "fleetexplain_*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("GFX-104: cannot list the feature test files: %v", err)
	}
	needle := "test" + "data"
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(needle)) {
			t.Errorf("GFX-104: %s refers to the repository %s directory", filepath.Base(p), needle)
		}
	}
}
