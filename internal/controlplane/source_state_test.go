package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/user/randproxy/internal/config"
	"github.com/user/randproxy/internal/fetcher"
	"github.com/user/randproxy/internal/proxy"
)

type sourceApplierTestProvider struct {
	name string
}

func (p sourceApplierTestProvider) Name() string {
	return p.name
}

func (p sourceApplierTestProvider) Status() proxy.ProviderStatus {
	return proxy.StatusOnline
}

func (p sourceApplierTestProvider) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
	_ = ctx
	return nil, nil
}

func TestManagerApply_reflectsPersistedDisabledSourcesInEffectiveConfig(t *testing.T) {
	// Given
	basePath := writeBaseConfigFile(t)
	manager, err := NewManager(basePath, ManagerOptions{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	desired := loadEffectiveConfig(t, basePath)
	desired.Sources.Enabled = map[string]bool{"alpha": false, "beta": true}

	// When
	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}

	// Then
	if !result.Persistence.Succeeded {
		t.Fatalf("persistence succeeded = %v (%s)", result.Persistence.Succeeded, result.Persistence.Error)
	}
	if got := result.EffectiveConfig.Sources.Enabled["alpha"]; got {
		t.Fatalf("result effective sources.enabled[alpha] = %v, want false", got)
	}
	if got := result.EffectiveConfig.Sources.Enabled["beta"]; !got {
		t.Fatalf("result effective sources.enabled[beta] = %v, want true", got)
	}
	exported := manager.Export()
	if got := exported.EffectiveConfig.Sources.Enabled["alpha"]; got {
		t.Fatalf("exported sources.enabled[alpha] = %v, want false", got)
	}
	reloaded := loadEffectiveConfig(t, basePath)
	if got := reloaded.Sources.Enabled["alpha"]; got {
		t.Fatalf("reloaded sources.enabled[alpha] = %v, want false", got)
	}
	if got := reloaded.Sources.Enabled["beta"]; !got {
		t.Fatalf("reloaded sources.enabled[beta] = %v, want true", got)
	}
}

func TestSourceStateApplier_appliesEffectiveSourceStateMapToFetcher(t *testing.T) {
	// Given
	fetcherRuntime := fetcher.New()
	fetcherRuntime.Add(sourceApplierTestProvider{name: "alpha"}, time.Minute, 0)
	applier := NewSourceStateApplier(fetcherRuntime)

	// When
	err := applier.Apply(context.Background(), LiveChangeSet{
		EffectiveConfig: &config.Config{
			Sources: config.SourcesConfig{
				Enabled: map[string]bool{"alpha": false},
			},
		},
	})
	if err != nil {
		t.Fatalf("apply disabled source state: %v", err)
	}
	stats := fetcherRuntime.SourceStats()

	// Then
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	if stats[0].Enabled {
		t.Fatalf("stats enabled = %v, want false", stats[0].Enabled)
	}

	// When
	err = applier.Apply(context.Background(), LiveChangeSet{
		EffectiveConfig: &config.Config{
			Sources: config.SourcesConfig{},
		},
	})
	if err != nil {
		t.Fatalf("apply default source state: %v", err)
	}
	stats = fetcherRuntime.SourceStats()

	// Then
	if !stats[0].Enabled {
		t.Fatalf("stats enabled = %v, want true", stats[0].Enabled)
	}
}
