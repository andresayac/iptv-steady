package testkit

import (
	"fmt"
	"testing"
	"time"

	"iptv-steady/internal/config"
)

const (
	ViewerName     = "viewer"
	ViewerPassword = "viewer-secret"
)

// AccountName is the name given to the panel at this position in the configuration.
func AccountName(index int) string {
	return fmt.Sprintf("account-%d", index)
}

// Config returns a valid configuration with short timings, one account per panel.
func Config(t testing.TB, panels ...*Panel) *config.Config {
	cfg := &config.Config{
		Server:  config.Server{Listen: ":0", ReadHeaderTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: time.Second},
		DataDir: t.TempDir(),
		Users:   []config.User{{Username: ViewerName, Password: ViewerPassword, MaxConnections: 2}},
		Upstream: config.Upstream{
			UserAgent: "test", ConnectTimeout: time.Second, TCPKeepAlive: time.Second, TLSTimeout: time.Second, HeaderTimeout: time.Second,
			CatalogTimeout: 5 * time.Second, CatalogHeaderTimeout: 2 * time.Second, StatusInterval: time.Hour, StatusTimeout: time.Second,
			StatusMaxBackoff: time.Hour, BreakerFailures: 3, BreakerCooldown: time.Minute,
		},
		Sync: config.Sync{Interval: time.Hour},
		File: config.File{
			ReadAhead: 256 << 10, ChunkSize: 16 << 10, OpenTimeout: 500 * time.Millisecond, ReopenTimeout: 300 * time.Millisecond, OpenPasses: 1, StallTimeout: 150 * time.Millisecond,
			SlowWindow: 400 * time.Millisecond, SlowMinRate: 32 << 10, RetriesPerCopy: 2, RetryDelays: []time.Duration{0, 20 * time.Millisecond},
			Reserve: config.Reserve{
				Empty: 10 * time.Millisecond, Low: 20 * time.Millisecond, Full: time.Minute,
				EmptyStallTimeout: 150 * time.Millisecond, FullStallTimeout: 150 * time.Millisecond, MinSupply: 0.8, ReplaceCooldown: time.Minute,
			},
			StarvedAfter:   100 * time.Millisecond,
			RecoveryBudget: 1500 * time.Millisecond, ProgressBytes: 64 << 10, ParkAfter: 200 * time.Millisecond, ClientIdleTimeout: 10 * time.Second,
			TailCheck: 16 << 10, StickyTTL: time.Hour, DeadLinkTTL: time.Hour, CacheEntries: 100,
		},
		Live: config.Live{
			ChunkSize: 16 << 10, OpenTimeout: 500 * time.Millisecond, StallTimeout: 150 * time.Millisecond,
			ReconnectBudget: 1500 * time.Millisecond, HealthyAfter: 500 * time.Millisecond, RetryDelays: []time.Duration{0, 20 * time.Millisecond},
		},
		Details: config.Details{VODTimeout: time.Second, SeriesTimeout: time.Second, VODTTL: time.Hour, SeriesTTL: time.Hour, CacheEntries: 100},
		Events:  config.Events{RetentionDays: 1},
		Catalog: config.Catalog{
			SlimLists: true,
			Merge:     config.Merge{VOD: true, Series: true, Live: true},
			Filters:   config.Filters{BlockContent: []string{"adult"}, AllowCategories: []string{"cartoons adult"}, BlockCategories: []string{"karaoke"}, BlockNames: []string{`^\(offline\)`}, HonorAdultFlag: true},
			Identity: config.Identity{
				YearPatterns:        []string{`[(\[]\s*((?:19|20)\d{2})\s*[)\]]`, `\[\s*((?:19|20)\d{2})\b[^\]]*\]`},
				FallbackYearPattern: `\s((?:19|20)\d{2})$`,
				MinYear:             1900, MaxYearAhead: 1, YearTolerance: 1,
				RemovePatterns:  []string{`\[[^\]]*\]`, `\([^)]*\d{3,4}p[^)]*\)`, `^\s*copia de\s+`},
				TagWords:        []string{"sub", "lat", "latino", "dual", "hd", "fhd", "cam"},
				TrailingWords:   []string{"latino", "dual", "hd", "fhd", "cam", "sub", "subtitulada"},
				StopWords:       []string{"y", "and"},
				PosterPattern:   `image\.tmdb\.org/.*?/([A-Za-z0-9_-]{20,})\.(?:jpg|png|webp)`,
				PosterMaxTitles: 4,
			},
			Categories: config.Categories{
				Fallback: "Others", NoiseWords: []string{"vod", "cine", "series"}, DropTrailingNumber: true, UppercaseWords: []string{"hbo"},
				Pinned: []string{"^premieres"}, Aliases: map[string]string{"accion": "Acción"},
			},
			CopyTraits: []config.CopyTrait{
				{Pattern: `\bcam\b`, Score: -1000, Label: "CAM"},
				{Pattern: `\bsub\b`, Score: -50, Label: "SUB"},
				{Pattern: `\b(lat|latino)\b`, Score: 20},
			},
		},
	}
	for i, panel := range panels {
		cfg.Accounts = append(cfg.Accounts, config.Account{Name: AccountName(i), URL: panel.URL(), Username: Username, Password: Password})
	}
	return cfg
}
