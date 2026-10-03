package liveclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/transport/mtls"
)

// jitterFraction is the half-width of the uniform jitter of a reconnect wait
// (GLI-083 (5)): the wait is the backoff times a factor from 0.8 to 1.2.
const jitterFraction = 0.2

// Fixed class words of the log (GLI-101): the log carries these, never the
// text of an error.
const (
	classBootID     = "boot_id"
	classTLS        = "tls_config"
	classDial       = "dial"
	classStreamOpen = "stream_open"
	classStreamEnd  = "stream_ended"
)

// client is one running stream client. It is used by the goroutine of Run only;
// the reader goroutine of a session owns nothing but the stream's receive side.
type client struct {
	o    Options
	host string
	log  *slog.Logger
}

// Run validates the options and the start-up configuration and then keeps a
// snapshot stream to the controller until ctx is cancelled (GLI-112).
//
// An option that breaks the rules of GLI-080 is an error that matches
// ErrInvalidOptions. A start-up failure (the TLS files, the certificate
// identity, the sysfs root, the boot ID file) matches ErrLiveConfig. After
// that, Run does not return for a network or server error: every end of a
// stream, whatever its status, is followed by a backoff and a new connection
// with a new hello. Cancelling ctx stops the cycles, closes the stream and
// returns nil.
func Run(ctx context.Context, o Options) error {
	if ctx == nil {
		return &optionError{"context"}
	}
	if err := o.ValidateFlags(); err != nil {
		return err
	}
	o = o.withDefaults()
	if err := validateStartup(o); err != nil {
		return err
	}
	c := &client{
		o:    o,
		host: controllerHost(o.Controller),
		log:  o.Logger.With("cluster_id", o.ClusterID, "node_uid", o.NodeUID),
	}
	c.log.Info("agent live started", "event", "start")
	c.loop(ctx)
	c.log.Info("agent live stopped", "event", "stop")
	return nil
}

// attemptResult is what one connection attempt tells the backoff.
type attemptResult struct {
	// accepted is true when the session got at least one ACCEPTED ack.
	accepted bool
	// code is the gRPC status code the stream ended with (OK when unknown).
	code codes.Code
}

// loop runs connection attempts until ctx ends. The wait after an attempt
// starts at ReconnectInitial, doubles up to ReconnectMax with a jitter of 20 %
// either way, returns to ReconnectInitial after a session that got an ACCEPTED
// ack, and is at least ExhaustedMin after a ResourceExhausted status.
func (c *client) loop(ctx context.Context) {
	backoff := c.o.ReconnectInitial
	for ctx.Err() == nil {
		res := c.attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		if res.accepted {
			backoff = c.o.ReconnectInitial
		}
		wait := jitter(min(backoff, c.o.ReconnectMax))
		if res.code == codes.ResourceExhausted {
			wait = max(wait, c.o.ExhaustedMin)
		}
		c.log.Info("reconnect scheduled", "event", "reconnect", "wait", wait.String(), "code", res.code.String())
		if !sleep(ctx, wait) {
			return
		}
		backoff = min(backoff*2, c.o.ReconnectMax)
	}
}

// jitter scales d by a uniform factor in [1-jitterFraction, 1+jitterFraction].
func jitter(d time.Duration) time.Duration {
	factor := 1 - jitterFraction + 2*jitterFraction*rand.Float64()
	return time.Duration(float64(d) * factor)
}

// sleep waits d on a real timer and reports whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// attempt makes one connection (GLI-083 (1)): it reads the boot ID file and the
// CA, certificate and key files again, builds a new TLS configuration and a new
// ClientConn, runs the session on it and closes the connection when the stream
// is over. A failure before the stream is a class word in the log and an
// attempt without an ACCEPTED ack.
func (c *client) attempt(ctx context.Context) attemptResult {
	bootID, err := readBootID(c.o.BootIDFile)
	if err != nil {
		c.log.Warn("connection attempt failed", "event", "connect_failed", "class", classBootID)
		return attemptResult{}
	}
	cfg, err := mtls.ClientConfig(c.o.tlsFiles(), c.host)
	if err != nil {
		c.log.Warn("connection attempt failed", "event", "connect_failed", "class", classTLS)
		return attemptResult{}
	}
	// passthrough hands the host name to the dialer as it is, so a name that
	// resolves to both IP families is tried the way net.Dial tries it.
	conn, err := grpc.NewClient("passthrough:///"+c.o.Controller,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithNoProxy(),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(maxWireBytes)),
	)
	if err != nil {
		c.log.Warn("connection attempt failed", "event", "connect_failed", "class", classDial)
		return attemptResult{}
	}
	// The connection carried one stream that is over, so a close error loses
	// nothing.
	defer func() { _ = conn.Close() }()
	return c.session(ctx, conn, bootID)
}

// codeOf returns the status code a stream ended with: OK for a clean end.
func codeOf(err error) codes.Code {
	if errors.Is(err, io.EOF) {
		return codes.OK
	}
	return status.Code(err)
}

// compressorName is the gRPC compressor the client sends with (GLI-050).
const compressorName = gzip.Name
