package kubeapi_test

// Lane T-2 assertion helpers (prefix gkaS). Expected values are written out from
// the spec text (message composition GKA-074, condition tables GKA-075/077/078,
// non-decision rendering GKA-072/076), not derived from any implementation.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
)

var gkaSHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func gkaSSec(ts metav1.Time) string { return ts.Time.UTC().Format(time.RFC3339) }

func gkaSASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// gkaSMsg composes prefix + "; " + token for every non-empty token (GKA-074).
func gkaSMsg(prefix string, tokens ...string) string {
	out := prefix
	for _, tk := range tokens {
		if tk != "" {
			out += "; " + tk
		}
	}
	return out
}

// gkaSNameList is the GKA-074 name-list notation for at most 8 names: byte
// ascending, comma separated, and ",+N" for the remaining N (N >= 1).
func gkaSNameList(names []string) string {
	s := append([]string(nil), names...)
	sort.Strings(s)
	if len(s) <= 8 {
		return strings.Join(s, ",")
	}
	return strings.Join(s[:8], ",") + fmt.Sprintf(",+%d", len(s)-8)
}

// gkaSC is one expected condition.
type gkaSC struct{ Type, Status, Reason, Message string }

func gkaSCondTypes(conds []metav1.Condition) string {
	var ts []string
	for _, c := range conds {
		ts = append(ts, c.Type)
	}
	return strings.Join(ts, ",")
}

func gkaSCondOf(conds []metav1.Condition, typ string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == typ {
			return &conds[i]
		}
	}
	return nil
}

// gkaSCheckConds checks the complete condition list: type-ascending order
// (GKA-074(a)), exact set, status/reason/message, observedGeneration == gen
// (GKA-074(b)) and a non-zero lastTransitionTime. Message bounds are GKA-046.
func gkaSCheckConds(t *testing.T, where string, got []metav1.Condition, gen int64, want []gkaSC) {
	t.Helper()
	for i := 1; i < len(got); i++ {
		if got[i-1].Type >= got[i].Type {
			t.Errorf("%s: conditions are not in ascending type order (GKA-074(a)): %s", where, gkaSCondTypes(got))
			break
		}
	}
	if len(got) != len(want) {
		var w []string
		for _, c := range want {
			w = append(w, c.Type)
		}
		t.Errorf("%s: condition types = [%s], want [%s]", where, gkaSCondTypes(got), strings.Join(w, ","))
		return
	}
	for i, wc := range want {
		g := got[i]
		if g.Type != wc.Type {
			t.Errorf("%s: condition[%d] type = %q, want %q", where, i, g.Type, wc.Type)
			continue
		}
		if string(g.Status) != wc.Status || g.Reason != wc.Reason || g.Message != wc.Message {
			t.Errorf("%s: %s = {%s %s %q}, want {%s %s %q}", where, wc.Type, g.Status, g.Reason, g.Message, wc.Status, wc.Reason, wc.Message)
		}
		if g.ObservedGeneration != gen {
			t.Errorf("%s: %s observedGeneration = %d, want %d (GKA-074(b))", where, wc.Type, g.ObservedGeneration, gen)
		}
		if g.LastTransitionTime.IsZero() {
			t.Errorf("%s: %s lastTransitionTime is zero", where, wc.Type)
		}
		if !gkaSASCII(g.Message) || len(g.Message) < 1 || len(g.Message) > 1024 {
			t.Errorf("%s: %s message %q is not 1-1024 ASCII characters (GKA-074(e))", where, wc.Type, g.Message)
		}
	}
}

// gkaSLTT asserts the lastTransitionTime (UTC second) of one condition.
func gkaSLTT(t *testing.T, where string, conds []metav1.Condition, typ, want string) {
	t.Helper()
	c := gkaSCondOf(conds, typ)
	if c == nil {
		t.Errorf("%s: condition %s missing", where, typ)
		return
	}
	if got := gkaSSec(c.LastTransitionTime); got != want {
		t.Errorf("%s: %s lastTransitionTime = %s, want %s (GKA-074(c))", where, typ, got, want)
	}
}

