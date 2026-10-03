package liveingest_test

// GLI-020..025 and GLI-124 (a): mTLS, client SAN grammar and roles, scope
// binding at the TLS layer, certificate lifetime, rotation and the absence of
// unauthenticated surface. Negative cases are written as often as positive ones.

import (
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliwTLSInfo records the client-side view of every handshake (also resumed ones).
type gliwTLSInfo struct {
	mu     sync.Mutex
	states []tls.ConnectionState
}

func (i *gliwTLSInfo) verify(cs tls.ConnectionState) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.states = append(i.states, cs)
	return nil
}

func (i *gliwTLSInfo) all() []tls.ConnectionState {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]tls.ConnectionState(nil), i.states...)
}

// gliwExpectRejected requires that a connection made with cfg (nil: cleartext
// h2c) never gets a ServerHello and is rejected promptly. A rejected TLS 1.3
// client certificate is only observable at the first read, so the stream may
// fail to open or fail at Send/Recv; the gRPC code is not specified (GLI-020).
func gliwExpectRejected(t *testing.T, s *gliServer, cfg *tls.Config, n gliwNode) {
	t.Helper()
	start := time.Now()
	st, closeFn, err := s.TryStream(t, cfg)
	if err != nil {
		return
	}
	defer closeFn()
	h, err := gliwHello(st, s.HelloMsg(n))
	if err == nil {
		t.Fatalf("the server answered a ServerHello (session %d) on a connection that must be rejected", h.GetSession())
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("the rejection was only observed after %v (%v)", d, err)
	}
}

// gliwNodeWithCert creates a node whose certificate is built from opts(uid).
func gliwNodeWithCert(t *testing.T, s *gliServer, label string, opts func(uid string) []gliCertOpt) gliwNode {
	t.Helper()
	n := s.AddNode(t, label)
	n.Cert = s.PKI.NodeCert(t, s.Config.ClusterID, n.UID, opts(n.UID)...)
	return n
}

func gliwIdentityURL(clusterID, role, subject string) string {
	return gliwSPIFFE(clusterID, role, subject)
}

func TestGLI020_TLSParameters(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "tls")
	cache := tls.NewLRUClientSessionCache(8)
	info := &gliwTLSInfo{}
	for i := 0; i < 2; i++ {
		cfg := s.ClientTLS(t, &n.Cert)
		cfg.ClientSessionCache = cache
		cfg.VerifyConnection = info.verify
		st, closeFn, err := s.TryStream(t, cfg)
		if err != nil {
			t.Fatalf("connection %d: open stream: %v", i, err)
		}
		h, err := gliwHello(st, s.HelloMsg(n))
		if err != nil {
			t.Fatalf("connection %d: hello: %v", i, err)
		}
		if h.GetSession() <= 0 {
			t.Fatalf("connection %d: session %d", i, h.GetSession())
		}
		closeFn()
	}
	states := info.all()
	if len(states) != 2 {
		t.Fatalf("recorded %d handshakes, want 2", len(states))
	}
	for i, cs := range states {
		if cs.Version != tls.VersionTLS13 {
			t.Errorf("connection %d negotiated TLS 0x%04x, want TLS 1.3", i, cs.Version)
		}
		if cs.NegotiatedProtocol != "h2" {
			t.Errorf("connection %d negotiated ALPN %q, want h2", i, cs.NegotiatedProtocol)
		}
		if cs.DidResume {
			t.Errorf("connection %d resumed a session: session tickets must be off (GLI-020)", i)
		}
	}
}

