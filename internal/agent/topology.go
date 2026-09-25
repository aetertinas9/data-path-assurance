package agent

import (
	"slices"

	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// hop is one observed containment edge: child LOCATED_IN parent.
type hop struct {
	from, to model.AssetRef
}

// hopKey identifies a hop; relation and origin are the same for every hop.
type hopKey struct {
	from, to string
}

func (h hop) key() hopKey {
	return hopKey{from: h.from.Key(), to: h.to.Key()}
}

// topology is the observed PCIe containment of one frame (GFO-047..049).
type topology struct {
	node   model.AssetRef
	assets map[string]model.AssetRef // by Key
	hops   map[hopKey]hop
	// out lists, per asset key, the distinct keys its hops lead to.
	out map[string][]string
}

// pciAsset returns the asset of a canonical PCI address.
func pciAsset(kind model.AssetKind, bdf string) model.AssetRef {
	return model.AssetRef{Kind: kind, Canonical: model.NamespacePCIBDF + ":" + bdf}
}

// nodeAsset returns the manifest node's asset.
func nodeAsset(uid string) model.AssetRef {
	return model.AssetRef{Kind: model.KindKubernetesNode, Canonical: model.NamespaceKubernetesNodeUID + ":" + uid}
}

// buildTopology derives assets and hops from every selected function's
// closure. An unresolved closure contributes only its function asset and makes
// the frame PARTIAL; a resolved one contributes its bridges and all hops of its
// members' paths. It also settles the completeness effect of unsupported
// layouts, which depends on whether the entry is related to a selected
// function.
func buildTopology(sf *sysfsFrame, node model.AssetRef, diags frameDiagnostics) *topology {
	t := &topology{
		node:   node,
		assets: map[string]model.AssetRef{node.Key(): node},
		hops:   map[hopKey]hop{},
		out:    map[string][]string{},
	}
	related := map[string]bool{}
	for _, f := range sf.selected {
		t.addAsset(pciAsset(model.KindPCIeFunction, f))
		members := closureOf(sf, f)
		for _, x := range members {
			related[x] = true
		}
		if !resolveClosure(sf, f, members, diags) {
			sf.partial = true
			continue
		}
		var own []hop
		for _, x := range members {
			if x != f {
				t.addAsset(bridgeAsset(sf.entries[x]))
			}
			own = append(own, pathHops(sf, sf.entries[x], node)...)
		}
		for _, h := range own {
			t.addHop(h)
		}
		if contradictory(own) {
			diags.add(codeTopologyContradiction, f)
		}
	}
	for _, name := range sortedEntryNames(sf) {
		e := sf.entries[name]
		if e.layoutOK {
			continue
		}
		diags.add(codeLayoutUnsupported, name)
		if related[name] {
			sf.partial = true
		}
	}
	return t
}

func (t *topology) addAsset(a model.AssetRef) {
	t.assets[a.Key()] = a
}

func (t *topology) addHop(h hop) {
	k := h.key()
	if _, ok := t.hops[k]; ok {
		return
	}
	t.hops[k] = h
	t.out[k.from] = append(t.out[k.from], k.to)
}

// closureOf returns closure(F): F plus, to a fixed point, every canonical
// address component of the path of each processed member. Members may name
// devices that are absent or unprocessed; those add nothing further.
func closureOf(sf *sysfsFrame, f string) []string {
	set := map[string]bool{f: true}
	queue := []string{f}
	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]
		e := sf.entries[x]
		if e == nil {
			continue
		}
		for _, c := range e.comps {
			if isCanonicalBDF(c) && !set[c] {
				set[c] = true
				queue = append(queue, c)
			}
		}
	}
	members := make([]string, 0, len(set))
	for x := range set {
		members = append(members, x)
	}
	slices.Sort(members)
	return members
}

// resolveClosure reports whether closure(F) is resolved (GFO-048): every
// member is listed, processed, classified and in a supported layout, and every
// member but F is a bridge. Each failing member leaves its cause.
func resolveClosure(sf *sysfsFrame, f string, members []string, diags frameDiagnostics) bool {
	resolved := true
	for _, x := range members {
		e := sf.entries[x]
		switch {
		case !sf.listed[x] || e == nil:
			diags.add(codeAncestorMissing, x)
			resolved = false
		case !e.layoutOK:
			// The layout diagnostic itself is recorded for every entry.
			resolved = false
		case x != f && (!e.classOK || !e.bridge):
			diags.add(codeAncestorNotBridge, x)
			resolved = false
		}
	}
	return resolved
}

