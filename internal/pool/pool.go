package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

type Status int

const (
	StatusBuffer Status = iota
	StatusReady
	StatusBlacklisted
)

type Entry struct {
	Proxy            *proxy.Proxy
	Status           Status
	UseCount         int
	MaxUse           int
	AddedAt          time.Time
	BlacklistedUntil time.Time
	LastUsed         time.Time

	LatencyEWMA      time.Duration
	LatencyVariance  time.Duration
	LatencyCount     int
	ConsecutiveFails int
	lastChecked      time.Time
}

func (e *Entry) Score() float64 {
	ewma := float64(e.LatencyEWMA)
	variance := float64(e.LatencyVariance)

	var score float64
	if ewma > 0 && e.LatencyCount > 1 {
		score = ewma * (1 + variance/ewma*2)
	} else {
		// Unmeasured or single-sample proxy: very poor finite score.
		// Keeping this finite lets request-time failures still push a fresh
		// proxy behind other fresh candidates instead of tying forever.
		score = math.MaxFloat64 / 1_000_000
	}

	// Confidence penalty: low-sample proxies get higher (worse) scores
	// to prefer established proxies over unknowns.
	if e.LatencyCount < 10 {
		score *= 1 + 1/float64(e.LatencyCount+1)
	}
	if e.ConsecutiveFails > 0 {
		penalty := 1 + float64(e.ConsecutiveFails)*10
		if penalty > 1_000 {
			penalty = 1_000
		}
		score *= penalty
	}
	return score
}

func (e *Entry) recordLatency(d time.Duration, alpha float64) {
	if e.LatencyCount == 0 {
		e.LatencyEWMA = d
		e.LatencyVariance = 0
	} else {
		oldEWMA := e.LatencyEWMA
		e.LatencyEWMA = time.Duration(alpha*float64(d) + (1-alpha)*float64(oldEWMA))
		dev := d - oldEWMA
		if dev < 0 {
			dev = -dev
		}
		n := float64(e.LatencyCount)
		e.LatencyVariance = time.Duration((n-1)/n*float64(e.LatencyVariance) + 1/n*float64(dev*dev))
	}
	e.LatencyCount++
	e.ConsecutiveFails = 0
}

type readyEntry struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Source   string `json:"source"`
	UseCount int    `json:"use_count"`
	MaxUse   int    `json:"max_use"`
	AddedAt  string `json:"added_at"`
	LastUsed string `json:"last_used"`

	// Blacklist fields (empty for ready entries)
	Status           string `json:"status,omitempty"`            // "ready" or "blacklisted"
	BlacklistedUntil string `json:"blacklisted_until,omitempty"` // RFC3339
}

type Pool struct {
	mu        sync.RWMutex
	buffer    []*Entry
	ready     []*Entry
	blacklist []*Entry
	known     map[string]bool
	notifyCh  chan struct{}

	minReady             int
	maxReady             int
	maxUse               int
	bufferMax            int
	blacklistTTL         time.Duration
	blacklistMax         int
	revalidateInterval   time.Duration
	latencyThreshold     time.Duration
	ewmaAlpha            float64
	consecutiveFailLimit int
	frontCheckCount      int

	stateFile  string
	bestProxy  atomic.Value // *Entry
	lastActive atomic.Value // time.Time
	lastSource string
}

func New(minReady, maxReady, maxUse, bufferMax int, blacklistTTL, revalidateInterval, latencyThreshold time.Duration, ewmaAlpha float64, consecutiveFailLimit, frontCheckCount int, stateFile string) *Pool {
	p := &Pool{
		minReady:             minReady,
		maxReady:             maxReady,
		maxUse:               maxUse,
		bufferMax:            bufferMax,
		blacklistTTL:         blacklistTTL,
		blacklistMax:         maxReady * 2, // cap blacklist at 2x ready pool
		revalidateInterval:   revalidateInterval,
		latencyThreshold:     latencyThreshold,
		ewmaAlpha:            ewmaAlpha,
		consecutiveFailLimit: consecutiveFailLimit,
		frontCheckCount:      frontCheckCount,
		stateFile:            stateFile,
		known:                make(map[string]bool),
		notifyCh:             make(chan struct{}, 1),
	}
	p.lastActive.Store(time.Now())
	if stateFile != "" {
		p.mu.Lock()
		p.load()
		p.mu.Unlock()
	}
	return p
}

func proxyKey(pr *proxy.Proxy) string {
	return fmt.Sprintf("%s:%d", pr.IP, pr.Port)
}

