package controller

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// writeResult is the outcome of one write attempt sequence on one object.
type writeResult int

const (
	writeNoop      writeResult = iota // nothing to write
	writeDone                         // written
	writeSkipped                      // object gone or replaced; nothing to retry
	writeRetry                        // transient failure; requeue with backoff
	writePermanent                    // 422, 401, 403: fallback tried, wait for the next tick
	writeAborted                      // context ended
)

// Conflict retry policy (GKA-123).
const (
	maxWriteAttempts = 5
	firstBackoff     = 10 * time.Millisecond
)

type errKind int

const (
	errTransient errKind = iota
	errNotFound
	errAlreadyExists
	errConflict
	errFieldManagerConflict
	errPermanent
)

// classify sorts an API error into the GKA-125 table rows.
func classify(err error) errKind {
	switch {
	case apierrors.IsNotFound(err):
		return errNotFound
	case apierrors.IsAlreadyExists(err):
		return errAlreadyExists
	case apierrors.IsConflict(err):
		if apierrors.HasStatusCause(err, metav1.CauseTypeFieldManagerConflict) {
			return errFieldManagerConflict
		}
		return errConflict
	case apierrors.IsInvalid(err), apierrors.IsUnauthorized(err), apierrors.IsForbidden(err),
		apierrors.IsBadRequest(err), apierrors.IsMethodNotSupported(err), apierrors.IsNotAcceptable(err),
		apierrors.IsUnsupportedMediaType(err), apierrors.IsRequestEntityTooLargeError(err):
		return errPermanent
	}
	return errTransient
}

// writer performs the controller's API writes.
type writer struct {
	c   client.Client
	log *slog.Logger
}

// logError records only what GKA-084 allows: the error class, the HTTP status
// code, the API reason and the object kind and name. It never logs err.Error().
func (wr *writer) logError(op, kind, name string, err error) {
	attrs := []any{"op", op, "kind", kind, "name", name}
	var status apierrors.APIStatus
	if s, ok := err.(apierrors.APIStatus); ok {
		status = s
	}
	if status != nil {
		attrs = append(attrs, "code", status.Status().Code, "reason", string(apierrors.ReasonForError(err)))
	} else {
		attrs = append(attrs, "class", "transport")
	}
	wr.log.Error("write failed", attrs...)
}

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

// backoff is the wait before attempt number attempt+1: 10 ms doubling, plus
// 0 to 50 percent jitter.
func backoff(attempt int) time.Duration {
	d := firstBackoff << (attempt - 1)
	return d + rand.N(d/2+1)
}

// statusOp is one status write on a CRD object.
type statusOp struct {
	kind    string
	cached  client.Object // the cached object; never modified
	manager string
	newObj  func() client.Object
	// apply sets the desired status on cur and reports whether it differs from
	// what cur had. It is called again on a fresher object after a conflict.
	apply func(cur client.Object) bool
}

// updateStatus writes op with optimistic concurrency (GKA-120, GKA-123): at
// most five attempts, and between attempts a direct GET of the latest object
// after a jittered exponential wait.
func (wr *writer) updateStatus(ctx context.Context, op statusOp) writeResult {
	cur := op.cached.DeepCopyObject().(client.Object)
	uid := op.cached.GetUID()
	name := op.cached.GetName()
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return writeAborted
		}
		if !op.apply(cur) {
			return writeNoop
		}
		err := wr.c.Status().Update(ctx, cur, client.FieldOwner(op.manager))
		if err == nil {
			return writeDone
		}
		if ctx.Err() != nil {
			return writeAborted
		}
		switch classify(err) {
		case errConflict:
			if attempt >= maxWriteAttempts {
				wr.logError("update-status", op.kind, name, err)
				return writeRetry
			}
			if !sleepCtx(ctx, backoff(attempt)) {
				return writeAborted
			}
			fresh := op.newObj()
			if gerr := wr.c.Get(ctx, client.ObjectKey{Name: name}, fresh); gerr != nil {
				if ctx.Err() != nil {
					return writeAborted
				}
				if classify(gerr) == errNotFound {
					return writeSkipped
				}
				wr.logError("get", op.kind, name, gerr)
				return writeRetry
			}
			if fresh.GetUID() != uid {
				return writeSkipped
			}
			cur = fresh
		case errNotFound:
			return writeSkipped
		case errPermanent:
			wr.logError("update-status", op.kind, name, err)
			return writePermanent
		default:
			wr.logError("update-status", op.kind, name, err)
			return writeRetry
		}
	}
}

// nodePathOwnerRef is the one ownerReference a NodePathState carries (GKA-101).
func nodePathOwnerRef(ns *nodeState) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "v1", Kind: "Node", Name: ns.obj.Name, UID: ns.obj.UID}
}

func ownerRefOK(p *v1alpha1.NodePathState, ns *nodeState) bool {
	if len(p.OwnerReferences) != 1 {
		return false
	}
	r := p.OwnerReferences[0]
	want := nodePathOwnerRef(ns)
	return r.APIVersion == want.APIVersion && r.Kind == want.Kind && r.Name == want.Name && r.UID == want.UID &&
		(r.Controller == nil || !*r.Controller) && (r.BlockOwnerDeletion == nil || !*r.BlockOwnerDeletion)
}

