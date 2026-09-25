package offline

import (
	"context"
	"errors"
	"strconv"

	"github.com/aetertinas9/data-path-assurance/internal/app"
)

// Source reads one offline snapshot artifact and one fleet file. It is the
// file-backed app.ReplaySource.
type Source struct {
	artifactPath, fleetPath string
}

// NewSource returns a source for the artifact and fleet file at the given
// paths. Nothing is opened until LoadReplay.
func NewSource(artifactPath, fleetPath string) app.ReplaySource {
	return &Source{artifactPath: artifactPath, fleetPath: fleetPath}
}

// LoadReplay reads, checks and converts both inputs in a fixed order: the
// artifact is opened, bounded and fully validated before the fleet file is
// touched; the fleet file is then opened, bounded and validated, and finally
// the two are matched against each other. Each rejection wraps
// ErrArtifactInvalid, ErrFleetInvalid or ErrBoundExceeded.
func (s *Source) LoadReplay(ctx context.Context) (app.Replay, error) {
	art, err := loadArtifact(s.artifactPath)
	if err != nil {
		return app.Replay{}, err
	}
	if err := ctx.Err(); err != nil {
		return app.Replay{}, err
	}

	fleetBytes, err := readBounded(s.fleetPath, MaxFleetBytes)
	switch {
	case errors.Is(err, errTooLarge):
		return app.Replay{}, inputError(ErrBoundExceeded, "fleet file is larger than 1048576 bytes")
	case err != nil:
		return app.Replay{}, inputError(ErrFleetInvalid, "fleet file is absent, unreadable or not a regular file")
	}
	fl, err := decodeFleet(fleetBytes)
	if err != nil {
		return app.Replay{}, err
	}

	if fl.clusterID != art.clusterID {
		return app.Replay{}, fleetFail("clusterID", "does not match the artifact clusterID")
	}
	for i, d := range fl.devices {
		if d.Node.UID != art.node.UID {
			return app.Replay{}, fleetFail("devices["+strconv.Itoa(i)+"].nodeRef.uid", "does not match the artifact node.uid")
		}
	}
	return app.Replay{
		ClusterID:      art.clusterID,
		Node:           art.node,
		CollectorTrust: art.trust,
		Frames:         art.frames,
		FleetUID:       fl.fleetUID,
		Policy:         fl.policy,
		Devices:        fl.devices,
	}, nil
}

// loadArtifact reads, decodes, checks and converts the artifact. Only the
// converted content outlives the call.
func loadArtifact(path string) (artifactContent, error) {
	data, err := readBounded(path, MaxArtifactBytes)
	switch {
	case errors.Is(err, errTooLarge):
		return artifactContent{}, inputError(ErrBoundExceeded, "artifact file is larger than 67108864 bytes")
	case err != nil:
		return artifactContent{}, inputError(ErrArtifactInvalid, "artifact file is absent, unreadable or not a regular file")
	}
	raw, err := decodeArtifact(data)
	if err != nil {
		return artifactContent{}, err
	}
	return checkArtifact(&raw)
}
