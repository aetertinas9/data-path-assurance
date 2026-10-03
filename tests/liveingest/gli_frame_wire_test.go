package liveingest_test

// Frame-level wire tests that need their own client connection: gzip streams
// (large frames: payload count limits, CP length limit) and raw-bytes streams
// (codec stage failures, L-CODEC). The shared Stream helper takes no call options,
// so these tests dial with the PKI files of the server themselves. Large-frame
// tests run one at a time (GLI-125) and are skipped with -short.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const (
	gliFxCPMax    = 16 * 1024 * 1024
	gliFxStreamRP = "/dpa.ingest.v1alpha1.Ingest/Stream"
)

// gliFxDial opens a TLS 1.3 client connection to the server with a node
// certificate and the server's CA (server name localhost).
func gliFxDial(t *testing.T, s *gliServer, cert gliCertFiles) *grpc.ClientConn {
	t.Helper()
	ca, err := os.ReadFile(s.PKI.CAFile)
	if err != nil {
		t.Fatalf("read the CA file: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatalf("the CA file holds no certificate")
	}
	kp, err := tls.LoadX509KeyPair(cert.CertFile, cert.KeyFile)
	if err != nil {
		t.Fatalf("load the client key pair: %v", err)
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{kp},
		ServerName: "localhost", NextProtos: []string{"h2"},
	}
	conn, err := grpc.NewClient(s.Addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// ConnectGzip is Connect over a stream whose requests are gzip-compressed.
func (e *gliFxEnv) ConnectGzip(name, uid, boot string) *gliFxConn {
	e.t.Helper()
	e.S.Nodes.Put(name, uid)
	e.S.Clock.Advance(gliFxStep)
	cert := e.S.PKI.NodeCert(e.t, gliFxCluster, uid)
	conn := gliFxDial(e.t, e.S, cert)
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	st, err := ingestpb.NewIngestClient(conn).Stream(ctx, grpc.UseCompressor(gzip.Name))
	if err != nil {
		e.t.Fatalf("open the gzip stream: %v", err)
	}
	hello, err := e.S.Hello(e.t, st, gliFxCluster, name, uid, boot)
	if err != nil {
		e.t.Fatalf("hello of %s over the gzip stream: %v", name, err)
	}
	return &gliFxConn{
		t: e.t, env: e, st: st, Name: name, UID: uid, Boot: boot, Hello: hello, Session: hello.GetSession(),
		oracle: gliFxOracle{session: hello.GetSession()},
	}
}

// ---------------------------------------------------------------------------
// Raw bytes (GLI-046 (a)).
// ---------------------------------------------------------------------------

// gliFxRawMsg is a message that the raw codec sends as is.
type gliFxRawMsg []byte

// gliFxRawCodec is the proto codec for generated messages and a pass-through
// for gliFxRawMsg (grpc.ForceCodecV2); it keeps the content-subtype proto.
type gliFxRawCodec struct{}

func (gliFxRawCodec) Name() string { return "proto" }

func (gliFxRawCodec) Marshal(v any) (mem.BufferSlice, error) {
	switch m := v.(type) {
	case gliFxRawMsg:
		return mem.BufferSlice{mem.SliceBuffer(m)}, nil
	case proto.Message:
		b, err := proto.Marshal(m)
		if err != nil {
			return nil, err
		}
		return mem.BufferSlice{mem.SliceBuffer(b)}, nil
	}
	return nil, fmt.Errorf("raw codec: cannot marshal %T", v)
}

func (gliFxRawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	m, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("raw codec: cannot unmarshal into %T", v)
	}
	return proto.Unmarshal(data.Materialize(), m)
}

// gliFxRawStream is a node stream after a successful hello on which arbitrary
// bytes can be sent.
type gliFxRawStream struct {
	t       *testing.T
	env     *gliFxEnv
	cs      grpc.ClientStream
	Session int64
}

// ConnectRaw opens a raw-capable stream for the node and performs the hello.
func (e *gliFxEnv) ConnectRaw(name, uid, boot string) *gliFxRawStream {
	e.t.Helper()
	e.S.Nodes.Put(name, uid)
	e.S.Clock.Advance(gliFxStep)
	cert := e.S.PKI.NodeCert(e.t, gliFxCluster, uid)
	conn := gliFxDial(e.t, e.S, cert)
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	cs, err := conn.NewStream(ctx, &grpc.StreamDesc{StreamName: "Stream", ServerStreams: true, ClientStreams: true}, gliFxStreamRP,
		grpc.ForceCodecV2(gliFxRawCodec{}))
	if err != nil {
		e.t.Fatalf("open the raw stream: %v", err)
	}
	r := &gliFxRawStream{t: e.t, env: e, cs: cs}
	hello := &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: &ingestpb.ClientHello{
		Version: "v1alpha1", ClusterId: gliFxCluster, NodeName: name, NodeUid: uid, BootId: boot,
	}}}
	if err := cs.SendMsg(hello); err != nil {
		e.t.Fatalf("send the raw hello: %v", err)
	}
	sf, err := r.recv(gliFxWait)
	if err != nil || sf.GetHello() == nil {
		e.t.Fatalf("raw hello: ServerFrame %v, error %v", sf, err)
	}
	r.Session = sf.GetHello().GetSession()
	return r
}

