package liveingest_test

// Acceptance scenarios of the wire tests (GLI-140: L-SPOOF-NODE, L-SIZE, L-LEADER,
// L-CODEC) and the server-side secret-leak scenario of GLI-101 / GLI-102.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcgzip "google.golang.org/grpc/encoding/gzip"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// L-SPOOF-NODE: the certificate of node A cannot write the state of node B.
func TestGLI140_L_SPOOF_NODE(t *testing.T) {
	s := gliStartServer(t)
	a, b := s.AddNode(t, "spoofa"), s.AddNode(t, "spoofb")
	bst, _, bh := s.Connect(t, b)
	s.Baseline(t, bst, b, bh.GetSession())

	t.Run("envelope node_uid names node B", func(t *testing.T) {
		st, _, h := s.Connect(t, a)
		f := gliwFrame(t, b, h.GetSession(), 0) // payload and envelope are node B's
		f.BootId = a.Boot                       // only the node identity is wrong
		gliwExpectAckThenEnd(t, "spoofed envelope", st, f, ingestpb.AckCode_INVALID, codes.PermissionDenied, b.UID)
		s.WantNoObservation(t, "node A", a)
		s.WantSnapshot(t, "node B", b, bh.GetSession(), 0)
	})
	t.Run("payload KubernetesNode asset names node B", func(t *testing.T) {
		s.Clock.Advance(10 * time.Second)
		st, _, h := s.Connect(t, a)
		f := gliwFrame(t, b, h.GetSession(), 0) // payload built for node B
		f.NodeUid, f.BootId = a.UID, a.Boot     // envelope claims node A
		gliwExpectAckThenEnd(t, "spoofed payload", st, f, ingestpb.AckCode_INVALID, codes.InvalidArgument, b.UID)
		s.WantNoObservation(t, "node A", a)
		s.WantSnapshot(t, "node B", b, bh.GetSession(), 0)
	})
	t.Run("hello with node B's name and uid", func(t *testing.T) {
		s.Clock.Advance(10 * time.Second)
		st, closeFn := s.Stream(t, a.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(b))
		gliwWantCode(t, "hello as B", err, codes.PermissionDenied, b.UID, b.Name)
	})
	t.Run("hello with node B's name and A's uid", func(t *testing.T) {
		s.Clock.Advance(10 * time.Second)
		st, closeFn := s.Stream(t, a.Cert)
		defer closeFn()
		h := s.HelloMsg(a)
		h.NodeName = b.Name
		_, err := gliwHello(st, h)
		gliwWantCode(t, "hello with B's name", err, codes.PermissionDenied, b.UID, b.Name)
	})
	s.WantSnapshot(t, "node B at the end", b, bh.GetSession(), 0)
	// Node B's own stream was never touched by A's attempts and keeps working.
	s.Clock.Advance(10 * time.Second)
	if ack := gliwExchange(t, bst, gliwFrame(t, b, bh.GetSession(), 1)); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
		t.Errorf("node B's own stream: ack %v, want ACCEPTED", ack.GetCode())
	}
}

// L-SIZE: 4 MiB + 1 byte of an identity message is ResourceExhausted without ack
// and without state change, exactly 4 MiB is not refused for its size, and 16 MiB
// + 1 byte after decompression (small on the wire) is ResourceExhausted.
func TestGLI140_L_SIZE(t *testing.T) {
	s := gliStartServer(t)
	run := func(t *testing.T, label string, total int, compress bool, wantRE bool) {
		t.Helper()
		n := s.AddNode(t, label)
		var opts []grpc.CallOption
		if compress {
			opts = append(opts, grpc.UseCompressor(grpcgzip.Name))
		}
		st, closeFn, h := s.Connect(t, n, opts...)
		defer closeFn()
		s.Baseline(t, st, n, h.GetSession())
		s.Clock.Advance(10 * time.Second)
		_ = st.Send(gliwBigRevisionFrame(t, n, h.GetSession(), 1, total))
		fr, err := st.Recv()
		if wantRE {
			if err != nil && gliwCode(err) == codes.ResourceExhausted {
				s.WantSnapshot(t, "state after the oversize message", n, h.GetSession(), 0)
				return
			}
			t.Fatalf("%s: got %v / %v, want ResourceExhausted and no ack", label, fr, err)
		}
		if err != nil || fr.GetAck().GetCode() != ingestpb.AckCode_INVALID {
			t.Fatalf("%s: got %v / %v, want an ack INVALID (not refused for size)", label, fr, err)
		}
	}
	run(t, "lsize1", gliwMaxRecvBytes+1, false, true)
	run(t, "lsize2", gliwMaxRecvBytes, false, false)
	run(t, "lsize3", gliwMaxInflate+1, true, true)
}

