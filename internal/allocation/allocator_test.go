package allocation

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
	proxycore "github.com/user/randproxy/internal/proxy"
)

func newTestPool(maxUse int) *pool.Pool {
	return pool.New(1, 10, maxUse, 10, time.Hour, time.Hour, 5*time.Second, 0.1, 3, 5, "")
}

func newReadyEntry(ip string, port int, source string, maxUse int) *pool.Entry {
	return &pool.Entry{
		Proxy: &proxycore.Proxy{
			IP:       ip,
			Port:     port,
			Protocol: proxycore.ProtocolSOCKS5,
			Source:   source,
		},
		Status:          pool.StatusBuffer,
		MaxUse:          maxUse,
		AddedAt:         time.Now(),
		LatencyEWMA:     100 * time.Millisecond,
		LatencyVariance: 10 * time.Millisecond,
		LatencyCount:    2,
	}
}

func activeLeaseCount(t *testing.T, allocator *defaultAllocator) int {
	t.Helper()

	allocator.mu.Lock()
	defer allocator.mu.Unlock()

	total := 0
	for _, leaseState := range allocator.activeLeases {
		total += leaseState.total
	}
	return total
}

func TestAllocatorAcquire_returnsNoProxyAvailable_whenReadyIsEmpty(t *testing.T) {
	// Given
	allocator := New(newTestPool(10)).(*defaultAllocator)

	// When
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if lease != nil {
		t.Fatalf("expected nil lease, got %#v", lease)
	}
	if !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("expected ErrNoProxyAvailable, got %v", err)
	}
	if got := activeLeaseCount(t, allocator); got != 0 {
		t.Fatalf("active leases = %d, want 0", got)
	}
}

func TestAllocatorAcquire_blacklistsEntry_whenEntryHitsMaxUse(t *testing.T) {
	// Given
	p := newTestPool(2)
	entry := newReadyEntry("203.0.113.10", 1080, "alpha", 2)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)

	// When
	firstLease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	firstLease.Finish(Result{Success: true, ConnectLatency: 50 * time.Millisecond})

	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	secondLease.Finish(Result{Success: true, ConnectLatency: 50 * time.Millisecond})

	thirdLease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if entry.UseCount != 2 {
		t.Fatalf("use count = %d, want 2", entry.UseCount)
	}
	if entry.Status != pool.StatusBlacklisted {
		t.Fatalf("status = %v, want %v", entry.Status, pool.StatusBlacklisted)
	}
	if p.ReadyCount() != 0 {
		t.Fatalf("ready count = %d, want 0", p.ReadyCount())
	}
	if p.BlacklistCount() != 1 {
		t.Fatalf("blacklist count = %d, want 1", p.BlacklistCount())
	}
	if thirdLease != nil {
		t.Fatalf("expected nil lease after blacklist, got %#v", thirdLease)
	}
	if !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("expected ErrNoProxyAvailable after blacklist, got %v", err)
	}
}

func TestAllocatorAcquire_rotatesAcrossEligibleSources_whenMultipleSourcesRemainEligible(t *testing.T) {
	// Given
	p := newTestPool(10)
	p.Promote(newReadyEntry("203.0.113.1", 1080, "alpha", 10))
	p.Promote(newReadyEntry("203.0.113.2", 1080, "beta", 10))
	allocator := New(p).(*defaultAllocator)

	// When
	gotSources := make([]string, 0, 3)
	for range 3 {
		lease, err := allocator.Acquire(context.Background(), AcquireRequest{})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		gotSources = append(gotSources, lease.entry.Proxy.Source)
		lease.Finish(Result{Success: true, ConnectLatency: 50 * time.Millisecond})
	}

	// Then
	wantSources := []string{"alpha", "beta", "alpha"}
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("sources = %v, want %v", gotSources, wantSources)
	}
}

func TestAllocatorAcquire_prefersLowerScoreWithinChosenSource(t *testing.T) {
	// Given
	p := newTestPool(10)
	slower := newReadyEntry("203.0.113.20", 1080, "alpha", 10)
	slower.LatencyEWMA = 250 * time.Millisecond
	slower.LatencyVariance = 30 * time.Millisecond
	slower.LatencyCount = 3

	faster := newReadyEntry("203.0.113.21", 1080, "alpha", 10)
	faster.LatencyEWMA = 50 * time.Millisecond
	faster.LatencyVariance = 5 * time.Millisecond
	faster.LatencyCount = 3

	beta := newReadyEntry("203.0.113.22", 1080, "beta", 10)
	beta.LatencyEWMA = 80 * time.Millisecond
	beta.LatencyVariance = 5 * time.Millisecond
	beta.LatencyCount = 3

	p.Promote(slower)
	p.Promote(faster)
	p.Promote(beta)
	allocator := New(p).(*defaultAllocator)

	// When
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.entry != faster {
		t.Fatalf("selected proxy = %s, want %s", lease.entry.Proxy.Addr(), faster.Proxy.Addr())
	}
	if faster.UseCount != 1 {
		t.Fatalf("faster use count = %d, want 1", faster.UseCount)
	}
	if slower.UseCount != 0 {
		t.Fatalf("slower use count = %d, want 0", slower.UseCount)
	}
}