func (r *gliFxRawStream) recv(d time.Duration) (*ingestpb.ServerFrame, error) {
	r.t.Helper()
	type res struct {
		f   *ingestpb.ServerFrame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		f := &ingestpb.ServerFrame{}
		err := r.cs.RecvMsg(f)
		ch <- res{f, err}
	}()
	select {
	case x := <-ch:
		return x.f, x.err
	case <-time.After(d):
		r.t.Fatalf("no server message within %s on the raw stream", d)
	}
	return nil, nil
}

// SendFrame sends a typed frame through the raw stream.
func (r *gliFxRawStream) SendFrame(f *ingestpb.SnapshotFrame) {
	r.t.Helper()
	r.env.S.Clock.Advance(gliFxStep)
	if err := r.cs.SendMsg(gliFxSnapshotFrame(f)); err != nil {
		r.t.Fatalf("send the typed frame: %v", err)
	}
}

// SendBytes sends bytes as one gRPC message without any marshaling.
func (r *gliFxRawStream) SendBytes(b []byte) {
	r.t.Helper()
	r.env.S.Clock.Advance(gliFxStep)
	if err := r.cs.SendMsg(gliFxRawMsg(b)); err != nil {
		r.t.Fatalf("send raw bytes: %v", err)
	}
}

// GLI-046 (a), L-CODEC: failures of the receive path (invalid UTF-8 of a proto3
// string anywhere in the message, truncated or malformed wire data) end the
// stream with a non-OK status before the handler sees a message: no ack, no
// state change, no marker echo, and the same node can say hello again and
// start a new session. grpc-go's code for this is version dependent, so it is
// only logged.
func TestGLI046_FrameCodecStageFailuresHaveNoAckAndNoStateChange(t *testing.T) {
	base := gliFxStart(t)
	const mark = "GLIUTF8MARK1"
	patch := func(t *testing.T, b []byte, old string, repl []byte, all bool) []byte {
		t.Helper()
		if len(repl) != len(old) {
			t.Fatalf("test bug: replacement of %q changes the length", old)
		}
		if n := bytes.Count(b, []byte(old)); n == 0 || (!all && n != 1) {
			t.Fatalf("test bug: %q occurs %d times in the frame bytes", old, n)
		}
		return bytes.Replace(b, []byte(old), repl, 1)
	}
	bad := func(n int) []byte { return bytes.Repeat([]byte{0xff}, n) }
	rows := []struct {
		name string
		mk   func(t *testing.T, good []byte) []byte
	}{
		{"invalid UTF-8 in a payload string", func(t *testing.T, good []byte) []byte { return patch(t, good, mark, bad(len(mark)), false) }},
		{"invalid UTF-8 in the envelope node_uid", func(t *testing.T, good []byte) []byte {
			return patch(t, good, gliFxNodeUID, bad(len(gliFxNodeUID)), true)
		}},
		{"invalid UTF-8 in the envelope boot_id", func(t *testing.T, good []byte) []byte {
			return patch(t, good, gliFxBootID, bad(len(gliFxBootID)), false)
		}},
		{"overlong two-byte encoding in a payload string", func(t *testing.T, good []byte) []byte {
			return patch(t, good, mark, bytes.Repeat([]byte{0xc0, 0x80}, len(mark)/2), false)
		}},
		{"truncated message", func(t *testing.T, good []byte) []byte { return good[:len(good)-3] }},
		{"invalid wire type", func(t *testing.T, good []byte) []byte { return []byte{0x0f, 0x01, 0x02} }},
		{"length prefix beyond the data", func(t *testing.T, good []byte) []byte { return []byte{0x12, 0xff, 0xff, 0xff, 0x7f} }},
		{"unterminated varint", func(t *testing.T, good []byte) []byte { return bytes.Repeat([]byte{0xff}, 12) }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			raw := env.ConnectRaw(gliFxNodeName, gliFxNodeUID, gliFxBootID)
			at0, at1 := gliFxAt(0), gliFxAt(1)
			f0 := gliFrame(t, gliPayload(gliFxNodeUID, gliFxBootID, raw.Session, 0, at0), raw.Session, 0, at0, true)
			raw.SendFrame(f0)
			sf, err := raw.recv(gliFxWait)
			if err != nil || sf.GetAck().GetCode() != ingestpb.AckCode_ACCEPTED {
				t.Fatalf("setup: baseline over the raw stream: %v / %v", sf, err)
			}
			before := env.Sig()
			p1 := gliPayload(gliFxNodeUID, gliFxBootID, raw.Session, 1, at1)
			p1.Observations[1].Unit = mark
			f1 := gliFrame(t, p1, raw.Session, 1, at1, true)
			good, err := proto.Marshal(gliFxSnapshotFrame(f1))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			raw.SendBytes(r.mk(t, good))
			sf, err = raw.recv(gliFxWait)
			if err == nil {
				t.Fatalf("GLI-046 (a): the server answered %T instead of ending the stream (no ack for a codec failure)", sf.GetFrame())
			}
			st, _ := status.FromError(err)
			if st.Code() == codes.OK {
				t.Errorf("GLI-046 (a): the stream ended OK, want a non-OK status")
			}
			if strings.Contains(st.Message(), mark) {
				t.Errorf("GLI-101: the status message echoes payload text %q", st.Message())
			}
			t.Logf("codec stage failure %q ended with %s (version dependent, informational)", r.name, st.Code())
			env.WantSigUnchanged(before, "GLI-046 state unchanged")
			// The node can start over: new hello, new session, new baseline.
			c := env.ConnectDefault()
			if c.Session <= raw.Session {
				t.Fatalf("GLI-031: the new session %d is not larger than %d", c.Session, raw.Session)
			}
			c.AcceptSeq(0, gliFxAt(0), true)
		})
	}
	t.Run("valid UTF-8 outside the character set is an ack INVALID, not a codec failure", func(t *testing.T) {
		env := base.For(t)
		c := env.ConnectDefault()
		before := env.Sig()
		p := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(0))
		p.Observations[1].Unit = "lanés"
		f := gliFrame(t, p, c.Session, 0, gliFxAt(0), true)
		c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "L-CODEC e-acute")
		env.WantSigUnchanged(before, "GLI-045")
	})
}

