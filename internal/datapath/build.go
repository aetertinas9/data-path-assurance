package datapath

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/nvidia"
	"github.com/aetertinas9/data-path-assurance/internal/sysfs"
)

// Build assembles the report of one host from its sysfs facts and the
// nvidia-smi outputs.
func Build(source string, h *sysfs.Host, nv nvidia.Outputs, opts Options) *Report {
	if opts.Table == nil {
		opts.Table = DefaultTable()
	}
	if opts.LocalityTarget == LocUnknown {
		opts.LocalityTarget = LocPXB
	}
	r := &Report{Source: source, NUMA: h.NUMA, Devices: map[string]*Device{}, Options: opts}
	for _, p := range h.Problems {
		r.Limitations = append(r.Limitations, "sysfs: "+p.String())
	}

	for i := range h.Devices {
		raw := &h.Devices[i]
		r.Devices[raw.BDF] = r.device(raw)
	}
	for _, d := range r.Devices {
		d.Link = r.link(d)
	}

	r.buildNICs(h)
	gpus, nvlinks, topo := r.parseNVIDIA(nv)
	r.buildGPUs(gpus, nvlinks, topo)
	for _, g := range r.GPUs {
		r.attach(g)
		r.peers(g)
		g.Checks = r.checks(g)
	}
	return r
}

func (r *Report) device(raw *sysfs.PCIDevice) *Device {
	d := &Device{BDF: raw.BDF, Raw: raw}
	if raw.IsVF {
		d.Role, d.Model = RoleOther, "virtual function"
		return d
	}
	if e, ok := r.Options.Table.Lookup(raw.Vendor, raw.Device); ok && raw.HaveIDs {
		d.Entry = &e
		d.Role, d.Model, d.Known = e.Role, e.Name, true
		return d
	}
	switch {
	case raw.IsBridge():
		d.Role, d.Model = RoleBridge, "PCI bridge"
	case raw.ClassBase() == 0x03 && raw.Vendor == VendorNVIDIA:
		d.Role, d.Model = RoleGPU, "NVIDIA GPU"
	case raw.ClassBase() == 0x02:
		d.Role, d.Model = RoleNIC, "network controller"
	default:
		d.Role, d.Model = RoleOther, "device"
	}
	if raw.HaveIDs {
		d.Model += fmt.Sprintf(" %04x:%04x", raw.Vendor, raw.Device)
	}
	if raw.Driver != "" {
		d.Model += " (" + raw.Driver + ")"
	}
	return d
}

// link judges the link of d to its parent. A root port has no link upward.
func (r *Report) link(d *Device) LinkState {
	var ls LinkState
	raw := d.Raw
	if raw.Parent() == "" {
		return ls
	}
	if raw.Link.WidthStatus != sysfs.OK || raw.Link.SpeedStatus != sysfs.OK {
		return ls
	}
	ls.Known = true
	ls.Width, ls.Speed = raw.Link.Width, raw.Link.Speed
	ls.MaxWidth, ls.MaxSpeed = raw.Link.MaxWidth, raw.Link.MaxSpeed
	ls.ExpWidth, ls.ExpSpeed = ls.MaxWidth, ls.MaxSpeed
	if parent := r.Devices[raw.Parent()]; parent != nil {
		pl := parent.Raw.Link
		if pl.MaxWidthStatus == sysfs.OK && pl.MaxSpeedStatus == sysfs.OK && raw.Link.MaxWidthStatus == sysfs.OK && raw.Link.MaxSpeedStatus == sysfs.OK {
			ls.ParentKnown = true
			ls.ExpWidth = min(ls.MaxWidth, pl.MaxWidth)
			ls.ExpSpeed = min(ls.MaxSpeed, pl.MaxSpeed)
			ls.PlatformLimited = ls.MaxWidth > pl.MaxWidth || ls.MaxSpeed > pl.MaxSpeed
		}
	}
	if ls.ExpWidth > 0 && ls.ExpSpeed > 0 {
		ls.Degraded = ls.Width < ls.ExpWidth || ls.Speed < ls.ExpSpeed
	}
	ls.GBps = float64(ls.Width) * LaneGBps(ls.Speed)
	return ls
}

func (r *Report) buildNICs(h *sysfs.Host) {
	for _, bdf := range sortedKeys(r.Devices) {
		d := r.Devices[bdf]
		if d.Role != RoleNIC && d.Role != RoleDPU {
			continue
		}
		n := &NIC{Dev: d, IsDPU: d.Role == RoleDPU}
		for _, rd := range h.RDMA {
			if rd.BDF == bdf {
				n.RDMA = append(n.RDMA, rd)
			}
		}
		for _, nd := range h.Net {
			if nd.BDF == bdf {
				n.Net = append(n.Net, nd)
			}
		}
		n.pickPort()
		r.NICs = append(r.NICs, n)
	}
}

