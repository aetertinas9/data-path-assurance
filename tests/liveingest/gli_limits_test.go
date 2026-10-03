package liveingest_test

// GLI-050..054 and GLI-124 (d): message sizes, payload counts, token buckets
// (frames and hellos) and node retention. Rate and retention run on the fake
// Clock; the size tests move up to 16 MiB per message and therefore run one at a
// time (no t.Parallel in these tests, GLI-125).

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/proto"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const (
	gliwMiB          = 1 << 20
	gliwMaxRecvBytes = 4 * gliwMiB
	gliwMaxInflate   = 16 * gliwMiB
)

// gliwPadTo builds a ClientFrame whose serialized size is exactly total bytes by
// growing a string/bytes field; build(k) must be affine in k apart from the
// varint length prefix, which the loop compensates.
func gliwPadTo(t *testing.T, total int, build func(k int) *ingestpb.ClientFrame) *ingestpb.ClientFrame {
	t.Helper()
	k := total
	for i := 0; i < 10; i++ {
		m := build(k)
		got := proto.Size(m)
		if got == total {
			return m
		}
		k += total - got
		if k < 0 {
			k = 0
		}
	}
	t.Fatalf("could not size a message to exactly %d bytes", total)
	return nil
}

// gliwBigRevisionFrame is a snapshot message padded through bundle_revision (an
// ASCII string: decodable, and refused by F3 with INVALID once it is read).
func gliwBigRevisionFrame(t *testing.T, n gliwNode, session int64, seq uint64, total int) *ingestpb.ClientFrame {
	t.Helper()
	return gliwPadTo(t, total, func(k int) *ingestpb.ClientFrame {
		return gliwSnapshotMsg(&ingestpb.SnapshotFrame{
			NodeUid: n.UID, BootId: n.Boot, Session: session, Sequence: seq,
			Completeness:   ingestpb.Completeness_COMPLETE,
			ObservedAt:     gliwTimestamp(gliwAt(seq)),
			BundleRevision: strings.Repeat("a", k),
		})
	})
}

// gliwBigDigestFrame is padded through payload_digest with the given bytes.
func gliwBigDigestFrame(t *testing.T, n gliwNode, session int64, seq uint64, total int, blob []byte) *ingestpb.ClientFrame {
	t.Helper()
	return gliwPadTo(t, total, func(k int) *ingestpb.ClientFrame {
		if k > len(blob) {
			k = len(blob)
		}
		return gliwSnapshotMsg(&ingestpb.SnapshotFrame{
			NodeUid: n.UID, BootId: n.Boot, Session: session, Sequence: seq,
			Completeness:  ingestpb.Completeness_COMPLETE,
			ObservedAt:    gliwTimestamp(gliwAt(seq)),
			PayloadDigest: blob[:k],
		})
	})
}

