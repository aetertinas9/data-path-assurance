package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
)

// Files names the PEM files of one TLS setup: the CA certificates that sign
// the peer, and the certificate chain and private key of this side.
type Files struct{ CAFile, CertFile, KeyFile string }

// maxFileBytes bounds how much of one file is read.
const maxFileBytes = 1 << 20

// The classes of file failure. The errors never carry a path or a content.
var (
	// ErrFile reports a file that is absent, unreadable, not a regular file
	// (a FIFO, socket, device or directory is refused without opening it) or
	// larger than 1 MiB.
	ErrFile = errors.New("mtls: a TLS file cannot be read")
	// ErrNoCA reports a CA file that holds no certificate, or something other
	// than certificates. An empty pool would fall back to the system roots, so
	// it is never built.
	ErrNoCA = errors.New("mtls: the CA file holds no usable certificate")
	// ErrKeyPair reports a certificate file and key file that do not parse or
	// do not belong together.
	ErrKeyPair = errors.New("mtls: the certificate and the key cannot be loaded as a pair")
)

// readFile reads the regular file at path. It looks at the file type first, so
// a FIFO or a device never blocks the caller.
func readFile(path string) ([]byte, error) {
	if path == "" {
		return nil, ErrFile
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxFileBytes {
		return nil, ErrFile
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrFile
	}
	defer func() { _ = f.Close() }() // read-only file: a close error loses nothing
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil, ErrFile
	}
	return data, nil
}

// loadCAPool reads the CA file into a pool: it must hold at least one
// certificate and nothing but certificates.
func loadCAPool(path string) (*x509.CertPool, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	certs := 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, ErrNoCA
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, ErrNoCA
		}
		pool.AddCert(cert)
		certs++
	}
	if certs == 0 {
		return nil, ErrNoCA
	}
	return pool, nil
}

// loadKeyPair reads the certificate chain and the private key. The leaf of the
// result is parsed.
func loadKeyPair(certFile, keyFile string) (tls.Certificate, error) {
	certPEM, err := readFile(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := readFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, ErrKeyPair
	}
	if pair.Leaf == nil {
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return tls.Certificate{}, ErrKeyPair
		}
		pair.Leaf = leaf
	}
	return pair, nil
}

// load reads the three files of f.
func load(f Files) (*x509.CertPool, tls.Certificate, error) {
	pool, err := loadCAPool(f.CAFile)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	pair, err := loadKeyPair(f.CertFile, f.KeyFile)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	return pool, pair, nil
}