// bridgeAsset returns a related bridge's asset: a root port when its own path
// puts it directly below the host bridge, a switch otherwise.
func bridgeAsset(e *sysfsEntry) model.AssetRef {
	if len(e.belowHost()) == 1 {
		return pciAsset(model.KindPCIeRootPort, e.name)
	}
	return pciAsset(model.KindPCIeSwitch, e.name)
}

// assetOf returns the asset a resolved closure member is represented by.
func assetOf(sf *sysfsFrame, bdf string) model.AssetRef {
	e := sf.entries[bdf]
	if e.gpu || e.nic {
		return pciAsset(model.KindPCIeFunction, bdf)
	}
	return bridgeAsset(e)
}

// pathHops returns the hops of one path: the address directly below the host
// bridge to the node, and each address to the one above it.
func pathHops(sf *sysfsFrame, e *sysfsEntry, node model.AssetRef) []hop {
	below := e.belowHost()
	hops := make([]hop, 0, len(below))
	hops = append(hops, hop{from: assetOf(sf, below[0]), to: node})
	for i := 1; i < len(below); i++ {
		hops = append(hops, hop{from: assetOf(sf, below[i]), to: assetOf(sf, below[i-1])})
	}
	return hops
}

// contradictory reports whether hops give some asset more than one parent or
// contain a cycle (GFO-049).
func contradictory(hops []hop) bool {
	next := map[string]string{}
	for _, h := range hops {
		k := h.key()
		if to, ok := next[k.from]; ok && to != k.to {
			return true
		}
		next[k.from] = k.to
	}
	// Every asset now has at most one parent; walk each chain once.
	const (
		unseen = iota
		walking
		done
	)
	state := map[string]int{}
	starts := make([]string, 0, len(next))
	for from := range next {
		starts = append(starts, from)
	}
	slices.Sort(starts)
	for _, start := range starts {
		var path []string
		cur, ok := start, true
		for ok && state[cur] == unseen {
			state[cur] = walking
			path = append(path, cur)
			cur, ok = next[cur]
		}
		if ok && state[cur] == walking {
			return true
		}
		for _, p := range path {
			state[p] = done
		}
	}
	return false
}

// sortedEntryNames returns the processed entry names, bytes ascending.
func sortedEntryNames(sf *sysfsFrame) []string {
	names := make([]string, 0, len(sf.entries))
	for name := range sf.entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// chainToRoot follows the frame's hops from f (GFO-060(a)). It succeeds only
// when every step has exactly one outgoing hop, nothing repeats, the node is
// reached, the asset right before the node is the only root port on the way,
// and f has a parent other than the node. It returns that parent and the root
// port.
func (t *topology) chainToRoot(f model.AssetRef) (parent, rootPort model.AssetRef, ok bool) {
	nodeKey := t.node.Key()
	cur := f.Key()
	visited := map[string]bool{cur: true}
	var chain []model.AssetRef
	for {
		outs := t.out[cur]
		if len(outs) != 1 {
			return model.AssetRef{}, model.AssetRef{}, false
		}
		next := outs[0]
		if next == nodeKey {
			break
		}
		if visited[next] {
			return model.AssetRef{}, model.AssetRef{}, false
		}
		visited[next] = true
		a, known := t.assets[next]
		if !known {
			return model.AssetRef{}, model.AssetRef{}, false
		}
		chain = append(chain, a)
		cur = next
	}
	if len(chain) == 0 {
		return model.AssetRef{}, model.AssetRef{}, false
	}
	last := chain[len(chain)-1]
	if last.Kind != model.KindPCIeRootPort {
		return model.AssetRef{}, model.AssetRef{}, false
	}
	for _, a := range chain[:len(chain)-1] {
		if a.Kind == model.KindPCIeRootPort {
			return model.AssetRef{}, model.AssetRef{}, false
		}
	}
	return chain[0], last, true
}
