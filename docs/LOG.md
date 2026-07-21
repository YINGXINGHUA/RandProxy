# Log

## 2026-07-07 — multi-round stress verification and connection-limit tuning
**Agent:** Sisyphus | **Scope:** scripts/server/config/runtime verification
- Extended `scripts/stress-runtime.go` to support multi-round runs, configurable pause, configurable mock upstream count, settle-time final sampling, JSON evidence output, failure classification, peak connection/lease capture, final gauge anomaly detection, goroutine count, and memory allocation reporting
- Ran smoke and high-concurrency stress passes; initial 192-concurrency burst exposed the hard-coded 200 connection cap via reset/EOF failures
- Changed proxy overload behavior from bare close to protocol-aware rejection: HTTP receives `503 Service Unavailable`, SOCKS5 receives `0x05 0xFF`
- Added `server.max_connections` to config and server runtime so the listener cap can be tuned for LAN stress or heavy local use while remaining bounded; config validation and server construction both cap the value at 10,000
- Final stress evidence passed: `.omo/evidence/stress-runtime-final-192c.json` with 192 concurrency, 3 rounds, 20s each, 12 mock upstreams, 370,734 total successful requests, 0 failures, 0 anomalies, 0 monitor failures, and final active connections/leases at 0
- Verification passed: `go test ./internal/config ./internal/server ./internal/pool ./scripts`, `go test -race ./internal/server ./internal/config ./internal/allocation`, and `go test ./...`

## 2026-07-07 — root project README
**Agent:** Sisyphus | **Scope:** documentation
- Added root `README.md` as the user-facing project entry point
- Covered project purpose, architecture, startup, proxy client usage, configuration model, recommended free-proxy settings, Web control-plane security, provider keys, dashboard build workflow, verification commands, troubleshooting, and documentation links
- Updated `docs/INDEX.md` and `docs/PROGRESS.md` to reference the README

## 2026-07-07 — configuration usability pass
**Agent:** Sisyphus | **Scope:** configuration/docs
- Expanded `config.jsonc` into a fuller operator template with commented examples for optional bind addresses, disabled Web listener, routing modes, all registered provider keys, health tuning, validation targets, validator throughput, TLS strictness, and file logging
- Tuned the shipped example for free-proxy operation: `buffer_max=500`, validator timeout `6s`, validator concurrency `24`, single Google validation target by default, and `consecutive_fail_limit=1`
- Added `TestRepositoryConfigJSONCLoads` so CI/test runs fail if the root `config.jsonc` becomes invalid JSONC or violates core config invariants
- Verification passed: `go test ./internal/config` and `go test ./...`

## 2026-07-07 — request-time bad upstream lifecycle fix
**Agent:** Sisyphus | **Scope:** pool/allocation/runtime reliability
- Diagnosed remote runtime logs where curl through RandProxy returned HTTP 502 / SOCKS5 host unreachable while the same bad upstream could be selected repeatedly
- Updated pool scoring so request-time failures penalize ready proxies, including fresh unmeasured candidates, and successful requests reset `ConsecutiveFails`
- Cleared pinned `bestProxy` on request-time failure and advanced source rotation away from the failed source when applicable
- Kept blacklisted proxies in `known` during cooldown and made blacklist insertion idempotent to prevent immediate provider re-ingest or duplicate blacklist entries from multiple failing leases
- Changed validator refill semantics so validation continues until the ready pool reaches `max_ready`, increasing the reserve of ready proxies for real traffic
- Added regressions for failed best avoidance, real allocator fresh-proxy fallback, failure score penalty, success fail reset, known retention during blacklist, and duplicate blacklist prevention
- Verification passed: `go test ./internal/pool ./internal/allocation ./internal/server`, `go test ./...`, and Oracle lifecycle review PASS

