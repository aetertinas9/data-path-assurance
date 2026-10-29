package sysfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSpeed(t *testing.T) {
	for in, want := range map[string]int{"32.0 GT/s PCIe": 320, "2.5 GT/s": 25, "64.0 GT/s PCIe": 640} {
		if got, st := ParseSpeed(in); st != OK || got != want {
			t.Errorf("%q -> %d %s", in, got, st)
		}
	}
	if _, st := ParseSpeed("Unknown speed"); st != NoData {
		t.Errorf("Unknown -> %s", st)
	}
	if _, st := ParseSpeed("fast"); st != Malformed {
		t.Errorf("fast -> %s", st)
	}
}

func TestParseConfig(t *testing.T) {
	img := make([]byte, 4096)
	img[0x06] = 0x10
	img[0x34] = 0x40
	img[0x40], img[0x42], img[0x43] = 0x10, 0x62, 0x00 // PCIe cap, downstream port (6)
	img[0x48], img[0x49] = 0x20, 0x20                  // MaxPayload 256, MaxReadReq 512
	img[0x100], img[0x101], img[0x102] = 0x0d, 0x00, 0x01
	img[0x104], img[0x106] = 0x7f, 0x1d
	c := ParseConfig(img)
	if !c.HasPCIe || c.PCIeType != 6 || c.MaxPayload != 256 || c.MaxReadReq != 512 {
		t.Errorf("pcie %+v", *c)
	}
	if !c.HasACS || c.ACSControl != 0x1d || !c.ExtendedRead {
		t.Errorf("acs %+v", *c)
	}
	short := ParseConfig(img[:64])
	if short.HasPCIe || short.ExtendedRead {
		t.Errorf("64-byte read must know nothing: %+v", *short)
	}
}

func TestReadProblemsAndContainment(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("devices/pci0000:00/0000:00:01.0/vendor", "0x8086\n")
	mk("devices/pci0000:00/0000:00:01.0/device", "0x1234\n")
	mk("devices/pci0000:00/0000:00:01.0/class", "0x060400\n")
	mk("devices/pci0000:00/0000:00:01.0/current_link_width", "Unknown\n")
	_ = os.MkdirAll(filepath.Join(dir, "bus/pci/devices"), 0o755)
	if err := os.Symlink("../../../devices/pci0000:00/0000:00:01.0", filepath.Join(dir, "bus/pci/devices/0000:00:01.0")); err != nil {
		t.Fatal(err)
	}
	// A link that escapes the root and a name that is not an address.
	_ = os.Symlink("/etc", filepath.Join(dir, "bus/pci/devices/0000:00:02.0"))
	_ = os.Symlink("../../../devices/pci0000:00/0000:00:01.0", filepath.Join(dir, "bus/pci/devices/junk"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h := Read(root)
	if len(h.Devices) != 1 {
		t.Fatalf("devices %+v", h.Devices)
	}
	d := h.Devices[0]
	if !d.IsBridge() || d.Link.WidthStatus != NoData || d.Link.SpeedStatus != Missing || d.NUMAStatus != Missing || d.NUMA != -1 {
		t.Errorf("%+v", d)
	}
	if len(h.Problems) < 2 {
		t.Errorf("problems %v", h.Problems)
	}
}
