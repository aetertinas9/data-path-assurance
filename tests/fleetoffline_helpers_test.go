package tests_test

// Shared helpers for the gpu-fleet-offline (GFO) black-box tests.
//
// Source of truth: the gpu-fleet-offline specification (v1.2).
// These helpers only build fixtures, run the path-agent binary and decode its
// output. They never read implementation sources.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fixed values used across the GFO tests.
const (
	gfoModule     = "github.com/aetertinas9/data-path-assurance"
	gfoSchemaIn   = "dpa.offline-fixture/v1"
	gfoSchemaOut  = "dpa.offline-snapshot/v1"
	gfoCluster    = "lab-a"
	gfoNodeName   = "gpu-node-1"
	gfoNodeUID    = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	gfoBootID     = "3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90"
	gfoProfile    = "offline:lab-a-basic"
	gfoT0         = "2026-09-24T00:00:00Z"
	gfoUUIDA      = "GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"
	gfoUUIDB      = "GPU-1b2c3d4e-5f60-7182-93a4-b5c6d7e8f9a0"
	gfoHB         = "pci0000:00"
	gfoRPA        = "0000:00:01.0"
	gfoSWU        = "0000:01:00.0"
	gfoSWD        = "0000:02:08.0"
	gfoGPU        = "0000:03:00.0"
	gfoRPB        = "0000:00:02.0"
	gfoNIC        = "0000:04:00.0"
	gfoParentSrc  = "path-agent/sysfs-parent"
	gfoWidthSrc   = "path-agent/sysfs-width"
	gfoNVIDIASrc  = "path-agent/nvidia-smi"
	gfoClassBr    = "0x060400\n"
	gfoClassGPU   = "0x030200\n"
	gfoClassNIC   = "0x020000\n"
	gfoVendorNV   = "0x10de\n"
	gfoSigClass   = "pcie.function.class_code"
	gfoSigCurrent = "pcie.link.width.current"
	gfoSigExpect  = "pcie.link.width.expected"
	gfoProvOp     = "operator_verified_wiring"
	gfoProvAdj    = "adjacent_capability_min"
)

// gfoTokens maps each path-agent exit code to its GFO-011 stderr token.
var gfoTokens = map[int]string{
	1: "internal",
	2: "usage",
	3: "fixture_invalid",
	4: "bound_exceeded",
	5: "live_unsupported",
}

// ---------------------------------------------------------------------------
// Repository, build and process execution.
// ---------------------------------------------------------------------------

func gfoRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("cannot resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

// gfoBuildAgent builds ./cmd/path-agent into t.TempDir() and returns the
// binary path.
func gfoBuildAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "path-agent")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/path-agent")
	cmd.Dir = gfoRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GFO-001: go build ./cmd/path-agent failed: %v\n%s", err, out)
	}
	return bin
}

type gfoRunOpt struct {
	Timeout time.Duration // hard kill deadline; default 60s
	Env     []string      // full environment; nil inherits os.Environ()
	Dir     string        // working directory; "" inherits
	Stdout  *os.File      // optional stdout override
}

type gfoResult struct {
	Exit    int
	Stdout  []byte
	Stderr  []byte
	Elapsed time.Duration
	Killed  bool
}

func gfoExec(t *testing.T, bin string, opt gfoRunOpt, args ...string) gfoResult {
	t.Helper()
	timeout := opt.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 2 * time.Second
	if opt.Env != nil {
		cmd.Env = opt.Env
	}
	cmd.Dir = opt.Dir
	var stdout, stderr bytes.Buffer
	if opt.Stdout != nil {
		cmd.Stdout = opt.Stdout
	} else {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	res := gfoResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Elapsed: time.Since(start)}
	if ctx.Err() != nil {
		res.Killed = true
		res.Exit = -1
		return res
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		res.Exit = 0
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		t.Fatalf("cannot run %s %q: %v", bin, args, err)
	}
	return res
}

func gfoTail(b []byte) string {
	const max = 2048
	if len(b) > max {
		return string(b[:max]) + "...(truncated)"
	}
	return string(b)
}

