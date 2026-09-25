package tests_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gfoAssertPartial / gfoAssertComplete check a frame's completeness.
func gfoAssertCompleteness(t *testing.T, fr gfoFrame, want, clause string) {
	t.Helper()
	if fr.Completeness != want {
		t.Errorf("%s: completeness %s, want %s; diagnostics %s", clause, fr.Completeness, want, fr.DiagString())
	}
}

func gfoAssertCode(t *testing.T, fr gfoFrame, code, clause string) {
	t.Helper()
	if fr.CountCode(code) == 0 {
		t.Errorf("%s: missing diagnostic %s; got %s", clause, code, fr.DiagString())
	}
}

func gfoAssertNoCode(t *testing.T, fr gfoFrame, code, clause string) {
	t.Helper()
	if n := fr.CountCode(code); n != 0 {
		t.Errorf("%s: unexpected diagnostic %s; got %s", clause, code, fr.DiagString())
	}
}

func gfoAssertAsset(t *testing.T, p gfoPayload, key string, want bool, clause string) {
	t.Helper()
	if p.HasAsset(key) != want {
		t.Errorf("%s: asset %s present=%v, want %v; assets %v", clause, key, !want, want, p.AssetKeys())
	}
}

// gfoAssertBasicIntact checks that the basic GPU/NIC chain survived.
func gfoAssertBasicIntact(t *testing.T, p gfoPayload, clause string) {
	t.Helper()
	for _, e := range [][2]string{
		{gfoFnKey(gfoGPU), gfoSWKey(gfoSWD)}, {gfoSWKey(gfoSWD), gfoSWKey(gfoSWU)}, {gfoSWKey(gfoSWU), gfoRPKey(gfoRPA)},
		{gfoRPKey(gfoRPA), gfoNodeKey(gfoNodeUID)}, {gfoFnKey(gfoNIC), gfoRPKey(gfoRPB)}, {gfoRPKey(gfoRPB), gfoNodeKey(gfoNodeUID)},
	} {
		if !p.HasEdge(e[0], e[1]) {
			t.Errorf("%s: basic edge %s -> %s missing; edges %v", clause, e[0], e[1], p.EdgeStrings())
		}
	}
}

// GFO-040/GFO-041: only canonical BDF entry names are processed; any other
// name is sysfs_entry_name_invalid (PARTIAL) and the rest is still processed.
func TestGFO040_EntryNameGrammar(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	invalid := []string{
		"0000:0A:00.0",      // upper-case hex
		"00000000:05:00.0",  // 8-digit zero-padded domain
		"00000:05:00.0",     // 5-digit domain with a leading zero
		"123456789:05:00.0", // 9-digit domain
		"000:05:00.0",       // 3-digit domain
		"0000:5:00.0",       // 1-digit bus
		"0000:05:20.0",      // device 0x20
		"0000:05:00.8",      // function 8
		"0000:05:00",        // no function
		"0000:05:00.0.0",    // extra suffix
		".gitkeep",
		"pci0000:00",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			// A GPU directory whose last component equals the name: if the
			// name were accepted, a function asset would appear.
			f.Dev(0, gfoGPUDev(gfoHB, name))
			a := f.OK(bin, "GFO-040 "+name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-041 "+name)
			gfoAssertCode(t, fr, "sysfs_entry_name_invalid", "GFO-041 "+name)
			gfoAssertAsset(t, fr.Payload, gfoFnKey(name), false, "GFO-041 "+name)
			gfoAssertBasicIntact(t, fr.Payload, "GFO-041 "+name)
			for _, as := range fr.Payload.Assets {
				if strings.Contains(as.Canonical, name) {
					t.Errorf("GFO-041: invalid entry %q produced asset %s", name, as.Key())
				}
			}
		})
	}
}

// GFO-040: 32-bit domains (5..8 hex digits without leading zero), device
// 0x1f and function 7 are canonical; host bridge pci<domain>:<bus>.
func TestGFO040_WideDomainsAccepted(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	f.Dev(0, gfoBridge("pci10000:00", "10000:00:01.0"))
	f.Dev(0, gfoGPUDev("pci10000:00", "10000:00:01.0", "10000:01:00.0"))
	f.Dev(0, gfoBridge("pciffffffff:00", "ffffffff:00:01.0"))
	f.Dev(0, gfoGPUDev("pciffffffff:00", "ffffffff:00:01.0", "ffffffff:ff:1f.7"))
	f.Write(gfoCSV(0), gfoUUIDA+", 00000000:03:00.0\n"+gfoUUIDB+", 00010000:01:00.0\n"+
		"GPU-2c3d4e5f-6071-8293-a4b5-c6d7e8f9a0b1, FFFFFFFF:FF:1F.7\n")
	a := f.OK(bin, "GFO-040 wide domains")
	fr := a.Frames[0]
	gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-040")
	p := fr.Payload
	for _, e := range [][2]string{
		{gfoFnKey("10000:01:00.0"), gfoRPKey("10000:00:01.0")}, {gfoRPKey("10000:00:01.0"), gfoNodeKey(gfoNodeUID)},
		{gfoFnKey("ffffffff:ff:1f.7"), gfoRPKey("ffffffff:00:01.0")}, {gfoRPKey("ffffffff:00:01.0"), gfoNodeKey(gfoNodeUID)},
	} {
		if !p.HasEdge(e[0], e[1]) {
			t.Errorf("GFO-040/GFO-048: edge %s -> %s missing; edges %v", e[0], e[1], p.EdgeStrings())
		}
	}
	if b, ok := p.Binding("10000:01:00.0"); !ok || b.UUID != gfoUUIDB {
		t.Errorf("GFO-071/GFO-075: 00010000:01:00.0 row must bind BDF 10000:01:00.0, got %+v", p.GPUBindings)
	}
	if _, ok := p.Binding("ffffffff:ff:1f.7"); !ok {
		t.Errorf("GFO-071/GFO-075: FFFFFFFF:FF:1F.7 row must bind BDF ffffffff:ff:1f.7, got %+v", p.GPUBindings)
	}
}

// GFO-041: listing failures leave the frame PARTIAL with no entry processed;
// an empty listing is a COMPLETE frame with no function.
func TestGFO041_Enumeration(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	outsideBus := func(t *testing.T) string {
		o := gfoNewBasic(t, 1)
		return o.P(gfoSys(0) + "/bus")
	}
	failures := []struct {
		name  string
		setup func(t *testing.T, f *gfoFx)
	}{
		{"devices_missing", func(t *testing.T, f *gfoFx) { f.Remove(gfoSys(0) + "/bus/pci/devices") }},
		{"bus_missing", func(t *testing.T, f *gfoFx) { f.Remove(gfoSys(0) + "/bus") }},
		{"devices_is_file", func(t *testing.T, f *gfoFx) {
			f.Remove(gfoSys(0) + "/bus/pci/devices")
			f.Write(gfoSys(0)+"/bus/pci/devices", "x")
		}},
		{"devices_is_fifo", func(t *testing.T, f *gfoFx) {
			f.Remove(gfoSys(0) + "/bus/pci/devices")
			f.Fifo(gfoSys(0) + "/bus/pci/devices")
		}},
		{"bus_symlink_outside_root", func(t *testing.T, f *gfoFx) {
			f.Remove(gfoSys(0) + "/bus")
			f.Link(gfoSys(0)+"/bus", outsideBus(t))
		}},
		{"bus_relative_symlink_escaping_frame", func(t *testing.T, f *gfoFx) {
			// frames/0/sys/bus -> ../../1/sys/bus lexically leaves the frame
			// sysfs root (GFO-030: the frame sysfs root is the boundary).
			f.Basic(1)
			f.Remove(gfoSys(0) + "/bus")
			f.Link(gfoSys(0)+"/bus", "../../1/sys/bus")
		}},
	}
	for _, c := range failures {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.setup(t, f)
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			a := gfoOK(t, res, &f.M, "GFO-041 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-041 "+c.name)
			gfoAssertCode(t, fr, "sysfs_list_failed", "GFO-041 "+c.name)
			if len(fr.Payload.Assets) != 1 || fr.Payload.Assets[0].Kind != "KubernetesNode" || len(fr.Payload.Edges) != 0 ||
				len(fr.Payload.Observations) != 0 || len(fr.Payload.GPUBindings) != 0 {
				t.Errorf("GFO-041 %s: a failed listing must process no entry; assets %v edges %v", c.name, fr.Payload.AssetKeys(), fr.Payload.EdgeStrings())
			}
		})
	}
	t.Run("empty_listing_complete", func(t *testing.T) {
		t.Parallel()
		f := gfoNewFx(t, 1)
		f.Mkdir(gfoSys(0) + "/bus/pci/devices")
		a := f.OK(bin, "GFO-041 empty")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-041 empty")
		if len(fr.Payload.Assets) != 1 || len(fr.Payload.Edges) != 0 || len(fr.Payload.Observations) != 0 {
			t.Errorf("GFO-041: empty listing must yield only the node asset; got %v", fr.Payload.AssetKeys())
		}
	})
}