func TestGLI050_MessageSizeLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("moves messages of up to 16 MiB; skipped with -short (GLI-125)")
	}
	s := gliStartServer(t)
	random := make([]byte, 5*gliwMiB)
	if _, err := crand.Read(random); err != nil {
		t.Fatalf("random: %v", err)
	}
	cases := []struct {
		name    string
		gzip    bool
		total   int
		random  bool
		wantRE  bool
		comment string
	}{
		{name: "identity 4 MiB - 1", total: gliwMaxRecvBytes - 1},
		{name: "identity exactly 4 MiB", total: gliwMaxRecvBytes},
		{name: "identity 4 MiB + 1", total: gliwMaxRecvBytes + 1, wantRE: true},
		{name: "identity 16 MiB + 1 (length prefix)", total: gliwMaxInflate + 1, wantRE: true},
		{name: "gzip, 8 MiB inflated, tiny on the wire", gzip: true, total: 8 * gliwMiB},
		{name: "gzip, exactly 16 MiB inflated", gzip: true, total: gliwMaxInflate},
		{name: "gzip, 16 MiB + 1 inflated", gzip: true, total: gliwMaxInflate + 1, wantRE: true},
		{name: "gzip, incompressible 3 MiB on the wire", gzip: true, total: 3 * gliwMiB, random: true},
		{name: "gzip, incompressible 4.5 MiB on the wire", gzip: true, total: 4*gliwMiB + gliwMiB/2, random: true, wantRE: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := s.AddNode(t, "size")
			var opts []grpc.CallOption
			if tc.gzip {
				opts = append(opts, grpc.UseCompressor(grpcgzip.Name))
			}
			st, closeFn, h := s.Connect(t, n, opts...)
			defer closeFn()
			session := h.GetSession()
			s.Baseline(t, st, n, session)

			var msg *ingestpb.ClientFrame
			if tc.random {
				msg = gliwBigDigestFrame(t, n, session, 1, tc.total, random)
			} else {
				msg = gliwBigRevisionFrame(t, n, session, 1, tc.total)
			}
			if got := proto.Size(msg); got != tc.total {
				t.Fatalf("test message is %d bytes, want %d", got, tc.total)
			}
			s.Clock.Advance(10 * time.Second)
			// A refused oversize message may close the stream before the whole body is
			// written; the status always comes from Recv.
			_ = st.Send(msg)
			fr, err := st.Recv()
			if tc.wantRE {
				if err == nil {
					t.Fatalf("got a server message (%v), want the stream to end with ResourceExhausted and no ack", fr)
				}
				if got := gliwCode(err); got != codes.ResourceExhausted {
					t.Fatalf("status %v (%v), want ResourceExhausted", got, err)
				}
			} else {
				if err != nil {
					t.Fatalf("a message within the limits ended the stream: %v", err)
				}
				if fr.GetAck() == nil || fr.GetAck().GetCode() != ingestpb.AckCode_INVALID {
					t.Fatalf("server message %v, want an ack INVALID (the payload is invalid, the size is not)", fr)
				}
				gliwExpectEnd(t, "after INVALID", st, codes.InvalidArgument)
			}
			s.WantSnapshot(t, "after the sized message", n, session, 0)
			// A new hello is accepted afterwards.
			s.Clock.Advance(10 * time.Second)
			_, closeFn2, h2 := s.Connect(t, n)
			closeFn2()
			if h2.GetSession() <= session {
				t.Errorf("session %d after %d", h2.GetSession(), session)
			}
		})
	}
}

// --- GLI-051 payload counts -----------------------------------------------------

