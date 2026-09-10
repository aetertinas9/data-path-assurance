package tests_test

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

var fleetT0 = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

func fleetEnvelope(session int64, sequence uint64, completeness fleet.SnapshotCompleteness, digest string, observedAt time.Time) fleet.SnapshotEnvelope {
	return fleet.SnapshotEnvelope{
		NodeUID: "node-uid", BootID: "boot-id", PayloadDigest: digest, BundleRevision: "bundle-1",
		Session: session, Sequence: sequence, Completeness: completeness, ObservedAt: observedAt,
	}
}

// GFL-015/GFL-025/GFL-027/GFL-129/GFL-136: cold start accepts exactly a
// structurally valid complete sequence-zero snapshot in the admitted session.
func TestGFL_015_016_025_027_129_136_AdmitSnapshotColdStart(t *testing.T) {
	candidate := fleetEnvelope(7, 0, fleet.CompletenessComplete, "digest-0", fleetT0)
	cursor, order, err := fleet.AdmitSnapshot(7, nil, candidate)
	if err != nil {
		t.Fatalf("AdmitSnapshot: %v", err)
	}
	if order != fleet.SnapshotAccepted || !cursor.Baseline {
		t.Fatalf("order/cursor = %s/%#v, want Accepted baseline", order.String(), cursor)
	}
	want := fleet.SnapshotCursor{
		NodeUID: candidate.NodeUID, BootID: candidate.BootID, PayloadDigest: candidate.PayloadDigest,
		BundleRevision: candidate.BundleRevision, Session: candidate.Session, Sequence: candidate.Sequence,
		Completeness: candidate.Completeness, ObservedAt: candidate.ObservedAt, Baseline: true,
	}
	if !reflect.DeepEqual(cursor, want) {
		t.Fatalf("cursor = %#v, want %#v", cursor, want)
	}

	for _, tc := range []struct {
		name     string
		admitted int64
		value    fleet.SnapshotEnvelope
	}{
		{"partial baseline", 7, fleetEnvelope(7, 0, fleet.CompletenessPartial, "digest-0", fleetT0)},
		{"wrong session", 8, fleetEnvelope(7, 0, fleet.CompletenessComplete, "digest-0", fleetT0)},
		{"nonzero first sequence", 7, fleetEnvelope(7, 1, fleet.CompletenessComplete, "digest-1", fleetT0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotOrder, gotErr := fleet.AdmitSnapshot(tc.admitted, nil, tc.value)
			if gotErr != nil || gotOrder != fleet.SnapshotWrongSession || !reflect.DeepEqual(got, fleet.SnapshotCursor{}) {
				t.Fatalf("got %#v/%s/%v, want zero/WrongSession/nil", got, gotOrder.String(), gotErr)
			}
		})
	}
}

// GFL-026/GFL-131: an accepted successor advances all envelope fields while
// retaining the baseline bit; a duplicate returns the prior cursor exactly and
// never re-stamps candidate time, completeness, or revision.
func TestGFL_026_131_AdmitSnapshotSequenceAndDuplicate(t *testing.T) {
	base, _, err := fleet.AdmitSnapshot(7, nil, fleetEnvelope(7, 0, fleet.CompletenessComplete, "digest-0", fleetT0))
	if err != nil {
		t.Fatal(err)
	}
	nextCandidate := fleetEnvelope(7, 1, fleet.CompletenessPartial, "digest-1", fleetT0.Add(time.Second))
	nextCandidate.BundleRevision = "bundle-2"
	next, order, err := fleet.AdmitSnapshot(7, &base, nextCandidate)
	if err != nil || order != fleet.SnapshotAccepted || !next.Baseline {
		t.Fatalf("successor = %#v/%s/%v", next, order.String(), err)
	}
	if next.Sequence != 1 || next.Completeness != fleet.CompletenessPartial || next.BundleRevision != "bundle-2" || !next.ObservedAt.Equal(nextCandidate.ObservedAt) {
		t.Fatalf("successor did not preserve candidate fields: %#v", next)
	}

	duplicateCandidate := nextCandidate
	duplicateCandidate.ObservedAt = nextCandidate.ObservedAt.Add(time.Hour)
	duplicateCandidate.Completeness = fleet.CompletenessComplete
	duplicateCandidate.BundleRevision = "restamped-revision"
	duplicate, order, err := fleet.AdmitSnapshot(7, &next, duplicateCandidate)
	if err != nil || order != fleet.SnapshotDuplicate || !reflect.DeepEqual(duplicate, next) {
		t.Fatalf("duplicate = %#v/%s/%v, want unchanged %#v", duplicate, order.String(), err, next)
	}
}

