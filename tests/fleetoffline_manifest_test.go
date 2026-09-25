package tests_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gfoMustReplace replaces the first old in s and fails the test when old is
// absent (guards manifest mutations against silently doing nothing).
func gfoMustReplace(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("test bug: %q not found in manifest %s", old, s)
	}
	return strings.Replace(s, old, repl, 1)
}

// GFO-030: only manifest.json, frames/<i>/sys and frames/<i>/nvidia-smi.csv
// (i < N) are used; frames/<N> and any other entry (even FIFOs) are ignored.
func TestGFO030_OnlyDeclaredInputsAreUsed(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	clean := gfoNewBasic(t, 1)
	want := clean.Run(bin)
	gfoOK(t, want, &clean.M, "GFO-030 clean")

	noisy := gfoNewBasic(t, 1)
	noisy.Write("README", "not an input\n")
	noisy.Fifo("extra.fifo")
	noisy.Fifo("frames/extra.fifo")
	noisy.Fifo("frames/0/other.fifo")
	noisy.Fifo("frames/0/nvidia-smi.csv.bak")
	noisy.Mkdir("frames/0/sys.orig")
	// frames/1 is beyond N=1: a GPU with a FIFO class and a FIFO CSV.
	noisy.Dev(1, gfoGPUDev(gfoHB, "0000:00:05.0"))
	noisy.Remove(gfoDevDir(1, []string{gfoHB, "0000:00:05.0"}) + "/class")
	noisy.Fifo(gfoDevDir(1, []string{gfoHB, "0000:00:05.0"}) + "/class")
	noisy.Fifo(gfoCSV(1))
	noisy.Fifo("frames/1/manifest.json")
	res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", noisy.save2())
	gfoOK(t, res, &noisy.M, "GFO-030 noisy")
	if !bytes.Equal(res.Stdout, want.Stdout) {
		t.Errorf("GFO-030: undeclared files changed stdout")
	}
}

// gfoIndentJSON pretty-prints valid JSON (structural whitespace only).
func gfoIndentJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(s), "", "  "); err != nil {
		t.Fatalf("test bug: %v", err)
	}
	return b.String()
}

// save2 writes the manifest and returns the fixture root.
func (f *gfoFx) save2() string {
	f.t.Helper()
	f.save()
	return f.Root
}

