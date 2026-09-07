package tests_test

import (
	"fmt"
	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/evidence"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
	"testing"
	"time"
)

var pcieEpoch = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)

func pcieAt(s time.Duration) time.Time { return pcieEpoch.Add(s) }
func pcieDims() map[string]string {
	return map[string]string{
		"pcie.root.canonical": "pci-bdf:0000:00:01.0", "pcie.peer.canonical": "pci-bdf:0000:00:01.0",
		"pcie.peer.kind": "PCIeRootPort", "pcie.expected.provenance": "adjacent_capability_min",
	}
}
func pciePairs(times ...time.Duration) []model.Observation {
	var out []model.Observation
	for _, at := range times {
		for i, sig := range []model.SignalRef{"pcie.link.width.current", "pcie.link.width.expected"} {
			out = append(out, model.Observation{ID: fmt.Sprintf("%d-%d", at, i), Source: model.SourceRef{Type: "agent", Name: "A"}, Subject: model.AssetRef{Kind: model.KindPCIeFunction, Canonical: "pci-bdf:0000:af:00.0"}, Signal: sig, Value: model.NewIntValue(int64(8 + 8*i)), Unit: "lanes", Dimensions: pcieDims(), ObservedAt: pcieAt(at), ReceivedAt: pcieAt(at), Quality: model.QualityGood})
		}
	}
	return out
}
func pcieBase() []model.Observation { return pciePairs(0, 15*time.Second, 30*time.Second) }
func pcieWindow(t testing.TB, obs []model.Observation, configs ...evidence.Config) evidence.Window {
	t.Helper()
	cfg := evidence.Config{MaxAge: 60 * time.Second, Horizon: 120 * time.Second, MaxSamples: 32}
	if len(configs) > 0 {
		cfg = configs[0]
	}
	w, err := evidence.NewWindow(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		w, err = w.Add(o)
		if err != nil {
			t.Fatalf("fixture Add %s: %v", o.ID, err)
		}
	}
	return w
}
func pcieCheck(t testing.TB, obs []model.Observation, now time.Time, n int) []model.Finding {
	t.Helper()
	got := pcie.EvaluateLinkWidth(pcieWindow(t, obs), now)
	if len(got) != n {
		t.Fatalf("findings=%+v; want count %d", got, n)
	}
	for _, f := range got {
		if err := f.Validate(); err != nil {
			t.Fatalf("invalid finding: %v", err)
		}
		if f.Type != model.FindingPCIeLinkWidthDegraded || f.Severity != model.SeverityWarning || f.Confidence != model.ConfidenceMedium || f.State != model.StateActive || len(f.MissingInputs) != 0 || len(f.Affected) != 0 {
			t.Fatalf("invalid active output: %+v", f)
		}
	}
	return got
}
