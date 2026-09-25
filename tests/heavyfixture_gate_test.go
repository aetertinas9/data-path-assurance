package tests_test

// Shared gate for tests with large fixtures.

import (
	"sync"
	"testing"
)

// heavyFixtureGate holds a single token. Tests with large fixtures take it
// for their heavy section (fixture generation, binary runs, independent
// recomputation) so that they do not compete with each other for memory and
// CPU; they stay parallel and still overlap with lighter tests.
var heavyFixtureGate = make(chan struct{}, 1)

// holdHeavyFixture blocks until the token is free and returns a function
// that gives it back; the token is also given back on test cleanup. Call it
// after t.Parallel() and at most once per test (the gate is not reentrant).
func holdHeavyFixture(t *testing.T) (release func()) {
	t.Helper()
	heavyFixtureGate <- struct{}{}
	var once sync.Once
	release = func() { once.Do(func() { <-heavyFixtureGate }) }
	t.Cleanup(release)
	return release
}
