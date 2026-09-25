package tests_test

import (
	"bytes"
	"testing"
	"time"
)

// gfoPairCheck asserts F's width pair (current c, expected e, provenance,
// peer, peer kind, root) or its absence with width_omitted(F).
type gfoPairWant struct {
	bdf                    string
	present                bool
	cur, exp, prov         string
	peer, peerKind, rootBD string
}

func gfoCheckPair(t *testing.T, fr gfoFrame, w gfoPairWant, clause string) {
	t.Helper()
	cur, exp, ok := fr.Payload.WidthPair(w.bdf)
	if !w.present {
		if ok {
			t.Errorf("%s: %s has a width pair %s/%s (%s), want none", clause, w.bdf, cur.IntValue(), exp.IntValue(), exp.Dim("pcie.expected.provenance"))
		}
		if !fr.HasDiag("width_omitted", w.bdf) {
			t.Errorf("%s: want width_omitted(%s), got %s", clause, w.bdf, fr.DiagString())
		}
		return
	}
	if !ok {
		t.Errorf("%s: %s has no width pair; diagnostics %s", clause, w.bdf, fr.DiagString())
		return
	}
	if cur.IntValue() != w.cur || exp.IntValue() != w.exp || exp.Dim("pcie.expected.provenance") != w.prov {
		t.Errorf("%s: %s pair %s/%s (%s), want %s/%s (%s)", clause, w.bdf, cur.IntValue(), exp.IntValue(), exp.Dim("pcie.expected.provenance"), w.cur, w.exp, w.prov)
	}
	if w.peer != "" && (exp.Dim("pcie.peer.canonical") != "pci-bdf:"+w.peer || exp.Dim("pcie.peer.kind") != w.peerKind ||
		exp.Dim("pcie.root.canonical") != "pci-bdf:"+w.rootBD) {
		t.Errorf("%s: %s dimensions %v, want peer %s (%s) root %s", clause, w.bdf, exp.Dims, w.peer, w.peerKind, w.rootBD)
	}
	if fr.HasDiag("width_omitted", w.bdf) {
		t.Errorf("%s: %s has a pair and width_omitted", clause, w.bdf)
	}
}

