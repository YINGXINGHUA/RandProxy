export type PolicyMode = 'balanced' | 'random_subset' | 'stable_subset' | 'single_best'

export type FeedbackKind = 'info' | 'success' | 'warning' | 'error'

export interface PoolStats {
  ready: number
  buffer: number
  blacklist: number
}

export interface ServerStats {
  total_requests: number
  success_requests: number
  fail_requests: number
  avg_latency_ms: number
  uptime_seconds: number
  active_connections: number
  active_leases: number
  request_window_1m: RequestWindowStats
  request_window_5m: RequestWindowStats
  request_window_30m: RequestWindowStats
  request_window_24h: RequestWindowStats
  request_window_3d: RequestWindowStats
}

export interface RequestWindowStats {
  total_requests: number
  success_requests: number
  fail_requests: number
  avg_latency_ms: number
}

export interface SourceInfo {
  name: string
  status: string
  total_fetched: number
  fetch_errors: number
  last_fetch: string
  last_error: string
  validated: number
  validation_failed: number
  pass_rate: number
  in_ready: number
}

export interface RequestRecord {
  time: string
  timestamp: string
  target: string
  proxy_ip: string
  latency_ms: number
  success: boolean
}

export interface PolicyConfig {
  mode: PolicyMode
  random_subset_size: number
  stable_subset_size: number
}

export interface ControlPlaneConfig {
  trusted_local_only: boolean
}

export interface OverviewPayload {
  control_plane: ControlPlaneConfig
  policy: PolicyConfig
  pool: PoolStats
  ready_history: number[]
  recent_requests: RequestRecord[]
  server: ServerStats
  sources: SourceInfo[]
}

export interface EffectiveConfigMeta {
  base_path: string
  override_path: string
}

export interface TargetConfig {
  host: string
  port: number
}

export interface ServerConfig {
  host: string
  port: number
  web_host: string
  web_port: number
  relay_idle_timeout: string
}

export interface PoolConfig {
  min_ready: number
  max_ready: number
  max_use: number
  buffer_max: number
  blacklist_ttl: string
  state_file: string
}

export interface ValidatorConfig {
  target_host: string
  target_port: number
  targets: TargetConfig[]
  timeout: string
  concurrency: number
  tls_insecure: boolean
}

export interface HealthConfig {
  revalidate_interval: string
  front_check_count: number
  latency_threshold: string
  ewma_alpha: number
  consecutive_fail_limit: number
  check_interval_active: string
  check_interval_idle: string
}

export interface LogConfig {
  prefix: string
  level: string
  file: string
  file_enable: boolean
}

export interface SourcesConfig {
  enabled: Record<string, boolean>
}

export interface ConfigPayload {
  server: ServerConfig
  pool: PoolConfig
  validator: ValidatorConfig
  health: HealthConfig
  log: LogConfig
  policy: PolicyConfig
  control_plane: ControlPlaneConfig
  sources: SourcesConfig
}

export interface OperationReceipt {
  OK: boolean
  Noop: boolean
  Message: string
}

export interface PersistenceReceipt {
  Attempted: boolean
  Succeeded: boolean
  OverridePath: string
  Error: string
}

export interface ReceiptEnvelope {
  operation: OperationReceipt
  persistence: PersistenceReceipt
}

export interface LastApplyReceipt {
  applied_live_fields: string[] | null
  restart_required_fields: string[] | null
  receipt: ReceiptEnvelope
}

export interface OverviewResponse {
  overview: OverviewPayload
  effective_config_meta: EffectiveConfigMeta
  last_apply_receipt: LastApplyReceipt | null
  restart_required: string[]
}

export interface ConfigResponse extends EffectiveConfigMeta {
  effective_config: ConfigPayload
  live_fields: string[]
  restart_required_fields: string[]
}

export interface ApplyConfigResponse {
  ok: boolean
  effective_config: ConfigPayload
  applied_live_fields: string[] | null
  restart_required_fields: string[] | null
  receipt: ReceiptEnvelope
}

export interface ProxyInventoryEntry {
  proxy_id: string
  ip: string
  port: number
  protocol: string
  source: string
  status: string
  use_count: number
  max_use: number
  active_leases: number
  added_at?: string
  last_used?: string
  blacklisted_until?: string
}

export interface PoolInventoryResponse {
  ready: ProxyInventoryEntry[]
  buffer: ProxyInventoryEntry[]
  blacklist: ProxyInventoryEntry[]
}

export interface PoolActionCounts {
  ready: number
  buffer: number
  blacklist: number
}

export interface PoolActionResponse {
  ok: boolean
  proxy_id: string
  action: string
  receipt: string
  counts: PoolActionCounts
}

export interface SourceToggleResponse {
  ok: boolean
  source: string
  enabled: boolean
  receipt: ReceiptEnvelope
}

export interface ApiErrorPayload {
  ok?: boolean
  code?: string
  message?: string
  action?: string
  proxy_id?: string
  source?: string
  enabled?: boolean
  receipt?: ReceiptEnvelope
}

export interface FeedbackEntry {
  kind: FeedbackKind
  title: string
  message: string
  timestamp: string
  details: string[]
}