// GFO-031: manifest JSON discipline (regular file, size, UTF-8 without BOM,
// object, no duplicate/unknown keys, no null, exact types, integer literal
// form, nothing but whitespace after the value).
func TestGFO031_ManifestJSONDiscipline(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	m := gfoDefaultManifest(1)
	m.Baselines = []gfoBL{{gfoGPU, gfoSWD, "16"}}
	base := m.JSON()
	rep := func(old, repl string) string { return gfoMustReplace(t, base, old, repl) }
	t0 := `{"observedAt":"` + gfoT0 + `"}`

	invalid := []struct{ name, raw string }{
		{"empty_file", ""},
		{"whitespace_only", " \n\t"},
		{"null_document", "null"},
		{"bom", "\xef\xbb\xbf" + base},
		{"top_level_array", "[" + base + "]"},
		{"top_level_string", `"manifest"`},
		{"top_level_number", "7"},
		{"trailing_garbage", base + "x"},
		{"trailing_second_value", base + "{}"},
		{"trailing_nul", base + "\x00"},
		{"syntax_double_comma", rep(`,"bootID":`, `,,"bootID":`)},
		{"syntax_truncated", base[:len(base)-1]},
		{"duplicate_top_key", rep(`"clusterID":"lab-a"`, `"clusterID":"lab-a","clusterID":"lab-a"`)},
		{"duplicate_top_key_other_value", rep(`"clusterID":"lab-a"`, `"clusterID":"lab-b","clusterID":"lab-a"`)},
		{"duplicate_node_key", rep(`"name":"gpu-node-1"`, `"name":"gpu-node-1","name":"gpu-node-1"`)},
		{"duplicate_trust_key", rep(`"session":7`, `"session":7,"session":7`)},
		{"duplicate_source_key", rep(`"sourceType":"agent"`, `"sourceType":"agent","sourceType":"agent"`)},
		{"duplicate_frame_key", rep(t0, `{"observedAt":"`+gfoT0+`","observedAt":"`+gfoT0+`"}`)},
		{"duplicate_baseline_key", rep(`"expectedWidth":16`, `"expectedWidth":16,"expectedWidth":16`)},
		{"unknown_top_key", rep(`{"schemaVersion"`, `{"extra":1,"schemaVersion"`)},
		{"unknown_node_key", rep(`"uid":`, `"extra":"x","uid":`)},
		{"unknown_trust_key", rep(`"session":7`, `"session":7,"extra":1`)},
		{"unknown_source_key", rep(`"sourceType":"agent"`, `"extra":"x","sourceType":"agent"`)},
		{"unknown_frame_key", rep(t0, `{"observedAt":"`+gfoT0+`","extra":1}`)},
		{"unknown_baseline_key", rep(`"expectedWidth":16`, `"expectedWidth":16,"extra":1`)},
		{"allocation_batch_key", rep(`{"schemaVersion"`, `{"allocationBatch":{},"schemaVersion"`)},
		{"null_required", rep(`"bootID":"`+gfoBootID+`"`, `"bootID":null`)},
		{"null_nested", rep(`"name":"gpu-node-1"`, `"name":null`)},
		{"null_optional_baselines", rep(`"operatorBaselines":[{"function":"`+gfoGPU+`","peer":"`+gfoSWD+`","expectedWidth":16}]`, `"operatorBaselines":null`)},
		{"missing_bootID", rep(`,"bootID":"`+gfoBootID+`"`, ``)},
		{"missing_node_uid", rep(`,"uid":"`+gfoNodeUID+`"`, ``)},
		{"missing_schemaVersion", rep(`"schemaVersion":"`+gfoSchemaIn+`",`, ``)},
		{"missing_ttl", rep(`,"evidenceTTLSeconds":300`, ``)},
		{"missing_frames", rep(`,"frames":[`+t0+`]`, ``)},
		{"missing_frame_observedAt", rep(t0, `{}`)},
		{"missing_source_name", rep(`,"sourceName":"`+gfoParentSrc+`"`, ``)},
		{"missing_baseline_width", rep(`,"expectedWidth":16`, ``)},
		{"session_string", rep(`"session":7`, `"session":"7"`)},
		{"session_fraction", rep(`"session":7`, `"session":7.0`)},
		{"session_exponent", rep(`"session":7`, `"session":7e0`)},
		{"session_negative_zero", rep(`"session":7`, `"session":-0`)},
		{"session_bool", rep(`"session":7`, `"session":true`)},
		{"ttl_fraction", rep(`"evidenceTTLSeconds":300`, `"evidenceTTLSeconds":300.0`)},
		{"ttl_exponent", rep(`"evidenceTTLSeconds":300`, `"evidenceTTLSeconds":3e2`)},
		{"ttl_string", rep(`"evidenceTTLSeconds":300`, `"evidenceTTLSeconds":"300"`)},
		{"width_string", rep(`"expectedWidth":16`, `"expectedWidth":"16"`)},
		{"width_fraction", rep(`"expectedWidth":16`, `"expectedWidth":16.0`)},
		{"width_exponent", rep(`"expectedWidth":16`, `"expectedWidth":1.6e1`)},
		{"frames_object", rep(`"frames":[`+t0+`]`, `"frames":`+t0)},
		{"node_array", rep(`"node":{"name":"gpu-node-1","uid":"`+gfoNodeUID+`"}`, `"node":["gpu-node-1","`+gfoNodeUID+`"]`)},
		{"sources_object", gfoMustReplace(t, rep(`"sources":[`, `"sources":{"x":[`), `]},"evidenceTTLSeconds"`, `]}},"evidenceTTLSeconds"`)},
		{"cluster_number", rep(`"clusterID":"lab-a"`, `"clusterID":7`)},
		{"observedAt_number", rep(t0, `{"observedAt":1790000000}`)},
		{"invalid_utf8", rep(`"name":"gpu-node-1"`, "\"name\":\"gpu-node-\xff\"")},
		{"invalid_utf8_outside_string", base + " \xff"},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			f.SaveRaw(c.raw)
			f.Fail(bin, 3, "GFO-031 "+c.name)
		})
	}

	sizes := []struct {
		name string
		size int
		ok   bool
	}{{"size_65536_ok", 65536, true}, {"size_65537_invalid", 65537, false}}
	for _, c := range sizes {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			f.SaveRaw(base + strings.Repeat(" ", c.size-len(base)))
			if st, err := os.Stat(f.P("manifest.json")); err != nil || st.Size() != int64(c.size) {
				t.Fatalf("test bug: manifest size %v, %v", st, err)
			}
			if c.ok {
				gfoOK(t, f.Run(bin), &m, "GFO-031 "+c.name)
			} else {
				f.Fail(bin, 3, "GFO-031 "+c.name)
			}
		})
	}

	valid := []struct{ name, raw, wantName string }{
		{"trailing_whitespace", base + "\n \t\r\n", gfoNodeName},
		{"pretty_whitespace", gfoIndentJSON(t, base), gfoNodeName},
		{"key_order_free", gfoMustReplace(t, rep(`,"evidenceTTLSeconds":300`, ``), `{"schemaVersion"`, `{"evidenceTTLSeconds":300,"schemaVersion"`), gfoNodeName},
		{"json_escapes_decoded", rep(`"name":"gpu-node-1"`, `"name":"gpu\u002dnode\/1"`), "gpu-node/1"},
		{"empty_baselines_present", rep(`"operatorBaselines":[{"function":"`+gfoGPU+`","peer":"`+gfoSWD+`","expectedWidth":16}]`, `"operatorBaselines":[]`), gfoNodeName},
	}
	for _, c := range valid {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			f.SaveRaw(c.raw)
			a := gfoOK(t, f.Run(bin), nil, "GFO-031 "+c.name)
			if a.NodeName != c.wantName || a.ClusterID != gfoCluster || a.Trust.Session != "7" {
				t.Errorf("GFO-031 %s: node %q cluster %q session %q", c.name, a.NodeName, a.ClusterID, a.Trust.Session)
			}
		})
	}

	t.Run("fifo_manifest_not_waited", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Manual()
		f.Fifo("manifest.json")
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.Root), 3, "GFO-031 FIFO manifest")
	})
	t.Run("directory_manifest", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Manual()
		f.Mkdir("manifest.json")
		f.Fail(bin, 3, "GFO-031 directory manifest")
	})
}

