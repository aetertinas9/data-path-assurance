package tests_test

// Artifact reader and integrity (GFX-030..038): JSON discipline, top-level
// values, frames, payload rules, digest and revision recomputation, M
// conversion, atomic rejection, trust source and time margins. Each case
// edits a path-agent artifact and, where the case targets one rule only,
// recomputes every other derived value so that only that rule is violated.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

type gfxArtCase struct {
	name string
	raw  []byte
	want int
	same bool // exit 0 output must equal the unedited artifact's output
}

// gfxArtEditor produces edited copies of a base artifact.
type gfxArtEditor struct {
	t    *testing.T
	base *gfoArtifact
}

// raw replaces exactly one occurrence of old in the artifact bytes.
func (ed gfxArtEditor) raw(old, repl string) []byte {
	ed.t.Helper()
	if n := bytes.Count(ed.base.Raw, []byte(old)); n != 1 {
		ed.t.Fatalf("test bug: %q occurs %d times in the artifact, want 1", old, n)
	}
	return bytes.Replace(ed.base.Raw, []byte(old), []byte(repl), 1)
}

// first replaces the first occurrence of old.
func (ed gfxArtEditor) first(old, repl string) []byte {
	ed.t.Helper()
	if !bytes.Contains(ed.base.Raw, []byte(old)) {
		ed.t.Fatalf("test bug: %q does not occur in the artifact", old)
	}
	return bytes.Replace(ed.base.Raw, []byte(old), []byte(repl), 1)
}

// all replaces every occurrence of old.
func (ed gfxArtEditor) all(old, repl string) []byte {
	ed.t.Helper()
	if !bytes.Contains(ed.base.Raw, []byte(old)) {
		ed.t.Fatalf("test bug: %q does not occur in the artifact", old)
	}
	return bytes.ReplaceAll(ed.base.Raw, []byte(old), []byte(repl))
}

// typed edits a decoded copy; seal is "none", "digest" (payloadDigest and
// bundleRevision) or "ids" (GFO-084 IDs as well).
func (ed gfxArtEditor) typed(seal string, edit func(a *gfoArtifact)) []byte {
	ed.t.Helper()
	c := gfxClone(ed.t, ed.base)
	edit(c)
	switch seal {
	case "ids":
		gfxSeal(ed.t, c, true)
	case "digest":
		gfxSeal(ed.t, c, false)
	default:
		c.Raw = gfoRenderArtifact(c)
	}
	return c.Raw
}

func gfxRunArtCases(t *testing.T, bins gfxBins, base *gfoArtifact, clause string, cases []gfxArtCase) {
	t.Helper()
	ref := gfxWriteInputs(t, base.Raw, gfxDefaultFleet().JSON())
	want := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, ref, "json")...), clause+" reference")
	for _, c := range cases {
		in := gfxWriteInputs(t, c.raw, gfxDefaultFleet().JSON())
		res := gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, gfxArgs("gpu", gfxDevName, in, "json")...)
		name := clause + " " + c.name
		if c.want != 0 {
			gfxFail(t, res, c.want, name, in.Dir, "zqx")
			continue
		}
		out := gfxOK(t, res, name)
		gfxDecodeChecked(t, out, name)
		if c.same && !bytes.Equal(out, want) {
			t.Errorf("%s: output differs from the unedited artifact output", name)
		}
	}
}

// gfxRetime rewrites frame fr as frame seq observed at `at` (all payload
// times follow the frame time; expiry = at + ttl seconds).
func gfxRetime(t *testing.T, fr *gfoFrame, seq int, at string, ttl int64) {
	t.Helper()
	exp := gfoAddSeconds(t, at, ttl)
	fr.Sequence = strconv.Itoa(seq)
	fr.ObservedAt = at
	p := &fr.Payload
	for k := range p.EdgeEvidence {
		p.EdgeEvidence[k].ObservedAt, p.EdgeEvidence[k].ExpiresAt = at, exp
	}
	for k := range p.Observations {
		o := &p.Observations[k]
		o.ObservedAt, o.ReceivedAt, o.ExpiresAt, o.Sequence = at, at, exp, fr.Sequence
	}
	for k := range p.GPUBindings {
		p.GPUBindings[k].ObservedAt, p.GPUBindings[k].ExpiresAt = at, exp
	}
}

