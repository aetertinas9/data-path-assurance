package framecore

import (
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/evidence"
)

const (
	// WindowHorizon is how far back the cumulative evidence window keeps a
	// series, measured from its latest observation.
	WindowHorizon = 86400 * time.Second
	// WindowMaxSamples is the per-series capacity of the window: one
	// observation per frame at most.
	WindowMaxSamples = 16
)

// WindowConfig returns the evidence window configuration of a frame
// evaluation: observations older than freshness are stale, a series is kept
// for WindowHorizon and holds at most WindowMaxSamples observations.
func WindowConfig(freshness time.Duration) evidence.Config {
	return evidence.Config{MaxAge: freshness, Horizon: WindowHorizon, MaxSamples: WindowMaxSamples}
}