// GFO-060: a pair only when the chain is unique to a root port with a parent,
// current is ok and an expected value exists; GPU and NIC alike. Width
// observation failures never change completeness [OD-12 C].
func TestGFO060_PairConditions(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	gpuCur := gfoDevDir(0, gfoPathGPU) + "/current_link_width"
	nicCur := gfoDevDir(0, gfoPathNIC) + "/current_link_width"
	cases := []struct {
		name  string
		setup func(f *gfoFx)
		gpu   gfoPairWant
		nic   gfoPairWant
		code  string // extra diagnostic expected (subject is the changed attribute)
		sub   string
	}{
		{"basic", func(f *gfoFx) {},
			gfoPairWant{gfoGPU, true, "16", "16", gfoProvOp, gfoSWD, "PCIeSwitch", gfoRPA},
			gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, gfoRPB, "PCIeRootPort", gfoRPB}, "", ""},
		{"gpu_current_missing", func(f *gfoFx) { f.Remove(gpuCur) },
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"sysfs_attr_missing", gfoAttrSubject(gfoPathGPU, "current_link_width")},
		{"gpu_current_fifo", func(f *gfoFx) { f.Remove(gpuCur); f.Fifo(gpuCur) },
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"sysfs_attr_unreadable", gfoAttrSubject(gfoPathGPU, "current_link_width")},
		{"gpu_current_directory", func(f *gfoFx) { f.Remove(gpuCur); f.Mkdir(gpuCur) },
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"sysfs_attr_unreadable", gfoAttrSubject(gfoPathGPU, "current_link_width")},
		{"gpu_current_malformed", func(f *gfoFx) { f.Write(gpuCur, "x16\n") },
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"sysfs_attr_malformed", gfoAttrSubject(gfoPathGPU, "current_link_width")},
		{"gpu_current_nodata", func(f *gfoFx) { f.Write(gpuCur, "Unknown\n") },
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"sysfs_attr_nodata", gfoAttrSubject(gfoPathGPU, "current_link_width")},
		{"gpu_current_4097_bytes", func(f *gfoFx) { f.Write(gpuCur, "16"+string(bytes.Repeat([]byte(" "), 4095))) },
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"sysfs_attr_malformed", gfoAttrSubject(gfoPathGPU, "current_link_width")},
		{"gpu_current_4096_bytes", func(f *gfoFx) { f.Write(gpuCur, "8"+string(bytes.Repeat([]byte(" "), 4095))) },
			gfoPairWant{gfoGPU, true, "8", "16", gfoProvOp, gfoSWD, "PCIeSwitch", gfoRPA}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			"", ""},
		{"nic_current_missing", func(f *gfoFx) { f.Remove(nicCur) },
			gfoPairWant{gfoGPU, true, "16", "16", gfoProvOp, "", "", ""}, gfoPairWant{bdf: gfoNIC},
			"sysfs_attr_missing", gfoAttrSubject(gfoPathNIC, "current_link_width")},
		{"nic_current_zero", func(f *gfoFx) { f.Write(nicCur, "0\n") },
			gfoPairWant{gfoGPU, true, "16", "16", gfoProvOp, "", "", ""}, gfoPairWant{bdf: gfoNIC},
			"sysfs_attr_nodata", gfoAttrSubject(gfoPathNIC, "current_link_width")},
		{"degraded_current_kept", func(f *gfoFx) { f.Write(gpuCur, "4\n"); f.Write(nicCur, "1\n") },
			gfoPairWant{gfoGPU, true, "4", "16", gfoProvOp, "", "", ""}, gfoPairWant{gfoNIC, true, "1", "16", gfoProvAdj, "", "", ""}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.setup(f)
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			a := gfoOK(t, res, &f.M, "GFO-060 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050/GFO-060 [OD-12] "+c.name)
			gfoCheckPair(t, fr, c.gpu, "GFO-060 "+c.name+" GPU")
			gfoCheckPair(t, fr, c.nic, "GFO-060 "+c.name+" NIC")
			if c.code != "" && !fr.HasDiag(c.code, c.sub) {
				t.Errorf("GFO-060 %s: want %s(%s), got %s", c.name, c.code, c.sub, fr.DiagString())
			}
		})
	}
}

// GFO-060: the width read set is exactly F current and, without a baseline,
// F and P max; everything else (bridge current, F max with a baseline, far
// ancestors) is never read: FIFOs there change nothing.
func TestGFO060_WidthReadScope(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	clean := gfoNewBasic(t, 1)
	want := clean.Run(bin)
	gfoOK(t, want, &clean.M, "GFO-060 clean")

	f := gfoNewBasic(t, 1)
	for _, p := range []string{
		gfoDevDir(0, gfoPathGPU) + "/max_link_width", // baseline present: F max not read
		gfoDevDir(0, gfoPathSWD) + "/max_link_width", // baseline present: P max not read
		gfoDevDir(0, gfoPathSWD) + "/current_link_width",
		gfoDevDir(0, gfoPathSWU) + "/current_link_width",
		gfoDevDir(0, gfoPathSWU) + "/max_link_width",
		gfoDevDir(0, gfoPathRPA) + "/current_link_width",
		gfoDevDir(0, gfoPathRPA) + "/max_link_width",
		gfoDevDir(0, gfoPathRPB) + "/current_link_width", // P current is never read
	} {
		f.Remove(p)
		f.Fifo(p)
	}
	res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
	gfoOK(t, res, &f.M, "GFO-060 read scope")
	if !bytes.Equal(res.Stdout, want.Stdout) {
		t.Errorf("GFO-060/GFO-044: width attributes outside the read set were read (stdout changed)")
	}
}

