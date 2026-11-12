package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/catalog"
	"iptv-steady/internal/config"
	"iptv-steady/internal/events"
	"iptv-steady/internal/httpapi"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/testkit"
)

type recorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recorder) Record(event events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) has(kind events.Kind) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.events, func(e events.Event) bool { return e.Kind == kind })
}

type stand struct {
	t        *testing.T
	cfg      *config.Config
	pool     *accounts.Pool
	manager  *catalog.Manager
	recorder *recorder
	front    *httptest.Server
}

func newStand(t *testing.T, panels ...*testkit.Panel) *stand {
	t.Helper()
	cfg := testkit.Config(t, panels...)
	return standFor(t, cfg)
}

func standFor(t *testing.T, cfg *config.Config) *stand {
	t.Helper()
	pool := accounts.NewPool(cfg)
	manager, err := catalog.NewManager(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	log := &recorder{}
	engine := relay.NewEngine(cfg.File, cfg.Live, pool, log)
	front := httptest.NewServer(httpapi.New(cfg, manager, pool, engine).Handler())
	t.Cleanup(front.Close)
	return &stand{t: t, cfg: cfg, pool: pool, manager: manager, recorder: log, front: front}
}

func (s *stand) sync() {
	s.t.Helper()
	if err := s.manager.Sync(context.Background()); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stand) api(action string, extra ...string) string {
	values := url.Values{"username": {testkit.ViewerName}, "password": {testkit.ViewerPassword}}
	if action != "" {
		values.Set("action", action)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		values.Set(extra[i], extra[i+1])
	}
	return s.front.URL + "/player_api.php?" + values.Encode()
}

func (s *stand) get(address string) (int, http.Header, []byte) {
	s.t.Helper()
	response, err := http.Get(address)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return response.StatusCode, response.Header, body
}

func (s *stand) rows(action string, extra ...string) []map[string]any {
	s.t.Helper()
	_, _, body := s.get(s.api(action, extra...))
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		s.t.Fatalf("%s: %v in %s", action, err, body)
	}
	return rows
}

func (s *stand) streamURL(segment string, id any, extension string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%v.%s", s.front.URL, segment, testkit.ViewerName, testkit.ViewerPassword, id, extension)
}

func id(value any) string {
	return fmt.Sprintf("%.0f", value.(float64))
}

func libraryPanel(t *testing.T, movieID string, content []byte) (*testkit.Panel, string) {
	t.Helper()
	panel := testkit.NewPanel(t)
	panel.SetList("get_vod_categories", `[{"category_id":"1","category_name":"Drama"},{"category_id":"2","category_name":"Adult Stuff"}]`)
	panel.SetList("get_vod_streams", fmt.Sprintf(`[
		{"name":"Up (2009)","stream_id":"%s","category_id":"1","container_extension":"mp4","plot":"long synopsis","added":"100"},
		{"name":"Hidden (2001)","stream_id":"99","category_id":"2","container_extension":"mp4"}]`, movieID))
	return panel, panel.AddMovie(movieID, "mp4", content)
}

func TestLoginHidesProviderAndRejectsBadPasswords(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)

	_, _, login := s.get(s.api(""))
	if strings.Contains(string(login), testkit.Password) || strings.Contains(string(login), panel.URL()) {
		t.Fatalf("login leaks provider details: %s", login)
	}
	if !strings.Contains(string(login), `"auth":1`) {
		t.Fatalf("login = %s", login)
	}
	_, _, rejected := s.get(s.front.URL + "/player_api.php?username=viewer&password=wrong")
	if !strings.Contains(string(rejected), `"auth":0`) {
		t.Fatalf("bad login = %s", rejected)
	}
}

func TestListsAreCompressedAndConditional(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)
	s.sync()
	address := s.api("get_vod_streams")
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	request, _ := http.NewRequest(http.MethodGet, address, nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	compressed, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers = %v", response.Header)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}

	request.Header.Set("If-None-Match", response.Header.Get("ETag"))
	conditional, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	conditional.Body.Close()
	if conditional.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d", conditional.StatusCode)
	}
}

func TestListsApplyFiltersAndCategories(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)
	s.sync()

	rows := s.rows("get_vod_streams")
	if len(rows) != 1 || rows[0]["name"] != "Up (2009)" {
		t.Fatalf("rows = %v", rows)
	}
	if _, found := rows[0]["plot"]; found {
		t.Fatal("slim lists should not include the plot")
	}
	categories := s.rows("get_vod_categories")
	if len(categories) != 1 {
		t.Fatalf("categories = %v", categories)
	}
	if got := s.rows("get_vod_streams", "category_id", categories[0]["category_id"].(string)); len(got) != 1 {
		t.Fatalf("category rows = %v", got)
	}
	if got := s.rows("get_vod_streams", "category_id", "12345"); len(got) != 0 {
		t.Fatalf("unknown category rows = %v", got)
	}
}