func TestGLI020_RejectedConnections(t *testing.T) {
	s := gliStartServer(t)
	s.StreamTimeout = 15 * time.Second
	foreign := gliForeignPKI(t)
	n := s.AddNode(t, "rej")

	uriOf := func(host string) []gliCertOpt {
		return []gliCertOpt{gliwURIs(&url.URL{Scheme: "spiffe", Host: host, Path: "/cluster/" + gliwCluster + "/node/" + n.UID})}
	}
	cases := []struct {
		name string
		cfg  func(t *testing.T) *tls.Config // nil config means cleartext h2c
	}{
		{"TLS 1.2 only client", func(t *testing.T) *tls.Config {
			c := s.ClientTLS(t, &n.Cert)
			c.MinVersion, c.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
			return c
		}},
		{"cleartext h2c client", func(t *testing.T) *tls.Config { return nil }},
		{"no client certificate", func(t *testing.T) *tls.Config { return s.ClientTLS(t, nil) }},
		{"certificate signed by another CA", func(t *testing.T) *tls.Config {
			f := foreign.NodeCert(t, gliwCluster, n.UID)
			return s.ClientTLS(t, &f)
		}},
		{"expired certificate", func(t *testing.T) *tls.Config {
			f := s.PKI.NodeCert(t, gliwCluster, n.UID, gliwExpired())
			return s.ClientTLS(t, &f)
		}},
		{"ServerAuth-only EKU", func(t *testing.T) *tls.Config {
			f := s.PKI.NodeCert(t, gliwCluster, n.UID, gliwEKU(x509.ExtKeyUsageServerAuth))
			return s.ClientTLS(t, &f)
		}},
		{"URI host with trailing dot (unparseable)", func(t *testing.T) *tls.Config {
			f := s.PKI.NodeCert(t, gliwCluster, n.UID, uriOf(gliwTrustDomain+".")...)
			return s.ClientTLS(t, &f)
		}},
		{"URI host with an empty label (unparseable)", func(t *testing.T) *tls.Config {
			f := s.PKI.NodeCert(t, gliwCluster, n.UID, uriOf("data-path-assurance..local")...)
			return s.ClientTLS(t, &f)
		}},
		{"URI host with a non-ASCII character (unparseable)", func(t *testing.T) *tls.Config {
			f := s.PKI.NodeCert(t, gliwCluster, n.UID, uriOf("data-path-assurance.é.local")...)
			return s.ClientTLS(t, &f)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gliwExpectRejected(t, s, tc.cfg(t), n)
		})
	}
	if calls := s.Sessions.Calls(); len(calls) != 0 {
		t.Errorf("a rejected connection reached the session store: %+v", calls)
	}
	if c := s.Nodes.Calls(); c != 0 {
		t.Errorf("a rejected connection reached the node directory %d times", c)
	}
	// The same node still gets in with a correct certificate: nothing was damaged.
	_, _, h := s.Connect(t, n)
	if h.GetSession() != 1 {
		t.Errorf("first accepted hello after the rejections got session %d, want 1", h.GetSession())
	}
}

