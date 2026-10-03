package liveingest_test

// Shared helpers of the path-agent / path-controller / liveclient tests of
// gpu-fleet-live-ingest. Every name starts with gliB so it cannot collide with
// the shared helpers of the other test files of this package: this file has its own tiny CA
// (so a test can build a server certificate with any SAN), its own scripted
// ingest server (the agent's hello and frames must be observed, which the real
// server does not allow), a GFX-102 BASE sysfs fixture and fake nvidia-smi
// scripts, and the process helpers for the two binaries. The only borrowed
// helper is the independent canonical digest gliDigest of the shared frame helpers.
//
// Nothing here reads the repository's testdata/ directory: every input lives in
// t.TempDir(). Keys and certificates are never printed.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/agent/liveclient"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliBPathMarker is a directory name that appears in every path flag value of an
// agent (CA, certificate, key, sysfs root, nvidia-smi): GLI-101 requires that no
// output ever carries a flag path, so the marker must never show up there.
const gliBPathMarker = "GLI-PATH-MARKER-9d41c7"

const (
	gliBGPUUUID  = "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d"
	gliBGPUBDF   = "0000:03:00.0"
	gliBCluster  = "lab-a"
	gliBNodeName = "gpu-node-1"
	gliBNodeUID  = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	gliBBootID   = "3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90"
)

// ---------------------------------------------------------------------------
// small utilities
// ---------------------------------------------------------------------------

