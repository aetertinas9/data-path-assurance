package mtls

import "errors"

// ErrServerName reports a server name that is not a DNS name.
var ErrServerName = errors.New("mtls: the server name is not a valid DNS name")

// ValidateServerName reports whether host may be the TLS server name of a
// connection: a DNS name of 1 to 253 bytes matching
// ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$ whose last
// label is not all digits. An IP address literal (IPv4, or IPv6 in brackets)
// is therefore never accepted: the server certificate is checked against a DNS
// name, not an address. The check reads no file and touches no network.
func ValidateServerName(host string) error {
	if !isDNSName(host) {
		return ErrServerName
	}
	last := host
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == '.' {
			last = host[i+1:]
			break
		}
	}
	allDigits := true
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return ErrServerName
	}
	return nil
}

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
