package tests_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

func TestPCIE_011_012_DuplicateExpectedWithValidAlternatePath(t *testing.T) {
	obs := pcieBase()
	duplicate := pciePairs(30 * time.Second)[1]
	duplicate.ID = "alternate-path-expected"
	duplicate.Dimensions["pcie.root.canonical"] = "root:alternate"
	duplicate.Dimensions["pcie.peer.canonical"] = "root:alternate"
	obs = append(obs, duplicate)
	for _, o := range obs {
		if err := o.Validate(); err != nil {
			t.Fatalf("invalid fixture observation %s: %v", o.ID, err)
		}
	}
	// The alternate dimensions form a distinct admissible evidence series;
	// pcieWindow requires every Add to succeed before evaluating the rule.
	w := pcieWindow(t, obs)
	if got := pcie.EvaluateLinkWidth(w, pcieAt(30*time.Second)); len(got) != 0 {
		t.Fatalf("latest batch has two expected observations: %+v", got)
	}
}

func TestPCIE_015_016_021_022_PathReturnRestartsHistory(t *testing.T) {
	obs := pcieBase()
	interruption := pciePairs(45 * time.Second)
	for i := range interruption {
		interruption[i].Dimensions["pcie.root.canonical"] = "root:alternate"
		interruption[i].Dimensions["pcie.peer.canonical"] = "root:alternate"
	}
	obs = append(obs, interruption...)
	renewed := pciePairs(60*time.Second, 75*time.Second)
	obs = append(obs, renewed...)
	pcieCheck(t, obs, pcieAt(75*time.Second), 0)
	last := pciePairs(90 * time.Second)
	obs = append(obs, last...)
	renewed = append(renewed, last...)
	finding := pcieCheck(t, obs, pcieAt(90*time.Second), 1)[0]
	pcieHistoryExactSuffix(t, finding, renewed, 60*time.Second, 90*time.Second)
}

func TestPCIE_013_014_018_021_022_AsynchronousSourceExactHistory(t *testing.T) {
	a := pciePairs(0, 30*time.Second, 60*time.Second)
	b := pciePairs(15*time.Second, 45*time.Second, 75*time.Second)
	for i := range b {
		b[i].Source.Name = "B"
		b[i].ID = "B-" + b[i].ID
	}
	// pcieWindow's explicit default MaxAge is 60 seconds. A's latest head
	// agrees with B while only B ends at the subject's latest instant.
	finding := pcieCheck(t, append(a, b...), pcieAt(75*time.Second), 1)[0]
	pcieHistoryExactSuffix(t, finding, b, 15*time.Second, 75*time.Second)
}

func pcieHistoryExactSuffix(t *testing.T, finding model.Finding, observations []model.Observation, first, last time.Duration) {
	t.Helper()
	if err := finding.Validate(); err != nil {
		t.Fatal(err)
	}
	if finding.FirstSeen != pcieAt(first) || finding.LastSeen != pcieAt(last) {
		t.Fatalf("suffix times %v/%v, want %v/%v", finding.FirstSeen, finding.LastSeen, pcieAt(first), pcieAt(last))
	}
	want := make([]model.EvidenceRef, 0, len(observations))
	for i, o := range observations {
		value := "8"
		if i%2 == 1 {
			value = "16"
		}
		want = append(want, model.EvidenceRef{ObservationID: o.ID, Summary: string(o.Signal) + "=" + value + " lanes"})
	}
	if !reflect.DeepEqual(finding.Evidence, want) {
		t.Fatalf("suffix evidence %+v, want exactly %+v", finding.Evidence, want)
	}
}