// gliBSyncBuf is a mutex protected output sink.
type gliBSyncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *gliBSyncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *gliBSyncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func gliBWrite(t testing.TB, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// gliBEventually polls cond every 25 ms and fails the test with what when the
// timeout (at most 20 s, GLI-125) expires.
func gliBEventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("not met within %s: %s", timeout, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// gliBH is GFO-084 H: SHA-256 over u32be(len)||bytes of every tuple element,
// the first 16 bytes as lower-case hex.
func gliBH(parts ...string) string {
	var b []byte
	for _, p := range parts {
		n := uint32(len(p))
		b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		b = append(b, p...)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// gliBFreePort returns a TCP port nothing listens on (best effort).
func gliBFreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// ---------------------------------------------------------------------------
// PKI (ECDSA P-256, every file in t.TempDir(), never printed)
// ---------------------------------------------------------------------------

type gliBPKI struct {
	dir    string
	key    *ecdsa.PrivateKey
	cert   *x509.Certificate
	CAFile string
	CAPEM  []byte
	serial atomic.Int64
	files  atomic.Int64
}

type gliBLeaf struct {
	Cert, Key string
	Leaf      *x509.Certificate
}

func gliBNewPKI(t *testing.T) *gliBPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gli test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	p := &gliBPKI{dir: t.TempDir(), key: key, cert: cert}
	p.serial.Store(1000)
	p.CAPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	p.CAFile = filepath.Join(p.dir, "ca.pem")
	gliBWrite(t, p.CAFile, string(p.CAPEM), 0o644)
	return p
}

// leaf issues a certificate from base (defaults: validity from an hour ago for
// ten years, digital-signature key usage) after the modifiers ran.
func (p *gliBPKI) leaf(t *testing.T, base *x509.Certificate, mods ...func(*x509.Certificate)) gliBLeaf {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	base.SerialNumber = big.NewInt(p.serial.Add(1))
	if base.NotBefore.IsZero() {
		base.NotBefore = time.Now().Add(-time.Hour)
	}
	if base.NotAfter.IsZero() {
		base.NotAfter = time.Now().AddDate(10, 0, 0)
	}
	base.KeyUsage = x509.KeyUsageDigitalSignature
	for _, m := range mods {
		m(base)
	}
	der, err := x509.CreateCertificate(rand.Reader, base, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	n := p.files.Add(1)
	l := gliBLeaf{
		Cert: filepath.Join(p.dir, fmt.Sprintf("leaf-%d.crt", n)),
		Key:  filepath.Join(p.dir, fmt.Sprintf("leaf-%d.key", n)),
		Leaf: parsed,
	}
	gliBWrite(t, l.Cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), 0o644)
	gliBWrite(t, l.Key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), 0o600)
	return l
}

// Server issues a server leaf (ServerAuth) with the DNS SANs (default localhost).
func (p *gliBPKI) Server(t *testing.T, dns ...string) gliBLeaf {
	t.Helper()
	if len(dns) == 0 {
		dns = []string{"localhost"}
	}
	return p.leaf(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "gli ingest server"},
		DNSNames:    dns,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
}

func gliBSAN(cluster, role, subject string) *url.URL {
	return &url.URL{Scheme: "spiffe", Host: "data-path-assurance.local", Path: "/cluster/" + cluster + "/" + role + "/" + subject}
}

// Node issues a node client leaf (URI SAN GLI-021, ClientAuth).
func (p *gliBPKI) Node(t *testing.T, cluster, uid string, mods ...func(*x509.Certificate)) gliBLeaf {
	t.Helper()
	return p.leaf(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "gli node"},
		URIs:        []*url.URL{gliBSAN(cluster, "node", uid)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, mods...)
}

// Role issues a client leaf of another role (fence, viewer).
func (p *gliBPKI) Role(t *testing.T, cluster, role, subject string) gliBLeaf {
	t.Helper()
	return p.leaf(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "gli " + role},
		URIs:        []*url.URL{gliBSAN(cluster, role, subject)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

// gliBInstall replaces the files certPath and keyPath with the leaf's content
// (write to a sibling then rename, so a reader never sees a half-written file).
func gliBInstall(t testing.TB, certPath, keyPath string, l gliBLeaf) {
	t.Helper()
	for _, c := range [][2]string{{l.Cert, certPath}, {l.Key, keyPath}} {
		b, err := os.ReadFile(c[0])
		if err != nil {
			t.Fatalf("read %s: %v", c[0], err)
		}
		tmp := c[1] + ".new"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			t.Fatalf("write %s: %v", tmp, err)
		}
		if err := os.Rename(tmp, c[1]); err != nil {
			t.Fatalf("rename %s: %v", tmp, err)
		}
	}
}

// ---------------------------------------------------------------------------
// sysfs fixture and fake nvidia-smi (GFX-102 BASE topology, GFO-104)
// ---------------------------------------------------------------------------

type gliBDev struct {
	path          []string
	class, vendor string
}

func gliBBaseDevices() []gliBDev {
	hb := "pci0000:00"
	return []gliBDev{
		{[]string{hb, "0000:00:01.0"}, "0x060400", "0x8086"},
		{[]string{hb, "0000:00:01.0", "0000:01:00.0"}, "0x060400", "0x10b5"},
		{[]string{hb, "0000:00:01.0", "0000:01:00.0", "0000:02:08.0"}, "0x060400", "0x10b5"},
		{[]string{hb, "0000:00:01.0", "0000:01:00.0", "0000:02:08.0", gliBGPUBDF}, "0x030200", "0x10de"},
		{[]string{hb, "0000:00:02.0"}, "0x060400", "0x8086"},
		{[]string{hb, "0000:00:02.0", "0000:04:00.0"}, "0x020000", "0x15b3"},
	}
}

// gliBBuildSysfs writes the BASE topology below root: device directories with
// class, vendor, current_link_width and max_link_width (value + newline) and a
// relative bus/pci/devices/<BDF> symlink for each.
func gliBBuildSysfs(t testing.TB, root string) {
	t.Helper()
	for _, d := range gliBBaseDevices() {
		dir := filepath.Join(root, "devices", filepath.Join(d.path...))
		gliBWrite(t, filepath.Join(dir, "class"), d.class+"\n", 0o644)
		gliBWrite(t, filepath.Join(dir, "vendor"), d.vendor+"\n", 0o644)
		gliBWrite(t, filepath.Join(dir, "current_link_width"), "16\n", 0o644)
		gliBWrite(t, filepath.Join(dir, "max_link_width"), "16\n", 0o644)
		link := filepath.Join(root, "bus", "pci", "devices", d.path[len(d.path)-1])
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(link), err)
		}
		if err := os.Symlink("../../../devices/"+strings.Join(d.path, "/"), link); err != nil {
			t.Fatalf("symlink %s: %v", link, err)
		}
	}
}

// gliBScript writes an executable shell script and returns its absolute,
// cleaned path (GFO-021).
func gliBScript(t testing.TB, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	gliBWrite(t, path, "#!/bin/sh\n"+body, 0o755)
	return path
}

func gliBNvidiaSMIOK(t testing.TB, dir string) string {
	t.Helper()
	return gliBScript(t, dir, "nvidia-smi", "printf '%s, %s\\n' '"+gliBGPUUUID+"' '00000000:03:00.0'\n")
}

// ---------------------------------------------------------------------------
// the agent under test
// ---------------------------------------------------------------------------

type gliBAgent struct {
	Dir                                string
	PKI                                *gliBPKI
	Cluster, NodeName, NodeUID, BootID string
	Sysfs, SMI, BootFile               string
	CAFile, CertFile, KeyFile          string
	Leaf                               gliBLeaf
	Logs                               *gliBSyncBuf
}

// gliBNewAgent builds a complete valid agent environment: PKI, a node leaf for
// lab-a / the test node UID, a BASE sysfs, a working fake nvidia-smi and a boot
// ID file. CA, certificate and key are copies, so a test may rewrite them.
func gliBNewAgent(t *testing.T) *gliBAgent {
	t.Helper()
	a := &gliBAgent{
		Dir: filepath.Join(t.TempDir(), gliBPathMarker), PKI: gliBNewPKI(t), Cluster: gliBCluster, NodeName: gliBNodeName, NodeUID: gliBNodeUID,
		BootID: gliBBootID, Logs: &gliBSyncBuf{},
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", a.Dir, err)
	}
	a.Sysfs = filepath.Join(a.Dir, "sys")
	gliBBuildSysfs(t, a.Sysfs)
	a.SMI = gliBNvidiaSMIOK(t, a.Dir)
	a.BootFile = filepath.Join(a.Dir, "boot_id")
	gliBWrite(t, a.BootFile, a.BootID+"\n", 0o644)
	a.CAFile = filepath.Join(a.Dir, "ca.pem")
	gliBWrite(t, a.CAFile, string(a.PKI.CAPEM), 0o644)
	a.CertFile = filepath.Join(a.Dir, "client.crt")
	a.KeyFile = filepath.Join(a.Dir, "client.key")
	a.Leaf = a.PKI.Node(t, a.Cluster, a.NodeUID)
	gliBInstall(t, a.CertFile, a.KeyFile, a.Leaf)
	return a
}

// Options are library options with test timings: Interval 150 ms, reconnect
// 200 ms .. 800 ms, hello and ack timeouts 3 s.
func (a *gliBAgent) Options(controller string) liveclient.Options {
	return liveclient.Options{
		Controller: controller, ClusterID: a.Cluster, NodeName: a.NodeName, NodeUID: a.NodeUID,
		SysfsRoot: a.Sysfs, NVIDIASMI: a.SMI, BootIDFile: a.BootFile,
		TLS:      liveclient.TLSFiles{CAFile: a.CAFile, CertFile: a.CertFile, KeyFile: a.KeyFile},
		Interval: 150 * time.Millisecond, HelloTimeout: 3 * time.Second, AckTimeout: 3 * time.Second,
		ReconnectInitial: 200 * time.Millisecond, ReconnectMax: 800 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(a.Logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

var gliBRequiredFlags = []string{"controller", "cluster-id", "node-name", "node-uid", "sysfs-root", "ca-file", "cert-file", "key-file"}

const gliBDrop = "\x00"

// Flags are the binary's required flags (plus --nvidia-smi and --boot-id-file)
// with overrides by flag name; an override of gliBDrop removes the flag.
func (a *gliBAgent) Flags(controller string, over map[string]string, extra ...string) []string {
	vals := map[string]string{
		"controller": controller, "cluster-id": a.Cluster, "node-name": a.NodeName, "node-uid": a.NodeUID,
		"sysfs-root": a.Sysfs, "ca-file": a.CAFile, "cert-file": a.CertFile, "key-file": a.KeyFile,
	}
	var args []string
	for _, fl := range gliBRequiredFlags {
		v := vals[fl]
		if o, ok := over[fl]; ok {
			v = o
		}
		if v != gliBDrop {
			args = append(args, "--"+fl, v)
		}
	}
	smi := a.SMI
	if o, ok := over["nvidia-smi"]; ok {
		smi = o
	}
	if smi != "" && smi != gliBDrop {
		args = append(args, "--nvidia-smi", smi)
	}
	if o, ok := over["boot-id-file"]; ok {
		if o != gliBDrop {
			args = append(args, "--boot-id-file", o)
		}
	} else {
		args = append(args, "--boot-id-file", a.BootFile)
	}
	return append(args, extra...)
}

// ---------------------------------------------------------------------------
// a running library client
// ---------------------------------------------------------------------------

type gliBClient struct {
	cancel context.CancelFunc
	fin    chan struct{}
	err    error
}

// gliBRun runs liveclient.Run in a goroutine; the test cleanup cancels it and
// waits for the return.
func gliBRun(t *testing.T, o liveclient.Options) *gliBClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &gliBClient{cancel: cancel, fin: make(chan struct{})}
	go func() {
		c.err = liveclient.Run(ctx, o)
		close(c.fin)
	}()
	t.Cleanup(func() { _ = c.Stop(t, 10*time.Second) })
	return c
}

// Running reports whether Run has not returned yet.
func (c *gliBClient) Running() bool {
	select {
	case <-c.fin:
		return false
	default:
		return true
	}
}

// Stop cancels the context and returns Run's result; it fails the test when Run
// does not return within limit.
func (c *gliBClient) Stop(t testing.TB, limit time.Duration) error {
	t.Helper()
	c.cancel()
	select {
	case <-c.fin:
		return c.err
	case <-time.After(limit):
		t.Errorf("liveclient.Run did not return within %s after the context was cancelled", limit)
		return errors.New("liveclient.Run did not return")
	}
}

// ---------------------------------------------------------------------------
// the scripted ingest server (mTLS, TLS 1.3, ALPN h2)
// ---------------------------------------------------------------------------

type gliBConnInfo struct {
	N                          int
	Leaf                       *x509.Certificate
	TLSVersion                 uint16
	ALPN                       string
	StartAt, HelloAt, ClosedAt time.Time
	Hello                      *ingestpb.ClientHello
	Frames                     []*ingestpb.SnapshotFrame
	FrameAt                    []time.Time
}

type gliBConn struct {
	mu   sync.Mutex
	info gliBConnInfo
}

func (c *gliBConn) Info() gliBConnInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.info
	out.Frames = append([]*ingestpb.SnapshotFrame(nil), c.info.Frames...)
	out.FrameAt = append([]time.Time(nil), c.info.FrameAt...)
	return out
}

// gliBStream is what a server handler works with.
type gliBStream struct {
	*gliBConn
	N      int
	Stream ingestpb.Ingest_StreamServer
}

func (s *gliBStream) Ctx() context.Context { return s.Stream.Context() }

func (s *gliBStream) RecvHello() (*ingestpb.ClientHello, error) {
	m, err := s.Stream.Recv()
	if err != nil {
		return nil, err
	}
	h := m.GetHello()
	if h == nil {
		return nil, status.Error(codes.InvalidArgument, "gli test server: first message is not a hello")
	}
	s.mu.Lock()
	s.info.Hello = h
	s.info.HelloAt = time.Now()
	s.mu.Unlock()
	return h, nil
}

func (s *gliBStream) SendHello(session int64, profile string) error {
	return s.Stream.Send(&ingestpb.ServerFrame{Frame: &ingestpb.ServerFrame_Hello{Hello: &ingestpb.ServerHello{Session: session, CollectorProfileId: profile}}})
}

func (s *gliBStream) RecvFrame() (*ingestpb.SnapshotFrame, error) {
	m, err := s.Stream.Recv()
	if err != nil {
		return nil, err
	}
	f := m.GetSnapshot()
	if f == nil {
		return nil, status.Error(codes.InvalidArgument, "gli test server: not a snapshot frame")
	}
	s.mu.Lock()
	s.info.Frames = append(s.info.Frames, f)
	s.info.FrameAt = append(s.info.FrameAt, time.Now())
	s.mu.Unlock()
	return f, nil
}

func (s *gliBStream) Ack(code ingestpb.AckCode, session int64, next uint64) error {
	return s.Stream.Send(&ingestpb.ServerFrame{Frame: &ingestpb.ServerFrame_Ack{Ack: &ingestpb.SnapshotAck{
		Code: code, AcceptedSession: session, ExpectedNextSequence: next, Message: "gli test",
	}}})
}

type gliBServerOpts struct {
	ServerCert             gliBLeaf
	ClientCAs              []*gliBPKI
	MinVersion, MaxVersion uint16
}

type gliBServer struct {
	ingestpb.UnimplementedIngestServer
	Addr, Port string
	srv        *grpc.Server
	cert       atomic.Pointer[tls.Certificate]
	mu         sync.Mutex
	conns      []*gliBConn
	handler    func(*gliBStream) error
}

// Target is the controller address the agent dials: the DNS name localhost and
// the server's port (GLI-112).
func (s *gliBServer) Target() string { return "localhost:" + s.Port }

// SetServerCert replaces the certificate presented to new connections.
func (s *gliBServer) SetServerCert(t testing.TB, l gliBLeaf) {
	t.Helper()
	c, err := tls.LoadX509KeyPair(l.Cert, l.Key)
	if err != nil {
		t.Fatalf("load server key pair: %v", err)
	}
	s.cert.Store(&c)
}

func (s *gliBServer) Conns() []gliBConnInfo {
	s.mu.Lock()
	conns := append([]*gliBConn(nil), s.conns...)
	s.mu.Unlock()
	out := make([]gliBConnInfo, len(conns))
	for i, c := range conns {
		out[i] = c.Info()
	}
	return out
}

func (s *gliBServer) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// SetHandler replaces the stream handler (used by the next stream).
func (s *gliBServer) SetHandler(h func(*gliBStream) error) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

func (s *gliBServer) Stream(stream ingestpb.Ingest_StreamServer) error {
	s.mu.Lock()
	n := len(s.conns) + 1
	c := &gliBConn{info: gliBConnInfo{N: n, StartAt: time.Now()}}
	s.conns = append(s.conns, c)
	handler := s.handler
	s.mu.Unlock()
	if p, ok := peer.FromContext(stream.Context()); ok {
		if ti, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			c.mu.Lock()
			c.info.TLSVersion = ti.State.Version
			c.info.ALPN = ti.State.NegotiatedProtocol
			if len(ti.State.PeerCertificates) > 0 {
				c.info.Leaf = ti.State.PeerCertificates[0]
			}
			c.mu.Unlock()
		}
	}
	defer func() {
		c.mu.Lock()
		c.info.ClosedAt = time.Now()
		c.mu.Unlock()
	}()
	if handler == nil {
		return status.Error(codes.Unimplemented, "gli test server: no handler")
	}
	return handler(&gliBStream{gliBConn: c, N: n, Stream: stream})
}

// gliBStartServer listens on 127.0.0.1:0 with mutual TLS (TLS 1.3 minimum, ALPN
// h2, client certificates verified against the given CAs) and serves handler.
func gliBStartServer(t *testing.T, o gliBServerOpts, handler func(*gliBStream) error) *gliBServer {
	t.Helper()
	s := &gliBServer{handler: handler}
	s.SetServerCert(t, o.ServerCert)
	pool := x509.NewCertPool()
	for _, p := range o.ClientCAs {
		pool.AddCert(p.cert)
	}
	cfg := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.cert.Load(), nil },
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool,
		MinVersion:     tls.VersionTLS13,
		NextProtos:     []string{"h2"},
	}
	if o.MinVersion != 0 {
		cfg.MinVersion = o.MinVersion
	}
	if o.MaxVersion != 0 {
		cfg.MaxVersion = o.MaxVersion
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.Addr = lis.Addr().String()
	s.Port = fmt.Sprint(lis.Addr().(*net.TCPAddr).Port)
	s.srv = grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
	ingestpb.RegisterIngestServer(s.srv, s)
	go func() { _ = s.srv.Serve(lis) }()
	t.Cleanup(s.srv.Stop)
	return s
}

