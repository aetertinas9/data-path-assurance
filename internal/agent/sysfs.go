package agent

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// Paths and bounds of the sysfs collector, relative to a sysfs root.
const (
	devicesDir       = "bus/pci/devices"
	maxDeviceNames   = 8192
	maxLinkTarget    = 4096
	maxAttrBytes     = 4096
	maxAncestors     = 16
	nvidiaVendorID   = 0x10de
	classBaseDisplay = 0x03
	classBaseNetwork = 0x02
	classBaseBridge  = 0x06
	classSubPCI      = 0x04
)

// Diagnostic codes (GFO-051).
const (
	codeListFailed            = "sysfs_list_failed"
	codeEntryNameInvalid      = "sysfs_entry_name_invalid"
	codeLinkInvalid           = "sysfs_link_invalid"
	codeLinkEscape            = "sysfs_link_escape"
	codeAncestorMissing       = "sysfs_ancestor_missing"
	codeAncestorNotBridge     = "sysfs_ancestor_not_bridge"
	codeLayoutUnsupported     = "sysfs_layout_unsupported"
	codeAttrMissing           = "sysfs_attr_missing"
	codeAttrPermission        = "sysfs_attr_permission"
	codeAttrUnreadable        = "sysfs_attr_unreadable"
	codeAttrMalformed         = "sysfs_attr_malformed"
	codeAttrNoData            = "sysfs_attr_nodata"
	codeTopologyContradiction = "topology_contradiction"
	codeWidthOmitted          = "width_omitted"
	codeBaselinePeerMismatch  = "baseline_peer_mismatch"
	codeBaselineUnmatched     = "baseline_unmatched"
	codeNVIDIAOutputAbsent    = "nvidia_output_absent"
	codeNVIDIAOutputUnread    = "nvidia_output_unreadable"
	codeNVIDIAOutputLimit     = "nvidia_output_limit"
	codeNVIDIAEmpty           = "nvidia_empty"
	codeNVIDIAMalformed       = "nvidia_malformed"
	codeNVIDIADuplicate       = "nvidia_duplicate"
	codeNVIDIABDFNotGPU       = "nvidia_bdf_not_gpu"
	codeNVIDIAGPUUnlisted     = "nvidia_gpu_unlisted"
)

// sysfsEntry is one processed entry: a canonically named device whose link
// resolved to a path R inside the sysfs root (GFO-042).
type sysfsEntry struct {
	name  string
	rel   string   // R, relative to the sysfs root
	comps []string // R split at "/"
	// host is the index of the host bridge component; valid when layoutOK.
	host     int
	layoutOK bool
	classOK  bool
	class    uint32
	bridge   bool
	gpu      bool
	nic      bool
}

// belowHost returns the canonical addresses below the host bridge: the
// entry's ancestors followed by the entry itself. Valid only when layoutOK.
func (e *sysfsEntry) belowHost() []string {
	return e.comps[e.host+1:]
}

// sysfsFrame is what one sysfs root yields before topology is derived.
type sysfsFrame struct {
	listed   map[string]bool        // canonical names present in the listing
	entries  map[string]*sysfsEntry // processed entries by name
	selected []string               // selected functions, bytes ascending
	partial  bool
}

// frameDiagnostics collects the deduplicated diagnostics of one frame.
type frameDiagnostics map[diagnostic]struct{}

// diagnostic is one (code, subject) pair; subject is already escaped.
type diagnostic struct {
	code, subject string
}

// add records a diagnostic, escaping every byte outside 0x21..0x7e and '%'.
func (d frameDiagnostics) add(code, subject string) {
	d[diagnostic{code: code, subject: escapeSubject(subject)}] = struct{}{}
}

