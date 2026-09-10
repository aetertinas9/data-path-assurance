package fleet

import (
	"slices"
	"strings"
	"time"
)

const (
	gateKey    = "infrastructure.data-path-assurance.io/gpu-path"
	gateValue  = "not-ready"
	gateEffect = "NoSchedule"
)

func EvaluateGate(input GateInput, previous *GateDecision, now time.Time) (GateDecision, error) {
	if now.IsZero() {
		return GateDecision{}, invalidf("EvaluateGate now is the zero time")
	}
	if err := input.Validate(); err != nil {
		return GateDecision{}, err
	}
	if previous != nil {
		if err := previous.Validate(); err != nil {
			return GateDecision{}, invalidf("EvaluateGate previous: %v", err)
		}
	}
	if input.NodeDecision.NodeUID != input.Node.UID {
		return GateDecision{}, invalidf("EvaluateGate node decision UID does not match node")
	}
	if previous != nil && !zero(previous.Ownership) {
		if zero(input.Ownership) || !sameOwnedTarget(previous.Ownership, input.Ownership) {
			return GateDecision{}, invalidf("EvaluateGate previous ownership does not match input ownership")
		}
	}
	if input.Mode == GateModeAudit {
		return GateDecision{Action: GateActionNone, Reason: "Ready", EvaluatedAt: now}, nil
	}
	if input.Mode == GateModeCleanup {
		if input.Ownership.CleanupRequestedAt.IsZero() {
			return GateDecision{}, invalidf("cleanup ownership lacks CleanupRequestedAt")
		}
		return evaluateCleanup(input, previous, now), nil
	}
	return evaluateEnforce(input, previous, now)
}

func evaluateEnforce(input GateInput, previous *GateDecision, now time.Time) (GateDecision, error) {
	owned := !zero(input.Ownership)
	if owned {
		if err := input.Ownership.Validate(); err != nil {
			return GateDecision{}, err
		}
		if input.Ownership.OwnerFleetUID != input.FleetUID || input.Ownership.NodeUID != input.Node.UID || !samePolicy(input.Ownership.Policy, input.Policy) {
			return GateDecision{}, invalidf("enforce ownership does not match current fleet/node/policy")
		}
	}
	conflict := input.FleetConflict || input.NodeDecision.Selection == SelectionConflict
	noDevices := input.NodeDecision.Selection == SelectionNoDevices
	if conflict || noDevices {
		if owned {
			return GateDecision{Action: GateActionMaintain, Ownership: cloneOwnership(input.Ownership), Reason: selectionReason(conflict), EvaluatedAt: now}, nil
		}
		return GateDecision{Action: GateActionNone, Reason: selectionReason(conflict), EvaluatedAt: now}, nil
	}
	good := input.NodeDecision.Qualification == QualificationQualified && input.NodeDecision.Eligibility == EligibilityEligible && nodeFresh(input.NodeDecision, now)
	if !good {
		if owned {
			o := cloneOwnership(input.Ownership)
			clearRecovery(&o)
			return GateDecision{Action: GateActionMaintain, Ownership: o, Reason: gateBlockedReason(input.NodeDecision), EvaluatedAt: now}, nil
		}
		o := GateOwnership{OwnerFleetUID: input.FleetUID, NodeUID: input.Node.UID, Key: gateKey, Value: gateValue, Effect: gateEffect, Policy: cloneCleanupPolicy(input.Policy), Phase: CleanupNone}
		return GateDecision{Action: GateActionAdd, Ownership: o, Reason: gateBlockedReason(input.NodeDecision), EvaluatedAt: now}, nil
	}
	if !owned {
		return GateDecision{Action: GateActionNone, Reason: "Ready", EvaluatedAt: now}, nil
	}
	o, accepted := advanceRecovery(cloneOwnership(input.Ownership), input.NodeDecision, input.ControllerID, previous, now, input.Policy.Freshness)
	if accepted && !o.RecoveryStartedAt.IsZero() && input.NodeDecision.NormalPointAt.Sub(o.RecoveryStartedAt) >= input.Policy.ReadyFor {
		return GateDecision{Action: GateActionRemove, Ownership: o, Reason: "Ready", EvaluatedAt: now}, nil
	}
	return GateDecision{Action: GateActionMaintain, Ownership: o, Reason: "Validating", EvaluatedAt: now}, nil
}

