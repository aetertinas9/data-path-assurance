package liveingest_test

// Harness for frame-level tests: a server wrapper over the shared helper
// gliStartServer, authenticated connections with a hello,
// the GLI-041 ack oracle (fleet.AdmitSnapshot on the independent digest) and
// NodeBundles observation helpers. Top-level identifiers use the prefix gliFx.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const (
	// gliFxStep is the fake-clock advance before every hello and frame. The
	// bucket of GLI-052 refills one token per 10 s, so advancing 10 s before
	// each frame (and each hello) keeps both buckets full (GLI-127).
	gliFxStep = 10 * time.Second
	// gliFxWait bounds every blocking receive (at least 2 s under -race, GLI-125).
	gliFxWait = 15 * time.Second

	gliFxOtherUID  = "11111111-2222-4333-8444-555555555555"
	gliFxOtherName = "gpu-node-2"
	gliFxDevName   = "gpu-node-1-gpu0"
	gliFxDevUID    = "0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41"

	// gliFxFresh is the policy freshness of the default query.
	gliFxFresh = 60 * time.Second
)

// ---------------------------------------------------------------------------
// Server wrapper.
// ---------------------------------------------------------------------------

type gliFxEnv struct {
	t *testing.T
	S *gliServer
}

// gliFxStart starts a server with explicit defaults (cluster, profile and
// trusted sources) and then the test's own mods; it assumes that mods run in
// order on the Config before NewServer. The default node is registered.
func gliFxStart(t *testing.T, mod ...func(*ingest.Config)) *gliFxEnv {
	t.Helper()
	base := func(c *ingest.Config) {
		c.ClusterID = gliFxCluster
		c.CollectorProfileID = gliFxProfile
		c.TrustedSources = ingest.DefaultTrustedSources()
	}
	s := gliStartServer(t, append([]func(*ingest.Config){base}, mod...)...)
	s.Nodes.Put(gliFxNodeName, gliFxNodeUID)
	return &gliFxEnv{t: t, S: s}
}

// For returns a view of the same server bound to the t of a subtest, so that
// helpers fail the subtest (not the parent) and streams close with the subtest.
func (e *gliFxEnv) For(t *testing.T) *gliFxEnv { return &gliFxEnv{t: t, S: e.S} }

// ---------------------------------------------------------------------------
// ACK oracle (GLI-041).
// ---------------------------------------------------------------------------

// gliFxAckCode maps the result of fleet.AdmitSnapshot to the ack code (GLI-041).
func gliFxAckCode(order fleet.SnapshotOrder, err error) ingestpb.AckCode {
	switch {
	case errors.Is(err, fleet.ErrInvalidInput):
		return ingestpb.AckCode_INVALID
	case order == fleet.SnapshotConflict && errors.Is(err, fleet.ErrConflict):
		return ingestpb.AckCode_CONFLICT
	case err != nil:
		return ingestpb.AckCode_ACK_CODE_UNSPECIFIED
	}
	switch order {
	case fleet.SnapshotAccepted:
		return ingestpb.AckCode_ACCEPTED
	case fleet.SnapshotDuplicate:
		return ingestpb.AckCode_DUPLICATE
	case fleet.SnapshotOutOfOrder:
		return ingestpb.AckCode_OUT_OF_ORDER
	case fleet.SnapshotGap:
		return ingestpb.AckCode_GAP
	case fleet.SnapshotWrongSession:
		return ingestpb.AckCode_WRONG_SESSION
	case fleet.SnapshotConflict:
		return ingestpb.AckCode_CONFLICT
	}
	return ingestpb.AckCode_ACK_CODE_UNSPECIFIED
}

// gliFxOracle replays the admission chain of one stream with fleet.AdmitSnapshot
// (GLI-041: the oracle is a direct call on the independent digest and revision).
type gliFxOracle struct {
	session int64
	prev    *fleet.SnapshotCursor
}

