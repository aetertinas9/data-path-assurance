package liveingest_test

// In-process ingest server harness (GLI-120, GLI-122). gliStartServer starts a
// real ingest.Server on 127.0.0.1:0 with the fakes of gli_fakes_test.go and a
// freshly generated PKI; every connection the helpers open is a real TLS 1.3
// gRPC connection.
//
// Names with the gliw prefix are private to this file group. The members of
// gliServer and the shared methods gliStartServer, Stream and Hello are the
// interface other test files rely on. Logs is the raw slog output; the server
// writes to it from its own goroutines, so tests read it through LogText()
// (mutex-protected), never through Logs directly.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const (
	gliwCluster = "lab-a"
	gliwVersion = "v1alpha1"
	// gliwStreamTimeout is the backstop for every client stream so a server bug
	// shows up as a failure instead of a hang.
	gliwStreamTimeout = 90 * time.Second
)

// gliwT0 is the default fake Clock start and the base of frame times.
var gliwT0 = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

// gliwAt is the observed_at of sequence seq: strictly increasing, one second apart.
func gliwAt(seq uint64) time.Time { return gliwT0.Add(time.Duration(seq) * time.Second) }

// --- log sink --------------------------------------------------------------

type gliwLogSink struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (s *gliwLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *gliwLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// --- server ----------------------------------------------------------------

type gliServer struct {
	Addr     string
	Server   *ingest.Server
	PKI      *gliPKI
	Clock    *gliClock
	Sessions *gliSessions
	Nodes    *gliNodes
	Leader   *gliLeader
	Logs     *bytes.Buffer // slog output; read through LogText()

	// Config is the configuration the server was built from (after the mods).
	Config ingest.Config
	// StreamTimeout bounds every client stream opened by the helpers (default
	// gliwStreamTimeout); tests that expect a prompt rejection shorten it.
	StreamTimeout time.Duration

	sink    *gliwLogSink
	cancel  context.CancelFunc
	done    chan error
	stopMu  sync.Mutex
	stopped bool
	stopErr error
	stopDur time.Duration
}

// gliwBase holds the fakes and the PKI a server is built from.
type gliwBase struct {
	PKI      *gliPKI
	Clock    *gliClock
	Sessions *gliSessions
	Nodes    *gliNodes
	Leader   *gliLeader
	Logs     *bytes.Buffer
	sink     *gliwLogSink
	t        *testing.T // the test the base belongs to (for helpers inside mutations)
}

func gliwNewBase(t *testing.T) *gliwBase {
	t.Helper()
	logs := &bytes.Buffer{}
	return &gliwBase{
		PKI:      gliNewPKI(t),
		Clock:    gliNewClock(gliwT0),
		Sessions: gliNewSessions(),
		Nodes:    gliNewNodes(),
		Leader:   gliNewLeader(true),
		Logs:     logs,
		sink:     &gliwLogSink{buf: logs},
		t:        t,
	}
}

// Config is the default valid server configuration over the base's fakes.
func (b *gliwBase) Config() ingest.Config {
	return ingest.Config{
		ClusterID:          gliwCluster,
		ServiceDNS:         "localhost",
		CollectorProfileID: "live:default",
		TrustedSources:     ingest.DefaultTrustedSources(),
		TLS: ingest.TLSFiles{
			CAFile:   b.PKI.CAFile,
			CertFile: b.PKI.Server.CertFile,
			KeyFile:  b.PKI.Server.KeyFile,
		},
		Nodes:    b.Nodes,
		Sessions: b.Sessions,
		Leader:   b.Leader,
		Clock:    b.Clock,
		Logger:   slog.New(slog.NewTextHandler(b.sink, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// gliwBuildServer builds (NewServer) but does not start a server.
func gliwBuildServer(t *testing.T, mod ...func(*ingest.Config)) *gliServer {
	t.Helper()
	b := gliwNewBase(t)
	cfg := b.Config()
	for _, m := range mod {
		m(&cfg)
	}
	srv, err := ingest.NewServer(cfg)
	if err != nil {
		t.Fatalf("gliw: NewServer rejected the test configuration: %v", err)
	}
	s := &gliServer{
		Server: srv, PKI: b.PKI, Clock: b.Clock, Sessions: b.Sessions, Nodes: b.Nodes,
		Leader: b.Leader, Logs: b.Logs, Config: cfg, sink: b.sink,
		StreamTimeout: gliwStreamTimeout,
	}
	// A mod may swap in its own fake of the same kind; follow it.
	if c, ok := cfg.Clock.(*gliClock); ok {
		s.Clock = c
	}
	if x, ok := cfg.Sessions.(*gliSessions); ok {
		s.Sessions = x
	}
	if x, ok := cfg.Nodes.(*gliNodes); ok {
		s.Nodes = x
	}
	if x, ok := cfg.Leader.(*gliLeader); ok {
		s.Leader = x
	}
	return s
}

// Start listens on 127.0.0.1:0 and serves in the background. The Cleanup cancels
// Serve and fails the test if it does not return nil within 20 seconds.
func (s *gliServer) Start(t *testing.T) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gliw: listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan error, 1)
	s.Addr = lis.Addr().String()
	go func() { s.done <- s.Server.Serve(ctx, lis) }()
	t.Cleanup(func() {
		if _, err := s.Stop(t); err != nil {
			t.Errorf("Serve after cancel returned %v, want nil within 20s", err)
		}
	})
}

// Stop cancels Serve and waits for it. It returns how long Serve took to return
// and Serve's result; repeated calls return the first result.
func (s *gliServer) Stop(t *testing.T) (time.Duration, error) {
	t.Helper()
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopped {
		return s.stopDur, s.stopErr
	}
	s.stopped = true
	start := time.Now()
	s.cancel()
	select {
	case err := <-s.done:
		s.stopErr = err
	case <-time.After(20 * time.Second):
		s.stopErr = errors.New("gliw: Serve did not return within 20s of cancel")
	}
	s.stopDur = time.Since(start)
	return s.stopDur, s.stopErr
}

// gliStartServer starts a server on 127.0.0.1:0 with fresh fakes (empty node
// directory, Leader leading, Clock at 2026-09-30T00:00:00Z).
func gliStartServer(t *testing.T, mod ...func(*ingest.Config)) *gliServer {
	t.Helper()
	s := gliwBuildServer(t, mod...)
	s.Start(t)
	return s
}

// LogText returns everything the server has logged so far.
func (s *gliServer) LogText() string { return s.sink.String() }

// --- nodes -------------------------------------------------------------------

type gliwNode struct {
	Name, UID, Boot string
	Cert            gliCertFiles
}

var gliwNodeCounter atomic.Int64

// AddNode creates a node (unique name, UID and boot ID), registers it in the node
// directory and issues its client certificate.
func (s *gliServer) AddNode(t *testing.T, label string) gliwNode {
	t.Helper()
	n := gliwNodeCounter.Add(1)
	node := gliwNode{
		Name: fmt.Sprintf("gliw-%s-%d", label, n),
		UID:  fmt.Sprintf("7c9e6679-7425-40de-944b-%012x", n),
		Boot: fmt.Sprintf("3f1c2a9e-8d4b-4e0a-9b1f-%012x", n),
	}
	node.Cert = s.PKI.NodeCert(t, s.Config.ClusterID, node.UID)
	s.Nodes.Put(node.Name, node.UID)
	return node
}

// HelloMsg is the valid ClientHello of a node.
func (s *gliServer) HelloMsg(n gliwNode) *ingestpb.ClientHello {
	return &ingestpb.ClientHello{
		Version: gliwVersion, ClusterId: s.Config.ClusterID,
		NodeName: n.Name, NodeUid: n.UID, BootId: n.Boot,
	}
}

// --- connections ---------------------------------------------------------------

// ClientTLS is a client TLS configuration that trusts the server PKI and presents
// c (nil: no client certificate). The certificate is handed over through
// GetClientCertificate, so even a leaf the server cannot parse is sent verbatim.
// Callers may edit the returned configuration (versions, caches, callbacks).
func (s *gliServer) ClientTLS(t *testing.T, c *gliCertFiles) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(s.PKI.CAPEM()) {
		t.Fatalf("gliw: cannot load the PKI CA")
	}
	var cert tls.Certificate
	if c != nil {
		cert = gliwLoadCert(t, *c)
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		},
	}
}

func (s *gliServer) dial(t *testing.T, creds credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(s.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("gliw: NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

// DialTLS opens a new client connection with the given TLS configuration.
func (s *gliServer) DialTLS(t *testing.T, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	return s.dial(t, credentials.NewTLS(cfg))
}

// DialPlaintext opens a cleartext HTTP/2 (h2c) client connection.
func (s *gliServer) DialPlaintext(t *testing.T) *grpc.ClientConn {
	t.Helper()
	return s.dial(t, insecure.NewCredentials())
}

func (s *gliServer) open(cc *grpc.ClientConn, opts ...grpc.CallOption) (ingestpb.Ingest_StreamClient, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.StreamTimeout)
	st, err := ingestpb.NewIngestClient(cc).Stream(ctx, opts...)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return st, cancel, nil
}

// StreamWith is Stream with call options (compression, forced codec).
func (s *gliServer) StreamWith(t *testing.T, c gliCertFiles, opts ...grpc.CallOption) (ingestpb.Ingest_StreamClient, func()) {
	t.Helper()
	cc := s.DialTLS(t, s.ClientTLS(t, &c))
	st, cancel, err := s.open(cc, opts...)
	if err != nil {
		t.Fatalf("gliw: open stream: %v", err)
	}
	closeFn := func() { cancel(); _ = cc.Close() }
	t.Cleanup(closeFn)
	return st, closeFn
}

// Stream opens a new ClientConn with certificate c and a new Ingest.Stream on it.
// The returned function closes both.
func (s *gliServer) Stream(t *testing.T, c gliCertFiles) (ingestpb.Ingest_StreamClient, func()) {
	t.Helper()
	return s.StreamWith(t, c)
}

// TryStream opens a stream with a prepared TLS configuration (or cleartext when
// cfg is nil) and reports a failure to open as an error instead of failing the
// test. A rejected connection may fail here or at the first Send/Recv.
func (s *gliServer) TryStream(t *testing.T, cfg *tls.Config, opts ...grpc.CallOption) (ingestpb.Ingest_StreamClient, func(), error) {
	t.Helper()
	var cc *grpc.ClientConn
	if cfg == nil {
		cc = s.DialPlaintext(t)
	} else {
		cc = s.DialTLS(t, cfg)
	}
	st, cancel, err := s.open(cc, opts...)
	if err != nil {
		return nil, func() { _ = cc.Close() }, err
	}
	closeFn := func() { cancel(); _ = cc.Close() }
	t.Cleanup(closeFn)
	return st, closeFn, nil
}

// RawStream is a stream whose codec passes gliwRaw messages through unchanged.
func (s *gliServer) RawStream(t *testing.T, c gliCertFiles, opts ...grpc.CallOption) (ingestpb.Ingest_StreamClient, func()) {
	t.Helper()
	return s.StreamWith(t, c, append([]grpc.CallOption{grpc.ForceCodecV2(gliwRawCodec{})}, opts...)...)
}

// --- raw codec ------------------------------------------------------------------

// gliwRaw is a message that the raw codec writes to the wire verbatim.
type gliwRaw []byte

// gliwRawCodec is the "proto" codec except that gliwRaw is not marshalled (GLI-046).
type gliwRawCodec struct{}

func (gliwRawCodec) Name() string { return "proto" }

func (gliwRawCodec) Marshal(v any) (mem.BufferSlice, error) {
	switch m := v.(type) {
	case gliwRaw:
		return mem.BufferSlice{mem.SliceBuffer(m)}, nil
	case proto.Message:
		b, err := proto.Marshal(m)
		if err != nil {
			return nil, err
		}
		return mem.BufferSlice{mem.SliceBuffer(b)}, nil
	}
	return nil, fmt.Errorf("gliw: cannot marshal %T", v)
}

func (gliwRawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	m, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("gliw: cannot unmarshal into %T", v)
	}
	return proto.Unmarshal(data.Materialize(), m)
}

// --- hello and frames ---------------------------------------------------------------

// gliwHello sends h as the first message and waits for the server's answer.
// Send errors that only say "the stream is already finished" (io.EOF) are not
// the answer; the status comes from Recv.
func gliwHello(st ingestpb.Ingest_StreamClient, h *ingestpb.ClientHello) (*ingestpb.ServerHello, error) {
	msg := &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: h}}
	if err := st.Send(msg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	fr, err := st.Recv()
	if err != nil {
		return nil, err
	}
	if fr.GetHello() == nil {
		return nil, errors.New("gliw: the first server message is not a ServerHello")
	}
	return fr.GetHello(), nil
}

// Hello sends a valid-version ClientHello with the given fields and returns the
// ServerHello, or the stream status error.
func (s *gliServer) Hello(t *testing.T, st ingestpb.Ingest_StreamClient, clusterID, nodeName, nodeUID, bootID string) (*ingestpb.ServerHello, error) {
	t.Helper()
	return gliwHello(st, &ingestpb.ClientHello{
		Version: gliwVersion, ClusterId: clusterID, NodeName: nodeName, NodeUid: nodeUID, BootId: bootID,
	})
}

// Connect opens a stream for the node and requires a successful hello.
func (s *gliServer) Connect(t *testing.T, n gliwNode, opts ...grpc.CallOption) (ingestpb.Ingest_StreamClient, func(), *ingestpb.ServerHello) {
	t.Helper()
	st, closeFn := s.StreamWith(t, n.Cert, opts...)
	h, err := gliwHello(st, s.HelloMsg(n))
	if err != nil {
		t.Fatalf("hello of %s failed: %v", n.Name, err)
	}
	if h.GetSession() <= 0 {
		t.Fatalf("ServerHello session = %d, want > 0", h.GetSession())
	}
	return st, closeFn, h
}

func gliwSnapshotMsg(f *ingestpb.SnapshotFrame) *ingestpb.ClientFrame {
	return &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Snapshot{Snapshot: f}}
}