// gliBAcceptAll answers the hello with session base+N (profile live:test) and
// acknowledges every frame ACCEPTED until the client ends the stream.
func gliBAcceptAll(base int64, onFrame func(s *gliBStream, f *ingestpb.SnapshotFrame)) func(*gliBStream) error {
	return func(s *gliBStream) error {
		if _, err := s.RecvHello(); err != nil {
			return err
		}
		session := base + int64(s.N)
		if err := s.SendHello(session, "live:test"); err != nil {
			return err
		}
		for {
			f, err := s.RecvFrame()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if onFrame != nil {
				onFrame(s, f)
			}
			if err := s.Ack(ingestpb.AckCode_ACCEPTED, session, f.GetSequence()+1); err != nil {
				return err
			}
		}
	}
}

// ---------------------------------------------------------------------------
// frame oracle (independent of the product: GFO-084 IDs, gliDigest of the shared frame helpers)
// ---------------------------------------------------------------------------

func gliBAssetKey(a *ingestpb.Asset) string { return a.GetKind() + "/" + a.GetCanonical() }

// gliBCheckEnvelope asserts the identity fields, the completeness, the validity
// of observed_at and the canonical digest and revision (through the independent
// gliDigest) of a frame; it returns observed_at.
func gliBCheckEnvelope(t *testing.T, a *gliBAgent, session int64, f *ingestpb.SnapshotFrame, wantSeq uint64, wantComplete ingestpb.Completeness) time.Time {
	t.Helper()
	if f == nil {
		t.Fatalf("frame %d: missing", wantSeq)
	}
	if f.GetNodeUid() != a.NodeUID || f.GetBootId() != a.BootID || f.GetSession() != session || f.GetSequence() != wantSeq {
		t.Errorf("frame identity = node %q boot %q session %d sequence %d, want %q %q %d %d",
			f.GetNodeUid(), f.GetBootId(), f.GetSession(), f.GetSequence(), a.NodeUID, a.BootID, session, wantSeq)
	}
	if f.GetCompleteness() != wantComplete {
		t.Errorf("frame %d completeness = %v, want %v", wantSeq, f.GetCompleteness(), wantComplete)
	}
	if f.GetObservedAt() == nil || f.GetObservedAt().CheckValid() != nil {
		t.Fatalf("frame %d observed_at is missing or invalid", wantSeq)
	}
	p := f.GetPayload()
	if p == nil {
		t.Fatalf("frame %d has no payload", wantSeq)
	}
	d := gliDigest(p)
	if !bytes.Equal(f.GetPayloadDigest(), d[:]) {
		t.Errorf("frame %d payload_digest %x is not the independent canonical digest %x (GLI-061)", wantSeq, f.GetPayloadDigest(), d[:])
	}
	if want := fmt.Sprintf("%d:%d:%x", session, wantSeq, d[:]); f.GetBundleRevision() != want {
		t.Errorf("frame %d bundle_revision = %q, want %q", wantSeq, f.GetBundleRevision(), want)
	}
	return f.GetObservedAt().AsTime()
}