// escapeSubject writes bytes outside 0x21..0x7e, and '%', as %XX.
func escapeSubject(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '%' {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// sysfsCollector reads one sysfs root under the closed read set of GFO-044.
type sysfsCollector struct {
	root  *os.Root
	diags frameDiagnostics
	frame *sysfsFrame
}

// fail records a diagnostic that makes the frame PARTIAL.
func (c *sysfsCollector) fail(code, subject string) {
	c.diags.add(code, subject)
	c.frame.partial = true
}

// collectSysfs lists and classifies the devices of one sysfs root. The only
// error it returns is a device count beyond its bound (ErrBoundExceeded).
func collectSysfs(root *os.Root, diags frameDiagnostics) (*sysfsFrame, error) {
	c := &sysfsCollector{
		root:  root,
		diags: diags,
		frame: &sysfsFrame{listed: map[string]bool{}, entries: map[string]*sysfsEntry{}},
	}
	names, ok := listDirNames(root, devicesDir, maxDeviceNames)
	if len(names) > maxDeviceNames {
		return nil, fmt.Errorf("%w: %s holds more than %d names", ErrBoundExceeded, devicesDir, maxDeviceNames)
	}
	if !ok {
		c.fail(codeListFailed, devicesDir)
		return c.frame, nil
	}
	slices.Sort(names)
	for _, name := range names {
		if !isCanonicalBDF(name) {
			c.fail(codeEntryNameInvalid, devicesDir+"/"+name)
			continue
		}
		c.frame.listed[name] = true
		rel, comps, code := resolveEntryLink(root, name)
		if code != "" {
			c.fail(code, name)
			continue
		}
		e := &sysfsEntry{name: name, rel: rel, comps: comps}
		e.host, e.layoutOK = supportedLayout(comps)
		c.classify(e)
		c.frame.entries[name] = e
		if e.gpu || e.nic {
			c.frame.selected = append(c.frame.selected, name)
		}
	}
	return c.frame, nil
}

// failedSysfsFrame is the frame of a sysfs root that could not be opened: the
// device list is unavailable.
func failedSysfsFrame(diags frameDiagnostics) *sysfsFrame {
	diags.add(codeListFailed, devicesDir)
	return &sysfsFrame{listed: map[string]bool{}, entries: map[string]*sysfsEntry{}, partial: true}
}

// resolveEntryLink resolves bus/pci/devices/<name> to R (GFO-042). It returns
// a diagnostic code instead when the entry is not a usable link. Nothing
// outside the root is opened or stat'ed: the target is checked lexically first
// and each component of R is then lstat'ed in order, so no symlink is ever
// followed.
func resolveEntryLink(root *os.Root, name string) (string, []string, string) {
	entry := devicesDir + "/" + name
	info, err := root.Lstat(entry)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return "", nil, codeLinkInvalid
	}
	target, err := root.Readlink(entry)
	if err != nil || target == "" || len(target) > maxLinkTarget {
		return "", nil, codeLinkInvalid
	}
	if strings.HasPrefix(target, "/") {
		return "", nil, codeLinkEscape
	}
	rel := path.Clean(devicesDir + "/" + target)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", nil, codeLinkEscape
	}
	comps := strings.Split(rel, "/")
	if rel == "." || comps[len(comps)-1] != name {
		return "", nil, codeLinkInvalid
	}
	for i := range comps {
		info, err := root.Lstat(strings.Join(comps[:i+1], "/"))
		if err != nil || !info.IsDir() {
			return "", nil, codeLinkInvalid
		}
	}
	return rel, comps, ""
}

// supportedLayout reports whether R is devices/<platform prefix>/<host
// bridge>/<1 to 17 distinct canonical addresses> (GFO-043), and where the host
// bridge is. The last component is the entry name, which the caller has
// already checked.
func supportedLayout(comps []string) (int, bool) {
	if len(comps) < 3 || comps[0] != "devices" {
		return 0, false
	}
	host := -1
	for i := 1; i < len(comps); i++ {
		if isHostBridge(comps[i]) {
			if host >= 0 {
				return 0, false
			}
			host = i
		}
	}
	if host < 0 {
		return 0, false
	}
	for i := 1; i < host; i++ {
		if isCanonicalBDF(comps[i]) {
			return 0, false
		}
	}
	tail := comps[host+1:]
	if len(tail) < 1 || len(tail) > maxAncestors+1 {
		return 0, false
	}
	seen := make(map[string]struct{}, len(tail))
	for _, c := range tail {
		if !isCanonicalBDF(c) {
			return 0, false
		}
		if _, dup := seen[c]; dup {
			return 0, false
		}
		seen[c] = struct{}{}
	}
	return host, true
}

