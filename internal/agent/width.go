package agent

import (
	"os"

	"github.com/aetertinas9/data-path-assurance/internal/domains/pcie"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// widthPair is one current/expected link width observation pair (GFO-060).
type widthPair struct {
	function          model.AssetRef
	current, expected int64
	rootPort, peer    model.AssetRef
	provenance        string
}

// evaluateWidths decides, for every selected function, whether a width pair
// can be emitted, reading only the width attributes GFO-060 allows and only
// after the frame's hops are complete. No outcome here changes completeness.
func evaluateWidths(root *os.Root, sf *sysfsFrame, t *topology, baselines []operatorBaseline, diags frameDiagnostics) []widthPair {
	byFunction := make(map[string]operatorBaseline, len(baselines))
	for _, b := range baselines {
		byFunction[b.function] = b
	}
	selected := make(map[string]bool, len(sf.selected))
	for _, f := range sf.selected {
		selected[f] = true
	}
	for _, b := range baselines {
		if !selected[b.function] {
			diags.add(codeBaselineUnmatched, b.function)
		}
	}

	var pairs []widthPair
	for _, f := range sf.selected {
		fAsset := pciAsset(model.KindPCIeFunction, f)
		parent, rootPort, ok := t.chainToRoot(fAsset)
		if !ok {
			diags.add(codeWidthOmitted, f)
			continue
		}
		parentBDF := parent.Canonical[len(model.NamespacePCIBDF)+1:]
		fEntry, pEntry := sf.entries[f], sf.entries[parentBDF]
		if fEntry == nil || pEntry == nil {
			// Unreachable: every asset on a chain comes from a resolved
			// closure, whose members are all processed entries.
			diags.add(codeWidthOmitted, f)
			continue
		}

		current, currentOK := readWidthAttr(root, fEntry.rel+"/current_link_width", diags)

		var expected int64
		var provenance string
		if b, has := byFunction[f]; has {
			if b.peer == parentBDF {
				expected, provenance = b.width, pcie.ProvenanceOperatorVerifiedWiring
			} else {
				diags.add(codeBaselinePeerMismatch, f)
			}
		} else {
			fMax, fOK := readWidthAttr(root, fEntry.rel+"/max_link_width", diags)
			pMax, pOK := readWidthAttr(root, pEntry.rel+"/max_link_width", diags)
			if fOK && pOK {
				expected, provenance = min(fMax, pMax), pcie.ProvenanceAdjacentCapabilityMin
			}
		}

		if !currentOK || provenance == "" {
			diags.add(codeWidthOmitted, f)
			continue
		}
		pairs = append(pairs, widthPair{
			function:   fAsset,
			current:    current,
			expected:   expected,
			rootPort:   rootPort,
			peer:       parent,
			provenance: provenance,
		})
	}
	return pairs
}

// readWidthAttr reads one width attribute, recording a diagnostic for any
// outcome but ok. A failure or nodata here is a diagnostic only.
func readWidthAttr(root *os.Root, name string, diags frameDiagnostics) (int64, bool) {
	v, status := readWidth(root, name)
	if status != readOK {
		diags.add(status.attrCode(), name)
		return 0, false
	}
	return v, true
}
