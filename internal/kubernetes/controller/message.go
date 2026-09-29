package controller

import (
	"strings"
	"unicode/utf8"
)

const maxMessageLength = 1024

// message is a condition message under the GKA-074 composition rule: a type
// prefix followed by tokens, each preceded by "; ", in the fixed order head
// tokens, "peer=<names>", then tail tokens.
type message struct {
	prefix string
	head   []string // tokens before peer= (qualified_not_eligible=<n>)
	peers  []string // names for the peer= token (empty: no token)
	tail   []string // internal error, no_observation, truncated, gate_not_implemented
}

func (m message) String() string {
	var b strings.Builder
	b.WriteString(m.prefix)
	for _, t := range m.head {
		if t != "" {
			b.WriteString("; " + t)
		}
	}
	fixed := 0
	for _, t := range m.tail {
		if t != "" {
			fixed += len("; ") + len(t)
		}
	}
	if len(m.peers) > 0 {
		budget := maxMessageLength - b.Len() - fixed - len("; peer=")
		b.WriteString("; peer=" + nameList(m.peers, budget))
	}
	for _, t := range m.tail {
		if t != "" {
			b.WriteString("; " + t)
		}
	}
	return b.String()
}

func internalErrorToken(class string) string {
	if class == "" {
		return ""
	}
	return "internal error: " + class
}

func truncatedToken(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	sorted := append([]string(nil), fields...)
	sortStrings(sorted)
	return "truncated:" + strings.Join(sorted, ",")
}

// okString reports whether s is valid UTF-8 whose length in characters lies in
// [lo, hi] (the way a CRD minLength/maxLength counts).
func okString(s string, lo, hi int) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

// isASCII reports whether every byte of s is in 0x20-0x7E.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
