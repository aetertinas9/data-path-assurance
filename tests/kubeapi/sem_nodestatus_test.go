package kubeapi_test

// GKA-076/077 (NodePathState status and conditions), GKA-082 (ordering and
// truncation of every list), GKA-074(a) and GKA-105 (foreign entries and
// never-written fields are preserved).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// GKA-076 (node decision table) and GKA-077 (NodeEligible / EvidenceFresh): the
// eligibility, reason and qualification of the node decision reach NodeEligible and
// the Node condition; EvidenceFresh is True only for a decided, non-Unknown node,
// otherwise Unknown (PartialSnapshot for a Partial observation) and never False.
func TestGKA076_077_111_NodeDecisionRowsAndEvidenceFresh(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	script := newGkaSScript(gkaSPlanReady)
	nodes := map[string]*corev1.Node{}
	for _, k := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7"} {
		nodes[k] = w.selNode(k)
	}
	w.device("d1", nodes["n1"], f)
	w.device("d2", nodes["n2"], f)
	w.device("d3", nodes["n3"], f, gkaSWithDesired("Maintenance"))
	w.device("d4", nodes["n4"], f)
	w.device("d5", nodes["n5"], f)
	w.device("d6", nodes["n6"], f)
	d71 := w.device("d71", nodes["n7"], f)
	d72 := w.device("d72", nodes["n7"], f)

	script.SetNode(nodes["n2"].Name, gkaSPlanDisqualified)
	script.SetNode(nodes["n4"].Name, gkaSEdit(gkaSPlanUnknown("Validating"), func(_ app.NodeAssessmentRequest, na *app.NodeAssessment) { na.Node.Reason = "EvidenceStale" }))
	script.SetNode(nodes["n5"].Name, gkaSEdit(gkaSPlanUnknown("Validating"), func(_ app.NodeAssessmentRequest, na *app.NodeAssessment) { na.Node.Reason = "FutureObservation" }))
	script.SetNode(nodes["n6"].Name, gkaSEdit(gkaSPlanUnknown("Validating"), func(_ app.NodeAssessmentRequest, na *app.NodeAssessment) {
		na.Observation.Completeness = fleet.CompletenessPartial
	}))
	// n7: only the first device (UID order) is decided, and there is no node decision at all.
	script.SetNode(nodes["n7"].Name, func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		full, err := gkaSPlanReady(req)
		return app.NodeAssessment{Devices: full.Devices[:1], Observation: full.Observation}, err
	})
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	rev := func(k string) string { return gkaSGraphRev(string(nodes[k].UID)) }
	rows := []struct {
		node string
		want gkaSNPSWant
		cond gkaSC
	}{
		{"n1", gkaSNPSWant{Elig: "True", Reason: "Ready", Qual: "Qualified", FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: rev("n1")},
			gkaSC{gkaSNodeCondType, "True", "Ready", "fleet=" + f.Name}},
		{"n2", gkaSNPSWant{Elig: "False", Reason: "Degraded", Qual: "Disqualified", FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: rev("n2")},
			gkaSC{gkaSNodeCondType, "False", "Degraded", "fleet=" + f.Name}},
		{"n3", gkaSNPSWant{Elig: "False", Reason: "Degraded", Qual: "Qualified", FreshStatus: "True", FreshReason: "Ready", Completeness: "Complete", GraphRevision: rev("n3")},
			gkaSC{gkaSNodeCondType, "False", "Degraded", "fleet=" + f.Name}},
		{"n4", gkaSNPSWant{Elig: "Unknown", Reason: "EvidenceStale", Completeness: "Complete", GraphRevision: rev("n4")},
			gkaSC{gkaSNodeCondType, "Unknown", "EvidenceStale", "fleet=" + f.Name}},
		{"n5", gkaSNPSWant{Elig: "Unknown", Reason: "FutureObservation", Completeness: "Complete", GraphRevision: rev("n5")},
			gkaSC{gkaSNodeCondType, "Unknown", "FutureObservation", "fleet=" + f.Name}},
		{"n6", gkaSNPSWant{Elig: "Unknown", Reason: "Validating", FreshReason: "PartialSnapshot", Completeness: "Partial", GraphRevision: rev("n6")},
			gkaSC{gkaSNodeCondType, "Unknown", "Validating", "fleet=" + f.Name}},
		// Node == nil with an observation: Unknown/Validating, and EvidenceFresh is not True.
		{"n7", gkaSNPSWant{Elig: "Unknown", Reason: "Validating", Completeness: "Complete", GraphRevision: rev("n7")},
			gkaSC{gkaSNodeCondType, "Unknown", "Validating", "fleet=" + f.Name}},
	}
	for _, r := range rows {
		want := r.want
		w.waitNPS(nodes[r.node].Name, func(p *v1alpha1.NodePathState) string {
			if why := gkaSNPSHas(p, want.Reason); why != "" {
				return why
			}
			if string(p.Status.EvidenceCompleteness) != want.Completeness {
				return "evidenceCompleteness = " + string(p.Status.EvidenceCompleteness)
			}
			return ""
		})
		cond := r.cond
		w.waitNode(nodes[r.node].Name, func(n *corev1.Node) string { return gkaSNodeCondIs(n, &cond) })
	}
	gkaSSettle(3)
	for _, r := range rows {
		gkaSCheckNPS(t, "GKA-076/077 "+r.node, w.npsNow(nodes[r.node].Name), r.want)
		gkaSCheckNodeCond(t, "GKA-111 "+r.node, w.nodeNow(nodes[r.node].Name), &r.cond)
	}
	// EvidenceFresh never says False.
	for _, r := range rows {
		if c := gkaSCondOf(w.npsNow(nodes[r.node].Name).Status.Conditions, "EvidenceFresh"); c == nil || c.Status == "False" {
			t.Errorf("GKA-077: %s EvidenceFresh = %v, want True or Unknown", r.node, c)
		}
	}
	// n7: the decided device carries its decision, the other one is Validating without no_observation.
	first, second := d71, d72
	if string(second.UID) < string(first.UID) {
		first, second = second, first
	}
	gkaSCheckNonDecision(t, "GKA-072 undecided device on an observed node", w.deviceNow(second.Name), gkaSNonDec{SR: "Validating", Phase: "Pending"})
	if got := w.deviceNow(first.Name); got.Status.Qualification != "Qualified" || got.Status.LifecyclePhase != "Ready" {
		t.Errorf("GKA-073: decided device qualification/phase = %s/%s, want Qualified/Ready", got.Status.Qualification, got.Status.LifecyclePhase)
	}
	sums := gkaSSummaries(w.npsNow(nodes["n7"].Name))
	if s := sums[first.Name]; s.Qualification != "Qualified" || s.LifecyclePhase != "Ready" {
		t.Errorf("GKA-076: decided device summary = %#v, want Qualified/Ready", s)
	}
	if s := sums[second.Name]; s.Qualification != "Unknown" || s.LifecyclePhase != "Pending" {
		t.Errorf("GKA-076: undecided device summary = %#v, want Unknown/Pending", s)
	}
}