func TestLeaseFinish_recordsCompletionOnce_whenCalledRepeatedly(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.30", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// When
	result := Result{Success: true, ConnectLatency: 42 * time.Millisecond}
	lease.Finish(result)
	lease.Finish(result)

	// Then
	if entry.LatencyCount != 3 {
		t.Fatalf("latency count = %d, want 3", entry.LatencyCount)
	}
	if got := activeLeaseCount(t, allocator); got != 0 {
		t.Fatalf("active leases = %d, want 0", got)
	}
}

func TestActiveLeaseSnapshotReportsCounts_whenLeasesAreOpenAndFinished(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.40", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)
	firstLease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	// When
	openSnapshot := allocator.ActiveLeaseSnapshot()
	firstLease.Finish(Result{Success: true, ConnectLatency: 50 * time.Millisecond})
	firstLease.Finish(Result{Success: true, ConnectLatency: 50 * time.Millisecond})
	partialSnapshot := allocator.ActiveLeaseSnapshot()
	secondLease.Finish(Result{Success: false})
	closedSnapshot := allocator.ActiveLeaseSnapshot()

	// Then
	proxyID := entry.Proxy.Addr()
	if openSnapshot.Total != 2 || openSnapshot.ByProxy[proxyID] != 2 {
		t.Fatalf("open snapshot = %#v, want total/by-proxy 2", openSnapshot)
	}
	if partialSnapshot.Total != 1 || partialSnapshot.ByProxy[proxyID] != 1 {
		t.Fatalf("partial snapshot = %#v, want total/by-proxy 1", partialSnapshot)
	}
	if closedSnapshot.Total != 0 {
		t.Fatalf("closed snapshot total = %d, want 0", closedSnapshot.Total)
	}
}

func TestLeaseFinish_releasesActiveLease_whenRequestCompletesWithFailure(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.31", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// When
	lease.Finish(Result{Success: false})

	// Then
	if entry.ConsecutiveFails != 1 {
		t.Fatalf("consecutive fails = %d, want 1", entry.ConsecutiveFails)
	}
	if got := activeLeaseCount(t, allocator); got != 0 {
		t.Fatalf("active leases = %d, want 0", got)
	}
}

func TestAllocatorAcquireAvoidsFreshProxyAfterRequestFailure_whenAlternativeExists(t *testing.T) {
	// Given
	p := newTestPool(10)
	failedFresh := newReadyEntry("203.0.113.33", 1080, "alpha", 10)
	failedFresh.LatencyEWMA = 0
	failedFresh.LatencyVariance = 0
	failedFresh.LatencyCount = 0

	backupFresh := newReadyEntry("203.0.113.34", 1080, "alpha", 10)
	backupFresh.LatencyEWMA = 0
	backupFresh.LatencyVariance = 0
	backupFresh.LatencyCount = 0
	p.Promote(failedFresh)
	p.Promote(backupFresh)
	allocator := New(p).(*defaultAllocator)

	firstLease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if firstLease.entry != failedFresh {
		t.Fatalf("first selected proxy = %s, want %s", firstLease.UpstreamAddr(), failedFresh.Proxy.Addr())
	}
	firstLease.Finish(Result{Success: false})

	// When
	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if secondLease.entry != backupFresh {
		t.Fatalf("second selected proxy = %s, want %s", secondLease.UpstreamAddr(), backupFresh.Proxy.Addr())
	}
	secondLease.Finish(Result{Success: true, ConnectLatency: 50 * time.Millisecond})
}

func TestAllocatorAcquire_doesNotTrackActiveLease_whenContextAlreadyCanceled(t *testing.T) {
	// Given
	p := newTestPool(10)
	entry := newReadyEntry("203.0.113.32", 1080, "alpha", 10)
	p.Promote(entry)
	allocator := New(p).(*defaultAllocator)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	lease, err := allocator.Acquire(ctx, AcquireRequest{})

	// Then
	if lease != nil {
		t.Fatalf("expected nil lease, got %#v", lease)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := activeLeaseCount(t, allocator); got != 0 {
		t.Fatalf("active leases = %d, want 0", got)
	}
	if entry.UseCount != 0 {
		t.Fatalf("use count = %d, want 0", entry.UseCount)
	}
}
