package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"

	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// minConnectionIdle is the least time a connection may stay without any stream
// before the transport closes it.
const minConnectionIdle = time.Minute

// gracefulStop is how long Serve lets the streams finish after the context was
// cancelled before it stops the transport by force (GLI-111).
const gracefulStop = 5 * time.Second

// service adapts the server to the generated service interface. It is a type
// of its own so that the generated Unimplemented embedding does not become
// part of the exported Server.
type service struct {
	ingestpb.UnimplementedIngestServer
	s *Server
}

// Stream is the Ingest.Stream handler.
func (svc *service) Stream(stream ingestpb.Ingest_StreamServer) error {
	return svc.s.handle(stream)
}

// serve runs the gRPC server on lis until ctx is cancelled.
func (s *Server) serve(ctx context.Context, lis net.Listener) error {
	// The second call does not touch the listener: it may be the one that the
	// first call is serving.
	if !s.served.CompareAndSwap(false, true) {
		return ErrAlreadyServed
	}
	s.runCtx = ctx

	// gzip is a registered compressor; referencing the package identifier is
	// what links it in (the server accepts gzip and identity messages).
	_ = gzip.Name
	g := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(s.tlsCfg)),
		grpc.MaxRecvMsgSize(maxDecompressed),
		grpc.ConnectionTimeout(s.cfg.Limits.HelloTimeout),
		grpc.WaitForHandlers(true),
		grpc.StatsHandler(statsHandler{}),
		// A connection that has carried no stream for a while carries nothing:
		// the transport closes it. Streams end by their own timers.
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: max(s.cfg.Limits.IdleTimeout, minConnectionIdle)}),
	)
	// Only the Ingest service is registered: no reflection, health, channelz,
	// admin or debug service (GLI-025).
	ingestpb.RegisterIngestServer(g, &service{s: s})

	errc := make(chan error, 1)
	go func() { errc <- g.Serve(lis) }()

	// errc carries the one result of g.Serve. received says it was taken
	// already (the listener failed before the context was cancelled).
	var serveErr error
	received := false
	select {
	case <-ctx.Done():
	case err := <-errc:
		// The listener failed. g.Serve has returned and closed lis.
		received = true
		serveErr = fmt.Errorf("ingest: serve: %w", err)
		if err == nil {
			serveErr = fmt.Errorf("ingest: serve: listener closed")
		}
	}

	// New hellos are refused and every active stream ends with Unavailable at
	// once; the transport then stops gracefully, by force after gracefulStop.
	close(s.stopping)
	stopped := make(chan struct{})
	go func() {
		g.GracefulStop()
		close(stopped)
	}()
	timer := time.NewTimer(gracefulStop)
	select {
	case <-stopped:
		timer.Stop()
	case <-timer.C:
		g.Stop()
		<-stopped
	}
	// g.Serve ends only after the stop. When the context was cancelled before
	// it had started, grpc-go closes lis in that very call (and answers
	// ErrServerStopped), so Serve returns only after the listener is closed
	// and the goroutine is gone. A stop is not an error; any other result is
	// a listener failure.
	if !received {
		if err := <-errc; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr = fmt.Errorf("ingest: serve: %w", err)
		}
	}
	// Every handler has returned (WaitForHandlers); the receive goroutines are
	// released by the end of their streams.
	s.wg.Wait()
	return serveErr
}
