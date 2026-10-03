// Package liveingest is the domain core of live snapshot ingest: it decides
// what an authenticated agent may say about its node and turns accepted
// snapshot frames into the assessment bundles the live assessor evaluates.
//
// This file set is the shared layer. It holds the ports the ingest server
// depends on (Clock, NodeDirectory, SessionStore, LeaderGate and their error
// sentinels), the wire-neutral frame and payload types that the gRPC adapter
// converts to and from protobuf messages, the payload validation rules
// (CheckFrame for one frame in isolation, CheckRelative for a frame in
// relation to the previous accepted one), the canonical payload digest and the
// bundle revision, the deterministic evidence IDs, and the trust stamping rule
// for sources that a collector trust profile does not list.
//
// The package is domain core: it imports only the standard library (no I/O,
// network, logging or TLS packages) and the ratified domain packages, never
// protobuf or gRPC, and it neither logs nor reads the clock. The same
// functions run in the controller on every received frame and in the agent
// before it sends one, so both sides compute one digest and one set of IDs.
package liveingest