## 2026-07-07 — configurable LAN control-plane mutation mode
**Agent:** Sisyphus | **Scope:** config/control-plane/security
- Fixed `control_plane.trusted_local_only=false` being normalized back to `true`, which incorrectly blocked LAN control-plane writes even when the config disabled local-only mode
- Changed mutation authorization so the loopback `RemoteAddr` check runs only when `trusted_local_only=true`; `X-RandProxy-Control: 1`, Host validation, and cross-site browser rejection apply as guardrails
- Added regression coverage for preserving `trusted_local_only=false`, allowing non-loopback LAN mutations in LAN mode, rejecting DNS-rebinding-style Host headers in both trusted-local and LAN modes, and rejecting missing-header/cross-site LAN mutation attempts
- Updated `config.jsonc` and docs to warn that LAN write mode is only appropriate on trusted private networks

## 2026-07-07 — web listener binding fix
**Agent:** Sisyphus | **Scope:** config/control-plane
- Fixed `server.web_host` normalization so enabled Web listeners honor configured bind addresses such as `0.0.0.0` or a specific interface IP instead of being forced to `127.0.0.1`
- Preserved the default trusted-local write boundary while keeping mutation header and cross-site guards in all trust modes
- Updated config and control-plane regression tests for the new split between Web listen binding and mutation authorization
- Updated `config.jsonc` comments to clarify that `web_host` controls bind address while mutation authorization defaults to loopback-only and can be explicitly opened for trusted LAN use
- Verification passed: `go test ./internal/config ./internal/server` and `go test ./...`

## 2026-07-07 — provider maintenance guide
**Agent:** Sisyphus | **Scope:** project
- Added `docs/PROVIDERS.md` for adding, disabling, replacing, and removing proxy Sources
- Documented the `proxy.Provider` interface, one-file-per-provider convention, shared `ListProvider` path for static URL lists, `main.go` registration point, `sources.enabled` runtime toggles, and verification commands
- Updated `docs/INDEX.md` to link the guide and `docs/PROGRESS.md` to record the documentation addition

## 2026-07-07 — final project acceptance review and blocker closeout
**Agent:** Sisyphus | **Scope:** project
- Final 5-lane acceptance review completed for RandProxy after v7-v10 fixes, dashboard/control-plane work, runtime metrics, active lease tracking, and stress harness validation
- Goal/constraint lane passed with high confidence; context-mining lane passed and noted `/root/RandProxy` is not a git repository, so git diff/branch inspection is unavailable in this environment
- Hands-on QA lane found and fixed a dashboard crash risk: `/api/v1/overview` emitted `sources: null` when no providers were configured; the response now emits `sources: []`, covered by `internal/server/controlplane_overview_test.go`
- Code-quality lane initially failed because `internal/pool` exposed runtime-control JSON response shape and allocator-owned `active_leases`; fixed by moving `/api/v1/pool` DTOs and active-lease composition into `internal/server/controlplane.go`, leaving `internal/pool/runtime_control_inventory.go` as a pure domain snapshot
- Security lane initially failed because loopback control-plane mutations were browser-CSRFable; fixed by requiring `X-RandProxy-Control: 1`, rejecting mismatched `Origin`, rejecting `Sec-Fetch-Site: cross-site`, and updating the Vue dashboard mutation fetches plus embedded HTML
- Post-fix verification passed: `go test ./...`, `npm --prefix ui run build`, `./build-dashboard.sh`, and `go run ./scripts/stress-runtime.go -duration 10s -concurrency 12` with 26,987 requests, zero failures, `/api/v1/overview=200`, `/api/v1/pool=200`, `peak_connections=19`, `peak_leases=13`, and idle-end active gauges at zero
- Code-quality and security blocker reruns both returned PASS; no remaining blocking acceptance issues were reported

## 2026-07-07 — runtime stability and traffic stress verification
**Agent:** Sisyphus | **Scope:** project
- Added `scripts/stress-runtime.go`, an isolated local stress harness that starts RandProxy with a local mock SOCKS5 upstream, local HTTP target, trusted-local control-plane manager, random loopback ports, and high local quota
- Harness exercises HTTP forward proxy, HTTP CONNECT, SOCKS5 CONNECT, `/api/v1/overview`, and `/api/v1/pool`, then emits a JSON summary suitable for repeatable evidence
- Short 10s / 12-concurrency run completed 48,891 proxied requests with zero failures and observed runtime peaks (`peak_connections=17`, `peak_leases=13`)
- 60s / 24-concurrency run completed 309,086 proxied requests with zero failures: 106,071 HTTP forward, 106,389 HTTP CONNECT, 96,626 SOCKS5 CONNECT
- Final stress evidence recorded `overview_status=200`, `pool_status=200`, `window_30m_requests=309086`, `peak_connections=42`, `peak_leases=26`, and idle-end `active_connections=0` / `active_leases=0`
- Verification also passed `go test ./...`; no stress-runtime process remained after the run

