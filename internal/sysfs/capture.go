package sysfs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Capture copies the read set of this package from root into the directory
// out, so that Read(out) sees what Read(root) saw: the PCI function
// directories with their attributes and config space, the bus/pci/devices
// links, the NUMA node CPU lists, and the RDMA and network class entries. It
// copies nothing else, and it never writes into root.
func Capture(root *os.Root, out string) error {
	h := Read(root)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for i := range h.Devices {
		d := &h.Devices[i]
		dir := filepath.Join(out, filepath.FromSlash(d.Dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, attr := range PCIAttributes {
			copyFile(root, d.Dir+"/"+attr, filepath.Join(dir, attr), MaxAttrBytes)
		}
		copyFile(root, d.Dir+"/config", filepath.Join(dir, "config"), MaxConfigBytes)
		for _, link := range PCILinks {
			if base, ok := readLinkBase(root, d.Dir+"/"+link); ok {
				_ = os.Symlink(path.Join("..", base), filepath.Join(dir, link))
			}
		}
		// bus/pci/devices/<bdf> -> ../../../devices/...
		busDir := filepath.Join(out, "bus", "pci", "devices")
		if err := os.MkdirAll(busDir, 0o755); err != nil {
			return err
		}
		rel := path.Join("..", "..", "..", d.Dir)
		_ = os.Remove(filepath.Join(busDir, d.BDF))
		if err := os.Symlink(filepath.FromSlash(rel), filepath.Join(busDir, d.BDF)); err != nil {
			return err
		}
	}
	for _, n := range h.NUMA {
		dir := filepath.Join(out, "devices", "system", "node", fmt.Sprintf("node%d", n.ID))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "cpulist"), []byte(n.CPUList+"\n"), 0o644); err != nil {
			return err
		}
	}
	for _, rd := range h.RDMA {
		dir := filepath.Join(out, "class", "infiniband", rd.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(dir, "device"))
		if err := os.Symlink(filepath.FromSlash(path.Join("..", "..", "..", "bus", "pci", "devices", rd.BDF)), filepath.Join(dir, "device")); err != nil {
			return err
		}
		for _, p := range rd.Ports {
			pd := filepath.Join(dir, "ports", fmt.Sprint(p.Number))
			if err := os.MkdirAll(pd, 0o755); err != nil {
				return err
			}
			src := fmt.Sprintf("class/infiniband/%s/ports/%d", rd.Name, p.Number)
			for _, attr := range RDMAAttributes {
				copyFile(root, src+"/"+attr, filepath.Join(pd, attr), MaxAttrBytes)
			}
		}
	}
	for _, nd := range h.Net {
		dir := filepath.Join(out, "class", "net", nd.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(dir, "device"))
		if err := os.Symlink(filepath.FromSlash(path.Join("..", "..", "..", "bus", "pci", "devices", nd.BDF)), filepath.Join(dir, "device")); err != nil {
			return err
		}
		for _, attr := range NetAttributes {
			copyFile(root, "class/net/"+nd.Name+"/"+attr, filepath.Join(dir, attr), MaxAttrBytes)
		}
	}
	return nil
}

// copyFile copies a bounded regular file; a file that cannot be read is
// simply absent in the capture, which Read reports the same way.
func copyFile(root *os.Root, src, dst string, limit int) {
	data, st := readFile(root, src, limit)
	if st != OK {
		return
	}
	if len(data) > limit {
		data = data[:limit]
	}
	if !strings.HasSuffix(dst, "config") && len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	_ = os.WriteFile(dst, data, 0o644)
}
