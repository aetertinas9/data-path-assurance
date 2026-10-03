package liveingest

import (
	"context"
	"fmt"
	"slices"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// invalidInput is the error of a query the engine cannot answer because the
// caller's own input is unusable. The detail names the input, never a value.
func invalidInput(what string) error {
	return fmt.Errorf("liveingest: %s: %w", what, fleet.ErrInvalidInput)
}

// NodeBundles implements app.LiveBundleSource (GLI-075). It builds one
// assessment bundle per intent from the published view of the node, at the
// assessment time q.Now: the topology, digests and window come from the last
// accepted frame and the ring behind it, the findings are evaluated at q.Now.
// A node without an active view, or when this process does not lead, is
// app.ErrNoObservation. It changes no state apart from releasing a view that
// retention expired, and it is safe for concurrent use.
func (e *Engine) NodeBundles(ctx context.Context, q app.LiveBundleQuery) (app.LiveBundleSet, error) {
	// (a) the query itself
	if q.Node.ClusterID != e.cfg.ClusterID || q.Node.UID == "" {
		return app.LiveBundleSet{}, invalidInput("node")
	}
	policy, err := fleet.NewPolicy(q.Policy)
	if err != nil {
		return app.LiveBundleSet{}, err
	}
	devices := make(map[string]struct{}, len(q.Intents))
	for _, in := range q.Intents {
		if err := in.Validate(); err != nil {
			return app.LiveBundleSet{}, err
		}
		if _, dup := devices[in.Device.UID]; dup {
			return app.LiveBundleSet{}, invalidInput("intents")
		}
		devices[in.Device.UID] = struct{}{}
	}
	if err := ctx.Err(); err != nil {
		return app.LiveBundleSet{}, err
	}

	// (b) leadership and the published view
	if !e.Leading() {
		return app.LiveBundleSet{}, app.ErrNoObservation
	}
	n := e.find(q.Node.UID)
	if n == nil {
		return app.LiveBundleSet{}, app.ErrNoObservation
	}
	v := n.view.Load()
	if v == nil {
		return app.LiveBundleSet{}, app.ErrNoObservation
	}
	if now := e.cfg.Clock.Now(); v.expired(now, e.cfg.NodeRetention) {
		n.expire(now, e.cfg.NodeRetention)
		return app.LiveBundleSet{}, app.ErrNoObservation
	}

	// (c) the bundles of the one view
	ev, err := v.evaluation(policy.Freshness)
	if err != nil {
		return app.LiveBundleSet{}, err
	}
	last := v.last()
	rev := last.c.Envelope.BundleRevision
	findings := pcie.EvaluateLinkWidth(ev.window, q.Now)
	template := fleet.AssessmentBundle{
		Policy:                policy,
		Snapshot:              last.c.Envelope,
		Admitted:              v.cursor,
		GraphRevision:         rev,
		WindowRevision:        rev,
		TopologyDigest:        last.topologyDigest,
		BaselineDigest:        ev.baselineDigest,
		Topology:              last.topology,
		Window:                ev.window,
		Provenance:            cloneProvenance(last.c.Provenance),
		Bindings:              cloneBindings(last.c.Bindings),
		Findings:              findings,
		FindingsEvaluatedAt:   q.Now,
		FindingsGraphRevision: rev,
		CollectorTrust: fleet.CollectorTrustProfile{
			ID:        e.cfg.CollectorProfileID,
			Mode:      fleet.TrustModeLive,
			ClusterID: e.cfg.ClusterID,
			NodeUID:   q.Node.UID,
			Session:   v.cursor.Session,
			Sources:   slices.Clone(e.cfg.TrustedSources),
		},
	}
	findingRecs := findingRecords(findings)
	evidenceRecs := ev.recordsFor(findings, policy.Freshness)

	out := app.LiveBundleSet{Devices: make([]app.LiveDeviceBundle, 0, len(q.Intents))}
	for _, in := range q.Intents {
		b := template
		b.Intent = in
		out.Devices = append(out.Devices, app.LiveDeviceBundle{
			DeviceUID: in.Device.UID,
			Bundle:    b,
			Findings:  findingRecs,
			Evidence:  evidenceRecs,
		})
	}
	return out, nil
}

// cloneAsset copies an asset reference including the aliases' raw bytes.
func cloneAsset(a model.AssetRef) model.AssetRef {
	if a.Aliases != nil {
		aliases := make([]model.TypedID, len(a.Aliases))
		for i, al := range a.Aliases {
			al.Raw = slices.Clone(al.Raw)
			aliases[i] = al
		}
		a.Aliases = aliases
	}
	return a
}

func cloneProvenance(in []fleet.EdgeProvenance) []fleet.EdgeProvenance {
	out := slices.Clone(in)
	for i := range out {
		out[i].Edge.From = cloneAsset(out[i].Edge.From)
		out[i].Edge.To = cloneAsset(out[i].Edge.To)
	}
	return out
}

func cloneBindings(in []fleet.ObservedBinding) []fleet.ObservedBinding {
	out := slices.Clone(in)
	for i := range out {
		out[i].Function = cloneAsset(out[i].Function)
	}
	return out
}
