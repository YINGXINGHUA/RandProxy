<script setup lang='ts'>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type {
  ApiErrorPayload,
  ApplyConfigResponse,
  ConfigPayload,
  ConfigResponse,
  FeedbackEntry,
  FeedbackKind,
  OverviewPayload,
  OverviewResponse,
  PolicyMode,
  PoolActionResponse,
  PoolInventoryResponse,
  ProxyInventoryEntry,
  RequestRecord,
  RequestWindowStats,
  ReceiptEnvelope,
  SourceInfo,
  SourceToggleResponse,
} from './types'

type ProxyAction = 'blacklist' | 'revalidate' | 'release'
type ConsoleModule = 'overview' | 'pool' | 'sources' | 'policy' | 'config' | 'events'
type TimeRangeKey = '30m' | '24h' | '3d'
type ConfigInfoPanel = 'current' | 'diff' | 'reference'
type PoolFilterKey = 'all' | PoolGroup['key']
type ConfigDiffKind = 'unchanged' | 'added' | 'removed' | 'changed'

interface ConsoleModuleOption {
  readonly key: ConsoleModule
  readonly label: string
  readonly caption: string
}

interface PolicyOption {
  readonly value: PolicyMode
  readonly label: string
  readonly hint: string
}

interface PoolGroup {
  readonly key: 'ready' | 'buffer' | 'blacklist'
  readonly title: string
  readonly entries: ProxyInventoryEntry[]
}

interface PoolFilterOption {
  readonly value: PoolFilterKey
  readonly label: string
}

interface TimeRangeOption {
  readonly value: TimeRangeKey
  readonly label: string
  readonly milliseconds: number
}

interface ConfigReferenceSection {
  readonly title: string
  readonly description: string
  readonly fields: readonly string[]
}

interface ConfigDiffRow {
  readonly key: string
  readonly kind: ConfigDiffKind
  readonly currentLineNumber: number | null
  readonly draftLineNumber: number | null
  readonly currentText: string
  readonly draftText: string
}

interface ConfigDiffResult {
  readonly valid: boolean
  readonly message: string
  readonly rows: ConfigDiffRow[]
  readonly changedCount: number
}

const policyOptions: readonly PolicyOption[] = [
  { value: 'balanced', label: '均衡轮转', hint: '在可用源之间均衡轮转。' },
  { value: 'random_subset', label: '随机子集', hint: '只在前 N 个候选中随机选择。' },
  { value: 'stable_subset', label: '稳定子集', hint: '在前 N 个候选内稳定轮转。' },
  { value: 'single_best', label: '单一最优', hint: '始终优先当前排序第一的代理。' },
] as const

const consoleModules: readonly ConsoleModuleOption[] = [
  { key: 'overview', label: '总览', caption: '运行态势' },
  { key: 'pool', label: '代理池', caption: '库存与动作' },
  { key: 'sources', label: '来源', caption: '抓取开关' },
  { key: 'policy', label: '策略', caption: '路由模式' },
  { key: 'config', label: '配置', caption: '生效与草稿' },
  { key: 'events', label: '事件', caption: '回执与请求' },
] as const

const activeModuleStorageKey = 'randproxy.activeModule'
const sourceVisibleOptions = [8, 16, 32, 64] as const
const poolVisibleOptions = [10, 25, 50, 100] as const
const requestVisibleOptions = [8, 12, 20, 40] as const

const timeRangeOptions: readonly TimeRangeOption[] = [
  { value: '30m', label: '近30分钟', milliseconds: 30 * 60 * 1000 },
  { value: '24h', label: '24小时', milliseconds: 24 * 60 * 60 * 1000 },
  { value: '3d', label: '3天', milliseconds: 3 * 24 * 60 * 60 * 1000 },
] as const

const poolFilterOptions: readonly PoolFilterOption[] = [
  { value: 'all', label: '全部' },
  { value: 'ready', label: '就绪' },
  { value: 'buffer', label: '待验证' },
  { value: 'blacklist', label: '冷却' },
] as const

const configReferenceSections: readonly ConfigReferenceSection[] = [
  { title: 'server', description: '控制代理服务与内置 Web 控制台监听地址，以及连接空闲超时。', fields: ['host / port', 'web_host / web_port', 'relay_idle_timeout'] },
  { title: 'pool', description: '控制就绪池容量、缓冲区上限、单代理使用配额与黑名单冷却时间。', fields: ['min_ready / max_ready', 'max_use', 'buffer_max', 'blacklist_ttl', 'state_file'] },
  { title: 'validator', description: '定义候选代理验证目标、超时、并发数与 TLS 验证行为。', fields: ['target_host / target_port', 'targets', 'timeout', 'concurrency', 'tls_insecure'] },
  { title: 'health', description: '控制已就绪代理的主动复检节奏、延迟阈值与连续失败剔除规则。', fields: ['revalidate_interval', 'front_check_count', 'latency_threshold', 'consecutive_fail_limit'] },
  { title: 'policy', description: '控制下游请求在就绪代理之间的选择方式，可在策略页热更新。', fields: ['mode', 'random_subset_size', 'stable_subset_size'] },
  { title: 'sources', description: '记录各免费代理来源是否启用，来源页开关会写入这里。', fields: ['enabled.<source_name>'] },
] as const

const emptyRequestWindowStats: RequestWindowStats = {
  total_requests: 0,
  success_requests: 0,
  fail_requests: 0,
  avg_latency_ms: 0,
}

function isConsoleModule(value: string | null): value is ConsoleModule {
  return consoleModules.some((module) => module.key === value)
}

function initialActiveModule(): ConsoleModule {
  if (typeof window === 'undefined') return 'overview'
  const saved = window.localStorage.getItem(activeModuleStorageKey)
  return isConsoleModule(saved) ? saved : 'overview'
}

const emptyOverview: OverviewPayload = {
  control_plane: { trusted_local_only: true },
  policy: { mode: 'balanced', random_subset_size: 3, stable_subset_size: 3 },
  pool: { ready: 0, buffer: 0, blacklist: 0 },
  ready_history: [],
  recent_requests: [],
  server: {
    total_requests: 0,
    success_requests: 0,
    fail_requests: 0,
    avg_latency_ms: 0,
    uptime_seconds: 0,
    active_connections: 0,
    active_leases: 0,
    request_window_1m: emptyRequestWindowStats,
    request_window_5m: emptyRequestWindowStats,
    request_window_30m: emptyRequestWindowStats,
    request_window_24h: emptyRequestWindowStats,
    request_window_3d: emptyRequestWindowStats,
  },
  sources: [],
}

const overview = ref<OverviewPayload>(emptyOverview)
const liveConfig = ref<ConfigPayload | null>(null)
const poolInventory = ref<PoolInventoryResponse>({ ready: [], buffer: [], blacklist: [] })
const draftText = ref('')
const draftError = ref('')
const liveFields = ref<string[]>([])
const restartFields = ref<string[]>([])
const basePath = ref('')
const overridePath = ref('')
const lastUpdated = ref('—')
const feedback = ref<FeedbackEntry | null>(null)
const latestOverviewApplyReceiptKey = ref('')
const refreshSeconds = ref(5)
const policyMode = ref<PolicyMode>('balanced')
const randomSubsetSize = ref(3)
const stableSubsetSize = ref(3)
const isBootstrapping = ref(true)
const isRefreshing = ref(false)
const refreshInFlight = ref(false)
const refreshRequestId = ref(0)
const isApplyingDraft = ref(false)
const isApplyingPolicy = ref(false)
const sourceActionName = ref('')
const proxyActionKey = ref('')
const activeModule = ref<ConsoleModule>(initialActiveModule())
const sourceVisibleCount = ref(16)
const poolVisibleCount = ref(25)
const requestVisibleCount = ref(12)
const activeTimeRange = ref<TimeRangeKey>('30m')
const activeConfigInfoPanel = ref<ConfigInfoPanel>('current')
const poolSearchText = ref('')
const activePoolFilter = ref<PoolFilterKey>('all')

let refreshTimer: ReturnType<typeof setInterval> | null = null

function cloneConfig(config: ConfigPayload): ConfigPayload {
  return JSON.parse(JSON.stringify(config)) as ConfigPayload
}

function formatConfig(config: ConfigPayload): string {
  return JSON.stringify(config, null, 2)
}

function nowLabel(): string {
  return new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function sanitizeTestId(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, '_')
}

function policyLabel(mode: PolicyMode): string {
  return policyOptions.find((option) => option.value === mode)?.label ?? mode
}

function feedbackKindLabel(kind: FeedbackKind): string {
  const labels: Record<FeedbackKind, string> = {
    info: '信息',
    success: '成功',
    warning: '注意',
    error: '错误',
  }
  return labels[kind]
}

function sourceStatusLabel(status: string): string {
  if (status === 'online') return '在线'
  if (status === 'offline') return '离线'
  return status || '未知'
}

function proxyActionLabel(action: ProxyAction): string {
  if (action === 'blacklist') return '移入黑名单'
  if (action === 'revalidate') return '重新验证'
  return '解除冷却'
}

