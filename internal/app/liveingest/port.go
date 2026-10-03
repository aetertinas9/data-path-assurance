package liveingest

import (
	"context"
	"errors"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// Clock supplies the current time. Retention, rate and idle decisions use the
// injected Clock; the assessment time of a bundle query does not.
type Clock interface{ Now() time.Time }

// NodeInfo is the name and UID of a Kubernetes Node.
type NodeInfo struct{ Name, UID string }

// NodeDirectory looks Nodes up by name.
type NodeDirectory interface {
	// GetNode returns the Node with the given name, or ErrNodeNotFound when
	// there is none.
	GetNode(ctx context.Context, name string) (NodeInfo, error)
}

// SessionStore allocates collector sessions.
type SessionStore interface {
	// AllocateSession durably records and returns a positive session that is
	// strictly greater than both the stored session of the node (0 when none)
	// and after, atomically. A stored session of math.MaxInt64 allocates
	// nothing and yields ErrSessionOverflow. after is the highest session the
	// caller has already issued for the node UID during its lifetime.
	AllocateSession(ctx context.Context, node fleet.NodeRef, after int64) (int64, error)
}

// LeaderGate reports whether this process currently holds leadership.
type LeaderGate interface{ Leading() bool }

// The errors of the ports. They are distinct and are matched with errors.Is.
var (
	// ErrNodeNotFound reports that NodeDirectory has no Node of that name.
	ErrNodeNotFound = errors.New("liveingest: node not found")
	// ErrNodeNotManaged reports that no fleet selects the node, or that the
	// stored state belongs to another Node UID.
	ErrNodeNotManaged = errors.New("liveingest: node is not managed")
	// ErrSessionOverflow reports that the stored session is math.MaxInt64.
	ErrSessionOverflow = errors.New("liveingest: session overflow")
	// ErrSessionConflict reports that concurrent writers kept the allocation
	// from committing.
	ErrSessionConflict = errors.New("liveingest: session conflict")
	// ErrStoreUnavailable reports that the store could not be reached or
	// answered with an error.
	ErrStoreUnavailable = errors.New("liveingest: store unavailable")
)
