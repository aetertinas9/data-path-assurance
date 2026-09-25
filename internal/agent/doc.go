// Package agent is the path-agent host adapter: a pure Go, read-only sysfs
// collector, the NVIDIA inventory parser and its bounded runner, and the
// offline snapshot artifact built from a fixture tree.
//
// Every host path is resolved through an injected *os.Root. The package holds
// no host path of its own, so a fixture tree and (later) a live host root go
// through the same collector rules.
//
// In offline mode nothing here reads the wall clock, the environment, the
// host name or the locale into an output value, and no process is started:
// every timestamp comes from the fixture manifest and NVIDIA inventory comes
// from a canned file.
package agent