// TestGLI021_SANGrammar drives every grammar variant of GLI-021 through a real
// handshake. Variants that survive the handshake but are no identity end in
// Unauthenticated before the hello is read (the node directory is never asked);
// valid variants get a ServerHello.
func TestGLI021_SANGrammar(t *testing.T) {
	s := gliStartServer(t)
	const marker = "GLIWMARK"
	id := func(uid string) string { return gliwIdentityURL(gliwCluster, "node", uid) }
	long := strings.Repeat("a", 129)
	cases := []struct {
		name string
		opts func(uid string) []gliCertOpt
		want codes.Code // codes.OK: ServerHello
	}{
		// --- no identity -> Unauthenticated
		{"two identity URIs", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings(id(u), id(u+"-second"))}
		}, codes.Unauthenticated},
		{"foreign trust domain only (marker in SAN)", func(u string) []gliCertOpt {
			return []gliCertOpt{
				gliwURIStrings("spiffe://other.local/cluster/" + marker + "-san/node/" + u),
				gliwOtherSANs([]string{"gliwmark-dns.example.com"}, nil),
			}
		}, codes.Unauthenticated},
		{"host with a port", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local:8443/cluster/lab-a/node/" + u)}
		}, codes.Unauthenticated},
		{"host in upper case (host is case sensitive)", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://Data-Path-Assurance.local/cluster/lab-a/node/" + u)}
		}, codes.Unauthenticated},
		{"scheme is not spiffe", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("https://data-path-assurance.local/cluster/lab-a/node/" + u)}
		}, codes.Unauthenticated},
		{"trailing slash", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings(id(u) + "/")}
		}, codes.Unauthenticated},
		{"three path items", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/lab-a/node")}
		}, codes.Unauthenticated},
		{"five path items", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings(id(u) + "/extra")}
		}, codes.Unauthenticated},
		{"empty subject", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/lab-a/node/")}
		}, codes.Unauthenticated},
		{"empty cluster", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster//node/" + u)}
		}, codes.Unauthenticated},
		{"first item is not cluster", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/clusters/lab-a/node/" + u)}
		}, codes.Unauthenticated},
		{"unknown role", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/lab-a/admin/" + u)}
		}, codes.Unauthenticated},
		{"cluster has a character outside the set", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/lab+a/node/" + u)}
		}, codes.Unauthenticated},
		{"subject has a character outside the set", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/lab-a/node/a+b")}
		}, codes.Unauthenticated},
		{"subject is non-ASCII", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIs(&url.URL{Scheme: "spiffe", Host: gliwTrustDomain, Path: "/cluster/lab-a/node/é"})}
		}, codes.Unauthenticated},
		{"cluster of 129 bytes", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/" + long + "/node/" + u)}
		}, codes.Unauthenticated},
		{"subject of 129 bytes", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://data-path-assurance.local/cluster/lab-a/node/" + long)}
		}, codes.Unauthenticated},
		{"percent-encoded subject (RawPath set)", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIs(&url.URL{
				Scheme: "spiffe", Host: gliwTrustDomain,
				Path: "/cluster/lab-a/node/A", RawPath: "/cluster/lab-a/node/%41",
			})}
		}, codes.Unauthenticated},
		{"query", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings(id(u) + "?x=1")}
		}, codes.Unauthenticated},
		{"force query (trailing question mark)", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings(id(u) + "?")}
		}, codes.Unauthenticated},
		{"userinfo", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings("spiffe://user@data-path-assurance.local/cluster/lab-a/node/" + u)}
		}, codes.Unauthenticated},
		{"fragment", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIStrings(id(u) + "#frag")}
		}, codes.Unauthenticated},
		{"no SAN at all", func(u string) []gliCertOpt { return []gliCertOpt{gliwNoSAN()} }, codes.Unauthenticated},
		{"DNS SAN only", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwNoSAN(), gliwOtherSANs([]string{"node.example.com"}, nil)}
		}, codes.Unauthenticated},
		{"ExtKeyUsage extension omitted", func(u string) []gliCertOpt { return []gliCertOpt{gliwEKU()} }, codes.Unauthenticated},
		{"ExtKeyUsage Any only", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwEKU(x509.ExtKeyUsageAny)}
		}, codes.Unauthenticated},
		// --- valid identities -> ServerHello
		{"scheme in upper case (Go lower-cases it)", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwURIs(&url.URL{Scheme: "SPIFFE", Host: gliwTrustDomain, Path: "/cluster/lab-a/node/" + u})}
		}, codes.OK},
		{"foreign trust-domain URI beside the identity is ignored", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwExtraURIStrings("spiffe://other.local/cluster/lab-b/node/zzz")}
		}, codes.OK},
		{"other URI schemes, DNS and IP SANs beside the identity are ignored", func(u string) []gliCertOpt {
			return []gliCertOpt{
				gliwExtraURIStrings("https://example.com/x", "spiffe://data-path-assurance.local:9/cluster/lab-b/node/zzz"),
				gliwOtherSANs([]string{"example.com"}, []string{"192.0.2.1"}),
			}
		}, codes.OK},
		{"ExtKeyUsage with ClientAuth among others", func(u string) []gliCertOpt {
			return []gliCertOpt{gliwEKU(x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)}
		}, codes.OK},
	}
	var forbidden = []string{marker, strings.ToLower(marker), s.PKI.Marker()}
	accepted := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := gliwNodeWithCert(t, s, "san", tc.opts)
			callsBefore, nodesBefore := len(s.Sessions.Calls()), s.Nodes.Calls()
			st, closeFn := s.Stream(t, n.Cert)
			defer closeFn()
			h, err := gliwHello(st, s.HelloMsg(n))
			if tc.want == codes.OK {
				if err != nil {
					t.Fatalf("valid identity rejected: %v", err)
				}
				if h.GetSession() <= 0 {
					t.Fatalf("session %d, want > 0", h.GetSession())
				}
				accepted++
				return
			}
			gliwWantCode(t, "hello", err, tc.want, forbidden...)
			if got := len(s.Sessions.Calls()); got != callsBefore {
				t.Errorf("a stream without identity reached the session store (%d -> %d calls)", callsBefore, got)
			}
			if got := s.Nodes.Calls(); got != nodesBefore {
				t.Errorf("a stream without identity reached the node directory (%d -> %d calls)", nodesBefore, got)
			}
		})
	}
	logs := s.LogText()
	for _, f := range forbidden {
		if strings.Contains(logs, f) {
			t.Errorf("the server log contains %q (certificate SAN text or file path)", f)
		}
	}
	if accepted == 0 {
		t.Fatal("no valid variant was exercised")
	}
}

