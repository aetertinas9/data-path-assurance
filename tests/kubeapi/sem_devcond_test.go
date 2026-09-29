package kubeapi_test

// GKA-075: every cell of the GPUDevice condition table (status, reason, message) for a
// decided device: DeviceQualified by qualification, LifecycleReady by phase,
// IdentityBound by binding state, AllocationKnown by allocation state, and the reason
// mapping R(dec.Reason) for False/Unknown.

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

func TestGKA075_DeviceConditionTableCells(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")

	type cell struct{ status, reason, message string }
	type row struct {
		desired string
		build   func(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment
		dq, lr  cell
		ib, ak  cell
		binding string // actualBinding.state
		bReason string // actualBinding.reason
		alloc   string // allocation.state
	}
	unk := func(reason string, edit func(*app.DeviceAssessment)) func(app.NodeAssessmentRequest, fleet.Intent) app.DeviceAssessment {
		return func(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
			da := gkaSUnknownAssessment(req, in, reason)
			if edit != nil {
				edit(&da)
			}
			return da
		}
	}
	allocUnknown := cell{"Unknown", "AllocationUnknown", "allocation=Unknown"}
	boundOK := cell{"True", "Ready", "binding=Bound"}
	rows := map[string]row{
		"ready": {"InService", gkaSReadyAssessment,
			cell{"True", "Ready", "qualification=Qualified phase=Ready"}, cell{"True", "Ready", "phase=Ready"}, boundOK, allocUnknown, "Bound", "Ready", "Unknown"},
		"disq": {"InService", gkaSDisqualifiedAssessment,
			cell{"False", "Degraded", "qualification=Disqualified phase=Degraded"}, cell{"False", "Degraded", "phase=Degraded"}, boundOK, allocUnknown, "Bound", "Ready", "Unknown"},
		"maintready": {"Maintenance", func(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
			da := gkaSReadyAssessment(req, in)
			da.Decision.Phase = fleet.PhaseMaintenanceReady
			return da
		}, cell{"True", "Ready", "qualification=Qualified phase=MaintenanceReady"}, cell{"True", "Ready", "phase=MaintenanceReady"}, boundOK, allocUnknown, "Bound", "Ready", "Unknown"},
		"retired": {"Retired", unk("Validating", func(da *app.DeviceAssessment) { da.Decision.Phase = fleet.PhaseRetired }),
			cell{"Unknown", "Validating", "qualification=Unknown phase=Retired"}, cell{"True", "Ready", "phase=Retired"},
			cell{"Unknown", "Validating", "binding=Unknown"}, allocUnknown, "Unknown", "Validating", "Unknown"},
		"pending": {"InService", unk("Validating", nil),
			cell{"Unknown", "Validating", "qualification=Unknown phase=Pending"}, cell{"Unknown", "Validating", "phase=Pending"},
			cell{"Unknown", "Validating", "binding=Unknown"}, allocUnknown, "Unknown", "Validating", "Unknown"},
		"validating": {"InService", unk("CoverageMissing", func(da *app.DeviceAssessment) { da.Decision.Phase = fleet.PhaseValidating }),
			cell{"Unknown", "CoverageMissing", "qualification=Unknown phase=Validating"}, cell{"Unknown", "CoverageMissing", "phase=Validating"},
			cell{"Unknown", "CoverageMissing", "binding=Unknown"}, allocUnknown, "Unknown", "CoverageMissing", "Unknown"},
		"mpending": {"Maintenance", unk("Validating", nil),
			cell{"Unknown", "Validating", "qualification=Unknown phase=MaintenancePending"}, cell{"Unknown", "Validating", "phase=MaintenancePending"},
			cell{"Unknown", "Validating", "binding=Unknown"}, allocUnknown, "Unknown", "Validating", "Unknown"},
		"retiring": {"Retired", unk("Validating", nil),
			cell{"Unknown", "Validating", "qualification=Unknown phase=Retiring"}, cell{"Unknown", "Validating", "phase=Retiring"},
			cell{"Unknown", "Validating", "binding=Unknown"}, allocUnknown, "Unknown", "Validating", "Unknown"},
		"bconflict": {"InService", unk("IdentityConflict", func(da *app.DeviceAssessment) { da.Decision.BindingState = fleet.BindingConflict }),
			cell{"Unknown", "IdentityConflict", "qualification=Unknown phase=Pending"}, cell{"Unknown", "IdentityConflict", "phase=Pending"},
			cell{"False", "IdentityConflict", "binding=Conflict"}, allocUnknown, "Conflict", "IdentityConflict", "Unknown"},
		"untrusted": {"InService", unk("UntrustedSource", nil),
			cell{"Unknown", "UntrustedSource", "qualification=Unknown phase=Pending"}, cell{"Unknown", "UntrustedSource", "phase=Pending"},
			cell{"Unknown", "UntrustedSource", "binding=Unknown"}, allocUnknown, "Unknown", "UntrustedSource", "Unknown"},
		"allocempty": {"InService", func(req app.NodeAssessmentRequest, in fleet.Intent) app.DeviceAssessment {
			da := gkaSReadyAssessment(req, in)
			da.Decision.Allocation = fleet.AllocationEmpty
			da.Allocation = &app.AllocationRecord{Profile: "nvidia-podresources", ObservedAt: gkaSEvidenceAt, ExpiresAt: gkaSEvidenceAt.Add(5 * time.Minute), EvidenceRefs: []string{"al:empty"}}
			return da
		}, cell{"True", "Ready", "qualification=Qualified phase=Ready"}, cell{"True", "Ready", "phase=Ready"}, boundOK,
			cell{"True", "Ready", "allocation=Empty"}, "Bound", "Ready", "Empty"},
	}
	devs := map[string]*v1alpha1.GPUDevice{}
	byName := map[string]string{}
	for k, r := range rows {
		d := w.device(k, n, f, gkaSWithDesired(r.desired))
		devs[k] = d
		byName[d.Name] = k
	}
	script := newGkaSScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		return gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
			da := rows[byName[in.Device.Name]].build(req, in)
			return &da
		}), nil
	})
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	for k := range rows {
		w.waitDevice(devs[k].Name, func(d *v1alpha1.GPUDevice) string {
			if d.Status.ObservedGeneration != d.Generation || len(d.Status.Conditions) != 4 || d.Status.GraphRevision == nil {
				return "status of the decided device not written yet"
			}
			return ""
		})
	}
	gkaSSettle(3)
	for k, r := range rows {
		d := w.deviceNow(devs[k].Name)
		gkaSCheckConds(t, "GKA-075 "+k, d.Status.Conditions, d.Generation, []gkaSC{
			{"AllocationKnown", r.ak.status, r.ak.reason, r.ak.message},
			{"DeviceQualified", r.dq.status, r.dq.reason, r.dq.message},
			{"IdentityBound", r.ib.status, r.ib.reason, r.ib.message},
			{"LifecycleReady", r.lr.status, r.lr.reason, r.lr.message},
		})
		if string(d.Status.ActualBinding.State) != r.binding || d.Status.ActualBinding.Reason != r.bReason {
			t.Errorf("GKA-073 %s: actualBinding = {%s %s}, want {%s %s}", k, d.Status.ActualBinding.State, d.Status.ActualBinding.Reason, r.binding, r.bReason)
		}
		if string(d.Status.Allocation.State) != r.alloc {
			t.Errorf("GKA-073 %s: allocation.state = %s, want %s", k, d.Status.Allocation.State, r.alloc)
		}
		if r.alloc == "Empty" {
			al := d.Status.Allocation
			if al.Reason != "Ready" || al.Profile == nil || *al.Profile != "nvidia-podresources" || len(al.EvidenceRefs) != 1 || al.EvidenceRefs[0] != "al:empty" || al.AffectedWorkloads == nil || len(al.AffectedWorkloads) != 0 {
				t.Errorf("GKA-073 %s: allocation = %#v, want Empty/Ready with one ref and no workloads", k, al)
			}
		}
		if r.binding != "Bound" && (d.Status.ActualBinding.Vendor != nil || d.Status.ActualBinding.BDF != nil || d.Status.ActualBinding.NodeUID != nil) {
			t.Errorf("GKA-073 %s: a non-Bound binding carries optional fields: %#v", k, d.Status.ActualBinding)
		}
	}
}

