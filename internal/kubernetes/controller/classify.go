package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

// buildWorld classifies every fleet, node and device of one pass (GKA-060 to
// GKA-069). It never mutates the cached objects it is given.
func buildWorld(now time.Time, fleets []*v1alpha1.GPUFleet, nodes []*corev1.Node, devices []*v1alpha1.GPUDevice, paths []*v1alpha1.NodePathState) *world {
	w := &world{
		now:       now,
		fleetBy:   make(map[string]*fleetState, len(fleets)),
		nodeBy:    make(map[string]*nodeState, len(nodes)),
		devicesBy: make(map[string][]*deviceState),
		paths:     make(map[string]*v1alpha1.NodePathState, len(paths)),
	}
	for _, p := range paths {
		w.paths[p.Name] = p
	}

	sortedFleets := slices.Clone(fleets)
	sort.Slice(sortedFleets, func(i, j int) bool { return sortedFleets[i].Name < sortedFleets[j].Name })
	for _, f := range sortedFleets {
		fs := &fleetState{obj: f, enforce: f.Spec.Mode == v1alpha1.FleetModeEnforce}
		fs.validSel = validSelector(&f.Spec.NodeSelector)
		fs.policy, fs.policyErr = buildPolicy(f)
		w.fleets = append(w.fleets, fs)
		w.fleetBy[f.Name] = fs
	}

	sortedNodes := slices.Clone(nodes)
	sort.Slice(sortedNodes, func(i, j int) bool { return sortedNodes[i].Name < sortedNodes[j].Name })
	for _, n := range sortedNodes {
		ns := &nodeState{obj: n, existing: w.paths[n.Name]}
		w.nodes = append(w.nodes, ns)
		w.nodeBy[n.Name] = ns
	}

	// S(N): fleets with a valid selector that match the Node's labels.
	for _, fs := range w.fleets {
		if !fs.validSel {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(&fs.obj.Spec.NodeSelector)
		if err != nil {
			fs.validSel = false
			continue
		}
		for _, ns := range w.nodes {
			if sel.Matches(labels.Set(ns.obj.Labels)) {
				fs.selected = append(fs.selected, ns)
				ns.fleets = append(ns.fleets, fs)
			}
		}
		fs.overCap = len(fs.selected) > maxFleetNodes
	}

	sortedDevices := slices.Clone(devices)
	sort.Slice(sortedDevices, func(i, j int) bool { return sortedDevices[i].Name < sortedDevices[j].Name })
	for _, d := range sortedDevices {
		ds := &deviceState{obj: d, intentObservedAt: intentObservedAt(d, now)}
		ds.fleet = w.fleetBy[d.Spec.FleetRef.Name]
		ds.node = w.nodeBy[d.Spec.NodeRef.Name]
		w.devices = append(w.devices, ds)
		w.devicesBy[d.Spec.NodeRef.Name] = append(w.devicesBy[d.Spec.NodeRef.Name], ds)
	}
	markIdentityConflicts(w.devices)

	for _, ds := range w.devices {
		ds.scope, ds.class = deviceScopeOf(ds)
		if ds.scope != deviceIdentityConflict {
			ds.peers = nil // peer= belongs to IdentityConflict scope messages only (GKA-075)
		}
	}
	for _, ns := range w.nodes {
		classifyNode(w, ns)
	}
	for _, fs := range w.fleets {
		for _, ns := range fs.selected {
			if len(devicesOf(w, ns, fs)) > 0 {
				fs.anyDevices = true
				break
			}
		}
	}
	return w
}

// validSelector reports whether a nodeSelector converts and is not empty
// (GKA-066).
func validSelector(s *metav1.LabelSelector) bool {
	if len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0 {
		return false
	}
	_, err := metav1.LabelSelectorAsSelector(s)
	return err == nil
}

// ceilSecond rounds t up to a whole second, in UTC.
func ceilSecond(t time.Time) time.Time {
	u := t.UTC().Truncate(time.Second)
	if u.Before(t) {
		u = u.Add(time.Second)
	}
	return u
}

// intentObservedAt is the intent observation time of a device (GKA-071): the
// stored one while generation and request id are unchanged, otherwise now
// rounded up to the second.
func intentObservedAt(d *v1alpha1.GPUDevice, now time.Time) time.Time {
	st := d.Status
	if st.ObservedRequestID != "" && st.ObservedGeneration == d.Generation &&
		st.ObservedRequestID == d.Spec.Request.ID && !st.IntentObservedAt.IsZero() {
		return st.IntentObservedAt.UTC()
	}
	return ceilSecond(now)
}

func claimKey(c v1alpha1.InventoryClaim) (string, bool) {
	if c.UUID != nil && *c.UUID != "" {
		return "u\x00" + c.Vendor + "\x00" + *c.UUID, true
	}
	if c.Serial != nil && *c.Serial != "" {
		return "s\x00" + c.Vendor + "\x00" + *c.Serial, true
	}
	return "", false
}

// markIdentityConflicts marks devices that share a claim with another device
// that is not being deleted (GKA-069). A Retired device is never marked itself.
func markIdentityConflicts(devices []*deviceState) {
	groups := map[string][]*deviceState{}
	for _, ds := range devices {
		if ds.obj.DeletionTimestamp != nil {
			continue
		}
		if key, ok := claimKey(ds.obj.Spec.InventoryClaim); ok {
			groups[key] = append(groups[key], ds)
		}
	}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		for _, ds := range group {
			if ds.obj.Spec.DesiredState == v1alpha1.DesiredStateRetired {
				continue
			}
			ds.scope = deviceIdentityConflict
			for _, other := range group {
				if other != ds {
					ds.peers = append(ds.peers, other.obj.Name)
				}
			}
			sort.Strings(ds.peers)
		}
	}
}

