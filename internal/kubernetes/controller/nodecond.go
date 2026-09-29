package controller

import (
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// nodeCondAction is what a pass does about the Node condition of one node.
type nodeCondAction int

const (
	nodeCondNone nodeCondAction = iota
	nodeCondPost
	nodeCondRemove
)

// conditionKey is the server-side apply list key of the controller's
// condition inside a managedFields entry.
const conditionKey = `k:{"type":"` + v1alpha1.NodeConditionTypeGPUFleetReady + `"}`

// ownsNodeCondition reports whether this controller published its condition on
// the node: an Apply entry of the node-condition manager on the status
// subresource that owns the DataPathGPUFleetReady list key (GKA-113).
func ownsNodeCondition(n *corev1.Node) bool {
	for _, mf := range n.ManagedFields {
		if mf.Manager != FieldManagerNodeCondition || mf.Operation != metav1.ManagedFieldsOperationApply || mf.Subresource != "status" || mf.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(mf.FieldsV1.Raw, &fields); err != nil {
			continue
		}
		status, _ := fields["f:status"].(map[string]any)
		conds, _ := status["f:conditions"].(map[string]any)
		if _, ok := conds[conditionKey]; ok {
			return true
		}
	}
	return false
}

// planNodeCondition decides what happens to the Node condition of ns
// (GKA-063, GKA-068, GKA-110, GKA-113).
func planNodeCondition(ns *nodeState) nodeCondAction {
	owned := ownsNodeCondition(ns.obj)
	switch ns.scope {
	case nodeHeld:
		if owned {
			return nodeCondPost
		}
	case nodeConflict:
		for _, fs := range ns.fleets {
			if fs.enforce {
				if owned {
					return nodeCondRemove
				}
				return nodeCondNone
			}
		}
		return nodeCondPost
	case nodeCapacity:
		if ns.fleets[0].enforce {
			if owned {
				return nodeCondRemove
			}
		} else if owned {
			return nodeCondPost
		}
	case nodeSelected:
		if ns.fleets[0].enforce {
			if owned {
				return nodeCondRemove
			}
			return nodeCondNone
		}
		return nodeCondPost
	default: // not selected
		if owned {
			return nodeCondRemove
		}
	}
	return nodeCondNone
}

// desiredNodeCondition builds the condition to publish (GKA-111). class, when
// not empty, renders it as an InternalError of that class (write fallback).
func desiredNodeCondition(ns *nodeState, class string, now time.Time) corev1.NodeCondition {
	ev := ns.eval
	if class != "" {
		ev = newNodeEval(v1alpha1.ReasonInternalError)
		ev.class = class
	}
	var names []string
	switch ns.scope {
	case nodeHeld:
		names = ns.holdFleets
	default:
		for _, fs := range ns.fleets {
			names = append(names, fs.obj.Name)
		}
	}
	tail := []string{internalErrorToken(ev.class)}
	if ev.noObservation {
		tail = append(tail, "no_observation")
	}
	status := corev1.ConditionUnknown
	switch eligibilityStatus(ev.eligibility) {
	case metav1.ConditionTrue:
		status = corev1.ConditionTrue
	case metav1.ConditionFalse:
		status = corev1.ConditionFalse
	}
	cond := corev1.NodeCondition{
		Type:               v1alpha1.NodeConditionTypeGPUFleetReady,
		Status:             status,
		Reason:             ev.reason,
		Message:            message{prefix: "fleet=" + nameList(names, maxMessageLength-len("fleet=")-tailLen(tail)), tail: tail}.String(),
		LastTransitionTime: metaTime(now.UTC().Truncate(time.Second)),
	}
	for _, old := range ns.obj.Status.Conditions {
		if old.Type == cond.Type && old.Status == cond.Status {
			cond.LastTransitionTime = old.LastTransitionTime
			break
		}
	}
	return cond
}

func tailLen(tail []string) int {
	n := 0
	for _, t := range tail {
		if t != "" {
			n += len("; ") + len(t)
		}
	}
	return n
}

// nodeConditionSame is the GKA-114 no-op test against the Node's current entry.
func nodeConditionSame(ns *nodeState, want corev1.NodeCondition) bool {
	for _, have := range ns.obj.Status.Conditions {
		if have.Type == want.Type {
			return have.Status == want.Status && have.Reason == want.Reason && have.Message == want.Message &&
				have.LastTransitionTime.UTC().Truncate(time.Second).Equal(want.LastTransitionTime.UTC().Truncate(time.Second))
		}
	}
	return false
}
