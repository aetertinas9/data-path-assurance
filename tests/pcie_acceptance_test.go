package tests_test

import (
	"encoding/hex"
	"fmt"
	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestPCIE_001_PureActiveCandidates(t *testing.T) {
	w := pcieWindow(t, pcieBase())
	a := pcie.EvaluateLinkWidth(w, pcieAt(30*time.Second))
	b := pcie.EvaluateLinkWidth(w, pcieAt(30*time.Second))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("nondeterministic")
	}
	pcieCheck(t, pcieBase(), pcieAt(30*time.Second), 1)
}
func TestPCIE_002_Constants(t *testing.T) {
	var current model.SignalRef = pcie.SignalLinkWidthCurrent
	var expected model.SignalRef = pcie.SignalLinkWidthExpected
	got := []string{string(current), string(expected), pcie.UnitLinkWidth, pcie.DimensionRootCanonical, pcie.DimensionPeerCanonical, pcie.DimensionPeerKind, pcie.DimensionExpectedProvenance, pcie.ProvenanceAdjacentCapabilityMin, pcie.ProvenanceOperatorVerifiedWiring}
	want := []string{"pcie.link.width.current", "pcie.link.width.expected", "lanes", "pcie.root.canonical", "pcie.peer.canonical", "pcie.peer.kind", "pcie.expected.provenance", "adjacent_capability_min", "operator_verified_wiring"}
	var samples int = pcie.LinkWidthMinSamples
	var duration time.Duration = pcie.LinkWidthMinDuration
	var gap time.Duration = pcie.LinkWidthMaxGap
	if !reflect.DeepEqual(got, want) || samples != 3 || duration != 30*time.Second || gap != 30*time.Second {
		t.Fatalf("constants %v %d %v %v", got, samples, duration, gap)
	}
}