// gfoFail asserts a failing run: exit code, GFO-011 token, GFO-012 stderr
// shape and an empty stdout (GFO-012, GFO-096).
func gfoFail(t *testing.T, res gfoResult, wantExit int, clause string) {
	t.Helper()
	if res.Killed {
		t.Fatalf("%s: path-agent did not finish (killed after %v)", clause, res.Elapsed)
	}
	if res.Exit != wantExit {
		t.Fatalf("%s: exit %d, want %d; stderr=%q stdout=%d bytes", clause, res.Exit, wantExit, gfoTail(res.Stderr), len(res.Stdout))
	}
	if len(res.Stdout) != 0 {
		t.Errorf("%s/GFO-012: exit %d wrote %d stdout bytes, want 0", clause, res.Exit, len(res.Stdout))
	}
	prefix := "path-agent: error: " + gfoTokens[wantExit] + ": "
	if !bytes.HasPrefix(res.Stderr, []byte(prefix)) {
		t.Errorf("%s/GFO-011/GFO-012: stderr first line %q, want prefix %q", clause, gfoFirstLine(res.Stderr), prefix)
	}
	if len(res.Stderr) > 8192 {
		t.Errorf("%s/GFO-012: stderr is %d bytes, want <= 8192", clause, len(res.Stderr))
	}
}

func gfoFirstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return string(b[:i])
	}
	return gfoTail(b)
}

// ---------------------------------------------------------------------------
// Manifest.
// ---------------------------------------------------------------------------

type gfoSrc struct{ Cap, Type, Name string }

type gfoBL struct {
	Function, Peer string
	Width          string // raw JSON literal
}

type gfoManifest struct {
	Schema, Cluster, NodeName, NodeUID, BootID, Profile string
	Session                                             string // raw JSON literal
	Sources                                             []gfoSrc
	TTL                                                 string // raw JSON literal
	Frames                                              []string
	Baselines                                           []gfoBL
	WithBaselines                                       bool // emit operatorBaselines (even when empty)
}

func gfoDefaultSources() []gfoSrc {
	return []gfoSrc{
		{"SysfsPhysicalParent", "agent", gfoParentSrc},
		{"SysfsPCIeWidth", "agent", gfoWidthSrc},
		{"OperatorBaseline", "agent", gfoWidthSrc},
		{"NVIDIAUUIDBinding", "agent", gfoNVIDIASrc},
	}
}

// gfoFrameTime returns T0 + i*15s in the manifest time form.
func gfoFrameTime(i int) string {
	t0, _ := time.Parse(time.RFC3339, gfoT0)
	return t0.Add(time.Duration(i) * 15 * time.Second).Format(time.RFC3339Nano)
}

func gfoDefaultManifest(frames int) gfoManifest {
	m := gfoManifest{
		Schema: gfoSchemaIn, Cluster: gfoCluster, NodeName: gfoNodeName, NodeUID: gfoNodeUID,
		BootID: gfoBootID, Profile: gfoProfile, Session: "7", Sources: gfoDefaultSources(), TTL: "300",
	}
	for i := 0; i < frames; i++ {
		m.Frames = append(m.Frames, gfoFrameTime(i))
	}
	return m
}