// GFL-026/GFL-122/GFL-131: conflict, replay, gap, wrong session, and uint64
// wrap are domain orders. Rejections preserve the exact admitted cursor.
func TestGFL_026_122_131_AdmitSnapshotRejectOrdersPreserveCursor(t *testing.T) {
	previous := fleet.SnapshotCursor{
		NodeUID: "node-uid", BootID: "boot-id", PayloadDigest: "digest-5", BundleRevision: "bundle-5",
		Session: 7, Sequence: 5, Completeness: fleet.CompletenessComplete, ObservedAt: fleetT0, Baseline: true,
	}
	cases := []struct {
		name      string
		admitted  int64
		candidate fleet.SnapshotEnvelope
		want      fleet.SnapshotOrder
		conflict  bool
	}{
		{"same sequence different digest", 7, fleetEnvelope(7, 5, fleet.CompletenessComplete, "other", fleetT0), fleet.SnapshotConflict, true},
		{"smaller sequence", 7, fleetEnvelope(7, 4, fleet.CompletenessComplete, "digest-4", fleetT0), fleet.SnapshotOutOfOrder, false},
		{"sequence gap", 7, fleetEnvelope(7, 8, fleet.CompletenessComplete, "digest-8", fleetT0), fleet.SnapshotGap, false},
		{"candidate wrong session", 7, fleetEnvelope(8, 6, fleet.CompletenessComplete, "digest-6", fleetT0), fleet.SnapshotWrongSession, false},
		{"admitted session wrong", 8, fleetEnvelope(7, 6, fleet.CompletenessComplete, "digest-6", fleetT0), fleet.SnapshotWrongSession, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, order, err := fleet.AdmitSnapshot(tc.admitted, &previous, tc.candidate)
			if order != tc.want || !reflect.DeepEqual(got, previous) {
				t.Fatalf("got %#v/%s, want unchanged/%s", got, order.String(), tc.want.String())
			}
			if errors.Is(err, fleet.ErrConflict) != tc.conflict {
				t.Fatalf("error = %v, ErrConflict match want %v", err, tc.conflict)
			}
			if !tc.conflict && err != nil {
				t.Fatalf("domain rejection returned error: %v", err)
			}
		})
	}

	max := previous
	max.Sequence = math.MaxUint64
	max.PayloadDigest = "digest-max"
	candidate := fleetEnvelope(7, 0, fleet.CompletenessComplete, "wrapped", fleetT0.Add(time.Second))
	got, order, err := fleet.AdmitSnapshot(7, &max, candidate)
	if err != nil || order != fleet.SnapshotOutOfOrder || !reflect.DeepEqual(got, max) {
		t.Fatalf("uint64 wrap = %#v/%s/%v, want unchanged/OutOfOrder/nil", got, order.String(), err)
	}
	newSession := fleetEnvelope(8, 0, fleet.CompletenessComplete, "new-session", fleetT0.Add(2*time.Second))
	got, order, err = fleet.AdmitSnapshot(8, &max, newSession)
	if err != nil || order != fleet.SnapshotAccepted || got.Session != 8 || got.Sequence != 0 || !got.Baseline {
		t.Fatalf("new session baseline = %#v/%s/%v, want session-8 sequence-0 Accepted", got, order.String(), err)
	}
}

