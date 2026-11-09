package catalog

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"iptv-steady/internal/config"
	"iptv-steady/internal/testkit"
	"iptv-steady/internal/xtream"
)

func testNormalizer(t *testing.T) *normalizer {
	t.Helper()
	cfg := testkit.Config(t).Catalog
	n, err := newNormalizer(cfg.Identity, cfg.Categories.UppercaseWords)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestParseTitle(t *testing.T) {
	n := testNormalizer(t)
	cases := []struct {
		name, yearField, wantKey string
		wantYear                 int
	}{
		{"Guardianes de la galaxia (2014).", "", "guardianes de la galaxia", 2014},
		{"La noche del demonio [2010, PG-13, 6.9][Full HD - Latino-Ingles][CruzCrispin]", "", "la noche del demonio", 2010},
		{"Brigada A   Los magníficos (2010)", "", "brigada a los magnificos", 2010},
		{"Brigada A: Los magníficos (2010)", "", "brigada a los magnificos", 2010},
		{"Shut In (Dual)", "", "shut in", 0},
		{"LAT - Avatar (2009)", "", "avatar", 2009},
		{"Five Nights at Freddy's 2 (2025) - CAM SUBTITULADA", "", "five nights at freddy s 2", 2025},
		{"the last sharknado its about time 2018", "", "the last sharknado its about time", 2018},
		{"Blade Runner 2049 (2017)", "", "blade runner 2049", 2017},
		{"1917 (2019)", "", "1917", 2019},
		{"2012", "2009", "2012", 2009},
		{"Copia de El supersabio", "", "el supersabio", 0},
		{"Amazon_ Jeff Bezos _ DW Documental(720P_HD)", "", "amazon jeff bezos dw documental", 0},
		{"Alvin y las ardillas 2 (2009)", "", "alvin las ardillas 2", 2009},
	}
	for _, c := range cases {
		key, year := n.parse(c.name, c.yearField, "")
		if key != c.wantKey || year != c.wantYear {
			t.Errorf("parse(%q) = %q, %d; want %q, %d", c.name, key, year, c.wantKey, c.wantYear)
		}
	}
}

func TestParseTitleKeepsNonLatinNames(t *testing.T) {
	key, year := testNormalizer(t).parse("僕のヒーローアカデミア (2018)", "", "")
	if key == "" || year != 2018 {
		t.Fatalf("key %q, year %d", key, year)
	}
}

func TestDisplayName(t *testing.T) {
	n := testNormalizer(t)
	cases := []struct {
		name string
		year int
		want string
	}{
		{"La noche del demonio [2010, PG-13, 6.9][Full HD - Latino-Ingles][CruzCrispin]", 2010, "La noche del demonio"},
		{"Shut In (Dual)", 0, "Shut In"},
		{"Shut In (Encerrada) (2022)", 2022, "Shut In (Encerrada)"},
		{"El prodigio HD", 0, "El prodigio"},
		{"the last sharknado its about time 2018", 2018, "The Last Sharknado Its About Time"},
		{"BAJO LA ESCALERA DE JACOB", 0, "Bajo La Escalera De Jacob"},
	}
	for _, c := range cases {
		if got := n.display(c.name, c.year); got != c.want {
			t.Errorf("display(%q) = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestCategoryIdentityIgnoresDecoration(t *testing.T) {
	c, err := newCategorizer(testkit.Config(t).Catalog.Categories, false)
	if err != nil {
		t.Fatal(err)
	}
	groups := [][]string{
		{"🎞️ VOD | COMEDIA", "♦️ CINE COMEDIA", "⭐️VOD COMEDIA 2"},
		{"⭐️VOD ROMANCE 1", "⭐️VOD ROMANCE 2", "♦️ CINE ROMANCE"},
	}
	for _, group := range groups {
		first, _ := c.identify(group[0])
		for _, name := range group[1:] {
			if key, _ := c.identify(name); key != first {
				t.Errorf("identify(%q) = %q; want %q", name, key, first)
			}
		}
	}
	if _, shown := c.identify("♦️ CINE ACCION"); shown != "Acción" {
		t.Errorf("alias = %q", shown)
	}
	if key, shown := c.identify(""); shown != "Others" || key == "" {
		t.Errorf("fallback = %q, %q", key, shown)
	}
}

func TestSectionRulesOnlyApplyToTheirSection(t *testing.T) {
	builder, _ := newTestBuilder(t, func(cfg *config.Catalog) {
		cfg.Categories.Aliases = map[string]string{"chile": "Cine Chileno", "others": "Otros"}
		cfg.Categories.Sections = map[string]config.CategorySection{"live": {Aliases: map[string]string{"chile": "Chile TV"}, Pinned: []string{"^chile"}}}
	})
	if _, shown := builder.categories[xtream.VOD].identify("Chile"); shown != "Cine Chileno" {
		t.Errorf("vod = %q", shown)
	}
	if _, shown := builder.categories[xtream.Live].identify("Chile"); shown != "Chile TV" {
		t.Errorf("live = %q", shown)
	}
	if _, shown := builder.categories[xtream.Live].identify(""); shown != "Otros" {
		t.Errorf("fallback = %q", shown)
	}
	if rank := builder.categories[xtream.Live].pinRank("chile tv"); rank != 0 {
		t.Errorf("live pin rank = %d", rank)
	}
	if rank := builder.categories[xtream.VOD].pinRank("premieres 2026"); rank != 0 {
		t.Errorf("vod pin rank = %d", rank)
	}
}

func liveSource(categories map[string]string, streams ...xtream.Stream) []Source {
	var list []xtream.Category
	for id, name := range categories {
		list = append(list, xtream.Category{ID: xtream.Text(id), Name: xtream.Text(name)})
	}
	return []Source{{Account: "a", Data: &xtream.Catalog{
		FetchedAt:  time.Now(),
		Categories: map[xtream.Kind][]xtream.Category{xtream.Live: list},
		Streams:    map[xtream.Kind][]xtream.Stream{xtream.Live: streams},
	}}}
}

func withLiveAliases(t *testing.T, ids *IDs, aliases map[string]string) *Builder {
	t.Helper()
	cfg := testkit.Config(t).Catalog
	cfg.Categories.Sections = map[string]config.CategorySection{"live": {Aliases: aliases}}
	builder, err := NewBuilder(cfg, ids)
	if err != nil {
		t.Fatal(err)
	}
	return builder
}

func TestRenamingALiveCategoryKeepsChannelNumbers(t *testing.T) {
	channels := liveSource(map[string]string{"1": "TV Colombia"}, movie("10", "Caracol", "1"))
	original, ids := newTestBuilder(t, nil)
	before := original.Build(channels).Section(xtream.Live).Titles[0].ID

	section := withLiveAliases(t, ids, map[string]string{"tv colombia": "Colombia"}).Build(channels).Section(xtream.Live)

	if section.Titles[0].ID != before || section.Categories[0].Name != "Colombia" {
		t.Fatalf("id %d, want %d; categories %+v", section.Titles[0].ID, before, section.Categories)
	}
}

func TestJoiningLiveCategoriesJoinsChannelsAndKeepsOldNumbers(t *testing.T) {
	channels := liveSource(map[string]string{"1": "TV Colombia", "2": "Colombia Tvº"}, movie("10", "Caracol", "1"), movie("20", "Caracol", "2"))
	original, ids := newTestBuilder(t, nil)
	separate := original.Build(channels).Section(xtream.Live).Titles
	if len(separate) != 2 {
		t.Fatalf("titles before = %v", len(separate))
	}

	section := withLiveAliases(t, ids, map[string]string{"tv colombia": "Colombia", "colombia tvº": "Colombia"}).Build(channels).Section(xtream.Live)

	if len(section.Titles) != 1 || len(section.Titles[0].Copies) != 2 || len(section.Categories) != 1 {
		t.Fatalf("titles %d, categories %d", len(section.Titles), len(section.Categories))
	}
	for _, old := range separate {
		if ids.Resolve(old.ID) != section.Titles[0].ID {
			t.Errorf("number %d no longer reaches the channel", old.ID)
		}
	}
}

type account struct {
	name       string
	categories map[string]string
	streams    []xtream.Stream
}

func sources(accounts ...account) []Source {
	out := make([]Source, 0, len(accounts))
	for _, a := range accounts {
		var categories []xtream.Category
		for id, name := range a.categories {
			categories = append(categories, xtream.Category{ID: xtream.Text(id), Name: xtream.Text(name)})
		}
		out = append(out, Source{Account: a.name, Data: &xtream.Catalog{
			FetchedAt:  time.Now(),
			Categories: map[xtream.Kind][]xtream.Category{xtream.VOD: categories},
			Streams:    map[xtream.Kind][]xtream.Stream{xtream.VOD: a.streams},
		}})
	}
	return out
}

func newTestBuilder(t *testing.T, tune func(*config.Catalog)) (*Builder, *IDs) {
	t.Helper()
	cfg := testkit.Config(t).Catalog
	if tune != nil {
		tune(&cfg)
	}
	ids, err := LoadIDs(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewBuilder(cfg, ids)
	if err != nil {
		t.Fatal(err)
	}
	return builder, ids
}

func names(section *Section) []string {
	out := make([]string, 0, len(section.Titles))
	for _, title := range section.Titles {
		out = append(out, title.Name)
	}
	slices.Sort(out)
	return out
}

func movie(id, name, category string) xtream.Stream {
	return xtream.Stream{StreamID: xtream.Text(id), Name: xtream.Text(name), CategoryID: xtream.Text(category), Extension: "mp4"}
}

func TestSameTitleFromSeveralAccountsBecomesOne(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	catalog := builder.Build(sources(
		account{"a", map[string]string{"1": "⭐️VOD DRAMA"}, []xtream.Stream{movie("10", "Matrix (1999)", "1")}},
		account{"b", map[string]string{"7": "♦️ CINE DRAMA"}, []xtream.Stream{movie("70", "Matrix [1999, R][Full HD - Latino][Uploader]", "7")}},
		account{"c", map[string]string{"3": "Drama"}, []xtream.Stream{movie("30", "Matrix (2000)", "3"), movie("31", "Matrix (2021)", "3")}},
	))
	section := catalog.Section(xtream.VOD)
	if want := []string{"Matrix (1999)", "Matrix (2021)"}; !slices.Equal(names(section), want) {
		t.Fatalf("titles = %v", names(section))
	}
	if len(section.Categories) != 1 {
		t.Fatalf("categories = %d", len(section.Categories))
	}
	for _, title := range section.Titles {
		if title.Name == "Matrix (1999)" && len(title.Copies) != 3 {
			t.Fatalf("copies = %+v", title.Copies)
		}
	}
}

func TestSharedPosterJoinsTranslatedTitles(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	poster := "https://image.tmdb.org/t/p/w500/abcdefghijklmnopqrstuvwxyz.jpg"
	first, second := movie("1", "Pet Sematary (2019)", "1"), movie("2", "Cementerio maldito (2019)", "1")
	first.Icon, second.Icon = xtream.Text(poster), xtream.Text(poster)
	section := builder.Build(sources(account{"a", map[string]string{"1": "Terror"}, []xtream.Stream{first, second}})).Section(xtream.VOD)
	if len(section.Titles) != 1 {
		t.Fatalf("titles = %v", names(section))
	}
}

func TestBlockedContentDisappearsEvenFromOtherCategories(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	section := builder.Build(sources(
		account{"a", map[string]string{"1": "Adult Movies", "2": "Drama", "3": "Cartoons Adult"}, []xtream.Stream{
			movie("10", "Hidden (2001)", "1"),
			movie("11", "Visible (2002)", "2"),
			movie("12", "(OFFLINE) Gone (2003)", "2"),
			movie("13", "Rick (2013)", "3"),
		}},
		account{"b", map[string]string{"5": "Drama"}, []xtream.Stream{movie("50", "Hidden (2001)", "5")}},
	)).Section(xtream.VOD)
	if want := []string{"Rick (2013)", "Visible (2002)"}; !slices.Equal(names(section), want) {
		t.Fatalf("titles = %v; want %v", names(section), want)
	}
}

func TestHiddenCategoryOnlyHidesTheCategory(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	section := builder.Build(sources(account{"a", map[string]string{"1": "Karaoke", "2": "Drama"}, []xtream.Stream{
		movie("10", "Only Karaoke (2001)", "1"),
		movie("11", "Listed Twice (2002)", "1"),
		movie("12", "Listed Twice (2002)", "2"),
	}})).Section(xtream.VOD)
	if want := []string{"Listed Twice (2002)"}; !slices.Equal(names(section), want) {
		t.Fatalf("titles = %v", names(section))
	}
	if len(section.Categories) != 1 || section.Categories[0].Name != "Drama" {
		t.Fatalf("categories = %+v", section.Categories)
	}
}

func TestAdultFlagFromProviderIsHonored(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	flagged := movie("10", "Flagged (2001)", "1")
	flagged.IsAdult = "1"
	section := builder.Build(sources(account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{flagged}})).Section(xtream.VOD)
	if len(section.Titles) != 0 {
		t.Fatalf("titles = %v", names(section))
	}
}

func TestCopiesAreRankedByConfiguredTraits(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	section := builder.Build(sources(
		account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{movie("10", "Up (2009) CAM", "1")}},
		account{"b", map[string]string{"1": "Drama"}, []xtream.Stream{movie("20", "Up (2009) (Sub)", "1")}},
		account{"c", map[string]string{"1": "Drama"}, []xtream.Stream{movie("30", "Up (2009) (Lat)", "1")}},
		account{"d", map[string]string{"1": "Drama"}, []xtream.Stream{movie("40", "Up (2009)", "1")}},
	)).Section(xtream.VOD)
	var order []string
	for _, version := range section.Titles[0].Copies {
		order = append(order, version.Account)
	}
	if want := []string{"c", "d", "b", "a"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v; want %v", order, want)
	}
}

func TestLabelMarksTitlesThatOnlyHaveWeakCopies(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	section := builder.Build(sources(account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{movie("10", "Up (2009) CAM", "1")}})).Section(xtream.VOD)
	if section.Titles[0].Name != "Up (2009) [CAM]" {
		t.Fatalf("name = %q", section.Titles[0].Name)
	}
}

func TestMergingCanBeDisabled(t *testing.T) {
	builder, _ := newTestBuilder(t, func(cfg *config.Catalog) { cfg.Merge.VOD = false })
	section := builder.Build(sources(
		account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{movie("10", "Up (2009)", "1")}},
		account{"b", map[string]string{"1": "Drama"}, []xtream.Stream{movie("20", "Up (2009)", "1")}},
	)).Section(xtream.VOD)
	if len(section.Titles) != 2 {
		t.Fatalf("titles = %d", len(section.Titles))
	}
}

// orderedSource keeps categories in the order the provider lists them, which the map-based helper cannot.
func orderedSource(name string, categories []xtream.Category, streams ...xtream.Stream) Source {
	return Source{Account: name, Data: &xtream.Catalog{
		FetchedAt:  time.Now(),
		Categories: map[xtream.Kind][]xtream.Category{xtream.VOD: categories},
		Streams:    map[xtream.Kind][]xtream.Stream{xtream.VOD: streams},
	}}
}

func originalNamesSources() []Source {
	return []Source{
		orderedSource("a", []xtream.Category{{ID: "1", Name: "⭐️VOD DRAMA"}, {ID: "2", Name: "Acción"}},
			movie("10", "Matrix (1999) [LAT]", "1"), movie("11", "Up (2009) CAM", "2")),
		orderedSource("b", []xtream.Category{{ID: "5", Name: "Zeta"}, {ID: "6", Name: "acción "}},
			movie("50", "Matrix [1999, R][Full HD - Latino]", "6"), movie("51", "Solo (2018)", "5")),
	}
}

func categoryNamesOf(section *Section) []string {
	out := make([]string, 0, len(section.Categories))
	for _, c := range section.Categories {
		out = append(out, c.Name)
	}
	return out
}

func TestOriginalNamesKeepProviderNamesAndOrder(t *testing.T) {
	builder, _ := newTestBuilder(t, func(cfg *config.Catalog) { cfg.OriginalNames, cfg.Categories.Pinned = true, nil })
	section := builder.Build(originalNamesSources()).Section(xtream.VOD)

	if got, want := names(section), []string{"Matrix (1999) [LAT]", "Solo (2018)", "Up (2009) CAM"}; !slices.Equal(got, want) {
		t.Fatalf("names = %q; want %q", got, want)
	}
	if got, want := categoryNamesOf(section), []string{"⭐️VOD DRAMA", "Acción", "Zeta", "acción"}; !slices.Equal(got, want) {
		t.Fatalf("categories = %q; want the providers' names in their order %q", got, want)
	}
}

func TestPinsStillApplyToOriginalNames(t *testing.T) {
	builder, _ := newTestBuilder(t, func(cfg *config.Catalog) { cfg.OriginalNames, cfg.Categories.Pinned = true, []string{"^zeta"} })
	section := builder.Build(originalNamesSources()).Section(xtream.VOD)

	if got, want := categoryNamesOf(section), []string{"Zeta", "⭐️VOD DRAMA", "Acción", "acción"}; !slices.Equal(got, want) {
		t.Fatalf("categories = %q; want %q", got, want)
	}
}

func TestSwitchingToOriginalNamesKeepsTitleNumbers(t *testing.T) {
	ids, err := LoadIDs(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	numbers := func(original bool) map[string]int64 {
		cfg := testkit.Config(t).Catalog
		cfg.OriginalNames = original
		builder, err := NewBuilder(cfg, ids)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, title := range builder.Build(originalNamesSources()).Section(xtream.VOD).Titles {
			out[title.Copies[0].Account+title.Copies[0].ID] = title.ID
		}
		return out
	}

	before, after := numbers(false), numbers(true)

	if len(before) != 3 || !maps.Equal(before, after) {
		t.Fatalf("numbers changed from %v to %v", before, after)
	}
}

func TestAccountsCanLimitTheSectionsTheyPublish(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	listed := sources(account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{movie("10", "Up (2009)", "1")}})
	listed[0].Sections = []string{"series"}
	if section := builder.Build(listed).Section(xtream.VOD); len(section.Titles) != 0 {
		t.Fatalf("titles = %v", names(section))
	}
	listed[0].Sections = []string{"vod"}
	if section := builder.Build(listed).Section(xtream.VOD); len(section.Titles) != 1 {
		t.Fatalf("titles = %v", names(section))
	}
}

func TestEarliestDateKeepsTitlesFromReappearingAsNew(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	first, second := movie("10", "Up (2009)", "1"), movie("20", "Up (2009)", "1")
	first.Added, second.Added = "200", "100"
	section := builder.Build(sources(
		account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{first}},
		account{"b", map[string]string{"1": "Drama"}, []xtream.Stream{second}},
	)).Section(xtream.VOD)
	if section.Titles[0].Added != "100" {
		t.Fatalf("added = %q", section.Titles[0].Added)
	}
}