// GKA-073: coverage rows are copied with the S1 state and reason as they are (including
// reasons that are not condition reasons: Normal, TopologyConflict, Unsupported), sorted by
// name in byte order (uppercase before lowercase), evidenceRefs sorted, and
// every zero time is omitted rather than written as the zero time.
func TestGKA073_CoverageStatesReasonsAndOmissions(t *testing.T) {
	w := newGkaSWorld(t)
	n := w.selNode("n")
	f := w.selFleet("f")
	d := w.device("d", n, f)
	at := gkaSEvidenceAt
	exp := at.Add(5 * time.Minute)
	// Deliberately not in name order, with unsorted (but unique: S1 requires it) evidence ids.
	input := []fleet.CoverageAssessment{
		{Name: "z-unsupported", PathKind: "nic-lldp-remote", State: fleet.CoverageUnsupported, Reason: "Unsupported"},
		{Name: "y-topology", PathKind: "gpu-nic-shared-ancestor", State: fleet.CoverageUnknown, Reason: "TopologyConflict", EvidenceIDs: []string{"tc-b", "tc-a"}},
		{Name: "x-future", PathKind: "gpu-pcie-root", State: fleet.CoverageUnknown, Reason: "FutureObservation", ObservedAt: at}, // one time only
		{Name: "w-bundle", PathKind: "gpu-pcie-parent", State: fleet.CoverageUnknown, Reason: "BundleMismatch"},
		{Name: "v-untrusted", PathKind: "gpu-pcie-parent", State: fleet.CoverageUnknown, Reason: "UntrustedSource"},
		{Name: "u-validating", PathKind: "gpu-pcie-parent", State: fleet.CoverageUnknown, Reason: "Validating"},
		{Name: "t-missing", PathKind: "gpu-pcie-link-width-normal", State: fleet.CoverageMissing, Reason: "CoverageMissing"},
		{Name: "s-normal", PathKind: "gpu-pcie-root", State: fleet.CoverageNormal, Reason: "Normal", EvidenceIDs: []string{"n-2", "n-1"}, ObservedAt: at, LatestObservedAt: at, ExpiresAt: exp, AssessmentSequence: 4},
		{Name: "aa-lower", PathKind: "gpu-pcie-root", State: fleet.CoverageNormal, Reason: "Normal", EvidenceIDs: []string{"n-3"}, ObservedAt: at, LatestObservedAt: at, ExpiresAt: exp, AssessmentSequence: 4},
		{Name: "Zz-upper", PathKind: "gpu-pcie-root", State: fleet.CoverageNormal, Reason: "Normal", EvidenceIDs: []string{"n-4"}, ObservedAt: at, LatestObservedAt: at, ExpiresAt: exp, AssessmentSequence: 4},
	}
	script := newGkaSScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		return gkaSAssessment(req, func(in fleet.Intent) *app.DeviceAssessment {
			da := gkaSReadyAssessment(req, in)
			da.Decision.Coverage = append([]fleet.CoverageAssessment(nil), input...)
			da.Decision.CoverageCursors = nil
			for _, c := range input {
				if c.State == fleet.CoverageNormal {
					da.Decision.CoverageCursors = append(da.Decision.CoverageCursors, fleet.CoverageCursor{
						Name: c.Name, ObservedAt: at, AssessmentSequence: 4, EvidenceDigest: gkaSHex("cursor|" + c.Name),
					})
				}
			}
			return &da
		}), nil
	})
	w.start(gkaSNewAssessor(script), newGkaFakeClock(gkaST0))

	got := w.waitDevice(d.Name, func(o *v1alpha1.GPUDevice) string {
		if o.Status.LifecyclePhase != "Ready" {
			return "phase = " + string(o.Status.LifecyclePhase)
		}
		return ""
	}).Status.Coverage
	type want struct {
		name, kind, state, reason string
		refs                      []string
		observed, latest, expires bool
	}
	wants := []want{
		{"Zz-upper", "gpu-pcie-root", "Normal", "Normal", []string{"n-4"}, true, true, true},
		{"aa-lower", "gpu-pcie-root", "Normal", "Normal", []string{"n-3"}, true, true, true},
		{"s-normal", "gpu-pcie-root", "Normal", "Normal", []string{"n-1", "n-2"}, true, true, true},
		{"t-missing", "gpu-pcie-link-width-normal", "Missing", "CoverageMissing", nil, false, false, false},
		{"u-validating", "gpu-pcie-parent", "Unknown", "Validating", nil, false, false, false},
		{"v-untrusted", "gpu-pcie-parent", "Unknown", "UntrustedSource", nil, false, false, false},
		{"w-bundle", "gpu-pcie-parent", "Unknown", "BundleMismatch", nil, false, false, false},
		{"x-future", "gpu-pcie-root", "Unknown", "FutureObservation", nil, true, false, false},
		{"y-topology", "gpu-nic-shared-ancestor", "Unknown", "TopologyConflict", []string{"tc-a", "tc-b"}, false, false, false},
		{"z-unsupported", "nic-lldp-remote", "Unsupported", "Unsupported", nil, false, false, false},
	}
	if len(got) != len(wants) {
		t.Fatalf("GKA-073: %d coverage rows, want %d: %#v", len(got), len(wants), got)
	}
	for i, wnt := range wants {
		c := got[i]
		if c.Name != wnt.name || c.PathKind != wnt.kind || string(c.State) != wnt.state || c.Reason != wnt.reason {
			t.Errorf("GKA-073: coverage[%d] = {%s %s %s %s}, want {%s %s %s %s} (byte order by name, reason passed through)", i, c.Name, c.PathKind, c.State, c.Reason, wnt.name, wnt.kind, wnt.state, wnt.reason)
			continue
		}
		if c.EvidenceRefs == nil || len(c.EvidenceRefs) != len(wnt.refs) {
			t.Errorf("GKA-073: coverage %s evidenceRefs = %#v, want %v (present, sorted)", wnt.name, c.EvidenceRefs, wnt.refs)
		} else {
			for j := range wnt.refs {
				if c.EvidenceRefs[j] != wnt.refs[j] {
					t.Errorf("GKA-073: coverage %s evidenceRefs = %v, want %v", wnt.name, c.EvidenceRefs, wnt.refs)
					break
				}
			}
		}
		present := func(p *metav1.Time) bool { return p != nil && !p.IsZero() }
		for _, chk := range []struct {
			what string
			got  bool
			want bool
		}{
			{"observedAt", present(c.ObservedAt), wnt.observed},
			{"latestObservedAt", present(c.LatestObservedAt), wnt.latest},
			{"expiresAt", present(c.ExpiresAt), wnt.expires},
		} {
			if chk.got != chk.want {
				t.Errorf("GKA-073: coverage %s %s present=%v, want %v (zero times are omitted)", wnt.name, chk.what, chk.got, chk.want)
			}
		}
	}
	// The decision is Qualified/Ready: nothing above may turn it into an invalid assessment.
	if dv := w.deviceNow(d.Name); dv.Status.Qualification != "Qualified" {
		t.Errorf("GKA-073: qualification = %s, want Qualified", dv.Status.Qualification)
	}
}
