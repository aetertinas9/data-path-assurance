package sysfs

import (
	"os"
	"slices"
	"strings"
)

// NUMANode is one NUMA node with its CPU list as sysfs prints it.
type NUMANode struct {
	ID      int
	CPUList string
}

// RDMAPort is one port of an RDMA device.
type RDMAPort struct {
	Number    int
	RateText  string // "400 Gb/sec (4X NDR)"
	RateGbps  int    // 0 when unknown
	State     string // "ACTIVE", "DOWN", ...
	PhysState string // "LinkUp", ...
	LinkLayer string // "InfiniBand" or "Ethernet"
}

// RDMADevice is one /sys/class/infiniband entry bound to a PCI function.
type RDMADevice struct {
	Name  string
	BDF   string
	Ports []RDMAPort
}

// NetDevice is one /sys/class/net entry bound to a PCI function.
type NetDevice struct {
	Name      string
	BDF       string
	SpeedMbps int // 0 when unknown (link down reports no speed)
	OperState string
	Carrier   string
}

// RDMAAttributes are the per-port attribute names capture copies.
var RDMAAttributes = []string{"rate", "state", "phys_state", "link_layer"}

// NetAttributes are the per-interface attribute names capture copies.
var NetAttributes = []string{"speed", "operstate", "carrier"}

func (h *Host) readNUMA(root *os.Root) {
	const dir = "devices/system/node"
	names, ok := listDir(root, dir)
	if !ok {
		return // a host without NUMA exposes nothing here; not a problem
	}
	slices.Sort(names)
	for _, name := range names {
		if !strings.HasPrefix(name, "node") {
			continue
		}
		id, ok := parseInt(name[4:])
		if !ok || id < 0 || id > 4095 {
			continue
		}
		cpus, st := readAttr(root, dir+"/"+name+"/cpulist")
		if st != OK {
			h.problem(dir+"/"+name+"/cpulist", st)
			continue
		}
		h.NUMA = append(h.NUMA, NUMANode{ID: int(id), CPUList: cpus})
	}
	slices.SortFunc(h.NUMA, func(a, b NUMANode) int { return a.ID - b.ID })
}

func (h *Host) readRDMA(root *os.Root) {
	const dir = "class/infiniband"
	names, ok := listDir(root, dir)
	if !ok {
		return
	}
	slices.Sort(names)
	for _, name := range names {
		if !namePattern.MatchString(name) {
			continue
		}
		base := dir + "/" + name
		bdf, ok := readLinkBase(root, base+"/device")
		if !ok || !IsBDF(bdf) {
			h.problem(base+"/device", Malformed)
			continue
		}
		dev := RDMADevice{Name: name, BDF: bdf}
		ports, ok := listDir(root, base+"/ports")
		if !ok {
			h.problem(base+"/ports", Unreadable)
		}
		slices.Sort(ports)
		for _, p := range ports {
			n, ok := parseInt(p)
			if !ok || n < 1 || n > 255 {
				continue
			}
			pd := base + "/ports/" + p
			port := RDMAPort{Number: int(n)}
			if s, st := readAttr(root, pd+"/rate"); st == OK {
				port.RateText = s
				port.RateGbps = parseRate(s)
			} else {
				h.problem(pd+"/rate", st)
			}
			if s, st := readAttr(root, pd+"/state"); st == OK {
				port.State = afterColon(s)
			} else {
				h.problem(pd+"/state", st)
			}
			if s, st := readAttr(root, pd+"/phys_state"); st == OK {
				port.PhysState = afterColon(s)
			}
			if s, st := readAttr(root, pd+"/link_layer"); st == OK {
				port.LinkLayer = s
			}
			dev.Ports = append(dev.Ports, port)
		}
		h.RDMA = append(h.RDMA, dev)
	}
}

func (h *Host) readNet(root *os.Root) {
	const dir = "class/net"
	names, ok := listDir(root, dir)
	if !ok {
		return
	}
	slices.Sort(names)
	for _, name := range names {
		if !namePattern.MatchString(name) {
			continue
		}
		base := dir + "/" + name
		bdf, ok := readLinkBase(root, base+"/device")
		if !ok || !IsBDF(bdf) {
			continue // virtual interfaces have no PCI device; not a problem
		}
		nd := NetDevice{Name: name, BDF: bdf}
		if s, st := readAttr(root, base+"/speed"); st == OK {
			if v, ok := parseInt(s); ok && v > 0 {
				nd.SpeedMbps = int(v)
			}
		}
		if s, st := readAttr(root, base+"/operstate"); st == OK {
			nd.OperState = s
		}
		if s, st := readAttr(root, base+"/carrier"); st == OK {
			nd.Carrier = s
		}
		h.Net = append(h.Net, nd)
	}
}

// parseRate reads the leading "<n> Gb/sec" of an RDMA rate string.
func parseRate(s string) int {
	f := strings.Fields(s)
	if len(f) < 2 || !strings.HasPrefix(f[1], "Gb") {
		return 0
	}
	v, ok := parseInt(f[0])
	if !ok || v <= 0 || v > 100000 {
		return 0
	}
	return int(v)
}

// afterColon turns "4: ACTIVE" into "ACTIVE".
func afterColon(s string) string {
	if _, after, ok := strings.Cut(s, ":"); ok {
		return strings.TrimSpace(after)
	}
	return s
}