// gliwSend sends one SnapshotFrame; a closed stream (io.EOF) is not an error here,
// the status is read by the following Recv.
func gliwSend(st ingestpb.Ingest_StreamClient, f *ingestpb.SnapshotFrame) error {
	if err := st.Send(gliwSnapshotMsg(f)); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// gliwRecvAck waits for the next server message and requires it to be an ack.
func gliwRecvAck(st ingestpb.Ingest_StreamClient) (*ingestpb.SnapshotAck, error) {
	fr, err := st.Recv()
	if err != nil {
		return nil, err
	}
	if fr.GetAck() == nil {
		return nil, errors.New("gliw: server message is not a SnapshotAck")
	}
	return fr.GetAck(), nil
}

// gliwExchange sends f and returns the ack, failing the test on any stream error.
func gliwExchange(t *testing.T, st ingestpb.Ingest_StreamClient, f *ingestpb.SnapshotFrame) *ingestpb.SnapshotAck {
	t.Helper()
	if err := gliwSend(st, f); err != nil {
		t.Fatalf("send frame: %v", err)
	}
	ack, err := gliwRecvAck(st)
	if err != nil {
		t.Fatalf("no ack for sequence %d: %v", f.GetSequence(), err)
	}
	return ack
}

// gliwFrame builds a valid complete frame for the node with the independent
// payload/digest builders shared by the test files (gliPayload, gliFrame). The envelope
// identity is set explicitly; neither field is covered by the digest (GLI-061).
func gliwFrame(t *testing.T, n gliwNode, session int64, seq uint64) *ingestpb.SnapshotFrame {
	t.Helper()
	at := gliwAt(seq)
	f := gliFrame(t, gliPayload(n.UID, n.Boot, session, seq, at), session, seq, at, true)
	f.NodeUid = n.UID
	f.BootId = n.Boot
	return f
}

// Baseline sends the complete sequence 0 of the session and requires ACCEPTED.
func (s *gliServer) Baseline(t *testing.T, st ingestpb.Ingest_StreamClient, n gliwNode, session int64) {
	t.Helper()
	ack := gliwExchange(t, st, gliwFrame(t, n, session, 0))
	if ack.GetCode() != ingestpb.AckCode_ACCEPTED {
		t.Fatalf("baseline of session %d: ack %v, want ACCEPTED", session, ack.GetCode())
	}
}

// gliwTimestamp is timestamppb.New for the common case.
func gliwTimestamp(tm time.Time) *timestamppb.Timestamp { return timestamppb.New(tm) }

// --- statuses ---------------------------------------------------------------------------

// gliwCode is the gRPC code of a stream error; io.EOF (clean end) is OK.
func gliwCode(err error) codes.Code {
	if err == nil || errors.Is(err, io.EOF) {
		return codes.OK
	}
	return status.Code(err)
}

func gliwMessage(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return ""
	}
	return status.Convert(err).Message()
}