// gliBCheckFrame asserts everything GLI-081/082/061/060(j) fix about an agent
// frame without calling product code: identity fields, the canonical digest and
// revision (gliDigest), the node asset, the observation and evidence times
// (= observed_at, expiry = observed_at + ttl), GFO-084 IDs, source names, the
// GPU width pair of the BASE fixture (provenance adjacent_capability_min: the
// live agent has no operator baseline) and the binding of the fake nvidia-smi.
func gliBCheckFrame(t *testing.T, a *gliBAgent, session int64, f *ingestpb.SnapshotFrame, wantSeq uint64, ttl time.Duration, wantComplete ingestpb.Completeness, wantBinding bool) {
	t.Helper()
	observed := gliBCheckEnvelope(t, a, session, f, wantSeq, wantComplete)
	expires := observed.Add(ttl)
	p := f.GetPayload()
	if p.GetAllocationBatch() != nil {
		t.Errorf("frame %d carries an allocation_batch: the S3b-1 agent never fills it (GLI-081 (g))", wantSeq)
	}
	nodes := 0
	for _, as := range p.GetAssets() {
		if as.GetKind() == "KubernetesNode" {
			nodes++
			if as.GetCanonical() != "kubernetes-node-uid:"+a.NodeUID {
				t.Errorf("frame %d node asset canonical = %q, want kubernetes-node-uid:%s", wantSeq, as.GetCanonical(), a.NodeUID)
			}
		}
	}
	if nodes != 1 {
		t.Errorf("frame %d has %d KubernetesNode assets, want exactly 1", wantSeq, nodes)
	}
	prefix := func(kind string) string { return fmt.Sprintf("%s:%d:%d:", kind, session, wantSeq) }
	if len(p.GetEdgeEvidence()) != len(p.GetEdges()) {
		t.Errorf("frame %d: %d edgeEvidence for %d edges", wantSeq, len(p.GetEdgeEvidence()), len(p.GetEdges()))
	}
	for i, ee := range p.GetEdgeEvidence() {
		if int(ee.GetEdgeIndex()) != i || i >= len(p.GetEdges()) {
			t.Errorf("frame %d: edgeEvidence %d has edge_index %d", wantSeq, i, ee.GetEdgeIndex())
			continue
		}
		e := p.GetEdges()[i]
		if e.GetRelation() != "LOCATED_IN" || e.GetOrigin() != "Observed" {
			t.Errorf("frame %d: edge %d relation %q origin %q, want LOCATED_IN Observed", wantSeq, i, e.GetRelation(), e.GetOrigin())
		}
		wantID := prefix("pe") + gliBH("dpa.edge-evidence.v1", e.GetFromKey(), e.GetRelation(), e.GetToKey(), e.GetOrigin())
		if ee.GetEvidenceId() != wantID {
			t.Errorf("frame %d: edgeEvidence %d id = %q, want %q (GFO-084)", wantSeq, i, ee.GetEvidenceId(), wantID)
		}
		if ee.GetKind() != "Observed" || ee.GetSourceType() != "agent" || ee.GetSourceName() != "path-agent/sysfs-parent" {
			t.Errorf("frame %d: edgeEvidence %d = kind %q source %q/%q", wantSeq, i, ee.GetKind(), ee.GetSourceType(), ee.GetSourceName())
		}
		if !ee.GetObservedAt().AsTime().Equal(observed) || !ee.GetExpiresAt().AsTime().Equal(expires) {
			t.Errorf("frame %d: edgeEvidence %d times %v/%v, want %v/%v", wantSeq, i, ee.GetObservedAt().AsTime(), ee.GetExpiresAt().AsTime(), observed, expires)
		}
	}
	gpuKey := "PCIeFunction/pci-bdf:" + gliBGPUBDF
	pair := map[string]string{}
	for _, o := range p.GetObservations() {
		if !o.GetObservedAt().AsTime().Equal(observed) || !o.GetReceivedAt().AsTime().Equal(observed) {
			t.Errorf("frame %d: observation %s observed/received %v/%v, want %v", wantSeq, o.GetSignal(), o.GetObservedAt().AsTime(), o.GetReceivedAt().AsTime(), observed)
		}
		if o.GetExpiresAt() == nil || !o.GetExpiresAt().AsTime().Equal(expires) {
			t.Errorf("frame %d: observation %s expires_at = %v, want %v (GFO-064)", wantSeq, o.GetSignal(), o.GetExpiresAt().AsTime(), expires)
		}
		if o.GetSequence() != wantSeq || o.GetQuality() != "Good" || o.GetRawDigest() != "" {
			t.Errorf("frame %d: observation %s sequence %d quality %q raw_digest %q", wantSeq, o.GetSignal(), o.GetSequence(), o.GetQuality(), o.GetRawDigest())
		}
		if wantID := prefix("ob") + gliBH("dpa.observation.v1", gliBAssetKey(o.GetSubject()), o.GetSignal()); o.GetId() != wantID {
			t.Errorf("frame %d: observation %s id = %q, want %q (GFO-084)", wantSeq, o.GetSignal(), o.GetId(), wantID)
		}
		if gliBAssetKey(o.GetSubject()) == gpuKey && (o.GetSignal() == "pcie.link.width.current" || o.GetSignal() == "pcie.link.width.expected") {
			if o.GetSource().GetType() != "agent" || o.GetSource().GetName() != "path-agent/sysfs-width" || o.GetUnit() != "lanes" || o.GetValue().GetIntValue() != 16 {
				t.Errorf("frame %d: GPU %s observation = source %v unit %q value %v", wantSeq, o.GetSignal(), o.GetSource(), o.GetUnit(), o.GetValue())
			}
			for _, dim := range o.GetDimensions() {
				if dim.GetKey() == "pcie.expected.provenance" {
					pair[o.GetSignal()] = dim.GetValue()
				}
			}
		}
	}
	for _, sig := range []string{"pcie.link.width.current", "pcie.link.width.expected"} {
		if pair[sig] != "adjacent_capability_min" {
			t.Errorf("frame %d: GPU %s provenance = %q, want adjacent_capability_min (GFO-062: the live agent has no operator baseline)", wantSeq, sig, pair[sig])
		}
	}
	bindings := p.GetGpuBindings()
	switch {
	case wantBinding && len(bindings) != 1:
		t.Errorf("frame %d has %d gpuBindings, want 1", wantSeq, len(bindings))
	case !wantBinding && len(bindings) != 0:
		t.Errorf("frame %d has %d gpuBindings, want 0 (nvidia-smi absent, failing or malformed: GLI-081 (b))", wantSeq, len(bindings))
	}
	for _, b := range bindings {
		if b.GetUuid() != gliBGPUUUID || b.GetBdf() != gliBGPUBDF || b.GetSerial() != "" || b.GetSourceType() != "agent" || b.GetSourceName() != "path-agent/nvidia-smi" {
			t.Errorf("frame %d: gpuBinding = %v", wantSeq, b)
		}
		if wantID := prefix("nb") + gliBH("dpa.gpu-binding.v1", b.GetUuid(), b.GetBdf()); b.GetEvidenceId() != wantID {
			t.Errorf("frame %d: gpuBinding id = %q, want %q (GFO-084)", wantSeq, b.GetEvidenceId(), wantID)
		}
		if !b.GetObservedAt().AsTime().Equal(observed) || !b.GetExpiresAt().AsTime().Equal(expires) {
			t.Errorf("frame %d: gpuBinding times %v/%v, want %v/%v", wantSeq, b.GetObservedAt().AsTime(), b.GetExpiresAt().AsTime(), observed, expires)
		}
	}
}

