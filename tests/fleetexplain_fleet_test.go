package tests_test

// Fleet file reader: JSON discipline, fields, domain validation, artifact
// agreement and target selection (GFX-020..024, fleet side of GFX-038).

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type gfxFleetCase struct {
	name string
	json string
	want int
	// same: an exit-0 output must equal the output of the normative fleet
	// file (the variant only differs in encoding).
	same bool
}

// gfxRunFleetCases runs every case against the S-BASIC artifact with a gpu
// request for the normative device.
func gfxRunFleetCases(t *testing.T, bins gfxBins, a *gfoArtifact, clause string, cases []gfxFleetCase) {
	t.Helper()
	ref := gfxWriteInputs(t, a.Raw, gfxDefaultFleet().JSON())
	want := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, ref, "json")...), clause+" reference")
	for _, c := range cases {
		in := gfxWriteInputs(t, a.Raw, c.json)
		res := gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, gfxArgs("gpu", gfxDevName, in, "json")...)
		name := clause + " " + c.name
		if c.want != 0 {
			gfxFail(t, res, c.want, name, in.Dir, "zqx")
			continue
		}
		out := gfxOK(t, res, name)
		gfxDecodeChecked(t, out, name)
		if c.same && !bytes.Equal(out, want) {
			t.Errorf("%s: output differs from the normative fleet file output", name)
		}
	}
}