// GFO-042: entry symlink resolution (non-symlink, absolute, lexical escape,
// symlinked components, missing target, last-component mismatch) and the
// lexical cleaning of valid targets.
func TestGFO042_SymlinkResolution(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	const x = "0000:05:00.0"
	gpuAttrs := gfoGPUDev(gfoHB, x).Attrs
	type tc struct {
		name  string
		setup func(t *testing.T, f *gfoFx)
		code  string // "" means the entry is valid and processed
	}
	realDir := func(f *gfoFx, path ...string) {
		f.Dev(0, gfoDev{Path: path, Attrs: gpuAttrs})
	}
	cases := []tc{
		{"entry_is_directory", func(t *testing.T, f *gfoFx) {
			for k, v := range gpuAttrs {
				f.Write(gfoEntry(0, x)+"/"+k, v)
			}
		}, "sysfs_link_invalid"},
		{"entry_is_file", func(t *testing.T, f *gfoFx) { f.Write(gfoEntry(0, x), "x") }, "sysfs_link_invalid"},
		{"entry_is_fifo", func(t *testing.T, f *gfoFx) { f.Fifo(gfoEntry(0, x)) }, "sysfs_link_invalid"},
		{"target_missing", func(t *testing.T, f *gfoFx) { f.Link(gfoEntry(0, x), "../../../devices/"+gfoHB+"/"+x) }, "sysfs_link_invalid"},
		{"target_is_file", func(t *testing.T, f *gfoFx) {
			f.Write(gfoDevDir(0, []string{gfoHB, x}), "x")
			f.Link(gfoEntry(0, x), "../../../devices/"+gfoHB+"/"+x)
		}, "sysfs_link_invalid"},
		{"last_component_mismatch", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, "0000:05:00.1")
			f.Link(gfoEntry(0, x), "../../../devices/"+gfoHB+"/0000:05:00.1")
		}, "sysfs_link_invalid"},
		{"target_dot", func(t *testing.T, f *gfoFx) { f.Link(gfoEntry(0, x), ".") }, "sysfs_link_invalid"},
		{"component_is_symlink", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoDev{Path: []string{"realhb", x}, Attrs: gpuAttrs})
			f.Link(gfoSys(0)+"/devices/pci0000:40", "realhb")
			f.Link(gfoEntry(0, x), "../../../devices/pci0000:40/"+x)
		}, "sysfs_link_invalid"},
		{"last_component_is_symlink", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, "real-"+x)
			f.Link(gfoDevDir(0, []string{gfoHB, x}), "real-"+x)
			f.Link(gfoEntry(0, x), "../../../devices/"+gfoHB+"/"+x)
		}, "sysfs_link_invalid"},
		{"absolute_target_inside_root", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, x)
			f.Link(gfoEntry(0, x), f.P(gfoDevDir(0, []string{gfoHB, x})))
		}, "sysfs_link_escape"},
		{"absolute_target_sys", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, x)
			f.Link(gfoEntry(0, x), "/sys/devices/"+gfoHB+"/"+x)
		}, "sysfs_link_escape"},
		{"relative_escape_reentering_root", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, x)
			f.Link(gfoEntry(0, x), "../../../../sys/devices/"+gfoHB+"/"+x)
		}, "sysfs_link_escape"},
		{"relative_escape_outside_fixture", func(t *testing.T, f *gfoFx) {
			o := gfoNewFx(t, 1)
			o.Dev(0, gfoGPUDev(gfoHB, x))
			rel, err := filepath.Rel(filepath.Dir(f.P(gfoEntry(0, x))), o.P(gfoDevDir(0, []string{gfoHB, x})))
			if err != nil {
				t.Fatal(err)
			}
			f.Link(gfoEntry(0, x), rel)
		}, "sysfs_link_escape"},
		{"valid_trailing_slash", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, x)
			f.Link(gfoEntry(0, x), "../../../devices/"+gfoHB+"/"+x+"/")
		}, ""},
		{"valid_dot_and_double_slash", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, x)
			f.Link(gfoEntry(0, x), "../../../devices/./"+gfoHB+"//"+x)
		}, ""},
		{"valid_inner_dotdot", func(t *testing.T, f *gfoFx) {
			realDir(f, gfoHB, x)
			f.Link(gfoEntry(0, x), "../../../devices/"+gfoHB+"/"+gfoRPA+"/../"+x)
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.setup(t, f)
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			a := gfoOK(t, res, &f.M, "GFO-042 "+c.name)
			fr := a.Frames[0]
			gfoAssertBasicIntact(t, fr.Payload, "GFO-042 "+c.name)
			if c.code == "" {
				gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-042 "+c.name)
				gfoAssertAsset(t, fr.Payload, gfoFnKey(x), true, "GFO-042 "+c.name)
				if !fr.Payload.HasEdge(gfoFnKey(x), gfoNodeKey(gfoNodeUID)) {
					t.Errorf("GFO-042/GFO-048 %s: edge %s -> node missing", c.name, x)
				}
				return
			}
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-042 "+c.name)
			gfoAssertCode(t, fr, c.code, "GFO-042 "+c.name)
			gfoAssertAsset(t, fr.Payload, gfoFnKey(x), false, "GFO-042 "+c.name)
		})
	}
}

// gfoChain returns n distinct bridge BDFs 0000:<b>:00.0 starting at bus b0.
func gfoChain(b0, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("0000:%02x:00.0", b0+i))
	}
	return out
}

// gfoAddChain adds bridges along path prefix+chain and a leaf device.
func gfoAddChain(f *gfoFx, frame int, prefix []string, chain []string, leaf gfoDev) {
	path := append([]string{}, prefix...)
	for _, b := range chain {
		path = append(path, b)
		f.Dev(frame, gfoBridge(path...))
	}
	leaf.Path = append(path, leaf.BDF)
	f.Dev(frame, leaf)
}