## 2026-07-07 — v10: runtime window stats and active lease dashboard
**Agent:** Sisyphus | **Scope:** project
- `internal/server/dashboard.go` 增加有界内存请求窗口统计，`/stats.server` 与 `/api/v1/overview.overview.server` 输出 `request_window_1m`、`request_window_5m`、`request_window_30m`、`request_window_24h`、`request_window_3d`
- 每个请求窗口提供 `total_requests`、`success_requests`、`fail_requests`、`avg_latency_ms`；窗口在 collect 时按当前时间剔除过期 bucket，空闲时不会保留陈旧统计
- `RequestRecord` 保留旧 `time` 字段，并新增 RFC3339 `timestamp` 字段，前端按 timestamp 进行时间范围过滤与排序
- `internal/server/proxy.go` 增加 active downstream connection gauge；`internal/allocation/allocator.go` 增加只读 active lease snapshot；`/api/v1/pool` 库存条目增加 `active_leases`
- `ui/src/types.ts` 与 `ui/src/App.vue` 接入后端窗口统计、active connection、active lease 与代理卡片租约标记；UI 明确用“租约”表达分配中状态，不把它称为 TCP 连接
- `internal/server/socks5_udp.go` 修正 UDP ASSOCIATE 失败路径的 lease 结束时机，避免失败回复前保留 active lease
- 代码质量复审发现成功请求过早 `Lease.Finish(success)` 会让代理卡片在 relay 仍活跃时显示 `租约 0`；修正为 SOCKS5 CONNECT、HTTP CONNECT、HTTP forward 与 UDP ASSOCIATE 的成功 lease 均覆盖实际请求/relay 生命周期
- 验证通过：`go test ./...`、`npm --prefix ui run build`、`./build-dashboard.sh`、浏览器 MCP 检查；`/api/v1/overview` 返回 30m/24h/3d 窗口，`/api/v1/pool` 返回 `active_leases`，Dashboard 无应用 console error

## 2026-07-07 — doc-sync audit after v10 UI filtering
**Agent:** Sisyphus | **Scope:** project
- Updated `docs/PROGRESS.md` and `docs/PLAN.md` verification metadata after the proxy inventory filtering and config diff UI work
- Added `docs/NOTES.md` as the project-level notes document required by the docs structure, carrying future ideas for backend time-window aggregation, active proxy state, and semantic config diff
- Updated `docs/INDEX.md` to link the notes document

## 2026-07-07 — v10: proxy inventory filtering and config diff reference
**Agent:** Sisyphus | **Scope:** project
- `ui/src/App.vue` 在代理池页增加本地搜索，覆盖代理 ID、IP、端口、来源、协议、状态与池分组标签
- 代理池页增加分组筛选：全部、就绪、待验证、冷却；筛选先应用到三类池，再按每组显示数量截断，并展示筛选/总数
- 配置页新增“配置差异”参考面板，对当前生效配置与覆盖草稿进行格式化后的逐行对比，标注新增、删除、变更、相同；草稿 JSON 无效时显示中文提示且不改写草稿
- 保持后端 API 不变，不新增依赖，不伪造后端统计数据
- 验证通过：`npm --prefix ui run build`、`./build-dashboard.sh`、`go test ./internal/server -run Dashboard`、浏览器 MCP 交互检查；`/api/v1/overview`、`/api/v1/config`、`/api/v1/pool` 均返回 200，应用 console error 为 0

