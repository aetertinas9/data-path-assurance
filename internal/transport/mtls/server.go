package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
)

// alpnH2 is the only application protocol of the transport.
const alpnH2 = "h2"

// ErrServiceDNS reports a server certificate that does not cover the
// configured service DNS name.
var ErrServiceDNS = errors.New("mtls: the server certificate does not cover the service DNS name")

// ServerConfig returns the TLS configuration of the ingest listener: TLS 1.3
// or later only, a verified client certificate required, the clients trusted
// only through the CA file, ALPN h2, no session tickets.
//
// The files are read once now, and an unreadable or unusable file, a CA file
// without certificates or a certificate that does not match its key is an
// error. When serviceDNS is not empty it must be a valid DNS name (see
// ValidateServerName) that the server certificate covers, else ErrServerName
// or ErrServiceDNS is returned.
//
// Afterwards the files are read again for every new connection, so rotated
// files take effect on the next connection: the returned configuration carries
// a GetConfigForClient that builds the complete per-connection configuration
// (the version floor, the client authentication mode, the CA pool, the ALPN
// list and the certificate, with session tickets off so that a new connection
// is always a full handshake and a resumed one cannot bypass a rotated
// certificate or CA). When that reload fails only that handshake fails, the
// listener keeps working, and onReloadError, when not nil, is called with the
// error; the error is one of this package's sentinels and holds no path.
func ServerConfig(f Files, serviceDNS string, onReloadError func(error)) (*tls.Config, error) {
	pool, pair, err := load(f)
	if err != nil {
		return nil, err
	}
	if serviceDNS != "" {
		if err := ValidateServerName(serviceDNS); err != nil {
			return nil, err
		}
		if err := pair.Leaf.VerifyHostname(serviceDNS); err != nil {
			return nil, ErrServiceDNS
		}
	}
	cfg := serverTLS(pool, pair)
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		pool, pair, err := load(f)
		if err != nil {
			if onReloadError != nil {
				onReloadError(err)
			}
			return nil, err
		}
		return serverTLS(pool, pair), nil
	}
	return cfg, nil
}

// serverTLS is the complete server configuration for one set of material.
func serverTLS(pool *x509.CertPool, pair tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              pool,
		Certificates:           []tls.Certificate{pair},
		NextProtos:             []string{alpnH2},
		SessionTicketsDisabled: true,
	}
}