// GFO-043: supported layout (one host bridge, optional non-BDF platform
// prefix, 1..17 distinct canonical BDF components ending in the entry).
func TestGFO043_Layout(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	const x = "0000:61:00.0"
	gpu := gfoGPUDev(gfoHB, x)
	type tc struct {
		name      string
		setup     func(t *testing.T, f *gfoFx)
		supported bool
	}
	cases := []tc{
		{"platform_prefix", func(t *testing.T, f *gfoFx) {
			gfoAddChain(f, 0, []string{"platform", "soc", "pci0000:60"}, []string{"0000:60:01.0"}, gfoDev{BDF: x, Attrs: gpu.Attrs})
		}, true},
		{"ancestors_16", func(t *testing.T, f *gfoFx) {
			gfoAddChain(f, 0, []string{"pci0000:60"}, gfoChain(0x70, 16), gfoDev{BDF: x, Attrs: gpu.Attrs})
		}, true},
		{"ancestors_17", func(t *testing.T, f *gfoFx) {
			gfoAddChain(f, 0, []string{"pci0000:60"}, gfoChain(0x70, 17), gfoDev{BDF: x, Attrs: gpu.Attrs})
		}, false},
		{"no_host_bridge", func(t *testing.T, f *gfoFx) { f.Dev(0, gfoGPUDev(x)) }, false},
		{"first_component_not_devices", func(t *testing.T, f *gfoFx) {
			f.Mkdir(gfoSys(0) + "/other/" + gfoHB + "/" + x)
			for k, v := range gpu.Attrs {
				f.Write(gfoSys(0)+"/other/"+gfoHB+"/"+x+"/"+k, v)
			}
			f.Link(gfoEntry(0, x), "../../../other/"+gfoHB+"/"+x)
		}, false},
		{"two_host_bridges", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoDev{BDF: "0000:00:0e.0", Path: []string{gfoHB, "0000:00:0e.0"}, Attrs: map[string]string{"class": "0x010400\n"}})
			f.Dev(0, gfoBridge(gfoHB, "0000:00:0e.0", "pci10000:e0", "10000:e0:06.0"))
			f.Dev(0, gfoDev{BDF: x, Path: []string{gfoHB, "0000:00:0e.0", "pci10000:e0", "10000:e0:06.0", x}, Attrs: gpu.Attrs})
		}, false},
		{"bdf_before_host_bridge", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoDev{BDF: x, Path: []string{"0000:ff:00.0", "pci0000:60", x}, Attrs: gpu.Attrs})
		}, false},
		{"host_bridge_zero_padded_domain", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoDev{BDF: x, Path: []string{"pci00000:60", x}, Attrs: gpu.Attrs})
		}, false},
		{"non_bdf_component_below_host_bridge", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoDev{BDF: x, Path: []string{"pci0000:60", "junk", x}, Attrs: gpu.Attrs})
		}, false},
		{"upper_case_ancestor_component", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoDev{BDF: x, Path: []string{"pci0000:60", "0000:60:1F.0", x}, Attrs: gpu.Attrs})
		}, false},
		{"duplicate_component", func(t *testing.T, f *gfoFx) {
			f.Dev(0, gfoBridge("pci0000:60", "0000:60:01.0"))
			f.Dev(0, gfoDev{BDF: x, Path: []string{"pci0000:60", "0000:60:01.0", "0000:60:01.0", x}, Attrs: gpu.Attrs})
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.setup(t, f)
			a := f.OK(bin, "GFO-043 "+c.name)
			fr := a.Frames[0]
			gfoAssertBasicIntact(t, fr.Payload, "GFO-043 "+c.name)
			// The GPU is selected either way (class/vendor are readable).
			gfoAssertAsset(t, fr.Payload, gfoFnKey(x), true, "GFO-043/GFO-048 "+c.name)
			if c.supported {
				gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-043 "+c.name)
				gfoAssertNoCode(t, fr, "sysfs_layout_unsupported", "GFO-043 "+c.name)
				if len(fr.Payload.EdgesFrom(gfoFnKey(x))) != 1 {
					t.Errorf("GFO-048 %s: want exactly one parent edge from %s, got %v", c.name, x, fr.Payload.EdgeStrings())
				}
				return
			}
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-043 "+c.name)
			gfoAssertCode(t, fr, "sysfs_layout_unsupported", "GFO-043 "+c.name)
			if n := len(fr.Payload.EdgesFrom(gfoFnKey(x))); n != 0 {
				t.Errorf("GFO-048 %s: unresolved closure must not emit hops from F, got %v", c.name, fr.Payload.EdgesFrom(gfoFnKey(x)))
			}
		})
	}

	t.Run("sixteen_ancestor_hops_and_kinds", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		chain := gfoChain(0x70, 16)
		gfoAddChain(f, 0, []string{"pci0000:60"}, chain, gfoDev{BDF: x, Attrs: gpu.Attrs})
		a := f.OK(bin, "GFO-043 16 ancestors")
		p := a.Frames[0].Payload
		gfoAssertAsset(t, p, gfoRPKey(chain[0]), true, "GFO-047")
		for _, b := range chain[1:] {
			gfoAssertAsset(t, p, gfoSWKey(b), true, "GFO-047")
		}
		want := [][2]string{{gfoFnKey(x), gfoSWKey(chain[15])}, {gfoRPKey(chain[0]), gfoNodeKey(gfoNodeUID)}}
		for i := 1; i < 16; i++ {
			from := gfoSWKey(chain[i])
			to := gfoSWKey(chain[i-1])
			if i == 1 {
				to = gfoRPKey(chain[0])
			}
			want = append(want, [2]string{from, to})
		}
		for _, e := range want {
			if !p.HasEdge(e[0], e[1]) {
				t.Errorf("GFO-048: hop %s -> %s missing", e[0], e[1])
			}
		}
	})

	t.Run("unrelated_unsupported_entry_is_diagnostic_only", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		// VMD-like nested host bridge with a non-selected NVMe below it.
		f.Dev(0, gfoDev{BDF: "0000:00:0e.0", Path: []string{gfoHB, "0000:00:0e.0"}, Attrs: map[string]string{"class": "0x010400\n"}})
		f.Dev(0, gfoBridge(gfoHB, "0000:00:0e.0", "pci10000:e0", "10000:e0:06.0"))
		f.Dev(0, gfoDev{BDF: "10000:e1:00.0", Path: []string{gfoHB, "0000:00:0e.0", "pci10000:e0", "10000:e0:06.0", "10000:e1:00.0"},
			Attrs: map[string]string{"class": "0x010802\n"}})
		// An audio function with no host bridge at all.
		f.Dev(0, gfoDev{BDF: "0000:06:00.0", Path: []string{"0000:06:00.0"}, Attrs: map[string]string{"class": "0x040300\n"}})
		a := f.OK(bin, "GFO-043 unrelated")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-043/GFO-050 unrelated unsupported")
		gfoAssertCode(t, fr, "sysfs_layout_unsupported", "GFO-043 unrelated")
		for _, key := range []string{gfoFnKey("10000:e1:00.0"), gfoFnKey("0000:06:00.0"), gfoFnKey("0000:00:0e.0"), gfoSWKey("10000:e0:06.0"), gfoRPKey("10000:e0:06.0")} {
			gfoAssertAsset(t, fr.Payload, key, false, "GFO-046/GFO-047 unrelated")
		}
	})
}

// gfoDecoyNames are GFO-044 excluded attribute names.
var gfoDecoyNames = []string{
	"config", "enable", "remove", "rescan", "reset", "reset_method", "resource", "resource0", "resource2",
	"resource0_wc", "rom", "driver_override", "sriov_numvfs", "sriov_totalvfs", "sriov_offset", "numa_node",
	"power", "driver", "subsystem",
}

