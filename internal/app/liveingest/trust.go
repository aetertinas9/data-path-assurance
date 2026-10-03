package liveingest

import (
	"fmt"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// UnmatchedSource is the fixed collector profile ID stamped on a value whose
// source the collector trust profile does not list. It is never a valid
// profile ID, so such a value is judged untrusted exactly like a value of an
// offline artifact whose source the trust profile lacks. An empty stamp is
// never produced: the fleet domain rejects it and a single one would fail the
// evaluation of the whole bundle.
const UnmatchedSource = "unmatched-source"

// NVIDIAVendor is the vendor every GPU binding of a live frame claims.
const NVIDIAVendor = "NVIDIA"

// offlineProfilePrefix is the prefix of the profile IDs of offline artifacts.
const offlineProfilePrefix = "offline:"

// ValidateProfileID reports whether id may be the collector profile ID of a
// live ingest: identifier ASCII of 1 to 128 bytes, without the offline: prefix
// and not equal to UnmatchedSource. The error wraps fleet.ErrInvalidInput.
func ValidateProfileID(id string) error {
	switch {
	case !isIdentifier(id, 1, maxIDBytes):
		return fmt.Errorf("%w: collector profile ID is not 1-128 bytes of identifier ASCII", fleet.ErrInvalidInput)
	case strings.HasPrefix(id, offlineProfilePrefix):
		return fmt.Errorf("%w: collector profile ID has the offline prefix", fleet.ErrInvalidInput)
	case id == UnmatchedSource:
		return fmt.Errorf("%w: collector profile ID is the unmatched-source marker", fleet.ErrInvalidInput)
	}
	return nil
}

// ValidateTrustedSources reports whether sources may be the trusted sources of
// a live collector profile: at least one, each valid, no (capability, source)
// pair twice and none granting the external fence capability. The error wraps
// fleet.ErrInvalidInput.
func ValidateTrustedSources(sources []fleet.TrustedSource) error {
	if len(sources) == 0 {
		return fmt.Errorf("%w: no trusted source", fleet.ErrInvalidInput)
	}
	seen := make(map[fleet.TrustedSource]struct{}, len(sources))
	for _, s := range sources {
		if err := s.Validate(); err != nil {
			return err
		}
		if s.Capability == fleet.TrustExternalFence {
			return fmt.Errorf("%w: a collector profile cannot grant the external fence capability", fleet.ErrInvalidInput)
		}
		if _, dup := seen[s]; dup {
			return fmt.Errorf("%w: a trusted source is listed twice", fleet.ErrInvalidInput)
		}
		seen[s] = struct{}{}
	}
	return nil
}

// StampProfile returns the collector profile ID to stamp on a value that a
// source of the given capability reported: profileID when sources lists the
// source for that capability, UnmatchedSource otherwise. A payload's own
// source strings never grant a capability.
func StampProfile(profileID string, sources []fleet.TrustedSource, capability fleet.TrustCapability, source model.SourceRef) string {
	for _, s := range sources {
		if s.Capability == capability && s.Source == source {
			return profileID
		}
	}
	return UnmatchedSource
}
