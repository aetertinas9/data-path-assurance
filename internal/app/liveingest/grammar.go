package liveingest

import "strconv"

// Bounds of the wire contract.
const (
	// MaxAssets and the following are the payload count limits; a payload that
	// holds more is invalid as a whole (nothing is truncated).
	MaxAssets             = 4096
	MaxEdges              = 8192
	MaxEdgeEvidence       = 8192
	MaxObservations       = 4096
	MaxGPUBindings        = 256
	MaxAllocationEntries  = 4096
	MaxAllocationEvidence = 64
	MaxAliases            = 64
	MaxDimensions         = 64

	// MaxCanonicalBytes is the bound of the canonical encoding of a payload.
	MaxCanonicalBytes = 16 << 20
	// MaxBundleRevisionBytes is the bound of a bundle revision.
	MaxBundleRevisionBytes = 128

	maxIDBytes       = 128
	maxSourceBytes   = 256
	maxIdentBytes    = 512
	maxValueString   = 4096
	maxOptionalBytes = 256
)

// isIdentifier reports whether s is min..max bytes of 0x21-0x7E only.
func isIdentifier(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// isPrintable reports whether s is min..max bytes of 0x20-0x7E only.
func isPrintable(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// isNameChars reports whether s matches [A-Za-z0-9._-]{1,max}.
func isNameChars(s string, max int) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ValidIdentifier reports whether s is min..max bytes of identifier ASCII,
// that is bytes 0x21 through 0x7E only.
func ValidIdentifier(s string, min, max int) bool { return isIdentifier(s, min, max) }

// ValidClusterID reports whether s matches [A-Za-z0-9._-]{1,128}.
func ValidClusterID(s string) bool { return isNameChars(s, 128) }

// ValidNodeUID reports whether s matches [A-Za-z0-9._-]{1,128}, the form of a
// node UID in a certificate identity and in a hello.
func ValidNodeUID(s string) bool { return isNameChars(s, 128) }

// ValidNodeName reports whether s is 1..253 bytes of identifier ASCII.
func ValidNodeName(s string) bool { return isIdentifier(s, 1, 253) }

// ValidBootID reports whether s is 1..128 bytes of identifier ASCII.
func ValidBootID(s string) bool { return isIdentifier(s, 1, maxIDBytes) }

func isLowerHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// isGPUUUID reports whether s matches
// ^GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$.
func isGPUUUID(s string) bool {
	if len(s) != 40 || s[:4] != "GPU-" {
		return false
	}
	groups := [...]int{8, 4, 4, 4, 12}
	pos := 4
	for gi, n := range groups {
		for j := 0; j < n; j++ {
			if !isLowerHex(s[pos]) {
				return false
			}
			pos++
		}
		if gi < len(groups)-1 {
			if s[pos] != '-' {
				return false
			}
			pos++
		}
	}
	return pos == len(s)
}

// isCanonicalBDF reports whether s is a canonical BDF:
// ^([0-9a-f]{4}|[1-9a-f][0-9a-f]{4,7}):[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$ with
// device <= 0x1f.
func isCanonicalBDF(s string) bool {
	// The domain ends at the first colon; the remainder has a fixed shape.
	colon := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 4 || colon > 8 {
		return false
	}
	domain := s[:colon]
	for i := 0; i < len(domain); i++ {
		if !isLowerHex(domain[i]) {
			return false
		}
	}
	if len(domain) > 4 && domain[0] == '0' {
		return false
	}
	rest := s[colon:]
	if len(rest) != len(":bb:dd.f") {
		return false
	}
	if rest[0] != ':' || !isLowerHex(rest[1]) || !isLowerHex(rest[2]) || rest[3] != ':' {
		return false
	}
	if rest[4] != '0' && rest[4] != '1' {
		return false
	}
	if !isLowerHex(rest[5]) || rest[6] != '.' || rest[7] < '0' || rest[7] > '7' {
		return false
	}
	device, err := strconv.ParseUint(rest[4:6], 16, 8)
	return err == nil && device <= 0x1f
}
