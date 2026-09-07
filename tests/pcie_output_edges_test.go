package tests_test

import (
	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPCIE_018_020_SourceTupleAndCurrentAlias(t *testing.T) {
	for _, types := range [][2]string{{"agent", "agent"}, {"agent", "gnmi"}} {
		a, b := pcieBase(), pcieBase()
		for i := range a {
			a[i].Source = model.SourceRef{Type: types[0], Name: "z"}
			a[i].ID = "chosen-" + a[i].ID
			b[i].Source = model.SourceRef{Type: types[1], Name: "zz"}
			b[i].ID = "other-" + b[i].ID
			if types[0] != types[1] {
				b[i].Source.Name = "a"
			}
			b[i].Subject.Aliases = []model.TypedID{{Namespace: "alias", Value: "wrong-source"}}
		}
		chosen := []model.TypedID{{Namespace: "alias", Value: "current", Raw: []byte{7, 8}, Source: "chosen"}}
		a[4].Subject.Aliases = chosen
		a[5].Subject.Aliases = []model.TypedID{{Namespace: "alias", Value: "wrong-expected"}}
		unrelated := pciePairs(40 * time.Second)[0]
		unrelated.Signal = "a.unrelated"
		unrelated.Subject.Aliases = []model.TypedID{{Namespace: "alias", Value: "wrong-signal"}}
		obs := append(append(b, a...), unrelated)
		f := pcieCheck(t, obs, pcieAt(40*time.Second), 1)[0]
		for _, e := range f.Evidence {
			if !strings.HasPrefix(e.ObservationID, "chosen-") {
				t.Fatal("tuple tie-break mixed source")
			}
		}
		for _, s := range f.Scope {
			if s.Kind == model.KindPCIeFunction && !reflect.DeepEqual(s.Aliases, chosen) {
				t.Fatalf("alias from wrong observation: %+v", s)
			}
		}
	}
}

func TestPCIE_012_LatestSourceMustQualify(t *testing.T) {
	a, b := pcieBase(), pciePairs(31*time.Second)
	for i := range b {
		b[i].Source.Name = "B"
	}
	pcieCheck(t, append(a, b...), pcieAt(31*time.Second), 0)
}

func TestPCIE_015_021_022_MaximalSuffixAndStableID(t *testing.T) {
	obs := pciePairs(0, 15*time.Second, 30*time.Second, 45*time.Second, 60*time.Second, 75*time.Second)
	obs[2].Value = model.NewIntValue(16)
	f := pcieCheck(t, obs, pcieAt(75*time.Second), 1)[0]
	if !f.FirstSeen.Equal(pcieAt(30*time.Second)) || !f.LastSeen.Equal(pcieAt(75*time.Second)) || len(f.Evidence) != 8 {
		t.Fatalf("maximal suffix %+v", f)
	}
	for _, e := range f.Evidence {
		if e.ObservationID == obs[0].ID || e.ObservationID == obs[1].ID || e.ObservationID == obs[2].ID || e.ObservationID == obs[3].ID {
			t.Fatal("pre-break evidence leaked")
		}
	}
	base := pcieCheck(t, pcieBase(), pcieAt(30*time.Second), 1)[0]
	if base.ID != f.ID {
		t.Fatal("time/current changed ID")
	}
	for _, change := range []string{"source", "provenance", "baseline", "alias", "path"} {
		obs := pcieBase()
		for i := range obs {
			switch change {
			case "source":
				obs[i].Source = model.SourceRef{Type: "custom", Name: "new"}
			case "provenance":
				obs[i].Dimensions["pcie.expected.provenance"] = "operator_verified_wiring"
			case "baseline":
				if i%2 == 1 {
					obs[i].Value = model.NewIntValue(32)
				}
			case "alias":
				obs[i].Subject.Aliases = []model.TypedID{{Namespace: "alias", Value: "new"}}
			case "path":
				obs[i].Dimensions["pcie.peer.kind"] = "PCIeSwitch"
			}
			obs[i].ID = "changed-" + obs[i].ID
		}
		got := pcieCheck(t, obs, pcieAt(31*time.Second), 1)[0]
		if (got.ID == base.ID) == (change == "path") {
			t.Fatalf("ID dependence wrong for %s", change)
		}
	}
}

func TestPCIE_008_022_023_ExactDecimalRendering(t *testing.T) {
	for _, tc := range []struct {
		cur, exp model.Value
		c, e     string
	}{
		{model.NewIntValue(9007199254740992), model.NewIntValue(9007199254740993), "9007199254740992", "9007199254740993"},
		{model.NewFloatValue(1e20), model.NewFloatValue(1e21), "100000000000000000000", "1000000000000000000000"},
	} {
		obs := pcieBase()
		for i := range obs {
			if i%2 == 0 {
				obs[i].Value = tc.cur
			} else {
				obs[i].Value = tc.exp
			}
		}
		f := pcieCheck(t, obs, pcieAt(30*time.Second), 1)[0]
		for i, e := range f.Evidence {
			want := "pcie.link.width.current=" + tc.c + " lanes"
			if i%2 == 1 {
				want = "pcie.link.width.expected=" + tc.e + " lanes"
			}
			if e.Summary != want {
				t.Fatalf("decimal %q want %q", e.Summary, want)
			}
		}
		if !strings.Contains(f.Explanation, "current="+tc.c+" lanes; expected="+tc.e+" lanes;") {
			t.Fatal("explanation rounded or scientific notation")
		}
	}
}

func TestPCIE_010_MonotonicRepresentation(t *testing.T) {
	// The clock supplies only a fixture instant; the oracle compares equivalent
	// representations and does not depend on the wall-clock value.
	anchor := time.Now()
	obs := pcieBase()
	for i := range obs {
		obs[i].ObservedAt = anchor.Add(time.Duration(i/2) * 15 * time.Second)
	}
	w := pcieWindow(t, obs)
	now := anchor.Add(30 * time.Second)
	got := pcie.EvaluateLinkWidth(w, now)
	for i := range obs {
		obs[i].ObservedAt = obs[i].ObservedAt.Round(0).UTC()
	}
	want := pcie.EvaluateLinkWidth(pcieWindow(t, obs), now.Round(0).UTC())
	if len(got) != 1 || !reflect.DeepEqual(got, want) || got[0].FirstSeen != anchor.Round(0).UTC() || got[0].LastSeen != now.Round(0).UTC() {
		t.Fatal("monotonic/location affected result")
	}
}