// TestGFX030_JSONDiscipline: GFO-031 discipline for the artifact, decimal
// string integers, number grammar, printable strings and T-form times.
func TestGFX030_JSONDiscipline(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	body := bytes.TrimSuffix(a.Raw, []byte("\n"))
	end := func(extra string) []byte {
		return append(append([]byte{}, body[:len(body)-1]...), []byte(extra+"}\n")...)
	}
	cases := []gfxArtCase{
		{"BOM", append([]byte("\xef\xbb\xbf"), a.Raw...), 6, false},
		{"invalid UTF-8", ed.raw(`"offline:lab-a-basic"`, "\"offline:lab-a-b\xffsic\""), 6, false},
		{"top-level array", append(append([]byte("["), body...), []byte("]\n")...), 6, false},
		{"empty file", []byte{}, 6, false},
		{"duplicate top-level key", end(`,"mode":"offline"`), 6, false},
		{"duplicate key after escape decoding", end(`,"mod\u0065":"offline"`), 6, false},
		{"duplicate nested key", ed.raw(`"node":{"name":"gpu-node-1",`, `"node":{"name":"gpu-node-1","name":"gpu-node-1",`), 6, false},
		{"unknown top-level key", end(`,"zqx":1`), 6, false},
		{"unknown frame key", ed.raw(`"diagnosticsTotal":0}`, `"diagnosticsTotal":0,"zqx":0}`), 6, false},
		{"unknown payload key", ed.raw(`"gpuBindings":[`, `"zqx":[],"gpuBindings":[`), 6, false},
		{"unknown asset key", ed.first(`"aliases":[]`, `"aliases":[],"zqx":1`), 6, false},
		{"unknown trust key", ed.raw(`"mode":"Offline"`, `"mode":"Offline","zqx":1`), 6, false},
		{"null value", ed.raw(`"diagnostics":[]`, `"diagnostics":null`), 6, false},
		{"missing required key", ed.first(`"bootID":"`+gfoBootID+`",`, ``), 6, false},
		{"session as number", ed.first(`"session":"7"`, `"session":7`), 6, false},
		{"frame sequence as number", ed.raw(`"sequence":"0","completeness"`, `"sequence":0,"completeness"`), 6, false},
		{"diagnosticsTotal as string", ed.raw(`"diagnosticsTotal":0`, `"diagnosticsTotal":"0"`), 6, false},
		{"diagnosticsTruncated as number", ed.raw(`"diagnosticsTruncated":false`, `"diagnosticsTruncated":0`), 6, false},
		{"edgeIndex as string", ed.first(`"edgeIndex":0`, `"edgeIndex":"0"`), 6, false},
		{"edgeIndex with fraction", ed.first(`"edgeIndex":0`, `"edgeIndex":0.0`), 6, false},
		{"diagnosticsTotal beyond int64", ed.raw(`"diagnosticsTotal":0`, `"diagnosticsTotal":9223372036854775808`), 6, false},
		{"session with leading zero", ed.first(`"session":"7"`, `"session":"07"`), 6, false},
		{"frame sequence with leading zero", ed.raw(`"sequence":"0","completeness"`, `"sequence":"00","completeness"`), 6, false},
		{"observation sequence with sign", ed.first(`"sequence":"0","quality"`, `"sequence":"+0","quality"`), 6, false},
		{"int value with leading zero", ed.first(`{"int":"16"}`, `{"int":"016"}`), 6, false},
		{"int value with plus sign", ed.first(`{"int":"16"}`, `{"int":"+16"}`), 6, false},
		{"int value negative zero", ed.first(`{"int":"16"}`, `{"int":"-0"}`), 6, false},
		{"string with tab escape", ed.raw(`"offline:lab-a-basic"`, `"offline:lab-a\tbasic"`), 6, false},
		{"string with escaped non-ASCII", ed.raw(`"offline:lab-a-basic"`, `"offline:lab-a-b\u00e9sic"`), 6, false},
		{"string with raw non-ASCII", ed.raw(`"offline:lab-a-basic"`, "\"offline:lab-a-bésic\""), 6, false},
		{"time with zero fraction", ed.all(`"2026-09-24T00:00:00Z"`, `"2026-09-24T00:00:00.000Z"`), 6, false},
		{"time with offset", ed.all(`"2026-09-24T00:00:00Z"`, `"2026-09-24T00:00:00+00:00"`), 6, false},
		{"time lower-case z", ed.all(`"2026-09-24T00:00:00Z"`, `"2026-09-24T00:00:00z"`), 6, false},
		{"trailing data", append(append([]byte{}, a.Raw...), 'x'), 6, false},
		{"second value", append(append([]byte{}, a.Raw...), []byte("{}")...), 6, false},
		{"trailing whitespace", append(append([]byte{}, a.Raw...), []byte(" \t\r\n ")...), 0, true},
		{"no final newline", body, 0, true},
		{"escaped key", ed.raw(`"mode":"offline"`, `"mod\u0065":"offline"`), 0, true},
		{"escaped value", ed.raw(`"mode":"offline"`, `"mode":"offlin\u0065"`), 0, true},
		{"escaped slash", ed.all(`path-agent/sysfs-parent`, `path-agent\/sysfs-parent`), 0, true},
	}
	gfxRunArtCases(t, bins, a, "GFX-030", cases)
}