// GLI-101: text of an invalid payload never comes back in ack or status
// messages nor in the server's slog output (read through the synchronized
// LogText of the shared server helper, never through the Logs buffer).
func TestGLI101_PayloadTextIsNeverEchoed(t *testing.T) {
	const marker = "GLIMARKER-8c1e5f07"
	env := gliFxStart(t)
	rows := []struct {
		name string
		mut  func(p *ingestpb.HostSnapshotV1)
	}{
		{"unit with the marker and a non-ASCII character", func(p *ingestpb.HostSnapshotV1) { p.Observations[1].Unit = marker + "é" }},
		{"dimension value with the marker and a space", func(p *ingestpb.HostSnapshotV1) {
			p.Observations[1].Dimensions[0].Value = marker + " x"
		}},
		{"source name with the marker and a space", func(p *ingestpb.HostSnapshotV1) {
			p.EdgeEvidence[0].SourceName = marker + " x"
		}},
		{"asset canonical with the marker and no namespace", func(p *ingestpb.HostSnapshotV1) {
			p.Assets = append(p.Assets, gliFxAsset("PCIeFunction", marker))
			gliFxSortPayload(p)
		}},
		{"asset kind that is not an asset kind", func(p *ingestpb.HostSnapshotV1) {
			p.Assets = append(p.Assets, gliFxAsset(marker, "pci-bdf:0000:0a:00.0"))
			gliFxSortPayload(p)
		}},
		{"gpuBinding uuid with the marker", func(p *ingestpb.HostSnapshotV1) { p.GpuBindings[0].Uuid = marker }},
		{"edge relation with the marker", func(p *ingestpb.HostSnapshotV1) { p.Edges[0].Relation = marker }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := env.For(t)
			c := e.ConnectDefault()
			p := gliPayload(c.UID, c.Boot, c.Session, 0, gliFxAt(0))
			r.mut(p)
			f := gliFrame(t, p, c.Session, 0, gliFxAt(0), true)
			a := c.Ack(f)
			if a.GetCode() != ingestpb.AckCode_INVALID {
				t.Fatalf("ack %s, want INVALID", a.GetCode())
			}
			if strings.Contains(a.GetMessage(), marker) {
				t.Errorf("GLI-101/GLI-102: the ack message echoes payload text: %q", a.GetMessage())
			}
			code, msg := c.End()
			if code != codes.InvalidArgument || strings.Contains(msg, marker) {
				t.Errorf("stream ended %s %q, want InvalidArgument without payload text", code, msg)
			}
		})
	}
	if log := env.S.LogText(); strings.Contains(log, marker) {
		t.Errorf("GLI-101: the server log contains payload text")
	}
}