func TestMovieSurvivesProviderSilenceThroughTheAPI(t *testing.T) {
	content := testkit.Content(1 << 21)
	panel, path := libraryPanel(t, "10", content)
	panel.Inject(path, testkit.Fault{Kind: testkit.Stall, At: 1 << 20})
	s := newStand(t, panel)
	s.sync()
	movie := s.rows("get_vod_streams")[0]

	status, _, body := s.get(s.streamURL("movie", id(movie["stream_id"]), "mp4"))

	if status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("status %d, %d bytes", status, len(body))
	}
	if !s.recorder.has(events.Recovered) {
		t.Fatal("the rescue was not recorded")
	}
}

func TestDeadLinkMovesToTheSameTitleOnAnotherAccount(t *testing.T) {
	content := testkit.Content(1 << 18)
	first, path := libraryPanel(t, "10", content)
	first.Always(path, testkit.Fault{Kind: testkit.Status, Code: http.StatusNotFound})
	second, _ := libraryPanel(t, "20", content)
	s := newStand(t, first, second)
	s.sync()
	rows := s.rows("get_vod_streams")
	if len(rows) != 1 {
		t.Fatalf("the two copies should merge: %v", rows)
	}

	status, _, body := s.get(s.streamURL("movie", id(rows[0]["stream_id"]), "mp4"))

	if status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("status %d, %d bytes", status, len(body))
	}
}

func TestBrowsersMayReadListsAndPiecesOfFiles(t *testing.T) {
	content := testkit.Content(1 << 16)
	panel, _ := libraryPanel(t, "10", content)
	s := newStand(t, panel)
	s.sync()

	_, header, _ := s.get(s.api("get_vod_streams"))
	if header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("lists: allow origin = %q", header.Get("Access-Control-Allow-Origin"))
	}

	movie := s.streamURL("movie", id(s.rows("get_vod_streams")[0]["stream_id"]), "mp4")
	preflight, err := http.NewRequest(http.MethodOptions, movie, nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "http://player.example")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflight.Header.Set("Access-Control-Request-Headers", "range")
	answer, err := http.DefaultClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	answer.Body.Close()
	if answer.StatusCode != http.StatusNoContent || !strings.Contains(answer.Header.Get("Access-Control-Allow-Headers"), "Range") {
		t.Fatalf("preflight: %d, allow headers %q", answer.StatusCode, answer.Header.Get("Access-Control-Allow-Headers"))
	}

	piece, err := http.NewRequest(http.MethodGet, movie, nil)
	if err != nil {
		t.Fatal(err)
	}
	piece.Header.Set("Range", "bytes=100-199")
	response, err := http.DefaultClient.Do(piece)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusPartialContent || !bytes.Equal(body, content[100:200]) {
		t.Fatalf("piece: %d, %d bytes", response.StatusCode, len(body))
	}
	if !strings.Contains(response.Header.Get("Access-Control-Expose-Headers"), "Content-Range") {
		t.Fatalf("piece: expose headers %q", response.Header.Get("Access-Control-Expose-Headers"))
	}
}

func TestStreamsRequireValidCredentialsAndKnownTitles(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)
	s.sync()
	movie := id(s.rows("get_vod_streams")[0]["stream_id"])

	if status, _, _ := s.get(fmt.Sprintf("%s/movie/%s/wrong/%s.mp4", s.front.URL, testkit.ViewerName, movie)); status != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d", status)
	}
	if status, _, _ := s.get(s.streamURL("movie", 1, "mp4")); status != http.StatusNotFound {
		t.Fatalf("unknown title status = %d", status)
	}
}

func TestMovieDetailsUseProxyIdentifiers(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)
	s.sync()
	movie := id(s.rows("get_vod_streams")[0]["stream_id"])

	_, _, body := s.get(s.api("get_vod_info", "vod_id", movie))

	var details struct {
		Info      map[string]any `json:"info"`
		MovieData map[string]any `json:"movie_data"`
	}
	if err := json.Unmarshal(body, &details); err != nil {
		t.Fatal(err)
	}
	if details.Info["plot"] != "a plot" || id(details.MovieData["stream_id"]) != movie {
		t.Fatalf("details = %s", body)
	}
}

func seriesPanel(t *testing.T, seriesID string, info string) *testkit.Panel {
	t.Helper()
	panel := testkit.NewPanel(t)
	panel.SetList("get_series_categories", `[{"category_id":"1","category_name":"Series Drama"}]`)
	panel.SetList("get_series", fmt.Sprintf(`[{"name":"Dark (2017)","series_id":"%s","category_id":"1"}]`, seriesID))
	panel.SetSeries(seriesID, info)
	return panel
}

