package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/pool"
	proxycore "github.com/user/randproxy/internal/proxy"
)

func TestManagerApply_switchesAllocatorPolicyLiveWithoutRestart(t *testing.T) {
	// Given
	basePath := writeBaseConfigFile(t)
	p := pool.New(1, 10, 10, 10, time.Hour, time.Hour, 5*time.Second, 0.1, 3, 5, "")
	alpha := newPoolReadyEntry("203.0.113.100", 1080, "alpha", 10, 120*time.Millisecond, 10*time.Millisecond)
	beta := newPoolReadyEntry("203.0.113.101", 1080, "beta", 10, 40*time.Millisecond, 5*time.Millisecond)
	p.Promote(alpha)
	p.Promote(beta)
	allocatorRuntime := allocation.NewWithOptions(p, allocation.Options{
		Policy: allocation.Policy{Mode: "balanced", RandomSubsetSize: 2, StableSubsetSize: 2},
	})
	manager, err := NewManager(basePath, ManagerOptions{LiveApplier: NewRuntimeApplier(allocatorRuntime, p)})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	beforeLease, err := allocatorRuntime.Acquire(context.Background(), allocation.AcquireRequest{})
	if err != nil {
		t.Fatalf("acquire before apply: %v", err)
	}
	beforeAddr := beforeLease.UpstreamAddr()
	beforeLease.Finish(allocation.Result{Success: true, ConnectLatency: 20 * time.Millisecond})

	desired := loadEffectiveConfig(t, basePath)
	desired.Policy.Mode = "single_best"
	desired.Pool.MaxUse = 7
	desired.Pool.BlacklistTTL = "2h"

	// When
	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}
	afterLease, err := allocatorRuntime.Acquire(context.Background(), allocation.AcquireRequest{})
	if err != nil {
		t.Fatalf("acquire after apply: %v", err)
	}
	afterAddr := afterLease.UpstreamAddr()
	afterLease.Finish(allocation.Result{Success: true, ConnectLatency: 20 * time.Millisecond})

	// Then
	if beforeAddr != alpha.Proxy.Addr() {
		t.Fatalf("before apply addr = %s, want %s", beforeAddr, alpha.Proxy.Addr())
	}
	if afterAddr != beta.Proxy.Addr() {
		t.Fatalf("after apply addr = %s, want %s", afterAddr, beta.Proxy.Addr())
	}
	if got := alpha.MaxUse; got != 7 {
		t.Fatalf("alpha max use = %d, want 7", got)
	}
	if got := beta.MaxUse; got != 7 {
		t.Fatalf("beta max use = %d, want 7", got)
	}
	if !result.Receipt.OK {
		t.Fatalf("receipt ok = %v, want true", result.Receipt.OK)
	}
	t.Logf(
		"manual QA receipt: receipt=%q before=%s after=%s applied_live=%v",
		result.Receipt.Message,
		beforeAddr,
		afterAddr,
		result.AppliedLiveFields,
	)
	if got := result.EffectiveConfig.Pool.BlacklistTTL; got != "2h" {
		t.Fatalf("effective blacklist ttl = %q, want %q", got, "2h")
	}
}

func newPoolReadyEntry(ip string, port int, source string, maxUse int, latency time.Duration, variance time.Duration) *pool.Entry {
	entry := &pool.Entry{
		Proxy: &proxycore.Proxy{
			IP:       ip,
			Port:     port,
			Protocol: proxycore.ProtocolSOCKS5,
			Source:   source,
		},
		Status:           pool.StatusBuffer,
		MaxUse:           maxUse,
		AddedAt:          time.Now(),
		LatencyEWMA:      latency,
		LatencyVariance:  variance,
		LatencyCount:     3,
		ConsecutiveFails: 0,
	}
	return entry
}