// ---- GPUDevice: scope (non-decision) rendering, GKA-072/075 -----------------

type gkaSNonDec struct {
	SR      string   // scope reason
	Phase   string   // lifecyclePhase (baseline, or Unknown for InternalError)
	Binding string   // actualBinding.state: Unknown (default) or Conflict
	Peer    string   // "peer=..." token (IdentityConflict only)
	Tail    []string // tokens after peer, in GKA-074 order (internal error / no_observation)
	Intent  string   // expected intentObservedAt (UTC second) or "" for non-zero only
}

func gkaSCheckNonDecision(t *testing.T, where string, d *v1alpha1.GPUDevice, x gkaSNonDec) {
	t.Helper()
	s := d.Status
	if x.Binding == "" {
		x.Binding = "Unknown"
	}
	allocReason := "AllocationUnknown"
	if x.SR == "InternalError" {
		allocReason = "InternalError"
	}
	if s.ObservedGeneration != d.Generation {
		t.Errorf("%s: observedGeneration = %d, want metadata.generation %d", where, s.ObservedGeneration, d.Generation)
	}
	if s.ObservedRequestID != d.Spec.Request.ID {
		t.Errorf("%s: observedRequestID = %q, want %q", where, s.ObservedRequestID, d.Spec.Request.ID)
	}
	if s.IntentObservedAt.IsZero() {
		t.Errorf("%s: intentObservedAt is zero (GKA-083: always written)", where)
	} else if x.Intent != "" && gkaSSec(s.IntentObservedAt) != x.Intent {
		t.Errorf("%s: intentObservedAt = %s, want %s", where, gkaSSec(s.IntentObservedAt), x.Intent)
	}
	ab := s.ActualBinding
	if string(ab.State) != x.Binding || ab.Reason != x.SR {
		t.Errorf("%s: actualBinding = {%s %s}, want {%s %s}", where, ab.State, ab.Reason, x.Binding, x.SR)
	}
	if ab.Vendor != nil || ab.UUID != nil || ab.Serial != nil || ab.NodeUID != nil || ab.BootID != nil || ab.BDF != nil ||
		ab.FunctionKey != nil || ab.Source != nil || ab.EvidenceID != nil || ab.ObservedAt != nil || ab.ExpiresAt != nil {
		t.Errorf("%s: actualBinding carries optional fields without a decision: %#v", where, ab)
	}
	if s.GraphRevision != nil {
		t.Errorf("%s: graphRevision = %q, want omitted (GKA-083)", where, *s.GraphRevision)
	}
	if string(s.Qualification) != "Unknown" {
		t.Errorf("%s: qualification = %s, want Unknown", where, s.Qualification)
	}
	if string(s.LifecyclePhase) != x.Phase {
		t.Errorf("%s: lifecyclePhase = %s, want %s", where, s.LifecyclePhase, x.Phase)
	}
	al := s.Allocation
	if string(al.State) != "Unknown" || al.Reason != allocReason || al.Profile != nil || al.ObservedAt != nil || al.ExpiresAt != nil {
		t.Errorf("%s: allocation = %#v, want {Unknown %s} only", where, al, allocReason)
	}
	if al.EvidenceRefs == nil || len(al.EvidenceRefs) != 0 || al.AffectedWorkloads == nil || len(al.AffectedWorkloads) != 0 {
		t.Errorf("%s: allocation lists = %#v / %#v, want present and empty", where, al.EvidenceRefs, al.AffectedWorkloads)
	}
	if s.Coverage == nil || len(s.Coverage) != 0 || s.FindingRefs == nil || len(s.FindingRefs) != 0 || s.EvidenceRefs == nil || len(s.EvidenceRefs) != 0 {
		t.Errorf("%s: coverage/findingRefs/evidenceRefs = %#v/%#v/%#v, want present and empty (GKA-072)", where, s.Coverage, s.FindingRefs, s.EvidenceRefs)
	}
	ibStatus := "Unknown"
	if x.SR == "IdentityConflict" {
		ibStatus = "False"
	}
	ibTokens := append([]string{x.Peer}, x.Tail...)
	gkaSCheckConds(t, where, s.Conditions, d.Generation, []gkaSC{
		{"AllocationKnown", "Unknown", allocReason, gkaSMsg("allocation=Unknown", x.Tail...)},
		{"DeviceQualified", "Unknown", x.SR, gkaSMsg("qualification=Unknown phase="+x.Phase, x.Tail...)},
		{"IdentityBound", ibStatus, x.SR, gkaSMsg("binding="+x.Binding, ibTokens...)},
		{"LifecycleReady", "Unknown", x.SR, gkaSMsg("phase="+x.Phase, x.Tail...)},
	})
}

