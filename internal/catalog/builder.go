package catalog

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"iptv-steady/internal/config"
	"iptv-steady/internal/xtream"
)

// Source is the catalog of one account. Empty Sections publishes everything.
type Source struct {
	Account  string
	Data     *xtream.Catalog
	Sections []string
}

func (s Source) publishes(kind xtream.Kind) bool {
	return len(s.Sections) == 0 || slices.Contains(s.Sections, string(kind))
}

type Builder struct {
	cfg        config.Catalog
	ids        *IDs
	filters    *filterSet
	names      *normalizer
	categories map[xtream.Kind]*categorizer
	traits     []trait
}

func NewBuilder(cfg config.Catalog, ids *IDs) (*Builder, error) {
	filters, err := newFilterSet(cfg.Filters)
	if err != nil {
		return nil, err
	}
	names, err := newNormalizer(cfg.Identity, cfg.Categories.UppercaseWords)
	if err != nil {
		return nil, err
	}
	categories := make(map[xtream.Kind]*categorizer, len(xtream.Kinds))
	for _, kind := range xtream.Kinds {
		if categories[kind], err = newCategorizer(cfg.Categories.For(string(kind)), cfg.OriginalNames); err != nil {
			return nil, err
		}
	}
	traits, err := newTraits(cfg.CopyTraits)
	if err != nil {
		return nil, err
	}
	return &Builder{cfg: cfg, ids: ids, filters: filters, names: names, categories: categories, traits: traits}, nil
}

func (b *Builder) Build(sources []Source) *Catalog {
	catalog := &Catalog{sections: map[xtream.Kind]*Section{}}
	for _, source := range sources {
		if source.Data.FetchedAt.After(catalog.FetchedAt) {
			catalog.FetchedAt = source.Data.FetchedAt
		}
	}
	for _, kind := range xtream.Kinds {
		catalog.sections[kind] = b.section(kind, sources)
	}
	return catalog
}

type row struct {
	account  string
	priority int
	stream   *xtream.Stream
	id       string
	name     string
	key      string
	year     int
	poster   string

	categoryOrigin   string
	categoryKey      string
	categoryShown    string
	categoryPosition int
	score            int
	label            string
	blocked          bool
	hidden           bool
}

type group struct {
	keys []string
	rows []*row
}

type categoryTally struct {
	id       int64
	shown    string
	count    int
	position int
}

func newStats() Stats {
	return Stats{Accounts: map[string]*AccountStats{}, BlockedContent: map[string]int{}, HiddenCategories: map[string]int{}}
}

func (b *Builder) section(kind xtream.Kind, sources []Source) *Section {
	stats := newStats()
	rows := b.collect(kind, sources, &stats)
	section := &Section{Kind: kind, slim: b.cfg.SlimLists}
	categories := map[string]*categoryTally{}
	used := map[int64]bool{}
	for _, g := range b.group(kind, rows, &stats) {
		if !g.visible() {
			stats.BlockedRows += len(g.rows)
			continue
		}
		section.Titles = append(section.Titles, b.title(kind, g, used, categories, &stats))
	}
	section.Categories = b.orderCategories(kind, categories)
	section.Stats = stats
	sortByAdded(section.Titles)
	section.prepare()
	return section
}

func (g group) visible() bool {
	listed := false
	for _, r := range g.rows {
		if r.blocked {
			return false
		}
		listed = listed || !r.hidden
	}
	return listed
}

// providerCategory is a category as an account lists it. Positions follow the accounts' order, then each provider's own.
type providerCategory struct {
	name     string
	position int
}

const unlisted = math.MaxInt

func categoryIndex(categories []xtream.Category, offset int) map[string]providerCategory {
	index := make(map[string]providerCategory, len(categories))
	for i, c := range categories {
		index[string(c.ID)] = providerCategory{name: string(c.Name), position: offset + i}
	}
	return index
}

func categoryOf(index map[string]providerCategory, stream *xtream.Stream) providerCategory {
	if found, ok := index[string(stream.CategoryID)]; ok {
		return found
	}
	for _, id := range stream.CategoryIDs {
		if found, ok := index[id]; ok {
			return found
		}
	}
	return providerCategory{position: unlisted}
}

