package pool

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type PoolCounts struct {
	Ready     int
	Buffer    int
	Blacklist int
}

type ProxyActionResult struct {
	ProxyID string
	Action  string
	Outcome string
	Counts  PoolCounts
}

type ProxyActionNotFoundError struct {
	Action  string
	ProxyID string
}

type ProxyActionCapacityError struct {
	Action   string
	ProxyID  string
	PoolName string
}

func (e *ProxyActionNotFoundError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Action, proxyActionNotFoundMessage, e.ProxyID)
}

func (e *ProxyActionNotFoundError) ErrorMessage() string {
	return proxyActionNotFoundMessage
}

func (e *ProxyActionCapacityError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Action, e.ErrorMessage(), e.ProxyID)
}

func (e *ProxyActionCapacityError) ErrorCode() string {
	switch e.PoolName {
	case "ready":
		return "ready_pool_full"
	case "buffer":
		return "buffer_pool_full"
	default:
		return "pool_full"
	}
}

func (e *ProxyActionCapacityError) ErrorMessage() string {
	switch e.PoolName {
	case "ready":
		return "ready pool is at capacity"
	case "buffer":
		return "buffer pool is at capacity"
	default:
		return "pool is at capacity"
	}
}

const proxyActionNotFoundMessage = "proxy id was not found in pool inventory"

func (p *Pool) OrderedEligibleEntriesMatching(match func(*Entry) bool) []*Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	eligible := make([]*Entry, 0, len(p.ready))
	for _, entry := range p.ready {
		if p.isEligibleReadyEntryLocked(entry, match) {
			eligible = append(eligible, entry)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		left := eligible[i]
		right := eligible[j]
		leftScore := left.Score()
		rightScore := right.Score()
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		if left.Proxy.Source != right.Proxy.Source {
			return left.Proxy.Source < right.Proxy.Source
		}
		return left.Proxy.Addr() < right.Proxy.Addr()
	})
	return eligible
}

func (p *Pool) AcquireSpecificEntry(ctx context.Context, target *Entry, match func(*Entry) bool, onAcquire func(*Entry)) *Entry {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil
		}
	}
	if target == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastActive.Store(time.Now())
	if !p.isEligibleReadyEntryLocked(target, match) {
		return nil
	}
	entry := p.completeAcquireLocked(target)
	if entry != nil && onAcquire != nil {
		onAcquire(entry)
	}
	return entry
}

func (p *Pool) ApplyMaxUse(maxUse int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxUse = maxUse

	for _, entry := range p.buffer {
		entry.MaxUse = maxUse
	}
	for _, entry := range p.blacklist {
		entry.MaxUse = maxUse
	}
	for _, entry := range p.ready {
		entry.MaxUse = maxUse
	}
	for i := len(p.ready) - 1; i >= 0; i-- {
		entry := p.ready[i]
		if entry.UseCount >= entry.MaxUse {
			p.blacklistEntry(entry)
		}
	}
}

func (p *Pool) ApplyBlacklistTTL(blacklistTTL time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blacklistTTL = blacklistTTL
}

func (p *Pool) ManualBlacklist(proxyID string) (ProxyActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry := p.findEntryByProxyIDLocked(p.ready, proxyID)
	if entry == nil {
		return ProxyActionResult{}, newProxyActionNotFoundError("blacklist", proxyID)
	}

	p.blacklistEntry(entry)
	return p.newProxyActionResultLocked(proxyID, "blacklist", "blacklisted"), nil
}

func (p *Pool) ManualRevalidate(proxyID string) (ProxyActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry := p.findEntryByProxyIDLocked(p.ready, proxyID)
	if entry == nil {
		entry = p.findEntryByProxyIDLocked(p.blacklist, proxyID)
		if entry == nil {
			return ProxyActionResult{}, newProxyActionNotFoundError("revalidate", proxyID)
		}
	}
	if len(p.buffer) >= p.bufferMax {
		return ProxyActionResult{}, newProxyActionCapacityError("revalidate", proxyID, "buffer")
	}
	if entry.Status == StatusReady {
		p.removeReadyEntryLocked(entry)
	} else {
		p.removeBlacklistEntryLocked(entry)
	}

	entry.Status = StatusBuffer
	entry.UseCount = 0
	entry.BlacklistedUntil = time.Time{}
	p.buffer = append(p.buffer, entry)
	select {
	case p.notifyCh <- struct{}{}:
	default:
	}

	return p.newProxyActionResultLocked(proxyID, "revalidate", "accepted_for_revalidation"), nil
}

func (p *Pool) ManualRelease(proxyID string) (ProxyActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry := p.findEntryByProxyIDLocked(p.blacklist, proxyID)
	if entry == nil {
		return ProxyActionResult{}, newProxyActionNotFoundError("release", proxyID)
	}
	if len(p.ready) >= p.maxReady {
		return ProxyActionResult{}, newProxyActionCapacityError("release", proxyID, "ready")
	}

	p.removeBlacklistEntryLocked(entry)
	entry.Status = StatusReady
	entry.UseCount = 0
	entry.BlacklistedUntil = time.Time{}
	entry.LastUsed = time.Now()
	p.ready = append(p.ready, entry)
	select {
	case p.notifyCh <- struct{}{}:
	default:
	}

	return p.newProxyActionResultLocked(proxyID, "release", "released_from_blacklist"), nil
}

func (p *Pool) findEntryByProxyIDLocked(entries []*Entry, proxyID string) *Entry {
	for _, entry := range entries {
		if entry.Proxy != nil && entry.Proxy.Addr() == proxyID {
			return entry
		}
	}
	return nil
}

func (p *Pool) removeReadyEntryLocked(target *Entry) {
	p.ready = removeEntryFromPoolEntriesLocked(p.ready, target)
	if best := p.bestProxy.Load(); best == target {
		p.bestProxy.Store((*Entry)(nil))
	}
}

func (p *Pool) removeBlacklistEntryLocked(target *Entry) {
	p.blacklist = removeEntryFromPoolEntriesLocked(p.blacklist, target)
}

func removeEntryFromPoolEntriesLocked(entries []*Entry, target *Entry) []*Entry {
	for i, entry := range entries {
		if entry != target {
			continue
		}
		last := len(entries) - 1
		entries[i] = entries[last]
		return entries[:last]
	}
	return entries
}

func (p *Pool) newProxyActionResultLocked(proxyID string, action string, receipt string) ProxyActionResult {
	return ProxyActionResult{
		ProxyID: proxyID,
		Action:  action,
		Outcome: receipt,
		Counts: PoolCounts{
			Ready:     len(p.ready),
			Buffer:    len(p.buffer),
			Blacklist: len(p.blacklist),
		},
	}
}

func newProxyActionNotFoundError(action string, proxyID string) error {
	return &ProxyActionNotFoundError{
		Action:  action,
		ProxyID: proxyID,
	}
}

func newProxyActionCapacityError(action string, proxyID string, poolName string) error {
	return &ProxyActionCapacityError{
		Action:   action,
		ProxyID:  proxyID,
		PoolName: poolName,
	}
}
