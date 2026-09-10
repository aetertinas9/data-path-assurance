package fleet

import (
	"fmt"
	"math"
)

func AdmitSnapshot(admittedSession int64, previous *SnapshotCursor, candidate SnapshotEnvelope) (SnapshotCursor, SnapshotOrder, error) {
	if admittedSession < 1 {
		return SnapshotCursor{}, 0, invalidf("AdmitSnapshot admittedSession is not positive")
	}
	if err := candidate.Validate(); err != nil {
		return SnapshotCursor{}, 0, err
	}
	if previous != nil {
		if err := previous.Validate(); err != nil {
			return SnapshotCursor{}, 0, invalidf("AdmitSnapshot previous: %v", err)
		}
	}
	if candidate.Session != admittedSession {
		return unchanged(previous), SnapshotWrongSession, nil
	}
	if previous == nil {
		if candidate.Sequence != 0 || candidate.Completeness != CompletenessComplete {
			return SnapshotCursor{}, SnapshotWrongSession, nil
		}
		return cursorOf(candidate, true), SnapshotAccepted, nil
	}
	old := *previous
	if old.Session != admittedSession || old.NodeUID != candidate.NodeUID || old.BootID != candidate.BootID {
		return old, SnapshotWrongSession, nil
	}
	if candidate.Sequence == old.Sequence {
		if candidate.PayloadDigest == old.PayloadDigest {
			return old, SnapshotDuplicate, nil
		}
		return old, SnapshotConflict, fmt.Errorf("%w: sequence %d has a different payload digest", ErrConflict, candidate.Sequence)
	}
	if candidate.Sequence < old.Sequence {
		return old, SnapshotOutOfOrder, nil
	}
	if old.Sequence == math.MaxUint64 || candidate.Sequence != old.Sequence+1 {
		return old, SnapshotGap, nil
	}
	return cursorOf(candidate, old.Baseline), SnapshotAccepted, nil
}

func cursorOf(e SnapshotEnvelope, baseline bool) SnapshotCursor {
	return SnapshotCursor{NodeUID: e.NodeUID, BootID: e.BootID, PayloadDigest: e.PayloadDigest, BundleRevision: e.BundleRevision, Session: e.Session, Sequence: e.Sequence, Completeness: e.Completeness, ObservedAt: e.ObservedAt, Baseline: baseline}
}
func unchanged(p *SnapshotCursor) SnapshotCursor {
	if p == nil {
		return SnapshotCursor{}
	}
	return *p
}
