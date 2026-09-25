package app

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/graph"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// allocationUnknownReason is the allocation reason when no allocation
// evidence is available.
const allocationUnknownReason = "AllocationUnknown"

// formatTime renders an instant in UTC RFC 3339 with nanoseconds, trailing
// fractional zeros removed.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func optionalTime(t time.Time) Optional {
	if t.IsZero() {
		return Optional{}
	}
	return some(formatTime(t))
}

// assembler holds what the explanation of one request is built from.
type assembler struct {
	r        *Replay
	res      *replayResult
	kind     TargetKind
	frame    *Frame // result frame
	outputs  []int  // output device indexes, by device UID
	limits   map[string]Limitation
	edges    []graph.Edge
	outgoing map[string][]int // from key -> result frame edge indexes
	// trusted holds, per result frame edge, the provenance that makes it a
	// trusted hop, or nil when it is not one.
	trusted []*fleet.EdgeProvenance
}

// assemble builds the explanation of the request from the evaluated replay.
func assemble(r *Replay, res *replayResult, kind TargetKind, target int) (Explanation, error) {
	a := &assembler{r: r, res: res, kind: kind, frame: &r.Frames[res.result], limits: map[string]Limitation{}}
	if kind == TargetGPU {
		a.outputs = []int{target}
	} else {
		for i := range r.Devices {
			a.outputs = append(a.outputs, i)
		}
		slices.SortFunc(a.outputs, func(x, y int) int {
			return strings.Compare(r.Devices[x].Device.UID, r.Devices[y].Device.UID)
		})
	}
	a.indexTopology()

	e := Explanation{
		Kind:                   kind,
		NodeName:               r.Node.Name,
		NodeUID:                r.Node.UID,
		NodeEligibility:        res.node.Eligibility.String(),
		AffectedWorkloads:      []WorkloadView{},
		ObservedAt:             formatTime(a.frame.Envelope.ObservedAt),
		EvaluatedAt:            formatTime(res.evaluated),
		NodeAssessmentRevision: res.node.AssessmentRevision,
		DeviceSummaries:        []DeviceSummary{},
	}
	if res.admitted {
		e.GraphRevision = some(a.frame.Envelope.BundleRevision)
	}
	if kind == TargetGPU {
		intent := r.Devices[target]
		dec := res.decisions[target]
		e.TargetName, e.TargetUID = intent.Device.Name, intent.Device.UID
		e.Identity = identityOf(dec)
		e.Phase = dec.Phase.String()
		e.Qualification = dec.Qualification.String()
		e.Reason = dec.Reason
		alloc, err := allocationOf(dec)
		if err != nil {
			return Explanation{}, err
		}
		e.Allocation = alloc
	} else {
		e.TargetName, e.TargetUID = r.Node.Name, r.Node.UID
		e.Qualification = res.node.Qualification.String()
		e.EligibilityReason = res.node.Reason
		for _, i := range a.outputs {
			intent, dec := r.Devices[i], res.decisions[i]
			e.DeviceSummaries = append(e.DeviceSummaries, DeviceSummary{
				Name:               intent.Device.Name,
				UID:                intent.Device.UID,
				DesiredState:       intent.Desired.String(),
				ObservedGeneration: strconv.FormatInt(intent.MetadataGeneration, 10),
				Qualification:      dec.Qualification.String(),
				LifecyclePhase:     dec.Phase.String(),
				Reason:             dec.Reason,
			})
		}
	}

	e.PathSegments = a.pathSegments()
	findings, err := a.activeFindings()
	if err != nil {
		return Explanation{}, err
	}
	e.ActiveFindings = findings
	coverage, err := a.coverage()
	if err != nil {
		return Explanation{}, err
	}
	e.Coverage = coverage
	a.deviceLimitations()
	a.frameLimitations()
	e.Limitations = a.sortedLimitations()

	finish(&e)
	return e, nil
}

// bound reports the bound function of an output device, if it is Bound.
func (a *assembler) bound(i int) (model.AssetRef, bool) {
	d := a.res.decisions[i]
	if d.BindingState != fleet.BindingBound {
		return model.AssetRef{}, false
	}
	return d.Binding.Function, true
}

