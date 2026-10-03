package liveingest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// HelloInput is what the engine needs to decide a hello: the identity the
// client certificate proved, and the fields of the ClientHello. The version
// and the unknown-field rules of the wire belong to the adapter.
type HelloInput struct {
	// CertClusterID and CertNodeUID come from the identity of the client
	// certificate, NotAfter from its leaf. A zero NotAfter means no lifetime
	// limit is applied to the frames of the stream.
	CertClusterID, CertNodeUID string
	NotAfter                   time.Time

	// The fields of ClientHello.
	ClusterID, NodeName, NodeUID, BootID string
}

// Handle is the engine's side of one authenticated stream after a successful
// hello: the node, the session, and the means to know that the stream was
// superseded or lost its leadership. The adapter selects on Done; whatever
// the adapter does, a frame of a stream that is no longer the node's active
// one never reaches the node's state (the fence is checked under the node's
// writer lock, not by the channel).
type Handle struct {
	e *Engine
	n *node

	nodeUID, nodeName, bootID string
	session                   int64
	notAfter                  time.Time

	done  chan struct{}
	once  sync.Once
	cause *Reject // written before done is closed
}

// Session returns the session the hello allocated.
func (h *Handle) Session() int64 { return h.session }

// Done is closed when the stream must end because the engine superseded it
// (another hello of the same node succeeded) or lost its leadership.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Cause returns how the stream must end. It is meaningful after Done is
// closed.
func (h *Handle) Cause() *Reject {
	select {
	case <-h.done:
		return h.cause
	default:
		return reject(EndAborted, ClassSuperseded)
	}
}

// finish ends the handle once, with the first cause.
func (h *Handle) finish(r *Reject) {
	h.once.Do(func() {
		h.cause = r
		close(h.done)
	})
}

// Release is called when the stream's handler returns. It frees the node's
// active slot (the observation view stays until retention, GLI-053).
func (h *Handle) Release() {
	h.n.mu.Lock()
	if h.n.active == h {
		h.n.active = nil
	}
	h.n.mu.Unlock()
	h.finish(reject(EndAborted, ClassCanceled))
}

// OnIdle is called when the idle timeout of the stream expired. It checks
// leadership at that point (GLI-037): a lost leadership ends the stream with
// Unavailable, otherwise with DeadlineExceeded.
func (h *Handle) OnIdle() *Reject {
	if !h.e.Leading() {
		return reject(EndUnavailable, ClassNotLeader)
	}
	return reject(EndDeadlineExceeded, ClassIdle)
}

// canceled returns the reject of a hello whose context is done (the stream
// ended or the server stops); that is not a failure of a port. It returns nil
// while the context is live.
func canceled(ctx context.Context) *Reject {
	if ctx.Err() != nil {
		return reject(EndUnavailable, ClassCanceled)
	}
	return nil
}

// Hello decides a ClientHello after the adapter has authenticated the
// stream, counted it against its stream limit, checked leadership and read the
// hello (GLI-030 (1)-(3)). It performs steps (4) to (9): the hello field
// format, the binding of certificate, hello and cluster, the node's hello
// bucket, the Node lookup, the session allocation and, last, the commit that
// fences the node's previous stream and drops its previous observations. A
// failed step has no effect on any state. The error is a *Reject.
//
// The same node's hellos run one at a time. ctx must be cancelled when the
// server stops or the stream ends; the ports must honour it.
func (e *Engine) Hello(ctx context.Context, in HelloInput) (*Handle, error) {
	// (4) field format
	if !ValidClusterID(in.ClusterID) || !ValidNodeName(in.NodeName) ||
		!ValidIdentifier(in.NodeUID, 1, maxIDBytes) || !ValidBootID(in.BootID) {
		return nil, reject(EndInvalidArgument, "hello_format")
	}
	// (5) binding: certificate, hello and the controller's cluster agree
	if in.CertClusterID != in.ClusterID || in.ClusterID != e.cfg.ClusterID || in.CertNodeUID != in.NodeUID {
		return nil, reject(EndPermissionDenied, ClassScope)
	}
	// (6) hello bucket
	now := e.cfg.Clock.Now()
	e.sweepIfDue(now)
	n := e.node(in.NodeUID)
	if !n.takeHello(now, e.cfg.HelloBurst, e.cfg.HelloInterval) {
		return nil, reject(EndResourceExhausted, ClassHelloRate)
	}

	// (7)-(9) run one at a time for the node.
	select {
	case n.helloSem <- struct{}{}:
	case <-ctx.Done():
		return nil, reject(EndUnavailable, ClassCanceled)
	}
	defer func() { <-n.helloSem }()

	// (7) the Node must exist and carry the UID of the certificate; a missing
	// Node and a different UID are indistinguishable to the peer.
	info, err := e.cfg.Nodes.GetNode(ctx, in.NodeName)
	switch {
	case errors.Is(err, ErrNodeNotFound):
		return nil, reject(EndPermissionDenied, ClassScope)
	case err != nil:
		if r := canceled(ctx); r != nil {
			return nil, r
		}
		return nil, reject(EndUnavailable, ClassDirectory)
	case info.UID != in.CertNodeUID:
		return nil, reject(EndPermissionDenied, ClassScope)
	}

	// (8) the session. Leadership is checked once more right before the write
	// the store makes, and the epoch fixes which leadership the commit belongs
	// to.
	if !e.Leading() {
		return nil, reject(EndUnavailable, ClassNotLeader)
	}
	epoch := e.epoch.Load()
	session, err := e.cfg.Sessions.AllocateSession(ctx, fleet.NodeRef{ClusterID: e.cfg.ClusterID, Name: in.NodeName, UID: in.NodeUID}, n.after)
	switch {
	case errors.Is(err, ErrNodeNotManaged):
		return nil, reject(EndFailedPrecondition, ClassNodeNotManaged)
	case errors.Is(err, ErrSessionOverflow):
		return nil, reject(EndResourceExhausted, ClassSessionOverflow)
	case errors.Is(err, ErrSessionConflict):
		return nil, reject(EndAborted, ClassSessionConflict)
	case err != nil:
		if r := canceled(ctx); r != nil {
			return nil, r
		}
		return nil, reject(EndUnavailable, ClassStoreUnavailable)
	case session < 1 || session <= n.after:
		// [defensive] a store that breaks the allocation contract
		return nil, reject(EndInternal, ClassSessionInvalid)
	}
	n.after = session

	// (9) commit: fence the previous stream and drop the previous observations.
	if r := canceled(ctx); r != nil {
		return nil, r
	}
	h := &Handle{
		e: e, n: n,
		nodeUID: in.NodeUID, nodeName: in.NodeName, bootID: in.BootID,
		session:  session,
		notAfter: in.NotAfter,
		done:     make(chan struct{}),
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if e.epoch.Load() != epoch {
		return nil, reject(EndUnavailable, ClassNotLeader)
	}
	if old := n.active; old != nil {
		old.finish(reject(EndAborted, ClassSuperseded))
	}
	n.active = h
	n.view.Store(nil)
	return h, nil
}
