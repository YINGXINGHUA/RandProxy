package controlplane

import (
	"context"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/pool"
)

type runtimeApplier struct {
	allocator allocation.Runtime
	pool      *pool.Pool
}

func NewRuntimeApplier(allocator allocation.Runtime, proxyPool *pool.Pool) LiveApplier {
	return runtimeApplier{allocator: allocator, pool: proxyPool}
}

func (a runtimeApplier) Apply(ctx context.Context, change LiveChangeSet) error {
	_ = ctx
	if change.EffectiveConfig == nil {
		return nil
	}
	if a.pool != nil {
		a.pool.ApplyBlacklistTTL(change.EffectiveConfig.PoolBlacklistTTL())
		a.pool.ApplyMaxUse(change.EffectiveConfig.PoolMaxUse())
	}
	if a.allocator != nil {
		a.allocator.ApplyPolicy(allocation.Policy{
			Mode:             change.EffectiveConfig.Policy.Mode,
			RandomSubsetSize: change.EffectiveConfig.Policy.RandomSubsetSize,
			StableSubsetSize: change.EffectiveConfig.Policy.StableSubsetSize,
		})
	}
	return nil
}