// Admit classifies f and advances the chain when f is accepted. It returns the
// expected code and the expected expected_next_sequence.
func (o *gliFxOracle) Admit(f *ingestpb.SnapshotFrame) (ingestpb.AckCode, uint64) {
	d := gliDigest(f.GetPayload())
	dh := hex.EncodeToString(d[:])
	comp := fleet.CompletenessComplete
	if f.GetCompleteness() == ingestpb.Completeness_PARTIAL {
		comp = fleet.CompletenessPartial
	}
	env := fleet.SnapshotEnvelope{
		NodeUID: f.GetNodeUid(), BootID: f.GetBootId(), PayloadDigest: dh,
		BundleRevision: fmt.Sprintf("%d:%d:%s", f.GetSession(), f.GetSequence(), dh),
		Session:        f.GetSession(), Sequence: f.GetSequence(), Completeness: comp, ObservedAt: f.GetObservedAt().AsTime(),
	}
	cur, order, err := fleet.AdmitSnapshot(o.session, o.prev, env)
	code := gliFxAckCode(order, err)
	if code == ingestpb.AckCode_ACCEPTED {
		c := cur
		o.prev = &c
	}
	return code, o.next()
}

// next is the expected_next_sequence of the chain state (GLI-041).
func (o *gliFxOracle) next() uint64 {
	if o.prev == nil {
		return 0
	}
	return o.prev.Sequence + 1
}

// ---------------------------------------------------------------------------
// Connections.
// ---------------------------------------------------------------------------

// gliFxConn is an authenticated stream with a successful hello.
type gliFxConn struct {
	t       *testing.T
	env     *gliFxEnv
	st      ingestpb.Ingest_StreamClient
	Name    string
	UID     string
	Boot    string
	Hello   *ingestpb.ServerHello
	Session int64
	oracle  gliFxOracle
}

// Connect registers the node, opens a stream with a node certificate of uid
// and performs the hello. It fails the test when the hello fails.
func (e *gliFxEnv) Connect(name, uid, boot string) *gliFxConn {
	e.t.Helper()
	c, err := e.TryConnect(name, uid, boot)
	if err != nil {
		e.t.Fatalf("hello of %s failed: %v", name, err)
	}
	return c
}

// TryConnect is Connect returning the hello error.
func (e *gliFxEnv) TryConnect(name, uid, boot string) (*gliFxConn, error) {
	e.t.Helper()
	e.S.Nodes.Put(name, uid)
	e.S.Clock.Advance(gliFxStep)
	cert := e.S.PKI.NodeCert(e.t, gliFxCluster, uid)
	st, stop := e.S.Stream(e.t, cert)
	var once sync.Once
	e.t.Cleanup(func() { once.Do(stop) })
	hello, err := e.S.Hello(e.t, st, gliFxCluster, name, uid, boot)
	if err != nil {
		return nil, err
	}
	return &gliFxConn{
		t: e.t, env: e, st: st, Name: name, UID: uid, Boot: boot, Hello: hello, Session: hello.GetSession(),
		oracle: gliFxOracle{session: hello.GetSession()},
	}, nil
}

// ConnectDefault connects the default node (gliFxNodeName, gliFxNodeUID).
func (e *gliFxEnv) ConnectDefault() *gliFxConn {
	e.t.Helper()
	return e.Connect(gliFxNodeName, gliFxNodeUID, gliFxBootID)
}

func (c *gliFxConn) recv(d time.Duration) (*ingestpb.ServerFrame, error) {
	c.t.Helper()
	type res struct {
		f   *ingestpb.ServerFrame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		f, err := c.st.Recv()
		ch <- res{f, err}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-time.After(d):
		c.t.Fatalf("no server message within %s (stream neither acked nor ended)", d)
	}
	return nil, nil
}

// SendClient sends a raw ClientFrame after advancing the fake clock.
func (c *gliFxConn) SendClient(cf *ingestpb.ClientFrame) {
	c.t.Helper()
	c.env.S.Clock.Advance(gliFxStep)
	if err := c.st.Send(cf); err != nil {
		c.t.Fatalf("Send: %v", err)
	}
}

func gliFxSnapshotFrame(f *ingestpb.SnapshotFrame) *ingestpb.ClientFrame {
	return &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Snapshot{Snapshot: f}}
}

// Send sends a SnapshotFrame.
func (c *gliFxConn) Send(f *ingestpb.SnapshotFrame) {
	c.t.Helper()
	c.SendClient(gliFxSnapshotFrame(f))
}