// GFO-061: an operator baseline for F wins (no adjacent value), a peer that
// is not P gives baseline_peer_mismatch without fallback, and a baseline
// function that is not selected gives baseline_unmatched.
func TestGFO061_OperatorBaseline(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	cases := []struct {
		name      string
		baselines []gfoBL
		setup     func(f *gfoFx)
		gpu, nic  gfoPairWant
		codes     map[string]string // code -> subject ("" = any subject)
		noDiags   bool
	}{
		{"baseline_wins_over_adjacent", []gfoBL{{gfoGPU, gfoSWD, "8"}}, func(f *gfoFx) {},
			gfoPairWant{gfoGPU, true, "16", "8", gfoProvOp, gfoSWD, "PCIeSwitch", gfoRPA},
			gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""}, nil, true},
		{"nic_baseline_root_port_peer", []gfoBL{{gfoGPU, gfoSWD, "16"}, {gfoNIC, gfoRPB, "32"}}, func(f *gfoFx) {
			f.Write(gfoDevDir(0, gfoPathNIC)+"/max_link_width", "garbage")
			f.Write(gfoDevDir(0, gfoPathRPB)+"/max_link_width", "garbage")
		},
			gfoPairWant{gfoGPU, true, "16", "16", gfoProvOp, "", "", ""},
			gfoPairWant{gfoNIC, true, "16", "32", gfoProvOp, gfoRPB, "PCIeRootPort", gfoRPB}, nil, true},
		{"peer_mismatch_no_fallback", []gfoBL{{gfoGPU, gfoSWU, "16"}}, func(f *gfoFx) {},
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			map[string]string{"baseline_peer_mismatch": ""}, false},
		{"peer_is_root_port_not_parent", []gfoBL{{gfoGPU, gfoRPA, "16"}}, func(f *gfoFx) {},
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			map[string]string{"baseline_peer_mismatch": ""}, false},
		{"unmatched_absent_function", []gfoBL{{gfoGPU, gfoSWD, "16"}, {"0000:99:00.0", gfoSWD, "16"}}, func(f *gfoFx) {},
			gfoPairWant{gfoGPU, true, "16", "16", gfoProvOp, "", "", ""}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			map[string]string{"baseline_unmatched": "0000:99:00.0"}, false},
		{"unmatched_bridge_function", []gfoBL{{gfoGPU, gfoSWD, "16"}, {gfoSWD, gfoSWU, "16"}}, func(f *gfoFx) {},
			gfoPairWant{gfoGPU, true, "16", "16", gfoProvOp, "", "", ""}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			map[string]string{"baseline_unmatched": gfoSWD}, false},
		{"unmatched_virtual_function", []gfoBL{{gfoGPU, gfoSWD, "16"}}, func(f *gfoFx) {
			f.Link(gfoDevDir(0, gfoPathGPU)+"/physfn", "../0000:02:08.0")
		},
			gfoPairWant{bdf: "0000:ff:1f.7"}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""},
			map[string]string{"baseline_unmatched": gfoGPU}, false},
		{"baseline_with_nodata_current", []gfoBL{{gfoGPU, gfoSWD, "16"}}, func(f *gfoFx) {
			f.Write(gfoDevDir(0, gfoPathGPU)+"/current_link_width", "")
		},
			gfoPairWant{bdf: gfoGPU}, gfoPairWant{gfoNIC, true, "16", "16", gfoProvAdj, "", "", ""}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			f.M.Baselines = c.baselines
			c.setup(f)
			a := f.OK(bin, "GFO-061 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050/GFO-061 "+c.name)
			if c.gpu.bdf == "0000:ff:1f.7" { // GPU is a VF: not selected at all
				gfoAssertAsset(t, fr.Payload, gfoFnKey(gfoGPU), false, "GFO-046 VF")
			} else {
				gfoCheckPair(t, fr, c.gpu, "GFO-061 "+c.name+" GPU")
			}
			gfoCheckPair(t, fr, c.nic, "GFO-061 "+c.name+" NIC")
			for code, sub := range c.codes {
				if sub == "" {
					gfoAssertCode(t, fr, code, "GFO-061 "+c.name)
				} else if !fr.HasDiag(code, sub) {
					t.Errorf("GFO-061 %s: want %s(%s), got %s", c.name, code, sub, fr.DiagString())
				}
			}
			if c.noDiags && fr.DiagnosticsTotal != 0 {
				t.Errorf("GFO-061/GFO-060 %s: with baselines covering F no max_link_width is read; got %s", c.name, fr.DiagString())
			}
		})
	}
}