// gliwHygiene checks the GLI-102 vocabulary rules of a handler-made message:
// at most 1 KiB, printable ASCII, no forbidden marker, no error-wrapping prefix.
func gliwHygiene(t *testing.T, what, msg string, forbidden ...string) {
	t.Helper()
	if len(msg) > 1024 {
		t.Errorf("%s: message is %d bytes, want <= 1024", what, len(msg))
	}
	for i := 0; i < len(msg); i++ {
		if msg[i] < 0x20 || msg[i] > 0x7e {
			t.Errorf("%s: message has byte 0x%02x outside 0x20-0x7E", what, msg[i])
			break
		}
	}
	for _, f := range forbidden {
		if f != "" && strings.Contains(msg, f) {
			t.Errorf("%s: message contains the forbidden string %q", what, f)
		}
	}
	if strings.Contains(msg, "rpc error") || strings.Contains(msg, "code = ") {
		t.Errorf("%s: message carries a wrapped error prefix: %q", what, msg)
	}
}

// gliwWantCode requires err to carry code want and a clean handler message.
func gliwWantCode(t *testing.T, what string, err error, want codes.Code, forbidden ...string) {
	t.Helper()
	if got := gliwCode(err); got != want {
		t.Fatalf("%s: status %v (%v), want %v", what, got, err, want)
	}
	gliwHygiene(t, what, gliwMessage(err), forbidden...)
}