// ---------------------------------------------------------------------------
// Large frames: payload count limits (GLI-051) and the CP length limit (GLI-050).
// ---------------------------------------------------------------------------

// gliFxRunBigRows is gliFxRunRows over gzip streams (the frames exceed the 4 MiB
// identity limit of GLI-050).
func gliFxRunBigRows(t *testing.T, base *gliFxEnv, rows []gliFxV1Row, clause string) {
	t.Helper()
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectGzip(gliFxNodeName, gliFxNodeUID, gliFxBootID)
			before := env.Sig()
			m := gliFxMc{session: c.Session, seq: 0, at: gliFxAt(0)}
			p := gliPayload(c.UID, c.Boot, m.session, m.seq, m.at)
			r.mut(p, m)
			f := gliFrame(t, p, c.Session, 0, m.at, true)
			if r.ok {
				a := c.Ack(f)
				gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, 1, clause+" at the limit")
				return
			}
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, clause+" over the limit")
			env.WantSigUnchanged(before, clause+" atomicity: no truncation, no PARTIAL conversion")
		})
	}
}

func gliFxBDF(domain int, i int) string {
	return fmt.Sprintf("%04x:%02x:%02x.%d", domain, (i>>8)&0xff, (i>>3)&0x1f, i&7)
}

// gliFxFillObs adds observations of the GPU function with unique signals until
// the payload has total observations.
func gliFxFillObs(p *ingestpb.HostSnapshotV1, m gliFxMc, total int) {
	for i := len(p.Observations); len(p.Observations) < total; i++ {
		o := gliFxObservation(m.session, m.seq, m.at, "PCIeFunction", "pci-bdf:"+gliFxGPUBDF, fmt.Sprintf("gli.e%05d", i), gliFxWidthSrc, gliFxInt(1), "n", nil)
		o.ExpiresAt = gliFxTS(m.at.Add(gliFxTTL))
		p.Observations = append(p.Observations, o)
	}
	gliFxSortPayload(p)
}