// GFO-062 [OD-04 A]: without a baseline e = min(F max, P max) when both are
// ok; otherwise there is no expected value.
func TestGFO062_AdjacentCapabilityMinimum(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	nicMax := gfoDevDir(0, gfoPathNIC) + "/max_link_width"
	rpbMax := gfoDevDir(0, gfoPathRPB) + "/max_link_width"
	cases := []struct {
		name       string
		fMax, pMax string // "" = remove the file
		cur        string
		exp        string // "" = no pair
		code, sub  string
	}{
		{"f16_p8", "16", "8", "16", "8", "", ""},
		{"f4_p16", "4", "16", "16", "4", "", ""},
		{"f12_p8", "12", "8", "16", "8", "", ""},
		{"f32_p32", "32", "32", "16", "32", "", ""},
		{"f1_p1", "1", "1", "16", "1", "", ""},
		{"current_above_expected_not_corrected", "8", "8", "32", "8", "", ""},
		{"f_max_unknown", "Unknown", "16", "16", "", "sysfs_attr_nodata", gfoAttrSubject(gfoPathNIC, "max_link_width")},
		{"p_max_zero", "16", "0", "16", "", "sysfs_attr_nodata", gfoAttrSubject(gfoPathRPB, "max_link_width")},
		{"f_max_missing", "", "16", "16", "", "sysfs_attr_missing", gfoAttrSubject(gfoPathNIC, "max_link_width")},
		{"p_max_missing", "16", "", "16", "", "sysfs_attr_missing", gfoAttrSubject(gfoPathRPB, "max_link_width")},
		{"p_max_malformed", "16", "10", "16", "", "sysfs_attr_malformed", gfoAttrSubject(gfoPathRPB, "max_link_width")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			for p, v := range map[string]string{nicMax: c.fMax, rpbMax: c.pMax} {
				if v == "" {
					f.Remove(p)
				} else {
					f.Write(p, v+"\n")
				}
			}
			f.Write(gfoDevDir(0, gfoPathNIC)+"/current_link_width", c.cur+"\n")
			a := f.OK(bin, "GFO-062 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050/GFO-062 "+c.name)
			if c.exp == "" {
				gfoCheckPair(t, fr, gfoPairWant{bdf: gfoNIC}, "GFO-062 "+c.name)
				if !fr.HasDiag(c.code, c.sub) {
					t.Errorf("GFO-062 %s: want %s(%s), got %s", c.name, c.code, c.sub, fr.DiagString())
				}
				return
			}
			gfoCheckPair(t, fr, gfoPairWant{gfoNIC, true, c.cur, c.exp, gfoProvAdj, gfoRPB, "PCIeRootPort", gfoRPB}, "GFO-062 "+c.name)
		})
	}

	t.Run("gpu_under_switch_without_baseline", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.M.Baselines = nil
		f.Write(gfoDevDir(0, gfoPathGPU)+"/max_link_width", "8\n")
		a := f.OK(bin, "GFO-062 GPU adjacent")
		gfoCheckPair(t, a.Frames[0], gfoPairWant{gfoGPU, true, "16", "8", gfoProvAdj, gfoSWD, "PCIeSwitch", gfoRPA}, "GFO-062 GPU")
	})
}

