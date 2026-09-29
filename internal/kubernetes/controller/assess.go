package controller

import (
	"context"
	"slices"
	"sort"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// assessNodes runs the assessment part of a pass (GKA-070(e)(f)): it forgets
// the continuity of nodes that stopped being assessable, then calls the
// assessor once per assessable node in Node UID order and records the
// validated verdict on the world. It reports false when ctx ended.
func (c *Controller) assessNodes(ctx context.Context, w *world) bool {
	type plan struct {
		ns  *nodeState
		req app.NodeAssessmentRequest
	}
	var plans []plan
	current := map[string]struct{}{}
	for _, ns := range w.nodes {
		if ns.scope != nodeSelected || len(ns.dStar) == 0 {
			continue
		}
		fs := ns.fleets[0]
		if class := c.checkRequest(ns, fs); class != "" {
			markNodeInternalError(ns, class)
			continue
		}
		plans = append(plans, plan{ns: ns, req: c.buildRequest(w, ns, fs)})
		current[string(ns.obj.UID)] = struct{}{}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].ns.obj.UID < plans[j].ns.obj.UID })

	var forget []string
	for uid := range c.assessable {
		if _, ok := current[uid]; !ok {
			forget = append(forget, uid)
		}
	}
	sort.Strings(forget)
	for _, uid := range forget {
		c.opts.assessor.ForgetNode(uid)
	}
	c.assessable = current

	for _, p := range plans {
		if !c.active(ctx) {
			return false
		}
		actx, cancel := context.WithTimeout(ctx, c.opts.resyncInterval)
		out, err := c.opts.assessor.AssessNode(actx, p.req)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			markNodeInternalError(p.ns, classAssessorFailed)
			continue
		}
		c.applyAssessment(p.ns, p.req, out)
	}
	return c.active(ctx)
}

// checkRequest is the GKA-034 pre-check: the fleet policy and every intent must
// validate before the assessor is called.
func (c *Controller) checkRequest(ns *nodeState, fs *fleetState) string {
	if fs.policyErr != nil {
		return classInvalidIntent
	}
	for _, ds := range ns.dStar {
		if buildIntent(ds, ns, c.opts.clusterID).Validate() != nil {
			return classInvalidIntent
		}
	}
	return ""
}

// buildRequest assembles a fresh request; the assessor may change its slices.
func (c *Controller) buildRequest(w *world, ns *nodeState, fs *fleetState) app.NodeAssessmentRequest {
	intents := make([]fleet.Intent, 0, len(ns.dStar))
	for _, ds := range ns.dStar {
		intents = append(intents, buildIntent(ds, ns, c.opts.clusterID))
	}
	policy := fs.policy
	policy.RequiredCoverage = slices.Clone(policy.RequiredCoverage)
	return app.NodeAssessmentRequest{
		Node:      fleet.NodeRef{ClusterID: c.opts.clusterID, Name: ns.obj.Name, UID: string(ns.obj.UID)},
		FleetUID:  string(fs.obj.UID),
		Policy:    policy,
		Selection: ns.sel,
		Intents:   intents,
		Now:       w.now,
	}
}

// markNodeInternalError renders the node and its assessable devices as an
// InternalError scope (GKA-084).
func markNodeInternalError(ns *nodeState, class string) {
	ns.assessed = false
	ns.eval = newNodeEval(reasonInternalError)
	ns.eval.class = class
	for _, ds := range ns.dStar {
		ds.scope = deviceInternalError
		ds.class = class
		ds.proj = nil
		ds.noObservation = false
	}
}

// applyAssessment validates an assessor result (GKA-085) and records it, or
// renders the node as InternalError when it does not validate.
func (c *Controller) applyAssessment(ns *nodeState, req app.NodeAssessmentRequest, out app.NodeAssessment) {
	projs, eval, err := validateAssessment(req, out)
	if err != nil {
		markNodeInternalError(ns, classInvalidAssessment)
		return
	}
	ns.assessed = true
	ns.eval = eval
	for _, ds := range ns.dStar {
		if p, ok := projs[string(ds.obj.UID)]; ok {
			ds.proj = p
		} else if eval.noObservation {
			ds.noObservation = true
		}
	}
}

