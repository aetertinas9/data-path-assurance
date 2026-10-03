package ingestadapter

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// leaderState is the immutable result of the last successful Lease lookup. The
// poller replaces the whole value; readers only load it.
type leaderState struct {
	// holds is true when the Lease named this process as its holder.
	holds bool
	// lastOK is the Clock reading when the lookup succeeded.
	lastOK time.Time
	// expiry is renewTime + leaseDurationSeconds, or the zero time when the
	// Lease did not carry both.
	expiry time.Time
}

// leaderGate is the liveingest.LeaderGate of the Lease.
type leaderGate struct{ a *Adapter }

// Leading reports whether this process is the leader. It is false until a
// lookup succeeded (fail closed), when the last successful lookup named
// another holder or none, when the Lease it saw has expired by the Clock, and
// when the last successful lookup is older than StaleAfter. A failed lookup
// alone changes nothing: the value of the last success stays until it goes
// stale. The time based conditions are evaluated at the moment of the call, so
// a poll that hangs cannot keep a stale "leading" alive.
func (g leaderGate) Leading() bool { return g.a.leading() }

func (a *Adapter) leading() bool {
	if a == nil {
		return false
	}
	st := a.leader.Load()
	if st == nil || !st.holds {
		return false
	}
	now := a.clock.Now()
	if now.Sub(st.lastOK) > a.staleAfter {
		return false
	}
	if !st.expiry.IsZero() && !now.Before(st.expiry) {
		return false
	}
	return true
}

// RunLeaderPoll reads the Lease once at once and then every PollInterval (a
// real-time ticker) until ctx is done, and returns nil. It starts no request
// after ctx is done. One poller may run at a time; another concurrent call
// returns an error without any request.
func (a *Adapter) RunLeaderPoll(ctx context.Context) error {
	if a == nil || a.lease == nil { // not built by New
		return errNotBuilt
	}
	if !a.polling.CompareAndSwap(false, true) {
		return errPollRunning
	}
	defer a.polling.Store(false)

	// A lookup that cannot finish within StaleAfter could not keep Leading true
	// anyway; cancelling it lets the next one start on time.
	timeout := max(a.staleAfter, a.pollInterval)
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		a.pollOnce(ctx, timeout)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// pollOnce reads the Lease and records the result of a successful lookup.
func (a *Adapter) pollOnce(ctx context.Context, timeout time.Duration) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lease, err := a.lease.Leases(a.leaseNS).Get(reqCtx, a.leaseName, metav1.GetOptions{})
	if ctx.Err() != nil {
		return // shutting down: record nothing
	}
	switch {
	case err == nil:
		st := &leaderState{lastOK: a.clock.Now()}
		holder := ""
		if lease.Spec.HolderIdentity != nil {
			holder = *lease.Spec.HolderIdentity
		}
		st.holds = holder != "" && holder == a.controllerID
		if lease.Spec.RenewTime != nil && lease.Spec.LeaseDurationSeconds != nil {
			st.expiry = lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
		}
		a.leader.Store(st)
	case apierrors.IsNotFound(err):
		// The API server answered that there is no Lease: nobody holds it.
		a.leader.Store(&leaderState{lastOK: a.clock.Now()})
	default:
		// timeout, 429, 5xx, connection errors, 401/403: keep the last value
		// until it goes stale.
	}
}