// classify reads class, and vendor and physfn where they matter, and decides
// whether the entry is a bridge, a GPU or a NIC (GFO-046).
func (c *sysfsCollector) classify(e *sysfsEntry) {
	classPath := e.rel + "/class"
	raw, status := readAttr(c.root, classPath)
	if status == readOK {
		if v, ok := parseHexAttr(raw, 6); ok {
			e.class, e.classOK = v, true
		} else {
			status = readMalformed
		}
	}
	if !e.classOK {
		c.fail(status.attrCode(), classPath)
		return
	}
	base, sub := e.class>>16, (e.class>>8)&0xff
	e.bridge = base == classBaseBridge && sub == classSubPCI
	if base != classBaseDisplay && base != classBaseNetwork {
		return
	}

	vendorOK := false
	var vendor uint32
	if base == classBaseDisplay {
		vendorPath := e.rel + "/vendor"
		raw, status := readAttr(c.root, vendorPath)
		if status == readOK {
			if v, ok := parseHexAttr(raw, 4); ok {
				vendor, vendorOK = v, true
			} else {
				status = readMalformed
			}
		}
		if !vendorOK {
			c.fail(status.attrCode(), vendorPath)
		}
	}

	// physfn is lstat'ed only: its presence marks a virtual function.
	physfnPath := e.rel + "/physfn"
	notVF := false
	if _, err := c.root.Lstat(physfnPath); err != nil {
		switch status := statusOf(err); status {
		case readMissing:
			notVF = true
		default:
			c.fail(status.attrCode(), physfnPath)
		}
	}
	e.gpu = base == classBaseDisplay && vendorOK && vendor == nvidiaVendorID && notVF
	e.nic = base == classBaseNetwork && notVF
}

// readAttr reads one attribute: a regular file of at most 4096 bytes with
// surrounding SP, HT, CR and LF trimmed and no NUL byte.
func readAttr(root *os.Root, name string) ([]byte, readStatus) {
	data, status := readRegularFile(root, name, maxAttrBytes)
	if status != readOK {
		return nil, status
	}
	if len(data) > maxAttrBytes {
		return nil, readMalformed
	}
	data = bytes.Trim(data, " \t\r\n")
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, readMalformed
	}
	return data, readOK
}

// parseHexAttr parses "0x" followed by exactly digits hex digits of either
// case.
func parseHexAttr(raw []byte, digits int) (uint32, bool) {
	if len(raw) != 2+digits || raw[0] != '0' || raw[1] != 'x' {
		return 0, false
	}
	var v uint32
	for _, ch := range raw[2:] {
		var d byte
		switch {
		case ch >= '0' && ch <= '9':
			d = ch - '0'
		case ch >= 'a' && ch <= 'f':
			d = ch - 'a' + 10
		case ch >= 'A' && ch <= 'F':
			d = ch - 'A' + 10
		default:
			return 0, false
		}
		v = v<<4 | uint32(d)
	}
	return v, true
}

// parseWidth parses a link width attribute: an unsigned decimal whose value is
// a PCIe width. Empty, "Unknown" and a zero value are nodata; anything else is
// malformed.
func parseWidth(raw []byte) (int64, readStatus) {
	if len(raw) == 0 || string(raw) == "Unknown" {
		return 0, readNoData
	}
	var v uint64
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, readMalformed
		}
		// Saturate: any value above the largest width is malformed anyway.
		if v <= 1<<20 {
			v = v*10 + uint64(ch-'0')
		}
	}
	if v == 0 {
		return 0, readNoData
	}
	if _, ok := validWidths[v]; !ok {
		return 0, readMalformed
	}
	return int64(v), readOK
}

// readWidth reads and parses one link width attribute.
func readWidth(root *os.Root, name string) (int64, readStatus) {
	raw, status := readAttr(root, name)
	if status != readOK {
		return 0, status
	}
	return parseWidth(raw)
}
