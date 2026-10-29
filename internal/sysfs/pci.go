// Package sysfs reads the data-path facts of one host from a sysfs tree:
// PCI functions with their containment chain, link state and NUMA node, the
// RDMA and network ports that sit on them, and the CPU lists of the NUMA
// nodes. Every read goes through an injected *os.Root, is read-only and
// bounded, and follows symlinks only inside that root, so a fixture directory
// and a live /sys are read by the same code. Nothing here is inferred: a value
// that cannot be read is reported as such and never guessed.
package sysfs

import (
	"os"
	"path"
	"slices"
	"strings"
)

// Attribute names read under every PCI function directory. Capture copies
// exactly these and nothing else.
var PCIAttributes = []string{
	"vendor", "device", "class", "revision", "subsystem_vendor", "subsystem_device",
	"current_link_width", "max_link_width", "current_link_speed", "max_link_speed",
	"numa_node", "aer_dev_correctable", "aer_dev_nonfatal", "aer_dev_fatal",
}

// PCILinks are the symlinks whose base name is read under a PCI function.
var PCILinks = []string{"driver", "physfn"}

// Problem is one thing that could not be read or understood. It is reported,
// never hidden, and never turned into a value.
type Problem struct {
	Path   string
	Status Status
}

func (p Problem) String() string { return p.Path + ": " + p.Status.String() }

// Attr is one read attribute value with its read status.
type Attr struct {
	Status Status
	Text   string
}

// Link is the negotiated and maximum PCIe link state of one function. Speed is
// in units of 0.1 GT/s (320 = 32.0 GT/s, PCIe Gen5). A zero with a non-OK
// status is unknown, not zero.
type Link struct {
	Width, MaxWidth             int
	WidthStatus, MaxWidthStatus Status
	Speed, MaxSpeed             int
	SpeedStatus, MaxSpeedStatus Status
}

// Config holds what the PCI configuration space said, when it could be read
// beyond the 64 bytes an unprivileged reader gets.
type Config struct {
	Bytes int
	// PCIe capability: device/port type (bits 7:4 of PCIe Capabilities).
	PCIeType     int
	HasPCIe      bool
	MaxPayload   int // bytes, from Device Control
	MaxReadReq   int // bytes, from Device Control
	HasACS       bool
	ACSCapable   uint16
	ACSControl   uint16
	ExtendedRead bool // true when the extended space (>= 256 bytes) was read
}

// ACS control bits.
const (
	ACSSourceValidation    = 1 << 0
	ACSTranslationBlock    = 1 << 1
	ACSP2PRequestRedirect  = 1 << 2
	ACSP2PCompletionRedir  = 1 << 3
	ACSUpstreamForwarding  = 1 << 4
	ACSP2PEgressControl    = 1 << 5
	ACSDirectTranslatedP2P = 1 << 6
)

// PCIDevice is one PCI function as sysfs lists it.
type PCIDevice struct {
	BDF string
	// Dir is the path of the function directory relative to the sysfs root,
	// e.g. devices/pci0000:16/0000:16:01.0/0000:17:00.0.
	Dir string
	// HostBridge is the pciDDDD:BB directory name the function sits under.
	HostBridge string
	// Chain is every PCI address on the way down from the root port to this
	// function, the function itself last.
	Chain []string

	Vendor, Device, Class uint32
	SubVendor, SubDevice  uint32
	Revision              uint32
	HaveIDs, HaveSubIDs   bool
	HaveRevision          bool
	Driver                string
	IsVF                  bool
	Link                  Link
	NUMA                  int // -1 when unknown or not set
	NUMAStatus            Status
	AERCorrectable        int64 // sum of the aer_dev_correctable counters
	AERCorrectableStatus  Status
	Config                *Config
	ConfigStatus          Status
}

// IsBridge reports whether the function is a PCI-to-PCI bridge.
func (d *PCIDevice) IsBridge() bool { return d.Class>>8 == 0x0604 }

// ClassBase returns the class base code (0x03 display, 0x02 network, ...).
func (d *PCIDevice) ClassBase() uint32 { return d.Class >> 16 }

// Parent returns the address of the bridge above this function, or "" at a
// root port.
func (d *PCIDevice) Parent() string {
	if len(d.Chain) < 2 {
		return ""
	}
	return d.Chain[len(d.Chain)-2]
}

// RootPort returns the root port address of the chain.
func (d *PCIDevice) RootPort() string {
	if len(d.Chain) == 0 {
		return ""
	}
	return d.Chain[0]
}

// Host is everything a sysfs tree said about one host.
type Host struct {
	Devices  []PCIDevice // by BDF, ascending
	NUMA     []NUMANode  // by ID, ascending
	RDMA     []RDMADevice
	Net      []NetDevice
	Problems []Problem
}

// Device returns the function at bdf.
func (h *Host) Device(bdf string) *PCIDevice {
	i, ok := slices.BinarySearchFunc(h.Devices, bdf, func(d PCIDevice, k string) int { return strings.Compare(d.BDF, k) })
	if !ok {
		return nil
	}
	return &h.Devices[i]
}

