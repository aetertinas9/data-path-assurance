package identity

import (
	"errors"
	"fmt"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// The sentinels this package reports. Violations of argument validity are not
// among them: an identity is valid by the model's rules plus one rule of this
// package, so they are reported with [model.ErrInvalid], which a caller working
// with pkg/model already tests for.
var (
	// ErrNotRegistered reports that an identity resolves to no asset, or that a
	// registering call named a canonical identity no asset is registered under.
	ErrNotRegistered = errors.New("identity: not registered")

	// ErrAmbiguous reports that identities asserted to name one and the same
	// asset resolve to two or more different ones.
	ErrAmbiguous = errors.New("identity: ambiguous identity set")

	// ErrConflict reports that one identity is claimed by two assets. Every
	// error reporting it also carries the details as a [*Conflict].
	ErrConflict = errors.New("identity: identity conflict")
)

// invalidf builds an error wrapping model.ErrInvalid, so that an argument
// violation here is tested for exactly as one from pkg/model is.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", model.ErrInvalid, fmt.Sprintf(format, args...))
}

// Conflict is one rejected registration: the identity both assets claim, the
// resolution that stands, and the resolution the rejected registration asked
// for.
//
// A *Conflict is the error a conflicting registration returns. It unwraps to
// [ErrConflict], so both errors.Is(err, ErrConflict) and errors.As(err, &c)
// hold for it.
type Conflict struct {
	ID       model.TypedID
	Existing model.AssetRef
	Claimed  model.AssetRef
}

// Error names the disputed identity and both claimants.
func (c *Conflict) Error() string {
	if c == nil {
		return "identity: <nil conflict>"
	}
	return fmt.Sprintf("identity: %s is claimed by both %s and %s; the first claim stands",
		c.ID, c.Existing.Key(), c.Claimed.Key())
}

// Unwrap reports ErrConflict, the sentinel every conflict shares.
func (c *Conflict) Unwrap() error {
	return ErrConflict
}