// WantAck receives the next server message and requires it to be an ack.
func (c *gliFxConn) WantAck() *ingestpb.SnapshotAck {
	c.t.Helper()
	sf, err := c.recv(gliFxWait)
	if err != nil {
		c.t.Fatalf("expected an ack but the stream ended: %v", err)
	}
	a := sf.GetAck()
	if a == nil {
		c.t.Fatalf("expected an ack, got %T", sf.GetFrame())
	}
	return a
}

// Ack sends f and returns the ack.
func (c *gliFxConn) Ack(f *ingestpb.SnapshotFrame) *ingestpb.SnapshotAck {
	c.t.Helper()
	c.Send(f)
	return c.WantAck()
}

// End waits for the end of the stream and returns its gRPC code and message
// (OK for a clean end). A further server message is a failure.
func (c *gliFxConn) End() (codes.Code, string) {
	c.t.Helper()
	sf, err := c.recv(gliFxWait)
	if err == nil {
		c.t.Fatalf("expected the end of the stream, got server message %T", sf.GetFrame())
	}
	if errors.Is(err, io.EOF) {
		return codes.OK, ""
	}
	st, _ := status.FromError(err)
	return st.Code(), st.Message()
}

// WantEnd requires the stream to end with code want.
func (c *gliFxConn) WantEnd(want codes.Code, clause string) {
	c.t.Helper()
	got, msg := c.End()
	if got != want {
		c.t.Errorf("%s: stream ended with %s (%q), want %s", clause, got, msg, want)
	}
	gliFxCheckMessage(c.t, msg, clause+" status message")
}

// Frame builds a valid BASE frame of this session.
func (c *gliFxConn) Frame(seq uint64, at time.Time, complete bool) *ingestpb.SnapshotFrame {
	c.t.Helper()
	p := gliPayload(c.UID, c.Boot, c.Session, seq, at)
	return gliFrame(c.t, p, c.Session, seq, at, complete)
}

// Classified sends f, asserts the ack the GLI-041 oracle predicts (code,
// accepted_session, expected_next_sequence) and the GLI-044 stream outcome.
func (c *gliFxConn) Classified(f *ingestpb.SnapshotFrame, clause string) ingestpb.AckCode {
	c.t.Helper()
	want, next := c.oracle.Admit(f)
	a := c.Ack(f)
	gliFxWantAck(c.t, a, want, c.Session, next, clause)
	switch want {
	case ingestpb.AckCode_ACCEPTED, ingestpb.AckCode_DUPLICATE:
	case ingestpb.AckCode_INVALID:
		c.WantEnd(codes.InvalidArgument, clause)
	default:
		c.WantEnd(codes.FailedPrecondition, clause)
	}
	return want
}

// AcceptSeq sends the valid BASE frame of seq and requires ACCEPTED.
func (c *gliFxConn) AcceptSeq(seq uint64, at time.Time, complete bool) *ingestpb.SnapshotFrame {
	c.t.Helper()
	f := c.Frame(seq, at, complete)
	if got := c.Classified(f, fmt.Sprintf("setup sequence %d", seq)); got != ingestpb.AckCode_ACCEPTED {
		c.t.Fatalf("setup sequence %d: oracle says %s, want ACCEPTED", seq, got)
	}
	return f
}

// Reject sends f, requires the given ack code with accepted_session and
// expected_next_sequence of the chain, and the given stream status.
func (c *gliFxConn) Reject(f *ingestpb.SnapshotFrame, want ingestpb.AckCode, stream codes.Code, clause string) {
	c.t.Helper()
	a := c.Ack(f)
	gliFxWantAck(c.t, a, want, c.Session, c.oracle.next(), clause)
	c.WantEnd(stream, clause)
}

// gliFxCheckMessage requires a fixed-vocabulary message: ASCII 0x20-0x7E,
// at most 1 KiB (GLI-041, GLI-102).
func gliFxCheckMessage(t *testing.T, msg, clause string) {
	t.Helper()
	if len(msg) > 1024 {
		t.Errorf("%s: message is %d bytes, want <= 1024 (GLI-102)", clause, len(msg))
	}
	for i := 0; i < len(msg); i++ {
		if msg[i] < 0x20 || msg[i] > 0x7e {
			t.Errorf("%s: message byte %d is 0x%02x, want ASCII 0x20-0x7E (GLI-102)", clause, i, msg[i])
			return
		}
	}
}