// TestGLI021_SubjectCharacterSet covers the accepted boundary values of the
// grammar: dots, dashes and underscores, the dot segments (not normalised), and
// the 128-byte limits of cluster and subject.
func TestGLI021_SubjectCharacterSet(t *testing.T) {
	longCluster := strings.Repeat("c", 128)
	s := gliStartServer(t, func(c *ingest.Config) { c.ClusterID = longCluster })
	for _, uid := range []string{".", "..", "a_b.c-d", "0", strings.Repeat("u", 128), "UPPER.lower_09-x"} {
		uid := uid
		name := uid
		if len(name) > 12 {
			name = name[:12] + "..."
		}
		t.Run("uid="+name, func(t *testing.T) {
			node := gliwNode{Name: "gliw-uid-" + gliwRandHex(t, 4), UID: uid, Boot: "boot-" + gliwRandHex(t, 4)}
			node.Cert = s.PKI.NodeCert(t, longCluster, uid)
			s.Nodes.Put(node.Name, node.UID)
			st, closeFn := s.Stream(t, node.Cert)
			defer closeFn()
			h, err := gliwHello(st, s.HelloMsg(node))
			if err != nil {
				t.Fatalf("hello for subject %q rejected: %v", name, err)
			}
			if h.GetSession() <= 0 {
				t.Fatalf("session %d", h.GetSession())
			}
		})
	}
}

func TestGLI021_RoleNotNodeIsPermissionDenied(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "role")
	for _, role := range []string{"fence", "viewer"} {
		role := role
		t.Run(role, func(t *testing.T) {
			cert := s.PKI.RoleCert(t, gliwCluster, role, role+"-subject-1")
			calls := s.Nodes.Calls()
			st, closeFn := s.Stream(t, cert)
			defer closeFn()
			_, err := gliwHello(st, s.HelloMsg(n))
			gliwWantCode(t, "hello with role "+role, err, codes.PermissionDenied)
			if s.Nodes.Calls() != calls {
				t.Errorf("the hello of a %s certificate was processed (node directory called)", role)
			}
		})
	}
	if c := s.Sessions.Calls(); len(c) != 0 {
		t.Errorf("session store called for non-node roles: %+v", c)
	}
}

// TestGLI030_AuthenticationBeforeLeaderAndCapacity: step (1) comes before (2) —
// a failed authentication must not reveal leadership or capacity state.
func TestGLI030_AuthenticationBeforeLeaderAndCapacity(t *testing.T) {
	s := gliStartServer(t, gliwLimits(ingest.Limits{MaxStreams: 1}))
	n := s.AddNode(t, "authfirst")
	bad := s.PKI.NodeCert(t, gliwCluster, n.UID, gliwNoSAN())
	fence := s.PKI.RoleCert(t, gliwCluster, "fence", "fence-1")

	// The only stream slot is taken by an established stream; a further valid
	// stream is turned away, which proves the capacity limit is reached.
	_, _, _ = s.Connect(t, n)
	st, closeFn := s.Stream(t, n.Cert)
	_, err := gliwHello(st, s.HelloMsg(n))
	gliwWantCode(t, "second stream with MaxStreams=1", err, codes.ResourceExhausted)
	closeFn()
	s.Leader.Set(false)
	calls := len(s.Sessions.Calls())
	nodes := s.Nodes.Calls()

	st, _ = s.Stream(t, bad)
	_, err = gliwHello(st, s.HelloMsg(n))
	gliwWantCode(t, "no identity, non-leader, full", err, codes.Unauthenticated)

	st, _ = s.Stream(t, fence)
	_, err = gliwHello(st, s.HelloMsg(n))
	gliwWantCode(t, "fence role, non-leader, full", err, codes.PermissionDenied)

	if got := len(s.Sessions.Calls()); got != calls {
		t.Errorf("session store called by rejected streams (%d -> %d)", calls, got)
	}
	if got := s.Nodes.Calls(); got != nodes {
		t.Errorf("node directory called by rejected streams (%d -> %d)", nodes, got)
	}
}

