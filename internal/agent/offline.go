package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Fixture layout (GFO-030).
const (
	manifestName   = "manifest.json"
	framesDir      = "frames"
	frameSysName   = "sys"
	cannedNVIDIA   = "nvidia-smi.csv"
	maxStdoutBytes = 64 << 20
)

// frameSysDir is the sysfs root of frame i: the "sys" directory of frames/<i>.
func frameSysDir(i int) string {
	return path.Join(framesDir, strconv.Itoa(i), frameSysName)
}

// frameCSVPath is frames/<i>/nvidia-smi.csv, the canned inventory of frame i.
func frameCSVPath(i int) string {
	return path.Join(framesDir, strconv.Itoa(i), cannedNVIDIA)
}

// frameResult is one finished frame.
type frameResult struct {
	complete    bool
	context     frameContext
	payload     payload
	digest      string
	revision    string
	diagnostics []diagnostic
	diagTotal   int
}

// OpenFixtureRoot opens dir as the fixture root. dir is stat'ed first, following
// symlinks, and anything but a directory is refused without being opened, so a
// FIFO or device given as the root cannot block. Every error wraps
// ErrFixtureInvalid.
func OpenFixtureRoot(dir string) (*os.Root, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fixtureInvalidf("fixture root cannot be stat'ed")
	}
	if !info.IsDir() {
		return nil, fixtureInvalidf("fixture root is not a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fixtureInvalidf("fixture root cannot be opened")
	}
	return root, nil
}

// CollectOffline builds the offline snapshot artifact of the fixture tree at
// fixture: the compact JSON document followed by one newline, ready to be
// written in a single call.
//
// It checks the fixture root, the manifest and every frame directory before
// collecting anything; a violation returns an error wrapping
// ErrFixtureInvalid. A bound that cannot be met without truncation, in any
// frame, returns an error wrapping ErrBoundExceeded. Any other error is
// internal. On error no artifact bytes are returned.
//
// No process is started, and every file read stays inside fixture.
func CollectOffline(ctx context.Context, fixture *os.Root) ([]byte, error) {
	if ctx == nil || fixture == nil {
		return nil, fmt.Errorf("%w: nil context or fixture root", ErrInvalidArgument)
	}
	info, err := fixture.Stat(".")
	if err != nil || !info.IsDir() {
		return nil, fixtureInvalidf("fixture root is not a readable directory")
	}
	data, status := readRegularFile(fixture, manifestName, manifestMaxBytes)
	if status != readOK {
		return nil, fixtureInvalidf("%s is absent, not a regular file inside the root, or unreadable", manifestName)
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	for i := range m.frames {
		info, err := fixture.Stat(frameSysDir(i))
		if err != nil || !info.IsDir() {
			return nil, fixtureInvalidf("frame %d directory is absent, not a directory, or outside the root", i)
		}
	}

	frames := make([]frameResult, len(m.frames))
	var internal error
	for i := range m.frames {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("agent: offline collection: %w", err)
		}
		r, err := collectFrame(fixture, &m, i)
		if errors.Is(err, ErrBoundExceeded) {
			return nil, err
		}
		if err != nil {
			// A later frame may still exceed a bound, which outranks this.
			if internal == nil {
				internal = err
			}
			continue
		}
		frames[i] = r
	}
	if internal != nil {
		return nil, internal
	}

	out, err := encodeArtifact(&m, frames)
	if err != nil {
		return nil, err
	}
	if len(out) > maxStdoutBytes {
		return nil, fmt.Errorf("%w: artifact of %d bytes", ErrBoundExceeded, len(out))
	}
	return out, nil
}