// gliFxWantAck asserts code, accepted_session, expected_next_sequence and the
// message vocabulary of an ack (GLI-041).
func gliFxWantAck(t *testing.T, a *ingestpb.SnapshotAck, code ingestpb.AckCode, session int64, next uint64, clause string) {
	t.Helper()
	if a.GetCode() != code {
		t.Errorf("%s: ack code %s, want %s", clause, a.GetCode(), code)
	}
	if a.GetAcceptedSession() != session {
		t.Errorf("%s: accepted_session %d, want the stream session %d", clause, a.GetAcceptedSession(), session)
	}
	if a.GetExpectedNextSequence() != next {
		t.Errorf("%s: expected_next_sequence %d, want %d", clause, a.GetExpectedNextSequence(), next)
	}
	gliFxCheckMessage(t, a.GetMessage(), clause+" ack")
}

// ---------------------------------------------------------------------------
// NodeBundles observation.
// ---------------------------------------------------------------------------

func gliFxPolicy(t *testing.T, freshness time.Duration) fleet.Policy {
	t.Helper()
	p, err := fleet.NewPolicy(fleet.Policy{
		Revision: "gli-policy-1",
		RequiredCoverage: []fleet.CoverageRequirement{
			{Name: "pcie-parent", PathKind: "gpu-pcie-parent", Required: true},
			{Name: "pcie-root", PathKind: "gpu-pcie-root", Required: true},
			{Name: "pcie-width", PathKind: "gpu-pcie-link-width-normal", Required: true},
			{Name: "nic-lldp", PathKind: "nic-lldp-remote", Required: false},
		},
		Freshness: freshness, ReadyFor: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("fleet.NewPolicy: %v", err)
	}
	return p
}

func gliFxIntent(t *testing.T, name, uid string) fleet.Intent {
	t.Helper()
	in := fleet.Intent{
		Device:  fleet.DeviceRef{Name: name, UID: uid},
		Node:    fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID},
		Desired: fleet.DesiredInService, RequestID: "enroll-1", MetadataGeneration: 1, ObservedAt: gliFxT0,
		Claim: fleet.InventoryClaim{Vendor: "NVIDIA", UUID: gliFxUUID, Source: "operator/asset-db", EvidenceID: "asset-db:rack7-u12-gpu0"},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("Intent.Validate: %v", err)
	}
	return in
}

func gliFxDefaultIntent(t *testing.T) fleet.Intent { return gliFxIntent(t, gliFxDevName, gliFxDevUID) }

// Query calls BundleSource().NodeBundles for the default node.
func (e *gliFxEnv) Query(now time.Time, pol fleet.Policy, intents ...fleet.Intent) (app.LiveBundleSet, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return e.S.Server.BundleSource().NodeBundles(ctx, app.LiveBundleQuery{
		Node:   fleet.NodeRef{ClusterID: gliFxCluster, Name: gliFxNodeName, UID: gliFxNodeUID},
		Policy: pol, Intents: intents, Now: now,
	})
}

// One queries the default device and returns its bundle; it fails the test on
// any error.
func (e *gliFxEnv) One(now time.Time, freshness time.Duration) app.LiveDeviceBundle {
	e.t.Helper()
	set, err := e.Query(now, gliFxPolicy(e.t, freshness), gliFxDefaultIntent(e.t))
	if err != nil {
		e.t.Fatalf("NodeBundles: %v", err)
	}
	if len(set.Devices) != 1 {
		e.t.Fatalf("NodeBundles returned %d devices for one intent, want 1", len(set.Devices))
	}
	return set.Devices[0]
}

