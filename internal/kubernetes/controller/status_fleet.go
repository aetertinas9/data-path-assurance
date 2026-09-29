package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// fleetStatus builds the desired status of a GPUFleet (GKA-078, GKA-079,
// GKA-087). class, when not empty, renders the minimal InternalError status of
// that class (the write fallback of GKA-125); a fleet with an invalid selector
// always renders that way.
func fleetStatus(cur *v1alpha1.GPUFleet, fs *fleetState, class string, now time.Time) v1alpha1.GPUFleetStatus {
	st := v1alpha1.GPUFleetStatus{
		ObservedGeneration: cur.Generation,
		OwnedNodeRefs:      cur.Status.OwnedNodeRefs,
	}
	if st.OwnedNodeRefs == nil {
		st.OwnedNodeRefs = []v1alpha1.OwnedNodeRef{}
	}
	var gate string
	if fs.enforce {
		gate = "gate_not_implemented"
	}
	build := func(status metav1.ConditionStatus, reason string, m message) {
		st.Conditions = buildConditions(cur.Status.Conditions, []condSpec{{v1alpha1.ConditionTypeFleetReady, status, reason, m.String()}}, cur.Generation, now)
	}
	counts := func(selected, ready, degraded, unknown int) string {
		return fmt.Sprintf("selected=%d ready=%d degraded=%d unknown=%d", selected, ready, degraded, unknown)
	}

	if class == "" && !fs.validSel {
		class = classInvalidSelector
	}
	if class != "" {
		build(metav1.ConditionUnknown, v1alpha1.ReasonInternalError,
			message{prefix: counts(0, 0, 0, 0), tail: []string{internalErrorToken(class), gate}})
		return st
	}

	selected := len(fs.selected)
	st.SelectedCount = int32(selected)
	if fs.overCap {
		st.UnknownCount = int32(selected)
		build(metav1.ConditionUnknown, v1alpha1.ReasonTargetCapacityExceeded,
			message{prefix: counts(selected, 0, 0, selected), tail: []string{gate}})
		return st
	}

	ready, degraded, unknown := 0, 0, 0
	allQualified, anyConflict, anyInternal, allComplete := true, false, false, selected > 0
	for _, ns := range fs.selected {
		ev := ns.eval
		switch {
		case ev.eligibility == fleet.EligibilityEligible:
			ready++
		case ev.qualification == fleet.QualificationDisqualified:
			degraded++
		default:
			unknown++
		}
		if ev.qualification != fleet.QualificationQualified {
			allQualified = false
		}
		if ns.scope == nodeConflict {
			anyConflict = true
		}
		if ev.class != "" {
			anyInternal = true
		}
		if ev.class != "" || ev.decision == nil || ev.decision.Selection != fleet.SelectionComplete {
			allComplete = false
		}
	}
	st.ReadyCount, st.DegradedCount, st.UnknownCount = int32(ready), int32(degraded), int32(unknown)
	if allComplete {
		rev := assessmentRevision(fs.selected)
		st.AssessmentRevision = &rev
	}
	m := message{prefix: counts(selected, ready, degraded, unknown), tail: []string{gate}}
	switch {
	case selected == 0 || !fs.anyDevices:
		build(metav1.ConditionUnknown, v1alpha1.ReasonNoMatchingDevices, m)
	case degraded >= 1:
		build(metav1.ConditionFalse, v1alpha1.ReasonDegraded, m)
	case allQualified && ready < selected:
		m.head = []string{fmt.Sprintf("qualified_not_eligible=%d", selected-ready)}
		build(metav1.ConditionUnknown, v1alpha1.ReasonValidating, m)
	case anyConflict:
		build(metav1.ConditionUnknown, v1alpha1.ReasonConflict, m)
	case anyInternal:
		build(metav1.ConditionUnknown, v1alpha1.ReasonInternalError, m)
	case unknown >= 1:
		build(metav1.ConditionUnknown, v1alpha1.ReasonValidating, m)
	default:
		build(metav1.ConditionTrue, v1alpha1.ReasonReady, m)
	}
	return st
}

// assessmentRevision is SHA-256 over the fleet revision label and, per node in
// Node UID order, the node UID and its AssessmentRevision (GKA-079).
func assessmentRevision(nodes []*nodeState) string {
	sorted := append([]*nodeState(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].obj.UID < sorted[j].obj.UID })
	h := sha256.New()
	h.Write([]byte("dpa.FleetAssessment.v1"))
	h.Write([]byte{0})
	for _, ns := range sorted {
		h.Write([]byte(ns.obj.UID))
		h.Write([]byte{0})
		h.Write([]byte(ns.eval.decision.AssessmentRevision))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