// TestGFX031_TopLevelValues: every top-level rule of GFX-031 is enforced;
// values at the accepted edges are covered by GFX-031 accepted runs.
func TestGFX031_TopLevelValues(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	long := func(n int) string { return strings.Repeat("a", n) }
	src := func(c *gfoArtifact) *[]gfoTrustSource { return &c.Trust.Sources }
	cases := []gfxArtCase{
		{"schemaVersion v2", ed.typed("none", func(c *gfoArtifact) { c.SchemaVersion = "dpa.offline-snapshot/v2" }), 6, false},
		{"mode live", ed.typed("none", func(c *gfoArtifact) { c.Mode = "live" }), 6, false},
		{"limitations empty", ed.typed("none", func(c *gfoArtifact) { c.Limitations = []string{} }), 6, false},
		{"limitations repeated", ed.typed("none", func(c *gfoArtifact) { c.Limitations = []string{"offline", "offline"} }), 6, false},
		{"limitations other", ed.typed("none", func(c *gfoArtifact) { c.Limitations = []string{"online"} }), 6, false},
		{"clusterID with space", ed.typed("none", func(c *gfoArtifact) { c.ClusterID = "lab a" }), 6, false},
		{"clusterID empty", ed.typed("none", func(c *gfoArtifact) { c.ClusterID = "" }), 6, false},
		{"clusterID 129 bytes", ed.typed("none", func(c *gfoArtifact) { c.ClusterID = long(129) }), 6, false},
		{"node.name empty", ed.typed("none", func(c *gfoArtifact) { c.NodeName = "" }), 6, false},
		{"node.name 254 bytes", ed.typed("none", func(c *gfoArtifact) { c.NodeName = long(254) }), 6, false},
		{"node.name with space", ed.typed("none", func(c *gfoArtifact) { c.NodeName = "gpu node" }), 6, false},
		{"node.uid empty", ed.typed("none", func(c *gfoArtifact) { c.NodeUID = "" }), 6, false},
		{"bootID 129 bytes", ed.typed("none", func(c *gfoArtifact) { c.BootID = long(129) }), 6, false},
		{"profileID offline: only", ed.typed("none", func(c *gfoArtifact) { c.Trust.ProfileID = "offline:" }), 6, false},
		{"profileID without prefix", ed.typed("none", func(c *gfoArtifact) { c.Trust.ProfileID = "online:lab-a-basic" }), 6, false},
		{"profileID upper-case prefix", ed.typed("none", func(c *gfoArtifact) { c.Trust.ProfileID = "Offline:lab-a-basic" }), 6, false},
		{"profileID 129 bytes", ed.typed("none", func(c *gfoArtifact) { c.Trust.ProfileID = "offline:" + long(121) }), 6, false},
		{"trust.mode Live", ed.typed("none", func(c *gfoArtifact) { c.Trust.Mode = "Live" }), 6, false},
		{"trust.mode lower case", ed.typed("none", func(c *gfoArtifact) { c.Trust.Mode = "offline" }), 6, false},
		{"session 0", ed.typed("ids", func(c *gfoArtifact) { c.Trust.Session, c.Frames[0].Session = "0", "0" }), 6, false},
		{"session beyond int64", ed.typed("none", func(c *gfoArtifact) {
			c.Trust.Session, c.Frames[0].Session = "9223372036854775808", "9223372036854775808"
		}), 6, false},
		{"sources empty", ed.typed("none", func(c *gfoArtifact) { *src(c) = []gfoTrustSource{} }), 6, false},
		{"sources 17", ed.typed("none", func(c *gfoArtifact) {
			var s []gfoTrustSource
			for i := 0; i < 17; i++ {
				s = append(s, gfoTrustSource{"SysfsPhysicalParent", "agent", fmt.Sprintf("src-%02d", i)})
			}
			*src(c) = s
		}), 6, false},
		{"sources out of order", ed.typed("none", func(c *gfoArtifact) {
			s := *src(c)
			s[0], s[1] = s[1], s[0]
		}), 6, false},
		{"sources duplicate", ed.typed("none", func(c *gfoArtifact) {
			s := *src(c)
			*src(c) = append([]gfoTrustSource{s[0]}, s...)
		}), 6, false},
		{"capability ExternalFence", ed.typed("none", func(c *gfoArtifact) {
			*src(c) = append(*src(c), gfoTrustSource{"ExternalFence", "zz", "zz"})
		}), 6, false},
		{"capability PodResourcesUUIDAllocation", ed.typed("none", func(c *gfoArtifact) {
			*src(c) = append(*src(c), gfoTrustSource{"PodResourcesUUIDAllocation", "zz", "zz"})
		}), 6, false},
		{"source type empty", ed.typed("none", func(c *gfoArtifact) { (*src(c))[0].Type = "" }), 6, false},
		{"source name 257 bytes", ed.typed("none", func(c *gfoArtifact) {
			*src(c) = append(*src(c), gfoTrustSource{"SysfsPhysicalParent", "zz", long(257)})
		}), 6, false},
		{"frames empty", ed.typed("none", func(c *gfoArtifact) { c.Frames = nil }), 6, false},
	}
	// 17 otherwise valid frames.
	seventeen := gfxClone(t, a)
	for i := 1; i < 17; i++ {
		fr := gfxClone(t, a).Frames[0]
		gfxRetime(t, &fr, i, gfoFrameTime(i), 300)
		seventeen.Frames = append(seventeen.Frames, fr)
	}
	gfxSeal(t, seventeen, true)
	cases = append(cases, gfxArtCase{"frames 17", seventeen.Raw, 6, false})
	gfxRunArtCases(t, bins, a, "GFX-031", cases)

	// Accepted edges, produced by path-agent from edge-valued manifests.
	accept := []struct {
		name string
		edit func(f *gfoFx, fl *gfxFleet)
	}{
		{"profileID of 9 bytes", func(f *gfoFx, fl *gfxFleet) { f.M.Profile = "offline:x" }},
		{"profileID of 128 bytes", func(f *gfoFx, fl *gfxFleet) { f.M.Profile = "offline:" + long(120) }},
		{"clusterID of 128 bytes", func(f *gfoFx, fl *gfxFleet) { f.M.Cluster = long(128); fl.ClusterID = long(128) }},
		{"node.name of 253 bytes", func(f *gfoFx, fl *gfxFleet) { f.M.NodeName = long(253); fl.Devices[0].NodeName = long(253) }},
		{"node.uid of 128 bytes", func(f *gfoFx, fl *gfxFleet) { f.M.NodeUID = long(128); fl.Devices[0].NodeUID = long(128) }},
		{"bootID of 128 bytes", func(f *gfoFx, fl *gfxFleet) { f.M.BootID = long(128) }},
		{"session max", func(f *gfoFx, fl *gfxFleet) { f.M.Session = "9223372036854775807" }},
		{"16 sources", func(f *gfoFx, fl *gfxFleet) {
			for i := 0; len(f.M.Sources) < 16; i++ {
				f.M.Sources = append(f.M.Sources, gfoSrc{"SysfsPhysicalParent", "extra", fmt.Sprintf("s%02d", i)})
			}
		}},
	}
	for _, x := range accept {
		f := gfxSBasic(t)
		fl := gfxDefaultFleet()
		x.edit(f, &fl)
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
		c.Check("gpu", gfxDevName, "GFX-031 accepted "+x.name)
		if x.name == "node.name of 253 bytes" {
			c.Check("node", long(253), "GFX-031 accepted "+x.name+" (node)")
		}
	}
}

