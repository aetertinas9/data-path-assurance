package liveingest

import "time"

// bucket is a token bucket counted in time instead of tokens: credit is the
// number of tokens times the refill interval, so fractional tokens and the
// boundary "at least one token" are exact integer arithmetic. A new bucket is
// full. The owner serializes access (node.bmu).
type bucket struct {
	credit time.Duration
	last   time.Time
	primed bool
}

// take refills the bucket to now and consumes one token if there is one. The
// capacity is burst tokens and one token is refilled per interval. Elapsed
// time is counted from the previous call, and a clock that went backwards
// counts as no time passed.
func (b *bucket) take(now time.Time, burst int, interval time.Duration) bool {
	capacity := time.Duration(burst) * interval
	if !b.primed {
		b.credit = capacity
		b.primed = true
	} else if elapsed := now.Sub(b.last); elapsed > 0 {
		if elapsed >= capacity || b.credit+elapsed > capacity {
			b.credit = capacity
		} else {
			b.credit += elapsed
		}
	}
	b.last = now
	if b.credit < interval {
		return false
	}
	b.credit -= interval
	return true
}