// PCIE-003 requires dependency closure and negative dependency injection checks.
// Assigned to integration's make arch-check and verifier; source inspection is
// forbidden in this physically isolated test-author worktree.
func TestPCIE_003_ArchitectureIntegrationObligation(t *testing.T) {
	t.Skip("structural AC: integration must run make arch-check and prohibited direct/transitive dependency cases")
}
func TestPCIE_004_EmptyAndIndependentFailure(t *testing.T) {
	if len(pcie.EvaluateLinkWidth(evidence.Window{}, pcieAt(0))) != 0 {
		t.Fatal("zero window")
	}
	if len(pcie.EvaluateLinkWidth(pcieWindow(t, pcieBase()), time.Time{})) != 0 {
		t.Fatal("zero now")
	}
	pcieCheck(t, nil, pcieAt(0), 0)
	bad := pcieBase()
	for i := range bad {
		bad[i].Subject.Canonical = "device:bad"
		bad[i].Dimensions["extra"] = "bad"
	}
	pcieCheck(t, append(pcieBase(), bad...), pcieAt(30*time.Second), 1)
}
func TestPCIE_005_InterestAndIdentity(t *testing.T) {
	for _, kind := range []model.AssetKind{model.KindPCIeSwitch, model.KindKubernetesNode} {
		obs := pcieBase()
		for i := range obs {
			obs[i].Subject.Kind = kind
		}
		pcieCheck(t, obs, pcieAt(30*time.Second), 0)
	}
	extra := pciePairs(31 * time.Second)
	for i := range extra {
		extra[i].Signal = "other.signal"
		extra[i].Dimensions = nil
	}
	pcieCheck(t, append(pcieBase(), extra...), pcieAt(30*time.Second), 1)
	obs := pcieBase()
	for i := 2; i < len(obs); i++ {
		obs[i].Subject.Canonical = "device:other"
	}
	pcieCheck(t, obs, pcieAt(30*time.Second), 0)
}
func TestPCIE_006_007_009_DimensionsAndProvenance(t *testing.T) {
	cases := []struct {
		name, key, value string
		drop             bool
	}{
		{"missing", "pcie.root.canonical", "", true}, {"extra", "extra", "x", false}, {"root-empty-ns", "pcie.root.canonical", ":x", false}, {"root-empty-value", "pcie.root.canonical", "x:", false}, {"root-no-colon", "pcie.root.canonical", "root", false},
		{"peer-self", "pcie.peer.kind", "PCIeFunction", false}, {"peer-root-mismatch", "pcie.peer.canonical", "pci-bdf:other", false}, {"kind-space", "pcie.peer.kind", "PCIeSwitch ", false}, {"kind-case", "pcie.peer.kind", "pcieswitch", false}, {"provenance", "pcie.expected.provenance", "learned", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := pcieBase()
			for i := range obs {
				if tc.drop {
					delete(obs[i].Dimensions, tc.key)
				} else {
					obs[i].Dimensions[tc.key] = tc.value
				}
				if tc.name == "peer-self" {
					obs[i].Dimensions["pcie.peer.canonical"] = obs[i].Subject.Canonical
				}
			}
			pcieCheck(t, obs, pcieAt(30*time.Second), 0)
		})
	}
	for _, kind := range []string{"PCIeSwitch", "PCIeFunction", "PCIeRootPort"} {
		t.Run("valid-"+kind, func(t *testing.T) {
			obs := pcieBase()
			for i := range obs {
				obs[i].Dimensions["pcie.peer.kind"] = kind
				obs[i].Dimensions["pcie.root.canonical"] = "open:ROOT:value"
				obs[i].Dimensions["pcie.peer.canonical"] = "open:ROOT:value"
			}
			pcieCheck(t, obs, pcieAt(30*time.Second), 1)
		})
	}
	obs := pcieBase()
	for i := range obs {
		obs[i].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
	}
	pcieCheck(t, obs, pcieAt(30*time.Second), 1)
}
func TestPCIE_008_009_NumericUnitQuality(t *testing.T) {
	for name, v := range map[string]model.Value{"zero": model.NewIntValue(0), "negative": model.NewIntValue(-1), "fraction": model.NewFloatValue(7.5), "bool": model.NewBoolValue(true), "string": model.NewStringValue("8")} {
		t.Run(name, func(t *testing.T) { obs := pcieBase(); obs[4].Value = v; pcieCheck(t, obs, pcieAt(30*time.Second), 0) })
	}
	for _, unit := range []string{"Lanes", "lane", "", " lanes"} {
		obs := pcieBase()
		obs[4].Unit = unit
		pcieCheck(t, obs, pcieAt(30*time.Second), 0)
	}
	for _, quality := range []model.EvidenceQuality{model.QualityUnknown, model.QualityDegraded} {
		for _, idx := range []int{4, 5} {
			obs := pcieBase()
			obs[idx].Quality = quality
			pcieCheck(t, obs, pcieAt(30*time.Second), 0)
		}
	}
	for _, tc := range []struct {
		cur, exp model.Value
		want     int
	}{
		{model.NewIntValue(9007199254740992), model.NewIntValue(9007199254740993), 1},
		{model.NewFloatValue(9007199254740992), model.NewIntValue(9007199254740993), 1},
		{model.NewIntValue(9007199254740993), model.NewFloatValue(9007199254740992), 0},
		{model.NewFloatValue(1e20), model.NewFloatValue(1e21), 1},
		{model.NewIntValue(3), model.NewIntValue(7), 1},
	} {
		obs := pcieBase()
		for i := range obs {
			if i%2 == 0 {
				obs[i].Value = tc.cur
			} else {
				obs[i].Value = tc.exp
			}
		}
		pcieCheck(t, obs, pcieAt(30*time.Second), tc.want)
	}
}
func TestPCIE_010_InstantOrderAndRetention(t *testing.T) {
	obs := pcieBase()
	want := pcieCheck(t, obs, pcieAt(30*time.Second), 1)
	for i := range obs {
		obs[i].ObservedAt = obs[i].ObservedAt.In(time.FixedZone("shift", 3600))
		obs[i].ReceivedAt = pcieAt(time.Duration(100-i) * time.Hour)
		obs[i].Sequence = uint64(100 - i)
		obs[i].RawDigest = "irrelevant"
	}
	for i, j := 0, len(obs)-1; i < j; i, j = i+1, j-1 {
		obs[i], obs[j] = obs[j], obs[i]
	}
	got := pcieCheck(t, obs, pcieAt(30*time.Second), 1)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("metadata or arrival order affected output")
	}
	w := pcieWindow(t, pcieBase())
	w, err := w.Prune(pcieAt(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(pcie.EvaluateLinkWidth(w, pcieAt(30*time.Second))) != 0 {
		t.Fatal("restored pruned history")
	}
	for _, cfg := range []evidence.Config{{MaxAge: 60 * time.Second, Horizon: 120 * time.Second, MaxSamples: 2}, {MaxAge: 60 * time.Second, Horizon: 29 * time.Second, MaxSamples: 16}} {
		w := pcieWindow(t, pcieBase(), cfg)
		if len(pcie.EvaluateLinkWidth(w, pcieAt(30*time.Second))) != 0 {
			t.Fatal("restored evicted history")
		}
	}
}
func TestPCIE_011_012_BatchAndLatestBlocking(t *testing.T) {
	for _, name := range []string{"missing", "mismatch", "duplicate", "future", "malformed", "cross-source", "near-time"} {
		t.Run(name, func(t *testing.T) {
			obs := pcieBase()
			now := pcieAt(30 * time.Second)
			switch name {
			case "missing":
				obs = obs[:5]
			case "mismatch":
				obs[5].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
			case "duplicate":
				o := pciePairs(30 * time.Second)[0]
				o.Dimensions["extra"] = "x"
				obs = append(obs, o)
			case "future":
				obs = append(obs, pciePairs(31 * time.Second)[0])
			case "malformed":
				o := pciePairs(31 * time.Second)
				for i := range o {
					delete(o[i].Dimensions, "pcie.root.canonical")
				}
				obs = append(obs, o...)
				now = pcieAt(31 * time.Second)
			case "cross-source":
				obs[5].Source.Name = "B"
			case "near-time":
				obs[5].ObservedAt = pcieAt(30*time.Second + time.Nanosecond)
				now = obs[5].ObservedAt
			}
			pcieCheck(t, obs, now, 0)
		})
	}
	// An invalid older batch breaks history, even when a different dimensions series contains the valid pairs.
	obs := pciePairs(0, 30*time.Second, 45*time.Second)
	bad := pciePairs(15 * time.Second)[0]
	bad.Unit = "bad"
	pcieCheck(t, append(obs, bad), pcieAt(45*time.Second), 0)
}
func TestPCIE_013_Freshness(t *testing.T) {
	pcieCheck(t, pcieBase(), pcieAt(90*time.Second-time.Nanosecond), 1)
	pcieCheck(t, pcieBase(), pcieAt(90*time.Second), 0)
	for _, idx := range []int{4, 5} {
		obs := pcieBase()
		obs[idx].ExpiresAt = pcieAt(31 * time.Second)
		pcieCheck(t, obs, pcieAt(31*time.Second-time.Nanosecond), 1)
		pcieCheck(t, obs, pcieAt(31*time.Second), 0)
	}
	obs := pciePairs(0, 30*time.Second, 60*time.Second)
	pcieCheck(t, obs, pcieAt(60*time.Second), 1)
}
func TestPCIE_014_017_018_SourceIsolationConflictAndChoice(t *testing.T) {
	cases := []struct {
		name            string
		a, b            []time.Duration
		now             time.Duration
		count, evidence int
		source          string
	}{
		{"split", []time.Duration{0, 15 * time.Second}, []time.Duration{30 * time.Second}, 30 * time.Second, 0, 0, ""},
		{"return", []time.Duration{0, 15 * time.Second, 30 * time.Second, 60 * time.Second}, []time.Duration{45 * time.Second}, 60 * time.Second, 1, 8, "A"},
		{"async", []time.Duration{0, 30 * time.Second, 60 * time.Second}, []time.Duration{15 * time.Second, 45 * time.Second, 75 * time.Second}, 75 * time.Second, 1, 6, "B"},
		{"tie", []time.Duration{0, 15 * time.Second, 30 * time.Second}, []time.Duration{0, 15 * time.Second, 30 * time.Second}, 30 * time.Second, 1, 6, "A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := pciePairs(tc.a...), pciePairs(tc.b...)
			for i := range b {
				b[i].Source.Name = "B"
				b[i].ID = "B-" + b[i].ID
			}
			got := pcieCheck(t, append(b, a...), pcieAt(tc.now), tc.count)
			if tc.count > 0 {
				if len(got[0].Evidence) != tc.evidence {
					t.Fatal("wrong source suffix")
				}
				for _, e := range got[0].Evidence {
					isB := len(e.ObservationID) > 2 && e.ObservationID[:2] == "B-"
					if isB != (tc.source == "B") {
						t.Fatal("source evidence mixed")
					}
				}
			}
		})
	}
	for _, name := range []string{"healthy", "baseline", "path", "provenance", "different-current", "stale", "invalid-old", "invalid-latest", "stale-latest"} {
		t.Run(name, func(t *testing.T) {
			b := pciePairs(29 * time.Second)
			want := 0
			for i := range b {
				b[i].Source.Name = "B"
				b[i].ID = "B-" + b[i].ID
				switch name {
				case "healthy":
					b[i].Value = model.NewIntValue(16)
				case "baseline":
					if i%2 == 1 {
						b[i].Value = model.NewIntValue(12)
					}
				case "path":
					b[i].Dimensions["pcie.root.canonical"] = "root:other"
					b[i].Dimensions["pcie.peer.canonical"] = "root:other"
				case "provenance":
					b[i].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
				case "different-current":
					if i%2 == 0 {
						b[i].Value = model.NewIntValue(4)
					}
					want = 1
				case "stale":
					b[i].ExpiresAt = pcieAt(30 * time.Second)
					want = 1
				case "invalid-old":
					b[i].Unit = "bad"
					want = 1
				case "invalid-latest":
					b[i].ObservedAt = pcieAt(30 * time.Second)
					b[i].Unit = "bad"
				case "stale-latest":
					b[i].ObservedAt = pcieAt(30 * time.Second)
					b[i].ExpiresAt = pcieAt(31 * time.Second)
				}
			}
			now := pcieAt(30 * time.Second)
			if name == "stale-latest" {
				now = pcieAt(31 * time.Second)
			}
			pcieCheck(t, append(pcieBase(), b...), now, want)
		})
	}
}
func TestPCIE_015_016_SuffixBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		times []time.Duration
		want  int
	}{{"two", []time.Duration{0, 30 * time.Second}, 0}, {"short", []time.Duration{0, 10 * time.Second, 20 * time.Second}, 0}, {"duration-below", []time.Duration{0, 15 * time.Second, 30*time.Second - time.Nanosecond}, 0}, {"gap-inclusive", []time.Duration{0, 30 * time.Second, 60 * time.Second}, 1}, {"gap-exceeded", []time.Duration{0, 30 * time.Second, 60*time.Second + time.Nanosecond}, 0}} {
		t.Run(tc.name, func(t *testing.T) { pcieCheck(t, pciePairs(tc.times...), pcieAt(tc.times[len(tc.times)-1]), tc.want) })
	}
	for _, name := range []string{"recovery", "middle-recovery", "root", "peer", "peer-kind", "provenance", "baseline", "float-equivalent", "current-change", "precision-baseline"} {
		t.Run(name, func(t *testing.T) {
			obs := pcieBase()
			want := 0
			switch name {
			case "recovery":
				obs[4].Value = model.NewIntValue(16)
			case "middle-recovery":
				obs[2].Value = model.NewIntValue(16)
			case "root":
				for i := 4; i < 6; i++ {
					obs[i].Dimensions["pcie.root.canonical"] = "root:new"
					obs[i].Dimensions["pcie.peer.canonical"] = "root:new"
				}
			case "peer":
				for i := range obs {
					obs[i].Dimensions["pcie.peer.kind"] = "PCIeSwitch"
				}
				for i := 4; i < 6; i++ {
					obs[i].Dimensions["pcie.peer.canonical"] = "switch:new"
				}
			case "peer-kind":
				for i := 4; i < 6; i++ {
					obs[i].Dimensions["pcie.peer.kind"] = "PCIeSwitch"
				}
			case "provenance":
				for i := 4; i < 6; i++ {
					obs[i].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
				}
			case "baseline":
				obs[5].Value = model.NewIntValue(12)
			case "float-equivalent":
				obs[3].Value = model.NewFloatValue(16)
				obs[4].Value = model.NewFloatValue(8)
				want = 1
			case "current-change":
				obs[2].Value = model.NewIntValue(4)
				want = 1
			case "precision-baseline":
				for i := 1; i < 6; i += 2 {
					obs[i].Value = model.NewIntValue(9007199254740993)
				}
				obs[3].Value = model.NewFloatValue(9007199254740992)
			}
			pcieCheck(t, obs, pcieAt(30*time.Second), want)
		})
	}
	obs := pciePairs(0, 15*time.Second, 30*time.Second, 45*time.Second)
	for i := 2; i < 4; i++ {
		obs[i].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
	}
	pcieCheck(t, obs, pcieAt(45*time.Second), 0)
}
func TestPCIE_019_025_AcceptanceExamples(t *testing.T) {
	for _, width := range []int64{8, 16, 32} {
		obs := pcieBase()
		for i := range obs {
			obs[i].Value = model.NewIntValue(width)
		}
		pcieCheck(t, obs, pcieAt(30*time.Second), 0)
	}
	obs := pcieBase()
	for i := range obs {
		obs[i].Value = model.NewIntValue(8)
		obs[i].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
	}
	pcieCheck(t, obs, pcieAt(30*time.Second), 0)
	f := pcieCheck(t, pcieBase(), pcieAt(30*time.Second), 1)[0]
	if len(f.Evidence) != 6 || !f.FirstSeen.Equal(pcieAt(0)) || !f.LastSeen.Equal(pcieAt(30*time.Second)) || len(f.Scope) == 0 || f.Explanation == "" {
		t.Fatal("active example output")
	}
}
func TestPCIE_020_021_022_023_ExactOutput(t *testing.T) {
	for _, provenance := range []string{"adjacent_capability_min", "operator_verified_wiring"} {
		t.Run(provenance, func(t *testing.T) {
			obs := pcieBase()
			alias := []model.TypedID{{Namespace: "display", Value: "GPU", Raw: []byte{1, 2}, Source: "inventory"}, {Namespace: "label", Value: "second", Raw: []byte{3}, Source: "other"}}
			for i := range obs {
				obs[i].Dimensions["pcie.expected.provenance"] = provenance
				obs[i].Dimensions["pcie.peer.kind"] = "PCIeSwitch"
				obs[i].Dimensions["pcie.peer.canonical"] = "switch:upstream"
				obs[i].Source.Type = "a\"type"
				obs[i].Source.Name = "line\nname"
			}
			obs[4].Subject.Aliases = alias
			obs[5].Subject.Aliases = []model.TypedID{{Namespace: "wrong", Value: "expected"}}
			f := pcieCheck(t, obs, pcieAt(30*time.Second), 1)[0]
			root := model.AssetRef{Kind: model.KindPCIeRootPort, Canonical: obs[4].Dimensions["pcie.root.canonical"]}
			peer := model.AssetRef{Kind: model.KindPCIeSwitch, Canonical: "switch:upstream"}
			scope := []model.AssetRef{obs[4].Subject, root, peer}
			sort.Slice(scope, func(i, j int) bool { return scope[i].Key() < scope[j].Key() })
			if !reflect.DeepEqual(f.Scope, scope) {
				t.Fatalf("scope %+v want %+v", f.Scope, scope)
			}
			h := func(s string) string { return hex.EncodeToString([]byte(s)) }
			id := "pcie-width-v1:" + h(obs[4].Subject.Key()) + ":" + h(root.Key()) + ":" + h(peer.Key())
			if f.ID != id {
				t.Fatalf("ID %q want %q", f.ID, id)
			}
			if f.FirstSeen != pcieAt(0) || f.LastSeen != pcieAt(30*time.Second) {
				t.Fatal("timestamps must be stripped UTC")
			}
			var refs []model.EvidenceRef
			for i, o := range obs {
				v := "8"
				if i%2 == 1 {
					v = "16"
				}
				refs = append(refs, model.EvidenceRef{ObservationID: o.ID, Summary: string(o.Signal) + "=" + v + " lanes"})
			}
			if !reflect.DeepEqual(f.Evidence, refs) {
				t.Fatalf("evidence %+v want %+v", f.Evidence, refs)
			}
			basis, limit, step := "adapter-supplied minimum of adjacent endpoint maximum widths", "capability does not establish verified wiring width", "Verify the adjacent link wiring baseline and inspect both link endpoints in audit mode."
			if provenance == "operator_verified_wiring" {
				basis = "adapter-supplied operator-verified wiring width"
				limit = "operator verification is asserted by the adapter and not revalidated by this rule"
				step = "Recheck the verified wiring baseline and inspect both link endpoints in audit mode."
			}
			q := strconv.Quote
			want := fmt.Sprintf("PCIe link width degraded: subject=%s; peer=%s; root=%s; current=8 lanes; expected=16 lanes; samples=3; first=%s; last=%s; source_type=%s; source_name=%s; provenance=%s; basis=%s; limitation=%s.", q(obs[4].Subject.Key()), q(peer.Key()), q(root.Key()), pcieAt(0).Format(time.RFC3339Nano), pcieAt(30*time.Second).Format(time.RFC3339Nano), q(obs[4].Source.Type), q(obs[4].Source.Name), q(provenance), basis, limit)
			if f.Explanation != want || f.SuggestedStep != step {
				t.Fatalf("explanation %q want %q; step %q", f.Explanation, want, f.SuggestedStep)
			}
		})
	}
	obs := pcieBase()
	for i := range obs {
		obs[i].ID = "same"
	}
	f := pcieCheck(t, obs, pcieAt(30*time.Second), 1)[0]
	if len(f.Evidence) != 6 || len(f.Scope) != 2 {
		t.Fatal("deduplication of evidence or scope incorrect")
	}
}
func TestPCIE_024_DeepCopiesAndSort(t *testing.T) {
	obs := pcieBase()
	alias := []model.TypedID{{Namespace: "name", Value: "gpu", Raw: []byte("raw"), Source: "input"}}
	for i := range obs {
		obs[i].Subject.Aliases = alias
	}
	other := pcieBase()
	for i := range other {
		other[i].Subject.Canonical = "device:z"
		other[i].Subject.Aliases = alias
	}
	obs = append(other, obs...)
	w := pcieWindow(t, obs)
	now := pcieAt(30 * time.Second)
	got := pcie.EvaluateLinkWidth(w, now)
	want := pcie.EvaluateLinkWidth(w, now)
	if len(got) != 2 || got[0].ID >= got[1].ID {
		t.Fatalf("sort/count: %+v", got)
	}
	alias[0].Raw[0] = 'X'
	alias[0].Source = "changed"
	obs[0].Dimensions["pcie.root.canonical"] = "changed:input"
	if !reflect.DeepEqual(got, want) {
		t.Fatal("input aliases changed finding")
	}
	for i := range got[0].Scope {
		if len(got[0].Scope[i].Aliases) > 0 {
			got[0].Scope[i].Aliases[0].Raw[0] = 'Y'
			got[0].Scope[i].Aliases[0].Source = "changed"
		}
		got[0].Scope[i].Canonical = "changed:output"
	}
	got[0].Evidence[0].Summary = "changed"
	got[0].ID = "changed"
	if !reflect.DeepEqual(got[1], want[1]) || !reflect.DeepEqual(pcie.EvaluateLinkWidth(w, now), want) {
		t.Fatal("return aliases input or another finding")
	}
}