// GFO-063: expected is never learned from current or previous frames.
func TestGFO063_NoLearning(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 4)
	gpuCur := []string{"16", "8", "4", "32"}
	nicCur := []string{"16", "4", "32", "8"}
	for i := 0; i < 4; i++ {
		f.Write(gfoDevDir(i, gfoPathGPU)+"/current_link_width", gpuCur[i]+"\n")
		f.Write(gfoDevDir(i, gfoPathNIC)+"/current_link_width", nicCur[i]+"\n")
	}
	// Frame 3: NIC max becomes nodata -> no expected, not carried forward.
	f.Write(gfoDevDir(3, gfoPathNIC)+"/max_link_width", "Unknown\n")
	a := f.OK(bin, "GFO-063")
	for i := 0; i < 4; i++ {
		gfoCheckPair(t, a.Frames[i], gfoPairWant{gfoGPU, true, gpuCur[i], "16", gfoProvOp, gfoSWD, "PCIeSwitch", gfoRPA}, "GFO-063 GPU frame")
		if i < 3 {
			gfoCheckPair(t, a.Frames[i], gfoPairWant{gfoNIC, true, nicCur[i], "16", gfoProvAdj, gfoRPB, "PCIeRootPort", gfoRPB}, "GFO-063 NIC frame")
		} else {
			gfoCheckPair(t, a.Frames[i], gfoPairWant{bdf: gfoNIC}, "GFO-063 NIC frame 3 (no carry-forward)")
		}
	}
}

// GFO-064: every field of the class and width observations of the basic
// fixture.
func TestGFO064_ObservationFields(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	a := gfoNewBasic(t, 1).OK(bin, "GFO-064")
	fr := a.Frames[0]
	obsAt, expAt := gfoT0, "2026-09-24T00:05:00Z"
	type want struct {
		subject, signal, srcName, value, unit string
		dims                                  []gfoKV
	}
	gpuDims := []gfoKV{{"pcie.expected.provenance", gfoProvOp}, {"pcie.peer.canonical", "pci-bdf:" + gfoSWD},
		{"pcie.peer.kind", "PCIeSwitch"}, {"pcie.root.canonical", "pci-bdf:" + gfoRPA}}
	nicDims := []gfoKV{{"pcie.expected.provenance", gfoProvAdj}, {"pcie.peer.canonical", "pci-bdf:" + gfoRPB},
		{"pcie.peer.kind", "PCIeRootPort"}, {"pcie.root.canonical", "pci-bdf:" + gfoRPB}}
	wants := []want{
		{gfoGPU, gfoSigClass, gfoParentSrc, "197120", "pci_class", []gfoKV{}},
		{gfoGPU, gfoSigCurrent, gfoWidthSrc, "16", "lanes", gpuDims},
		{gfoGPU, gfoSigExpect, gfoWidthSrc, "16", "lanes", gpuDims},
		{gfoNIC, gfoSigClass, gfoParentSrc, "131072", "pci_class", []gfoKV{}},
		{gfoNIC, gfoSigCurrent, gfoWidthSrc, "16", "lanes", nicDims},
		{gfoNIC, gfoSigExpect, gfoWidthSrc, "16", "lanes", nicDims},
	}
	if len(fr.Payload.Observations) != len(wants) {
		t.Fatalf("GFO-064: %d observations, want %d", len(fr.Payload.Observations), len(wants))
	}
	for i, w := range wants {
		o := fr.Payload.Observations[i]
		ok := o.ID == gfoObservationID("7", "0", gfoFnKey(w.subject), w.signal) &&
			o.SourceType == "agent" && o.SourceName == w.srcName &&
			o.Subject.Key() == gfoFnKey(w.subject) && len(o.Subject.Aliases) == 0 &&
			o.Signal == w.signal && o.ValueKind == "int" && o.IntValue() == w.value && o.Unit == w.unit &&
			gfoCompareDims(o.Dims, w.dims) == 0 &&
			o.ObservedAt == obsAt && o.ReceivedAt == obsAt && o.ExpiresAt == expAt &&
			o.Sequence == "0" && o.Quality == "Good" && o.RawDigest == ""
		if !ok {
			t.Errorf("GFO-064/GFO-085: observation %d = %+v, want %+v", i, o, w)
		}
	}
}