// L-LEADER: a replica that is not the leader refuses hellos and leaves the
// session store alone, even though a leader replica shares the same store.
func TestGLI140_L_LEADER(t *testing.T) {
	shared := gliNewSessions()
	useShared := func(c *ingest.Config) { c.Sessions = shared }
	one := gliStartServer(t, useShared)
	two := gliStartServer(t, useShared)
	n := one.AddNode(t, "leader")
	n2 := n
	n2.Cert = two.PKI.NodeCert(t, gliwCluster, n.UID)
	two.Nodes.Put(n.Name, n.UID)

	two.Leader.Set(false)
	st, closeFn := two.Stream(t, n2.Cert)
	_, err := gliwHello(st, two.HelloMsg(n2))
	gliwWantCode(t, "hello on the non-leader", err, codes.Unavailable)
	closeFn()
	if c := shared.Calls(); len(c) != 0 {
		t.Fatalf("the non-leader touched the shared store: %+v", c)
	}
	_, _, h1 := one.Connect(t, n)
	if h1.GetSession() != 1 {
		t.Fatalf("leader session = %d, want 1", h1.GetSession())
	}

	// Failover: the roles swap.
	one.Leader.Set(false)
	two.Leader.Set(true)
	st, closeFn = one.Stream(t, n.Cert)
	_, err = gliwHello(st, one.HelloMsg(n))
	gliwWantCode(t, "hello on the former leader", err, codes.Unavailable)
	closeFn()
	_, _, h2 := two.Connect(t, n2)
	if h2.GetSession() <= h1.GetSession() {
		t.Fatalf("session after failover %d, want greater than %d", h2.GetSession(), h1.GetSession())
	}
	if c := shared.Calls(); len(c) != 2 {
		t.Fatalf("shared store calls = %+v, want exactly the two leader hellos", c)
	}
}

// L-CODEC: undecodable bytes end the stream without an ack and without effect and
// the node may say hello again; a valid-UTF-8 non-ASCII string is answered with an
// ack INVALID.
func TestGLI140_L_CODEC(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "lcodec")
	st, _, h := s.Connect(t, n, grpc.ForceCodecV2(gliwRawCodec{}))
	session := h.GetSession()
	s.Baseline(t, st, n, session)
	_ = st.SendMsg(gliwRawSnapshotString(9, []byte{'r', 0xff, 0xfe}))
	fr, err := st.Recv()
	if err == nil {
		t.Fatalf("got a server message (%v), want the stream to end without an ack", fr)
	}
	if gliwCode(err) == codes.OK {
		t.Fatal("the stream ended with OK")
	}
	s.WantSnapshot(t, "after the invalid UTF-8 message", n, session, 0)

	s.Clock.Advance(10 * time.Second)
	st2, _, h2 := s.Connect(t, n)
	if h2.GetSession() <= session {
		t.Fatalf("session %d after %d", h2.GetSession(), session)
	}
	f := gliwFrame(t, n, h2.GetSession(), 0)
	f.BundleRevision = "é"
	ack := gliwExchange(t, st2, f)
	if ack.GetCode() != ingestpb.AckCode_INVALID {
		t.Fatalf("non-ASCII UTF-8 string: ack %v, want INVALID", ack.GetCode())
	}
	gliwExpectEnd(t, "after INVALID", st2, codes.InvalidArgument)
}

