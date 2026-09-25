package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// canonicalBDFPattern is the canonical PCI address: a lowercase domain of four
// hex digits, or five to eight without a leading zero; a two-digit bus; a
// device from 00 to 1f; and a function from 0 to 7.
var canonicalBDFPattern = regexp.MustCompile(`^([0-9a-f]{4}|[1-9a-f][0-9a-f]{4,7}):[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$`)

// hostBridgePattern is a host bridge directory name in a device path.
var hostBridgePattern = regexp.MustCompile(`^pci([0-9a-f]{4}|[1-9a-f][0-9a-f]{4,7}):[0-9a-f]{2}$`)

// busIDPattern is the pci.bus_id column of the NVIDIA query: a four or eight
// digit domain in either case, then bus, device and function.
var busIDPattern = regexp.MustCompile(`^([0-9A-Fa-f]{4}(?:[0-9A-Fa-f]{4})?):([0-9A-Fa-f]{2}):([0-9A-Fa-f]{2})\.([0-7])$`)

// isCanonicalBDF reports whether s is a canonical PCI address.
func isCanonicalBDF(s string) bool {
	return canonicalBDFPattern.MatchString(s)
}

// isHostBridge reports whether s names a host bridge directory.
func isHostBridge(s string) bool {
	return hostBridgePattern.MatchString(s)
}

// canonicalBusID converts an NVIDIA pci.bus_id value to the canonical PCI
// address by value: 00000000:3B:00.0 and 0000:3b:00.0 both become
// 0000:3b:00.0, and 00010000:01:00.0 becomes 10000:01:00.0. It reports false
// for a value outside the grammar or with a device above 0x1f.
func canonicalBusID(s string) (string, bool) {
	m := busIDPattern.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	domain, err := strconv.ParseUint(m[1], 16, 32)
	if err != nil {
		return "", false
	}
	device, err := strconv.ParseUint(m[3], 16, 8)
	if err != nil || device > 0x1f {
		return "", false
	}
	var b strings.Builder
	d := strconv.FormatUint(domain, 16)
	for i := len(d); i < 4; i++ {
		b.WriteByte('0')
	}
	b.WriteString(d)
	b.WriteByte(':')
	b.WriteString(strings.ToLower(m[2]))
	b.WriteByte(':')
	b.WriteString(strings.ToLower(m[3]))
	b.WriteByte('.')
	b.WriteString(m[4])
	return b.String(), true
}
