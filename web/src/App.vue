<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  Activity, AlertCircle, ArrowUpDown, Bell, Check, ChevronLeft, ChevronRight,
  ChartNoAxesCombined, CircleDollarSign, CircleStop, Clipboard, Copy, Download, Gauge, KeyRound, LayoutDashboard, ListFilter, Play,
  LoaderCircle, LogOut, Mail, Menu, Monitor, Moon, MoreHorizontal, Network,
  PanelLeftClose, PanelLeftOpen, Pencil, Plus, RefreshCw, Search, Server,
  ShieldCheck, Sun, Trash2, Upload, Webhook, X,
} from 'lucide-vue-next'
import { ApiError, api, listOf } from './api'
const UsageChart = defineAsyncComponent(() => import('./UsageChart.vue'))
import {
  COMMON_UPSTREAM_MODELS, DEFAULT_UPSTREAM_PROTOCOLS, UPSTREAM_PROTOCOLS, bulkSetModels, connectionTestText, modelBatchSelection,
  modelsForPayload, parseModelList, setModelSelected, userAgentMode, userAgentValue, usesNewAPICredentials,
} from './upstream-form'
import type { UpstreamUserAgentMode } from './upstream-form'

type View = 'dashboard' | 'groups' | 'upstreams' | 'keys' | 'logs' | 'usage' | 'channels'
type Theme = 'auto' | 'light' | 'dark'
type CCSwitchApp = 'claude' | 'codex' | 'gemini'
type Json = Record<string, any>

interface Upstream {
  id: number
  name: string
  kind: 'newapi' | 'sub2api'
  base_url: string
  user_agent?: string
  enabled: boolean
  balance_protection_enabled?: boolean
  balance_suspended?: boolean
  zero_balance_checks?: number
  priority: number
  protocols: string[]
  models: string[]
  models_locked?: boolean
  pricing_profile_id?: number
  model_aliases: Record<string, string>
  connect_timeout_ms?: number
  first_byte_timeout_ms?: number
  idle_timeout_ms?: number
  failure_threshold: number
  cooldown_seconds?: number
  health_status: string
  consecutive_failures?: number
  circuit_open_until?: string
  last_check_at?: string
  last_error?: string
  today_requests?: number
  today_tokens?: number
  today_cost_usd?: number
  today_cost_coverage?: number
  lifetime_requests?: number
  lifetime_cost_usd?: number
  lifetime_cost_coverage?: number
  balance?: { status?: string; available?: number; used?: number; currency?: string; unlimited?: boolean; updated_at?: string; last_success_at?: string }
}

interface UpstreamGroup {
  key: string
  base_url: string
  items: Upstream[]
  priority: number
  total: number
  healthy: number
  enabled: number
  available: number
  balance_suspended: number
  circuit_open: number
  protocols: string[]
  models: string[]
  today_requests: number
  today_tokens: number
  last_check_at?: string
}

interface ClientKey {
  id: number
  name: string
  prefix?: string
  key_prefix?: string
  enabled: boolean
  protocols: string[]
  models: string[]
  last_used_at?: string
  created_at: string
  group_id: number
  group_name?: string
}

interface Group {
  id: number
  name: string
  enabled: boolean
  upstream_ids: number[]
  key_count: number
  created_at: string
  updated_at: string
}

interface RequestLog {
  id?: number
  request_id: string
  upstream_id?: number
  upstream_name?: string
  group_name?: string
  api_key_name?: string
  protocol: string
  model: string
  status_code: number
  duration_ms: number
  ttfb_ms?: number | null
  ttft_ms?: number | null
  attempts: Array<{ upstream_id?: number; upstream_name?: string; status_code?: number; error?: string; duration_ms?: number; ttfb_ms?: number | null; ttft_ms?: number | null }>
  usage?: {
    input_tokens?: number
    output_tokens?: number
    cached_input_tokens?: number
    cache_creation_input_tokens?: number
    uncached_input_tokens?: number
  }
  error_code?: string
  cost_usd?: number
  created_at: string
}

interface Channel {
  id: number
  name: string
  kind: 'email' | 'webhook'
  enabled: boolean
  created_at?: string
}

interface AlertRule {
  id: number
  event: string
  upstream_id?: number
  threshold: number
  window_seconds: number
  cooldown_seconds: number
  max_notifications: number
  enabled: boolean
}

interface ModelProtocolResult {
  protocol: string
  status: 'success' | 'degraded' | 'failed'
  status_code?: number
  latency_ms: number
  ping_latency_ms?: number
  error?: string
}

interface ModelTestReport {
  model: string
  status: 'available' | 'partial' | 'unavailable'
  results: ModelProtocolResult[]
  error?: string
}

const navItems = [
  { id: 'dashboard' as View, label: '总览', icon: LayoutDashboard },
  { id: 'groups' as View, label: '分组', icon: Network },
  { id: 'upstreams' as View, label: '上游', icon: Server },
  { id: 'keys' as View, label: '客户端密钥', icon: KeyRound },
  { id: 'logs' as View, label: '请求日志', icon: ListFilter },
  { id: 'usage' as View, label: '用量', icon: Gauge },
  { id: 'channels' as View, label: '通知', icon: Bell },
]

const themeOptions = [
  { id: 'auto' as Theme, label: '跟随系统', icon: Monitor },
  { id: 'light' as Theme, label: '浅色', icon: Sun },
  { id: 'dark' as Theme, label: '深色', icon: Moon },
]

function readStored(key: string) {
  try { return localStorage.getItem(key) } catch { return null }
}

function writeStored(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* Persistence is optional. */ }
}

function readStoredJSON<T>(key: string, fallback: T): T {
  try {
    const value = localStorage.getItem(key)
    return value ? JSON.parse(value) as T : fallback
  } catch { return fallback }
}

const storedTheme = readStored('dapi-theme') as Theme | null
const theme = ref<Theme>(['auto', 'light', 'dark'].includes(storedTheme || '') ? storedTheme! : 'auto')
const resolvedTheme = ref<'light' | 'dark'>('light')
const colorScheme = matchMedia('(prefers-color-scheme: dark)')
const sidebarCollapsed = ref(readStored('dapi-sidebar-collapsed') === 'true')
const openRowMenu = ref('')
const rowMenuPosition = reactive({ top: 0, left: 0 })
let rowMenuTrigger: HTMLElement | null = null
const expandedMobileRow = ref('')
const sortState = reactive<Record<string, { key: string; direction: 1 | -1 }>>({})

const auth = ref<'checking' | 'guest' | 'ready'>('checking')
const admin = ref<Json>({})
const loginForm = reactive({ username: '', password: '' })
const loginError = ref('')
const loginBusy = ref(false)
const view = ref<View>('dashboard')
const loading = ref(false)
let pageLoadController: AbortController | null = null
let pageLoadSequence = 0
const menuOpen = ref(false)
const toast = reactive({ show: false, message: '', error: false })
let toastTimer = 0
const lastUpdatedAt = ref<Date | null>(null)
const autoRefresh = ref(readStored('dapi-auto-refresh') === 'true')
let autoRefreshTimer: number | null = null
const refreshError = ref('')
let dashboardRangeController: AbortController | null = null
let dashboardRangeSequence = 0

const dashboard = ref<Json>({})
const dashboardCostRows = ref<Json[]>([])
const dashboardRangeUsage = ref<Json[]>([])
const pricing = ref<Json>({})
const pricingModal = ref(false)
const editingPricing = ref<number | null>(null)
const pricingForm = reactive({ name: '', provider: '', source_url: '', source_version: 'custom', prices: '' })
const dashboardRange = ref<'24h' | '7d' | '30d'>(['24h', '7d', '30d'].includes(readStored('dapi-dashboard-range') || '') ? readStored('dapi-dashboard-range') as '24h' | '7d' | '30d' : '24h')
const dashboardMetric = ref<'requests' | 'tokens' | 'cache' | 'cost'>(['requests', 'tokens', 'cache', 'cost'].includes(readStored('dapi-dashboard-metric') || '') ? readStored('dapi-dashboard-metric') as 'requests' | 'tokens' | 'cache' | 'cost' : 'requests')
const dashboardRouteView = ref<'current' | 'topology'>(readStored('dapi-dashboard-route-view') === 'topology' ? 'topology' : 'current')
const dashboardMetricsExpanded = ref(false)
const dashboardCompare = ref(false)
const dashboardComparison = ref<Json>({})
const dashboardComparisonLoading = ref(false)
const upstreams = ref<Upstream[]>([])
const upstreamGroupDrawer = ref<UpstreamGroup | null>(null)
const upstreamFilter = reactive({ search: '', status: 'all', protocol: 'all' })
Object.assign(upstreamFilter, readStoredJSON('dapi-upstream-filter', {}))
const upstreamSelectedIds = ref<number[]>([])
const groups = ref<Group[]>([])
const keys = ref<ClientKey[]>([])
const keyFilter = reactive({ search: '', status: 'all', group_id: '' })
Object.assign(keyFilter, readStoredJSON('dapi-key-filter', {}))
const logs = ref<RequestLog[]>([])
const usage = ref<Json>({})
const channels = ref<Channel[]>([])
const alertRules = ref<AlertRule[]>([])
const maxAttempts = ref(3)
const logFilter = reactive({ status: '', upstream_id: '', group_id: '', limit: 50, offset: 0 })
Object.assign(logFilter, readStoredJSON('dapi-log-filter', {}))
const expandedLog = ref<string | null>(null)
const usageFilter = reactive({
  days: 30,
  granularity: 'day',
  dimension: 'upstream',
  topN: 5,
  upstream_id: '',
  group_id: '',
  api_key_id: '',
  protocol: '',
  model: '',
})
Object.assign(usageFilter, readStoredJSON('dapi-usage-filter', {}))
const usageMetric = ref<'requests' | 'tokens' | 'latency' | 'cache'>('requests')
const mobileFiltersOpen = ref(false)
const notificationSection = ref<'channels' | 'alerts' | 'settings'>('channels')

const upstreamModal = ref(false)
const editingUpstream = ref<number | null>(null)
const upstreamModelMode = ref<'auto' | 'manual'>('auto')
const fetchingModels = ref(false)
const modelDiscoveryAttempted = ref(false)
const discoveredModels = ref<string[]>([])
const testingModelNames = ref<string[]>([])
const modelTestResults = ref<Record<string, ModelTestReport>>({})
const modelTestProgress = reactive({ running: false, stopped: false, total: 0, completed: 0 })
let modelTestRun = 0
let activeModelTestRun: { id: number; stopped: boolean; controller: AbortController } | null = null
let singleModelTestController: AbortController | null = null
const upstreamFormElement = ref<HTMLFormElement | null>(null)
const upstreamForm = reactive({
  name: '', kind: 'sub2api' as 'newapi' | 'sub2api', base_url: '', api_key: '', access_token: '', user_id: '',
  user_agent_mode: 'default' as UpstreamUserAgentMode, user_agent: '',
  enabled: true, balance_protection_enabled: true, priority: 100, protocols: [...DEFAULT_UPSTREAM_PROTOCOLS] as string[], models: '', aliases: '', connect_timeout_ms: 5000,
  first_byte_timeout_ms: 60000, idle_timeout_ms: 300000, failure_threshold: 3, cooldown_seconds: 60, clear_balance_credentials: false, pricing_profile_id: 0,
})
const keyModal = ref(false)
const editingKey = ref<number | null>(null)
const keyForm = reactive({ name: '', enabled: true, group_id: 0, protocols: ['responses'] as string[], models: '' })
const keySimulationModal = ref(false)
const keySimulationTarget = ref<ClientKey | null>(null)
const keySimulationProtocol = ref('responses')
const keySimulationModel = ref('')
const keySimulationBusy = ref(false)
const keySimulationResult = ref<Json | null>(null)
const groupModal = ref(false)
const editingGroup = ref<number | null>(null)
const groupForm = reactive({ name: '', enabled: true, upstream_ids: [] as number[] })
const revealedKey = ref('')
const ccswitchModal = ref(false)
const ccswitchTarget = ref<ClientKey | null>(null)
const ccswitchApp = ref<CCSwitchApp>('claude')
const ccswitchName = ref('')
const ccswitchModel = ref('')
const ccswitchSecret = ref('')
const ccswitchModels = ref<string[]>([])
const fetchingCCSwitchModels = ref(false)
let ccswitchOpenSequence = 0
let ccswitchModelsController: AbortController | null = null
let keySecretController: AbortController | null = null
const createdKeyForImport = ref<ClientKey | null>(null)
const channelModal = ref(false)
const channelTestID = ref<number | null>(null)
const passwordModal = ref(false)
const confirmDialog = reactive({ show: false, title: '', message: '', confirmLabel: '删除' })
let resolveConfirmation: ((confirmed: boolean) => void) | null = null
const passwordForm = reactive({ current_password: '', new_password: '', confirm_password: '' })
const channelForm = reactive({ name: '', kind: 'webhook' as 'email' | 'webhook', provider: '', enabled: true, target: '', smtp_host: '', smtp_port: 587, username: '', password: '' })
const newRule = reactive({ event: 'low_balance', upstream_id: '', threshold: 5, window_seconds: 300, cooldown_seconds: 1800, max_notifications: 3 })
const saving = ref(false)

const setupSteps = computed(() => [
  { id: 'upstream', label: '添加第一个上游', description: '配置 Base URL、Key 并获取模型', done: upstreams.value.length > 0, view: 'upstreams' as View },
  { id: 'group', label: '创建路由分组', description: '把上游按用途组成路由范围', done: groups.value.some((group) => group.enabled && (group.upstream_ids || []).length > 0), view: 'groups' as View },
  { id: 'key', label: '创建客户端密钥', description: '绑定分组并限制协议或模型', done: keys.value.some((key) => key.enabled), view: 'keys' as View },
  { id: 'request', label: '发起一次测试请求', description: '确认客户端可以通过网关访问', done: Number(dashboard.value.requests_24h ?? dashboard.value.stats?.requests_24h ?? dashboard.value.summary?.requests_24h ?? 0) > 0, view: 'logs' as View },
])
const setupComplete = computed(() => setupSteps.value.every((step) => step.done))
const keyRoutePreview = computed(() => {
  const group = groups.value.find((item) => item.id === Number(keyForm.group_id))
  if (!group) return []
  return (group.upstream_ids || []).map((id) => upstreams.value.find((item) => item.id === id)).filter(Boolean).sort((a, b) => Number(a!.priority) - Number(b!.priority) || a!.id - b!.id) as Upstream[]
})

watch(() => upstreamForm.kind, (kind) => {
  upstreamForm.access_token = ''
  upstreamForm.user_id = ''
  upstreamForm.clear_balance_credentials = kind === 'sub2api' && editingUpstream.value !== null
})

watch(autoRefresh, (enabled) => {
  writeStored('dapi-auto-refresh', String(enabled))
  if (autoRefreshTimer !== null) window.clearInterval(autoRefreshTimer)
  autoRefreshTimer = enabled
    ? window.setInterval(() => {
      if (auth.value === 'ready' && document.visibilityState === 'visible' && !loading.value && !activeOverlayId.value) void loadCurrent()
    }, 30000)
    : null
}, { immediate: true })

watch(dashboardRange, (value) => writeStored('dapi-dashboard-range', value))
watch(dashboardMetric, (value) => writeStored('dapi-dashboard-metric', value))
watch(dashboardRouteView, (value) => writeStored('dapi-dashboard-route-view', value))
watch(usageFilter, (value) => writeStored('dapi-usage-filter', JSON.stringify(value)), { deep: true })
watch(upstreamFilter, (value) => writeStored('dapi-upstream-filter', JSON.stringify(value)), { deep: true })
watch(keyFilter, (value) => writeStored('dapi-key-filter', JSON.stringify(value)), { deep: true })
watch(logFilter, (value) => writeStored('dapi-log-filter', JSON.stringify({ status: value.status, upstream_id: value.upstream_id, group_id: value.group_id })), { deep: true })