func TestGLI020_ServerCertificateRotation(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "rot")
	connect := func() *big.Int {
		t.Helper()
		info := &gliwTLSInfo{}
		cfg := s.ClientTLS(t, &n.Cert)
		cfg.VerifyConnection = info.verify
		st, closeFn, err := s.TryStream(t, cfg)
		if err != nil {
			t.Fatalf("open stream: %v", err)
		}
		defer closeFn()
		if _, err := gliwHello(st, s.HelloMsg(n)); err != nil {
			t.Fatalf("hello: %v", err)
		}
		states := info.all()
		if len(states) != 1 || len(states[0].PeerCertificates) == 0 {
			t.Fatalf("recorded %d handshakes", len(states))
		}
		return states[0].PeerCertificates[0].SerialNumber
	}
	// Space the hellos by the hello rate so the node's hello bucket never matters.
	first := connect()
	if want := gliwReadSerial(t, s.PKI.Server); first.Cmp(want) != 0 {
		t.Fatalf("first connection saw serial %v, want %v", first, want)
	}
	s.Clock.Advance(time.Minute)
	s.PKI.RotateServer(t)
	rotated := gliwReadSerial(t, s.PKI.Server)
	if rotated.Cmp(first) == 0 {
		t.Fatal("rotation did not change the server certificate")
	}
	if second := connect(); second.Cmp(rotated) != 0 {
		t.Fatalf("a new connection after the file replacement saw serial %v, want the rotated %v", second, rotated)
	}
}

func TestGLI020_CAFileRotation(t *testing.T) {
	s := gliStartServer(t)
	foreign := gliForeignPKI(t)
	n := s.AddNode(t, "carot")
	foreignCert := foreign.NodeCert(t, gliwCluster, n.UID)
	s.StreamTimeout = 15 * time.Second

	gliwWriteFile(t, s.PKI.CAFile, foreign.CAPEM())
	gliwExpectRejected(t, s, s.ClientTLS(t, &n.Cert), n)
	{
		st, closeFn := s.Stream(t, foreignCert)
		if _, err := gliwHello(st, s.HelloMsg(n)); err != nil {
			t.Fatalf("a certificate of the newly configured CA was rejected: %v", err)
		}
		closeFn()
	}
	s.Clock.Advance(time.Minute)
	gliwWriteFile(t, s.PKI.CAFile, s.PKI.CAPEM())
	gliwExpectRejected(t, s, s.ClientTLS(t, &foreignCert), n)
	st, closeFn := s.Stream(t, n.Cert)
	defer closeFn()
	if _, err := gliwHello(st, s.HelloMsg(n)); err != nil {
		t.Fatalf("the original CA was not honoured again after the file was restored: %v", err)
	}
}