// ---- GKA-082 -------------------------------------------------------------------

func gkaSIDs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return out
}

// gkaSRev returns ids in reverse order (unique, unsorted input).
func gkaSRev(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[len(ids)-1-i] = id
	}
	return out
}

func gkaSWorkloads(n int) []app.WorkloadRecord {
	out := make([]app.WorkloadRecord, n)
	for i := range out {
		out[i] = app.WorkloadRecord{
			Namespace: "ns", Name: fmt.Sprintf("pod-%03d", i), UID: fmt.Sprintf("wl-%03d", i), Container: "main", CreatedAt: gkaSEvidenceAt,
		}
	}
	return out
}

func gkaSSetEvidence(da *app.DeviceAssessment, ids []string) {
	da.Decision.EvidenceIDs = append([]string(nil), ids...)
	da.Evidence = nil
	for _, id := range ids {
		da.Evidence = append(da.Evidence, app.EvidenceRecord{ID: id, Summary: "evidence " + id, ObservedAt: gkaSEvidenceAt, ExpiresAt: gkaSEvidenceAt.Add(5 * time.Minute)})
	}
}

func gkaSSetFindings(da *app.DeviceAssessment, ids []string) {
	da.Decision.FindingIDs = append([]string(nil), ids...)
	da.Findings = nil
	for _, id := range ids {
		da.Findings = append(da.Findings, app.FindingRecord{ID: id, Type: "PCIE_LINK_WIDTH_DEGRADED", Severity: "high", State: "active"})
	}
}

func gkaSSetAllocation(da *app.DeviceAssessment, refs []string, workloads []app.WorkloadRecord) {
	da.Decision.Allocation = fleet.AllocationInUse
	da.Allocation = &app.AllocationRecord{
		Profile: "nvidia-podresources", ObservedAt: gkaSEvidenceAt, ExpiresAt: gkaSEvidenceAt.Add(5 * time.Minute),
		EvidenceRefs: refs, Workloads: workloads,
	}
}

