package allocation

import (
	"context"
	"math/rand"
	"sort"

	"github.com/user/randproxy/internal/pool"
)

const (
	policyModeBalanced     = "balanced"
	policyModeRandomSubset = "random_subset"
	policyModeStableSubset = "stable_subset"
	policyModeSingleBest   = "single_best"
	defaultSubsetSize      = 3
)

type Policy struct {
	Mode             string
	RandomSubsetSize int
	StableSubsetSize int
}

type Options struct {
	Policy     Policy
	RandomIntn func(int) int
}

type Runtime interface {
	Allocator
	ApplyPolicy(policy Policy)
}

func NewWithOptions(p *pool.Pool, options Options) Runtime {
	allocator := &defaultAllocator{
		pool:         p,
		activeLeases: make(map[string]*leaseState),
		policy:       normalizePolicy(options.Policy),
		randomIntn:   options.RandomIntn,
	}
	if allocator.randomIntn == nil {
		allocator.randomIntn = rand.Intn
	}
	return allocator
}

func (a *defaultAllocator) ApplyPolicy(policy Policy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.policy = normalizePolicy(policy)
	a.stableCursor = 0
}

func (a *defaultAllocator) currentPolicy() Policy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy
}

func (a *defaultAllocator) acquireWithPolicy(ctx context.Context, req AcquireRequest, policy Policy) (*Lease, error) {
	match := func(entry *pool.Entry) bool {
		return a.isEntryEligibleForIsolation(entry, req.IsolationKey)
	}

	for range 2 {
		orderedEligible := a.pool.OrderedEligibleEntriesMatching(match)
		selected := a.selectPolicyEntry(orderedEligible, policy)
		if selected == nil {
			break
		}

		var lease *Lease
		entry := a.pool.AcquireSpecificEntry(ctx, selected, match, func(acquired *pool.Entry) {
			lease = &Lease{
				allocator:    a,
				entry:        acquired,
				isolationKey: req.IsolationKey,
			}
			a.trackActiveLease(lease)
		})
		if entry == nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		if lease == nil {
			lease = &Lease{
				allocator:    a,
				entry:        entry,
				isolationKey: req.IsolationKey,
			}
			a.trackActiveLease(lease)
		}
		a.recordPolicySelection(policy, lease.entry)
		return lease, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNoProxyAvailable
}

func (a *defaultAllocator) selectPolicyEntry(orderedEligible []*pool.Entry, policy Policy) *pool.Entry {
	if len(orderedEligible) == 0 {
		return nil
	}

	switch policy.Mode {
	case policyModeBalanced:
		return a.selectBalancedEntry(orderedEligible)
	case policyModeSingleBest:
		return orderedEligible[0]
	case policyModeRandomSubset:
		subset := capSubset(orderedEligible, policy.RandomSubsetSize)
		return subset[a.randomIntn(len(subset))]
	case policyModeStableSubset:
		subset := capSubset(orderedEligible, policy.StableSubsetSize)
		return subset[a.nextStableIndex(len(subset))]
	default:
		return orderedEligible[0]
	}
}

func (a *defaultAllocator) selectBalancedEntry(orderedEligible []*pool.Entry) *pool.Entry {
	sources := distinctSourcesByName(orderedEligible)
	if len(sources) == 0 {
		return nil
	}

	selectedSource := sources[0]
	lastSource := a.currentBalancedSource()
	for idx, source := range sources {
		if source != lastSource {
			continue
		}
		selectedSource = sources[(idx+1)%len(sources)]
		break
	}

	for _, entry := range orderedEligible {
		if entry.Proxy.Source == selectedSource {
			return entry
		}
	}
	return nil
}

func distinctSourcesByName(entries []*pool.Entry) []string {
	seen := make(map[string]struct{}, len(entries))
	sources := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Proxy == nil {
			continue
		}
		source := entry.Proxy.Source
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return sources
}

func (a *defaultAllocator) currentBalancedSource() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastBalancedSource
}

func (a *defaultAllocator) recordPolicySelection(policy Policy, entry *pool.Entry) {
	if entry == nil || entry.Proxy == nil {
		return
	}
	if policy.Mode != policyModeBalanced {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastBalancedSource = entry.Proxy.Source
}

func (a *defaultAllocator) nextStableIndex(size int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := a.stableCursor % size
	a.stableCursor++
	return idx
}

func capSubset(entries []*pool.Entry, subsetSize int) []*pool.Entry {
	if subsetSize <= 0 || subsetSize >= len(entries) {
		return entries
	}
	return entries[:subsetSize]
}

func normalizePolicy(policy Policy) Policy {
	normalized := policy
	if normalized.Mode == "" {
		normalized.Mode = policyModeBalanced
	}
	if normalized.RandomSubsetSize <= 0 {
		normalized.RandomSubsetSize = defaultSubsetSize
	}
	if normalized.StableSubsetSize <= 0 {
		normalized.StableSubsetSize = defaultSubsetSize
	}
	return normalized
}