// GFO-032: manifest field rules, including exact ASCII/length boundaries.
func TestGFO032_ManifestFieldRules(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	rep := strings.Repeat
	src := func(n int) []gfoSrc {
		var out []gfoSrc
		for i := 0; i < n; i++ {
			out = append(out, gfoSrc{"SysfsPhysicalParent", "agent", fmt.Sprintf("source-%02d", i)})
		}
		return out
	}
	baselines := func(n int) []gfoBL {
		var out []gfoBL
		for i := 0; i < n; i++ {
			out = append(out, gfoBL{fmt.Sprintf("0001:%02x:%02x.0", i/32, i%32), "0002:00:00.0", "16"})
		}
		return out
	}
	type tc struct {
		name string
		mut  func(m *gfoManifest)
		ok   bool
	}
	cases := []tc{
		{"schema_v2", func(m *gfoManifest) { m.Schema = "dpa.offline-fixture/v2" }, false},
		{"schema_case", func(m *gfoManifest) { m.Schema = "DPA.offline-fixture/v1" }, false},
		{"schema_trailing_space", func(m *gfoManifest) { m.Schema = gfoSchemaIn + " " }, false},
		{"schema_empty", func(m *gfoManifest) { m.Schema = "" }, false},
		{"cluster_128", func(m *gfoManifest) { m.Cluster = rep("c", 128) }, true},
		{"cluster_129", func(m *gfoManifest) { m.Cluster = rep("c", 129) }, false},
		{"cluster_charset", func(m *gfoManifest) { m.Cluster = "A.b_c-9" }, true},
		{"cluster_empty", func(m *gfoManifest) { m.Cluster = "" }, false},
		{"cluster_space", func(m *gfoManifest) { m.Cluster = "lab a" }, false},
		{"cluster_slash", func(m *gfoManifest) { m.Cluster = "lab/a" }, false},
		{"cluster_colon", func(m *gfoManifest) { m.Cluster = "lab:a" }, false},
		{"cluster_non_ascii", func(m *gfoManifest) { m.Cluster = "lab-\u00e9" }, false},
		{"name_253", func(m *gfoManifest) { m.NodeName = rep("n", 253) }, true},
		{"name_254", func(m *gfoManifest) { m.NodeName = rep("n", 254) }, false},
		{"name_empty", func(m *gfoManifest) { m.NodeName = "" }, false},
		{"name_ascii_edges", func(m *gfoManifest) { m.NodeName = "!~" }, true},
		{"name_space", func(m *gfoManifest) { m.NodeName = "gpu node" }, false},
		{"name_leading_space", func(m *gfoManifest) { m.NodeName = " gpu" }, false},
		{"name_tab", func(m *gfoManifest) { m.NodeName = "gpu\tnode" }, false},
		{"name_del", func(m *gfoManifest) { m.NodeName = "gpu\x7f" }, false},
		{"name_non_ascii", func(m *gfoManifest) { m.NodeName = "gpu-\u00e9" }, false},
		{"uid_128", func(m *gfoManifest) { m.NodeUID = rep("u", 128) }, true},
		{"uid_129", func(m *gfoManifest) { m.NodeUID = rep("u", 129) }, false},
		{"uid_empty", func(m *gfoManifest) { m.NodeUID = "" }, false},
		{"uid_space", func(m *gfoManifest) { m.NodeUID = "uid 1" }, false},
		{"boot_128", func(m *gfoManifest) { m.BootID = rep("b", 128) }, true},
		{"boot_129", func(m *gfoManifest) { m.BootID = rep("b", 129) }, false},
		{"boot_empty", func(m *gfoManifest) { m.BootID = "" }, false},
		{"profile_9", func(m *gfoManifest) { m.Profile = "offline:x" }, true},
		{"profile_8", func(m *gfoManifest) { m.Profile = "offline:" }, false},
		{"profile_128", func(m *gfoManifest) { m.Profile = "offline:" + rep("p", 120) }, true},
		{"profile_129", func(m *gfoManifest) { m.Profile = "offline:" + rep("p", 121) }, false},
		{"profile_prefix_case", func(m *gfoManifest) { m.Profile = "Offline:x" }, false},
		{"profile_live", func(m *gfoManifest) { m.Profile = "live:abcdef" }, false},
		{"profile_space", func(m *gfoManifest) { m.Profile = "offline: x" }, false},
		{"session_1", func(m *gfoManifest) { m.Session = "1" }, true},
		{"session_max", func(m *gfoManifest) { m.Session = "9223372036854775807" }, true},
		{"session_0", func(m *gfoManifest) { m.Session = "0" }, false},
		{"session_negative", func(m *gfoManifest) { m.Session = "-1" }, false},
		{"session_max_plus_1", func(m *gfoManifest) { m.Session = "9223372036854775808" }, false},
		{"session_u64_overflow", func(m *gfoManifest) { m.Session = "18446744073709551616" }, false},
		{"sources_0", func(m *gfoManifest) { m.Sources = nil }, false},
		{"sources_1", func(m *gfoManifest) { m.Sources = src(1) }, true},
		{"sources_16", func(m *gfoManifest) { m.Sources = src(16) }, true},
		{"sources_17", func(m *gfoManifest) { m.Sources = src(17) }, false},
		{"source_same_capability_other_source", func(m *gfoManifest) {
			m.Sources = append(m.Sources, gfoSrc{"SysfsPhysicalParent", "agent", "other"})
		}, true},
		{"source_duplicate_triple", func(m *gfoManifest) { m.Sources = append(m.Sources, m.Sources[0]) }, false},
		{"capability_pod_resources", func(m *gfoManifest) { m.Sources[0].Cap = "PodResourcesUUIDAllocation" }, false},
		{"capability_external_fence", func(m *gfoManifest) { m.Sources[0].Cap = "ExternalFence" }, false},
		{"capability_prefixed", func(m *gfoManifest) { m.Sources[0].Cap = "TrustSysfsPhysicalParent" }, false},
		{"capability_case", func(m *gfoManifest) { m.Sources[0].Cap = "sysfsPhysicalParent" }, false},
		{"capability_unknown", func(m *gfoManifest) { m.Sources[0].Cap = "Bogus" }, false},
		{"capability_empty", func(m *gfoManifest) { m.Sources[0].Cap = "" }, false},
		{"source_type_empty", func(m *gfoManifest) { m.Sources[0].Type = "" }, false},
		{"source_type_space", func(m *gfoManifest) { m.Sources[0].Type = "agent " }, false},
		{"source_name_empty", func(m *gfoManifest) { m.Sources[0].Name = "" }, false},
		{"source_name_256", func(m *gfoManifest) { m.Sources[0].Name = rep("s", 256) }, true},
		{"source_name_257", func(m *gfoManifest) { m.Sources[0].Name = rep("s", 257) }, false},
		{"source_type_256", func(m *gfoManifest) { m.Sources[0].Type = rep("t", 256) }, true},
		{"source_type_257", func(m *gfoManifest) { m.Sources[0].Type = rep("t", 257) }, false},
		{"ttl_1", func(m *gfoManifest) { m.TTL = "1" }, true},
		{"ttl_86400", func(m *gfoManifest) { m.TTL = "86400" }, true},
		{"ttl_0", func(m *gfoManifest) { m.TTL = "0" }, false},
		{"ttl_86401", func(m *gfoManifest) { m.TTL = "86401" }, false},
		{"ttl_negative", func(m *gfoManifest) { m.TTL = "-300" }, false},
		{"frames_0", func(m *gfoManifest) { m.Frames = nil }, false},
		{"frames_17", func(m *gfoManifest) {
			m.Frames = nil
			for i := 0; i < 17; i++ {
				m.Frames = append(m.Frames, gfoFrameTime(i))
			}
		}, false},
		{"baselines_absent", func(m *gfoManifest) { m.Baselines = nil; m.WithBaselines = false }, true},
		{"baselines_256", func(m *gfoManifest) { m.Baselines = baselines(256) }, true},
		{"baselines_257", func(m *gfoManifest) { m.Baselines = baselines(257) }, false},
		{"baseline_function_8digit_domain", func(m *gfoManifest) { m.Baselines[0].Function = "00000000:03:00.0" }, false},
		{"baseline_function_uppercase", func(m *gfoManifest) { m.Baselines[0].Function = "0000:0A:00.0" }, false},
		{"baseline_function_trailing_space", func(m *gfoManifest) { m.Baselines[0].Function = gfoGPU + " " }, false},
		{"baseline_function_device_20", func(m *gfoManifest) { m.Baselines[0].Function = "0000:03:20.0" }, false},
		{"baseline_function_fn_8", func(m *gfoManifest) { m.Baselines[0].Function = "0000:03:00.8" }, false},
		{"baseline_function_5digit_domain", func(m *gfoManifest) { m.Baselines[0].Function = "10000:01:00.0" }, true},
		{"baseline_function_5digit_zero_domain", func(m *gfoManifest) { m.Baselines[0].Function = "00000:01:00.0" }, false},
		{"baseline_peer_short_bus", func(m *gfoManifest) { m.Baselines[0].Peer = "0000:2:08.0" }, false},
		{"baseline_peer_uppercase", func(m *gfoManifest) { m.Baselines[0].Peer = "0000:02:0A.0" }, false},
		{"baseline_function_equals_peer", func(m *gfoManifest) { m.Baselines[0].Peer = m.Baselines[0].Function }, false},
		{"baseline_duplicate_function", func(m *gfoManifest) {
			m.Baselines = append(m.Baselines, gfoBL{gfoGPU, gfoSWU, "16"})
		}, false},
		{"width_3", func(m *gfoManifest) { m.Baselines[0].Width = "3" }, false},
		{"width_0", func(m *gfoManifest) { m.Baselines[0].Width = "0" }, false},
		{"width_64", func(m *gfoManifest) { m.Baselines[0].Width = "64" }, false},
		{"width_negative", func(m *gfoManifest) { m.Baselines[0].Width = "-16" }, false},
	}
	for _, w := range []string{"1", "2", "4", "8", "12", "16", "32"} {
		cases = append(cases, tc{"width_" + w, func(m *gfoManifest) { m.Baselines[0].Width = w }, true})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.mut(&f.M)
			if c.ok {
				f.OK(bin, "GFO-032 "+c.name)
			} else {
				f.Fail(bin, 3, "GFO-032 "+c.name)
			}
		})
	}
}