func (p *Pool) Feed(proxies []*proxy.Proxy) {
	var batch []*Entry
	now := time.Now()
	p.mu.Lock()
	for _, pr := range proxies {
		key := proxyKey(pr)
		if p.known[key] {
			continue
		}
		// SSRF protection: reject private/link-local IPs at ingestion
		if ip := net.ParseIP(pr.IP); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
			p.known[key] = true // mark as known so we don't retry
			continue
		}
		if len(p.buffer)+len(batch) >= p.bufferMax {
			break
		}
		p.known[key] = true
		batch = append(batch, &Entry{
			Proxy:   pr,
			Status:  StatusBuffer,
			MaxUse:  p.maxUse,
			AddedAt: now,
		})
	}
	if len(batch) > 0 {
		p.buffer = append(p.buffer, batch...)
		select {
		case p.notifyCh <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
}

func (p *Pool) Promote(e *Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.Status == StatusReady {
		return
	}
	if len(p.ready) >= p.maxReady {
		return // pool full
	}
	e.Status = StatusReady
	e.LastUsed = time.Now()
	p.ready = append(p.ready, e)
	select {
	case p.notifyCh <- struct{}{}:
	default:
	}
}

func (p *Pool) NextBuffer() *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.buffer) == 0 {
		return nil
	}
	e := p.buffer[0]
	p.buffer = p.buffer[1:]
	return e
}

// Forget removes a proxy from the known map so it can be re-fetched from sources.
func (p *Pool) Forget(pr *proxy.Proxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.known, proxyKey(pr))
}

func (p *Pool) acquireEntryLocked(match func(*Entry) bool) *Entry {
	if len(p.ready) == 0 {
		return nil
	}
	if best := p.bestProxy.Load(); best != nil {
		if e, ok := best.(*Entry); ok && p.isEligibleReadyEntryLocked(e, match) {
			return p.completeAcquireLocked(e)
		}
	}

	bySource := make(map[string][]*Entry)
	for _, e := range p.ready {
		if !p.isEligibleReadyEntryLocked(e, match) {
			continue
		}
		bySource[e.Proxy.Source] = append(bySource[e.Proxy.Source], e)
	}

	if len(bySource) == 0 {
		for _, e := range p.ready {
			if e.Status == StatusReady && (match == nil || match(e)) {
				return p.completeAcquireLocked(e)
			}
		}
		return nil
	}

	sources := make([]string, 0, len(bySource))
	for src := range bySource {
		sources = append(sources, src)
	}
	sort.Strings(sources)

	nextSrc := ""
	for i, src := range sources {
		if src > p.lastSource {
			nextSrc = src
			break
		}
		if i == len(sources)-1 {
			nextSrc = sources[0]
		}
	}

	candidates := bySource[nextSrc]
	var best *Entry
	bestScore := math.MaxFloat64
	for _, e := range candidates {
		s := e.Score()
		if s < bestScore {
			bestScore = s
			best = e
		}
	}

	if best == nil {
		return nil
	}

	p.lastSource = nextSrc
	return p.completeAcquireLocked(best)
}

func (p *Pool) isEligibleReadyEntryLocked(entry *Entry, match func(*Entry) bool) bool {
	if entry == nil || entry.Status != StatusReady || entry.UseCount >= entry.MaxUse {
		return false
	}
	if match != nil && !match(entry) {
		return false
	}
	return true
}

func (p *Pool) completeAcquireLocked(entry *Entry) *Entry {
	entry.UseCount++
	entry.LastUsed = time.Now()
	if entry.UseCount >= entry.MaxUse {
		p.blacklistEntry(entry)
	}
	return entry
}

func (p *Pool) AcquireEntryMatching(ctx context.Context, match func(*Entry) bool, onAcquire func(*Entry)) *Entry {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastActive.Store(time.Now())

	entry := p.acquireEntryLocked(match)
	if entry != nil && onAcquire != nil {
		onAcquire(entry)
	}
	return entry
}

func (p *Pool) AcquireEntry(ctx context.Context) *Entry {
	return p.AcquireEntryMatching(ctx, nil, nil)
}

func (p *Pool) CompleteSuccess(e *Entry, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.ConsecutiveFails = 0
	e.recordLatency(d, p.ewmaAlpha)
}

func (p *Pool) CompleteFailure(e *Entry) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.ConsecutiveFails++
	if best := p.bestProxy.Load(); best == e {
		p.bestProxy.Store((*Entry)(nil))
		p.lastSource = e.Proxy.Source
	}
	if p.consecutiveFailLimit > 0 && e.ConsecutiveFails >= p.consecutiveFailLimit {
		log.Printf("[DEBUG] [pool] evict %s (%d consecutive fails)", e.Proxy.Addr(), e.ConsecutiveFails)
		p.blacklistEntry(e)
		return true
	}
	return false
}

func (p *Pool) IsIdle(window time.Duration) bool {
	last := p.lastActive.Load().(time.Time)
	return time.Since(last) > window
}

