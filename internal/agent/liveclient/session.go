package liveclient

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"github.com/aetertinas9/data-path-assurance/internal/agent"
	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

const (
	// maxCycleFailures is how many consecutive cycles may end without a frame
	// the controller acknowledged before the stream is closed (GLI-083 (3)).
	maxCycleFailures = 5

	// endWait bounds how long a failed send waits for the status that ended the
	// stream; the status is only logged.
	endWait = time.Second

	// offlineProfilePrefix marks the profile ID of an offline artifact, which a
	// live ServerHello never carries (GLI-083 (2)).
	offlineProfilePrefix = "offline:"
)

// Fixed class words of a cycle or session that ends (GLI-101).
const (
	classHelloTimeout   = "hello_timeout"
	classHelloInvalid   = "hello_invalid"
	classAckTimeout     = "ack_timeout"
	classAckInvalid     = "ack_invalid"
	classAckRejected    = "ack_rejected"
	classUnexpectedMsg  = "unexpected_message"
	classSendFailed     = "send_failed"
	classCollect        = "collect_failed"
	classCollectBound   = "collect_bound"
	classPartialBase    = "partial_baseline"
	classClockNotAdvanc = "clock_not_advancing"
	classCycleFailures  = "cycle_failures"
	classInvalidFrame   = "frame_invalid"
)

// recvItem is one message or the terminal error of the receive side.
type recvItem struct {
	msg *ingestpb.ServerFrame
	err error
}

// session is the state of one stream (GLI-083): the frames are sent one at a
// time, each waits for its ack. It is used by the goroutine that runs it.
type session struct {
	c      *client
	stream ingestpb.Ingest_StreamClient
	msgs   <-chan recvItem
	// cancel ends the stream; the timers of the hello and of a frame call it.
	cancel   context.CancelFunc
	log      *slog.Logger
	bootID   string
	id       int64
	profile  string
	sequence uint64
	// prev is the observed_at of the last frame that was sent in this session.
	prev    time.Time
	hasPrev bool
	// accepted becomes true at the first ACCEPTED ack; code is the status the
	// stream ended with when it is known.
	accepted bool
	code     codes.Code
	failures int
}

// session opens the stream on conn, exchanges the hello and sends frames until
// the stream ends, a frame is rejected or ctx is cancelled.
func (c *client) session(ctx context.Context, conn *grpc.ClientConn, bootID string) attemptResult {
	sctx, cancel := context.WithCancel(ctx)
	// The timer covers the stream open and the wait for the ServerHello. The
	// stream context has no deadline of its own: a session lives as long as the
	// stream does.
	var helloTimedOut atomic.Bool
	helloTimer := time.AfterFunc(c.o.HelloTimeout, func() {
		helloTimedOut.Store(true)
		cancel()
	})

	stream, err := ingestpb.NewIngestClient(conn).Stream(sctx, grpc.UseCompressor(compressorName))
	if err != nil {
		helloTimer.Stop()
		cancel()
		class := classStreamOpen
		if helloTimedOut.Load() {
			class = classHelloTimeout
		}
		c.log.Warn("stream open failed", "event", "session_failed", "class", class, "code", codeOf(err).String())
		return attemptResult{code: codeOf(err)}
	}

	msgs := make(chan recvItem)
	finished := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	// The reader owns the receive side of the stream. It ends when Recv fails,
	// which cancelling sctx forces. It delivers every message, the terminal
	// error included, until the session function has returned (finished), so a
	// timer that cancels the stream cannot swallow the status the session is
	// waiting for.
	go func() {
		defer readers.Done()
		for {
			msg, err := stream.Recv()
			select {
			case msgs <- recvItem{msg: msg, err: err}:
			case <-finished:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		close(finished)
		helloTimer.Stop()
		cancel()
		readers.Wait()
	}()

	s := &session{c: c, stream: stream, msgs: msgs, cancel: cancel, log: c.log, bootID: bootID}
	if !s.hello(ctx, helloTimer, &helloTimedOut) {
		return attemptResult{accepted: s.accepted, code: s.code}
	}
	s.run(ctx)
	return attemptResult{accepted: s.accepted, code: s.code}
}

// hello sends the ClientHello and waits for a valid ServerHello (GLI-083 (1),
// (2)). It reports whether the session is established.
func (s *session) hello(ctx context.Context, helloTimer *time.Timer, timedOut *atomic.Bool) bool {
	hello := &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: &ingestpb.ClientHello{
		Version:   liveingest.WireVersion,
		ClusterId: s.c.o.ClusterID,
		NodeName:  s.c.o.NodeName,
		NodeUid:   s.c.o.NodeUID,
		BootId:    s.bootID,
	}}}
	if err := s.stream.Send(hello); err != nil {
		s.code = s.endCode(ctx)
		class := classSendFailed
		if timedOut.Load() {
			class = classHelloTimeout
		}
		s.log.Warn("hello failed", "event", "session_failed", "class", class, "code", s.code.String())
		return false
	}
	var it recvItem
	select {
	case it = <-s.msgs:
	case <-ctx.Done():
		return false
	}
	if it.err != nil {
		s.code = codeOf(it.err)
		class := classStreamEnd
		if timedOut.Load() {
			class = classHelloTimeout
		}
		s.log.Warn("hello failed", "event", "session_failed", "class", class, "code", s.code.String())
		return false
	}
	if !helloTimer.Stop() {
		s.log.Warn("hello failed", "event", "session_failed", "class", classHelloTimeout, "code", codes.DeadlineExceeded.String())
		return false
	}
	h := it.msg.GetHello()
	if h == nil || h.GetSession() <= 0 || !liveingest.ValidIdentifier(h.GetCollectorProfileId(), 1, 128) ||
		strings.HasPrefix(h.GetCollectorProfileId(), offlineProfilePrefix) {
		s.log.Warn("hello failed", "event", "session_failed", "class", classHelloInvalid)
		return false
	}
	s.id = h.GetSession()
	s.profile = h.GetCollectorProfileId()
	s.log.Info("session established", "event", "session", "session", s.id)
	return true
}