// gliFxFillAssets adds PCIeFunction assets until the payload has total assets.
func gliFxFillAssets(p *ingestpb.HostSnapshotV1, total int) {
	for i := 0; len(p.Assets) < total; i++ {
		p.Assets = append(p.Assets, gliFxAsset("PCIeFunction", "pci-bdf:"+gliFxBDF(0x10, i)))
	}
	gliFxSortPayload(p)
}

// gliFxFillEdges adds extra functions and eight switches and function->switch
// edges (with evidence) until the payload has total edges.
func gliFxFillEdges(p *ingestpb.HostSnapshotV1, m gliFxMc, total int) {
	funcs := (total-len(p.Edges))/8 + 1
	for j := 0; j < funcs; j++ {
		p.Assets = append(p.Assets, gliFxAsset("PCIeFunction", "pci-bdf:"+gliFxBDF(0x10, j)))
	}
	for k := 0; k < 8; k++ {
		p.Assets = append(p.Assets, gliFxAsset("PCIeSwitch", fmt.Sprintf("pci-bdf:0020:00:%02x.0", k)))
	}
	for j := 0; len(p.Edges) < total; j++ {
		for k := 0; k < 8 && len(p.Edges) < total; k++ {
			e, ev := gliFxEdgeParts(m.session, m.seq, m.at, gliFxFnKey(gliFxBDF(0x10, j)), gliFxSWKey(fmt.Sprintf("0020:00:%02x.0", k)))
			ev.EdgeIndex = uint32(len(p.Edges))
			p.Edges = append(p.Edges, e)
			p.EdgeEvidence = append(p.EdgeEvidence, ev)
		}
	}
	gliFxSortPayload(p)
}

// gliFxFillBindings adds gpuBindings with distinct (bdf, uuid) until total.
func gliFxFillBindings(p *ingestpb.HostSnapshotV1, m gliFxMc, total int) {
	for i := 0; len(p.GpuBindings) < total; i++ {
		uuid := fmt.Sprintf("GPU-%08x-0000-4000-8000-%012x", i, i)
		p.GpuBindings = append(p.GpuBindings, gliFxBinding(m.session, m.seq, m.at, uuid, fmt.Sprintf("0020:%02x:00.0", i)))
	}
	gliFxSortPayload(p)
}

// gliFxSixtyFourDims is 64 dimensions with strictly increasing one-byte keys.
func gliFxSixtyFourDims(n int) []*ingestpb.Dimension {
	keys := []string{"-"}
	for c := '0'; c <= '9'; c++ {
		keys = append(keys, string(c))
	}
	for c := 'A'; c <= 'Z'; c++ {
		keys = append(keys, string(c))
	}
	keys = append(keys, "_")
	for c := 'a'; c <= 'z'; c++ {
		keys = append(keys, string(c))
	}
	var out []*ingestpb.Dimension
	for _, k := range keys[:n] {
		out = append(out, &ingestpb.Dimension{Key: k, Value: "v"})
	}
	return out
}