## 2026-07-06 — v10: Chinese layered operations console redesign
**Agent:** Sisyphus | **Scope:** project
- `ui/src/App.vue` 从深色长滚动页面重构为中文浅色操作台，采用侧栏模块导航：总览、代理池、来源、策略、配置、事件
- 保留原有 `/api/v1/*` 行为与测试钩子，同时新增当前模块 `localStorage` 持久化，刷新后不再丢失位置
- 代理池、来源、事件、配置区域使用内部滚动与显示数量控制，避免所有信息堆在一个页面里
- 按用户二次建议优化：顶部工作区工具条压缩为单行；总览改为真实指标卡与倒序请求列表；代理池改为紧凑卡片；策略页加入模式说明；配置页加入“当前配置 / 字段说明”参考切换；事件页聚焦回执、失败和最近请求
- 代码质量复审发现刷新乱序风险后，前端共享状态读取改为 request-id 最新请求获胜；策略/配置/来源/代理动作后的刷新统一经强制最新刷新路径
- `npm --prefix ui run build` 与 `./build-dashboard.sh` 通过，`internal/server/dashboard.html` 已刷新为新嵌入页面
- 浏览器 MCP 验证 `/dashboard` 可加载，模块导航可切换，刷新后保留“配置”模块；console 无应用错误，`/api/v1/overview`、`/api/v1/config`、`/api/v1/pool` 均返回 200
- 视觉 QA 双审通过：确认无深色紫色/AI 风格回退，中文标签和分层信息架构符合本次要求
- Post-implementation review 发现并修复 UI 异步错误处理问题：手动刷新、策略应用、配置应用、来源切换、代理动作的异常都会写入固定回执条；刷新增加 in-flight guard 防止重叠请求乱序

## 2026-07-06 — v10: trusted-local hardening and final closeout evidence
**Agent:** Sisyphus | **Scope:** project
- 当时加固为默认 trusted-local 控制面；后续 `2026-07-07 — configurable LAN control-plane mutation mode` 已调整为默认本机写入、可显式开启可信局域网写入
- `internal/pool/runtime_control.go` 的 `ManualRelease` / `ManualRevalidate` 加入容量不变量保护：respect `maxReady` / `bufferMax`，冲突时返回结构化 `409` 语义
- 配置校验补齐 `pool.max_ready > 0` 与 `pool.max_ready >= pool.min_ready`，避免接受会破坏 ready 池补充语义的无效配置
- `config.jsonc` 与 final-wave F3 evidence 中的 source 名称示例统一到 runtime provider 名称（`scdn-socks5`、`43.135.31.113`）
- 补齐 v10 final-wave evidence：`f1` / `f2` / `f4` 日志落盘，`f3` 记录补入最终浏览器/交互证据交叉引用

## 2026-07-06 — v10: trusted-local operations console docs and example config sync
**Agent:** Sisyphus-Junior | **Scope:** project
- `docs/PROGRESS.md` 与 `docs/PLAN.md` 改写为已交付的 control-plane 模型，不再保留 read-only dashboard 说法
- 文档明确 trusted-local 操作台定位、sidecar `config.override.json` 持久化、live-apply 与 restart-required 边界、4 种 routing mode、source/proxy 控制动作
- `config.jsonc` 示例补齐 `policy`、`control_plane`、`sources.enabled`，并把 `server.web_host` 调整为 `127.0.0.1` 以匹配 trusted-local 操作面定位
- 前端构建与嵌入 workflow 固化为 `npm --prefix ui run build` 后复制 `ui/dist/index.html` 到 `internal/server/dashboard.html`，仓库脚本 `build-dashboard.sh` 执行同一流程
- 复核 `/stats` 兼容语义仍保留 `pool`、`server`、`sources`、`ready_history`、`recent_requests` 顶层字段

## 2026-07-06 — v9 phase 1: allocator-first request acquisition cleanup
**Agent:** Sisyphus-Junior | **Scope:** project
- 删除 `internal/pool/pool.go` 中遗留的 `Pool.Next()` 请求期入口；pool 对 allocator 暴露的 seam 收敛为 `AcquireEntry()` / `AcquireEntryMatching()`
- `internal/pool/pool_next_test.go` 改为直接锁定 `AcquireEntry()` 语义，保留 no-ready、quota/blacklist、按源轮转、同源低分优先的表征测试
- 代码路径确认：`internal/server/proxy.go` 与 `internal/server/socks5_udp.go` 通过 `Allocator.Acquire(...)` 进入请求期选路，`main.go` 负责注入默认 allocator
- 文档更新为 allocator-first ownership model：allocator 负责请求期协调与活跃 lease 隔离，pool 保留生命周期/记账变更所有权
- 明确 phase 1 仅新增 keyed active-lease isolation；后续控制面写接口由 v10 叠加；validator 保持 admission gate
- 验证目标：`go test ./...` 与 callsite inventory 证明请求服务路径中已无 `Pool.Next()` 依赖