// gliwExpectEnd requires the next server message to be the end of the stream
// (no ack) with the given code.
func gliwExpectEnd(t *testing.T, what string, st ingestpb.Ingest_StreamClient, want codes.Code, forbidden ...string) {
	t.Helper()
	fr, err := st.Recv()
	if err == nil {
		t.Fatalf("%s: got a server message (%v), want the stream to end with %v and no ack", what, fr, want)
	}
	gliwWantCode(t, what, err, want, forbidden...)
}

// gliwExpectAckThenEnd sends f, requires an ack with code ackWant and then the end
// of the stream with endWant.
func gliwExpectAckThenEnd(t *testing.T, what string, st ingestpb.Ingest_StreamClient, f *ingestpb.SnapshotFrame, ackWant ingestpb.AckCode, endWant codes.Code, forbidden ...string) *ingestpb.SnapshotAck {
	t.Helper()
	ack := gliwExchange(t, st, f)
	if ack.GetCode() != ackWant {
		t.Fatalf("%s: ack %v, want %v", what, ack.GetCode(), ackWant)
	}
	gliwHygiene(t, what+" ack", ack.GetMessage(), forbidden...)
	gliwExpectEnd(t, what+" end", st, endWant, forbidden...)
	return ack
}

// --- observing node state through the bundle source -------------------------------------