func evaluateCleanup(input GateInput, previous *GateDecision, now time.Time) GateDecision {
	o := cloneOwnership(input.Ownership)
	o.Phase = CleanupPending
	duration := o.Policy.ReadyFor
	if duration < 5*time.Minute {
		duration = 5 * time.Minute
	}
	good := input.NodeDecision.Qualification == QualificationQualified && nodeFresh(input.NodeDecision, now)
	if !good {
		clearRecovery(&o)
		return GateDecision{Action: GateActionMaintain, Ownership: o, Reason: "CleanupPending", EvaluatedAt: now}
	}
	var accepted bool
	o, accepted = advanceRecovery(o, input.NodeDecision, input.ControllerID, previous, now, o.Policy.Freshness)
	if accepted && !o.RecoveryStartedAt.IsZero() && input.NodeDecision.NormalPointAt.Sub(o.RecoveryStartedAt) >= duration {
		o.Phase = CleanupCompleted
		return GateDecision{Action: GateActionRemove, Ownership: o, Reason: "CleanupStable", EvaluatedAt: now}
	}
	if !o.RecoveryStartedAt.IsZero() {
		o.Phase = CleanupStable
	}
	return GateDecision{Action: GateActionMaintain, Ownership: o, Reason: "CleanupPending", EvaluatedAt: now}
}
func advanceRecovery(o GateOwnership, n NodeDecision, controller string, previous *GateDecision, now time.Time, freshness time.Duration) (GateOwnership, bool) {
	if n.NormalPointDigest == "" || n.NormalPointAt.IsZero() || n.EvaluatedAt.After(now) || !now.Before(n.ValidUntil) || now.Sub(n.NormalPointAt) > freshness {
		clearRecovery(&o)
		return o, false
	}
	continuity := previous != nil && previous.Ownership.RecoveryControllerID == controller && previous.Ownership.LastRecoveryDigest != "" && previous.Ownership.LastRecoveryDigest != n.NormalPointDigest && n.NormalPointAt.After(previous.Ownership.LastRecoveryPointAt) && n.NormalPointAt.Sub(previous.Ownership.LastRecoveryPointAt) <= freshness
	if continuity {
		o.RecoveryStartedAt = previous.Ownership.RecoveryStartedAt
	} else {
		o.RecoveryStartedAt = n.NormalPointAt
	}
	o.LastRecoveryPointAt = n.NormalPointAt
	o.LastRecoveryDigest = n.NormalPointDigest
	o.RecoveryControllerID = controller
	return o, true
}
func nodeFresh(n NodeDecision, now time.Time) bool {
	return !n.EvaluatedAt.After(now) && n.ValidUntil.After(now)
}
func clearRecovery(o *GateOwnership) {
	o.RecoveryStartedAt = time.Time{}
	o.LastRecoveryPointAt = time.Time{}
	o.RecoveryControllerID = ""
	o.LastRecoveryDigest = ""
}
func sameOwnedTarget(a, b GateOwnership) bool {
	return a.OwnerFleetUID == b.OwnerFleetUID && a.NodeUID == b.NodeUID && a.Key == b.Key && a.Value == b.Value && a.Effect == b.Effect && samePolicy(a.Policy, b.Policy)
}
func samePolicy(a, b CleanupPolicySnapshot) bool {
	if a.PolicyVersion != b.PolicyVersion || a.Freshness != b.Freshness || a.ReadyFor != b.ReadyFor || len(a.RequiredCoverage) != len(b.RequiredCoverage) {
		return false
	}
	left := cloneCoverageRequirements(a.RequiredCoverage)
	right := cloneCoverageRequirements(b.RequiredCoverage)
	slices.SortFunc(left, func(x, y CoverageRequirement) int { return strings.Compare(x.Name, y.Name) })
	slices.SortFunc(right, func(x, y CoverageRequirement) int { return strings.Compare(x.Name, y.Name) })
	return slices.Equal(left, right)
}
func selectionReason(conflict bool) string {
	if conflict {
		return "Conflict"
	}
	return "NoMatchingDevices"
}
func gateBlockedReason(n NodeDecision) string {
	if n.Reason != "" {
		return n.Reason
	}
	if n.Qualification == QualificationDisqualified {
		return "Degraded"
	}
	return "Validating"
}
func cloneCleanupPolicy(p CleanupPolicySnapshot) CleanupPolicySnapshot {
	p.RequiredCoverage = cloneCoverageRequirements(p.RequiredCoverage)
	return p
}
