package pool

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/user/randproxy/internal/proxy"
)

func newTestPoolForAcquireEntry(maxUse int) *Pool {
	return New(1, 10, maxUse, 10, time.Hour, time.Hour, 5*time.Second, 0.1, 3, 5, "")
}

func newReadyEntry(ip string, port int, source string, maxUse int) *Entry {
	return &Entry{
		Proxy: &proxy.Proxy{
			IP:       ip,
			Port:     port,
			Protocol: proxy.ProtocolSOCKS5,
			Source:   source,
		},
		Status:          StatusReady,
		MaxUse:          maxUse,
		AddedAt:         time.Now(),
		LatencyEWMA:     100 * time.Millisecond,
		LatencyVariance: 10 * time.Millisecond,
		LatencyCount:    2,
	}
}

func TestPoolAcquireEntryReturnsNil_whenReadyIsEmpty(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)

	entry := pool.AcquireEntry(context.Background())
	if entry != nil {
		t.Fatalf("expected nil entry, got %v", entry)
	}
}

func TestPoolAcquireEntryBlacklistsEntry_whenEntryHitsMaxUse(t *testing.T) {
	pool := newTestPoolForAcquireEntry(2)
	entry := newReadyEntry("203.0.113.10", 1080, "alpha", 2)
	pool.ready = []*Entry{entry}

	first := pool.AcquireEntry(context.Background())
	if first != entry {
		t.Fatalf("expected first acquisition to return original entry")
	}
	if entry.UseCount != 1 {
		t.Fatalf("expected use count 1 after first acquisition, got %d", entry.UseCount)
	}
	if entry.Status != StatusReady {
		t.Fatalf("expected entry to remain ready after first acquisition, got %v", entry.Status)
	}

	second := pool.AcquireEntry(context.Background())
	if second != entry {
		t.Fatalf("expected second acquisition to return original entry")
	}
	if entry.UseCount != 2 {
		t.Fatalf("expected use count 2 after second acquisition, got %d", entry.UseCount)
	}
	if entry.Status != StatusBlacklisted {
		t.Fatalf("expected entry to be blacklisted at max use, got %v", entry.Status)
	}
	if pool.ReadyCount() != 0 {
		t.Fatalf("expected ready pool to be empty after blacklist, got %d", pool.ReadyCount())
	}
	if pool.BlacklistCount() != 1 {
		t.Fatalf("expected blacklist count 1, got %d", pool.BlacklistCount())
	}

	third := pool.AcquireEntry(context.Background())
	if third != nil {
		t.Fatalf("expected no eligible entry after blacklist, got %v", third)
	}
	if pool.BlacklistCount() != 1 {
		t.Fatalf("expected blacklist count to stay 1, got %d", pool.BlacklistCount())
	}
}

func TestPoolAcquireEntryRotatesAcrossEligibleSources_whenMultipleSourcesRemainEligible(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	pool.ready = []*Entry{
		newReadyEntry("203.0.113.1", 1080, "alpha", 10),
		newReadyEntry("203.0.113.2", 1080, "beta", 10),
	}

	gotSources := make([]string, 0, 3)
	for range 3 {
		entry := pool.AcquireEntry(context.Background())
		if entry == nil {
			t.Fatal("expected ready entry")
		}
		gotSources = append(gotSources, entry.Proxy.Source)
	}

	wantSources := []string{"alpha", "beta", "alpha"}
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("unexpected source order: got %v want %v", gotSources, wantSources)
	}
}

func TestPoolAcquireEntryPrefersLowerScoreWithinChosenSource(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
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

	pool.ready = []*Entry{slower, faster, beta}

	entry := pool.AcquireEntry(context.Background())
	if entry == nil {
		t.Fatal("expected ready entry")
	}
	if entry != faster {
		t.Fatalf("expected lower-score alpha entry, got %s", entry.Proxy.Addr())
	}
	if entry.UseCount != 1 {
		t.Fatalf("expected chosen entry use count 1, got %d", entry.UseCount)
	}
	if slower.UseCount != 0 {
		t.Fatalf("expected higher-score alpha entry to remain unused, got %d", slower.UseCount)
	}
}

