package datapath

import (
	"fmt"
	"slices"
	"strings"
)

func linkText(l LinkState) string {
	if !l.Known {
		return "unknown"
	}
	return fmt.Sprintf("Gen%d x%d", Gen(l.Speed), l.Width)
}

func expText(l LinkState) string {
	return fmt.Sprintf("Gen%d x%d", Gen(l.ExpSpeed), l.ExpWidth)
}

// checks judges one GPU's path.
func (r *Report) checks(g *GPU) []Check {
	var out []Check
	add := func(name string, s Status, detail string) { out = append(out, Check{name, s, detail}) }

	// 1. The GPU's own link.
	l := g.Dev.Link
	switch {
	case !l.Known:
		add("gpu-link", Unknown, "link width or speed not readable")
	case l.Degraded:
		add("gpu-link", Fail, fmt.Sprintf("%s negotiated, %s expected", linkText(l), expText(l)))
	case l.PlatformLimited:
		add("gpu-link", Warn, fmt.Sprintf("%s; GPU supports Gen%d x%d but its port does not", linkText(l), Gen(l.MaxSpeed), l.MaxWidth))
	default:
		add("gpu-link", Pass, linkText(l))
	}

	// 2. Every link above the GPU up to the root port.
	status, detail := Pass, "all at expected width and speed"
	for _, h := range g.Hops[1:] {
		if h.Kind == "root" {
			continue
		}
		hl := h.Dev.Link
		if !hl.Known {
			if status == Pass {
				status, detail = Unknown, h.Dev.BDF+" link not readable"
			}
			continue
		}
		if hl.Degraded {
			status, detail = Fail, fmt.Sprintf("%s at %s, expected %s", h.Dev.BDF, linkText(hl), expText(hl))
			break
		}
	}
	if len(g.Hops) <= 2 {
		status, detail = NA, "GPU sits directly under the root port"
	}
	add("upstream-links", status, detail)

	// 3..5. The best NIC.
	best := g.Best()
	switch {
	case best == nil:
		add("nic-locality", Unknown, "no NIC or DPU found")
		add("nic-port", Unknown, "no NIC or DPU found")
		add("numa-aligned", Unknown, "no NIC or DPU found")
	default:
		n := best.NIC
		where := n.Dev.BDF + " " + n.Dev.Model
		switch {
		case best.Locality == LocUnknown:
			add("nic-locality", Unknown, where+": locality not derivable")
		case best.Locality <= r.Options.LocalityTarget:
			add("nic-locality", Pass, fmt.Sprintf("%s via %s (target %s or better)", where, best.Locality, r.Options.LocalityTarget))
		default:
			add("nic-locality", Fail, fmt.Sprintf("%s via %s, target %s or better", where, best.Locality, r.Options.LocalityTarget))
		}
		switch {
		case !n.PortKnown:
			add("nic-port", Unknown, where+": no RDMA or network port state readable")
		case !n.PortUp:
			add("nic-port", Fail, fmt.Sprintf("%s %s", n.Port, n.PortState))
		case n.Dev.Entry != nil && n.Dev.Entry.PortGbps > 0 && n.PortGbps < n.Dev.Entry.PortGbps:
			add("nic-port", Warn, fmt.Sprintf("%s %d Gb/s, device supports %d Gb/s", n.Port, n.PortGbps, n.Dev.Entry.PortGbps))
		case n.PortGbps == 0:
			add("nic-port", Warn, fmt.Sprintf("%s %s, rate unknown", n.Port, n.PortState))
		default:
			add("nic-port", Pass, fmt.Sprintf("%s %d Gb/s %s", n.Port, n.PortGbps, n.PortState))
		}
		nn := r.numaOf(n.Dev)
		switch {
		case g.NUMA < 0 || nn < 0:
			add("numa-aligned", Unknown, "NUMA node of GPU or NIC not readable")
		case g.NUMA == nn:
			add("numa-aligned", Pass, fmt.Sprintf("GPU and %s both on NUMA %d", n.Port, g.NUMA))
		default:
			add("numa-aligned", Warn, fmt.Sprintf("GPU on NUMA %d, %s on NUMA %d", g.NUMA, n.Port, nn))
		}
	}

	// 6. PCIe peer-to-peer through switches must not be redirected by ACS.
	var pcie, blocked, unknown []string
	for _, p := range g.Peers {
		if p.Locality != LocPIX && p.Locality != LocPXB {
			continue
		}
		pcie = append(pcie, p.GPU.Dev.BDF)
		if !p.ACSKnown {
			unknown = append(unknown, p.GPU.Dev.BDF)
		}
		for _, b := range p.ACSOn {
			if !contains(blocked, b) {
				blocked = append(blocked, b)
			}
		}
	}
	slices.Sort(blocked)
	switch {
	case len(pcie) == 0:
		add("p2p-acs", NA, "no GPU peer under the same root port")
	case len(blocked) > 0:
		add("p2p-acs", Fail, "ACS P2P redirect enabled on "+strings.Join(blocked, ", "))
	case len(unknown) > 0:
		add("p2p-acs", Unknown, "ACS not readable (config space needs root) for peers "+strings.Join(unknown, ", "))
	default:
		add("p2p-acs", Pass, "no ACS redirect on the switch path to "+strings.Join(pcie, ", "))
	}

	// 7. NVLink.
	switch {
	case g.NVLink == nil || len(g.NVLink.Links) == 0:
		if g.Dev.Entry != nil && g.Dev.Entry.NVLink {
			add("nvlink", Unknown, "GPU has NVLink but nvidia-smi nvlink reported no links")
		} else {
			add("nvlink", NA, "no NVLink on this GPU")
		}
	case g.NVLink.Active == len(g.NVLink.Links):
		add("nvlink", Pass, fmt.Sprintf("%d of %d links active", g.NVLink.Active, len(g.NVLink.Links)))
	default:
		add("nvlink", Fail, fmt.Sprintf("%d of %d links active", g.NVLink.Active, len(g.NVLink.Links)))
	}

	// 8. Correctable PCIe errors on the GPU link.
	raw := g.Dev.Raw
	switch {
	case raw.AERCorrectableStatus != 0:
		add("pcie-errors", NA, "AER counters not exposed")
	case raw.AERCorrectable > 0:
		add("pcie-errors", Warn, fmt.Sprintf("%d correctable errors counted on the GPU link", raw.AERCorrectable))
	default:
		add("pcie-errors", Pass, "no correctable errors counted")
	}

	// 9. Payload sizes between the GPU and its NIC.
	if best != nil && raw.Config != nil && raw.Config.HasPCIe && best.NIC.Dev.Raw.Config != nil && best.NIC.Dev.Raw.Config.HasPCIe {
		gc, nc := raw.Config, best.NIC.Dev.Raw.Config
		if gc.MaxPayload != nc.MaxPayload {
			add("pcie-payload", Warn, fmt.Sprintf("MaxPayload GPU %dB, NIC %dB; MaxReadReq GPU %dB, NIC %dB", gc.MaxPayload, nc.MaxPayload, gc.MaxReadReq, nc.MaxReadReq))
		} else {
			add("pcie-payload", Pass, fmt.Sprintf("MaxPayload %dB on both; MaxReadReq GPU %dB, NIC %dB", gc.MaxPayload, gc.MaxReadReq, nc.MaxReadReq))
		}
	} else {
		add("pcie-payload", NA, "config space not readable")
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
