package liveingest_test

// Shared test PKI (GLI-121). Every file lives in a t.TempDir() sub-directory
// whose name carries a unique marker (used by the GLI-101 secret-leak checks),
// every key and certificate is ECDSA P-256 and nothing is printed.
//
// The shared test helpers of this file are gliCertFiles, gliCertOpt, gliPKI,
// gliNewPKI, NodeCert, RoleCert and gliForeignPKI. Everything else in this file
// carries the gliw prefix and is private to this file group.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// gliwTrustDomain is the only SPIFFE trust domain GLI-021 accepts.
	gliwTrustDomain = "data-path-assurance.local"
	// gliwTenYears keeps default leaves valid for every fake Clock position
	// (GLI-121: the fake Clock may be advanced by NodeRetention and more).
	gliwTenYears = 10 * 365 * 24 * time.Hour
)

type gliCertFiles struct{ CertFile, KeyFile string }

// gliCertOpt edits the leaf template after the defaults are filled in, so an
// option may clear a default (URIs, ExtKeyUsage) as well as replace it.
type gliCertOpt func(*x509.Certificate)

type gliPKI struct {
	CAFile string
	Server gliCertFiles

	dir    string
	caPEM  []byte
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	n      atomic.Int64
}

func gliwKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gliw: generate key: %v", err)
	}
	return k
}

func gliwSerial(t *testing.T) *big.Int {
	t.Helper()
	limit := new(big.Int).Lsh(big.NewInt(1), 127)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		t.Fatalf("gliw: serial: %v", err)
	}
	return n.Add(n, big.NewInt(1))
}

func gliwRandHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("gliw: random: %v", err)
	}
	return hex.EncodeToString(b)
}

// gliwMarkerDir creates a private directory whose base name is a unique marker.
func gliwMarkerDir(t *testing.T, label string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gliw-"+label+"-"+gliwRandHex(t, 6)+"-mark")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("gliw: mkdir: %v", err)
	}
	return dir
}

func gliwWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatalf("gliw: write file: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("gliw: rename file: %v", err)
	}
}

func gliwPEMCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func gliwPEMKey(t *testing.T, k *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("gliw: marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// gliNewPKI creates a self-signed CA and a server leaf (DNS SAN localhost, ServerAuth).
func gliNewPKI(t *testing.T) *gliPKI {
	t.Helper()
	key := gliwKey(t)
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          gliwSerial(t),
		Subject:               pkix.Name{CommonName: "gli test ca"},
		NotBefore:             now.Add(-2 * time.Hour),
		NotAfter:              now.Add(gliwTenYears),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("gliw: create CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("gliw: parse CA: %v", err)
	}
	p := &gliPKI{dir: gliwMarkerDir(t, "pki"), caPEM: gliwPEMCert(der), caCert: cert, caKey: key}
	p.CAFile = filepath.Join(p.dir, "ca.pem")
	gliwWriteFile(t, p.CAFile, p.caPEM)
	p.Server = p.issue(t, p.serverTemplate(), nil, "server")
	return p
}

// gliForeignPKI is a second, unrelated CA.
func gliForeignPKI(t *testing.T) *gliPKI {
	t.Helper()
	return gliNewPKI(t)
}

func (p *gliPKI) leafTemplate(cn string, eku []x509.ExtKeyUsage) *x509.Certificate {
	now := time.Now()
	return &x509.Certificate{
		SerialNumber: big.NewInt(1), // replaced in issue()
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-2 * time.Hour),
		NotAfter:     now.Add(gliwTenYears),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
	}
}

func (p *gliPKI) serverTemplate() *x509.Certificate {
	tmpl := p.leafTemplate("gli ingest server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	tmpl.DNSNames = []string{"localhost"}
	return tmpl
}

// issue applies opts after the defaults, signs the leaf and writes both files.
func (p *gliPKI) issue(t *testing.T, tmpl *x509.Certificate, opts []gliCertOpt, name string) gliCertFiles {
	t.Helper()
	tmpl.SerialNumber = gliwSerial(t)
	for _, o := range opts {
		o(tmpl)
	}
	key := gliwKey(t)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatalf("gliw: create leaf %q: %v", name, err)
	}
	n := p.n.Add(1)
	files := gliCertFiles{
		CertFile: filepath.Join(p.dir, fmt.Sprintf("%s-%d.crt", name, n)),
		KeyFile:  filepath.Join(p.dir, fmt.Sprintf("%s-%d.key", name, n)),
	}
	gliwWriteFile(t, files.CertFile, gliwPEMCert(der))
	gliwWriteFile(t, files.KeyFile, gliwPEMKey(t, key))
	return files
}

// NodeCert issues a role=node client certificate (GLI-021 SAN, ClientAuth).
func (p *gliPKI) NodeCert(t *testing.T, clusterID, nodeUID string, opts ...gliCertOpt) gliCertFiles {
	t.Helper()
	return p.RoleCert(t, clusterID, "node", nodeUID, opts...)
}

// RoleCert issues a client certificate for an arbitrary role and subject.
func (p *gliPKI) RoleCert(t *testing.T, clusterID, role, subject string, opts ...gliCertOpt) gliCertFiles {
	t.Helper()
	tmpl := p.leafTemplate("gli "+role, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	tmpl.URIs = []*url.URL{gliwMustURL(gliwSPIFFE(clusterID, role, subject))}
	return p.issue(t, tmpl, opts, "client-"+role)
}

// RotateServer re-issues the server leaf (new key and serial, same CA) over the
// same file paths. It must not run while a connection attempt is in flight.
func (p *gliPKI) RotateServer(t *testing.T, opts ...gliCertOpt) {
	t.Helper()
	fresh := p.issue(t, p.serverTemplate(), opts, "server-rotated")
	for _, pair := range [][2]string{{fresh.KeyFile, p.Server.KeyFile}, {fresh.CertFile, p.Server.CertFile}} {
		data, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatalf("gliw: read rotated file: %v", err)
		}
		gliwWriteFile(t, pair[1], data)
	}
}

// CAPEM returns a copy of the CA certificate PEM (the in-memory original, not the file).
func (p *gliPKI) CAPEM() []byte { return append([]byte(nil), p.caPEM...) }

// Marker is the unique directory name every file of this PKI lives under.
func (p *gliPKI) Marker() string { return filepath.Base(p.dir) }

// Dir is the directory holding the PKI files.
func (p *gliPKI) Dir() string { return p.dir }

func gliwSPIFFE(clusterID, role, subject string) string {
	return fmt.Sprintf("spiffe://%s/cluster/%s/%s/%s", gliwTrustDomain, clusterID, role, subject)
}

func gliwMustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(fmt.Sprintf("gliw: bad test URL %q: %v", raw, err))
	}
	return u
}

