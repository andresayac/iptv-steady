package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/config"
	"iptv-steady/internal/xtream"
)

// ErrSyncRunning reports that another sync is already in progress.
var ErrSyncRunning = errors.New("a catalog sync is already running")

// Manager keeps the last good catalog of every account on disk, so a provider failure never empties the published catalog.
type Manager struct {
	accounts []config.Account
	pool     *accounts.Pool
	builder  *Builder
	ids      *IDs
	rawDir   string
	interval time.Duration
	current  atomic.Pointer[Catalog]
	syncing  sync.Mutex
}

func NewManager(cfg *config.Config, pool *accounts.Pool) (*Manager, error) {
	rawDir := filepath.Join(cfg.DataDir, "catalogs")
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		return nil, err
	}
	ids, err := LoadIDs(filepath.Join(cfg.DataDir, "ids.json"))
	if err != nil {
		return nil, err
	}
	builder, err := NewBuilder(cfg.Catalog, ids)
	if err != nil {
		return nil, err
	}
	return &Manager{accounts: cfg.Accounts, pool: pool, builder: builder, ids: ids, rawDir: rawDir, interval: cfg.Sync.Interval}, nil
}

func (m *Manager) Current() *Catalog { return m.current.Load() }

func (m *Manager) IDs() *IDs { return m.ids }

// Find locates a title by the ID a player knows, following merges.
func (m *Manager) Find(kind xtream.Kind, id int64) *Title {
	catalog := m.Current()
	if catalog == nil {
		return nil
	}
	return catalog.Section(kind).Lookup(m.ids.Resolve(id))
}

// Restore rebuilds the catalog from disk so a restart serves immediately.
func (m *Manager) Restore() bool {
	var sources []Source
	for _, account := range m.pool.All() {
		if data, err := m.readRaw(account.Name); err == nil {
			sources = append(sources, m.source(account.Name, data))
		}
	}
	if len(sources) == 0 {
		return false
	}
	m.publish(sources)
	return true
}

func (m *Manager) Sync(ctx context.Context) error {
	if !m.syncing.TryLock() {
		return ErrSyncRunning
	}
	defer m.syncing.Unlock()
	all := m.pool.All()
	fetched := make([]*xtream.Catalog, len(all))
	failures := make([]error, len(all))
	var group sync.WaitGroup
	for i, account := range all {
		group.Go(func() { fetched[i], failures[i] = fetchUsable(ctx, account) })
	}
	group.Wait()

	var sources []Source
	for i, account := range all {
		data := fetched[i]
		if failures[i] != nil {
			slog.Warn("account sync failed, keeping its last good catalog", "error", failures[i])
			var err error
			if data, err = m.readRaw(account.Name); err != nil {
				continue
			}
		} else if err := m.writeRaw(account.Name, data); err != nil {
			slog.Error("saving account catalog", "account", account.Name, "error", err)
		}
		sources = append(sources, m.source(account.Name, data))
	}
	if len(sources) > 0 {
		m.publish(sources)
	}
	return errors.Join(failures...)
}

func (m *Manager) source(account string, data *xtream.Catalog) Source {
	source := Source{Account: account, Data: data}
	for _, configured := range m.accounts {
		if configured.Name == account {
			source.Sections = configured.Sections
		}
	}
	return source
}

func fetchUsable(ctx context.Context, account *accounts.Account) (*xtream.Catalog, error) {
	data, err := account.Client.FetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	for _, kind := range xtream.Kinds {
		if len(data.Streams[kind]) > 0 {
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s: the provider returned an empty catalog", account.Name)
}

func (m *Manager) publish(sources []Source) {
	m.current.Store(m.builder.Build(sources))
	if err := m.ids.Save(); err != nil {
		slog.Error("saving IDs", "error", err)
	}
}

func (m *Manager) Run(ctx context.Context) {
	if catalog := m.Current(); catalog == nil || time.Since(catalog.FetchedAt) > m.interval {
		m.syncAndLog(ctx)
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.syncAndLog(ctx)
		}
	}
}

func (m *Manager) syncAndLog(ctx context.Context) {
	if err := m.Sync(ctx); err != nil {
		slog.Warn("catalog sync finished with errors", "error", err)
	}
}

func (m *Manager) rawPath(account string) string {
	return filepath.Join(m.rawDir, account+".json.gz")
}

func (m *Manager) writeRaw(account string, data *xtream.Catalog) error {
	var buffer bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	if err := json.NewEncoder(writer).Encode(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return writeAtomic(m.rawPath(account), buffer.Bytes())
}

func (m *Manager) readRaw(account string) (*xtream.Catalog, error) {
	file, err := os.Open(m.rawPath(account))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	data := &xtream.Catalog{}
	if err := json.NewDecoder(reader).Decode(data); err != nil {
		return nil, err
	}
	return data, nil
}
