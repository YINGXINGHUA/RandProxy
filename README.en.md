# RandProxy

Self-hosted proxy aggregation gateway. Fetches SOCKS5 candidates from multiple free sources, validates with health scoring and quota management, exposes HTTP CONNECT + SOCKS5 downstream. Includes a Web monitoring dashboard.

## Features

- **Multi-source fetching**: 9+ free proxy sources (66daili, ProxyScrape, Proxifly, HProxy, ThorData, scdn-socks5, goodips, geonode, etc.), each with independent intervals and status tracking
- **Three-layer validation**: SOCKS5 handshake → TLS handshake → HTTP GET; only fully-verified proxies enter the ready pool
- **Health scoring**: EWMA latency + variance + consecutive fail eviction + dynamic check intervals
- **Dual-protocol downstream**: Auto-detects HTTP CONNECT and SOCKS5 on the same port
- **State persistence**: Restores ready pool and blacklist across restarts
- **Web dashboard**: Vue 3 + Vite, single embedded HTML, zero CDN dependencies

## Architecture

```
Sources → Fetcher → Buffer → Validator → Ready Pool → Proxy Server → Upstream SOCKS5 → Target
                                              ↳ Blacklist / Cooldown
```

Module boundaries:

- `internal/provider/` — proxy source adapters
- `internal/fetcher/` — scheduled source fetching
- `internal/validator/` — candidate validation (SOCKS5 → TLS → HTTP)
- `internal/pool/` — proxy lifecycle (ready / buffer / blacklist / quota / health)
- `internal/server/` — downstream proxy handling + Web dashboard + `/stats` API
- `ui/` — Vue 3 dashboard source, built and embedded into the binary

## Quick Start

```bash
go build -o randproxy .
./randproxy
```

Use a custom config:

```bash
./randproxy --config /path/to/config.jsonc
```

Default endpoints (from `config.jsonc`):

- Proxy listener: `server.host:server.port` (default `0.0.0.0:8080`)
- Web dashboard: `server.web_host:server.web_port` (default `0.0.0.0:8081`)
- Dashboard route: `/dashboard`
- Stats endpoint: `/stats`

Client usage:

```bash
# HTTP CONNECT
export http_proxy=http://127.0.0.1:8080
export https_proxy=http://127.0.0.1:8080
curl https://www.xxx.com/

# SOCKS5
curl --proxy socks5://127.0.0.1:8080 https://www.xxx.com/
```

## Configuration

Main config file: `config.jsonc` (includes Chinese comments).

Runtime control-plane writes are stored in `config.override.json`, merged at startup. If a value in `config.jsonc` appears not to take effect, check whether `config.override.json` is overriding it.

Fields requiring restart:

- `server.*`
- `validator.*`
- `health.*`
- `log.*`
- `pool.min_ready`
- `pool.max_ready`
- `pool.buffer_max`
- `pool.state_file`

Fields applied at runtime:

- `pool.max_use`
- `pool.blacklist_ttl`
- `sources.enabled.*`

## Recommended Settings (Free Proxy Mode)

Free proxies have low pass rates. Suggested config:

```jsonc
{
  "pool": {
    "min_ready": 5,
    "max_ready": 20,
    "buffer_max": 300,
    "max_use": 25,
    "blacklist_ttl": "24h"
  },
  "server": {
    "max_connections": 200,
    "relay_idle_timeout": "60s"
  },
  "validator": {
    "timeout": "10s",
    "concurrency": 4,
    "targets": [{ "host": "www.xxx.com", "port": 443 }]
  },
  "health": {
    "latency_threshold": "3000ms",
    "consecutive_fail_limit": 3
  }
}
```

## Providers

Registered proxy sources:

| Name | Type | Description |
|------|------|-------------|
| `66daili` | API | 66daili.com, 10s interval |
| `proxyscrape` | URL list | ProxyScrape API |
| `proxifly` | URL list | proxifly.com free list |
| `hproxy` | URL list | hugeproxy.com |
| `thordata` | URL list | thordata.com |
| `scdn-socks5` | API | scdn.io SOCKS5 endpoint |
| `goodips` | API | goodips.com |
| `geonode` | API | geonode.com free tier |
| `43.135.31.113` | API | Custom endpoint |

Disable low-quality sources in `sources.enabled`:

```jsonc
"sources": {
  "enabled": {
    "66daili": false,
    "scdn-socks5": false
  }
}
```

## Dashboard Build

After changing `ui/`, rebuild:

```bash
npm --prefix ui run build
./build-dashboard.sh
```

`build-dashboard.sh` runs the Vue build and copies `ui/dist/index.html` to `internal/server/dashboard.html`.

## Troubleshooting

**`503 No proxy available`**:
- Ready pool is empty; wait for validation to populate it
- Increase `validator.concurrency` or `pool.buffer_max`

**`502 Bad gateway` / SOCKS5 errors**:
- A ready proxy failed during real request-time dialing
- RandProxy records failures and blacklists bad upstreams
- Free proxy sources may not have enough working proxies for your target

**Ready count stays low**:
- Provider pass rate is poor
- Validation target may be too strict or unreachable from many free proxies
- Disable noisy providers and align `validator.targets` with your real target

**Web config changes not applied**:
- Check `config.override.json`
- Some fields require restart

## Documentation

- [docs/INDEX.md](docs/INDEX.md) — documentation index
- [docs/PROGRESS.md](docs/PROGRESS.md) — current state and architecture
- [docs/PLAN.md](docs/PLAN.md) — completed work and roadmap
- [docs/LOG.md](docs/LOG.md) — change log
- [docs/PROVIDERS.md](docs/PROVIDERS.md) — provider maintenance guide
- [CONTEXT.md](CONTEXT.md) — glossary and project context

## License

MIT
