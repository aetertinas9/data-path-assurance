package tests_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// GFO-100: in fixture mode no child process is created (a fake nvidia-smi
// first on PATH and executables inside the fixture are never run).
func TestGFO100_NoChildProcessInFixtureMode(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	markers := t.TempDir()
	pathDir := t.TempDir()
	for _, name := range []string{"nvidia-smi", "sh", "lspci"} {
		script := "#!/bin/sh\ntouch " + gfoQuoteSh(filepath.Join(markers, name)) + "\n"
		if err := os.WriteFile(filepath.Join(pathDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := gfoNewBasic(t, 1)
	f.Write("nvidia-smi", "#!/bin/sh\ntouch "+gfoQuoteSh(filepath.Join(markers, "root-nvidia-smi"))+"\n")
	if err := os.Chmod(f.P("nvidia-smi"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.save()
	env := append([]string{}, os.Environ()...)
	env = append(env, "PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	res := gfoExec(t, bin, gfoRunOpt{Env: env, Dir: f.Root}, "--fixture-root", f.Root)
	gfoOK(t, res, &f.M, "GFO-100")
	entries, err := os.ReadDir(markers)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("GFO-100/GFO-074: fixture mode executed %s", e.Name())
	}
}

// GFO-101(a): each path escaping the fixture root (manifest, frame sysfs
// root, entry symlink, attribute file, canned CSV) gives its specified
// result and none of the outside content reaches the output.
func TestGFO101a_EscapeFixtures(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	const other = "0000:66:00.0"
	outside := func(t *testing.T) *gfoFx {
		o := gfoNewBasic(t, 1)
		o.Dev(0, gfoGPUDev(gfoHB, other))
		o.Write(gfoCSV(0), gfoUUIDB+", 0000:03:00.0\n")
		o.M.NodeName = "outside-node"
		o.save()
		return o
	}
	t.Run("manifest", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Manual()
		f.Link("manifest.json", filepath.Join(outside(t).Root, "manifest.json"))
		f.Fail(bin, 3, "GFO-101(a) manifest")
	})
	t.Run("frame_sysfs_root", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Remove(gfoSys(0))
		f.Link(gfoSys(0), outside(t).P(gfoSys(0)))
		f.Fail(bin, 3, "GFO-101(a) frames/0/sys")
	})
	t.Run("entry_symlink", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Link(gfoEntry(0, other), outside(t).P(gfoDevDir(0, []string{gfoHB, other})))
		res := f.Run(bin)
		a := gfoOK(t, res, &f.M, "GFO-101(a) entry")
		gfoAssertCode(t, a.Frames[0], "sysfs_link_escape", "GFO-101(a) entry")
		gfoAssertAsset(t, a.Frames[0].Payload, gfoFnKey(other), false, "GFO-101(a) entry")
	})
	t.Run("attribute_file", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		p := gfoDevDir(0, gfoPathGPU) + "/class"
		f.Remove(p)
		f.Link(p, outside(t).P(p))
		a := f.OK(bin, "GFO-101(a) attribute")
		if !a.Frames[0].HasDiag("sysfs_attr_unreadable", gfoAttrSubject(gfoPathGPU, "class")) {
			t.Errorf("GFO-101(a): want sysfs_attr_unreadable(%s), got %s", gfoAttrSubject(gfoPathGPU, "class"), a.Frames[0].DiagString())
		}
		gfoAssertAsset(t, a.Frames[0].Payload, gfoFnKey(gfoGPU), false, "GFO-101(a) attribute")
	})
	t.Run("canned_csv", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Remove(gfoCSV(0))
		f.Link(gfoCSV(0), outside(t).P(gfoCSV(0)))
		res := f.Run(bin)
		a := gfoOK(t, res, &f.M, "GFO-101(a) csv")
		if !a.Frames[0].HasDiag("nvidia_output_unreadable", gfoCSV(0)) || len(a.Frames[0].Payload.GPUBindings) != 0 {
			t.Errorf("GFO-101(a): want nvidia_output_unreadable and no binding, got %s", a.Frames[0].DiagString())
		}
		if bytes.Contains(res.Stdout, []byte(gfoUUIDB)) || bytes.Contains(res.Stdout, []byte("outside-node")) {
			t.Errorf("GFO-101(a): outside content reached the artifact")
		}
	})
}

