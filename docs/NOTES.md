# Notes

Last updated: 2026-07-07 | Last verified: 2026-07-07

## Open Ideas
- **P95 与 ready 变化趋势**: 请求数、成功率、失败数与平均延迟已有后端窗口统计；P95 延迟和 ready 数变化趋势仍需额外聚合后再展示。
- **活跃 relay 细分状态**: 后端已暴露 active connection 总数与每代理 active lease 数；active lease 覆盖请求/relay 生命周期。若要区分 TCP CONNECT、HTTP forward、UDP ASSOCIATE 等具体活跃类型，需要在 server 层继续细分追踪。
- **长周期 soak test**: `scripts/stress-runtime.go` 已覆盖 60 秒本地并发压测；上线前可增加 1-3 小时 soak 模式，采集内存、goroutine 与窗口统计漂移。
- **代理质量分级设计**: 最终验收通过后，剩余最高价值功能增强是按来源通过率、延迟、失败率为来源/代理分级；这不是验收 blocker。

## Parked Explorations
- **配置语义 diff**: 配置页提供逐行参考 diff；更准确的 JSON 语义 diff 可在编辑体验继续升级时评估。