const title = computed(() => navItems.find((item) => item.id === view.value)?.label || '')
const gatewayBaseURL = computed(() => window.location.origin)
function canonicalBaseURL(value: string) {
  const raw = String(value || '').trim()
  try {
    const parsed = new URL(raw)
    const port = parsed.port && !((parsed.protocol === 'https:' && parsed.port === '443') || (parsed.protocol === 'http:' && parsed.port === '80')) ? `:${parsed.port}` : ''
    const path = parsed.pathname.replace(/\/+$/, '')
    return `${parsed.protocol.toLowerCase()}//${parsed.hostname.toLowerCase()}${port}${path}`.toLowerCase()
  } catch {
    return raw.replace(/\/+$/, '').toLowerCase()
  }
}
const upstreamGroups = computed<UpstreamGroup[]>(() => {
  const grouped = new Map<string, Upstream[]>()
  for (const item of upstreams.value) {
    const key = canonicalBaseURL(item.base_url)
    grouped.set(key, [...(grouped.get(key) || []), item])
  }
  return [...grouped.entries()].map(([key, items]) => ({
    key,
    base_url: key,
    items,
    priority: Math.min(...items.map((item) => Number(item.priority ?? 0))),
    total: items.length,
    healthy: items.filter((item) => item.enabled && !item.balance_suspended && item.health_status === 'healthy').length,
    enabled: items.filter((item) => item.enabled).length,
    available: items.filter((item) => item.enabled && !item.balance_suspended).length,
    balance_suspended: items.filter((item) => item.enabled && item.balance_suspended).length,
    circuit_open: items.filter((item) => item.enabled && (item.health_status === 'open' || Boolean(item.circuit_open_until && new Date(item.circuit_open_until) > new Date()))).length,
    protocols: [...new Set(items.flatMap((item) => item.protocols || []))],
    models: [...new Set(items.flatMap((item) => item.models || []))],
    today_requests: items.reduce((sum, item) => sum + Number(item.today_requests || 0), 0),
    today_tokens: items.reduce((sum, item) => sum + Number(item.today_tokens || 0), 0),
    last_check_at: items.map((item) => item.last_check_at).filter(Boolean).sort().at(-1),
  })).sort((a, b) => a.priority - b.priority || a.base_url.localeCompare(b.base_url))
})
const shownDashUpstreams = computed<UpstreamGroup[]>(() => upstreamGroups.value.slice(0, 8))
const filteredUpstreamGroups = computed(() => upstreamGroups.value.filter((item) => {
  const search = upstreamFilter.search.trim().toLocaleLowerCase('zh-CN')
  if (search && !`${item.base_url} ${item.items.map((entry) => entry.name).join(' ')}`.toLocaleLowerCase('zh-CN').includes(search)) return false
  if (upstreamFilter.status === 'healthy' && groupStatusTone(item) !== 'good') return false
  if (upstreamFilter.status === 'warning' && groupStatusTone(item) !== 'warn') return false
  if (upstreamFilter.status === 'error' && groupStatusTone(item) !== 'bad') return false
  if (upstreamFilter.protocol !== 'all' && !item.protocols.includes(upstreamFilter.protocol)) return false
  return true
}))
const allVisibleUpstreamsSelected = computed(() => filteredUpstreamGroups.value.length > 0 && filteredUpstreamGroups.value.every((item) => item.items.every((entry) => upstreamSelectedIds.value.includes(entry.id))))
const someVisibleUpstreamsSelected = computed(() => {
  const visibleIDs = filteredUpstreamGroups.value.flatMap((item) => item.items.map((entry) => entry.id))
  const selectedCount = visibleIDs.filter((id) => upstreamSelectedIds.value.includes(id)).length
  return selectedCount > 0 && selectedCount < visibleIDs.length
})
const filteredKeys = computed(() => keys.value.filter((item) => {
  const search = keyFilter.search.trim().toLocaleLowerCase('zh-CN')
  if (search && !`${item.name} ${item.prefix || item.key_prefix || ''}`.toLocaleLowerCase('zh-CN').includes(search)) return false
  if (keyFilter.status === 'enabled' && !item.enabled) return false
  if (keyFilter.status === 'disabled' && item.enabled) return false
  if (keyFilter.group_id && Number(item.group_id) !== Number(keyFilter.group_id)) return false
  return true
}))
const dashboardTopologyGroups = computed<Group[]>(() => {
  const enabledGroups = groups.value.filter((group) => group.enabled)
  return (enabledGroups.length ? enabledGroups : groups.value).slice(0, 4)
})
const dashboardTopologyKeys = computed<ClientKey[]>(() => keys.value.slice(0, 6))
const dashboardTopologyHasData = computed(() => Boolean(dashboardTopologyKeys.value.length || dashboardTopologyGroups.value.length || shownDashUpstreams.value.length))
const topologyFocusKeyId = ref<number | null>(null)
const topologyFocusGroupId = ref<number | null>(null)
const topologyPulseActive = ref(false)
let topologyPulseTimer: number | null = null
const topologyFocusedKey = computed(() => topologyFocusKeyId.value == null ? null : keys.value.find((item) => Number(item.id) === topologyFocusKeyId.value) || null)
const topologyFocusedGroup = computed(() => {
  const groupID = topologyFocusGroupId.value ?? topologyFocusedKey.value?.group_id
  return groupID == null ? null : groups.value.find((item) => Number(item.id) === Number(groupID)) || null
})
const topologyFocusedUpstreamIDs = computed(() => new Set(topologyFocusedGroup.value?.upstream_ids || []))
const topologyHasFocus = computed(() => topologyFocusKeyId.value != null || topologyFocusGroupId.value != null)
const topologyFocusedUpstreams = computed(() => topologyFocusedGroup.value ? topologyGroupUpstreams(topologyFocusedGroup.value) : [])
function focusTopologyKey(item: ClientKey) {
  topologyFocusKeyId.value = Number(item.id)
  topologyFocusGroupId.value = null
}
function focusTopologyGroup(item: Group) {
  topologyFocusGroupId.value = Number(item.id)
  topologyFocusKeyId.value = null
}
function clearTopologyFocus() {
  topologyFocusKeyId.value = null
  topologyFocusGroupId.value = null
}
function triggerTopologyPulse() {
  topologyPulseActive.value = true
  if (topologyPulseTimer !== null) window.clearTimeout(topologyPulseTimer)
  topologyPulseTimer = window.setTimeout(() => {
    topologyPulseActive.value = false
    topologyPulseTimer = null
  }, 1800)
}
function topologyNodeState(type: 'key' | 'group' | 'upstream', id: number) {
  if (!topologyHasFocus.value) return ''
  if (type === 'key') return topologyFocusKeyId.value === id ? 'is-focused' : 'is-dimmed'
  if (type === 'group') return (topologyFocusedGroup.value?.id === id) ? 'is-focused' : 'is-dimmed'
  return topologyFocusedUpstreamIDs.value.has(id) ? 'is-focused' : 'is-dimmed'
}
function topologyGroupNodeState(group: UpstreamGroup) {
  if (!topologyHasFocus.value) return ''
  return group.items.some((item) => topologyFocusedUpstreamIDs.value.has(item.id)) ? 'is-focused' : 'is-dimmed'
}
const dashboardRows = computed<Json[]>(() => {
  const rows = dashboard.value.daily
  return Array.isArray(rows) ? rows : []
})
const dashboardHourlyRows = computed<Json[]>(() => Array.isArray(dashboard.value.hourly) ? dashboard.value.hourly : [])
const dashboardChartRows = computed<Json[]>(() => {
  if (dashboardRange.value === '24h') return dashboardHourlyRows.value.length ? dashboardHourlyRows.value : dashboardRows.value
  return dashboardRangeUsage.value
})
const dashboardTrendTotals = computed(() => dashboardChartRows.value.reduce((sum, row) => ({
  requests: sum.requests + Number(row.requests || 0),
  tokens: sum.tokens + Number(row.tokens ?? (Number(row.input_tokens || 0) + Number(row.output_tokens || 0))),
  cached: sum.cached + Number(row.cached_input_tokens || 0),
  cost: sum.cost + Number(row.cost_usd || 0),
}), { requests: 0, tokens: 0, cached: 0, cost: 0 }))
function usageTotalsFromRows(rows: Json[]) {
  return rows.reduce((sum, row) => ({
    requests: sum.requests + Number(row.requests || 0),
    successes: sum.successes + Number(row.successes || 0),
    tokens: sum.tokens + Number(row.tokens ?? (Number(row.input_tokens || 0) + Number(row.output_tokens || 0))),
    cost: sum.cost + Number(row.cost_usd || 0),
  }), { requests: 0, successes: 0, tokens: 0, cost: 0 })
}
const dashboardComparisonTotals = computed(() => {
  const supplied = dashboardComparison.value.totals || dashboardComparison.value.summary
  if (supplied) return {
    requests: Number(supplied.requests || 0), successes: Number(supplied.successes || 0),
    tokens: Number(supplied.input_tokens || 0) + Number(supplied.output_tokens || 0), cost: Number(supplied.cost_usd || 0),
  }
  return usageTotalsFromRows(listOf<Json>(dashboardComparison.value.daily || dashboardComparison.value.items))
})
const dashboardComparisonReady = computed(() => dashboardCompare.value && dashboardRange.value !== '24h' && !dashboardComparisonLoading.value && Boolean(Object.keys(dashboardComparison.value).length))
function dashboardDelta(key: 'requests' | 'tokens' | 'cost') {
  if (!dashboardComparisonReady.value) return null
  const current = dashboardTrendTotals.value[key]
  const previous = dashboardComparisonTotals.value[key]
  if (!previous) return current > 0 ? 1 : null
  return (current - previous) / previous
}
function dashboardDeltaText(key: 'requests' | 'tokens' | 'cost') {
  const delta = dashboardDelta(key)
  if (delta == null) return ''
  const percent = Math.abs(delta * 100).toFixed(1)
  return `${delta >= 0 ? '↑' : '↓'} ${percent}% 环比`
}
function dashboardDeltaTone(key: 'requests' | 'tokens' | 'cost') {
  const delta = dashboardDelta(key)
  return delta == null ? '' : delta >= 0 ? 'up' : 'down'
}
const dashboardTotals = computed(() => dashboardRows.value.reduce((sum, row) => ({
  requests: sum.requests + Number(row.requests || 0),
  tokens: sum.tokens + Number(row.tokens ?? (Number(row.input_tokens || 0) + Number(row.output_tokens || 0))),
}), { requests: 0, tokens: 0 }))
const dashboardCost = computed(() => Number(dashboard.value.cost_usd_24h || 0))
const dashboardCostCNY = computed(() => dashboardCost.value * Number(pricing.value.usd_cny_rate || 7.2))
const dashboardCostCoverage = computed(() => dashboard.value.cost_coverage == null ? null : Number(dashboard.value.cost_coverage))
const dashboardCacheHitRate = computed(() => dashboard.value.cache_hit_rate == null ? null : Number(dashboard.value.cache_hit_rate))
const dashboardRequestHitRate = computed(() => dashboard.value.request_hit_rate == null ? null : Number(dashboard.value.request_hit_rate))
const dashboardCostByModel = computed(() => {
  const totals = new Map<string, { model: string; cost_usd: number; requests: number; known: number; input: number; output: number; cached: number; cacheWrite: number }>()
  for (const row of dashboardCostRows.value) {
    const model = String(row.model || row.dimension_label || '未命名模型')
    const current = totals.get(model) || { model, cost_usd: 0, requests: 0, known: 0, input: 0, output: 0, cached: 0, cacheWrite: 0 }
    current.cost_usd += Number(row.cost_usd || 0)
    current.requests += Number(row.requests || 0)
    current.known += Number(row.cost_known_requests || 0)
    current.input += Number(row.input_tokens || 0)
    current.output += Number(row.output_tokens || 0)
    current.cached += Number(row.cached_input_tokens || row.cache_read_tokens || 0)
    current.cacheWrite += Number(row.cache_creation_input_tokens || row.cache_write_tokens || 0)
    totals.set(model, current)
  }
  return [...totals.values()].sort((a, b) => b.cost_usd - a.cost_usd).slice(0, 8)
})
const dashboardCost30d = computed(() => dashboardCostRows.value.reduce((sum, row) => sum + Number(row.cost_usd || 0), 0))
const dashboardCostCoverage30d = computed(() => {
  const requests = dashboardCostRows.value.reduce((sum, row) => sum + Number(row.requests || 0), 0)
  const known = dashboardCostRows.value.reduce((sum, row) => sum + Number(row.cost_known_requests || 0), 0)
  return requests > 0 ? known / requests : null
})
const dashboardCostForecast30d = computed(() => {
  const days = new Set(dashboardCostRows.value.map((row) => String(row.day || row.date || '').slice(0, 10)).filter(Boolean)).size
  return days > 0 ? dashboardCost30d.value / days * 30 : 0
})
const summary = computed(() => {
  const stats = dashboard.value.stats || dashboard.value.summary || dashboard.value
  const total = Number(stats.upstreams_total ?? stats.total_upstreams ?? upstreamGroups.value.length)
  const healthy = Number(stats.upstreams_healthy ?? stats.healthy_upstreams ?? upstreamGroups.value.filter((group) => group.healthy === group.total).length)
  return {
    total,
    healthy,
    requests: Number(stats.requests_24h ?? stats.request_count ?? 0),
    success: Number(stats.success_rate ?? 0),
            latency: Number(stats.avg_latency_ms ?? stats.average_latency_ms ?? 0),
  }
})
const usageRows = computed<Json[]>(() => {
  const raw = usage.value.daily ?? usage.value.trend ?? usage.value.items ?? usage.value.data ?? []
  return Array.isArray(raw) ? raw : []
})
const usageTotals = computed(() => {
  const supplied = usage.value.totals || usage.value.summary
  if (supplied) return supplied
  return usageRows.value.reduce((sum, row) => ({
    requests: sum.requests + Number(row.requests || 0),
    input_tokens: sum.input_tokens + Number(row.input_tokens || 0),
    output_tokens: sum.output_tokens + Number(row.output_tokens || 0),
    cached_input_tokens: sum.cached_input_tokens + Number(row.cached_input_tokens || 0),
    cache_creation_input_tokens: sum.cache_creation_input_tokens + Number(row.cache_creation_input_tokens || row.cache_write_tokens || 0),
    successes: sum.successes + Number(row.successes || 0),
    duration_ms: sum.duration_ms + Number(row.duration_ms || row.total_duration_ms || 0),
    cost_usd: sum.cost_usd + Number(row.cost_usd || 0),
    cost_known_requests: sum.cost_known_requests + Number(row.cost_known_requests || 0),
  }), { requests: 0, input_tokens: 0, output_tokens: 0, cached_input_tokens: 0, cache_creation_input_tokens: 0, successes: 0, duration_ms: 0, cost_usd: 0, cost_known_requests: 0 })
})
const usageInputTokens = computed(() => Number(usageTotals.value.input_tokens ?? 0))
const usageOutputTokens = computed(() => Number(usageTotals.value.output_tokens ?? 0))
const usageCachedTokens = computed(() => Number(usageTotals.value.cached_input_tokens ?? usageTotals.value.cache_read_tokens ?? 0))
const usageCacheWriteTokens = computed(() => {
  const value = usageTotals.value.cache_creation_input_tokens ?? usageTotals.value.cache_write_tokens
  return value == null ? null : Number(value)
})
const usageTokenHitRate = computed(() => {
  const explicit = usageTotals.value.token_hit_rate ?? usageTotals.value.cache_hit_rate
  if (explicit != null) return Number(explicit)
  if (usageTotals.value.usage_requests != null && Number(usageTotals.value.usage_requests) <= 0) return null
  const uncached = usageTotals.value.uncached_input_tokens ?? (usageInputTokens.value - usageCachedTokens.value)
  const denominator = usageCachedTokens.value + Number(uncached || 0)
  return denominator > 0 ? usageCachedTokens.value / denominator : null
})
const usageRequestHitRate = computed(() => {
  const explicit = usageTotals.value.request_hit_rate ?? usageTotals.value.cache_request_hit_rate
  if (explicit != null) return Number(explicit)
  if (usageTotals.value.usage_requests != null) {
    const usageRequests = Number(usageTotals.value.usage_requests)
    return usageRequests > 0 ? Number(usageTotals.value.cache_hit_requests ?? 0) / usageRequests : null
  }
  const requests = Number(usageTotals.value.requests || 0)
  const hits = Number(usageTotals.value.cache_hit_requests ?? usageTotals.value.cached_requests ?? 0)
  return requests > 0 && usageTotals.value.cache_hit_requests != null ? hits / requests : null
})
const usageAvgLatency = computed(() => usageTotals.value.avg_duration_ms ?? usageTotals.value.average_duration_ms ?? null)
const usageP95Latency = computed(() => usageTotals.value.p95_duration_ms ?? usageTotals.value.p95_ms ?? null)
const usageCostUsd = computed(() => Number(usageTotals.value.cost_usd ?? usageRows.value.reduce((sum, row) => sum + Number(row.cost_usd || 0), 0)))
const usageCostCoverage = computed(() => {
  const explicit = usageTotals.value.cost_coverage
  if (explicit != null) return Number(explicit)
  const requests = Number(usageTotals.value.requests || 0)
  const known = Number(usageTotals.value.cost_known_requests || 0)
  return requests > 0 && known > 0 ? known / requests : null
})
const usageCostForecast30d = computed(() => {
  const days = new Set(usageRows.value.map((row) => String(row.day || row.date || '').slice(0, 10)).filter(Boolean)).size || usageFilter.days
  return days > 0 ? usageCostUsd.value / days * 30 : 0
})
const selectedUpstreamModels = computed(() => parseModelList(upstreamForm.models))
const batchModelSelection = computed(() => modelBatchSelection(upstreamForm.models, discoveredModels.value))
const allDiscoveredModelsSelected = computed(() => discoveredModels.value.length > 0
  && discoveredModels.value.every((model) => selectedUpstreamModels.value.includes(model)))
const modelTestReports = computed(() => Object.values(modelTestResults.value))
const modelTestSummary = computed(() => modelTestReports.value.reduce((summary, report) => {
  summary[report.status]++
  return summary
}, { available: 0, partial: 0, unavailable: 0 }))
const modelTestsBusy = computed(() => modelTestProgress.running || testingModelNames.value.length > 0)

const activeOverlayId = computed(() => {
  if (confirmDialog.show) return 'confirm'
  if (ccswitchModal.value) return 'ccswitch'
  if (revealedKey.value) return 'secret'
  if (keySimulationModal.value) return 'key-simulation'
  if (passwordModal.value) return 'password'
  if (channelModal.value) return 'channel'
  if (keyModal.value) return 'key'
  if (upstreamModal.value) return 'upstream'
  if (upstreamGroupDrawer.value) return 'upstream-group'
  return ''
})
let restoreFocus: HTMLElement | null = null

function applyTheme() {
  const next = theme.value === 'auto' ? (colorScheme.matches ? 'dark' : 'light') : theme.value
  resolvedTheme.value = next
  document.documentElement.dataset.theme = next
  document.documentElement.style.colorScheme = next
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', next === 'dark' ? '#151816' : '#f4f6f4')
}

function setTheme(next: Theme) {
  theme.value = next
  writeStored('dapi-theme', next)
  applyTheme()
}

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
  writeStored('dapi-sidebar-collapsed', String(sidebarCollapsed.value))
}

function closeRowMenu(restore = true) {
  openRowMenu.value = ''
  if (restore) rowMenuTrigger?.focus()
  rowMenuTrigger = null
}

async function toggleRowMenu(id: string, event: MouseEvent) {
  if (openRowMenu.value === id) { closeRowMenu(); return }
  rowMenuTrigger = event.currentTarget as HTMLElement
  const rect = rowMenuTrigger.getBoundingClientRect()
  rowMenuPosition.top = rect.bottom + 6
  rowMenuPosition.left = Math.max(8, Math.min(window.innerWidth - 160, rect.right - 152))
  openRowMenu.value = id
  await nextTick()
  const menu = document.querySelector<HTMLElement>('.row-menu')
  if (!menu) return
  rowMenuPosition.top = rect.bottom + menu.offsetHeight + 14 <= window.innerHeight
    ? rect.bottom + 6
    : Math.max(8, rect.top - menu.offsetHeight - 6)
  menu.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
}

function handleRowMenuKeydown(event: KeyboardEvent) {
  const items = Array.from((event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="menuitem"]'))
  const current = items.indexOf(document.activeElement as HTMLElement)
  let next = current
  if (event.key === 'ArrowDown') next = (current + 1) % items.length
  else if (event.key === 'ArrowUp') next = (current - 1 + items.length) % items.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = items.length - 1
  else if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    closeRowMenu()
    return
  } else return
  event.preventDefault()
  items[next]?.focus()
}

function toggleSort(table: string, key: string) {
  const current = sortState[table]
  sortState[table] = { key, direction: current?.key === key && current.direction === 1 ? -1 : 1 }
}

function sortValue(item: Json, key: string): string | number {
  if (key === 'balance') return Number(item.balance?.available ?? -1)
  if (key === 'models') return Number(item.models?.length || 0)
  if (key === 'tokens') return Number(item.usage?.input_tokens || 0) + Number(item.usage?.output_tokens || 0)
  const value = key.split('.').reduce<any>((result, part) => result?.[part], item)
  if (typeof value === 'boolean') return value ? 1 : 0
  if (typeof value === 'number') return value
  const timestamp = typeof value === 'string' && /(_at|day)$/.test(key) ? Date.parse(value) : Number.NaN
  return Number.isNaN(timestamp) ? String(value ?? '').toLocaleLowerCase('zh-CN') : timestamp
}

function sortRows<T extends object>(rows: T[], table: string) {
  const current = sortState[table]
  if (!current) return rows
  return [...rows].sort((left, right) => {
    const a = sortValue(left as Json, current.key)
    const b = sortValue(right as Json, current.key)
    return (typeof a === 'number' && typeof b === 'number' ? a - b : String(a).localeCompare(String(b), 'zh-CN')) * current.direction
  })
}

function ariaSort(table: string, key: string): 'ascending' | 'descending' | 'none' {
  const current = sortState[table]
  if (current?.key !== key) return 'none'
  return current.direction === 1 ? 'ascending' : 'descending'
}

function toggleLog(requestID: string) {
  expandedLog.value = expandedLog.value === requestID ? null : requestID
}

function setLogPreset(preset: 'all' | 'errors') {
  if (preset === 'errors') logFilter.status = 'error'
  else Object.assign(logFilter, { status: '', upstream_id: '', group_id: '' })
  logFilter.offset = 0
  void loadLogs()
}

function attemptWidth(item: RequestLog, duration?: number) {
  const durations = (item.attempts || []).map((attempt) => Number(attempt.duration_ms || 0))
  const max = Math.max(...durations, Number(item.duration_ms || 0), 1)
  return `${Math.max(6, Math.round(Number(duration || 0) / max * 100))}%`
}

function copyWithExecCommand(value: string) {
  const field = document.createElement('textarea')
  field.value = value
  field.setAttribute('readonly', '')
  field.style.cssText = 'position:fixed;inset:0;opacity:0;pointer-events:none'
  document.body.appendChild(field)
  let copied = false
  try { field.select(); copied = document.execCommand('copy') } catch { copied = false } finally { field.remove() }
  return copied
}

async function copyValue(value: string, label = '内容') {
  let copied = false
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try { await navigator.clipboard.writeText(value); copied = true } catch { copied = copyWithExecCommand(value) }
  } else {
    copied = copyWithExecCommand(value)
  }
  notify(copied ? `${label}已复制` : '复制失败，请手动选择并复制', !copied)
  return copied
}

async function resolveClientKey(item: ClientKey, signal?: AbortSignal) {
  const result = await api.get<Json>(`/api/admin/keys/${item.id}/secret`, { signal })
  const rawKey = result?.key || result?.api_key || result?.secret || ''
  if (!rawKey) throw new Error('该密钥没有可复制的加密副本，请重新创建密钥')
  return rawKey
}

function requestConfirmation(title: string, message: string, confirmLabel = '删除') {
  Object.assign(confirmDialog, { show: true, title, message, confirmLabel })
  return new Promise<boolean>((resolve) => { resolveConfirmation = resolve })
}

function settleConfirmation(confirmed: boolean) {
  confirmDialog.show = false
  resolveConfirmation?.(confirmed)
  resolveConfirmation = null
}

function activeOverlay() {
  if (ccswitchModal.value) return document.querySelector<HTMLElement>('.ccswitch-backdrop') || null
  return Array.from(document.querySelectorAll<HTMLElement>('.modal-backdrop, .drawer-backdrop')).at(-1) || null
}

function handleGlobalKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    if (openRowMenu.value) { closeRowMenu(); return }
    if (ccswitchModal.value) { closeCCSwitch(); return }
    if (revealedKey.value) return
    if (keySimulationModal.value) { closeKeySimulation(); return }
    if (confirmDialog.show) settleConfirmation(false)
    else if (passwordModal.value) passwordModal.value = false
    else if (channelModal.value) channelModal.value = false
    else if (keyModal.value) keyModal.value = false
    else if (upstreamModal.value) closeUpstream()
    else if (upstreamGroupDrawer.value) upstreamGroupDrawer.value = null
    return
  }
  if (event.key !== 'Tab' || !activeOverlayId.value) return
  const overlay = activeOverlay()
  const focusable = Array.from(overlay?.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])') || [])
  if (!focusable.length) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}

applyTheme()

watch(activeOverlayId, async (current, previous) => {
  if (current) {
    if (!previous) restoreFocus = document.activeElement as HTMLElement
    await nextTick()
    const overlay = activeOverlay()
    const target = overlay?.querySelector<HTMLElement>('[autofocus]') || overlay?.querySelector<HTMLElement>('button, input, select, textarea')
    target?.focus()
  } else if (previous) {
    restoreFocus?.focus()
    restoreFocus = null
  }
})

function notify(message: string, error = false) {
  window.clearTimeout(toastTimer)
  Object.assign(toast, { show: true, message, error })
  toastTimer = window.setTimeout(() => { toast.show = false }, 3200)
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : '操作失败'
}

async function login() {
  loginBusy.value = true
  loginError.value = ''
  try {
    await api.post('/api/admin/login', loginForm)
    admin.value = await api.get('/api/admin/me')
    auth.value = 'ready'
    await go('dashboard')
  } catch (error) {
    loginError.value = error instanceof ApiError && error.status === 401 ? '用户名或密码错误' : errorMessage(error)
  } finally {
    loginBusy.value = false
  }
}

async function logout() {
  try { await api.post('/api/admin/logout') } finally {
    autoRefresh.value = false
    closeCCSwitch()
    closeSecret()
    auth.value = 'guest'
    loginForm.password = ''
  }
}

