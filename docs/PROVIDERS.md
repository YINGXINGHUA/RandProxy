# Provider Guide

This document explains how to add, disable, replace, or remove proxy Sources.

## Current Model

RandProxy treats every proxy Source as a `proxy.Provider`:

```go
type Provider interface {
    Name() string
    Status() ProviderStatus
    Fetch(ctx context.Context) ([]*Proxy, error)
}
```

Provider implementations live under `internal/provider/`. Dedicated providers are one file per Source, for example:

- `daili66.go`
- `scdn.go`
- `goodips.go`
- `proxy43.go`
- `geonode.go`

`list.go` is a shared helper for static list sources. It provides `NewListProvider(...)` plus parsers for JSON and `ip:port` text formats. Existing sources such as `proxyscrape`, `proxifly`, `hproxy`, and `thordata` are registered from `main.go` using this helper instead of having dedicated files.

Recommended rule for future work: create one dedicated file per provider when the source needs custom request headers, response parsing, status handling, rate-limit behavior, authentication, pagination, or special error handling. Use `ListProvider` only for simple static URL lists.

## Add a Dedicated Provider

Create `internal/provider/<source_name>.go`.

Minimal structure:

```go
package provider

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/user/randproxy/internal/proxy"
)

const exampleSourceURL = "https://example.com/proxies.txt"

type ExampleSource struct {
    Client *http.Client
    status statusField
}

func (p *ExampleSource) Name() string { return "example-source" }

func (p *ExampleSource) Status() proxy.ProviderStatus { return p.status.get() }

func (p *ExampleSource) Fetch(ctx context.Context) ([]*proxy.Proxy, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, exampleSourceURL, nil)
    if err != nil {
        p.status.set(proxy.StatusOffline)
        return nil, fmt.Errorf("example-source: build request: %w", err)
    }
    req.Header.Set("User-Agent", "RandProxy/1.0")

    resp, err := p.client().Do(req)
    if err != nil {
        p.status.set(proxy.StatusOffline)
        return nil, fmt.Errorf("example-source: request: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        p.status.set(proxy.StatusOffline)
        return nil, fmt.Errorf("example-source: status %d", resp.StatusCode)
    }

    proxies := []*proxy.Proxy{
        {
            IP:       "203.0.113.10",
            Port:     1080,
            Protocol: proxy.ProtocolSOCKS5,
            Source:   p.Name(),
        },
    }
    p.status.set(proxy.StatusOnline)
    return proxies, nil
}

func (p *ExampleSource) client() *http.Client {
    if p.Client == nil {
        p.Client = &http.Client{Timeout: 15 * time.Second}
    }
    return p.Client
}
```

Provider requirements:

- `Name()` must return a stable source key. This key is used in logs, dashboard source state, `sources.enabled`, fetch stats, validation stats, and ready-by-source counts.
- `Fetch(ctx)` must respect context cancellation by using `http.NewRequestWithContext` or an equivalent context-aware client.
- `Fetch(ctx)` must close response bodies.
- Return only parsed proxy candidates. The Validator is responsible for proving whether the candidate works.
- Set `Source: p.Name()` on returned proxies unless a shared helper already does it.
- Prefer `proxy.ProtocolSOCKS5` when the source is known to provide SOCKS5. Non-SOCKS5 candidates are forgotten by the validator and will not become Ready proxies.
- Use `statusField` for thread-safe status tracking.
- Use a bounded HTTP timeout. Existing providers use 15 seconds.

## Register the Provider

Open `main.go` and find the provider registration block:

```go
add := func(p proxy.Provider, interval, initDelay time.Duration) {
    allProviders = append(allProviders, namedProvider{p, interval})
    f.Add(p, interval, initDelay)
}
```

Add the provider in the registration list:

```go
add(&provider.ExampleSource{}, 5*time.Minute, 0)
```

Use `initDelay` when a source rate-limits immediate startup fetches. `66daili` uses a 10-second initial delay.

## Add a Simple Static List Source

If the source is just a static URL that returns JSON or `ip:port` text, reuse `ListProvider`:

```go
add(provider.NewListProvider("example-list",
    "https://example.com/socks5.txt",
    provider.ParseTXT), 5*time.Minute, 0)
```

Existing parsers:

- `ParseTXT`: one `ip:port` per line.
- `ParseProxyScrape`: ProxyScrape JSON format.
- `ParseProxifly`: Proxifly JSON format.

If the format is different but still generic, add a parser to `list.go`. If the source has custom behavior, create a dedicated file instead.

## Enable or Disable a Source

Source enablement is controlled by the source name returned by `Name()`.

In `config.jsonc`:

```jsonc
"sources": {
  "enabled": {
    "example-source": true
  }
}
```

Rules:

- Missing keys default to enabled.
- `false` disables future fetch ticks for that provider.
- The Web control plane can change `sources.enabled.*` at runtime and persists the change into `config.override.json`.
- Disabling a source does not immediately delete already Ready proxies from that source. It stops future fetches.

## Remove a Provider

To remove a provider cleanly:

1. Remove its `add(...)` registration from `main.go`.
2. Delete the dedicated file under `internal/provider/` if no other code uses it.
3. Remove its key from `config.jsonc` and `config.override.json` if present.
4. Run verification commands.

If the provider used `ListProvider`, only remove the `add(provider.NewListProvider(...))` registration and any config keys.

## Verification Checklist

Run these after adding, replacing, or removing a provider:

```bash
go test ./...
go run ./scripts/stress-runtime.go -duration 10s -concurrency 12
```

Optional manual check with the Web control plane enabled:

1. Open `/dashboard`.
2. Go to `来源`.
3. Confirm the source name appears with a sensible status.
4. Toggle the source off and on.
5. Confirm `/api/v1/overview` returns the source in `overview.sources`.

## Provider Quality Expectations

A provider is worth keeping when it consistently improves Ready proxy supply.

Prefer sources that:

- Return SOCKS5 candidates.
- Have stable response formats.
- Respect reasonable request intervals.
- Return fresh IPs instead of stale repeated lists.
- Do not require brittle HTML scraping.

Remove or park sources that:

- Return mostly HTTP/HTTPS proxies when SOCKS5 is needed.
- Return many private, loopback, malformed, or duplicate candidates.
- Rate-limit aggressively or return frequent 403/429 responses.
- Require high-maintenance scraping for low Ready yield.

## Long-Running Notes

Provider code runs for the lifetime of the process. Keep each provider conservative:

- No unbounded goroutines inside providers.
- No package-level mutable cache unless it is bounded and protected by a mutex.
- No request without timeout or context.
- No response body left open.
- No panic on malformed upstream data.

Fetcher stats and dashboard status depend on provider behavior, so failed fetches should return an error and set `StatusOffline` rather than silently returning bad data.