// ---- GPUFleet ----------------------------------------------------------------

type gkaSFleetWant struct {
	Selected, Ready, Degraded, Unknown int32
	Status, Reason, Message            string
	Revision                           string // "" = must be omitted, "*" = any lowercase hex-64, else exact
}

func gkaSCheckFleet(t *testing.T, where string, f *v1alpha1.GPUFleet, w gkaSFleetWant) {
	t.Helper()
	s := f.Status
	if s.ObservedGeneration != f.Generation {
		t.Errorf("%s: observedGeneration = %d, want %d", where, s.ObservedGeneration, f.Generation)
	}
	if s.SelectedCount != w.Selected || s.ReadyCount != w.Ready || s.DegradedCount != w.Degraded || s.UnknownCount != w.Unknown {
		t.Errorf("%s: counts selected/ready/degraded/unknown = %d/%d/%d/%d, want %d/%d/%d/%d",
			where, s.SelectedCount, s.ReadyCount, s.DegradedCount, s.UnknownCount, w.Selected, w.Ready, w.Degraded, w.Unknown)
	}
	if s.SelectedCount != s.ReadyCount+s.DegradedCount+s.UnknownCount {
		t.Errorf("%s: counts do not sum to selectedCount (GKA-078): %d != %d+%d+%d", where, s.SelectedCount, s.ReadyCount, s.DegradedCount, s.UnknownCount)
	}
	switch w.Revision {
	case "":
		if s.AssessmentRevision != nil {
			t.Errorf("%s: assessmentRevision = %q, want omitted (GKA-079)", where, *s.AssessmentRevision)
		}
	case "*":
		if s.AssessmentRevision == nil || !gkaSHex64.MatchString(*s.AssessmentRevision) {
			t.Errorf("%s: assessmentRevision = %v, want lowercase hex-64", where, s.AssessmentRevision)
		}
	default:
		if s.AssessmentRevision == nil || *s.AssessmentRevision != w.Revision {
			t.Errorf("%s: assessmentRevision = %v, want %s", where, s.AssessmentRevision, w.Revision)
		}
	}
	if s.OwnedNodeRefs == nil {
		t.Errorf("%s: ownedNodeRefs is absent, want [] or a preserved value (GKA-105)", where)
	}
	gkaSCheckConds(t, where, s.Conditions, f.Generation, []gkaSC{{"FleetReady", w.Status, w.Reason, w.Message}})
}

// ---- NodePathState -----------------------------------------------------------

type gkaSNPSWant struct {
	Elig          string   // NodeEligible status: True / False / Unknown
	Reason        string   // NE reason (NodeEligible reason)
	Qual          string   // qualification in the NodeEligible prefix (default Unknown)
	EligLabel     string   // eligibility in the NodeEligible prefix (default: same as the status value)
	NodeTail      []string // NodeEligible tokens between the prefix and gate_not_implemented
	Enforce       bool     // gate_not_implemented on NodeEligible
	FreshStatus   string   // EvidenceFresh status (default Unknown)
	FreshReason   string   // EvidenceFresh reason (default = Reason)
	FreshTail     []string // EvidenceFresh tokens
	Completeness  string   // status.evidenceCompleteness (default Unknown)
	GraphRevision string   // "" = omitted, "*" = any non-empty, else exact
}