func (b *Builder) collect(kind xtream.Kind, sources []Source, stats *Stats) []*row {
	var rows []*row
	offset := 0
	for priority, source := range sources {
		if !source.publishes(kind) {
			continue
		}
		listed := source.Data.Categories[kind]
		names := categoryIndex(listed, offset)
		offset += len(listed)
		accountStats := &AccountStats{}
		stats.Accounts[source.Account] = accountStats
		streams := source.Data.Streams[kind]
		stats.RawRows += len(streams)
		for i := range streams {
			r := b.row(kind, source.Account, priority, &streams[i], names, stats)
			if r != nil {
				accountStats.Rows++
				rows = append(rows, r)
			}
		}
	}
	return rows
}

func (b *Builder) row(kind xtream.Kind, account string, priority int, stream *xtream.Stream, names map[string]providerCategory, stats *Stats) *row {
	name := strings.TrimSpace(string(stream.Name))
	id := stream.Identifier(kind)
	if name == "" || id == "" {
		return nil
	}
	listed := categoryOf(names, stream)
	category := listed.name
	foldedCategory, foldedName := fold(category), fold(name)
	r := &row{account: account, priority: priority, stream: stream, id: id, name: name}
	r.blocked = b.filters.blocksContent(foldedCategory, foldedName, string(stream.IsAdult) == "1")
	r.hidden = b.filters.hidesCategory(foldedCategory)
	switch {
	case r.blocked:
		stats.BlockedContent[category]++
	case r.hidden:
		stats.HiddenCategories[category]++
	}
	r.key, r.year = b.names.parse(name, string(stream.Year), stream.Release())
	if r.key == "" {
		r.key = "#" + account + "|" + id
	}
	r.poster = b.names.posterKey(stream.Artwork(kind))
	r.categoryOrigin = b.categories[kind].origin(category)
	r.categoryKey, r.categoryShown = b.categories[kind].identify(category)
	r.categoryPosition = listed.position
	r.score, r.label = evaluateTraits(b.traits, foldedName+" | "+foldedCategory)
	return r
}

func (b *Builder) merges(kind xtream.Kind) bool {
	switch kind {
	case xtream.VOD:
		return b.cfg.Merge.VOD
	case xtream.Series:
		return b.cfg.Merge.Series
	}
	return b.cfg.Merge.Live
}

func (b *Builder) identity(kind xtream.Kind, r *row, category string) string {
	switch {
	case !b.merges(kind):
		return fmt.Sprintf("%s|%s|%s", kind, r.account, r.id)
	case kind == xtream.Live:
		return fmt.Sprintf("%s|%s|%s", kind, category, r.key)
	}
	return fmt.Sprintf("%s|%s|%d", kind, r.key, r.year)
}

// group processes keys in sorted order so assigned IDs never depend on map iteration.
func (b *Builder) group(kind xtream.Kind, rows []*row, stats *Stats) []group {
	links := newUnionFind(len(rows))
	identity := b.cfg.Identity
	switch {
	case !b.merges(kind):
	case kind == xtream.Live:
		linkEqualKeys(rows, links, func(r *row) string { return b.identity(kind, r, r.categoryKey) })
	default:
		linkByTitle(rows, links, identity.YearTolerance)
		stats.PosterMerges = linkByPoster(rows, links, identity.PosterMaxTitles, identity.YearTolerance)
	}
	members := map[int][]int{}
	for i := range rows {
		root := links.find(i)
		members[root] = append(members[root], i)
	}
	groups := make([]group, 0, len(members))
	for _, indexes := range members {
		g := group{rows: make([]*row, 0, len(indexes))}
		seen := map[string]bool{}
		for _, i := range indexes {
			g.rows = append(g.rows, rows[i])
			if key := b.identity(kind, rows[i], rows[i].categoryOrigin); !seen[key] {
				seen[key] = true
				g.keys = append(g.keys, key)
			}
		}
		sort.Strings(g.keys)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].keys[0] < groups[j].keys[0] })
	return groups
}

func commonYear(rows []*row) int {
	votes := map[int]int{}
	for _, r := range rows {
		if r.year != 0 {
			votes[r.year]++
		}
	}
	year, best := 0, 0
	for candidate, count := range votes {
		if count > best || (count == best && candidate < year) {
			year, best = candidate, count
		}
	}
	return year
}

func fill(target *string, value xtream.Text) {
	if *target == "" && value != "" && value != "0" {
		*target = string(value)
	}
}

