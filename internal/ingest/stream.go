package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/internal/transport/mtls"
)

// errRole is the identity failure of a certificate whose role is not node.
var errRole = errors.New("ingest: role not allowed")

// identityOf reads the identity of the authenticated client from the TLS state
// of the stream (GLI-021): the leaf certificate must carry a valid identity
// URI and the client authentication usage, and the role must be node. It
// returns the identity and the end of the leaf's validity.
func identityOf(ctx context.Context) (mtls.Identity, time.Time, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return mtls.Identity{}, time.Time{}, mtls.ErrInvalidIdentity
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.PeerCertificates) == 0 {
		return mtls.Identity{}, time.Time{}, mtls.ErrInvalidIdentity
	}
	leaf := info.State.PeerCertificates[0]
	id, err := mtls.ClientIdentity(leaf)
	if err != nil {
		return mtls.Identity{}, time.Time{}, err
	}
	if id.Role != mtls.RoleNode {
		return mtls.Identity{}, time.Time{}, errRole
	}
	return id, leaf.NotAfter, nil
}

// received is one message of the stream, or the end of it.
type received struct {
	msg  *ingestpb.ClientFrame
	wire int // payload length on the wire
	err  error
}

// receiver reads the messages of one stream in a goroutine of its own, one at
// a time and only when asked (so a stream never buffers a message ahead of the
// one being processed), which lets the handler wait on its timers and on the
// fence at the same time. The goroutine is owned by the handler: it ends when
// done is closed or the stream breaks, and Serve waits for it.
type receiver struct {
	want chan struct{}
	got  chan received
	done chan struct{}
}

