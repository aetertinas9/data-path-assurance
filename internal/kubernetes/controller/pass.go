package controller

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// runPass performs one pass (GKA-070): read the caches, classify, assess, and
// write what changed. It reports whether some write failed transiently and
// should be retried with backoff.
func (c *Controller) runPass(ctx context.Context, isTick bool) bool {
	if isTick {
		clear(c.skip) // every rejected object is retried on a tick
	}
	now := c.opts.clock.Now()
	w := buildWorld(now,
		listStore[v1alpha1.GPUFleet](c.inf.fleets),
		listStore[corev1.Node](c.inf.nodes),
		listStore[v1alpha1.GPUDevice](c.inf.devices),
		listStore[v1alpha1.NodePathState](c.inf.paths),
	)
	initEvals(w)
	if !c.assessNodes(ctx, w) {
		return false
	}
	return c.writeAll(ctx, w, isTick)
}

// passWriter tracks the retry bookkeeping of one pass.
type passWriter struct {
	c      *Controller
	w      *world
	isTick bool
	retry  bool
}

// gate reports whether the object may be written in this pass. An object the
// API server rejected permanently is only retried on ticks (GKA-125).
func (p *passWriter) gate(key string) bool {
	if p.isTick {
		delete(p.c.skip, key)
		return true
	}
	_, blocked := p.c.skip[key]
	return !blocked
}

func (p *passWriter) note(key string, res writeResult) {
	switch res {
	case writeRetry:
		p.retry = true
	case writePermanent:
		p.c.skip[key] = struct{}{}
	}
}

