package allocation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
)

func newScoredReadyEntry(ip string, port int, source string, maxUse int, latency time.Duration, variance time.Duration) *pool.Entry {
	entry := newReadyEntry(ip, port, source, maxUse)
	entry.LatencyEWMA = latency
	entry.LatencyVariance = variance
	entry.LatencyCount = 3
	return entry
}

func TestAllocatorAcquire_usesDifferentUpstreamForDifferentIsolationKeys_whenAlternativeExists(t *testing.T) {
	// Given
	p := newTestPool(10)
	faster := newScoredReadyEntry("203.0.113.40", 1080, "alpha", 10, 50*time.Millisecond, 5*time.Millisecond)
	slower := newScoredReadyEntry("203.0.113.41", 1080, "alpha", 10, 200*time.Millisecond, 25*time.Millisecond)
	p.Promote(faster)
	p.Promote(slower)
	allocator := New(p).(*defaultAllocator)

	firstLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer firstLease.Finish(Result{Success: false})

	// When
	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-b"})

	// Then
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	defer secondLease.Finish(Result{Success: false})
	if firstLease.UpstreamAddr() == secondLease.UpstreamAddr() {
		t.Fatalf("different keys reused %s, want allocator to choose a different active upstream", firstLease.UpstreamAddr())
	}
	if secondLease.entry != slower {
		t.Fatalf("second lease = %s, want %s", secondLease.UpstreamAddr(), slower.Proxy.Addr())
	}
}

func TestAllocatorAcquire_reusesActiveUpstreamForSameIsolationKey_whenNoAlternativeExists(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.42", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)

	firstLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer firstLease.Finish(Result{Success: false})

	// When
	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})

	// Then
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	defer secondLease.Finish(Result{Success: false})
	if secondLease.UpstreamAddr() != firstLease.UpstreamAddr() {
		t.Fatalf("same key got %s, want %s", secondLease.UpstreamAddr(), firstLease.UpstreamAddr())
	}
}

func TestAllocatorAcquire_sharesActiveUpstreamForEmptyIsolationKey_whenNonEmptyKeyHoldsLease(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.43", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)

	keyedLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("keyed acquire: %v", err)
	}
	defer keyedLease.Finish(Result{Success: false})

	// When
	emptyLease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if err != nil {
		t.Fatalf("empty-key acquire: %v", err)
	}
	defer emptyLease.Finish(Result{Success: false})
	if emptyLease.UpstreamAddr() != keyedLease.UpstreamAddr() {
		t.Fatalf("empty key got %s, want shared upstream %s", emptyLease.UpstreamAddr(), keyedLease.UpstreamAddr())
	}
}

func TestAllocatorAcquire_respectsQuotaAndCooldownGates_whenIsolationBlocksActiveUpstream(t *testing.T) {
	// Given
	p := newTestPool(10)
	activeEntry := newScoredReadyEntry("203.0.113.44", 1080, "beta", 10, 50*time.Millisecond, 5*time.Millisecond)
	blacklistedEntry := newScoredReadyEntry("203.0.113.45", 1080, "alpha", 10, 150*time.Millisecond, 15*time.Millisecond)
	p.Promote(activeEntry)
	p.Promote(blacklistedEntry)
	blacklistedEntry.UseCount = blacklistedEntry.MaxUse - 1
	setupAllocator := New(p).(*defaultAllocator)
	leaseToBlacklist, err := setupAllocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("setup acquire to blacklist alternative: %v", err)
	}
	leaseToBlacklist.Finish(Result{Success: true, ConnectLatency: 10 * time.Millisecond})
	if blacklistedEntry.Status != pool.StatusBlacklisted {
		t.Fatalf("setup status = %v, want %v", blacklistedEntry.Status, pool.StatusBlacklisted)
	}
	blacklistedUseCount := blacklistedEntry.UseCount
	allocator := New(p).(*defaultAllocator)

	activeLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("active lease acquire: %v", err)
	}
	defer activeLease.Finish(Result{Success: false})

	// When
	blockedLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-b"})

	// Then
	if blockedLease != nil {
		t.Fatalf("expected nil lease when only alternative is blacklisted, got %#v", blockedLease)
	}
	if !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("expected ErrNoProxyAvailable, got %v", err)
	}
	if blacklistedEntry.UseCount != blacklistedUseCount {
		t.Fatalf("blacklisted use count = %d, want unchanged %d", blacklistedEntry.UseCount, blacklistedUseCount)
	}
}

func TestAllocatorAcquire_respectsHealthGate_whenIsolationBlocksActiveUpstream(t *testing.T) {
	// Given
	p := pool.New(1, 10, 10, 10, time.Hour, time.Nanosecond, 5*time.Second, 0.1, 3, 5, "")
	activeEntry := newScoredReadyEntry("203.0.113.46", 1080, "alpha", 10, 50*time.Millisecond, 5*time.Millisecond)
	unhealthyEntry := newScoredReadyEntry("203.0.113.47", 1080, "alpha", 10, 150*time.Millisecond, 15*time.Millisecond)
	p.Promote(activeEntry)
	p.Promote(unhealthyEntry)
	p.HealthCheck(func(entry *pool.Entry) bool {
		return entry != unhealthyEntry
	})
	allocator := New(p).(*defaultAllocator)

	activeLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("active lease acquire: %v", err)
	}
	defer activeLease.Finish(Result{Success: false})

	// When
	blockedLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-b"})

	// Then
	if blockedLease != nil {
		t.Fatalf("expected nil lease when only alternative is unhealthy, got %#v", blockedLease)
	}
	if !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("expected ErrNoProxyAvailable, got %v", err)
	}
	if unhealthyEntry.Status != pool.StatusBlacklisted {
		t.Fatalf("unhealthy status = %v, want %v", unhealthyEntry.Status, pool.StatusBlacklisted)
	}
}

func TestAllocatorAcquire_failsFastForDifferentIsolationKey_whenAllEligibleUpstreamsAreBusy(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.48", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)

	activeLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("active lease acquire: %v", err)
	}
	defer activeLease.Finish(Result{Success: false})

	// When
	blockedLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-b"})

	// Then
	if blockedLease != nil {
		t.Fatalf("expected nil lease, got %#v", blockedLease)
	}
	if !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("expected ErrNoProxyAvailable, got %v", err)
	}
}

func TestLeaseFinish_releasesIsolationStateAfterConcurrentFinishCalls(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.49", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-a"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	finish := func() {
		defer wg.Done()
		<-start
		lease.Finish(Result{Success: false})
	}

	wg.Add(2)
	go finish()
	go finish()

	// When
	close(start)
	wg.Wait()
	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{IsolationKey: "key-b"})

	// Then
	if err != nil {
		t.Fatalf("reacquire after concurrent finish: %v", err)
	}
	defer secondLease.Finish(Result{Success: false})
	if secondLease.UpstreamAddr() != entry.Proxy.Addr() {
		t.Fatalf("reacquired %s, want %s", secondLease.UpstreamAddr(), entry.Proxy.Addr())
	}
	if got := activeLeaseCount(t, allocator); got != 1 {
		t.Fatalf("active leases = %d, want 1 after reacquire", got)
	}
}
