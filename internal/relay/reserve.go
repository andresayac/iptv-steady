package relay

import (
	"math"
	"time"

	"iptv-steady/internal/config"
)

// reserveGauge learns how fast the viewer drains the buffer, so buffered bytes can be read as seconds of video.
type reserveGauge struct {
	cfg    config.Reserve
	window time.Duration

	started  time.Time
	queued   int64
	received int64

	pace     float64
	measured bool
}

// restart opens a new measuring window, so the gap of a reconnection never counts as a provider falling behind.
func (g *reserveGauge) restart(now time.Time, buffered int64) {
	g.started, g.queued, g.received = now, buffered, 0
}

// observe closes a window once it is long enough and reports whether the provider supplied too little with few seconds left.
func (g *reserveGauge) observe(now time.Time, received int, buffered int64) bool {
	g.received += int64(received)
	elapsed := now.Sub(g.started)
	if elapsed < g.window {
		return false
	}
	consumed := float64(max(g.received-(buffered-g.queued), 0))
	g.pace, g.measured = consumed/elapsed.Seconds(), true
	behind := float64(g.received) < g.cfg.MinSupply*consumed && g.seconds(buffered) < g.cfg.Low.Seconds()
	g.restart(now, buffered)
	return behind
}

// seconds assumes nothing is buffered until the viewer's pace is known, and an endless reserve while the viewer is not consuming.
func (g *reserveGauge) seconds(buffered int64) float64 {
	switch {
	case !g.measured || buffered == 0:
		return 0
	case g.pace <= 0:
		return math.Inf(1)
	}
	return float64(buffered) / g.pace
}

func (g *reserveGauge) stallTimeout(buffered int64, normal time.Duration) time.Duration {
	switch seconds := g.seconds(buffered); {
	case seconds < g.cfg.Empty.Seconds():
		return g.cfg.EmptyStallTimeout
	case seconds >= g.cfg.Full.Seconds():
		return g.cfg.FullStallTimeout
	}
	return normal
}
