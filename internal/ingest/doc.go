// Package ingest is the gRPC server adapter of live snapshot ingest: it
// terminates mutual TLS, reads the identity of the client certificate, applies
// the message size, rate and time limits, converts between the generated wire
// messages (package ingestpb) and the wire-neutral types of
// internal/app/liveingest, and maps the outcomes of the domain core to
// acknowledgements and gRPC status codes.
//
// The adapter owns no Kubernetes knowledge. The Kubernetes Node directory, the
// session store and the leadership gate are ports (internal/app/liveingest)
// that the composition root wires in.
package ingest
