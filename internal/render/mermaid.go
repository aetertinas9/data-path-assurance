package render

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/datapath"
)

func id(bdf string) string {
	return "n" + strings.NewReplacer(":", "_", ".", "_").Replace(bdf)
}

// Mermaid renders the physical topology that the GPUs' paths touch as a
// flowchart: NUMA nodes, root ports, switch ports, GPUs and NICs, with NVLink
// peers as dashed edges.
func Mermaid(r *datapath.Report) []byte {
	var b bytes.Buffer
	b.WriteString("flowchart TB\n")
	nodes := map[string]bool{}
	edges := map[string]bool{}
	add := func(s string) {
		if !nodes[s] {
			nodes[s] = true
			b.WriteString("  " + s + "\n")
		}
	}
	edge := func(s string) {
		if !edges[s] {
			edges[s] = true
			b.WriteString("  " + s + "\n")
		}
	}
	declare := func(d *datapath.Device, label string) {
		shape := "[%s]"
		switch d.Role {
		case datapath.RoleGPU:
			shape = "([%s])"
		case datapath.RoleNIC, datapath.RoleDPU:
			shape = "[[%s]]"
		case datapath.RoleBridge:
			shape = "{{%s}}"
		}
		add(id(d.BDF) + fmt.Sprintf(shape, `"`+strings.ReplaceAll(label, `"`, "'")+`"`))
	}
	chain := func(hops []datapath.Hop, numaID int) {
		for i, h := range hops {
			label := h.Dev.BDF + "<br/>" + shortModel(h.Dev.Model)
			if h.Kind == "gpu" || h.Kind == "nic" || h.Kind == "dpu" {
				label = shortModel(h.Dev.Model) + "<br/>" + h.Dev.BDF
			}
			declare(h.Dev, label)
			if i+1 < len(hops) {
				edge(fmt.Sprintf("%s -- %q --> %s", id(hops[i+1].Dev.BDF), linkLabel(h.Dev.Link), id(h.Dev.BDF)))
			} else if numaID >= 0 {
				add(fmt.Sprintf("numa%d[\"NUMA %d\"]", numaID, numaID))
				edge(fmt.Sprintf("numa%d --> %s", numaID, id(h.Dev.BDF)))
			}
		}
	}
	for _, g := range r.GPUs {
		chain(g.Hops, g.NUMA)
	}
	for _, n := range r.NICs {
		hops := []datapath.Hop{}
		for _, g := range r.GPUs {
			for _, a := range g.NICs {
				if a.NIC == n {
					hops = nicHops(r, n)
				}
			}
		}
		if len(hops) == 0 {
			continue
		}
		numaID := -1
		if n.Dev.Raw.NUMA >= 0 {
			numaID = n.Dev.Raw.NUMA
		}
		chain(hops, numaID)
	}
	var seen []string
	for _, g := range r.GPUs {
		for _, p := range g.Peers {
			if p.NVLinks == 0 {
				continue
			}
			k := g.Dev.BDF + p.GPU.Dev.BDF
			rk := p.GPU.Dev.BDF + g.Dev.BDF
			if slices.Contains(seen, rk) {
				continue
			}
			seen = append(seen, k)
			edge(fmt.Sprintf("%s -. \"NVLink x%d\" .- %s", id(g.Dev.BDF), p.NVLinks, id(p.GPU.Dev.BDF)))
		}
	}
	return b.Bytes()
}

func linkLabel(l datapath.LinkState) string {
	if !l.Known {
		return "?"
	}
	s := fmt.Sprintf("Gen%d x%d", datapath.Gen(l.Speed), l.Width)
	if l.Degraded {
		s += " DEGRADED"
	}
	return s
}

func nicHops(r *datapath.Report, n *datapath.NIC) []datapath.Hop {
	chain := n.Dev.Raw.Chain
	var hops []datapath.Hop
	for i := len(chain) - 1; i >= 0; i-- {
		d := r.Devices[chain[i]]
		if d == nil {
			return nil
		}
		kind := "bridge"
		switch {
		case i == len(chain)-1:
			kind = n.Dev.Role.String()
		case i == 0:
			kind = "root"
		}
		hops = append(hops, datapath.Hop{Dev: d, Kind: kind})
	}
	return hops
}