// GFO-032/GFO-095: 16 frames is accepted and yields 16 frames.
func TestGFO032_SixteenFramesAccepted(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 16)
	a := f.OK(bin, "GFO-032 16 frames")
	if len(a.Frames) != 16 {
		t.Fatalf("GFO-081: %d frames, want 16", len(a.Frames))
	}
}

// GFO-033: observedAt grammar, UTC conversion, strict increase and the
// protobuf timestamp range for observedAt and observedAt+TTL.
func TestGFO033_ObservedAtRules(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	type tc struct {
		name   string
		frames []string
		ttl    string
		ok     bool
		want   []string // expected output observedAt when ok
	}
	one := func(name, in string, ok bool, want string) tc {
		c := tc{name: name, frames: []string{in}, ok: ok}
		if ok {
			c.want = []string{want}
		}
		return c
	}
	cases := []tc{
		one("zulu", "2026-09-24T00:00:00Z", true, "2026-09-24T00:00:00Z"),
		one("fraction_1", "2026-09-24T00:00:00.5Z", true, "2026-09-24T00:00:00.5Z"),
		one("fraction_9", "2026-09-24T00:00:00.123456789Z", true, "2026-09-24T00:00:00.123456789Z"),
		one("fraction_trailing_zero_trimmed", "2026-09-24T00:00:00.100Z", true, "2026-09-24T00:00:00.1Z"),
		one("fraction_all_zero_dropped", "2026-09-24T00:00:00.000Z", true, "2026-09-24T00:00:00Z"),
		one("positive_offset", "2026-09-24T09:00:00+09:00", true, "2026-09-24T00:00:00Z"),
		one("negative_offset_half_hour", "2026-09-23T19:30:00-04:30", true, "2026-09-24T00:00:00Z"),
		one("offset_crosses_year", "2027-01-01T08:00:00+09:00", true, "2026-12-31T23:00:00Z"),
		one("leap_day", "2024-02-29T12:00:00Z", true, "2024-02-29T12:00:00Z"),
		one("last_second_of_day", "2026-09-24T23:59:59Z", true, "2026-09-24T23:59:59Z"),
		one("min_nonzero_instant", "0001-01-01T00:00:00.000000001Z", true, "0001-01-01T00:00:00.000000001Z"),
		{name: "max_expires_in_range", frames: []string{"9999-12-31T23:59:58.999999999Z"}, ttl: "1", ok: true, want: []string{"9999-12-31T23:59:58.999999999Z"}},
		one("fraction_10_digits", "2026-09-24T00:00:00.1234567890Z", false, ""),
		one("fraction_empty", "2026-09-24T00:00:00.Z", false, ""),
		one("fraction_comma", "2026-09-24T00:00:00,5Z", false, ""),
		one("lowercase_t", "2026-09-24t00:00:00Z", false, ""),
		one("lowercase_z", "2026-09-24T00:00:00z", false, ""),
		one("space_separator", "2026-09-24 00:00:00Z", false, ""),
		one("no_zone", "2026-09-24T00:00:00", false, ""),
		one("zone_without_colon", "2026-09-24T00:00:00+0900", false, ""),
		one("zone_hours_only", "2026-09-24T00:00:00+09", false, ""),
		one("no_seconds", "2026-09-24T00:00Z", false, ""),
		one("second_60", "2026-09-24T23:59:60Z", false, ""),
		one("feb_29_non_leap", "2026-02-29T00:00:00Z", false, ""),
		one("month_13", "2026-13-01T00:00:00Z", false, ""),
		one("month_00", "2026-00-10T00:00:00Z", false, ""),
		one("day_31_in_september", "2026-09-31T00:00:00Z", false, ""),
		one("day_00", "2026-09-00T00:00:00Z", false, ""),
		one("single_digit_month", "2026-9-24T00:00:00Z", false, ""),
		one("two_digit_year", "26-09-24T00:00:00Z", false, ""),
		one("five_digit_year", "12026-09-24T00:00:00Z", false, ""),
		one("leading_space", " 2026-09-24T00:00:00Z", false, ""),
		one("trailing_space", "2026-09-24T00:00:00Z ", false, ""),
		one("empty", "", false, ""),
		one("year_0000", "0000-12-31T23:59:59Z", false, ""),
		one("offset_before_year_1", "0001-01-01T00:00:00+00:01", false, ""),
		one("offset_after_year_9999", "9999-12-31T23:59:59-00:01", false, ""),
		{name: "expires_after_range", frames: []string{"9999-12-31T23:59:59Z"}, ttl: "1"},
		{name: "expires_after_range_nanos", frames: []string{"9999-12-31T23:59:59.999999999Z"}, ttl: "1"},
		{name: "increasing_by_1ns", frames: []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:00.000000001Z"}, ok: true,
			want: []string{"2026-09-24T00:00:00Z", "2026-09-24T00:00:00.000000001Z"}},
		{name: "equal_frames", frames: []string{gfoT0, gfoT0}},
		{name: "decreasing_frames", frames: []string{"2026-09-24T00:00:01Z", gfoT0}},
		{name: "equal_instant_other_zone", frames: []string{"2026-09-24T09:00:00+09:00", gfoT0}},
		{name: "decreasing_after_utc", frames: []string{"2026-09-24T00:00:01Z", "2026-09-24T09:00:00.5+09:00"}},
		{name: "third_frame_decreasing", frames: []string{gfoT0, "2026-09-24T00:00:15Z", "2026-09-24T00:00:10Z"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, len(c.frames))
			f.M.Frames = c.frames
			if c.ttl != "" {
				f.M.TTL = c.ttl
			}
			if !c.ok {
				f.Fail(bin, 3, "GFO-033 "+c.name)
				return
			}
			a := f.OK(bin, "GFO-033 "+c.name)
			for i, w := range c.want {
				if a.Frames[i].ObservedAt != w {
					t.Errorf("GFO-033: frame %d observedAt %q, want %q", i, a.Frames[i].ObservedAt, w)
				}
			}
		})
	}
}