// gliwID is the GFO-084 ID: <prefix>:<session>:<sequence>:<hex of the first 16
// bytes of SHA-256 over u32be(len)||bytes of every tuple element>. It is an
// independent implementation, checked against the specification examples.
func gliwID(prefix string, session int64, seq uint64, tuple ...string) string {
	var buf []byte
	for _, e := range tuple {
		n := len(e)
		buf = append(buf, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		buf = append(buf, e...)
	}
	sum := sha256.Sum256(buf)
	return fmt.Sprintf("%s:%d:%d:%x", prefix, session, seq, sum[:16])
}

func gliwBDF(domain, i int) string {
	return fmt.Sprintf("%04x:%02x:%02x.%x", domain, i/256, (i/8)%32, i%8)
}

type gliwCount struct {
	assets      int // total assets including the KubernetesNode asset (>= 1)
	funcs       int // PCIeFunction assets used by edges/observations/bindings (<= assets-1)
	switches    int
	parents     int // edges per function (needs switches > parents)
	extraEdges  int // additional distinct function-0 edges
	signals     int // observations per function
	extraObs    int // additional signals on function 0
	bindings    int
	aliases     int // aliases on function 0
	dims        int // dimensions on one extra observation (0: none)
	entries     int
	evidenceRef int
}

// gliwCountPayload builds a payload that satisfies every V1 rule except possibly
// the count under test, with all collections in the strictly increasing order of
// GLI-060 (d) and all ids recomputed (GFO-084).
func gliwCountPayload(n gliwNode, session int64, seq uint64, at time.Time, c gliwCount) *ingestpb.HostSnapshotV1 {
	exp := at.Add(300 * time.Second)
	p := &ingestpb.HostSnapshotV1{}
	nodeAsset := &ingestpb.Asset{Kind: "KubernetesNode", Canonical: "kubernetes-node-uid:" + n.UID}
	p.Assets = append(p.Assets, nodeAsset)
	fnKey := func(i int) string { return "PCIeFunction/pci-bdf:" + gliwBDF(0, i) }
	swKey := func(i int) string { return "PCIeSwitch/pci-bdf:" + gliwBDF(1, i) }
	nFuncAssets := c.assets - 1 - c.switches
	for i := 0; i < nFuncAssets; i++ {
		a := &ingestpb.Asset{Kind: "PCIeFunction", Canonical: "pci-bdf:" + gliwBDF(0, i)}
		if i == 0 {
			for k := 0; k < c.aliases; k++ {
				a.Aliases = append(a.Aliases, &ingestpb.Alias{Namespace: "gliw-alias", Value: fmt.Sprintf("%04d", k)})
			}
		}
		p.Assets = append(p.Assets, a)
	}
	for i := 0; i < c.switches; i++ {
		p.Assets = append(p.Assets, &ingestpb.Asset{Kind: "PCIeSwitch", Canonical: "pci-bdf:" + gliwBDF(1, i)})
	}

	type edge struct{ from, to string }
	var edges []edge
	if c.switches > 0 {
		for i := 0; i < c.funcs; i++ {
			for k := 0; k < c.parents; k++ {
				edges = append(edges, edge{fnKey(i), swKey((i + k) % c.switches)})
			}
		}
		for k := 0; k < c.extraEdges; k++ {
			edges = append(edges, edge{fnKey(0), swKey((c.parents + k) % c.switches)})
		}
	}
	sort.Slice(edges, func(a, b int) bool {
		if edges[a].from != edges[b].from {
			return edges[a].from < edges[b].from
		}
		return edges[a].to < edges[b].to
	})
	for i, e := range edges {
		p.Edges = append(p.Edges, &ingestpb.Edge{FromKey: e.from, Relation: "LOCATED_IN", ToKey: e.to, Origin: "Observed"})
		p.EdgeEvidence = append(p.EdgeEvidence, &ingestpb.EdgeEvidence{
			EdgeIndex: uint32(i), Kind: "Observed", SourceType: "agent", SourceName: "path-agent/sysfs-parent",
			ObservedAt: gliwTimestamp(at), ExpiresAt: gliwTimestamp(exp),
			EvidenceId: gliwID("pe", session, seq, "dpa.edge-evidence.v1", e.from, "LOCATED_IN", e.to, "Observed"),
		})
	}

	obs := func(i int, signal string, dims []*ingestpb.Dimension) *ingestpb.Observation {
		subject := &ingestpb.Asset{Kind: "PCIeFunction", Canonical: "pci-bdf:" + gliwBDF(0, i)}
		return &ingestpb.Observation{
			Id:         gliwID("ob", session, seq, "dpa.observation.v1", fnKey(i), signal),
			Source:     &ingestpb.SourceRef{Type: "agent", Name: "path-agent/sysfs-width"},
			Subject:    subject,
			Signal:     signal,
			Value:      &ingestpb.Value{V: &ingestpb.Value_IntValue{IntValue: 1}},
			Unit:       "u",
			Dimensions: dims,
			ObservedAt: gliwTimestamp(at), ReceivedAt: gliwTimestamp(at),
			Sequence: seq, Quality: "Good",
		}
	}
	if c.dims > 0 {
		var dims []*ingestpb.Dimension
		for k := 0; k < c.dims; k++ {
			dims = append(dims, &ingestpb.Dimension{Key: fmt.Sprintf("gliw.dim_%03d", k), Value: "v"})
		}
		p.Observations = append(p.Observations, obs(0, "gliw.dims_case", dims))
	}
	for i := 0; i < c.funcs && c.signals > 0; i++ {
		count := c.signals
		if i == 0 {
			count += c.extraObs
		}
		for j := 0; j < count; j++ {
			p.Observations = append(p.Observations, obs(i, fmt.Sprintf("gliw.metric_%02d", j), nil))
		}
	}

	for i := 0; i < c.bindings; i++ {
		uuid := fmt.Sprintf("GPU-%08x-3e4f-4a5b-8c6d-%012x", i, i)
		bdf := gliwBDF(0, i)
		p.GpuBindings = append(p.GpuBindings, &ingestpb.GPUBinding{
			Uuid: uuid, Bdf: bdf, SourceType: "agent", SourceName: "path-agent/nvidia-smi",
			EvidenceId: gliwID("nb", session, seq, "dpa.gpu-binding.v1", uuid, bdf),
			ObservedAt: gliwTimestamp(at), ExpiresAt: gliwTimestamp(exp),
		})
	}

	if c.entries > 0 || c.evidenceRef > 0 {
		ab := &ingestpb.AllocationBatch{
			NodeUid: n.UID, BootId: n.Boot, Session: session, Sequence: seq,
			ObservedAt: gliwTimestamp(at), ExpiresAt: gliwTimestamp(exp), Complete: true,
			Profile: ingestpb.AllocationProfile_NVIDIA_PODRESOURCES_UUID,
		}
		for i := 0; i < c.evidenceRef; i++ {
			ab.EvidenceRefs = append(ab.EvidenceRefs, fmt.Sprintf("ref-%04d", i))
		}
		for i := 0; i < c.entries; i++ {
			ab.Entries = append(ab.Entries, &ingestpb.AllocationEntry{
				ResourceName: "nvidia.com/gpu", DeviceId: "GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
				PodNamespace: "ml", PodName: fmt.Sprintf("pod-%05d", i), ContainerName: "main",
			})
		}
		p.AllocationBatch = ab
	}
	return p
}

// TestGLI051_PayloadCountLimits: exactly the limit is accepted (and the frame is
// fully valid, so the refusal of limit+1 is attributable to the count alone);
// one more is INVALID. Frames are sent gzip-compressed because the larger ones
// exceed 4 MiB uncompressed (GLI-050).
func TestGLI051_PayloadCountLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("builds payloads with thousands of assets, edges and observations; skipped with -short (GLI-125)")
	}
	s := gliStartServer(t)
	type shape struct {
		name  string
		limit gliwCount
		over  gliwCount
	}
	base := gliwCount{assets: 1}
	with := func(f func(c *gliwCount)) gliwCount {
		c := base
		f(&c)
		return c
	}
	shapes := []shape{
		{"assets 4096", with(func(c *gliwCount) { c.assets = 4096 }), with(func(c *gliwCount) { c.assets = 4097 })},
		{"edges and edgeEvidence 8192",
			with(func(c *gliwCount) { c.assets, c.funcs, c.switches, c.parents = 4096, 2048, 2047, 4 }),
			with(func(c *gliwCount) { c.assets, c.funcs, c.switches, c.parents, c.extraEdges = 4096, 2048, 2047, 4, 1 })},
		{"observations 4096",
			with(func(c *gliwCount) { c.assets, c.funcs, c.signals = 2049, 2048, 2 }),
			with(func(c *gliwCount) { c.assets, c.funcs, c.signals, c.extraObs = 2049, 2048, 2, 1 })},
		{"gpuBindings 256",
			with(func(c *gliwCount) { c.assets, c.funcs, c.bindings = 257, 256, 256 }),
			with(func(c *gliwCount) { c.assets, c.funcs, c.bindings = 258, 257, 257 })},
		{"asset aliases 64",
			with(func(c *gliwCount) { c.assets, c.aliases = 2, 64 }),
			with(func(c *gliwCount) { c.assets, c.aliases = 2, 65 })},
		{"observation dimensions 64",
			with(func(c *gliwCount) { c.assets, c.funcs, c.dims = 2, 1, 64 }),
			with(func(c *gliwCount) { c.assets, c.funcs, c.dims = 2, 1, 65 })},
		{"allocation entries 4096",
			with(func(c *gliwCount) { c.entries = 4096 }),
			with(func(c *gliwCount) { c.entries = 4097 })},
		{"allocation evidenceRefs 64",
			with(func(c *gliwCount) { c.entries, c.evidenceRef = 1, 64 }),
			with(func(c *gliwCount) { c.entries, c.evidenceRef = 1, 65 })},
	}
	run := func(t *testing.T, c gliwCount, want ingestpb.AckCode) {
		t.Helper()
		n := s.AddNode(t, "count")
		st, closeFn, h := s.Connect(t, n, grpc.UseCompressor(grpcgzip.Name))
		defer closeFn()
		session := h.GetSession()
		at := gliwAt(0)
		p := gliwCountPayload(n, session, 0, at, c)
		f := gliFrame(t, p, session, 0, at, true)
		f.NodeUid, f.BootId = n.UID, n.Boot
		ack := gliwExchange(t, st, f)
		if ack.GetCode() != want {
			t.Fatalf("ack %v, want %v", ack.GetCode(), want)
		}
		if want == ingestpb.AckCode_INVALID {
			gliwExpectEnd(t, "after INVALID", st, codes.InvalidArgument)
			s.WantNoObservation(t, "rejected frame", n)
			return
		}
		s.WantSnapshot(t, "accepted frame", n, session, 0)
	}
	for _, sh := range shapes {
		sh := sh
		t.Run(sh.name+" accepted", func(t *testing.T) { run(t, sh.limit, ingestpb.AckCode_ACCEPTED) })
		t.Run(sh.name+" plus one is INVALID", func(t *testing.T) { run(t, sh.over, ingestpb.AckCode_INVALID) })
	}
}

