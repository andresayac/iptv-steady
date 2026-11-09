package config

import (
	"bytes"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Server   Server    `yaml:"server"`
	DataDir  string    `yaml:"data_dir"`
	Users    []User    `yaml:"users"`
	Accounts []Account `yaml:"accounts"`
	Upstream Upstream  `yaml:"upstream"`
	Sync     Sync      `yaml:"sync"`
	File     File      `yaml:"file"`
	Live     Live      `yaml:"live"`
	Details  Details   `yaml:"details"`
	Events   Events    `yaml:"events"`
	Catalog  Catalog   `yaml:"catalog"`
}

type Server struct {
	Listen            string        `yaml:"listen"`
	PublicURL         string        `yaml:"public_url"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}

type User struct {
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	MaxConnections int    `yaml:"max_connections"`
}

type Account struct {
	Name           string `yaml:"name"`
	URL            string `yaml:"url"`
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	MaxConnections int    `yaml:"max_connections"`
	// Sections limits what this account publishes: vod, series, live. Empty means all.
	Sections []string `yaml:"sections"`
}

type Upstream struct {
	UserAgent            string        `yaml:"user_agent"`
	ConnectTimeout       time.Duration `yaml:"connect_timeout"`
	TCPKeepAlive         time.Duration `yaml:"tcp_keepalive"`
	TLSTimeout           time.Duration `yaml:"tls_timeout"`
	HeaderTimeout        time.Duration `yaml:"header_timeout"`
	CatalogTimeout       time.Duration `yaml:"catalog_timeout"`
	CatalogHeaderTimeout time.Duration `yaml:"catalog_header_timeout"`
	StatusInterval       time.Duration `yaml:"status_interval"`
	StatusTimeout        time.Duration `yaml:"status_timeout"`
	StatusMaxBackoff     time.Duration `yaml:"status_max_backoff"`
	BreakerFailures      int           `yaml:"breaker_failures"`
	BreakerCooldown      time.Duration `yaml:"breaker_cooldown"`
}

type Sync struct {
	Interval time.Duration `yaml:"interval"`
}

type File struct {
	ReadAhead         ByteSize        `yaml:"read_ahead"`
	ChunkSize         ByteSize        `yaml:"chunk_size"`
	OpenTimeout       time.Duration   `yaml:"open_timeout"`
	ReopenTimeout     time.Duration   `yaml:"reopen_timeout"`
	OpenPasses        int             `yaml:"open_passes"`
	StallTimeout      time.Duration   `yaml:"stall_timeout"`
	SlowWindow        time.Duration   `yaml:"slow_window"`
	SlowMinRate       ByteSize        `yaml:"slow_min_rate"`
	Reserve           Reserve         `yaml:"reserve"`
	StarvedAfter      time.Duration   `yaml:"starved_after"`
	RetriesPerCopy    int             `yaml:"retries_per_copy"`
	RetryDelays       []time.Duration `yaml:"retry_delays"`
	RecoveryBudget    time.Duration   `yaml:"recovery_budget"`
	ProgressBytes     ByteSize        `yaml:"progress_bytes"`
	ParkAfter         time.Duration   `yaml:"park_after"`
	ClientIdleTimeout time.Duration   `yaml:"client_idle_timeout"`
	TailCheck         ByteSize        `yaml:"tail_check"`
	StickyTTL         time.Duration   `yaml:"sticky_ttl"`
	DeadLinkTTL       time.Duration   `yaml:"dead_link_ttl"`
	CacheEntries      int             `yaml:"cache_entries"`
}

// Reserve adapts the reaction to a provider to the seconds of video already waiting for the viewer.
type Reserve struct {
	Empty             time.Duration `yaml:"empty"`
	Low               time.Duration `yaml:"low"`
	Full              time.Duration `yaml:"full"`
	EmptyStallTimeout time.Duration `yaml:"empty_stall_timeout"`
	FullStallTimeout  time.Duration `yaml:"full_stall_timeout"`
	MinSupply         float64       `yaml:"min_supply"`
	ReplaceCooldown   time.Duration `yaml:"replace_cooldown"`
}

type Live struct {
	ChunkSize       ByteSize        `yaml:"chunk_size"`
	OpenTimeout     time.Duration   `yaml:"open_timeout"`
	StallTimeout    time.Duration   `yaml:"stall_timeout"`
	ReconnectBudget time.Duration   `yaml:"reconnect_budget"`
	HealthyAfter    time.Duration   `yaml:"healthy_after"`
	RetryDelays     []time.Duration `yaml:"retry_delays"`
}

type Details struct {
	VODTimeout    time.Duration `yaml:"vod_timeout"`
	SeriesTimeout time.Duration `yaml:"series_timeout"`
	VODTTL        time.Duration `yaml:"vod_ttl"`
	SeriesTTL     time.Duration `yaml:"series_ttl"`
	CacheEntries  int           `yaml:"cache_entries"`
}

type Events struct {
	RetentionDays int `yaml:"retention_days"`
}

type Catalog struct {
	SlimLists bool `yaml:"slim_lists"`
	// OriginalNames keeps title and category names as providers send them, and categories in their order.
	OriginalNames bool        `yaml:"original_names"`
	Merge         Merge       `yaml:"merge"`
	Filters       Filters     `yaml:"filters"`
	Identity      Identity    `yaml:"identity"`
	Categories    Categories  `yaml:"categories"`
	CopyTraits    []CopyTrait `yaml:"copy_traits"`
}

type Merge struct {
	VOD    bool `yaml:"vod"`
	Series bool `yaml:"series"`
	Live   bool `yaml:"live"`
}

type Filters struct {
	BlockContent    []string `yaml:"block_content"`
	AllowCategories []string `yaml:"allow_categories"`
	BlockCategories []string `yaml:"block_categories"`
	BlockNames      []string `yaml:"block_names"`
	HonorAdultFlag  bool     `yaml:"honor_adult_flag"`
}

type Identity struct {
	YearPatterns        []string `yaml:"year_patterns"`
	FallbackYearPattern string   `yaml:"fallback_year_pattern"`
	MinYear             int      `yaml:"min_year"`
	MaxYearAhead        int      `yaml:"max_year_ahead"`
	YearTolerance       int      `yaml:"year_tolerance"`
	RemovePatterns      []string `yaml:"remove_patterns"`
	TagWords            []string `yaml:"tag_words"`
	TrailingWords       []string `yaml:"trailing_words"`
	StopWords           []string `yaml:"stop_words"`
	PosterPattern       string   `yaml:"poster_pattern"`
	PosterMaxTitles     int      `yaml:"poster_max_titles"`
}

type Categories struct {
	Fallback           string            `yaml:"fallback"`
	NoiseWords         []string          `yaml:"noise_words"`
	DropTrailingNumber bool              `yaml:"drop_trailing_number"`
	UppercaseWords     []string          `yaml:"uppercase_words"`
	Pinned             []string          `yaml:"pinned"`
	Aliases            map[string]string `yaml:"aliases"`
	// Sections refines pins and aliases for vod, series or live.
	Sections map[string]CategorySection `yaml:"sections"`
}

type CategorySection struct {
	Pinned  []string          `yaml:"pinned"`
	Aliases map[string]string `yaml:"aliases"`
}

// For returns the rules of one section: its aliases take precedence and its pins, when it has them, replace the shared ones.
func (c Categories) For(section string) Categories {
	override, found := c.Sections[section]
	if !found {
		return c
	}
	aliases := make(map[string]string, len(c.Aliases)+len(override.Aliases))
	maps.Copy(aliases, c.Aliases)
	maps.Copy(aliases, override.Aliases)
	c.Aliases = aliases
	if override.Pinned != nil {
		c.Pinned = override.Pinned
	}
	return c
}

type CopyTrait struct {
	Pattern string `yaml:"pattern"`
	Score   int    `yaml:"score"`
	Label   string `yaml:"label"`
}

func Load(path string) (*Config, error) {
	dir := filepath.Dir(path)
	if err := loadEnvFile(filepath.Join(dir, ".env")); err != nil {
		return nil, fmt.Errorf("reading .env: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := decodeInto(raw, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := cfg.expandReferences(); err != nil {
		return nil, err
	}
	if cfg.DataDir != "" && !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(dir, cfg.DataDir)
	}
	return cfg, cfg.Validate()
}

func decodeInto(raw []byte, cfg *Config) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	return decoder.Decode(cfg)
}

func (c *Config) expandReferences() error {
	var e expander
	e.apply(&c.Server.Listen)
	e.apply(&c.Server.PublicURL)
	for i := range c.Users {
		e.apply(&c.Users[i].Username)
		e.apply(&c.Users[i].Password)
	}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		e.apply(&a.URL)
		e.apply(&a.Username)
		e.apply(&a.Password)
	}
	return e.err()
}

var knownSections = []string{"vod", "series", "live"}

type problems []string

func (p *problems) require(ok bool, format string, args ...any) {
	if !ok {
		*p = append(*p, fmt.Sprintf(format, args...))
	}
}

func (p *problems) positive(values map[string]int64) {
	for name, value := range values {
		p.require(value > 0, "%s must be positive", name)
	}
}

func (c *Config) Validate() error {
	var p problems
	p.require(c.Server.Listen != "", "server.listen is required")
	p.require(c.DataDir != "", "data_dir is required")
	p.require(c.Upstream.UserAgent != "", "upstream.user_agent is required")
	p.require(len(c.Users) > 0, "at least one user is required")
	p.require(len(c.Accounts) > 0, "at least one account is required")
	p.require(len(c.File.RetryDelays) > 0, "file.retry_delays is required")
	p.require(len(c.Live.RetryDelays) > 0, "live.retry_delays is required")
	for i, u := range c.Users {
		p.require(u.Username != "" && u.Password != "", "users[%d] needs a username and a password", i)
	}
	seen := map[string]bool{}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		parsed, err := url.Parse(a.URL)
		valid := err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
		p.require(valid, "accounts[%d] has an invalid url", i)
		p.require(a.Name != "" && !seen[a.Name], "accounts[%d] needs a unique name", i)
		p.require(a.Username != "" && a.Password != "", "account %q needs a username and a password", a.Name)
		for _, section := range a.Sections {
			p.require(slices.Contains(knownSections, section), "account %q has an unknown section %q", a.Name, section)
		}
		seen[a.Name] = true
		a.URL = strings.TrimRight(a.URL, "/")
	}
	for section := range c.Catalog.Categories.Sections {
		p.require(slices.Contains(knownSections, section), "catalog.categories.sections has an unknown section %q", section)
	}
	reserve := c.File.Reserve
	p.require(reserve.Empty < reserve.Low && reserve.Low < reserve.Full, "file.reserve needs empty < low < full")
	p.require(reserve.MinSupply > 0 && reserve.MinSupply <= 1, "file.reserve.min_supply must be above 0 and at most 1")
	p.positive(c.timeouts())
	p.positive(c.sizes())
	if len(p) == 0 {
		return nil
	}
	sort.Strings(p)
	return fmt.Errorf("invalid configuration:\n  %s", strings.Join(p, "\n  "))
}

func (c *Config) timeouts() map[string]int64 {
	durations := map[string]time.Duration{
		"server.read_header_timeout":       c.Server.ReadHeaderTimeout,
		"server.idle_timeout":              c.Server.IdleTimeout,
		"server.shutdown_timeout":          c.Server.ShutdownTimeout,
		"upstream.connect_timeout":         c.Upstream.ConnectTimeout,
		"upstream.tcp_keepalive":           c.Upstream.TCPKeepAlive,
		"upstream.tls_timeout":             c.Upstream.TLSTimeout,
		"upstream.header_timeout":          c.Upstream.HeaderTimeout,
		"upstream.catalog_timeout":         c.Upstream.CatalogTimeout,
		"upstream.catalog_header_timeout":  c.Upstream.CatalogHeaderTimeout,
		"upstream.status_interval":         c.Upstream.StatusInterval,
		"upstream.status_timeout":          c.Upstream.StatusTimeout,
		"upstream.status_max_backoff":      c.Upstream.StatusMaxBackoff,
		"upstream.breaker_cooldown":        c.Upstream.BreakerCooldown,
		"sync.interval":                    c.Sync.Interval,
		"file.open_timeout":                c.File.OpenTimeout,
		"file.reopen_timeout":              c.File.ReopenTimeout,
		"file.stall_timeout":               c.File.StallTimeout,
		"file.slow_window":                 c.File.SlowWindow,
		"file.reserve.empty":               c.File.Reserve.Empty,
		"file.reserve.empty_stall_timeout": c.File.Reserve.EmptyStallTimeout,
		"file.reserve.full_stall_timeout":  c.File.Reserve.FullStallTimeout,
		"file.reserve.replace_cooldown":    c.File.Reserve.ReplaceCooldown,
		"file.starved_after":               c.File.StarvedAfter,
		"file.recovery_budget":             c.File.RecoveryBudget,
		"file.park_after":                  c.File.ParkAfter,
		"file.client_idle_timeout":         c.File.ClientIdleTimeout,
		"file.sticky_ttl":                  c.File.StickyTTL,
		"file.dead_link_ttl":               c.File.DeadLinkTTL,
		"live.open_timeout":                c.Live.OpenTimeout,
		"live.stall_timeout":               c.Live.StallTimeout,
		"live.reconnect_budget":            c.Live.ReconnectBudget,
		"live.healthy_after":               c.Live.HealthyAfter,
		"details.vod_timeout":              c.Details.VODTimeout,
		"details.series_timeout":           c.Details.SeriesTimeout,
		"details.vod_ttl":                  c.Details.VODTTL,
		"details.series_ttl":               c.Details.SeriesTTL,
	}
	out := make(map[string]int64, len(durations))
	for name, value := range durations {
		out[name] = int64(value)
	}
	return out
}

func (c *Config) sizes() map[string]int64 {
	return map[string]int64{
		"upstream.breaker_failures": int64(c.Upstream.BreakerFailures),
		"file.read_ahead":           int64(c.File.ReadAhead),
		"file.chunk_size":           int64(c.File.ChunkSize),
		"file.open_passes":          int64(c.File.OpenPasses),
		"file.slow_min_rate":        int64(c.File.SlowMinRate),
		"file.retries_per_copy":     int64(c.File.RetriesPerCopy),
		"file.progress_bytes":       int64(c.File.ProgressBytes),
		"file.tail_check":           int64(c.File.TailCheck),
		"file.cache_entries":        int64(c.File.CacheEntries),
		"live.chunk_size":           int64(c.Live.ChunkSize),
		"details.cache_entries":     int64(c.Details.CacheEntries),
		"events.retention_days":     int64(c.Events.RetentionDays),
	}
}
