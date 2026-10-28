package datapath_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/datapath"
	"github.com/aetertinas9/data-path-assurance/internal/nvidia"
	"github.com/aetertinas9/data-path-assurance/internal/render"
	"github.com/aetertinas9/data-path-assurance/internal/sysfs"
)

var update = flag.Bool("update", false, "rewrite the golden files and sample fixtures under testdata")

func build(t *testing.T, dir string) *datapath.Report {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sysRoot, err := root.OpenRoot("sys")
	if err != nil {
		t.Fatal(err)
	}
	defer sysRoot.Close()
	return datapath.Build("fixture:test", sysfs.Read(sysRoot), nvidia.FromDir(root, "nvidia"), datapath.DefaultOptions())
}

func check(t *testing.T, g *datapath.GPU, name string) datapath.Check {
	t.Helper()
	for _, c := range g.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("GPU %s has no check %q", g.Dev.BDF, name)
	return datapath.Check{}
}

func gpu(t *testing.T, r *datapath.Report, bdf string) *datapath.GPU {
	t.Helper()
	for _, g := range r.GPUs {
		if g.Dev.BDF == bdf {
			return g
		}
	}
	t.Fatalf("no GPU %s", bdf)
	return nil
}

func TestTwoSocketRTX(t *testing.T) {
	dir := t.TempDir()
	twoSocketRTX(t, dir)
	r := build(t, dir)
	if len(r.GPUs) != 3 || len(r.NICs) != 2 {
		t.Fatalf("got %d GPUs and %d NICs", len(r.GPUs), len(r.NICs))
	}
	for _, l := range r.Limitations {
		t.Errorf("unexpected limitation: %s", l)
	}

	g0 := gpu(t, r, "0000:1b:00.0")
	if g0.Name != "NVIDIA RTX PRO 6000 Blackwell Server Edition" || g0.Index != 0 || g0.NUMA != 0 || g0.CPUList != "0-47,96-143" {
		t.Errorf("GPU0 identity: %+v", *g0)
	}
	if kinds := hopKinds(g0); kinds != "gpu downstream upstream root" {
		t.Errorf("GPU0 hops %q", kinds)
	}
	best := g0.Best()
	if best.NIC.Dev.BDF != "0000:1c:00.0" || best.Locality != datapath.LocPIX || best.TopoRel != "PIX" {
		t.Errorf("GPU0 best NIC %+v", best)
	}
	if best.Capacity != 50 || !strings.HasPrefix(best.Bottleneck, "port mlx5_0/1") {
		t.Errorf("GPU0 capacity %v via %q", best.Capacity, best.Bottleneck)
	}
	if g0.NICs[1].NIC.Dev.BDF != "0000:9c:00.0" || g0.NICs[1].Locality != datapath.LocSYS || !g0.NICs[1].NIC.IsDPU {
		t.Errorf("GPU0 second NIC %+v", g0.NICs[1])
	}
	if p := g0.Peers[0]; p.GPU.Dev.BDF != "0000:1d:00.0" || p.Locality != datapath.LocPIX || !p.ACSKnown || strings.Join(p.ACSOn, ",") != "0000:1a:04.0,0000:1a:05.0" {
		t.Errorf("GPU0 peer GPU1 %+v", p)
	}
	if p := g0.Peers[1]; p.GPU.Dev.BDF != "0000:9b:00.0" || p.Locality != datapath.LocSYS {
		t.Errorf("GPU0 peer GPU2 %+v", p)
	}
	want := map[string]datapath.Status{
		"gpu-link": datapath.Pass, "upstream-links": datapath.Pass, "nic-locality": datapath.Pass,
		"nic-port": datapath.Pass, "numa-aligned": datapath.Pass, "p2p-acs": datapath.Fail,
		"nvlink": datapath.NA, "pcie-errors": datapath.Pass, "pcie-payload": datapath.Pass,
	}
	for name, st := range want {
		if c := check(t, g0, name); c.Status != st {
			t.Errorf("GPU0 %s = %s (%s), want %s", name, c.Status, c.Detail, st)
		}
	}

	g2 := gpu(t, r, "0000:9b:00.0")
	if c := check(t, g2, "gpu-link"); c.Status != datapath.Fail || !strings.Contains(c.Detail, "Gen4 x8 negotiated, Gen5 x16 expected") {
		t.Errorf("GPU2 gpu-link %+v", c)
	}
	if c := check(t, g2, "upstream-links"); c.Status != datapath.NA {
		t.Errorf("GPU2 upstream-links %+v", c)
	}
	if b := g2.Best(); b.NIC.Dev.BDF != "0000:9c:00.0" || b.Locality != datapath.LocPHB || b.Capacity > 16 || b.Capacity < 15 || b.Bottleneck != "pcie 0000:9b:00.0" {
		t.Errorf("GPU2 best %+v", b)
	}
	if c := check(t, g2, "nic-locality"); c.Status != datapath.Fail {
		t.Errorf("GPU2 nic-locality %+v", c)
	}
	if c := check(t, g2, "pcie-errors"); c.Status != datapath.Warn || !strings.Contains(c.Detail, "15 correctable") {
		t.Errorf("GPU2 pcie-errors %+v", c)
	}
	if c := check(t, g2, "p2p-acs"); c.Status != datapath.NA {
		t.Errorf("GPU2 p2p-acs %+v", c)
	}
	if r.Overall() != datapath.Fail {
		t.Errorf("overall %s", r.Overall())
	}
	golden(t, "two-socket-rtx", dir, r)
}

