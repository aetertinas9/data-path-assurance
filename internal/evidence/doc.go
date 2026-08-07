// Package evidence organizes observations into the bounded window that rules
// read their evidence from.
//
// A [Window] groups the observations it is given into series — one series per
// source, subject, signal and set of dimensions — and keeps each series bounded
// two ways at once: by a time horizon and by a sample count. It is not a
// time-series database, and it is not meant to become one. A rule that only
// works if high-rate data is kept here is a redesign signal, not a reason for a
// larger configuration.
//
// Four things live here. Assembly: [Window.Add] and [Window.Prune] decide what
// is retained. Freshness: [Series.Fresh] says whether a series is still recent
// enough to judge by. Rate: [Series.Rate] turns a counter series into a rate
// that no reset can make negative or infinite. Level: [Window.Level] says how
// well the window's own content supports a claim.
//
// Content and judgement are kept apart. Reading a window back — its subjects,
// its series, their observations — shows what is retained whatever time it is;
// stale evidence still shows, because a stale reading and its age are what an
// explanation is made of. Only the judgements take a time, and they take it as
// an argument.
//
// A [Window] is an immutable value. Add and Prune return a new window and leave
// the receiver exactly as it was, so a window handed to a rule cannot change
// under it, and copying one — by assignment, by passing it — is free. The zero
// Window is a window with no configuration: every query and judgement answers
// as an empty window does, and assembly is refused, because there is nothing to
// say what should be retained. The same holds one level down for [Series] and
// [RateResult], both of which are values a later window operation cannot reach.
//
// Purity: nothing here touches I/O, the clock, randomness, or the environment.
// Every operation returning a collection states its order, so map iteration
// order never reaches a caller and the same calls always give the same answers.
// An operation that fails returns the zero value beside the error, except where
// a window is returned — there the receiver comes back unchanged, so that
// w, err = w.Add(o) never costs a caller the window it had. No exported
// function or method here panics, whatever it is given, and that includes the
// zero value of every type.
//
// This package does not synchronize anything. Serializing assembly is the
// caller's business — in this system, the single-writer reducer's.
package evidence