func gliwPolicy() fleet.Policy {
	return fleet.Policy{
		Revision: "gliw-policy-1",
		RequiredCoverage: []fleet.CoverageRequirement{
			{Name: "parent", PathKind: "gpu-pcie-parent", Required: true},
		},
		Freshness: 60 * time.Second,
		ReadyFor:  60 * time.Second,
	}
}

func gliwIntent(node fleet.NodeRef, at time.Time) fleet.Intent {
	return fleet.Intent{
		Device:             fleet.DeviceRef{Name: "gliw-gpu-0", UID: "gliw-device-uid-0"},
		Node:               node,
		Desired:            fleet.DesiredInService,
		RequestID:          "gliw-request-1",
		MetadataGeneration: 1,
		ObservedAt:         at,
		Claim: fleet.InventoryClaim{
			Vendor: "NVIDIA", UUID: "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
			Source: "spec", EvidenceID: "gliw-claim-1",
		},
	}
}

// Query is a valid LiveBundleQuery for the node at the server's Clock. With
// intent=false the query has no intents, so a successful call returns an empty
// set and the call only distinguishes "observed" from "no observation".
func (s *gliServer) Query(n gliwNode, intent bool) app.LiveBundleQuery {
	now := s.Clock.Now()
	q := app.LiveBundleQuery{
		Node:   fleet.NodeRef{ClusterID: s.Config.ClusterID, Name: n.Name, UID: n.UID},
		Policy: gliwPolicy(),
		Now:    now,
	}
	if intent {
		q.Intents = []fleet.Intent{gliwIntent(q.Node, now)}
	}
	return q
}