// ---------------------------------------------------------------------------
// the binaries
// ---------------------------------------------------------------------------

var (
	gliBBuildMu  sync.Mutex
	gliBBuilt    = map[string]string{}
	gliBBuildErr = map[string]error{}
)

// gliBBinary builds ./cmd/<name> once per test process into build/gli-test-bin
// (the repository's self-ignoring build directory), with CGO_ENABLED=0 (GLI-120).
func gliBBinary(t *testing.T, name string) string {
	t.Helper()
	gliBBuildMu.Lock()
	defer gliBBuildMu.Unlock()
	if p, ok := gliBBuilt[name]; ok {
		return p
	}
	if err, ok := gliBBuildErr[name]; ok {
		t.Fatalf("go build ./cmd/%s failed earlier: %v", name, err)
	}
	root := gliXRoot(t)
	outDir := filepath.Join(root, "build", "gli-test-bin")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("cannot create %s: %v", outDir, err)
	}
	if _, err := os.Stat(filepath.Join(root, "build", ".gitignore")); err != nil {
		_ = os.WriteFile(filepath.Join(root, "build", ".gitignore"), []byte("*\n"), 0o644)
	}
	out := filepath.Join(outDir, name)
	stdout, stderr, err := gliXRun(root, gliXEnv("CGO_ENABLED=0"), 20*time.Minute, "go", "build", "-o", out, "./cmd/"+name)
	if err != nil {
		gliBBuildErr[name] = err
		t.Fatalf("go build ./cmd/%s failed: %v\n%s%s", name, err, gliXHead(stdout, 1500), gliXHead(stderr, 3000))
	}
	gliBBuilt[name] = out
	return out
}

