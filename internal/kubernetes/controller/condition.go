package controller

import (
	"encoding/json"
	"slices"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// condSpec is one condition the controller publishes.
type condSpec struct {
	typ    string
	status metav1.ConditionStatus
	reason string
	msg    string
}

// buildConditions returns the desired conditions list (GKA-074): the published
// conditions, with lastTransitionTime kept from the existing entry of the same
// type while the status is unchanged, plus every existing entry of a type this
// controller does not publish, in type byte order.
func buildConditions(existing []metav1.Condition, specs []condSpec, generation int64, now time.Time) []metav1.Condition {
	stamp := metaTime(now.UTC().Truncate(time.Second))
	out := make([]metav1.Condition, 0, len(specs)+len(existing))
	published := map[string]bool{}
	for _, s := range specs {
		published[s.typ] = true
		c := metav1.Condition{
			Type:               s.typ,
			Status:             s.status,
			ObservedGeneration: generation,
			LastTransitionTime: stamp,
			Reason:             s.reason,
			Message:            s.msg,
		}
		for _, old := range existing {
			if old.Type == s.typ && old.Status == s.status {
				c.LastTransitionTime = old.LastTransitionTime
				break
			}
		}
		out = append(out, c)
	}
	for _, old := range existing {
		if !published[old.Type] {
			out = append(out, *old.DeepCopy())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// statusEqual is the GKA-122 sameness test: both statuses, serialized the way
// the API server stores them, are equal once conditions are in type order.
// Times therefore compare at whole seconds.
func statusEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && slices.Equal(ja, jb)
}
