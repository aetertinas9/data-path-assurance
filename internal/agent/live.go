package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Live-only diagnostic codes of the NVIDIA query runner. They are logged and
// never reach a wire frame (GLI-081 (f)); the other NVIDIA codes are the ones
// of GFO-051.
const (
	codeNVIDIAStartFailed = "nvidia_start_failed"
	codeNVIDIATimeout     = "nvidia_timeout"
	codeNVIDIAExitFailed  = "nvidia_exit_failed"
	nvidiaDiagSubject     = "nvidia-smi"
)

// LiveDimension is one key/value dimension of a live observation.
type LiveDimension struct{ Key, Value string }

// LiveObservation is one integer observation of a live collection. It names
// the signal and its value; the frame it ends up in gives it an ID and its
// instants.
type LiveObservation struct {
	Source  model.SourceRef
	Subject model.AssetRef
	Signal  string
	Value   int64
	Unit    string
	// Dimensions are in key order.
	Dimensions []LiveDimension
}

// LiveEdge is one observed containment edge: From is located in To.
type LiveEdge struct{ From, To model.AssetRef }

// LiveDiagnostic is one collector diagnostic. Subject is already escaped to
// the bytes 0x21 through 0x7E and holds no file content, serial or child
// output.
type LiveDiagnostic struct{ Code, Subject string }

// LiveFrame is what one live collection of a host observed, before it is bound
// to a session, a sequence and instants. Every collection is in the wire order
// of GFO-085: assets by key, edges by (from key, to key), observations by
// (subject key, signal, source type, source name, dimensions) and bindings by
// (address, UUID). The edge evidence of edge i has source EdgeSource and every
// binding has source BindingSource.
type LiveFrame struct {
	// Complete is false when the frame is PARTIAL (GFO-050).
	Complete      bool
	Assets        []model.AssetRef
	Edges         []LiveEdge
	Observations  []LiveObservation
	Bindings      []GPUInventoryEntry
	EdgeSource    model.SourceRef
	BindingSource model.SourceRef
	// Diagnostics are the deduplicated diagnostics in (code, subject) order,
	// at most 256, and DiagnosticsTotal counts them before that cut.
	Diagnostics      []LiveDiagnostic
	DiagnosticsTotal int
}

