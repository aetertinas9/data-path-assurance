package controller

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// shutdownGrace bounds how long Run waits for its parts to stop after the
// context ended; the library defaults (30s) are too long for GKA-022.
// releaseTimeout bounds the Lease release Run attempts itself on cancellation;
// a stalled API server must not hold Run back (GKA-022(a)).
const (
	shutdownGrace  = 7 * time.Second
	releaseTimeout = 2 * time.Second
)

// Controller watches the GPU fleet objects and Nodes and publishes their
// status. It is single use: Run may be called once.
type Controller struct {
	opts   normalized
	scheme *runtime.Scheme

	started atomic.Bool

	trigger chan struct{} // coalesced "run a pass" signal from the watches

	// Owned by the pass goroutine (lead) after Run started.
	wr         *writer
	inf        *informers
	elector    *leaderelection.LeaderElector
	assessable map[string]struct{} // node UIDs the assessor was called for in the previous pass
	skip       map[string]struct{} // objects rejected permanently, retried on ticks only
	retryDelay time.Duration
}

// New validates opts without any network access and returns a Controller.
func New(opts Options) (*Controller, error) {
	n, err := normalize(opts)
	if err != nil {
		return nil, err
	}
	installLoggers(n.logger)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("controller: build scheme: %w", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("controller: build scheme: %w", err)
	}
	return &Controller{
		opts:       n,
		scheme:     scheme,
		trigger:    make(chan struct{}, 1),
		assessable: map[string]struct{}{},
		skip:       map[string]struct{}{},
	}, nil
}

func (c *Controller) poke() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

// Run takes part in leader election and, as leader, keeps the status of the
// watched objects current until ctx ends (nil) or leadership is lost
// (ErrLeadershipLost). A second call returns ErrAlreadyRun.
func (c *Controller) Run(ctx context.Context) error {
	if !c.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	cli, err := client.New(c.opts.restConfig, client.Options{Scheme: c.scheme})
	if err != nil {
		return fmt.Errorf("controller: build client: %w", err)
	}
	c.wr = &writer{c: cli, log: c.opts.logger}
	c.inf, err = newInformers(c.opts.restConfig, c.scheme, c.poke)
	if err != nil {
		return fmt.Errorf("controller: build informers: %w", err)
	}
	coord, err := coordinationv1client.NewForConfig(c.opts.restConfig)
	if err != nil {
		return fmt.Errorf("controller: build lease client: %w", err)
	}
	le := c.opts.leaderElection

	runCtx, cancel := context.WithCancel(withLogger(ctx, c.opts.logger))
	defer cancel()

	var (
		mu       sync.Mutex
		closed   bool
		leaderWG sync.WaitGroup
	)
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: le.ID, Namespace: le.Namespace},
		Client:     coord,
		LockConfig: resourcelock.ResourceLockConfig{Identity: c.opts.controllerID}, // no EventRecorder: no Event objects
	}
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: le.LeaseDuration,
		RenewDeadline: le.RenewDeadline,
		RetryPeriod:   le.RetryPeriod,
		// The library would release the Lease itself, but it does so with a
		// context nobody can cancel and even after it gave up renewing, which
		// keeps the pass loop alive for another RenewDeadline while the API
		// server stalls. Run releases on cancellation only, with a short bound.
		ReleaseOnCancel: false,
		Name:            le.ID,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadCtx context.Context) {
				mu.Lock()
				if closed {
					mu.Unlock()
					return
				}
				leaderWG.Add(1)
				mu.Unlock()
				defer leaderWG.Done()
				c.lead(leadCtx)
			},
			OnStoppedLeading: func() {},
		},
	})
	if err != nil {
		return fmt.Errorf("controller: build leader elector: %w", err)
	}
	c.elector = elector

	informersDone := c.inf.run(runCtx)
	electorDone := make(chan struct{})
	go func() {
		defer close(electorDone)
		elector.Run(runCtx)
	}()

	// One deadline bounds the whole shutdown (GKA-022(a)): it starts when the
	// context ends or leadership is lost.
	var graceOnce sync.Once
	grace := make(chan struct{})
	startGrace := func() {
		graceOnce.Do(func() { time.AfterFunc(shutdownGrace, func() { close(grace) }) })
	}
	electorStopped := false
	select {
	case <-electorDone: // leadership lost, or the context ended
		electorStopped = true
		startGrace()
	case <-ctx.Done():
		startGrace()
		select {
		case <-electorDone:
			electorStopped = true
		case <-grace:
			c.opts.logger.Warn("leader election did not stop in time")
		}
	}

	mu.Lock()
	closed = true
	mu.Unlock()
	cancel()
	c.waitAll(grace, &leaderWG, informersDone)

	if ctx.Err() != nil {
		// A normal stop hands the Lease back. Losing leadership does not: the
		// Lease belongs to someone else or will simply expire.
		if electorStopped && c.elector.IsLeader() {
			c.releaseLease(lock)
		}
		return nil
	}
	return ErrLeadershipLost
}

