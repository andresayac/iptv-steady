package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/catalog"
	"iptv-steady/internal/config"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/ttlcache"
	"iptv-steady/internal/xtream"
)

type seriesEntry struct {
	body     []byte
	episodes map[int64][]relay.Source
}

// Details serves title information on demand and merges the seasons each account holds.
type Details struct {
	cfg     config.Details
	catalog *catalog.Manager
	pool    *accounts.Pool
	movies  *ttlcache.Cache[[]byte]
	shows   *ttlcache.Cache[*seriesEntry]
}

func newDetails(cfg config.Details, manager *catalog.Manager, pool *accounts.Pool) *Details {
	return &Details{
		cfg: cfg, catalog: manager, pool: pool,
		movies: ttlcache.New[[]byte](cfg.CacheEntries),
		shows:  ttlcache.New[*seriesEntry](cfg.CacheEntries),
	}
}

func cacheKey(id int64) string { return strconv.FormatInt(id, 10) }

func setIfEmpty(target map[string]any, key, value string) {
	if value == "" {
		return
	}
	if current, found := target[key]; !found || current == nil || current == "" {
		target[key] = value
	}
}

func fillInfo(info map[string]any, title *catalog.Title, coverKey string) {
	info["name"] = title.BareName()
	setIfEmpty(info, "title", title.BareName())
	setIfEmpty(info, coverKey, title.Icon)
	setIfEmpty(info, "plot", title.Plot)
	setIfEmpty(info, "cast", title.Cast)
	setIfEmpty(info, "director", title.Director)
	setIfEmpty(info, "genre", title.Genre)
	setIfEmpty(info, "rating", title.Rating)
	setIfEmpty(info, "youtube_trailer", title.Trailer)
	setIfEmpty(info, "releasedate", title.ReleaseDate)
	setIfEmpty(info, "releaseDate", title.ReleaseDate)
}

func text(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	}
	return ""
}

func number(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	}
	return 0
}

func (d *Details) movie(ctx context.Context, title *catalog.Title) []byte {
	key := cacheKey(title.ID)
	if cached, found := d.movies.Get(key); found {
		return cached
	}
	info := d.fetchMovieInfo(ctx, title)
	fillInfo(info, title, "movie_image")
	body, _ := json.Marshal(map[string]any{
		"info": info,
		"movie_data": map[string]any{
			"stream_id": title.ID, "name": title.Name, "title": title.BareName(), "added": title.Added,
			"category_id": strconv.FormatInt(title.Categories[0], 10), "category_ids": title.Categories,
			"container_extension": title.PreferredExtension(), "custom_sid": "", "direct_source": "",
		},
	})
	d.movies.Set(key, body, d.cfg.VODTTL)
	return body
}

// fetchMovieInfo prefers a copy that has a synopsis and keeps the first answer as a last resort.
func (d *Details) fetchMovieInfo(ctx context.Context, title *catalog.Title) map[string]any {
	info := map[string]any{}
	for _, version := range title.Copies {
		var answer struct {
			Info json.RawMessage `json:"info"`
		}
		attempt, cancel := context.WithTimeout(ctx, d.cfg.VODTimeout)
		err := d.pool.Get(version.Account).Client.Get(attempt, "get_vod_info", url.Values{"vod_id": {version.ID}}, &answer)
		cancel()
		if err != nil {
			slog.Debug("movie details failed", "error", err)
			continue
		}
		var parsed map[string]any
		if json.Unmarshal(answer.Info, &parsed) != nil || len(parsed) == 0 {
			continue
		}
		if len(info) == 0 {
			info = parsed
		}
		if text(parsed["plot"]) != "" {
			return parsed
		}
	}
	return info
}

type parsedEpisode struct {
	season int
	number int
	data   map[string]any
}

// parseEpisodes reads both shapes panels use: an object keyed by season or a list of seasons.
func parseEpisodes(value any) []parsedEpisode {
	var out []parsedEpisode
	add := func(fallbackSeason int, list []any) {
		for i, item := range list {
			episode, ok := item.(map[string]any)
			if !ok {
				continue
			}
			season, position := number(episode["season"]), number(episode["episode_num"])
			if season == 0 {
				season = fallbackSeason
			}
			if position == 0 {
				position = i + 1
			}
			out = append(out, parsedEpisode{season, position, episode})
		}
	}
	switch seasons := value.(type) {
	case map[string]any:
		for key, entry := range seasons {
			if list, ok := entry.([]any); ok {
				parsed, _ := strconv.Atoi(key)
				add(parsed, list)
			}
		}
	case []any:
		for i, entry := range seasons {
			if list, ok := entry.([]any); ok {
				add(i+1, list)
			}
		}
		if len(out) == 0 {
			add(1, seasons)
		}
	}
	return out
}

type episodeSlot struct {
	season  int
	number  int
	data    map[string]any
	sources []relay.Source
}