// TestGFX020_JSONDiscipline: UTF-8 without BOM, one top-level object, no
// duplicate or unknown key at any depth, no null, exact types, integer
// literals, whitespace-only trailer; keys compare after escape decoding.
func TestGFX020_JSONDiscipline(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	g := gfxDefaultFleet().JSON()
	r := func(old, repl string) string { return gfxReplace(t, g, old, repl) }
	reordered := `{"devices":[{"inventoryClaim":{"evidenceID":"asset-db:rack7-u12-gpu0","source":"operator/asset-db","uuid":"GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d","vendor":"NVIDIA"},` +
		`"intentObservedAt":"2026-09-24T00:00:00Z","metadataGeneration":1,"requestID":"enroll-1","desiredState":"InService",` +
		`"nodeRef":{"uid":"7c9e6679-7425-40de-944b-e07fc1f90ae7","name":"gpu-node-1"},"uid":"0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41","name":"gpu-node-1-gpu0"}],` +
		`"policy":{"requiredCoverage":[{"required":true,"pathKind":"gpu-pcie-parent","name":"pcie-parent"},{"required":true,"pathKind":"gpu-pcie-root","name":"pcie-root"},` +
		`{"required":true,"pathKind":"gpu-pcie-link-width-normal","name":"pcie-width"},{"required":false,"pathKind":"nic-lldp-remote","name":"nic-lldp"}],` +
		`"readyForSeconds":30,"freshnessSeconds":60,"revision":"gpu-path-v1"},"fleet":{"uid":"5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10","name":"lab-a-gpus"},` +
		`"clusterID":"lab-a","schemaVersion":"dpa.offline-fleet/v1"}`
	pretty := gfxSpaced(g)
	cases := []gfxFleetCase{
		{"BOM", "\xef\xbb\xbf" + g, 7, false},
		{"invalid UTF-8 in a string", r(`"revision":"gpu-path-v1"`, "\"revision\":\"gpu-path-\xff1\""), 7, false},
		{"empty file", "", 7, false},
		{"whitespace only", " \n", 7, false},
		{"top-level array", "[" + g + "]", 7, false},
		{"top-level string", `"x"`, 7, false},
		{"top-level null", "null", 7, false},
		{"duplicate top-level key", r(`"clusterID":"lab-a"`, `"clusterID":"lab-a","clusterID":"lab-a"`), 7, false},
		{"duplicate nested key", r(`"revision":"gpu-path-v1"`, `"revision":"gpu-path-v1","revision":"gpu-path-v1"`), 7, false},
		{"duplicate key after escape decoding", r(`"clusterID":"lab-a"`, `"clusterID":"lab-a","clus\u0074erID":"lab-a"`), 7, false},
		{"duplicate device key", r(`"requestID":"enroll-1"`, `"requestID":"enroll-1","requestID":"enroll-2"`), 7, false},
		{"duplicate claim key", r(`"vendor":"NVIDIA"`, `"vendor":"NVIDIA","vendor":"NVIDIA"`), 7, false},
		{"unknown top-level key", r(`"clusterID":"lab-a"`, `"clusterID":"lab-a","zqx":1`), 7, false},
		{"unknown fleet key", r(`"name":"lab-a-gpus"`, `"name":"lab-a-gpus","zqx":"a"`), 7, false},
		{"unknown policy key", r(`"revision":"gpu-path-v1"`, `"revision":"gpu-path-v1","zqx":[]`), 7, false},
		{"unknown coverage key", r(`"name":"nic-lldp"`, `"name":"nic-lldp","zqx":true`), 7, false},
		{"unknown device key", r(`"requestID":"enroll-1"`, `"requestID":"enroll-1","zqx":{}`), 7, false},
		{"unknown nodeRef key", r(`"name":"gpu-node-1",`, `"name":"gpu-node-1","zqx":"x",`), 7, false},
		{"unknown claim key", r(`"vendor":"NVIDIA"`, `"vendor":"NVIDIA","zqx":"x"`), 7, false},
		{"key case differs", r(`"clusterID":"lab-a"`, `"ClusterID":"lab-a"`), 7, false},
		{"null optional serial", r(`"vendor":"NVIDIA"`, `"vendor":"NVIDIA","serial":null`), 7, false},
		{"null required value", r(`"requestID":"enroll-1"`, `"requestID":null`), 7, false},
		{"null devices", r(`"devices":[`, `"devices":null,"zqxrest":[`), 7, false},
		{"integer as string", r(`"metadataGeneration":1`, `"metadataGeneration":"1"`), 7, false},
		{"integer with fraction", r(`"freshnessSeconds":60`, `"freshnessSeconds":60.0`), 7, false},
		{"integer with exponent", r(`"freshnessSeconds":60`, `"freshnessSeconds":6e1`), 7, false},
		{"negative integer", r(`"readyForSeconds":30`, `"readyForSeconds":-30`), 7, false},
		{"leading zero integer", r(`"freshnessSeconds":60`, `"freshnessSeconds":060`), 7, false},
		{"plus sign integer", r(`"freshnessSeconds":60`, `"freshnessSeconds":+60`), 7, false},
		{"integer beyond int64", r(`"metadataGeneration":1`, `"metadataGeneration":9223372036854775808`), 7, false},
		{"boolean as string", r(`"required":false`, `"required":"false"`), 7, false},
		{"boolean as number", r(`"required":false`, `"required":0`), 7, false},
		{"string as number", r(`"revision":"gpu-path-v1"`, `"revision":1`), 7, false},
		{"object instead of array", r(`"devices":[`, `"devices":{"x":[`) + "}", 7, false},
		{"array instead of object", r(`"fleet":{"name":"lab-a-gpus","uid":"5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"}`, `"fleet":["lab-a-gpus"]`), 7, false},
		{"missing required top-level key", r(`"clusterID":"lab-a",`, ``), 7, false},
		{"missing required device key", r(`,"requestID":"enroll-1"`, ``), 7, false},
		{"missing requiredCoverage", r(`,"requiredCoverage":[{"name":"pcie-parent","pathKind":"gpu-pcie-parent","required":true},{"name":"pcie-root","pathKind":"gpu-pcie-root","required":true},{"name":"pcie-width","pathKind":"gpu-pcie-link-width-normal","required":true},{"name":"nic-lldp","pathKind":"nic-lldp-remote","required":false}]`, ``), 7, false},
		{"trailing data", g + "x", 7, false},
		{"second value", g + "{}", 7, false},
		{"trailing NUL", g + "\x00", 7, false},
		{"trailing whitespace", g + " \t\r\n", 0, true},
		{"leading whitespace", " \r\n\t" + g, 0, true},
		{"whitespace between tokens", pretty, 0, true},
		{"free key order", reordered, 0, true},
		{"escaped key", r(`"clusterID":"lab-a"`, `"clus\u0074erID":"lab-a"`), 0, true},
		{"escaped value", r(`"clusterID":"lab-a"`, `"clusterID":"lab\u002da"`), 0, true},
		{"escaped slash in value", r(`"source":"operator/asset-db"`, `"source":"operator\/asset-db"`), 0, true},
		{"escaped value with a control character", r(`"requestID":"enroll-1"`, `"requestID":"enroll\t1"`), 7, false},
		{"escaped value outside ASCII", r(`"requestID":"enroll-1"`, `"requestID":"enroll\u00e91"`), 7, false},
	}
	gfxRunFleetCases(t, bins, a, "GFX-020", cases)
}