// GFO-002/GFO-003/GFO-102: path-agent builds with CGO_ENABLED=0 for the
// three targets; its dependencies (CGO 0 and 1) are standard library or this
// module, never internal/nativepcie, with no cgo files; go.mod has no
// require; make arch-check passes.
func TestGFO102_GFO003_BuildAndDependencies(t *testing.T) {
	t.Parallel()
	root := gfoRepoRoot(t)
	run := func(t *testing.T, env []string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	for _, cgo := range []string{"0", "1"} {
		out := run(t, []string{"CGO_ENABLED=" + cgo}, "go", "list", "-deps", "-f", "{{.ImportPath}}|{{.Standard}}|{{len .CgoFiles}}", "./cmd/path-agent", "./internal/agent")
		sc := bufio.NewScanner(strings.NewReader(out))
		for sc.Scan() {
			parts := strings.Split(sc.Text(), "|")
			if len(parts) != 3 {
				t.Fatalf("GFO-102: unexpected go list line %q", sc.Text())
			}
			path, std, cgoFiles := parts[0], parts[1], parts[2]
			if strings.HasSuffix(path, "/internal/nativepcie") || strings.Contains(path, "/internal/nativepcie/") {
				t.Errorf("GFO-002/GFO-102 CGO_ENABLED=%s: dependency on %s", cgo, path)
			}
			if std == "true" {
				continue
			}
			if path != gfoModule && !strings.HasPrefix(path, gfoModule+"/") {
				t.Errorf("GFO-003/GFO-102 CGO_ENABLED=%s: non-standard, non-module dependency %s", cgo, path)
			}
			if cgoFiles != "0" {
				t.Errorf("GFO-003/GFO-102 CGO_ENABLED=%s: module package %s has %s cgo files", cgo, path, cgoFiles)
			}
		}
	}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		out := filepath.Join(t.TempDir(), "path-agent-"+target[0]+"-"+target[1])
		run(t, []string{"CGO_ENABLED=0", "GOOS=" + target[0], "GOARCH=" + target[1]}, "go", "build", "-o", out, "./cmd/path-agent")
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if l := strings.TrimSpace(line); l == "require" || strings.HasPrefix(l, "require ") || strings.HasPrefix(l, "require(") {
			t.Errorf("GFO-003/GFO-102: go.mod has a require directive: %q", l)
		}
	}
	if !strings.HasPrefix(string(mod), "module "+gfoModule+"\n") {
		t.Errorf("GFO-003: go.mod module line changed")
	}
	if out := run(t, nil, "make", "arch-check"); !strings.Contains(out, "arch-check: OK") {
		t.Errorf("GFO-102: make arch-check output %q lacks OK", out)
	}
}

// GFO-002: the only product is the offline artifact: offline mode and
// limitation, no allocationBatch, no live data.
func TestGFO002_OfflineArtifactOnly(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	res := f.Run(bin)
	a := gfoOK(t, res, &f.M, "GFO-002")
	if a.Mode != "offline" || a.Trust.Mode != "Offline" || len(a.Limitations) != 1 || a.Limitations[0] != "offline" {
		t.Errorf("GFO-002/GFO-016: mode %q trust mode %q limitations %v", a.Mode, a.Trust.Mode, a.Limitations)
	}
	if bytes.Contains(res.Stdout, []byte("allocationBatch")) {
		t.Errorf("GFO-002/GFO-082: allocationBatch emitted")
	}
}

