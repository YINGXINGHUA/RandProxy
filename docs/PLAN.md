# Plan
Last updated: 2026-07-07 | Last verified: 2026-07-07 (final acceptance review passed)

## Next
*(All planned items complete)*

## Later
- SOCKS4 协议检测兼容
- SOCKS5 BIND 支持
- 代理质量分级（按源历史通过率）

## Completed
- ✅ LAN control-plane mutation mode: `control_plane.trusted_local_only=false` now permits LAN write operations while retaining control-header, Host validation, and cross-site request guards
- ✅ Web listener binding fix: `server.web_host` now supports `0.0.0.0` and specific interface IPs while mutation endpoints remain trusted-local guarded
- ✅ Final project acceptance review: 5-lane review passed after fixing `/api/v1/overview` empty-source handling, server-owned `/api/v1/pool` DTO composition, and control-plane mutation CSRF/origin/header protection
- ✅ Runtime stability/stress verification: isolated local harness (`scripts/stress-runtime.go`) covering HTTP forward, HTTP CONNECT, SOCKS5 CONNECT, control-plane overview, pool inventory, active gauges, and 60s/24-concurrency traffic with zero failures
- ✅ v10: operations console + configurable control-plane trust mode（中文浅色分层 UI、单行顶部工具条、后端 30m/24h/3d 窗口统计、active connection/lease 指标、紧凑代理卡片、代理池搜索/筛选、代理 active lease 展示、配置差异参考、配置参考切换、sidecar override persistence、4 种 routing mode、source/proxy action、single-file embed workflow、`/stats` compatibility、默认 loopback-only 写入、可显式启用可信局域网写入、capacity-safe manual actions）
- ✅ v9 phase 1: allocator-first request acquisition + keyed active-lease isolation（删除 `Pool.Next()` 请求期入口，保留 pool 生命周期/记账所有权）
- ✅ v7: 系统性优化审计 — 19 项问题发现，13 项修复（P0 锁/评分/Forget + P1 channel/ticker/swap/pool/验证 + P2 HTTP/SOCKS5 auth/状态码/源多样性/CORS + 验证目标多样化）
- ✅ v8: Web 控制面板端口拆分 + SOCKS5 UDP ASSOCIATE（保留 BIND 不支持，丢弃 FRAG!=0）
- ✅ v1: Core pipeline (Fetcher → Validator → Pool → Server)
- ✅ Config system (JSONC + Chinese comments + `--config` flag)
- ✅ Log levels (debug/info/warn/error/silent)
- ✅ Domain glossary (CONTEXT.md) + ADR 0001
- ✅ v2: Multi-source expansion (9 providers)
- ✅ v3: Web dashboard (Vue 3 + Chart.js) + /stats endpoint
- ✅ v4: Health system v2 (EWMA + variance + bestProxy + dynamic check)
- ✅ v5: SOCKS5 downstream on same port as HTTP CONNECT
- ✅ 对抗式审查两轮 + 全部修复（Score 单位 / bestProxy 悬空 / HealthCheck 竞态 / SSRF 防护 / Dail 超时 / known map 无限增长等）
- ✅ 三轮深度审计 + 14 项修复（SOCKS5 字节错位 / variance / load 锁 / SkipCheck 挂起 / nil panic / DialContext / lastChecked 竞态 / Promote 防重 / JSON 注释破坏 / ParseDuration 警告 / 流式延迟 / 拨号超时 / 黑名单持久化）

## Parked
- Metric export (prometheus / simple HTTP stats endpoint) — no demand
- Web-page scraper providers (parse HTML proxy lists) — ROI low