// releaseLease clears the holder of the Lease so another instance can take over
// at once (GKA-142). It uses a context of its own with a short limit and gives
// up silently when the API server does not answer.
func (c *Controller) releaseLease(lock *resourcelock.LeaseLock) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	old, _, err := lock.Get(ctx)
	if err != nil {
		c.wr.logError("get", "Lease", lock.LeaseMeta.Name, err)
		return
	}
	if old.HolderIdentity != c.opts.controllerID {
		return
	}
	now := metav1.NewTime(NewSystemClock().Now())
	release := resourcelock.LeaderElectionRecord{
		LeaderTransitions:    old.LeaderTransitions,
		LeaseDurationSeconds: 1,
		RenewTime:            now,
		AcquireTime:          now,
	}
	if err := lock.Update(ctx, release); err != nil {
		c.wr.logError("update", "Lease", lock.LeaseMeta.Name, err)
	}
}

// active reports whether passes may still write: the context is alive and the
// last observed Lease holder is this instance. The holder is refreshed on every
// renew attempt, so a takeover by another instance stops writes within one
// RetryPeriod, before the renew deadline expires.
func (c *Controller) active(ctx context.Context) bool {
	return ctx.Err() == nil && c.elector.IsLeader()
}

// waitAll waits for the wait groups, giving up when grace is closed.
func (c *Controller) waitAll(grace <-chan struct{}, groups ...*sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, g := range groups {
			g.Wait()
		}
	}()
	select {
	case <-done:
	case <-grace:
		c.opts.logger.Warn("workers did not stop in time")
	}
}

// lead runs the passes while this instance is the leader (GKA-070): the first
// one once every cache has synced, then one per watch event, per tick and per
// failure requeue.
func (c *Controller) lead(ctx context.Context) {
	c.opts.logger.Info("became leader")
	if !c.inf.waitSynced(ctx) {
		return
	}
	ticker := time.NewTicker(c.opts.resyncInterval) // a real timer, not the injected Clock
	defer ticker.Stop()
	var (
		retryTimer *time.Timer
		retryC     <-chan time.Time
	)
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()

	isTick := true
	for {
		if ctx.Err() != nil {
			return
		}
		// A pass only runs while this instance is the observed Lease holder.
		// Losing the lease ends the elector (and ctx); until then just wait.
		retry := false
		if c.elector.IsLeader() {
			retry = c.safePass(ctx, isTick)
		}
		if ctx.Err() != nil {
			return
		}
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer, retryC = nil, nil
		}
		if retry {
			c.retryDelay = nextRetryDelay(c.retryDelay, c.opts.resyncInterval)
			retryTimer = time.NewTimer(c.retryDelay)
			retryC = retryTimer.C
		} else {
			c.retryDelay = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-c.trigger:
			isTick = false
		case <-retryC:
			isTick = false
		case <-ticker.C:
			isTick = true
		}
	}
}

// nextRetryDelay is the requeue backoff of GKA-125: it starts at
// min(100ms, resync), doubles, and never exceeds resync.
func nextRetryDelay(prev, resync time.Duration) time.Duration {
	if prev <= 0 {
		return min(100*time.Millisecond, resync)
	}
	return min(prev*2, resync)
}

// safePass runs one pass and turns a panic into a logged failure that is
// retried, so one bad object cannot take the process down.
func (c *Controller) safePass(ctx context.Context, isTick bool) (retry bool) {
	defer func() {
		if r := recover(); r != nil {
			c.opts.logger.Error("pass failed", "class", "panic", "type", fmt.Sprintf("%T", r))
			retry = true
		}
	}()
	return c.runPass(ctx, isTick)
}
