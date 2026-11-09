package relay

import (
	"testing"
	"time"

	"iptv-steady/internal/config"
)

const megabyte = 1_000_000

func newGauge() *reserveGauge {
	return &reserveGauge{
		window: 10 * time.Second,
		cfg: config.Reserve{
			Empty: 3 * time.Second, Low: 30 * time.Second, Full: 60 * time.Second,
			EmptyStallTimeout: 3 * time.Second, FullStallTimeout: 15 * time.Second, MinSupply: 0.8,
		},
	}
}

func TestStallTimeoutFollowsTheReserve(t *testing.T) {
	const normal = 5 * time.Second
	cases := map[string]struct {
		pace     float64
		measured bool
		buffered int64
		want     time.Duration
	}{
		"pace still unknown":          {0, false, 40 * megabyte, 3 * time.Second},
		"nothing buffered":            {750_000, true, 0, 3 * time.Second},
		"almost empty":                {750_000, true, 1 * megabyte, 3 * time.Second},
		"some video left":             {750_000, true, 20 * megabyte, normal},
		"over a minute left":          {750_000, true, 60 * megabyte, 15 * time.Second},
		"high bitrate":                {3_000_000, true, 60 * megabyte, normal},
		"viewer is consuming nothing": {0, true, 10 * megabyte, 15 * time.Second},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			gauge := newGauge()
			gauge.pace, gauge.measured = c.pace, c.measured
			if got := gauge.stallTimeout(c.buffered, normal); got != c.want {
				t.Fatalf("timeout = %s; want %s", got, c.want)
			}
		})
	}
}

// The figures replay a real night: the provider supplied 0.27 MB/s to a viewer consuming 0.75 MB/s.
func TestProviderFallingBehindIsFlaggedOnceFewSecondsRemain(t *testing.T) {
	gauge := newGauge()
	now := time.Date(2026, 11, 9, 3, 14, 59, 0, time.UTC)
	buffered := int64(43_400_000)
	gauge.restart(now, buffered)
	flaggedAfter := time.Duration(0)
	for step := 1; step <= 9 && flaggedAfter == 0; step++ {
		buffered -= 4_800_000
		if gauge.observe(now.Add(time.Duration(step)*10*time.Second), 2_700_000, buffered) {
			flaggedAfter = time.Duration(step) * 10 * time.Second
		}
	}
	if flaggedAfter != 50*time.Second {
		t.Fatalf("flagged after %s; want 50s, with about 26 seconds of video left", flaggedAfter)
	}
}

func TestSteadySupplyIsNotFlagged(t *testing.T) {
	gauge := newGauge()
	now := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	gauge.restart(now, 5*megabyte)
	if gauge.observe(now.Add(10*time.Second), 7_400_000, 5*megabyte-100_000) {
		t.Fatal("a provider keeping up with the viewer was flagged")
	}
}

func TestFallingBehindWithPlentyLeftIsNotFlagged(t *testing.T) {
	gauge := newGauge()
	now := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	gauge.restart(now, 60*megabyte)
	if gauge.observe(now.Add(10*time.Second), 2_700_000, 60*megabyte-4_800_000) {
		t.Fatal("the reserve still holds over a minute of video")
	}
}

func TestPaceIsKeptAcrossReconnections(t *testing.T) {
	gauge := newGauge()
	now := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	gauge.restart(now, 10*megabyte)
	gauge.observe(now.Add(10*time.Second), 7_500_000, 10*megabyte)
	gauge.restart(now.Add(13*time.Second), 8*megabyte)
	if got := gauge.seconds(7_500_000); got != 10 {
		t.Fatalf("seconds = %v; want 10", got)
	}
}