// GKA-082 (+GKA-073 ordering): each list is sorted (bytes), de-duplicated (record-level lists only: decision-level
// ids are unique by S1 Validate) where the
// spec says so, cut to its bound exactly above the bound (16/17, 32/33, 8/9, 256/257),
// and only a real cut leaves a truncated: token on the named condition; several tokens
// are byte ordered.
func TestGKA082_DeviceListOrderingAndTruncation(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")

	type want struct {
		findings, evidence, coverageRefs int
		qualTok, allocTok                string
		alloc                            bool
		allocRefs, workloads             int
	}
	cases := map[string]struct {
		edit func(da *app.DeviceAssessment)
		want want
	}{
		"f16":  {func(da *app.DeviceAssessment) { gkaSSetFindings(da, gkaSIDs("fnd", 16)) }, want{findings: 16, evidence: 3, coverageRefs: 1}},
		"f17":  {func(da *app.DeviceAssessment) { gkaSSetFindings(da, gkaSRev(gkaSIDs("fnd", 17))) }, want{findings: 16, evidence: 3, coverageRefs: 1, qualTok: "truncated:findingRefs"}},
		"e32":  {func(da *app.DeviceAssessment) { gkaSSetEvidence(da, gkaSIDs("evi", 32)) }, want{evidence: 32, coverageRefs: 1}},
		"e33":  {func(da *app.DeviceAssessment) { gkaSSetEvidence(da, gkaSRev(gkaSIDs("evi", 33))) }, want{evidence: 32, coverageRefs: 1, qualTok: "truncated:evidenceRefs"}},
		"cr16": {func(da *app.DeviceAssessment) { da.Decision.Coverage[0].EvidenceIDs = gkaSIDs("cov", 16) }, want{evidence: 3, coverageRefs: 16}},
		"cr17": {func(da *app.DeviceAssessment) { da.Decision.Coverage[0].EvidenceIDs = gkaSRev(gkaSIDs("cov", 17)) }, want{evidence: 3, coverageRefs: 16, qualTok: "truncated:coverage.evidenceRefs"}},
		// Decision-level evidence ids must be unique (S1 CoverageAssessment.Validate), so duplicates can only be
		// exercised on the record-level lists below (a9dup, w257dup); "cr17" feeds the 17 ids in reverse order
		// so the cut has to be taken after sorting.
		"a8": {func(da *app.DeviceAssessment) { gkaSSetAllocation(da, gkaSIDs("al", 8), gkaSWorkloads(1)) },
			want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 8, workloads: 1}},
		"a9": {func(da *app.DeviceAssessment) { gkaSSetAllocation(da, gkaSIDs("al", 9), gkaSWorkloads(1)) },
			want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 8, workloads: 1, allocTok: "truncated:allocation.evidenceRefs"}},
		"a9dup": {func(da *app.DeviceAssessment) {
			gkaSSetAllocation(da, append(gkaSIDs("al", 8), "al000"), gkaSWorkloads(1))
		}, want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 8, workloads: 1}},
		"w256": {func(da *app.DeviceAssessment) { gkaSSetAllocation(da, gkaSIDs("al", 1), gkaSWorkloads(256)) },
			want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 1, workloads: 256}},
		"w257": {func(da *app.DeviceAssessment) { gkaSSetAllocation(da, gkaSIDs("al", 1), gkaSWorkloads(257)) },
			want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 1, workloads: 256, allocTok: "truncated:affectedWorkloads"}},
		// 257 records where one uid repeats: 256 distinct uids, the first container by byte order is kept.
		"w257dup": {func(da *app.DeviceAssessment) {
			wl := gkaSWorkloads(256)
			wl[0].Container = "zzz"
			extra := wl[0]
			extra.Container = "aaa"
			gkaSSetAllocation(da, gkaSIDs("al", 1), append(wl, extra))
		}, want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 1, workloads: 256}},
		"multi1": {func(da *app.DeviceAssessment) {
			gkaSSetFindings(da, gkaSRev(gkaSIDs("fnd", 17)))
			gkaSSetEvidence(da, gkaSRev(gkaSIDs("evi", 33)))
			da.Decision.Coverage[0].EvidenceIDs = gkaSRev(gkaSIDs("cov", 17))
		}, want{findings: 16, evidence: 32, coverageRefs: 16, qualTok: "truncated:coverage.evidenceRefs,evidenceRefs,findingRefs"}},
		"multi2": {func(da *app.DeviceAssessment) { gkaSSetAllocation(da, gkaSIDs("al", 9), gkaSWorkloads(257)) },
			want{evidence: 3, coverageRefs: 1, alloc: true, allocRefs: 8, workloads: 256, allocTok: "truncated:affectedWorkloads,allocation.evidenceRefs"}},
	}
	names := make([]string, 0, len(cases))
	for k := range cases {
		names = append(names, k)
	}
	sort.Strings(names)
	devs := map[string]*v1alpha1.GPUDevice{}
	byName := map[string]string{}
	for _, k := range names {
		devs[k] = w.device(k, n, f)
		byName[devs[k].Name] = k
	}
	// One workload of w256 is finished: deletedAt is carried, the rest omit it.
	script := newGkaSScript(gkaSPlanReady)
	script.SetDefault(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		return gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
			da := gkaSReadyAssessment(req, in)
			k := byName[in.Device.Name]
			cases[k].edit(&da)
			if k == "w256" {
				da.Allocation.Workloads[5].DeletedAt = gkaSEvidenceAt.Add(time.Minute)
			}
			return &da
		}), nil
	})
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	for _, k := range names {
		w.waitDevice(devs[k].Name, func(d *v1alpha1.GPUDevice) string {
			if d.Status.LifecyclePhase != "Ready" {
				return "phase = " + string(d.Status.LifecyclePhase)
			}
			return ""
		})
	}
	gkaSSettle(3)
	sortedAsc := func(what string, ids []string) {
		t.Helper()
		if !sort.StringsAreSorted(ids) {
			t.Errorf("GKA-082/073: %s not in byte order: %v", what, ids)
		}
	}
	for _, k := range names {
		wnt := cases[k].want
		d := w.deviceNow(devs[k].Name)
		s := d.Status
		where := "GKA-082 " + k
		if len(s.FindingRefs) != wnt.findings || len(s.EvidenceRefs) != wnt.evidence || len(s.Coverage[0].EvidenceRefs) != wnt.coverageRefs {
			t.Errorf("%s: findingRefs/evidenceRefs/coverage[0].evidenceRefs = %d/%d/%d, want %d/%d/%d", where, len(s.FindingRefs), len(s.EvidenceRefs), len(s.Coverage[0].EvidenceRefs), wnt.findings, wnt.evidence, wnt.coverageRefs)
		}
		var ids []string
		for _, e := range s.EvidenceRefs {
			ids = append(ids, e.ID)
		}
		sortedAsc(where+" evidenceRefs", ids)
		ids = nil
		for _, e := range s.FindingRefs {
			ids = append(ids, e.ID)
		}
		sortedAsc(where+" findingRefs", ids)
		sortedAsc(where+" coverage evidenceRefs", s.Coverage[0].EvidenceRefs)
		// The cut keeps the front of the byte order.
		if k == "f17" || k == "multi1" {
			if s.FindingRefs[0].ID != "fnd000" || s.FindingRefs[len(s.FindingRefs)-1].ID != "fnd015" {
				t.Errorf("%s: findingRefs kept %s..%s, want fnd000..fnd015", where, s.FindingRefs[0].ID, s.FindingRefs[len(s.FindingRefs)-1].ID)
			}
		}
		if k == "e33" || k == "multi1" {
			if s.EvidenceRefs[0].ID != "evi000" || s.EvidenceRefs[len(s.EvidenceRefs)-1].ID != "evi031" {
				t.Errorf("%s: evidenceRefs kept %s..%s, want evi000..evi031", where, s.EvidenceRefs[0].ID, s.EvidenceRefs[len(s.EvidenceRefs)-1].ID)
			}
		}
		if k == "cr17" || k == "multi1" {
			if c := s.Coverage[0].EvidenceRefs; c[0] != "cov000" || c[len(c)-1] != "cov015" {
				t.Errorf("%s: coverage evidenceRefs kept %s..%s, want cov000..cov015", where, c[0], c[len(c)-1])
			}
		}
		qual := gkaSMsg("qualification=Qualified phase=Ready", wnt.qualTok)
		alloc := gkaSMsg("allocation=Unknown", wnt.allocTok)
		allocStatus, allocReason := "Unknown", "AllocationUnknown"
		if wnt.alloc {
			alloc = gkaSMsg("allocation=InUse", wnt.allocTok)
			allocStatus, allocReason = "True", "Ready"
		}
		gkaSCheckConds(t, where, s.Conditions, 1, []gkaSC{
			{"AllocationKnown", allocStatus, allocReason, alloc},
			{"DeviceQualified", "True", "Ready", qual},
			{"IdentityBound", "True", "Ready", "binding=Bound"},
			{"LifecycleReady", "True", "Ready", "phase=Ready"},
		})
		if wnt.alloc {
			al := s.Allocation
			if al.State != "InUse" || al.Reason != "Ready" || al.Profile == nil || *al.Profile != "nvidia-podresources" ||
				al.ObservedAt == nil || gkaSSec(*al.ObservedAt) != gkaSAt30 || al.ExpiresAt == nil || gkaSSec(*al.ExpiresAt) != gkaSAt530 {
				t.Errorf("%s: allocation = %#v, want InUse/Ready/nvidia-podresources 00:00:30..00:05:30", where, al)
			}
			if len(al.EvidenceRefs) != wnt.allocRefs || len(al.AffectedWorkloads) != wnt.workloads {
				t.Errorf("%s: allocation evidenceRefs/affectedWorkloads = %d/%d, want %d/%d", where, len(al.EvidenceRefs), len(al.AffectedWorkloads), wnt.allocRefs, wnt.workloads)
			}
			sortedAsc(where+" allocation evidenceRefs", al.EvidenceRefs)
			var uids []string
			for _, wl := range al.AffectedWorkloads {
				uids = append(uids, wl.UID)
			}
			sortedAsc(where+" affectedWorkloads uid", uids)
			for i, wl := range al.AffectedWorkloads {
				if wl.Namespace != "ns" || wl.Name != fmt.Sprintf("pod-%03d", i) || wl.UID != fmt.Sprintf("wl-%03d", i) || wl.CreatedAt.IsZero() {
					t.Errorf("%s: affectedWorkloads[%d] = %#v, want ns/pod-%03d/wl-%03d", where, i, wl, i, i)
					break
				}
				wantContainer := "main"
				if k == "w257dup" && i == 0 {
					wantContainer = "aaa" // same uid: the first container by byte order
				}
				if wl.Container != wantContainer {
					t.Errorf("%s: affectedWorkloads[%d].container = %q, want %q", where, i, wl.Container, wantContainer)
				}
				if k == "w256" && i == 5 {
					if wl.DeletedAt == nil || gkaSSec(*wl.DeletedAt) != gkaSDay+"00:01:30Z" {
						t.Errorf("%s: finished workload deletedAt = %v, want 00:01:30", where, wl.DeletedAt)
					}
				} else if wl.DeletedAt != nil {
					t.Errorf("%s: affectedWorkloads[%d].deletedAt = %v, want omitted for a zero value", where, i, wl.DeletedAt)
				}
			}
		} else if s.Allocation.State != "Unknown" {
			t.Errorf("%s: allocation.state = %s, want Unknown", where, s.Allocation.State)
		}
	}
}