// TestGFX021_Fields: every row of the GFX-021 field table at its boundaries.
func TestGFX021_Fields(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	g := gfxDefaultFleet().JSON()
	r := func(old, repl string) string { return gfxReplace(t, g, old, repl) }
	claim := `"uuid":"GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"`
	cases := []gfxFleetCase{
		{"schemaVersion v2", r(`"dpa.offline-fleet/v1"`, `"dpa.offline-fleet/v2"`), 7, false},
		{"schemaVersion empty", r(`"dpa.offline-fleet/v1"`, `""`), 7, false},
		{"clusterID with space", r(`"clusterID":"lab-a"`, `"clusterID":"lab a"`), 7, false},
		{"clusterID empty", r(`"clusterID":"lab-a"`, `"clusterID":""`), 7, false},
		{"clusterID 129 bytes", r(`"clusterID":"lab-a"`, `"clusterID":"`+strings.Repeat("a", 129)+`"`), 7, false},
		{"fleet.name leading dash", r(`"name":"lab-a-gpus"`, `"name":"-lab"`), 7, false},
		{"fleet.name trailing dash", r(`"name":"lab-a-gpus"`, `"name":"lab-"`), 7, false},
		{"fleet.name upper case", r(`"name":"lab-a-gpus"`, `"name":"Lab"`), 7, false},
		{"fleet.name empty label", r(`"name":"lab-a-gpus"`, `"name":"a..b"`), 7, false},
		{"fleet.name leading dot", r(`"name":"lab-a-gpus"`, `"name":".a"`), 7, false},
		{"fleet.name trailing dot", r(`"name":"lab-a-gpus"`, `"name":"a."`), 7, false},
		{"fleet.name underscore", r(`"name":"lab-a-gpus"`, `"name":"a_b"`), 7, false},
		{"fleet.name 254 bytes", r(`"name":"lab-a-gpus"`, `"name":"`+strings.Repeat("a", 254)+`"`), 7, false},
		{"fleet.name 253 bytes", r(`"name":"lab-a-gpus"`, `"name":"`+strings.Repeat("a", 253)+`"`), 0, true},
		{"fleet.name one byte", r(`"name":"lab-a-gpus"`, `"name":"a"`), 0, true},
		{"fleet.name dotted labels", r(`"name":"lab-a-gpus"`, `"name":"a-1.b0.c"`), 0, true},
		{"fleet.uid empty", r(`"uid":"5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"`, `"uid":""`), 7, false},
		{"fleet.uid 129 bytes", r(`"uid":"5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"`, `"uid":"`+strings.Repeat("u", 129)+`"`), 7, false},
		{"fleet.uid with space", r(`"uid":"5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"`, `"uid":"a b"`), 7, false},
		{"policy.revision empty", r(`"revision":"gpu-path-v1"`, `"revision":""`), 7, false},
		{"policy.revision 129 bytes", r(`"revision":"gpu-path-v1"`, `"revision":"`+strings.Repeat("r", 129)+`"`), 7, false},
		{"freshnessSeconds 0", r(`"freshnessSeconds":60`, `"freshnessSeconds":0`), 7, false},
		{"freshnessSeconds 86401", r(`"freshnessSeconds":60`, `"freshnessSeconds":86401`), 7, false},
		{"readyForSeconds 0", r(`"readyForSeconds":30`, `"readyForSeconds":0`), 7, false},
		{"readyForSeconds 86401", r(`"readyForSeconds":30`, `"readyForSeconds":86401`), 7, false},
		{"coverage name empty", r(`"name":"nic-lldp"`, `"name":""`), 7, false},
		{"coverage name 254 bytes", r(`"name":"nic-lldp"`, `"name":"`+strings.Repeat("c", 254)+`"`), 7, false},
		{"coverage name with space", r(`"name":"nic-lldp"`, `"name":"nic lldp"`), 7, false},
		{"coverage name with slash", r(`"name":"nic-lldp"`, `"name":"nic/lldp"`), 7, false},
		{"pathKind upper case", r(`"pathKind":"nic-lldp-remote"`, `"pathKind":"NIC-LLDP-REMOTE"`), 7, false},
		{"pathKind unknown", r(`"pathKind":"nic-lldp-remote"`, `"pathKind":"nic-lldp"`), 7, false},
		{"desiredState lower case", r(`"desiredState":"InService"`, `"desiredState":"inService"`), 7, false},
		{"desiredState Unknown", r(`"desiredState":"InService"`, `"desiredState":"Unknown"`), 7, false},
		{"device name upper case", r(`"name":"gpu-node-1-gpu0"`, `"name":"GPU0"`), 7, false},
		{"device name underscore", r(`"name":"gpu-node-1-gpu0"`, `"name":"gpu_0"`), 7, false},
		{"device uid empty", r(`"uid":"0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41"`, `"uid":""`), 7, false},
		{"device uid 129 bytes", r(`"uid":"0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41"`, `"uid":"`+strings.Repeat("d", 129)+`"`), 7, false},
		{"device uid with space", r(`"uid":"0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41"`, `"uid":"a b"`), 7, false},
		{"nodeRef.name empty", r(`"name":"gpu-node-1",`, `"name":"",`), 7, false},
		{"nodeRef.name 254 bytes", r(`"name":"gpu-node-1",`, `"name":"`+strings.Repeat("n", 254)+`",`), 7, false},
		{"nodeRef.name with space", r(`"name":"gpu-node-1",`, `"name":"gpu node",`), 7, false},
		{"nodeRef.uid empty", r(`"uid":"7c9e6679-7425-40de-944b-e07fc1f90ae7"`, `"uid":""`), 7, false},
		{"requestID empty", r(`"requestID":"enroll-1"`, `"requestID":""`), 7, false},
		{"requestID 129 bytes", r(`"requestID":"enroll-1"`, `"requestID":"`+strings.Repeat("q", 129)+`"`), 7, false},
		{"metadataGeneration 0", r(`"metadataGeneration":1`, `"metadataGeneration":0`), 7, false},
		{"vendor lower case", r(`"vendor":"NVIDIA"`, `"vendor":"nvidia"`), 7, false},
		{"vendor empty", r(`"vendor":"NVIDIA"`, `"vendor":""`), 7, false},
		{"uuid upper case", r(claim, `"uuid":"GPU-5F0B1C2D-3E4F-4A5B-8C6D-7E8F9A0B1C2D"`), 7, false},
		{"uuid MIG", r(claim, `"uuid":"MIG-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"`), 7, false},
		{"uuid empty", r(claim, `"uuid":""`), 7, false},
		{"uuid and serial absent", r(claim+`,`, ``), 7, false},
		{"serial empty", r(claim, claim+`,"serial":""`), 7, false},
		{"serial 129 bytes", r(claim, claim+`,"serial":"`+strings.Repeat("s", 129)+`"`), 7, false},
		{"source empty", r(`"source":"operator/asset-db"`, `"source":""`), 7, false},
		{"source 257 bytes", r(`"source":"operator/asset-db"`, `"source":"`+strings.Repeat("s", 257)+`"`), 7, false},
		{"evidenceID empty", r(`"evidenceID":"asset-db:rack7-u12-gpu0"`, `"evidenceID":""`), 7, false},
		{"evidenceID 129 bytes", r(`"evidenceID":"asset-db:rack7-u12-gpu0"`, `"evidenceID":"`+strings.Repeat("e", 129)+`"`), 7, false},
	}
	for _, at := range []string{"2026-09-24 00:00:00Z", "2026-09-24T00:00:00z", "2026-09-24t00:00:00Z", "2026-02-30T00:00:00Z",
		"2026-09-24T24:00:00Z", "2026-09-24T00:60:00Z", "2026-09-24T00:00:60Z", "2026-09-24T00:00:00.1234567890Z",
		"2026-09-24T00:00:00", "2026-09-24T00:00:00+0900", "2026-9-24T00:00:00Z", "2026-09-24T00:00:00.Z", "", "+2026-09-24T00:00:00Z"} {
		cases = append(cases, gfxFleetCase{"intentObservedAt " + at, r(`"intentObservedAt":"2026-09-24T00:00:00Z"`, `"intentObservedAt":"`+at+`"`), 7, false})
	}
	gfxRunFleetCases(t, bins, a, "GFX-021", cases)

	// Accepted boundary values are checked end to end against the oracle.
	type variant struct {
		name string
		edit func(f *gfxFleet)
	}
	variants := []variant{
		{"fleet.uid 128 bytes", func(f *gfxFleet) { f.UID = strings.Repeat("u", 128) }},
		{"fleet.uid printable ASCII", func(f *gfxFleet) { f.UID = `!"#$%&'()*+,-./~\` }},
		{"policy.revision 128 bytes", func(f *gfxFleet) { f.Revision = strings.Repeat("r", 128) }},
		{"freshnessSeconds 1", func(f *gfxFleet) { f.Freshness = 1 }},
		{"freshnessSeconds 86400", func(f *gfxFleet) { f.Freshness = 86400 }},
		{"readyForSeconds 1", func(f *gfxFleet) { f.ReadyFor = 1 }},
		{"readyForSeconds 86400", func(f *gfxFleet) { f.ReadyFor = 86400 }},
		{"requiredCoverage empty", func(f *gfxFleet) { f.Coverage = nil }},
		{"requiredCoverage 32", func(f *gfxFleet) {
			f.Coverage = nil
			for i := 0; i < 32; i++ {
				f.Coverage = append(f.Coverage, gfxCovReq{fmt.Sprintf("c%02d", i), "gpu-pcie-parent", i%2 == 0})
			}
		}},
		{"every pathKind", func(f *gfxFleet) {
			f.Coverage = append(f.Coverage, gfxCovReq{"shared", "gpu-nic-shared-ancestor", true})
		}},
		{"coverage names at the grammar edges", func(f *gfxFleet) {
			f.Coverage = []gfxCovReq{{"-", "gpu-pcie-parent", true}, {strings.Repeat("Z", 253), "gpu-pcie-root", true}, {"a.b_C-9", "gpu-pcie-link-width-normal", false}}
		}},
		{"device name 253 bytes", func(f *gfxFleet) { f.Devices[0].Name = strings.Repeat("g", 253) }},
		{"device uid 128 bytes", func(f *gfxFleet) { f.Devices[0].UID = strings.Repeat("d", 128) }},
		{"nodeRef.name 253 bytes printable", func(f *gfxFleet) { f.Devices[0].NodeName = "N" + strings.Repeat("~", 252) }},
		{"requestID 128 bytes", func(f *gfxFleet) { f.Devices[0].RequestID = strings.Repeat("q", 128) }},
		{"metadataGeneration max", func(f *gfxFleet) { f.Devices[0].Generation = 9223372036854775807 }},
		{"desired Maintenance", func(f *gfxFleet) { f.Devices[0].Desired = "Maintenance" }},
		{"desired Retired", func(f *gfxFleet) { f.Devices[0].Desired = "Retired" }},
		{"intentObservedAt with offset", func(f *gfxFleet) { f.Devices[0].IntentAt = "2026-09-24T09:00:00+09:00" }},
		{"intentObservedAt negative zero offset", func(f *gfxFleet) { f.Devices[0].IntentAt = "2026-09-23T23:59:59.5-00:00" }},
		{"intentObservedAt nanoseconds", func(f *gfxFleet) { f.Devices[0].IntentAt = "2026-09-23T23:59:59.999999999Z" }},
		{"serial with uuid", func(f *gfxFleet) { f.Devices[0].Serial = strings.Repeat("s", 128) }},
		{"serial without uuid", func(f *gfxFleet) { f.Devices[0].UUID, f.Devices[0].Serial = "", "SN-0001" }},
		{"source 256 bytes", func(f *gfxFleet) { f.Devices[0].Source = strings.Repeat("s", 256) }},
		{"evidenceID 128 bytes", func(f *gfxFleet) { f.Devices[0].EvidenceID = strings.Repeat("e", 128) }},
	}
	for _, v := range variants {
		fl := gfxDefaultFleet()
		v.edit(&fl)
		c := gfxNewCase(t, bins, a, fl)
		c.Check("gpu", fl.Devices[0].Name, "GFX-021 accepted "+v.name)
	}
}

// TestGFX022_DomainValidation: duplicates of device name, device uid,
// present uuid and coverage name are invalid; other repeats are not.
func TestGFX022_DomainValidation(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	second := func(edit func(d *gfxDevice)) gfxFleet {
		fl := gfxDefaultFleet()
		d := gfxDefaultDevice()
		d.Name, d.UID, d.UUID, d.EvidenceID = "gpu-node-1-gpu1", "uid-gpu1", "GPU-00000000-0000-4000-8000-000000000001", "asset-db:gpu1"
		edit(&d)
		fl.Devices = append(fl.Devices, d)
		return fl
	}
	invalid := map[string]gfxFleet{
		"duplicate device name": second(func(d *gfxDevice) { d.Name = gfxDevName }),
		"duplicate device uid":  second(func(d *gfxDevice) { d.UID = gfxDevUID }),
		"duplicate uuid":        second(func(d *gfxDevice) { d.UUID = gfxUUID }),
	}
	dupCov := gfxDefaultFleet()
	dupCov.Coverage = append(dupCov.Coverage, gfxCovReq{"pcie-root", "gpu-pcie-parent", false})
	invalid["duplicate coverage name"] = dupCov
	for name, fl := range invalid {
		in := gfxWriteInputs(t, a.Raw, fl.JSON())
		gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "json")...), 7, "GFX-022 "+name)
	}
	valid := map[string]gfxFleet{
		"same serial without uuid": func() gfxFleet {
			fl := second(func(d *gfxDevice) { d.UUID, d.Serial = "", "SN-1" })
			fl.Devices[0].UUID, fl.Devices[0].Serial = "", "SN-1"
			return fl
		}(),
		"same requestID and evidenceID": second(func(d *gfxDevice) { d.RequestID, d.EvidenceID = "enroll-1", gfxClaimEvidence }),
		"no required coverage": func() gfxFleet {
			fl := gfxDefaultFleet()
			for i := range fl.Coverage {
				fl.Coverage[i].Required = false
			}
			return fl
		}(),
	}
	for name, fl := range valid {
		c := gfxNewCase(t, bins, a, fl)
		e := c.Check("gpu", gfxDevName, "GFX-022 accepted "+name)
		if name == "no required coverage" && e.Qualification != "Unknown" {
			t.Errorf("GFX-022/GFL-041A: policy without required coverage gave qualification %q, want Unknown", e.Qualification)
		}
		c.Check("node", gfoNodeName, "GFX-022 accepted "+name+" (node)")
	}
}

// TestGFX023_ArtifactAgreement: clusterID and every nodeRef.uid must match
// the artifact; a different nodeRef.name is accepted with node_name_differs.
func TestGFX023_ArtifactAgreement(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	other := gfxDefaultFleet()
	other.ClusterID = "lab-b"
	in := gfxWriteInputs(t, a.Raw, other.JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 7, "GFX-023/GFX-102 error row: fleet clusterID lab-b")
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("node", gfoNodeName, in, "")...), 7, "GFX-023 clusterID mismatch (node)")
	two := gfxDefaultFleet()
	d := gfxDefaultDevice()
	d.Name, d.UID, d.UUID, d.NodeUID = "gpu-other", "uid-other", "GPU-00000000-0000-4000-8000-000000000002", "other-node-uid"
	two.Devices = append(two.Devices, d)
	in = gfxWriteInputs(t, a.Raw, two.JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 7, "GFX-023 second device on another node uid")
	caseUID := gfxDefaultFleet()
	caseUID.Devices[0].NodeUID = strings.ToUpper(gfoNodeUID)
	in = gfxWriteInputs(t, a.Raw, caseUID.JSON())
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 7, "GFX-023 nodeRef.uid differs only in case")

	renamed := gfxDefaultFleet()
	renamed.Devices[0].NodeName = "gpu-node-1-old"
	c := gfxNewCase(t, bins, a, renamed)
	e := c.Check("gpu", gfxDevName, "GFX-023 S-NAMEDIFF")
	if !e.HasLim("node_name_differs", gfxDevName) {
		t.Errorf("GFX-023: limitations %v lack node_name_differs %s", e.Lims(), gfxDevName)
	}
	c.Check("node", gfoNodeName, "GFX-023 S-NAMEDIFF (node)")
	empty := gfxDefaultFleet()
	empty.Devices = nil
	ce := gfxNewCase(t, bins, a, empty)
	ne := ce.Check("node", gfoNodeName, "GFX-023 zero devices")
	if !ne.HasLim("no_devices", "") || ne.HasLim("allocation_unavailable", "") || len(ne.Devices) != 0 {
		t.Errorf("GFX-023/GFX-080: zero devices: limitations %v deviceSummaries %d, want no_devices without allocation_unavailable", ne.Lims(), len(ne.Devices))
	}
	gfxFail(t, ce.Run("gpu", gfxDevName, ""), 4, "GFX-024 gpu request with zero devices")
}

// TestGFX024_TargetSelection: gpu targets match devices[].name exactly; node
// targets match the artifact node name only, never nodeRef.name.
func TestGFX024_TargetSelection(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	renamed := gfxDefaultFleet()
	renamed.Devices[0].NodeName = "gpu-node-1-old"
	c := gfxNewCase(t, bins, a, renamed)
	for _, n := range []string{"GPU-NODE-1-GPU0", "gpu-node-1-gpu", "gpu-node-1-gpu00", gfoNodeName, gfxDevUID, gfxUUID} {
		gfxFail(t, c.Run("gpu", n, ""), 4, "GFX-024 gpu "+n, n)
	}
	for _, n := range []string{"gpu-node-1-old", "GPU-NODE-1", gfxDevName, gfoNodeUID, "gpu-node-1."} {
		gfxFail(t, c.Run("node", n, ""), 4, "GFX-024 node "+n, n)
	}
	c.Check("node", gfoNodeName, "GFX-024 node by artifact name")
	c.Check("gpu", gfxDevName, "GFX-024 gpu by device name")

	// An artifact node name starting with '-' cannot be a node target, but
	// gpu explain still works.
	f := gfxSBasic(t)
	f.M.NodeName = "-dash-node"
	da := gfxAgentArtifact(t, bins.Agent, f)
	fl := gfxDefaultFleet()
	fl.Devices[0].NodeName = "-dash-node"
	dc := gfxNewCase(t, bins, da, fl)
	gfxFail(t, dc.Run("node", "-dash-node", ""), 2, "GFX-024 node name starting with '-'")
	dc.Check("gpu", gfxDevName, "GFX-024 gpu on a node whose name starts with '-'")
}

// TestGFX038_FleetIntentTimeMargin: intentObservedAt must leave one day of
// room on both ends of the timestamp range.
func TestGFX038_FleetIntentTimeMargin(t *testing.T) {
	t.Parallel()
	bins := gfxBuild(t, true)
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	for _, at := range []string{"0001-01-01T00:00:00Z", "0001-01-01T00:00:00.000000001Z", "0001-01-02T00:00:00Z", "9999-12-31T00:00:00Z", "9999-12-31T23:59:59.999999999Z"} {
		fl := gfxDefaultFleet()
		fl.Devices[0].IntentAt = at
		in := gfxWriteInputs(t, a.Raw, fl.JSON())
		gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "")...), 7, "GFX-038 intentObservedAt "+at)
	}
	for _, at := range []string{"0001-01-02T00:00:00.000000001Z", "9999-12-30T23:59:59.999999999Z"} {
		fl := gfxDefaultFleet()
		fl.Devices[0].IntentAt = at
		c := gfxNewCase(t, bins, a, fl)
		c.Check("gpu", gfxDevName, "GFX-038 accepted intentObservedAt "+at)
	}
}