func (s *Server) startReceiver(stream ingestpb.Ingest_StreamServer, state *rpcState) *receiver {
	r := &receiver{want: make(chan struct{}), got: make(chan received), done: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-r.want:
			case <-r.done:
				return
			}
			m := new(ingestpb.ClientFrame)
			err := stream.RecvMsg(m)
			rec := received{msg: m, wire: state.take(), err: err}
			select {
			case r.got <- rec:
			case <-r.done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

// next asks the receiver for one more message.
func (r *receiver) next() {
	select {
	case r.want <- struct{}{}:
	case <-r.done:
	}
}

// handle serves one Ingest.Stream call (GLI-030, GLI-037, GLI-040).
func (s *Server) handle(stream ingestpb.Ingest_StreamServer) error {
	ctx := stream.Context()

	// (1) identity, before anything is said about leadership or capacity.
	id, notAfter, err := identityOf(ctx)
	if err != nil {
		st := identityStatus(err)
		s.log.Info("stream rejected", slog.String("event", "identity"), slog.String("code", codeClass(st)))
		return st
	}
	log := s.log.With(slog.String("cluster", id.ClusterID), slog.String("node_uid", id.Subject))
	n := s.streams.Add(1)
	defer s.streams.Add(-1)

	select {
	case <-s.stopping:
		return status.Error(codes.Unavailable, msgStopping)
	default:
	}
	// (2) leadership and capacity.
	if !s.engine.Leading() {
		log.Info("hello rejected", slog.String("code", codes.Unavailable.String()), slog.String("class", liveingest.ClassNotLeader))
		return status.Error(codes.Unavailable, msgNotLeader)
	}
	if n > int64(s.cfg.Limits.MaxStreams) {
		log.Info("hello rejected", slog.String("code", codes.ResourceExhausted.String()), slog.String("class", "max_streams"))
		return status.Error(codes.ResourceExhausted, msgTooManyStreams)
	}

	rx := s.startReceiver(stream, stateOf(ctx))
	defer close(rx.done)

	helloTimer := time.NewTimer(s.cfg.Limits.HelloTimeout)
	defer helloTimer.Stop()
	// The certificate lifetime is a real-time bound of the stream (GLI-024).
	expiry := time.NewTimer(time.Until(notAfter))
	defer expiry.Stop()

	// (3) the first message, within HelloTimeout.
	rx.next()
	var first received
	select {
	case first = <-rx.got:
	case <-helloTimer.C:
		return s.end(log, status.Error(codes.DeadlineExceeded, msgHelloTimeout), "hello_timeout")
	case <-expiry.C:
		return s.end(log, status.Error(codes.Unauthenticated, msgCertExpired), liveingest.ClassCertExpired)
	case <-s.stopping:
		return status.Error(codes.Unavailable, msgStopping)
	case <-ctx.Done():
		return status.Error(codes.Canceled, msgCanceled)
	}
	if st := s.checkReceived(ctx, first); st != nil {
		if errors.Is(st, io.EOF) {
			return nil
		}
		return s.end(log, st, "recv")
	}
	hello := first.msg.GetHello()
	switch {
	case hello == nil:
		// Another message, or an empty one: not a hello.
		return s.end(log, status.Error(codes.InvalidArgument, msgInvalidHello), "hello_order")
	case hasUnknown(first.msg) || hasUnknown(hello) || hello.GetVersion() != liveingest.WireVersion:
		return s.end(log, status.Error(codes.InvalidArgument, msgInvalidHello), "hello_format")
	}

	// (4) to (9). The hello context ends with the stream and with Serve.
	helloCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.runCtx, cancel)
	h, err := s.engine.Hello(helloCtx, liveingest.HelloInput{
		CertClusterID: id.ClusterID,
		CertNodeUID:   id.Subject,
		NotAfter:      notAfter,
		ClusterID:     hello.GetClusterId(),
		NodeName:      hello.GetNodeName(),
		NodeUID:       hello.GetNodeUid(),
		BootID:        hello.GetBootId(),
	})
	stop()
	cancel()
	if err != nil {
		r := asReject(err)
		st := rejectStatus(r)
		switch {
		case s.stopped():
			st = status.Error(codes.Unavailable, msgStopping)
		case ctx.Err() != nil:
			st = status.Error(codes.Canceled, msgCanceled)
		case certExpired(expiry):
			st = status.Error(codes.Unauthenticated, msgCertExpired)
		}
		return s.end(log, st, r.Class)
	}
	defer h.Release()
	log = log.With(slog.Int64("session", h.Session()))

	// (10) ServerHello.
	if err := stream.Send(&ingestpb.ServerFrame{Frame: &ingestpb.ServerFrame_Hello{Hello: &ingestpb.ServerHello{
		Session:            h.Session(),
		CollectorProfileId: s.cfg.CollectorProfileID,
	}}}); err != nil {
		return s.end(log, status.Error(codes.Unavailable, msgUnavailable), "send")
	}
	log.Info("hello accepted")

	// The frames.
	idle := time.NewTimer(s.cfg.Limits.IdleTimeout)
	defer idle.Stop()
	for {
		rx.next()
		idle.Reset(s.cfg.Limits.IdleTimeout)
		var m received
		select {
		case m = <-rx.got:
		case <-idle.C:
			r := h.OnIdle()
			return s.end(log, rejectStatus(r), r.Class)
		case <-expiry.C:
			return s.end(log, status.Error(codes.Unauthenticated, msgCertExpired), liveingest.ClassCertExpired)
		case <-h.Done():
			r := h.Cause()
			return s.end(log, rejectStatus(r), r.Class)
		case <-s.stopping:
			return s.end(log, status.Error(codes.Unavailable, msgStopping), "stopping")
		case <-ctx.Done():
			return s.end(log, status.Error(codes.Canceled, msgCanceled), liveingest.ClassCanceled)
		}
		if st := s.checkReceived(ctx, m); st != nil {
			if errors.Is(st, io.EOF) {
				log.Info("stream ended", slog.String("code", codes.OK.String()))
				return nil
			}
			return s.end(log, st, "recv")
		}
		if st := s.frame(log, stream, h, m.msg); st != nil {
			return st
		}
	}
}

// certExpired reports whether the real-time certificate timer has fired, that
// is whether the end of the leaf's validity has passed (GLI-024).
func certExpired(expiry *time.Timer) bool {
	select {
	case <-expiry.C:
		return true
	default:
		return false
	}
}

// stopped reports whether Serve was cancelled.
func (s *Server) stopped() bool {
	select {
	case <-s.stopping:
		return true
	default:
		return false
	}
}

// checkReceived turns a failed or oversized receive into the status that ends
// the stream. A client that closed its side is io.EOF; the codec-stage
// failures keep the transport's code (GLI-046 (a)); a message whose payload on
// the wire is over 4 MiB ends the stream with ResourceExhausted (GLI-050).
func (s *Server) checkReceived(ctx context.Context, m received) error {
	if m.err != nil {
		if errors.Is(m.err, io.EOF) {
			return io.EOF
		}
		return recvStatus(ctx, m.err)
	}
	if m.wire > maxWirePayload {
		return status.Error(codes.ResourceExhausted, msgMessageTooLarge)
	}
	return nil
}

// frame processes one received message after the hello (GLI-040) and returns
// the status that ends the stream, or nil to continue.
func (s *Server) frame(log *slog.Logger, stream ingestpb.Ingest_StreamServer, h *liveingest.Handle, cf *ingestpb.ClientFrame) error {
	// F1 wrapper: an empty message and a second hello end the stream (no ack).
	sf := cf.GetSnapshot()
	if sf == nil {
		msg := msgEmptyFrame
		if cf.GetHello() != nil {
			msg = msgUnexpectedHello
		}
		return s.end(log, status.Error(codes.InvalidArgument, msg), "frame_wrapper")
	}
	// F1 leadership, certificate lifetime, rate.
	if err := h.Begin(); err != nil {
		r := asReject(err)
		return s.end(log, rejectStatus(r), r.Class)
	}
	// F2 to F7.
	res, err := h.Submit(frameFromProto(cf, sf))
	if err != nil {
		r := asReject(err)
		return s.end(log, rejectStatus(r), r.Class)
	}
	if res.Ack != liveingest.AckNone {
		code, msg := ackCodeOf(res.Ack)
		if err := stream.Send(&ingestpb.ServerFrame{Frame: &ingestpb.ServerFrame_Ack{Ack: &ingestpb.SnapshotAck{
			Code:                 code,
			AcceptedSession:      h.Session(),
			ExpectedNextSequence: res.ExpectedNext,
			Message:              msg,
		}}}); err != nil {
			return s.end(log, status.Error(codes.Unavailable, msgUnavailable), "send")
		}
	}
	log.Debug("frame", slog.Uint64("sequence", sf.GetSequence()), slog.String("ack", ackName(res.Ack)), slog.String("class", res.Class), slog.Int("size", res.Size))
	if res.End != liveingest.EndNone {
		return s.end(log, endStatus(res), res.Class)
	}
	return nil
}

// ackName names an acknowledgement outcome for the logs.
func ackName(a liveingest.AckCode) string {
	c, _ := ackCodeOf(a)
	return c.String()
}

// end logs how a stream ends and returns its status.
func (s *Server) end(log *slog.Logger, st error, class string) error {
	log.Info("stream ended", slog.String("code", codeClass(st)), slog.String("class", class))
	return st
}
