package fetcher

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

// FetchStat tracks a single source's fetch metrics.
type FetchStat struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Fetched   int64  `json:"total_fetched"`
	Errors    int64  `json:"fetch_errors"`
	LastFetch string `json:"last_fetch"`
	LastError string `json:"last_error"`
}

type Fetcher struct {
	mu        sync.Mutex
	providers []providerConfig
	out       chan []*proxy.Proxy
	cancel    context.CancelFunc
	running   bool
	stats     map[string]*FetchStat
	enabled   map[string]bool

	// SkipCheck, if set, is called before each fetch tick.
	// If it returns true, the fetch is skipped (pool is full).
	SkipCheck func(name string) bool
}

type providerConfig struct {
	provider  proxy.Provider
	interval  time.Duration
	initDelay time.Duration
}

func New() *Fetcher {
	return &Fetcher{
		out:     make(chan []*proxy.Proxy, 64),
		stats:   make(map[string]*FetchStat),
		enabled: make(map[string]bool),
	}
}

func (f *Fetcher) Add(p proxy.Provider, interval, initDelay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		log.Printf("[WARN] [fetcher] already running, skipping provider %s", p.Name())
		return
	}
	enabled := f.sourceEnabledLocked(p.Name())
	f.enabled[p.Name()] = enabled
	f.providers = append(f.providers, providerConfig{provider: p, interval: interval, initDelay: initDelay})
	f.stats[p.Name()] = &FetchStat{Name: p.Name(), Enabled: enabled}
}

func (f *Fetcher) Out() <-chan []*proxy.Proxy { return f.out }

func (f *Fetcher) SetSourceEnabled(name string, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled[name] = enabled
	if stat := f.stats[name]; stat != nil {
		stat.Enabled = enabled
	}
}

func (f *Fetcher) ApplySourceStates(enabled map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	nextStates := make(map[string]bool, len(f.stats)+len(enabled))
	for name, stat := range f.stats {
		sourceEnabled := true
		if configured, ok := enabled[name]; ok {
			sourceEnabled = configured
		}
		nextStates[name] = sourceEnabled
		stat.Enabled = sourceEnabled
	}
	for name, sourceEnabled := range enabled {
		if _, known := nextStates[name]; known {
			continue
		}
		nextStates[name] = sourceEnabled
	}
	f.enabled = nextStates
}

// SourceStats returns a copy of all fetch stats.
func (f *Fetcher) SourceStats() []FetchStat {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FetchStat, 0, len(f.stats))
	for _, s := range f.stats {
		out = append(out, *s)
	}
	return out
}

func (f *Fetcher) Run(ctx context.Context) {
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return
	}
	f.running = true
	ctx, f.cancel = context.WithCancel(ctx)
	f.mu.Unlock()

	var wg sync.WaitGroup
	for _, pc := range f.providers {
		pc := pc
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.runProvider(ctx, pc)
		}()
	}
	wg.Wait()
	close(f.out)
}

func (f *Fetcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
	}
}

func (f *Fetcher) runProvider(ctx context.Context, pc providerConfig) {
	name := pc.provider.Name()
	log.Printf("[INFO] [fetcher] starting provider %q (interval=%v, delay=%v)", name, pc.interval, pc.initDelay)

	if pc.initDelay > 0 {
		select {
		case <-ctx.Done():
			log.Printf("[INFO] [fetcher] provider %q stopped: %v", name, ctx.Err())
			return
		case <-time.After(pc.initDelay):
		}
	}
	if f.sourceEnabled(name) {
		f.fetchAndSend(ctx, pc.provider)
	}
	ticker := time.NewTicker(pc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[INFO] [fetcher] provider %q stopped: %v", name, ctx.Err())
			return
		case <-ticker.C:
			if !f.sourceEnabled(name) {
				select {
				case <-ctx.Done():
					log.Printf("[INFO] [fetcher] provider %q stopped: %v", name, ctx.Err())
					return
				default:
				}
				continue
			}
			// When SkipCheck returns true, we must still check ctx before
			// re-entering the select loop — otherwise the goroutine can
			// leak forever if SkipCheck is permanently true (e.g. pool full).
			if f.SkipCheck != nil && f.SkipCheck(name) {
				select {
				case <-ctx.Done():
					log.Printf("[INFO] [fetcher] provider %q stopped: %v", name, ctx.Err())
					return
				default:
				}
				continue
			}
			f.fetchAndSend(ctx, pc.provider)
		}
	}
}

func (f *Fetcher) sourceEnabled(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourceEnabledLocked(name)
}

func (f *Fetcher) sourceEnabledLocked(name string) bool {
	enabled, ok := f.enabled[name]
	if !ok {
		return true
	}
	return enabled
}

func (f *Fetcher) fetchAndSend(ctx context.Context, p proxy.Provider) {
	proxies, err := p.Fetch(ctx)
	f.mu.Lock()
	s := f.stats[p.Name()]
	if s != nil {
		if err != nil {
			s.Errors++
			s.LastError = err.Error()
		} else {
			s.Fetched += int64(len(proxies))
			s.LastFetch = time.Now().Format("15:04:05")
		}
	}
	f.mu.Unlock()
	if err != nil {
		log.Printf("[ERROR] [fetcher] provider %q fetch error: %v", p.Name(), err)
		return
	}
	if len(proxies) == 0 {
		return
	}
	log.Printf("[INFO] [fetcher] provider %q fetched %d proxies", p.Name(), len(proxies))
	select {
	case f.out <- proxies:
	case <-ctx.Done():
	}
}