// --- certificate options -------------------------------------------------

// gliwURIStrings replaces the URI SAN list.
func gliwURIStrings(raw ...string) gliCertOpt {
	return func(c *x509.Certificate) {
		c.URIs = nil
		for _, r := range raw {
			c.URIs = append(c.URIs, gliwMustURL(r))
		}
	}
}

// gliwURIs replaces the URI SAN list with prepared URLs.
func gliwURIs(uris ...*url.URL) gliCertOpt {
	return func(c *x509.Certificate) { c.URIs = uris }
}

// gliwExtraURIStrings appends URI SANs after the identity URI.
func gliwExtraURIStrings(raw ...string) gliCertOpt {
	return func(c *x509.Certificate) {
		for _, r := range raw {
			c.URIs = append(c.URIs, gliwMustURL(r))
		}
	}
}

// gliwNoSAN removes every SAN.
func gliwNoSAN() gliCertOpt {
	return func(c *x509.Certificate) {
		c.URIs, c.DNSNames, c.IPAddresses, c.EmailAddresses = nil, nil, nil, nil
	}
}

// gliwOtherSANs adds non-URI SANs.
func gliwOtherSANs(dns []string, ips []string) gliCertOpt {
	return func(c *x509.Certificate) {
		c.DNSNames = append(c.DNSNames, dns...)
		for _, s := range ips {
			c.IPAddresses = append(c.IPAddresses, net.ParseIP(s))
		}
	}
}

// gliwEKU replaces ExtKeyUsage; with no argument the extension is omitted.
func gliwEKU(eku ...x509.ExtKeyUsage) gliCertOpt {
	return func(c *x509.Certificate) { c.ExtKeyUsage = eku }
}

// gliwValidity sets NotBefore and NotAfter.
func gliwValidity(notBefore, notAfter time.Time) gliCertOpt {
	return func(c *x509.Certificate) { c.NotBefore, c.NotAfter = notBefore, notAfter }
}

// gliwNotAfter sets NotAfter only. X.509 stores whole seconds, so callers that
// compare against it must read the parsed value (gliwReadNotAfter).
func gliwNotAfter(tm time.Time) gliCertOpt {
	return func(c *x509.Certificate) { c.NotAfter = tm }
}

// gliwExpired makes a leaf whose validity ended an hour ago.
func gliwExpired() gliCertOpt {
	now := time.Now()
	return gliwValidity(now.Add(-3*time.Hour), now.Add(-time.Hour))
}

// --- loading --------------------------------------------------------------

// gliwLoadCert loads a certificate and key WITHOUT parsing the leaf, so that
// certificates the server must reject at parse time (invalid URI host) can still
// be presented by the test client.
func gliwLoadCert(t *testing.T, f gliCertFiles) tls.Certificate {
	t.Helper()
	certPEM, err := os.ReadFile(f.CertFile)
	if err != nil {
		t.Fatalf("gliw: read certificate: %v", err)
	}
	var chain [][]byte
	for {
		var blk *pem.Block
		blk, certPEM = pem.Decode(certPEM)
		if blk == nil {
			break
		}
		if blk.Type == "CERTIFICATE" {
			chain = append(chain, blk.Bytes)
		}
	}
	if len(chain) == 0 {
		t.Fatalf("gliw: no certificate in %s", filepath.Base(f.CertFile))
	}
	keyPEM, err := os.ReadFile(f.KeyFile)
	if err != nil {
		t.Fatalf("gliw: read key: %v", err)
	}
	blk, _ := pem.Decode(keyPEM)
	if blk == nil {
		t.Fatalf("gliw: no PEM block in key file")
	}
	key, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		t.Fatalf("gliw: parse key: %v", err)
	}
	return tls.Certificate{Certificate: chain, PrivateKey: key}
}

// gliwReadNotAfter returns the NotAfter actually encoded in the certificate file.
func gliwReadNotAfter(t *testing.T, f gliCertFiles) time.Time {
	t.Helper()
	cert := gliwLoadCert(t, f)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("gliw: parse leaf: %v", err)
	}
	return leaf.NotAfter
}

// gliwReadSerial returns the serial number of the certificate file.
func gliwReadSerial(t *testing.T, f gliCertFiles) *big.Int {
	t.Helper()
	cert := gliwLoadCert(t, f)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("gliw: parse leaf: %v", err)
	}
	return leaf.SerialNumber
}