// --- GLI-052 rate -------------------------------------------------------------------------

// gliwDrainFrames connects, accepts the baseline and spends the two remaining
// frame tokens on duplicates; the node's frame bucket is then empty.
func gliwDrainFrames(t *testing.T, s *gliServer, n gliwNode) (ingestpb.Ingest_StreamClient, int64, *ingestpb.SnapshotFrame) {
	t.Helper()
	st, _, h := s.Connect(t, n)
	session := h.GetSession()
	f0 := gliwFrame(t, n, session, 0)
	if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
		t.Fatalf("baseline ack %v", ack.GetCode())
	}
	for i := 0; i < 2; i++ {
		if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_DUPLICATE {
			t.Fatalf("duplicate %d ack %v", i+1, ack.GetCode())
		}
	}
	return st, session, f0
}

// TestGLI052_ExampleFromTheSpecification follows GLI-141: three frames pass at
// t=0 and the fourth ends the stream with ResourceExhausted; a reconnect at the
// same instant gets a hello but no frame token; ten seconds later one hello and
// one frame token are available again.
func TestGLI052_ExampleFromTheSpecification(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "rate")
	other := s.AddNode(t, "rateother")

	st, session, _ := gliwDrainFrames(t, s, n)
	// The fourth frame is a perfectly valid new sequence; it must not be processed.
	if err := gliwSend(st, gliwFrame(t, n, session, 1)); err != nil {
		t.Fatalf("send: %v", err)
	}
	gliwExpectEnd(t, "fourth frame", st, codes.ResourceExhausted)
	s.WantSnapshot(t, "fourth frame was not applied", n, session, 0)

	// Another node is not affected.
	ost, _, oh := s.Connect(t, other)
	s.Baseline(t, ost, other, oh.GetSession())

	// Reconnect at the same instant: the hello token exists, the frame token does not.
	st2, _, h2 := s.Connect(t, n)
	if h2.GetSession() <= session {
		t.Fatalf("session %d then %d", session, h2.GetSession())
	}
	if err := gliwSend(st2, gliwFrame(t, n, h2.GetSession(), 0)); err != nil {
		t.Fatalf("send: %v", err)
	}
	gliwExpectEnd(t, "baseline right after reconnecting", st2, codes.ResourceExhausted)
	s.WantNoObservation(t, "reconnect cleared the old observation and the baseline was refused", n)

	// Ten seconds later: hello bucket 1 + 1, frame bucket 0 + 1.
	s.Clock.Advance(10 * time.Second)
	st3, _, h3 := s.Connect(t, n)
	s.Baseline(t, st3, n, h3.GetSession())
	s.WantSnapshot(t, "after ten seconds", n, h3.GetSession(), 0)
	if err := gliwSend(st3, gliwFrame(t, n, h3.GetSession(), 1)); err != nil {
		t.Fatalf("send: %v", err)
	}
	gliwExpectEnd(t, "second frame at that instant", st3, codes.ResourceExhausted)
}

