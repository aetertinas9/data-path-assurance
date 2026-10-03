package mtls

import "crypto/tls"

// ClientConfig reads the CA, certificate and key files and returns the TLS
// configuration of one client connection: TLS 1.3 or later only, the server
// certificate verified against the CA file and against serverName (its DNS
// subject alternative names; the common name is ignored), the client
// certificate presented, ALPN h2. Certificate verification cannot be turned
// off through this package.
//
// serverName must be a DNS name (ValidateServerName), else ErrServerName is
// returned. File failures return ErrFile, ErrNoCA or ErrKeyPair. The leaf of
// Certificates[0] is parsed, so the caller can read the identity of its own
// certificate with ParseIdentity. Call ClientConfig again for every
// connection attempt to pick up rotated files.
func ClientConfig(f Files, serverName string) (*tls.Config, error) {
	if err := ValidateServerName(serverName); err != nil {
		return nil, err
	}
	pool, pair, err := load(f)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{pair},
		ServerName:   serverName,
		NextProtos:   []string{alpnH2},
	}, nil
}
