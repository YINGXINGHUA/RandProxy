package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBaseConfigJSONC = `{
  // human-owned base config
  "server": {"host": "127.0.0.1", "port": 9090, "web_host": "127.0.0.1", "web_port": 9091, "relay_idle_timeout": "30s"},
  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": true}
}`

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeOverrideFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

func TestLoadKeepsWebListenerDisabledWhenFieldsOmitted(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "127.0.0.1", "port": 9090, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.ListenAddr(); got != "127.0.0.1:9090" {
		t.Fatalf("proxy listen addr = %q", got)
	}
	if got := cfg.WebListenAddr(); got != "" {
		t.Fatalf("web listen addr = %q", got)
	}
}

func TestRepositoryConfigJSONCLoads(t *testing.T) {
	path := filepath.Join("..", "..", "config.jsonc")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	if got := cfg.Validator.Concurrency; got <= 0 {
		t.Fatalf("validator concurrency = %d, want positive", got)
	}
	if got := cfg.Server.MaxConnections; got <= 0 {
		t.Fatalf("server max_connections = %d, want positive", got)
	}
	if got := cfg.Pool.MaxReady; got < cfg.Pool.MinReady {
		t.Fatalf("pool max_ready = %d below min_ready = %d", got, cfg.Pool.MinReady)
	}
}

func TestLoadRejectsNonPositiveServerMaxConnections(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "127.0.0.1", "port": 9090, "web_host": "127.0.0.1", "web_port": 9091, "relay_idle_timeout": "30s", "max_connections": -1},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "server.max_connections") {
		t.Fatal("expected non-positive server.max_connections to be rejected")
	}
}

func TestLoadRejectsTooLargeServerMaxConnections(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "127.0.0.1", "port": 9090, "web_host": "127.0.0.1", "web_port": 9091, "relay_idle_timeout": "30s", "max_connections": 10001},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "server.max_connections") {
		t.Fatal("expected too-large server.max_connections to be rejected")
	}
}

func TestLoadRejectsCollidingWebPort(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "0.0.0.0", "port": 8080, "web_port": 8080, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "server.web_port must differ") {
		t.Fatalf("expected web/proxy port collision error, got %v", err)
	}
}

func TestLoadRejectsNonPositivePoolMaxReady(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "127.0.0.1", "port": 9090, "web_host": "127.0.0.1", "web_port": 9091, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 0, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "pool.max_ready must be > 0") {
		t.Fatalf("expected pool.max_ready validation error, got %v", err)
	}
}

func TestLoadRejectsPoolMaxReadyBelowMinReady(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "127.0.0.1", "port": 9090, "web_host": "127.0.0.1", "web_port": 9091, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 2, "max_ready": 1, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "pool.max_ready must be >= pool.min_ready") {
		t.Fatalf("expected pool capacity invariant error, got %v", err)
	}
}

func TestLoadAllowsDisabledWebListener(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "0.0.0.0", "port": 8080, "web_port": 0, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.WebListenAddr(); got != "" {
		t.Fatalf("web listen addr = %q", got)
	}
}

func TestLoadUsesProxyHostWhenWebHostMissing(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "127.0.0.1", "port": 8080, "web_port": 8081, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.WebListenAddr(); got != "127.0.0.1:8081" {
		t.Fatalf("web listen addr = %q", got)
	}
}

func TestLoadPreservesTrustedLocalOnlyFalseAndKeepsConfiguredWebListener(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "0.0.0.0", "port": 8080, "web_host": "0.0.0.0", "web_port": 8081, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false},
	  "control_plane": {"trusted_local_only": false}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ControlPlane.TrustedLocalOnly {
		t.Fatal("expected trusted-local-only false to be preserved")
	}
	if got := cfg.Server.WebHost; got != "0.0.0.0" {
		t.Fatalf("web_host = %q", got)
	}
	if got := cfg.WebListenAddr(); got != "0.0.0.0:8081" {
		t.Fatalf("web listen addr = %q", got)
	}
}