// deviceScopeOf applies the GKA-062 table. Rows 1 and 2 (UID mismatch) come
// before the identity conflict marked in markIdentityConflicts.
func deviceScopeOf(ds *deviceState) (deviceScope, string) {
	ref := ds.obj.Spec
	if ds.fleet != nil && string(ds.fleet.obj.UID) != ref.FleetRef.UID {
		return deviceUIDMismatch, ""
	}
	if ds.node != nil && string(ds.node.obj.UID) != ref.NodeRef.UID {
		return deviceUIDMismatch, ""
	}
	if ds.scope == deviceIdentityConflict {
		return deviceIdentityConflict, ""
	}
	if ds.fleet != nil && !ds.fleet.validSel {
		return deviceInternalError, classInvalidSelector
	}
	if ds.fleet == nil || ds.node == nil || !slices.Contains(ds.node.fleets, ds.fleet) {
		return deviceNoMatching, ""
	}
	if len(ds.node.fleets) >= 2 {
		return deviceConflict, ""
	}
	if ds.fleet.overCap {
		return deviceCapacity, ""
	}
	return deviceAssessable, ""
}

// devicesOf returns D(N,F): the devices whose node and fleet references match
// both name and UID.
func devicesOf(w *world, ns *nodeState, fs *fleetState) []*deviceState {
	var out []*deviceState
	for _, ds := range w.devicesBy[ns.obj.Name] {
		spec := ds.obj.Spec
		if spec.NodeRef.UID == string(ns.obj.UID) && spec.FleetRef.Name == fs.obj.Name && spec.FleetRef.UID == string(fs.obj.UID) {
			out = append(out, ds)
		}
	}
	return out
}

// classifyNode applies the GKA-067 table and, for the last row, the GKA-064
// selection table.
func classifyNode(w *world, ns *nodeState) {
	switch {
	case len(ns.fleets) == 0:
		ns.holdFleets = heldFleets(w, ns)
		if len(ns.holdFleets) > 0 {
			ns.scope = nodeHeld
			ns.d = heldDevices(w, ns)
		} else {
			ns.scope = nodeNotSelected
		}
	case len(ns.fleets) >= 2:
		ns.scope = nodeConflict
		seen := map[*deviceState]bool{}
		for _, fs := range ns.fleets {
			for _, ds := range devicesOf(w, ns, fs) {
				if !seen[ds] {
					seen[ds] = true
					ns.dUnion = append(ns.dUnion, ds)
				}
			}
		}
	case ns.fleets[0].overCap:
		ns.scope = nodeCapacity
		ns.d = devicesOf(w, ns, ns.fleets[0])
	default:
		ns.scope = nodeSelected
		fs := ns.fleets[0]
		ns.d = devicesOf(w, ns, fs)
		inD := map[*deviceState]bool{}
		for _, ds := range ns.d {
			inD[ds] = true
			if ds.scope != deviceIdentityConflict {
				ns.dStar = append(ns.dStar, ds)
			}
		}
		ns.xStar = len(ns.d) - len(ns.dStar)
		for _, ds := range w.devicesBy[ns.obj.Name] {
			if !inD[ds] && ds.obj.Spec.FleetRef.Name == fs.obj.Name {
				ns.xStar++
			}
		}
		switch {
		case len(ns.dStar) > 0 && ns.xStar == 0:
			ns.sel = fleet.SelectionComplete
		case len(ns.dStar) > 0:
			ns.sel = fleet.SelectionPartial
		case ns.xStar > 0:
			ns.sel = fleet.SelectionPartial
		default:
			ns.sel = fleet.SelectionNoDevices
		}
		sort.Slice(ns.dStar, func(i, j int) bool { return ns.dStar[i].obj.UID < ns.dStar[j].obj.UID })
	}
}