type gliBResult struct {
	Exit           int
	Stdout, Stderr string
	Elapsed        time.Duration
	Killed         bool
}

// gliBExec runs the binary to completion; it is killed after timeout.
func gliBExec(t testing.TB, bin string, env []string, timeout time.Duration, args ...string) gliBResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 2 * time.Second
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr gliBSyncBuf
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	res := gliBResult{Stdout: stdout.String(), Stderr: stderr.String(), Elapsed: time.Since(start)}
	if ctx.Err() != nil {
		res.Killed = true
		res.Exit = -1
		return res
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		t.Fatalf("cannot run %s %q: %v", bin, args, err)
	}
	return res
}

// gliBProc is a binary that keeps running.
type gliBProc struct {
	cmd    *exec.Cmd
	Stdout gliBSyncBuf
	Stderr gliBSyncBuf
	done   chan struct{}
	err    error
}

func gliBSpawn(t *testing.T, bin string, env []string, args ...string) *gliBProc {
	t.Helper()
	p := &gliBProc{done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	if env != nil {
		p.cmd.Env = env
	}
	p.cmd.Stdout, p.cmd.Stderr = &p.Stdout, &p.Stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("cannot start %s: %v", bin, err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *gliBProc) Pid() int { return p.cmd.Process.Pid }

func (p *gliBProc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Wait returns the exit code and whether the process exited within timeout.
func (p *gliBProc) Wait(timeout time.Duration) (int, bool) {
	select {
	case <-p.done:
		if p.err == nil {
			return 0, true
		}
		var ee *exec.ExitError
		if errors.As(p.err, &ee) {
			return ee.ExitCode(), true
		}
		return -2, true
	case <-time.After(timeout):
		return 0, false
	}
}

func (p *gliBProc) Signal(t testing.TB, sig syscall.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("cannot signal the process: %v", err)
	}
}

// gliBCleanEnv is the process environment without anything that could make a
// binary find a cluster on its own, plus extra entries.
func gliBCleanEnv(t testing.TB, extra ...string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "KUBECONFIG="), strings.HasPrefix(kv, "KUBERNETES_SERVICE_HOST="), strings.HasPrefix(kv, "KUBERNETES_SERVICE_PORT="),
			strings.HasPrefix(kv, "HOME="), strings.HasPrefix(kv, "GRPC_GO_LOG"):
		default:
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+t.TempDir())
	return append(env, extra...)
}