func (b *Builder) title(kind xtream.Kind, g group, used map[int64]bool, categories map[string]*categoryTally, stats *Stats) *Title {
	slices.SortStableFunc(g.rows, func(x, y *row) int {
		if order := cmp.Compare(y.score, x.score); order != 0 {
			return order
		}
		return cmp.Compare(x.priority, y.priority)
	})
	year := commonYear(g.rows)
	t := &Title{ID: b.ids.GroupID(g.keys, used), Year: year, Name: b.displayName(kind, g.rows[0], year)}
	accounts := map[string]bool{}
	listed := map[string]bool{}
	var earliest int64
	for _, r := range g.rows {
		fillMetadata(t, kind, r.stream)
		if added, _ := strconv.ParseInt(string(r.stream.Added), 10, 64); added > 0 && (earliest == 0 || added < earliest) {
			earliest = added
		}
		t.Copies = append(t.Copies, Copy{Account: r.account, ID: r.id, Extension: string(r.stream.Extension), Score: r.score})
		accounts[r.account] = true
		if !r.hidden && !listed[r.categoryKey] {
			listed[r.categoryKey] = true
			t.Categories = append(t.Categories, b.registerCategory(kind, r, categories))
		}
	}
	if earliest > 0 {
		t.Added = strconv.FormatInt(earliest, 10)
	}
	countTitle(stats, accounts)
	return t
}

func fillMetadata(t *Title, kind xtream.Kind, s *xtream.Stream) {
	fill(&t.Icon, xtream.Text(s.Artwork(kind)))
	fill(&t.Rating, s.Rating)
	fill(&t.Rating5, s.Rating5)
	fill(&t.Modified, s.Modified)
	fill(&t.Plot, s.Plot)
	fill(&t.Cast, s.Cast)
	fill(&t.Director, s.Director)
	fill(&t.Genre, s.Genre)
	fill(&t.ReleaseDate, xtream.Text(s.Release()))
	fill(&t.Trailer, s.Trailer)
	fill(&t.Trailer, s.TrailerAlt)
	fill(&t.Runtime, s.Runtime)
	fill(&t.EPG, s.EPG)
	if len(t.Backdrop) == 0 {
		t.Backdrop = s.Backdrop
	}
}

func (b *Builder) displayName(kind xtream.Kind, best *row, year int) string {
	if kind == xtream.Live || b.cfg.OriginalNames {
		return best.name
	}
	name := b.names.display(best.name, year)
	if year > 0 {
		name = fmt.Sprintf("%s (%d)", name, year)
	}
	if best.label != "" {
		name += " [" + best.label + "]"
	}
	return name
}

func (b *Builder) registerCategory(kind xtream.Kind, r *row, categories map[string]*categoryTally) int64 {
	entry := categories[r.categoryKey]
	if entry == nil {
		entry = &categoryTally{id: b.ids.Assign(fmt.Sprintf("category|%s|%s", kind, r.categoryKey)), position: r.categoryPosition}
		categories[r.categoryKey] = entry
	}
	entry.position = min(entry.position, r.categoryPosition)
	entry.shown = preferredSpelling(entry.shown, r.categoryShown)
	entry.count++
	return entry.id
}

func countTitle(stats *Stats, accounts map[string]bool) {
	stats.Titles++
	if len(accounts) > 1 {
		stats.MultiAccount++
	}
	for account := range accounts {
		accountStats := stats.Accounts[account]
		accountStats.Titles++
		if len(accounts) == 1 {
			accountStats.Exclusive++
		}
	}
}

func (b *Builder) orderCategories(kind xtream.Kind, categories map[string]*categoryTally) []*Category {
	keys := make([]string, 0, len(categories))
	for key := range categories {
		keys = append(keys, key)
	}
	b.categories[kind].sortKeys(keys, func(key string) int { return categories[key].position })
	ordered := make([]*Category, 0, len(keys))
	for _, key := range keys {
		entry := categories[key]
		ordered = append(ordered, &Category{ID: entry.id, Name: entry.shown, Count: entry.count})
	}
	return ordered
}

func sortByAdded(titles []*Title) {
	sort.SliceStable(titles, func(i, j int) bool {
		addedI, _ := strconv.ParseInt(titles[i].Added, 10, 64)
		addedJ, _ := strconv.ParseInt(titles[j].Added, 10, 64)
		if addedI != addedJ {
			return addedI > addedJ
		}
		return titles[i].ID > titles[j].ID
	})
}
