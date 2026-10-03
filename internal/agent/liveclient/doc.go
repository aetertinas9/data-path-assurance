// Package liveclient is the live snapshot stream client of path-agent: it
// collects the host with the collector of package agent every cycle, builds a
// frame that the controller's ingest accepts, and sends it over a mutually
// authenticated gRPC stream with stop-and-wait acknowledgements, reconnecting
// with a jittered backoff whenever the stream ends for any reason.
//
// The package reads the clock only through Options.Clock, keeps no
// package-level mutable state, and never writes a path, a certificate or a
// payload-derived string to a log or an error: errors carry fixed phrases and
// logs carry event names, identifiers of the node, counts and class words.
//
// The payload digest, the evidence IDs and the frame checks are the functions
// of package liveingest that the controller runs on every received frame, so
// both sides compute one digest. The client never sets the allocation batch.
package liveclient