func (d *Details) fetchSeries(ctx context.Context, title *catalog.Title) []map[string]any {
	answers := make([]map[string]any, len(title.Copies))
	var group sync.WaitGroup
	for i, version := range title.Copies {
		group.Go(func() {
			attempt, cancel := context.WithTimeout(ctx, d.cfg.SeriesTimeout)
			defer cancel()
			var answer map[string]any
			if err := d.pool.Get(version.Account).Client.Get(attempt, "get_series_info", url.Values{"series_id": {version.ID}}, &answer); err != nil {
				slog.Debug("series details failed", "error", err)
				return
			}
			answers[i] = answer
		})
	}
	group.Wait()
	return answers
}

func (d *Details) series(ctx context.Context, title *catalog.Title) (*seriesEntry, error) {
	key := cacheKey(title.ID)
	if cached, found := d.shows.Get(key); found {
		return cached, nil
	}
	answers := d.fetchSeries(ctx, title)
	if !slices.ContainsFunc(answers, func(a map[string]any) bool { return a != nil }) {
		return nil, fmt.Errorf("no account answered for series %d", title.ID)
	}
	slots := map[[2]int]*episodeSlot{}
	info := map[string]any{}
	seasonInfo := map[int]map[string]any{}
	for i, answer := range answers {
		if answer == nil {
			continue
		}
		if parsed, ok := answer["info"].(map[string]any); ok && len(info) == 0 {
			info = parsed
		}
		collectSeasons(answer["seasons"], seasonInfo)
		for _, episode := range parseEpisodes(answer["episodes"]) {
			slotKey := [2]int{episode.season, episode.number}
			if slots[slotKey] == nil {
				slots[slotKey] = &episodeSlot{season: episode.season, number: episode.number, data: episode.data}
			}
			source := relay.Source{Account: title.Copies[i].Account, ID: text(episode.data["id"]), Extension: text(episode.data["container_extension"])}
			slots[slotKey].sources = append(slots[slotKey].sources, source)
		}
	}
	entry, err := d.assemble(title, slots, info, seasonInfo)
	if err != nil {
		return nil, err
	}
	d.shows.Set(key, entry, d.cfg.SeriesTTL)
	return entry, nil
}

func collectSeasons(value any, into map[int]map[string]any) {
	list, _ := value.([]any)
	for _, item := range list {
		if season, ok := item.(map[string]any); ok {
			if n := number(season["season_number"]); into[n] == nil {
				into[n] = season
			}
		}
	}
}

func (d *Details) assemble(title *catalog.Title, slots map[[2]int]*episodeSlot, info map[string]any, seasonInfo map[int]map[string]any) (*seriesEntry, error) {
	entry := &seriesEntry{episodes: map[int64][]relay.Source{}}
	bySeason := map[int][]*episodeSlot{}
	for _, slot := range slots {
		bySeason[slot.season] = append(bySeason[slot.season], slot)
	}
	episodes := map[string][]map[string]any{}
	seasonNumbers := make([]int, 0, len(bySeason))
	for season, list := range bySeason {
		seasonNumbers = append(seasonNumbers, season)
		slices.SortFunc(list, func(x, y *episodeSlot) int { return cmp.Compare(x.number, y.number) })
		rows := make([]map[string]any, 0, len(list))
		for _, slot := range list {
			id := d.catalog.IDs().AssignEpisode(title.ID, slot.season, slot.number)
			entry.episodes[id] = slot.sources
			slot.data["id"] = strconv.FormatInt(id, 10)
			slot.data["season"] = slot.season
			slot.data["episode_num"] = slot.number
			slot.data["direct_source"] = ""
			rows = append(rows, slot.data)
		}
		episodes[strconv.Itoa(season)] = rows
	}
	slices.Sort(seasonNumbers)
	seasons := make([]map[string]any, 0, len(seasonNumbers))
	for _, n := range seasonNumbers {
		season := seasonInfo[n]
		if season == nil {
			season = map[string]any{"season_number": n, "name": fmt.Sprintf("Season %d", n), "cover": title.Icon}
		}
		season["episode_count"] = len(bySeason[n])
		seasons = append(seasons, season)
	}
	fillInfo(info, title, "cover")
	var err error
	entry.body, err = json.Marshal(map[string]any{"seasons": seasons, "info": info, "episodes": episodes})
	return entry, err
}

func (d *Details) episodeMedia(ctx context.Context, episode int64) (relay.Media, bool) {
	seriesID, found := d.catalog.IDs().EpisodeSeries(episode)
	if !found {
		return relay.Media{}, false
	}
	title := d.catalog.Find(xtream.Series, seriesID)
	if title == nil {
		return relay.Media{}, false
	}
	entry, err := d.series(ctx, title)
	if err != nil || len(entry.episodes[episode]) == 0 {
		return relay.Media{}, false
	}
	return relay.Media{Kind: xtream.Series, TitleID: episode, Title: title.Name, Sources: entry.episodes[episode]}, true
}
