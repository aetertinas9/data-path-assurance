package offline

import "time"

var (
	// minInstant and maxInstant bound every accepted timestamp: the protobuf
	// timestamp range without the zero instant.
	minInstant = time.Date(1, time.January, 1, 0, 0, 0, 1, time.UTC)
	maxInstant = time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	// evaluationMargin is the room every evaluation instant needs on both
	// sides so that freshness, ready and horizon arithmetic stays in range.
	evaluationMargin = 86400 * time.Second
)

// formatT renders t in the offline timestamp notation: UTC, RFC 3339 with
// nanoseconds, trailing fractional zeros removed, no fraction when zero.
func formatT(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTimestamp parses YYYY-MM-DDTHH:MM:SS[.f{1,9}](Z|+HH:MM|-HH:MM) with a
// valid calendar date and seconds 00-59, and returns the UTC instant. The
// instant must lie in [minInstant, maxInstant].
func parseTimestamp(s string) (time.Time, bool) {
	n := len(s)
	if n < 20 {
		return time.Time{}, false
	}
	num := func(from, width int) (int, bool) {
		v := 0
		for i := from; i < from+width; i++ {
			c := s[i]
			if c < '0' || c > '9' {
				return 0, false
			}
			v = v*10 + int(c-'0')
		}
		return v, true
	}
	year, ok1 := num(0, 4)
	month, ok2 := num(5, 2)
	day, ok3 := num(8, 2)
	hour, ok4 := num(11, 2)
	minute, ok5 := num(14, 2)
	second, ok6 := num(17, 2)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return time.Time{}, false
	}
	if s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' {
		return time.Time{}, false
	}
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) {
		return time.Time{}, false
	}
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	pos := 19
	nanos := 0
	if pos < n && s[pos] == '.' {
		pos++
		start := pos
		for pos < n && s[pos] >= '0' && s[pos] <= '9' {
			pos++
		}
		digits := pos - start
		if digits < 1 || digits > 9 {
			return time.Time{}, false
		}
		v, _ := num(start, digits)
		for i := digits; i < 9; i++ {
			v *= 10
		}
		nanos = v
	}
	if pos >= n {
		return time.Time{}, false
	}
	offset := 0
	switch s[pos] {
	case 'Z':
		pos++
	case '+', '-':
		if n-pos != 6 || s[pos+3] != ':' {
			return time.Time{}, false
		}
		oh, okh := num(pos+1, 2)
		om, okm := num(pos+4, 2)
		if !okh || !okm || oh > 23 || om > 59 {
			return time.Time{}, false
		}
		offset = oh*3600 + om*60
		if s[pos] == '-' {
			offset = -offset
		}
		pos += 6
	default:
		return time.Time{}, false
	}
	if pos != n {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.UTC)
	t = t.Add(-time.Duration(offset) * time.Second)
	if t.Before(minInstant) || t.After(maxInstant) {
		return time.Time{}, false
	}
	return t, true
}

// parseTNotation parses a timestamp that must already be in the offline
// timestamp notation (formatT of itself).
func parseTNotation(s string) (time.Time, bool) {
	t, ok := parseTimestamp(s)
	if !ok || formatT(t) != s {
		return time.Time{}, false
	}
	return t, true
}

// hasEvaluationMargin reports whether t - 86400s and t + 86400s both stay in
// [minInstant, maxInstant].
func hasEvaluationMargin(t time.Time) bool {
	return !t.Add(-evaluationMargin).Before(minInstant) && !t.Add(evaluationMargin).After(maxInstant)
}

func daysIn(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	default:
		return 31
	}
}
