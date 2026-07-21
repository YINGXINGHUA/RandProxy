package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const maxServerConnectionsLimit = 10000

type Config struct {
	Server       ServerConfig       `json:"server"`
	Pool         PoolConfig         `json:"pool"`
	Validator    ValidatorConfig    `json:"validator"`
	Health       HealthConfig       `json:"health"`
	Log          LogConfig          `json:"log"`
	Policy       PolicyConfig       `json:"policy"`
	ControlPlane ControlPlaneConfig `json:"control_plane"`
	Sources      SourcesConfig      `json:"sources"`
}

type ServerConfig struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	WebHost          string `json:"web_host"`
	WebPort          int    `json:"web_port"`
	RelayIdleTimeout string `json:"relay_idle_timeout"`
	MaxConnections   int    `json:"max_connections"`
}

type PoolConfig struct {
	MinReady     int    `json:"min_ready"`
	MaxReady     int    `json:"max_ready"`
	MaxUse       int    `json:"max_use"`
	BufferMax    int    `json:"buffer_max"`
	BlacklistTTL string `json:"blacklist_ttl"`
	StateFile    string `json:"state_file"`
}

type HealthConfig struct {
	RevalidateInterval   string  `json:"revalidate_interval"`    // full pool revalidation period, e.g. "1h"
	FrontCheckCount      int     `json:"front_check_count"`      // how many front proxies to check each cycle
	LatencyThreshold     string  `json:"latency_threshold"`      // EWMA latency threshold, e.g. "3000ms"
	EwmaAlpha            float64 `json:"ewma_alpha"`             // EWMA smoothing factor (0-1), e.g. 0.3
	ConsecutiveFailLimit int     `json:"consecutive_fail_limit"` // evict after this many consecutive fails
	CheckIntervalActive  string  `json:"check_interval_active"`  // active period check interval, e.g. "30s"
	CheckIntervalIdle    string  `json:"check_interval_idle"`    // idle period check interval, e.g. "5m"
}

type TargetConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type ValidatorConfig struct {
	TargetHost  string         `json:"target_host"`
	TargetPort  int            `json:"target_port"`
	Targets     []TargetConfig `json:"targets"`
	Timeout     string         `json:"timeout"`
	Concurrency int            `json:"concurrency"`
	TLSInsecure bool           `json:"tls_insecure"`
}

type LogConfig struct {
	Prefix     string `json:"prefix"`
	Level      string `json:"level"`
	File       string `json:"file"`
	FileEnable bool   `json:"file_enable"`
}

type PolicyConfig struct {
	Mode             string `json:"mode"`
	RandomSubsetSize int    `json:"random_subset_size"`
	StableSubsetSize int    `json:"stable_subset_size"`
}

type ControlPlaneConfig struct {
	TrustedLocalOnly bool `json:"trusted_local_only"`
}

type SourcesConfig struct {
	Enabled map[string]bool `json:"enabled"`
}

func Load(path string) (*Config, error) {
	return loadConfigFile(path)
}

func LoadEffective(basePath string) (*Config, error) {
	cfg, err := loadConfigFile(basePath)
	if err != nil {
		return nil, err
	}
	overridePath := DefaultOverridePath(basePath)
	raw, err := os.ReadFile(overridePath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("config: read override %s: %w", overridePath, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: parse override %s: %w", overridePath, err)
	}
	cfg.normalize()
	return cfg, cfg.validate()
}

func DefaultOverridePath(basePath string) string {
	if strings.HasSuffix(basePath, ".jsonc") {
		return strings.TrimSuffix(basePath, ".jsonc") + ".override.json"
	}
	return filepath.Join(filepath.Dir(basePath), filepath.Base(basePath)+".override.json")
}

func SaveOverride(basePath string, cfg *Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config: override config is required")
	}
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return "", err
	}
	baseCfg, err := loadConfigFile(basePath)
	if err != nil {
		return "", err
	}
	overrideDoc, err := buildOverrideDocument(baseCfg, cfg)
	if err != nil {
		return "", err
	}
	overridePath := DefaultOverridePath(basePath)
	data, err := json.MarshalIndent(overrideDoc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("config: marshal override %s: %w", overridePath, err)
	}
	if err := os.WriteFile(overridePath, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("config: write override %s: %w", overridePath, err)
	}
	return overridePath, nil
}

func loadConfigFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cleaned := stripJSONComments(string(raw))
	cfg := defaults()
	if err := json.Unmarshal([]byte(cleaned), cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.normalize()
	return cfg, cfg.validate()
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

func (c *Config) WebListenAddr() string {
	if c.Server.WebPort == 0 {
		return ""
	}
	host := c.controlPlaneWebHost()
	return fmt.Sprintf("%s:%d", host, c.Server.WebPort)
}

func (c *Config) RelayIdleTimeout() time.Duration {
	d, err := time.ParseDuration(c.Server.RelayIdleTimeout)
	if err != nil {
		return 60 * time.Second
	}
	return d
}

// --- Helpers ---

func (c *Config) PoolMaxUse() int   { return c.Pool.MaxUse }
func (c *Config) PoolMinReady() int { return c.Pool.MinReady }
func (c *Config) PoolMaxReady() int { return c.Pool.MaxReady }
func (c *Config) PoolBlacklistTTL() time.Duration {
	d, err := time.ParseDuration(c.Pool.BlacklistTTL)
	if err != nil {
		log.Printf("[WARN] config: pool.blacklist_ttl parse error: %v, using 0", err)
	}
	return d
}
func (c *Config) ValidatorTimeout() time.Duration {
	d, err := time.ParseDuration(c.Validator.Timeout)
	if err != nil {
		log.Printf("[WARN] config: validator.timeout parse error: %v, using 0", err)
	}
	return d
}
func (c *Config) RevalidateInterval() time.Duration {
	d, err := time.ParseDuration(c.Health.RevalidateInterval)
	if err != nil {
		log.Printf("[WARN] config: health.revalidate_interval parse error: %v, using 0", err)
	}
	return d
}
func (c *Config) LatencyThreshold() time.Duration {
	d, err := time.ParseDuration(c.Health.LatencyThreshold)
	if err != nil {
		log.Printf("[WARN] config: health.latency_threshold parse error: %v, using 0", err)
	}
	return d
}
func (c *Config) CheckIntervalActive() time.Duration {
	d, err := time.ParseDuration(c.Health.CheckIntervalActive)
	if err != nil {
		log.Printf("[WARN] config: health.check_interval_active parse error: %v, using 0", err)
	}
	return d
}
func (c *Config) CheckIntervalIdle() time.Duration {
	d, err := time.ParseDuration(c.Health.CheckIntervalIdle)
	if err != nil {
		log.Printf("[WARN] config: health.check_interval_idle parse error: %v, using 0", err)
	}
	return d
}

// --- Internal ---

// findCommentStart returns the index of the first // that is not inside a JSON string.
func findCommentStart(s string) int {
	inString := false
	escape := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString && i+1 < len(s) && ch == '/' && s[i+1] == '/' {
			return i
		}
	}
	return -1
}

func stripJSONComments(s string) string {
	for {
		start := strings.Index(s, "/*")
		if start < 0 {
			break
		}
		end := strings.Index(s[start+2:], "*/")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+2+end+2:]
	}
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		idx := findCommentStart(line)
		if idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Host:             "0.0.0.0",
			Port:             8080,
			WebPort:          0,
			RelayIdleTimeout: "60s",
			MaxConnections:   200,
		},
		Pool: PoolConfig{
			MinReady:     10,
			MaxReady:     100,
			MaxUse:       25,
			BufferMax:    5000,
			BlacklistTTL: "24h",
			StateFile:    "pool_state.json",
		},
		Health: HealthConfig{
			RevalidateInterval:   "1h",
			FrontCheckCount:      3,
			LatencyThreshold:     "3000ms",
			EwmaAlpha:            0.3,
			ConsecutiveFailLimit: 3,
			CheckIntervalActive:  "30s",
			CheckIntervalIdle:    "5m",
		},
		Validator: ValidatorConfig{
			TargetHost: "api.literouter.com",
			TargetPort: 443,
			Targets: []TargetConfig{
				{Host: "api.literouter.com", Port: 443},
				{Host: "httpbin.org", Port: 443},
				{Host: "example.com", Port: 443},
			},
			Timeout:     "6s",
			Concurrency: 5,
			TLSInsecure: true,
		},
		Log: LogConfig{
			Prefix: "[randproxy]",
			File:   "randproxy.log",
		},
		Policy: PolicyConfig{
			Mode:             "balanced",
			RandomSubsetSize: 3,
			StableSubsetSize: 3,
		},
		ControlPlane: ControlPlaneConfig{
			TrustedLocalOnly: true,
		},
		Sources: SourcesConfig{
			Enabled: map[string]bool{},
		},
	}
}