func (a *assembler) addLimitation(code string, subject Optional) {
	a.limits[code+"\x00"+subject.Value] = Limitation{Code: code, Subject: subject}
}

func (a *assembler) sortedLimitations() []Limitation {
	out := make([]Limitation, 0, len(a.limits))
	for _, l := range a.limits {
		out = append(out, l)
	}
	slices.SortFunc(out, func(x, y Limitation) int {
		if c := strings.Compare(x.Code, y.Code); c != 0 {
			return c
		}
		return strings.Compare(x.Subject.Value, y.Subject.Value)
	})
	return out
}

func identityOf(d fleet.DeviceDecision) *Identity {
	id := &Identity{State: d.BindingState.String(), Reason: d.Reason}
	if d.BindingState != fleet.BindingBound {
		return id
	}
	b := d.Binding
	id.Reason = "Ready"
	id.Vendor = some(b.Claim.Vendor)
	id.UUID = some(b.Claim.UUID)
	if b.Claim.Serial != "" {
		id.Serial = some(b.Claim.Serial)
	}
	id.NodeUID = some(b.Node.UID)
	id.BootID = some(b.BootID)
	id.BDF = some(b.BDF)
	id.FunctionKey = some(b.Function.Key())
	id.Source = &SourceView{Type: b.Source.Type, Name: b.Source.Name}
	id.EvidenceID = some(b.EvidenceID)
	id.ObservedAt = some(formatTime(b.ObservedAt))
	id.ExpiresAt = some(formatTime(b.ExpiresAt))
	return id
}

func allocationOf(d fleet.DeviceDecision) (*AllocationView, error) {
	if d.Allocation != fleet.AllocationUnknown {
		return nil, evaluationError("allocation evidence is not available offline")
	}
	return &AllocationView{
		State:             d.Allocation.String(),
		Reason:            allocationUnknownReason,
		EvidenceRefs:      []string{},
		AffectedWorkloads: []WorkloadView{},
	}, nil
}

// indexTopology indexes the result frame edges by their source asset and
// marks the trusted hops: observed provenance from a source trusted for the
// physical parent capability that is fresh at the evaluation instant.
func (a *assembler) indexTopology() {
	a.edges = a.frame.Edges
	a.outgoing = map[string][]int{}
	byEdge := provenanceFor(a.frame.Provenance)
	a.trusted = make([]*fleet.EdgeProvenance, len(a.edges))
	now := a.res.evaluated
	freshness := a.r.Policy.Freshness
	for i, e := range a.edges {
		a.outgoing[e.From.Key()] = append(a.outgoing[e.From.Key()], i)
		for _, p := range byEdge[edgeIdentity(e)] {
			if p.Kind != fleet.EdgeEvidenceObserved || !a.trustedFor(fleet.TrustSysfsPhysicalParent, p.Source) {
				continue
			}
			deadline := p.ObservedAt.Add(freshness)
			if p.ExpiresAt.Before(deadline) {
				deadline = p.ExpiresAt
			}
			if !p.ObservedAt.After(now) && now.Before(deadline) {
				a.trusted[i] = &p
				break
			}
		}
	}
}

func (a *assembler) trustedFor(c fleet.TrustCapability, s model.SourceRef) bool {
	for _, t := range a.r.CollectorTrust.Sources {
		if t.Capability == c && t.Source == s {
			return true
		}
	}
	return false
}