func TestIDsSurviveRebuildsAndMerges(t *testing.T) {
	builder, ids := newTestBuilder(t, nil)
	poster := xtream.Text("https://image.tmdb.org/t/p/w500/abcdefghijklmnopqrstuvwxyz.jpg")
	first := movie("1", "Pet Sematary (2019)", "1")
	first.Icon = poster
	second := movie("2", "Cementerio maldito (2019)", "1")
	categories := map[string]string{"1": "Terror"}

	before := builder.Build(sources(account{"a", categories, []xtream.Stream{first, second}})).Section(xtream.VOD)
	if len(before.Titles) != 2 {
		t.Fatalf("titles = %d", len(before.Titles))
	}
	again := builder.Build(sources(account{"a", categories, []xtream.Stream{first, second}})).Section(xtream.VOD)
	if before.Titles[0].ID != again.Titles[0].ID || before.Titles[1].ID != again.Titles[1].ID {
		t.Fatal("IDs changed between identical builds")
	}

	second.Icon = poster
	merged := builder.Build(sources(account{"a", categories, []xtream.Stream{first, second}})).Section(xtream.VOD)
	if len(merged.Titles) != 1 {
		t.Fatalf("titles = %d", len(merged.Titles))
	}
	for _, title := range before.Titles {
		if merged.Lookup(ids.Resolve(title.ID)) == nil {
			t.Fatalf("old ID %d stopped resolving", title.ID)
		}
	}
}

func TestIDsArePersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	ids, err := LoadIDs(path)
	if err != nil {
		t.Fatal(err)
	}
	assigned := ids.Assign("key")
	episode := ids.AssignEpisode(7, 1, 2)
	if err := ids.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadIDs(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Assign("key") != assigned {
		t.Fatal("key changed after reload")
	}
	if series, found := reloaded.EpisodeSeries(episode); !found || series != 7 {
		t.Fatalf("episode series = %d, %v", series, found)
	}
}

func TestSlimListsOmitHeavyFields(t *testing.T) {
	builder, _ := newTestBuilder(t, nil)
	stream := movie("10", "Up (2009)", "1")
	stream.Plot = "a long synopsis"
	section := builder.Build(sources(account{"a", map[string]string{"1": "Drama"}, []xtream.Stream{stream}})).Section(xtream.VOD)
	row := section.row(1, section.Titles[0], section.Titles[0].Categories[0]).(vodRow)
	if row.Plot != "" {
		t.Fatal("slim rows should not carry the plot")
	}
	if section.Titles[0].Plot == "" {
		t.Fatal("the plot should stay available for details")
	}
}
