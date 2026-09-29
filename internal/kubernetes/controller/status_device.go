package controller

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// deviceRender is everything the status of one GPUDevice is built from.
type deviceRender struct {
	scope deviceScope
	class string
	peers []string
	noObs bool
	proj  *deviceProjection
}

func renderOf(ds *deviceState) deviceRender {
	return deviceRender{scope: ds.scope, class: ds.class, peers: ds.peers, noObs: ds.noObservation, proj: ds.proj}
}

// scopeReason is the condition reason of a device scope without a decision
// (GKA-072).
func scopeReason(s deviceScope) string {
	switch s {
	case deviceNoMatching:
		return v1alpha1.ReasonNoMatchingDevices
	case deviceUIDMismatch:
		return v1alpha1.ReasonUIDMismatch
	case deviceIdentityConflict:
		return v1alpha1.ReasonIdentityConflict
	case deviceInternalError:
		return v1alpha1.ReasonInternalError
	case deviceConflict:
		return v1alpha1.ReasonConflict
	case deviceCapacity:
		return v1alpha1.ReasonTargetCapacityExceeded
	}
	return v1alpha1.ReasonValidating
}

// deviceQualPhase is the qualification and phase a device status carries, for
// the NodePathState device summaries.
func deviceQualPhase(ds *deviceState) (v1alpha1.Qualification, v1alpha1.LifecyclePhase) {
	if ds.proj != nil {
		return ds.proj.qualification, ds.proj.phase
	}
	if ds.scope == deviceInternalError {
		return v1alpha1.QualificationUnknown, v1alpha1.LifecyclePhaseUnknown
	}
	return v1alpha1.QualificationUnknown, baselinePhase(ds.obj.Spec.DesiredState)
}

// deviceStatus builds the desired status of cur (GKA-072 to GKA-075).
// Existing conditions of types this controller does not publish are kept.
func deviceStatus(cur *v1alpha1.GPUDevice, r deviceRender, now time.Time) v1alpha1.GPUDeviceStatus {
	st := v1alpha1.GPUDeviceStatus{
		ObservedGeneration: cur.Generation,
		ObservedRequestID:  cur.Spec.Request.ID,
		IntentObservedAt:   metaTime(intentObservedAt(cur, now)),
		Coverage:           []v1alpha1.CoverageSummary{},
		FindingRefs:        []v1alpha1.FindingSummary{},
		EvidenceRefs:       []v1alpha1.EvidenceSummary{},
	}
	var specs []condSpec
	if p := r.proj; p != nil {
		st.ActualBinding = p.binding
		gr := p.graphRevision
		st.GraphRevision = &gr
		st.Qualification = p.qualification
		st.LifecyclePhase = p.phase
		st.Allocation = p.allocation
		st.Coverage = p.coverage
		if st.Coverage == nil {
			st.Coverage = []v1alpha1.CoverageSummary{}
		}
		if p.findings != nil {
			st.FindingRefs = p.findings
		}
		if p.evidence != nil {
			st.EvidenceRefs = p.evidence
		}
		specs = decidedDeviceConditions(p)
	} else {
		sr := scopeReason(r.scope)
		bindState := v1alpha1.BindingStateUnknown
		if r.scope == deviceIdentityConflict {
			bindState = v1alpha1.BindingStateConflict
		}
		phase := baselinePhase(cur.Spec.DesiredState)
		allocReason := v1alpha1.ReasonAllocationUnknown
		if r.scope == deviceInternalError {
			phase = v1alpha1.LifecyclePhaseUnknown
			allocReason = v1alpha1.ReasonInternalError
		}
		st.ActualBinding = v1alpha1.ActualBinding{State: bindState, Reason: sr}
		st.Qualification = v1alpha1.QualificationUnknown
		st.LifecyclePhase = phase
		st.Allocation = v1alpha1.AllocationSummary{
			State:             v1alpha1.AllocationStateUnknown,
			Reason:            allocReason,
			EvidenceRefs:      []string{},
			AffectedWorkloads: []v1alpha1.AffectedWorkload{},
		}
		specs = undecidedDeviceConditions(r, sr, allocReason, bindState, phase)
	}
	st.Conditions = buildConditions(cur.Status.Conditions, specs, cur.Generation, now)
	return st
}