// reach walks outgoing edges from start (over trusted hops only, or over all
// edges) and returns the shortest hop count to every reached asset key.
func (a *assembler) reach(start model.AssetRef, trustedOnly bool) map[string]int {
	dist := map[string]int{start.Key(): 0}
	queue := []string{start.Key()}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, i := range a.outgoing[u] {
			if trustedOnly && a.trusted[i] == nil {
				continue
			}
			v := a.edges[i].To.Key()
			if _, ok := dist[v]; !ok {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}
	return dist
}

// pathSegments selects the trusted hops reachable from the bound function of
// every Bound output device, each with its shortest depth.
func (a *assembler) pathSegments() []PathSegment {
	depth := map[int]int{}
	for _, i := range a.outputs {
		f, ok := a.bound(i)
		if !ok || !a.res.admitted {
			continue
		}
		dist := a.reach(f, true)
		for key, d := range dist {
			for _, ei := range a.outgoing[key] {
				if a.trusted[ei] == nil {
					continue
				}
				if old, seen := depth[ei]; !seen || d < old {
					depth[ei] = d
				}
			}
		}
	}
	out := make([]PathSegment, 0, len(depth))
	for ei, d := range depth {
		e, p := a.edges[ei], a.trusted[ei]
		out = append(out, PathSegment{
			From:       e.From.Key(),
			Relation:   e.Relation.String(),
			To:         e.To.Key(),
			Origin:     e.Origin.String(),
			Kind:       p.Kind.String(),
			Source:     SourceView{Type: p.Source.Type, Name: p.Source.Name},
			EvidenceID: p.EvidenceID,
			ObservedAt: formatTime(p.ObservedAt),
			ExpiresAt:  formatTime(p.ExpiresAt),
			depth:      d,
		})
	}
	slices.SortFunc(out, func(x, y PathSegment) int {
		if x.depth != y.depth {
			return x.depth - y.depth
		}
		if c := compareStrings(x.From, y.From); c != 0 {
			return c
		}
		if c := compareStrings(x.To, y.To); c != 0 {
			return c
		}
		if c := compareStrings(x.Relation, y.Relation); c != 0 {
			return c
		}
		return compareStrings(x.Origin, y.Origin)
	})
	return out
}

func compareStrings(x, y string) int { return strings.Compare(x, y) }

// activeFindings selects the result frame findings the output devices'
// decisions applied, and records the findings that were not applied and the
// applied width findings whose evidence comes from an untrusted source.
func (a *assembler) activeFindings() ([]FindingView, error) {
	selected := map[string]bool{}
	for _, i := range a.outputs {
		for _, id := range a.res.decisions[i].FindingIDs {
			selected[id] = true
		}
	}
	var out []FindingView
	var applied []model.Finding
	for _, f := range a.res.findings {
		if selected[f.ID] && a.res.admitted {
			applied = append(applied, f)
			continue
		}
		if a.kind == TargetNode {
			a.addLimitation("finding_not_applied", some(f.ID))
			continue
		}
		if fn, ok := a.bound(a.outputs[0]); ok && scopeHas(f.Scope, fn) {
			a.addLimitation("finding_not_applied", some(f.ID))
		}
	}
	// The window index is built only when a width finding needs it.
	var observations map[string]model.Observation
	for _, f := range applied {
		if f.Type != model.FindingPCIeLinkWidthDegraded {
			out = append(out, findingView(f))
			continue
		}
		if observations == nil {
			observations = a.windowObservations()
		}
		if a.untrustedWidthEvidence(f, observations) {
			a.addLimitation("finding_source_untrusted", some(f.ID))
		}
		out = append(out, findingView(f))
	}
	slices.SortFunc(out, func(x, y FindingView) int { return strings.Compare(x.ID, y.ID) })
	if out == nil {
		out = []FindingView{}
	}
	return out, nil
}

func scopeHas(scope []model.AssetRef, a model.AssetRef) bool {
	for _, s := range scope {
		if s.Key() == a.Key() {
			return true
		}
	}
	return false
}

// windowObservations indexes the result window's observations by ID.
func (a *assembler) windowObservations() map[string]model.Observation {
	out := map[string]model.Observation{}
	w := a.res.window
	for _, subject := range w.Subjects() {
		signals, err := w.Signals(subject)
		if err != nil {
			continue
		}
		for _, signal := range signals {
			series, err := w.SeriesFor(subject, signal)
			if err != nil {
				continue
			}
			for _, s := range series {
				for _, o := range s.Observations() {
					if _, ok := out[o.ID]; !ok {
						out[o.ID] = o
					}
				}
			}
		}
	}
	return out
}

// untrustedWidthEvidence reports whether any evidence observation of f found
// in the window is from a source the profile does not trust for PCIe width,
// or claims an operator-verified baseline from a source not trusted for it.
func (a *assembler) untrustedWidthEvidence(f model.Finding, obs map[string]model.Observation) bool {
	for _, ref := range f.Evidence {
		o, ok := obs[ref.ObservationID]
		if !ok {
			continue
		}
		if !a.trustedFor(fleet.TrustSysfsPCIeWidth, o.Source) {
			return true
		}
		if o.Dimensions[pcie.DimensionExpectedProvenance] == pcie.ProvenanceOperatorVerifiedWiring && !a.trustedFor(fleet.TrustOperatorBaseline, o.Source) {
			return true
		}
	}
	return false
}

func findingView(f model.Finding) FindingView {
	v := FindingView{
		ID:            f.ID,
		Type:          f.Type.String(),
		Severity:      f.Severity.String(),
		State:         f.State.String(),
		Confidence:    f.Confidence.String(),
		Scope:         make([]string, 0, len(f.Scope)),
		Evidence:      make([]EvidenceView, 0, len(f.Evidence)),
		MissingInputs: make([]string, 0, len(f.MissingInputs)),
		Affected:      make([]ImpactView, 0, len(f.Affected)),
		FirstSeen:     formatTime(f.FirstSeen),
		LastSeen:      formatTime(f.LastSeen),
		Explanation:   f.Explanation,
		SuggestedStep: f.SuggestedStep,
	}
	for _, s := range f.Scope {
		v.Scope = append(v.Scope, s.Key())
	}
	for _, e := range f.Evidence {
		v.Evidence = append(v.Evidence, EvidenceView{ObservationID: e.ObservationID, Summary: e.Summary})
	}
	for _, m := range f.MissingInputs {
		v.MissingInputs = append(v.MissingInputs, m.String())
	}
	for _, i := range f.Affected {
		v.Affected = append(v.Affected, ImpactView{Asset: i.Asset.Key(), Accuracy: i.Accuracy.String()})
	}
	return v
}

// deviceCoverage lists every policy coverage entry of one device, by name:
// the device decision's assessment when it has one, and a synthetic Unknown
// entry carrying the decision reason otherwise.
func (a *assembler) deviceCoverage(i int) ([]CoverageView, error) {
	dec := a.res.decisions[i]
	byName := map[string]fleet.CoverageAssessment{}
	policyNames := map[string]bool{}
	for _, c := range a.r.Policy.RequiredCoverage {
		policyNames[c.Name] = true
	}
	for _, c := range dec.Coverage {
		if !policyNames[c.Name] {
			return nil, evaluationError("a coverage assessment names no policy entry")
		}
		byName[c.Name] = c
	}
	out := make([]CoverageView, 0, len(a.r.Policy.RequiredCoverage))
	for _, c := range a.r.Policy.RequiredCoverage {
		v := CoverageView{Name: c.Name, Required: c.Required, deviceUID: a.r.Devices[i].Device.UID}
		if as, ok := byName[c.Name]; ok {
			v.PathKind = as.PathKind
			v.State = as.State.String()
			v.Reason = as.Reason
			v.EvidenceRefs = sortedUnique(as.EvidenceIDs)
			v.ObservedAt = optionalTime(as.ObservedAt)
			v.LatestObservedAt = optionalTime(as.LatestObservedAt)
			v.ExpiresAt = optionalTime(as.ExpiresAt)
		} else {
			v.PathKind = c.PathKind
			v.State = fleet.CoverageUnknown.String()
			v.Reason = dec.Reason
			v.EvidenceRefs = []string{}
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(x, y CoverageView) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

// coverage lists the GPU device's coverage, or for a node the unmet required
// coverage of every device.
func (a *assembler) coverage() ([]CoverageView, error) {
	if a.kind == TargetGPU {
		return a.deviceCoverage(a.outputs[0])
	}
	out := []CoverageView{}
	for _, i := range a.outputs {
		list, err := a.deviceCoverage(i)
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			if !v.Required || v.State == fleet.CoverageNormal.String() {
				continue
			}
			v.Device = some(a.r.Devices[i].Device.Name)
			out = append(out, v)
		}
	}
	return out, nil
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// deviceLimitations records the limitations that concern output devices.
func (a *assembler) deviceLimitations() {
	r := a.r
	if a.kind == TargetNode && len(r.Devices) == 0 {
		a.addLimitation("no_devices", Optional{})
	}
	for _, i := range a.outputs {
		intent := r.Devices[i]
		bundle := a.res.bundles[i]
		if bundle.Allocation == nil {
			a.addLimitation("allocation_unavailable", Optional{})
		}
		if intent.Desired != fleet.DesiredInService && bundle.Fence == nil {
			a.addLimitation("fence_unavailable", Optional{})
		}
		if intent.Node.Name != r.Node.Name {
			a.addLimitation("node_name_differs", some(intent.Device.Name))
		}
		f, ok := a.bound(i)
		if !ok {
			continue
		}
		subject := some(f.Key())
		all := a.reach(f, false)
		for key := range all {
			for _, ei := range a.outgoing[key] {
				if a.trusted[ei] == nil {
					a.addLimitation("path_untrusted", subject)
				}
			}
		}
		if a.pathContradicts(f, all) {
			a.addLimitation("path_contradiction", subject)
		}
		if out := a.outgoing[f.Key()]; len(out) == 1 && a.edges[out[0]].To.Key() == a.res.nodeAsset.Key() {
			a.addLimitation("root_port_absent", subject)
		}
		if !a.hasCurrentWidth(f) {
			a.addLimitation("width_pair_absent", subject)
		}
	}
}

// pathContradicts reports whether an asset reachable from f has two or more
// outgoing edges, or the single outgoing path from f revisits an asset.
func (a *assembler) pathContradicts(f model.AssetRef, reachable map[string]int) bool {
	for key := range reachable {
		if len(a.outgoing[key]) >= 2 {
			return true
		}
	}
	visited := map[string]bool{f.Key(): true}
	current := f.Key()
	for {
		out := a.outgoing[current]
		if len(out) == 0 {
			return false
		}
		next := a.edges[out[0]].To.Key()
		if visited[next] {
			return true
		}
		visited[next] = true
		current = next
	}
}

func (a *assembler) hasCurrentWidth(f model.AssetRef) bool {
	for _, o := range a.frame.Observations {
		if o.Signal == pcie.SignalLinkWidthCurrent && o.Subject.Key() == f.Key() {
			return true
		}
	}
	return false
}

// classificationInputs are the attribute names whose observation failure
// leaves GPU classification unknown.
var classificationInputs = map[string]bool{"class": true, "vendor": true, "physfn": true}

// frameLimitations records the limitations that concern the replay and the
// result frame.
func (a *assembler) frameLimitations() {
	r, res, f := a.r, a.res, a.frame
	a.addLimitation("traffic_path_unverified", Optional{})
	if r.CollectorTrust.Mode == fleet.TrustModeOffline {
		a.addLimitation("offline", Optional{})
		a.addLimitation("offline_trust_not_live", some(r.CollectorTrust.ID))
	}
	if !res.admitted {
		a.addLimitation("no_admitted_frame", some(res.firstOrder.String()))
	}
	for k := 0; k <= res.result; k++ {
		e := r.Frames[k].Envelope
		if e.Completeness == fleet.CompletenessPartial {
			a.addLimitation("frame_partial", some(strconv.FormatUint(e.Sequence, 10)))
		}
	}
	classification := false
	for _, d := range f.Diagnostics {
		a.addLimitation("collector:"+d.Code, some(d.Subject))
		switch {
		case d.Code == "sysfs_list_failed", d.Code == "sysfs_link_invalid", d.Code == "sysfs_link_escape":
			classification = true
		case strings.HasPrefix(d.Code, "sysfs_attr_"):
			name := d.Subject
			if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
				name = name[slash+1:]
			}
			if classificationInputs[name] {
				classification = true
			}
		}
	}
	if len(f.Diagnostics) > 0 {
		a.addLimitation("diagnostics_outside_digest", Optional{})
	}
	if f.DiagnosticsTruncated {
		a.addLimitation("diagnostics_truncated", some(strconv.FormatUint(f.DiagnosticsTotal, 10)))
	}
	if classification {
		a.addLimitation("nvidia_classification_unknown", Optional{})
	}
}