func (p *Pool) Save() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.stateFile == "" {
		return
	}
	entries := make([]readyEntry, 0, len(p.ready)+len(p.blacklist))
	for _, e := range p.ready {
		entries = append(entries, readyEntry{
			IP:       e.Proxy.IP,
			Port:     e.Proxy.Port,
			Protocol: string(e.Proxy.Protocol),
			Source:   e.Proxy.Source,
			UseCount: e.UseCount,
			MaxUse:   e.MaxUse,
			AddedAt:  e.AddedAt.Format(time.RFC3339),
			LastUsed: e.LastUsed.Format(time.RFC3339),
			Status:   "ready",
		})
	}
	for _, e := range p.blacklist {
		entries = append(entries, readyEntry{
			IP:               e.Proxy.IP,
			Port:             e.Proxy.Port,
			Protocol:         string(e.Proxy.Protocol),
			Source:           e.Proxy.Source,
			UseCount:         e.UseCount,
			MaxUse:           e.MaxUse,
			AddedAt:          e.AddedAt.Format(time.RFC3339),
			LastUsed:         e.LastUsed.Format(time.RFC3339),
			Status:           "blacklisted",
			BlacklistedUntil: e.BlacklistedUntil.Format(time.RFC3339),
		})
	}
	if len(entries) == 0 {
		return
	}
	data, err := json.Marshal(entries)
	if err != nil {
		log.Printf("[ERROR] [pool] save: %v", err)
		return
	}
	if err := os.WriteFile(p.stateFile, data, 0644); err != nil {
		log.Printf("[ERROR] [pool] save: %v", err)
	}
	log.Printf("[INFO] [pool] saved %d entries (ready=%d blacklist=%d) to %s",
		len(entries), len(p.ready), len(p.blacklist), p.stateFile)
}

func (p *Pool) load() {
	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		return
	}
	var entries []readyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("[WARN] [pool] load state: %v", err)
		return
	}
	var readyCount, blacklistCount, bufferCount int
	now := time.Now()
	for _, re := range entries {
		pr := &proxy.Proxy{
			IP:       re.IP,
			Port:     re.Port,
			Protocol: proxy.Protocol(re.Protocol),
			Source:   re.Source,
		}
		// SSRF protection: reject private/link-local IPs from state file too
		if ip := net.ParseIP(pr.IP); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
			continue
		}
		key := proxyKey(pr)
		if p.known[key] {
			continue
		}
		p.known[key] = true

		e := &Entry{
			Proxy:    pr,
			UseCount: re.UseCount,
			MaxUse:   re.MaxUse,
		}

		switch re.Status {
		case "blacklisted":
			until, err := time.Parse(time.RFC3339, re.BlacklistedUntil)
			if err != nil || now.After(until) {
				if len(p.buffer) >= p.bufferMax {
					continue
				}
				e.Status = StatusBuffer
				e.UseCount = 0
				p.buffer = append(p.buffer, e)
				bufferCount++
			} else {
				e.Status = StatusBlacklisted
				e.BlacklistedUntil = until
				if p.blacklistMax > 0 && len(p.blacklist) >= p.blacklistMax {
					delete(p.known, key)
					continue
				}
				p.blacklist = append(p.blacklist, e)
				blacklistCount++
			}
		default:
			// "ready" or legacy entries without status field
			if len(p.ready) >= p.maxReady {
				continue
			}
			e.Status = StatusReady
			p.ready = append(p.ready, e)
			readyCount++
		}
	}
	log.Printf("[INFO] [pool] loaded %d entries (ready=%d buffer=%d blacklist=%d) from %s",
		len(entries), readyCount, bufferCount, blacklistCount, p.stateFile)
}

func (p *Pool) ReadyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.ready)
}

func (p *Pool) BufferCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.buffer)
}

func (p *Pool) BlacklistCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.blacklist)
}

func (p *Pool) NeedRefill() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.ready) < p.maxReady
}

// NotifyCh returns a channel that is signalled when the pool state changes
// (new candidates added to buffer, or entries promoted to ready).
func (p *Pool) NotifyCh() <-chan struct{} {
	return p.notifyCh
}

func (p *Pool) BufferNeedRefill() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.buffer) < p.bufferMax/2
}

// ReadyBySource counts ready proxies grouped by source name.
func (p *Pool) ReadyBySource() map[string]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	m := make(map[string]int)
	for _, e := range p.ready {
		m[e.Proxy.Source]++
	}
	return m
}

func (p *Pool) releaseExpired() {
	now := time.Now()
	keep := p.blacklist[:0]
	for _, e := range p.blacklist {
		if now.After(e.BlacklistedUntil) {
			if len(p.ready) >= p.maxReady {
				keep = append(keep, e) // delay release until room
				continue
			}
			e.UseCount = 0
			e.Status = StatusReady
			e.BlacklistedUntil = time.Time{}
			p.ready = append(p.ready, e)
		} else {
			keep = append(keep, e)
		}
	}
	// Enforce blacklist cap: drop oldest entries when over limit
	if p.blacklistMax > 0 && len(keep) > p.blacklistMax {
		drop := len(keep) - p.blacklistMax
		for _, e := range keep[:drop] {
			delete(p.known, proxyKey(e.Proxy))
		}
		keep = keep[drop:]
	}
	p.blacklist = keep
}