// gfoLoadManifest decodes a fixture manifest.json from disk (GFO-031/032
// shape) into a gfoManifest for the artifact checks.
func gfoLoadManifest(t *testing.T, path string) *gfoManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("GFO-104: %v", err)
	}
	var raw struct {
		SchemaVersion string `json:"schemaVersion"`
		ClusterID     string `json:"clusterID"`
		Node          struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"node"`
		BootID string `json:"bootID"`
		Trust  struct {
			ProfileID string      `json:"profileID"`
			Session   json.Number `json:"session"`
			Sources   []struct {
				Capability string `json:"capability"`
				SourceType string `json:"sourceType"`
				SourceName string `json:"sourceName"`
			} `json:"sources"`
		} `json:"trust"`
		EvidenceTTLSeconds json.Number `json:"evidenceTTLSeconds"`
		Frames             []struct {
			ObservedAt string `json:"observedAt"`
		} `json:"frames"`
		OperatorBaselines []struct {
			Function      string      `json:"function"`
			Peer          string      `json:"peer"`
			ExpectedWidth json.Number `json:"expectedWidth"`
		} `json:"operatorBaselines"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("GFO-104: manifest %s does not decode: %v", path, err)
	}
	m := &gfoManifest{
		Schema: raw.SchemaVersion, Cluster: raw.ClusterID, NodeName: raw.Node.Name, NodeUID: raw.Node.UID,
		BootID: raw.BootID, Profile: raw.Trust.ProfileID, Session: raw.Trust.Session.String(), TTL: raw.EvidenceTTLSeconds.String(),
	}
	for _, s := range raw.Trust.Sources {
		m.Sources = append(m.Sources, gfoSrc{s.Capability, s.SourceType, s.SourceName})
	}
	for _, fr := range raw.Frames {
		m.Frames = append(m.Frames, fr.ObservedAt)
	}
	for _, b := range raw.OperatorBaselines {
		m.Baselines = append(m.Baselines, gfoBL{b.Function, b.Peer, b.ExpectedWidth.String()})
	}
	return m
}

// gfoCheckSampleLinks requires every symlink under a fixture root to be
// relative and to stay inside that root lexically, and at least one symlink
// to exist.
func gfoCheckSampleLinks(t *testing.T, root string) {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		n++
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			t.Errorf("GFO-104: symlink %s has absolute target %q", p, target)
			return nil
		}
		resolved := filepath.Join(filepath.Dir(p), target)
		if rel, err := filepath.Rel(root, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("GFO-104: symlink %s -> %q leaves the fixture root", p, target)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Errorf("GFO-104: fixture %s has no bus/pci/devices symlinks", root)
	}
}

// gfoChainFrom follows the unique parent edges from key to the node.
func gfoChainFrom(p gfoPayload, key, nodeKey string) []string {
	chain := []string{key}
	for cur := key; cur != nodeKey && len(chain) < 20; {
		out := p.EdgesFrom(cur)
		if len(out) != 1 {
			return chain
		}
		cur = out[0].ToKey
		chain = append(chain, cur)
	}
	return chain
}

// gfoFunctionsByClass maps class value -> PCIeFunction keys.
func gfoFunctionsByClass(p gfoPayload) map[string][]string {
	out := map[string][]string{}
	for _, o := range p.Observations {
		if o.Signal == gfoSigClass {
			out[o.IntValue()] = append(out[o.IntValue()], o.Subject.Key())
		}
	}
	return out
}

func gfoAdmitAll(t *testing.T, a *gfoArtifact, m *gfoManifest, clause string) {
	t.Helper()
	session, err := strconv.ParseInt(m.Session, 10, 64)
	if err != nil {
		t.Fatalf("%s: manifest session %q: %v", clause, m.Session, err)
	}
	var cursor *fleet.SnapshotCursor
	for i := range a.Frames {
		r, err := gfoApplyM(a, i)
		if err != nil {
			t.Fatalf("%s: GFO-090: %v", clause, err)
		}
		next, order, err := fleet.AdmitSnapshot(session, cursor, r.Envelope)
		if err != nil || order != fleet.SnapshotAccepted {
			t.Fatalf("%s: GFO-092: frame %d admission %v, %v", clause, i, order, err)
		}
		gfoCheckPCIeSchema(t, r, clause)
		cursor = &next
	}
}