func decidedDeviceConditions(p *deviceProjection) []condSpec {
	qs := metav1.ConditionUnknown
	switch p.qualification {
	case v1alpha1.QualificationQualified:
		qs = metav1.ConditionTrue
	case v1alpha1.QualificationDisqualified:
		qs = metav1.ConditionFalse
	}
	ls := metav1.ConditionUnknown
	switch p.phase {
	case v1alpha1.LifecyclePhaseReady, v1alpha1.LifecyclePhaseMaintenanceReady, v1alpha1.LifecyclePhaseRetired:
		ls = metav1.ConditionTrue
	case v1alpha1.LifecyclePhaseDegraded:
		ls = metav1.ConditionFalse
	}
	bs := metav1.ConditionUnknown
	switch p.binding.State {
	case v1alpha1.BindingStateBound:
		bs = metav1.ConditionTrue
	case v1alpha1.BindingStateConflict:
		bs = metav1.ConditionFalse
	}
	as := metav1.ConditionUnknown
	if p.allocation.State != v1alpha1.AllocationStateUnknown {
		as = metav1.ConditionTrue
	}
	reason := func(s metav1.ConditionStatus) string {
		if s == metav1.ConditionTrue {
			return v1alpha1.ReasonReady
		}
		return p.dec.Reason
	}
	allocReason := v1alpha1.ReasonAllocationUnknown
	if as == metav1.ConditionTrue {
		allocReason = v1alpha1.ReasonReady
	}
	return []condSpec{
		{v1alpha1.ConditionTypeAllocationKnown, as, allocReason,
			message{prefix: "allocation=" + string(p.allocation.State), tail: []string{truncatedToken(p.truncAlloc)}}.String()},
		{v1alpha1.ConditionTypeDeviceQualified, qs, reason(qs),
			message{prefix: "qualification=" + string(p.qualification) + " phase=" + string(p.phase), tail: []string{truncatedToken(p.truncQual)}}.String()},
		{v1alpha1.ConditionTypeIdentityBound, bs, reason(bs),
			message{prefix: "binding=" + string(p.binding.State)}.String()},
		{v1alpha1.ConditionTypeLifecycleReady, ls, reason(ls),
			message{prefix: "phase=" + string(p.phase)}.String()},
	}
}

func undecidedDeviceConditions(r deviceRender, sr, allocReason string, bind v1alpha1.BindingState, phase v1alpha1.LifecyclePhase) []condSpec {
	var tail []string
	tail = append(tail, internalErrorToken(r.class))
	if r.noObs {
		tail = append(tail, "no_observation")
	}
	bindStatus := metav1.ConditionUnknown
	if r.scope == deviceIdentityConflict {
		bindStatus = metav1.ConditionFalse
	}
	mk := func(prefix string) string { return message{prefix: prefix, tail: tail}.String() }
	var peers []string
	if r.scope == deviceIdentityConflict {
		peers = r.peers
	}
	return []condSpec{
		{v1alpha1.ConditionTypeAllocationKnown, metav1.ConditionUnknown, allocReason, mk("allocation=Unknown")},
		{v1alpha1.ConditionTypeDeviceQualified, metav1.ConditionUnknown, sr, mk("qualification=Unknown phase=" + string(phase))},
		{v1alpha1.ConditionTypeIdentityBound, bindStatus, sr, message{prefix: "binding=" + string(bind), peers: peers, tail: tail}.String()},
		{v1alpha1.ConditionTypeLifecycleReady, metav1.ConditionUnknown, sr, mk("phase=" + string(phase))},
	}
}