// GFO-034: manifest trust is copied (sorted) and never changes what is
// collected; a fixture that trusts nothing still collects the same frames.
func TestGFO034_TrustIsCopiedNotApplied(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	full := gfoNewBasic(t, 1)
	a := full.OK(bin, "GFO-034 trusted")

	none := gfoNewBasic(t, 1)
	none.M.Sources = []gfoSrc{{"SysfsPhysicalParent", "agent", "someone-else"}}
	b := none.OK(bin, "GFO-034 untrusted")
	if len(b.Trust.Sources) != 1 || b.Trust.Sources[0] != (gfoTrustSource{"SysfsPhysicalParent", "agent", "someone-else"}) {
		t.Errorf("GFO-034: trust.sources %+v, want exactly the single manifest source", b.Trust.Sources)
	}
	if gfoRenderFrame(a.Frames[0]) != gfoRenderFrame(b.Frames[0]) {
		t.Errorf("GFO-034: collected frame differs when the manifest trusts nothing")
	}

	reversed := gfoNewBasic(t, 1)
	reversed.M.Sources = []gfoSrc{
		{"SysfsPhysicalParent", "zz", "b"}, {"NVIDIAUUIDBinding", "agent", "z"}, {"NVIDIAUUIDBinding", "agent", "a"},
		{"OperatorBaseline", "agent", gfoWidthSrc}, {"NVIDIAUUIDBinding", "Agent", "a"},
	}
	c := reversed.OK(bin, "GFO-034 unsorted sources")
	want := []gfoTrustSource{
		{"NVIDIAUUIDBinding", "Agent", "a"}, {"NVIDIAUUIDBinding", "agent", "a"}, {"NVIDIAUUIDBinding", "agent", "z"},
		{"OperatorBaseline", "agent", gfoWidthSrc}, {"SysfsPhysicalParent", "zz", "b"},
	}
	if fmt.Sprint(c.Trust.Sources) != fmt.Sprint(want) {
		t.Errorf("GFO-080: trust.sources %v, want byte order %v", c.Trust.Sources, want)
	}
	if gfoRenderFrame(a.Frames[0]) != gfoRenderFrame(c.Frames[0]) {
		t.Errorf("GFO-034: collected frame differs when only trust sources differ")
	}
}