// gfoDecorate adds decoys of the given kind to every device directory of the
// basic topology and a few places of the frame sysfs root.
func gfoDecorate(t *testing.T, f *gfoFx, frame int, kind string) {
	t.Helper()
	outside := filepath.Join(t.TempDir(), "outside-decoy")
	if err := os.WriteFile(outside, []byte("0x030200\n16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs := []string{}
	for _, p := range [][]string{gfoPathRPA, gfoPathSWU, gfoPathSWD, gfoPathGPU, gfoPathRPB, gfoPathNIC, {gfoHB}} {
		dirs = append(dirs, gfoDevDir(frame, p))
	}
	extra := []string{gfoSys(frame) + "/bus/pci/rescan", gfoSys(frame) + "/bus/pci/drivers_probe", gfoDevDir(frame, []string{gfoHB}) + "/class"}
	var targets []string
	for _, d := range dirs {
		for _, n := range gfoDecoyNames {
			targets = append(targets, d+"/"+n)
		}
	}
	targets = append(targets, extra...)
	for _, p := range targets {
		switch kind {
		case "fifo":
			f.Fifo(p)
		case "mode000":
			f.Write(p, "0x030200\n")
			f.Chmod(p, 0)
		case "escape":
			f.Link(p, outside)
		default:
			t.Fatalf("test bug: decoy kind %q", kind)
		}
	}
}

// GFO-044/GFO-101(b): excluded names (as FIFO, mode 000 or root-escaping
// symlink) are never opened: stdout equals the decoy-free run, within 10s.
func TestGFO044_GFO101b_ExcludedNamesAreNeverRead(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	clean := gfoNewBasic(t, 1)
	want := clean.Run(bin)
	gfoOK(t, want, &clean.M, "GFO-044 clean")
	for _, kind := range []string{"fifo", "mode000", "escape"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			gfoDecorate(t, f, 0, kind)
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			gfoOK(t, res, &f.M, "GFO-044/GFO-101(b) "+kind)
			if !bytes.Equal(res.Stdout, want.Stdout) {
				t.Errorf("GFO-044/GFO-101(b): %s decoys changed stdout", kind)
			}
		})
	}
}

// GFO-044: collection never writes: the fixture tree (content, modes,
// mtimes, links) is unchanged by a run.
func TestGFO044_NoWrites(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 2)
	gfoDecorate(t, f, 1, "fifo")
	f.save()
	before := gfoTreeState(t, f.Root)
	gfoOK(t, gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.Root), &f.M, "GFO-044 no writes")
	after := gfoTreeState(t, f.Root)
	if len(before) != len(after) {
		t.Errorf("GFO-044: tree has %d entries after the run, %d before", len(after), len(before))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("GFO-044: %s changed: %q -> %q", k, v, after[k])
		}
	}
}

// GFO-045: class attribute reading and grammar on the GPU; results other
// than ok leave the entry unclassified and the frame PARTIAL.
func TestGFO045_ClassAttribute(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }
	type tc struct {
		name  string
		setup func(t *testing.T, f *gfoFx, p string)
		code  string // "" = ok
		value int64  // expected class value when ok
	}
	write := func(v string) func(t *testing.T, f *gfoFx, p string) {
		return func(t *testing.T, f *gfoFx, p string) { f.Write(p, v) }
	}
	cases := []tc{
		{"plain", write("0x030200"), "", 0x030200},
		{"trim_sp_ht_cr_lf", write(" \t0x030200\r\n"), "", 0x030200},
		{"many_newlines", write("0x030200\n\n\n"), "", 0x030200},
		{"upper_case_digits", write("0x0302AB\n"), "", 0x0302ab},
		{"exactly_4096_bytes", write(pad("0x030200", 4096)), "", 0x030200},
		{"4097_bytes", write(pad("0x030200", 4097)), "sysfs_attr_malformed", 0},
		{"five_digits", write("0x30200\n"), "sysfs_attr_malformed", 0},
		{"seven_digits", write("0x0302000\n"), "sysfs_attr_malformed", 0},
		{"no_prefix", write("030200\n"), "sysfs_attr_malformed", 0},
		{"non_hex", write("0x03020g\n"), "sysfs_attr_malformed", 0},
		{"empty", write(""), "sysfs_attr_malformed", 0},
		{"nul_inside", write("0x03\x000200\n"), "sysfs_attr_malformed", 0},
		{"nul_trailing", write("0x030200\x00"), "sysfs_attr_malformed", 0},
		{"vertical_tab", write("0x030200\v"), "sysfs_attr_malformed", 0},
		{"inner_space", write("0x 030200"), "sysfs_attr_malformed", 0},
		{"sign", write("+0x030200"), "sysfs_attr_malformed", 0},
		{"missing", func(t *testing.T, f *gfoFx, p string) { f.Remove(p) }, "sysfs_attr_missing", 0},
		{"fifo", func(t *testing.T, f *gfoFx, p string) { f.Remove(p); f.Fifo(p) }, "sysfs_attr_unreadable", 0},
		{"directory", func(t *testing.T, f *gfoFx, p string) { f.Remove(p); f.Mkdir(p) }, "sysfs_attr_unreadable", 0},
		{"symlink_outside_root", func(t *testing.T, f *gfoFx, p string) {
			o := filepath.Join(t.TempDir(), "class")
			if err := os.WriteFile(o, []byte("0x030200\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			f.Remove(p)
			f.Link(p, o)
		}, "sysfs_attr_unreadable", 0},
		{"relative_symlink_escaping_frame", func(t *testing.T, f *gfoFx, p string) {
			f.Write("frames/outside-class", "0x030200\n")
			f.Remove(p)
			rel, err := filepath.Rel(filepath.Dir(f.P(p)), f.P("frames/outside-class"))
			if err != nil {
				t.Fatal(err)
			}
			f.Link(p, rel)
		}, "sysfs_attr_unreadable", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			p := gfoDevDir(0, gfoPathGPU) + "/class"
			c.setup(t, f, p)
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			a := gfoOK(t, res, &f.M, "GFO-045 "+c.name)
			fr := a.Frames[0]
			if c.code == "" {
				gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-045 "+c.name)
				cls := fr.Payload.Obs(gfoFnKey(gfoGPU), gfoSigClass)
				if len(cls) != 1 || cls[0].IntValue() != strconv.FormatInt(c.value, 10) {
					t.Errorf("GFO-045/GFO-064 %s: class observation %+v, want value %d", c.name, cls, c.value)
				}
				return
			}
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-045/GFO-050 "+c.name)
			if !fr.HasDiag(c.code, gfoAttrSubject(gfoPathGPU, "class")) {
				t.Errorf("GFO-045/GFO-051 %s: want %s(%s), got %s", c.name, c.code, gfoAttrSubject(gfoPathGPU, "class"), fr.DiagString())
			}
			gfoAssertAsset(t, fr.Payload, gfoFnKey(gfoGPU), false, "GFO-046 "+c.name)
			if bytes.Contains(res.Stdout, []byte("0x0302")) {
				t.Errorf("GFO-051: attribute content leaked into the artifact")
			}
		})
	}
}

// GFO-045: EACCES on an attribute is `permission`. Root can read mode 000,
// so the case is skipped for euid 0.
func TestGFO045_AttributePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("GFO-045 sysfs_attr_permission: euid 0 can read mode 000 files")
	}
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	f.Chmod(gfoDevDir(0, gfoPathGPU)+"/class", 0)
	a := f.OK(bin, "GFO-045 permission")
	fr := a.Frames[0]
	gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-045 permission")
	if !fr.HasDiag("sysfs_attr_permission", gfoAttrSubject(gfoPathGPU, "class")) {
		t.Errorf("GFO-045: want sysfs_attr_permission(%s), got %s", gfoAttrSubject(gfoPathGPU, "class"), fr.DiagString())
	}
	gfoAssertAsset(t, fr.Payload, gfoFnKey(gfoGPU), false, "GFO-046 permission")

	// Width attribute permission: diagnostic + width_omitted, still COMPLETE
	// [OD-12 C].
	w := gfoNewBasic(t, 1)
	w.Chmod(gfoDevDir(0, gfoPathGPU)+"/current_link_width", 0)
	b := w.OK(bin, "GFO-045 width permission")
	wf := b.Frames[0]
	gfoAssertCompleteness(t, wf, "COMPLETE", "GFO-050/GFO-060 width permission")
	if !wf.HasDiag("sysfs_attr_permission", gfoAttrSubject(gfoPathGPU, "current_link_width")) || !wf.HasDiag("width_omitted", gfoGPU) {
		t.Errorf("GFO-060: want sysfs_attr_permission(current_link_width) and width_omitted(%s), got %s", gfoGPU, wf.DiagString())
	}
}