## 2026-07-05 — v8: Web 控制面板端口拆分 + SOCKS5 UDP ASSOCIATE
**Agent:** Sisyphus | **Scope:** project
- `server.port` 保持代理监听；新增 `server.web_host` / `server.web_port` 承载 Web 控制面板
- 兼容性修正：代码默认 `web_port=0`（旧配置不自动暴露新 HTTP 面），仓库示例配置显式设置 `8081`
- 代理端口移除 `/`、`/dashboard`、`/stats` 复用逻辑，Web 面板改由独立 HTTP listener 提供
- 新增 `RunWeb(ctx)` 和 `dashboard_test.go`，覆盖独立 Web handler 的 `/` 和 `/stats`
- SOCKS5 `handleSOCKS5` 现在区分 `CONNECT` 与 `UDP ASSOCIATE`
- 新增 `socks5_udp.go`：本地 UDP relay、上游 UDP association、固定首个客户端 `IP:port`、上游 reply 头校验、`FRAG != 0` 丢弃
- 新增 `socks5_udp_test.go`：覆盖成功 relay、分片丢弃、上游失败回复映射、客户端端口固定
- 验证通过：`go test ./internal/server ./internal/config && go build ./...`

## 2026-07-04 — v7: 验证目标多样化
**Agent:** Sisyphus | **Scope:** project
- 新增 `validator.Target` 类型和 `ValidatorConfig.Targets` 列表
- 每次验证从目标列表随机选择，确保代理能访问不同域名
- 向后兼容：`Targets` 为空时 fallback 到 `TargetHost`/`TargetPort`
- 默认目标：`www.xxx.com:443`、`httpbin.org:443`、`example.com:443`
- `config.jsonc` 新增 `targets` 配置项

## 2026-07-04 — v7: P2 兼容性增强（5 项）
**Agent:** Sisyphus | **Scope:** project
- **P2-1 HTTP 正向代理**: 新增 `handleHTTPForward`，支持 GET/POST/PUT/DELETE 等方法的正向代理转发。通过 SOCKS5 上游连接目标服务器，转发请求和响应
- **P2-2 SOCKS5 认证协商**: `handleSOCKS5` 支持 no-auth (0x00) 和 username/password (0x02) 认证方法。优先选 no-auth，fallback 到 username/password，都不支持则返回 0xFF
- **P2-3 验证深度增强**: `testSOCKS5` 解析 HTTP 状态码（`strings.SplitN` + `fmt.Sscanf`），2xx/3xx 通过，4xx/5xx 拒绝。替代原来的 `strings.HasPrefix(resp, "HTTP/")`
- **P2-4 源多样性保证**: `Next()` 按源轮询选择。按源分组 → 字母排序 → 选 `lastSource` 之后的源 → 源内选 Score() 最优。避免单源热点
- **P2-5 CORS 限制**: 移除 `statsHandler` 的 `Access-Control-Allow-Origin: *`，仅允许同源访问
- 所有修改通过 `go build ./...` + `go vet ./...`

