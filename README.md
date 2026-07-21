# RandProxy

自建代理聚合网关。从多个免费源抓取 SOCKS5 代理，自动验证、健康评分、配额管理，暴露 HTTP CONNECT + SOCKS5 下游端口，附带 Web 监控面板。

## 核心特性

- **多源抓取**：支持 9+ 免费代理源（66daili、ProxyScrape、Proxifly、HProxy、ThorData、scdn-socks5、goodips、geonode 等），每源独立间隔、状态追踪
- **三级验证**：SOCKS5 握手 → TLS 握手 → HTTP GET，全部通过才入就绪池
- **健康评分**：EWMA 延迟 + 方差 + 连续失败驱逐 + 动态检查间隔
- **双协议下游**：自动识别 HTTP CONNECT 和 SOCKS5，同端口服务
- **状态持久化**：重启后恢复就绪池和黑名单状态
- **Web 面板**：Vue 3 + Vite 构建，单 HTML 嵌入，零 CDN 依赖

## 架构

```
代理源 → 抓取器 → 缓冲池 → 验证器 → 就绪池 → 代理服务 → 上游 SOCKS5 → 目标
                                      ↳ 黑名单 / 冷却
```

模块边界：

- `internal/provider/` — 代理源适配器
- `internal/fetcher/` — 定时抓取编排
- `internal/validator/` — 候选验证（SOCKS5 → TLS → HTTP）
- `internal/pool/` — 代理生命周期（就绪 / 缓冲 / 黑名单 / 配额 / 健康）
- `internal/server/` — 下游代理处理 + Web 面板 + `/stats` 接口
- `ui/` — Vue 3 仪表盘源码，构建后嵌入二进制

## 快速开始

```bash
go build -o randproxy .
./randproxy
```

使用自定义配置：

```bash
./randproxy --config /path/to/config.jsonc
```

默认端口（来自 `config.jsonc`）：

- 代理监听：`server.host:server.port`（默认 `0.0.0.0:8080`）
- Web 面板：`server.web_host:server.web_port`（默认 `0.0.0.0:8081`）
- 仪表盘路由：`/dashboard`
- 统计接口：`/stats`

客户端使用示例：

```bash
# HTTP CONNECT
export http_proxy=http://127.0.0.1:8080
export https_proxy=http://127.0.0.1:8080
curl https://api.literouter.com/

# SOCKS5
curl --proxy socks5://127.0.0.1:8080 https://api.literouter.com/
```

## 配置

主配置文件：`config.jsonc`，包含中文注释。

运行时控制面写入存储在 `config.override.json`，启动时合并到基础配置。如果 `config.jsonc` 中的值似乎未生效，检查是否有 `config.override.json` 覆盖了它。

需要重启才能生效的字段：

- `server.*`
- `validator.*`
- `health.*`
- `log.*`
- `pool.min_ready`
- `pool.max_ready`
- `pool.buffer_max`
- `pool.state_file`

运行时可热更新的字段：

- `pool.max_use`
- `pool.blacklist_ttl`
- `sources.enabled.*`

## 推荐配置（免费代理场景）

免费代理通过率较低，建议配置：

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
    "targets": [{ "host": "api.literouter.com", "port": 443 }]
  },
  "health": {
    "latency_threshold": "3000ms",
    "consecutive_fail_limit": 3
  }
}
```

## 代理源

当前注册的代理源：

| 名称 | 类型 | 说明 |
|------|------|------|
| `66daili` | API | 66daili.com，10s 间隔 |
| `proxyscrape` | URL 列表 | ProxyScrape API |
| `proxifly` | URL 列表 | proxifly.com 免费列表 |
| `hproxy` | URL 列表 | hugeproxy.com |
| `thordata` | URL 列表 | thordata.com |
| `scdn-socks5` | API | scdn.io SOCKS5 端点 |
| `goodips` | API | goodips.com |
| `geonode` | API | geonode.com 免费层 |
| `43.135.31.113` | API | 自定义端点 |

在 `sources.enabled` 中禁用低质量源：

```jsonc
"sources": {
  "enabled": {
    "66daili": false,
    "scdn-socks5": false
  }
}
```

## 仪表盘构建

修改 `ui/` 后重新构建：

```bash
npm --prefix ui run build
./build-dashboard.sh
```

`build-dashboard.sh` 运行 Vue 构建并将 `ui/dist/index.html` 复制到 `internal/server/dashboard.html`。

## 故障排查

**`503 No proxy available`**：
- 就绪池为空，等待验证器填充
- 增加 `validator.concurrency` 或 `pool.buffer_max`

**`502 Bad gateway` / SOCKS5 错误**：
- 就绪代理在实际请求时失败
- RandProxy 会记录失败并拉黑上游
- 免费代理源可能没有足够可用的代理

**就绪数量持续偏低**：
- 代理源通过率低
- 验证目标可能过于严格或从许多免费代理不可达
- 禁用噪音源，调整 `validator.targets` 与实际目标对齐

**Web 配置更改未生效**：
- 检查 `config.override.json`
- 部分字段需要重启

## 文档

- [docs/INDEX.md](docs/INDEX.md) — 文档索引
- [docs/PROGRESS.md](docs/PROGRESS.md) — 当前状态和架构
- [docs/PLAN.md](docs/PLAN.md) — 已完成工作和后续计划
- [docs/LOG.md](docs/LOG.md) — 变更日志
- [docs/PROVIDERS.md](docs/PROVIDERS.md) — 代理源维护指南
- [CONTEXT.md](CONTEXT.md) — 术语表和项目上下文

## 许可证

MIT
