package datapath

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/sysfs"
)

// Classify places b relative to a from their sysfs chains and NUMA nodes.
func (r *Report) Classify(a, b *Device) Locality {
	ra, rb := a.Raw, b.Raw
	if ra.HostBridge == "" || rb.HostBridge == "" {
		return LocUnknown
	}
	if ra.HostBridge != rb.HostBridge {
		na, nb := r.numaOf(a), r.numaOf(b)
		switch {
		case na < 0 || nb < 0:
			return LocUnknown
		case na == nb:
			return LocNODE
		default:
			return LocSYS
		}
	}
	common := commonPrefix(ra.Chain[:len(ra.Chain)-1], rb.Chain[:len(rb.Chain)-1])
	if common == 0 {
		return LocPHB
	}
	traversed := (len(ra.Chain) - 1 - common) + (len(rb.Chain) - 1 - common)
	if traversed <= 2 {
		return LocPIX
	}
	return LocPXB
}

// numaOf is the NUMA node of a device, falling back to its root port's.
func (r *Report) numaOf(d *Device) int {
	if d.Raw.NUMAStatus == sysfs.OK && d.Raw.NUMA >= 0 {
		return d.Raw.NUMA
	}
	if rp := r.Devices[d.Raw.RootPort()]; rp != nil && rp.Raw.NUMAStatus == sysfs.OK && rp.Raw.NUMA >= 0 {
		return rp.Raw.NUMA
	}
	return -1
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// pathLinks lists the links traversed between a and b: every device on
// either chain below their common ancestor, each contributing its link to
// its parent. Crossing the root complex contributes no link.
func (r *Report) pathLinks(a, b *Device) []*Device {
	ca, cb := a.Raw.Chain, b.Raw.Chain
	common := 0
	if a.Raw.HostBridge == b.Raw.HostBridge {
		common = commonPrefix(ca, cb)
	}
	var devs []*Device
	for _, c := range [][]string{ca, cb} {
		for i := len(c) - 1; i >= common; i-- {
			if i == 0 {
				continue // a root port has no link upward
			}
			if d := r.Devices[c[i]]; d != nil {
				devs = append(devs, d)
			}
		}
	}
	return devs
}

// attach ranks every NIC as seen from g.
func (r *Report) attach(g *GPU) {
	for _, n := range r.NICs {
		at := Attachment{NIC: n, Locality: r.Classify(g.Dev, n.Dev)}
		if r.Topo != nil && g.TopoName != "" {
			if name := r.topoNIC(n); name != "" {
				at.TopoRel, _ = r.Topo.Rel(g.TopoName, name)
				if loc, ok := ParseLocality(at.TopoRel); ok && at.Locality != LocUnknown && loc != at.Locality {
					r.Limitations = append(r.Limitations, fmt.Sprintf("%s to %s: sysfs says %s, nvidia-smi topo says %s", g.Dev.BDF, n.Dev.BDF, at.Locality, loc))
				}
			}
		}
		at.Capacity, at.Bottleneck, at.Hops = r.capacity(g.Dev, n)
		g.NICs = append(g.NICs, at)
	}
	rank := func(a Attachment) int {
		score := 0
		if r.Options.PreferRDMA && len(a.NIC.RDMA) == 0 {
			score += 1000
		}
		if !a.NIC.PortUp {
			score += 100
		}
		if a.Locality == LocUnknown {
			score += 50
		}
		return score + int(a.Locality)
	}
	slices.SortStableFunc(g.NICs, func(x, y Attachment) int {
		if c := rank(x) - rank(y); c != 0 {
			return c
		}
		if c := y.NIC.PortGbps - x.NIC.PortGbps; c != 0 {
			return c
		}
		return strings.Compare(x.NIC.Dev.BDF, y.NIC.Dev.BDF)
	})
}

// topoNIC finds the topo column of a NIC through the NIC legend and the RDMA
// device names on it.
func (r *Report) topoNIC(n *NIC) string {
	for col, name := range r.Topo.NICNames {
		for _, rd := range n.RDMA {
			if rd.Name == name {
				return col
			}
		}
	}
	return ""
}

// capacity is the slowest hop between a GPU and a NIC's port.
func (r *Report) capacity(gpu *Device, n *NIC) (float64, string, []string) {
	best, where := -1.0, ""
	var hops []string
	consider := func(gbps float64, name string) {
		if best < 0 || gbps < best {
			best, where = gbps, name
		}
	}
	for _, d := range r.pathLinks(gpu, n.Dev) {
		if !d.Link.Known || d.Link.GBps == 0 {
			return 0, d.BDF + " link unknown", hops
		}
		hops = append(hops, fmt.Sprintf("%s Gen%d x%d %.0fGB/s", d.BDF, Gen(d.Link.Speed), d.Link.Width, d.Link.GBps))
		consider(d.Link.GBps, "pcie "+d.BDF)
	}
	if gpu.Raw.HostBridge != n.Dev.Raw.HostBridge {
		hops = append(hops, "root complex / CPU interconnect")
	}
	if n.PortGbps > 0 {
		consider(float64(n.PortGbps)/8, "port "+n.Port)
	} else {
		return 0, "port rate unknown", hops
	}
	return best, where, hops
}

// peers lists the other GPUs as seen from g.
func (r *Report) peers(g *GPU) {
	for _, o := range r.GPUs {
		if o == g {
			continue
		}
		p := Peer{GPU: o, Locality: r.Classify(g.Dev, o.Dev)}
		if r.Topo != nil && g.TopoName != "" && o.TopoName != "" {
			p.TopoRel, _ = r.Topo.Rel(g.TopoName, o.TopoName)
			if strings.HasPrefix(p.TopoRel, "NV") {
				fmt.Sscanf(p.TopoRel, "NV%d", &p.NVLinks)
			} else if loc, ok := ParseLocality(p.TopoRel); ok && p.Locality != LocUnknown && loc != p.Locality {
				r.Limitations = append(r.Limitations, fmt.Sprintf("%s to %s: sysfs says %s, nvidia-smi topo says %s", g.Dev.BDF, o.Dev.BDF, p.Locality, loc))
			}
		}
		if p.Locality == LocPIX || p.Locality == LocPXB {
			p.ACSKnown = true
			for _, d := range r.pathLinks(g.Dev, o.Dev) {
				if d.Role != RoleBridge {
					continue
				}
				cfg := d.Raw.Config
				if cfg == nil || !cfg.ExtendedRead {
					p.ACSKnown = false
					continue
				}
				if cfg.HasACS && cfg.ACSControl&(ACSRedirectMask) != 0 {
					p.ACSOn = append(p.ACSOn, d.BDF)
				}
			}
		}
		slices.Sort(p.ACSOn)
		g.Peers = append(g.Peers, p)
	}
}

// ACSRedirectMask are the ACS control bits that send peer-to-peer traffic
// through the root complex instead of across the switch.
const ACSRedirectMask = 1<<2 | 1<<3 | 1<<5 // P2P request redirect, completion redirect, egress control