func TestGLI052_RefillBoundary(t *testing.T) {
	s := gliStartServer(t)
	reconnectBaseline := func(t *testing.T, n gliwNode, wantAccepted bool) {
		t.Helper()
		st, _, h := s.Connect(t, n)
		f := gliwFrame(t, n, h.GetSession(), 0)
		if err := gliwSend(st, f); err != nil {
			t.Fatalf("send: %v", err)
		}
		if wantAccepted {
			ack, err := gliwRecvAck(st)
			if err != nil || ack.GetCode() != ingestpb.AckCode_ACCEPTED {
				t.Fatalf("baseline: ack %v err %v, want ACCEPTED", ack, err)
			}
			return
		}
		gliwExpectEnd(t, "baseline before the token is back", st, codes.ResourceExhausted)
	}
	t.Run("9 seconds is not enough", func(t *testing.T) {
		n := s.AddNode(t, "r9")
		gliwDrainFrames(t, s, n)
		s.Clock.Advance(9 * time.Second)
		reconnectBaseline(t, n, false)
	})
	t.Run("10 seconds is enough (the boundary token is usable)", func(t *testing.T) {
		n := s.AddNode(t, "r10")
		gliwDrainFrames(t, s, n)
		s.Clock.Advance(10 * time.Second)
		reconnectBaseline(t, n, true)
	})
	t.Run("a long idle period does not accumulate beyond the burst", func(t *testing.T) {
		n := s.AddNode(t, "rcap")
		st, _, h := s.Connect(t, n)
		f0 := gliwFrame(t, n, h.GetSession(), 0)
		if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("baseline ack %v", ack.GetCode())
		}
		s.Clock.Advance(time.Hour)
		for i := 0; i < 3; i++ {
			if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_DUPLICATE {
				t.Fatalf("resend %d after the idle hour: ack %v", i+1, ack.GetCode())
			}
		}
		if err := gliwSend(st, f0); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "fourth frame after an idle hour", st, codes.ResourceExhausted)
	})
	t.Run("clock regression gives no tokens", func(t *testing.T) {
		n := s.AddNode(t, "rback")
		st, _, _ := gliwDrainFrames(t, s, n)
		s.Clock.Advance(-time.Hour)
		f := gliwFrame(t, n, 1, 0)
		if err := gliwSend(st, f); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "frame after the clock went backwards", st, codes.ResourceExhausted)
	})
}

