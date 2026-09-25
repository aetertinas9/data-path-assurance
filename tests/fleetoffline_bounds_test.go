package tests_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gfoFastNICs creates n NIC functions (class only, optional widths) under
// devices/<parent...> of one frame using plain file operations (bulk
// fixtures for GFO-095). BDFs are 0000:<0x10+i/256>:<(i/8)%32>.<i%8>.
func gfoFastNICs(t *testing.T, f *gfoFx, frame, n int, parent []string, widths bool) {
	t.Helper()
	sys := f.P(gfoSys(frame))
	devs := filepath.Join(sys, "bus", "pci", "devices")
	if err := os.MkdirAll(devs, 0o755); err != nil {
		t.Fatal(err)
	}
	base := "devices/" + strings.Join(parent, "/")
	for i := 0; i < n; i++ {
		bdf := fmt.Sprintf("0000:%02x:%02x.%d", 0x10+i/256, (i/8)%32, i%8)
		dir := filepath.Join(sys, filepath.FromSlash(base), bdf)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{"class": gfoClassNIC}
		if widths {
			files["current_link_width"] = "16\n"
			files["max_link_width"] = "16\n"
		}
		for k, v := range files {
			if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink("../../../"+base+"/"+bdf, filepath.Join(devs, bdf)); err != nil {
			t.Fatal(err)
		}
	}
}

// GFO-041/GFO-095: 8192 enumerated names are accepted, the 8193rd is exit 4
// (names are counted whether or not they are canonical).
func TestGFO095_EnumeratedNameBound(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	t.Run("8192_invalid_names", func(t *testing.T) {
		t.Parallel()
		f := gfoNewFx(t, 1)
		gfoFillNames(t, f, 0, 8192)
		a := f.OK(bin, "GFO-095 8192 names")
		fr := a.Frames[0]
		// 8192 sysfs_entry_name_invalid + nvidia_output_absent (no CSV).
		if fr.DiagnosticsTotal != 8193 || !fr.DiagnosticsTruncated || len(fr.Diagnostics) != 256 {
			t.Errorf("GFO-051/GFO-095: total %d truncated %v len %d, want 8193/true/256", fr.DiagnosticsTotal, fr.DiagnosticsTruncated, len(fr.Diagnostics))
		}
	})
	t.Run("8193_invalid_names", func(t *testing.T) {
		t.Parallel()
		f := gfoNewFx(t, 1)
		gfoFillNames(t, f, 0, 8193)
		f.Fail(bin, 4, "GFO-041/GFO-095 8193 names")
	})
	t.Run("8186_names_plus_basic_entries", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		gfoFillNames(t, f, 0, 8186) // + 6 basic entries = 8192
		a := f.OK(bin, "GFO-095 8192 with basic")
		gfoAssertBasicIntact(t, a.Frames[0].Payload, "GFO-095 8192 with basic")
		if a.Frames[0].DiagnosticsTotal != 8186 {
			t.Errorf("GFO-051: diagnosticsTotal %d, want 8186", a.Frames[0].DiagnosticsTotal)
		}
	})
	t.Run("8187_names_plus_basic_entries", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		gfoFillNames(t, f, 0, 8187) // + 6 basic entries = 8193
		f.Fail(bin, 4, "GFO-095 8193 with basic")
	})
}

// GFO-095: at most 4096 assets per frame; 4097 is exit 4, never truncation
// or PARTIAL. N NICs directly under the host bridge give N+1 assets.
func TestGFO095_AssetBound(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	t.Run("4096_assets", func(t *testing.T) {
		t.Parallel()
		f := gfoNewFx(t, 1)
		gfoFastNICs(t, f, 0, 4095, []string{gfoHB}, false)
		a := f.OK(bin, "GFO-095 4096 assets")
		if n := len(a.Frames[0].Payload.Assets); n != 4096 {
			t.Errorf("GFO-095: %d assets, want 4096", n)
		}
		gfoAssertCompleteness(t, a.Frames[0], "COMPLETE", "GFO-095 4096 assets")
	})
	t.Run("4097_assets", func(t *testing.T) {
		t.Parallel()
		f := gfoNewFx(t, 1)
		gfoFastNICs(t, f, 0, 4096, []string{gfoHB}, false)
		f.Fail(bin, 4, "GFO-095 4097 assets")
	})
}