// TestGFX032_Frames: frame identity, sequence, completeness, strictly
// increasing time, digest form and diagnostics rules.
func TestGFX032_Frames(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	f0 := func(c *gfoArtifact) *gfoFrame { return &c.Frames[0] }
	diags := func(n int) []gfoDiag {
		var d []gfoDiag
		for i := 0; i < n; i++ {
			d = append(d, gfoDiag{"sysfs_entry_name_invalid", fmt.Sprintf("bus/pci/devices/x%03d", i)})
		}
		return d
	}
	cases := []gfxArtCase{
		{"nodeUID mismatch", ed.typed("none", func(c *gfoArtifact) { f0(c).NodeUID = "other" }), 6, false},
		{"bootID mismatch", ed.typed("none", func(c *gfoArtifact) { f0(c).BootID = "other" }), 6, false},
		{"session mismatch", ed.typed("ids", func(c *gfoArtifact) { f0(c).Session = "8" }), 6, false},
		{"sequence not index", ed.typed("ids", func(c *gfoArtifact) { gfxRetime(t, f0(c), 1, f0(c).ObservedAt, 300) }), 6, false},
		{"completeness UNKNOWN", ed.typed("none", func(c *gfoArtifact) { f0(c).Completeness = "UNKNOWN" }), 6, false},
		{"completeness mixed case", ed.typed("none", func(c *gfoArtifact) { f0(c).Completeness = "Complete" }), 6, false},
		{"payloadDigest upper case", ed.typed("none", func(c *gfoArtifact) {
			f0(c).PayloadDigest = strings.ToUpper(f0(c).PayloadDigest)
			f0(c).BundleRevision = "7:0:" + f0(c).PayloadDigest
		}), 6, false},
		{"payloadDigest 63 hex", ed.typed("none", func(c *gfoArtifact) {
			f0(c).PayloadDigest = f0(c).PayloadDigest[:63]
			f0(c).BundleRevision = "7:0:" + f0(c).PayloadDigest
		}), 6, false},
		{"diagnostic code unknown", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"zqx_code", "x"}}, 1
		}), 6, false},
		{"diagnostic subject empty", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"width_omitted", ""}}, 1
		}), 6, false},
		{"diagnostic subject with space", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"width_omitted", "a b"}}, 1
		}), 6, false},
		{"diagnostic subject 16385 bytes", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"width_omitted", strings.Repeat("s", 16385)}}, 1
		}), 6, false},
		{"diagnostics out of order", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"width_omitted", "b"}, {"width_omitted", "a"}}, 2
		}), 6, false},
		{"diagnostics code order", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"width_omitted", "a"}, {"baseline_unmatched", "a"}}, 2
		}), 6, false},
		{"diagnostics duplicate", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = []gfoDiag{{"width_omitted", "a"}, {"width_omitted", "a"}}, 2
		}), 6, false},
		{"truncated without enough diagnostics", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal, f0(c).DiagnosticsTruncated = diags(1), 1, true
		}), 6, false},
		{"total differs from count", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal = diags(1), 2
		}), 6, false},
		{"truncated with total 256", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal, f0(c).DiagnosticsTruncated = diags(256), 256, true
		}), 6, false},
		{"truncated with 255 diagnostics", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal, f0(c).DiagnosticsTruncated = diags(255), 300, true
		}), 6, false},
		{"257 diagnostics", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal, f0(c).DiagnosticsTruncated = diags(257), 257, true
		}), 6, false},
		{"not truncated with total 257", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Diagnostics, f0(c).DiagnosticsTotal, f0(c).DiagnosticsTruncated = diags(256), 257, false
		}), 6, false},
	}
	gfxRunArtCases(t, bins, a, "GFX-032", cases)

	// Frame times must strictly increase.
	r := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	red := gfxArtEditor{t, r}
	timeCases := []gfxArtCase{
		{"frame 1 at the frame 0 time", red.typed("ids", func(c *gfoArtifact) { gfxRetime(t, &c.Frames[1], 1, c.Frames[0].ObservedAt, 300) }), 6, false},
		{"frame 2 before frame 1", red.typed("ids", func(c *gfoArtifact) { gfxRetime(t, &c.Frames[2], 2, gfoFrameTime(0), 300) }), 6, false},
		{"frames swapped", red.typed("ids", func(c *gfoArtifact) { c.Frames[1], c.Frames[2] = c.Frames[2], c.Frames[1] }), 6, false},
	}
	gfxRunArtCases(t, bins, r, "GFX-032", timeCases)

	// Accepted diagnostics edges (diagnostics are outside the digest).
	accepted := []struct {
		name  string
		edit  func(fr *gfoFrame)
		check func(e *gfxExpl) string
	}{
		{"subject of 16384 bytes", func(fr *gfoFrame) {
			fr.Diagnostics, fr.DiagnosticsTotal = []gfoDiag{{"width_omitted", strings.Repeat("s", 16384)}}, 1
		}, func(e *gfxExpl) string {
			if !e.HasLim("collector:width_omitted", strings.Repeat("s", 16384)) {
				return "collector:width_omitted with the 16384-byte subject missing"
			}
			return ""
		}},
		{"256 diagnostics of 257", func(fr *gfoFrame) {
			fr.Diagnostics, fr.DiagnosticsTotal, fr.DiagnosticsTruncated = diags(256), 257, true
		}, func(e *gfxExpl) string {
			if !e.HasLim("diagnostics_truncated", "257") || e.CountLim("collector:sysfs_entry_name_invalid") != 256 || !e.HasLim("diagnostics_outside_digest", "") {
				return fmt.Sprintf("want diagnostics_truncated 257, 256 collector entries and diagnostics_outside_digest; got %d collector entries", e.CountLim("collector:sysfs_entry_name_invalid"))
			}
			return ""
		}},
	}
	for _, x := range accepted {
		c := gfxClone(t, a)
		x.edit(&c.Frames[0])
		c.Raw = gfoRenderArtifact(c)
		cs := gfxNewCase(t, bins, c, gfxDefaultFleet())
		e := cs.Check("gpu", gfxDevName, "GFX-032 accepted "+x.name)
		if msg := x.check(e); msg != "" {
			t.Errorf("GFX-032/GFX-081 accepted %s: %s", x.name, msg)
		}
	}
}