// gfoQ quotes s as a JSON string (manifest input side). Bytes >= 0x20 are
// copied verbatim so tests can inject arbitrary UTF-8 or invalid bytes.
func gfoQ(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (m gfoManifest) JSON() string {
	var b strings.Builder
	b.WriteString(`{"schemaVersion":` + gfoQ(m.Schema))
	b.WriteString(`,"clusterID":` + gfoQ(m.Cluster))
	b.WriteString(`,"node":{"name":` + gfoQ(m.NodeName) + `,"uid":` + gfoQ(m.NodeUID) + `}`)
	b.WriteString(`,"bootID":` + gfoQ(m.BootID))
	b.WriteString(`,"trust":{"profileID":` + gfoQ(m.Profile) + `,"session":` + m.Session + `,"sources":[`)
	for i, s := range m.Sources {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"capability":` + gfoQ(s.Cap) + `,"sourceType":` + gfoQ(s.Type) + `,"sourceName":` + gfoQ(s.Name) + `}`)
	}
	b.WriteString(`]}`)
	b.WriteString(`,"evidenceTTLSeconds":` + m.TTL)
	b.WriteString(`,"frames":[`)
	for i, f := range m.Frames {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"observedAt":` + gfoQ(f) + `}`)
	}
	b.WriteString(`]`)
	if m.WithBaselines || len(m.Baselines) > 0 {
		b.WriteString(`,"operatorBaselines":[`)
		for i, bl := range m.Baselines {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"function":` + gfoQ(bl.Function) + `,"peer":` + gfoQ(bl.Peer) + `,"expectedWidth":` + bl.Width + `}`)
		}
		b.WriteString(`]`)
	}
	b.WriteString(`}`)
	return b.String()
}

// gfoTTL returns the manifest TTL in seconds (valid manifests only).
func (m gfoManifest) ttlSeconds() (int64, bool) {
	v, err := strconv.ParseInt(m.TTL, 10, 64)
	return v, err == nil
}

// ---------------------------------------------------------------------------
// Fixture builder.
// ---------------------------------------------------------------------------

type gfoFx struct {
	t      *testing.T
	Root   string
	M      gfoManifest
	manual bool // manifest.json is managed by the test itself
}

// gfoNewFx creates an empty fixture root with frames/<i>/sys for i < frames
// and a default manifest (no baselines, no devices, no canned CSV).
func gfoNewFx(t *testing.T, frames int) *gfoFx {
	t.Helper()
	f := &gfoFx{t: t, Root: filepath.Join(t.TempDir(), "fixture"), M: gfoDefaultManifest(frames)}
	for i := 0; i < frames; i++ {
		f.Mkdir(gfoSys(i))
	}
	if frames == 0 {
		f.Mkdir(".")
	}
	return f
}

func gfoSys(frame int) string { return fmt.Sprintf("frames/%d/sys", frame) }
func gfoCSV(frame int) string { return fmt.Sprintf("frames/%d/nvidia-smi.csv", frame) }

func (f *gfoFx) P(rel string) string { return filepath.Join(f.Root, filepath.FromSlash(rel)) }

func (f *gfoFx) Mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(f.P(rel), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", rel, err)
	}
}

func (f *gfoFx) Write(rel, data string) {
	f.t.Helper()
	f.Mkdir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))))
	if err := os.WriteFile(f.P(rel), []byte(data), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}
}

func (f *gfoFx) Link(rel, target string) {
	f.t.Helper()
	f.Mkdir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))))
	if err := os.Symlink(target, f.P(rel)); err != nil {
		f.t.Fatalf("symlink %s -> %s: %v", rel, target, err)
	}
}

func (f *gfoFx) Fifo(rel string) {
	f.t.Helper()
	f.Mkdir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))))
	if err := syscall.Mkfifo(f.P(rel), 0o644); err != nil {
		f.t.Fatalf("mkfifo %s: %v", rel, err)
	}
}

func (f *gfoFx) Remove(rel string) {
	f.t.Helper()
	if err := os.RemoveAll(f.P(rel)); err != nil {
		f.t.Fatalf("remove %s: %v", rel, err)
	}
}

// Chmod changes a mode and restores 0755/0644 at cleanup so TempDir removal
// still works.
func (f *gfoFx) Chmod(rel string, mode os.FileMode) {
	f.t.Helper()
	p := f.P(rel)
	fi, err := os.Lstat(p)
	if err != nil {
		f.t.Fatalf("lstat %s: %v", rel, err)
	}
	restore := os.FileMode(0o644)
	if fi.IsDir() {
		restore = 0o755
	}
	if err := os.Chmod(p, mode); err != nil {
		f.t.Fatalf("chmod %s: %v", rel, err)
	}
	f.t.Cleanup(func() { _ = os.Chmod(p, restore) })
}

// Manual marks manifest.json as test-managed (Run will not rewrite it).
func (f *gfoFx) Manual() { f.manual = true }

// SaveRaw writes manifest.json verbatim and marks it test-managed.
func (f *gfoFx) SaveRaw(s string) {
	f.t.Helper()
	f.manual = true
	f.Write("manifest.json", s)
}

func (f *gfoFx) save() {
	f.t.Helper()
	if !f.manual {
		f.Write("manifest.json", f.M.JSON())
	}
}

// Run writes the manifest (unless test-managed) and runs path-agent.
func (f *gfoFx) Run(bin string) gfoResult {
	f.t.Helper()
	f.save()
	return gfoExec(f.t, bin, gfoRunOpt{}, "--fixture-root", f.Root)
}

// OK runs the fixture and asserts a successful, spec-conformant artifact.
func (f *gfoFx) OK(bin, clause string) *gfoArtifact {
	f.t.Helper()
	res := f.Run(bin)
	var m *gfoManifest
	if !f.manual {
		m = &f.M
	}
	return gfoOK(f.t, res, m, clause)
}

// Fail runs the fixture and asserts the given failing exit code.
func (f *gfoFx) Fail(bin string, exit int, clause string) {
	f.t.Helper()
	gfoFail(f.t, f.Run(bin), exit, clause)
}

// gfoOK asserts exit 0, empty stderr and runs every artifact invariant.
func gfoOK(t *testing.T, res gfoResult, m *gfoManifest, clause string) *gfoArtifact {
	t.Helper()
	if res.Killed {
		t.Fatalf("%s: path-agent did not finish (killed after %v)", clause, res.Elapsed)
	}
	if res.Exit != 0 {
		t.Fatalf("%s: exit %d, want 0; stderr=%q", clause, res.Exit, gfoTail(res.Stderr))
	}
	if len(res.Stderr) != 0 {
		t.Errorf("%s/GFO-012: exit 0 wrote stderr %q, want 0 bytes", clause, gfoTail(res.Stderr))
	}
	a, err := gfoParseArtifact(res.Stdout)
	if err != nil {
		t.Fatalf("%s: artifact does not match the GFO-080..083 schema: %v\nstdout head: %s", clause, err, gfoTail(res.Stdout))
	}
	if len(a.Frames) == 0 || (m != nil && len(a.Frames) != len(m.Frames)) {
		want := "at least 1"
		if m != nil {
			want = fmt.Sprint(len(m.Frames))
		}
		t.Fatalf("%s: GFO-081: artifact has %d frames, want %s", clause, len(a.Frames), want)
	}
	gfoCheckArtifact(t, a, m, clause)
	return a
}

// ---------------------------------------------------------------------------
// sysfs device helpers.
// ---------------------------------------------------------------------------

// gfoDev describes one PCI function directory devices/<Path...> inside a
// frame sysfs root, its attribute files and (when BDF != "") the
// bus/pci/devices/<BDF> entry symlink with a relative target.
type gfoDev struct {
	BDF   string
	Path  []string
	Attrs map[string]string
}

func gfoBridge(path ...string) gfoDev {
	return gfoDev{BDF: path[len(path)-1], Path: path, Attrs: map[string]string{
		"class": gfoClassBr, "current_link_width": "16\n", "max_link_width": "16\n",
	}}
}

func gfoGPUDev(path ...string) gfoDev {
	return gfoDev{BDF: path[len(path)-1], Path: path, Attrs: map[string]string{
		"class": gfoClassGPU, "vendor": gfoVendorNV, "current_link_width": "16\n", "max_link_width": "16\n",
	}}
}

func gfoNICDev(path ...string) gfoDev {
	return gfoDev{BDF: path[len(path)-1], Path: path, Attrs: map[string]string{
		"class": gfoClassNIC, "vendor": "0x15b3\n", "current_link_width": "16\n", "max_link_width": "16\n",
	}}
}

// with returns a copy of d with attribute overrides; an empty value removes
// the attribute file.
func (d gfoDev) with(kv ...string) gfoDev {
	attrs := map[string]string{}
	for k, v := range d.Attrs {
		attrs[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(attrs, kv[i])
		} else {
			attrs[kv[i]] = kv[i+1]
		}
	}
	d.Attrs = attrs
	return d
}

func gfoDevDir(frame int, path []string) string {
	return gfoSys(frame) + "/devices/" + strings.Join(path, "/")
}

func gfoEntry(frame int, name string) string {
	return gfoSys(frame) + "/bus/pci/devices/" + name
}

// gfoAttrSubject is the frame-sysfs-root relative attribute path <R>/<attr>.
func gfoAttrSubject(path []string, attr string) string {
	return "devices/" + strings.Join(path, "/") + "/" + attr
}

func (f *gfoFx) Dev(frame int, d gfoDev) {
	f.t.Helper()
	dir := gfoDevDir(frame, d.Path)
	f.Mkdir(dir)
	names := make([]string, 0, len(d.Attrs))
	for k := range d.Attrs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		f.Write(dir+"/"+k, d.Attrs[k])
	}
	if d.BDF != "" {
		f.Link(gfoEntry(frame, d.BDF), "../../../devices/"+strings.Join(d.Path, "/"))
	}
}

// Topology paths of the basic fixture (GFO-104 shape).
var (
	gfoPathRPA = []string{gfoHB, gfoRPA}
	gfoPathSWU = []string{gfoHB, gfoRPA, gfoSWU}
	gfoPathSWD = []string{gfoHB, gfoRPA, gfoSWU, gfoSWD}
	gfoPathGPU = []string{gfoHB, gfoRPA, gfoSWU, gfoSWD, gfoGPU}
	gfoPathRPB = []string{gfoHB, gfoRPB}
	gfoPathNIC = []string{gfoHB, gfoRPB, gfoNIC}
)

// Basic adds the basic topology to one frame: GPU under switch downstream ->
// switch upstream -> root port A, NIC under root port B, all widths 16 and a
// canned CSV row for the GPU (8-digit upper-case domain).
func (f *gfoFx) Basic(frame int) {
	f.t.Helper()
	f.Dev(frame, gfoBridge(gfoPathRPA...))
	f.Dev(frame, gfoBridge(gfoPathSWU...))
	f.Dev(frame, gfoBridge(gfoPathSWD...))
	f.Dev(frame, gfoGPUDev(gfoPathGPU...))
	f.Dev(frame, gfoBridge(gfoPathRPB...))
	f.Dev(frame, gfoNICDev(gfoPathNIC...))
	f.Write(gfoCSV(frame), gfoUUIDA+", 00000000:03:00.0\n")
}

// gfoNewBasic returns a fixture whose every frame has the basic topology and
// whose manifest has the GPU baseline (peer = switch downstream, width 16).
func gfoNewBasic(t *testing.T, frames int) *gfoFx {
	t.Helper()
	f := gfoNewFx(t, frames)
	for i := 0; i < frames; i++ {
		f.Basic(i)
	}
	f.M.Baselines = []gfoBL{{gfoGPU, gfoSWD, "16"}}
	return f
}

// ---------------------------------------------------------------------------
// Key and ID helpers (GFO-047, GFO-084).
// ---------------------------------------------------------------------------

func gfoFnKey(bdf string) string { return "PCIeFunction/pci-bdf:" + bdf }
func gfoRPKey(bdf string) string { return "PCIeRootPort/pci-bdf:" + bdf }
func gfoSWKey(bdf string) string { return "PCIeSwitch/pci-bdf:" + bdf }
func gfoNodeKey(uid string) string {
	return "KubernetesNode/kubernetes-node-uid:" + uid
}

// gfoH is GFO-084 H: SHA-256 over u32be(len)||bytes of every tuple element,
// first 16 bytes as lower-case hex.
func gfoH(parts ...string) string {
	var b []byte
	for _, p := range parts {
		b = gfoAppendU32(b, uint32(len(p)))
		b = append(b, p...)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func gfoEdgeEvidenceID(session, seq string, e gfoEdge) string {
	return "pe:" + session + ":" + seq + ":" + gfoH("dpa.edge-evidence.v1", e.FromKey, e.Relation, e.ToKey, e.Origin)
}

func gfoObservationID(session, seq, subjectKey, signal string) string {
	return "ob:" + session + ":" + seq + ":" + gfoH("dpa.observation.v1", subjectKey, signal)
}

func gfoBindingID(session, seq, uuid, bdf string) string {
	return "nb:" + session + ":" + seq + ":" + gfoH("dpa.gpu-binding.v1", uuid, bdf)
}

func gfoAppendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func gfoAppendU64(b []byte, v uint64) []byte {
	return append(b, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32), byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// ---------------------------------------------------------------------------
// Fixture tree snapshots (GFO-044 no writes, GFO-088 determinism).
// ---------------------------------------------------------------------------

// gfoTreeState records every path under root without following symlinks:
// type, permission bits, size, mtime, symlink target and file content hash.
// FIFOs are never opened.
func gfoTreeState(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		desc := fmt.Sprintf("%v %d %d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			desc += " -> " + target
		case fi.Mode().IsRegular() && fi.Mode().Perm()&0o444 != 0:
			data, err := os.ReadFile(p)
			if err == nil {
				sum := sha256.Sum256(data)
				desc += " " + hex.EncodeToString(sum[:])
			}
		}
		state[rel] = desc
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return state
}
