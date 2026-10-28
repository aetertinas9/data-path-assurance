package datapath_test

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a synthetic host under dir: a sysfs tree the way the
// kernel lays it out and canned nvidia-smi outputs.
type fixture struct {
	t   testing.TB
	dir string
}

func newFixture(t testing.TB, dir string) *fixture {
	t.Helper()
	for _, d := range []string{"sys/bus/pci/devices", "sys/devices/system/node", "sys/class/infiniband", "sys/class/net", "nvidia"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &fixture{t: t, dir: dir}
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) link(rel, target string) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(filepath.FromSlash(target), p); err != nil {
		f.t.Fatal(err)
	}
}

// pciSpec is one PCI function to lay out.
type pciSpec struct {
	host   string   // host bridge dir, e.g. pci0000:16
	chain  []string // root port ... self
	vendor uint32
	device uint32
	class  uint32
	numa   int
	width  int // current / max link width
	maxW   int
	speed  string // current / max link speed text
	maxS   string
	driver string
	config []byte
	aer    string // aer_dev_correctable content; "" omits the file
}

func (f *fixture) pci(s pciSpec) {
	f.t.Helper()
	dir := "sys/devices/" + s.host + "/" + strings.Join(s.chain, "/")
	bdf := s.chain[len(s.chain)-1]
	f.write(dir+"/vendor", fmt.Sprintf("0x%04x\n", s.vendor))
	f.write(dir+"/device", fmt.Sprintf("0x%04x\n", s.device))
	f.write(dir+"/class", fmt.Sprintf("0x%06x\n", s.class))
	f.write(dir+"/numa_node", fmt.Sprintf("%d\n", s.numa))
	if s.width > 0 {
		f.write(dir+"/current_link_width", fmt.Sprintf("%d\n", s.width))
		f.write(dir+"/max_link_width", fmt.Sprintf("%d\n", s.maxW))
		f.write(dir+"/current_link_speed", s.speed+"\n")
		f.write(dir+"/max_link_speed", s.maxS+"\n")
	}
	if s.driver != "" {
		f.link(dir+"/driver", "../../../../bus/pci/drivers/"+s.driver)
	}
	if s.config != nil {
		f.write(dir+"/config", string(s.config))
	}
	if s.aer != "" {
		f.write(dir+"/aer_dev_correctable", s.aer)
	}
	f.link("sys/bus/pci/devices/"+bdf, path.Join("../../../devices", s.host, strings.Join(s.chain, "/")))
}

func (f *fixture) numa(id int, cpus string) {
	f.write(fmt.Sprintf("sys/devices/system/node/node%d/cpulist", id), cpus+"\n")
}

func (f *fixture) rdma(name, bdf string, rate, state string) {
	base := "sys/class/infiniband/" + name
	f.link(base+"/device", "../../../bus/pci/devices/"+bdf)
	f.write(base+"/ports/1/rate", rate+"\n")
	f.write(base+"/ports/1/state", state+"\n")
	f.write(base+"/ports/1/phys_state", "5: LinkUp\n")
	f.write(base+"/ports/1/link_layer", "InfiniBand\n")
}

func (f *fixture) net(name, bdf string, speed int, oper string) {
	base := "sys/class/net/" + name
	f.link(base+"/device", "../../../bus/pci/devices/"+bdf)
	f.write(base+"/speed", fmt.Sprintf("%d\n", speed))
	f.write(base+"/operstate", oper+"\n")
	f.write(base+"/carrier", "1\n")
}

func (f *fixture) nvidia(query, nvlink, topo string) {
	f.write("nvidia/query.csv", query)
	f.write("nvidia/nvlink.txt", nvlink)
	f.write("nvidia/topo.txt", topo)
}

