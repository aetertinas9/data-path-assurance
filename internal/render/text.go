// Package render writes a datapath.Report as text, JSON or a Mermaid diagram.
package render

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/aetertinas9/data-path-assurance/internal/datapath"
	"github.com/aetertinas9/data-path-assurance/internal/sysfs"
)

func gbps(v float64) string {
	if v <= 0 {
		return "?"
	}
	return fmt.Sprintf("%.0fGB/s", v)
}

func link(l datapath.LinkState) string {
	if !l.Known {
		return "link ?"
	}
	s := fmt.Sprintf("Gen%d x%d %s", datapath.Gen(l.Speed), l.Width, gbps(l.GBps))
	switch {
	case l.Degraded:
		s += fmt.Sprintf(" DEGRADED (expected Gen%d x%d)", datapath.Gen(l.ExpSpeed), l.ExpWidth)
	case l.PlatformLimited:
		s += fmt.Sprintf(" (device max Gen%d x%d)", datapath.Gen(l.MaxSpeed), l.MaxWidth)
	}
	return s
}

func acs(d *datapath.Device) string {
	c := d.Raw.Config
	switch {
	case c == nil || !c.ExtendedRead:
		return "ACS ?"
	case !c.HasACS:
		return "ACS none"
	case c.ACSControl&datapath.ACSRedirectMask != 0:
		var bits []string
		for _, b := range []struct {
			m uint16
			n string
		}{{sysfs.ACSP2PRequestRedirect, "ReqRedir"}, {sysfs.ACSP2PCompletionRedir, "CmplRedir"}, {sysfs.ACSP2PEgressControl, "EgressCtrl"}} {
			if c.ACSControl&b.m != 0 {
				bits = append(bits, b.n)
			}
		}
		return "ACS REDIRECT " + strings.Join(bits, "+")
	default:
		return "ACS off"
	}
}

func numa(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprint(n)
}

func port(n *datapath.NIC) string {
	if !n.PortKnown {
		return "port ?"
	}
	rate := "?"
	if n.PortGbps > 0 {
		rate = fmt.Sprintf("%dGb/s", n.PortGbps)
	}
	return fmt.Sprintf("%s %s %s", n.Port, rate, n.PortState)
}

func nvlink(g *datapath.GPU) string {
	if g.NVLink == nil || len(g.NVLink.Links) == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", g.NVLink.Active, len(g.NVLink.Links))
}

func worst(g *datapath.GPU) string {
	w := datapath.Pass
	for _, c := range g.Checks {
		if c.Status == datapath.Fail {
			return "FAIL"
		}
		if c.Status == datapath.Warn {
			w = datapath.Warn
		}
	}
	return w.String()
}

// Text renders the whole report.
func Text(r *datapath.Report) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "HOST  source=%s  numa=%d  gpus=%d  nics=%d  result=%s\n\n", r.Source, len(r.NUMA), len(r.GPUs), len(r.NICs), r.Overall())
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GPU\tMODEL\tLINK\tNUMA\tBEST NIC\tLOC\tCAP\tNVLINK\tRESULT")
	for _, g := range r.GPUs {
		best, loc, capacity := "-", "-", "-"
		if a := g.Best(); a != nil {
			best = a.NIC.Dev.BDF + " " + shortModel(a.NIC.Dev.Model)
			loc = a.Locality.String()
			capacity = gbps(a.Capacity)
		}
		lk := "?"
		if g.Dev.Link.Known {
			lk = fmt.Sprintf("Gen%d x%d", datapath.Gen(g.Dev.Link.Speed), g.Dev.Link.Width)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", g.Dev.BDF, shortModel(g.Name), lk, numa(g.NUMA), best, loc, capacity, nvlink(g), worst(g))
	}
	tw.Flush()
	for _, g := range r.GPUs {
		b.WriteString("\n")
		b.Write(GPUText(r, g))
	}
	if len(r.Limitations) > 0 {
		b.WriteString("\nLIMITATIONS\n")
		for _, l := range r.Limitations {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	return b.Bytes()
}

// GPUText renders one GPU's path.
func GPUText(r *datapath.Report, g *datapath.GPU) []byte {
	var b bytes.Buffer
	id := ""
	if g.UUID != "" {
		id = fmt.Sprintf("  [GPU%d %s]", g.Index, g.UUID)
	}
	fmt.Fprintf(&b, "GPU %s  %s%s\n", g.Dev.BDF, g.Name, id)
	fmt.Fprintf(&b, "  link   %s\n", link(g.Dev.Link))
	b.WriteString("  path   ")
	for i, h := range g.Hops {
		if i > 0 {
			b.WriteString("\n         ")
		}
		switch h.Kind {
		case "root":
			fmt.Fprintf(&b, "%s root  NUMA %s", h.Dev.BDF, numa(g.NUMA))
			if g.CPUList != "" {
				fmt.Fprintf(&b, "  cpus %s", g.CPUList)
			}
		case "gpu":
			fmt.Fprintf(&b, "%s gpu  %s", h.Dev.BDF, link(h.Dev.Link))
		default:
			fmt.Fprintf(&b, "%s %s  %s  %s", h.Dev.BDF, h.Kind, link(h.Dev.Link), acs(h.Dev))
		}
	}
	b.WriteString("\n")
	for _, a := range g.NICs {
		kind := "nic"
		if a.NIC.IsDPU {
			kind = "dpu"
		}
		topo := ""
		if a.TopoRel != "" {
			topo = "  topo " + a.TopoRel
		}
		fmt.Fprintf(&b, "  %s    %s %s  %s  %s  cap %s (%s)%s\n", kind, a.NIC.Dev.BDF, shortModel(a.NIC.Dev.Model), port(a.NIC), a.Locality, gbps(a.Capacity), a.Bottleneck, topo)
	}
	for _, p := range g.Peers {
		extra := ""
		switch {
		case p.NVLinks > 0:
			extra = fmt.Sprintf("  nvlink x%d", p.NVLinks)
		case len(p.ACSOn) > 0:
			extra = "  p2p BLOCKED by ACS on " + strings.Join(p.ACSOn, ", ")
		case (p.Locality == datapath.LocPIX || p.Locality == datapath.LocPXB) && !p.ACSKnown:
			extra = "  p2p ACS ?"
		case p.Locality == datapath.LocPIX || p.Locality == datapath.LocPXB:
			extra = "  p2p via switch"
		}
		fmt.Fprintf(&b, "  peer   %s %s  %s%s\n", p.GPU.Dev.BDF, shortModel(p.GPU.Name), p.Locality, extra)
	}
	if g.NVLink != nil && len(g.NVLink.Links) > 0 {
		fmt.Fprintf(&b, "  nvlink %d of %d links active\n", g.NVLink.Active, len(g.NVLink.Links))
	}
	b.WriteString("  checks\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, c := range g.Checks {
		fmt.Fprintf(tw, "    %s\t%s\t%s\n", c.Name, c.Status, c.Detail)
	}
	tw.Flush()
	return b.Bytes()
}

func shortModel(m string) string {
	m = strings.TrimPrefix(m, "NVIDIA ")
	if i := strings.Index(m, " ("); i > 0 {
		m = m[:i]
	}
	return m
}