func TestSeriesMergesSeasonsAndEpisodesFailOver(t *testing.T) {
	content := testkit.Content(1 << 16)
	first := seriesPanel(t, "5", `{"seasons":[],"info":{"plot":"p"},"episodes":{"1":[{"id":"100","episode_num":"1","season":1,"container_extension":"mp4","direct_source":"http://leak"}]}}`)
	second := seriesPanel(t, "9", `{"seasons":[],"info":{},"episodes":[[{"id":"200","episode_num":1,"season":1,"container_extension":"mkv"}],[{"id":"201","episode_num":1,"season":2,"container_extension":"mkv"}]]}`)
	dead := first.AddEpisode("100", "mp4", content)
	first.Always(dead, testkit.Fault{Kind: testkit.Status, Code: http.StatusNotFound})
	second.AddEpisode("200", "mkv", content)
	second.AddEpisode("201", "mkv", content)
	s := newStand(t, first, second)
	s.sync()
	series := s.rows("get_series")
	if len(series) != 1 {
		t.Fatalf("series = %v", series)
	}

	_, _, body := s.get(s.api("get_series_info", "series_id", id(series[0]["series_id"])))

	if strings.Contains(string(body), "http://leak") {
		t.Fatalf("provider address leaked: %s", body)
	}
	var details struct {
		Episodes map[string][]map[string]any `json:"episodes"`
	}
	if err := json.Unmarshal(body, &details); err != nil {
		t.Fatal(err)
	}
	if len(details.Episodes["1"]) != 1 || len(details.Episodes["2"]) != 1 {
		t.Fatalf("episodes = %v", details.Episodes)
	}
	for _, season := range []string{"1", "2"} {
		status, _, played := s.get(s.streamURL("series", details.Episodes[season][0]["id"], "mp4"))
		if status != http.StatusOK || !bytes.Equal(played, content) {
			t.Fatalf("season %s: status %d, %d bytes", season, status, len(played))
		}
	}
}

func TestProviderOutageKeepsItsLastGoodCatalog(t *testing.T) {
	first, _ := libraryPanel(t, "10", testkit.Content(1024))
	second := testkit.NewPanel(t)
	second.SetList("get_vod_categories", `[{"category_id":"1","category_name":"Drama"}]`)
	second.SetList("get_vod_streams", `[{"name":"Coco (2017)","stream_id":"70","category_id":"1","container_extension":"mp4"}]`)
	s := newStand(t, first, second)
	s.sync()
	second.SetBroken(true)

	if err := s.manager.Sync(context.Background()); err == nil {
		t.Fatal("the outage should be reported")
	}
	if rows := s.rows("get_vod_streams"); len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}

	restarted := standFor(t, s.cfg)
	if !restarted.manager.Restore() {
		t.Fatal("a restart should restore the catalog from disk")
	}
	if rows := restarted.rows("get_vod_streams"); len(rows) != 2 {
		t.Fatalf("rows after restart = %v", rows)
	}
}

func TestEmptyAnswerNeverReplacesACatalog(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)
	s.sync()
	panel.SetList("get_vod_streams", `[]`)

	if err := s.manager.Sync(context.Background()); err == nil {
		t.Fatal("an empty catalog should be reported")
	}
	if rows := s.rows("get_vod_streams"); len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
}

func TestPlaylistPointsToTheProxy(t *testing.T) {
	panel, _ := libraryPanel(t, "10", testkit.Content(1024))
	s := newStand(t, panel)
	s.sync()
	address := fmt.Sprintf("%s/get.php?username=%s&password=%s&type=m3u_plus", s.front.URL, testkit.ViewerName, testkit.ViewerPassword)

	status, _, body := s.get(address)

	text := string(body)
	if status != http.StatusOK || !strings.HasPrefix(text, "#EXTM3U") || !strings.Contains(text, s.front.URL+"/movie/") {
		t.Fatalf("status %d: %s", status, text)
	}
	if strings.Contains(text, testkit.Password) || strings.Contains(text, panel.URL()) {
		t.Fatalf("playlist leaks provider details: %s", text)
	}
}

func TestLiveChannelsPlayThroughTheAPI(t *testing.T) {
	panel := testkit.NewPanel(t)
	panel.SetList("get_live_categories", `[{"category_id":"1","category_name":"News"}]`)
	panel.SetList("get_live_streams", `[{"name":"News One","stream_id":"7","category_id":"1"}]`)
	panel.AddChannel("7")
	s := newStand(t, panel)
	s.sync()
	channel := id(s.rows("get_live_streams")[0]["stream_id"])

	response, err := http.Get(s.streamURL("live", channel, "ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadFull(response.Body, make([]byte, 20_000)); err != nil {
		t.Fatal(err)
	}
}