func (c *Controller) writeAll(ctx context.Context, w *world, isTick bool) bool {
	p := &passWriter{c: c, w: w, isTick: isTick}
	for _, ns := range w.nodes {
		if !c.active(ctx) {
			return p.retry
		}
		p.writeNode(ctx, ns)
	}
	names := make([]string, 0)
	for name := range w.paths {
		if w.nodeBy[name] == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names { // GKA-102(a): the Node is gone
		if !c.active(ctx) {
			return p.retry
		}
		key := "NodePathState/" + name
		if p.gate(key) {
			p.note(key, c.wr.deleteNodePath(ctx, w.paths[name]))
		}
	}
	for _, ds := range w.devices {
		if !c.active(ctx) {
			return p.retry
		}
		p.writeDevice(ctx, ds)
	}
	for _, fs := range w.fleets {
		if !c.active(ctx) {
			return p.retry
		}
		p.writeFleet(ctx, fs)
	}
	return p.retry
}

// sortedConditions returns a copy of conds in type byte order.
func sortedConditions(conds []metav1.Condition) []metav1.Condition {
	out := append([]metav1.Condition(nil), conds...)
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func (p *passWriter) writeDevice(ctx context.Context, ds *deviceState) {
	key := "GPUDevice/" + ds.obj.Name
	if !p.gate(key) {
		return
	}
	now := p.w.now
	build := func(r deviceRender) func(client.Object) bool {
		return func(cur client.Object) bool {
			d := cur.(*v1alpha1.GPUDevice)
			desired := deviceStatus(d, r, now)
			have := d.Status
			have.Conditions = sortedConditions(have.Conditions)
			if statusEqual(have, desired) {
				return false
			}
			d.Status = desired
			return true
		}
	}
	op := statusOp{
		kind: "GPUDevice", cached: ds.obj, manager: FieldManagerDeviceStatus,
		newObj: func() client.Object { return &v1alpha1.GPUDevice{} },
		apply:  build(renderOf(ds)),
	}
	res := p.c.wr.updateStatus(ctx, op)
	if res == writePermanent {
		op.apply = build(deviceRender{scope: deviceInternalError, class: classStatusWriteRefused})
		p.c.wr.updateStatus(ctx, op)
	}
	p.note(key, res)
}

func (p *passWriter) writeFleet(ctx context.Context, fs *fleetState) {
	key := "GPUFleet/" + fs.obj.Name
	if !p.gate(key) {
		return
	}
	now := p.w.now
	build := func(class string) func(client.Object) bool {
		return func(cur client.Object) bool {
			f := cur.(*v1alpha1.GPUFleet)
			desired := fleetStatus(f, fs, class, now)
			have := f.Status
			have.Conditions = sortedConditions(have.Conditions)
			if statusEqual(have, desired) {
				return false
			}
			f.Status = desired
			return true
		}
	}
	op := statusOp{
		kind: "GPUFleet", cached: fs.obj, manager: FieldManagerFleetStatus,
		newObj: func() client.Object { return &v1alpha1.GPUFleet{} },
		apply:  build(""),
	}
	res := p.c.wr.updateStatus(ctx, op)
	if res == writePermanent {
		op.apply = build(classStatusWriteRefused)
		p.c.wr.updateStatus(ctx, op)
	}
	p.note(key, res)
}

// writeNode does everything a node needs: its NodePathState (GKA-100 to
// GKA-105) and its Node condition (GKA-110 to GKA-114).
func (p *passWriter) writeNode(ctx context.Context, ns *nodeState) {
	p.writeNodePath(ctx, ns)
	if !p.c.active(ctx) {
		return
	}
	p.writeNodeCondition(ctx, ns)
}

func (p *passWriter) writeNodePath(ctx context.Context, ns *nodeState) {
	key := "NodePathState/" + ns.obj.Name
	if !p.gate(key) {
		return
	}
	existing := ns.existing
	wr := p.c.wr
	if existing != nil && existing.Spec.NodeRef.UID != string(ns.obj.UID) {
		// GKA-102(c): the Node was replaced; the new state is created on a later pass.
		p.note(key, wr.deleteNodePath(ctx, existing))
		return
	}
	if existing != nil && existing.DeletionTimestamp != nil {
		return
	}
	switch ns.scope {
	case nodeNotSelected:
		if existing != nil { // GKA-102(b)
			p.note(key, wr.deleteNodePath(ctx, existing))
		}
		return
	case nodeHeld, nodeCapacity:
		if existing == nil { // never created for these scopes
			return
		}
	}
	obj := existing
	if obj == nil {
		created, res := wr.createNodePath(ctx, ns)
		p.note(key, res)
		if created == nil {
			return
		}
		obj = created
	}
	if !ownerRefOK(obj, ns) {
		fixed, res := wr.fixOwnerRef(ctx, obj, ns)
		p.note(key, res)
		if fixed == nil {
			return
		}
		obj = fixed
	}
	now := p.w.now
	build := func(class string) func(client.Object) bool {
		return func(cur client.Object) bool {
			np := cur.(*v1alpha1.NodePathState)
			desired := nodePathStatus(p.w, np, ns, class, now)
			have := np.Status
			have.Conditions = sortedConditions(have.Conditions)
			if statusEqual(have, desired) {
				return false
			}
			np.Status = desired
			return true
		}
	}
	op := statusOp{
		kind: "NodePathState", cached: obj, manager: FieldManagerNodePathStatus,
		newObj: func() client.Object { return &v1alpha1.NodePathState{} },
		apply:  build(""),
	}
	res := wr.updateStatus(ctx, op)
	if res == writePermanent {
		op.apply = build(classStatusWriteRefused)
		wr.updateStatus(ctx, op)
	}
	p.note(key, res)
}

func (p *passWriter) writeNodeCondition(ctx context.Context, ns *nodeState) {
	key := "Node/" + ns.obj.Name
	act := planNodeCondition(ns)
	if act == nodeCondNone || !p.gate(key) {
		return
	}
	wr := p.c.wr
	switch act {
	case nodeCondRemove:
		p.note(key, wr.applyNodeCondition(ctx, ns.obj.Name, nil))
	case nodeCondPost:
		cond := desiredNodeCondition(ns, "", p.w.now)
		if nodeConditionSame(ns, cond) {
			return
		}
		res := wr.applyNodeCondition(ctx, ns.obj.Name, &cond)
		if res == writePermanent {
			fb := desiredNodeCondition(ns, classStatusWriteRefused, p.w.now)
			wr.applyNodeCondition(ctx, ns.obj.Name, &fb)
		}
		p.note(key, res)
	}
}