// endCode waits briefly for the status that ended the stream, for the log. A
// message that arrives instead is dropped.
func (s *session) endCode(ctx context.Context) codes.Code {
	t := time.NewTimer(endWait)
	defer t.Stop()
	for {
		select {
		case it := <-s.msgs:
			if it.err != nil {
				return codeOf(it.err)
			}
		case <-t.C:
			return codes.Unknown
		case <-ctx.Done():
			return codes.Canceled
		}
	}
}

// outcome is how one cycle ended.
type outcome int

const (
	// sent: the frame was acknowledged ACCEPTED or DUPLICATE.
	sent outcome = iota
	// skipped: no frame was sent; the cycle counts as a failure.
	skipped
	// ended: the session is over (stream error, rejected ack, ack timeout,
	// shutdown).
	ended
)

// run is the frame loop. A cycle starts every Interval on a real timer, the
// first one at once; a cycle that takes longer than Interval is followed by the
// next one without a wait (GLI-082).
func (s *session) run(ctx context.Context) {
	var next *time.Timer
	defer func() {
		if next != nil {
			next.Stop()
		}
	}()
	for {
		if next != nil {
			select {
			case <-next.C:
			case it := <-s.msgs:
				s.unexpected(it)
				return
			case <-ctx.Done():
				return
			}
		}
		next = time.NewTimer(s.c.o.Interval)
		switch s.cycle(ctx) {
		case ended:
			return
		case skipped:
			s.failures++
			if s.failures >= maxCycleFailures {
				s.log.Warn("session closed", "event", "session_closed", "class", classCycleFailures)
				return
			}
		case sent:
			s.failures = 0
		}
	}
}

// unexpected records a message or status that arrives while no frame is
// outstanding: a server that closes the stream, or a message that is not an
// answer to a frame.
func (s *session) unexpected(it recvItem) {
	if it.err != nil {
		s.code = codeOf(it.err)
		s.log.Info("session closed", "event", "session_closed", "class", classStreamEnd, "code", s.code.String())
		return
	}
	s.log.Warn("session closed", "event", "session_closed", "class", classUnexpectedMsg)
}