// Read reads the whole host from root. It never fails as a whole: what could
// not be read is listed under Problems.
func Read(root *os.Root) *Host {
	h := &Host{}
	h.readPCI(root)
	h.readNUMA(root)
	h.readRDMA(root)
	h.readNet(root)
	return h
}

func (h *Host) problem(p string, s Status) {
	h.Problems = append(h.Problems, Problem{Path: p, Status: s})
}

const pciDevicesDir = "bus/pci/devices"

func (h *Host) readPCI(root *os.Root) {
	names, ok := listDir(root, pciDevicesDir)
	if !ok {
		h.problem(pciDevicesDir, Unreadable)
		return
	}
	slices.Sort(names)
	for _, name := range names {
		if !IsBDF(name) {
			h.problem(pciDevicesDir+"/"+name, Malformed)
			continue
		}
		d, ok := h.resolve(root, name)
		if !ok {
			continue
		}
		h.readAttributes(root, &d)
		h.Devices = append(h.Devices, d)
	}
}

// resolve turns bus/pci/devices/<bdf> into the device directory and its
// chain, following the link lexically and lstat-ing each component.
func (h *Host) resolve(root *os.Root, name string) (PCIDevice, bool) {
	entry := pciDevicesDir + "/" + name
	target, err := root.Readlink(entry)
	if err != nil || target == "" || len(target) > maxLinkTarget || strings.HasPrefix(target, "/") {
		h.problem(entry, Malformed)
		return PCIDevice{}, false
	}
	rel := path.Clean(pciDevicesDir + "/" + target)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		h.problem(entry, Malformed)
		return PCIDevice{}, false
	}
	comps := strings.Split(rel, "/")
	if comps[0] != "devices" || comps[len(comps)-1] != name {
		h.problem(entry, Malformed)
		return PCIDevice{}, false
	}
	host := -1
	for i, c := range comps {
		if hostBridgePattern.MatchString(c) {
			host = i
			break
		}
	}
	if host < 0 {
		h.problem(entry, Malformed)
		return PCIDevice{}, false
	}
	d := PCIDevice{BDF: name, Dir: rel, HostBridge: comps[host], NUMA: -1}
	for _, c := range comps[host+1:] {
		if !IsBDF(c) {
			h.problem(entry, Malformed)
			return PCIDevice{}, false
		}
		d.Chain = append(d.Chain, c)
	}
	for i := range comps {
		info, err := root.Lstat(strings.Join(comps[:i+1], "/"))
		if err != nil || !info.IsDir() {
			h.problem(entry, Unreadable)
			return PCIDevice{}, false
		}
	}
	return d, true
}

func (h *Host) hex(root *os.Root, dir, attr string, required bool) (uint32, bool) {
	s, st := readAttr(root, dir+"/"+attr)
	if st != OK {
		if required || st != Missing {
			h.problem(dir+"/"+attr, st)
		}
		return 0, false
	}
	v, ok := parseHex(s)
	if !ok {
		h.problem(dir+"/"+attr, Malformed)
	}
	return v, ok
}

func (h *Host) readAttributes(root *os.Root, d *PCIDevice) {
	dir := d.Dir
	vendor, okV := h.hex(root, dir, "vendor", true)
	device, okD := h.hex(root, dir, "device", true)
	class, okC := h.hex(root, dir, "class", true)
	d.Vendor, d.Device, d.Class, d.HaveIDs = vendor, device, class, okV && okD && okC
	sv, okSV := h.hex(root, dir, "subsystem_vendor", false)
	sd, okSD := h.hex(root, dir, "subsystem_device", false)
	d.SubVendor, d.SubDevice, d.HaveSubIDs = sv, sd, okSV && okSD
	d.Revision, d.HaveRevision = h.hex(root, dir, "revision", false)

	if base, ok := readLinkBase(root, dir+"/driver"); ok {
		d.Driver = base
	}
	if _, err := root.Lstat(dir + "/physfn"); err == nil {
		d.IsVF = true
	}

	d.Link.Width, d.Link.WidthStatus = readWidth(root, dir+"/current_link_width")
	d.Link.MaxWidth, d.Link.MaxWidthStatus = readWidth(root, dir+"/max_link_width")
	d.Link.Speed, d.Link.SpeedStatus = readSpeed(root, dir+"/current_link_speed")
	d.Link.MaxSpeed, d.Link.MaxSpeedStatus = readSpeed(root, dir+"/max_link_speed")

	// numa_node is writable on a live host; it is opened read-only here.
	if s, st := readAttr(root, dir+"/numa_node"); st == OK {
		if v, ok := parseInt(s); ok && v >= -1 && v < 4096 {
			d.NUMA, d.NUMAStatus = int(v), OK
		} else {
			d.NUMAStatus = Malformed
		}
	} else {
		d.NUMAStatus = st
	}

	d.AERCorrectable, d.AERCorrectableStatus = readAER(root, dir+"/aer_dev_correctable")

	cfg, st := readConfig(root, dir+"/config")
	d.Config, d.ConfigStatus = cfg, st
}