// config builds a 4 KiB configuration space image with a PCIe capability at
// 0x40 (port type, MaxPayload and MaxReadReq) and, when acs is true, an ACS
// extended capability at 0x100 with the given control bits.
func config(pcieType int, mps, mrrs int, acs bool, acsCtrl uint16) []byte {
	img := make([]byte, 4096)
	img[0x06] = 0x10 // status: capabilities list
	img[0x34] = 0x40
	img[0x40] = 0x10 // PCI Express
	img[0x41] = 0
	capReg := uint16(pcieType<<4) | 2
	img[0x42], img[0x43] = byte(capReg), byte(capReg>>8)
	enc := func(bytes int) uint16 {
		n := uint16(0)
		for v := 128; v < bytes; v <<= 1 {
			n++
		}
		return n
	}
	devctl := enc(mps)<<5 | enc(mrrs)<<12
	img[0x48], img[0x49] = byte(devctl), byte(devctl>>8)
	if acs {
		hdr := uint32(0x000d) | 1<<16 // id, version 1, next 0
		img[0x100], img[0x101], img[0x102], img[0x103] = byte(hdr), byte(hdr>>8), byte(hdr>>16), byte(hdr>>24)
		img[0x104], img[0x105] = 0x7f, 0 // capable of everything
		img[0x106], img[0x107] = byte(acsCtrl), byte(acsCtrl>>8)
	}
	return img
}

const (
	gen4 = "16.0 GT/s PCIe"
	gen5 = "32.0 GT/s PCIe"
	gen6 = "64.0 GT/s PCIe"
)