func gkaSCheckNPS(t *testing.T, where string, p *v1alpha1.NodePathState, w gkaSNPSWant) {
	t.Helper()
	if w.Qual == "" {
		w.Qual = "Unknown"
	}
	if w.EligLabel == "" {
		w.EligLabel = w.Elig
		switch w.Elig {
		case "True":
			w.EligLabel = "Eligible"
		case "False":
			w.EligLabel = "Ineligible"
		}
	}
	if w.FreshStatus == "" {
		w.FreshStatus = "Unknown"
	}
	if w.FreshReason == "" {
		w.FreshReason = w.Reason
	}
	if w.Completeness == "" {
		w.Completeness = "Unknown"
	}
	s := p.Status
	if s.ObservedGeneration != p.Generation {
		t.Errorf("%s: observedGeneration = %d, want %d", where, s.ObservedGeneration, p.Generation)
	}
	if s.NodeRef != p.Spec.NodeRef {
		t.Errorf("%s: status.nodeRef = %#v, want spec.nodeRef %#v", where, s.NodeRef, p.Spec.NodeRef)
	}
	if string(s.EvidenceCompleteness) != w.Completeness {
		t.Errorf("%s: evidenceCompleteness = %s, want %s", where, s.EvidenceCompleteness, w.Completeness)
	}
	switch w.GraphRevision {
	case "":
		if s.GraphRevision != nil {
			t.Errorf("%s: graphRevision = %q, want omitted (GKA-083)", where, *s.GraphRevision)
		}
	case "*":
		if s.GraphRevision == nil || *s.GraphRevision == "" {
			t.Errorf("%s: graphRevision omitted, want a value", where)
		}
	default:
		if s.GraphRevision == nil || *s.GraphRevision != w.GraphRevision {
			t.Errorf("%s: graphRevision = %v, want %s", where, s.GraphRevision, w.GraphRevision)
		}
	}
	if s.DeviceSummaries == nil {
		t.Errorf("%s: deviceSummaries is absent, want a list (possibly empty)", where)
	}
	nodeTokens := append([]string(nil), w.NodeTail...)
	if w.Enforce {
		nodeTokens = append(nodeTokens, "gate_not_implemented")
	}
	gkaSCheckConds(t, where, s.Conditions, p.Generation, []gkaSC{
		{"EvidenceFresh", w.FreshStatus, w.FreshReason, gkaSMsg("completeness="+w.Completeness, w.FreshTail...)},
		{"NodeEligible", w.Elig, w.Reason, gkaSMsg("eligibility="+w.EligLabel+" qualification="+w.Qual, nodeTokens...)},
	})
}

func gkaSSummaries(p *v1alpha1.NodePathState) map[string]v1alpha1.DeviceSummary {
	out := map[string]v1alpha1.DeviceSummary{}
	for _, ds := range p.Status.DeviceSummaries {
		out[ds.Name] = ds
	}
	return out
}

// ---- Node condition ------------------------------------------------------------

const gkaSNodeCondType = "DataPathGPUFleetReady"

func gkaSNodeCond(n *corev1.Node) *corev1.NodeCondition {
	for i := range n.Status.Conditions {
		if string(n.Status.Conditions[i].Type) == gkaSNodeCondType {
			return &n.Status.Conditions[i]
		}
	}
	return nil
}

