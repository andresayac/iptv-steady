package relay

import (
	"context"
	"fmt"
	"slices"
	"time"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/config"
	"iptv-steady/internal/events"
	"iptv-steady/internal/ttlcache"
	"iptv-steady/internal/xtream"
)

// Source is one account's copy of a title.
type Source struct {
	Account   string
	ID        string
	Extension string
}

type Media struct {
	Kind    xtream.Kind
	TitleID int64
	Title   string
	Sources []Source
}

type Viewer struct {
	User   string
	Player string
}

type Recorder interface {
	Record(events.Event)
}

type binding struct {
	source Source
	total  int64
}

type Engine struct {
	file     config.File
	live     config.Live
	pool     *accounts.Pool
	recorder Recorder
	bindings *ttlcache.Cache[binding]
	dead     *ttlcache.Cache[struct{}]
	sizes    *ttlcache.Cache[int64]
}

func NewEngine(file config.File, live config.Live, pool *accounts.Pool, recorder Recorder) *Engine {
	return &Engine{
		file:     file,
		live:     live,
		pool:     pool,
		recorder: recorder,
		bindings: ttlcache.New[binding](file.CacheEntries),
		dead:     ttlcache.New[struct{}](file.CacheEntries),
		sizes:    ttlcache.New[int64](file.CacheEntries),
	}
}

func sourceKey(kind xtream.Kind, source Source) string {
	return fmt.Sprintf("%s|%s|%s", kind, source.Account, source.ID)
}

func (e *Engine) markDead(kind xtream.Kind, source Source) {
	e.dead.Set(sourceKey(kind, source), struct{}{}, e.file.DeadLinkTTL)
}

func (e *Engine) rememberSize(kind xtream.Kind, source Source, total int64) {
	if total >= 0 {
		e.sizes.Set(sourceKey(kind, source), total, e.file.StickyTTL)
	}
}

// holdsOtherFile saves asking a provider again for a copy already seen with a different size.
func (e *Engine) holdsOtherFile(kind xtream.Kind, source Source, total int64) bool {
	known, found := e.sizes.Get(sourceKey(kind, source))
	return found && total >= 0 && known != total
}

// ordered puts healthy accounts first, then busy, then failing ones, and known dead links last.
func (e *Engine) ordered(media Media, preferred *Source) []Source {
	var ready, busy, blocked, dead []Source
	for _, source := range media.Sources {
		account := e.pool.Get(source.Account)
		if account == nil {
			continue
		}
		_, isDead := e.dead.Get(sourceKey(media.Kind, source))
		switch {
		case isDead:
			dead = append(dead, source)
		case account.Blocked():
			blocked = append(blocked, source)
		case account.Full():
			busy = append(busy, source)
		default:
			ready = append(ready, source)
		}
	}
	order := slices.Concat(ready, busy, blocked, dead)
	if preferred != nil {
		if at := slices.Index(order, *preferred); at > 0 {
			order = slices.Insert(slices.Delete(order, at, at+1), 0, *preferred)
		}
	}
	return order
}

func (e *Engine) streamURL(kind xtream.Kind, source Source, extension string) string {
	return e.pool.Get(source.Account).Client.StreamURL(kind, source.ID, extension)
}

func sleep(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func delayFor(delays []time.Duration, attempt int) time.Duration {
	return delays[min(attempt, len(delays)-1)]
}

// playback carries what every event of a stream has in common.
type playback struct {
	engine *Engine
	viewer Viewer
	media  Media
}

func (p *playback) record(event events.Event) {
	event.User, event.Player = p.viewer.User, p.viewer.Player
	event.Media, event.TitleID, event.Title = string(p.media.Kind), p.media.TitleID, p.media.Title
	p.engine.recorder.Record(event)
}

func (p *playback) recordServed(account string, bytes int64) {
	if bytes > 0 {
		p.record(events.Event{Kind: events.Served, Account: account, Bytes: bytes})
	}
}
