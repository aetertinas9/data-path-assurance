package ingestadapter

import (
	"context"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

const (
	// fieldManager is the field manager of the NodePathState status writes
	// (the name of the status controller; a PUT has no manager conflicts, the
	// safety of the writes rests on the write invariants of AllocateSession).
	fieldManager = "dpa-nodepath-status"

	// maxAllocateAttempts counts the first attempt, firstBackoff is the wait
	// after the first conflict (it doubles, plus 0 to 50 percent jitter): the
	// conflict policy of the status controller.
	maxAllocateAttempts = 5
	firstBackoff        = 10 * time.Millisecond

	// maxConcurrentAllocations bounds the AllocateSession calls in progress.
	maxConcurrentAllocations = 16
	// allocateTimeout bounds one call in total, waiting for the node lock and
	// the concurrency slot included. It is shorter than the 30 seconds an
	// agent waits for the ServerHello.
	allocateTimeout = 20 * time.Second
)

// sessionStore is the liveingest.SessionStore of the NodePathState status.
type sessionStore struct{ a *Adapter }

// AllocateSession durably records and returns a session strictly greater than
// the stored status.collectorSession of the node and than after.
//
// Write invariants (they, not the field manager name, make the write safe next
// to the status controller): every status write is a PUT of the whole object
// that was just read, guarded by its resourceVersion (never a patch, apply or
// unguarded PUT); after a conflict the latest object is read again and only
// collectorSession is replaced; every other status field is written back as it
// was read. A status that is empty (no nodeRef) gets the smallest status that
// passes the schema and the CEL rules.
//
// Errors: liveingest.ErrNodeNotManaged (no NodePathState of the node, or one
// that belongs to another Node UID), ErrSessionOverflow (no larger session
// exists), ErrSessionConflict (five attempts lost to concurrent writers),
// ErrStoreUnavailable (anything else, a done ctx, the 20 second limit, or this
// process no longer leading when the write was due).
func (s sessionStore) AllocateSession(ctx context.Context, node fleet.NodeRef, after int64) (int64, error) {
	a := s.a
	if !validNodeName(node.Name) || node.UID == "" {
		return 0, liveingest.ErrNodeNotManaged
	}
	if a == nil || a.hello == nil { // not built by New
		return 0, unavailable(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, allocateTimeout)
	defer cancel()

	unlock, err := a.locks.lock(ctx, node.UID)
	if err != nil {
		return 0, unavailable(err)
	}
	defer unlock()
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	case <-ctx.Done():
		return 0, unavailable(ctx.Err())
	}

	key := client.ObjectKey{Name: node.Name}
	var lastConflict error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, unavailable(err)
		}
		var obj v1alpha1.NodePathState
		if err := a.hello.Get(ctx, key, &obj); err != nil {
			return 0, classifyRead(ctx, err)
		}
		if obj.Spec.NodeRef.UID != node.UID {
			return 0, liveingest.ErrNodeNotManaged
		}

		session, ok := nextSession(obj.Status.CollectorSession, after)
		if !ok {
			return 0, liveingest.ErrSessionOverflow
		}
		if obj.ResourceVersion == "" {
			// Never write without the precondition (invariant I1).
			return 0, unavailable(nil)
		}
		// The leadership check sits right before the write; once ctx is done
		// no new request starts.
		if !a.leading() {
			return 0, unavailable(nil)
		}
		if err := ctx.Err(); err != nil {
			return 0, unavailable(err)
		}

		setSession(&obj, session)
		err := a.hello.Status().Update(ctx, &obj, client.FieldOwner(fieldManager))
		switch {
		case err == nil:
			return session, nil
		case apierrors.IsConflict(err):
			lastConflict = err
			if attempt >= maxAllocateAttempts {
				return 0, conflict(lastConflict)
			}
			if !sleepCtx(ctx, backoff(attempt)) {
				return 0, unavailable(ctx.Err())
			}
		case apierrors.IsNotFound(err):
			return 0, liveingest.ErrNodeNotManaged
		case ctx.Err() != nil:
			return 0, unavailable(ctx.Err())
		default:
			return 0, unavailable(err)
		}
	}
}

// classifyRead maps the error of reading the NodePathState.
func classifyRead(ctx context.Context, err error) error {
	switch {
	case apierrors.IsNotFound(err):
		return liveingest.ErrNodeNotManaged
	case ctx.Err() != nil:
		return unavailable(ctx.Err())
	default:
		return unavailable(err)
	}
}

// nextSession returns the smallest session greater than the stored one and
// after. ok is false when there is none (the stored session, or after, is
// math.MaxInt64). A negative value counts as 0.
func nextSession(stored *int64, after int64) (session int64, ok bool) {
	base := max(after, 0)
	if stored != nil {
		base = max(base, *stored)
	}
	if base == math.MaxInt64 {
		return 0, false
	}
	return base + 1, true
}

// setSession prepares obj, the object just read, for the status write.
func setSession(obj *v1alpha1.NodePathState, session int64) {
	if obj.Status.NodeRef.Name == "" {
		// Empty status: the minimal one that satisfies the schema and the CEL
		// rules (graphRevision stays unset with evidenceCompleteness Unknown).
		obj.Status = v1alpha1.NodePathStateStatus{
			ObservedGeneration:   obj.Generation,
			NodeRef:              obj.Spec.NodeRef,
			EvidenceCompleteness: v1alpha1.SnapshotCompletenessUnknown,
			DeviceSummaries:      []v1alpha1.DeviceSummary{},
			Conditions:           []metav1.Condition{},
		}
	}
	obj.Status.CollectorSession = &session
	// A nil slice would serialize as null, which the schema rejects.
	if obj.Status.DeviceSummaries == nil {
		obj.Status.DeviceSummaries = []v1alpha1.DeviceSummary{}
	}
	if obj.Status.Conditions == nil {
		obj.Status.Conditions = []metav1.Condition{}
	}
	if g := obj.Status.GateOwnership; g != nil && g.Policy.RequiredCoverage == nil {
		g.Policy.RequiredCoverage = []v1alpha1.FleetCoverageRequirement{}
	}
	// The server keeps the managed fields of the live object for a status
	// write; do not send them back.
	obj.ManagedFields = nil
}

// backoff is the wait after the attempt-th failed attempt: 10 ms doubling,
// plus 0 to 50 percent jitter.
func backoff(attempt int) time.Duration {
	d := firstBackoff << (attempt - 1)
	return d + rand.N(d/2+1)
}

// sleepCtx waits for d and reports false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// keyedLocks is one mutual exclusion lock per key whose waiters give up with
// their ctx. Ownership: m and the refs counters are guarded by mu; a lock's
// channel is its mutex (a token in the channel means held).
type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	held chan struct{} // capacity 1
	refs int           // holders and waiters; guarded by keyedLocks.mu
}

// lock waits for the lock of key. The returned function releases it.
func (k *keyedLocks) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*keyedLock)
	}
	l := k.m[key]
	if l == nil {
		l = &keyedLock{held: make(chan struct{}, 1)}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
	select {
	case l.held <- struct{}{}:
		return func() { <-l.held; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