// gfoEdgePlan plans NIC chains over K bridges whose consecutive pairs are
// globally distinct (pair (x, x+s mod K) fixes s), so every chain adds new
// hops. It returns chains of bridge indices whose resolved hop set has
// exactly target edges (GFO-048: F->last bridge, bridge->previous bridge,
// first bridge->node, and every used bridge's own R hop bridge->node).
func gfoEdgePlan(t *testing.T, k, target int) [][]int {
	t.Helper()
	edges := map[string]bool{}
	var plan [][]int
	delta := func(c []int, n int) int {
		seen := map[string]bool{}
		d := 0
		try := func(key string) {
			if !edges[key] && !seen[key] {
				seen[key] = true
				d++
			}
		}
		try(fmt.Sprintf("F%d>A%d", n, c[len(c)-1]))
		for j := 1; j < len(c); j++ {
			try(fmt.Sprintf("A%d>A%d", c[j], c[j-1]))
		}
		for _, b := range c {
			try(fmt.Sprintf("A%d>N", b))
		}
		return d
	}
	add := func(c []int) {
		n := len(plan)
		edges[fmt.Sprintf("F%d>A%d", n, c[len(c)-1])] = true
		for j := 1; j < len(c); j++ {
			edges[fmt.Sprintf("A%d>A%d", c[j], c[j-1])] = true
		}
		for _, b := range c {
			edges[fmt.Sprintf("A%d>N", b)] = true
		}
		plan = append(plan, append([]int(nil), c...))
	}
	for s := 1; s < k && len(edges) < target; s++ {
		cycle := make([]int, k)
		for j := range cycle {
			cycle[j] = (j * s) % k
		}
		for start := 0; start+16 <= k && len(edges) < target; start += 16 {
			c := cycle[start : start+16]
			if len(edges)+delta(c, len(plan)) <= target {
				add(c)
				continue
			}
			for l := 15; l >= 1; l-- {
				if len(edges)+delta(c[:l], len(plan)) == target {
					add(c[:l])
					break
				}
			}
		}
	}
	if len(edges) != target {
		t.Fatalf("test bug: planned %d edges, want %d", len(edges), target)
	}
	return plan
}

// gfoEdgeFixture builds K bridges directly under the host bridge and one NIC
// per planned chain below the chain's bridges (resolved but contradictory
// closures, GFO-048/049).
func gfoEdgeFixture(t *testing.T, target int) *gfoFx {
	t.Helper()
	const k = 101
	bridge := func(i int) string { return fmt.Sprintf("0000:%02x:00.0", 0x01+i) }
	f := gfoNewFx(t, 1)
	for i := 0; i < k; i++ {
		f.Dev(0, gfoDev{BDF: bridge(i), Path: []string{gfoHB, bridge(i)}, Attrs: map[string]string{"class": gfoClassBr}})
	}
	for n, c := range gfoEdgePlan(t, k, target) {
		path := []string{gfoHB}
		for _, b := range c {
			path = append(path, bridge(b))
		}
		leaf := fmt.Sprintf("0000:%02x:%02x.%d", 0x80+n/256, (n/8)%32, n%8)
		f.Dev(0, gfoDev{BDF: leaf, Path: append(path, leaf), Attrs: map[string]string{"class": gfoClassNIC}})
	}
	return f
}

// GFO-095: at most 8192 edges (and edgeEvidence) per frame; 8193 is exit 4.
// Contradictory chains (GFO-049 keeps every hop) reach the edge bound with
// only ~600 assets.
func TestGFO095_EdgeBound(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	t.Run("8192_edges", func(t *testing.T) {
		t.Parallel()
		f := gfoEdgeFixture(t, 8192)
		a := f.OK(bin, "GFO-095 8192 edges")
		p := a.Frames[0].Payload
		if len(p.Edges) != 8192 || len(p.EdgeEvidence) != 8192 {
			t.Errorf("GFO-048/GFO-095: %d edges / %d evidence, want exactly 8192 hops", len(p.Edges), len(p.EdgeEvidence))
		}
		gfoAssertCompleteness(t, a.Frames[0], "COMPLETE", "GFO-049/GFO-095 contradictions are not PARTIAL")
	})
	t.Run("8193_edges", func(t *testing.T) {
		t.Parallel()
		f := gfoEdgeFixture(t, 8193)
		f.Fail(bin, 4, "GFO-095 8193 edges")
	})
}

// GFO-095: total stdout above 64 MiB is exit 4 with nothing written. Nine
// frames of 4094 NICs (widths ok) are ~73 MiB by the GFO-083 encoding while
// every per-frame bound holds.
func TestGFO095_StdoutBound(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	const frames, nics = 9, 4094
	// Self-check of the premise with the independent encoder: one frame's
	// payload alone is 4094 * 2077 bytes + fixed part.
	if frames*nics*2077 <= 64<<20 {
		t.Fatalf("test bug: fixture would not exceed 64 MiB")
	}
	f := gfoNewFx(t, frames)
	for i := 0; i < frames; i++ {
		f.Dev(i, gfoBridge(gfoHB, "0000:00:01.0"))
		gfoFastNICs(t, f, i, nics, []string{gfoHB, "0000:00:01.0"}, true)
	}
	f.Fail(bin, 4, "GFO-095 stdout > 64 MiB")
}

// GFO-096: a bound exceeded in any frame writes nothing at all (not even the
// valid earlier frames).
func TestGFO096_AtomicOutput(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	t.Run("names_bound_in_last_frame", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 3)
		gfoFillNames(t, f, 2, 8193)
		f.Fail(bin, 4, "GFO-096 frame 2 names")
	})
	t.Run("asset_bound_in_second_frame", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 2)
		f.Remove(gfoSys(1))
		f.Mkdir(gfoSys(1))
		gfoFastNICs(t, f, 1, 4096, []string{gfoHB}, false)
		f.Fail(bin, 4, "GFO-096 frame 1 assets")
	})
	t.Run("invalid_later_frame_dir", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 3)
		f.Remove("frames/2/sys")
		f.Fail(bin, 3, "GFO-096 frame 2 missing")
	})
}