// TestGLI052_ConfiguredBuckets: narrower limits are honoured.
func TestGLI052_ConfiguredBuckets(t *testing.T) {
	t.Run("FrameBurst 1", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{FrameBurst: 1, FrameInterval: 20 * time.Second}))
		n := s.AddNode(t, "fb1")
		st, _, h := s.Connect(t, n)
		f0 := gliwFrame(t, n, h.GetSession(), 0)
		if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("baseline ack %v", ack.GetCode())
		}
		if err := gliwSend(st, f0); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "second frame with FrameBurst 1", st, codes.ResourceExhausted)
		// A 20 s interval: after 10 s still no token, after 20 s one.
		s.Clock.Advance(10 * time.Second)
		st2, _, h2 := s.Connect(t, n)
		if err := gliwSend(st2, gliwFrame(t, n, h2.GetSession(), 0)); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "baseline after 10 s of a 20 s interval", st2, codes.ResourceExhausted)
	})
	t.Run("HelloBurst 1", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{HelloBurst: 1}))
		n := s.AddNode(t, "hb1")
		_, closeFn, _ := s.Connect(t, n)
		closeFn()
		st, closeFn2 := s.Stream(t, n.Cert)
		defer closeFn2()
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "second hello with HelloBurst 1", err, codes.ResourceExhausted)
	})
}

func TestGLI052_HelloBucket(t *testing.T) {
	s := gliStartServer(t)
	hello := func(t *testing.T, n gliwNode) error {
		t.Helper()
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n))
		return err
	}
	t.Run("three then the fourth is refused", func(t *testing.T) {
		n := s.AddNode(t, "h4")
		for i := 0; i < 3; i++ {
			if err := hello(t, n); err != nil {
				t.Fatalf("hello %d: %v", i+1, err)
			}
		}
		gliwWantCode(t, "fourth hello", hello(t, n), codes.ResourceExhausted)
		if c := s.Sessions.CallsFor(n.UID); len(c) != 3 {
			t.Errorf("store calls = %d, want 3", len(c))
		}
	})
	t.Run("9 seconds is not enough", func(t *testing.T) {
		n := s.AddNode(t, "h9")
		for i := 0; i < 3; i++ {
			if err := hello(t, n); err != nil {
				t.Fatalf("hello %d: %v", i+1, err)
			}
		}
		s.Clock.Advance(9 * time.Second)
		gliwWantCode(t, "hello 9 s later", hello(t, n), codes.ResourceExhausted)
	})
	t.Run("10 seconds is enough and exactly one token is back", func(t *testing.T) {
		n := s.AddNode(t, "h10")
		for i := 0; i < 3; i++ {
			if err := hello(t, n); err != nil {
				t.Fatalf("hello %d: %v", i+1, err)
			}
		}
		s.Clock.Advance(10 * time.Second)
		if err := hello(t, n); err != nil {
			t.Fatalf("hello 10 s later: %v", err)
		}
		gliwWantCode(t, "second hello at that instant", hello(t, n), codes.ResourceExhausted)
	})
	t.Run("buckets are per node", func(t *testing.T) {
		a, b := s.AddNode(t, "ha"), s.AddNode(t, "hb")
		for i := 0; i < 3; i++ {
			if err := hello(t, a); err != nil {
				t.Fatalf("hello %d: %v", i+1, err)
			}
		}
		gliwWantCode(t, "node A, fourth hello", hello(t, a), codes.ResourceExhausted)
		if err := hello(t, b); err != nil {
			t.Fatalf("node B was affected by node A's bucket: %v", err)
		}
	})
	t.Run("clock regression gives no tokens", func(t *testing.T) {
		n := s.AddNode(t, "hback")
		for i := 0; i < 3; i++ {
			if err := hello(t, n); err != nil {
				t.Fatalf("hello %d: %v", i+1, err)
			}
		}
		s.Clock.Advance(-time.Hour)
		gliwWantCode(t, "hello after the clock went backwards", hello(t, n), codes.ResourceExhausted)
	})
}