func TestNVLinkPair(t *testing.T) {
	dir := t.TempDir()
	nvlinkPair(t, dir)
	r := build(t, dir)
	for _, l := range r.Limitations {
		t.Errorf("unexpected limitation: %s", l)
	}
	g0, g1 := gpu(t, r, "0000:03:00.0"), gpu(t, r, "0000:04:00.0")
	if g0.Name != "NVIDIA B300" || g0.Dev.Link.GBps < 120 || datapath.Gen(g0.Dev.Link.Speed) != 6 {
		t.Errorf("GPU0 %s link %+v", g0.Name, g0.Dev.Link)
	}
	if p := g0.Peers[0]; p.NVLinks != 18 || p.Locality != datapath.LocPIX || len(p.ACSOn) != 0 || !p.ACSKnown {
		t.Errorf("peer %+v", p)
	}
	if c := check(t, g0, "nvlink"); c.Status != datapath.Pass || c.Detail != "18 of 18 links active" {
		t.Errorf("GPU0 nvlink %+v", c)
	}
	if c := check(t, g1, "nvlink"); c.Status != datapath.Fail || c.Detail != "17 of 18 links active" {
		t.Errorf("GPU1 nvlink %+v", c)
	}
	if c := check(t, g0, "p2p-acs"); c.Status != datapath.Pass {
		t.Errorf("GPU0 p2p-acs %+v", c)
	}
	b := g0.Best()
	if b.Capacity != 100 || !strings.HasPrefix(b.Bottleneck, "port mlx5_0/1") || b.NIC.PortGbps != 800 {
		t.Errorf("best %+v", b)
	}
	// The switch's upstream link runs Gen5 on a Gen5 root port although the
	// switch could do Gen6: platform limited, not degraded.
	usp := r.Devices["0000:01:00.0"]
	if usp.Link.Degraded || !usp.Link.PlatformLimited {
		t.Errorf("upstream port link %+v", usp.Link)
	}
	if c := check(t, g0, "upstream-links"); c.Status != datapath.Pass {
		t.Errorf("GPU0 upstream-links %+v", c)
	}
	golden(t, "nvlink-pair", dir, r)
}

func TestUnknownsStayUnknown(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, dir)
	// A GPU under a root port with no link attributes, no config, no NUMA
	// and no nvidia-smi outputs at all.
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0"}, vendor: 0x8086, device: 1, class: 0x060400, numa: -1})
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0", "0000:01:00.0"}, vendor: 0x10de, device: 0xffff, class: 0x030200, numa: -1})
	r := build(t, dir)
	if len(r.GPUs) != 1 {
		t.Fatalf("GPUs %d", len(r.GPUs))
	}
	g := r.GPUs[0]
	if g.Name != "NVIDIA GPU 10de:ffff" || g.NUMA != -1 || g.UUID != "" {
		t.Errorf("GPU %+v", *g)
	}
	for _, name := range []string{"gpu-link", "nic-locality", "nic-port", "numa-aligned"} {
		if c := check(t, g, name); c.Status != datapath.Unknown {
			t.Errorf("%s = %s, want UNKNOWN", name, c.Status)
		}
	}
	if c := check(t, g, "pcie-payload"); c.Status != datapath.NA {
		t.Errorf("pcie-payload %+v", c)
	}
	if len(r.Limitations) < 3 {
		t.Errorf("expected nvidia-smi limitations, got %v", r.Limitations)
	}
	if r.Overall() != datapath.Pass {
		t.Errorf("unknowns must not fail: %s", r.Overall())
	}
	out := render.Text(r)
	if !bytes.Contains(out, []byte("link   link ?")) || !bytes.Contains(out, []byte("LIMITATIONS")) {
		t.Errorf("text:\n%s", out)
	}
}

func TestLocalityTargetAndGPUSelection(t *testing.T) {
	dir := t.TempDir()
	twoSocketRTX(t, dir)
	root, _ := os.OpenRoot(dir)
	defer root.Close()
	sysRoot, _ := root.OpenRoot("sys")
	defer sysRoot.Close()
	opts := datapath.DefaultOptions()
	opts.LocalityTarget = datapath.LocSYS
	r := datapath.Build("x", sysfs.Read(sysRoot), nvidia.FromDir(root, "nvidia"), opts)
	if c := check(t, gpu(t, r, "0000:9b:00.0"), "nic-locality"); c.Status != datapath.Pass {
		t.Errorf("with target SYS, PHB must pass: %+v", c)
	}
}

func hopKinds(g *datapath.GPU) string {
	var k []string
	for _, h := range g.Hops {
		k = append(k, h.Kind)
	}
	return strings.Join(k, " ")
}

// golden compares the text, JSON and Mermaid renderings with the files under
// testdata/golden and, with -update, rewrites them and copies the fixture to
// testdata/fixtures/<name> so that pathctl can be run against it.
func golden(t *testing.T, name, fixtureDir string, r *datapath.Report) {
	t.Helper()
	r.Source = "fixture:testdata/fixtures/" + name
	base := filepath.Join("..", "..", "testdata")
	js, err := render.JSON(r)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string][]byte{name + ".txt": render.Text(r), name + ".json": js, name + ".mmd": render.Mermaid(r)}
	for file, got := range outputs {
		p := filepath.Join(base, "golden", file)
		if *update {
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v (run with -update)", p, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from golden; run with -update after reviewing:\n%s", file, got)
		}
	}
	if *update {
		dst := filepath.Join(base, "fixtures", name)
		_ = os.RemoveAll(dst)
		if err := copyTree(fixtureDir, dst); err != nil {
			t.Fatal(err)
		}
	}
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		}
	})
}
