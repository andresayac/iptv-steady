package catalog

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"

	"iptv-steady/internal/xtream"
)

const defaultMovieExtension = "mp4"

// Payload is a ready-to-send, compressed JSON document.
type Payload struct {
	Compressed []byte
	ETag       string
}

func encodePayload(value any) Payload {
	var buffer bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	_ = json.NewEncoder(writer).Encode(value)
	_ = writer.Close()
	hash := fnv.New64a()
	hash.Write(buffer.Bytes())
	return Payload{Compressed: buffer.Bytes(), ETag: fmt.Sprintf(`"%x"`, hash.Sum64())}
}

type vodRow struct {
	Num         int     `json:"num"`
	Name        string  `json:"name"`
	StreamType  string  `json:"stream_type"`
	StreamID    int64   `json:"stream_id"`
	StreamIcon  string  `json:"stream_icon"`
	Rating      string  `json:"rating"`
	Rating5     float64 `json:"rating_5based"`
	Added       string  `json:"added"`
	CategoryID  string  `json:"category_id"`
	CategoryIDs []int64 `json:"category_ids"`
	Extension   string  `json:"container_extension"`
	CustomSID   string  `json:"custom_sid"`
	Direct      string  `json:"direct_source"`
	Title       string  `json:"title,omitempty"`
	Year        string  `json:"year,omitempty"`
	Plot        string  `json:"plot,omitempty"`
	Cast        string  `json:"cast,omitempty"`
	Director    string  `json:"director,omitempty"`
	Genre       string  `json:"genre,omitempty"`
	ReleaseDate string  `json:"release_date,omitempty"`
	Trailer     string  `json:"youtube_trailer,omitempty"`
	Runtime     string  `json:"episode_run_time,omitempty"`
}

type seriesRow struct {
	Num         int      `json:"num"`
	Name        string   `json:"name"`
	StreamType  string   `json:"stream_type"`
	SeriesID    int64    `json:"series_id"`
	Cover       string   `json:"cover"`
	Modified    string   `json:"last_modified"`
	Rating      string   `json:"rating"`
	Rating5     float64  `json:"rating_5based"`
	CategoryID  string   `json:"category_id"`
	CategoryIDs []int64  `json:"category_ids"`
	Title       string   `json:"title,omitempty"`
	Year        string   `json:"year,omitempty"`
	Plot        string   `json:"plot,omitempty"`
	Cast        string   `json:"cast,omitempty"`
	Director    string   `json:"director,omitempty"`
	Genre       string   `json:"genre,omitempty"`
	ReleaseDate string   `json:"releaseDate,omitempty"`
	Backdrop    []string `json:"backdrop_path,omitempty"`
	Trailer     string   `json:"youtube_trailer,omitempty"`
	Runtime     string   `json:"episode_run_time,omitempty"`
}

type liveRow struct {
	Num         int     `json:"num"`
	Name        string  `json:"name"`
	StreamType  string  `json:"stream_type"`
	StreamID    int64   `json:"stream_id"`
	StreamIcon  string  `json:"stream_icon"`
	EPG         string  `json:"epg_channel_id"`
	Added       string  `json:"added"`
	CategoryID  string  `json:"category_id"`
	CategoryIDs []int64 `json:"category_ids"`
	CustomSID   string  `json:"custom_sid"`
	TVArchive   int     `json:"tv_archive"`
	Direct      string  `json:"direct_source"`
	ArchiveDays int     `json:"tv_archive_duration"`
}

type categoryRow struct {
	ID     string `json:"category_id"`
	Name   string `json:"category_name"`
	Parent int    `json:"parent_id"`
}

func yearText(year int) string {
	if year == 0 {
		return ""
	}
	return strconv.Itoa(year)
}

func (s *Section) row(number int, t *Title, category int64) any {
	categoryID := strconv.FormatInt(category, 10)
	rating5, _ := strconv.ParseFloat(t.Rating5, 64)
	switch s.Kind {
	case xtream.Series:
		out := seriesRow{Num: number, Name: t.Name, StreamType: "series", SeriesID: t.ID, Cover: t.Icon, Modified: t.Modified, Rating: t.Rating, Rating5: rating5, CategoryID: categoryID, CategoryIDs: t.Categories}
		if !s.slim {
			out.Title, out.Year, out.Plot, out.Cast, out.Director, out.Genre, out.ReleaseDate, out.Backdrop, out.Trailer, out.Runtime = t.BareName(), yearText(t.Year), t.Plot, t.Cast, t.Director, t.Genre, t.ReleaseDate, t.Backdrop, t.Trailer, t.Runtime
		}
		return out
	case xtream.Live:
		return liveRow{Num: number, Name: t.Name, StreamType: "live", StreamID: t.ID, StreamIcon: t.Icon, EPG: t.EPG, Added: t.Added, CategoryID: categoryID, CategoryIDs: t.Categories}
	}
	out := vodRow{Num: number, Name: t.Name, StreamType: "movie", StreamID: t.ID, StreamIcon: t.Icon, Rating: t.Rating, Rating5: rating5, Added: t.Added, CategoryID: categoryID, CategoryIDs: t.Categories, Extension: t.PreferredExtension()}
	if !s.slim {
		out.Title, out.Year, out.Plot, out.Cast, out.Director, out.Genre, out.ReleaseDate, out.Trailer, out.Runtime = t.BareName(), yearText(t.Year), t.Plot, t.Cast, t.Director, t.Genre, t.ReleaseDate, t.Trailer, t.Runtime
	}
	return out
}

func (t *Title) PreferredExtension() string {
	if len(t.Copies) > 0 && t.Copies[0].Extension != "" {
		return t.Copies[0].Extension
	}
	return defaultMovieExtension
}

func (s *Section) prepare() {
	s.index = make(map[int64]*Title, len(s.Titles))
	rows := make([]any, 0, len(s.Titles))
	for i, t := range s.Titles {
		s.index[t.ID] = t
		rows = append(rows, s.row(i+1, t, t.Categories[0]))
	}
	s.list = encodePayload(rows)
	categories := make([]categoryRow, 0, len(s.Categories))
	for _, c := range s.Categories {
		categories = append(categories, categoryRow{ID: strconv.FormatInt(c.ID, 10), Name: c.Name})
	}
	s.categories = encodePayload(categories)
}

func (s *Section) List() Payload { return s.list }

func (s *Section) CategoryList() Payload { return s.categories }

// ListCategory returns the rows of one category, built once per catalog generation.
func (s *Section) ListCategory(category int64) Payload {
	if cached, found := s.byCategory.Load(category); found {
		return cached.(Payload)
	}
	rows := []any{}
	for _, t := range s.Titles {
		if slices.Contains(t.Categories, category) {
			rows = append(rows, s.row(len(rows)+1, t, category))
		}
	}
	payload := encodePayload(rows)
	s.byCategory.Store(category, payload)
	return payload
}
