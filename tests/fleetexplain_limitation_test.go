package tests_test

// Limitations and collector diagnostics (GFX-080..085).

import (
	"testing"
)

// gfxWithDiags returns a copy of a whose frame i carries the given
// diagnostics (diagnostics are outside the digest, GFO-086).
func gfxWithDiags(t *testing.T, a *gfoArtifact, i int, diags ...gfoDiag) *gfoArtifact {
	t.Helper()
	c := gfxClone(t, a)
	c.Frames[i].Diagnostics = append([]gfoDiag{}, diags...)
	c.Frames[i].DiagnosticsTotal = len(diags)
	c.Frames[i].DiagnosticsTruncated = false
	c.Raw = gfoRenderArtifact(c)
	return c
}

// TestGFX080_LimitationSet: codes appear only under their conditions, once,
// in (code, subject) order; F(d)-subject codes only for bound devices.
func TestGFX080_LimitationSet(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	// Exact set for S-BASIC is asserted in TestGFX102_SBasic. Here: the
	// node form of S-BASIC and the per-device subjects.
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	n := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("node", gfoNodeName, "GFX-080 S-BASIC node")
	gfxWant(t, "GFX-080 S-BASIC node", "limitations", gfxJoin(n.Lims()),
		gfxJoin([]string{"allocation_unavailable", "offline", "offline_trust_not_live offline:lab-a-basic", "traffic_path_unverified"}))

	// A host-bridge function that is not bound carries no F(d) limitation.
	f := gfxSRootless(t)
	gfxWithoutSources(f, "NVIDIAUUIDBinding")
	u := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-080 unbound rootless")
	gfxWantLims(t, "GFX-080 unbound rootless", u, nil, []string{"root_port_absent", "width_pair_absent", "path_contradiction", "path_untrusted"})

	// node_name_differs for every output device whose nodeRef.name differs;
	// for a gpu request only the target device counts.
	fl := gfxTwoDevices()
	fl.Devices[1].NodeName = "old-name"
	c := gfxNewCase(t, bins, a, fl)
	g := c.Check("gpu", gfxDevName, "GFX-080 gpu with a renamed peer device")
	gfxWantLims(t, "GFX-080 gpu", g, nil, []string{"node_name_differs"})
	nn := c.Check("node", gfoNodeName, "GFX-080 node with a renamed device")
	gfxWantLims(t, "GFX-080 node", nn, []string{"node_name_differs gpu-node-1-spare"}, []string{"node_name_differs " + gfxDevName})
	s := c.Check("gpu", "gpu-node-1-spare", "GFX-080 renamed gpu")
	gfxWantLims(t, "GFX-080 renamed gpu", s, []string{"node_name_differs gpu-node-1-spare"}, nil)
}

// TestGFX081_CollectorDiagnostics: every diagnostic of R, unchanged, and none
// from other frames.
func TestGFX081_CollectorDiagnostics(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	r := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	r = gfxWithDiags(t, r, 0, gfoDiag{"baseline_unmatched", "0000:0e:00.0"})
	r = gfxWithDiags(t, r, 2, gfoDiag{"sysfs_ancestor_missing", "devices/pci0000:00/0000:00:1f.0/0000:1f:00.0"},
		gfoDiag{"sysfs_layout_unsupported", "devices/pci0000:00/pci0001:00/0001:00:01.0"})
	c := gfxNewCase(t, bins, r, gfxDefaultFleet())
	for _, kind := range []string{"gpu", "node"} {
		name := gfxDevName
		if kind == "node" {
			name = gfoNodeName
		}
		e := c.Check(kind, name, "GFX-081 "+kind)
		gfxWantLims(t, "GFX-081/V2-01 "+kind, e, []string{
			"collector:sysfs_ancestor_missing devices/pci0000:00/0000:00:1f.0/0000:1f:00.0",
			"collector:sysfs_layout_unsupported devices/pci0000:00/pci0001:00/0001:00:01.0",
			"diagnostics_outside_digest"}, []string{"collector:baseline_unmatched", "diagnostics_truncated"})
	}
}

// TestGFX082_RootPortAbsent: a bound function whose only outgoing edge goes
// to the node; not a contradiction.
func TestGFX082_RootPortAbsent(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSRootless(t)), gfxDefaultFleet())
	e := c.Check("gpu", gfxDevName, "GFX-082")
	gfxWantLims(t, "GFX-082", e, []string{"root_port_absent " + gfxKGPU}, []string{"path_contradiction"})
	n := c.Check("node", gfoNodeName, "GFX-082 node")
	gfxWantLims(t, "GFX-082 node", n, []string{"root_port_absent " + gfxKGPU}, nil)
	b := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSBasic(t)), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-082 negative")
	gfxWantLims(t, "GFX-082 negative", b, nil, []string{"root_port_absent"})
	// Two outgoing edges, one to the node: not root_port_absent but a
	// contradiction.
	a := gfxAgentArtifact(t, bins.Agent, gfxSRootless(t))
	p := &a.Frames[0].Payload
	if !p.HasAsset(gfxKSWD) {
		p.Assets = append(p.Assets, gfoAsset{"PCIeSwitch", "pci-bdf:" + gfoSWD, []gfoAlias{}})
	}
	gfxAddEdge(t, a, 0, gfxKGPU, gfxKSWD)
	x := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-082/GFX-083 two parents")
	gfxWantLims(t, "GFX-082/GFX-083 two parents", x, []string{"path_contradiction " + gfxKGPU}, []string{"root_port_absent"})
}

