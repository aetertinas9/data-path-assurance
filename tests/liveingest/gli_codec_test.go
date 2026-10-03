package liveingest_test

// GLI-046 (codec stage) and the L-CODEC acceptance scenario. Messages the gRPC
// codec cannot decode end the stream without an ack, change nothing, and the node
// may say hello again. A valid-UTF-8 string with a character outside the ASCII
// sets is not a codec failure: it is an ack INVALID (GLI-060 (a)).

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliwCorruptName is a compressor that behaves like gzip until gliwCorruptSwitch is
// set, and then writes a corrupt stream. The server (same process) decompresses it
// with real gzip. It is registered in a package-level initialiser, before any
// goroutine of any test exists, because the registry is not safe for concurrent use.
const gliwCorruptName = "gliwcorrupt"

var gliwCorruptSwitch atomic.Bool

type gliwCorruptCompressor struct{}

func (gliwCorruptCompressor) Name() string { return gliwCorruptName }

func (gliwCorruptCompressor) Compress(w io.Writer) (io.WriteCloser, error) {
	if gliwCorruptSwitch.Load() {
		return &gliwGarbageWriter{w: w}, nil
	}
	return gzip.NewWriter(w), nil
}

func (gliwCorruptCompressor) Decompress(r io.Reader) (io.Reader, error) {
	return gzip.NewReader(r)
}

type gliwGarbageWriter struct{ w io.Writer }

func (g *gliwGarbageWriter) Write(p []byte) (int, error) { return len(p), nil }

func (g *gliwGarbageWriter) Close() error {
	_, err := g.w.Write([]byte{0x1f, 0x8b, 0x08, 0xff, 0xff, 0xff, 0xff, 0xde, 0xad, 0xbe, 0xef})
	return err
}

var _ = func() bool {
	encoding.RegisterCompressor(gliwCorruptCompressor{})
	return true
}()

func gliwRawSnapshot(inner []byte) gliwRaw {
	b := protowire.AppendTag(nil, 2, protowire.BytesType) // ClientFrame.snapshot
	return gliwRaw(protowire.AppendBytes(b, inner))
}

// gliwRawSnapshotString puts val into string field `field` of a SnapshotFrame.
func gliwRawSnapshotString(field protowire.Number, val []byte) gliwRaw {
	inner := protowire.AppendTag(nil, field, protowire.BytesType)
	inner = protowire.AppendBytes(inner, val)
	return gliwRawSnapshot(inner)
}

func TestGLI046_CodecFailuresEndWithoutAck(t *testing.T) {
	s := gliStartServer(t)
	cases := []struct {
		name string
		msg  gliwRaw
	}{
		{"invalid UTF-8 in bundle_revision", gliwRawSnapshotString(9, []byte{'a', 0xff, 0xfe})},
		{"invalid UTF-8 in node_uid", gliwRawSnapshotString(1, []byte{0xc3, 0x28})},
		{"overlong UTF-8 sequence", gliwRawSnapshotString(2, []byte{0xc0, 0xaf})},
		{"truncated length-delimited field", gliwRaw{0x12, 0x20, 0x0a, 0x01, 'a'}},
		{"tag zero", gliwRaw{0x00}},
		{"unterminated varint", gliwRaw(bytes.Repeat([]byte{0xff}, 11))},
		{"stray end-group tag", gliwRaw{0x0c}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := s.AddNode(t, "codec")
			st, _, h := s.Connect(t, n, grpc.ForceCodecV2(gliwRawCodec{}))
			session := h.GetSession()
			s.Baseline(t, st, n, session)
			if err := st.SendMsg(tc.msg); err != nil && gliwCode(err) == codes.OK {
				t.Logf("send: %v", err)
			}
			fr, err := st.Recv()
			if err == nil {
				t.Fatalf("got a server message (%v), want the stream to end without an ack", fr)
			}
			if gliwCode(err) == codes.OK {
				t.Fatalf("the stream ended with OK, want a failure status")
			}
			t.Logf("codec failure status (library dependent, informational): %v", gliwCode(err))
			s.WantSnapshot(t, "after the undecodable message", n, session, 0)
			s.Clock.Advance(10 * time.Second)
			_, closeFn, h2 := s.Connect(t, n)
			closeFn()
			if h2.GetSession() <= session {
				t.Errorf("session %d after %d", h2.GetSession(), session)
			}
		})
	}
}