## 2026-07-04 — v7: P0 + P1 优化修复（8 项）
**Agent:** Sisyphus | **Scope:** project
- **P0-1 HealthCheck 写锁内 I/O 修复**: 第二次 revalidation pass 移到锁外。用 `passed`/`dead2` 切片在无锁循环中跟踪结果，锁内循环只更新 `lastChecked` 和移除 dead 条目
- **P0-2 Next() 评分系统接入**: fallback 路径遍历 `p.ready` 用 `Score()` 选最低分代理，替代 FIFO `ready[0]`。提取 `blacklistEntry()` 辅助方法消除 4+ 处重复
- **P0-3 非 SOCKS5 代理处理**: 新增 `Pool.Forget()` 方法清理 known map，validator 丢弃非 SOCKS5 代理时调用，允许后续重新抓取
- **P1-1 Validator channel 通知**: `Pool.notifyCh` 缓冲 channel，`Feed()`/`Promote()` 后发信号。worker 用 `select` 等待通知 + 超时，替代 `time.Sleep(500ms)` 忙等待
- **P1-2 releaseExpired 移出热路径**: 从 `Next()` 移除，新增 `ReleaseExpired()` 公开方法，`main.go` 添加 30s ticker 定期调用
- **P1-3 swap-remove**: `blacklistEntry` 用 O(1) swap-remove 替代 O(n) slice shift。因 `Next()` 已用 Score() 选代理，顺序无关
- **P1-4 relay buffer sync.Pool**: 新增 `bufPool = sync.Pool`，`copyWithIdleTimeout` 用 `Get`/`Put` 复用 32KB buffer。200 并发峰值内存从 12.8MB 降至共享池
- **P1-5 重启验证**: `load()` 中过期 blacklist 条目放入 buffer 等待验证，不再直接进入 ready 池
- 所有修改通过 `go build ./...` + `go vet ./...`

## 2026-07-04 — v7: 系统性优化审计（19 项问题发现）
**Agent:** Sisyphus | **Scope:** project
- 全面代码审查，发现 19 个问题，按 P0/P1/P2 分级
- **P0 严重 Bug（3 项）**:
  - HealthCheck 第二次 revalidation pass 在写锁内执行网络 I/O（TestEntry → 6s 超时 × N 个 overdue = 系统冻结）
  - `Next()` 从未调用 `Entry.Score()`，评分系统形同虚设；bestProxy 热点导致单代理承受全部流量
  - Validator 静默丢弃非 SOCKS5 代理（known map 已标记，永不重试，大量可用代理被浪费）
- **P1 性能问题（5 项）**:
  - Validator 忙等待轮询（`time.Sleep(500ms)` → 平均 250ms 延迟）
  - `releaseExpired()` 在 `Next()` 热路径中遍历整个 blacklist，持有写锁
  - ready 列表 O(n) 删除（多处 `append(s[:i], s[i+1:]...)`）
  - relay 每连接分配 32KB buffer，无复用（200 并发 = 6.4MB）
  - 重启后过期 blacklist 条目未经验证直接放入 ready 池
- **P2 兼容性（5 项）**:
  - HTTP 只支持 CONNECT，不支持 GET/POST 正向代理
  - SOCKS5 强制 no-auth，不兼容 username/password 客户端
  - 验证只检查 `HTTP/` 前缀，不检查状态码
  - ready 池无源多样性保证
  - `/stats` CORS `*` 过于宽松
- 计划文档已更新，准备开始 P0 修复

## 2026-07-02 — v5: 对抗式审查两轮 + 全部修复
**Agent:** Sisyphus | **Scope:** project
- 第一轮 5 角度审查（First Principles / Razor / 对抗代码 / 对抗安全 / Murphy）
  - Score() 时间单位错误修复：未测代理返回 `math.MaxFloat64`
  - bestProxy 悬空指针修复：RecordFail / HealthCheck 移除时清空
  - HealthCheck 数据竞态修复：快照 Entry 字段时持锁
  - SSRF 防护：validator 和 Feed 两层拦截私有/链路本地 IP
  - HTTP bufio 数据丢失修复：`prefixedReaderConn` 让 relay 先读缓冲区
  - 验证器 dial 超时：`socks5.Direct` → `&net.Dialer{Timeout}`
  - 验证器 panic recover 保护
  - 连接限流 `maxConcurrentConns=200` + Daili66 延迟 10s
  - Proxy43/Geonode 保持独立；庞各庄遗漏映射修复（port 为 string 类型）