// TestGLI101_NoMarkerInStatusAckOrLogs sends unique marker strings through every
// server-side channel named by GLI-101 (hello fields, store and directory error
// bodies, frame and payload strings, file paths) and requires that none of them
// appears in any gRPC status message, ack message or slog record.
func TestGLI101_NoMarkerInStatusAckOrLogs(t *testing.T) {
	const marker = "GLIWMARK-leak"
	s := gliStartServer(t)
	n := s.AddNode(t, "leak")
	forbidden := []string{marker, "GLIWMARK", s.PKI.Marker()}
	step := func() { s.Clock.Advance(10 * time.Second) }

	// (iii) hello fields, (iv) store and directory error bodies
	step()
	{
		st, closeFn := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.Version, h.NodeName, h.BootId, h.NodeUid = marker+"-v", marker+"-name x", marker+"-bé", marker+"-u x"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "marker hello", err, codes.InvalidArgument, forbidden...)
		closeFn()
	}
	step()
	{
		s.Sessions.SetErr(fmt.Errorf("%s: %w", marker+"-store-body", liveingest.ErrStoreUnavailable))
		st, closeFn := s.Stream(t, n.Cert)
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "store error body", err, codes.Unavailable, forbidden...)
		closeFn()
		s.Sessions.SetErr(nil)
	}
	step()
	{
		s.Nodes.SetErr(errors.New(marker + "-directory-body"))
		st, closeFn := s.Stream(t, n.Cert)
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "directory error body", err, codes.Unavailable, forbidden...)
		closeFn()
		s.Nodes.SetErr(nil)
	}

	// frame strings: envelope, payload, identity fields
	frameCases := []struct {
		name string
		mut  func(t *testing.T, f *ingestpb.SnapshotFrame)
		ack  ingestpb.AckCode
		end  codes.Code
	}{
		{"marker in bundle_revision", func(t *testing.T, f *ingestpb.SnapshotFrame) { f.BundleRevision = marker + "-rev" },
			ingestpb.AckCode_INVALID, codes.InvalidArgument},
		{"marker in the node asset kind", func(t *testing.T, f *ingestpb.SnapshotFrame) {
			for _, a := range f.GetPayload().GetAssets() {
				if a.GetKind() == "KubernetesNode" {
					a.Kind = marker + "-kind"
				}
			}
			gliReseal(t, f)
		}, ingestpb.AckCode_INVALID, codes.InvalidArgument},
		{"marker in the node asset alias", func(t *testing.T, f *ingestpb.SnapshotFrame) {
			for _, a := range f.GetPayload().GetAssets() {
				if a.GetKind() == "KubernetesNode" {
					a.Aliases = []*ingestpb.Alias{{Namespace: marker + "-ns", Value: marker + " valueé"}}
				}
			}
			gliReseal(t, f)
		}, ingestpb.AckCode_INVALID, codes.InvalidArgument},
		{"marker in boot_id", func(t *testing.T, f *ingestpb.SnapshotFrame) { f.BootId = marker + "-boot" },
			ingestpb.AckCode_INVALID, codes.FailedPrecondition},
		{"marker in node_uid", func(t *testing.T, f *ingestpb.SnapshotFrame) { f.NodeUid = marker + "-uid" },
			ingestpb.AckCode_INVALID, codes.PermissionDenied},
	}
	for _, tc := range frameCases {
		t.Run(tc.name, func(t *testing.T) {
			step()
			st, _, h := s.Connect(t, n)
			f := gliwFrame(t, n, h.GetSession(), 0)
			tc.mut(t, f)
			gliwExpectAckThenEnd(t, tc.name, st, f, tc.ack, tc.end, forbidden...)
		})
	}

	logs := s.LogText()
	for _, f := range append(forbidden, "PRIVATE KEY", "BEGIN CERTIFICATE") {
		if strings.Contains(logs, f) {
			t.Errorf("the server log contains %q", f)
		}
	}
}