// GLI-051: the decode limits, each at the limit (accepted) and one over
// (INVALID for the whole frame; nothing is truncated or turned PARTIAL).
func TestGLI051_FramePayloadCountLimitsAtAndOverByOne(t *testing.T) {
	if testing.Short() {
		t.Skip("large frames; skipped with -short")
	}
	base := gliFxStart(t)
	entry := func(i int) *ingestpb.AllocationEntry {
		return &ingestpb.AllocationEntry{
			ResourceName: "nvidia.com/gpu", DeviceId: gliFxUUID, PodNamespace: "ml", PodName: fmt.Sprintf("p%04d", i), ContainerName: "main",
		}
	}
	withBatch := func(p *ingestpb.HostSnapshotV1, m gliFxMc, nEntries, nRefs int) {
		ab := &ingestpb.AllocationBatch{
			NodeUid: gliFxNodeUID, BootId: gliFxBootID, Session: m.session, Sequence: m.seq,
			ObservedAt: gliFxTS(m.at), ExpiresAt: gliFxTS(m.at.Add(gliFxTTL)), Complete: true,
			Profile: ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID,
		}
		for i := 0; i < nEntries; i++ {
			ab.Entries = append(ab.Entries, entry(i))
		}
		for i := 0; i < nRefs; i++ {
			ab.EvidenceRefs = append(ab.EvidenceRefs, fmt.Sprintf("ev:%03d", i))
		}
		p.AllocationBatch = ab
	}
	aliases := func(n int) []*ingestpb.Alias {
		var out []*ingestpb.Alias
		for i := 0; i < n; i++ {
			out = append(out, &ingestpb.Alias{Namespace: "lldp-port-id", Value: fmt.Sprintf("p%03d", i)})
		}
		return out
	}
	rows := []gliFxV1Row{
		{"observations 4096", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillObs(p, m, 4096) }},
		{"observations 4097", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillObs(p, m, 4097) }},
		{"assets 4096", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillAssets(p, 4096) }},
		{"assets 4097", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillAssets(p, 4097) }},
		{"edges and edgeEvidence 8192", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillEdges(p, m, 8192) }},
		{"edges and edgeEvidence 8193", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillEdges(p, m, 8193) }},
		{"gpuBindings 256", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillBindings(p, m, 256) }},
		{"gpuBindings 257", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { gliFxFillBindings(p, m, 257) }},
		{"asset aliases 64", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Assets[2].Aliases = aliases(64) }},
		{"asset aliases 65", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { p.Assets[2].Aliases = aliases(65) }},
		{"observation dimensions 64", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			gliFxExtraObs(p, m, "gli.extra", gliFxInt(1), "n", gliFxSixtyFourDims(64))
		}},
		{"observation dimensions 65", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) {
			d := gliFxSixtyFourDims(64)
			d = append(d, &ingestpb.Dimension{Key: "~", Value: "v"})
			gliFxExtraObs(p, m, "gli.extra", gliFxInt(1), "n", d)
		}},
		{"allocation entries 4096", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { withBatch(p, m, 4096, 0) }},
		{"allocation entries 4097", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { withBatch(p, m, 4097, 0) }},
		{"allocation evidence_refs 64", true, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { withBatch(p, m, 1, 64) }},
		{"allocation evidence_refs 65", false, func(p *ingestpb.HostSnapshotV1, m gliFxMc) { withBatch(p, m, 1, 65) }},
	}
	gliFxRunBigRows(t, base, rows, "GLI-051")
}

// gliFxFatDims is n dimensions with strictly increasing keys of klen bytes and
// values of vlen bytes.
func gliFxFatDims(n, klen, vlen int) []*ingestpb.Dimension {
	var out []*ingestpb.Dimension
	for j := 0; j < n; j++ {
		k := fmt.Sprintf("k%02d", j)
		k += strings.Repeat("x", klen-len(k))
		out = append(out, &ingestpb.Dimension{Key: k, Value: strings.Repeat("v", vlen)})
	}
	return out
}