// TestGFX033_Payload: payload shape, bounds, value forms, string lengths,
// order, times, IDs, the node asset and binding grammar.
func TestGFX033_Payload(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	p := func(c *gfoArtifact) *gfoPayload { return &c.Frames[0].Payload }
	obs := func(c *gfoArtifact, subj, sig string) *gfoObs {
		return &p(c).Observations[gfxObsIndex(t, c, 0, subj, sig)]
	}
	gpuKey := gfoFnKey(gfoGPU)
	nodeIdx := func(c *gfoArtifact) int {
		for i, as := range p(c).Assets {
			if as.Kind == "KubernetesNode" {
				return i
			}
		}
		t.Fatalf("test bug: no node asset")
		return -1
	}
	aliases := func(n int) []gfoAlias {
		var out []gfoAlias
		for i := 0; i < n; i++ {
			out = append(out, gfoAlias{"test-ns", fmt.Sprintf("alias-%03d", i)})
		}
		return out
	}
	dims := func(n int) []gfoKV {
		var out []gfoKV
		for i := 0; i < n; i++ {
			out = append(out, gfoKV{fmt.Sprintf("k%03d", i), "v"})
		}
		return out
	}
	extraAssets := func(c *gfoArtifact, n int) {
		for i := 0; i < n; i++ {
			p(c).Assets = append(p(c).Assets, gfoAsset{"Site", fmt.Sprintf("site:s%05d", i), []gfoAlias{}})
		}
		gfxSortPayload(p(c))
	}
	addStringObs := func(c *gfoArtifact, value string) {
		fr := &c.Frames[0]
		gpu := obs(c, gpuKey, gfoSigClass)
		o := *gpu
		o.Signal, o.ValueKind, o.ValueJSON, o.Unit, o.Dims = "test.label", "string", gfoQuoteOut(value), "text", []gfoKV{}
		o.ID = gfoObservationID(fr.Session, fr.Sequence, gpuKey, o.Signal)
		p(c).Observations = append(p(c).Observations, o)
		gfxSortPayload(p(c))
	}
	cases := []gfxArtCase{
		// (a) shape and counts.
		{"allocationBatch present", ed.raw(`"gpuBindings":[`, `"allocationBatch":{},"gpuBindings":[`), 6, false},
		{"65 aliases on one asset", ed.typed("digest", func(c *gfoArtifact) { p(c).Assets[nodeIdx(c)].Aliases = aliases(65) }), 6, false},
		{"65 dimensions", ed.typed("digest", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Dims = dims(65) }), 6, false},
		{"4097 assets", ed.typed("digest", func(c *gfoArtifact) { extraAssets(c, 4097-len(p(c).Assets)) }), 6, false},
		// (b) values.
		{"asset kind unknown", ed.typed("digest", func(c *gfoArtifact) {
			p(c).Assets = append(p(c).Assets, gfoAsset{"Bogus", "x:y", []gfoAlias{}})
			gfxSortPayload(p(c))
		}), 6, false},
		{"subject kind unknown", ed.typed("digest", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Subject.Kind = "Bogus" }), 6, false},
		{"relation CONNECTED_TO", ed.typed("ids", func(c *gfoArtifact) { p(c).Edges[0].Relation = "CONNECTED_TO" }), 6, false},
		{"origin Intended", ed.typed("ids", func(c *gfoArtifact) { p(c).Edges[0].Origin = "Intended" }), 6, false},
		{"edgeEvidence kind Intended", ed.typed("digest", func(c *gfoArtifact) { p(c).EdgeEvidence[0].Kind = "Intended" }), 6, false},
		{"quality unknown word", ed.typed("digest", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Quality = "Excellent" }), 6, false},
		{"value with two keys", ed.first(`{"int":"16"}`, `{"int":"16","float":"16"}`), 6, false},
		{"value object empty", ed.first(`{"int":"16"}`, `{}`), 6, false},
		{"float not in shortest form", ed.typed("digest", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigExpect)
			o.ValueKind, o.ValueJSON = "float", `"16.0"`
		}), 6, false},
		{"float with exponent", ed.typed("none", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigExpect)
			o.ValueKind, o.ValueJSON = "float", `"1.6e1"`
		}), 6, false},
		{"float NaN", ed.typed("none", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigExpect)
			o.ValueKind, o.ValueJSON = "float", `"NaN"`
		}), 6, false},
		{"float infinite", ed.typed("none", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigExpect)
			o.ValueKind, o.ValueJSON = "float", `"+Inf"`
		}), 6, false},
		{"bool as string", ed.typed("none", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigClass)
			o.ValueKind, o.ValueJSON = "bool", `"true"`
		}), 6, false},
		{"string as number", ed.typed("none", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigClass)
			o.ValueKind, o.ValueJSON = "string", `1`
		}), 6, false},
		{"int as number", ed.typed("none", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigClass)
			o.ValueJSON = strings.Trim(o.ValueJSON, `"`)
		}), 6, false},
		// (c) string lengths.
		{"edge sourceName 257 bytes", ed.typed("digest", func(c *gfoArtifact) { p(c).EdgeEvidence[0].SourceName = strings.Repeat("s", 257) }), 6, false},
		{"string value 4097 bytes", ed.typed("digest", func(c *gfoArtifact) { addStringObs(c, strings.Repeat("v", 4097)) }), 6, false},
		{"rawDigest 257 bytes", ed.typed("digest", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).RawDigest = strings.Repeat("r", 257) }), 6, false},
		{"canonical 513 bytes", ed.typed("digest", func(c *gfoArtifact) {
			p(c).Assets = append(p(c).Assets, gfoAsset{"Site", "site:" + strings.Repeat("x", 508), []gfoAlias{}})
			gfxSortPayload(p(c))
		}), 6, false},
		{"unit empty", ed.typed("digest", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Unit = "" }), 6, false},
		{"alias value empty", ed.typed("digest", func(c *gfoArtifact) { p(c).Assets[nodeIdx(c)].Aliases = []gfoAlias{{"ns", ""}} }), 6, false},
		// (d) order and uniqueness.
		{"assets out of order", ed.typed("digest", func(c *gfoArtifact) { p(c).Assets[0], p(c).Assets[1] = p(c).Assets[1], p(c).Assets[0] }), 6, false},
		{"duplicate asset", ed.typed("digest", func(c *gfoArtifact) {
			p(c).Assets = append(p(c).Assets, p(c).Assets[len(p(c).Assets)-1])
		}), 6, false},
		{"edges out of order", ed.typed("ids", func(c *gfoArtifact) {
			q := p(c)
			q.Edges[0], q.Edges[1] = q.Edges[1], q.Edges[0]
		}), 6, false},
		{"edgeEvidence missing", ed.typed("digest", func(c *gfoArtifact) {
			q := p(c)
			q.EdgeEvidence = q.EdgeEvidence[:len(q.EdgeEvidence)-1]
		}), 6, false},
		{"edgeIndex not position", ed.typed("digest", func(c *gfoArtifact) {
			q := p(c)
			q.EdgeEvidence[0].EdgeIndex, q.EdgeEvidence[1].EdgeIndex = q.EdgeEvidence[1].EdgeIndex, q.EdgeEvidence[0].EdgeIndex
		}), 6, false},
		{"observations out of order", ed.typed("digest", func(c *gfoArtifact) {
			q := p(c)
			q.Observations[0], q.Observations[1] = q.Observations[1], q.Observations[0]
		}), 6, false},
		{"observation id repeated", ed.typed("digest", func(c *gfoArtifact) {
			o := *obs(c, gpuKey, gfoSigClass)
			o.Dims = []gfoKV{{"x", "y"}}
			p(c).Observations = append(p(c).Observations, o)
			gfxSortPayload(p(c))
		}), 6, false},
		{"gpuBindings out of order", ed.typed("ids", func(c *gfoArtifact) {
			q := p(c)
			b := q.GPUBindings[0]
			b.BDF = gfoNIC
			q.GPUBindings = append([]gfoBinding{b}, q.GPUBindings...)
		}), 6, false},
		// (e) times.
		{"edgeEvidence observedAt differs", ed.typed("digest", func(c *gfoArtifact) {
			p(c).EdgeEvidence[0].ObservedAt = gfoAddSeconds(t, c.Frames[0].ObservedAt, -1)
		}), 6, false},
		{"expiresAt equals observedAt", ed.typed("digest", func(c *gfoArtifact) {
			p(c).EdgeEvidence[0].ExpiresAt = c.Frames[0].ObservedAt
		}), 6, false},
		{"expiresAt before observedAt", ed.typed("digest", func(c *gfoArtifact) {
			obs(c, gpuKey, gfoSigClass).ExpiresAt = gfoAddSeconds(t, c.Frames[0].ObservedAt, -1)
		}), 6, false},
		{"receivedAt differs", ed.typed("digest", func(c *gfoArtifact) {
			obs(c, gpuKey, gfoSigClass).ReceivedAt = gfoAddSeconds(t, c.Frames[0].ObservedAt, 1)
		}), 6, false},
		{"observation sequence differs", ed.typed("digest", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Sequence = "1" }), 6, false},
		{"binding observedAt differs", ed.typed("digest", func(c *gfoArtifact) {
			p(c).GPUBindings[0].ObservedAt = gfoAddSeconds(t, c.Frames[0].ObservedAt, -1)
		}), 6, false},
		// (f) IDs.
		{"edge evidenceID not recomputed", ed.typed("digest", func(c *gfoArtifact) {
			p(c).EdgeEvidence[0].EvidenceID = gfxFlipLast(p(c).EdgeEvidence[0].EvidenceID)
		}), 6, false},
		{"observation id not recomputed", ed.typed("digest", func(c *gfoArtifact) {
			o := obs(c, gpuKey, gfoSigClass)
			o.ID = gfxFlipLast(o.ID)
		}), 6, false},
		{"binding evidenceID not recomputed", ed.typed("digest", func(c *gfoArtifact) {
			p(c).GPUBindings[0].EvidenceID = gfxFlipLast(p(c).GPUBindings[0].EvidenceID)
		}), 6, false},
		{"evidenceID of another sequence", ed.typed("digest", func(c *gfoArtifact) {
			p(c).GPUBindings[0].EvidenceID = gfoBindingID("7", "1", gfxUUID, gfoGPU)
		}), 6, false},
		// (g) node asset.
		{"second KubernetesNode asset", ed.typed("digest", func(c *gfoArtifact) {
			p(c).Assets = append(p(c).Assets, gfoAsset{"KubernetesNode", "kubernetes-node-uid:zz-other", []gfoAlias{}})
			gfxSortPayload(p(c))
		}), 6, false},
		{"node asset of another uid", ed.typed("ids", func(c *gfoArtifact) {
			old := gfoNodeKey(gfoNodeUID)
			p(c).Assets[nodeIdx(c)].Canonical = "kubernetes-node-uid:other"
			for k := range p(c).Edges {
				if p(c).Edges[k].ToKey == old {
					p(c).Edges[k].ToKey = "KubernetesNode/kubernetes-node-uid:other"
				}
			}
			gfxSortPayload(p(c))
		}), 6, false},
		// (h) binding grammar.
		{"binding bdf with 8-digit domain", ed.typed("ids", func(c *gfoArtifact) { p(c).GPUBindings[0].BDF = "00000000:03:00.0" }), 6, false},
		{"binding uuid upper case", ed.typed("ids", func(c *gfoArtifact) { p(c).GPUBindings[0].UUID = strings.ToUpper(gfxUUID) }), 6, false},
		{"binding serial present", ed.typed("digest", func(c *gfoArtifact) { p(c).GPUBindings[0].Serial = "SN-1" }), 6, false},
	}
	gfxRunArtCases(t, bins, a, "GFX-033", cases)

	// Accepted boundary values, checked end to end against the oracle.
	accept := []struct {
		name string
		edit func(c *gfoArtifact)
	}{
		{"64 aliases", func(c *gfoArtifact) { p(c).Assets[nodeIdx(c)].Aliases = aliases(64) }},
		{"64 dimensions", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Dims = dims(64) }},
		{"4096 assets", func(c *gfoArtifact) { extraAssets(c, 4096-len(p(c).Assets)) }},
		{"canonical 512 bytes", func(c *gfoArtifact) {
			p(c).Assets = append(p(c).Assets, gfoAsset{"Site", "site:" + strings.Repeat("x", 507), []gfoAlias{}})
			gfxSortPayload(p(c))
		}},
		{"string value 4096 bytes", func(c *gfoArtifact) { addStringObs(c, strings.Repeat("v", 4096)) }},
		{"string value empty", func(c *gfoArtifact) { addStringObs(c, "") }},
		{"rawDigest 256 bytes", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).RawDigest = strings.Repeat("r", 256) }},
		{"edge sourceName 256 bytes", func(c *gfoArtifact) { p(c).EdgeEvidence[0].SourceName = strings.Repeat("s", 256) }},
		{"quality Degraded", func(c *gfoArtifact) { obs(c, gpuKey, gfoSigClass).Quality = "Degraded" }},
	}
	for _, x := range accept {
		c := gfxClone(t, a)
		x.edit(c)
		gfxSeal(t, c, true)
		cs := gfxNewCase(t, bins, c, gfxDefaultFleet())
		cs.Check("gpu", gfxDevName, "GFX-033 accepted "+x.name)
	}
}