// GFO-035: operatorBaselines apply to every frame; a frame without the
// function reports baseline_unmatched only there.
func TestGFO035_BaselineAppliesToEveryFrame(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewFx(t, 3)
	f.Basic(0)
	f.Basic(2)
	// Frame 1 has the switch chain but no GPU.
	f.Dev(1, gfoBridge(gfoPathRPA...))
	f.Dev(1, gfoBridge(gfoPathSWU...))
	f.Dev(1, gfoBridge(gfoPathSWD...))
	f.Dev(1, gfoBridge(gfoPathRPB...))
	f.Dev(1, gfoNICDev(gfoPathNIC...))
	f.M.Baselines = []gfoBL{{gfoGPU, gfoSWD, "8"}}
	a := f.OK(bin, "GFO-035")
	for _, i := range []int{0, 2} {
		_, exp, ok := a.Frames[i].Payload.WidthPair(gfoGPU)
		if !ok || exp.IntValue() != "8" || exp.Dim("pcie.expected.provenance") != gfoProvOp {
			t.Errorf("GFO-035: frame %d GPU expected width %q/%q, want operator baseline 8", i, exp.IntValue(), exp.Dim("pcie.expected.provenance"))
		}
		if a.Frames[i].CountCode("baseline_unmatched") != 0 {
			t.Errorf("GFO-061: frame %d has baseline_unmatched: %s", i, a.Frames[i].DiagString())
		}
	}
	if !a.Frames[1].HasDiag("baseline_unmatched", gfoGPU) {
		t.Errorf("GFO-061: frame 1 without the GPU lacks baseline_unmatched(%s): %s", gfoGPU, a.Frames[1].DiagString())
	}
}

