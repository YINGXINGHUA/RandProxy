package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/user/randproxy/internal/config"
)

const testBaseConfigJSONC = `{
  // human-owned base config
  "server": {"host": "127.0.0.1", "port": 9090, "web_host": "127.0.0.1", "web_port": 9091, "relay_idle_timeout": "30s"},
  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": true}
}`

type fakeLiveApplier struct {
	callCount int
	fields    []string
	configs   []*config.Config
	err       error
}

func (f *fakeLiveApplier) Apply(ctx context.Context, change LiveChangeSet) error {
	f.callCount++
	f.fields = append([]string(nil), change.Fields...)
	f.configs = append(f.configs, change.EffectiveConfig)
	return f.err
}

func writeBaseConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(path, []byte(testBaseConfigJSONC), 0o644); err != nil {
		t.Fatalf("write base config: %v", err)
	}
	return path
}

func writeOverrideFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write override file: %v", err)
	}
}

func loadEffectiveConfig(t *testing.T, basePath string) *config.Config {
	t.Helper()
	cfg, err := config.LoadEffective(basePath)
	if err != nil {
		t.Fatalf("load effective config: %v", err)
	}
	return cfg
}

func assertFieldPathsEqual(t *testing.T, got []string, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field paths = %#v, want %#v", got, want)
	}
}

func TestManagerApply_classifiesLiveFields(t *testing.T) {
	basePath := writeBaseConfigFile(t)
	liveApplier := &fakeLiveApplier{}
	manager, err := NewManager(basePath, ManagerOptions{LiveApplier: liveApplier})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	desired := loadEffectiveConfig(t, basePath)
	desired.Policy.Mode = "single_best"
	desired.Policy.RandomSubsetSize = 5
	desired.Policy.StableSubsetSize = 4
	desired.Pool.MaxUse = 9
	desired.Pool.BlacklistTTL = "2h"
	desired.Sources.Enabled = map[string]bool{"alpha": false, "beta": true}

	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}

	assertFieldPathsEqual(t, result.AppliedLiveFields, []string{
		"policy.mode",
		"policy.random_subset_size",
		"policy.stable_subset_size",
		"pool.blacklist_ttl",
		"pool.max_use",
		"sources.enabled.alpha",
		"sources.enabled.beta",
	})
	assertFieldPathsEqual(t, result.RestartRequiredFields, []string{})
	if !result.Persistence.Attempted {
		t.Fatalf("persistence attempted = %v", result.Persistence.Attempted)
	}
	if !result.Persistence.Succeeded {
		t.Fatalf("persistence succeeded = %v (%s)", result.Persistence.Succeeded, result.Persistence.Error)
	}
	if liveApplier.callCount != 1 {
		t.Fatalf("live apply calls = %d, want 1", liveApplier.callCount)
	}
	assertFieldPathsEqual(t, liveApplier.fields, result.AppliedLiveFields)
	if got := result.EffectiveConfig.Pool.MaxUse; got != 9 {
		t.Fatalf("effective pool max_use = %d", got)
	}
}

func TestManagerApply_classifiesRestartRequiredFields(t *testing.T) {
	basePath := writeBaseConfigFile(t)
	liveApplier := &fakeLiveApplier{}
	manager, err := NewManager(basePath, ManagerOptions{LiveApplier: liveApplier})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	desired := loadEffectiveConfig(t, basePath)
	desired.Policy.Mode = "single_best"
	desired.Server.WebPort = 0
	desired.Validator.Concurrency = 3
	desired.Health.CheckIntervalIdle = "10m"
	desired.Log.Level = "debug"
	desired.Pool.BufferMax = 25

	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}

	assertFieldPathsEqual(t, result.AppliedLiveFields, []string{"policy.mode"})
	assertFieldPathsEqual(t, result.RestartRequiredFields, []string{
		"health.check_interval_idle",
		"log.level",
		"pool.buffer_max",
		"server.web_port",
		"validator.concurrency",
	})
	if liveApplier.callCount != 1 {
		t.Fatalf("live apply calls = %d, want 1", liveApplier.callCount)
	}
	if !result.Persistence.Succeeded {
		t.Fatalf("persistence succeeded = %v (%s)", result.Persistence.Succeeded, result.Persistence.Error)
	}
	t.Logf(
		"manual QA receipt: applied_live=%v restart_required=%v persistence_succeeded=%t override_path=%s",
		result.AppliedLiveFields,
		result.RestartRequiredFields,
		result.Persistence.Succeeded,
		result.Persistence.OverridePath,
	)
}