func gfxFlipLast(s string) string {
	if s == "" {
		return "x"
	}
	last := s[len(s)-1]
	repl := byte('0')
	if last == '0' {
		repl = '1'
	}
	return s[:len(s)-1] + string(repl)
}

// TestGFX034_DigestAndRevision: payloadDigest is recomputed from the GFO-086
// canonical bytes and bundleRevision from session, sequence and digest.
func TestGFX034_DigestAndRevision(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	other := gfxAgentArtifact(t, bins.Agent, gfxSWidth(t))
	f0 := func(c *gfoArtifact) *gfoFrame { return &c.Frames[0] }
	cases := []gfxArtCase{
		{"GFX-102 error row: one payload byte changed", ed.first(`"unit":"lanes"`, `"unit":"lanez"`), 6, false},
		{"digest of another payload", ed.typed("none", func(c *gfoArtifact) {
			f0(c).PayloadDigest = other.Frames[0].PayloadDigest
			f0(c).BundleRevision = "7:0:" + f0(c).PayloadDigest
		}), 6, false},
		{"digest not recomputed after an edit", ed.typed("none", func(c *gfoArtifact) {
			f0(c).Payload.Observations[0].RawDigest = "r"
		}), 6, false},
		{"revision with another session", ed.typed("none", func(c *gfoArtifact) { f0(c).BundleRevision = "8:0:" + f0(c).PayloadDigest }), 6, false},
		{"revision with padded sequence", ed.typed("none", func(c *gfoArtifact) { f0(c).BundleRevision = "7:00:" + f0(c).PayloadDigest }), 6, false},
		{"revision with another digest", ed.typed("none", func(c *gfoArtifact) { f0(c).BundleRevision = "7:0:" + other.Frames[0].PayloadDigest }), 6, false},
		{"revision without sequence", ed.typed("none", func(c *gfoArtifact) { f0(c).BundleRevision = "7:" + f0(c).PayloadDigest }), 6, false},
	}
	gfxRunArtCases(t, bins, a, "GFX-034", cases)
}