// gkaSCheckNodeCond checks the condition (nil want = absent) including the GKA-111
// facts: no lastHeartbeatTime, non-zero lastTransitionTime, ASCII 1-1024 message.
func gkaSCheckNodeCond(t *testing.T, where string, n *corev1.Node, want *gkaSC) {
	t.Helper()
	got := gkaSNodeCond(n)
	if want == nil {
		if got != nil {
			t.Errorf("%s: node %s carries condition %s = {%s %s %q}, want none", where, n.Name, gkaSNodeCondType, got.Status, got.Reason, got.Message)
		}
		return
	}
	if got == nil {
		t.Errorf("%s: node %s has no %s condition, want {%s %s %q}", where, n.Name, gkaSNodeCondType, want.Status, want.Reason, want.Message)
		return
	}
	if string(got.Status) != want.Status || got.Reason != want.Reason || got.Message != want.Message {
		t.Errorf("%s: node %s condition = {%s %s %q}, want {%s %s %q}", where, n.Name, got.Status, got.Reason, got.Message, want.Status, want.Reason, want.Message)
	}
	if !got.LastHeartbeatTime.IsZero() {
		t.Errorf("%s: node %s condition carries lastHeartbeatTime %s, want none (GKA-111)", where, n.Name, gkaSSec(got.LastHeartbeatTime))
	}
	if got.LastTransitionTime.IsZero() {
		t.Errorf("%s: node %s condition lastTransitionTime is zero", where, n.Name)
	}
	if !gkaSASCII(got.Message) || len(got.Message) < 1 || len(got.Message) > 1024 {
		t.Errorf("%s: node %s condition message %q is not 1-1024 ASCII characters", where, n.Name, got.Message)
	}
}

// gkaSNodeCondIs is the polling form of gkaSCheckNodeCond ("" = satisfied).
func gkaSNodeCondIs(n *corev1.Node, want *gkaSC) string {
	got := gkaSNodeCond(n)
	if want == nil {
		if got != nil {
			return fmt.Sprintf("condition still present {%s %s %q}", got.Status, got.Reason, got.Message)
		}
		return ""
	}
	if got == nil {
		return "condition absent"
	}
	if string(got.Status) != want.Status || got.Reason != want.Reason || got.Message != want.Message {
		return fmt.Sprintf("condition {%s %s %q}, want {%s %s %q}", got.Status, got.Reason, got.Message, want.Status, want.Reason, want.Message)
	}
	return ""
}

// gkaSNodeFacts is the part of a Node the controller must never change (GKA-061,
// GKA-121, GKA-086): labels, annotations, taints, unschedulable, finalizers.
func gkaSNodeFacts(n *corev1.Node) string {
	var taints []string
	for _, tn := range n.Spec.Taints {
		taints = append(taints, fmt.Sprintf("%s=%s:%s", tn.Key, tn.Value, tn.Effect))
	}
	sort.Strings(taints)
	return fmt.Sprintf("labels=%v annotations=%v taints=%v unschedulable=%v finalizers=%v",
		n.Labels, n.Annotations, taints, n.Spec.Unschedulable, n.Finalizers)
}

// gkaSFleetReadyOf returns the FleetReady condition of a fleet, if any.
func gkaSFleetReadyOf(f *v1alpha1.GPUFleet) *metav1.Condition {
	return gkaSCondOf(f.Status.Conditions, "FleetReady")
}

// gkaSFleetIs is the polling form: status/reason/message of FleetReady ("" = satisfied).
func gkaSFleetIs(f *v1alpha1.GPUFleet, status, reason string) string {
	c := gkaSFleetReadyOf(f)
	if c == nil {
		return "no FleetReady condition yet"
	}
	if string(c.Status) != status || c.Reason != reason {
		return fmt.Sprintf("FleetReady = {%s %s %q}, want {%s %s}", c.Status, c.Reason, c.Message, status, reason)
	}
	if f.Status.ObservedGeneration != f.Generation {
		return fmt.Sprintf("observedGeneration %d != generation %d", f.Status.ObservedGeneration, f.Generation)
	}
	return ""
}

// gkaSDeviceHas is the polling form for "the device status reached scope SR".
func gkaSDeviceHas(d *v1alpha1.GPUDevice, reason string) string {
	if d.Status.ObservedGeneration != d.Generation || len(d.Status.Conditions) == 0 {
		return "status not written yet"
	}
	c := gkaSCondOf(d.Status.Conditions, "DeviceQualified")
	if c == nil {
		return "no DeviceQualified condition yet"
	}
	if c.Reason != reason {
		return fmt.Sprintf("DeviceQualified reason = %s, want %s", c.Reason, reason)
	}
	return ""
}