// CollectLive observes the host once, with the collector rules of the offline
// mode (GFO §4 to §6) and without an operator baseline: expected width is the
// adjacent capability minimum only (GFO-062).
//
// sysfs is the sysfs observation root, opened by the caller for this
// collection; nil means it could not be opened, which yields a PARTIAL frame
// like an unreadable device list does. nodeUID is the UID of the Kubernetes
// Node whose asset anchors the topology. When nvidiaSMI is not empty, it is the
// absolute path of nvidia-smi and the inventory is queried now with
// RunNVIDIAQuery; any failure of that query leaves the inventory unknown (no
// binding) and does not change completeness (GFO-073). With nvidiaSMI empty no
// process is started.
//
// The only errors are a device count beyond its bound (ErrBoundExceeded), a nil
// context (ErrInvalidArgument) and the end of ctx during the inventory query.
func CollectLive(ctx context.Context, sysfs *os.Root, nodeUID, nvidiaSMI string) (LiveFrame, error) {
	if ctx == nil {
		return LiveFrame{}, fmt.Errorf("%w: nil context", ErrInvalidArgument)
	}
	diags := frameDiagnostics{}
	var sf *sysfsFrame
	if sysfs == nil {
		sf = failedSysfsFrame(diags)
	} else {
		var err error
		if sf, err = collectSysfs(sysfs, diags); err != nil {
			return LiveFrame{}, err
		}
	}

	node := nodeAsset(nodeUID)
	topo := buildTopology(sf, node, diags)
	// Without a sysfs root no function is selected, so no attribute is read.
	pairs := evaluateWidths(sysfs, sf, topo, nil, diags)

	var bindings []GPUInventoryEntry
	if nvidiaSMI != "" {
		rows, err := RunNVIDIAQuery(ctx, nvidiaSMI, DefaultNVIDIALimits())
		switch {
		case err == nil:
			bindings = bindGPURows(rows, sf, diags)
		case ctx.Err() != nil:
			return LiveFrame{}, fmt.Errorf("agent: live collection: %w", ctx.Err())
		default:
			diags.add(nvidiaFailureCode(err), nvidiaDiagSubject)
		}
	}

	frame := LiveFrame{
		Complete:      !sf.partial,
		EdgeSource:    parentSource,
		BindingSource: nvidiaSource,
	}
	for _, a := range topo.assets {
		frame.Assets = append(frame.Assets, a)
	}
	slices.SortFunc(frame.Assets, func(x, y model.AssetRef) int { return strings.Compare(x.Key(), y.Key()) })

	hops := make([]hop, 0, len(topo.hops))
	for _, h := range topo.hops {
		hops = append(hops, h)
	}
	slices.SortFunc(hops, compareHops)
	for _, h := range hops {
		frame.Edges = append(frame.Edges, LiveEdge{From: h.from, To: h.to})
	}

	for _, f := range sf.selected {
		frame.Observations = append(frame.Observations, LiveObservation{
			Source:  parentSource,
			Subject: pciAsset(model.KindPCIeFunction, f),
			Signal:  string(signalClassCode),
			Value:   int64(sf.entries[f].class),
			Unit:    unitPCIClass,
		})
	}
	for _, w := range pairs {
		dims := []LiveDimension{
			{Key: pcie.DimensionExpectedProvenance, Value: w.provenance},
			{Key: pcie.DimensionPeerCanonical, Value: w.peer.Canonical},
			{Key: pcie.DimensionPeerKind, Value: w.peer.Kind.String()},
			{Key: pcie.DimensionRootCanonical, Value: w.rootPort.Canonical},
		}
		slices.SortFunc(dims, func(x, y LiveDimension) int { return strings.Compare(x.Key, y.Key) })
		frame.Observations = append(frame.Observations,
			LiveObservation{Source: widthSource, Subject: w.function, Signal: string(pcie.SignalLinkWidthCurrent),
				Value: w.current, Unit: pcie.UnitLinkWidth, Dimensions: dims},
			LiveObservation{Source: widthSource, Subject: w.function, Signal: string(pcie.SignalLinkWidthExpected),
				Value: w.expected, Unit: pcie.UnitLinkWidth, Dimensions: dims})
	}
	slices.SortFunc(frame.Observations, compareLiveObservations)

	frame.Bindings = slices.Clone(bindings)
	slices.SortFunc(frame.Bindings, func(x, y GPUInventoryEntry) int {
		if c := strings.Compare(x.BDF, y.BDF); c != 0 {
			return c
		}
		return strings.Compare(x.UUID, y.UUID)
	})

	list, total := diags.sorted()
	for _, d := range list {
		frame.Diagnostics = append(frame.Diagnostics, LiveDiagnostic{Code: d.code, Subject: d.subject})
	}
	frame.DiagnosticsTotal = total
	return frame, nil
}

// nvidiaFailureCode maps a failed inventory query to its diagnostic code.
func nvidiaFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrNVIDIATimeout):
		return codeNVIDIATimeout
	case errors.Is(err, ErrNVIDIAOutputLimit):
		return codeNVIDIAOutputLimit
	case errors.Is(err, ErrNVIDIAExit):
		return codeNVIDIAExitFailed
	case errors.Is(err, ErrNVIDIAEmpty):
		return codeNVIDIAEmpty
	case errors.Is(err, ErrNVIDIAMalformed):
		return codeNVIDIAMalformed
	case errors.Is(err, ErrNVIDIADuplicate):
		return codeNVIDIADuplicate
	default:
		return codeNVIDIAStartFailed
	}
}

// compareLiveObservations is the observation order of GFO-085 without the
// instants and the ID, which cannot break a tie within one frame: an
// observation is named by its subject and signal.
func compareLiveObservations(x, y LiveObservation) int {
	if c := strings.Compare(x.Subject.Key(), y.Subject.Key()); c != 0 {
		return c
	}
	if c := strings.Compare(x.Signal, y.Signal); c != 0 {
		return c
	}
	if c := strings.Compare(x.Source.Type, y.Source.Type); c != 0 {
		return c
	}
	if c := strings.Compare(x.Source.Name, y.Source.Name); c != 0 {
		return c
	}
	for i := 0; i < len(x.Dimensions) && i < len(y.Dimensions); i++ {
		if c := strings.Compare(x.Dimensions[i].Key, y.Dimensions[i].Key); c != 0 {
			return c
		}
		if c := strings.Compare(x.Dimensions[i].Value, y.Dimensions[i].Value); c != 0 {
			return c
		}
	}
	return len(x.Dimensions) - len(y.Dimensions)
}