func FuzzPCIeLinkWidthDeterminism(f *testing.F) {
	f.Add([]byte{8, 16, 8, 16, 8, 16})
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 32 {
			data = data[:32]
		}
		obs := pcieBase()
		for i, b := range data {
			at := time.Duration(i+3) * 15 * time.Second
			pair := pciePairs(at)
			pair[0].Value = model.NewIntValue(int64(b % 33))
			pair[1].Value = model.NewIntValue(int64((b / 3) % 33))
			if b&1 != 0 {
				pair[0].Quality = model.QualityUnknown
			}
			obs = append(obs, pair...)
		}
		cfg := evidence.Config{MaxAge: 60 * time.Second, Horizon: time.Hour, MaxSamples: 40}
		w := pcieWindow(t, obs, cfg)
		now := obs[len(obs)-1].ObservedAt
		a := pcie.EvaluateLinkWidth(w, now)
		b := pcie.EvaluateLinkWidth(w, now)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("PCIE-001/024 nondeterminism")
		}
		for _, finding := range a {
			if err := finding.Validate(); err != nil {
				t.Fatal(err)
			}
			if finding.Type != model.FindingPCIeLinkWidthDegraded || finding.State != model.StateActive {
				t.Fatal("PCIE-019 output")
			}
		}
	})
}
