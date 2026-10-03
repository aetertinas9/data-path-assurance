// Package framecore holds the frame evaluation calculations that the offline
// explain use case (package app) and the live ingest source
// (package app/liveingest) share: the evidence window configuration, the
// topology snapshot of one frame, the TopologyDigest and BaselineDigest of one
// frame, and the provenance index by edge identity.
//
// The calculations were moved here from package app without changing them, so
// that the offline and the live evaluation cannot drift apart. The package is
// domain core: it imports only the standard library and the ratified domain
// packages, knows nothing about files, wire formats or logging, and does not
// import package app.
package framecore