- 第二轮 `/review-work` 5 代理并行审查
  - QA 实测：HTTP CONNECT 401（目标认证）、dashboard HTML、stats JSON、SSRF 单元测试、graceful shutdown 全部 PASS
  - 发现：重复 RecordLatency + DialContext 缺失 + load() SSRF 缺失 + known/blacklist 无限增长
  - 修复：删重复行、Dial→DialContext、load() SSRF 检查+maxReady 上限、blacklist 上限 maxReady*2、清理 config.jsonc 废字段 + openproxylist.go 死代码
- relay 空闲超时配置化：`server.relay_idle_timeout`，可配置 "60s" / "5m" / "0s"（不限）
- 状态：所有已知问题已修复。项目稳定可用。

## 2026-07-02 — v6: 前端标签页重构 + 最终验收
**Agent:** Sisyphus | **Scope:** project
- Vue 3 + Vite + TypeScript + Chart.js 独立项目（`ui/` 目录）
- vite-plugin-singlefile 打包单 HTML（~291KB），零 CDN 依赖
- 3 标签页布局：概览（卡片+趋势图+快速统计）/ 代理源（概要栏+来源表）/ 请求（统计+最近请求）
- 刷新间隔可调（3s/5s/10s/30s）
- `/stats` 新增 `uptime_seconds`，前端显示 "已运行 Xh Ym"
- Oracle 审查 `/stats` 接口：评价 SATISFACTORY，无冲突无冗余
- 最终验收测试：build + vet + HTTP CONNECT (0.73s / 401) + dashboard + stats + graceful shutdown 全部 PASS
- 项目收尾，无遗留 TODO/FIXME

## 2026-07-01 — v4: 健康系统 v2（EWMA + 方差 + bestProxy + 动态检查）
**Agent:** Sisyphus | **Scope:** project
- HealthCheck 重写：快照→无锁测试→评分→选 bestProxy→全池轮询重验
- 质量评分改用 EWMA + 方差（非简单平均），α=0.3 可配置
- 连续失败逐出：3 次建连失败立即释放
- 动态检查间隔：活跃 30s / 空闲 5m
- bestProxy 机制：每次健康检查选最优代理优先使用
- 移除老式闲置超时逻辑，改为定时重验证（revalidate_interval: 1h）
- 去重 map bug 修复：delete(known) 只在确认 entry 在 ready 中时执行
- Score 置信度惩罚：低样本代理不会被选为 bestProxy
- 配置新增 `health` 段（revalidate_interval / ewma_alpha / consecutive_fail_limit / check_interval_active/idle）

## 2026-07-01 — v3: Web dashboard + review fixes
**Agent:** Sisyphus | **Scope:** project
- Vue 3 + Chart.js dashboard at `GET /` with pool cards, source status, ready trend chart, request stats
- JSON stats endpoint at `GET /stats`
- Request counting (total/success/fail/latency) via atomic counters
- Previous review round 5 issues fixed: provider status race (statusField), Daili66 Offline, buffer cap 5000, validator debug logging, log.SetPrefix
- Post-fix review: all 5 agents PASSED
- Thread-safe provider status via automated statusField embedded type (sync.Mutex)

## 2026-06-30 — v2: Multi-source + Provider Status + 5 new sources
**Agent:** Sisyphus | **Scope:** project
- Updated `Provider` interface with `Status()` (Online/Offline/Unknown)
- Dedicated provider files: scdn (SOCKS5 filter, 45% pass), goodips, proxy43 (70% pass!), geonode, openproxylist
- `ListProvider` generic type updated with status tracking
- main.go: 9 providers registered, status display in stats ticker
- Integration test: all 9 providers [✓], curl → literouter → 401 in 0.73s
- Created `ListProvider` in `internal/provider/list.go` — generic URL-based provider with pluggable parsers
- Implemented `ParseProxyScrape`, `ParseProxifly`, `ParseTXT` parsers
- Registered 4 new sources: ProxyScrape (600), Proxifly (552), HProxy (885), Thordata (271)
- Total candidate pool: ~2,300 SOCKS5 proxies
- Integration test passed: curl → proxy → new-source SOCKS5 → literouter → 401
- Existing sources (goodips, 89ip, ihuan) remain in docs as known-low-quality

