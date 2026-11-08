package relay_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/config"
	"iptv-steady/internal/events"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/testkit"
	"iptv-steady/internal/xtream"
)

const fileSize = 1 << 21

type memoryRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (m *memoryRecorder) Record(event events.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *memoryRecorder) kinds() []events.Kind {
	m.mu.Lock()
	defer m.mu.Unlock()
	kinds := make([]events.Kind, 0, len(m.events))
	for _, event := range m.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func (m *memoryRecorder) all() []events.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events)
}

type stand struct {
	t        *testing.T
	cfg      *config.Config
	pool     *accounts.Pool
	engine   *relay.Engine
	recorder *memoryRecorder
	front    *httptest.Server
	media    relay.Media
}

func newStand(t *testing.T, tune func(*config.Config), media relay.Media, panels ...*testkit.Panel) *stand {
	t.Helper()
	cfg := testkit.Config(t, panels...)
	if tune != nil {
		tune(cfg)
	}
	s := &stand{t: t, cfg: cfg, pool: accounts.NewPool(cfg), recorder: &memoryRecorder{}, media: media}
	s.engine = relay.NewEngine(cfg.File, cfg.Live, s.pool, s.recorder)
	viewer := relay.Viewer{User: "viewer", Player: "test-player"}
	s.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.media.Kind == xtream.Live {
			s.engine.ServeLive(w, r, viewer, s.media, "ts")
			return
		}
		s.engine.ServeFile(w, r, viewer, s.media)
	}))
	t.Cleanup(s.front.Close)
	return s
}

func source(index int, id, extension string) relay.Source {
	return relay.Source{Account: testkit.AccountName(index), ID: id, Extension: extension}
}

func movie(sources ...relay.Source) relay.Media {
	return relay.Media{Kind: xtream.VOD, TitleID: 1, Title: "A Movie", Sources: sources}
}

type result struct {
	status  int
	header  http.Header
	body    []byte
	readErr error
	elapsed time.Duration
}

func (s *stand) fetch(method, rangeHeader string) result {
	s.t.Helper()
	request, err := http.NewRequest(method, s.front.URL, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	started := time.Now()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	return result{response.StatusCode, response.Header, body, readErr, time.Since(started)}
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func requireIntact(t *testing.T, got result, want []byte) {
	t.Helper()
	if got.status != http.StatusOK && got.status != http.StatusPartialContent {
		t.Fatalf("status = %d", got.status)
	}
	if got.readErr != nil {
		t.Fatalf("read error: %v", got.readErr)
	}
	if !bytes.Equal(got.body, want) {
		t.Fatalf("received %d bytes that differ from the expected %d", len(got.body), len(want))
	}
}

func requireKinds(t *testing.T, recorder *memoryRecorder, want ...events.Kind) {
	t.Helper()
	for _, kind := range want {
		if !slices.Contains(recorder.kinds(), kind) {
			t.Fatalf("events = %v; missing %s", recorder.kinds(), kind)
		}
	}
}

func TestSilentProviderIsResumedAtExactByte(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	path := panel.AddMovie("10", "mp4", content)
	const stallAt = 1 << 20
	panel.Inject(path, testkit.Fault{Kind: testkit.Stall, At: stallAt})
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	got := s.fetch(http.MethodGet, "")

	requireIntact(t, got, content)
	requireKinds(t, s.recorder, events.Stalled, events.Recovered)
	requests := panel.Requests(path)
	want := fmt.Sprintf("bytes=%d-%d", stallAt, fileSize-1)
	if len(requests) != 2 || requests[1].Range != want {
		t.Fatalf("requests = %+v; want a resume with %s", requests, want)
	}
	if got.elapsed > 3*time.Second {
		t.Fatalf("recovery took %s", got.elapsed)
	}
}

func TestCutConnectionIsResumed(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	path := panel.AddMovie("10", "mp4", content)
	panel.Inject(path, testkit.Fault{Kind: testkit.Cut, At: 700_000}, testkit.Fault{Kind: testkit.Cut, At: 1_400_000})
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	requireIntact(t, s.fetch(http.MethodGet, ""), content)
	requireKinds(t, s.recorder, events.Dropped, events.Recovered)
}

func TestTrickleCountsAsStall(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	path := panel.AddMovie("10", "mp4", content)
	panel.Inject(path, testkit.Fault{Kind: testkit.Trickle, At: 300_000})
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	requireIntact(t, s.fetch(http.MethodGet, ""), content)
	requireKinds(t, s.recorder, events.Slow, events.Recovered)
}

func TestUnusableLinksFallThroughToNextAccount(t *testing.T) {
	cases := map[string]testkit.Fault{
		"not found":   {Kind: testkit.Status, Code: http.StatusNotFound},
		"empty body":  {Kind: testkit.EmptyBody},
		"forbidden":   {Kind: testkit.Status, Code: http.StatusForbidden},
		"overloaded":  {Kind: testkit.Status, Code: http.StatusServiceUnavailable},
		"silent open": {Kind: testkit.Stall, At: 0},
	}
	for name, fault := range cases {
		t.Run(name, func(t *testing.T) {
			first, second := testkit.NewPanel(t), testkit.NewPanel(t)
			content := testkit.Content(fileSize)
			first.Always(first.AddMovie("10", "mp4", content), fault)
			second.AddMovie("20", "mkv", content)
			s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mkv")), first, second)

			requireIntact(t, s.fetch(http.MethodGet, ""), content)
		})
	}
}

func TestBusyAccountIsTriedLast(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	firstPath := first.AddMovie("10", "mp4", content)
	second.AddMovie("20", "mp4", content)
	s := newStand(t, func(cfg *config.Config) { cfg.Accounts[0].MaxConnections = 1 }, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)
	release := s.pool.Get(testkit.AccountName(0)).Acquire()
	defer release()

	requireIntact(t, s.fetch(http.MethodGet, ""), content)
	if len(first.Requests(firstPath)) != 0 {
		t.Fatal("the account at its connection limit was used first")
	}
}

func TestFailingAccountIsAvoidedByBreaker(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	firstPath := first.AddMovie("10", "mp4", content)
	first.Always(firstPath, testkit.Fault{Kind: testkit.Status, Code: http.StatusInternalServerError})
	second.AddMovie("20", "mp4", content)
	s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)

	for attempt := range s.cfg.Upstream.BreakerFailures {
		s.media.TitleID = int64(100 + attempt)
		requireIntact(t, s.fetch(http.MethodGet, ""), content)
	}
	before := len(first.Requests(firstPath))
	s.media.TitleID = 200
	requireIntact(t, s.fetch(http.MethodGet, ""), content)
	if len(first.Requests(firstPath)) != before {
		t.Fatal("a tripped account was still tried first")
	}
}

func TestIdenticalCopyContinuesMidFile(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	const failAt = 1 << 20
	first.Always(first.AddMovie("10", "mp4", content), testkit.Fault{Kind: testkit.Stall, At: failAt})
	secondPath := second.AddMovie("20", "mp4", content)
	s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)

	got := s.fetch(http.MethodGet, "")

	requireIntact(t, got, content)
	requireKinds(t, s.recorder, events.SwitchedCopy, events.Recovered)
	if requests := second.Requests(secondPath); len(requests) != 1 || !strings.HasPrefix(requests[0].Range, fmt.Sprintf("bytes=%d-", failAt-int64(s.cfg.File.TailCheck))) {
		t.Fatalf("second account requests = %+v", requests)
	}
}