// twoSocketRTX lays out a two-socket host with three RTX PRO 6000 GPUs, a
// ConnectX-7 and a BlueField-3:
//
//	NUMA 0  pci0000:16  root 16:01.0 - usp 19:00.0 - dsp 1a:04.0 - GPU 1b:00.0  (ACS redirect on)
//	                                            - dsp 1a:05.0 - GPU 1d:00.0  (ACS redirect on)
//	                                            - dsp 1a:06.0 - CX7 1c:00.0  mlx5_0 400G ACTIVE
//	NUMA 1  pci0000:97  root 97:01.0 - GPU 9b:00.0  (Gen4 x8, degraded)
//	                    root 97:02.0 - BF3 9c:00.0  mlx5_2 400G ACTIVE
func twoSocketRTX(t testing.TB, dir string) *fixture {
	f := newFixture(t, dir)
	f.numa(0, "0-47,96-143")
	f.numa(1, "48-95,144-191")
	rp := config(4, 256, 512, false, 0)
	dspOn := config(6, 256, 512, true, 0x1d)
	dspOff := config(6, 256, 512, true, 0x00)
	usp := config(5, 256, 512, false, 0)
	ep := config(0, 256, 512, false, 0)
	f.pci(pciSpec{host: "pci0000:16", chain: []string{"0000:16:01.0"}, vendor: 0x8086, device: 0x1bbd, class: 0x060400, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "pcieport", config: rp})
	f.pci(pciSpec{host: "pci0000:16", chain: []string{"0000:16:01.0", "0000:19:00.0"}, vendor: 0x1000, device: 0xc030, class: 0x060400, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "pcieport", config: usp})
	for _, d := range []struct {
		bdf string
		cfg []byte
	}{{"0000:1a:04.0", dspOn}, {"0000:1a:05.0", dspOn}, {"0000:1a:06.0", dspOff}} {
		f.pci(pciSpec{host: "pci0000:16", chain: []string{"0000:16:01.0", "0000:19:00.0", d.bdf}, vendor: 0x1000, device: 0xc030, class: 0x060400, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "pcieport", config: d.cfg})
	}
	f.pci(pciSpec{host: "pci0000:16", chain: []string{"0000:16:01.0", "0000:19:00.0", "0000:1a:04.0", "0000:1b:00.0"}, vendor: 0x10de, device: 0x2bb5, class: 0x030200, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "nvidia", config: ep, aer: "RxErr 0\nBadTLP 0\n"})
	f.pci(pciSpec{host: "pci0000:16", chain: []string{"0000:16:01.0", "0000:19:00.0", "0000:1a:05.0", "0000:1d:00.0"}, vendor: 0x10de, device: 0x2bb5, class: 0x030200, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "nvidia", config: ep, aer: "RxErr 0\nBadTLP 0\n"})
	f.pci(pciSpec{host: "pci0000:16", chain: []string{"0000:16:01.0", "0000:19:00.0", "0000:1a:06.0", "0000:1c:00.0"}, vendor: 0x15b3, device: 0x1021, class: 0x020700, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "mlx5_core", config: ep})
	f.pci(pciSpec{host: "pci0000:97", chain: []string{"0000:97:01.0"}, vendor: 0x8086, device: 0x1bbd, class: 0x060400, numa: 1, width: 8, maxW: 16, speed: gen4, maxS: gen5, driver: "pcieport", config: rp})
	f.pci(pciSpec{host: "pci0000:97", chain: []string{"0000:97:01.0", "0000:9b:00.0"}, vendor: 0x10de, device: 0x2bb5, class: 0x030200, numa: 1, width: 8, maxW: 16, speed: gen4, maxS: gen5, driver: "nvidia", config: ep, aer: "RxErr 12\nBadTLP 3\n"})
	f.pci(pciSpec{host: "pci0000:97", chain: []string{"0000:97:02.0"}, vendor: 0x8086, device: 0x1bbd, class: 0x060400, numa: 1, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "pcieport", config: rp})
	f.pci(pciSpec{host: "pci0000:97", chain: []string{"0000:97:02.0", "0000:9c:00.0"}, vendor: 0x15b3, device: 0xa2dc, class: 0x020700, numa: 1, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "mlx5_core", config: ep})
	f.rdma("mlx5_0", "0000:1c:00.0", "400 Gb/sec (4X NDR)", "4: ACTIVE")
	f.rdma("mlx5_2", "0000:9c:00.0", "400 Gb/sec (4X NDR)", "4: ACTIVE")
	f.net("ibp28s0", "0000:1c:00.0", 400000, "up")
	f.nvidia(
		"0, NVIDIA RTX PRO 6000 Blackwell Server Edition, GPU-11111111-1111-1111-1111-111111111111, 00000000:1B:00.0, 5, 5, 16, 16\n"+
			"1, NVIDIA RTX PRO 6000 Blackwell Server Edition, GPU-22222222-2222-2222-2222-222222222222, 00000000:1D:00.0, 5, 5, 16, 16\n"+
			"2, NVIDIA RTX PRO 6000 Blackwell Server Edition, GPU-33333333-3333-3333-3333-333333333333, 00000000:9B:00.0, 4, 5, 8, 16\n",
		"GPU 0: NVIDIA RTX PRO 6000 Blackwell Server Edition (UUID: GPU-11111111-1111-1111-1111-111111111111)\n"+
			"\t NVML: Unable to retrieve NVLink information\n",
		`	GPU0	GPU1	GPU2	NIC0	NIC1	CPU Affinity	NUMA Affinity	GPU NUMA ID
GPU0	 X 	PIX	SYS	PIX	SYS	0-47,96-143	0		N/A
GPU1	PIX	 X 	SYS	PIX	SYS	0-47,96-143	0		N/A
GPU2	SYS	SYS	 X 	SYS	PHB	48-95,144-191	1		N/A
NIC0	PIX	PIX	SYS	 X 	SYS
NIC1	SYS	SYS	PHB	SYS	 X 

Legend:

  X    = Self
  SYS  = Connection traversing PCIe as well as the SMP interconnect between NUMA nodes (e.g., QPI/UPI)
  NODE = Connection traversing PCIe as well as the interconnect between PCIe Host Bridges within a NUMA node
  PHB  = Connection traversing PCIe as well as a PCIe Host Bridge (typically the CPU)
  PXB  = Connection traversing multiple PCIe bridges (without traversing the PCIe Host Bridge)
  PIX  = Connection traversing at most a single PCIe bridge
  NV#  = Connection traversing a bonded set of # NVLinks

NIC Legend:

  NIC0: mlx5_0
  NIC1: mlx5_2
`)
	return f
}