const (
	reasonInternalError = "InternalError"
	reasonValidating    = "Validating"
)

// validateAssessment applies GKA-085 to an assessor result and returns the
// device projections by device UID and the node verdict.
func validateAssessment(req app.NodeAssessmentRequest, out app.NodeAssessment) (map[string]*deviceProjection, nodeEval, error) {
	eval := newNodeEval(reasonValidating)
	if len(out.Devices) == 0 {
		if out.Node != nil || out.Observation != nil {
			return nil, eval, invalidAssessment("node verdict without device decisions")
		}
		eval.noObservation = true
		return nil, eval, nil
	}
	obs := out.Observation
	if obs == nil || obs.GraphRevision == "" || !okString(obs.GraphRevision, 1, 128) {
		return nil, eval, invalidAssessment("observation is missing or has no graph revision")
	}
	if obs.Completeness != fleet.CompletenessComplete && obs.Completeness != fleet.CompletenessPartial {
		return nil, eval, invalidAssessment("observation completeness is invalid")
	}
	intents := make(map[string]fleet.Intent, len(req.Intents))
	for _, in := range req.Intents {
		intents[in.Device.UID] = in
	}
	projs := make(map[string]*deviceProjection, len(out.Devices))
	for _, da := range out.Devices {
		dec := da.Decision
		if err := dec.Validate(); err != nil {
			return nil, eval, invalidAssessment("decision does not validate")
		}
		in, ok := intents[dec.DeviceUID]
		if !ok {
			return nil, eval, invalidAssessment("decision for a device outside the request")
		}
		if _, dup := projs[dec.DeviceUID]; dup {
			return nil, eval, invalidAssessment("two decisions for one device")
		}
		if dec.NodeUID != req.Node.UID || dec.PolicyRevision != req.Policy.Revision || dec.RequestID != in.RequestID ||
			dec.MetadataGeneration != in.MetadataGeneration || dec.Desired != in.Desired {
			return nil, eval, invalidAssessment("decision does not match its intent")
		}
		if dec.GraphRevision != obs.GraphRevision {
			return nil, eval, invalidAssessment("decision graph revision differs from the observation")
		}
		p, err := projectDecision(da)
		if err != nil {
			return nil, eval, err
		}
		projs[dec.DeviceUID] = p
	}
	eval.obs = &app.NodeObservation{GraphRevision: obs.GraphRevision, Completeness: obs.Completeness}
	if nd := out.Node; nd != nil {
		if err := nd.Validate(); err != nil {
			return nil, eval, invalidAssessment("node decision does not validate")
		}
		if nd.NodeUID != req.Node.UID || nd.FleetUID != req.FleetUID {
			return nil, eval, invalidAssessment("node decision identity differs from the request")
		}
		wantSel := fleet.SelectionComplete
		if req.Selection == fleet.SelectionPartial || len(out.Devices) < len(req.Intents) {
			wantSel = fleet.SelectionPartial
		}
		if nd.Selection != wantSel || nd.DeviceCount != len(out.Devices) {
			return nil, eval, invalidAssessment("node decision selection or count is inconsistent")
		}
		if nd.Selection == fleet.SelectionPartial && (nd.Eligibility != fleet.EligibilityUnknown || nd.Qualification != fleet.QualificationUnknown) {
			return nil, eval, invalidAssessment("partial node decision is not unknown")
		}
		if nd.Eligibility == fleet.EligibilityEligible && nd.Qualification == fleet.QualificationDisqualified {
			return nil, eval, invalidAssessment("eligible node is disqualified")
		}
		if !inV22(nd.Reason) {
			return nil, eval, invalidAssessment("node reason is not a condition reason")
		}
		cp := *nd
		eval.decision = &cp
		eval.eligibility = nd.Eligibility
		eval.qualification = nd.Qualification
		eval.reason = nd.Reason
	}
	return projs, eval, nil
}
