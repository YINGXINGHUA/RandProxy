package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/user/randproxy/internal/allocation"
	"github.com/user/randproxy/internal/config"
	"github.com/user/randproxy/internal/controlplane"
	"github.com/user/randproxy/internal/fetcher"
	"github.com/user/randproxy/internal/lgr"
	"github.com/user/randproxy/internal/pool"
	"github.com/user/randproxy/internal/provider"
	"github.com/user/randproxy/internal/proxy"
	"github.com/user/randproxy/internal/server"
	"github.com/user/randproxy/internal/validator"
)

func main() {
	cfgPath := flag.String("config", "config.jsonc", "path to config file")
	flag.Parse()

	cfg, err := config.LoadEffective(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.SetFlags(log.Ltime | log.Lshortfile)
	log.SetPrefix(cfg.Log.Prefix + " ")

	var logWriters []io.Writer
	logWriters = append(logWriters, lgr.NewLevelWriter(cfg.Log.Level))
	if cfg.Log.FileEnable && cfg.Log.File != "" {
		logFile, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Printf("[WARN] failed to open log file %s: %v", cfg.Log.File, err)
		} else {
			logWriters = append(logWriters, logFile)
			log.Printf("[INFO] logging to %s", cfg.Log.File)
		}
	}
	log.SetOutput(io.MultiWriter(logWriters...))
	log.Printf("[INFO] loaded config from %s", *cfgPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Pool ---
	p := pool.New(
		cfg.PoolMinReady(),
		cfg.PoolMaxReady(),
		cfg.PoolMaxUse(),
		cfg.Pool.BufferMax,
		cfg.PoolBlacklistTTL(),
		cfg.RevalidateInterval(),
		cfg.LatencyThreshold(),
		cfg.Health.EwmaAlpha,
		cfg.Health.ConsecutiveFailLimit,
		cfg.Health.FrontCheckCount,
		cfg.Pool.StateFile,
	)

	// --- Fetcher: register providers ---
	f := fetcher.New()
	f.ApplySourceStates(cfg.Sources.Enabled)
	f.SkipCheck = func(name string) bool {
		return !p.BufferNeedRefill()
	}

	// Keep a slice for status reporting
	type namedProvider struct {
		proxy.Provider
		interval time.Duration
	}
	var allProviders []namedProvider

	add := func(p proxy.Provider, interval, initDelay time.Duration) {
		allProviders = append(allProviders, namedProvider{p, interval})
		f.Add(p, interval, initDelay)
	}

	// 66daili: delay initial fetch 10s to avoid 429
	add(&provider.Daili66{}, 10*time.Second, 10*time.Second)

	// CDN-hosted JSON proxy lists
	add(provider.NewListProvider("proxyscrape",
		"https://cdn.jsdelivr.net/gh/proxyscrape/free-proxy-list@main/proxies/protocols/socks5/data.json",
		provider.ParseProxyScrape), 5*time.Minute, 0)

	add(provider.NewListProvider("proxifly",
		"https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.json",
		provider.ParseProxifly), 5*time.Minute, 0)

	add(provider.NewListProvider("hproxy",
		"https://raw.githubusercontent.com/hproxy-com/free-proxy-list/main/socks5.txt",
		provider.ParseTXT), 5*time.Minute, 0)

	add(provider.NewListProvider("thordata",
		"https://raw.githubusercontent.com/Thordata/awesome-free-proxy-list/main/proxies/socks5.txt",
		provider.ParseTXT), 5*time.Minute, 0)

	// Dedicated source files
	add(&provider.Scdn{}, 5*time.Minute, 0)
	add(&provider.Goodips{}, 5*time.Minute, 0)
	add(&provider.Proxy43{}, 5*time.Minute, 0)
	add(&provider.Geonode{}, 5*time.Minute, 0)

	go func() {
		log.Printf("[INFO] starting fetcher")
		f.Run(ctx)
	}()

	go func() {
		for batch := range f.Out() {
			log.Printf("[INFO] fetcher received %d proxies", len(batch))
			p.Feed(batch)
			log.Printf("[INFO] pool: %s", p.DumpStats())
		}
	}()

	// --- Validator: buffer → ready ---
	var targets []validator.Target
	for _, t := range cfg.Validator.Targets {
		targets = append(targets, validator.Target{Host: t.Host, Port: t.Port})
	}
	v := validator.New(p, validator.Config{
		TargetHost:  cfg.Validator.TargetHost,
		TargetPort:  cfg.Validator.TargetPort,
		Targets:     targets,
		Timeout:     cfg.ValidatorTimeout(),
		Concurrency: cfg.Validator.Concurrency,
		TLSInsecure: cfg.Validator.TLSInsecure,
	})
	go func() {
		log.Printf("[INFO] starting validator")
		v.Run(ctx)
	}()

	// --- Proxy server: HTTP CONNECT → SOCKS5 ---
	allocator := allocation.NewWithOptions(p, allocation.Options{
		Policy: allocation.Policy{
			Mode:             cfg.Policy.Mode,
			RandomSubsetSize: cfg.Policy.RandomSubsetSize,
			StableSubsetSize: cfg.Policy.StableSubsetSize,
		},
	})
	liveApplier := controlplane.NewMultiApplier(
		controlplane.NewRuntimeApplier(allocator, p),
		controlplane.NewSourceStateApplier(f),
	)
	manager, err := controlplane.NewManager(*cfgPath, controlplane.ManagerOptions{LiveApplier: liveApplier})
	if err != nil {
		log.Fatalf("controlplane: %v", err)
	}
	srv := server.NewWithAllocator(server.Config{
		Listen:           cfg.ListenAddr(),
		WebListen:        cfg.WebListenAddr(),
		RelayIdleTimeout: cfg.RelayIdleTimeout(),
		MaxConnections:   cfg.Server.MaxConnections,
	}, p, allocator)
	srv.SetControlPlaneManager(manager)
	srv.SetStatsProvider(func() []proxy.Provider {
		ps := make([]proxy.Provider, len(allProviders))
		for i, np := range allProviders {
			ps[i] = np.Provider
		}
		return ps
	})
	srv.SetFetchStats(f.SourceStats)
	srv.SetValidStats(v.SourceStats)
	go func() {
		log.Printf("[INFO] starting proxy server on %s", cfg.ListenAddr())
		if err := srv.Run(ctx); err != nil {
			log.Printf("[ERROR] proxy server error: %v", err)
		}
	}()
	if webAddr := cfg.WebListenAddr(); webAddr != "" {
		go func() {
			log.Printf("[INFO] starting web control panel on %s", webAddr)
			if err := srv.RunWeb(ctx); err != nil {
				log.Printf("[ERROR] web control panel error: %v", err)
			}
		}()
	}

	// --- Stats ticker ---
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Printf("[INFO] pool: %s", p.DumpStats())
				for _, np := range allProviders {
					s := "?"
					switch np.Status() {
					case proxy.StatusOnline:
						s = "✓"
					case proxy.StatusOffline:
						s = "✗"
					}
					log.Printf("[INFO] provider %s [%s]", np.Name(), s)
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.ReleaseExpired()
			}
		}
	}()

	// --- Health checker: dynamic interval based on activity ---
	go func() {
		activeInterval := cfg.CheckIntervalActive()
		idleInterval := cfg.CheckIntervalIdle()
		interval := activeInterval
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.HealthCheck(v.TestEntry)
				// Adjust interval: active vs idle
				if p.IsIdle(idleInterval) {
					interval = idleInterval
				} else {
					interval = activeInterval
				}
				ticker.Reset(interval)
			}
		}
	}()

	// --- Signal handling ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	<-sigCh

	log.Printf("[INFO] shutting down...")
	f.Stop()
	p.Save()
	cancel()
	time.Sleep(500 * time.Millisecond)
	log.Printf("[INFO] done")
}
