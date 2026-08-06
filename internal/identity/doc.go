// Package identity resolves the typed identities the system observes to the
// assets they name, and keeps the aliases each asset is known by.
//
// One invariant governs everything here: an identity resolves to at most one
// asset. When a second asset claims an identity that already resolves, the
// package does not merge the two. The claim is rejected, the existing binding
// stands, and the caller is handed the details as a [Conflict] — which
// [NewConflictFinding] turns into the IDENTITY_CONFLICT finding a human reads.
// Merging two assets is a decision for that human, not for the machine.
//
// An identity may be used here when the model accepts it and its namespace
// contains no colon. The colon rule belongs to this package: it is what keeps
// the canonical rendering "<namespace>:<value>" reversible by [ParseCanonical],
// which splits at the first colon so that a value may carry colons of its own
// ("pci-bdf:0000:af:00.0").
//
// Identities are compared byte for byte on namespace and value alone; Raw and
// Source take no part. Nothing is trimmed, case-folded or Unicode-normalized:
// "PCI-BDF" and "pci-bdf" are two identities, not one, and so are " a" and "a".
// Settling on a spelling is the adapters' business, upstream of here.
//
// Purity: nothing in this package touches I/O, the clock, randomness, or the
// environment. The one operation that needs a time takes it as an argument.
// Every operation returning a collection states its order, so map iteration
// order never reaches a caller and the same calls always give the same answers.
// An operation that fails leaves the resolver exactly as it was and returns the
// zero value beside the error; no exported function or method here panics.
//
// A [Resolver] is not safe for concurrent use. Serializing access to one is the
// caller's business.
package identity
