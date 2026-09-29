package app

import (
	"context"
	"errors"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// ErrNoObservation is returned by a LiveBundleSource that has no observation
// for the queried node. It is the "no observation" signal of the source port.
var ErrNoObservation = errors.New("app: no observation")

// LiveBundleSource supplies the assessment bundles the live assessor evaluates.
// A source that has nothing for the node returns ErrNoObservation (or an empty
// set); any other error is a failure of the source itself.
type LiveBundleSource interface {
	NodeBundles(ctx context.Context, q LiveBundleQuery) (LiveBundleSet, error)
}

// LiveBundleQuery names what a source is asked for: the bundles of the given
// intents on one node. The assessor passes copies of its request slices.
type LiveBundleQuery struct {
	Node    fleet.NodeRef
	Policy  fleet.Policy
	Intents []fleet.Intent
	Now     time.Time
}

// LiveBundleSet is what a source returns for one node: a bundle per device it
// can evaluate. A subset of the requested devices is valid.
type LiveBundleSet struct {
	Devices []LiveDeviceBundle
}

// LiveDeviceBundle is one device's bundle with the detail records that go with
// its decision. The assessor overwrites Bundle.Policy and Bundle.Intent with the
// request's values.
type LiveDeviceBundle struct {
	DeviceUID  string
	Bundle     fleet.AssessmentBundle
	Findings   []FindingRecord
	Evidence   []EvidenceRecord
	Allocation *AllocationRecord
}

type noObservationSource struct{}

// NewNoObservationSource returns a source that never observes anything: its
// NodeBundles always returns an empty set and ErrNoObservation.
func NewNoObservationSource() LiveBundleSource { return noObservationSource{} }

// NodeBundles reports that nothing is observed.
func (noObservationSource) NodeBundles(context.Context, LiveBundleQuery) (LiveBundleSet, error) {
	return LiveBundleSet{}, ErrNoObservation
}
