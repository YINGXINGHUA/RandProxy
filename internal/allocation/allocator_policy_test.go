package allocation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/user/randproxy/internal/pool"
)

func newPolicyAllocator(p *pool.Pool, policy Policy, randomIntn func(int) int) *defaultAllocator {
	return NewWithOptions(p, Options{Policy: policy, RandomIntn: randomIntn}).(*defaultAllocator)
}

func TestAllocatorAcquire_preservesBalancedParity_whenPolicyModeIsBalanced(t *testing.T) {
	// Given
	p := newTestPool(10)
	p.Promote(newReadyEntry("203.0.113.60", 1080, "alpha", 10))
	p.Promote(newReadyEntry("203.0.113.61", 1080, "beta", 10))
	allocator := newPolicyAllocator(p, Policy{Mode: "balanced", RandomSubsetSize: 2, StableSubsetSize: 2}, nil)

	// When
	gotSources := make([]string, 0, 3)
	for range 3 {
		lease, err := allocator.Acquire(context.Background(), AcquireRequest{})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		gotSources = append(gotSources, lease.entry.Proxy.Source)
		lease.Finish(Result{Success: true, ConnectLatency: 25 * time.Millisecond})
	}

	// Then
	wantSources := []string{"alpha", "beta", "alpha"}
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("sources = %v, want %v", gotSources, wantSources)
	}
}

func TestAllocatorAcquire_balancedUsesAllocatorOrderedEligibleSet_whenPoolBestProxyIsPinned(t *testing.T) {
	// Given
	p := newTestPool(10)
	alpha := newScoredReadyEntry("203.0.113.65", 1080, "alpha", 10, 40*time.Millisecond, 5*time.Millisecond)
	beta := newScoredReadyEntry("203.0.113.66", 1080, "beta", 10, 80*time.Millisecond, 5*time.Millisecond)
	p.Promote(alpha)
	p.Promote(beta)
	p.HealthCheck(nil)
	allocator := newPolicyAllocator(p, Policy{Mode: "balanced", RandomSubsetSize: 2, StableSubsetSize: 2}, nil)

	// When
	firstLease, err := allocator.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	firstLease.Finish(Result{Success: true, ConnectLatency: 25 * time.Millisecond})
	secondLease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if firstLease.entry != alpha {
		t.Fatalf("first selected proxy = %s, want %s", firstLease.UpstreamAddr(), alpha.Proxy.Addr())
	}
	if secondLease.entry != beta {
		t.Fatalf("second selected proxy = %s, want %s", secondLease.UpstreamAddr(), beta.Proxy.Addr())
	}
	secondLease.Finish(Result{Success: true, ConnectLatency: 25 * time.Millisecond})
}

func TestAllocatorAcquire_confinesRandomSubsetSelectionToTopSubset(t *testing.T) {
	// Given
	p := newTestPool(10)
	best := newScoredReadyEntry("203.0.113.70", 1080, "alpha", 10, 40*time.Millisecond, 5*time.Millisecond)
	second := newScoredReadyEntry("203.0.113.71", 1080, "alpha", 10, 60*time.Millisecond, 5*time.Millisecond)
	third := newScoredReadyEntry("203.0.113.72", 1080, "alpha", 10, 90*time.Millisecond, 5*time.Millisecond)
	fourth := newScoredReadyEntry("203.0.113.73", 1080, "alpha", 10, 120*time.Millisecond, 5*time.Millisecond)
	p.Promote(best)
	p.Promote(second)
	p.Promote(third)
	p.Promote(fourth)
	allocator := newPolicyAllocator(p, Policy{Mode: "random_subset", RandomSubsetSize: 2, StableSubsetSize: 2}, func(n int) int {
		if n != 2 {
			t.Fatalf("subset size = %d, want 2", n)
		}
		return 1
	})

	// When
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.entry != second {
		t.Fatalf("selected proxy = %s, want top-subset member %s", lease.UpstreamAddr(), second.Proxy.Addr())
	}
	if third.UseCount != 0 || fourth.UseCount != 0 {
		t.Fatalf("random subset touched entries outside the top subset")
	}
	lease.Finish(Result{Success: true, ConnectLatency: 25 * time.Millisecond})
}

func TestAllocatorAcquire_reusesStableTopSubsetDeterministically_whenPolicyModeIsStableSubset(t *testing.T) {
	// Given
	p := newTestPool(10)
	best := newScoredReadyEntry("203.0.113.80", 1080, "alpha", 10, 40*time.Millisecond, 5*time.Millisecond)
	second := newScoredReadyEntry("203.0.113.81", 1080, "alpha", 10, 60*time.Millisecond, 5*time.Millisecond)
	third := newScoredReadyEntry("203.0.113.82", 1080, "alpha", 10, 90*time.Millisecond, 5*time.Millisecond)
	p.Promote(best)
	p.Promote(second)
	p.Promote(third)
	allocator := newPolicyAllocator(p, Policy{Mode: "stable_subset", RandomSubsetSize: 2, StableSubsetSize: 2}, nil)

	// When
	gotAddrs := make([]string, 0, 4)
	leases := make([]*Lease, 0, 4)
	for range 4 {
		lease, err := allocator.Acquire(context.Background(), AcquireRequest{})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		gotAddrs = append(gotAddrs, lease.UpstreamAddr())
		leases = append(leases, lease)
	}
	for _, lease := range leases {
		lease.Finish(Result{Success: true, ConnectLatency: 25 * time.Millisecond})
	}

	// Then
	wantAddrs := []string{best.Proxy.Addr(), second.Proxy.Addr(), best.Proxy.Addr(), second.Proxy.Addr()}
	if !reflect.DeepEqual(gotAddrs, wantAddrs) {
		t.Fatalf("addresses = %v, want %v", gotAddrs, wantAddrs)
	}
	if third.UseCount != 0 {
		t.Fatalf("third-ranked proxy use count = %d, want 0", third.UseCount)
	}
}

func TestAllocatorAcquire_usesBestEligibleProxy_whenPolicyModeIsSingleBest(t *testing.T) {
	// Given
	p := newTestPool(10)
	slower := newScoredReadyEntry("203.0.113.90", 1080, "alpha", 10, 150*time.Millisecond, 15*time.Millisecond)
	best := newScoredReadyEntry("203.0.113.91", 1080, "beta", 10, 40*time.Millisecond, 5*time.Millisecond)
	p.Promote(slower)
	p.Promote(best)
	allocator := newPolicyAllocator(p, Policy{Mode: "single_best", RandomSubsetSize: 2, StableSubsetSize: 2}, nil)

	// When
	lease, err := allocator.Acquire(context.Background(), AcquireRequest{})

	// Then
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.entry != best {
		t.Fatalf("selected proxy = %s, want %s", lease.UpstreamAddr(), best.Proxy.Addr())
	}
	if slower.UseCount != 0 {
		t.Fatalf("slower proxy use count = %d, want 0", slower.UseCount)
	}
	lease.Finish(Result{Success: true, ConnectLatency: 25 * time.Millisecond})
}