// TestGLI020_BrokenTLSFilesFailOnlyTheHandshake: a corrupt or missing file fails
// the handshakes that read it, the server keeps listening and recovers when the
// file is restored; the log gets the failure class only (GLI-020, GLI-101).
func TestGLI020_BrokenTLSFilesFailOnlyTheHandshake(t *testing.T) {
	s := gliStartServer(t)
	s.StreamTimeout = 15 * time.Second
	n := s.AddNode(t, "broken")
	const garbage = "GLIW-GARBAGE-SECRET not a pem file"

	mustRead := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", "tls file", err)
		}
		return b
	}
	files := []struct {
		name string
		path string
	}{
		{"server key", s.PKI.Server.KeyFile},
		{"server certificate", s.PKI.Server.CertFile},
		{"CA file", s.PKI.CAFile},
	}
	for _, f := range files {
		f := f
		t.Run(f.name, func(t *testing.T) {
			orig := mustRead(f.path)
			for _, mode := range []string{"garbage", "removed", "empty"} {
				switch mode {
				case "garbage":
					gliwWriteFile(t, f.path, []byte(garbage))
				case "removed":
					if err := os.Remove(f.path); err != nil {
						t.Fatalf("remove: %v", err)
					}
				case "empty":
					gliwWriteFile(t, f.path, nil)
				}
				gliwExpectRejected(t, s, s.ClientTLS(t, &n.Cert), n)
				// The listener still accepts TCP connections: the server did not stop.
				c, err := net.DialTimeout("tcp", s.Addr, 3*time.Second)
				if err != nil {
					t.Fatalf("%s %s: the server stopped listening: %v", f.name, mode, err)
				}
				_ = c.Close()
			}
			gliwWriteFile(t, f.path, orig)
			s.Clock.Advance(time.Minute)
			st, closeFn := s.Stream(t, n.Cert)
			defer closeFn()
			if _, err := gliwHello(st, s.HelloMsg(n)); err != nil {
				t.Fatalf("after restoring the %s the hello failed: %v", f.name, err)
			}
		})
	}
	logs := s.LogText()
	if strings.TrimSpace(logs) == "" {
		t.Error("handshake failures left no log record (GLI-020: the cause class is recorded)")
	}
	for _, f := range []string{garbage, "GLIW-GARBAGE-SECRET", s.PKI.Marker(), "BEGIN CERTIFICATE", "PRIVATE KEY"} {
		if strings.Contains(logs, f) {
			t.Errorf("the server log contains %q", f)
		}
	}
}

// TestGLI020_ConnectionWithoutHandshakeIsClosed: a TCP client that connects and
// sends nothing, and one that stalls inside the TLS handshake, are closed within
// HelloTimeout + 2 s (GLI-124 (a)); -race load margin of 2 s is added.
func TestGLI020_ConnectionWithoutHandshakeIsClosed(t *testing.T) {
	const helloTimeout = time.Second
	s := gliStartServer(t, gliwLimits(ingest.Limits{HelloTimeout: helloTimeout}))
	cases := []struct {
		name string
		send []byte
	}{
		{"silent TCP client", nil},
		{"TLS record header only", []byte{0x16, 0x03, 0x01, 0x00, 0xff}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c, err := net.DialTimeout("tcp", s.Addr, 3*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = c.Close() }()
			if tc.send != nil {
				if _, err := c.Write(tc.send); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			start := time.Now()
			if err := c.SetReadDeadline(start.Add(helloTimeout + 6*time.Second)); err != nil {
				t.Fatalf("deadline: %v", err)
			}
			buf := make([]byte, 512)
			for {
				_, err := c.Read(buf)
				if err == nil {
					continue // alert bytes may precede the close
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					t.Fatalf("the connection was still open %v after it started (HelloTimeout %v)", time.Since(start), helloTimeout)
				}
				break
			}
			if d := time.Since(start); d > helloTimeout+4*time.Second {
				t.Errorf("closed after %v, want within HelloTimeout + 2 s (+2 s load margin)", d)
			}
		})
	}
}

// TestGLI024_StreamEndsAtNotAfter: real-time expiry. The certificate expires
// 4-5 s after issue (X.509 has second resolution); the stream must end with
// Unauthenticated within one second of the encoded NotAfter (+2 s load margin).
func TestGLI024_StreamEndsAtNotAfter(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "expiry")
	n.Cert = s.PKI.NodeCert(t, s.Config.ClusterID, n.UID, gliwNotAfter(time.Now().Add(5*time.Second)))
	notAfter := gliwReadNotAfter(t, n.Cert)
	st, _, h := s.Connect(t, n)
	if time.Until(notAfter) < 1500*time.Millisecond {
		t.Fatalf("the hello took too long (%v left of the certificate); machine too slow for this test", time.Until(notAfter))
	}
	_ = h
	_, err := st.Recv()
	ended := time.Now()
	gliwWantCode(t, "stream at NotAfter", err, codes.Unauthenticated)
	if ended.Before(notAfter.Add(-200 * time.Millisecond)) {
		t.Errorf("the stream ended %v before NotAfter", notAfter.Sub(ended))
	}
	if late := ended.Sub(notAfter); late > 3*time.Second {
		t.Errorf("the stream ended %v after NotAfter, want within 1 s (+2 s load margin)", late)
	}
	// A rotated certificate starts a new hello and a new session.
	fresh := s.PKI.NodeCert(t, s.Config.ClusterID, n.UID)
	st2, closeFn := s.Stream(t, fresh)
	defer closeFn()
	h2, err := gliwHello(st2, s.HelloMsg(n))
	if err != nil {
		t.Fatalf("hello with the rotated certificate: %v", err)
	}
	if h2.GetSession() <= h.GetSession() {
		t.Errorf("new session %d is not greater than %d", h2.GetSession(), h.GetSession())
	}
}