// gliBStartLine checks the shape of a start-up failure (GLI-080, GKA-162): one
// line `<prog>: error: <token>: <message>`, printable ASCII, at most 8 KiB,
// containing none of the forbidden substrings; messages, when given, is the
// closed set the message must come from.
func gliBStartLine(t testing.TB, what string, res gliBResult, prog, token string, forbidden []string, messages ...string) {
	t.Helper()
	if res.Stdout != "" {
		t.Errorf("%s: stdout is not empty: %q", what, gliXHead(res.Stdout, 200))
	}
	for _, b := range []byte(res.Stderr) {
		if b != '\n' && (b < 0x20 || b > 0x7e) {
			t.Errorf("%s: stderr holds the byte 0x%02x: %q", what, b, gliXHead(res.Stderr, 300))
			return
		}
	}
	if len(res.Stderr) > 8192 {
		t.Errorf("%s: stderr is %d bytes, want at most 8192", what, len(res.Stderr))
	}
	if strings.Count(res.Stderr, "\n") != 1 || !strings.HasSuffix(res.Stderr, "\n") {
		t.Errorf("%s: stderr must be exactly one line ending in a newline, got %q", what, gliXHead(res.Stderr, 400))
		return
	}
	pre := prog + ": error: " + token + ": "
	if !strings.HasPrefix(res.Stderr, pre) {
		t.Errorf("%s: stderr %q does not start with %q", what, gliXHead(res.Stderr, 300), pre)
		return
	}
	msg := strings.TrimSuffix(strings.TrimPrefix(res.Stderr, pre), "\n")
	if msg == "" {
		t.Errorf("%s: the message is empty", what)
	}
	if len(messages) > 0 {
		ok := false
		for _, m := range messages {
			ok = ok || m == msg
		}
		if !ok {
			t.Errorf("%s: message %q, want one of %v", what, msg, messages)
		}
	}
	for _, f := range forbidden {
		if f != "" && strings.Contains(res.Stderr, f) {
			t.Errorf("%s: stderr contains the forbidden text %q: %q", what, f, gliXHead(res.Stderr, 300))
		}
	}
}

