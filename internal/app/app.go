package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"text/tabwriter"
	"time"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/catalog"
	"iptv-steady/internal/config"
	"iptv-steady/internal/events"
	"iptv-steady/internal/httpapi"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/xtream"
)

type App struct {
	eventsDir string
	journal   *events.Journal
	pool      *accounts.Pool
	catalog   *catalog.Manager
	server    *httpapi.Server
}

func New(cfg *config.Config) (*App, error) {
	eventsDir := filepath.Join(cfg.DataDir, "events")
	journal, err := events.Open(eventsDir, cfg.Events.RetentionDays)
	if err != nil {
		return nil, err
	}
	pool := accounts.NewPool(cfg)
	manager, err := catalog.NewManager(cfg, pool)
	if err != nil {
		journal.Close()
		return nil, err
	}
	engine := relay.NewEngine(cfg.File, cfg.Live, pool, journal)
	return &App{eventsDir: eventsDir, journal: journal, pool: pool, catalog: manager, server: httpapi.New(cfg, manager, pool, engine)}, nil
}

func (a *App) Close() { a.journal.Close() }

func (a *App) Serve(ctx context.Context) error {
	if a.catalog.Restore() {
		slog.Info("catalog restored from disk")
	}
	var background sync.WaitGroup
	background.Go(func() { a.pool.Run(ctx) })
	background.Go(func() { a.catalog.Run(ctx) })
	err := a.server.Run(ctx)
	background.Wait()
	return err
}

// Sync fetches every account once and prints the resulting catalog.
func (a *App) Sync(ctx context.Context, out io.Writer) error {
	a.catalog.Restore()
	err := a.catalog.Sync(ctx)
	if current := a.catalog.Current(); current != nil {
		writeCatalogSummary(out, current)
	}
	return err
}

func writeCatalogSummary(out io.Writer, current *catalog.Catalog) {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "section\taccount\trows\ttitles\texclusive")
	for _, kind := range xtream.Kinds {
		section := current.Section(kind)
		stats := section.Stats
		fmt.Fprintf(table, "%s\t(all)\t%d\t%d\t%d multi-account, %d blocked, %d categories\n", kind, stats.RawRows, stats.Titles, stats.MultiAccount, stats.BlockedRows, len(section.Categories))
		for account, values := range stats.Accounts {
			fmt.Fprintf(table, "\t%s\t%d\t%d\t%d\n", account, values.Rows, values.Titles, values.Exclusive)
		}
	}
	table.Flush()
}

// Report prints playback events from the last days.
func (a *App) Report(out io.Writer, days int) error {
	recorded, err := events.Read(a.eventsDir, time.Now().AddDate(0, 0, -days))
	if err != nil {
		return err
	}
	return events.WriteReport(out, recorded)
}