// GFL-013/GFL-015/GFL-025/GFL-136: future frames are structurally admissible,
// while malformed input and nonnil partial previous cursors return zero/domain
// state and ErrInvalidInput without panicking.
func TestGFL_013_015_025_136_AdmitSnapshotValidation(t *testing.T) {
	future := fleetEnvelope(3, 0, fleet.CompletenessComplete, "future", fleetT0.Add(24*time.Hour))
	if _, order, err := fleet.AdmitSnapshot(3, nil, future); err != nil || order != fleet.SnapshotAccepted {
		t.Fatalf("future frame admission = %s/%v, want Accepted/nil", order.String(), err)
	}

	invalidCandidates := []fleet.SnapshotEnvelope{
		fleetEnvelope(0, 0, fleet.CompletenessComplete, "digest", fleetT0),
		fleetEnvelope(1, 0, fleet.CompletenessUnknown, "digest", fleetT0),
		fleetEnvelope(1, 0, fleet.CompletenessComplete, "", fleetT0),
		fleetEnvelope(1, 0, fleet.CompletenessComplete, "digest", time.Time{}),
	}
	invalidCandidates[2].BootID = ""
	for _, candidate := range invalidCandidates {
		got, _, err := fleet.AdmitSnapshot(1, nil, candidate)
		if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.SnapshotCursor{}) {
			t.Errorf("malformed candidate returned %#v/%v, want zero/ErrInvalidInput", got, err)
		}
	}
	partial := fleet.SnapshotCursor{NodeUID: "node-uid"}
	if got, _, err := fleet.AdmitSnapshot(1, &partial, fleetEnvelope(1, 1, fleet.CompletenessComplete, "digest", fleetT0)); !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.SnapshotCursor{}) {
		t.Fatalf("partial previous returned %#v/%v, want zero/ErrInvalidInput", got, err)
	}
}

// GFL-025/GFL-027: admittedSession must be positive. A strictly newer admitted
// session accepts only its complete sequence-zero baseline; same/older reuse,
// partial baseline, and nonzero first frames preserve the prior cursor.
func TestGFL_025_027_AdmitSnapshotSessionTransitionBoundaries(t *testing.T) {
	validBaseline := fleetEnvelope(1, 0, fleet.CompletenessComplete, "baseline", fleetT0)
	for _, admitted := range []int64{0, -1} {
		got, _, err := fleet.AdmitSnapshot(admitted, nil, validBaseline)
		if !errors.Is(err, fleet.ErrInvalidInput) || !reflect.DeepEqual(got, fleet.SnapshotCursor{}) {
			t.Errorf("admittedSession %d returned %#v/%v, want zero/ErrInvalidInput", admitted, got, err)
		}
	}

	previous, _, err := fleet.AdmitSnapshot(7, nil, fleetEnvelope(7, 0, fleet.CompletenessComplete, "session-7", fleetT0))
	if err != nil {
		t.Fatal(err)
	}
	newBaseline := fleetEnvelope(8, 0, fleet.CompletenessComplete, "session-8", fleetT0.Add(time.Second))
	accepted, order, err := fleet.AdmitSnapshot(8, &previous, newBaseline)
	if err != nil || order != fleet.SnapshotAccepted || accepted.Session != 8 || !accepted.Baseline {
		t.Fatalf("new baseline = %#v/%s/%v", accepted, order.String(), err)
	}
	for _, candidate := range []fleet.SnapshotEnvelope{
		fleetEnvelope(7, 0, fleet.CompletenessComplete, "same-session", fleetT0.Add(time.Second)),
		fleetEnvelope(6, 0, fleet.CompletenessComplete, "older-session", fleetT0.Add(time.Second)),
		fleetEnvelope(8, 0, fleet.CompletenessPartial, "partial-new", fleetT0.Add(time.Second)),
		fleetEnvelope(8, 1, fleet.CompletenessComplete, "nonzero-new", fleetT0.Add(time.Second)),
	} {
		got, gotOrder, gotErr := fleet.AdmitSnapshot(8, &previous, candidate)
		if gotErr != nil || gotOrder != fleet.SnapshotWrongSession || !reflect.DeepEqual(got, previous) {
			t.Errorf("rejected transition returned %#v/%s/%v, want unchanged/WrongSession/nil", got, gotOrder.String(), gotErr)
		}
	}
}
