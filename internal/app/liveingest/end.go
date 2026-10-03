package liveingest

// End says how a stream ends when the engine decides it must. The adapter that
// owns the transport maps it to a transport status; the engine knows nothing
// of gRPC.
type End uint8

// The ways a stream ends. EndNone means the stream stays open.
const (
	EndNone End = iota
	EndUnauthenticated
	EndPermissionDenied
	EndInvalidArgument
	EndDeadlineExceeded
	EndFailedPrecondition
	EndResourceExhausted
	EndAborted
	EndUnavailable
	EndInternal
)

// AckCode is the outcome the engine reports for one snapshot frame. AckNone
// means no acknowledgement is sent.
type AckCode uint8

// The acknowledgement outcomes of a snapshot frame.
const (
	AckNone AckCode = iota
	AckAccepted
	AckDuplicate
	AckOutOfOrder
	AckGap
	AckWrongSession
	AckConflict
	AckInvalid
)

// The class words of a Reject. They are a fixed vocabulary: they never repeat a
// value that came from a peer, so they are safe to log and to map to a fixed
// message.
const (
	ClassNotLeader        = "not_leader"
	ClassScope            = "scope"
	ClassHelloRate        = "hello_rate"
	ClassFrameRate        = "frame_rate"
	ClassNodeNotManaged   = "node_not_managed"
	ClassSessionOverflow  = "session_overflow"
	ClassSessionConflict  = "session_conflict"
	ClassStoreUnavailable = "store_unavailable"
	ClassDirectory        = "directory_unavailable"
	ClassSessionInvalid   = "session_invalid"
	ClassCanceled         = "canceled"
	ClassSuperseded       = "superseded"
	ClassCertExpired      = "cert_expired"
	ClassIdle             = "idle"
	ClassInternal         = "internal"
)

// Reject is the error of a hello or a frame step that ends the stream without
// an acknowledgement. End says how the stream ends and Class names the reason
// in the fixed vocabulary above.
type Reject struct {
	End   End
	Class string
}

// Error renders the class word.
func (r *Reject) Error() string { return "liveingest: rejected: " + r.Class }

func reject(end End, class string) *Reject { return &Reject{End: end, Class: class} }
