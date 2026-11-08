package xtream

import (
	"bytes"
	"encoding/json"
	"time"
)

type Kind string

const (
	VOD    Kind = "vod"
	Series Kind = "series"
	Live   Kind = "live"
)

var Kinds = []Kind{VOD, Series, Live}

// Text accepts the strings, numbers and nulls that different panels send for the same field.
type Text string

func (t *Text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || b[0] == 'n' || b[0] == '[' || b[0] == '{':
		*t = ""
		return nil
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = Text(s)
		return nil
	}
	*t = Text(b)
	return nil
}

func (t Text) String() string { return string(t) }

// TextList accepts arrays of mixed scalars and treats anything else as empty.
type TextList []string

func (l *TextList) UnmarshalJSON(b []byte) error {
	*l = nil
	if b = bytes.TrimSpace(b); len(b) == 0 || b[0] != '[' {
		return nil
	}
	var items []Text
	if err := json.Unmarshal(b, &items); err != nil {
		return nil
	}
	for _, item := range items {
		if item != "" {
			*l = append(*l, string(item))
		}
	}
	return nil
}

type Category struct {
	ID   Text `json:"category_id"`
	Name Text `json:"category_name"`
}

type Stream struct {
	Name        Text     `json:"name"`
	Year        Text     `json:"year,omitempty"`
	StreamID    Text     `json:"stream_id,omitempty"`
	SeriesID    Text     `json:"series_id,omitempty"`
	Icon        Text     `json:"stream_icon,omitempty"`
	Cover       Text     `json:"cover,omitempty"`
	Rating      Text     `json:"rating,omitempty"`
	Rating5     Text     `json:"rating_5based,omitempty"`
	Added       Text     `json:"added,omitempty"`
	Modified    Text     `json:"last_modified,omitempty"`
	Plot        Text     `json:"plot,omitempty"`
	Cast        Text     `json:"cast,omitempty"`
	Director    Text     `json:"director,omitempty"`
	Genre       Text     `json:"genre,omitempty"`
	ReleaseDate Text     `json:"release_date,omitempty"`
	ReleaseAlt  Text     `json:"releaseDate,omitempty"`
	Trailer     Text     `json:"youtube_trailer,omitempty"`
	TrailerAlt  Text     `json:"trailer,omitempty"`
	Runtime     Text     `json:"episode_run_time,omitempty"`
	CategoryID  Text     `json:"category_id,omitempty"`
	CategoryIDs TextList `json:"category_ids,omitempty"`
	Extension   Text     `json:"container_extension,omitempty"`
	EPG         Text     `json:"epg_channel_id,omitempty"`
	IsAdult     Text     `json:"is_adult,omitempty"`
	Backdrop    TextList `json:"backdrop_path,omitempty"`
}

func (s *Stream) Identifier(kind Kind) string {
	if kind == Series {
		return string(s.SeriesID)
	}
	return string(s.StreamID)
}

func (s *Stream) Artwork(kind Kind) string {
	if kind == Series {
		return string(s.Cover)
	}
	return string(s.Icon)
}

func (s *Stream) Release() string {
	if s.ReleaseDate != "" {
		return string(s.ReleaseDate)
	}
	return string(s.ReleaseAlt)
}

// Catalog is everything one account lists.
type Catalog struct {
	FetchedAt  time.Time           `json:"fetched_at"`
	Categories map[Kind][]Category `json:"categories"`
	Streams    map[Kind][]Stream   `json:"streams"`
}

type Status struct {
	Authorized bool
	State      string
	ExpiresAt  int64
	Active     int
	Max        int
}
