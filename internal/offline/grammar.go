package offline

import "strconv"

// isASCII reports whether s is min..max bytes of 0x21-0x7E only.
func isASCII(s string, min, max int) bool {
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

// isClusterID reports whether s matches [A-Za-z0-9._-]{1,128}.
func isClusterID(s string) bool {
	return isNameChars(s, 128)
}

// isCoverageName reports whether s matches [A-Za-z0-9._-]{1,253}.
func isCoverageName(s string) bool {
	return isNameChars(s, 253)
}

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

// isDNSName reports whether s is 1-253 bytes matching
// ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$.
func isDNSName(s string) bool {
	if len(s) < 1 || len(s) > 253 {
		return false
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			continue
		}
		if !isDNSLabel(s[start:i]) {
			return false
		}
		start = i + 1
	}
	return true
}

func isDNSLabel(l string) bool {
	if l == "" {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && c != '-' {
			return false
		}
		if c == '-' && (i == 0 || i == len(l)-1) {
			return false
		}
	}
	return true
}

func isLowerHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// isDigestHex reports whether s is exactly 64 lowercase hex digits.
func isDigestHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isLowerHex(s[i]) {
			return false
		}
	}
	return true
}

// isGPUUUID reports whether s matches
// ^GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$.
func isGPUUUID(s string) bool {
	if len(s) != 40 || s[:4] != "GPU-" {
		return false
	}
	groups := []int{8, 4, 4, 4, 12}
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
