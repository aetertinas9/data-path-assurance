package controller

import (
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// initEvals gives every node its verdict before any assessment (GKA-076): the
// scope-derived Unknown verdicts. Assessment overrides the assessable ones.
func initEvals(w *world) {
	for _, ns := range w.nodes {
		switch ns.scope {
		case nodeHeld:
			ns.eval = newNodeEval(v1alpha1.ReasonInternalError)
			ns.eval.class = classInvalidSelector
		case nodeConflict:
			ns.eval = newNodeEval(v1alpha1.ReasonConflict)
		case nodeCapacity:
			ns.eval = newNodeEval(v1alpha1.ReasonTargetCapacityExceeded)
		case nodeSelected:
			if ns.sel == fleet.SelectionNoDevices {
				ns.eval = newNodeEval(v1alpha1.ReasonNoMatchingDevices)
			} else {
				ns.eval = newNodeEval(v1alpha1.ReasonValidating)
			}
		default:
			ns.eval = newNodeEval(v1alpha1.ReasonValidating)
		}
	}
}

// nodeEnforce reports whether the fleets that decide the node's NodeEligible
// message include an Enforce fleet (GKA-077, GKA-086).
func nodeEnforce(w *world, ns *nodeState) bool {
	if ns.scope == nodeHeld {
		for _, name := range ns.holdFleets {
			if fs := w.fleetBy[name]; fs != nil && fs.enforce {
				return true
			}
		}
		return false
	}
	for _, fs := range ns.fleets {
		if fs.enforce {
			return true
		}
	}
	return false
}

func eligibilityStatus(e fleet.Eligibility) metav1.ConditionStatus {
	switch e {
	case fleet.EligibilityEligible:
		return metav1.ConditionTrue
	case fleet.EligibilityIneligible:
		return metav1.ConditionFalse
	}
	return metav1.ConditionUnknown
}

// summaryDevices returns the devices a NodePathState summarizes.
func summaryDevices(ns *nodeState) []*deviceState {
	if ns.scope == nodeConflict {
		return ns.dUnion
	}
	return ns.d
}

// nodePathStatus builds the desired status of a NodePathState (GKA-076,
// GKA-077). class, when not empty, renders the object as an InternalError with
// that class regardless of the node's verdict (the write fallback of GKA-125).
func nodePathStatus(w *world, cur *v1alpha1.NodePathState, ns *nodeState, class string, now time.Time) v1alpha1.NodePathStateStatus {
	ev := ns.eval
	if class != "" {
		ev = newNodeEval(v1alpha1.ReasonInternalError)
		ev.class = class
	}
	st := v1alpha1.NodePathStateStatus{
		ObservedGeneration:   cur.Generation,
		NodeRef:              cur.Spec.NodeRef,
		CollectorSession:     cur.Status.CollectorSession,
		EvidenceCompleteness: v1alpha1.SnapshotCompletenessUnknown,
		DeviceSummaries:      []v1alpha1.DeviceSummary{},
		GateOwnership:        cur.Status.GateOwnership,
	}
	if ev.obs != nil {
		gr := ev.obs.GraphRevision
		st.GraphRevision = &gr
		st.EvidenceCompleteness = v1alpha1.SnapshotCompleteness(ev.obs.Completeness.String())
	}

	devs := append([]*deviceState(nil), summaryDevices(ns)...)
	sort.Slice(devs, func(i, j int) bool { return devs[i].obj.UID < devs[j].obj.UID })
	truncated := false
	if len(devs) > maxDeviceSummaries {
		devs = devs[:maxDeviceSummaries]
		truncated = true
	}
	for _, ds := range devs {
		qual, phase := deviceQualPhase(ds)
		if ns.scope == nodeHeld || class != "" {
			qual, phase = v1alpha1.QualificationUnknown, v1alpha1.LifecyclePhaseUnknown
		}
		st.DeviceSummaries = append(st.DeviceSummaries, v1alpha1.DeviceSummary{
			Name:               ds.obj.Name,
			UID:                string(ds.obj.UID),
			DesiredState:       ds.obj.Spec.DesiredState,
			ObservedGeneration: ds.obj.Generation,
			Qualification:      qual,
			LifecyclePhase:     phase,
		})
	}

	tail := []string{internalErrorToken(ev.class)}
	if ev.noObservation {
		tail = append(tail, "no_observation")
	}
	eligTail := append([]string(nil), tail...)
	if truncated {
		eligTail = append(eligTail, truncatedToken([]string{"deviceSummaries"}))
	}
	if nodeEnforce(w, ns) {
		eligTail = append(eligTail, "gate_not_implemented")
	}

	freshStatus := metav1.ConditionUnknown
	freshReason := ev.reason
	if class == "" && nodeEvidenceFresh(ns) {
		freshStatus = metav1.ConditionTrue
		freshReason = v1alpha1.ReasonReady
	} else if ev.obs != nil && ev.obs.Completeness == fleet.CompletenessPartial {
		freshReason = v1alpha1.ReasonPartialSnapshot
	}
	completeness := "Unknown"
	if ev.obs != nil {
		completeness = ev.obs.Completeness.String()
	}
	specs := []condSpec{
		{v1alpha1.ConditionTypeEvidenceFresh, freshStatus, freshReason, message{prefix: "completeness=" + completeness, tail: tail}.String()},
		{v1alpha1.ConditionTypeNodeEligible, eligibilityStatus(ev.eligibility), ev.reason,
			message{prefix: "eligibility=" + ev.eligibility.String() + " qualification=" + ev.qualification.String(), tail: eligTail}.String()},
	}
	st.Conditions = buildConditions(cur.Status.Conditions, specs, cur.Generation, now)
	return st
}

// nodeEvidenceFresh is the True condition of EvidenceFresh (GKA-077): a node
// decision that is not Unknown and a decision, not Unknown, for every D* device.
func nodeEvidenceFresh(ns *nodeState) bool {
	if ns.scope != nodeSelected || !ns.assessed || ns.eval.class != "" {
		return false
	}
	nd := ns.eval.decision
	if nd == nil || nd.Qualification == fleet.QualificationUnknown {
		return false
	}
	for _, ds := range ns.dStar {
		if ds.proj == nil || ds.proj.dec.Qualification == fleet.QualificationUnknown {
			return false
		}
	}
	return true
}
