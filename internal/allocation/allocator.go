package allocation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/user/randproxy/internal/pool"
)

var ErrNoProxyAvailable = errors.New("allocation: no proxy available")

type AcquireRequest struct {
	IsolationKey string
}

type Result struct {
	Success        bool
	ConnectLatency time.Duration
}

type Allocator interface {
	Acquire(ctx context.Context, req AcquireRequest) (*Lease, error)
}

type ActiveLeaseSnapshotter interface {
	ActiveLeaseSnapshot() ActiveLeaseSnapshot
}

type ActiveLeaseSnapshot struct {
	Total   int
	ByProxy map[string]int
}

type Lease struct {
	allocator    *defaultAllocator
	entry        *pool.Entry
	isolationKey string
	finishOnce   sync.Once
}

func (l *Lease) UpstreamAddr() string {
	if l == nil || l.entry == nil || l.entry.Proxy == nil {
		return ""
	}

	return l.entry.Proxy.Addr()
}

func (l *Lease) Finish(result Result) {
	if l == nil {
		return
	}

	l.finishOnce.Do(func() {
		l.allocator.finishLease(l, result)
	})
}

type defaultAllocator struct {
	pool               *pool.Pool
	mu                 sync.Mutex
	activeLeases       map[string]*leaseState
	policy             Policy
	randomIntn         func(int) int
	stableCursor       int
	lastBalancedSource string
}

type leaseState struct {
	total         int
	nonEmptyTotal int
	keys          map[string]int
}

func New(p *pool.Pool) Allocator {
	return NewWithOptions(p, Options{})
}

func (a *defaultAllocator) Acquire(ctx context.Context, req AcquireRequest) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	policy := a.currentPolicy()
	return a.acquireWithPolicy(ctx, req, policy)
}

func (a *defaultAllocator) isEntryEligibleForIsolation(entry *pool.Entry, isolationKey string) bool {
	if isolationKey == "" {
		return true
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	state, ok := a.activeLeases[entry.Proxy.Addr()]
	if !ok || state.total == 0 {
		return true
	}
	if state.nonEmptyTotal == state.keys[isolationKey] {
		return true
	}
	return false
}

func (a *defaultAllocator) trackActiveLease(lease *Lease) {
	a.mu.Lock()
	defer a.mu.Unlock()

	proxyAddr := lease.entry.Proxy.Addr()
	state, ok := a.activeLeases[proxyAddr]
	if !ok {
		state = &leaseState{}
		a.activeLeases[proxyAddr] = state
	}
	state.total++
	if lease.isolationKey != "" {
		state.nonEmptyTotal++
		if state.keys == nil {
			state.keys = make(map[string]int)
		}
		state.keys[lease.isolationKey]++
	}
}

func (a *defaultAllocator) ActiveLeaseSnapshot() ActiveLeaseSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	snapshot := ActiveLeaseSnapshot{ByProxy: make(map[string]int, len(a.activeLeases))}
	for proxyAddr, state := range a.activeLeases {
		if state.total <= 0 {
			continue
		}
		snapshot.ByProxy[proxyAddr] = state.total
		snapshot.Total += state.total
	}
	return snapshot
}

func (a *defaultAllocator) finishLease(lease *Lease, result Result) {
	if result.Success {
		a.pool.CompleteSuccess(lease.entry, result.ConnectLatency)
	} else {
		a.pool.CompleteFailure(lease.entry)
	}

	a.releaseActiveLease(lease)
}

func (a *defaultAllocator) releaseActiveLease(lease *Lease) {
	a.mu.Lock()
	defer a.mu.Unlock()

	proxyAddr := lease.entry.Proxy.Addr()
	state, ok := a.activeLeases[proxyAddr]
	if !ok {
		return
	}

	if state.total > 0 {
		state.total--
	}
	if lease.isolationKey != "" && state.keys != nil {
		if state.nonEmptyTotal > 0 {
			state.nonEmptyTotal--
		}
		state.keys[lease.isolationKey]--
		if state.keys[lease.isolationKey] <= 0 {
			delete(state.keys, lease.isolationKey)
		}
	}

	if state.total <= 0 {
		delete(a.activeLeases, proxyAddr)
	}
}