func TestCopyThatKeepsFailingIsLeftBehind(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	firstPath := first.AddMovie("10", "mp4", content)
	for i := range 6 {
		first.Inject(firstPath, testkit.Fault{Kind: testkit.Stall, At: int64(300_000 + i*8_000)})
	}
	second.AddMovie("20", "mp4", content)
	s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)

	requireIntact(t, s.fetch(http.MethodGet, ""), content)

	requireKinds(t, s.recorder, events.SwitchedCopy)
	if requests := len(first.Requests(firstPath)); requests > s.cfg.File.RetriesPerCopy+1 {
		t.Fatalf("the failing copy was tried %d times", requests)
	}
}

func TestDifferentFileIsNeverSpliced(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content, other := testkit.Content(fileSize), bytes.Repeat([]byte{0xAB}, fileSize)
	const failAt = 1 << 20
	first.Always(first.AddMovie("10", "mp4", content), testkit.Fault{Kind: testkit.Stall, At: failAt})
	second.AddMovie("20", "mp4", other)
	s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)

	got := s.fetch(http.MethodGet, "")

	if got.readErr == nil {
		t.Fatal("the stream should end with an error, not look complete")
	}
	if !bytes.Equal(got.body, content[:len(got.body)]) {
		t.Fatal("bytes from a different file reached the viewer")
	}
	if len(got.body) != failAt {
		t.Fatalf("delivered %d bytes; everything received before the failure should arrive", len(got.body))
	}
	requireKinds(t, s.recorder, events.GaveUp)
}

func TestGivesUpInsteadOfHanging(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	panel.Always(panel.AddMovie("10", "mp4", content), testkit.Fault{Kind: testkit.Stall, At: 500_000})
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	got := s.fetch(http.MethodGet, "")

	if got.readErr == nil {
		t.Fatal("the viewer should see a failure")
	}
	if limit := s.cfg.File.RecoveryBudget + 2*time.Second; got.elapsed > limit {
		t.Fatalf("hung for %s; the budget allows %s", got.elapsed, limit)
	}
	requireKinds(t, s.recorder, events.GaveUp)
	eventually(t, "the provider connection to close", func() bool { return panel.Active() == 0 })
}

func TestDeadProviderAtOpenAnswersBadGateway(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.Always(panel.AddMovie("10", "mp4", testkit.Content(1024)), testkit.Fault{Kind: testkit.Status, Code: http.StatusNotFound})
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	if got := s.fetch(http.MethodGet, ""); got.status != http.StatusBadGateway {
		t.Fatalf("status = %d", got.status)
	}
	requireKinds(t, s.recorder, events.DeadLink, events.GaveUp)
}

