package datapath

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Role is what a PCI function does on the data path.
type Role int

const (
	RoleOther Role = iota
	RoleBridge
	RoleGPU
	RoleNIC
	RoleDPU
)

func (r Role) String() string {
	switch r {
	case RoleBridge:
		return "bridge"
	case RoleGPU:
		return "gpu"
	case RoleNIC:
		return "nic"
	case RoleDPU:
		return "dpu"
	default:
		return "other"
	}
}

// Entry describes one known PCI device ID.
type Entry struct {
	Vendor, Device uint32
	Name           string
	Role           Role
	// PortGbps is the maximum port rate of a NIC or DPU, 0 when not applicable.
	PortGbps int
	// NVLink says the GPU has NVLink; a GPU without it peers over PCIe only.
	NVLink bool
}

// Vendor IDs.
const (
	VendorNVIDIA   = 0x10de
	VendorMellanox = 0x15b3
	VendorBroadcom = 0x1000
	VendorPLX      = 0x10b5
)

// builtin is the device table. IDs for parts that have not been seen on a
// real host are confirmed by `lspci -nn` during capture; an unknown ID still
// gets a role from its class code, so the path is drawn either way.
var builtin = []Entry{
	{VendorNVIDIA, 0x2bb1, "NVIDIA RTX PRO 6000 Blackwell Workstation Edition", RoleGPU, 0, false},
	{VendorNVIDIA, 0x2bb4, "NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition", RoleGPU, 0, false},
	{VendorNVIDIA, 0x2bb5, "NVIDIA RTX PRO 6000 Blackwell Server Edition", RoleGPU, 0, false},
	{VendorNVIDIA, 0x2901, "NVIDIA B200", RoleGPU, 0, true},
	{VendorNVIDIA, 0x2330, "NVIDIA H100 80GB HBM3", RoleGPU, 0, true},
	{VendorNVIDIA, 0x2331, "NVIDIA H100 PCIe", RoleGPU, 0, true},
	{VendorMellanox, 0x101b, "ConnectX-6", RoleNIC, 200, false},
	{VendorMellanox, 0x101d, "ConnectX-6 Dx", RoleNIC, 200, false},
	{VendorMellanox, 0x1021, "ConnectX-7", RoleNIC, 400, false},
	{VendorMellanox, 0x1023, "ConnectX-8", RoleNIC, 800, false},
	{VendorMellanox, 0xa2d6, "BlueField-2 (integrated ConnectX-6 Dx)", RoleDPU, 200, false},
	{VendorMellanox, 0xa2dc, "BlueField-3 (integrated ConnectX-7)", RoleDPU, 400, false},
	{VendorMellanox, 0xc2d3, "BlueField-2 SoC management interface", RoleOther, 0, false},
	{VendorMellanox, 0xc2d5, "BlueField-3 SoC management interface", RoleOther, 0, false},
}

// Table resolves vendor:device pairs to entries.
type Table struct {
	entries map[[2]uint32]Entry
}

// DefaultTable returns the built-in table.
func DefaultTable() *Table {
	t := &Table{entries: map[[2]uint32]Entry{}}
	for _, e := range builtin {
		t.entries[[2]uint32{e.Vendor, e.Device}] = e
	}
	return t
}

// Lookup returns the entry for a vendor and device ID.
func (t *Table) Lookup(vendor, device uint32) (Entry, bool) {
	e, ok := t.entries[[2]uint32{vendor, device}]
	return e, ok
}

type jsonEntry struct {
	Vendor   string `json:"vendor"`
	Device   string `json:"device"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	PortGbps int    `json:"port_gbps"`
	NVLink   bool   `json:"nvlink"`
}

// LoadExtra adds the entries of a JSON file to the table. The file is a list
// of {"vendor":"0x15b3","device":"0x1021","name":"...","role":"nic",
// "port_gbps":400,"nvlink":false}; a listed ID replaces the built-in one.
func (t *Table) LoadExtra(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var list []jsonEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for i, je := range list {
		v, err1 := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(je.Vendor), "0x"), 16, 16)
		d, err2 := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(je.Device), "0x"), 16, 16)
		if err1 != nil || err2 != nil || je.Name == "" {
			return fmt.Errorf("%s: entry %d: vendor, device and name are required hex IDs and a name", path, i)
		}
		role, ok := map[string]Role{"gpu": RoleGPU, "nic": RoleNIC, "dpu": RoleDPU, "bridge": RoleBridge, "other": RoleOther}[je.Role]
		if !ok {
			return fmt.Errorf("%s: entry %d: role must be gpu, nic, dpu, bridge or other", path, i)
		}
		t.entries[[2]uint32{uint32(v), uint32(d)}] = Entry{uint32(v), uint32(d), je.Name, role, je.PortGbps, je.NVLink}
	}
	return nil
}