// GKA-082: deviceSummaries is cut at 256 (in UID order) and NodeEligible says so,
// after the other tokens and before gate_not_implemented; 256 devices are not cut.
// The cut never changes the selection the assessor sees (GKA-064).
func TestGKA082_DeviceSummariesTruncation(t *testing.T) {
	w := newGkaSWorld(t)
	fast := gkaSFastClient(t, w.e)
	keyA, keyE := "gka-sem/a", "gka-sem/e"
	n256 := w.node("n256", map[string]string{keyA: w.sel})
	n257 := w.node("n257", map[string]string{keyE: w.sel})
	fA := w.fleet("fa", metav1.LabelSelector{MatchLabels: map[string]string{keyA: w.sel}})
	fE := w.fleet("fe", metav1.LabelSelector{MatchLabels: map[string]string{keyE: w.sel}}, gkaSEnforce)

	var objs []*v1alpha1.GPUDevice
	for i := 0; i < 256; i++ {
		objs = append(objs, w.deviceObject(fmt.Sprintf("a%03d", i), n256, fA))
	}
	for i := 0; i < 257; i++ {
		objs = append(objs, w.deviceObject(fmt.Sprintf("e%03d", i), n257, fE))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		_ = gkaSParallel(len(objs), 32, func(i int) error {
			return client.IgnoreNotFound(fast.Delete(ctx, &v1alpha1.GPUDevice{ObjectMeta: metav1.ObjectMeta{Name: objs[i].Name}}))
		})
	})
	if err := gkaSParallel(len(objs), 32, func(i int) error {
		ctx, cancel := gkaSTO()
		defer cancel()
		return fast.Create(ctx, objs[i])
	}); err != nil {
		t.Fatalf("creating %d devices: %v", len(objs), err)
	}

	a := gkaSNewAssessor(newGkaSScript(gkaSPlanNone))
	cfg := rest.CopyConfig(w.e.Config)
	cfg.QPS, cfg.Burst = 1000, 2000
	gkaSStart(t, cfg, a, newGkaFakeClock(gkaST0))

	w.waitNPS(n257.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	w.waitNPS(n256.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	gkaSSettle(3)

	p257 := w.npsNow(n257.Name)
	gkaSCheckNPS(t, "GKA-082 257 devices", p257, gkaSNPSWant{
		Elig: "Unknown", Reason: "Validating",
		NodeTail: []string{"no_observation", "truncated:deviceSummaries"}, FreshTail: []string{"no_observation"}, Enforce: true,
	})
	var all []string
	for _, o := range objs[256:] {
		all = append(all, string(o.UID)) // Create filled in the server-assigned UID
	}
	sort.Strings(all)
	var got []string
	for _, ds := range p257.Status.DeviceSummaries {
		got = append(got, ds.UID)
	}
	if len(got) != 256 || strings.Join(got, ",") != strings.Join(all[:256], ",") {
		t.Errorf("GKA-082: deviceSummaries = %d entries; want the first 256 of the 257 devices in UID order", len(got))
	}
	p256 := w.npsNow(n256.Name)
	gkaSCheckNPS(t, "GKA-082 256 devices", p256, gkaSNPSWant{
		Elig: "Unknown", Reason: "Validating", NodeTail: []string{"no_observation"}, FreshTail: []string{"no_observation"},
	})
	if len(p256.Status.DeviceSummaries) != 256 {
		t.Errorf("GKA-082: deviceSummaries = %d entries for exactly 256 devices, want 256", len(p256.Status.DeviceSummaries))
	}
	for _, r := range gkaSRequestsFor(a, n257.Name) {
		if r.Selection != fleet.SelectionComplete || len(r.Intents) != 257 {
			t.Errorf("GKA-064/082: the truncated node is assessed with selection %s and %d intents, want Complete and 257", r.Selection.String(), len(r.Intents))
			break
		}
	}
}

// GKA-074(a) + GKA-105 + GKA-124: entries of types S3a does not own (GateOwned,
// CleanupReady; EvidenceFresh on a device) and the fields S3a never writes
// (ownedNodeRefs, collectorSession, gateOwnership) are kept exactly, in type order,
// across ordinary updates and across the InternalError minimal status.
func TestGKA074_105_ForeignConditionsAndUnownedFieldsPreserved(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	d := w.device("d", n, f)
	script := newGkaSScript(gkaSPlanNone)
	clk := newGkaFakeClock(gkaST0)
	w.start(gkaSNewAssessor(script), clk)

	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string { return gkaSDeviceHas(o, "Validating") })
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "Validating") })
	w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })

	foreignAt := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	foreign := func(typ, status, reason, msg string) metav1.Condition {
		return metav1.Condition{Type: typ, Status: metav1.ConditionStatus(status), Reason: reason, Message: msg, ObservedGeneration: 7, LastTransitionTime: foreignAt}
	}
	fGate := foreign("GateOwned", "True", "Ready", "foreign gate owner")
	fClean := foreign("CleanupReady", "False", "CleanupPending", "foreign cleanup state")
	nGate := foreign("GateOwned", "Unknown", "CleanupStable", "foreign node gate")
	dFresh := foreign("EvidenceFresh", "True", "Ready", "foreign device evidence")
	owned := []v1alpha1.OwnedNodeRef{{Name: "gka-owned-node", UID: "gka-owned-uid"}}
	session := gkaSPtr(int64(7))
	gate := &v1alpha1.GateOwnershipStatus{
		OwnerFleetUID: "fleet-uid-x", NodeUID: "node-uid-x", Key: "infrastructure.data-path-assurance.io/gpu-path", Value: "not-ready", Effect: "NoSchedule",
		Policy:       v1alpha1.GateOwnershipPolicy{PolicyVersion: "pv-1", RequiredCoverage: gkaSRequiredCoverage(), FreshnessSeconds: 60, ReadyForSeconds: 30},
		CleanupPhase: v1alpha1.CleanupPhase("None"), CleanupRequestedAt: foreignAt,
	}
	w.fleetStatusAsForeign(f.Name, func(o *v1alpha1.GPUFleet) {
		o.Status.Conditions = append(o.Status.Conditions, fGate, fClean)
		o.Status.OwnedNodeRefs = owned
	})
	w.npsStatusAsForeign(n.Name, func(o *v1alpha1.NodePathState) {
		o.Status.Conditions = append(o.Status.Conditions, nGate)
		o.Status.CollectorSession = session
		o.Status.GateOwnership = gate
	})
	w.deviceStatusAsForeign(d.Name, func(o *v1alpha1.GPUDevice) { o.Status.Conditions = append(o.Status.Conditions, dFresh) })

	sameForeign := func(where string, conds []metav1.Condition, want ...metav1.Condition) {
		t.Helper()
		for _, wc := range want {
			c := gkaSCondOf(conds, wc.Type)
			if c == nil {
				t.Errorf("%s: foreign condition %s was removed (GKA-074(a))", where, wc.Type)
				continue
			}
			if c.Status != wc.Status || c.Reason != wc.Reason || c.Message != wc.Message || c.ObservedGeneration != 7 || gkaSSec(c.LastTransitionTime) != gkaSSec(foreignAt) {
				t.Errorf("%s: foreign %s = {%s %s %q gen %d %s}, want it untouched {%s %s %q gen 7 %s}", where, wc.Type, c.Status, c.Reason, c.Message, c.ObservedGeneration, gkaSSec(c.LastTransitionTime), wc.Status, wc.Reason, wc.Message, gkaSSec(foreignAt))
			}
		}
	}
	// controllerWrote: the controller has rewritten the status since the foreign writer appended its entries
	// (a semantic change was forced). Only then is the type order its responsibility: GKA-122 compares the
	// conditions after sorting, so an unchanged status is left exactly as the foreign writer ordered it.
	checkAll := func(phase string, controllerWrote bool) {
		t.Helper()
		fl, dv, np := w.fleetNow(f.Name), w.deviceNow(d.Name), w.npsNow(n.Name)
		sameForeign(phase+" fleet", fl.Status.Conditions, fGate, fClean)
		sameForeign(phase+" device", dv.Status.Conditions, dFresh)
		sameForeign(phase+" nodepathstate", np.Status.Conditions, nGate)
		for where, conds := range map[string][]metav1.Condition{"fleet": fl.Status.Conditions, "device": dv.Status.Conditions, "nodepathstate": np.Status.Conditions} {
			if !controllerWrote {
				break
			}
			for i := 1; i < len(conds); i++ {
				if conds[i-1].Type >= conds[i].Type {
					t.Errorf("%s %s: conditions not in ascending type order: %s", phase, where, gkaSCondTypes(conds))
					break
				}
			}
		}
		if got := fl.Status.OwnedNodeRefs; len(got) != 1 || got[0] != owned[0] {
			t.Errorf("%s: ownedNodeRefs = %#v, want it preserved (GKA-105)", phase, got)
		}
		if np.Status.CollectorSession == nil || *np.Status.CollectorSession != 7 {
			t.Errorf("%s: collectorSession = %v, want 7 preserved (GKA-105)", phase, np.Status.CollectorSession)
		}
		if got := np.Status.GateOwnership; got == nil || got.OwnerFleetUID != gate.OwnerFleetUID || got.Key != gate.Key || got.CleanupPhase != "None" || len(got.Policy.RequiredCoverage) != 3 {
			t.Errorf("%s: gateOwnership = %#v, want the seeded value preserved (GKA-105)", phase, got)
		}
	}
	// Ordinary controller updates around the seeded values.
	gkaSSettle(4)
	checkAll("after passes", false)
	if fl := w.fleetNow(f.Name); gkaSCondOf(fl.Status.Conditions, "FleetReady") == nil || len(fl.Status.Conditions) != 3 {
		t.Errorf("GKA-074(a): fleet conditions = %s, want CleanupReady,FleetReady,GateOwned", gkaSCondTypes(fl.Status.Conditions))
	}
	if dv := w.deviceNow(d.Name); len(dv.Status.Conditions) != 5 {
		t.Errorf("GKA-074(a): device conditions = %s, want the four owned types plus the foreign EvidenceFresh", gkaSCondTypes(dv.Status.Conditions))
	}

	// A real status change (observation arrives) rewrites the owned entries only.
	clk.Set(gkaST0.Add(30 * time.Second))
	script.SetDefault(gkaSPlanReady)
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "True", "Ready") })
	w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string {
		if o.Status.LifecyclePhase != "Ready" {
			return "phase = " + string(o.Status.LifecyclePhase)
		}
		return ""
	})
	w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Ready") })
	gkaSSettle(2)
	checkAll("after observation", true)

	// The minimal InternalError status of an invalid selector keeps the foreign entries and ownedNodeRefs.
	w.updateFleet(f.Name, func(o *v1alpha1.GPUFleet) {
		o.Spec.NodeSelector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: gkaSSelKey, Operator: metav1.LabelSelectorOpIn}}}
	})
	w.waitFleet(f.Name, func(o *v1alpha1.GPUFleet) string { return gkaSFleetIs(o, "Unknown", "InternalError") })
	w.waitNPS(n.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "InternalError") })
	gkaSSettle(2)
	checkAll("after InternalError", true)
	fl := w.fleetNow(f.Name)
	if fl.Status.SelectedCount != 0 || fl.Status.AssessmentRevision != nil {
		t.Errorf("GKA-087: minimal status counts/revision = %d/%v, want 0/omitted", fl.Status.SelectedCount, fl.Status.AssessmentRevision)
	}
}