// GFO-036: invalid fixture layouts end with exit 3 (checked root -> manifest
// -> frame directories -> collection); later sysfs problems are observations.
func TestGFO036_FixtureInvalid(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	outside := func(t *testing.T) string {
		// A complete, valid basic fixture outside the fixture root.
		o := gfoNewBasic(t, 1)
		o.save()
		return o.Root
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, f *gfoFx) string // returns the --fixture-root value
	}{
		{"root_missing", func(t *testing.T, f *gfoFx) string { return filepath.Join(f.Root, "missing") }},
		{"root_is_file", func(t *testing.T, f *gfoFx) string { f.Write("plain", "x"); return f.P("plain") }},
		{"root_is_fifo", func(t *testing.T, f *gfoFx) string { f.Fifo("root.fifo"); return f.P("root.fifo") }},
		{"manifest_missing", func(t *testing.T, f *gfoFx) string { f.Manual(); return f.Root }},
		{"manifest_dangling_symlink", func(t *testing.T, f *gfoFx) string {
			f.Manual()
			f.Link("manifest.json", "nowhere.json")
			return f.Root
		}},
		{"manifest_symlink_loop", func(t *testing.T, f *gfoFx) string {
			f.Manual()
			f.Link("manifest.json", "manifest.json")
			return f.Root
		}},
		{"manifest_symlink_outside_root", func(t *testing.T, f *gfoFx) string {
			f.Manual()
			f.Link("manifest.json", filepath.Join(outside(t), "manifest.json"))
			return f.Root
		}},
		{"manifest_relative_symlink_outside_root", func(t *testing.T, f *gfoFx) string {
			f.Manual()
			o := outside(t)
			rel, err := filepath.Rel(f.Root, filepath.Join(o, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			f.Link("manifest.json", rel)
			return f.Root
		}},
		{"frame0_sys_missing", func(t *testing.T, f *gfoFx) string { f.Remove("frames/0/sys"); return f.Root }},
		{"frame0_missing", func(t *testing.T, f *gfoFx) string { f.Remove("frames/0"); return f.Root }},
		{"frame0_sys_is_file", func(t *testing.T, f *gfoFx) string {
			f.Remove("frames/0/sys")
			f.Write("frames/0/sys", "x")
			return f.Root
		}},
		{"frame0_sys_is_fifo", func(t *testing.T, f *gfoFx) string {
			f.Remove("frames/0/sys")
			f.Fifo("frames/0/sys")
			return f.Root
		}},
		{"frame0_sys_symlink_outside_root", func(t *testing.T, f *gfoFx) string {
			f.Remove("frames/0/sys")
			f.Link("frames/0/sys", filepath.Join(outside(t), "frames/0/sys"))
			return f.Root
		}},
		{"frame2_sys_missing", func(t *testing.T, f *gfoFx) string {
			f.M = gfoDefaultManifest(3)
			f.Mkdir("frames/1/sys")
			return f.Root
		}},
		{"manifest_invalid_and_frame_over_bound", func(t *testing.T, f *gfoFx) string {
			gfoFillNames(t, f, 0, 8193)
			f.M.Cluster = ""
			return f.Root
		}},
		{"frame1_missing_and_frame0_over_bound", func(t *testing.T, f *gfoFx) string {
			f.M = gfoDefaultManifest(2)
			gfoFillNames(t, f, 0, 8193)
			return f.Root
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			root := c.setup(t, f)
			f.save()
			gfoFail(t, gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", root), 3, "GFO-036 "+c.name)
		})
	}

	t.Run("empty_frame_sys_is_observation", func(t *testing.T) {
		t.Parallel()
		f := gfoNewFx(t, 1)
		a := f.OK(bin, "GFO-036 empty sys")
		if a.Frames[0].Completeness != "PARTIAL" || a.Frames[0].CountCode("sysfs_list_failed") != 1 {
			t.Errorf("GFO-041: frame sys without bus/pci/devices: %s %s, want PARTIAL with sysfs_list_failed", a.Frames[0].Completeness, a.Frames[0].DiagString())
		}
	})
}

// GFO-033 (ratified v1.1): the zero instant 0001-01-01T00:00:00Z is outside
// the accepted range (exit 3, nothing on stdout) in any notation, with or
// without functions; the next instant ...00.000000001Z is valid.
func TestGFO033_ZeroInstantExcluded(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	withGPU := func(t *testing.T) *gfoFx { return gfoNewBasic(t, 1) }
	emptyDevices := func(t *testing.T) *gfoFx {
		f := gfoNewFx(t, 1)
		f.Mkdir(gfoSys(0) + "/bus/pci/devices")
		return f
	}
	cases := []struct {
		name    string
		fixture func(t *testing.T) *gfoFx
		at      string
		want    string // expected output observedAt; "" = exit 3
	}{
		{"zero_instant_with_gpu", withGPU, "0001-01-01T00:00:00Z", ""},
		{"zero_instant_empty_devices", emptyDevices, "0001-01-01T00:00:00Z", ""},
		{"zero_instant_offset_form", withGPU, "0001-01-01T01:00:00+01:00", ""},
		{"zero_instant_offset_form_empty_devices", emptyDevices, "0001-01-01T01:00:00+01:00", ""},
		{"zero_instant_zero_fraction", withGPU, "0001-01-01T00:00:00.000000000Z", ""},
		{"next_instant_with_gpu", withGPU, "0001-01-01T00:00:00.000000001Z", "0001-01-01T00:00:00.000000001Z"},
		{"next_instant_empty_devices", emptyDevices, "0001-01-01T00:00:00.000000001Z", "0001-01-01T00:00:00.000000001Z"},
		{"next_instant_offset_form", withGPU, "0001-01-01T01:00:00.000000001+01:00", "0001-01-01T00:00:00.000000001Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := c.fixture(t)
			f.M.Frames = []string{c.at}
			if c.want == "" {
				f.Fail(bin, 3, "GFO-033 v1.1 "+c.name)
				return
			}
			a := f.OK(bin, "GFO-033 v1.1 "+c.name)
			if a.Frames[0].ObservedAt != c.want {
				t.Errorf("GFO-033: observedAt %q, want %q", a.Frames[0].ObservedAt, c.want)
			}
		})
	}
}
