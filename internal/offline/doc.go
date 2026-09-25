// Package offline reads the inputs of an offline explanation: the snapshot
// artifact written by path-agent in fixture mode (dpa.offline-snapshot/v1) and
// the offline fleet file (dpa.offline-fleet/v1).
//
// Both files are opened only as regular files, read under a size bound and
// decoded strictly. For the artifact, every deterministic ID, payload digest
// and bundle revision is recomputed independently from the artifact contract
// and every frame is converted to ratified domain values, which must validate.
// The result is an app.Replay; nothing here evaluates it.
package offline