// GFO-045/GFO-046: vendor is read only for base 0x03 entries; non-ok vendor
// there is PARTIAL, anywhere else it is never read.
func TestGFO045_VendorAttribute(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	type tc struct {
		name     string
		dev      []string // device path whose vendor is changed
		vendor   string   // content; "<fifo>" or "<missing>" for special files
		selected bool     // GPU still selected
		code     string   // expected diagnostic on that vendor ("" none)
		complete bool
	}
	cases := []tc{
		{"gpu_upper_case", gfoPathGPU, "0x10DE\n", true, "", true},
		{"gpu_trimmed", gfoPathGPU, "\t0x10de \r\n", true, "", true},
		{"gpu_other_vendor", gfoPathGPU, "0x1002\n", false, "", true},
		{"gpu_five_digits", gfoPathGPU, "0x10de0\n", false, "sysfs_attr_malformed", false},
		{"gpu_no_prefix", gfoPathGPU, "10de\n", false, "sysfs_attr_malformed", false},
		{"gpu_missing", gfoPathGPU, "<missing>", false, "sysfs_attr_missing", false},
		{"gpu_fifo", gfoPathGPU, "<fifo>", false, "sysfs_attr_unreadable", false},
		{"nic_fifo_not_read", gfoPathNIC, "<fifo>", true, "", true},
		{"nic_garbage_not_read", gfoPathNIC, "garbage", true, "", true},
		{"nic_missing_not_read", gfoPathNIC, "<missing>", true, "", true},
		{"bridge_fifo_not_read", gfoPathSWD, "<fifo>", true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			p := gfoDevDir(0, c.dev) + "/vendor"
			f.Remove(p)
			switch c.vendor {
			case "<fifo>":
				f.Fifo(p)
			case "<missing>":
			default:
				f.Write(p, c.vendor)
			}
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			a := gfoOK(t, res, &f.M, "GFO-045 vendor "+c.name)
			fr := a.Frames[0]
			want := "PARTIAL"
			if c.complete {
				want = "COMPLETE"
			}
			gfoAssertCompleteness(t, fr, want, "GFO-046/GFO-050 "+c.name)
			gfoAssertAsset(t, fr.Payload, gfoFnKey(gfoGPU), c.selected || c.dev[len(c.dev)-1] != gfoGPU, "GFO-046 "+c.name)
			sub := gfoAttrSubject(c.dev, "vendor")
			if c.code != "" && !fr.HasDiag(c.code, sub) {
				t.Errorf("GFO-045 %s: want %s(%s), got %s", c.name, c.code, sub, fr.DiagString())
			}
			for _, d := range fr.Diagnostics {
				if c.code == "" && d.Subject == sub {
					t.Errorf("GFO-046 %s: vendor must not be read here, got %s(%s)", c.name, d.Code, d.Subject)
				}
			}
		})
	}
}

// GFO-045/GFO-060: current_link_width grammar on the NIC (adjacent
// baseline): ok values, nodata (0, empty, exactly "Unknown") and malformed.
// Width results never change completeness [OD-12 C].
func TestGFO045_WidthGrammar(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	type tc struct {
		content string
		want    string // current value, "nodata" or "malformed"
	}
	cases := []tc{
		{"16", "16"}, {"16\n", "16"}, {"016", "16"}, {"0016\n", "16"}, {" \t8 \r\n", "8"},
		{"1", "1"}, {"2", "2"}, {"4", "4"}, {"8", "8"}, {"12", "12"}, {"32", "32"},
		{"0", "nodata"}, {"00\n", "nodata"}, {"", "nodata"}, {"\n", "nodata"}, {"Unknown", "nodata"}, {"Unknown\n", "nodata"},
		{"unknown", "malformed"}, {"UNKNOWN", "malformed"}, {"3", "malformed"}, {"6", "malformed"}, {"24", "malformed"},
		{"64", "malformed"}, {"+16", "malformed"}, {"-16", "malformed"}, {"0x10", "malformed"}, {"16.0", "malformed"},
		{"1 6", "malformed"}, {"16x", "malformed"}, {"x16", "malformed"}, {"99999999999999999999", "malformed"},
		{"16\x00", "malformed"}, {"Unknown16", "malformed"}, {"16\v", "malformed"},
	}
	sub := gfoAttrSubject(gfoPathNIC, "current_link_width")
	for _, c := range cases {
		name := strconv.Quote(c.content)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			f.Write(gfoDevDir(0, gfoPathNIC)+"/current_link_width", c.content)
			a := f.OK(bin, "GFO-045 width "+name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050/GFO-060 [OD-12] "+name)
			cur, exp, ok := fr.Payload.WidthPair(gfoNIC)
			switch c.want {
			case "nodata", "malformed":
				if ok {
					t.Errorf("GFO-060 %s: pair emitted (%s/%s) for a %s current width", name, cur.IntValue(), exp.IntValue(), c.want)
				}
				if !fr.HasDiag("sysfs_attr_"+c.want, sub) || !fr.HasDiag("width_omitted", gfoNIC) {
					t.Errorf("GFO-045/GFO-060 %s: want sysfs_attr_%s(%s) and width_omitted(%s), got %s", name, c.want, sub, gfoNIC, fr.DiagString())
				}
			default:
				if !ok || cur.IntValue() != c.want || exp.IntValue() != "16" {
					t.Errorf("GFO-045/GFO-062 %s: pair ok=%v current %q expected %q, want %s/16", name, ok, cur.IntValue(), exp.IntValue(), c.want)
				}
				gfoAssertNoCode(t, fr, "width_omitted", "GFO-060 "+name)
			}
		})
	}
}