// GFO-104: the two sample topologies are not shipped in the repository and
// no repository sample is read. Each subtest builds its fixture under
// t.TempDir(), with relative symlinks that stay inside the fixture root, and
// checks the specified results.
//
//   - gpu-fleet-basic: one frame; an NVIDIA GPU (class 0x030200, vendor
//     0x10de) under switch downstream -> switch upstream -> root port, a NIC
//     (class 0x020000) under a different root port, every width 16, one GPU
//     operator baseline whose peer is the switch downstream, a canned
//     nvidia-smi.csv GPU row with an 8-digit upper-case domain, and a
//     manifest trusting the three section 2.3 sources for their capabilities
//     (OperatorBaseline on the width source).
//   - gpu-fleet-width-replay: the same topology in three frames (0s, 15s,
//     30s) with GPU current width 8 and baseline 16 in every frame.
func TestGFO104_SampleFixtures(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)

	t.Run("gpu-fleet-basic", func(t *testing.T) {
		f := gfoNewBasic(t, 1)
		f.save()
		root := f.Root
		gfoCheckSampleLinks(t, root)
		m := gfoLoadManifest(t, f.P("manifest.json"))
		a := gfoOK(t, gfoExec(t, bin, gfoRunOpt{}, "--fixture-root", root), m, "GFO-104 basic")
		if len(a.Frames) != 1 {
			t.Fatalf("GFO-104: basic sample has %d frames, want 1", len(a.Frames))
		}
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-104 basic")
		p := fr.Payload
		nodeKey := gfoNodeKey(a.NodeUID)
		byClass := gfoFunctionsByClass(p)
		if len(byClass["197120"]) != 1 || len(byClass["131072"]) != 1 {
			t.Fatalf("GFO-104: want one GPU (0x030200) and one NIC (0x020000), got %v", byClass)
		}
		gpu, nic := byClass["197120"][0], byClass["131072"][0]
		gpuBDF := strings.TrimPrefix(gpu, "PCIeFunction/pci-bdf:")
		gc := gfoChainFrom(p, gpu, nodeKey)
		if len(gc) != 5 || !strings.HasPrefix(gc[1], "PCIeSwitch/") || !strings.HasPrefix(gc[2], "PCIeSwitch/") || !strings.HasPrefix(gc[3], "PCIeRootPort/") || gc[4] != nodeKey {
			t.Errorf("GFO-104: GPU chain %v, want GPU -> switch downstream -> switch upstream -> root port -> node", gc)
		}
		nc := gfoChainFrom(p, nic, nodeKey)
		if len(nc) != 3 || !strings.HasPrefix(nc[1], "PCIeRootPort/") || nc[2] != nodeKey || (len(gc) > 3 && nc[1] == gc[3]) {
			t.Errorf("GFO-104: NIC chain %v, want NIC -> another root port -> node", nc)
		}
		if len(p.GPUBindings) != 1 || p.GPUBindings[0].BDF != gpuBDF {
			t.Errorf("GFO-104: gpuBindings %+v, want exactly one for %s", p.GPUBindings, gpuBDF)
		}
		cur, exp, ok := p.WidthPair(gpuBDF)
		if !ok || cur.IntValue() != "16" || exp.IntValue() != "16" || exp.Dim("pcie.expected.provenance") != gfoProvOp ||
			(len(gc) > 1 && "PCIeSwitch/"+exp.Dim("pcie.peer.canonical") != gc[1]) {
			t.Errorf("GFO-104: GPU width pair %s/%s (%s) peer %s", cur.IntValue(), exp.IntValue(), exp.Dim("pcie.expected.provenance"), exp.Dim("pcie.peer.canonical"))
		}
		ncur, nexp, ok := p.WidthPair(strings.TrimPrefix(nic, "PCIeFunction/pci-bdf:"))
		if !ok || ncur.IntValue() != "16" || nexp.IntValue() != "16" || nexp.Dim("pcie.expected.provenance") != gfoProvAdj {
			t.Errorf("GFO-104 [OD-04 A]: NIC width pair %s/%s (%s), want adjacent_capability_min 16/16", ncur.IntValue(), nexp.IntValue(), nexp.Dim("pcie.expected.provenance"))
		}
		if len(m.Baselines) != 1 || m.Baselines[0].Function != gpuBDF || (len(gc) > 1 && "PCIeSwitch/pci-bdf:"+m.Baselines[0].Peer != gc[1]) {
			t.Errorf("GFO-104: manifest baselines %+v, want one GPU baseline with peer = switch downstream", m.Baselines)
		}
		wantSrc := map[string]string{"SysfsPhysicalParent": gfoParentSrc, "SysfsPCIeWidth": gfoWidthSrc, "OperatorBaseline": gfoWidthSrc, "NVIDIAUUIDBinding": gfoNVIDIASrc}
		for capName, name := range wantSrc {
			found := false
			for _, s := range m.Sources {
				if s.Cap == capName && s.Type == "agent" && s.Name == name {
					found = true
				}
			}
			if !found {
				t.Errorf("GFO-104: manifest does not trust {%s agent %s}", capName, name)
			}
		}
		csv, err := os.ReadFile(filepath.Join(root, "frames", "0", "nvidia-smi.csv"))
		if err != nil {
			t.Fatalf("GFO-104: canned CSV: %v", err)
		}
		upper := regexp.MustCompile(`^[0-9A-F]{8}:`)
		found := false
		for _, line := range strings.Split(strings.TrimRight(string(csv), "\r\n"), "\n") {
			parts := strings.Split(line, ",")
			if len(parts) != 2 {
				continue
			}
			bus := strings.TrimSpace(strings.TrimRight(parts[1], "\r"))
			if len(bus) >= 7 && len(gpuBDF) >= 7 && strings.EqualFold(bus[len(bus)-7:], gpuBDF[len(gpuBDF)-7:]) && upper.MatchString(bus) {
				found = true
			}
		}
		if !found {
			t.Errorf("GFO-104: canned CSV has no GPU row with an 8-digit upper-case domain: %q", csv)
		}
		gfoAdmitAll(t, a, m, "GFO-104 basic")
	})

	t.Run("gpu-fleet-width-replay", func(t *testing.T) {
		f := gfoNewBasic(t, 3)
		for i := 0; i < 3; i++ {
			f.Write(gfoDevDir(i, gfoPathGPU)+"/current_link_width", "8\n")
		}
		f.save()
		root := f.Root
		gfoCheckSampleLinks(t, root)
		m := gfoLoadManifest(t, f.P("manifest.json"))
		a := gfoOK(t, gfoExec(t, bin, gfoRunOpt{}, "--fixture-root", root), m, "GFO-104 width replay")
		if len(a.Frames) != 3 {
			t.Fatalf("GFO-104: width replay has %d frames, want 3", len(a.Frames))
		}
		t0, _ := gfoTime(a.Frames[0].ObservedAt)
		for i, fr := range a.Frames {
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-104 width replay")
			at, _ := gfoTime(fr.ObservedAt)
			if at.Sub(t0) != time.Duration(i)*15*time.Second {
				t.Errorf("GFO-104: frame %d at %s, want frame 0 + %ds", i, fr.ObservedAt, 15*i)
			}
			byClass := gfoFunctionsByClass(fr.Payload)
			if len(byClass["197120"]) != 1 {
				t.Fatalf("GFO-104: frame %d GPUs %v", i, byClass)
			}
			gpuBDF := strings.TrimPrefix(byClass["197120"][0], "PCIeFunction/pci-bdf:")
			cur, exp, ok := fr.Payload.WidthPair(gpuBDF)
			if !ok || cur.IntValue() != "8" || exp.IntValue() != "16" || exp.Dim("pcie.expected.provenance") != gfoProvOp {
				t.Errorf("GFO-104: frame %d GPU width %s/%s (%s), want current 8, baseline 16", i, cur.IntValue(), exp.IntValue(), exp.Dim("pcie.expected.provenance"))
			}
			if strings.Join(fr.Payload.AssetKeys(), " ") != strings.Join(a.Frames[0].Payload.AssetKeys(), " ") ||
				strings.Join(fr.Payload.EdgeStrings(), " ") != strings.Join(a.Frames[0].Payload.EdgeStrings(), " ") {
				t.Errorf("GFO-104: frame %d topology differs from frame 0", i)
			}
		}
		gfoAdmitAll(t, a, m, "GFO-104 width replay")
	})
}
