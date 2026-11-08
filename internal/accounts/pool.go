package accounts

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"iptv-steady/internal/config"
	"iptv-steady/internal/xtream"
)

type Health struct {
	Status    xtream.Status
	CheckedAt time.Time
	Err       string
}

// Account tracks one provider login: open streams, health and a circuit breaker.
type Account struct {
	Name   string
	Client *xtream.Client

	configuredMax   int
	breakerFailures int32
	breakerCooldown time.Duration

	open         atomic.Int32
	health       atomic.Pointer[Health]
	failures     atomic.Int32
	blockedUntil atomic.Int64
}

func (a *Account) Acquire() func() {
	a.open.Add(1)
	return sync.OnceFunc(func() { a.open.Add(-1) })
}

func (a *Account) Open() int { return int(a.open.Load()) }

func (a *Account) limit() int {
	if a.configuredMax > 0 {
		return a.configuredMax
	}
	if health := a.health.Load(); health != nil {
		return health.Status.Max
	}
	return 0
}

func (a *Account) Full() bool {
	limit := a.limit()
	return limit > 0 && a.Open() >= limit
}

func (a *Account) Blocked() bool {
	return time.Now().UnixNano() < a.blockedUntil.Load()
}

func (a *Account) Failed() {
	if a.failures.Add(1) >= a.breakerFailures {
		a.failures.Store(0)
		a.blockedUntil.Store(time.Now().Add(a.breakerCooldown).UnixNano())
	}
}

func (a *Account) Succeeded() { a.failures.Store(0) }

func (a *Account) Health() *Health { return a.health.Load() }

func (a *Account) refresh(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	status, err := a.Client.Status(ctx)
	health := &Health{Status: status, CheckedAt: time.Now().UTC()}
	if err != nil {
		health.Err = err.Error()
		if previous := a.health.Load(); previous != nil {
			health.Status = previous.Status
		}
	}
	a.health.Store(health)
	return err
}

type Pool struct {
	upstream config.Upstream
	stream   *http.Client
	api      *http.Client
	accounts []*Account
	byName   map[string]*Account
}

// Stream connections are abandoned mid-body, so they are never reused or compressed.
func newTransport(cfg config.Upstream, headerTimeout time.Duration, reusable bool) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: cfg.TCPKeepAlive}).DialContext,
		TLSHandshakeTimeout:   cfg.TLSTimeout,
		ResponseHeaderTimeout: headerTimeout,
		DisableCompression:    !reusable,
		DisableKeepAlives:     !reusable,
	}
}

func NewPool(cfg *config.Config) *Pool {
	api := &http.Client{Timeout: cfg.Upstream.CatalogTimeout, Transport: newTransport(cfg.Upstream, cfg.Upstream.CatalogHeaderTimeout, true)}
	pool := &Pool{
		upstream: cfg.Upstream,
		stream:   &http.Client{Transport: newTransport(cfg.Upstream, cfg.Upstream.HeaderTimeout, false)},
		api:      api,
		byName:   map[string]*Account{},
	}
	for _, a := range cfg.Accounts {
		credentials := xtream.Credentials{Name: a.Name, BaseURL: a.URL, Username: a.Username, Password: a.Password}
		account := &Account{
			Name:            a.Name,
			Client:          xtream.NewClient(credentials, cfg.Upstream.UserAgent, api),
			configuredMax:   a.MaxConnections,
			breakerFailures: int32(cfg.Upstream.BreakerFailures),
			breakerCooldown: cfg.Upstream.BreakerCooldown,
		}
		pool.accounts = append(pool.accounts, account)
		pool.byName[a.Name] = account
	}
	return pool
}

func (p *Pool) Get(name string) *Account { return p.byName[name] }

func (p *Pool) All() []*Account { return p.accounts }

func (p *Pool) StreamClient() *http.Client { return p.stream }

func (p *Pool) APIClient() *http.Client { return p.api }

func (p *Pool) UserAgent() string { return p.upstream.UserAgent }

// Run keeps account status fresh, waiting longer after each failure so a provider that is down or rate limiting is not pressed further.
func (p *Pool) Run(ctx context.Context) {
	var group sync.WaitGroup
	for _, account := range p.accounts {
		group.Go(func() { p.watch(ctx, account) })
	}
	group.Wait()
}

func (p *Pool) watch(ctx context.Context, account *Account) {
	wait := p.upstream.StatusInterval
	for {
		if err := account.refresh(ctx, p.upstream.StatusTimeout); err != nil {
			slog.Warn("account check failed", "error", err)
			wait = min(wait*2, p.upstream.StatusMaxBackoff)
		} else {
			wait = p.upstream.StatusInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
