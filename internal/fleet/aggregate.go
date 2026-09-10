package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"
)

func AggregateNode(input AggregateNodeInput, previous *NodeDecision, now time.Time) (NodeDecision, error) {
	if now.IsZero() {
		return NodeDecision{}, invalidf("AggregateNode now is the zero time")
	}
	if err := input.Validate(); err != nil {
		return NodeDecision{}, err
	}
	if previous != nil {
		if err := previous.Validate(); err != nil {
			return NodeDecision{}, invalidf("AggregateNode previous: %v", err)
		}
	}
	devices := cloneDeviceAggregates(input.Devices)
	slices.SortFunc(devices, func(a, b DeviceAggregate) int { return strings.Compare(a.DeviceUID, b.DeviceUID) })
	seen := map[string]struct{}{}
	for _, a := range devices {
		if _, ok := seen[a.DeviceUID]; ok {
			return NodeDecision{}, invalidf("AggregateNode duplicate device UID %q", a.DeviceUID)
		}
		seen[a.DeviceUID] = struct{}{}
		if a.NodeUID != input.Node.UID || a.Decision.NodeUID != input.Node.UID || a.DeviceUID != a.Decision.DeviceUID || a.Desired != a.Decision.Desired || a.MetadataGeneration != a.Decision.MetadataGeneration {
			return NodeDecision{}, invalidf("AggregateNode device aggregate does not match its decision or node")
		}
	}
	d := NodeDecision{NodeUID: input.Node.UID, FleetUID: input.FleetUID, Selection: input.Selection, DeviceCount: len(devices), EvaluatedAt: now, Qualification: QualificationUnknown, Eligibility: EligibilityUnknown, Reason: "Validating"}
	d.AssessmentRevision = assessmentDigest(input.FleetUID, devices)
	if input.Selection != SelectionComplete {
		switch input.Selection {
		case SelectionConflict:
			d.Reason = "Conflict"
		case SelectionNoDevices:
			d.Reason = "NoMatchingDevices"
		default:
			d.Reason = "Validating"
		}
		return d, nil
	}
	if len(devices) == 0 {
		d.Reason = "NoMatchingDevices"
		return d, nil
	}
	allQualified := true
	anyDisqualified := false
	allReady := input.Selection == SelectionComplete
	futureChild, expiredChild := false, false
	for _, a := range devices {
		child := a.Decision
		if child.EvaluatedAt.After(now) {
			futureChild = true
			allQualified = false
			allReady = false
			continue
		}
		if child.Qualification == QualificationQualified && !child.ValidUntil.After(now) {
			expiredChild = true
			allQualified = false
			allReady = false
			continue
		}
		switch child.Qualification {
		case QualificationQualified:
			d.ValidUntil = minTime(d.ValidUntil, child.ValidUntil)
		case QualificationDisqualified:
			anyDisqualified = true
			allQualified = false
		default:
			allQualified = false
		}
		if a.Desired != DesiredInService || child.Phase != PhaseReady {
			allReady = false
		}
	}
	switch {
	case futureChild || expiredChild:
		d.Qualification = QualificationUnknown
		d.ValidUntil = time.Time{}
	case anyDisqualified:
		d.Qualification = QualificationDisqualified
		d.ValidUntil = time.Time{}
	case allQualified:
		d.Qualification = QualificationQualified
	default:
		d.Qualification = QualificationUnknown
		d.ValidUntil = time.Time{}
	}
	if input.Selection == SelectionComplete {
		if allReady && !futureChild && !expiredChild {
			d.Eligibility = EligibilityEligible
		} else {
			d.Eligibility = EligibilityIneligible
		}
	} else {
		d.Eligibility = EligibilityUnknown
	}
	if futureChild {
		d.Eligibility = EligibilityUnknown
		d.Reason = "FutureObservation"
	} else if expiredChild {
		d.Eligibility = EligibilityUnknown
		d.Reason = "EvidenceStale"
	} else if d.Eligibility == EligibilityEligible {
		d.Reason = "Ready"
	} else if input.Selection != SelectionComplete {
		switch input.Selection {
		case SelectionConflict:
			d.Reason = "Conflict"
		case SelectionNoDevices:
			d.Reason = "NoMatchingDevices"
		default:
			d.Reason = "Validating"
		}
	} else {
		d.Reason = "Degraded"
	}
	if input.Selection == SelectionComplete && !futureChild && !expiredChild && allAccepted(devices) {
		d.NormalPointAt = devices[0].Decision.LastCompositeMin
		for _, a := range devices[1:] {
			if a.Decision.LastCompositeMin.Before(d.NormalPointAt) {
				d.NormalPointAt = a.Decision.LastCompositeMin
			}
		}
		d.NormalPointDigest = normalDigest(devices)
	}
	return d, nil
}

func allAccepted(devices []DeviceAggregate) bool {
	if len(devices) == 0 {
		return false
	}
	for _, a := range devices {
		d := a.Decision
		if !d.AcceptedNormalPoint || d.LastCompositeMin.IsZero() || d.Qualification != QualificationQualified || !d.ValidUntil.After(d.EvaluatedAt) {
			return false
		}
	}
	return true
}
func deviceTuple(a DeviceAggregate) string {
	var b strings.Builder
	parts := []string{a.DeviceUID, strconv.FormatInt(a.MetadataGeneration, 10), a.Decision.GraphRevision, a.Decision.LastCompositeMin.UTC().Format(time.RFC3339Nano)}
	digests := make([]string, len(a.Decision.CoverageCursors))
	for i, c := range a.Decision.CoverageCursors {
		digests[i] = c.EvidenceDigest
	}
	slices.Sort(digests)
	parts = append(parts, digests...)
	for _, p := range parts {
		writeLength(&b, p)
	}
	return b.String()
}
func assessmentDigest(fleetUID string, devices []DeviceAggregate) string {
	h := sha256.New()
	writeLength(h, fleetUID)
	for _, a := range devices {
		writeLength(h, deviceTuple(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func normalDigest(devices []DeviceAggregate) string {
	h := sha256.New()
	for _, a := range devices {
		writeLength(h, deviceTuple(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}