// nvlinkPair lays out two NVLink GPUs and a ConnectX-8 behind one Gen6
// switch on a Gen5 root port, as an HGX-style baseboard looks from the host.
func nvlinkPair(t testing.TB, dir string) *fixture {
	f := newFixture(t, dir)
	f.numa(0, "0-63")
	rp := config(4, 512, 4096, false, 0)
	usp := config(5, 512, 4096, false, 0)
	dsp := config(6, 512, 4096, true, 0x00)
	ep := config(0, 512, 4096, false, 0)
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0"}, vendor: 0x8086, device: 0x1bbd, class: 0x060400, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen5, driver: "pcieport", config: rp})
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0", "0000:01:00.0"}, vendor: 0x15b3, device: 0x1976, class: 0x060400, numa: 0, width: 16, maxW: 16, speed: gen5, maxS: gen6, driver: "pcieport", config: usp})
	for _, b := range []string{"0000:02:00.0", "0000:02:01.0", "0000:02:02.0"} {
		f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0", "0000:01:00.0", b}, vendor: 0x15b3, device: 0x1976, class: 0x060400, numa: 0, width: 16, maxW: 16, speed: gen6, maxS: gen6, driver: "pcieport", config: dsp})
	}
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0", "0000:01:00.0", "0000:02:00.0", "0000:03:00.0"}, vendor: 0x10de, device: 0x2901, class: 0x030200, numa: 0, width: 16, maxW: 16, speed: gen6, maxS: gen6, driver: "nvidia", config: ep, aer: "RxErr 0\n"})
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0", "0000:01:00.0", "0000:02:01.0", "0000:04:00.0"}, vendor: 0x10de, device: 0x2901, class: 0x030200, numa: 0, width: 16, maxW: 16, speed: gen6, maxS: gen6, driver: "nvidia", config: ep, aer: "RxErr 0\n"})
	f.pci(pciSpec{host: "pci0000:00", chain: []string{"0000:00:01.0", "0000:01:00.0", "0000:02:02.0", "0000:05:00.0"}, vendor: 0x15b3, device: 0x1023, class: 0x020700, numa: 0, width: 16, maxW: 16, speed: gen6, maxS: gen6, driver: "mlx5_core", config: ep})
	f.rdma("mlx5_0", "0000:05:00.0", "800 Gb/sec (4X XDR)", "4: ACTIVE")
	links := func() string {
		var b strings.Builder
		for i := 0; i < 18; i++ {
			fmt.Fprintf(&b, "\t Link %d: 50.000 GB/s\n", i)
		}
		return b.String()
	}
	f.nvidia(
		"0, NVIDIA B300, GPU-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa, 00000000:03:00.0, 6, 6, 16, 16\n"+
			"1, NVIDIA B300, GPU-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb, 00000000:04:00.0, 6, 6, 16, 16\n",
		"GPU 0: NVIDIA B300 (UUID: GPU-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa)\n"+links()+
			"GPU 1: NVIDIA B300 (UUID: GPU-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb)\n"+strings.Replace(links(), "Link 17: 50.000 GB/s", "Link 17: <inactive>", 1),
		`	GPU0	GPU1	NIC0	CPU Affinity	NUMA Affinity	GPU NUMA ID
GPU0	 X 	NV18	PIX	0-63	0		N/A
GPU1	NV18	 X 	PIX	0-63	0		N/A
NIC0	PIX	PIX	 X 

Legend:

  X    = Self
  NV#  = Connection traversing a bonded set of # NVLinks

NIC Legend:

  NIC0: mlx5_0
`)
	return f
}