// TestGFX035_RatifiedConversion: M must build every value and the topology
// must resync; edges must reference payload assets.
func TestGFX035_RatifiedConversion(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	ed := gfxArtEditor{t, a}
	p := func(c *gfoArtifact) *gfoPayload { return &c.Frames[0].Payload }
	cases := []gfxArtCase{
		{"edge endpoint not an asset", ed.typed("digest", func(c *gfoArtifact) {
			var keep []gfoAsset
			for _, as := range p(c).Assets {
				if as.Key() != gfoSWKey(gfoSWU) {
					keep = append(keep, as)
				}
			}
			p(c).Assets = keep
		}), 6, false},
		{"canonical without namespace separator", ed.typed("digest", func(c *gfoArtifact) {
			p(c).Assets = append(p(c).Assets, gfoAsset{"Site", "nocolon", []gfoAlias{}})
			gfxSortPayload(p(c))
		}), 6, false},
		{"self edge", ed.typed("ids", func(c *gfoArtifact) {
			q := p(c)
			q.Edges = append(q.Edges, gfoEdge{gfoFnKey(gfoGPU), "LOCATED_IN", gfoFnKey(gfoGPU), "Observed"})
			ev := q.EdgeEvidence[0]
			ev.EdgeIndex = len(q.Edges) - 1
			q.EdgeEvidence = append(q.EdgeEvidence, ev)
			gfxSortPayload(q)
		}), 6, false},
		{"observation with invalid signal", ed.typed("ids", func(c *gfoArtifact) {
			q := p(c)
			o := q.Observations[0]
			o.Signal = "Bad Signal"
			q.Observations = append(q.Observations, o)
			gfxSortPayload(q)
		}), 6, false},
	}
	gfxRunArtCases(t, bins, a, "GFX-035", cases)
}