// gliFxBigPayload builds a valid payload whose CP is exactly target bytes within
// the 4096 observation limit: observations with 64 dimensions of 40
// byte keys and values. For strings shorter than 128 bytes the CP spends 4 bytes
// per string length where protobuf spends 1-2 (and nothing for the repeated
// dimension wrapper), so an observation's CP (about 5.9 KB) exceeds its
// protobuf size (about 5.7 KB) and about 2870 observations make a CP of 16 MiB
// in a message of about 16.3 MB, below the 16 MiB receive limit. A last
// observation with a padded string value fills the remainder.
func gliFxBigPayload(t *testing.T, c *gliFxConn, m gliFxMc, target int) *ingestpb.HostSnapshotV1 {
	t.Helper()
	p := gliPayload(c.UID, c.Boot, m.session, m.seq, m.at)
	mk := func(i int, v *ingestpb.Value) *ingestpb.Observation {
		o := gliFxObservation(m.session, m.seq, m.at, "PCIeFunction", "pci-bdf:"+gliFxGPUBDF, fmt.Sprintf("gli.p%06d", i), gliFxWidthSrc, v, "lanes", gliFxFatDims(64, 40, 40))
		o.ExpiresAt = gliFxTS(m.at.Add(gliFxTTL))
		return o
	}
	cpOf := func(o *ingestpb.Observation) int {
		return len(gliFxCPObservations(&ingestpb.HostSnapshotV1{Observations: []*ingestpb.Observation{o}})) - 4
	}
	baseCP := len(gliCanonical(p))
	perObs := cpOf(mk(0, gliFxInt(16)))
	padObs0 := cpOf(mk(0, gliFxStrVal("")))
	n := (target - baseCP - padObs0) / perObs
	if n < 1 {
		t.Fatalf("test bug: target %d too small", target)
	}
	if len(p.Observations)+n+1 > 4096 {
		t.Fatalf("test bug: %d observations would exceed the 4096 observation limit", len(p.Observations)+n+1)
	}
	for i := 0; i < n; i++ {
		p.Observations = append(p.Observations, mk(i, gliFxInt(16)))
	}
	pad := target - baseCP - perObs*n - padObs0
	if pad < 0 || pad > 4096 {
		t.Fatalf("test bug: padding %d outside 0..4096", pad)
	}
	p.Observations = append(p.Observations, mk(n, gliFxStrVal(strings.Repeat("p", pad))))
	gliFxSortPayload(p)
	if got := len(gliCanonical(p)); got != target {
		t.Fatalf("test bug: CP is %d bytes, want %d", got, target)
	}
	return p
}

// GLI-050: a frame whose canonical CP is longer than 16 MiB is rejected with an
// ack INVALID although the message itself is within the receive limits (gzip,
// protobuf size below 16 MiB, at most 4096 observations with 64 dimensions each);
// a CP of exactly 16 MiB is accepted. The case is reachable inside the count
// limits (see gliFxBigPayload); the protobuf size is asserted per case.
func TestGLI050_FrameCPOverSixteenMiBIsInvalidAck(t *testing.T) {
	if testing.Short() {
		t.Skip("16 MiB frame; skipped with -short")
	}
	base := gliFxStart(t)
	cases := []struct {
		name string
		cp   int
		ok   bool
	}{
		{"CP of exactly 16 MiB is accepted", gliFxCPMax, true},
		{"CP of 16 MiB plus one byte is INVALID", gliFxCPMax + 1, false},
	}
	for _, tc := range cases { // one at a time (GLI-125)
		t.Run(tc.name, func(t *testing.T) {
			env := base.For(t)
			c := env.ConnectGzip(gliFxNodeName, gliFxNodeUID, gliFxBootID)
			m := gliFxMc{session: c.Session, seq: 0, at: gliFxAt(0)}
			p := gliFxBigPayload(t, c, m, tc.cp)
			f := gliFrame(t, p, c.Session, 0, m.at, true)
			if wire := proto.Size(gliFxSnapshotFrame(f)); wire >= gliFxCPMax {
				t.Fatalf("test bug: the protobuf message is %d bytes, it must stay below 16 MiB so that only the CP length can reject it", wire)
			}
			before := env.Sig()
			if tc.ok {
				a := c.Ack(f)
				gliFxWantAck(t, a, ingestpb.AckCode_ACCEPTED, c.Session, 1, "GLI-050 CP == 16 MiB")
				return
			}
			c.Reject(f, ingestpb.AckCode_INVALID, codes.InvalidArgument, "GLI-050 CP > 16 MiB")
			env.WantSigUnchanged(before, "GLI-045")
		})
	}
}
