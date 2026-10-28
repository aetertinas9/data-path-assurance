// Package datapath turns the sysfs and NVIDIA facts of one host into the data
// path of every GPU: the PCIe chain up to its NUMA node, the NICs and DPUs it
// can reach and how close they are, its GPU peers over NVLink or PCIe, the
// capacity of each path and the checks that say whether the path can run at
// full speed. Everything here is a pure function of its inputs.
package datapath

import (
	"github.com/aetertinas9/data-path-assurance/internal/nvidia"
	"github.com/aetertinas9/data-path-assurance/internal/sysfs"
)

// Device is one PCI function with its data-path role.
type Device struct {
	BDF   string
	Role  Role
	Model string
	// Known says the model came from the device table or the driver, not
	// from the bare vendor:device pair.
	Known bool
	Entry *Entry
	Raw   *sysfs.PCIDevice
	Link  LinkState
}

// LinkState is the link of a function to its parent, judged against what the
// two ends can do.
type LinkState struct {
	Known           bool
	Width, Speed    int // negotiated; speed in 0.1 GT/s
	MaxWidth        int
	MaxSpeed        int
	ExpWidth        int // min of the two ends' maxima
	ExpSpeed        int
	ParentKnown     bool
	Degraded        bool // negotiated below expected
	PlatformLimited bool // this end can do more than its parent
	GBps            float64
}

// Gen returns the PCIe generation of a speed in 0.1 GT/s, 0 when unknown.
func Gen(speed int) int {
	switch speed {
	case 25:
		return 1
	case 50:
		return 2
	case 80:
		return 3
	case 160:
		return 4
	case 320:
		return 5
	case 640:
		return 6
	}
	return 0
}

// LaneGBps is the usable per-lane, per-direction bandwidth of each speed.
func LaneGBps(speed int) float64 {
	switch speed {
	case 25:
		return 0.25
	case 50:
		return 0.5
	case 80:
		return 0.985
	case 160:
		return 1.969
	case 320:
		return 3.938
	case 640:
		return 7.563
	}
	return 0
}

// Hop is one device on a GPU's chain, the GPU first and the root port last.
type Hop struct {
	Dev  *Device
	Kind string // gpu, downstream, upstream, bridge, root
}

// Locality is how two functions are placed relative to each other, in the
// vocabulary of `nvidia-smi topo -m`.
type Locality int

const (
	LocUnknown Locality = iota
	LocPIX              // same PCIe switch
	LocPXB              // several PCIe switches, same root port
	LocPHB              // same host bridge, different root ports
	LocNODE             // different host bridges, same NUMA node
	LocSYS              // different NUMA nodes
)

func (l Locality) String() string {
	switch l {
	case LocPIX:
		return "PIX"
	case LocPXB:
		return "PXB"
	case LocPHB:
		return "PHB"
	case LocNODE:
		return "NODE"
	case LocSYS:
		return "SYS"
	}
	return "UNKNOWN"
}

// ParseLocality reads a locality name; ok is false for anything else.
func ParseLocality(s string) (Locality, bool) {
	for l := LocPIX; l <= LocSYS; l++ {
		if l.String() == s {
			return l, true
		}
	}
	return LocUnknown, false
}

// NIC is a NIC or DPU function with the ports on it.
type NIC struct {
	Dev   *Device
	RDMA  []sysfs.RDMADevice
	Net   []sysfs.NetDevice
	IsDPU bool
	// Port is the best port: the highest active rate, else any.
	Port      string // "mlx5_0/1" or "eth0"
	PortGbps  int
	PortState string // ACTIVE, DOWN, up, ...
	PortUp    bool
	PortKnown bool
}

// Attachment is one NIC as seen from a GPU.
type Attachment struct {
	NIC      *NIC
	Locality Locality
	TopoRel  string // what nvidia-smi topo said, "" when absent
	// Capacity is the per-direction bandwidth of the slowest hop between the
	// GPU and the NIC port, 0 when a hop is unknown.
	Capacity   float64
	Bottleneck string
	// Hops are the links traversed, GPU side first then NIC side.
	Hops []string
}

// Peer is another GPU as seen from a GPU.
type Peer struct {
	GPU      *GPU
	Locality Locality
	TopoRel  string
	NVLinks  int      // from the topo matrix (NV#), 0 when none
	ACSOn    []string // bridges on the PCIe path with P2P redirect enabled
	ACSKnown bool
}

// GPU is the data path of one GPU.
type GPU struct {
	Dev      *Device
	Index    int // nvidia-smi index, -1 when unknown
	UUID     string
	Name     string
	Hops     []Hop
	NUMA     int
	CPUList  string
	NVLink   *nvidia.NVLink
	NICs     []Attachment // best first
	Peers    []Peer
	Checks   []Check
	Smi      *nvidia.GPU
	TopoName string // "GPU<n>" in the topo matrix
}

// Best returns the preferred NIC attachment, nil without any.
func (g *GPU) Best() *Attachment {
	if len(g.NICs) == 0 {
		return nil
	}
	return &g.NICs[0]
}

// Status of one check.
type Status int

const (
	Pass Status = iota
	Warn
	Fail
	Unknown
	NA
)

func (s Status) String() string {
	return [...]string{"PASS", "WARN", "FAIL", "UNKNOWN", "N/A"}[s]
}

// Check is one judgement about a GPU's path.
type Check struct {
	Name   string
	Status Status
	Detail string
}

// Options steer the checks.
type Options struct {
	// LocalityTarget is the worst acceptable GPU-to-NIC locality.
	LocalityTarget Locality
	// PreferRDMA ranks RDMA-capable NICs above plain network ports.
	PreferRDMA bool
	Table      *Table
}

// DefaultOptions: PXB or better, RDMA first, built-in device table.
func DefaultOptions() Options {
	return Options{LocalityTarget: LocPXB, PreferRDMA: true, Table: DefaultTable()}
}

// Report is the data path of one host.
type Report struct {
	Source      string
	NUMA        []sysfs.NUMANode
	Devices     map[string]*Device
	GPUs        []*GPU
	NICs        []*NIC
	Topo        *nvidia.Topo
	Limitations []string
	Options     Options
}

// Overall is the worst check status over every GPU (NA and Unknown do not
// count as failures).
func (r *Report) Overall() Status {
	worst := Pass
	for _, g := range r.GPUs {
		for _, c := range g.Checks {
			if c.Status == Fail {
				return Fail
			}
			if c.Status == Warn {
				worst = Warn
			}
		}
	}
	return worst
}