// TestGFX083_PathContradiction: multiple outgoing edges anywhere on the
// reachable path (trusted or not) or a revisit; the limitation survives
// segment truncation (TestGFX090_PathSegmentsTruncation).
func TestGFX083_PathContradiction(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for name, a := range map[string]*gfoArtifact{
		"S-CONTRA":   gfxAgentArtifact(t, bins.Agent, gfxSContra(t)),
		"S-CONTRA-F": gfxSContraF(t, bins.Agent),
	} {
		e := gfxNewCase(t, bins, a, gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-083 "+name)
		gfxWantLims(t, "GFX-083 "+name, e, []string{"path_contradiction " + gfxKGPU}, nil)
	}
	// The contradiction is judged on every edge of R, trusted or not.
	f := gfxSContra(t)
	gfxWithoutSources(f, "SysfsPhysicalParent")
	u := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-083 untrusted edges")
	gfxWantLims(t, "GFX-083 untrusted edges", u, []string{"path_contradiction " + gfxKGPU, "path_untrusted " + gfxKGPU}, nil)
	gfxWantSegs(t, "GFX-083 untrusted edges", u)
	// A contradiction on another device's path does not concern this one.
	n := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSRootless(t)), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-083 negative")
	gfxWantLims(t, "GFX-083 negative", n, nil, []string{"path_contradiction"})
}

// TestGFX084_WidthPairAbsent: no current width observation of F(d) in R.
func TestGFX084_WidthPairAbsent(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for name, f := range map[string]*gfoFx{"S-STALE": gfxSStale(t), "S-CARRY": gfxSCarry(t), "S-CONTRA": gfxSContra(t)} {
		e := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-084 "+name)
		gfxWantLims(t, "GFX-084 "+name, e, []string{"width_pair_absent " + gfxKGPU}, nil)
	}
	b := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSReady(t)), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-084 negative")
	gfxWantLims(t, "GFX-084 negative", b, nil, []string{"width_pair_absent"})
	// Only R counts: a missing pair in frame 1 alone leaves no limitation.
	f := gfxSReady(t)
	f.Remove(gfxAttr(1, gfoPathGPU, "current_link_width"))
	m := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-084 earlier frame only")
	gfxWantLims(t, "GFX-084 earlier frame only", m, nil, []string{"width_pair_absent"})
}

// TestGFX085_ClassificationUnknown: listing, link and class/vendor/physfn
// attribute failures mark GPU classification as unknown.
func TestGFX085_ClassificationUnknown(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	cases := []struct {
		diag gfoDiag
		want bool
	}{
		{gfoDiag{"sysfs_list_failed", "bus/pci/devices"}, true},
		{gfoDiag{"sysfs_link_invalid", "bus/pci/devices/0000:0f:00.0"}, true},
		{gfoDiag{"sysfs_link_escape", "bus/pci/devices/0000:0f:00.0"}, true},
		{gfoDiag{"sysfs_attr_missing", "devices/pci0000:00/0000:00:1f.0/class"}, true},
		{gfoDiag{"sysfs_attr_permission", "devices/pci0000:00/0000:00:1f.0/vendor"}, true},
		{gfoDiag{"sysfs_attr_unreadable", "devices/pci0000:00/0000:00:1f.0/physfn"}, true},
		{gfoDiag{"sysfs_attr_malformed", "devices/pci0000:00/0000:00:1f.0/class"}, true},
		{gfoDiag{"sysfs_attr_nodata", "devices/pci0000:00/0000:00:1f.0/current_link_width"}, false},
		{gfoDiag{"sysfs_attr_missing", "devices/pci0000:00/0000:00:1f.0/classx"}, false},
		{gfoDiag{"sysfs_attr_missing", "devices/pci0000:00/0000:00:1f.0/subclass"}, false},
		{gfoDiag{"sysfs_entry_name_invalid", "bus/pci/devices/class"}, false},
		{gfoDiag{"nvidia_bdf_not_gpu", "0000:0f:00.0"}, false},
		{gfoDiag{"sysfs_ancestor_missing", "devices/pci0000:00/0000:00:1f.0"}, false},
	}
	for _, x := range cases {
		cl := "GFX-085 " + x.diag.Code + " " + x.diag.Subject
		e := gfxNewCase(t, bins, gfxWithDiags(t, a, 0, x.diag), gfxDefaultFleet()).Check("gpu", gfxDevName, cl)
		if got := e.HasLim("nvidia_classification_unknown", ""); got != x.want {
			t.Errorf("%s: nvidia_classification_unknown present=%v, want %v", cl, got, x.want)
		}
	}
	e := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSLastPartial(t)), gfxDefaultFleet()).Check("gpu", gfxDevName, "GFX-085 S-LASTPARTIAL")
	gfxWantLims(t, "GFX-085 S-LASTPARTIAL", e, []string{"nvidia_classification_unknown", "collector:nvidia_bdf_not_gpu " + gfoGPU}, nil)
}
