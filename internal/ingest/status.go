package ingest

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/internal/transport/mtls"
)

// The fixed vocabulary of the status messages and acknowledgement messages the
// adapter writes (GLI-102). A message never carries the text of an error, an
// identity, a path or anything that came from a peer.
const (
	msgUnauthenticated = "unauthenticated"
	msgRoleNotAllowed  = "role not allowed"
	msgPermission      = "permission denied"
	msgNotLeader       = "not leader"
	msgTooManyStreams  = "too many streams"
	msgInvalidHello    = "invalid hello"
	msgUnexpectedHello = "unexpected hello"
	msgEmptyFrame      = "empty frame"
	msgHelloTimeout    = "hello timeout"
	msgIdleTimeout     = "idle timeout"
	msgRateLimited     = "rate limited"
	msgNodeNotManaged  = "node not managed"
	msgSessionOverflow = "session overflow"
	msgSessionConflict = "session conflict"
	msgUnavailable     = "unavailable"
	msgSuperseded      = "superseded"
	msgCertExpired     = "certificate expired"
	msgCanceled        = "canceled"
	msgStopping        = "server stopping"
	msgInternal        = "internal error"
	msgMessageTooLarge = "message too large"
	msgInvalidMessage  = "invalid message"

	ackAccepted     = "accepted"
	ackDuplicate    = "duplicate"
	ackOutOfOrder   = "out of order: new session required"
	ackGap          = "gap: new session required"
	ackWrongSession = "wrong session"
	ackConflict     = "conflict"
	ackInvalid      = "invalid frame"
)

// codeOf maps how the engine ends a stream to the gRPC code of GLI-100.
func codeOf(e liveingest.End) codes.Code {
	switch e {
	case liveingest.EndUnauthenticated:
		return codes.Unauthenticated
	case liveingest.EndPermissionDenied:
		return codes.PermissionDenied
	case liveingest.EndInvalidArgument:
		return codes.InvalidArgument
	case liveingest.EndDeadlineExceeded:
		return codes.DeadlineExceeded
	case liveingest.EndFailedPrecondition:
		return codes.FailedPrecondition
	case liveingest.EndResourceExhausted:
		return codes.ResourceExhausted
	case liveingest.EndAborted:
		return codes.Aborted
	case liveingest.EndUnavailable:
		return codes.Unavailable
	default:
		return codes.Internal
	}
}

// rejectMessage maps the class word of a reject to its fixed message.
func rejectMessage(r *liveingest.Reject) string {
	switch r.Class {
	case liveingest.ClassNotLeader:
		return msgNotLeader
	case liveingest.ClassScope:
		return msgPermission
	case "hello_format":
		return msgInvalidHello
	case liveingest.ClassHelloRate, liveingest.ClassFrameRate:
		return msgRateLimited
	case liveingest.ClassNodeNotManaged:
		return msgNodeNotManaged
	case liveingest.ClassSessionOverflow:
		return msgSessionOverflow
	case liveingest.ClassSessionConflict:
		return msgSessionConflict
	case liveingest.ClassStoreUnavailable, liveingest.ClassDirectory:
		return msgUnavailable
	case liveingest.ClassSuperseded:
		return msgSuperseded
	case liveingest.ClassCertExpired:
		return msgCertExpired
	case liveingest.ClassIdle:
		return msgIdleTimeout
	case liveingest.ClassCanceled:
		return msgCanceled
	default:
		return msgInternal
	}
}

// rejectStatus is the status error of an engine reject.
func rejectStatus(r *liveingest.Reject) error {
	return status.Error(codeOf(r.End), rejectMessage(r))
}

// asReject extracts the engine reject of an error, or makes the internal one.
func asReject(err error) *liveingest.Reject {
	var r *liveingest.Reject
	if errors.As(err, &r) && r != nil {
		return r
	}
	return &liveingest.Reject{End: liveingest.EndInternal, Class: liveingest.ClassInternal}
}

// ackCodeOf maps the engine's acknowledgement outcome to the wire code and the
// fixed message of the acknowledgement.
func ackCodeOf(a liveingest.AckCode) (ingestpb.AckCode, string) {
	switch a {
	case liveingest.AckAccepted:
		return ingestpb.AckCode_ACCEPTED, ackAccepted
	case liveingest.AckDuplicate:
		return ingestpb.AckCode_DUPLICATE, ackDuplicate
	case liveingest.AckOutOfOrder:
		return ingestpb.AckCode_OUT_OF_ORDER, ackOutOfOrder
	case liveingest.AckGap:
		return ingestpb.AckCode_GAP, ackGap
	case liveingest.AckWrongSession:
		return ingestpb.AckCode_WRONG_SESSION, ackWrongSession
	case liveingest.AckConflict:
		return ingestpb.AckCode_CONFLICT, ackConflict
	default:
		return ingestpb.AckCode_INVALID, ackInvalid
	}
}

// endStatus is the status error that ends a stream after the acknowledgement
// of a frame (GLI-044).
func endStatus(res liveingest.FrameResult) error {
	msg := msgInvalidMessage
	switch res.Ack {
	case liveingest.AckInvalid:
		msg = ackInvalid
		if res.End == liveingest.EndPermissionDenied {
			msg = msgPermission
		}
	case liveingest.AckOutOfOrder:
		msg = ackOutOfOrder
	case liveingest.AckGap:
		msg = ackGap
	case liveingest.AckWrongSession:
		msg = ackWrongSession
	case liveingest.AckConflict:
		msg = ackConflict
	case liveingest.AckAccepted:
		msg = ackOutOfOrder // the sequence space is used up: a new session is required
	}
	return status.Error(codeOf(res.End), msg)
}

// identityStatus maps the failure of the identity step to a status.
func identityStatus(err error) error {
	if errors.Is(err, errRole) {
		return status.Error(codes.PermissionDenied, msgRoleNotAllowed)
	}
	return status.Error(codes.Unauthenticated, msgUnauthenticated)
}

// classOfMTLS names a failure of the TLS material in the fixed vocabulary of
// the logs.
func classOfMTLS(err error) string {
	switch {
	case errors.Is(err, mtls.ErrFile):
		return "tls_file"
	case errors.Is(err, mtls.ErrNoCA):
		return "tls_ca"
	case errors.Is(err, mtls.ErrKeyPair):
		return "tls_keypair"
	case errors.Is(err, mtls.ErrServerName), errors.Is(err, mtls.ErrServiceDNS):
		return "tls_server_name"
	default:
		return "tls"
	}
}

// recvStatus is the status a failed receive ends the stream with. The codec
// stage failures (unmarshal, decompression, an unregistered encoding, a
// message over the receive bound) were already written to the stream by the
// transport with their own code; the code is kept and the message is the fixed
// one.
func recvStatus(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.Error(codes.Canceled, msgCanceled)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() == codes.OK {
		return status.Error(codes.Internal, msgInvalidMessage)
	}
	return status.Error(st.Code(), msgInvalidMessage)
}

// codeClass names the code of a status in the fixed vocabulary of the logs.
func codeClass(err error) string {
	return status.Code(err).String()
}