// GKA-077 (EvidenceFresh, each term on its own): True needs Node != nil AND
// Node.Qualification != Unknown AND every D* device decided with a qualification != Unknown.
// Both inputs below are answers GKA-085 accepts (a missing node decision with a full set of
// decisions is a valid "subset" answer; a node decision is not checked against its children's
// qualification), so a rejected assessment cannot hide the check.
func TestGKA077_EvidenceFreshNeedsEveryTerm(t *testing.T) {
	w := newGkaSWorld(t)
	f := w.selFleet("f")
	nA, nB := w.selNode("na"), w.selNode("nb")
	da1, da2 := w.device("da1", nA, f), w.device("da2", nA, f)
	db1, db2 := w.device("db1", nB, f), w.device("db2", nB, f)
	script := newGkaSScript(gkaSPlanReady)
	// (i) no node decision, although every device is Qualified/Ready.
	script.SetNode(nA.Name, gkaSEdit(gkaSPlanReady, func(_ app.NodeAssessmentRequest, na *app.NodeAssessment) { na.Node = nil }))
	// (ii) a Qualified node decision while one device of D* is Unknown.
	script.SetNode(nB.Name, func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		first := req.Intents[0].Device.UID
		na := gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
			var da app.DeviceAssessment
			if in.Device.UID == first {
				da = gkaSReadyAssessment(req, in)
			} else {
				da = gkaSUnknownAssessment(req, in, "Validating")
			}
			return &da
		})
		na.Node = gkaSNodeDecision(req, fleet.SelectionComplete, fleet.EligibilityIneligible, fleet.QualificationQualified, "Degraded", len(na.Devices))
		return na, nil
	})
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	w.waitNPS(nA.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Validating") })
	w.waitNPS(nB.Name, func(p *v1alpha1.NodePathState) string { return gkaSNPSHas(p, "Degraded") })
	gkaSSettle(3)

	// (i): NodeEligible follows the "Node == nil" row, EvidenceFresh is Unknown with the same reason.
	gkaSCheckNPS(t, "GKA-077 (i) Node == nil", w.npsNow(nA.Name), gkaSNPSWant{
		Elig: "Unknown", Reason: "Validating", Completeness: "Complete", GraphRevision: gkaSGraphRev(string(nA.UID)),
	})
	for _, d := range []*v1alpha1.GPUDevice{da1, da2} {
		if got := w.deviceNow(d.Name); got.Status.Qualification != "Qualified" {
			t.Errorf("GKA-077 (i) setup: %s qualification = %s, want Qualified (the fake must be all-Qualified)", d.Name, got.Status.Qualification)
		}
	}
	// (ii): the node decision is Qualified/Ineligible, one device is Unknown: EvidenceFresh is Unknown with the node reason.
	gkaSCheckNPS(t, "GKA-077 (ii) one Unknown device", w.npsNow(nB.Name), gkaSNPSWant{
		Elig: "False", Reason: "Degraded", Qual: "Qualified", FreshStatus: "Unknown", FreshReason: "Degraded",
		Completeness: "Complete", GraphRevision: gkaSGraphRev(string(nB.UID)),
	})
	qualified, unknown := 0, 0
	for _, d := range []*v1alpha1.GPUDevice{db1, db2} {
		switch got := w.deviceNow(d.Name); got.Status.Qualification {
		case "Qualified":
			qualified++
		case "Unknown":
			unknown++
		}
	}
	if qualified != 1 || unknown != 1 {
		t.Errorf("GKA-077 (ii) setup: devices Qualified/Unknown = %d/%d, want 1/1", qualified, unknown)
	}
}