// gkaSNPSHas polls for "NodeEligible reason == reason".
func gkaSNPSHas(p *v1alpha1.NodePathState, reason string) string {
	c := gkaSCondOf(p.Status.Conditions, "NodeEligible")
	if c == nil {
		return "no NodeEligible condition yet"
	}
	if c.Reason != reason {
		return fmt.Sprintf("NodeEligible reason = %s (%q), want %s", c.Reason, c.Message, reason)
	}
	return ""
}

// TestGKA074_SemMessageHelpersMatchSpecExamples pins the expectation helpers to the
// literal examples of GKA-074 (no envtest): a wrong helper would otherwise make every
// message assertion in this lane wrong in the same way.
func TestGKA074_SemMessageHelpersMatchSpecExamples(t *testing.T) {
	if got, want := gkaSMsg("selected=0 ready=0 degraded=0 unknown=0", "internal error: invalid nodeSelector"),
		"selected=0 ready=0 degraded=0 unknown=0; internal error: invalid nodeSelector"; got != want {
		t.Errorf("GKA-074 example 1: %q, want %q", got, want)
	}
	if got, want := gkaSMsg("qualification=Unknown phase=Pending", "no_observation"), "qualification=Unknown phase=Pending; no_observation"; got != want {
		t.Errorf("GKA-074 example 2: %q, want %q", got, want)
	}
	// Token order: qualified_not_eligible, peer, internal error, no_observation, truncated, gate_not_implemented.
	if got, want := gkaSMsg("p", "", "peer=a", "", "no_observation", "truncated:x", "gate_not_implemented"), "p; peer=a; no_observation; truncated:x; gate_not_implemented"; got != want {
		t.Errorf("GKA-074 token order: %q, want %q", got, want)
	}
	names := []string{"k", "j", "i", "h", "g", "f", "e", "d", "c", "b", "a"}
	if got, want := "peer="+gkaSNameList(names), "peer=a,b,c,d,e,f,g,h,+3"; got != want {
		t.Errorf("GKA-074 name list example: %q, want %q", got, want)
	}
	if got := gkaSNameList([]string{"b", "a"}); got != "a,b" {
		t.Errorf("name list of two = %q, want a,b", got)
	}
	if got := gkaSNameList(names[3:]); got != "a,b,c,d,e,f,g,h" {
		t.Errorf("name list of exactly eight = %q, want no remainder token", got)
	}
	if got := gkaSNameList(names[2:]); got != "a,b,c,d,e,f,g,h,+1" {
		t.Errorf("name list of nine = %q, want ,+1", got)
	}
	// The shortened form of the spec example "peer=a,b,+3" is accepted by the cut checker.
	gkaSCheckPeerList(t, "cut checker", &metav1.Condition{Message: "binding=Conflict; peer=a,b,+3"}, "binding=Conflict; peer=", []string{"a", "b", "c", "d", "e"})
	// The revision helper is the digest of the GKA-079 text for one node.
	sum := sha256.Sum256([]byte("dpa.FleetAssessment.v1\x00uid-1\x00rev-1\x00"))
	if got, want := gkaSFleetRevisionOf(map[string]string{"uid-1": "rev-1"}), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("GKA-079 helper: %s, want %s", got, want)
	}
	two := sha256.Sum256([]byte("dpa.FleetAssessment.v1\x00uid-1\x00rev-1\x00uid-2\x00rev-2\x00"))
	if got, want := gkaSFleetRevisionOf(map[string]string{"uid-2": "rev-2", "uid-1": "rev-1"}), hex.EncodeToString(two[:]); got != want {
		t.Errorf("GKA-079 helper (UID order): %s, want %s", got, want)
	}
}