func TestPoolCompleteFailureAvoidsFailedBestProxyOnNextAcquire(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	failedBest := newReadyEntry("203.0.113.30", 1080, "alpha", 10)
	backup := newReadyEntry("203.0.113.31", 1080, "beta", 10)
	pool.ready = []*Entry{failedBest, backup}
	pool.bestProxy.Store(failedBest)

	first := pool.AcquireEntry(context.Background())
	if first != failedBest {
		t.Fatalf("expected pinned best proxy first, got %v", first)
	}
	pool.CompleteFailure(failedBest)

	second := pool.AcquireEntry(context.Background())
	if second == nil {
		t.Fatal("expected backup entry after failed best proxy")
	}
	if second == failedBest {
		t.Fatalf("failed best proxy was immediately reused")
	}
	if second != backup {
		t.Fatalf("expected backup entry after failed best proxy, got %s", second.Proxy.Addr())
	}
}

func TestEntryScorePenalizesConsecutiveFailuresWithinSource(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	failedFast := newReadyEntry("203.0.113.40", 1080, "alpha", 10)
	failedFast.LatencyEWMA = 50 * time.Millisecond
	failedFast.LatencyVariance = 5 * time.Millisecond
	failedFast.LatencyCount = 3
	failedFast.ConsecutiveFails = 1

	stableSlow := newReadyEntry("203.0.113.41", 1080, "alpha", 10)
	stableSlow.LatencyEWMA = 80 * time.Millisecond
	stableSlow.LatencyVariance = 5 * time.Millisecond
	stableSlow.LatencyCount = 3

	pool.ready = []*Entry{failedFast, stableSlow}

	entry := pool.AcquireEntry(context.Background())
	if entry != stableSlow {
		t.Fatalf("expected stable entry to beat recently failed entry, got %s", entry.Proxy.Addr())
	}
}

func TestPoolCompleteSuccessResetsConsecutiveFailures(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	entry := newReadyEntry("203.0.113.50", 1080, "alpha", 10)
	entry.ConsecutiveFails = 2
	pool.ready = []*Entry{entry}

	pool.CompleteSuccess(entry, 50*time.Millisecond)

	if entry.ConsecutiveFails != 0 {
		t.Fatalf("expected success to reset consecutive failures, got %d", entry.ConsecutiveFails)
	}
}

func TestPoolCompleteFailureKeepsBlacklistedProxyKnownDuringCooldown(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	entry := newReadyEntry("203.0.113.60", 1080, "alpha", 10)
	pool.ready = []*Entry{entry}
	pool.known[proxyKey(entry.Proxy)] = true

	for range 3 {
		pool.CompleteFailure(entry)
	}

	if entry.Status != StatusBlacklisted {
		t.Fatalf("expected entry to be blacklisted, got %v", entry.Status)
	}
	pool.Feed([]*proxy.Proxy{entry.Proxy})
	if pool.BufferCount() != 0 {
		t.Fatalf("expected blacklisted duplicate to stay out of buffer, got buffer=%d", pool.BufferCount())
	}
	if !pool.known[proxyKey(entry.Proxy)] {
		t.Fatal("expected blacklisted proxy to remain known during cooldown")
	}
}

func TestPoolCompleteFailureDoesNotDuplicateBlacklistEntry(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	entry := newReadyEntry("203.0.113.61", 1080, "alpha", 10)
	pool.ready = []*Entry{entry}
	pool.known[proxyKey(entry.Proxy)] = true

	for range 3 {
		pool.CompleteFailure(entry)
	}
	pool.CompleteFailure(entry)
	pool.CompleteFailure(entry)

	if pool.BlacklistCount() != 1 {
		t.Fatalf("expected one blacklist entry, got %d", pool.BlacklistCount())
	}
}

func TestPoolNeedRefillContinuesUntilReadyReachesMaxReady(t *testing.T) {
	pool := newTestPoolForAcquireEntry(10)
	for i := 0; i < 9; i++ {
		pool.ready = append(pool.ready, newReadyEntry("203.0.113.200", 1080+i, "alpha", 10))
	}
	if !pool.NeedRefill() {
		t.Fatal("expected validator refill while ready is below maxReady")
	}
	pool.ready = append(pool.ready, newReadyEntry("203.0.113.250", 1080, "alpha", 10))
	if pool.NeedRefill() {
		t.Fatal("expected validator refill to stop at maxReady")
	}
}