// --- GLI-053 retention ---------------------------------------------------------------------

func TestGLI053_NodeRetention(t *testing.T) {
	const retention = 24 * time.Hour
	t.Run("default retention is 24 hours from the last accepted frame", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "ret")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.Clock.Advance(retention - time.Second)
		s.WantObserved(t, "one second before retention ends", n)
		s.Clock.Advance(2 * time.Second)
		s.WantNoObservation(t, "one second after retention ended", n)
		// The node starts over with a new hello.
		_, _, h2 := s.Connect(t, n)
		if h2.GetSession() <= h.GetSession() {
			t.Errorf("sessions %d then %d", h.GetSession(), h2.GetSession())
		}
		s.WantNoObservation(t, "new hello, before its baseline", n)
	})
	t.Run("an accepted frame restarts the period", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "retframe")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.Clock.Advance(12 * time.Hour)
		if ack := gliwExchange(t, st, gliwFrame(t, n, h.GetSession(), 1)); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("sequence 1: ack %v", ack.GetCode())
		}
		s.Clock.Advance(23*time.Hour + 59*time.Minute)
		s.WantObserved(t, "23h59m after the last accepted frame (35h59m after the baseline)", n)
	})
	t.Run("a duplicate does not restart the period", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "retdup")
		st, _, h := s.Connect(t, n)
		f0 := gliwFrame(t, n, h.GetSession(), 0)
		if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("baseline ack %v", ack.GetCode())
		}
		s.Clock.Advance(23 * time.Hour)
		if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_DUPLICATE {
			t.Fatalf("resend ack %v", ack.GetCode())
		}
		s.Clock.Advance(time.Hour + time.Second)
		s.WantNoObservation(t, "24h1s after the baseline although a duplicate arrived at 23h", n)
	})
	t.Run("retention uses the server clock and not the query time", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "retq")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		q := s.Query(n, false)
		q.Now = s.Clock.Now().Add(48 * time.Hour)
		if _, err := s.Server.BundleSource().NodeBundles(context.Background(), q); err != nil {
			t.Fatalf("query time 48h ahead with an unmoved server clock: %v, want an observation", err)
		}
		s.Clock.Advance(25 * time.Hour)
		q = s.Query(n, false)
		q.Now = gliwT0
		if _, err := s.Server.BundleSource().NodeBundles(context.Background(), q); err == nil {
			t.Fatalf("an old query time kept a node alive 25h after its last frame")
		} else if !isNoObservation(err) {
			t.Fatalf("error %v, want app.ErrNoObservation", err)
		}
	})
	t.Run("configured retention", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{NodeRetention: 10 * time.Minute}))
		n := s.AddNode(t, "retcfg")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.Clock.Advance(10*time.Minute - time.Second)
		s.WantObserved(t, "one second before the configured retention ends", n)
		s.Clock.Advance(2 * time.Second)
		s.WantNoObservation(t, "one second after the configured retention ended", n)
	})
	t.Run("another node is not affected", func(t *testing.T) {
		s := gliStartServer(t)
		a, b := s.AddNode(t, "reta"), s.AddNode(t, "retb")
		sa, _, ha := s.Connect(t, a)
		s.Baseline(t, sa, a, ha.GetSession())
		s.Clock.Advance(20 * time.Hour)
		sb, _, hb := s.Connect(t, b)
		s.Baseline(t, sb, b, hb.GetSession())
		s.Clock.Advance(5 * time.Hour)
		s.WantNoObservation(t, "node A, 25h after its frame", a)
		s.WantObserved(t, "node B, 5h after its frame", b)
	})
}

func isNoObservation(err error) bool { return errors.Is(err, app.ErrNoObservation) }