// pickPort chooses the port that represents the NIC: the fastest active RDMA
// port, else the fastest RDMA port, else the fastest network interface.
func (n *NIC) pickPort() {
	best := -1
	for _, rd := range n.RDMA {
		for _, p := range rd.Ports {
			up := p.State == "ACTIVE"
			score := p.RateGbps
			if up {
				score += 1 << 20
			}
			if score > best {
				best = score
				n.Port = fmt.Sprintf("%s/%d", rd.Name, p.Number)
				n.PortGbps, n.PortState, n.PortUp, n.PortKnown = p.RateGbps, p.State, up, p.State != ""
			}
		}
	}
	if n.PortKnown {
		return
	}
	for _, nd := range n.Net {
		up := nd.OperState == "up"
		score := nd.SpeedMbps / 1000
		if up {
			score += 1 << 20
		}
		if score > best {
			best = score
			n.Port = nd.Name
			n.PortGbps, n.PortState, n.PortUp, n.PortKnown = nd.SpeedMbps/1000, nd.OperState, up, nd.OperState != ""
		}
	}
}

func (r *Report) parseNVIDIA(nv nvidia.Outputs) ([]nvidia.GPU, []nvidia.NVLink, *nvidia.Topo) {
	for _, name := range []string{nvidia.QueryFile, nvidia.NVLinkFile, nvidia.TopoFile} {
		if err := nv.Errors[name]; err != nil {
			r.Limitations = append(r.Limitations, "nvidia-smi "+strings.TrimSuffix(strings.TrimSuffix(name, ".csv"), ".txt")+": "+err.Error())
		}
	}
	var gpus []nvidia.GPU
	if nv.Query != nil {
		var errs []error
		gpus, errs = nvidia.ParseQuery(nv.Query)
		for _, e := range errs {
			r.Limitations = append(r.Limitations, "nvidia-smi query: "+e.Error())
		}
	}
	var links []nvidia.NVLink
	if nv.NVLink != nil {
		var errs []error
		links, errs = nvidia.ParseNVLink(nv.NVLink)
		for _, e := range errs {
			r.Limitations = append(r.Limitations, "nvidia-smi nvlink: "+e.Error())
		}
	}
	var topo *nvidia.Topo
	if nv.Topo != nil {
		t, err := nvidia.ParseTopo(nv.Topo)
		if err != nil {
			r.Limitations = append(r.Limitations, "nvidia-smi topo: "+err.Error())
		} else {
			topo = t
		}
	}
	r.Topo = topo
	return gpus, links, topo
}

func (r *Report) buildGPUs(gpus []nvidia.GPU, links []nvidia.NVLink, topo *nvidia.Topo) {
	byBDF := map[string]*nvidia.GPU{}
	for i := range gpus {
		g := &gpus[i]
		if r.Devices[g.BDF] == nil {
			r.Limitations = append(r.Limitations, "nvidia-smi lists "+g.BDF+" which sysfs does not")
			continue
		}
		byBDF[g.BDF] = g
	}
	byUUID := map[string]*nvidia.NVLink{}
	for i := range links {
		byUUID[links[i].UUID] = &links[i]
	}
	for _, bdf := range sortedKeys(r.Devices) {
		d := r.Devices[bdf]
		if d.Role != RoleGPU {
			continue
		}
		g := &GPU{Dev: d, Index: -1, NUMA: -1, Name: d.Model}
		if smi := byBDF[bdf]; smi != nil {
			g.Smi, g.Index, g.UUID, g.Name = smi, smi.Index, smi.UUID, smi.Name
			g.TopoName = fmt.Sprintf("GPU%d", smi.Index)
			d.Model, d.Known = smi.Name, true
			g.NVLink = byUUID[smi.UUID]
		} else if len(byBDF) > 0 {
			r.Limitations = append(r.Limitations, "GPU "+bdf+" is not in the nvidia-smi inventory")
		}
		g.Hops = r.hops(d)
		if d.Raw.NUMAStatus == sysfs.OK && d.Raw.NUMA >= 0 {
			g.NUMA = d.Raw.NUMA
		} else if rp := r.Devices[d.Raw.RootPort()]; rp != nil && rp.Raw.NUMAStatus == sysfs.OK && rp.Raw.NUMA >= 0 {
			g.NUMA = rp.Raw.NUMA
		}
		for _, n := range r.NUMA {
			if n.ID == g.NUMA {
				g.CPUList = n.CPUList
			}
		}
		r.GPUs = append(r.GPUs, g)
	}
}

// hops lists the chain of a device from itself up to its root port.
func (r *Report) hops(d *Device) []Hop {
	chain := d.Raw.Chain
	var hops []Hop
	for i := len(chain) - 1; i >= 0; i-- {
		dev := r.Devices[chain[i]]
		if dev == nil {
			dev = &Device{BDF: chain[i], Role: RoleBridge, Model: "bridge (not listed)", Raw: &sysfs.PCIDevice{BDF: chain[i], NUMA: -1}}
			r.Devices[chain[i]] = dev
		}
		kind := "bridge"
		switch {
		case i == len(chain)-1:
			kind = d.Role.String()
		case i == 0:
			kind = "root"
		case dev.Raw.Config != nil && dev.Raw.Config.HasPCIe:
			switch dev.Raw.Config.PCIeType {
			case 5:
				kind = "upstream"
			case 6:
				kind = "downstream"
			case 4:
				kind = "root"
			}
		default:
			// Without config space: a bridge whose parent is a bridge that is
			// not a root port is a downstream port when its parent is itself
			// below the root port; alternate levels from the root.
			if (len(chain)-1-i)%2 == 1 {
				kind = "downstream"
			} else {
				kind = "upstream"
			}
		}
		hops = append(hops, Hop{Dev: dev, Kind: kind})
	}
	return hops
}

func sortedKeys(m map[string]*Device) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