// GFO-046: classification (GPU = base 0x03 + vendor 0x10de + not VF, NIC =
// base 0x02 + not VF, bridge = 0x06/0x04) and the class observation value.
func TestGFO046_Classification(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	leaf := func(bdf, class string, extra ...string) gfoDev {
		d := gfoDev{BDF: bdf, Path: []string{gfoHB, bdf}, Attrs: map[string]string{
			"class": class, "vendor": gfoVendorNV, "current_link_width": "16\n", "max_link_width": "16\n",
		}}
		return d.with(extra...)
	}
	type want struct {
		bdf      string
		selected bool
		class    int64
	}
	devs := []struct {
		d gfoDev
		w want
	}{
		{leaf("0000:10:00.0", "0x030000\n"), want{"0000:10:00.0", true, 0x030000}},
		{leaf("0000:11:00.0", "0x038000\n"), want{"0000:11:00.0", true, 0x038000}},
		{leaf("0000:12:00.0", "0x030200\n", "vendor", "0x1002\n"), want{"0000:12:00.0", false, 0}},
		{leaf("0000:16:00.0", "0x020000\n", "vendor", "0x8086\n"), want{"0000:16:00.0", true, 0x020000}},
		{leaf("0000:17:00.0", "0x020700\n"), want{"0000:17:00.0", true, 0x020700}},
		{leaf("0000:19:00.0", "0x010802\n"), want{"0000:19:00.0", false, 0}},
		{leaf("0000:1a:00.0", "0x040300\n"), want{"0000:1a:00.0", false, 0}},
		{leaf("0000:1b:00.0", "0x060400\n"), want{"0000:1b:00.0", false, 0}},
		{leaf("0000:1c:00.0", "0x000000\n"), want{"0000:1c:00.0", false, 0}},
	}
	for _, d := range devs {
		f.Dev(0, d.d)
	}
	// Virtual functions: physfn exists (symlink, regular file, dangling
	// symlink, directory) -> never selected.
	vf := []struct{ bdf, class, kind string }{
		{"0000:13:00.0", "0x030200\n", "symlink"}, {"0000:14:00.0", "0x030200\n", "file"},
		{"0000:15:00.0", "0x030200\n", "dangling"}, {"0000:18:00.0", "0x020000\n", "dir"},
	}
	for _, v := range vf {
		f.Dev(0, leaf(v.bdf, v.class))
		p := gfoDevDir(0, []string{gfoHB, v.bdf}) + "/physfn"
		switch v.kind {
		case "symlink":
			f.Link(p, "../"+gfoRPA)
		case "file":
			f.Write(p, "")
		case "dangling":
			f.Link(p, "../0000:ee:00.0")
		case "dir":
			f.Mkdir(p)
		}
		devs = append(devs, struct {
			d gfoDev
			w want
		}{gfoDev{}, want{v.bdf, false, 0}})
	}
	a := f.OK(bin, "GFO-046")
	fr := a.Frames[0]
	gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-046")
	for _, d := range devs {
		key := gfoFnKey(d.w.bdf)
		gfoAssertAsset(t, fr.Payload, key, d.w.selected, "GFO-046 "+d.w.bdf)
		for _, k := range []string{gfoRPKey(d.w.bdf), gfoSWKey(d.w.bdf)} {
			gfoAssertAsset(t, fr.Payload, k, false, "GFO-046/GFO-047 unrelated "+d.w.bdf)
		}
		cls := fr.Payload.Obs(key, gfoSigClass)
		if d.w.selected && (len(cls) != 1 || cls[0].IntValue() != strconv.FormatInt(d.w.class, 10)) {
			t.Errorf("GFO-064: %s class observation %+v, want %d", d.w.bdf, cls, d.w.class)
		}
	}
	// Selected NIC class observation of the basic NIC (0x020000).
	if cls := fr.Payload.Obs(gfoFnKey(gfoNIC), gfoSigClass); len(cls) != 1 || cls[0].IntValue() != "131072" {
		t.Errorf("GFO-064: basic NIC class observation %+v, want 131072", cls)
	}
}

// GFO-046/GFO-048: ancestor bridge classes (0x0604xx is a bridge; 0x0600xx,
// 0x0680xx or a GPU are not -> sysfs_ancestor_not_bridge).
func TestGFO046_AncestorMustBeBridge(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	const x = "0000:81:00.0"
	cases := []struct {
		class  string
		bridge bool
	}{{"0x060401\n", true}, {"0x0604ff\n", true}, {"0x060000\n", false}, {"0x068000\n", false}, {"0x030200\n", false}, {"0x050400\n", false}}
	for _, c := range cases {
		name := strings.TrimSpace(c.class)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			anc := gfoBridge(gfoHB, "0000:00:08.0").with("class", c.class)
			f.Dev(0, anc)
			f.Dev(0, gfoGPUDev(gfoHB, "0000:00:08.0", x))
			a := f.OK(bin, "GFO-048 ancestor "+name)
			fr := a.Frames[0]
			gfoAssertAsset(t, fr.Payload, gfoFnKey(x), true, "GFO-048 "+name)
			if c.bridge {
				gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-048 "+name)
				if !fr.Payload.HasEdge(gfoFnKey(x), gfoRPKey("0000:00:08.0")) {
					t.Errorf("GFO-048 %s: edge F -> root port missing: %v", name, fr.Payload.EdgeStrings())
				}
				return
			}
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-048 "+name)
			gfoAssertCode(t, fr, "sysfs_ancestor_not_bridge", "GFO-048 "+name)
			if n := len(fr.Payload.EdgesFrom(gfoFnKey(x))); n != 0 {
				t.Errorf("GFO-048 %s: unresolved closure emitted hops from F: %v", name, fr.Payload.EdgesFrom(gfoFnKey(x)))
			}
			if !fr.HasDiag("width_omitted", x) {
				t.Errorf("GFO-060 %s: want width_omitted(%s), got %s", name, x, fr.DiagString())
			}
		})
	}
}

// GFO-047: asset kinds of the basic topology, including a root port being
// decided by the bridge's own R.
func TestGFO047_AssetKinds(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	a := gfoNewBasic(t, 1).OK(bin, "GFO-047")
	got := strings.Join(a.Frames[0].Payload.AssetKeys(), "\n")
	want := strings.Join([]string{
		gfoNodeKey(gfoNodeUID), gfoFnKey(gfoGPU), gfoFnKey(gfoNIC), gfoRPKey(gfoRPA), gfoRPKey(gfoRPB), gfoSWKey(gfoSWU), gfoSWKey(gfoSWD),
	}, "\n")
	if got != want {
		t.Errorf("GFO-047/GFO-085: assets\n%s\nwant\n%s", got, want)
	}
}

