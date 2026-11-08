package catalog

import (
	"sync"
	"time"

	"iptv-steady/internal/xtream"
)

// Copy is one account's version of a title.
type Copy struct {
	Account   string
	ID        string
	Extension string
	Score     int
}

type Title struct {
	ID          int64
	Name        string
	Year        int
	Icon        string
	Rating      string
	Rating5     string
	Added       string
	Modified    string
	Plot        string
	Cast        string
	Director    string
	Genre       string
	ReleaseDate string
	Trailer     string
	Runtime     string
	EPG         string
	Backdrop    []string
	Categories  []int64
	Copies      []Copy
}

// BareName is the title without the year suffix added for display.
func (t *Title) BareName() string {
	return trimYearSuffix(t.Name, t.Year)
}

type Category struct {
	ID    int64
	Name  string
	Count int
}

type AccountStats struct {
	Rows      int
	Titles    int
	Exclusive int
}

type Stats struct {
	RawRows          int
	BlockedRows      int
	Titles           int
	MultiAccount     int
	PosterMerges     int
	Accounts         map[string]*AccountStats
	BlockedContent   map[string]int
	HiddenCategories map[string]int
}

type Section struct {
	Kind       xtream.Kind
	Titles     []*Title
	Categories []*Category
	Stats      Stats

	slim       bool
	index      map[int64]*Title
	list       Payload
	categories Payload
	byCategory sync.Map
}

func (s *Section) Lookup(id int64) *Title { return s.index[id] }

type Catalog struct {
	FetchedAt time.Time
	sections  map[xtream.Kind]*Section
}

func (c *Catalog) Section(kind xtream.Kind) *Section { return c.sections[kind] }
