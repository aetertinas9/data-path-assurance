package identity

import (
	"fmt"
	"time"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// conflictSuggestedStep is what an IDENTITY_CONFLICT finding asks a human to
// do. The wording is not part of the contract; that every conflict gets the
// same wording is.
const conflictSuggestedStep = "Decide by hand which asset owns the identity, then correct the source that reported the other claim. Identities are never merged automatically."

// NewConflictFinding turns a conflict into the IDENTITY_CONFLICT finding that
// reports it.
//
// The finding is a warning rather than a critical one: v0.1 audits, and a
// disputed identity does not by itself stop anything from being scheduled. It
// is held with high confidence, because a conflict is read straight off two
// registrations rather than inferred from anything. Its scope names both
// claimants in the order the conflict does — the resolution that stands, then
// the one that was rejected.
//
// Numbering a finding and dating it are the caller's business, so id and seenAt
// come in as arguments; which workloads a conflict affects is worked out
// elsewhere, so the finding claims none.
func NewConflictFinding(c Conflict, id string, evidence []model.EvidenceRef, seenAt time.Time) (model.Finding, error) {
	if err := checkIdentity("NewConflictFinding conflicting identity", c.ID); err != nil {
		return model.Finding{}, err
	}
	if err := c.Existing.Validate(); err != nil {
		return model.Finding{}, invalidf("NewConflictFinding existing asset: %s", err)
	}
	if err := c.Claimed.Validate(); err != nil {
		return model.Finding{}, invalidf("NewConflictFinding claimed asset: %s", err)
	}
	if id == "" {
		return model.Finding{}, invalidf("NewConflictFinding: finding ID is empty")
	}
	if len(evidence) == 0 {
		return model.Finding{}, invalidf("NewConflictFinding: no evidence given")
	}
	if seenAt.IsZero() {
		return model.Finding{}, invalidf("NewConflictFinding: seenAt is the zero time")
	}
	// NewFinding checks the evidence entries, re-checks everything above that
	// it can see for itself, and returns a deep copy — so neither the evidence
	// slice nor anything reachable from c can be changed afterwards to reach
	// the finding that was handed back.
	return model.NewFinding(model.Finding{
		ID:            id,
		Type:          model.FindingIdentityConflict,
		Scope:         []model.AssetRef{c.Existing, c.Claimed},
		Severity:      model.SeverityWarning,
		Confidence:    model.ConfidenceHigh,
		State:         model.StateActive,
		Evidence:      evidence,
		MissingInputs: []model.SignalRef{},
		Affected:      []model.ImpactRef{},
		FirstSeen:     seenAt,
		LastSeen:      seenAt,
		Explanation:   conflictExplanation(c),
		SuggestedStep: conflictSuggestedStep,
	})
}

// conflictExplanation states the conflict in the reader's terms, naming the
// disputed identity and both claimants — the kinds included, since a kind
// conflict has both sides claiming the very same canonical identity. The
// wording is not part of the contract, but the same conflict always produces
// the same sentence.
func conflictExplanation(c Conflict) string {
	return fmt.Sprintf(
		"Identity %s is claimed by two assets: %s (%s), which keeps it, and %s (%s), whose registration was rejected. "+
			"They were not merged: an identity resolves to at most one asset, and merging two of them is a human decision.",
		c.ID, c.Existing.Canonical, c.Existing.Kind, c.Claimed.Canonical, c.Claimed.Kind)
}