// createNodePath creates the NodePathState of a node (GKA-100, GKA-101). An
// AlreadyExists answer counts as success and the object is read again. The
// returned object is nil unless the state can be written to.
func (wr *writer) createNodePath(ctx context.Context, ns *nodeState) (*v1alpha1.NodePathState, writeResult) {
	obj := &v1alpha1.NodePathState{
		ObjectMeta: metav1.ObjectMeta{Name: ns.obj.Name, OwnerReferences: []metav1.OwnerReference{nodePathOwnerRef(ns)}},
		Spec:       v1alpha1.NodePathStateSpec{NodeRef: v1alpha1.ObjectRef{Name: ns.obj.Name, UID: string(ns.obj.UID)}},
	}
	err := wr.c.Create(ctx, obj, client.FieldOwner(FieldManagerNodePathSpec))
	if err == nil {
		return obj, writeDone
	}
	if ctx.Err() != nil {
		return nil, writeAborted
	}
	switch classify(err) {
	case errAlreadyExists:
		got := &v1alpha1.NodePathState{}
		if gerr := wr.c.Get(ctx, client.ObjectKey{Name: ns.obj.Name}, got); gerr != nil {
			if ctx.Err() != nil {
				return nil, writeAborted
			}
			if classify(gerr) == errNotFound {
				return nil, writeSkipped
			}
			wr.logError("get", "NodePathState", ns.obj.Name, gerr)
			return nil, writeRetry
		}
		if got.Spec.NodeRef.UID != string(ns.obj.UID) {
			return nil, writeSkipped
		}
		return got, writeNoop
	case errPermanent:
		wr.logError("create", "NodePathState", ns.obj.Name, err)
		return nil, writePermanent
	default:
		wr.logError("create", "NodePathState", ns.obj.Name, err)
		return nil, writeRetry
	}
}

// fixOwnerRef rewrites the ownerReferences of an existing NodePathState with
// the spec manager (GKA-101), retrying on conflict like a status write.
func (wr *writer) fixOwnerRef(ctx context.Context, cached *v1alpha1.NodePathState, ns *nodeState) (*v1alpha1.NodePathState, writeResult) {
	cur := cached.DeepCopy()
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return nil, writeAborted
		}
		if ownerRefOK(cur, ns) {
			return cur, writeNoop
		}
		cur.OwnerReferences = []metav1.OwnerReference{nodePathOwnerRef(ns)}
		err := wr.c.Update(ctx, cur, client.FieldOwner(FieldManagerNodePathSpec))
		if err == nil {
			return cur, writeDone
		}
		if ctx.Err() != nil {
			return nil, writeAborted
		}
		switch classify(err) {
		case errConflict:
			if attempt >= maxWriteAttempts {
				wr.logError("update", "NodePathState", cached.Name, err)
				return nil, writeRetry
			}
			if !sleepCtx(ctx, backoff(attempt)) {
				return nil, writeAborted
			}
			fresh := &v1alpha1.NodePathState{}
			if gerr := wr.c.Get(ctx, client.ObjectKey{Name: cached.Name}, fresh); gerr != nil {
				if ctx.Err() != nil {
					return nil, writeAborted
				}
				if classify(gerr) == errNotFound {
					return nil, writeSkipped
				}
				wr.logError("get", "NodePathState", cached.Name, gerr)
				return nil, writeRetry
			}
			if fresh.UID != cached.UID {
				return nil, writeSkipped
			}
			cur = fresh
		case errNotFound:
			return nil, writeSkipped
		case errPermanent:
			wr.logError("update", "NodePathState", cached.Name, err)
			return nil, writePermanent
		default:
			wr.logError("update", "NodePathState", cached.Name, err)
			return nil, writeRetry
		}
	}
}

// deleteNodePath deletes a NodePathState with its UID as precondition
// (GKA-102).
func (wr *writer) deleteNodePath(ctx context.Context, p *v1alpha1.NodePathState) writeResult {
	uid := p.UID
	err := wr.c.Delete(ctx, p.DeepCopy(), client.Preconditions{UID: &uid})
	if err == nil {
		return writeDone
	}
	if ctx.Err() != nil {
		return writeAborted
	}
	switch classify(err) {
	case errNotFound, errConflict:
		return writeSkipped
	case errPermanent:
		wr.logError("delete", "NodePathState", p.Name, err)
		return writePermanent
	default:
		wr.logError("delete", "NodePathState", p.Name, err)
		return writeRetry
	}
}

// applyNodeCondition server-side applies the controller's own condition list
// to nodes/status (GKA-112): the body carries only apiVersion, kind, the name
// and status.conditions, and no force is used. cond nil applies an empty list,
// which removes the controller's own entry.
func (wr *writer) applyNodeCondition(ctx context.Context, nodeName string, cond *corev1.NodeCondition) writeResult {
	conds := []any{}
	if cond != nil {
		entry := map[string]any{
			"type":               string(cond.Type),
			"status":             string(cond.Status),
			"reason":             cond.Reason,
			"message":            cond.Message,
			"lastTransitionTime": cond.LastTransitionTime.UTC().Format(time.RFC3339),
		}
		conds = append(conds, entry)
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata":   map[string]any{"name": nodeName},
		"status":     map[string]any{"conditions": conds},
	})
	if err != nil {
		return writeSkipped
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("Node")
	u.SetName(nodeName)
	err = wr.c.Status().Patch(ctx, u, client.RawPatch(types.ApplyPatchType, body), client.FieldOwner(FieldManagerNodeCondition))
	if err == nil {
		return writeDone
	}
	if ctx.Err() != nil {
		return writeAborted
	}
	switch classify(err) {
	case errNotFound:
		return writeSkipped
	case errFieldManagerConflict:
		// The condition belongs to another manager; never overwrite it and try
		// again on the next pass.
		return writeSkipped
	case errPermanent:
		wr.logError("apply", "Node", nodeName, err)
		return writePermanent
	default:
		wr.logError("apply", "Node", nodeName, err)
		return writeRetry
	}
}