func TestLoadAllowsSpecificWebListenerHost(t *testing.T) {
	path := writeConfigFile(t, `{
	  "server": {"host": "0.0.0.0", "port": 8080, "web_host": "192.0.2.10", "web_port": 8081, "relay_idle_timeout": "30s"},
	  "pool": {"min_ready": 1, "max_ready": 10, "max_use": 2, "buffer_max": 10, "blacklist_ttl": "1h", "state_file": ""},
	  "health": {"revalidate_interval": "1h", "front_check_count": 1, "latency_threshold": "1s", "ewma_alpha": 0.3, "consecutive_fail_limit": 1, "check_interval_active": "30s", "check_interval_idle": "5m"},
	  "validator": {"target_host": "example.com", "target_port": 443, "timeout": "5s", "concurrency": 1, "tls_insecure": true},
	  "log": {"prefix": "[test]", "level": "info", "file": "", "file_enable": false}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.WebListenAddr(); got != "192.0.2.10:8081" {
		t.Fatalf("web listen addr = %q", got)
	}
}

func TestDefaultOverridePath_returnsSiblingOverrideJSON(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "config.jsonc")
	want := filepath.Join(filepath.Dir(basePath), "config.override.json")

	if got := DefaultOverridePath(basePath); got != want {
		t.Fatalf("override path = %q, want %q", got, want)
	}
}

func TestLoadEffective_usesBaseConfigWhenOverrideMissing(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)

	cfg, err := LoadEffective(basePath)
	if err != nil {
		t.Fatalf("load effective config: %v", err)
	}
	if got := cfg.Server.Port; got != 9090 {
		t.Fatalf("server port = %d", got)
	}
	if got := cfg.Policy.Mode; got != "balanced" {
		t.Fatalf("policy mode = %q", got)
	}
	if got := cfg.ControlPlane.TrustedLocalOnly; !got {
		t.Fatalf("trusted-local-only = %v", got)
	}
}

func TestLoadEffective_mergesOverrideFile(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)
	overridePath := DefaultOverridePath(basePath)
	writeOverrideFile(t, overridePath, `{
	  "server": {"web_port": 0},
	  "pool": {"max_use": 9},
	  "log": {"file_enable": false},
	  "policy": {"mode": "single_best", "random_subset_size": 4, "stable_subset_size": 2},
	  "sources": {"enabled": {"alpha": false}}
	}`)

	cfg, err := LoadEffective(basePath)
	if err != nil {
		t.Fatalf("load effective config: %v", err)
	}
	if got := cfg.WebListenAddr(); got != "" {
		t.Fatalf("web listen addr = %q", got)
	}
	if got := cfg.Pool.MaxUse; got != 9 {
		t.Fatalf("pool max_use = %d", got)
	}
	if got := cfg.Log.FileEnable; got {
		t.Fatalf("log.file_enable = %v", got)
	}
	if got := cfg.Policy.Mode; got != "single_best" {
		t.Fatalf("policy mode = %q", got)
	}
	if got := cfg.Sources.Enabled["alpha"]; got {
		t.Fatalf("sources.enabled[alpha] = %v", got)
	}
}

func TestLoadEffective_returnsErrorWhenOverrideMalformed(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)
	overridePath := DefaultOverridePath(basePath)
	writeOverrideFile(t, overridePath, `{"policy":`)

	_, err := LoadEffective(basePath)
	if err == nil || !strings.Contains(err.Error(), "parse override") {
		t.Fatalf("expected override parse error, got %v", err)
	}
}

func TestLoadEffective_rejectsInvalidMergedPolicy(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)
	overridePath := DefaultOverridePath(basePath)
	writeOverrideFile(t, overridePath, `{"policy":{"mode":"wrong"}}`)

	_, err := LoadEffective(basePath)
	if err == nil || !strings.Contains(err.Error(), "policy.mode") {
		t.Fatalf("expected policy.mode validation error, got %v", err)
	}
}

func TestLoadEffective_rejectsInvalidMergedPoolCapacity(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)
	overridePath := DefaultOverridePath(basePath)
	writeOverrideFile(t, overridePath, `{"pool":{"min_ready":11}}`)

	_, err := LoadEffective(basePath)
	if err == nil || !strings.Contains(err.Error(), "pool.max_ready must be >= pool.min_ready") {
		t.Fatalf("expected pool capacity validation error, got %v", err)
	}
}

func TestLoadEffectivePreservesTrustedLocalOnlyFalseAndKeepsOverrideWebListener(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)
	overridePath := DefaultOverridePath(basePath)
	writeOverrideFile(t, overridePath, `{
	  "server": {"web_host": "0.0.0.0", "web_port": 9091},
	  "control_plane": {"trusted_local_only": false}
	}`)

	cfg, err := LoadEffective(basePath)
	if err != nil {
		t.Fatalf("load effective config: %v", err)
	}
	if cfg.ControlPlane.TrustedLocalOnly {
		t.Fatal("expected trusted-local-only false to be preserved after override")
	}
	if got := cfg.Server.WebHost; got != "0.0.0.0" {
		t.Fatalf("web_host = %q", got)
	}
	if got := cfg.WebListenAddr(); got != "0.0.0.0:9091" {
		t.Fatalf("web listen addr = %q", got)
	}
}

func TestSaveOverride_roundTripsWithoutRewritingBaseConfig(t *testing.T) {
	basePath := writeConfigFile(t, testBaseConfigJSONC)
	baseBefore, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read base before save: %v", err)
	}

	cfg, err := LoadEffective(basePath)
	if err != nil {
		t.Fatalf("load effective config: %v", err)
	}
	cfg.Server.WebPort = 0
	cfg.Pool.MaxUse = 7
	cfg.Policy.Mode = "stable_subset"
	cfg.Policy.StableSubsetSize = 2
	cfg.Sources.Enabled = map[string]bool{"alpha": false}

	overridePath, err := SaveOverride(basePath, cfg)
	if err != nil {
		t.Fatalf("save override: %v", err)
	}
	t.Logf("manual QA receipt: base_path=%s override_path=%s", basePath, overridePath)
	if got := overridePath; got != DefaultOverridePath(basePath) {
		t.Fatalf("saved override path = %q", got)
	}

	baseAfter, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read base after save: %v", err)
	}
	if string(baseAfter) != string(baseBefore) {
		t.Fatalf("base config changed during save")
	}
	t.Logf("manual QA receipt: base file bytes unchanged after SaveOverride")

	overrideRaw, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	var overrideDoc map[string]any
	if err := json.Unmarshal(overrideRaw, &overrideDoc); err != nil {
		t.Fatalf("override is not valid json: %v", err)
	}

	roundTrip, err := LoadEffective(basePath)
	if err != nil {
		t.Fatalf("load round-trip config: %v", err)
	}
	if got := roundTrip.WebListenAddr(); got != "" {
		t.Fatalf("round-trip web listen addr = %q", got)
	}
	if got := roundTrip.Pool.MaxUse; got != 7 {
		t.Fatalf("round-trip pool max_use = %d", got)
	}
	if got := roundTrip.Policy.Mode; got != "stable_subset" {
		t.Fatalf("round-trip policy mode = %q", got)
	}
	if got := roundTrip.Sources.Enabled["alpha"]; got {
		t.Fatalf("round-trip sources.enabled[alpha] = %v", got)
	}
}