function formatUptime(seconds: number): string {
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const remainder = seconds % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${remainder}s`
  return `${remainder}s`
}

function formatTime(value?: string): string {
  if (!value || value.length === 0) return '—'
  const parsed = Date.parse(value)
  if (!Number.isFinite(parsed)) return value
  return new Date(parsed).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function activeTimeRangeOption(): TimeRangeOption {
  return timeRangeOptions.find((option) => option.value === activeTimeRange.value) ?? timeRangeOptions[0]
}

function requestTimestampMs(request: RequestRecord): number | null {
  const parsed = Date.parse(request.timestamp || request.time)
  return Number.isFinite(parsed) ? parsed : null
}

function requestTimeLabel(request: RequestRecord): string {
  return request.timestamp ? formatTime(request.timestamp) : request.time
}

function healthToneClass(tone: 'success' | 'warning' | 'error' | 'muted'): string {
  if (tone === 'success') return 'metric-card--live'
  if (tone === 'warning') return 'metric-card--warning'
  if (tone === 'error') return 'metric-card--danger'
  return 'metric-card--muted'
}

function readyHealthTone(): 'success' | 'warning' | 'error' {
  if (overview.value.pool.ready >= Math.max(overview.value.pool.blacklist, 1)) return 'success'
  if (overview.value.pool.ready > 0) return 'warning'
  return 'error'
}

function successHealthTone(): 'success' | 'warning' | 'error' | 'muted' {
  const rate = successRateNumber.value
  if (rate === null) return 'muted'
  if (rate >= 95) return 'success'
  if (rate >= 80) return 'warning'
  return 'error'
}

function latencyHealthTone(): 'success' | 'warning' | 'error' | 'muted' {
  const latency = selectedRequestWindow.value.avg_latency_ms
  if (selectedRequestWindow.value.total_requests === 0) return 'muted'
  if (latency <= 800) return 'success'
  if (latency <= 2000) return 'warning'
  return 'error'
}

function poolGroupTone(groupKey: PoolGroup['key']): string {
  if (groupKey === 'ready') return 'status-chip--success'
  if (groupKey === 'buffer') return 'status-chip--warning'
  return 'status-chip--muted'
}

function poolGroupLabel(groupKey: PoolGroup['key']): string {
  if (groupKey === 'ready') return '可用'
  if (groupKey === 'buffer') return '待验证'
  return '冷却中'
}

function proxyStatusLabel(entry: ProxyInventoryEntry, groupKey: PoolGroup['key']): string {
  if (groupKey === 'ready') return entry.status || 'ready'
  if (groupKey === 'buffer') return entry.status || 'buffer'
  return entry.status || 'blacklist'
}

function proxyTimelineLabel(entry: ProxyInventoryEntry, groupKey: PoolGroup['key']): string {
  if (groupKey === 'blacklist') return `冷却至 ${formatTime(entry.blacklisted_until)}`
  if (entry.last_used) return `最近使用 ${formatTime(entry.last_used)}`
  if (entry.added_at) return `加入 ${formatTime(entry.added_at)}`
  return '尚无时间记录'
}

function proxyLeaseToneClass(activeLeases: number): string {
  return activeLeases > 0 ? 'lease-chip--active' : 'lease-chip--idle'
}

function normalizedSearchText(value: string): string {
  return value.trim().toLowerCase()
}

function proxySearchText(entry: ProxyInventoryEntry, groupKey: PoolGroup['key']): string {
  return [
    entry.proxy_id,
    entry.ip,
    `${entry.ip}:${entry.port}`,
    String(entry.port),
    entry.source,
    entry.protocol,
    entry.status,
    String(entry.active_leases),
    proxyStatusLabel(entry, groupKey),
    poolGroupLabel(groupKey),
  ].join(' ').toLowerCase()
}

function poolEntryMatches(entry: ProxyInventoryEntry, groupKey: PoolGroup['key'], searchText: string): boolean {
  if (activePoolFilter.value !== 'all' && activePoolFilter.value !== groupKey) return false
  if (searchText.length === 0) return true
  return proxySearchText(entry, groupKey).includes(searchText)
}

function parseDraftForDiff(): ConfigPayload | null {
  try {
    const parsed: unknown = JSON.parse(draftText.value)
    if (typeof parsed !== 'object' || parsed === null) return null
    return parsed as ConfigPayload
  } catch (_error: unknown) {
    return null
  }
}

function diffKindLabel(kind: ConfigDiffKind): string {
  if (kind === 'added') return '新增'
  if (kind === 'removed') return '删除'
  if (kind === 'changed') return '变更'
  return '相同'
}

function setFeedback(kind: FeedbackKind, title: string, message: string, details: string[] = []): void {
  feedback.value = { kind, title, message, details, timestamp: nowLabel() }
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}

function buildReceiptFeedbackKey(title: string, receipt: ReceiptEnvelope, applied: string[] = [], restart: string[] = []): string {
  return `${title}::${receipt.operation.Message}::${receiptDetails(receipt, applied, restart).join('|')}`
}

function receiptDetails(receipt: ReceiptEnvelope, applied: string[] = [], restart: string[] = []): string[] {
  const details = [`操作结果：${receipt.operation.Message}`]
  if (receipt.persistence.OverridePath) details.push(`覆盖配置：${receipt.persistence.OverridePath}`)
  if (receipt.persistence.Error) details.push(`持久化错误：${receipt.persistence.Error}`)
  if (applied.length > 0) details.push(`已热更新字段：${applied.join(', ')}`)
  if (restart.length > 0) details.push(`需重启字段：${restart.join(', ')}`)
  return details
}

function normalizeFieldList(fields: string[] | null | undefined): string[] {
  return Array.isArray(fields) ? fields : []
}

async function readJson<T>(response: Response): Promise<T> {
  return await response.json() as T
}

function isApplyConfigResponse(payload: ApplyConfigResponse | ApiErrorPayload): payload is ApplyConfigResponse {
  return 'ok' in payload && payload.ok === true && 'receipt' in payload && 'effective_config' in payload
}

function isSourceToggleResponse(payload: SourceToggleResponse | ApiErrorPayload): payload is SourceToggleResponse {
  return 'ok' in payload && payload.ok === true && 'receipt' in payload
}

function isPoolActionResponse(payload: PoolActionResponse | ApiErrorPayload): payload is PoolActionResponse {
  return 'ok' in payload && payload.ok === true && 'counts' in payload
}

function syncDraft(config: ConfigPayload): void {
  draftText.value = formatConfig(config)
  draftError.value = ''
}

function parseDraftConfig(): ConfigPayload | null {
  try {
    const parsed: unknown = JSON.parse(draftText.value)
    if (typeof parsed !== 'object' || parsed === null) {
      draftError.value = '草稿内容必须是 JSON 对象。'
      return null
    }
    draftError.value = ''
    return parsed as ConfigPayload
  } catch (error: unknown) {
    draftError.value = error instanceof Error ? error.message : '草稿 JSON 无法解析。'
    return null
  }
}

function sourceEnabled(sourceName: string): boolean {
  return liveConfig.value?.sources.enabled[sourceName] !== false
}

function isLatestRefresh(requestId: number): boolean {
  return requestId === refreshRequestId.value
}

async function fetchOverview(requestId: number): Promise<void> {
  const response = await fetch('/api/v1/overview')
  if (!response.ok) {
    const payload = await readJson<ApiErrorPayload>(response)
    throw new Error(payload.message ?? `overview request failed with ${response.status}`)
  }
  const payload = await readJson<OverviewResponse>(response)
  if (!isLatestRefresh(requestId)) return
  overview.value = payload.overview
  basePath.value = payload.effective_config_meta.base_path
  overridePath.value = payload.effective_config_meta.override_path
  lastUpdated.value = nowLabel()
  if (payload.last_apply_receipt !== null) {
    const receipt = payload.last_apply_receipt.receipt
    const restartRequiredFields = normalizeFieldList(payload.last_apply_receipt.restart_required_fields)
    const appliedLiveFields = normalizeFieldList(payload.last_apply_receipt.applied_live_fields)
    const feedbackKey = buildReceiptFeedbackKey('最近应用回执', receipt, appliedLiveFields, restartRequiredFields)
    if (feedbackKey !== latestOverviewApplyReceiptKey.value) {
      latestOverviewApplyReceiptKey.value = feedbackKey
      if (feedback.value !== null && feedback.value.title !== '最近应用回执') return
      setFeedback(
        receipt.operation.OK ? (restartRequiredFields.length > 0 ? 'warning' : 'success') : 'error',
        '最近应用回执',
        receipt.operation.Message,
        receiptDetails(receipt, appliedLiveFields, restartRequiredFields),
      )
    }
  }
}

async function fetchConfig(requestId: number): Promise<void> {
  const response = await fetch('/api/v1/config')
  if (!response.ok) {
    const payload = await readJson<ApiErrorPayload>(response)
    throw new Error(payload.message ?? `config request failed with ${response.status}`)
  }
  const payload = await readJson<ConfigResponse>(response)
  if (!isLatestRefresh(requestId)) return
  const shouldSyncDraft = liveConfig.value === null || draftText.value === formatConfig(liveConfig.value)
  const shouldSyncPolicyDraft = liveConfig.value === null || !policyDirty.value
  liveConfig.value = cloneConfig(payload.effective_config)
  if (shouldSyncDraft) syncDraft(payload.effective_config)
  if (shouldSyncPolicyDraft) {
    policyMode.value = payload.effective_config.policy.mode
    randomSubsetSize.value = payload.effective_config.policy.random_subset_size
    stableSubsetSize.value = payload.effective_config.policy.stable_subset_size
  }
  liveFields.value = payload.live_fields
  restartFields.value = payload.restart_required_fields
  basePath.value = payload.base_path
  overridePath.value = payload.override_path
}

async function fetchPool(requestId: number): Promise<void> {
  const response = await fetch('/api/v1/pool')
  if (!response.ok) {
    const payload = await readJson<ApiErrorPayload>(response)
    throw new Error(payload.message ?? `pool request failed with ${response.status}`)
  }
  const payload = await readJson<PoolInventoryResponse>(response)
  if (!isLatestRefresh(requestId)) return
  poolInventory.value = payload
}

async function refreshAll(force = false): Promise<void> {
  if (refreshInFlight.value && !force) return
  const requestId = refreshRequestId.value + 1
  refreshRequestId.value = requestId
  refreshInFlight.value = true
  if (!isBootstrapping.value) isRefreshing.value = true
  try {
    await Promise.all([fetchOverview(requestId), fetchConfig(requestId), fetchPool(requestId)])
  } finally {
    if (isLatestRefresh(requestId)) {
      isBootstrapping.value = false
      isRefreshing.value = false
      refreshInFlight.value = false
    }
  }
}

async function refreshFromUser(): Promise<void> {
  try {
    await refreshAll()
  } catch (error: unknown) {
    setFeedback('error', '刷新失败', errorMessage(error, '刷新失败。'))
  }
}

function resetRefreshTimer(): void {
  if (refreshTimer !== null) clearInterval(refreshTimer)
  refreshTimer = setInterval(() => {
    void refreshAll().catch((error: unknown) => {
      const message = error instanceof Error ? error.message : '刷新失败。'
      setFeedback('error', '刷新失败', message)
    })
  }, refreshSeconds.value * 1000)
}

async function applyConfigPayload(config: ConfigPayload, title: string): Promise<void> {
  const response = await fetch('/api/v1/config', {
    method: 'PUT',
    headers: controlPlaneMutationHeaders('application/json'),
    body: JSON.stringify(config),
  })
  const payload = await readJson<ApplyConfigResponse | ApiErrorPayload>(response)
  if (!response.ok || !isApplyConfigResponse(payload)) {
    const message = 'message' in payload && typeof payload.message === 'string' ? payload.message : `${title}失败。`
    setFeedback('error', title, message)
    return
  }
  const restartRequiredFields = normalizeFieldList(payload.restart_required_fields)
  const appliedLiveFields = normalizeFieldList(payload.applied_live_fields)
  liveConfig.value = cloneConfig(payload.effective_config)
  syncDraft(payload.effective_config)
  await refreshAll(true)
  setFeedback(
    restartRequiredFields.length > 0 ? 'warning' : 'success',
    title,
    payload.receipt.operation.Message,
    receiptDetails(payload.receipt, appliedLiveFields, restartRequiredFields),
  )
}

async function applyDraftConfig(): Promise<void> {
  const parsed = parseDraftConfig()
  if (parsed === null) return
  isApplyingDraft.value = true
  try {
    await applyConfigPayload(parsed, '配置已应用')
  } catch (error: unknown) {
    setFeedback('error', '配置应用失败', errorMessage(error, '配置应用失败。'))
  } finally {
    isApplyingDraft.value = false
  }
}

async function applyPolicy(): Promise<void> {
  if (liveConfig.value === null) return
  isApplyingPolicy.value = true
  try {
    const nextConfig = cloneConfig(liveConfig.value)
    nextConfig.policy.mode = policyMode.value
    nextConfig.policy.random_subset_size = randomSubsetSize.value
    nextConfig.policy.stable_subset_size = stableSubsetSize.value
    await applyConfigPayload(nextConfig, '策略已更新')
  } catch (error: unknown) {
    setFeedback('error', '策略应用失败', errorMessage(error, '策略应用失败。'))
  } finally {
    isApplyingPolicy.value = false
  }
}

async function toggleSource(source: SourceInfo, enabled: boolean): Promise<void> {
  sourceActionName.value = source.name
  try {
    const action = enabled ? 'enable' : 'disable'
    const response = await fetch(`/api/v1/sources/${encodeURIComponent(source.name)}/${action}`, {
      method: 'POST',
      headers: controlPlaneMutationHeaders(),
    })
    const payload = await readJson<SourceToggleResponse | ApiErrorPayload>(response)
    if (!response.ok || !isSourceToggleResponse(payload)) {
      const message = 'message' in payload && typeof payload.message === 'string' ? payload.message : `${source.name} 更新失败。`
      setFeedback('error', '来源变更失败', message)
      return
    }
    await refreshAll(true)
    setFeedback(enabled ? 'success' : 'warning', enabled ? '来源已启用' : '来源已停用', payload.receipt.operation.Message, receiptDetails(payload.receipt))
  } catch (error: unknown) {
    setFeedback('error', '来源变更失败', errorMessage(error, `${source.name} 更新失败。`))
  } finally {
    sourceActionName.value = ''
  }
}

async function runProxyAction(entry: ProxyInventoryEntry, action: ProxyAction): Promise<void> {
  proxyActionKey.value = `${action}:${entry.proxy_id}`
  try {
    const response = await fetch(`/api/v1/pool/proxies/${encodeURIComponent(entry.proxy_id)}/${action}`, {
      method: 'POST',
      headers: controlPlaneMutationHeaders(),
    })
    const payload = await readJson<PoolActionResponse | ApiErrorPayload>(response)
    if (!response.ok || !isPoolActionResponse(payload)) {
      const message = 'message' in payload && typeof payload.message === 'string' ? payload.message : `${proxyActionLabel(action)}失败。`
      setFeedback('error', '代理操作失败', message)
      return
    }
    await refreshAll(true)
    setFeedback(action === 'blacklist' ? 'warning' : 'success', proxyActionLabel(action), payload.receipt, [
      `代理：${payload.proxy_id}`,
      `计数：就绪=${payload.counts.ready}，缓冲=${payload.counts.buffer}，黑名单=${payload.counts.blacklist}`,
    ])
  } catch (error: unknown) {
    setFeedback('error', '代理操作失败', errorMessage(error, `${proxyActionLabel(action)}失败。`))
  } finally {
    proxyActionKey.value = ''
  }
}

function controlPlaneMutationHeaders(contentType?: string): HeadersInit {
  const headers: Record<string, string> = { 'X-RandProxy-Control': '1' }
  if (contentType !== undefined) headers['Content-Type'] = contentType
  return headers
}

const selectedRequestWindow = computed<RequestWindowStats>(() => {
  if (activeTimeRange.value === '24h') return overview.value.server.request_window_24h
  if (activeTimeRange.value === '3d') return overview.value.server.request_window_3d
  return overview.value.server.request_window_30m
})

const successRateNumber = computed<number | null>(() => {
  if (selectedRequestWindow.value.total_requests === 0) return null
  return (selectedRequestWindow.value.success_requests / selectedRequestWindow.value.total_requests) * 100
})

const successRate = computed(() => successRateNumber.value === null ? '—' : `${successRateNumber.value.toFixed(1)}%`)

const averageLatencyLabel = computed(() => {
  if (selectedRequestWindow.value.total_requests === 0) return '—'
  return `${selectedRequestWindow.value.avg_latency_ms.toFixed(1)}ms`
})

const readyHealthLabel = computed(() => {
  const tone = readyHealthTone()
  if (tone === 'success') return '健康'
  if (tone === 'warning') return '偏低'
  return '缺货'
})

const successHealthLabel = computed(() => {
  const tone = successHealthTone()
  if (tone === 'success') return '稳定'
  if (tone === 'warning') return '观察'
  if (tone === 'error') return '异常'
  return '暂无请求'
})

const latencyHealthLabel = computed(() => {
  const tone = latencyHealthTone()
  if (tone === 'success') return '顺畅'
  if (tone === 'warning') return '偏慢'
  if (tone === 'error') return '拥塞'
  return '暂无请求'
})

const draftDirty = computed(() => {
  if (liveConfig.value === null) return false
  return draftText.value !== formatConfig(liveConfig.value)
})

const policyDirty = computed(() => {
  if (liveConfig.value === null) return false
  return policyMode.value !== liveConfig.value.policy.mode
    || randomSubsetSize.value !== liveConfig.value.policy.random_subset_size
    || stableSubsetSize.value !== liveConfig.value.policy.stable_subset_size
})

const currentPolicyHint = computed(() => policyOptions.find((option) => option.value === policyMode.value)?.hint ?? '')

const livePolicyHint = computed(() => {
  const liveMode = liveConfig.value?.policy.mode ?? overview.value.policy.mode
  return policyOptions.find((option) => option.value === liveMode)?.hint ?? ''
})

const sortedSources = computed(() => [...overview.value.sources].sort((left, right) => left.name.localeCompare(right.name, 'zh-CN')))

const visibleSources = computed(() => sortedSources.value.slice(0, sourceVisibleCount.value))

const hiddenSourceCount = computed(() => Math.max(sortedSources.value.length - visibleSources.value.length, 0))

const poolGroups = computed<PoolGroup[]>(() => [
  { key: 'ready', title: '就绪池', entries: poolInventory.value.ready },
  { key: 'buffer', title: '缓冲区', entries: poolInventory.value.buffer },
  { key: 'blacklist', title: '黑名单', entries: poolInventory.value.blacklist },
])

const poolSearchQuery = computed(() => normalizedSearchText(poolSearchText.value))

const visiblePoolGroups = computed(() => poolGroups.value.map((group) => ({
  ...group,
  filteredEntries: group.entries.filter((entry) => poolEntryMatches(entry, group.key, poolSearchQuery.value)),
})).map((group) => ({
  ...group,
  visibleEntries: group.filteredEntries.slice(0, poolVisibleCount.value),
  hiddenCount: Math.max(group.filteredEntries.length - poolVisibleCount.value, 0),
})))

const totalPoolCount = computed(() => poolInventory.value.ready.length + poolInventory.value.buffer.length + poolInventory.value.blacklist.length)

const filteredPoolCount = computed(() => visiblePoolGroups.value.reduce((total, group) => total + group.filteredEntries.length, 0))

const activeTimeRangeLabel = computed(() => activeTimeRangeOption().label)

const filteredRequestRecords = computed(() => {
  const cutoffMs = Date.now() - activeTimeRangeOption().milliseconds
  return overview.value.recent_requests
    .map((request, index) => ({ request, index, timestampMs: requestTimestampMs(request) }))
    .filter((item) => item.timestampMs === null || item.timestampMs >= cutoffMs)
    .sort((left, right) => {
      if (left.timestampMs !== null && right.timestampMs !== null) return right.timestampMs - left.timestampMs
      if (left.timestampMs !== null) return -1
      if (right.timestampMs !== null) return 1
      return right.index - left.index
    })
    .map((item) => item.request)
})

const recentRequests = computed(() => filteredRequestRecords.value.slice(0, requestVisibleCount.value))

const recentFailures = computed(() => filteredRequestRecords.value.filter((request) => !request.success).slice(0, requestVisibleCount.value))

const hiddenRequestCount = computed(() => Math.max(filteredRequestRecords.value.length - recentRequests.value.length, 0))

const liveConfigText = computed(() => {
  if (liveConfig.value === null) return '等待有效配置...'
  return formatConfig(liveConfig.value)
})

const configDiff = computed<ConfigDiffResult>(() => {
  if (liveConfig.value === null) {
    return { valid: false, message: '等待生效配置后再生成差异参考。', rows: [], changedCount: 0 }
  }

  const parsedDraft = parseDraftForDiff()
  if (parsedDraft === null) {
    return { valid: false, message: '草稿 JSON 暂无效，配置差异会等待草稿可解析后再显示。', rows: [], changedCount: 0 }
  }

  const currentLines = formatConfig(liveConfig.value).split('\n')
  const draftLines = formatConfig(parsedDraft).split('\n')
  const maxLineCount = Math.max(currentLines.length, draftLines.length)
  const rows: ConfigDiffRow[] = []

  for (let index = 0; index < maxLineCount; index += 1) {
    const currentText = currentLines[index]
    const draftLineText = draftLines[index]
    const currentLineNumber = currentText === undefined ? null : index + 1
    const draftLineNumber = draftLineText === undefined ? null : index + 1
    const kind: ConfigDiffKind = currentText === draftLineText
      ? 'unchanged'
      : currentText === undefined
        ? 'added'
        : draftLineText === undefined
          ? 'removed'
          : 'changed'

    rows.push({
      key: `config-diff-${index}`,
      kind,
      currentLineNumber,
      draftLineNumber,
      currentText: currentText ?? '',
      draftText: draftLineText ?? '',
    })
  }

  const changedCount = rows.filter((row) => row.kind !== 'unchanged').length
  return {
    valid: true,
    message: changedCount === 0 ? '草稿与当前配置逐行一致。' : `逐行参考发现 ${changedCount} 行差异。`,
    rows,
    changedCount,
  }
})

const trustedLocalLabel = computed(() => overview.value.control_plane.trusted_local_only ? '仅本机写入' : '允许远程写入')

function moduleCount(module: ConsoleModule): string {
  if (module === 'pool') return String(totalPoolCount.value)
  if (module === 'sources') return String(sortedSources.value.length)
  if (module === 'events') return String(overview.value.recent_requests.length)
  if (module === 'policy') return policyLabel(overview.value.policy.mode)
  if (module === 'config') return draftDirty.value ? '有草稿' : '已同步'
  return isBootstrapping.value ? '启动中' : lastUpdated.value
}

watch(activeModule, (nextModule) => {
  if (typeof window !== 'undefined') window.localStorage.setItem(activeModuleStorageKey, nextModule)
})

onMounted(() => {
  void refreshAll().catch((error: unknown) => {
    const message = error instanceof Error ? error.message : '控制台启动失败。'
    setFeedback('error', '控制台启动失败', message)
  }).finally(() => {
    if (!feedback.value) {
      setFeedback('info', '控制台已连接', '控制台已连接到本机可信控制平面。', [
        `基础配置：${basePath.value}`,
        `覆盖配置：${overridePath.value}`,
      ])
    }
    resetRefreshTimer()
  })
})

onUnmounted(() => {
  if (refreshTimer !== null) clearInterval(refreshTimer)
})
</script>

<template>
  <div class='console-shell'>
    <aside class='console-sidebar' aria-label='控制台模块'>
      <div class='brand-block'>
        <p class='brand-kicker'>RandProxy</p>
        <h1>运行控制台</h1>
        <p>代理池、来源、策略与配置变更集中在一个本机可信操作面板。</p>
      </div>

      <nav class='module-nav'>
        <button
          v-for='module in consoleModules'
          :key='module.key'
          class='module-tab'
          :class='{ "module-tab--active": activeModule === module.key }'
          type='button'
          @click='activeModule = module.key'
        >
          <span>
            <strong>{{ module.label }}</strong>
            <small>{{ module.caption }}</small>
          </span>
          <em>{{ moduleCount(module.key) }}</em>
        </button>
      </nav>

      <div class='sidebar-controls'>
        <div class='sidebar-badges'>
          <span class='status-chip status-chip--info'>{{ trustedLocalLabel }}</span>
          <span class='status-chip' :class='draftDirty ? "status-chip--warning" : "status-chip--success"'>
            {{ draftDirty ? '草稿未应用' : '草稿已同步' }}
          </span>
        </div>

        <button class='ghost-button sidebar-refresh' type='button' :disabled='isRefreshing' @click='refreshFromUser'>
          {{ isRefreshing ? '刷新中...' : '立即刷新' }}
        </button>
      </div>
    </aside>

    <main class='console-workspace'>
      <section class='top-toolbar' data-testid='feedback-surface' aria-live='polite'>
        <div class='toolbar-feedback'>
          <span v-if='feedback' class='feedback-pill' :data-kind='feedback.kind'>{{ feedbackKindLabel(feedback.kind) }}</span>
          <span v-else class='feedback-pill' data-kind='info'>等待</span>
          <div>
            <p class='receipt-title'>{{ feedback?.title ?? '等待控制平面回执' }}</p>
            <p class='receipt-message'>{{ feedback?.message ?? '执行配置、来源或代理操作后，最新结果会固定显示在这里。' }}</p>
          </div>
        </div>
        <div class='workspace-controls'>
          <button class='ghost-button compact-button' type='button' :disabled='isRefreshing' @click='refreshFromUser'>
            {{ isRefreshing ? '刷新中...' : '刷新' }}
          </button>
          <label class='inline-field'>
            <span>间隔</span>
            <select v-model.number='refreshSeconds' @change='resetRefreshTimer'>
              <option :value='3'>3 秒</option>
              <option :value='5'>5 秒</option>
              <option :value='10'>10 秒</option>
              <option :value='30'>30 秒</option>
            </select>
          </label>
          <label class='inline-field'>
            <span>时间</span>
            <select v-model='activeTimeRange'>
              <option v-for='option in timeRangeOptions' :key='option.value' :value='option.value'>{{ option.label }}</option>
            </select>
          </label>
          <span class='panel-meta'>更新 {{ lastUpdated }}</span>
        </div>
      </section>

      <section v-if='activeModule === "overview"' class='module-panel' data-testid='overview-section'>
        <div class='panel-heading'>
          <div>
            <p class='panel-kicker'>总览</p>
            <h2>运行态势</h2>
          </div>
          <span class='panel-meta'>{{ isBootstrapping ? '启动中...' : `更新于 ${lastUpdated}` }}</span>
        </div>

        <div class='metric-grid'>
          <div class='metric-card' :class='healthToneClass(readyHealthTone())'>
            <span>就绪代理</span>
            <strong>{{ overview.pool.ready }}</strong>
            <em>{{ readyHealthLabel }}</em>
          </div>
          <div class='metric-card' :class='overview.server.active_leases > 0 ? "metric-card--live" : "metric-card--muted"'>
            <span>活跃租约</span>
            <strong>{{ overview.server.active_leases }}</strong>
            <em>分配中</em>
          </div>
          <div class='metric-card' :class='overview.server.active_connections > 0 ? "metric-card--live" : "metric-card--muted"'>
            <span>活跃连接</span>
            <strong>{{ overview.server.active_connections }}</strong>
            <em>TCP 中继</em>
          </div>
          <div class='metric-card metric-card--muted'>
            <span>{{ activeTimeRangeLabel }}请求</span>
            <strong>{{ selectedRequestWindow.total_requests }}</strong>
            <em>成功 {{ selectedRequestWindow.success_requests }} / 失败 {{ selectedRequestWindow.fail_requests }}</em>
          </div>
          <div class='metric-card' :class='healthToneClass(successHealthTone())'>
            <span>{{ activeTimeRangeLabel }}成功率</span>
            <strong>{{ successRate }}</strong>
            <em>{{ successHealthLabel }}</em>
          </div>
          <div class='metric-card' :class='healthToneClass(latencyHealthTone())'>
            <span>{{ activeTimeRangeLabel }}平均延迟</span>
            <strong>{{ averageLatencyLabel }}</strong>
            <em>{{ latencyHealthLabel }}</em>
          </div>
        </div>

        <div class='overview-grid overview-grid--compact'>
          <article class='surface-panel'>
            <p class='subheading'>运行元数据</p>
            <dl class='meta-list'>
              <div><dt>累计请求</dt><dd>{{ overview.server.total_requests }}</dd></div>
              <div><dt>累计成功 / 失败</dt><dd>{{ overview.server.success_requests }} / {{ overview.server.fail_requests }}</dd></div>
              <div><dt>累计平均延迟</dt><dd>{{ overview.server.total_requests === 0 ? '—' : `${overview.server.avg_latency_ms.toFixed(1)}ms` }}</dd></div>
              <div><dt>代理池</dt><dd>待验证 {{ overview.pool.buffer }} / 冷却 {{ overview.pool.blacklist }}</dd></div>
              <div><dt>运行时长</dt><dd>{{ formatUptime(overview.server.uptime_seconds) }}</dd></div>
              <div><dt>写入边界</dt><dd>{{ overview.control_plane.trusted_local_only ? '仅本机' : '远程可写' }}</dd></div>
              <div><dt>策略</dt><dd>{{ policyLabel(overview.policy.mode) }}</dd></div>
            </dl>
          </article>

          <article class='surface-panel'>
            <div class='subheading-row'>
              <p class='subheading'>最近请求</p>
              <span class='panel-meta'>{{ activeTimeRangeLabel }} · 显示 {{ recentRequests.length }} / {{ filteredRequestRecords.length }}</span>
            </div>
            <div v-if='recentRequests.length === 0' class='empty-state'>当前时间范围暂无请求记录。</div>
            <ul v-else class='request-list request-list--overview'>
              <li v-for='request in recentRequests' :key='`${request.timestamp || request.time}-${request.target}-${request.proxy_ip}`' class='request-item'>
                <div>
                  <strong>{{ request.target }}</strong>
                  <p class='table-meta'>{{ request.proxy_ip }} · {{ requestTimeLabel(request) }}</p>
                </div>
                <div class='request-metrics'>
                  <span class='mono'>{{ request.latency_ms }}ms</span>
                  <span class='status-chip' :class='request.success ? "status-chip--success" : "status-chip--error"'>
                    {{ request.success ? '成功' : '失败' }}
                  </span>
                </div>
              </li>
            </ul>
            <p v-if='hiddenRequestCount > 0' class='table-note'>还有 {{ hiddenRequestCount }} 条在当前时间范围内，可到事件页调高显示条数。</p>
          </article>
        </div>
      </section>

      <section v-else-if='activeModule === "pool"' class='module-panel'>
        <div class='panel-heading'>
          <div>
            <p class='panel-kicker'>代理池</p>
            <h2>库存与代理动作</h2>
          </div>
          <div class='toolbar'>
            <label class='inline-field pool-search-field'>
              <span>搜索</span>
              <input v-model='poolSearchText' type='search' placeholder='ID / IP / 来源 / 协议 / 状态'>
            </label>
            <div class='segmented-control' aria-label='代理池分组筛选'>
              <button
                v-for='option in poolFilterOptions'
                :key='option.value'
                type='button'
                :class='{ "segmented-control__item--active": activePoolFilter === option.value }'
                @click='activePoolFilter = option.value'
              >
                {{ option.label }}
              </button>
            </div>
            <label class='inline-field'>
              <span>每组显示</span>
              <select v-model.number='poolVisibleCount'>
                <option v-for='option in poolVisibleOptions' :key='option' :value='option'>{{ option }}</option>
              </select>
            </label>
            <span class='panel-meta'>筛选 {{ filteredPoolCount }} / {{ totalPoolCount }} 个代理</span>
          </div>
        </div>

        <div class='pool-columns'>
          <article v-for='group in visiblePoolGroups' :key='group.key' class='pool-group'>
            <div class='group-header'>
              <h3>{{ group.title }}</h3>
              <span>显示 {{ group.visibleEntries.length }} / {{ group.filteredEntries.length }} · 总 {{ group.entries.length }}</span>
            </div>

            <div v-if='group.entries.length === 0' class='empty-state'>{{ group.title }}暂无代理。</div>
            <div v-else-if='group.filteredEntries.length === 0' class='empty-state'>当前筛选下暂无{{ group.title }}代理。</div>

            <div v-else class='proxy-card-list'>
              <article v-for='entry in group.visibleEntries' :key='`${group.key}-${entry.proxy_id}`' class='proxy-card'>
                <div class='proxy-card-main'>
                  <span class='state-dot' :data-state='group.key' aria-hidden='true'></span>
                  <div>
                    <strong class='mono'>{{ entry.proxy_id }}</strong>
                    <p class='table-meta'>{{ entry.source || '未知来源' }} · {{ entry.protocol || '未知协议' }} · {{ proxyStatusLabel(entry, group.key) }}</p>
                  </div>
                </div>

                <div class='proxy-card-meta'>
                  <span class='status-chip' :class='poolGroupTone(group.key)'>{{ poolGroupLabel(group.key) }}</span>
                  <span class='lease-chip' :class='proxyLeaseToneClass(entry.active_leases)'>
                    <span class='lease-dot' aria-hidden='true'></span>
                    租约 {{ entry.active_leases }}
                  </span>
                  <span class='mono'>{{ entry.use_count }} / {{ entry.max_use }}</span>
                  <span>{{ proxyTimelineLabel(entry, group.key) }}</span>
                </div>

                <div class='proxy-card-actions'>
                  <button
                    v-if='group.key === "ready"'
                    class='danger-button compact-button'
                    type='button'
                    :data-testid='`proxy-action-blacklist-${sanitizeTestId(entry.proxy_id)}`'
                    :disabled='proxyActionKey === `blacklist:${entry.proxy_id}`'
                    @click='runProxyAction(entry, "blacklist")'
                  >
                    {{ proxyActionKey === `blacklist:${entry.proxy_id}` ? '处理中...' : '移入黑名单' }}
                  </button>
                  <button
                    v-if='group.key !== "buffer"'
                    class='compact-button'
                    type='button'
                    :data-testid='`proxy-action-revalidate-${sanitizeTestId(entry.proxy_id)}`'
                    :disabled='proxyActionKey === `revalidate:${entry.proxy_id}`'
                    @click='runProxyAction(entry, "revalidate")'
                  >
                    {{ proxyActionKey === `revalidate:${entry.proxy_id}` ? '验证中...' : '重新验证' }}
                  </button>
                  <button
                    v-if='group.key === "blacklist"'
                    class='compact-button'
                    type='button'
                    :data-testid='`proxy-action-release-${sanitizeTestId(entry.proxy_id)}`'
                    :disabled='proxyActionKey === `release:${entry.proxy_id}`'
                    @click='runProxyAction(entry, "release")'
                  >
                    {{ proxyActionKey === `release:${entry.proxy_id}` ? '释放中...' : '解除冷却' }}
                  </button>
                </div>
              </article>
            </div>
            <p v-if='group.hiddenCount > 0' class='table-note'>还有 {{ group.hiddenCount }} 个未显示，可调高每组显示数量。</p>
          </article>
        </div>
      </section>

      <section v-else-if='activeModule === "sources"' class='module-panel'>
        <div class='panel-heading'>
          <div>
            <p class='panel-kicker'>来源</p>
            <h2>抓取来源开关</h2>
          </div>
          <div class='toolbar'>
            <label class='inline-field'>
              <span>显示来源</span>
              <select v-model.number='sourceVisibleCount'>
                <option v-for='option in sourceVisibleOptions' :key='option' :value='option'>{{ option }}</option>
              </select>
            </label>
            <span class='panel-meta'>共 {{ sortedSources.length }} 个来源</span>
          </div>
        </div>

        <div class='table-shell table-shell--module'>
          <table class='operator-table'>
            <thead>
              <tr>
                <th>名称</th>
                <th>状态</th>
                <th>就绪</th>
                <th>通过率</th>
                <th>最后拉取</th>
                <th>动作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for='source in visibleSources' :key='source.name'>
                <td>
                  <strong>{{ source.name }}</strong>
                  <div class='table-meta'>验证 {{ source.validated }} / 失败 {{ source.validation_failed }}</div>
                </td>
                <td>
                  <span class='status-chip' :class='sourceEnabled(source.name) ? "status-chip--success" : "status-chip--muted"'>
                    {{ sourceEnabled(source.name) ? '启用' : '停用' }}
                  </span>
                  <div class='table-meta'>{{ sourceStatusLabel(source.status) }}</div>
                </td>
                <td class='mono'>{{ source.in_ready }}</td>
                <td class='mono'>{{ source.pass_rate.toFixed(1) }}%</td>
                <td>{{ formatTime(source.last_fetch) }}</td>
                <td>
                  <button
                    type='button'
                    :data-testid='`source-toggle-${sanitizeTestId(source.name)}`'
                    :disabled='sourceActionName === source.name'
                    @click='toggleSource(source, !sourceEnabled(source.name))'
                  >
                    {{ sourceActionName === source.name ? '处理中...' : (sourceEnabled(source.name) ? '停用' : '启用') }}
                  </button>
                </td>
              </tr>
              <tr v-if='visibleSources.length === 0'>
                <td colspan='6' class='empty-cell'>暂无来源。</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if='hiddenSourceCount > 0' class='table-note'>还有 {{ hiddenSourceCount }} 个来源未显示。</p>
      </section>

      <section v-else-if='activeModule === "policy"' class='module-panel'>
        <div class='panel-heading'>
          <div>
            <p class='panel-kicker'>策略</p>
            <h2>实时路由模式</h2>
          </div>
          <span class='panel-meta'>{{ policyDirty ? '有未应用变更' : '与生效配置一致' }}</span>
        </div>

        <div class='policy-layout'>
          <article class='surface-panel surface-panel--strong'>
            <span class='field-label'>当前生效模式</span>
            <strong class='policy-mode'>{{ policyLabel(overview.policy.mode) }}</strong>
            <p>{{ livePolicyHint }}</p>
            <dl class='meta-list'>
              <div><dt>随机子集</dt><dd>{{ overview.policy.random_subset_size }}</dd></div>
              <div><dt>稳定子集</dt><dd>{{ overview.policy.stable_subset_size }}</dd></div>
            </dl>
          </article>

          <article class='surface-panel'>
            <div class='control-stack'>
              <label class='field'>
                <span class='field-label'>路由模式</span>
                <select v-model='policyMode' data-testid='policy-mode-select'>
                  <option v-for='option in policyOptions' :key='option.value' :value='option.value'>{{ option.label }}</option>
                </select>
              </label>

              <label class='field'>
                <span class='field-label'>随机子集大小</span>
                <input v-model.number='randomSubsetSize' min='1' type='number'>
              </label>

              <label class='field'>
                <span class='field-label'>稳定子集大小</span>
                <input v-model.number='stableSubsetSize' min='1' type='number'>
              </label>

              <button class='primary-button' type='button' :disabled='isApplyingPolicy' @click='applyPolicy'>
                {{ isApplyingPolicy ? '应用中...' : '应用实时策略' }}
              </button>

              <p class='field-hint'>{{ currentPolicyHint }}</p>
            </div>
          </article>

          <article class='surface-panel policy-help'>
            <p class='subheading'>模式说明</p>
            <ul class='policy-guide-list'>
              <li v-for='option in policyOptions' :key='`guide-${option.value}`'>
                <strong>{{ option.label }}</strong>
                <span>{{ option.hint }}</span>
              </li>
            </ul>
          </article>
        </div>
      </section>

      <section v-else-if='activeModule === "config"' class='module-panel'>
        <div class='panel-heading'>
          <div>
            <p class='panel-kicker'>配置</p>
            <h2>生效配置与草稿</h2>
          </div>
          <span class='panel-meta'>{{ draftDirty ? '草稿已修改' : '草稿已同步' }}</span>
        </div>

        <div class='config-grid'>
          <article class='config-pane'>
            <div class='subheading-row'>
              <p class='subheading'>配置参考</p>
              <div class='segmented-control' aria-label='配置参考视图'>
                <button type='button' :class='{ "segmented-control__item--active": activeConfigInfoPanel === "current" }' @click='activeConfigInfoPanel = "current"'>当前配置</button>
                <button type='button' :class='{ "segmented-control__item--active": activeConfigInfoPanel === "diff" }' @click='activeConfigInfoPanel = "diff"'>配置差异</button>
                <button type='button' :class='{ "segmented-control__item--active": activeConfigInfoPanel === "reference" }' @click='activeConfigInfoPanel = "reference"'>字段说明</button>
              </div>
            </div>

            <template v-if='activeConfigInfoPanel === "current"'>
              <div class='summary-grid'>
                <div class='summary-card'>
                  <span class='field-label'>基础配置路径</span>
                  <strong>{{ basePath || '—' }}</strong>
                </div>
                <div class='summary-card'>
                  <span class='field-label'>覆盖配置路径</span>
                  <strong>{{ overridePath || '—' }}</strong>
                </div>
              </div>

              <div class='token-group'>
                <p class='field-label'>可热更新字段</p>
                <div class='token-list'>
                  <span v-for='field in liveFields' :key='`live-${field}`' class='inline-token inline-token--success'>{{ field }}</span>
                  <span v-if='liveFields.length === 0' class='inline-token inline-token--muted'>暂无上报</span>
                </div>
              </div>

              <div class='token-group'>
                <p class='field-label'>需重启字段</p>
                <div class='token-list'>
                  <span v-for='field in restartFields' :key='`restart-${field}`' class='inline-token inline-token--warning'>{{ field }}</span>
                  <span v-if='restartFields.length === 0' class='inline-token inline-token--muted'>暂无上报</span>
                </div>
              </div>

              <pre class='config-preview'>{{ liveConfigText }}</pre>
            </template>

            <template v-else-if='activeConfigInfoPanel === "diff"'>
              <div class='diff-summary'>
                <span class='status-chip' :class='configDiff.valid && configDiff.changedCount === 0 ? "status-chip--success" : "status-chip--warning"'>逐行参考</span>
                <p>{{ configDiff.message }}</p>
              </div>

              <div v-if='!configDiff.valid' class='empty-state'>请先修正草稿 JSON，差异面板不会改写草稿内容。</div>
              <div v-else class='config-diff-list' aria-label='当前配置与草稿逐行差异'>
                <div class='config-diff-head' aria-hidden='true'>
                  <span>当前</span>
                  <span>草稿</span>
                  <span>状态</span>
                </div>
                <div v-for='row in configDiff.rows' :key='row.key' class='config-diff-row' :data-kind='row.kind'>
                  <div class='diff-line'>
                    <span class='diff-line-number'>{{ row.currentLineNumber ?? '—' }}</span>
                    <code>{{ row.currentText || ' ' }}</code>
                  </div>
                  <div class='diff-line'>
                    <span class='diff-line-number'>{{ row.draftLineNumber ?? '—' }}</span>
                    <code>{{ row.draftText || ' ' }}</code>
                  </div>
                  <span class='diff-kind'>{{ diffKindLabel(row.kind) }}</span>
                </div>
              </div>
            </template>

            <div v-else class='config-reference-list'>
              <article v-for='section in configReferenceSections' :key='section.title' class='config-reference-card'>
                <strong>{{ section.title }}</strong>
                <p>{{ section.description }}</p>
                <div class='token-list'>
                  <span v-for='field in section.fields' :key='`${section.title}-${field}`' class='inline-token inline-token--muted'>{{ field }}</span>
                </div>
              </article>
            </div>
          </article>

          <article class='config-pane config-pane--draft'>
            <div class='subheading-row'>
              <p class='subheading'>覆盖配置草稿</p>
              <span class='status-chip' :class='draftDirty ? "status-chip--warning" : "status-chip--success"'>
                {{ draftDirty ? '已编辑' : '已同步' }}
              </span>
            </div>

            <p class='field-hint'>编辑有效配置 JSON 后应用；可热更新字段立即生效，其他字段会返回需重启回执。</p>

            <textarea
              :value='draftText'
              class='config-editor'
              spellcheck='false'
              @input='draftText = ($event.target as HTMLTextAreaElement).value'
            />

            <p v-if='draftError' class='error-text'>{{ draftError }}</p>

            <div class='draft-actions'>
              <button class='ghost-button' type='button' :disabled='liveConfig === null || !draftDirty' @click='liveConfig && syncDraft(liveConfig)'>
                重置草稿
              </button>
              <button class='primary-button' type='button' data-testid='config-apply' :disabled='isApplyingDraft' @click='applyDraftConfig'>
                {{ isApplyingDraft ? '应用中...' : '应用草稿配置' }}
              </button>
            </div>
          </article>
        </div>
      </section>

      <section v-else class='module-panel'>
        <div class='panel-heading'>
          <div>
            <p class='panel-kicker'>事件</p>
            <h2>回执、请求与失败</h2>
          </div>
          <div class='toolbar'>
            <label class='inline-field'>
              <span>请求条数</span>
              <select v-model.number='requestVisibleCount'>
                <option v-for='option in requestVisibleOptions' :key='option' :value='option'>{{ option }}</option>
              </select>
            </label>
            <span class='panel-meta'>{{ activeTimeRangeLabel }}</span>
          </div>
        </div>

        <div class='events-grid'>
          <article class='surface-panel'>
            <div class='subheading-row'>
              <p class='subheading'>最新回执</p>
              <span v-if='feedback' class='feedback-pill' :data-kind='feedback.kind'>{{ feedbackKindLabel(feedback.kind) }}</span>
            </div>
            <p class='feedback-message'>{{ feedback?.message ?? '暂无回执。' }}</p>
            <ul v-if='feedback' class='feedback-list'>
              <li>{{ feedback.timestamp }} · {{ feedback.title }}</li>
              <li v-for='detail in feedback.details' :key='detail'>{{ detail }}</li>
            </ul>
          </article>

          <article class='surface-panel'>
            <div class='subheading-row'>
              <p class='subheading'>失败 / 错误</p>
              <span class='panel-meta'>{{ recentFailures.length }} 条</span>
            </div>
            <div v-if='recentFailures.length === 0' class='empty-state'>当前时间范围没有失败请求。</div>
            <ul v-else class='request-list request-list--compact'>
              <li v-for='request in recentFailures' :key='`failure-${request.timestamp || request.time}-${request.target}-${request.proxy_ip}`' class='request-item request-item--error'>
                <div>
                  <strong>{{ request.target }}</strong>
                  <p class='table-meta'>{{ request.proxy_ip }} · {{ requestTimeLabel(request) }}</p>
                </div>
                <span class='mono'>{{ request.latency_ms }}ms</span>
              </li>
            </ul>
          </article>

          <article class='surface-panel surface-panel--wide events-requests'>
            <div class='subheading-row'>
              <p class='subheading'>最近请求</p>
              <span class='panel-meta'>显示 {{ recentRequests.length }} / {{ filteredRequestRecords.length }}</span>
            </div>
            <div v-if='recentRequests.length === 0' class='empty-state'>当前时间范围暂无请求记录。</div>
            <ul v-else class='request-list request-list--events'>
              <li v-for='request in recentRequests' :key='`${request.timestamp || request.time}-${request.target}-${request.proxy_ip}`' class='request-item'>
                <div>
                  <strong>{{ request.target }}</strong>
                  <p class='table-meta'>{{ request.proxy_ip }} · {{ requestTimeLabel(request) }}</p>
                </div>
                <div class='request-metrics'>
                  <span class='mono'>{{ request.latency_ms }}ms</span>
                  <span class='status-chip' :class='request.success ? "status-chip--success" : "status-chip--error"'>
                    {{ request.success ? '成功' : '失败' }}
                  </span>
                </div>
              </li>
            </ul>
          </article>
        </div>
      </section>
    </main>
  </div>
</template>

<style>
:root {
  color-scheme: light;
  --surface-base: #f5f7fa;
  --surface-panel: #ffffff;
  --surface-subtle: #f9fafb;
  --surface-strong: #e8eef8;
  --text-primary: #1a1a2e;
  --text-secondary: #374151;
  --text-muted: #6b7280;
  --text-faint: #9ca3af;
  --border-default: #d1d5db;
  --border-subtle: #e8e8ef;
  --accent-primary: #2563eb;
  --accent-hover: #1d4ed8;
  --status-success: #16a34a;
  --status-success-bg: #f0fdf4;
  --status-warning: #d97706;
  --status-warning-bg: #fff7ed;
  --status-error: #dc2626;
  --status-error-bg: #fef2f2;
  --status-info: #2563eb;
  --status-info-bg: #eff6ff;
  --space-1: 4px;
  --space-2: 8px;
  --space-3: 12px;
  --space-4: 16px;
  --space-5: 20px;
  --space-6: 24px;
  --space-8: 32px;
  --space-10: 40px;
  --space-12: 48px;
  --space-16: 64px;
  --shadow-resting: 0 1px 3px rgba(15, 23, 42, 0.06), 0 1px 2px rgba(15, 23, 42, 0.04);
  --shadow-elevated: 0 6px 18px rgba(37, 99, 235, 0.08);
  --radius-lg: 8px;
  --radius-md: 8px;
  --radius-sm: 6px;
  --font-sans: "SF Pro Display", "Segoe UI", "Noto Sans SC", "PingFang SC", sans-serif;
  --font-mono: "JetBrains Mono", "SFMono-Regular", "SF Mono", Consolas, monospace;
}

* {
  box-sizing: border-box;
}

html,
body,
#app {
  min-height: 100%;
}

body {
  margin: 0;
  background: var(--surface-base);
  color: var(--text-primary);
  font-family: var(--font-sans);
}

button,
input,
select,
textarea {
  font: inherit;
}

button {
  min-height: var(--space-10);
  padding: 0 var(--space-4);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-panel);
  color: var(--text-secondary);
  cursor: pointer;
  font-weight: 600;
  transition: background-color 180ms ease-in-out, border-color 180ms ease-in-out, box-shadow 180ms ease-in-out, transform 120ms ease-out;
}

button:hover:not(:disabled) {
  border-color: var(--accent-primary);
  background: var(--status-info-bg);
  color: var(--accent-primary);
}

button:active:not(:disabled) {
  transform: translateY(1px);
}

button:focus-visible,
select:focus-visible,
input:focus-visible,
textarea:focus-visible {
  outline: none;
  border-color: var(--accent-primary);
  box-shadow: 0 0 0 3px rgba(37, 99, 235, 0.16);
}

button:disabled {
  cursor: wait;
  opacity: 0.62;
}

select,
input,
textarea {
  width: 100%;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-panel);
  color: var(--text-primary);
}

select,
input {
  min-height: var(--space-10);
  padding: 0 var(--space-3);
}

textarea {
  min-height: 520px;
  padding: var(--space-4);
  resize: vertical;
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.55;
}

.console-shell {
  width: min(1440px, 100%);
  min-height: 100vh;
  margin: 0 auto;
  padding: var(--space-8);
  display: grid;
  grid-template-columns: 304px minmax(0, 1fr);
  gap: var(--space-6);
}

.console-sidebar {
  position: sticky;
  top: var(--space-8);
  align-self: start;
  display: grid;
  gap: var(--space-6);
  max-height: calc(100vh - var(--space-16));
  padding: var(--space-6);
  overflow: auto;
  background: var(--surface-panel);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-resting);
}

.brand-block h1,
.panel-heading h2,
.group-header h3,
.subheading,
.receipt-title,
.policy-mode,
.summary-card strong {
  margin: 0;
}

.brand-kicker,
.panel-kicker,
.field-label {
  margin: 0 0 var(--space-2);
  color: var(--text-faint);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}

.brand-block h1 {
  font-size: 2rem;
  line-height: 1.15;
}

.brand-block p:not(.brand-kicker),
.field-hint,
.panel-meta,
.table-meta,
.table-note,
.receipt-message {
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.45;
}

.module-nav,
.sidebar-controls,
.control-stack,
.token-group,
.config-pane {
  display: grid;
  gap: var(--space-4);
}

.module-tab {
  min-height: 64px;
  justify-content: space-between;
  padding: var(--space-3) var(--space-4);
  display: flex;
  align-items: center;
  gap: var(--space-3);
  text-align: left;
}

.module-tab span {
  display: grid;
  gap: var(--space-1);
}

.module-tab strong {
  color: var(--text-primary);
  font-size: 15px;
}

.module-tab small,
.module-tab em {
  color: var(--text-muted);
  font-size: 12px;
  font-style: normal;
}

.module-tab--active {
  border-color: var(--accent-primary);
  background: var(--status-info-bg);
  box-shadow: inset 3px 0 0 var(--accent-primary);
}

.sidebar-badges,
.token-list,
.draft-actions,
.toolbar,
.request-metrics {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.field {
  display: grid;
  gap: var(--space-2);
}

.inline-field {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  color: var(--text-muted);
  font-size: 13px;
  white-space: nowrap;
}

.inline-field select {
  width: auto;
  min-width: 88px;
}

.pool-search-field input {
  width: min(280px, 32vw);
}

.console-workspace {
  display: grid;
  align-content: start;
  gap: var(--space-5);
  min-width: 0;
}

.top-toolbar,
.module-panel,
.surface-panel,
.metric-card,
.summary-card,
.pool-group,
.config-pane {
  background: var(--surface-panel);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-resting);
}

.top-toolbar {
  position: sticky;
  top: var(--space-4);
  z-index: 2;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
}

.toolbar-feedback {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
}

.workspace-controls {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-2);
  flex-wrap: wrap;
}

.receipt-title {
  color: var(--text-primary);
  font-size: 14px;
  font-weight: 700;
}

.receipt-message {
  margin: var(--space-1) 0 0;
  max-width: 520px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.module-panel {
  padding: var(--space-6);
  min-width: 0;
}

.panel-heading,
.subheading-row,
.group-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}

.panel-heading {
  margin-bottom: var(--space-5);
}

.panel-heading h2 {
  font-size: 1.5rem;
  line-height: 1.2;
}

.metric-grid,
.overview-grid,
.summary-grid,
.policy-layout,
.config-grid,
.pool-columns {
  display: grid;
  gap: var(--space-4);
}

.metric-grid {
  grid-template-columns: repeat(6, minmax(0, 1fr));
}

.overview-grid,
.config-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
  margin-top: var(--space-5);
}

.policy-layout {
  grid-template-columns: 0.9fr 1fr 1.15fr;
  margin-top: var(--space-5);
}

.summary-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.pool-columns {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.metric-card,
.surface-panel,
.summary-card,
.pool-group,
.config-pane {
  padding: var(--space-5);
}

.surface-panel--wide {
  margin-top: var(--space-5);
}

.surface-panel--strong,
.metric-card--muted {
  background: var(--surface-strong);
}

.metric-card {
  display: grid;
  gap: var(--space-2);
}

.metric-card span {
  color: var(--text-muted);
  font-size: 13px;
}

.metric-card em {
  color: var(--text-muted);
  font-size: 12px;
  font-style: normal;
}

.metric-card strong,
.summary-card strong,
.policy-mode {
  color: var(--text-primary);
  font-size: 1.75rem;
  line-height: 1.2;
}

.metric-card--live {
  background: var(--status-success-bg);
  border-color: rgba(22, 163, 74, 0.22);
}

.metric-card--warning {
  background: var(--status-warning-bg);
  border-color: rgba(217, 119, 6, 0.24);
}

.metric-card--danger {
  background: var(--status-error-bg);
  border-color: rgba(220, 38, 38, 0.18);
}

.meta-list {
  margin: var(--space-4) 0 0;
  display: grid;
  gap: var(--space-3);
}

.meta-list div {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding-bottom: var(--space-3);
  border-bottom: 1px solid var(--border-subtle);
}

.meta-list dt {
  color: var(--text-muted);
}

.meta-list dd {
  margin: 0;
  color: var(--text-primary);
  font-family: var(--font-mono);
}

.request-list {
  list-style: none;
  margin: var(--space-4) 0 0;
  padding: 0;
  display: grid;
  gap: var(--space-3);
  max-height: 340px;
  overflow: auto;
}

.request-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
}

.request-item strong,
.mono {
  font-family: var(--font-mono);
}

.status-chip,
.inline-token,
.feedback-pill {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  min-height: 28px;
  padding: 0 var(--space-3);
  border: 1px solid transparent;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;
}

.status-chip--info,
.feedback-pill[data-kind='info'] {
  color: var(--status-info);
  background: var(--status-info-bg);
  border-color: rgba(37, 99, 235, 0.18);
}

.status-chip--success,
.feedback-pill[data-kind='success'],
.inline-token--success {
  color: var(--status-success);
  background: var(--status-success-bg);
  border-color: rgba(22, 163, 74, 0.18);
}

.status-chip--warning,
.feedback-pill[data-kind='warning'],
.inline-token--warning {
  color: var(--status-warning);
  background: var(--status-warning-bg);
  border-color: rgba(217, 119, 6, 0.22);
}

.status-chip--error,
.feedback-pill[data-kind='error'] {
  color: var(--status-error);
  background: var(--status-error-bg);
  border-color: rgba(220, 38, 38, 0.18);
}

.status-chip--muted,
.inline-token--muted {
  color: var(--text-muted);
  background: var(--surface-subtle);
  border-color: var(--border-default);
}

.lease-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  min-height: 28px;
  padding: 0 var(--space-3);
  border: 1px solid var(--border-default);
  border-radius: 999px;
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;
}

.lease-dot {
  width: var(--space-2);
  height: var(--space-2);
  border-radius: 999px;
  background: currentColor;
}

.lease-chip--active {
  color: var(--status-success);
  background: var(--status-success-bg);
  border-color: var(--status-success);
}

.lease-chip--idle {
  color: var(--text-muted);
  background: var(--surface-subtle);
}

.table-shell {
  overflow: auto;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}

.table-shell--module {
  max-height: 640px;
}

.operator-table {
  width: 100%;
  min-width: 760px;
  border-collapse: collapse;
  font-size: 13px;
}

.operator-table th,
.operator-table td {
  padding: var(--space-3) var(--space-4);
  text-align: left;
  vertical-align: top;
}

.operator-table thead th {
  position: sticky;
  top: 0;
  z-index: 1;
  color: var(--text-muted);
  background: var(--surface-subtle);
  border-bottom: 1px solid var(--border-default);
  font-size: 12px;
  font-weight: 700;
}

.operator-table tbody td {
  border-bottom: 1px solid var(--border-subtle);
}

.operator-table tbody tr:hover {
  background: var(--surface-subtle);
}

.primary-button {
  background: var(--accent-primary);
  color: var(--surface-panel);
  border-color: var(--accent-primary);
}

.primary-button:hover:not(:disabled) {
  background: var(--accent-hover);
  color: var(--surface-panel);
}

.ghost-button {
  background: transparent;
}

.compact-button {
  min-height: var(--space-8);
  padding: 0 var(--space-3);
}

.sidebar-refresh {
  width: 100%;
}

.danger-button {
  color: var(--status-error);
  border-color: rgba(220, 38, 38, 0.22);
  background: var(--status-error-bg);
}

.danger-button:hover:not(:disabled) {
  border-color: var(--status-error);
  background: var(--status-error-bg);
  color: var(--status-error);
}

.group-header span,
.empty-state,
.empty-cell,
.error-text,
.feedback-list {
  font-size: 13px;
}

.group-header span,
.empty-state,
.empty-cell {
  color: var(--text-muted);
}

.empty-state,
.empty-cell {
  padding: var(--space-5);
  background: var(--surface-subtle);
}

.empty-state {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
}

.empty-cell {
  text-align: center;
}

.table-note {
  margin: var(--space-3) 0 0;
}

.summary-card {
  overflow: hidden;
}

.summary-card strong {
  display: block;
  overflow-wrap: anywhere;
  font-size: 13px;
  font-family: var(--font-mono);
  line-height: 1.45;
}

.config-preview,
.config-editor {
  min-height: 520px;
  max-height: 64vh;
  overflow: auto;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.55;
}

.diff-summary {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}

.diff-summary p {
  margin: 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.45;
}

.config-diff-list {
  display: grid;
  max-height: 620px;
  overflow: auto;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
}

.config-diff-head,
.config-diff-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 64px;
  gap: var(--space-2);
  align-items: stretch;
}

.config-diff-head {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--border-default);
  background: var(--surface-panel);
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 700;
}

.config-diff-row {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--border-subtle);
}

.config-diff-row[data-kind='added'] {
  background: var(--status-success-bg);
}

.config-diff-row[data-kind='removed'] {
  background: var(--status-error-bg);
}

.config-diff-row[data-kind='changed'] {
  background: var(--status-warning-bg);
}

.diff-line {
  min-width: 0;
  display: grid;
  grid-template-columns: 40px minmax(0, 1fr);
  gap: var(--space-2);
  align-items: start;
}

.diff-line-number {
  color: var(--text-faint);
  font-family: var(--font-mono);
  font-size: 12px;
  text-align: right;
}

.diff-line code {
  min-width: 0;
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.diff-kind {
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 700;
}

.config-preview {
  margin: 0;
  padding: var(--space-4);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.feedback-message {
  margin: 0;
  color: var(--text-primary);
  font-size: 16px;
  font-weight: 600;
  line-height: 1.5;
}

.feedback-list {
  margin: var(--space-4) 0 0;
  padding-left: var(--space-4);
  color: var(--text-secondary);
  display: grid;
  gap: var(--space-2);
}

.proxy-card-list,
.config-reference-list {
  display: grid;
  gap: var(--space-3);
  max-height: 620px;
  overflow: auto;
  padding-right: var(--space-1);
}

.proxy-card-list,
.request-list,
.config-preview,
.config-editor,
.config-diff-list,
.table-shell,
.console-sidebar,
.config-reference-list {
  scrollbar-width: thin;
  scrollbar-color: var(--border-default) transparent;
}

.proxy-card-list::-webkit-scrollbar,
.request-list::-webkit-scrollbar,
.config-preview::-webkit-scrollbar,
.config-editor::-webkit-scrollbar,
.config-diff-list::-webkit-scrollbar,
.table-shell::-webkit-scrollbar,
.console-sidebar::-webkit-scrollbar,
.config-reference-list::-webkit-scrollbar {
  width: var(--space-2);
  height: var(--space-2);
}

.proxy-card-list::-webkit-scrollbar-thumb,
.request-list::-webkit-scrollbar-thumb,
.config-preview::-webkit-scrollbar-thumb,
.config-editor::-webkit-scrollbar-thumb,
.config-diff-list::-webkit-scrollbar-thumb,
.table-shell::-webkit-scrollbar-thumb,
.console-sidebar::-webkit-scrollbar-thumb,
.config-reference-list::-webkit-scrollbar-thumb {
  background: var(--border-default);
  border-radius: 999px;
}

.proxy-card {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
}

.proxy-card-main,
.proxy-card-meta,
.proxy-card-actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.proxy-card-main {
  align-items: flex-start;
}

.proxy-card-meta,
.proxy-card-actions {
  flex-wrap: wrap;
  color: var(--text-muted);
  font-size: 12px;
}

.state-dot {
  flex: 0 0 var(--space-2);
  width: var(--space-2);
  height: var(--space-2);
  margin-top: var(--space-2);
  border-radius: 999px;
  background: var(--text-faint);
}

.state-dot[data-state='ready'] {
  background: var(--status-success);
}

.state-dot[data-state='buffer'] {
  background: var(--status-warning);
}

.state-dot[data-state='blacklist'] {
  background: var(--text-muted);
}

.policy-help {
  align-self: start;
}

.policy-guide-list,
.config-reference-list {
  list-style: none;
  margin: var(--space-4) 0 0;
  padding: 0;
}

.policy-guide-list {
  display: grid;
  gap: var(--space-3);
}

.policy-guide-list li,
.config-reference-card {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
}

.policy-guide-list strong,
.config-reference-card strong {
  color: var(--text-primary);
}

.policy-guide-list span,
.config-reference-card p {
  margin: 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.45;
}

.segmented-control {
  display: inline-flex;
  gap: var(--space-1);
  padding: var(--space-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
}

.segmented-control button {
  min-height: var(--space-8);
  border-color: transparent;
  background: transparent;
}

.segmented-control__item--active {
  background: var(--surface-panel) !important;
  border-color: var(--border-default) !important;
  color: var(--accent-primary);
  box-shadow: var(--shadow-resting);
}

.events-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-4);
}

.events-requests {
  grid-column: 1 / -1;
  margin-top: 0;
}

.request-list--overview {
  max-height: 320px;
}

.request-list--compact {
  max-height: 260px;
}

.request-list--events {
  max-height: 420px;
}

.request-item--error {
  border-color: rgba(220, 38, 38, 0.18);
  background: var(--status-error-bg);
}

.error-text {
  margin: 0;
  color: var(--status-error);
}

@media (max-width: 1279px) {
  .console-shell {
    grid-template-columns: 1fr;
  }

  .console-sidebar {
    position: static;
    max-height: none;
  }

  .module-nav {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .pool-columns,
  .config-grid,
  .policy-layout {
    grid-template-columns: 1fr;
  }

  .metric-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 900px) {
  .metric-grid,
  .overview-grid,
  .policy-layout,
  .summary-grid,
  .events-grid {
    grid-template-columns: 1fr;
  }

  .top-toolbar,
  .panel-heading,
  .subheading-row,
  .group-header,
  .request-item {
    flex-direction: column;
    align-items: flex-start;
  }

  .workspace-controls {
    justify-content: flex-start;
    width: 100%;
  }

  .receipt-message {
    max-width: 100%;
    white-space: normal;
  }
}

@media (max-width: 767px) {
  .console-shell {
    padding: var(--space-4);
  }

  .module-panel,
  .console-sidebar,
  .surface-panel,
  .pool-group,
  .config-pane {
    padding: var(--space-4);
  }

  .module-nav {
    grid-template-columns: 1fr;
  }

  .toolbar,
  .inline-field,
  .workspace-controls,
  .segmented-control {
    align-items: stretch;
    width: 100%;
  }

  .inline-field select,
  .pool-search-field input,
  .segmented-control button {
    width: 100%;
  }

  .config-diff-head,
  .config-diff-row {
    grid-template-columns: 1fr;
  }

  .metric-grid {
    grid-template-columns: 1fr;
  }
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation: none !important;
    transition: none !important;
    scroll-behavior: auto !important;
  }
}
</style>