// collectFrame observes frame i: sysfs through the frame's own root, topology,
// widths and the canned NVIDIA inventory.
func collectFrame(fixture *os.Root, m *manifest, i int) (frameResult, error) {
	fc := frameContext{session: m.session, sequence: uint64(i), times: m.frames[i]}
	diags := frameDiagnostics{}

	var sf *sysfsFrame
	sysRoot, err := fixture.OpenRoot(frameSysDir(i))
	if err != nil {
		sf = failedSysfsFrame(diags)
	} else {
		// The frame root is only read, so a close failure loses nothing.
		defer func() { _ = sysRoot.Close() }()
		if sf, err = collectSysfs(sysRoot, diags); err != nil {
			return frameResult{}, err
		}
	}

	node := nodeAsset(m.nodeUID)
	topo := buildTopology(sf, node, diags)
	// Without a sysfs root there is no selected function, so no width
	// attribute is read and every baseline is unmatched.
	pairs := evaluateWidths(sysRoot, sf, topo, m.baselines, diags)
	bindings := cannedBindings(fixture, i, sf, diags)

	classes := make(map[string]uint32, len(sf.selected))
	for _, f := range sf.selected {
		classes[f] = sf.entries[f].class
	}
	p, err := buildPayload(fc, topo, classes, sf.selected, pairs, bindings)
	if err != nil {
		return frameResult{}, err
	}
	digest, err := p.digest(fc)
	if err != nil {
		return frameResult{}, err
	}
	list, total := diags.sorted()
	return frameResult{
		complete:    !sf.partial,
		context:     fc,
		payload:     p,
		digest:      digest,
		revision:    strconv.FormatInt(fc.session, 10) + ":" + strconv.FormatUint(fc.sequence, 10) + ":" + digest,
		diagnostics: list,
		diagTotal:   total,
	}, nil
}

// cannedBindings reads frames/<i>/nvidia-smi.csv and returns the rows whose
// address is a GPU function of the frame (GFO-073..075). Any read or parse
// failure leaves the inventory Unknown: no binding at all.
func cannedBindings(fixture *os.Root, i int, sf *sysfsFrame, diags frameDiagnostics) []GPUInventoryEntry {
	csvPath := frameCSVPath(i)
	data, status := readRegularFile(fixture, csvPath, NVIDIAMaxStdoutBytes)
	switch status {
	case readOK:
	case readMissing:
		diags.add(codeNVIDIAOutputAbsent, csvPath)
		return nil
	default:
		diags.add(codeNVIDIAOutputUnread, csvPath)
		return nil
	}
	if len(data) > NVIDIAMaxStdoutBytes {
		diags.add(codeNVIDIAOutputLimit, csvPath)
		return nil
	}
	rows, err := ParseNVIDIAQueryOutput(data)
	if err != nil {
		switch {
		case errors.Is(err, ErrNVIDIAEmpty):
			diags.add(codeNVIDIAEmpty, csvPath)
		case errors.Is(err, ErrNVIDIAOutputLimit):
			diags.add(codeNVIDIAOutputLimit, csvPath)
		case errors.Is(err, ErrNVIDIADuplicate):
			diags.add(codeNVIDIADuplicate, csvPath)
		default:
			diags.add(codeNVIDIAMalformed, csvPath)
		}
		return nil
	}

	gpus := map[string]bool{}
	for _, f := range sf.selected {
		if sf.entries[f].gpu {
			gpus[f] = true
		}
	}
	listed := map[string]bool{}
	var bound []GPUInventoryEntry
	for _, row := range rows {
		if !gpus[row.BDF] {
			diags.add(codeNVIDIABDFNotGPU, row.BDF)
			continue
		}
		listed[row.BDF] = true
		bound = append(bound, row)
	}
	for _, f := range sf.selected {
		if gpus[f] && !listed[f] {
			diags.add(codeNVIDIAGPUUnlisted, f)
		}
	}
	return bound
}

// sorted returns the diagnostics by (code, subject) bytes, cut to the first
// 256, with the count before the cut.
func (d frameDiagnostics) sorted() ([]diagnostic, int) {
	list := make([]diagnostic, 0, len(d))
	for k := range d {
		list = append(list, k)
	}
	slices.SortFunc(list, func(x, y diagnostic) int {
		if c := strings.Compare(x.code, y.code); c != 0 {
			return c
		}
		return strings.Compare(x.subject, y.subject)
	})
	total := len(list)
	if total > maxDiagnostics {
		list = list[:maxDiagnostics]
	}
	return list, total
}