// TestGLI046_UndecodableHello: the first message is already garbage.
func TestGLI046_UndecodableHello(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "codechello")
	st, _ := s.RawStream(t, n.Cert)
	// ClientFrame.hello (field 1) holding ClientHello.version (field 1) = 0xFF.
	inner := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), []byte{0xff})
	msg := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), inner)
	if err := st.SendMsg(gliwRaw(msg)); err != nil && gliwCode(err) == codes.OK {
		t.Logf("send: %v", err)
	}
	fr, err := st.Recv()
	if err == nil {
		t.Fatalf("got a server message (%v), want the stream to end", fr)
	}
	if gliwCode(err) == codes.OK {
		t.Fatalf("the stream ended with OK")
	}
	if c := s.Sessions.Calls(); len(c) != 0 {
		t.Errorf("session store called: %+v", c)
	}
	_, closeFn, _ := s.Connect(t, n)
	closeFn()
}

// TestGLI046_DecompressionFailure: a message whose compressed form cannot be
// inflated is a codec-stage failure too.
func TestGLI046_DecompressionFailure(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "codecgz")
	st, _, h := s.Connect(t, n, grpc.UseCompressor(gliwCorruptName))
	session := h.GetSession()
	s.Baseline(t, st, n, session)
	s.Clock.Advance(10 * time.Second)
	gliwCorruptSwitch.Store(true)
	err := gliwSend(st, gliwFrame(t, n, session, 1))
	gliwCorruptSwitch.Store(false)
	if err != nil {
		t.Logf("send: %v", err)
	}
	fr, err := st.Recv()
	if err == nil {
		t.Fatalf("got a server message (%v), want the stream to end without an ack", fr)
	}
	if gliwCode(err) == codes.OK {
		t.Fatalf("the stream ended with OK")
	}
	s.WantSnapshot(t, "after the corrupt compressed message", n, session, 0)
	s.Clock.Advance(10 * time.Second)
	_, closeFn, _ := s.Connect(t, n)
	closeFn()
}

// TestGLI046_NonASCIIStringIsAckInvalid (L-CODEC, second half): valid UTF-8 that
// is not ASCII reaches the handler and is refused there with ack INVALID; the same
// frame with an ASCII value is accepted, so the character is the only difference.
func TestGLI046_NonASCIIStringIsAckInvalid(t *testing.T) {
	s := gliStartServer(t)
	mutate := func(t *testing.T, f *ingestpb.SnapshotFrame, value string) {
		t.Helper()
		if len(f.GetPayload().GetObservations()) > 0 {
			f.Payload.Observations[0].Unit = value
		} else {
			found := false
			for _, a := range f.GetPayload().GetAssets() {
				if a.GetKind() == "KubernetesNode" {
					a.Aliases = []*ingestpb.Alias{{Namespace: "gliw", Value: value}}
					found = true
				}
			}
			if !found {
				t.Fatalf("payload has neither an observation nor a KubernetesNode asset to mutate")
			}
		}
		gliReseal(t, f)
	}
	cases := []struct {
		name  string
		value string
		want  ingestpb.AckCode
	}{
		{"ASCII control", "units", ingestpb.AckCode_ACCEPTED},
		{"e acute (valid UTF-8, not ASCII)", "unités", ingestpb.AckCode_INVALID},
		{"CJK character", "units漢", ingestpb.AckCode_INVALID},
		{"control character", "un\x01its", ingestpb.AckCode_INVALID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := s.AddNode(t, "ascii")
			st, _, h := s.Connect(t, n)
			f := gliwFrame(t, n, h.GetSession(), 0)
			mutate(t, f, tc.value)
			ack := gliwExchange(t, st, f)
			if ack.GetCode() != tc.want {
				t.Fatalf("ack %v, want %v", ack.GetCode(), tc.want)
			}
			if tc.want == ingestpb.AckCode_INVALID {
				gliwHygiene(t, "ack", ack.GetMessage(), "é", "漢")
				gliwExpectEnd(t, "after INVALID", st, codes.InvalidArgument)
				s.WantNoObservation(t, "rejected frame", n)
				// ... and the node may say hello again.
				s.Clock.Advance(10 * time.Second)
				_, closeFn, _ := s.Connect(t, n)
				closeFn()
				return
			}
			s.WantSnapshot(t, "accepted control frame", n, h.GetSession(), 0)
		})
	}
}