// gliBSlogLines asserts that every non-empty stderr line is a slog text record
// (it carries level=), except the allowed last line.
func gliBSlogLines(t testing.TB, what, stderr, allowedLast string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if allowedLast != "" && i == len(lines)-1 && l == allowedLast {
			continue
		}
		if !strings.Contains(l, "level=") {
			t.Errorf("%s: stderr line %d is not a slog text record (no level=): %q", what, i+1, gliXHead(l, 300))
		}
	}
}

// gliBListenSockets counts the LISTEN TCP sockets of a process with lsof (or
// /proc); ok is false when the platform offers neither.
func gliBListenSockets(pid int) (n int, how string, ok bool) {
	if path, err := exec.LookPath("lsof"); err == nil {
		out, err := exec.Command(path, "-nP", "-a", "-p", fmt.Sprint(pid), "-iTCP", "-sTCP:LISTEN").Output()
		if err != nil && len(out) > 0 {
			return 0, "lsof", false
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return 0, "lsof", true
		}
		return len(lines) - 1, "lsof: " + strings.Join(lines, " | "), true // the first line is the header
	}
	inodes := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			return 0, "", false
		}
		for i, line := range strings.Split(string(b), "\n") {
			fs := strings.Fields(line)
			if i == 0 || len(fs) < 10 || fs[3] != "0A" {
				continue
			}
			inodes[fs[9]] = true
		}
	}
	links, err := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", pid))
	if err != nil {
		return 0, "", false
	}
	count := 0
	for _, l := range links {
		target, err := os.Readlink(l)
		if err == nil && strings.HasPrefix(target, "socket:[") && inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] {
			count++
		}
	}
	return count, "/proc", true
}