var validWidths = map[int]bool{1: true, 2: true, 4: true, 8: true, 12: true, 16: true, 32: true}

func readWidth(root *os.Root, name string) (int, Status) {
	s, st := readAttr(root, name)
	if st != OK {
		return 0, st
	}
	if s == "Unknown" || s == "0" {
		return 0, NoData
	}
	v, ok := parseInt(s)
	if !ok || !validWidths[int(v)] {
		return 0, Malformed
	}
	return int(v), OK
}

// readSpeed parses "32.0 GT/s PCIe" or "2.5 GT/s" into tenths of GT/s.
func readSpeed(root *os.Root, name string) (int, Status) {
	s, st := readAttr(root, name)
	if st != OK {
		return 0, st
	}
	return ParseSpeed(s)
}

// ParseSpeed parses a sysfs link speed string into tenths of GT/s.
func ParseSpeed(s string) (int, Status) {
	if s == "Unknown" || strings.HasPrefix(s, "Unknown") {
		return 0, NoData
	}
	fields := strings.Fields(s)
	if len(fields) < 2 || fields[1] != "GT/s" {
		return 0, Malformed
	}
	whole, frac, _ := strings.Cut(fields[0], ".")
	w, ok := parseInt(whole)
	if !ok || w < 0 || w > 1000 {
		return 0, Malformed
	}
	f := int64(0)
	if frac != "" {
		if len(frac) != 1 || frac[0] < '0' || frac[0] > '9' {
			return 0, Malformed
		}
		f = int64(frac[0] - '0')
	}
	v := int(w*10 + f)
	if v == 0 {
		return 0, NoData
	}
	return v, OK
}

// readAER sums the counters of an aer_dev_* table ("RxErr 0\nBadTLP 3\n...").
func readAER(root *os.Root, name string) (int64, Status) {
	s, st := readAttr(root, name)
	if st != OK {
		return 0, st
	}
	var total int64
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return 0, Malformed
		}
		v, ok := parseInt(f[1])
		if !ok || v < 0 {
			return 0, Malformed
		}
		total += v
	}
	return total, OK
}

// readConfig reads the configuration space and parses the capabilities the
// data path cares about. Unprivileged readers get 64 bytes, which is not
// enough for any capability; the result then says ExtendedRead false.
func readConfig(root *os.Root, name string) (*Config, Status) {
	data, st := readFile(root, name, MaxConfigBytes)
	if st != OK {
		return nil, st
	}
	if len(data) > MaxConfigBytes {
		data = data[:MaxConfigBytes]
	}
	return ParseConfig(data), OK
}

// ParseConfig parses a PCI configuration space image.
func ParseConfig(data []byte) *Config {
	c := &Config{Bytes: len(data)}
	if len(data) < 64 {
		return c
	}
	u16 := func(off int) (uint16, bool) {
		if off+2 > len(data) {
			return 0, false
		}
		return uint16(data[off]) | uint16(data[off+1])<<8, true
	}
	u32 := func(off int) (uint32, bool) {
		if off+4 > len(data) {
			return 0, false
		}
		return uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16 | uint32(data[off+3])<<24, true
	}
	// Capability list (status bit 4 says it exists).
	if status, ok := u16(0x06); ok && status&0x10 != 0 {
		ptr := int(data[0x34]) &^ 3
		for i := 0; i < 48 && ptr >= 0x40 && ptr+2 <= len(data); i++ {
			id, next := data[ptr], int(data[ptr+1])&^3
			if id == 0x10 { // PCI Express
				if capReg, ok := u16(ptr + 2); ok {
					c.HasPCIe = true
					c.PCIeType = int(capReg>>4) & 0xf
				}
				if devctl, ok := u16(ptr + 8); ok {
					c.MaxPayload = 128 << ((devctl >> 5) & 7)
					c.MaxReadReq = 128 << ((devctl >> 12) & 7)
				}
			}
			if next == 0 || next == ptr {
				break
			}
			ptr = next
		}
	}
	if len(data) >= 0x104 {
		c.ExtendedRead = true
		ptr := 0x100
		for i := 0; i < 64 && ptr >= 0x100 && ptr+4 <= len(data); i++ {
			hdr, ok := u32(ptr)
			if !ok || hdr == 0 || hdr == 0xffffffff {
				break
			}
			id, next := hdr&0xffff, int(hdr>>20)&^3
			if id == 0x000d { // ACS
				if capv, ok := u16(ptr + 4); ok {
					ctl, _ := u16(ptr + 6)
					c.HasACS, c.ACSCapable, c.ACSControl = true, capv, ctl
				}
			}
			if next == 0 || next == ptr {
				break
			}
			ptr = next
		}
	}
	return c
}