## 2026-06-30 — v1 complete, domain glossary + ADR recorded
**Agent:** Sisyphus | **Scope:** project
- Validator module (SOCKS5 + TLS + HTTP GET to target)
- Proxy server module (HTTP CONNECT → SOCKS5 upstream, usage counting)
- Config system with `--config` flag, JSON comment support, annotated `config.example.json`
- Log levels via `LevelWriter`: debug/info/warn/error/silent
- `CONTEXT.md` created with unified glossary (13 terms)
- ADR 0001: SOCKS5 upstream protocol decision documented
- Integration test: curl → proxy → SOCKS5 → literouter returns 401 ✅
- All v1 items complete. Moving to Later tasks.

## 2026-06-30 — Go project skeleton built
**Agent:** Sisyphus | **Scope:** project
- Go module init (`github.com/user/randproxy`)
- Core types: `Proxy` struct, `Protocol` enum, `Provider` interface
- Provider impl: `Daili66` (66daili.com, 10s interval, SOCKS5+HTTP)
- Provider impl: `Scdn` (scdn.io, disabled — no HTTPS CONNECT)
- `Fetcher` — multi-provider orchestration, per-provider intervals via goroutines
- `Pool` — buffer → ready → blacklist lifecycle with TTL-based release
- `main.go` — entry point wiring fetcher + pool + signal handling
- Python probe scripts retained in `py-probe/`
- Key finding: 66daili SOCKS5 proxies pass HTTPS to literouter (74% rate)

## 2026-06-30 — Documentation initialized
**Agent:** Sisyphus | **Scope:** project
- Created `docs/` directory with INDEX, PROGRESS, PLAN, LOG
- Project: RandProxy — a Go-based random proxy pool (single-process)
- Status: Architecture discussion phase, no code yet

## 2026-07-03 — 三轮深度审计 + 14 项修复
**Agent:** Sisyphus | **Scope:** project
- **SOCKS5 握手字节错位**: handleConn 吃掉第 1 字节，handleSOCKS5 未正确还原。≥2 auth method 时 curl (97)(7) 失败
- **bestProxy nil interface 崩溃**: `atomic.Value.Store((*Entry)(nil))` 存的是非 nil interface 包裹 nil 指针，解引用 `e.UseCount++` 崩溃
- **handleConn 无 recover**: 任意 handler panic 杀死整个进程。加 recover() 捕获
- **recordLatency 方差**: 使用更新后的 EWMA 算偏差，低估方差 ~alpha×(d-old)。改回 oldEWMA。MAE→MSE 平方偏差
- **load() 不加锁**: New() 里调用 load() 时 p.mu 未持锁。开发环境安全但防御性持锁
- **SkipCheck goroutine 挂起**: 池满时 provider goroutine 永远不退出，close(f.out) 不执行，下游 range 阻塞。加 ctx.Done() 检查
- **statsHandler nil panic**: SetStatsProvider 未调用时 s.stats 为 nil。加 nil guard 返回 `{}`
- **验证器 Dial→DialContext**: 验证器拨号不支持上下文取消，关闭时最多等 6s 超时。testSOCKS5 签名加 ctx
- **lastChecked 锁外写入**: HealthCheck 快照释放锁后直接写 e.lastChecked。改为 updated 切片锁内更新
- **Promote() 防重复**: Promoto 未检查 Entry 是否已在 ready，可能重复入池
- **stripJSONComments 破坏字符串**: JSON 值中的 `//`（如 URL）被误删。加 findCommentStart 状态跟踪
- **ParseDuration 静默失败**: 6 个辅助函数忽略错误返回 0，零值导致黑名单 TTL 立即释放等。加 log.Printf("[WARN]")
- **流式延迟污染 EWMA**: recordLatency 使用 relay 完成后的 total time，流式请求（SSE/chat）几十秒的中继时间算入代理延迟。改为 relay 前截取 connTime
- **上游拨号无超时**: 两个 handler 都用 socks5.Direct（裸 net.Dial 零超时），死代理卡 Linux 默认 2-3 分钟。改为 &net.Dialer{Timeout: 10s}
- **黑名单持久化**: Save/load 仅处理 ready。扩展 readyEntry 加 status + blacklisted_until 字段，重启恢复黑名单状态
