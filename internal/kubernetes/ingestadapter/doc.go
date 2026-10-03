// Package ingestadapter is the Kubernetes adapter of live snapshot ingest. It
// implements the three ports the ingest server depends on
// (internal/app/liveingest):
//
//   - NodeDirectory reads a Node by name from the API server.
//   - SessionStore allocates the collector session of a node by a
//     resourceVersion-guarded update of the NodePathState status. It is the
//     only writer of status.collectorSession and keeps every field the status
//     controller owns exactly as it read it.
//   - LeaderGate follows the Lease the status controller elects its leader
//     with, so that only the leading process accepts agents.
//
// The adapter never creates a NodePathState, never logs and never puts a
// response body into an error: its errors carry a fixed text and expose their
// cause through Unwrap only. Every request goes through the transport
// configuration of the REST config it was built from. The requests of the
// hello path (Node and NodePathState) and the Lease polls use two clients with
// two rate limiters, so a burst of hellos cannot starve the leadership gate.
//
// The API verbs it uses are a subset of those of the status controller:
// nodepathstates get, nodepathstates/status update, nodes get and leases get.
package ingestadapter