// GFO-048: closure resolution and hop emission.
func TestGFO048_ClosureAndHops(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	node := gfoNodeKey(gfoNodeUID)

	t.Run("ancestor_missing_entry", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Remove(gfoEntry(0, gfoSWU))
		a := f.OK(bin, "GFO-048 missing")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-048 missing")
		gfoAssertCode(t, fr, "sysfs_ancestor_missing", "GFO-048 missing")
		p := fr.Payload
		gfoAssertAsset(t, p, gfoFnKey(gfoGPU), true, "GFO-048 missing")
		if n := len(p.EdgesFrom(gfoFnKey(gfoGPU))); n != 0 {
			t.Errorf("GFO-048: unresolved closure emitted hops from the GPU: %v", p.EdgesFrom(gfoFnKey(gfoGPU)))
		}
		if len(p.Obs(gfoFnKey(gfoGPU), gfoSigClass)) != 1 || !fr.HasDiag("width_omitted", gfoGPU) {
			t.Errorf("GFO-064/GFO-060: GPU must keep its class observation and get width_omitted; diags %s", fr.DiagString())
		}
		if _, ok := p.Binding(gfoGPU); !ok {
			t.Errorf("GFO-050/GFO-075: valid GPU binding must still be emitted in a PARTIAL frame")
		}
		if !p.HasEdge(gfoFnKey(gfoNIC), gfoRPKey(gfoRPB)) || !p.HasEdge(gfoRPKey(gfoRPB), node) {
			t.Errorf("GFO-048: NIC chain must be unaffected: %v", p.EdgeStrings())
		}
		if _, _, ok := p.WidthPair(gfoNIC); !ok {
			t.Errorf("GFO-060: NIC width pair must be unaffected")
		}
	})

	// Closure causes (b) and (c): an ancestor whose own entry fails GFO-042 or
	// whose class is not ok leaves closure(F) unresolved (no hops from F).
	for _, c := range []struct {
		name, code, sub string
		setup           func(f *gfoFx)
	}{
		{"ancestor_link_invalid", "sysfs_link_invalid", "", func(f *gfoFx) {
			f.Remove(gfoEntry(0, gfoSWU))
			f.Write(gfoEntry(0, gfoSWU), "x")
		}},
		{"ancestor_class_malformed", "sysfs_attr_malformed", gfoAttrSubject(gfoPathSWU, "class"), func(f *gfoFx) {
			f.Write(gfoDevDir(0, gfoPathSWU)+"/class", "junk\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.setup(f)
			a := f.OK(bin, "GFO-048 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-048/GFO-050 "+c.name)
			if c.sub == "" {
				gfoAssertCode(t, fr, c.code, "GFO-042 "+c.name)
			} else if !fr.HasDiag(c.code, c.sub) {
				t.Errorf("GFO-045 %s: want %s(%s), got %s", c.name, c.code, c.sub, fr.DiagString())
			}
			if n := len(fr.Payload.EdgesFrom(gfoFnKey(gfoGPU))); n != 0 {
				t.Errorf("GFO-048 %s: unresolved closure emitted hops from the GPU: %v", c.name, fr.Payload.EdgesFrom(gfoFnKey(gfoGPU)))
			}
			gfoAssertAsset(t, fr.Payload, gfoFnKey(gfoGPU), true, "GFO-048 "+c.name)
			if !fr.HasDiag("width_omitted", gfoGPU) {
				t.Errorf("GFO-060 %s: want width_omitted(%s)", c.name, gfoGPU)
			}
		})
	}

	t.Run("ancestor_of_ancestor_missing", func(t *testing.T) {
		// F's R names only A; A's own R names C; C has no entry. The closure
		// fixpoint pulls C in, so closure(F) is unresolved.
		t.Parallel()
		f := gfoNewBasic(t, 1)
		const cBDF, aBDF, fBDF = "0000:1f:00.0", "0000:20:00.0", "0000:21:00.0"
		f.Mkdir(gfoDevDir(0, []string{gfoHB, cBDF}))
		f.Dev(0, gfoBridge(gfoHB, cBDF, aBDF))
		f.Dev(0, gfoGPUDev(gfoHB, aBDF, fBDF))
		a := f.OK(bin, "GFO-048 fixpoint missing")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-048 fixpoint missing")
		gfoAssertCode(t, fr, "sysfs_ancestor_missing", "GFO-048 fixpoint missing")
		if n := len(fr.Payload.EdgesFrom(gfoFnKey(fBDF))); n != 0 {
			t.Errorf("GFO-048: unresolved closure emitted hops from F: %v", fr.Payload.EdgesFrom(gfoFnKey(fBDF)))
		}
	})

	t.Run("ancestor_of_ancestor_resolved", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		const cBDF, aBDF, fBDF = "0000:1f:00.0", "0000:20:00.0", "0000:21:00.0"
		f.Dev(0, gfoBridge(gfoHB, cBDF))
		f.Dev(0, gfoBridge(gfoHB, cBDF, aBDF))
		f.Dev(0, gfoGPUDev(gfoHB, aBDF, fBDF))
		a := f.OK(bin, "GFO-048 fixpoint resolved")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-048/GFO-049 fixpoint resolved")
		p := fr.Payload
		for _, e := range [][2]string{
			{gfoFnKey(fBDF), gfoSWKey(aBDF)}, {gfoSWKey(aBDF), node}, {gfoSWKey(aBDF), gfoRPKey(cBDF)}, {gfoRPKey(cBDF), node},
		} {
			if !p.HasEdge(e[0], e[1]) {
				t.Errorf("GFO-048/GFO-049: hop %s -> %s missing; edges %v", e[0], e[1], p.EdgeStrings())
			}
		}
		if !fr.HasDiag("topology_contradiction", fBDF) || !fr.HasDiag("width_omitted", fBDF) {
			t.Errorf("GFO-049/GFO-060: want topology_contradiction(%s) and width_omitted(%s), got %s", fBDF, fBDF, fr.DiagString())
		}
	})

	t.Run("function_directly_under_host_bridge", func(t *testing.T) {
		// [OD-08] one F -> node edge, no root port asset, no width reads.
		t.Parallel()
		f := gfoNewBasic(t, 1)
		const x = "0000:30:00.0"
		f.Dev(0, gfoGPUDev(gfoHB, x).with("current_link_width", "", "max_link_width", ""))
		f.Fifo(gfoDevDir(0, []string{gfoHB, x}) + "/current_link_width")
		f.Fifo(gfoDevDir(0, []string{gfoHB, x}) + "/max_link_width")
		res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
		a := gfoOK(t, res, &f.M, "GFO-048 OD-08")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-048 [OD-08]")
		out := fr.Payload.EdgesFrom(gfoFnKey(x))
		if len(out) != 1 || out[0].ToKey != node {
			t.Errorf("GFO-048 [OD-08]: edges from F %v, want exactly F -> node", out)
		}
		gfoAssertAsset(t, fr.Payload, gfoRPKey(x), false, "GFO-048 [OD-08]")
		if !fr.HasDiag("width_omitted", x) {
			t.Errorf("GFO-060: want width_omitted(%s), got %s", x, fr.DiagString())
		}
		for _, d := range fr.Diagnostics {
			if strings.HasPrefix(d.Code, "sysfs_attr_") && strings.Contains(d.Subject, x) {
				t.Errorf("GFO-060: width attributes of F without a parent must not be read, got %s(%s)", d.Code, d.Subject)
			}
		}
	})

	t.Run("shared_ancestors_deduplicated_and_siblings_ignored", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Dev(0, gfoGPUDev(gfoHB, gfoRPA, gfoSWU, gfoSWD, "0000:03:00.1"))
		f.Dev(0, gfoDev{BDF: "0000:03:00.2", Path: []string{gfoHB, gfoRPA, gfoSWU, gfoSWD, "0000:03:00.2"}, Attrs: map[string]string{"class": "0x040300\n"}})
		f.Dev(0, gfoBridge(gfoHB, "0000:00:1c.0"))
		f.M.Baselines = append(f.M.Baselines, gfoBL{"0000:03:00.1", gfoSWD, "16"})
		f.Write(gfoCSV(0), gfoUUIDA+", 0000:03:00.0\n"+gfoUUIDB+", 0000:03:00.1\n")
		a := f.OK(bin, "GFO-048 shared")
		p := a.Frames[0].Payload
		gfoAssertCompleteness(t, a.Frames[0], "COMPLETE", "GFO-048 shared")
		if len(p.Edges) != 7 {
			t.Errorf("GFO-048: want 7 distinct hops (2 GPUs + shared chain + NIC chain), got %v", p.EdgeStrings())
		}
		gfoAssertAsset(t, p, gfoFnKey("0000:03:00.2"), false, "GFO-046 audio sibling")
		gfoAssertAsset(t, p, gfoRPKey("0000:00:1c.0"), false, "GFO-047 unrelated bridge")
		if !p.HasEdge(gfoFnKey("0000:03:00.1"), gfoSWKey(gfoSWD)) {
			t.Errorf("GFO-048: second GPU hop missing")
		}
	})

	t.Run("ancestor_is_selected_gpu", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		const g, n = "0000:40:00.0", "0000:41:00.0"
		f.Dev(0, gfoGPUDev(gfoHB, g))
		f.Dev(0, gfoNICDev(gfoHB, g, n))
		a := f.OK(bin, "GFO-048 gpu ancestor")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "PARTIAL", "GFO-048 gpu ancestor")
		gfoAssertCode(t, fr, "sysfs_ancestor_not_bridge", "GFO-048 gpu ancestor")
		if !fr.Payload.HasEdge(gfoFnKey(g), node) {
			t.Errorf("GFO-048: the GPU's own closure is resolved and must keep its hop: %v", fr.Payload.EdgeStrings())
		}
		if len(fr.Payload.EdgesFrom(gfoFnKey(n))) != 0 {
			t.Errorf("GFO-048: NIC below a non-bridge must not emit hops")
		}
	})
}