// cycle collects the host once and sends the frame (GLI-081, GLI-082).
func (s *session) cycle(ctx context.Context) outcome {
	observedAt := s.c.o.Clock.Now().UTC()
	if s.hasPrev && !observedAt.After(s.prev) {
		s.log.Warn("cycle skipped", "event", "cycle_skipped", "class", classClockNotAdvanc, "sequence", s.sequence)
		return skipped
	}

	root, err := openSysfsRoot(s.c.o.SysfsRoot)
	if err != nil {
		root = nil // the collector turns a missing root into a PARTIAL frame
	}
	lf, err := agent.CollectLive(ctx, root, s.c.o.NodeUID, s.c.o.NVIDIASMI)
	if root != nil {
		// The root was only read, so a close failure loses nothing.
		_ = root.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ended
		}
		class := classCollect
		if errors.Is(err, agent.ErrBoundExceeded) {
			class = classCollectBound
		}
		s.log.Warn("cycle skipped", "event", "cycle_skipped", "class", class, "sequence", s.sequence)
		return skipped
	}
	s.logDiagnostics(lf)
	if s.sequence == 0 && !lf.Complete {
		s.log.Warn("cycle skipped", "event", "cycle_skipped", "class", classPartialBase, "sequence", s.sequence)
		return skipped
	}

	frame, err := buildFrame(lf, frameParams{
		nodeUID:    s.c.o.NodeUID,
		bootID:     s.bootID,
		session:    s.id,
		sequence:   s.sequence,
		observedAt: observedAt,
		ttl:        s.c.o.EvidenceTTL,
	})
	if err != nil {
		s.log.Warn("cycle skipped", "event", "cycle_skipped", "class", frameClass(err, classCanonicalDigest), "sequence", s.sequence)
		return skipped
	}
	checked, err := liveingest.CheckFrame(liveingest.FrameContext{
		ClusterID: s.c.o.ClusterID,
		NodeName:  s.c.o.NodeName,
		NodeUID:   s.c.o.NodeUID,
		BootID:    s.bootID,
		Session:   s.id,
		ProfileID: s.profile,
	}, frame)
	if err == nil {
		err = liveingest.CheckRelative(checked, s.prev, s.hasPrev)
	}
	if err != nil {
		s.log.Warn("cycle skipped", "event", "cycle_skipped", "class", frameClass(err, classInvalidFrame), "sequence", s.sequence)
		return skipped
	}

	msg := &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Snapshot{Snapshot: snapshotMessage(frame)}}
	if err := checkMessageSize(msg); err != nil {
		class := classTooLarge
		if !errors.Is(err, errTooLarge) {
			class = classMarshal
		}
		s.log.Warn("cycle skipped", "event", "cycle_skipped", "class", class, "sequence", s.sequence)
		return skipped
	}
	return s.send(ctx, msg, observedAt, lf.Complete)
}

// frameClass returns the fixed class of an invalid-frame error, or fallback.
func frameClass(err error, fallback string) string {
	if c := liveingest.Class(err); c != "" {
		return c
	}
	return fallback
}

// send sends one frame and waits for its ack (stop-and-wait, GLI-083 (4)). One
// AckTimeout covers the send and the wait: a server that stops reading cannot
// hold the send forever either. When it passes, the timer cancels the stream.
func (s *session) send(ctx context.Context, msg *ingestpb.ClientFrame, observedAt time.Time, complete bool) outcome {
	var timedOut atomic.Bool
	watchdog := time.AfterFunc(s.c.o.AckTimeout, func() {
		timedOut.Store(true)
		s.cancel()
	})
	defer watchdog.Stop()

	if err := s.stream.Send(msg); err != nil {
		if ctx.Err() != nil {
			return ended
		}
		if timedOut.Load() {
			s.log.Warn("session closed", "event", "session_closed", "class", classAckTimeout, "sequence", s.sequence)
			return ended
		}
		s.code = s.endCode(ctx)
		s.log.Warn("session closed", "event", "session_closed", "class", classSendFailed, "code", s.code.String(), "sequence", s.sequence)
		return ended
	}
	var it recvItem
	select {
	case it = <-s.msgs:
	case <-ctx.Done():
		return ended
	}
	if it.err != nil {
		if timedOut.Load() {
			s.log.Warn("session closed", "event", "session_closed", "class", classAckTimeout, "sequence", s.sequence)
			return ended
		}
		s.code = codeOf(it.err)
		s.log.Info("session closed", "event", "session_closed", "class", classStreamEnd, "code", s.code.String(), "sequence", s.sequence)
		return ended
	}
	ack := it.msg.GetAck()
	if ack == nil {
		s.log.Warn("session closed", "event", "session_closed", "class", classAckInvalid, "sequence", s.sequence)
		return ended
	}
	switch ack.GetCode() {
	case ingestpb.AckCode_ACCEPTED:
		s.accepted = true
	case ingestpb.AckCode_DUPLICATE:
	default:
		s.log.Warn("session closed", "event", "session_closed", "class", classAckRejected,
			"ack", ack.GetCode().String(), "sequence", s.sequence)
		return ended
	}
	s.log.Debug("frame acknowledged", "event", "frame", "ack", ack.GetCode().String(),
		"session", s.id, "sequence", s.sequence, "complete", complete)
	s.sequence++
	s.prev = observedAt
	s.hasPrev = true
	return sent
}

// logDiagnostics logs the collector diagnostics of one cycle: the count at info
// when there are any, each code and subject at debug. They are not on the wire
// (GLI-081 (f)).
func (s *session) logDiagnostics(lf agent.LiveFrame) {
	if lf.DiagnosticsTotal == 0 && lf.Complete {
		return
	}
	s.log.Info("collector diagnostics", "event", "diagnostics", "count", lf.DiagnosticsTotal,
		"complete", lf.Complete, "sequence", s.sequence)
	for _, d := range lf.Diagnostics {
		s.log.Debug("collector diagnostic", "event", "diagnostic", "code", d.Code, "subject", d.Subject)
	}
}