// WantNoObservation requires app.ErrNoObservation and an empty set (GLI-075 (b)).
func (e *gliFxEnv) WantNoObservation(clause string) {
	e.t.Helper()
	set, err := e.Query(gliFxT0.Add(time.Minute), gliFxPolicy(e.t, gliFxFresh), gliFxDefaultIntent(e.t))
	if !errors.Is(err, app.ErrNoObservation) {
		e.t.Errorf("%s: NodeBundles error = %v, want app.ErrNoObservation", clause, err)
	}
	if len(set.Devices) != 0 {
		e.t.Errorf("%s: NodeBundles returned %d devices with ErrNoObservation, want the empty set", clause, len(set.Devices))
	}
}

// SigOf is Sig for another node of the same server (same device and policy).
func (e *gliFxEnv) SigOf(name, uid string) string {
	e.t.Helper()
	in := gliFxDefaultIntent(e.t)
	in.Node = fleet.NodeRef{ClusterID: gliFxCluster, Name: name, UID: uid}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	set, err := e.S.Server.BundleSource().NodeBundles(ctx, app.LiveBundleQuery{
		Node: in.Node, Policy: gliFxPolicy(e.t, gliFxFresh), Intents: []fleet.Intent{in}, Now: gliFxT0.Add(48 * time.Hour),
	})
	if errors.Is(err, app.ErrNoObservation) {
		return "no observation"
	}
	if err != nil {
		e.t.Fatalf("NodeBundles(%s): %v", name, err)
	}
	return gliFxSig(set)
}

// gliFxSig renders a bundle set so that two observations of the same node
// state compare equal and any state change shows up (GLI-045 atomicity).
func gliFxSig(set app.LiveBundleSet) string {
	var b strings.Builder
	for _, d := range set.Devices {
		bd := d.Bundle
		fmt.Fprintf(&b, "device=%s\npolicy=%s\nintent=%s\nsnapshot=%s\nadmitted=%s\n", d.DeviceUID, gliOrPolicyString(bd.Policy),
			gliOrIntentString(bd.Intent), gliOrEnvString(bd.Snapshot), gliOrCursorString(bd.Admitted))
		fmt.Fprintf(&b, "graph=%s window=%s topology=%s baseline=%s\n", bd.GraphRevision, bd.WindowRevision, bd.TopologyDigest, bd.BaselineDigest)
		fmt.Fprintf(&b, "prov=%v\nbind=%v\n", gliOrProvStrings(bd.Provenance), gliOrBindStrings(bd.Bindings))
		fmt.Fprintf(&b, "findings=%v at=%s rev=%s\ntrust=%s\n", gliOrFindingSigs(bd.Findings), gliOrT(bd.FindingsEvaluatedAt),
			bd.FindingsGraphRevision, gliOrTrustString(bd.CollectorTrust))
		fmt.Fprintf(&b, "records=%v %v\nalloc=%t fence=%t fenceTrust=%t topology=%t\nwindowsig=\n%s", d.Findings, d.Evidence,
			bd.Allocation != nil, bd.Fence != nil, bd.FenceTrust != nil, bd.Topology != nil, gliOrWindowSig(bd.Window))
	}
	return b.String()
}

// Sig is the signature of the default device bundle at a fixed late query time,
// or "no observation".
func (e *gliFxEnv) Sig() string {
	e.t.Helper()
	set, err := e.Query(gliFxT0.Add(48*time.Hour), gliFxPolicy(e.t, gliFxFresh), gliFxDefaultIntent(e.t))
	if errors.Is(err, app.ErrNoObservation) {
		return "no observation"
	}
	if err != nil {
		e.t.Fatalf("NodeBundles: %v", err)
	}
	return gliFxSig(set)
}

// WantSigUnchanged requires the node state signature to equal before.
func (e *gliFxEnv) WantSigUnchanged(before, clause string) {
	e.t.Helper()
	if after := e.Sig(); after != before {
		e.t.Errorf("%s: node observation state changed (GLI-045)\nbefore:\n%s\nafter:\n%s", clause, before, after)
	}
}

// gliFxCloneFrame returns a deep copy of a frame (the payload is copied too).
func gliFxCloneFrame(f *ingestpb.SnapshotFrame) *ingestpb.SnapshotFrame {
	c := proto.Clone(f).(*ingestpb.SnapshotFrame)
	if v, ok := gliFxIdent.Load(f.GetPayload()); ok {
		gliFxIdent.Store(c.GetPayload(), v)
	}
	return c
}