// Observed calls NodeBundles without intents: nil when the node has an Active
// observation, an error satisfying app.ErrNoObservation when it has none. Any
// other error means the test fixture (policy) was rejected and fails the test.
func (s *gliServer) Observed(t *testing.T, n gliwNode) error {
	t.Helper()
	set, err := s.Server.BundleSource().NodeBundles(context.Background(), s.Query(n, false))
	if err != nil {
		if !errors.Is(err, app.ErrNoObservation) {
			t.Fatalf("NodeBundles(no intents) failed with %v; the query fixture is invalid or the source is broken", err)
		}
		if len(set.Devices) != 0 {
			t.Fatalf("NodeBundles returned %d devices together with ErrNoObservation", len(set.Devices))
		}
		return err
	}
	if len(set.Devices) != 0 {
		t.Fatalf("NodeBundles(no intents) returned %d devices, want 0", len(set.Devices))
	}
	return nil
}

// WantObserved requires an Active observation.
func (s *gliServer) WantObserved(t *testing.T, what string, n gliwNode) {
	t.Helper()
	if err := s.Observed(t, n); err != nil {
		t.Fatalf("%s: NodeBundles = %v, want an Active observation", what, err)
	}
}

// WantNoObservation requires app.ErrNoObservation.
func (s *gliServer) WantNoObservation(t *testing.T, what string, n gliwNode) {
	t.Helper()
	if err := s.Observed(t, n); err == nil {
		t.Fatalf("%s: NodeBundles returned an observation, want app.ErrNoObservation", what)
	}
}

// Snapshot returns the envelope of the node's last accepted frame as the bundle
// source reports it (one intent), or the NodeBundles error.
func (s *gliServer) Snapshot(t *testing.T, n gliwNode) (fleet.SnapshotEnvelope, error) {
	t.Helper()
	set, err := s.Server.BundleSource().NodeBundles(context.Background(), s.Query(n, true))
	if err != nil {
		if errors.Is(err, app.ErrNoObservation) {
			return fleet.SnapshotEnvelope{}, err
		}
		t.Fatalf("NodeBundles(one intent) failed with %v; the intent fixture is invalid or the source is broken", err)
	}
	if len(set.Devices) != 1 {
		t.Fatalf("NodeBundles(one intent) returned %d devices, want 1", len(set.Devices))
	}
	return set.Devices[0].Bundle.Snapshot, nil
}

// WantSnapshot requires the last accepted frame to be (session, sequence).
func (s *gliServer) WantSnapshot(t *testing.T, what string, n gliwNode, session int64, seq uint64) {
	t.Helper()
	env, err := s.Snapshot(t, n)
	if err != nil {
		t.Fatalf("%s: NodeBundles = %v, want an observation at session %d sequence %d", what, err, session, seq)
	}
	if env.Session != session || env.Sequence != seq {
		t.Fatalf("%s: last accepted frame is session %d sequence %d, want session %d sequence %d",
			what, env.Session, env.Sequence, session, seq)
	}
}

// --- small utilities ---------------------------------------------------------------------

// gliwEventually polls cond every 50 ms for at most d (GLI-125: <= 20 s).
func gliwEventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v: %s", d, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// gliwLimits sets the Limits of a configuration.
func gliwLimits(l ingest.Limits) func(*ingest.Config) {
	return func(c *ingest.Config) { c.Limits = l }
}
