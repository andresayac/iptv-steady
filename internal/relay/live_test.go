package relay_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"iptv-steady/internal/config"
	"iptv-steady/internal/events"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/testkit"
	"iptv-steady/internal/xtream"
)

func channel(sources ...relay.Source) relay.Media {
	return relay.Media{Kind: xtream.Live, TitleID: 5, Title: "A Channel", Sources: sources}
}

func watch(t *testing.T, s *stand, bytesWanted int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.front.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if _, err := io.ReadFull(response.Body, make([]byte, bytesWanted)); err != nil {
		t.Fatalf("the viewer lost the stream: %v", err)
	}
}

func TestLiveReconnectsAfterCutWithoutDroppingViewer(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.Inject(panel.AddChannel("7"), testkit.Fault{Kind: testkit.Cut, At: 20_000})
	s := newStand(t, nil, channel(source(0, "7", "ts")), panel)

	watch(t, s, 100_000)

	requireKinds(t, s.recorder, events.Dropped, events.Recovered)
}

func TestLiveReconnectsAfterSilence(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.Inject(panel.AddChannel("7"), testkit.Fault{Kind: testkit.Stall, At: 20_000})
	s := newStand(t, nil, channel(source(0, "7", "ts")), panel)

	watch(t, s, 100_000)

	requireKinds(t, s.recorder, events.Stalled, events.Recovered)
}

func TestLiveFallsThroughDeadAccountAtOpen(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	first.Always(first.AddChannel("7"), testkit.Fault{Kind: testkit.Status, Code: http.StatusNotFound})
	second.AddChannel("8")
	s := newStand(t, nil, channel(source(0, "7", "ts"), source(1, "8", "ts")), first, second)

	watch(t, s, 50_000)
}

func TestLiveSwitchesAccountWhenChannelDies(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	first.Always(first.AddChannel("7"), testkit.Fault{Kind: testkit.Cut, At: 20_000})
	secondPath := second.AddChannel("8")
	s := newStand(t, nil, channel(source(0, "7", "ts"), source(1, "8", "ts")), first, second)

	watch(t, s, 150_000)

	if len(second.Requests(secondPath)) == 0 {
		t.Fatal("the second account was never used")
	}
}

func TestLiveGivesUpOnChannelThatKeepsDying(t *testing.T) {
	panel := testkit.NewPanel(t)
	path := panel.AddChannel("7")
	panel.Always(path, testkit.Fault{Kind: testkit.Cut, At: 2_000})
	s := newStand(t, nil, channel(source(0, "7", "ts")), panel)

	got := s.fetch(http.MethodGet, "")

	if got.readErr == nil {
		t.Fatal("the viewer should see the stream end with an error")
	}
	if limit := s.cfg.Live.ReconnectBudget + 2*time.Second; got.elapsed > limit {
		t.Fatalf("kept trying for %s; the budget allows %s", got.elapsed, limit)
	}
	requireKinds(t, s.recorder, events.GaveUp)
	eventually(t, "the provider connection to close", func() bool { return panel.Active() == 0 })
}

func TestLiveBudgetRestartsAfterAHealthyConnection(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.Inject(panel.AddChannel("7"), testkit.Fault{Kind: testkit.Cut, At: 500_000})
	s := newStand(t, func(cfg *config.Config) { cfg.Live.HealthyAfter = 50 * time.Millisecond }, channel(source(0, "7", "ts")), panel)

	watch(t, s, 900_000)

	if slices.Contains(s.recorder.kinds(), events.GaveUp) {
		t.Fatal("a healthy channel must not exhaust the budget")
	}
}

func TestLiveLeavingReleasesProvider(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.AddChannel("7")
	s := newStand(t, nil, channel(source(0, "7", "ts")), panel)

	watch(t, s, 50_000)

	account := s.pool.Get(testkit.AccountName(0))
	eventually(t, "the provider connection to close", func() bool { return panel.Active() == 0 && account.Open() == 0 })
}