func TestLeavingReleasesProviderConnection(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.AddMovie("10", "mp4", testkit.Content(fileSize))
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.front.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadFull(response.Body, make([]byte, 16<<10)); err != nil {
		t.Fatal(err)
	}

	cancel()

	account := s.pool.Get(testkit.AccountName(0))
	eventually(t, "the provider connection to close", func() bool { return panel.Active() == 0 && account.Open() == 0 })
}

func TestPausedViewerReleasesProviderThenResumes(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(48 << 20)
	path := panel.AddMovie("10", "mp4", content)
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)
	response, err := http.Get(s.front.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 64<<10)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the provider connection to be released", func() bool { return panel.Active() == 0 })
	requireKinds(t, s.recorder, events.Parked)

	rest, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(first, rest...), content) {
		t.Fatal("the file was corrupted across the pause")
	}
	if len(panel.Requests(path)) < 2 {
		t.Fatal("the provider should have been reopened after the pause")
	}
}

func TestRepeatedSeeksServeExactBytesAndLeaveNothingOpen(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	panel.AddMovie("10", "mp4", content)
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	for _, start := range []int{0, 1_500_000, 200, 2_000_000, 900_000} {
		end := min(start+100_000, fileSize-1)
		got := s.fetch(http.MethodGet, fmt.Sprintf("bytes=%d-%d", start, end))
		if got.status != http.StatusPartialContent {
			t.Fatalf("status = %d", got.status)
		}
		want := fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize)
		if got.header.Get("Content-Range") != want {
			t.Fatalf("Content-Range = %q; want %q", got.header.Get("Content-Range"), want)
		}
		requireIntact(t, got, content[start:end+1])
	}
	eventually(t, "every provider connection to close", func() bool { return panel.Active() == 0 && s.pool.Get(testkit.AccountName(0)).Open() == 0 })
}

func TestSeekStaysOnTheFileAlreadyPlaying(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	firstPath := first.AddMovie("10", "mp4", content)
	second.AddMovie("20", "mp4", testkit.Content(fileSize/2))
	s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)
	requireIntact(t, s.fetch(http.MethodGet, "bytes=0-1000"), content[:1001])

	s.media.Sources = []relay.Source{source(1, "20", "mp4"), source(0, "10", "mp4")}
	requireIntact(t, s.fetch(http.MethodGet, "bytes=500000-501000"), content[500000:501001])

	if len(first.Requests(firstPath)) != 2 {
		t.Fatal("the seek moved to another file")
	}
}

func TestSeekNeverLandsOnAFileOfAnotherSize(t *testing.T) {
	first, second := testkit.NewPanel(t), testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	firstPath := first.AddMovie("10", "mp4", content)
	second.AddMovie("20", "mp4", testkit.Content(fileSize*2))
	s := newStand(t, nil, movie(source(0, "10", "mp4"), source(1, "20", "mp4")), first, second)
	requireIntact(t, s.fetch(http.MethodGet, "bytes=0-1000"), content[:1001])

	first.Always(firstPath, testkit.Fault{Kind: testkit.Status, Code: http.StatusNotFound})

	if got := s.fetch(http.MethodGet, "bytes=500000-501000"); got.status != http.StatusBadGateway {
		t.Fatalf("status = %d; a seek must not switch to a different file", got.status)
	}
}

func TestSuffixAndInvalidRanges(t *testing.T) {
	panel := testkit.NewPanel(t)
	content := testkit.Content(fileSize)
	panel.AddMovie("10", "mp4", content)
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	requireIntact(t, s.fetch(http.MethodGet, "bytes=-1000"), content[fileSize-1000:])
	if got := s.fetch(http.MethodGet, fmt.Sprintf("bytes=%d-", fileSize+10)); got.status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d", got.status)
	}
	requireIntact(t, s.fetch(http.MethodGet, "bytes=abc"), content)
}

func TestHeadAnswersWithoutHoldingTheProvider(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.AddMovie("10", "mp4", testkit.Content(fileSize))
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)

	got := s.fetch(http.MethodHead, "")

	if got.status != http.StatusOK || got.header.Get("Content-Length") != fmt.Sprint(fileSize) || len(got.body) != 0 {
		t.Fatalf("HEAD = %d, length %q, %d body bytes", got.status, got.header.Get("Content-Length"), len(got.body))
	}
	eventually(t, "the provider connection to close", func() bool { return panel.Active() == 0 })
}

func TestEventsNeverContainCredentials(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.AddMovie("10", "mp4", testkit.Content(1024))
	s := newStand(t, nil, movie(source(0, "10", "mp4")), panel)
	panel.Close()

	if got := s.fetch(http.MethodGet, ""); got.status != http.StatusBadGateway {
		t.Fatalf("status = %d", got.status)
	}
	for _, event := range s.recorder.all() {
		if strings.Contains(event.Detail, testkit.Password) || strings.Contains(event.Detail, testkit.Username) {
			t.Fatalf("credentials leaked in event: %+v", event)
		}
	}
}