async function changePassword() {
  if (passwordForm.new_password !== passwordForm.confirm_password) return notify('两次输入的新密码不一致', true)
  saving.value = true
  try {
    await api.put('/api/admin/password', { current_password: passwordForm.current_password, new_password: passwordForm.new_password })
    passwordModal.value = false
    auth.value = 'guest'
    loginForm.password = ''
    Object.assign(passwordForm, { current_password: '', new_password: '', confirm_password: '' })
    notify('密码已修改，请重新登录')
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function go(next: View) {
  view.value = next
  menuOpen.value = false
  if (location.hash !== `#${next}`) history.pushState(null, '', `#${next}`)
  await loadCurrent()
}

function beginPageLoad() {
  pageLoadController?.abort()
  dashboardRangeController?.abort()
  dashboardRangeController = null
  const controller = new AbortController()
  pageLoadController = controller
  const sequence = ++pageLoadSequence
  loading.value = true
  refreshError.value = ''
  return { controller, sequence }
}

function isCurrentPageLoad(sequence: number) {
  return sequence === pageLoadSequence && !pageLoadController?.signal.aborted
}

function viewFromLocation() {
  const hash = location.hash.slice(1) as View
  return navItems.some((item) => item.id === hash) ? hash : 'dashboard'
}

function handleHistoryNavigation() {
  const next = viewFromLocation()
  if (next === view.value) return
  view.value = next
  void loadCurrent()
}

async function loadCurrent() {
  const { controller, sequence } = beginPageLoad()
  const signal = controller.signal
  try {
    if (view.value === 'dashboard') {
      const [dash, ups, costData, pricingData, groupData, keyData] = await Promise.all([
        api.get<Json>('/api/admin/dashboard', { signal }), api.get('/api/admin/upstreams', { signal }),
        api.get<Json>('/api/admin/usage?days=30&granularity=day&dimension=model&top_n=8', { signal }),
        api.get<Json>('/api/admin/pricing', { signal }),
        api.get('/api/admin/groups', { signal }), api.get('/api/admin/keys', { signal }),
      ])
      dashboard.value = dash || {}
      upstreams.value = listOf<Upstream>(ups)
      dashboardCostRows.value = listOf<Json>(costData?.daily || costData?.items)
      pricing.value = pricingData || {}
      groups.value = listOf<Group>(groupData)
      keys.value = listOf<ClientKey>(keyData)
      triggerTopologyPulse()
      if (dashboardRange.value !== '24h') await loadDashboardRange(signal)
    } else if (view.value === 'upstreams') {
      const [upstreamData, pricingData] = await Promise.all([api.get('/api/admin/upstreams', { signal }), api.get<Json>('/api/admin/pricing', { signal })])
      upstreams.value = listOf<Upstream>(upstreamData)
      pricing.value = pricingData || {}
    }
    else if (view.value === 'groups') {
      const [groupData, upstreamData] = await Promise.all([api.get('/api/admin/groups', { signal }), api.get('/api/admin/upstreams', { signal })])
      groups.value = listOf<Group>(groupData)
      upstreams.value = listOf<Upstream>(upstreamData)
    }
    else if (view.value === 'keys') {
      const [keyData, groupData] = await Promise.all([api.get('/api/admin/keys', { signal }), api.get('/api/admin/groups', { signal })])
      keys.value = listOf<ClientKey>(keyData)
      groups.value = listOf<Group>(groupData)
    }
    else if (view.value === 'logs') {
      const [, upstreamData, groupData] = await Promise.all([loadLogs(signal), api.get('/api/admin/upstreams', { signal }), api.get('/api/admin/groups', { signal })])
      upstreams.value = listOf<Upstream>(upstreamData)
      groups.value = listOf<Group>(groupData)
    }
    else if (view.value === 'usage') {
      const [, upstreamData, keyData, groupData] = await Promise.all([loadUsage(signal), api.get('/api/admin/upstreams', { signal }), api.get('/api/admin/keys', { signal }), api.get('/api/admin/groups', { signal })])
      upstreams.value = listOf<Upstream>(upstreamData)
      keys.value = listOf<ClientKey>(keyData)
      groups.value = listOf<Group>(groupData)
    }
    else {
      const [channelData, ruleData, settingsData, upstreamData] = await Promise.all([
        api.get('/api/admin/channels', { signal }), api.get('/api/admin/alert-rules', { signal }), api.get<Json>('/api/admin/settings', { signal }), api.get('/api/admin/upstreams', { signal }),
      ])
      channels.value = listOf<Channel>(channelData)
      alertRules.value = listOf<AlertRule>(ruleData)
      maxAttempts.value = Number(settingsData.max_attempts || 3)
      upstreams.value = listOf<Upstream>(upstreamData)
    }
    lastUpdatedAt.value = new Date()
  } catch (error) {
    if (isAbortError(error) || !isCurrentPageLoad(sequence)) return
    if (error instanceof ApiError && error.status === 401) {
      autoRefresh.value = false
      auth.value = 'guest'
    } else {
      refreshError.value = errorMessage(error)
      notify(refreshError.value, true)
    }
  } finally {
    if (isCurrentPageLoad(sequence)) {
      loading.value = false
      pageLoadController = null
    }
  }
}

function fmtUpdatedAt(value: Date | null) {
  if (!value) return '尚未刷新'
  return `更新于 ${new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(value)}`
}

function toggleDashboardMetrics() {
  dashboardMetricsExpanded.value = !dashboardMetricsExpanded.value
}

function exportUsageCSV() {
  const headers = ['日期', '维度', '请求', '成功', '输入 Token', '输出 Token', '缓存读', '缓存写', '平均耗时(s)', 'P95耗时(s)']
  const seconds = (value: unknown) => value == null || Number.isNaN(Number(value)) ? '' : (Number(value) / 1000).toFixed(2)
  const rows = usageRows.value.map((row) => [
    String(row.day || row.date || row.label || '').slice(0, 10), usageDimensionLabel(row), row.requests, row.successes,
    row.input_tokens, row.output_tokens, row.cached_input_tokens ?? row.cache_read_tokens ?? '',
    row.cache_creation_input_tokens ?? row.cache_write_tokens ?? '', seconds(row.avg_duration_ms ?? row.average_duration_ms), seconds(row.p95_duration_ms ?? row.p95_ms),
  ])
  const escapeCell = (value: unknown) => `"${String(value ?? '').replace(/"/g, '""')}"`
  const csv = [headers, ...rows].map((row) => row.map(escapeCell).join(',')).join('\n')
  const blob = new Blob([`\ufeff${csv}`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `dapi-usage-${new Date().toISOString().slice(0, 10)}.csv`
  link.click()
  URL.revokeObjectURL(url)
  notify(`已导出 ${rows.length} 条用量记录`)
}

async function loadDashboardRange(signal?: AbortSignal) {
  const sequence = ++dashboardRangeSequence
  dashboardRangeController?.abort()
  if (dashboardRange.value === '24h') {
    dashboardRangeController = null
    dashboardRangeUsage.value = []
    dashboardComparison.value = {}
    dashboardComparisonLoading.value = false
    return
  }
  const controller = new AbortController()
  dashboardRangeController = controller
  const abortFromPageLoad = () => controller.abort()
  signal?.addEventListener('abort', abortFromPageLoad, { once: true })
  const days = dashboardRange.value === '7d' ? 7 : 30
  const today = new Date()
  today.setUTCHours(0, 0, 0, 0)
  const to = new Date(today)
  const from = new Date(today)
  from.setUTCDate(from.getUTCDate() - days + 1)
  const previousTo = new Date(from)
  previousTo.setUTCDate(previousTo.getUTCDate() - 1)
  const previousFrom = new Date(previousTo)
  previousFrom.setUTCDate(previousFrom.getUTCDate() - days + 1)
  const fmt = (value: Date) => value.toISOString().slice(0, 10)
  const query = (start: Date, end: Date) => `/api/admin/usage?granularity=day&dimension=&top_n=1&from=${fmt(start)}&to=${fmt(end)}`
  dashboardComparisonLoading.value = true
  try {
    const currentRequest = api.get<Json>(query(from, to), { signal: controller.signal })
    const previousRequest = dashboardCompare.value ? api.get<Json>(query(previousFrom, previousTo), { signal: controller.signal }) : Promise.resolve<Json>({})
    const [result, previous] = await Promise.all([currentRequest, previousRequest])
    if (controller.signal.aborted || sequence !== dashboardRangeSequence) return
    dashboardRangeUsage.value = listOf<Json>(result?.daily || result?.items)
    dashboardComparison.value = previous || {}
  } catch (error) { if (!isAbortError(error)) notify(errorMessage(error), true) }
  finally {
    signal?.removeEventListener('abort', abortFromPageLoad)
    if (sequence === dashboardRangeSequence) {
      dashboardComparisonLoading.value = false
      dashboardRangeController = null
    }
  }
}

function toggleDashboardCompare() {
  dashboardCompare.value = !dashboardCompare.value
  void loadDashboardRange()
}

async function refreshPricing() {
  saving.value = true
  try {
    await api.post('/api/admin/pricing/refresh')
    pricing.value = await api.get<Json>('/api/admin/pricing')
    notify('LiteLLM 价格已同步；手动档案仍作为兜底')
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function backfillPricing() {
  if (!(await requestConfirmation('回算历史成本', '将按请求发生时有效的价格档案补齐最近 365 天未知成本。'))) return
  saving.value = true
  try {
    const result = await api.post<Json>('/api/admin/pricing/backfill', {})
    await loadCurrent()
    notify(`已回算 ${Number(result?.logs_updated || 0).toLocaleString()} 条请求成本`)
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

function openPricingProfile(profile?: Json) {
  editingPricing.value = profile?.id ?? null
  const prices = listOf<Json>(profile?.prices).map((price) => [price.model, price.input_usd_per_million, price.output_usd_per_million, price.cache_read_usd_per_million, price.cache_write_usd_per_million].join(', ')).join('\n')
  Object.assign(pricingForm, profile ? { name: profile.name, provider: profile.provider || '', source_url: profile.source_url || '', source_version: profile.source_version || 'custom', prices } : { name: '', provider: '', source_url: '', source_version: 'custom', prices: '' })
  pricingModal.value = true
}

async function savePricingProfile() {
  const prices = pricingForm.prices.split('\n').map((line) => line.split(',').map((value) => value.trim())).filter((parts) => parts[0]).map(([model, input, output, cacheRead, cacheWrite]) => ({ model, input_usd_per_million: Number(input || 0), output_usd_per_million: Number(output || 0), cache_read_usd_per_million: Number(cacheRead || 0), cache_write_usd_per_million: Number(cacheWrite || 0) }))
  if (prices.some((price) => !Number.isFinite(price.input_usd_per_million) || !Number.isFinite(price.output_usd_per_million) || !Number.isFinite(price.cache_read_usd_per_million) || !Number.isFinite(price.cache_write_usd_per_million))) {
    notify('价格必须是数字', true)
    return
  }
  saving.value = true
  try {
    const payload = { name: pricingForm.name, provider: pricingForm.provider, source_url: pricingForm.source_url, source_version: pricingForm.source_version, prices }
    if (editingPricing.value) await api.put(`/api/admin/pricing/profiles/${editingPricing.value}`, payload)
    else await api.post('/api/admin/pricing/profiles', payload)
    pricing.value = await api.get<Json>('/api/admin/pricing')
    pricingModal.value = false
    notify(editingPricing.value ? '价格档案已更新' : '价格档案已创建')
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function removePricingProfile(profile: Json) {
  if (!(await requestConfirmation('删除价格档案', `“${profile.name}”将被删除，已绑定上游会恢复为未计价。`))) return
  try {
    await api.delete(`/api/admin/pricing/profiles/${profile.id}`)
    pricing.value = await api.get<Json>('/api/admin/pricing')
    notify('价格档案已删除')
  } catch (error) { notify(errorMessage(error), true) }
}

function pricingProfileName(id?: number) {
  if (!id) return '未计价'
  return listOf<Json>(pricing.value.profiles).find((profile) => Number(profile.id) === Number(id))?.name || `档案 #${id}`
}

function groupStatusText(group: UpstreamGroup) {
  if (!group.enabled) return '已停用'
  if (!group.available && group.balance_suspended) return `余额暂停 · ${group.balance_suspended}/${group.enabled}`
  if (group.balance_suspended) return `部分可用 · ${group.available}/${group.enabled}`
  return group.healthy === group.available ? `正常 · ${group.healthy}/${group.available}` : group.healthy > 0 ? `部分正常 · ${group.healthy}/${group.available}` : `异常 · 0/${group.available}`
}

function groupStatusTone(group: UpstreamGroup) {
  if (!group.enabled || (!group.available && group.balance_suspended)) return 'warn'
  if (!group.available || group.healthy === 0) return 'bad'
  if (group.balance_suspended || group.healthy < group.available) return 'warn'
  return 'good'
}

function groupCircuitText(group: UpstreamGroup) {
  return group.circuit_open ? `熔断 ${group.circuit_open}/${group.enabled}` : '未熔断'
}

function upstreamRouteText(upstream: Upstream) {
  if (!upstream.enabled) return '已停用'
  if (upstream.balance_suspended) return '余额暂停'
  return statusText(upstream.health_status)
}

function upstreamRouteTone(upstream: Upstream) {
  if (!upstream.enabled || upstream.balance_suspended) return 'warn'
  return statusTone(upstream.health_status)
}

function balanceProtectionText(upstream: Upstream) {
  if (upstream.balance_protection_enabled === false) return '已关闭'
  if (upstream.balance_suspended) return '已暂停路由'
  if (upstream.zero_balance_checks) return `待确认 ${upstream.zero_balance_checks}/2`
  return '保护中'
}

function topologyGroupUpstreams(group: Group) {
  return (group.upstream_ids || []).map((id) => upstreams.value.find((item) => Number(item.id) === Number(id))).filter(Boolean) as Upstream[]
}

function topologyGroupPriorities(group: Group) {
  return [...new Set(topologyGroupUpstreams(group).map((item) => Number(item.priority)).filter((priority) => Number.isFinite(priority)))].sort((a, b) => a - b)
}

function topologyKeysForGroup(group: Group) {
  return keys.value.filter((key) => Number(key.group_id) === Number(group.id))
}

function openUpstreamGroup(group: UpstreamGroup) {
  upstreamGroupDrawer.value = group
}

async function loadLogs(signal?: AbortSignal) {
  const cycle = signal ? null : beginPageLoad()
  const requestSignal = signal || cycle!.controller.signal
  const query = new URLSearchParams({ limit: String(logFilter.limit), offset: String(logFilter.offset) })
  if (logFilter.status) query.set('status', logFilter.status)
  if (logFilter.upstream_id) query.set('upstream_id', logFilter.upstream_id)
  if (logFilter.group_id) query.set('group_id', logFilter.group_id)
  try {
    const result = await api.get<RequestLog[] | Json>(`/api/admin/logs?${query}`, { signal: requestSignal })
    if (!requestSignal.aborted && (!cycle || isCurrentPageLoad(cycle.sequence))) logs.value = listOf<RequestLog>(result)
  } catch (error) {
    if (!cycle) throw error
    if (!isAbortError(error)) notify(errorMessage(error), true)
  } finally {
    if (cycle && isCurrentPageLoad(cycle.sequence)) {
      loading.value = false
      pageLoadController = null
    }
  }
}

async function loadUsage(signal?: AbortSignal) {
  const cycle = signal ? null : beginPageLoad()
  const requestSignal = signal || cycle!.controller.signal
  const query = new URLSearchParams({
    days: String(usageFilter.days),
    granularity: usageFilter.granularity,
    dimension: usageFilter.dimension,
    top_n: String(usageFilter.topN),
  })
  const filterKey = ({ upstream: 'upstream_id', api_key: 'api_key_id', group: 'group_id', protocol: 'protocol', model: 'model' } as Record<string, keyof typeof usageFilter>)[usageFilter.dimension]
  if (filterKey && usageFilter[filterKey]) query.set(filterKey, String(usageFilter[filterKey]))
  try {
    const result = await api.get<Json>(`/api/admin/usage?${query}`, { signal: requestSignal })
    if (!requestSignal.aborted && (!cycle || isCurrentPageLoad(cycle.sequence))) usage.value = result
  } catch (error) {
    if (!cycle) throw error
    if (!isAbortError(error)) notify(errorMessage(error), true)
  } finally {
    if (cycle && isCurrentPageLoad(cycle.sequence)) {
      loading.value = false
      pageLoadController = null
    }
  }
}

async function applyUsageFilters() {
  await loadUsage()
  mobileFiltersOpen.value = false
}

function openUpstream(item?: Upstream) {
  upstreamGroupDrawer.value = null
  modelTestRun++
  singleModelTestController?.abort()
  singleModelTestController = null
  if (activeModelTestRun) {
    activeModelTestRun.stopped = true
    activeModelTestRun.controller.abort()
    activeModelTestRun = null
  }
  editingUpstream.value = item?.id ?? null
  upstreamModelMode.value = item?.models_locked ? 'manual' : 'auto'
  fetchingModels.value = false
  modelDiscoveryAttempted.value = false
  discoveredModels.value = []
  testingModelNames.value = []
  modelTestResults.value = {}
  Object.assign(modelTestProgress, { running: false, stopped: false, total: 0, completed: 0 })
  Object.assign(upstreamForm, item ? {
    name: item.name, kind: item.kind, base_url: item.base_url, api_key: '', access_token: '', user_id: '', enabled: item.enabled,
    balance_protection_enabled: item.balance_protection_enabled ?? true,
    user_agent_mode: userAgentMode(item.user_agent || ''), user_agent: item.user_agent || '',
    priority: item.priority, protocols: [...(item.protocols || [])], models: (item.models || []).join(', '),
    aliases: Object.entries(item.model_aliases || {}).map(([from, to]) => `${from}=${to}`).join('\n'),
    connect_timeout_ms: item.connect_timeout_ms ?? 5000, first_byte_timeout_ms: item.first_byte_timeout_ms ?? 60000,
    idle_timeout_ms: item.idle_timeout_ms ?? 300000, failure_threshold: item.failure_threshold ?? 3,
    cooldown_seconds: item.cooldown_seconds ?? 60, clear_balance_credentials: false,
    pricing_profile_id: item.pricing_profile_id ?? 0,
  } : {
    name: '', kind: 'sub2api', base_url: '', api_key: '', access_token: '', user_id: '', enabled: true, priority: 100,
    balance_protection_enabled: true,
    user_agent_mode: 'default', user_agent: '',
    protocols: [...DEFAULT_UPSTREAM_PROTOCOLS], models: '', aliases: '', connect_timeout_ms: 5000, first_byte_timeout_ms: 60000,
    idle_timeout_ms: 300000, failure_threshold: 3, cooldown_seconds: 60, clear_balance_credentials: false, pricing_profile_id: 0,
  })
  upstreamModal.value = true
}

function upstreamPayload() {
  const aliases: Record<string, string> = {}
  upstreamForm.aliases.split('\n').forEach((line) => {
    const [from, ...to] = line.split('=')
    if (from?.trim() && to.join('=').trim()) aliases[from.trim()] = to.join('=').trim()
  })
  const newapi = usesNewAPICredentials(upstreamForm.kind)
  const { user_agent_mode: userAgentModeValue, ...payload } = upstreamForm
  return {
    ...payload,
    user_agent: userAgentValue(userAgentModeValue, upstreamForm.user_agent),
    base_url: upstreamForm.base_url.replace(/\/+$/, ''),
    access_token: newapi ? upstreamForm.access_token : '',
    user_id: newapi ? upstreamForm.user_id : '',
    clear_balance_credentials: newapi ? upstreamForm.clear_balance_credentials : editingUpstream.value !== null,
    models: modelsForPayload(upstreamForm.models, upstreamModelMode.value === 'manual'),
    models_locked: upstreamModelMode.value === 'manual',
    model_aliases: aliases,
    aliases: undefined,
  }
}

function selectCommonModel(model: string, event: Event) {
  upstreamForm.models = setModelSelected(upstreamForm.models, model, (event.target as HTMLInputElement).checked)
}

function selectDiscoveredModel(model: string, event: Event) {
  upstreamModelMode.value = 'manual'
  upstreamForm.models = setModelSelected(upstreamForm.models, model, (event.target as HTMLInputElement).checked)
}

function toggleDiscoveredModels() {
  upstreamModelMode.value = 'manual'
  upstreamForm.models = bulkSetModels(upstreamForm.models, discoveredModels.value, !allDiscoveredModelsSelected.value)
}

function modelStatusText(status: ModelTestReport['status']) {
  return { available: '可用', partial: '部分可用', unavailable: '不可用' }[status]
}

function modelStatusTone(status: ModelTestReport['status']) {
  return { available: 'good', partial: 'warn', unavailable: 'bad' }[status]
}

function isAbortError(error: unknown) {
  return error instanceof Error && error.name === 'AbortError'
}

async function probeUpstream() {
  return api.post<Json>('/api/admin/upstreams/test', { ...upstreamPayload(), id: editingUpstream.value || 0 })
}

async function requestModelTest(model: string, audit: boolean, signal?: AbortSignal) {
  const report = await api.post<ModelTestReport>('/api/admin/upstreams/test-model', {
    ...upstreamPayload(), id: editingUpstream.value || 0, model, audit,
  }, { signal })
  return { ...report, model: report.model || model, results: Array.isArray(report.results) ? report.results : [] }
}

async function testOneModel(model: string) {
  if (!upstreamFormElement.value?.reportValidity() || modelTestsBusy.value) return
  if (!upstreamForm.protocols.length) return notify('请至少选择一个协议', true)
  const controller = new AbortController()
  singleModelTestController = controller
  testingModelNames.value = [model]
  try {
    const report = await requestModelTest(model, true, controller.signal)
    if (singleModelTestController !== controller) return
    modelTestResults.value = { ...modelTestResults.value, [model]: report }
  } catch (error) {
    if (isAbortError(error) || singleModelTestController !== controller) return
    const message = errorMessage(error)
    modelTestResults.value = { ...modelTestResults.value, [model]: { model, status: 'unavailable', results: [], error: message } }
    notify(message, true)
  } finally {
    if (singleModelTestController === controller) {
      singleModelTestController = null
      testingModelNames.value = []
    }
  }
}

async function auditModelBatch(reports: ModelTestReport[], stopped: boolean, id: number, name: string) {
  const counts = reports.reduce((summary, report) => {
    summary[report.status]++
    summary.protocolRequests += report.results.length
    return summary
  }, { available: 0, partial: 0, unavailable: 0, protocolRequests: 0 })
  await api.post('/api/admin/upstreams/test-models/audit', {
    id,
    name,
    models_count: reports.length,
    protocol_requests: counts.protocolRequests,
    available: counts.available,
    partial: counts.partial,
    unavailable: counts.unavailable,
    stopped,
  })
}

function stopModelTests() {
  if (!activeModelTestRun) return
  activeModelTestRun.stopped = true
  modelTestProgress.stopped = true
  activeModelTestRun.controller.abort()
}

function closeUpstream() {
  if (activeModelTestRun) stopModelTests()
  singleModelTestController?.abort()
  singleModelTestController = null
  testingModelNames.value = []
  upstreamModal.value = false
}

async function testSelectedModels() {
  if (!upstreamFormElement.value?.reportValidity() || modelTestsBusy.value) return
  if (!upstreamForm.protocols.length) return notify('请至少选择一个协议', true)
  const selection = batchModelSelection.value
  if (!selection.total) return notify('请先选择已获取的模型', true)
  if (selection.exceedsLimit) return notify(`一次最多测试 20 个模型，当前已选 ${selection.total} 个`, true)
  const models = selection.models
  if (models.length > 5) {
    const requests = models.length
    const confirmed = await requestConfirmation(
      '批量测试模型',
      `将测试 ${models.length} 个模型，共 ${requests} 个协议请求：${models.join('、')}。`,
      '开始测试',
    )
    if (!confirmed) return
  }

  const run = { id: ++modelTestRun, stopped: false, controller: new AbortController() }
  const upstreamID = editingUpstream.value || 0
  const upstreamName = upstreamForm.name
  activeModelTestRun = run
  modelTestResults.value = {}
  Object.assign(modelTestProgress, { running: true, stopped: false, total: models.length, completed: 0 })
  const completedReports: ModelTestReport[] = []
  let cursor = 0
  const worker = async () => {
    while (!run.stopped) {
      const model = models[cursor++]
      if (!model) return
      testingModelNames.value = [...testingModelNames.value, model]
      try {
        const report = await requestModelTest(model, false, run.controller.signal)
        completedReports.push(report)
        if (activeModelTestRun?.id === run.id) {
          modelTestResults.value = { ...modelTestResults.value, [model]: report }
          modelTestProgress.completed++
        }
      } catch (error) {
        if (isAbortError(error) || run.stopped) return
        const report: ModelTestReport = { model, status: 'unavailable', results: [], error: errorMessage(error) }
        completedReports.push(report)
        if (activeModelTestRun?.id === run.id) {
          modelTestResults.value = { ...modelTestResults.value, [model]: report }
          modelTestProgress.completed++
        }
      } finally {
        testingModelNames.value = testingModelNames.value.filter((item) => item !== model)
      }
    }
  }

  await Promise.all(Array.from({ length: Math.min(3, models.length) }, worker))
  try {
    await auditModelBatch(completedReports, run.stopped, upstreamID, upstreamName)
  } catch {
    if (activeModelTestRun?.id === run.id) notify('模型测试已结束，但审计记录失败', true)
  } finally {
    if (activeModelTestRun?.id === run.id) {
      activeModelTestRun = null
      modelTestProgress.running = false
      testingModelNames.value = []
      notify(run.stopped ? `已停止，保留 ${completedReports.length} 个完成结果` : `已完成 ${completedReports.length} 个模型测试`)
    }
  }
}

async function fetchUpstreamModels() {
  if (!upstreamFormElement.value?.reportValidity()) return
  if (!upstreamForm.protocols.length) return notify('请至少选择一个协议', true)
  const selectedBeforeDiscovery = upstreamModelMode.value === 'manual' ? upstreamForm.models : ''
  fetchingModels.value = true
  modelDiscoveryAttempted.value = true
  discoveredModels.value = []
  modelTestResults.value = {}
  try {
    const result = await probeUpstream()
    const ok = result.status === 'healthy'
    const models = Array.from(new Set((Array.isArray(result.models) ? result.models : [])
      .map((model) => String(model).trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b))
    discoveredModels.value = models
    if (models.length) {
      upstreamModelMode.value = 'manual'
      upstreamForm.models = selectedBeforeDiscovery
      notify(`已获取 ${models.length} 个模型`)
    } else {
      notify(ok ? '上游未返回模型' : connectionTestText(result), true)
    }
  } catch (error) {
    notify(errorMessage(error), true)
  } finally { fetchingModels.value = false }
}

async function saveUpstream() {
  if (activeModelTestRun) stopModelTests()
  singleModelTestController?.abort()
  singleModelTestController = null
  saving.value = true
  try {
    const payload = upstreamPayload()
    if (editingUpstream.value) await api.put(`/api/admin/upstreams/${editingUpstream.value}`, payload)
    else await api.post('/api/admin/upstreams', payload)
    upstreamModal.value = false
    notify(editingUpstream.value ? '上游已更新' : '上游已添加')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function removeUpstream(item: Upstream) {
  if (!(await requestConfirmation('删除上游', `“${item.name}”将停止接收新请求，历史日志仍会保留。`))) return
  try { await api.delete(`/api/admin/upstreams/${item.id}`); upstreamGroupDrawer.value = null; notify('上游已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

async function upstreamAction(item: Upstream, action: 'check' | 'balance' | 'models') {
  upstreamGroupDrawer.value = null
  try {
    await api.post(`/api/admin/upstreams/${item.id}/${action}`)
    notify({ check: '连接检查完成', balance: '余额已刷新', models: '模型列表已刷新' }[action])
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) }
}

function toggleUpstreamSelection(item: Upstream, event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  upstreamSelectedIds.value = checked
    ? [...new Set([...upstreamSelectedIds.value, item.id])]
    : upstreamSelectedIds.value.filter((id) => id !== item.id)
}

function toggleAllVisibleUpstreams(event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  const visibleIDs = filteredUpstreamGroups.value.flatMap((item) => item.items.map((entry) => entry.id))
  upstreamSelectedIds.value = checked
    ? [...new Set([...upstreamSelectedIds.value, ...visibleIDs])]
    : upstreamSelectedIds.value.filter((id) => !visibleIDs.includes(id))
}

async function bulkUpstreamAction(action: 'check' | 'balance') {
  const items = upstreams.value.filter((item) => upstreamSelectedIds.value.includes(item.id))
  if (!items.length) return notify('请先选择上游', true)
  saving.value = true
  try {
    const results = await Promise.allSettled(items.map((item) => api.post(`/api/admin/upstreams/${item.id}/${action}`)))
    const succeeded = results.filter((result) => result.status === 'fulfilled').length
    const failed = results.length - succeeded
    upstreamSelectedIds.value = []
    notify(failed
      ? `${action === 'check' ? '检查' : '刷新余额'}完成：${succeeded} 个成功，${failed} 个失败`
      : action === 'check' ? `已检查 ${items.length} 个上游` : `已刷新 ${items.length} 个上游余额`, failed > 0)
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) }
  finally { saving.value = false }
}

function openChannel() {
  Object.assign(channelForm, { name: '', kind: 'webhook', provider: '', enabled: true, target: '', smtp_host: '', smtp_port: 587, username: '', password: '' })
  channelModal.value = true
}

function openKey(item?: ClientKey) {
  editingKey.value = item?.id ?? null
  Object.assign(keyForm, item ? {
    name: item.name, enabled: item.enabled, group_id: item.group_id || 0, protocols: [...(item.protocols || [])], models: (item.models || []).join(', '),
  } : { name: '', enabled: true, group_id: groups.value.find((group) => group.enabled && (group.upstream_ids || []).length)?.id || 0, protocols: ['responses'], models: '' })
  keyModal.value = true
}

function openGroup(item?: Group) {
  editingGroup.value = item?.id ?? null
  Object.assign(groupForm, item ? { name: item.name, enabled: item.enabled, upstream_ids: [...(item.upstream_ids || [])] } : { name: '', enabled: true, upstream_ids: [] })
  groupModal.value = true
}

async function saveGroup() {
  saving.value = true
  try {
    const payload = { name: groupForm.name, enabled: groupForm.enabled, upstream_ids: groupForm.upstream_ids }
    if (editingGroup.value) await api.put(`/api/admin/groups/${editingGroup.value}`, payload)
    else await api.post('/api/admin/groups', payload)
    groupModal.value = false
    notify(editingGroup.value ? '分组已更新' : '分组已创建')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function removeGroup(item: Group) {
  if (item.key_count > 0) return notify('分组仍有绑定密钥，请先迁移密钥', true)
  if (!(await requestConfirmation('删除分组', `“${item.name}”将被删除。`))) return
  try { await api.delete(`/api/admin/groups/${item.id}`); notify('分组已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

async function saveKey() {
  saving.value = true
  try {
    const payload = { ...keyForm, group_id: Number(keyForm.group_id), models: keyForm.models.split(',').map((v) => v.trim()).filter(Boolean) }
    const result: Json = editingKey.value
      ? await api.put(`/api/admin/keys/${editingKey.value}`, payload)
      : await api.post('/api/admin/keys', payload)
    keyModal.value = false
    if (!editingKey.value) {
      const rawKey = result.key || result.api_key || result.secret || ''
      createdKeyForImport.value = {
        id: Number(result.id || 0), name: keyForm.name, enabled: true,
        protocols: [...keyForm.protocols], models: [...payload.models], group_id: Number(keyForm.group_id), created_at: new Date().toISOString(),
      }
      revealedKey.value = rawKey
    }
    notify(editingKey.value ? '客户端密钥已更新' : '客户端密钥已创建')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

function preferredCCSwitchApp(item: ClientKey): CCSwitchApp {
  return item.protocols?.includes('messages') ? 'claude' : 'codex'
}

async function copyClientKey(item: ClientKey) {
  keySecretController?.abort()
  const controller = new AbortController()
  keySecretController = controller
  try {
    const rawKey = await resolveClientKey(item, controller.signal)
    if (keySecretController !== controller) return
    const copied = await copyValue(rawKey, '密钥')
    if (controller.signal.aborted || keySecretController !== controller) return
    if (!copied) {
      createdKeyForImport.value = item
      revealedKey.value = rawKey
      notify('浏览器未授权自动复制，请在弹窗中点击复制按钮', true)
    }
  } catch (error) {
    if (!isAbortError(error)) notify(errorMessage(error), true)
  } finally {
    if (keySecretController === controller) keySecretController = null
  }
}

function openKeySimulation(item: ClientKey) {
  keySimulationTarget.value = item
  keySimulationProtocol.value = item.protocols?.[0] || 'responses'
  keySimulationModel.value = item.models?.[0] || ''
  keySimulationResult.value = null
  keySimulationModal.value = true
}

function closeKeySimulation() {
  keySimulationModal.value = false
  keySimulationTarget.value = null
  keySimulationResult.value = null
  keySimulationBusy.value = false
}

async function runKeySimulation() {
  const target = keySimulationTarget.value
  const model = keySimulationModel.value.trim()
  if (!target || !model) return notify('请选择或填写模型', true)
  keySimulationBusy.value = true
  keySimulationResult.value = null
  const started = performance.now()
  try {
    const secret = await resolveClientKey(target)
    const protocol = keySimulationProtocol.value
    const endpoint = protocol === 'chat' ? '/v1/chat/completions' : protocol === 'messages' ? '/v1/messages' : '/v1/responses'
    const headers: Record<string, string> = { 'Content-Type': 'application/json', Authorization: `Bearer ${secret}` }
    if (protocol === 'messages') {
      headers['x-api-key'] = secret
      headers['anthropic-version'] = '2023-06-01'
      delete headers.Authorization
    }
    const body = protocol === 'messages'
      ? { model, max_tokens: 8, messages: [{ role: 'user', content: 'ping' }] }
      : protocol === 'chat'
        ? { model, max_tokens: 8, messages: [{ role: 'user', content: 'ping' }] }
        : { model, max_output_tokens: 8, input: 'ping' }
    const response = await fetch(endpoint, { method: 'POST', headers, body: JSON.stringify(body), cache: 'no-store' })
    const text = await response.text()
    let detail = ''
    try { detail = String((JSON.parse(text) as Json)?.error?.message || '') } catch { detail = text.slice(0, 180) }
    keySimulationResult.value = { ok: response.ok, status: response.status, duration_ms: performance.now() - started, detail }
  } catch (error) {
    keySimulationResult.value = { ok: false, status: 0, duration_ms: performance.now() - started, detail: errorMessage(error) }
  } finally { keySimulationBusy.value = false }
}

async function openCCSwitch(item: ClientKey) {
  const sequence = ++ccswitchOpenSequence
  keySecretController?.abort()
  const controller = new AbortController()
  keySecretController = controller
  ccswitchTarget.value = item
  ccswitchApp.value = preferredCCSwitchApp(item)
  ccswitchName.value = item.name || 'D-API Gateway'
  ccswitchModel.value = ''
  ccswitchModels.value = []
  try {
    ccswitchSecret.value = await resolveClientKey(item, controller.signal)
    if (keySecretController !== controller) return
  } catch (error) {
    if (sequence !== ccswitchOpenSequence) return
    if (isAbortError(error)) return
    ccswitchSecret.value = ''
    notify(errorMessage(error), true)
  } finally {
    if (keySecretController === controller) keySecretController = null
  }
  if (sequence !== ccswitchOpenSequence) return
  ccswitchModal.value = true
}

function changeCCSwitchApp(event: Event) {
  const next = (event.target as HTMLSelectElement).value as CCSwitchApp
  ccswitchApp.value = next
  ccswitchModel.value = ''
  ccswitchModels.value = []
}

function closeCCSwitch() {
  ccswitchOpenSequence++
  keySecretController?.abort()
  keySecretController = null
  ccswitchModelsController?.abort()
  ccswitchModelsController = null
  ccswitchModal.value = false
  ccswitchTarget.value = null
  ccswitchName.value = ''
  ccswitchModel.value = ''
  ccswitchSecret.value = ''
  ccswitchModels.value = []
  fetchingCCSwitchModels.value = false
}

function openCCSwitchFromSecret() {
  if (!createdKeyForImport.value || !revealedKey.value) return
  const item = createdKeyForImport.value
  ccswitchTarget.value = item
  ccswitchApp.value = preferredCCSwitchApp(item)
  ccswitchName.value = item.name || 'D-API Gateway'
  ccswitchModel.value = ''
  ccswitchModels.value = []
  ccswitchSecret.value = revealedKey.value
  ccswitchModal.value = true
}

function buildCCSwitchURL() {
  const endpoint = ccswitchApp.value === 'codex' ? `${gatewayBaseURL.value}/v1` : gatewayBaseURL.value
  const params = new URLSearchParams({
    resource: 'provider',
    app: ccswitchApp.value,
    name: ccswitchName.value.trim() || ccswitchTarget.value?.name || 'D-API Gateway',
    homepage: gatewayBaseURL.value,
    endpoint,
    apiKey: ccswitchSecret.value.trim(),
    enabled: 'true',
  })
  if (ccswitchModel.value.trim()) params.set('model', ccswitchModel.value.trim())
  return `ccswitch://v1/import?${params.toString()}`
}

async function fetchCCSwitchModels() {
  if (!ccswitchSecret.value.trim()) return notify('请先提供客户端密钥', true)
  ccswitchModelsController?.abort()
  const controller = new AbortController()
  ccswitchModelsController = controller
  fetchingCCSwitchModels.value = true
  try {
    const body = await api.get<Json>('/v1/models', {
      cache: 'no-store',
      headers: { Authorization: `Bearer ${ccswitchSecret.value.trim()}` },
      signal: controller.signal,
    })
    if (ccswitchModelsController !== controller) return
    const models: string[] = []
    for (const item of (Array.isArray(body?.data) ? body.data : [])) {
      const model = String(typeof item === 'string' ? item : item?.id || '').trim()
      if (model && !models.includes(model)) models.push(model)
    }
    ccswitchModels.value = Array.from(new Set(models)).sort((a, b) => a.localeCompare(b))
    notify(ccswitchModels.value.length ? `已获取 ${ccswitchModels.value.length} 个模型` : '网关暂无可用模型', !ccswitchModels.value.length)
  } catch (error) {
    if (!isAbortError(error)) notify(errorMessage(error), true)
  } finally {
    if (ccswitchModelsController === controller) {
      ccswitchModelsController = null
      fetchingCCSwitchModels.value = false
    }
  }
}

function importToCCSwitch() {
  if (!ccswitchTarget.value) return
  if (!ccswitchSecret.value.trim()) return notify('请粘贴该客户端密钥的完整值后再导入', true)
  try {
    window.open(buildCCSwitchURL(), '_blank', 'noopener,noreferrer')
    closeCCSwitch()
    closeSecret()
  } catch {
    notify('无法打开 CCSwitch，请确认已安装并注册协议处理程序', true)
  }
}

async function removeKey(item: ClientKey) {
  if (!(await requestConfirmation('删除客户端密钥', `“${item.name}”删除后，使用该密钥的请求将立即失败。`))) return
  try { await api.delete(`/api/admin/keys/${item.id}`); notify('客户端密钥已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

async function copySecret() {
  if (!(await copyValue(revealedKey.value, '密钥'))) {
    const input = document.querySelector<HTMLInputElement>('.secret-mask')
    input?.focus()
    input?.select()
  }
}

function closeSecret() {
  keySecretController?.abort()
  keySecretController = null
  revealedKey.value = ''
  createdKeyForImport.value = null
}

async function copyClientConfig() {
  if (!revealedKey.value) return
  const config = [
    '# D-API OpenAI-compatible endpoint',
    'export OPENAI_BASE_URL=' + gatewayBaseURL.value + '/v1',
    'export OPENAI_API_KEY=' + revealedKey.value,
    '',
    '# D-API Anthropic-compatible endpoint',
    'export ANTHROPIC_BASE_URL=' + gatewayBaseURL.value,
    'export ANTHROPIC_API_KEY=' + revealedKey.value,
  ].join('\n')
  await copyValue(config, '接入配置')
}

async function saveChannel() {
  saving.value = true
  try {
    const config = channelForm.kind === 'webhook'
      ? { url: channelForm.target, provider: channelForm.provider || undefined }
      : { to: channelForm.target, smtp_host: channelForm.smtp_host, smtp_port: channelForm.smtp_port, username: channelForm.username, password: channelForm.password }
    await api.post('/api/admin/channels', { name: channelForm.name, kind: channelForm.kind, enabled: channelForm.enabled, config })
    channelModal.value = false
    notify('通知渠道已添加')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function testChannel(item: Channel) {
  if (item.kind !== 'webhook' || channelTestID.value !== null) return
  channelTestID.value = item.id
  try {
    const result = await api.post<{ ok?: boolean; message?: string }>(`/api/admin/channels/${item.id}/test`)
    notify(result?.message || 'Webhook 测试成功')
  } catch (error) {
    notify(errorMessage(error), true)
  } finally {
    channelTestID.value = null
  }
}

async function removeChannel(item: Channel) {
  if (!(await requestConfirmation('删除通知渠道', `“${item.name}”将不再接收新的告警通知。`))) return
  try { await api.delete(`/api/admin/channels/${item.id}`); notify('通知渠道已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

async function saveSettings() {
  try { await api.put('/api/admin/settings', { max_attempts: maxAttempts.value }); notify('路由设置已保存') }
  catch (error) { notify(errorMessage(error), true) }
}

async function saveRule(rule: AlertRule) {
  try {
    await api.put(`/api/admin/alert-rules/${rule.id}`, {
      threshold: Number(rule.threshold), window_seconds: Number(rule.window_seconds),
      cooldown_seconds: Number(rule.cooldown_seconds), max_notifications: Number(rule.max_notifications), enabled: rule.enabled,
    })
    notify('告警规则已保存')
  } catch (error) { notify(errorMessage(error), true) }
}

async function addRule() {
  if (!newRule.upstream_id) return notify('请选择上游', true)
  try {
    await api.post('/api/admin/alert-rules', {
      event: newRule.event, upstream_id: Number(newRule.upstream_id), threshold: Number(newRule.threshold),
      window_seconds: Number(newRule.window_seconds), cooldown_seconds: Number(newRule.cooldown_seconds), enabled: true,
      max_notifications: Number(newRule.max_notifications),
    })
    notify('上游告警覆盖已添加')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) }
}

async function removeRule(rule: AlertRule) {
  if (!rule.upstream_id || !(await requestConfirmation('删除告警覆盖', `将恢复“${upstreamName(rule.upstream_id)}”对应事件的全局默认规则。`))) return
  try { await api.delete(`/api/admin/alert-rules/${rule.id}`); notify('告警覆盖已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

function inspectLogUpstream(item: RequestLog) {
  const upstream = item.upstream_id ? upstreams.value.find((entry) => entry.id === item.upstream_id) : undefined
  if (upstream) openUpstream(upstream)
  else notify('该请求没有可打开的上游配置', true)
}

function inspectLogAction(item: RequestLog, action: 'check' | 'balance') {
  const upstream = item.upstream_id ? upstreams.value.find((entry) => entry.id === item.upstream_id) : undefined
  if (upstream) void upstreamAction(upstream, action)
  else notify('该请求没有可操作的上游配置', true)
}

function alertEventText(event: string) {
  return ({ low_balance: '余额过低', balance_unavailable: '余额查询失败', error_rate: '上游错误率', latency: '上游延迟', client_error_rate: '客户端错误率', login_failure: '登录失败', new_login_ip: '新 IP 登录' } as Record<string, string>)[event] || event
}

function upstreamName(id?: number) {
  return id ? upstreams.value.find((item) => item.id === id)?.name || `#${id}` : '全局默认'
}

function statusTone(status?: string) {
  if (['healthy', 'ok', 'active', 'success'].includes(status || '')) return 'good'
  if (['unhealthy', 'failed', 'open', 'error'].includes(status || '')) return 'bad'
  return 'warn'
}

function statusText(status?: string) {
  return ({ healthy: '正常', unhealthy: '异常', unknown: '未知', open: '熔断', checking: '检查中' } as Record<string, string>)[status || ''] || status || '未知'
}

function fmtDate(value?: string) {
  return value ? new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value)) : '从未'
}

function fmtNumber(value?: number) {
  const number = Number(value || 0)
  const locale = Math.abs(number) > 99999 ? 'en-US' : 'zh-CN'
  return new Intl.NumberFormat(locale, { notation: Math.abs(number) > 99999 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(number)
}

function fmtMetric(value?: number | null, suffix = '') {
  return value == null || Number.isNaN(Number(value)) ? '—' : `${fmtNumber(Number(value))}${suffix}`
}

function fmtDuration(value?: number | null) {
  if (value == null || Number.isNaN(Number(value))) return '—'
  return `${(Number(value) / 1000).toFixed(2)} s`
}

function fmtCurrency(value: number, currency: 'USD' | 'CNY') {
  return new Intl.NumberFormat('zh-CN', { style: 'currency', currency, minimumFractionDigits: currency === 'USD' && Math.abs(value) < 1 ? 4 : 2, maximumFractionDigits: currency === 'USD' && Math.abs(value) < 1 ? 6 : 2 }).format(Number(value || 0))
}

function fmtPercent(value?: number | null) {
  if (value == null || Number.isNaN(Number(value))) return '—'
  const number = Number(value)
  return `${(number <= 1 ? number * 100 : number).toFixed(1)}%`
}

function logTokenHitRate(item: RequestLog) {
  const usage = item.usage
  if (!usage || (usage.cached_input_tokens == null && usage.input_tokens == null && usage.cache_creation_input_tokens == null)) return null
	const uncached = usage.uncached_input_tokens ?? (usage.input_tokens == null ? null : item.protocol === 'messages'
		? Number(usage.input_tokens) + Number(usage.cache_creation_input_tokens || 0)
		: Number(usage.input_tokens) - Number(usage.cached_input_tokens || 0))
  const cached = Number(usage.cached_input_tokens || 0)
  const denominator = cached + Number(uncached || 0)
  return denominator > 0 ? cached / denominator : null
}

function usageDimensionLabel(row: Json) {
  return String(row.dimension_label || row.label || row.upstream_name || row.api_key_name || row.group_name || row.protocol || row.model || '总计')
}

function usageRowKey(row: Json, index: number) {
  return `${row.day || row.date || index}-${usageDimensionLabel(row)}`
}

function fmtBalance(upstream: Upstream) {
  const balance = upstream.balance
  if (!balance || balance.status === 'unsupported') return '不支持'
  if (balance.unlimited) return '无限额'
  if (balance.available == null) return '未知'
  return `${balance.currency || '$'} ${balance.available.toFixed(2)}`
}

function fmtBalanceUsed(upstream: Upstream) {
  const balance = upstream.balance
  return balance?.used == null ? '暂无数据' : `${balance.currency || '$'} ${balance.used.toFixed(2)}`
}

function balanceUsedUpdatedAt(upstream: Upstream) {
  return upstream.balance?.last_success_at || upstream.balance?.updated_at
}

function officialCostDetail(upstream: Upstream, period: 'today' | 'lifetime') {
  const cost = Number(period === 'today' ? upstream.today_cost_usd : upstream.lifetime_cost_usd || 0)
  const coverage = period === 'today' ? upstream.today_cost_coverage : upstream.lifetime_cost_coverage
  return `${fmtCurrency(cost, 'USD')} / ${fmtCurrency(cost * Number(pricing.value.usd_cny_rate || 7.2), 'CNY')} · 覆盖 ${fmtPercent(coverage)}`
}

onMounted(async () => {
  colorScheme.addEventListener('change', applyTheme)
  window.addEventListener('keydown', handleGlobalKeydown)
  window.addEventListener('popstate', handleHistoryNavigation)
  window.addEventListener('hashchange', handleHistoryNavigation)
  try {
    admin.value = await api.get('/api/admin/me')
    auth.value = 'ready'
    view.value = viewFromLocation()
    if (!location.hash) history.replaceState(null, '', '#dashboard')
    await loadCurrent()
  } catch { auth.value = 'guest' }
})

onBeforeUnmount(() => {
  if (activeModelTestRun) stopModelTests()
  singleModelTestController?.abort()
  pageLoadController?.abort()
  pageLoadController = null
  dashboardRangeController?.abort()
  dashboardRangeController = null
  ccswitchModelsController?.abort()
  ccswitchModelsController = null
  window.clearTimeout(toastTimer)
  if (topologyPulseTimer !== null) window.clearTimeout(topologyPulseTimer)
  topologyPulseTimer = null
  if (autoRefreshTimer !== null) window.clearInterval(autoRefreshTimer)
  autoRefreshTimer = null
  colorScheme.removeEventListener('change', applyTheme)
  window.removeEventListener('keydown', handleGlobalKeydown)
  window.removeEventListener('popstate', handleHistoryNavigation)
  window.removeEventListener('hashchange', handleHistoryNavigation)
})
</script>

<template>
  <div v-if="auth === 'checking'" class="boot" aria-label="正在加载">
    <div class="brand-mark"><Network :size="22" /></div><LoaderCircle class="spin" :size="22" />
  </div>

  <main v-else-if="auth === 'guest'" class="login-shell">
    <section class="login-panel">
      <div class="login-brand"><span class="brand-mark"><Network :size="22" /></span><strong>D-API</strong></div>
      <div>
        <h1>登录</h1>
        <p class="muted">管理上游路由、用量和运行状态。</p>
      </div>
      <form class="form-stack" @submit.prevent="login">
        <label>管理员账号<input v-model.trim="loginForm.username" autocomplete="username" required autofocus /></label>
        <label>密码<input v-model="loginForm.password" type="password" autocomplete="current-password" required /></label>
        <p v-if="loginError" class="form-error" role="alert"><AlertCircle :size="16" />{{ loginError }}</p>
        <button class="primary wide" :disabled="loginBusy">
          <LoaderCircle v-if="loginBusy" class="spin" :size="17" /><ShieldCheck v-else :size="17" />进入控制台
        </button>
      </form>
    </section>
    <aside class="login-aside" aria-hidden="true">
      <div class="login-wordmark"><span>D</span><span>API</span></div>
      <p>路由状态，一眼清楚。</p>
      <div class="route-line"><i></i><i></i><i></i></div>
    </aside>
  </main>

  <div v-else class="app-shell" :class="{ 'sidebar-collapsed': sidebarCollapsed }" @click="closeRowMenu(false)">
    <aside class="sidebar" :class="{ open: menuOpen, collapsed: sidebarCollapsed }">
      <div class="sidebar-head"><span class="brand-mark"><Network :size="20" /></span><strong>D-API</strong><button class="icon mobile-close" title="关闭菜单" @click="menuOpen = false"><X :size="19" /></button></div>
      <nav aria-label="主导航">
        <button v-for="item in navItems" :key="item.id" :class="{ active: view === item.id }" :title="sidebarCollapsed ? item.label : undefined" @click="go(item.id)">
          <component :is="item.icon" :size="18" />{{ item.label }}
        </button>
      </nav>
      <div class="sidebar-foot">
        <div><span class="avatar">{{ String(admin.username || 'A').slice(0, 1).toUpperCase() }}</span><span><strong>{{ admin.username || '管理员' }}</strong><small>管理员</small></span></div>
        <button class="icon" title="修改密码" @click="passwordModal = true"><ShieldCheck :size="18" /></button>
        <button class="icon" title="退出登录" @click="logout"><LogOut :size="18" /></button>
      </div>
      <button class="sidebar-toggle" :title="sidebarCollapsed ? '展开侧栏' : '收起侧栏'" @click="toggleSidebar">
        <PanelLeftOpen v-if="sidebarCollapsed" :size="15" /><PanelLeftClose v-else :size="15" />
      </button>
    </aside>
    <button v-if="menuOpen" class="menu-backdrop" aria-label="关闭菜单" @click="menuOpen = false"></button>

    <main class="workspace">
      <header class="topbar">
        <button class="icon menu-button" title="打开菜单" @click="menuOpen = true"><Menu :size="20" /></button>
        <h1>{{ title }}</h1>
        <div class="topbar-actions">
          <div class="theme-control" role="group" aria-label="主题">
            <button v-for="option in themeOptions" :key="option.id" class="icon" :class="{ active: theme === option.id }" :title="option.label" :aria-pressed="theme === option.id" @click="setTheme(option.id)">
              <component :is="option.icon" :size="16" />
            </button>
          </div>
          <span class="refresh-meta" :class="{ stale: !lastUpdatedAt || refreshError }" :title="refreshError || undefined">{{ refreshError || fmtUpdatedAt(lastUpdatedAt) }}</span>
          <label class="auto-refresh-control" title="自动刷新（30 秒）">
            <input v-model="autoRefresh" type="checkbox" />
            <span>自动刷新</span>
          </label>
          <button class="icon refresh" title="刷新当前页面" :disabled="loading" @click="loadCurrent"><RefreshCw :class="{ spin: loading }" :size="17" /></button>
        </div>
        <div v-if="loading" class="loading-line" aria-hidden="true"></div>
      </header>

      <div class="content" :aria-busy="loading">
        <section v-if="view === 'dashboard'" class="view-stack">
          <section v-if="!setupComplete" class="setup-panel panel">
            <div class="setup-panel-head"><div><h2>完成首次配置</h2><p>按顺序完成以下步骤，快速验证网关可以正常路由。</p></div><span class="setup-progress">{{ setupSteps.filter((step) => step.done).length }}/{{ setupSteps.length }}</span></div>
            <div class="setup-steps">
              <button v-for="step in setupSteps" :key="step.id" class="setup-step" :class="{ done: step.done }" @click="go(step.view)">
                <span class="setup-step-icon"><Check v-if="step.done" :size="15" /><span v-else>{{ setupSteps.indexOf(step) + 1 }}</span></span>
                <span><strong>{{ step.label }}</strong><small>{{ step.description }}</small></span><ChevronRight :size="15" />
              </button>
            </div>
          </section>
          <div class="dashboard-metric-grid dashboard-health-grid">
            <article class="dashboard-metric"><span class="metric-icon green"><Server :size="19" /></span><div><small>可用上游</small><strong>{{ summary.healthy }}<em>/ {{ summary.total }}</em></strong><span class="metric-detail">{{ dashboard.active_alerts || 0 }} 个活跃告警</span></div></article>
            <article class="dashboard-metric"><span class="metric-icon ink"><Activity :size="19" /></span><div><small>24 小时请求</small><strong>{{ fmtNumber(summary.requests) }}</strong><span class="metric-detail">{{ fmtNumber(dashboard.usage_requests_24h) }} 条含 Token</span></div></article>
            <article class="dashboard-metric"><span class="metric-icon blue"><Check :size="19" /></span><div><small>成功率</small><strong>{{ summary.success.toFixed(1) }}<em>%</em></strong><span class="metric-detail">按 HTTP 2xx-3xx 统计</span></div></article>
            <article class="dashboard-metric"><span class="metric-icon amber"><Gauge :size="19" /></span><div><small>平均延迟</small><strong>{{ fmtDuration(summary.latency) }}</strong><span class="metric-detail">24 小时请求均值</span></div></article>
            <template v-if="dashboardMetricsExpanded">
              <article class="dashboard-metric"><span class="metric-icon green"><ChevronRight :size="19" /></span><div><small>输入 / 输出 Token</small><strong>{{ fmtNumber(dashboard.input_tokens_24h) }} <em>/ {{ fmtNumber(dashboard.output_tokens_24h) }}</em></strong><span class="metric-detail">缓存读 {{ fmtNumber(dashboard.cached_input_tokens_24h) }}</span></div></article>
              <article class="dashboard-metric"><span class="metric-icon blue"><ChartNoAxesCombined :size="19" /></span><div><small>Token 命中率</small><strong>{{ fmtPercent(dashboardCacheHitRate) }}</strong><span class="metric-detail">缓存读 / 输入 Token</span></div></article>
              <article class="dashboard-metric"><span class="metric-icon amber"><Network :size="19" /></span><div><small>请求命中率</small><strong>{{ fmtPercent(dashboardRequestHitRate) }}</strong><span class="metric-detail">{{ fmtNumber(dashboard.cache_hit_requests_24h) }} 次命中</span></div></article>
              <article class="dashboard-metric cost-metric"><span class="metric-icon green"><CircleDollarSign :size="19" /></span><div><small>官方价格估算 · 24 小时</small><strong>{{ fmtCurrency(dashboardCost, 'USD') }}</strong><span class="metric-detail">约 {{ fmtCurrency(dashboardCostCNY, 'CNY') }} · 覆盖 {{ fmtPercent(dashboardCostCoverage) }}</span></div></article>
            </template>
          </div>
          <button class="metrics-toggle text-button" :aria-expanded="dashboardMetricsExpanded" @click="toggleDashboardMetrics">{{ dashboardMetricsExpanded ? '收起详细指标' : '显示 Token、缓存与成本指标' }}<ChevronRight :class="{ rotate: dashboardMetricsExpanded }" :size="15" /></button>
          <section class="panel usage-panel dashboard-usage-panel dashboard-trend-panel">
            <div class="panel-head dashboard-toolbar">
              <div><h2>用量趋势</h2><p>{{ dashboardRange === '24h' ? '按浏览器本地时区显示最近 24 小时' : `${dashboardRange === '7d' ? '近 7 天' : '近 30 天'} · 日粒度` }}</p></div>
              <div class="dashboard-controls">
                <div class="segmented-control" aria-label="时间范围"><button v-for="item in [{id:'24h',label:'24 小时'},{id:'7d',label:'7 天'},{id:'30d',label:'30 天'}]" :key="item.id" :class="{ active: dashboardRange === item.id }" @click="dashboardRange = item.id; loadDashboardRange()">{{ item.label }}</button></div><label class="compare-control"><input type="checkbox" :checked="dashboardCompare" :disabled="dashboardRange === '24h' || dashboardComparisonLoading" @change="toggleDashboardCompare" />对比上周期</label>
                <div class="segmented-control" aria-label="趋势指标"><button v-for="item in [{id:'requests',label:'请求'},{id:'tokens',label:'Token'},{id:'cache',label:'缓存'},{id:'cost',label:'成本'}]" :key="item.id" :class="{ active: dashboardMetric === item.id }" @click="dashboardMetric = item.id">{{ item.label }}</button></div>
              </div>
            </div>
            <div v-if="dashboardCompare" class="comparison-summary" :class="{ pending: dashboardComparisonLoading }"><span>{{ dashboardComparisonLoading ? '正在读取上周期数据…' : dashboardComparisonReady ? '环比上周期' : '选择 7 天或 30 天后可用' }}</span><template v-if="dashboardComparisonReady"><strong :class="dashboardDeltaTone('requests')">请求 {{ dashboardDeltaText('requests') }}</strong><strong :class="dashboardDeltaTone('tokens')">Token {{ dashboardDeltaText('tokens') }}</strong><strong :class="dashboardDeltaTone('cost')">成本 {{ dashboardDeltaText('cost') }}</strong></template></div>
            <div class="chart-summary dashboard-chart-summary" aria-label="当前趋势汇总">
              <span><i class="bar"></i><small>请求</small><strong>{{ fmtNumber(dashboardTrendTotals.requests) }}</strong></span><span><i class="line"></i><small>Token</small><strong>{{ fmtNumber(dashboardTrendTotals.tokens) }}</strong></span><span><i class="cache-dot"></i><small>缓存读</small><strong>{{ fmtNumber(dashboardTrendTotals.cached) }}</strong></span><span><i class="cost-dot"></i><small>成本 USD</small><strong>{{ fmtCurrency(dashboardTrendTotals.cost, 'USD') }}</strong></span>
            </div>
            <div v-if="dashboardChartRows.length" class="chart-frame dashboard-chart-frame"><UsageChart :rows="dashboardChartRows" :theme="resolvedTheme" :metric="dashboardMetric" :range-label="dashboardRange === '24h' ? '近 24 小时' : dashboardRange === '7d' ? '近 7 天' : '近 30 天'" /></div>
            <div v-else class="empty"><ChartNoAxesCombined :size="22" /><strong>暂无使用数据</strong><span>产生请求后显示趋势。</span></div>
          </section>
          <div class="dashboard-detail-grid dashboard-supporting-grid">
            <section class="panel cost-breakdown"><div class="panel-head"><div><h2>成本明细</h2><p>近 30 天 · 模型与 Token 类型</p></div><span class="muted">{{ fmtPercent(dashboardCostCoverage30d) }} 已计价 · 月度预测 {{ fmtCurrency(dashboardCostForecast30d, 'USD') }}</span></div><div class="table-wrap"><table><thead><tr><th>模型</th><th>请求</th><th>覆盖率</th><th>Token 构成</th><th class="right">USD</th><th class="right">CNY</th></tr></thead><tbody><tr v-for="row in dashboardCostByModel" :key="row.model"><td><strong>{{ row.model }}</strong></td><td>{{ fmtNumber(row.requests) }}</td><td>{{ fmtPercent(row.requests ? row.known / row.requests : null) }}</td><td><small class="token-breakdown">入 {{ fmtNumber(row.input) }} · 出 {{ fmtNumber(row.output) }}<br />读 {{ fmtNumber(row.cached) }} · 写 {{ fmtNumber(row.cacheWrite) }}</small></td><td class="right"><strong>{{ fmtCurrency(row.cost_usd, 'USD') }}</strong></td><td class="right">{{ fmtCurrency(row.cost_usd * Number(pricing.usd_cny_rate || 7.2), 'CNY') }}</td></tr><tr v-if="!dashboardCostByModel.length"><td colspan="6"><div class="empty"><CircleDollarSign :size="20" /><strong>暂无可计价成本</strong><span>绑定价格档案并产生带 Token 的请求后显示。</span></div></td></tr></tbody></table></div></section>
            <section class="panel pricing-snapshot"><div class="panel-head"><div><h2>价格档案</h2><p>LiteLLM 自动价格 · 手动档案兜底 · USD/CNY {{ Number(pricing.usd_cny_rate || 7.2).toFixed(2) }}</p></div><div class="row-actions"><button class="secondary" :disabled="saving" @click="backfillPricing"><RefreshCw :class="{ spin: saving }" :size="15" />回算成本</button><button class="secondary" @click="openPricingProfile()"><Plus :size="15" />新建档案</button><button class="icon" title="同步 LiteLLM 价格" :disabled="saving" @click="refreshPricing"><RefreshCw :class="{ spin: saving }" :size="16" /></button></div></div><div class="snapshot-list"><div v-for="profile in listOf<Json>(pricing.profiles)" :key="profile.id" class="snapshot-row"><div><strong>{{ profile.name }}</strong><small>{{ profile.prices?.length || 0 }} 个模型 · {{ profile.source_version || '内置快照' }}</small></div><span class="muted">{{ profile.last_refreshed_at ? fmtDate(profile.last_refreshed_at) : '待同步' }}</span><div class="row-actions"><button class="icon" title="编辑价格档案" @click="openPricingProfile(profile)"><Pencil :size="15" /></button><button class="icon danger" title="删除价格档案" @click="removePricingProfile(profile)"><Trash2 :size="15" /></button></div></div><div v-if="!listOf<Json>(pricing.profiles).length" class="empty"><CircleDollarSign :size="20" /><strong>暂无价格档案</strong></div></div></section>
          </div>
          <section class="panel dashboard-upstream-panel">
            <div class="panel-head dashboard-route-head">
              <div><h2>上游状态</h2><p>{{ dashboardRouteView === 'topology' ? '按 baseurl 聚合的路由拓扑与决策路径' : '当前路由顺序与连接状态' }}</p></div>
              <div class="dashboard-route-actions">
                <div class="segmented-control" aria-label="上游视图">
                  <button :class="{ active: dashboardRouteView === 'current' }" :aria-pressed="dashboardRouteView === 'current'" @click="dashboardRouteView = 'current'">列表</button>
                  <button :class="{ active: dashboardRouteView === 'topology' }" :aria-pressed="dashboardRouteView === 'topology'" @click="dashboardRouteView = 'topology'">拓扑</button>
                </div>
                <button class="text-button" @click="go('upstreams')">查看全部 <ChevronRight :size="15" /></button>
              </div>
            </div>
            <div v-if="dashboardRouteView === 'current'" class="table-wrap">
              <table class="dashboard-upstream-table">
                <thead><tr><th>优先级</th><th>上游</th><th>状态</th><th>今日用量</th><th>账号扣费总额</th><th>官方价格估算</th><th>余额</th><th>协议</th><th>最后检查</th></tr></thead>
                <tbody>
                  <tr v-for="item in shownDashUpstreams" :key="item.key" class="clickable" tabindex="0" @click="openUpstreamGroup(item)" @keydown.enter.prevent="openUpstreamGroup(item)">
                    <td><span class="priority">{{ item.priority }}</span></td>
                    <td><strong>{{ item.base_url }}</strong><small>{{ item.total }} 个 Key · {{ item.available }} 个可路由</small></td>
                    <td><span class="status" :class="groupStatusTone(item)"><i></i>{{ groupStatusText(item) }}</span></td>
                  <td><strong class="usage-value">{{ fmtNumber(item.today_tokens) }} Token</strong><small>{{ fmtNumber(item.today_requests) }} 次请求</small></td>
                    <td><strong>{{ item.total > 1 ? '多账号' : fmtBalanceUsed(item.items[0]) }}</strong><small>{{ item.total > 1 ? '展开后按 Key 查看' : fmtDate(balanceUsedUpdatedAt(item.items[0])) }}</small></td>
                    <td><strong>{{ item.total > 1 ? '多账号' : officialCostDetail(item.items[0], 'today') }}</strong><small>{{ item.total > 1 ? '不聚合不同账号' : `历史 ${officialCostDetail(item.items[0], 'lifetime')}` }}</small></td>
                    <td><strong class="balance">{{ item.total > 1 ? '多 Key' : fmtBalance(item.items[0]) }}</strong><small v-if="item.total > 1">展开查看余额</small></td>
                    <td><span class="tag" v-for="protocol in item.protocols" :key="protocol">{{ protocol }}</span></td>
                    <td class="muted nowrap">{{ fmtDate(item.last_check_at) }}</td>
                  </tr>
              <tr v-if="!shownDashUpstreams.length"><td colspan="9"><div class="empty"><Server :size="22" /><strong>暂无上游</strong><span>创建路由目标后，这里会显示健康与余额状态。</span><button class="secondary" @click="openUpstream()"><Plus :size="15" />添加上游</button></div></td></tr>
                </tbody>
              </table>
            </div>
            <div v-else class="dashboard-route-topology" :class="{ 'is-empty': !dashboardTopologyHasData, 'topology-live': topologyPulseActive && summary.requests > 0, 'topology-status-good': summary.success >= 99, 'topology-status-warn': summary.success >= 95 && summary.success < 99, 'topology-status-bad': summary.success < 95 }">
              <div v-if="dashboardTopologyHasData" class="topology-flow">
                <section class="topology-column topology-source" aria-label="客户端密钥入口">
                  <div class="topology-column-label"><KeyRound :size="14" /><span>客户端密钥</span><small>{{ keys.length }} 个</small></div>
                  <div v-if="dashboardTopologyKeys.length" class="topology-node-list">
                    <article v-for="item in dashboardTopologyKeys" :key="item.id" class="topology-node key-node" :class="topologyNodeState('key', item.id)" role="button" :aria-label="`聚焦客户端密钥 ${item.name || item.prefix || item.key_prefix || `密钥 #${item.id}`}`" tabindex="0" @click="focusTopologyKey(item)" @keydown.enter.prevent="focusTopologyKey(item)" @keydown.space.prevent="focusTopologyKey(item)">
                      <span class="topology-node-mark"><KeyRound :size="13" /></span>
                      <div><strong>{{ item.name || item.prefix || item.key_prefix || `密钥 #${item.id}` }}</strong><small>{{ item.group_name || '未分组' }}</small></div>
                      <span class="status" :class="item.enabled ? 'good' : 'warn'"><i></i>{{ item.enabled ? '启用' : '停用' }}</span>
                    </article>
                  </div>
                  <div v-else class="topology-empty-node"><KeyRound :size="17" /><span>暂无客户端密钥</span></div>
                  <span v-if="keys.length > dashboardTopologyKeys.length" class="topology-more">+ {{ keys.length - dashboardTopologyKeys.length }} 个未展开</span>
                </section>

                <div class="topology-bridge" aria-hidden="true"><span class="topology-bridge-line"></span><i class="topology-pulse"></i><small>匹配分组</small></div>

                <section class="topology-column topology-decision" aria-label="分组策略">
                  <div class="topology-column-label"><Network :size="14" /><span>分组决策</span><small>{{ groups.length }} 个</small></div>
                  <div v-if="dashboardTopologyGroups.length" class="topology-node-list">
                    <article v-for="item in dashboardTopologyGroups" :key="item.id" class="topology-node group-node" :class="topologyNodeState('group', item.id)" role="button" :aria-label="`聚焦分组 ${item.name}`" tabindex="0" @click="focusTopologyGroup(item)" @keydown.enter.prevent="focusTopologyGroup(item)" @keydown.space.prevent="focusTopologyGroup(item)">
                      <div class="topology-node-heading"><strong>{{ item.name }}</strong><span class="topology-strategy">按优先级</span></div>
                      <div class="topology-group-meta"><span>{{ item.key_count || 0 }} 个密钥</span><span>{{ item.upstream_ids?.length || 0 }} 条线路</span><span v-if="topologyGroupPriorities(item).length">优先级 {{ topologyGroupPriorities(item).map((priority) => `P${priority}`).join(' / ') }}</span><span v-else>待配置</span></div>
                      <div class="topology-group-keys"><span v-for="key in topologyKeysForGroup(item).slice(0, 3)" :key="key.id" class="topology-client-key"><KeyRound :size="10" />{{ key.name || key.prefix || key.key_prefix || `密钥 #${key.id}` }}</span><span v-if="topologyKeysForGroup(item).length > 3" class="topology-key-overflow">+{{ topologyKeysForGroup(item).length - 3 }}</span><span v-if="!topologyKeysForGroup(item).length" class="muted">暂无客户端密钥</span></div>
                      <div class="topology-group-line"><span :style="{ transform: `scaleX(${Math.max(18, Math.min(100, (item.upstream_ids?.length || 0) * 28)) / 100})` }"></span></div>
                    </article>
                  </div>
                  <div v-else class="topology-empty-node"><Network :size="17" /><span>暂无分组策略</span></div>
                  <span v-if="groups.length > dashboardTopologyGroups.length" class="topology-more">+ {{ groups.length - dashboardTopologyGroups.length }} 个未展开</span>
                </section>

                <div class="topology-bridge" aria-hidden="true"><span class="topology-bridge-line"></span><i class="topology-pulse"></i><small>按优先级路由</small></div>

                <section class="topology-column topology-target" aria-label="上游集群">
                  <div class="topology-column-label"><Server :size="14" /><span>上游集群</span><small>{{ upstreamGroups.length }} 个 baseurl</small></div>
                  <div v-if="shownDashUpstreams.length" class="topology-node-list">
                    <article v-for="item in shownDashUpstreams" :key="item.key" class="topology-node upstream-node" :class="[groupStatusTone(item), topologyGroupNodeState(item)]" role="button" :aria-label="`打开上游集群 ${item.base_url}`" tabindex="0" @click="openUpstreamGroup(item)" @keydown.enter.prevent="openUpstreamGroup(item)" @keydown.space.prevent="openUpstreamGroup(item)">
                      <div class="topology-node-heading"><strong :title="item.base_url">{{ item.base_url }}</strong><span class="priority-badge">P{{ item.priority }}</span></div>
                      <div class="topology-upstream-state"><span class="status" :class="groupStatusTone(item)"><i></i>{{ groupStatusText(item) }}</span><span class="topology-circuit" :class="item.circuit_open ? 'bad' : 'muted'">{{ groupCircuitText(item) }}</span></div>
                      <div class="topology-upstream-keys"><div v-for="key in item.items.slice(0, 6)" :key="key.id" class="topology-upstream-key"><span class="topology-key-dot" :class="key.health_status === 'healthy' && !key.balance_suspended ? 'healthy' : key.health_status === 'open' ? 'open' : 'unknown'">{{ (key.name || `#${key.id}`).slice(0, 1).toUpperCase() }}</span><strong :title="key.name">{{ key.name || `上游 Key #${key.id}` }}</strong><span class="topology-key-priority">P{{ key.priority }}</span><span class="status" :class="upstreamRouteTone(key)"><i></i>{{ upstreamRouteText(key) }}</span></div><span v-if="item.total > 6" class="topology-more">+ {{ item.total - 6 }} 个 Key，点击查看全部</span></div>
                      <div class="topology-upstream-meta"><span>{{ item.healthy }}/{{ item.available }} 可用{{ item.balance_suspended ? ` · ${item.balance_suspended} 个余额暂停` : '' }}</span><span>{{ fmtNumber(item.today_requests) }} 次 · {{ fmtNumber(item.today_tokens) }} Token</span></div>
                    </article>
                  </div>
                  <div v-else class="topology-empty-node"><Server :size="17" /><span>暂无上游集群</span></div>
                  <span v-if="upstreamGroups.length > shownDashUpstreams.length" class="topology-more">+ {{ upstreamGroups.length - shownDashUpstreams.length }} 个未展开</span>
                </section>
              </div>
              <div v-else class="empty"><Network :size="22" /><strong>暂无路由拓扑数据</strong><span>创建客户端密钥、分组和上游后，这里会显示完整决策路径。</span></div>
              <div v-if="dashboardTopologyHasData" class="topology-response">
                <span class="topology-response-line" aria-hidden="true"></span>
                <div class="topology-response-node"><span class="topology-response-icon"><Check :size="15" /></span><div><strong>响应出口</strong><small>{{ fmtNumber(summary.requests) }} 请求 · 成功率 {{ summary.success.toFixed(1) }}% · 平均 {{ fmtDuration(summary.latency) }}</small></div><span class="status" :class="summary.success >= 99 ? 'good' : summary.success >= 95 ? 'warn' : 'bad'"><i></i>{{ summary.success >= 99 ? '稳定' : summary.success >= 95 ? '关注' : '异常' }}</span></div>
              </div>
              <section v-if="topologyHasFocus" class="topology-focus-panel" aria-live="polite"><div><strong>{{ topologyFocusedKey ? `客户端 Key：${topologyFocusedKey.name}` : `分组：${topologyFocusedGroup?.name || '未知分组'}` }}</strong><small>{{ topologyFocusedKey ? `按所属分组 · ${topologyFocusedUpstreams.length} 条候选线路` : `${topologyFocusedUpstreams.length} 条候选线路 · 按优先级尝试` }}</small></div><div class="topology-focus-route"><span v-for="item in topologyFocusedUpstreams" :key="item.id" class="topology-focus-chip"><b>P{{ item.priority }}</b>{{ item.name }}<em :class="upstreamRouteTone(item)"><i></i>{{ upstreamRouteText(item) }}</em></span><span v-if="!topologyFocusedUpstreams.length" class="muted">暂无可用线路</span></div><button class="icon" title="清除拓扑聚焦" @click="clearTopologyFocus"><X :size="15" /></button></section>
            </div>
          </section>
        </section>

        <section v-else-if="view === 'groups'" class="view-stack">
          <div class="action-row"><p>{{ groups.length }} 个分组，密钥只会在所属分组内路由。</p><button class="primary" @click="openGroup()"><Plus :size="17" />创建分组</button></div>
          <section class="panel table-panel"><div class="table-wrap"><table>
            <thead><tr><th>名称</th><th>状态</th><th>上游</th><th>绑定密钥</th><th class="right">操作</th></tr></thead>
            <tbody>
              <tr v-for="item in groups" :key="item.id" :class="{ subdued: !item.enabled }">
                <td><strong>{{ item.name }}</strong><small>创建于 {{ fmtDate(item.created_at) }}</small></td>
                <td><span class="status" :class="item.enabled ? 'good' : 'warn'"><i></i>{{ item.enabled ? '启用' : '停用' }}</span></td>
                <td>{{ item.upstream_ids?.map((id) => upstreams.find((upstream) => upstream.id === id)?.name || `#${id}`).join('、') || '无上游' }}</td>
                <td>{{ item.key_count }} 个密钥</td>
                <td class="right"><div class="row-actions"><button class="icon" title="编辑" @click="openGroup(item)"><Pencil :size="16" /></button><button class="icon danger" title="删除" :disabled="item.key_count > 0" @click="removeGroup(item)"><Trash2 :size="16" /></button></div></td>
              </tr>
              <tr v-if="!groups.length"><td colspan="5"><div class="empty"><Network :size="22" /><strong>还没有分组</strong><span>创建分组并绑定上游后，为客户端密钥选择路由范围。</span><button class="secondary" @click="openGroup()"><Plus :size="15" />创建分组</button></div></td></tr>
            </tbody>
          </table></div></section>
        </section>

        <section v-else-if="view === 'upstreams'" class="view-stack">
          <div class="action-row upstream-toolbar"><div class="toolbar-summary"><p>{{ filteredUpstreamGroups.length }}/{{ upstreamGroups.length }} 个 Base URL，数字越小优先级越高。</p><span v-if="upstreamSelectedIds.length" class="selection-count">已选 {{ upstreamSelectedIds.length }} 个 Key</span></div><div class="toolbar-actions"><button v-if="upstreamSelectedIds.length" class="secondary" :disabled="saving" @click="bulkUpstreamAction('check')"><Activity :size="15" />批量检查</button><button v-if="upstreamSelectedIds.length" class="secondary" :disabled="saving" @click="bulkUpstreamAction('balance')"><CircleDollarSign :size="15" />批量刷新余额</button><button class="primary" @click="openUpstream()"><Plus :size="17" />添加上游</button></div></div>
          <div class="list-filterbar"><label><span>搜索</span><input v-model.trim="upstreamFilter.search" placeholder="Base URL 或 Key 名称" /></label><label><span>状态</span><select v-model="upstreamFilter.status"><option value="all">全部状态</option><option value="healthy">正常</option><option value="warning">关注</option><option value="error">异常</option></select></label><label><span>协议</span><select v-model="upstreamFilter.protocol"><option value="all">全部协议</option><option v-for="protocol in UPSTREAM_PROTOCOLS" :key="protocol" :value="protocol">{{ protocol }}</option></select></label><button class="text-button" @click="Object.assign(upstreamFilter, { search: '', status: 'all', protocol: 'all' })">清除筛选</button></div>
          <section class="panel table-panel"><div class="table-wrap"><table class="upstream-table">
            <thead><tr>
              <th><input class="upstream-checkbox" type="checkbox" :checked="allVisibleUpstreamsSelected" :indeterminate="someVisibleUpstreamsSelected" aria-label="选择全部可见上游 Key" @change="toggleAllVisibleUpstreams" /></th><th :aria-sort="ariaSort('upstreams', 'priority')"><button class="sort-button" @click="toggleSort('upstreams', 'priority')">顺序<ArrowUpDown :size="12" /></button></th>
              <th :aria-sort="ariaSort('upstreams', 'name')"><button class="sort-button" @click="toggleSort('upstreams', 'name')">上游<ArrowUpDown :size="12" /></button></th>
              <th :aria-sort="ariaSort('upstreams', 'health_status')"><button class="sort-button" @click="toggleSort('upstreams', 'health_status')">连接<ArrowUpDown :size="12" /></button></th>
              <th>能力</th>
              <th>账号扣费总额</th><th>官方价格估算</th>
              <th :aria-sort="ariaSort('upstreams', 'balance')"><button class="sort-button" @click="toggleSort('upstreams', 'balance')">余额<ArrowUpDown :size="12" /></button></th>
              <th>熔断</th><th class="right">操作</th>
            </tr></thead>
            <tbody>
              <template v-for="item in sortRows(filteredUpstreamGroups, 'upstreams')" :key="item.key">
              <tr :class="{ subdued: item.enabled === 0 }" class="clickable" tabindex="0" @click="openUpstreamGroup(item)" @keydown.enter.prevent="openUpstreamGroup(item)">
                <td><input class="upstream-checkbox" type="checkbox" :checked="item.items.every((entry) => upstreamSelectedIds.includes(entry.id))" :indeterminate="item.items.some((entry) => upstreamSelectedIds.includes(entry.id)) && !item.items.every((entry) => upstreamSelectedIds.includes(entry.id))" :aria-label="`选择 ${item.base_url} 下的 Key`" @click.stop @change="item.items.forEach((entry) => toggleUpstreamSelection(entry, $event))" /></td><td><span class="priority">{{ item.priority }}</span></td>
                <td><strong>{{ item.base_url }}</strong><small class="cell-copy">{{ item.total }} 个 Key · {{ item.available }} 个可路由<button class="copy-button" title="复制 Base URL" @click.stop="copyValue(item.base_url, 'Base URL')"><Copy :size="12" /></button></small></td>
                <td><span class="status" :class="groupStatusTone(item)"><i></i>{{ groupStatusText(item) }}</span><small v-if="item.total > 1">点击查看各 Key 状态</small></td>
                <td><div class="tag-row"><span class="tag" v-for="protocol in item.protocols" :key="protocol">{{ protocol }}</span></div><small>{{ item.models?.length || 0 }} 个模型</small></td>
                <td><strong>{{ item.total > 1 ? '多账号' : fmtBalanceUsed(item.items[0]) }}</strong><small>{{ item.total > 1 ? '抽屉内分别展示' : fmtDate(balanceUsedUpdatedAt(item.items[0])) }}</small></td>
                <td><strong>{{ item.total > 1 ? '多账号' : officialCostDetail(item.items[0], 'today') }}</strong><small>{{ item.total > 1 ? '不聚合不同账号' : `历史 ${officialCostDetail(item.items[0], 'lifetime')}` }}</small></td>
                <td><strong class="balance">{{ item.total > 1 ? '多 Key' : fmtBalance(item.items[0]) }}</strong><small>{{ item.total > 1 ? '抽屉内分别展示' : fmtDate(item.items[0].balance?.updated_at) }}</small></td>
                <td><small>{{ groupCircuitText(item) }}</small><small v-if="item.balance_suspended">{{ item.balance_suspended }} 个余额暂停</small><small v-else-if="item.total > item.enabled">{{ item.total - item.enabled }} 个停用</small></td>
                <td class="menu-cell"><div class="row-actions">
                  <button class="icon" title="查看 Key" @click.stop="openUpstreamGroup(item)"><MoreHorizontal :size="16" /></button>
                </div></td>
              </tr>
              <tr v-if="expandedMobileRow === `upstream-${item.key}`" class="mobile-detail-row"><td colspan="10"><dl><div><dt>连接</dt><dd>{{ groupStatusText(item) }}</dd></div><div><dt>协议</dt><dd>{{ item.protocols?.join(', ') || '全部' }}</dd></div><div><dt>模型</dt><dd>{{ item.models?.length || 0 }} 个</dd></div><div><dt>Key 数量</dt><dd>{{ item.total }}</dd></div></dl></td></tr>
              </template>
              <tr v-if="!filteredUpstreamGroups.length"><td colspan="10"><div class="empty"><Server :size="22" /><strong>{{ upstreamGroups.length ? '没有匹配的上游' : '还没有配置上游' }}</strong><span>{{ upstreamGroups.length ? '调整筛选条件后重试。' : '添加第一个 NewAPI 或 Sub2API 路由目标。' }}</span><button v-if="!upstreamGroups.length" class="secondary" @click="openUpstream()"><Plus :size="15" />添加上游</button></div></td></tr>
            </tbody>
          </table></div></section>
        </section>

        <section v-else-if="view === 'keys'" class="view-stack">
          <div class="action-row keys-action-row">
            <div class="keys-action-info">
              <p>为每个客户端分配独立密钥与模型权限。</p>
              <div class="gateway-base-url" aria-label="网关 Base URL">
                <span>网关 Base URL</span>
                <code :title="gatewayBaseURL">{{ gatewayBaseURL }}</code>
                <button class="copy-button gateway-base-url-copy" title="复制网关 Base URL" aria-label="复制网关 Base URL" @click="copyValue(gatewayBaseURL, '网关 Base URL')"><Copy :size="14" /></button>
              </div>
            </div>
            <button class="primary" @click="openKey()"><Plus :size="17" />创建密钥</button>
          </div>
          <div class="list-filterbar"><label><span>搜索</span><input v-model.trim="keyFilter.search" placeholder="名称或密钥前缀" /></label><label><span>状态</span><select v-model="keyFilter.status"><option value="all">全部状态</option><option value="enabled">启用</option><option value="disabled">停用</option></select></label><label><span>分组</span><select v-model="keyFilter.group_id"><option value="">全部分组</option><option v-for="group in groups" :key="group.id" :value="String(group.id)">{{ group.name }}</option></select></label><button class="text-button" @click="Object.assign(keyFilter, { search: '', status: 'all', group_id: '' })">清除筛选</button></div>
          <section class="panel table-panel"><div class="table-wrap"><table class="key-table">
            <thead><tr>
              <th :aria-sort="ariaSort('keys', 'name')"><button class="sort-button" @click="toggleSort('keys', 'name')">名称<ArrowUpDown :size="12" /></button></th>
              <th>密钥前缀</th>
              <th :aria-sort="ariaSort('keys', 'enabled')"><button class="sort-button" @click="toggleSort('keys', 'enabled')">状态<ArrowUpDown :size="12" /></button></th>
              <th>分组</th>
              <th>协议</th>
              <th :aria-sort="ariaSort('keys', 'models')"><button class="sort-button" @click="toggleSort('keys', 'models')">模型限制<ArrowUpDown :size="12" /></button></th>
              <th :aria-sort="ariaSort('keys', 'last_used_at')"><button class="sort-button" @click="toggleSort('keys', 'last_used_at')">最后使用<ArrowUpDown :size="12" /></button></th>
              <th class="right">操作</th>
            </tr></thead>
            <tbody>
              <template v-for="item in sortRows(filteredKeys, 'keys')" :key="item.id">
              <tr :class="{ subdued: !item.enabled }">
                <td><strong>{{ item.name }}</strong><small>创建于 {{ fmtDate(item.created_at) }}</small></td>
                <td><code>{{ item.prefix || item.key_prefix || '-' }}••••••••</code></td>
                <td><span class="status" :class="item.enabled ? 'good' : 'warn'"><i></i>{{ item.enabled ? '启用' : '停用' }}</span></td>
                <td>{{ item.group_name || '历史未分组' }}</td>
                <td><span class="tag" v-for="protocol in item.protocols" :key="protocol">{{ protocol }}</span><span v-if="!item.protocols?.length" class="muted">全部</span></td>
                <td>{{ item.models?.length ? `${item.models.length} 个模型` : '全部模型' }}</td><td class="muted">{{ fmtDate(item.last_used_at) }}</td>
                <td class="menu-cell"><div class="row-actions"><button class="icon" title="复制密钥" aria-label="复制密钥" @click="copyClientKey(item)"><Copy :size="16" /></button><button class="icon" title="模拟请求" aria-label="模拟请求" @click="openKeySimulation(item)"><Play :size="16" /></button><button class="icon" title="编辑" @click="openKey(item)"><Pencil :size="16" /></button><button class="icon" title="导入 CCSwitch" aria-label="导入 CCSwitch" @click="openCCSwitch(item)"><Upload :size="16" /></button><button class="icon mobile-row-toggle" title="展开详情" :aria-expanded="expandedMobileRow === `key-${item.id}`" @click="expandedMobileRow = expandedMobileRow === `key-${item.id}` ? '' : `key-${item.id}`"><ChevronRight :class="{ rotate: expandedMobileRow === `key-${item.id}` }" :size="17" /></button><button class="icon" title="更多操作" aria-haspopup="menu" :aria-expanded="openRowMenu === `key-${item.id}`" @click.stop="toggleRowMenu(`key-${item.id}`, $event)"><MoreHorizontal :size="17" /></button><Teleport to="body"><div v-if="openRowMenu === `key-${item.id}`" class="row-menu" role="menu" :style="{ top: `${rowMenuPosition.top}px`, left: `${rowMenuPosition.left}px` }" @click.stop @keydown="handleRowMenuKeydown"><button role="menuitem" @click="closeRowMenu(); copyClientKey(item)"><Copy :size="15" />复制密钥</button><button role="menuitem" @click="closeRowMenu(); openKeySimulation(item)"><Play :size="15" />模拟请求</button><button role="menuitem" @click="closeRowMenu(); openCCSwitch(item)"><Upload :size="15" />导入 CCSwitch</button><button class="danger" role="menuitem" @click="closeRowMenu(); removeKey(item)"><Trash2 :size="15" />删除密钥</button></div></Teleport></div></td>
              </tr>
              <tr v-if="expandedMobileRow === `key-${item.id}`" class="mobile-detail-row"><td colspan="8"><dl><div><dt>分组</dt><dd>{{ item.group_name || '历史未分组' }}</dd></div><div><dt>密钥前缀</dt><dd><code>{{ item.prefix || item.key_prefix || '-' }}••••••••</code></dd></div><div><dt>协议</dt><dd>{{ item.protocols?.join(', ') || '全部' }}</dd></div><div><dt>模型限制</dt><dd>{{ item.models?.length ? `${item.models.length} 个模型` : '全部模型' }}</dd></div><div><dt>最后使用</dt><dd>{{ fmtDate(item.last_used_at) }}</dd></div><div><dt>创建时间</dt><dd>{{ fmtDate(item.created_at) }}</dd></div></dl></td></tr>
              </template>
              <tr v-if="!filteredKeys.length"><td colspan="8"><div class="empty"><KeyRound :size="22" /><strong>{{ keys.length ? '没有匹配的客户端密钥' : '还没有客户端密钥' }}</strong><span>{{ keys.length ? '调整筛选条件后重试。' : '为调用方创建独立密钥并限制协议与模型。' }}</span><button v-if="!keys.length" class="secondary" @click="openKey()"><Plus :size="15" />创建密钥</button></div></td></tr>
            </tbody>
          </table></div></section>
        </section>

        <section v-else-if="view === 'logs'" class="view-stack">
          <form class="filterbar" @submit.prevent="logFilter.offset = 0; loadLogs()">
            <label><span>状态</span><select v-model="logFilter.status"><option value="">全部</option><option value="success">成功</option><option value="error">失败</option><option value="429">429</option><option value="5xx">5xx</option></select></label>
            <label><span>上游</span><select v-model="logFilter.upstream_id"><option value="">全部</option><option v-for="item in upstreams" :value="String(item.id)" :key="item.id">{{ item.name }}</option></select></label>
            <label><span>分组</span><select v-model="logFilter.group_id"><option value="">全部</option><option v-for="item in groups" :value="String(item.id)" :key="item.id">{{ item.name }}</option></select></label>
            <button class="secondary"><Search :size="16" />筛选</button><div class="quick-filters"><button type="button" class="text-button" :class="{ active: logFilter.status === 'error' }" @click="setLogPreset('errors')">只看失败</button><button type="button" class="text-button" :class="{ active: !logFilter.status && !logFilter.upstream_id && !logFilter.group_id }" @click="setLogPreset('all')">清除筛选</button></div>
          </form>
          <section class="panel table-panel"><div class="table-wrap"><table>
            <thead><tr>
              <th :aria-sort="ariaSort('logs', 'created_at')"><button class="sort-button" @click="toggleSort('logs', 'created_at')">时间<ArrowUpDown :size="12" /></button></th>
              <th>请求</th><th>协议 / 模型</th>
              <th :aria-sort="ariaSort('logs', 'status_code')"><button class="sort-button" @click="toggleSort('logs', 'status_code')">结果<ArrowUpDown :size="12" /></button></th>
              <th :aria-sort="ariaSort('logs', 'upstream_name')"><button class="sort-button" @click="toggleSort('logs', 'upstream_name')">上游<ArrowUpDown :size="12" /></button></th>
              <th :aria-sort="ariaSort('logs', 'duration_ms')"><button class="sort-button" @click="toggleSort('logs', 'duration_ms')">耗时<ArrowUpDown :size="12" /></button></th>
              <th :aria-sort="ariaSort('logs', 'tokens')"><button class="sort-button" @click="toggleSort('logs', 'tokens')">Token<ArrowUpDown :size="12" /></button></th><th></th>
            </tr></thead>
            <tbody>
              <template v-for="item in sortRows(logs, 'logs')" :key="item.request_id">
                <tr class="clickable" tabindex="0" role="button" :aria-expanded="expandedLog === item.request_id" :aria-label="`展开请求 ${item.request_id} 详情`" @click="toggleLog(item.request_id)" @keydown.enter.prevent="toggleLog(item.request_id)" @keydown.space.prevent="toggleLog(item.request_id)">
                  <td class="nowrap">{{ fmtDate(item.created_at) }}</td><td><span class="cell-copy"><code>{{ item.request_id.slice(0, 12) }}</code><button class="copy-button" title="复制完整请求 ID" @click.stop="copyValue(item.request_id, '请求 ID')"><Copy :size="12" /></button></span><small>{{ item.api_key_name || '未知客户端' }} · {{ item.group_name || '历史未分组' }}</small></td>
                  <td><strong>{{ item.protocol }}</strong><small>{{ item.model || '-' }}</small></td>
                  <td><span class="status" :class="item.status_code < 400 ? 'good' : 'bad'"><i></i>{{ item.status_code }}</span><small v-if="item.error_code">{{ item.error_code }}</small></td>
                  <td>{{ item.upstream_name || (item.upstream_id ? `#${item.upstream_id}` : '-') }}</td><td><strong>{{ fmtDuration(item.duration_ms) }}</strong><small>TTFB {{ fmtDuration(item.ttfb_ms) }} · TTFT {{ fmtDuration(item.ttft_ms) }}</small></td>
                  <td><strong>{{ item.usage?.input_tokens == null && item.usage?.output_tokens == null ? '未知' : fmtNumber((item.usage?.input_tokens || 0) + (item.usage?.output_tokens || 0)) }}</strong><small v-if="item.usage">入 {{ fmtMetric(item.usage.input_tokens) }} / 出 {{ fmtMetric(item.usage.output_tokens) }}</small></td>
                  <td><ChevronRight :size="16" :class="{ rotate: expandedLog === item.request_id }" /></td>
                </tr>
                <tr v-if="expandedLog === item.request_id" class="attempt-row"><td colspan="8"><div class="attempts">
                  <div class="log-detail-grid">
                    <div><span>总耗时</span><strong>{{ fmtDuration(item.duration_ms) }}</strong></div>
                    <div><span>首包 TTFB</span><strong>{{ fmtDuration(item.ttfb_ms) }}</strong></div>
                    <div><span>首字 TTFT</span><strong>{{ fmtDuration(item.ttft_ms) }}</strong></div>
                    <div><span>输入 Token</span><strong>{{ fmtMetric(item.usage?.input_tokens) }}</strong></div>
                    <div><span>输出 Token</span><strong>{{ fmtMetric(item.usage?.output_tokens) }}</strong></div>
                    <div><span>缓存读取</span><strong>{{ fmtMetric(item.usage?.cached_input_tokens) }}</strong></div>
                    <div><span>缓存写入</span><strong>{{ fmtMetric(item.usage?.cache_creation_input_tokens) }}</strong></div>
                    <div><span>Token 命中率</span><strong>{{ fmtPercent(logTokenHitRate(item)) }}</strong></div>
                  </div>
                  <div class="attempt-head"><div><strong>切换链</strong><span>{{ item.attempts?.length || 0 }} 次尝试</span></div><div class="log-detail-actions"><button v-if="item.upstream_id" class="text-button" @click.stop="inspectLogUpstream(item)"><Server :size="14" />查看上游</button><button v-if="item.upstream_id" class="text-button" @click.stop="inspectLogAction(item, 'check')"><Activity :size="14" />检查连接</button><button v-if="item.upstream_id" class="text-button" @click.stop="inspectLogAction(item, 'balance')"><CircleDollarSign :size="14" />刷新余额</button></div></div>
                  <div v-if="item.attempts?.length" class="attempt-timeline" aria-label="请求尝试时间轴"><div v-for="(attempt, index) in item.attempts" :key="index" class="attempt-timeline-row"><span>{{ index + 1 }}</span><div><strong>{{ attempt.upstream_name || `上游 #${attempt.upstream_id}` }}</strong><div class="attempt-track"><i :class="attempt.status_code && attempt.status_code < 400 ? 'good' : 'bad'" :style="{ width: attemptWidth(item, attempt.duration_ms) }"></i></div></div><small>{{ fmtDuration(attempt.duration_ms) }}</small></div></div>
                  <p v-else class="muted">未记录上游尝试。</p>
                </div></td></tr>
              </template>
              <tr v-if="!logs.length"><td colspan="8"><div class="empty"><Search :size="22" /><strong>暂无符合条件的日志</strong><span>调整状态或上游筛选条件后重试。</span></div></td></tr>
            </tbody>
          </table></div><div class="pagination"><span>每页 {{ logFilter.limit }} 条</span><div><button class="icon" title="上一页" :disabled="logFilter.offset === 0" @click="logFilter.offset = Math.max(0, logFilter.offset - logFilter.limit); loadLogs()"><ChevronLeft :size="17" /></button><button class="icon" title="下一页" :disabled="logs.length < logFilter.limit" @click="logFilter.offset += logFilter.limit; loadLogs()"><ChevronRight :size="17" /></button></div></div></section>
        </section>

        <section v-else-if="view === 'usage'" class="view-stack">
          <div class="mobile-filter-bar"><span>{{ usageFilter.days === 30 ? '近 30 天' : ('近 ' + usageFilter.days + ' 天') }} · 按{{ usageFilter.dimension === 'upstream' ? '上游' : usageFilter.dimension === 'api_key' ? '客户端' : usageFilter.dimension === 'group' ? '分组' : usageFilter.dimension === 'protocol' ? '协议' : '模型' }}</span><button class="secondary" type="button" @click="mobileFiltersOpen = !mobileFiltersOpen"><ListFilter :size="15" />{{ mobileFiltersOpen ? '收起筛选' : '筛选' }}</button><button class="secondary" type="button" @click="exportUsageCSV"><Download :size="15" />导出 CSV</button></div>
          <form class="filterbar usage-filterbar" :class="{ 'filters-open': mobileFiltersOpen }" @submit.prevent="applyUsageFilters">
            <button class="icon mobile-filter-close" type="button" title="关闭筛选" @click="mobileFiltersOpen = false"><X :size="17" /></button>
            <label><span>时间范围</span><select v-model.number="usageFilter.days"><option :value="7">近 7 天</option><option :value="30">近 30 天</option><option :value="90">近 90 天</option><option :value="365">近 1 年</option></select></label>
            <label><span>统计粒度</span><select v-model="usageFilter.granularity"><option value="day">按天</option><option value="week">按周</option><option value="month">按月</option></select></label>
            <label><span>拆分维度</span><select v-model="usageFilter.dimension"><option value="upstream">上游</option><option value="api_key">客户端密钥</option><option value="group">分组</option><option value="protocol">协议</option><option value="model">模型</option></select></label>
            <label><span>Top N</span><select v-model.number="usageFilter.topN"><option :value="5">Top 5</option><option :value="10">Top 10</option><option :value="20">Top 20</option><option :value="50">Top 50</option></select></label>
            <label v-if="usageFilter.dimension === 'upstream'"><span>上游筛选</span><select v-model="usageFilter.upstream_id"><option value="">全部上游</option><option v-for="item in upstreams" :key="item.id" :value="String(item.id)">{{ item.name }}</option></select></label>
            <label v-if="usageFilter.dimension === 'api_key'"><span>客户端筛选</span><select v-model="usageFilter.api_key_id"><option value="">全部客户端</option><option v-for="item in keys" :key="item.id" :value="String(item.id)">{{ item.name }}</option></select></label>
            <label v-if="usageFilter.dimension === 'group'"><span>分组筛选</span><select v-model="usageFilter.group_id"><option value="">全部分组</option><option v-for="item in groups" :key="item.id" :value="String(item.id)">{{ item.name }}</option></select></label>
            <label v-if="usageFilter.dimension === 'protocol'"><span>协议筛选</span><select v-model="usageFilter.protocol"><option value="">全部协议</option><option v-for="protocol in UPSTREAM_PROTOCOLS" :key="protocol" :value="protocol">{{ protocol }}</option></select></label>
            <label v-if="usageFilter.dimension === 'model'"><span>模型筛选</span><input v-model.trim="usageFilter.model" placeholder="模型名称" /></label>
            <button class="secondary"><Search :size="16" />应用筛选</button><button class="secondary usage-export-button" type="button" @click="exportUsageCSV"><Download :size="15" />导出 CSV</button>
          </form>
          <div class="metric-grid usage-metric-grid">
            <article><span class="metric-icon ink"><Activity :size="19" /></span><div><small>{{ usageFilter.days }} 天请求</small><strong>{{ fmtNumber(usageTotals.requests) }}</strong></div></article>
            <article><span class="metric-icon green"><ChevronRight :size="19" /></span><div><small>输入 Token</small><strong>{{ fmtNumber(usageInputTokens) }}</strong></div></article>
            <article><span class="metric-icon blue"><ChevronLeft :size="19" /></span><div><small>输出 Token</small><strong>{{ fmtNumber(usageOutputTokens) }}</strong></div></article>
			<article><span class="metric-icon amber"><CircleDollarSign :size="19" /></span><div><small>缓存读 / 写</small><strong>{{ fmtNumber(usageCachedTokens) }} <em>/ {{ fmtMetric(usageCacheWriteTokens) }}</em></strong></div></article>
            <article><span class="metric-icon green"><Check :size="19" /></span><div><small>Token 命中率</small><strong>{{ fmtPercent(usageTokenHitRate) }}</strong></div></article>
			<article><span class="metric-icon blue"><Gauge :size="19" /></span><div><small>平均 / P95 耗时</small><strong>{{ fmtDuration(usageAvgLatency) }} <em>/ {{ fmtDuration(usageP95Latency) }}</em></strong></div></article>
			<article class="cost-metric"><span class="metric-icon green"><CircleDollarSign :size="19" /></span><div><small>官方价格估算</small><strong>{{ fmtCurrency(usageCostUsd, 'USD') }}</strong><span class="metric-detail">约 {{ fmtCurrency(usageCostUsd * Number(pricing.usd_cny_rate || 7.2), 'CNY') }} · 覆盖 {{ fmtPercent(usageCostCoverage) }}</span></div></article>
			<article class="cost-metric"><span class="metric-icon amber"><ChartNoAxesCombined :size="19" /></span><div><small>月度成本预测</small><strong>{{ fmtCurrency(usageCostForecast30d, 'USD') }}</strong><span class="metric-detail">按当前周期日均外推 30 天</span></div></article>
          </div>
          <section class="panel usage-panel"><div class="panel-head usage-chart-head"><div><h2>使用趋势</h2><p>{{ usageFilter.granularity === 'day' ? '按天' : usageFilter.granularity === 'week' ? '按周' : '按月' }} · {{ usageDimensionLabel({ label: usageFilter.dimension === 'upstream' ? '上游' : usageFilter.dimension === 'api_key' ? '客户端密钥' : usageFilter.dimension === 'group' ? '分组' : usageFilter.dimension === 'protocol' ? '协议' : '模型' }) }}</p></div><div class="segmented-control" role="group" aria-label="趋势指标"><button :aria-pressed="usageMetric === 'requests'" :class="{ active: usageMetric === 'requests' }" @click="usageMetric = 'requests'">请求</button><button :aria-pressed="usageMetric === 'tokens'" :class="{ active: usageMetric === 'tokens' }" @click="usageMetric = 'tokens'">Token</button><button :aria-pressed="usageMetric === 'cache'" :class="{ active: usageMetric === 'cache' }" @click="usageMetric = 'cache'">缓存</button><button :aria-pressed="usageMetric === 'latency'" :class="{ active: usageMetric === 'latency' }" @click="usageMetric = 'latency'">耗时</button></div></div>
            <div v-if="usageRows.length" class="chart-frame"><UsageChart :rows="usageRows" :theme="resolvedTheme" :metric="usageMetric" :range-label="`近 ${usageFilter.days} 天`" /></div>
            <div v-else class="empty"><Gauge :size="22" /><strong>暂无用量数据</strong><span>产生请求后，这里会显示趋势与缓存命中情况。</span></div>
          </section>
          <section class="panel table-panel usage-table-panel"><div class="panel-head"><div><h2>明细汇总</h2><p>Top {{ usageFilter.topN }} · 其余项目聚合为“其他”</p></div><div class="panel-head-actions"><span class="muted usage-hit-summary">请求命中率 {{ fmtPercent(usageRequestHitRate) }}</span><button class="secondary usage-export-inline" type="button" @click="exportUsageCSV"><Download :size="15" />导出</button></div></div><div class="table-wrap"><table><thead><tr>
            <th :aria-sort="ariaSort('usage', 'day')"><button class="sort-button" @click="toggleSort('usage', 'day')">日期<ArrowUpDown :size="12" /></button></th><th>维度</th>
            <th :aria-sort="ariaSort('usage', 'requests')"><button class="sort-button" @click="toggleSort('usage', 'requests')">请求<ArrowUpDown :size="12" /></button></th>
            <th :aria-sort="ariaSort('usage', 'successes')"><button class="sort-button" @click="toggleSort('usage', 'successes')">成功<ArrowUpDown :size="12" /></button></th>
            <th :aria-sort="ariaSort('usage', 'input_tokens')"><button class="sort-button" @click="toggleSort('usage', 'input_tokens')">输入 Token<ArrowUpDown :size="12" /></button></th>
            <th :aria-sort="ariaSort('usage', 'output_tokens')"><button class="sort-button" @click="toggleSort('usage', 'output_tokens')">输出 Token<ArrowUpDown :size="12" /></button></th>
            <th :aria-sort="ariaSort('usage', 'cached_input_tokens')"><button class="sort-button" @click="toggleSort('usage', 'cached_input_tokens')">缓存读 / 写<ArrowUpDown :size="12" /></button></th><th>平均 / P95</th>
          </tr></thead><tbody><tr v-for="(row, index) in sortRows([...usageRows].reverse(), 'usage')" :key="usageRowKey(row, index)"><td><strong>{{ String(row.day || row.date || row.label || '').slice(0, 10) || '—' }}</strong></td><td><strong>{{ usageDimensionLabel(row) }}</strong></td><td>{{ fmtNumber(row.requests) }}</td><td>{{ fmtNumber(row.successes) }}</td><td>{{ fmtMetric(row.input_tokens) }}</td><td>{{ fmtMetric(row.output_tokens) }}</td><td>{{ fmtMetric(row.cached_input_tokens ?? row.cache_read_tokens) }} / {{ fmtMetric(row.cache_creation_input_tokens ?? row.cache_write_tokens) }}</td><td>{{ fmtDuration(row.avg_duration_ms ?? row.average_duration_ms) }} / {{ fmtDuration(row.p95_duration_ms ?? row.p95_ms) }}</td></tr></tbody></table></div></section>
        </section>

        <section v-else class="view-stack">
          <div class="section-tabs" role="tablist" aria-label="通知和设置">
            <button role="tab" :aria-selected="notificationSection === 'channels'" :class="{ active: notificationSection === 'channels' }" @click="notificationSection = 'channels'"><Bell :size="15" />通知渠道</button>
            <button role="tab" :aria-selected="notificationSection === 'alerts'" :class="{ active: notificationSection === 'alerts' }" @click="notificationSection = 'alerts'"><AlertCircle :size="15" />告警规则</button>
            <button role="tab" :aria-selected="notificationSection === 'settings'" :class="{ active: notificationSection === 'settings' }" @click="notificationSection = 'settings'"><Network :size="15" />路由设置</button>
          </div>
          <template v-if="notificationSection === 'channels'">
            <div class="action-row"><p>上游状态与安全事件将发送到已启用渠道。</p><button class="primary" @click="openChannel"><Plus :size="17" />添加渠道</button></div>
            <div class="channel-grid">
              <article v-for="item in channels" :key="item.id" class="channel-card"><span class="channel-icon"><Mail v-if="item.kind === 'email'" :size="21" /><Webhook v-else :size="21" /></span><div><strong>{{ item.name }}</strong><small>{{ item.kind === 'email' ? '邮件' : 'Webhook' }} · {{ item.enabled ? '已启用' : '已停用' }}</small></div><span class="status" :class="item.enabled ? 'good' : 'warn'"><i></i>{{ item.enabled ? '启用' : '停用' }}</span><div class="channel-actions"><button v-if="item.kind === 'webhook'" class="secondary channel-test" :disabled="channelTestID !== null" :aria-busy="channelTestID === item.id" @click="testChannel(item)"><LoaderCircle v-if="channelTestID === item.id" class="spin" :size="14" /><Webhook v-else :size="14" />{{ channelTestID === item.id ? '测试中' : '测试' }}</button><button class="icon danger" title="删除" @click="removeChannel(item)"><Trash2 :size="16" /></button></div></article>
              <div v-if="!channels.length" class="empty panel">还没有通知渠道。</div>
            </div>
          </template>
          <template v-else-if="notificationSection === 'settings'">
            <section class="panel settings-strip"><div><h2>路由设置</h2><p>单次请求最多尝试的上游数量</p></div><label>最大尝试次数<input v-model.number="maxAttempts" type="number" min="1" max="5" /></label><button class="secondary" @click="saveSettings"><Check :size="16" />保存</button></section>
            <div class="notice-panel"><Check :size="16" /><span>客户端 Key 仅使用所属分组内的上游；候选按优先级依次尝试（数字越小越优先），单次请求最多尝试这里设置的上游数量。</span></div>
          </template>
          <template v-else>
            <section class="panel table-panel"><div class="panel-head"><div><h2>告警规则</h2><p>全局默认规则可直接调整；上游规则会覆盖同类默认值</p></div></div>
              <div class="rule-add"><select v-model="newRule.event"><option v-for="event in ['low_balance','balance_unavailable','error_rate','latency','client_error_rate']" :key="event" :value="event">{{ alertEventText(event) }}</option></select><select v-model="newRule.upstream_id"><option value="">选择上游</option><option v-for="item in upstreams" :key="item.id" :value="String(item.id)">{{ item.name }}</option></select><input v-model.number="newRule.threshold" type="number" min="0" step="0.1" title="阈值" /><button class="secondary" @click="addRule"><Plus :size="16" />添加覆盖</button></div>
              <div class="table-wrap"><table><thead><tr><th>事件</th><th>范围</th><th>阈值</th><th>窗口（秒）</th><th>冷却（秒）</th><th>最多提醒</th><th>启用</th><th></th></tr></thead><tbody>
                <tr v-for="rule in alertRules" :key="rule.id"><td><strong>{{ alertEventText(rule.event) }}</strong></td><td>{{ upstreamName(rule.upstream_id) }}</td><td><input v-model.number="rule.threshold" class="table-input" type="number" min="0" step="0.1" /></td><td><input v-model.number="rule.window_seconds" class="table-input" type="number" min="60" /></td><td><input v-model.number="rule.cooldown_seconds" class="table-input" type="number" min="60" /></td><td><input v-model.number="rule.max_notifications" class="table-input" type="number" min="1" max="100" /></td><td><label class="switch"><input v-model="rule.enabled" type="checkbox" /><span></span></label></td><td class="right"><button class="icon" title="保存规则" @click="saveRule(rule)"><Check :size="16" /></button><button v-if="rule.upstream_id" class="icon danger" title="删除覆盖" @click="removeRule(rule)"><Trash2 :size="16" /></button></td></tr>
              </tbody></table></div>
            </section>
          </template>
        </section>
      </div>
    </main>
  </div>

  <div v-if="upstreamModal" class="drawer-backdrop" @mousedown.self="closeUpstream">
    <section class="drawer" role="dialog" aria-modal="true" aria-labelledby="upstream-title">
      <header><div><h2 id="upstream-title">{{ editingUpstream ? '编辑上游' : '添加上游' }}</h2><p>配置路由能力、模型与故障恢复策略</p></div><button class="icon" title="关闭" @click="closeUpstream"><X :size="19" /></button></header>
      <form ref="upstreamFormElement" @submit.prevent="saveUpstream">
        <div class="form-section"><h3>基本信息</h3><div class="form-grid">
          <label>名称<input v-model.trim="upstreamForm.name" required autofocus placeholder="生产主线路" /></label>
          <label>类型<select v-model="upstreamForm.kind"><option value="newapi">NewAPI</option><option value="sub2api">Sub2API</option></select></label>
          <label class="span-2">Base URL<input v-model.trim="upstreamForm.base_url" type="url" required placeholder="https://api.example.com" /></label>
          <label>User-Agent 策略<select v-model="upstreamForm.user_agent_mode"><option value="default">默认</option><option value="codex">Codex 兼容</option><option value="opencode">OpenCode 兼容</option><option value="custom">自定义</option></select></label>
          <label v-if="upstreamForm.user_agent_mode === 'custom'">自定义 User-Agent<input v-model="upstreamForm.user_agent" maxlength="256" placeholder="client/1.0.0" /></label>
          <label>API Key<input v-model="upstreamForm.api_key" type="password" :required="!editingUpstream" :placeholder="editingUpstream ? '留空表示不修改' : 'sk-…'" autocomplete="off" /></label>
          <label>优先级<input v-model.number="upstreamForm.priority" type="number" min="0" required /></label>
          <label class="span-2">价格档案 <span>用于官方价格估算</span><select v-model.number="upstreamForm.pricing_profile_id"><option :value="0">未计价</option><option v-for="profile in listOf<Json>(pricing.profiles)" :key="profile.id" :value="profile.id">{{ profile.name }}（{{ profile.prices?.length || 0 }} 个模型）</option></select></label>
          <template v-if="usesNewAPICredentials(upstreamForm.kind)">
            <label>Access Token <span>可选</span><input v-model="upstreamForm.access_token" type="password" placeholder="用于余额查询" autocomplete="off" /></label>
            <label>User ID <span>可选</span><input v-model.trim="upstreamForm.user_id" placeholder="用于余额查询" /></label>
            <label v-if="editingUpstream" class="switch span-2"><input v-model="upstreamForm.clear_balance_credentials" type="checkbox" /><span></span>清除已保存的 Access Token 与 User ID</label>
          </template>
          <label class="switch span-2"><input v-model="upstreamForm.enabled" type="checkbox" /><span></span>启用该上游</label>
          <label class="switch span-2"><input v-model="upstreamForm.balance_protection_enabled" type="checkbox" /><span></span>余额耗尽时自动暂停路由</label>
        </div></div>
        <div class="form-section"><h3>能力与模型</h3><div class="form-grid">
          <fieldset class="span-2"><legend>协议 <span>新建时默认 Responses</span></legend><div class="check-row"><label v-for="protocol in UPSTREAM_PROTOCOLS" :key="protocol"><input v-model="upstreamForm.protocols" type="checkbox" :value="protocol" />{{ protocol }}</label></div></fieldset>
          <fieldset class="span-2"><legend>模型策略</legend><div class="check-row">
            <label><input v-model="upstreamModelMode" type="radio" value="auto" />自动发现 / 不限制（默认）</label>
            <label><input v-model="upstreamModelMode" type="radio" value="manual" />指定模型</label>
          </div></fieldset>
          <fieldset class="span-2 model-discovery">
            <legend>上游模型 <span v-if="discoveredModels.length">{{ discoveredModels.length }} 个</span></legend>
            <button type="button" class="secondary model-fetch" :disabled="saving || fetchingModels || modelTestsBusy" @click="fetchUpstreamModels">
              <LoaderCircle v-if="fetchingModels" class="spin" :size="16" /><Download v-else :size="16" />获取上游模型
            </button>
            <div v-if="discoveredModels.length" class="discovered-models">
              <div class="model-list-toolbar"><span>已选 {{ discoveredModels.filter((model) => selectedUpstreamModels.includes(model)).length }} / {{ discoveredModels.length }}</span><button type="button" class="text-button model-select-toggle" @click="toggleDiscoveredModels">{{ allDiscoveredModelsSelected ? '取消全选' : '全选' }}</button></div>
              <TransitionGroup name="model-chip" tag="div" class="model-list">
                <div v-for="model in discoveredModels" :key="model" class="model-row">
                  <label><input type="checkbox" :checked="selectedUpstreamModels.includes(model)" @change="selectDiscoveredModel(model, $event)" /><span :title="model">{{ model }}</span></label>
                  <span v-if="modelTestResults[model]" class="status" :class="modelStatusTone(modelTestResults[model].status)"><i></i>{{ modelStatusText(modelTestResults[model].status) }}</span>
                  <button type="button" class="icon model-test-one" :title="`测试 ${model}`" :disabled="saving || fetchingModels || modelTestsBusy" @click="testOneModel(model)"><LoaderCircle v-if="testingModelNames.includes(model)" class="spin" :size="15" /><Activity v-else :size="15" /></button>
                </div>
              </TransitionGroup>
            </div>
            <div v-else-if="modelDiscoveryAttempted && !fetchingModels" class="model-empty">上游未返回模型</div>
            <div class="model-batch-bar">
              <span>已选 {{ batchModelSelection.total }} 个已获取模型，单次最多 20 个</span>
              <button v-if="modelTestProgress.running" type="button" class="secondary model-test-stop" @click="stopModelTests"><CircleStop :size="16" />停止测试</button>
              <button v-else type="button" class="secondary model-batch-test" :disabled="saving || fetchingModels || modelTestsBusy || !batchModelSelection.total" @click="testSelectedModels"><Activity :size="16" />测试已选模型</button>
            </div>
            <section v-if="modelTestReports.length || modelTestProgress.running" class="model-test-summary" aria-live="polite" :aria-busy="modelTestProgress.running">
              <header><div><strong>模型测试结果</strong><span v-if="modelTestProgress.total">{{ modelTestProgress.completed }} / {{ modelTestProgress.total }}{{ modelTestProgress.stopped ? ' · 已停止' : '' }}</span></div><div class="model-test-totals"><span class="good">可用 {{ modelTestSummary.available }}</span><span class="warn">部分可用 {{ modelTestSummary.partial }}</span><span class="bad">不可用 {{ modelTestSummary.unavailable }}</span></div></header>
              <ul v-if="modelTestReports.length" class="model-test-reports">
                <li v-for="report in modelTestReports" :key="report.model" class="model-test-report">
                  <div><strong :title="report.model">{{ report.model }}</strong><span class="status" :class="modelStatusTone(report.status)"><i></i>{{ modelStatusText(report.status) }}</span></div>
                  <p v-if="report.error" class="model-test-error">{{ report.error }}</p>
                  <ul v-else class="protocol-results">
                    <li v-for="result in report.results" :key="result.protocol" class="protocol-result">
                      <strong>{{ result.protocol }}</strong><span :class="result.status === 'success' ? 'good' : result.status === 'degraded' ? 'warn' : 'bad'">{{ result.status === 'success' ? '成功' : result.status === 'degraded' ? '变慢' : '失败' }}</span><span>{{ result.status_code ? `HTTP ${result.status_code}` : '无状态码' }}</span><span>{{ fmtDuration(result.latency_ms) }}</span><small v-if="result.ping_latency_ms" :title="`Origin HEAD ${fmtDuration(result.ping_latency_ms)}`">HEAD {{ fmtDuration(result.ping_latency_ms) }}</small><small v-if="result.error" :title="result.error">{{ result.error }}</small>
                    </li>
                  </ul>
                </li>
              </ul>
            </section>
          </fieldset>
          <template v-if="upstreamModelMode === 'manual'">
            <fieldset class="span-2"><legend>常用模型</legend><div class="check-row"><label v-for="model in COMMON_UPSTREAM_MODELS" :key="model"><input type="checkbox" :checked="parseModelList(upstreamForm.models).includes(model)" @change="selectCommonModel(model, $event)" />{{ model }}</label></div></fieldset>
            <label class="span-2">模型列表 <span>逗号分隔</span><textarea v-model="upstreamForm.models" rows="2" placeholder="gpt-5.6, claude-opus-4-6"></textarea></label>
          </template>
          <label class="span-2">模型别名 <span>每行一个：客户端名称=上游名称</span><textarea v-model="upstreamForm.aliases" rows="3" placeholder="claude-sonnet=claude-sonnet-4-20250514"></textarea></label>
        </div></div>
        <div class="form-section"><h3>超时与熔断</h3><div class="form-grid compact-grid">
          <label>连接超时 <span>ms</span><input v-model.number="upstreamForm.connect_timeout_ms" type="number" min="100" required /></label>
          <label>首包超时 <span>ms</span><input v-model.number="upstreamForm.first_byte_timeout_ms" type="number" min="1000" required /></label>
          <label>流空闲超时 <span>ms</span><input v-model.number="upstreamForm.idle_timeout_ms" type="number" min="1000" required /></label>
          <label>失败阈值 <span>次</span><input v-model.number="upstreamForm.failure_threshold" type="number" min="1" max="20" required /></label>
          <label>冷却时间 <span>秒</span><input v-model.number="upstreamForm.cooldown_seconds" type="number" min="1" required /></label>
        </div></div>
        <footer><button type="button" class="secondary" @click="closeUpstream">取消</button><button class="primary" :disabled="saving || fetchingModels || modelTestsBusy"><LoaderCircle v-if="saving" class="spin" :size="16" />保存上游</button></footer>
      </form>
    </section>
  </div>

  <div v-if="upstreamGroupDrawer" class="drawer-backdrop" @mousedown.self="upstreamGroupDrawer = null">
    <section class="drawer upstream-group-drawer" role="dialog" aria-modal="true" aria-labelledby="upstream-group-title">
      <header><div><h2 id="upstream-group-title">{{ upstreamGroupDrawer.base_url }}</h2><p>{{ upstreamGroupDrawer.total }} 个 Key · 聚合展示，路由仍按各 Key 优先级</p></div><button class="icon" title="关闭" @click="upstreamGroupDrawer = null"><X :size="19" /></button></header>
      <div class="upstream-key-list">
        <article v-for="item in upstreamGroupDrawer.items" :key="item.id" class="upstream-key-card">
          <div class="upstream-key-card-head"><div><strong :title="item.name">{{ item.name }}</strong><small>优先级 P{{ item.priority }} · {{ item.protocols?.join(' / ') }}</small></div><span class="status" :class="upstreamRouteTone(item)"><i></i>{{ upstreamRouteText(item) }}</span></div>
          <dl><div><dt>今日请求</dt><dd>{{ fmtNumber(item.today_requests) }}</dd></div><div><dt>今日 Token</dt><dd>{{ fmtNumber(item.today_tokens) }}</dd></div><div><dt>账号扣费总额</dt><dd>{{ fmtBalanceUsed(item) }}<small>{{ fmtDate(balanceUsedUpdatedAt(item)) }}</small></dd></div><div><dt>今日官方估算</dt><dd>{{ officialCostDetail(item, 'today') }}</dd></div><div><dt>历史官方估算</dt><dd>{{ officialCostDetail(item, 'lifetime') }}</dd></div><div><dt>余额</dt><dd>{{ fmtBalance(item) }}</dd></div><div><dt>余额保护</dt><dd>{{ balanceProtectionText(item) }}</dd></div><div><dt>价格档案</dt><dd>{{ pricingProfileName(item.pricing_profile_id) }}</dd></div><div><dt>熔断</dt><dd>{{ item.health_status === 'open' ? `开启 · ${item.consecutive_failures || 0} 次失败` : `未开启 · ${item.consecutive_failures || 0} 次失败` }}</dd></div></dl>
          <div class="upstream-key-actions"><button class="secondary" @click="openUpstream(item)"><Pencil :size="15" />编辑 Key</button><button class="icon" title="检查连接" @click="upstreamAction(item, 'check')"><Activity :size="16" /></button><button class="icon" title="刷新余额" @click="upstreamAction(item, 'balance')"><CircleDollarSign :size="16" /></button><button class="icon" title="刷新模型" @click="upstreamAction(item, 'models')"><Download :size="16" /></button><button class="icon danger" title="删除上游" @click="removeUpstream(item)"><Trash2 :size="16" /></button></div>
        </article>
      </div>
    </section>
  </div>

  <div v-if="groupModal" class="modal-backdrop" @mousedown.self="groupModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="group-title"><header><div><h2 id="group-title">{{ editingGroup ? '编辑分组' : '创建分组' }}</h2><p>选择该分组允许使用的上游</p></div><button class="icon" title="关闭" @click="groupModal = false"><X :size="19" /></button></header><form @submit.prevent="saveGroup"><div class="form-section"><div class="form-grid">
    <label class="span-2">名称<input v-model.trim="groupForm.name" required autofocus placeholder="生产线路" /></label>
    <fieldset class="span-2"><legend>上游成员</legend><div class="check-row"><label v-for="item in upstreams" :key="item.id"><input v-model="groupForm.upstream_ids" type="checkbox" :value="item.id" />{{ item.name }}</label><span v-if="!upstreams.length" class="muted">请先创建上游</span></div></fieldset>
    <label class="switch span-2"><input v-model="groupForm.enabled" type="checkbox" /><span></span>启用分组</label>
  </div></div><footer><button type="button" class="secondary" @click="groupModal = false">取消</button><button class="primary" :disabled="saving || !groupForm.upstream_ids.length">{{ editingGroup ? '保存修改' : '创建分组' }}</button></footer></form></section></div>

  <div v-if="pricingModal" class="modal-backdrop" @mousedown.self="pricingModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="pricing-title"><header><div><h2 id="pricing-title">{{ editingPricing ? '编辑价格档案' : '新建价格档案' }}</h2><p>每行一个模型：模型名，输入，输出，缓存读，缓存写（USD / 百万 Token）</p></div><button class="icon" title="关闭" @click="pricingModal = false"><X :size="19" /></button></header><form @submit.prevent="savePricingProfile"><div class="form-section"><div class="form-grid"><label>名称<input v-model.trim="pricingForm.name" required maxlength="200" placeholder="内部模型价格" /></label><label>提供方<input v-model.trim="pricingForm.provider" maxlength="200" placeholder="内部" /></label><label class="span-2">来源 URL<input v-model.trim="pricingForm.source_url" type="url" maxlength="2048" placeholder="https://…" /></label><label class="span-2">版本<input v-model.trim="pricingForm.source_version" maxlength="100" placeholder="custom-2026-01" /></label><label class="span-2">模型价格<textarea v-model.trim="pricingForm.prices" rows="7" required placeholder="gpt-5.6, 2, 8, 1, 0"></textarea></label></div></div><footer><button type="button" class="secondary" @click="pricingModal = false">取消</button><button class="primary" :disabled="saving">保存档案</button></footer></form></section></div>

  <div v-if="keyModal" class="modal-backdrop" @mousedown.self="keyModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="key-title"><header><div><h2 id="key-title">{{ editingKey ? '编辑客户端密钥' : '创建客户端密钥' }}</h2><p>设置客户端访问范围</p></div><button class="icon" title="关闭" @click="keyModal = false"><X :size="19" /></button></header><form @submit.prevent="saveKey"><div class="form-section"><div class="form-grid">
    <label class="span-2">名称<input v-model.trim="keyForm.name" required autofocus placeholder="Claude Code 工作站" /></label>
    <label class="span-2">分组<select v-model.number="keyForm.group_id" required><option :value="0" disabled>选择分组</option><option v-for="item in groups" :key="item.id" :value="item.id" :disabled="!item.enabled || !(item.upstream_ids || []).length">{{ item.name }}{{ !item.enabled ? '（停用）' : '' }}</option></select></label>
    <div class="route-preview span-2" aria-live="polite">
      <div class="route-preview-head"><strong>生效路由预览</strong><span v-if="keyRoutePreview.length">{{ keyRoutePreview.length }} 条候选</span></div>
      <div v-if="keyRoutePreview.length" class="route-preview-list">
        <div v-for="item in keyRoutePreview" :key="item.id" class="route-preview-row"><span class="priority">P{{ item.priority }}</span><strong>{{ item.name }}</strong><span class="status" :class="upstreamRouteTone(item)"><i></i>{{ upstreamRouteText(item) }}</span></div>
      </div>
      <p v-else>选择一个包含上游的分组后，这里会显示实际尝试顺序。</p>
    </div>
    <fieldset class="span-2"><legend>允许协议</legend><div class="check-row"><label v-for="protocol in UPSTREAM_PROTOCOLS" :key="protocol"><input v-model="keyForm.protocols" type="checkbox" :value="protocol" />{{ protocol }}</label></div></fieldset>
    <label class="span-2">允许模型 <span>逗号分隔；留空表示全部</span><textarea v-model="keyForm.models" rows="3" placeholder="gpt-5, claude-sonnet"></textarea></label>
    <label class="switch span-2"><input v-model="keyForm.enabled" type="checkbox" /><span></span>启用密钥</label>
  </div></div><footer><button type="button" class="secondary" @click="keyModal = false">取消</button><button class="primary" :disabled="saving">{{ editingKey ? '保存修改' : '创建密钥' }}</button></footer></form></section></div>

  <div v-if="keySimulationModal" class="modal-backdrop" @mousedown.self="closeKeySimulation"><section class="modal simulation-modal" role="dialog" aria-modal="true" aria-labelledby="simulation-title"><header><div><h2 id="simulation-title">模拟客户端请求</h2><p>{{ keySimulationTarget?.name }} · 实际走所属分组路由</p></div><button class="icon" title="关闭" @click="closeKeySimulation"><X :size="19" /></button></header><form @submit.prevent="runKeySimulation"><div class="form-section"><div class="form-grid"><label>协议<select v-model="keySimulationProtocol"><option v-for="protocol in keySimulationTarget?.protocols?.length ? keySimulationTarget.protocols : UPSTREAM_PROTOCOLS" :key="protocol" :value="protocol">{{ protocol }}</option></select></label><label>模型<input v-model.trim="keySimulationModel" required list="simulation-models" placeholder="例如 gpt-5.6" /><datalist id="simulation-models"><option v-for="model in keySimulationTarget?.models || []" :key="model" :value="model" /></datalist></label><p class="form-hint span-2">将发送一个最小 ping 请求，可能产生少量 Token 消耗；结果会计入请求日志。</p><div v-if="keySimulationResult" class="simulation-result span-2" :class="keySimulationResult.ok ? 'good' : 'bad'"><div><strong>{{ keySimulationResult.ok ? '请求成功' : '请求失败' }}</strong><span>{{ keySimulationResult.status ? `HTTP ${keySimulationResult.status}` : '未建立连接' }} · {{ fmtDuration(keySimulationResult.duration_ms) }}</span></div><p v-if="keySimulationResult.detail">{{ keySimulationResult.detail }}</p></div></div></div><footer><button type="button" class="secondary" @click="closeKeySimulation">取消</button><button class="primary" :disabled="keySimulationBusy"><LoaderCircle v-if="keySimulationBusy" class="spin" :size="16" /><Play v-else :size="16" />发送测试请求</button></footer></form></section></div>

  <div v-if="ccswitchModal" class="modal-backdrop ccswitch-backdrop" @mousedown.self="closeCCSwitch"><section class="modal ccswitch-modal" role="dialog" aria-modal="true" aria-labelledby="ccswitch-title"><header><div><h2 id="ccswitch-title">导入到 CCSwitch</h2><p>把当前客户端密钥配置到本机 CCSwitch</p></div><button class="icon" title="关闭" @click="closeCCSwitch"><X :size="19" /></button></header><form @submit.prevent="importToCCSwitch"><div class="form-section"><div class="form-stack">
    <label>客户端<select :value="ccswitchApp" @change="changeCCSwitchApp"><option value="claude">Claude Code</option><option value="codex">Codex</option><option value="gemini">Gemini CLI</option></select></label>
    <label>配置名称<input v-model.trim="ccswitchName" required placeholder="D-API Gateway" /></label>
    <label>模型 <span>可选，留空表示不限制</span><div class="inline-control"><select v-model="ccswitchModel"><option value="">不限制模型</option><option v-for="model in ccswitchModels" :key="model" :value="model">{{ model }}</option></select><button type="button" class="secondary" :disabled="fetchingCCSwitchModels" @click="fetchCCSwitchModels"><LoaderCircle v-if="fetchingCCSwitchModels" class="spin" :size="15" /><Download v-else :size="15" />获取模型</button></div></label>
    <label>完整客户端密钥 <span v-if="ccswitchSecret">已自动填充当前密钥</span><input v-model="ccswitchSecret" type="password" required autocomplete="off" placeholder="当前密钥不可用时可手动粘贴" /></label>
    <p class="form-hint ccswitch-warning">安全提示：当前 CCSwitch 协议需要将密钥交给本机导入器。请仅在受信任设备使用；导入完成后本页会立即清理密钥。网关地址：<code>{{ gatewayBaseURL }}</code></p>
  </div></div><footer><button type="button" class="secondary" @click="closeCCSwitch">取消</button><button class="primary"><Upload :size="16" />打开 CCSwitch</button></footer></form></section></div>

  <div v-if="revealedKey" class="modal-backdrop"><section class="modal secret-modal" role="dialog" aria-modal="true" aria-labelledby="secret-title"><header><div><h2 id="secret-title">客户端密钥已创建</h2><p>仅可在此时复制</p></div></header><div class="secret-body"><p>密钥已隐藏。点击下方区域或复制按钮后立即保存；关闭后无法再次获取。</p><div class="secret-value" title="点击复制密钥" @click="copySecret"><input class="secret-mask" type="password" :value="revealedKey" readonly autocomplete="off" aria-label="已隐藏的客户端密钥，点击复制" @keydown.enter.prevent="copySecret" /><button class="icon" title="复制密钥" @click.stop="copySecret"><Clipboard :size="17" /></button></div><button class="text-button config-copy-button" @click="copyClientConfig"><Copy :size="15" />复制 OpenAI / Anthropic 接入配置</button></div><footer><button v-if="createdKeyForImport" class="secondary" @click="openCCSwitchFromSecret"><Upload :size="16" />导入 CCSwitch</button><button class="primary" autofocus @click="closeSecret">我已保存</button></footer></section></div>

  <div v-if="channelModal" class="modal-backdrop" @mousedown.self="channelModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="channel-title"><header><div><h2 id="channel-title">添加通知渠道</h2><p>将运行状态发送到外部渠道</p></div><button class="icon" title="关闭" @click="channelModal = false"><X :size="19" /></button></header><form @submit.prevent="saveChannel"><div class="form-section"><div class="form-grid">
    <label>名称<input v-model.trim="channelForm.name" required autofocus placeholder="运维告警" /></label><label>类型<select v-model="channelForm.kind"><option value="email">邮件</option><option value="webhook">Webhook</option></select></label>
    <template v-if="channelForm.kind === 'webhook'"><label>平台<select v-model="channelForm.provider"><option value="">自动识别</option><option value="dingtalk">钉钉</option><option value="feishu">飞书 / Lark</option><option value="wecom">企业微信</option><option value="slack">Slack</option><option value="discord">Discord</option><option value="generic">通用 JSON</option></select></label><label>Webhook URL<input v-model.trim="channelForm.target" type="url" required placeholder="https://hooks.example.com/…" /></label></template>
    <template v-else><label class="span-2">收件地址<input v-model.trim="channelForm.target" type="email" required /></label><label>SMTP 主机<input v-model.trim="channelForm.smtp_host" required placeholder="smtp.example.com" /></label><label>端口<input v-model.number="channelForm.smtp_port" type="number" min="1" max="65535" required /></label><label>用户名<input v-model="channelForm.username" autocomplete="off" /></label><label>密码<input v-model="channelForm.password" type="password" autocomplete="new-password" /></label></template>
    <label class="switch span-2"><input v-model="channelForm.enabled" type="checkbox" /><span></span>启用渠道</label>
  </div></div><footer><button type="button" class="secondary" @click="channelModal = false">取消</button><button class="primary" :disabled="saving">添加渠道</button></footer></form></section></div>

  <div v-if="passwordModal" class="modal-backdrop" @mousedown.self="passwordModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="password-title"><header><div><h2 id="password-title">修改管理员密码</h2><p>修改后需要重新登录</p></div><button class="icon" title="关闭" @click="passwordModal = false"><X :size="19" /></button></header><form @submit.prevent="changePassword"><div class="form-section"><div class="form-stack">
    <label>当前密码<input v-model="passwordForm.current_password" type="password" autocomplete="current-password" required autofocus /></label>
    <label>新密码 <span>至少 12 个字符</span><input v-model="passwordForm.new_password" type="password" minlength="12" autocomplete="new-password" required /></label>
    <label>确认新密码<input v-model="passwordForm.confirm_password" type="password" minlength="12" autocomplete="new-password" required /></label>
  </div></div><footer><button type="button" class="secondary" @click="passwordModal = false">取消</button><button class="primary" :disabled="saving">修改并重新登录</button></footer></form></section></div>

  <div v-if="confirmDialog.show" class="modal-backdrop" @mousedown.self="settleConfirmation(false)"><section class="modal confirm-modal" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-message"><div class="confirm-body"><span class="confirm-icon"><AlertCircle :size="20" /></span><div><h2 id="confirm-title">{{ confirmDialog.title }}</h2><p id="confirm-message">{{ confirmDialog.message }}</p></div></div><footer><button class="secondary" @click="settleConfirmation(false)">取消</button><button class="danger-button" autofocus @click="settleConfirmation(true)">{{ confirmDialog.confirmLabel }}</button></footer></section></div>

  <Transition name="toast"><div v-if="toast.show" class="toast" :class="{ error: toast.error }" role="status"><AlertCircle v-if="toast.error" :size="17" /><Check v-else :size="17" />{{ toast.message }}</div></Transition>
</template>