func TestManagerApply_returnsFailureReceiptWhenOverrideSaveFails(t *testing.T) {
	basePath := writeBaseConfigFile(t)
	liveApplier := &fakeLiveApplier{}
	manager, err := NewManager(basePath, ManagerOptions{LiveApplier: liveApplier})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	before := manager.Export()
	desired := loadEffectiveConfig(t, basePath)
	desired.Policy.Mode = "single_best"
	desired.Server.WebPort = 0

	if err := os.Mkdir(before.OverridePath, 0o755); err != nil {
		t.Fatalf("create blocking override directory: %v", err)
	}

	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}

	if !result.Persistence.Attempted {
		t.Fatalf("persistence attempted = %v", result.Persistence.Attempted)
	}
	if result.Persistence.Succeeded {
		t.Fatalf("persistence succeeded = %v, want false", result.Persistence.Succeeded)
	}
	if result.Receipt.OK {
		t.Fatalf("receipt ok = %v, want false", result.Receipt.OK)
	}
	assertFieldPathsEqual(t, result.AppliedLiveFields, []string{})
	assertFieldPathsEqual(t, result.RestartRequiredFields, []string{})
	if liveApplier.callCount != 0 {
		t.Fatalf("live apply calls = %d, want 0", liveApplier.callCount)
	}
	after := manager.Export()
	if !reflect.DeepEqual(after.EffectiveConfig, before.EffectiveConfig) {
		t.Fatalf("effective config changed on failed persistence")
	}
}

func TestManagerApply_detectsNoopRequests(t *testing.T) {
	basePath := writeBaseConfigFile(t)
	liveApplier := &fakeLiveApplier{}
	manager, err := NewManager(basePath, ManagerOptions{LiveApplier: liveApplier})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	desired := loadEffectiveConfig(t, basePath)
	result, err := manager.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}

	if !result.Receipt.Noop {
		t.Fatalf("receipt noop = %v, want true", result.Receipt.Noop)
	}
	if result.Persistence.Attempted {
		t.Fatalf("persistence attempted = %v, want false", result.Persistence.Attempted)
	}
	if liveApplier.callCount != 0 {
		t.Fatalf("live apply calls = %d, want 0", liveApplier.callCount)
	}
	assertFieldPathsEqual(t, result.AppliedLiveFields, []string{})
	assertFieldPathsEqual(t, result.RestartRequiredFields, []string{})
}

func TestManagerExport_returnsEffectiveConfigSnapshot(t *testing.T) {
	basePath := writeBaseConfigFile(t)
	overridePath := config.DefaultOverridePath(basePath)
	writeOverrideFile(t, overridePath, `{"pool":{"max_use":7},"sources":{"enabled":{"alpha":false}}}`)

	manager, err := NewManager(basePath, ManagerOptions{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	exported := manager.Export()
	if exported.BasePath != basePath {
		t.Fatalf("base path = %q, want %q", exported.BasePath, basePath)
	}
	if exported.OverridePath != overridePath {
		t.Fatalf("override path = %q, want %q", exported.OverridePath, overridePath)
	}
	if got := exported.EffectiveConfig.Pool.MaxUse; got != 7 {
		t.Fatalf("exported pool max_use = %d", got)
	}
	if got := exported.EffectiveConfig.Sources.Enabled["alpha"]; got {
		t.Fatalf("exported sources.enabled[alpha] = %v", got)
	}

	exported.EffectiveConfig.Pool.MaxUse = 99
	exported.EffectiveConfig.Sources.Enabled["alpha"] = true

	reloaded := manager.Export()
	if got := reloaded.EffectiveConfig.Pool.MaxUse; got != 7 {
		t.Fatalf("reloaded pool max_use = %d, want 7", got)
	}
	if got := reloaded.EffectiveConfig.Sources.Enabled["alpha"]; got {
		t.Fatalf("reloaded sources.enabled[alpha] = %v, want false", got)
	}
}
