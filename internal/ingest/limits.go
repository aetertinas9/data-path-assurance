package ingest

import (
	"context"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/stats"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// The two independent message bounds of GLI-050. gRPC's own receive bound
// (maxDecompressed) applies to the decompressed length and, because the
// transport also compares it with the length prefix before it reads the body,
// to the largest message it buffers. The wire bound is checked by the adapter
// after the receive, from the length the stats handler recorded.
//
// Receive resources (GLI-054). The 4 MiB check is a verdict on a message that
// was already received, buffered, decompressed and decoded: it is not a bound
// on memory. What bounds memory is, per message, the transport's 16 MiB
// (plus up to 16 MiB of decompressed result); per stream, one message at a time
// (the receiver reads only when the handler asks, so nothing is read ahead);
// across streams, the frame rate bucket of the node and MaxStreams. The worst
// case, MaxStreams streams each holding a maximum message, is a calculation
// that nothing in this package measures and that the default MaxStreams does
// not make small.
const (
	maxWirePayload  = 4 << 20  // 4 MiB: the payload bytes on the wire, compressed or not
	maxDecompressed = 16 << 20 // 16 MiB: the message after decompression
)

// The call limits of the ports (GLI-030 (7), (8)): real-time bounds that the
// engine, which owns no timer, relies on the adapter to put on every call.
const (
	nodeLookupTimeout = 5 * time.Second
	sessionTimeout    = 20 * time.Second
)

// rpcState is what the stats handler keeps per stream: the payload length of
// the message that was received last.
type rpcState struct{ wire atomic.Int64 }

// take returns the wire payload length of the message received last.
func (s *rpcState) take() int {
	return int(s.wire.Swap(0))
}

type rpcStateKey struct{}

// statsHandler records, for every message a stream receives, the length of
// its payload on the wire. gRPC calls HandleRPC from inside the receive of the
// message, in the receiving goroutine, before the receive returns.
type statsHandler struct{}

func (statsHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return context.WithValue(ctx, rpcStateKey{}, &rpcState{})
}

func (statsHandler) HandleRPC(ctx context.Context, st stats.RPCStats) {
	in, ok := st.(*stats.InPayload)
	if !ok {
		return
	}
	if s, ok := ctx.Value(rpcStateKey{}).(*rpcState); ok {
		s.wire.Store(int64(in.CompressedLength))
	}
}

func (statsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

func (statsHandler) HandleConn(context.Context, stats.ConnStats) {}

// stateOf returns the stats state of a stream context, or a detached one when
// the handler did not tag it (then no wire bound is seen).
func stateOf(ctx context.Context) *rpcState {
	if s, ok := ctx.Value(rpcStateKey{}).(*rpcState); ok {
		return s
	}
	return &rpcState{}
}

// boundedNodes puts the call limit of GLI-030 (7) on a NodeDirectory.
type boundedNodes struct{ inner liveingest.NodeDirectory }

func (b boundedNodes) GetNode(ctx context.Context, name string) (liveingest.NodeInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, nodeLookupTimeout)
	defer cancel()
	return b.inner.GetNode(ctx, name)
}

// boundedSessions puts the call limit of GLI-030 (8) on a SessionStore.
type boundedSessions struct{ inner liveingest.SessionStore }

func (b boundedSessions) AllocateSession(ctx context.Context, node fleet.NodeRef, after int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()
	return b.inner.AllocateSession(ctx, node, after)
}

// systemClock is the Clock of a server that was given none.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
