// Package mtls holds the mutual TLS building blocks of the live ingest
// transport: the TLS 1.3 server configuration that demands and verifies a
// client certificate, the client configuration, and the reading of the
// identity a client certificate carries.
//
// The package depends on the standard library only. It reads certificate, key
// and CA files each time it builds a configuration (the server does so for
// every new connection), so a rotated file takes effect without a restart.
// Its errors are fixed phrases that never contain a path, a file content or a
// certificate subject, so they are safe to log and to show.
//
// Identity grammar. A client certificate names its holder with exactly one
// URI subject alternative name of the form
//
//	spiffe://data-path-assurance.local/cluster/<clusterID>/<role>/<subject>
//
// where role is node, fence or viewer. For role node the subject is the UID of
// the Kubernetes Node the certificate belongs to. Services built on this
// package decide which roles they accept.
package mtls
