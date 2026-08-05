// Package model holds the value types shared by the whole domain core
// (identity, graph, evidence, correlation, domains, impact, policy, app).
//
// It is the ubiquitous language of the system: the names and invariants
// defined here are the vocabulary of every later specification.
//
// Three constraints shape everything in this package.
//
// Purity: nothing here touches I/O, the clock, randomness, or the
// environment. Every time.Time is supplied by the caller as a value, so the
// same arguments always produce the same result.
//
// No anemic values: invariants are enforced by the constructors and by
// Validate. A value assembled without going through a constructor can always
// be re-checked with Validate, and every value a constructor returns satisfies
// Validate.
//
// No dependencies: the package imports nothing outside the Go standard
// library. The repository's "make arch-check" target enforces this.
//
// Every constructor reports invariant violations by returning the zero value
// together with an error that wraps [ErrInvalid]; no exported function in this
// package panics.
package model