// GFO-049: observed contradictions (two parents, cycle) keep every hop,
// report topology_contradiction(F) and do not make the frame PARTIAL.
func TestGFO049_ContradictionsAreKept(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	node := gfoNodeKey(gfoNodeUID)
	const aBDF, bBDF, cBDF, fBDF = "0000:50:00.0", "0000:51:00.0", "0000:4f:00.0", "0000:52:00.0"

	t.Run("two_parents", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Dev(0, gfoBridge(gfoHB, aBDF))
		f.Dev(0, gfoBridge(gfoHB, cBDF))
		f.Dev(0, gfoBridge(gfoHB, cBDF, bBDF)) // B's own R: .../C/B
		f.Mkdir(gfoDevDir(0, []string{gfoHB, aBDF, bBDF}))
		f.Dev(0, gfoGPUDev(gfoHB, aBDF, bBDF, fBDF)) // F's R: .../A/B/F
		a := f.OK(bin, "GFO-049 two parents")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-049 two parents")
		p := fr.Payload
		for _, e := range [][2]string{
			{gfoFnKey(fBDF), gfoSWKey(bBDF)}, {gfoSWKey(bBDF), gfoRPKey(aBDF)}, {gfoSWKey(bBDF), gfoRPKey(cBDF)},
			{gfoRPKey(aBDF), node}, {gfoRPKey(cBDF), node},
		} {
			if !p.HasEdge(e[0], e[1]) {
				t.Errorf("GFO-049: hop %s -> %s missing (no branch may be chosen); edges %v", e[0], e[1], p.EdgeStrings())
			}
		}
		if !fr.HasDiag("topology_contradiction", fBDF) {
			t.Errorf("GFO-049: want topology_contradiction(%s), got %s", fBDF, fr.DiagString())
		}
		if _, _, ok := p.WidthPair(fBDF); ok || !fr.HasDiag("width_omitted", fBDF) {
			t.Errorf("GFO-060(a): contradicted chain must omit the width pair")
		}
		gfoAssertBasicIntact(t, p, "GFO-049 two parents")
		if _, _, ok := p.WidthPair(gfoGPU); !ok {
			t.Errorf("GFO-049: contradiction elsewhere must not affect the basic GPU pair")
		}
	})

	t.Run("cycle", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Dev(0, gfoBridge(gfoHB, bBDF))       // B's own R: pci/B
		f.Dev(0, gfoBridge(gfoHB, bBDF, aBDF)) // A's own R: pci/B/A
		f.Mkdir(gfoDevDir(0, []string{gfoHB, aBDF, bBDF}))
		f.Dev(0, gfoGPUDev(gfoHB, aBDF, bBDF, fBDF)) // F's R: pci/A/B/F
		a := f.OK(bin, "GFO-049 cycle")
		fr := a.Frames[0]
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-049 cycle")
		p := fr.Payload
		for _, e := range [][2]string{
			{gfoFnKey(fBDF), gfoRPKey(bBDF)}, {gfoRPKey(bBDF), gfoSWKey(aBDF)}, {gfoSWKey(aBDF), node},
			{gfoSWKey(aBDF), gfoRPKey(bBDF)}, {gfoRPKey(bBDF), node},
		} {
			if !p.HasEdge(e[0], e[1]) {
				t.Errorf("GFO-049: hop %s -> %s missing; edges %v", e[0], e[1], p.EdgeStrings())
			}
		}
		if !fr.HasDiag("topology_contradiction", fBDF) {
			t.Errorf("GFO-049: want topology_contradiction(%s), got %s", fBDF, fr.DiagString())
		}
	})
}

// GFO-050: a PARTIAL frame still emits every validly observed item with the
// same rules: the payload (and digest) equals the COMPLETE run's payload.
func TestGFO050_PartialKeepsValidObservations(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	clean := gfoNewBasic(t, 1).OK(bin, "GFO-050 clean")
	f := gfoNewBasic(t, 1)
	f.Write(gfoSys(0)+"/bus/pci/devices/.gitkeep", "")
	dirty := f.OK(bin, "GFO-050 partial")
	gfoAssertCompleteness(t, clean.Frames[0], "COMPLETE", "GFO-050 clean")
	gfoAssertCompleteness(t, dirty.Frames[0], "PARTIAL", "GFO-050 .gitkeep")
	if gfoRenderPayload(clean.Frames[0].Payload) != gfoRenderPayload(dirty.Frames[0].Payload) ||
		clean.Frames[0].PayloadDigest != dirty.Frames[0].PayloadDigest || clean.Frames[0].BundleRevision != dirty.Frames[0].BundleRevision {
		t.Errorf("GFO-050/GFO-086: PARTIAL frame payload/digest/revision differ from the COMPLETE run")
	}
}

// GFO-051: diagnostics are merged, sorted by (code, subject), truncated to
// 256 with total/truncated, and subjects are %XX-escaped.
func TestGFO051_Diagnostics(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)

	t.Run("merge_identical", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Dev(0, gfoNICDev(gfoHB, gfoRPB, "0000:04:00.1"))
		f.Write(gfoDevDir(0, gfoPathRPB)+"/max_link_width", "17\n")
		a := f.OK(bin, "GFO-051 merge")
		fr := a.Frames[0]
		want := []gfoDiag{
			{"sysfs_attr_malformed", gfoAttrSubject(gfoPathRPB, "max_link_width")},
			{"width_omitted", gfoNIC}, {"width_omitted", "0000:04:00.1"},
		}
		if fmt.Sprint(fr.Diagnostics) != fmt.Sprint(want) || fr.DiagnosticsTotal != 3 || fr.DiagnosticsTruncated {
			t.Errorf("GFO-051/GFO-062: diagnostics %v total %d truncated %v, want %v total 3",
				fr.Diagnostics, fr.DiagnosticsTotal, fr.DiagnosticsTruncated, want)
		}
		gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050 width failure [OD-12]")
	})

	counts := []struct {
		n         int
		truncated bool
	}{{255, false}, {256, false}, {257, true}, {300, true}}
	for _, c := range counts {
		t.Run(fmt.Sprintf("names_%d", c.n), func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			var names []string
			for i := 0; i < c.n; i++ {
				name := fmt.Sprintf("bad-%03d", i)
				names = append(names, name)
				f.Write(gfoSys(0)+"/bus/pci/devices/"+name, "")
			}
			a := f.OK(bin, "GFO-051 truncation")
			fr := a.Frames[0]
			wantLen := c.n
			if wantLen > 256 {
				wantLen = 256
			}
			if fr.DiagnosticsTotal != c.n || fr.DiagnosticsTruncated != c.truncated || len(fr.Diagnostics) != wantLen {
				t.Fatalf("GFO-051: n=%d: len %d total %d truncated %v; want len %d total %d truncated %v",
					c.n, len(fr.Diagnostics), fr.DiagnosticsTotal, fr.DiagnosticsTruncated, wantLen, c.n, c.truncated)
			}
			for i, d := range fr.Diagnostics {
				if d.Code != "sysfs_entry_name_invalid" || !strings.HasSuffix(d.Subject, names[i]) {
					t.Errorf("GFO-051: kept diagnostic %d = %s(%s), want the %d-th smallest subject ending in %s", i, d.Code, d.Subject, i, names[i])
					break
				}
			}
		})
	}

	t.Run("subject_escaping", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		names := map[string][]string{
			"a b":     {"a%20b"},
			"c%d":     {"c%25d"},
			"e\tf":    {"e%09f"},
			"g\x7fh":  {"g%7Fh"},
			"i\u00e9": {"i%C3%A9", "ie%CC%81"}, // NFC, or NFD if the file system normalizes
		}
		for n := range names {
			f.Write(gfoSys(0)+"/bus/pci/devices/"+n, "")
		}
		a := f.OK(bin, "GFO-051 escaping")
		fr := a.Frames[0]
		for n, forms := range names {
			found := false
			for _, d := range fr.Diagnostics {
				for _, form := range forms {
					if d.Code == "sysfs_entry_name_invalid" && strings.Contains(d.Subject, form) {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("GFO-051: no sysfs_entry_name_invalid subject containing %v for name %q; got %s", forms, n, fr.DiagString())
			}
		}
	})

	t.Run("file_content_never_in_subject", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Write(gfoDevDir(0, gfoPathNIC)+"/class", "CANARY-CLASS-9d1e\n")
		res := f.Run(bin)
		gfoOK(t, res, &f.M, "GFO-051 content")
		if bytes.Contains(res.Stdout, []byte("CANARY-CLASS-9d1e")) {
			t.Errorf("GFO-051: attribute file content appears in the artifact")
		}
	})
}