func (c *Config) normalize() {
	if c == nil {
		return
	}
	c.Server.WebHost = normalizedControlPlaneWebHost(c.Server.Host, c.Server.WebHost, c.Server.WebPort)
	if c.Server.MaxConnections == 0 {
		c.Server.MaxConnections = 200
	}
}

func (c *Config) controlPlaneWebHost() string {
	return normalizedControlPlaneWebHost(c.Server.Host, c.Server.WebHost, c.Server.WebPort)
}

func normalizedControlPlaneWebHost(serverHost string, webHost string, webPort int) string {
	if webPort == 0 {
		return webHost
	}
	if strings.TrimSpace(webHost) != "" {
		return webHost
	}
	return serverHost
}

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("config: server.port must be 1-65535")
	}
	if c.Server.WebPort < 0 || c.Server.WebPort > 65535 {
		return fmt.Errorf("config: server.web_port must be 0-65535")
	}
	if c.Server.WebPort != 0 && c.Server.WebPort == c.Server.Port {
		return fmt.Errorf("config: server.web_port must differ from server.port")
	}
	if c.Server.MaxConnections <= 0 || c.Server.MaxConnections > maxServerConnectionsLimit {
		return fmt.Errorf("config: server.max_connections must be 1-%d", maxServerConnectionsLimit)
	}
	if c.Pool.MinReady <= 0 {
		return fmt.Errorf("config: pool.min_ready must be > 0")
	}
	if c.Pool.MaxReady <= 0 {
		return fmt.Errorf("config: pool.max_ready must be > 0")
	}
	if c.Pool.MaxReady < c.Pool.MinReady {
		return fmt.Errorf("config: pool.max_ready must be >= pool.min_ready")
	}
	if c.Pool.MaxUse <= 0 {
		return fmt.Errorf("config: pool.max_use must be > 0")
	}
	if _, err := time.ParseDuration(c.Pool.BlacklistTTL); err != nil {
		return fmt.Errorf("config: pool.blacklist_ttl: %w", err)
	}
	if c.Validator.TargetHost == "" {
		return fmt.Errorf("config: validator.target_host is required")
	}
	if _, err := time.ParseDuration(c.Validator.Timeout); err != nil {
		return fmt.Errorf("config: validator.timeout: %w", err)
	}
	if _, err := time.ParseDuration(c.Health.RevalidateInterval); err != nil {
		return fmt.Errorf("config: health.revalidate_interval: %w", err)
	}
	if c.Health.EwmaAlpha <= 0 || c.Health.EwmaAlpha > 1 {
		return fmt.Errorf("config: health.ewma_alpha must be 0-1")
	}
	if c.Policy.Mode == "" {
		return fmt.Errorf("config: policy.mode is required")
	}
	switch c.Policy.Mode {
	case "balanced", "random_subset", "stable_subset", "single_best":
	default:
		return fmt.Errorf("config: policy.mode must be one of balanced, random_subset, stable_subset, single_best")
	}
	if c.Policy.RandomSubsetSize <= 0 {
		return fmt.Errorf("config: policy.random_subset_size must be > 0")
	}
	if c.Policy.StableSubsetSize <= 0 {
		return fmt.Errorf("config: policy.stable_subset_size must be > 0")
	}
	for name := range c.Sources.Enabled {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config: sources.enabled keys must be non-empty")
		}
	}
	return nil
}

func buildOverrideDocument(baseCfg *Config, effectiveCfg *Config) (map[string]any, error) {
	baseDoc, err := configToDocument(baseCfg)
	if err != nil {
		return nil, err
	}
	effectiveDoc, err := configToDocument(effectiveCfg)
	if err != nil {
		return nil, err
	}
	diff, ok := diffDocument(baseDoc, effectiveDoc).(map[string]any)
	if !ok || diff == nil {
		return map[string]any{}, nil
	}
	return diff, nil
}

func configToDocument(cfg *Config) (map[string]any, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("config: marshal document: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("config: unmarshal document: %w", err)
	}
	return doc, nil
}

func diffDocument(base any, effective any) any {
	baseMap, baseIsMap := base.(map[string]any)
	effectiveMap, effectiveIsMap := effective.(map[string]any)
	if baseIsMap && effectiveIsMap {
		diff := make(map[string]any)
		for key, effectiveValue := range effectiveMap {
			valueDiff := diffDocument(baseMap[key], effectiveValue)
			if valueDiff != nil {
				diff[key] = valueDiff
			}
		}
		if len(diff) == 0 {
			return nil
		}
		return diff
	}
	if reflect.DeepEqual(base, effective) {
		return nil
	}
	return effective
}