func (p *Pool) ReleaseExpired() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseExpired()
}

// blacklistEntry moves e from ready to blacklist. Caller must hold p.mu.
func (p *Pool) blacklistEntry(e *Entry) {
	if e.Status == StatusBlacklisted {
		return
	}
	e.Status = StatusBlacklisted
	e.BlacklistedUntil = time.Now().Add(p.blacklistTTL)
	p.blacklist = append(p.blacklist, e)
	for i, r := range p.ready {
		if r == e {
			last := len(p.ready) - 1
			p.ready[i] = p.ready[last]
			p.ready = p.ready[:last]
			break
		}
	}
	if best := p.bestProxy.Load(); best == e {
		p.bestProxy.Store((*Entry)(nil))
	}
}

// HealthCheck tests front proxies, scores, and runs full pool revalidation.
// fn is called without lock (may do network I/O). Return true = healthy.
func (p *Pool) HealthCheck(fn func(*Entry) bool) {
	type candidateCheck struct {
		entry           *Entry
		latencyEWMA     time.Duration
		latencyVariance time.Duration
		latencyCount    int
		lastChecked     time.Time
	}

	p.mu.Lock()
	n := p.frontCheckCount
	if n > len(p.ready) {
		n = len(p.ready)
	}
	candidates := make([]candidateCheck, n)
	for i := 0; i < n; i++ {
		e := p.ready[i]
		candidates[i] = candidateCheck{
			entry:           e,
			latencyEWMA:     e.LatencyEWMA,
			latencyVariance: e.LatencyVariance,
			latencyCount:    e.LatencyCount,
			lastChecked:     e.lastChecked,
		}
	}
	p.mu.Unlock()

	if len(candidates) == 0 {
		return
	}

	now := time.Now()
	var bestScored *Entry
	bestScore := math.MaxFloat64
	dead := make([]*Entry, 0, n)
	var updated []*Entry

	for _, cc := range candidates {
		e := cc.entry
		if p.latencyThreshold > 0 && cc.latencyCount >= 2 && cc.latencyEWMA > p.latencyThreshold {
			log.Printf("[DEBUG] [pool] health: slow %s (ewma=%v)", e.Proxy.Addr(), cc.latencyEWMA.Round(time.Millisecond))
			dead = append(dead, e)
			continue
		}
		if p.revalidateInterval > 0 && now.Sub(cc.lastChecked) > p.revalidateInterval {
			if fn != nil && !fn(e) {
				log.Printf("[DEBUG] [pool] health: dead %s (revalidate)", e.Proxy.Addr())
				dead = append(dead, e)
				continue
			}
			updated = append(updated, e)
		}
		cs := scoreFromFields(cc.latencyEWMA, cc.latencyVariance, cc.latencyCount)
		if cs < bestScore {
			bestScore = cs
			bestScored = e
		}
	}

	p.mu.Lock()
	for _, e := range updated {
		e.lastChecked = now
	}
	for _, e := range dead {
		p.blacklistEntry(e)
	}
	if bestScored != nil && bestScored.Status == StatusReady {
		p.bestProxy.Store(bestScored)
	}
	p.mu.Unlock()

	if p.revalidateInterval <= 0 {
		return
	}
	now = time.Now()
	var overdue []*Entry
	p.mu.RLock()
	for _, e := range p.ready {
		if now.Sub(e.lastChecked) > p.revalidateInterval {
			overdue = append(overdue, e)
		}
	}
	p.mu.RUnlock()

	var passed []*Entry
	var dead2 []*Entry
	for _, e := range overdue {
		if fn == nil || fn(e) {
			passed = append(passed, e)
		} else {
			dead2 = append(dead2, e)
		}
	}

	p.mu.Lock()
	for _, e := range passed {
		e.lastChecked = now
	}
	for _, e := range dead2 {
		p.blacklistEntry(e)
	}
	p.mu.Unlock()
}

func scoreFromFields(ewma, variance time.Duration, count int) float64 {
	if ewma <= 0 || count <= 1 {
		return math.MaxFloat64
	}
	e := float64(ewma)
	v := float64(variance)
	score := e * (1 + v/e*2)
	if count < 10 {
		score *= 1 + 1/float64(count+1)
	}
	return score
}

func (p *Pool) DumpStats() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return fmt.Sprintf("buffer=%d ready=%d blacklist=%d", len(p.buffer), len(p.ready), len(p.blacklist))
}