// heldFleets returns the names of the invalid-selector fleets that some device
// names as its fleet while pointing at this node by name (GKA-068).
func heldFleets(w *world, ns *nodeState) []string {
	set := map[string]bool{}
	for _, ds := range w.devicesBy[ns.obj.Name] {
		if fs := w.fleetBy[ds.obj.Spec.FleetRef.Name]; fs != nil && !fs.validSel {
			set[fs.obj.Name] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// heldDevices returns the devices that make a node held.
func heldDevices(w *world, ns *nodeState) []*deviceState {
	var out []*deviceState
	for _, ds := range w.devicesBy[ns.obj.Name] {
		if fs := w.fleetBy[ds.obj.Spec.FleetRef.Name]; fs != nil && !fs.validSel {
			out = append(out, ds)
		}
	}
	return out
}

// buildPolicy assembles the fleet.Policy of a GPUFleet (GKA-080).
func buildPolicy(f *v1alpha1.GPUFleet) (fleet.Policy, error) {
	cov := make([]fleet.CoverageRequirement, 0, len(f.Spec.RequiredCoverage))
	for _, r := range f.Spec.RequiredCoverage {
		cov = append(cov, fleet.CoverageRequirement{Name: r.Name, PathKind: r.PathKind, Required: r.Required})
	}
	sort.Slice(cov, func(i, j int) bool { return cov[i].Name < cov[j].Name })
	p := fleet.Policy{
		Revision:         policyRevision(string(f.UID), f.Spec.FreshnessSeconds, f.Spec.ReadyForSeconds, cov),
		RequiredCoverage: cov,
		Freshness:        time.Duration(f.Spec.FreshnessSeconds) * time.Second,
		ReadyFor:         time.Duration(f.Spec.ReadyForSeconds) * time.Second,
	}
	if _, err := fleet.NewPolicy(p); err != nil {
		return p, err
	}
	return p, nil
}

// policyRevision is a deterministic function of the fleet UID, the two windows
// and the name-ordered coverage list only.
func policyRevision(uid string, freshness, readyFor int32, cov []fleet.CoverageRequirement) string {
	h := sha256.New()
	h.Write([]byte("dpa.Policy.v1\x00"))
	h.Write([]byte(uid + "\x00"))
	h.Write([]byte(strconv.FormatInt(int64(freshness), 10) + "\x00"))
	h.Write([]byte(strconv.FormatInt(int64(readyFor), 10) + "\x00"))
	for _, r := range cov {
		h.Write([]byte(r.Name + "\x00" + r.PathKind + "\x00" + strconv.FormatBool(r.Required) + "\x00"))
	}
	return "p1:" + hex.EncodeToString(h.Sum(nil))
}

// desiredOf converts the CRD desired state to the domain value (zero when
// unknown, which fails Intent.Validate).
func desiredOf(s v1alpha1.DesiredState) fleet.DesiredState {
	switch s {
	case v1alpha1.DesiredStateInService:
		return fleet.DesiredInService
	case v1alpha1.DesiredStateMaintenance:
		return fleet.DesiredMaintenance
	case v1alpha1.DesiredStateRetired:
		return fleet.DesiredRetired
	}
	return 0
}

// baselinePhase is the lifecycle phase of a device without a decision (GKA-072).
func baselinePhase(s v1alpha1.DesiredState) v1alpha1.LifecyclePhase {
	switch s {
	case v1alpha1.DesiredStateInService:
		return v1alpha1.LifecyclePhasePending
	case v1alpha1.DesiredStateMaintenance:
		return v1alpha1.LifecyclePhaseMaintenancePending
	case v1alpha1.DesiredStateRetired:
		return v1alpha1.LifecyclePhaseRetiring
	}
	return v1alpha1.LifecyclePhaseUnknown
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// buildIntent assembles the fleet.Intent of a device (GKA-081).
func buildIntent(ds *deviceState, ns *nodeState, clusterID string) fleet.Intent {
	spec := ds.obj.Spec
	return fleet.Intent{
		Device:             fleet.DeviceRef{Name: ds.obj.Name, UID: string(ds.obj.UID)},
		Node:               fleet.NodeRef{ClusterID: clusterID, Name: ns.obj.Name, UID: string(ns.obj.UID)},
		Desired:            desiredOf(spec.DesiredState),
		RequestID:          spec.Request.ID,
		MetadataGeneration: ds.obj.Generation,
		ObservedAt:         ds.intentObservedAt,
		Claim: fleet.InventoryClaim{
			Vendor:     spec.InventoryClaim.Vendor,
			UUID:       derefString(spec.InventoryClaim.UUID),
			Serial:     derefString(spec.InventoryClaim.Serial),
			Source:     spec.InventoryClaim.Source,
			EvidenceID: spec.InventoryClaim.EvidenceID,
		},
	}
}

// nameList renders sorted names as a comma list: at most 8 names, the omitted
// count as ",+N", and fewer names when the whole text must fit budget
// characters (GKA-074).
func nameList(names []string, budget int) string {
	sorted := slices.Clone(names)
	sort.Strings(sorted)
	k := min(len(sorted), 8)
	for ; k >= 0; k-- {
		var b strings.Builder
		b.WriteString(strings.Join(sorted[:k], ","))
		if rest := len(sorted) - k; rest > 0 {
			if k > 0 {
				b.WriteByte(',')
			}
			b.WriteString("+" + strconv.Itoa(rest))
		}
		if b.Len() <= budget || k == 0 {
			return b.String()
		}
	}
	return ""
}