// TestGLI024_FrameAtOrAfterNotAfterIsRejected: the fake Clock decides for frames.
// A frame processed at Clock.Now() >= NotAfter changes nothing and gets no ack;
// one second earlier it is accepted.
func TestGLI024_FrameAtOrAfterNotAfterIsRejected(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	s := gliStartServer(t, func(c *ingest.Config) { c.Clock = gliNewClock(start) })
	n := s.AddNode(t, "fakeexp")
	n.Cert = s.PKI.NodeCert(t, s.Config.ClusterID, n.UID, gliwNotAfter(start.Add(3*time.Hour)))
	if got := gliwReadNotAfter(t, n.Cert); !got.Equal(start.Add(3 * time.Hour)) {
		t.Fatalf("NotAfter encoded as %v, want %v", got, start.Add(3*time.Hour))
	}
	st, _, h := s.Connect(t, n)
	session := h.GetSession()
	s.Baseline(t, st, n, session)

	s.Clock.Advance(3*time.Hour - time.Second) // Now() = NotAfter - 1 s
	ack := gliwExchange(t, st, gliwFrame(t, n, session, 1))
	if ack.GetCode() != ingestpb.AckCode_ACCEPTED {
		t.Fatalf("frame one second before NotAfter: ack %v, want ACCEPTED", ack.GetCode())
	}
	s.WantSnapshot(t, "before expiry", n, session, 1)

	s.Clock.Advance(time.Second) // Now() == NotAfter: rejected (>=)
	if err := gliwSend(st, gliwFrame(t, n, session, 2)); err != nil {
		t.Fatalf("send: %v", err)
	}
	gliwExpectEnd(t, "frame at NotAfter", st, codes.Unauthenticated)
	s.WantSnapshot(t, "after the rejected frame", n, session, 1)

	// A rotated certificate gets a new session and the old observation is gone.
	fresh := s.PKI.NodeCert(t, s.Config.ClusterID, n.UID)
	st2, closeFn := s.Stream(t, fresh)
	defer closeFn()
	h2, err := gliwHello(st2, s.HelloMsg(n))
	if err != nil {
		t.Fatalf("hello with the rotated certificate: %v", err)
	}
	if h2.GetSession() <= session {
		t.Errorf("session %d after rotation, want > %d", h2.GetSession(), session)
	}
	s.WantNoObservation(t, "after the new hello", n)
}

// TestGLI025_OnlyIngestServiceIsRegistered: the listener serves nothing but
// Ingest. Reflection, health, channelz, admin and debug methods called with a
// valid node certificate are Unimplemented.
func TestGLI025_OnlyIngestServiceIsRegistered(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "svc")
	cc := s.DialTLS(t, s.ClientTLS(t, &n.Cert))
	methods := []string{
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
		"/grpc.channelz.v1.Channelz/GetTopChannels",
		"/grpc.channelz.v1.Channelz/GetServers",
		"/grpc.testing.TestService/EmptyCall",
		"/grpc.admin.v1.Admin/Anything",
		"/debug/pprof/",
		"/dpa.ingest.v1alpha1.Ingest/Other",
		"/dpa.ingest.v1alpha1.Other/Stream",
	}
	for _, m := range methods {
		m := m
		t.Run(m, func(t *testing.T) {
			err := cc.Invoke(t.Context(), m, &ingestpb.ClientHello{}, &ingestpb.ClientHello{})
			if got := gliwCode(err); got != codes.Unimplemented {
				t.Fatalf("%s -> %v (%v), want Unimplemented", m, got, err)
			}
		})
	}
	// The one registered method works on the same connection.
	st, cancel, err := s.open(cc)
	if err != nil {
		t.Fatalf("open Ingest.Stream: %v", err)
	}
	defer cancel()
	if _, err := gliwHello(st, s.HelloMsg(n)); err != nil {
		t.Fatalf("Ingest.Stream hello: %v", err)
	}
}