// TestGFX036_WholeArtifactRejection: any violation in any frame rejects the
// artifact as a whole; a PARTIAL frame is not a violation.
func TestGFX036_WholeArtifactRejection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	r := gfxAgentArtifact(t, bins.Agent, gfxSReady(t))
	ed := gfxArtEditor{t, r}
	cases := []gfxArtCase{
		{"only the last frame's digest is wrong", ed.typed("none", func(c *gfoArtifact) {
			c.Frames[2].Payload.Observations[0].RawDigest = "r"
		}), 6, false},
		{"only the last frame has an unknown diagnostic code", ed.typed("none", func(c *gfoArtifact) {
			c.Frames[2].Diagnostics, c.Frames[2].DiagnosticsTotal = []gfoDiag{{"zqx", "x"}}, 1
		}), 6, false},
		{"only the middle frame has a bad sequence", ed.typed("ids", func(c *gfoArtifact) {
			gfxRetime(t, &c.Frames[1], 5, c.Frames[1].ObservedAt, 300)
		}), 6, false},
	}
	gfxRunArtCases(t, bins, r, "GFX-036", cases)
	f := gfxSReady(t)
	f.Write(gfoSys(1)+"/bus/pci/devices/.gitkeep", "")
	c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	e := c.Check("gpu", gfxDevName, "GFX-036 PARTIAL frame is evaluated")
	if !e.HasLim("frame_partial", "1") {
		t.Errorf("GFX-036/GFX-080: limitations %v lack frame_partial 1", e.Lims())
	}
}

// TestGFX037_TrustFromArtifactOnly: the trust profile is the artifact's;
// only the artifact trust changes the result, and it is never live.
func TestGFX037_TrustFromArtifactOnly(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	trusted := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, gfxSBasic(t)), gfxDefaultFleet())
	f := gfxSBasic(t)
	f.M.Sources = []gfoSrc{{"SysfsPhysicalParent", "agent", gfoParentSrc}, {"SysfsPCIeWidth", "agent", gfoWidthSrc}, {"OperatorBaseline", "agent", gfoWidthSrc}}
	untrusted := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), gfxDefaultFleet())
	te := trusted.Check("gpu", gfxDevName, "GFX-037 trusted binding source")
	ue := untrusted.Check("gpu", gfxDevName, "GFX-037 untrusted binding source")
	if te.Identity["state"] != "Bound" || ue.Identity["state"] != "Unknown" || ue.Identity["reason"] != "UntrustedSource" {
		t.Errorf("GFX-037: identity %v / %v, want Bound for the trusting artifact and Unknown/UntrustedSource otherwise", te.Identity, ue.Identity)
	}
	for _, e := range []*gfxExpl{te, ue} {
		if !e.HasLim("offline_trust_not_live", "offline:lab-a-basic") || !e.HasLim("offline", "") {
			t.Errorf("GFX-037/GFX-080: limitations %v lack offline and offline_trust_not_live offline:lab-a-basic", e.Lims())
		}
	}
	// The environment cannot add trust.
	res := gfxRun(t, trusted.Bins.Pathctl, gfoRunOpt{Env: append(gfxEnv(), "DPA_TRUST=NVIDIAUUIDBinding", "PATHCTL_TRUST=live")}, gfxArgs("gpu", gfxDevName, untrusted.In, "json")...)
	if out := gfxOK(t, res, "GFX-037 with trust-like environment"); !bytes.Equal(out, ue.Raw) {
		t.Errorf("GFX-037/GFX-004: environment changed the output")
	}
}

// TestGFX038_ArtifactTimeMargin: every frame time keeps one day of room on
// both ends of the timestamp range; path-agent accepts the whole range.
func TestGFX038_ArtifactTimeMargin(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	for _, at := range []string{"0001-01-02T00:00:00Z", "0001-01-01T12:00:00Z", "9999-12-31T00:00:00Z", "9999-12-31T23:00:00Z"} {
		f := gfxSBasic(t)
		f.M.Frames = []string{at}
		fl := gfxDefaultFleet()
		fl.Devices[0].IntentAt = "2026-09-24T00:00:00Z"
		in := gfxWriteInputs(t, gfxAgentArtifact(t, bins.Agent, f).Raw, fl.JSON())
		gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 6, "GFX-038 frame at "+at)
	}
	// A later frame outside the margin rejects the whole artifact.
	f := gfxBaseFixture(t, 2)
	f.M.Frames = []string{"9999-12-30T00:00:00Z", "9999-12-31T00:00:00.5Z"}
	in := gfxWriteInputs(t, gfxAgentArtifact(t, bins.Agent, f).Raw, gfxDefaultFleet().JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 6, "GFX-038 second frame outside the margin")
	for _, at := range []string{"0001-01-02T00:00:00.000000001Z", "9999-12-30T23:59:59.999999999Z"} {
		f := gfxSBasic(t)
		f.M.Frames = []string{at}
		fl := gfxDefaultFleet()
		fl.Devices[0].IntentAt = at
		c := gfxNewCase(t, bins, gfxAgentArtifact(t, bins.Agent, f), fl)
		e := c.Check("gpu", gfxDevName, "GFX-038 accepted frame at "+at)
		if e.ObservedAt != at || e.Identity["state"] != "Bound" {
			t.Errorf("GFX-038/GFX-041: observedAt %q identity %v, want %q and Bound", e.ObservedAt, e.Identity, at)
		}
	}
}
