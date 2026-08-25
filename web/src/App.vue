<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  Activity, AlertCircle, Bell, Check, ChevronLeft, ChevronRight, CircleDollarSign,
  Clipboard, Gauge, KeyRound, LayoutDashboard, ListFilter, LoaderCircle, LogOut,
  Mail, Menu, Network, Pencil, Plus, RefreshCw, Search, Server, ShieldCheck,
  Trash2, Webhook, X,
} from 'lucide-vue-next'
import { ApiError, api, listOf } from './api'
import {
  COMMON_UPSTREAM_MODELS, DEFAULT_UPSTREAM_PROTOCOLS, UPSTREAM_PROTOCOLS, connectionTestText,
  modelsForPayload, parseModelList, setModelSelected, usesNewAPICredentials,
} from './upstream-form'

type View = 'dashboard' | 'upstreams' | 'keys' | 'logs' | 'usage' | 'channels'
type Json = Record<string, any>

interface Upstream {
  id: number
  name: string
  kind: 'newapi' | 'sub2api'
  base_url: string
  enabled: boolean
  priority: number
  protocols: string[]
  models: string[]
  models_locked?: boolean
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
  balance?: { status?: string; available?: number; used?: number; currency?: string; unlimited?: boolean; updated_at?: string }
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
}

interface RequestLog {
  id?: number
  request_id: string
  upstream_id?: number
  upstream_name?: string
  api_key_name?: string
  protocol: string
  model: string
  status_code: number
  duration_ms: number
  attempts: Array<{ upstream_id?: number; upstream_name?: string; status_code?: number; error?: string; duration_ms?: number }>
  usage?: { input_tokens?: number; output_tokens?: number; cached_input_tokens?: number }
  error_code?: string
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
  enabled: boolean
}

const navItems = [
  { id: 'dashboard' as View, label: '总览', icon: LayoutDashboard },
  { id: 'upstreams' as View, label: '上游', icon: Server },
  { id: 'keys' as View, label: '客户端密钥', icon: KeyRound },
  { id: 'logs' as View, label: '请求日志', icon: ListFilter },
  { id: 'usage' as View, label: '用量', icon: Gauge },
  { id: 'channels' as View, label: '通知', icon: Bell },
]

const auth = ref<'checking' | 'guest' | 'ready'>('checking')
const admin = ref<Json>({})
const loginForm = reactive({ username: '', password: '' })
const loginError = ref('')
const loginBusy = ref(false)
const view = ref<View>('dashboard')
const loading = ref(false)
const menuOpen = ref(false)
const toast = reactive({ show: false, message: '', error: false })
let toastTimer = 0

const dashboard = ref<Json>({})
const upstreams = ref<Upstream[]>([])
const keys = ref<ClientKey[]>([])
const logs = ref<RequestLog[]>([])
const usage = ref<Json>({})
const channels = ref<Channel[]>([])
const alertRules = ref<AlertRule[]>([])
const maxAttempts = ref(3)
const logFilter = reactive({ status: '', upstream_id: '', limit: 50, offset: 0 })
const expandedLog = ref<string | null>(null)

const upstreamModal = ref(false)
const editingUpstream = ref<number | null>(null)
const upstreamModelMode = ref<'auto' | 'manual'>('auto')
const testingUpstream = ref(false)
const connectionTest = ref<{ ok: boolean; message: string } | null>(null)
const upstreamFormElement = ref<HTMLFormElement | null>(null)
const upstreamForm = reactive({
  name: '', kind: 'newapi' as 'newapi' | 'sub2api', base_url: '', api_key: '', access_token: '', user_id: '',
  enabled: true, priority: 100, protocols: [...DEFAULT_UPSTREAM_PROTOCOLS] as string[], models: '', aliases: '', connect_timeout_ms: 5000,
  first_byte_timeout_ms: 60000, idle_timeout_ms: 300000, failure_threshold: 3, cooldown_seconds: 60, clear_balance_credentials: false,
})
const keyModal = ref(false)
const editingKey = ref<number | null>(null)
const keyForm = reactive({ name: '', enabled: true, protocols: ['chat'] as string[], models: '' })
const revealedKey = ref('')
const channelModal = ref(false)
const passwordModal = ref(false)
const passwordForm = reactive({ current_password: '', new_password: '', confirm_password: '' })
const channelForm = reactive({ name: '', kind: 'email' as 'email' | 'webhook', enabled: true, target: '', smtp_host: '', smtp_port: 587, username: '', password: '' })
const newRule = reactive({ event: 'low_balance', upstream_id: '', threshold: 5, window_seconds: 300, cooldown_seconds: 1800 })
const saving = ref(false)

watch(() => upstreamForm.kind, (kind) => {
  upstreamForm.access_token = ''
  upstreamForm.user_id = ''
  upstreamForm.clear_balance_credentials = kind === 'sub2api' && editingUpstream.value !== null
})

const title = computed(() => navItems.find((item) => item.id === view.value)?.label || '')
const dashUpstreams = computed<Upstream[]>(() => listOf<Upstream>((dashboard.value as Json).upstreams))
const shownDashUpstreams = computed(() => dashUpstreams.value.length ? dashUpstreams.value : upstreams.value.slice(0, 8))
const summary = computed(() => {
  const stats = dashboard.value.stats || dashboard.value.summary || dashboard.value
  const total = Number(stats.upstreams_total ?? stats.total_upstreams ?? upstreams.value.length)
  const healthy = Number(stats.upstreams_healthy ?? stats.healthy_upstreams ?? upstreams.value.filter((u) => u.health_status === 'healthy').length)
  return {
    total,
    healthy,
    requests: Number(stats.requests_24h ?? stats.request_count ?? 0),
    success: Number(stats.success_rate ?? 0),
    latency: Number(stats.avg_latency_ms ?? stats.average_latency_ms ?? 0),
  }
})
const usageRows = computed<Json[]>(() => {
  const raw = usage.value.daily ?? usage.value.items ?? usage.value.data ?? []
  return Array.isArray(raw) ? raw : []
})
const maxUsageRequests = computed(() => Math.max(1, ...usageRows.value.map((row) => Number(row.requests || 0))))
const usageTotals = computed(() => {
  const supplied = usage.value.totals || usage.value.summary
  if (supplied) return supplied
  return usageRows.value.reduce((sum, row) => ({
    requests: sum.requests + Number(row.requests || 0),
    input_tokens: sum.input_tokens + Number(row.input_tokens || 0),
    output_tokens: sum.output_tokens + Number(row.output_tokens || 0),
  }), { requests: 0, input_tokens: 0, output_tokens: 0 })
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
  history.replaceState(null, '', `#${next}`)
  await loadCurrent()
}

async function loadCurrent() {
  loading.value = true
  try {
    if (view.value === 'dashboard') {
      const [dash, ups] = await Promise.all([api.get<Json>('/api/admin/dashboard'), api.get('/api/admin/upstreams')])
      dashboard.value = dash || {}
      upstreams.value = listOf<Upstream>(ups)
    } else if (view.value === 'upstreams') upstreams.value = listOf<Upstream>(await api.get('/api/admin/upstreams'))
    else if (view.value === 'keys') keys.value = listOf<ClientKey>(await api.get('/api/admin/keys'))
    else if (view.value === 'logs') await loadLogs()
    else if (view.value === 'usage') usage.value = await api.get('/api/admin/usage?days=30')
    else {
      const [channelData, ruleData, settingsData, upstreamData] = await Promise.all([
        api.get('/api/admin/channels'), api.get('/api/admin/alert-rules'), api.get<Json>('/api/admin/settings'), api.get('/api/admin/upstreams'),
      ])
      channels.value = listOf<Channel>(channelData)
      alertRules.value = listOf<AlertRule>(ruleData)
      maxAttempts.value = Number(settingsData.max_attempts || 3)
      upstreams.value = listOf<Upstream>(upstreamData)
    }
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) auth.value = 'guest'
    else notify(errorMessage(error), true)
  } finally {
    loading.value = false
  }
}

async function loadLogs() {
  const query = new URLSearchParams({ limit: String(logFilter.limit), offset: String(logFilter.offset) })
  if (logFilter.status) query.set('status', logFilter.status)
  if (logFilter.upstream_id) query.set('upstream_id', logFilter.upstream_id)
  logs.value = listOf<RequestLog>(await api.get(`/api/admin/logs?${query}`))
}

function openUpstream(item?: Upstream) {
  editingUpstream.value = item?.id ?? null
  upstreamModelMode.value = item?.models_locked ? 'manual' : 'auto'
  connectionTest.value = null
  Object.assign(upstreamForm, item ? {
    name: item.name, kind: item.kind, base_url: item.base_url, api_key: '', access_token: '', user_id: '', enabled: item.enabled,
    priority: item.priority, protocols: [...(item.protocols || [])], models: (item.models || []).join(', '),
    aliases: Object.entries(item.model_aliases || {}).map(([from, to]) => `${from}=${to}`).join('\n'),
    connect_timeout_ms: item.connect_timeout_ms ?? 5000, first_byte_timeout_ms: item.first_byte_timeout_ms ?? 60000,
    idle_timeout_ms: item.idle_timeout_ms ?? 300000, failure_threshold: item.failure_threshold ?? 3,
    cooldown_seconds: item.cooldown_seconds ?? 60, clear_balance_credentials: false,
  } : {
    name: '', kind: 'newapi', base_url: '', api_key: '', access_token: '', user_id: '', enabled: true, priority: 100,
    protocols: [...DEFAULT_UPSTREAM_PROTOCOLS], models: '', aliases: '', connect_timeout_ms: 5000, first_byte_timeout_ms: 60000,
    idle_timeout_ms: 300000, failure_threshold: 3, cooldown_seconds: 60, clear_balance_credentials: false,
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
  return {
    ...upstreamForm,
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

async function testUpstreamConnection() {
  if (!upstreamFormElement.value?.reportValidity()) return
  testingUpstream.value = true
  connectionTest.value = null
  try {
    const result = await api.post<Json>('/api/admin/upstreams/test', { ...upstreamPayload(), id: editingUpstream.value || 0 })
    const ok = result.status === 'healthy'
    const message = connectionTestText(result)
    connectionTest.value = { ok, message }
    notify(message, !ok)
  } catch (error) {
    const message = errorMessage(error)
    connectionTest.value = { ok: false, message }
    notify(message, true)
  } finally { testingUpstream.value = false }
}

async function saveUpstream() {
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
  if (!confirm(`删除上游“${item.name}”？历史日志不会随之删除。`)) return
  try { await api.delete(`/api/admin/upstreams/${item.id}`); notify('上游已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

async function upstreamAction(item: Upstream, action: 'check' | 'balance' | 'models') {
  try {
    await api.post(`/api/admin/upstreams/${item.id}/${action}`)
    notify({ check: '连接检查完成', balance: '余额已刷新', models: '模型列表已刷新' }[action])
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) }
}

function openKey(item?: ClientKey) {
  editingKey.value = item?.id ?? null
  Object.assign(keyForm, item ? {
    name: item.name, enabled: item.enabled, protocols: [...(item.protocols || [])], models: (item.models || []).join(', '),
  } : { name: '', enabled: true, protocols: ['chat'], models: '' })
  keyModal.value = true
}

async function saveKey() {
  saving.value = true
  try {
    const payload = { ...keyForm, models: keyForm.models.split(',').map((v) => v.trim()).filter(Boolean) }
    const result: Json = editingKey.value
      ? await api.put(`/api/admin/keys/${editingKey.value}`, payload)
      : await api.post('/api/admin/keys', payload)
    keyModal.value = false
    if (!editingKey.value) revealedKey.value = result.key || result.api_key || result.secret || ''
    notify(editingKey.value ? '客户端密钥已更新' : '客户端密钥已创建')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function removeKey(item: ClientKey) {
  if (!confirm(`删除客户端密钥“${item.name}”？使用该密钥的请求将立即失败。`)) return
  try { await api.delete(`/api/admin/keys/${item.id}`); notify('客户端密钥已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
}

async function copySecret() {
  await navigator.clipboard.writeText(revealedKey.value)
  notify('密钥已复制')
}

async function saveChannel() {
  saving.value = true
  try {
    const config = channelForm.kind === 'webhook'
      ? { url: channelForm.target }
      : { to: channelForm.target, smtp_host: channelForm.smtp_host, smtp_port: channelForm.smtp_port, username: channelForm.username, password: channelForm.password }
    await api.post('/api/admin/channels', { name: channelForm.name, kind: channelForm.kind, enabled: channelForm.enabled, config })
    channelModal.value = false
    notify('通知渠道已添加')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function removeChannel(item: Channel) {
  if (!confirm(`删除通知渠道“${item.name}”？`)) return
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
      cooldown_seconds: Number(rule.cooldown_seconds), enabled: rule.enabled,
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
    })
    notify('上游告警覆盖已添加')
    await loadCurrent()
  } catch (error) { notify(errorMessage(error), true) }
}

async function removeRule(rule: AlertRule) {
  if (!rule.upstream_id || !confirm('删除这条上游告警覆盖？')) return
  try { await api.delete(`/api/admin/alert-rules/${rule.id}`); notify('告警覆盖已删除'); await loadCurrent() }
  catch (error) { notify(errorMessage(error), true) }
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
  return new Intl.NumberFormat('zh-CN', { notation: (value || 0) > 99999 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(value || 0)
}

function fmtBalance(upstream: Upstream) {
  const balance = upstream.balance
  if (!balance || balance.status === 'unsupported') return '不支持'
  if (balance.unlimited) return '无限额'
  if (balance.available == null) return '未知'
  return `${balance.currency || '$'} ${balance.available.toFixed(2)}`
}

onMounted(async () => {
  try {
    admin.value = await api.get('/api/admin/me')
    auth.value = 'ready'
    const hash = location.hash.slice(1) as View
    view.value = navItems.some((item) => item.id === hash) ? hash : 'dashboard'
    await loadCurrent()
  } catch { auth.value = 'guest' }
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
        <p class="eyebrow">管理控制台</p>
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
      <Network :size="50" />
      <p>按优先级转发。异常自动切换。状态始终清楚。</p>
      <div class="route-line"><i></i><i></i><i></i></div>
    </aside>
  </main>

  <div v-else class="app-shell">
    <aside class="sidebar" :class="{ open: menuOpen }">
      <div class="sidebar-head"><span class="brand-mark"><Network :size="20" /></span><strong>D-API</strong><button class="icon mobile-close" title="关闭菜单" @click="menuOpen = false"><X :size="19" /></button></div>
      <nav aria-label="主导航">
        <button v-for="item in navItems" :key="item.id" :class="{ active: view === item.id }" @click="go(item.id)">
          <component :is="item.icon" :size="18" />{{ item.label }}
        </button>
      </nav>
      <div class="sidebar-foot">
        <div><span class="avatar">{{ String(admin.username || 'A').slice(0, 1).toUpperCase() }}</span><span><strong>{{ admin.username || '管理员' }}</strong><small>管理员</small></span></div>
        <button class="icon" title="修改密码" @click="passwordModal = true"><ShieldCheck :size="18" /></button>
        <button class="icon" title="退出登录" @click="logout"><LogOut :size="18" /></button>
      </div>
    </aside>
    <button v-if="menuOpen" class="menu-backdrop" aria-label="关闭菜单" @click="menuOpen = false"></button>

    <main class="workspace">
      <header class="topbar">
        <button class="icon menu-button" title="打开菜单" @click="menuOpen = true"><Menu :size="20" /></button>
        <div><p class="eyebrow">D-API / {{ title }}</p><h1>{{ title }}</h1></div>
        <button class="secondary refresh" :disabled="loading" @click="loadCurrent"><RefreshCw :class="{ spin: loading }" :size="16" />刷新</button>
      </header>

      <div class="content">
        <section v-if="view === 'dashboard'" class="view-stack">
          <div class="metric-grid">
            <article><span class="metric-icon green"><Server :size="19" /></span><div><small>可用上游</small><strong>{{ summary.healthy }}<em>/ {{ summary.total }}</em></strong></div></article>
            <article><span class="metric-icon ink"><Activity :size="19" /></span><div><small>24 小时请求</small><strong>{{ fmtNumber(summary.requests) }}</strong></div></article>
            <article><span class="metric-icon blue"><Check :size="19" /></span><div><small>成功率</small><strong>{{ summary.success.toFixed(1) }}<em>%</em></strong></div></article>
            <article><span class="metric-icon amber"><Gauge :size="19" /></span><div><small>平均延迟</small><strong>{{ fmtNumber(summary.latency) }}<em>ms</em></strong></div></article>
          </div>
          <section class="panel">
            <div class="panel-head"><div><h2>上游状态</h2><p>当前路由顺序与连接状态</p></div><button class="text-button" @click="go('upstreams')">查看全部 <ChevronRight :size="15" /></button></div>
            <div class="table-wrap">
              <table>
                <thead><tr><th>优先级</th><th>上游</th><th>状态</th><th>协议</th><th>余额</th><th>最后检查</th></tr></thead>
                <tbody>
                  <tr v-for="item in shownDashUpstreams" :key="item.id">
                    <td><span class="priority">{{ item.priority }}</span></td>
                    <td><strong>{{ item.name }}</strong><small>{{ item.kind }} · {{ item.base_url }}</small></td>
                    <td><span class="status" :class="statusTone(item.health_status)"><i></i>{{ statusText(item.health_status) }}</span></td>
                    <td><span class="tag" v-for="protocol in item.protocols" :key="protocol">{{ protocol }}</span></td>
                    <td>{{ fmtBalance(item) }}</td><td class="muted nowrap">{{ fmtDate(item.last_check_at) }}</td>
                  </tr>
                  <tr v-if="!shownDashUpstreams.length"><td colspan="6"><div class="empty">暂无上游，先创建一个路由目标。</div></td></tr>
                </tbody>
              </table>
            </div>
          </section>
        </section>

        <section v-else-if="view === 'upstreams'" class="view-stack">
          <div class="action-row"><p>{{ upstreams.length }} 个上游，数字越小优先级越高。</p><button class="primary" @click="openUpstream()"><Plus :size="17" />添加上游</button></div>
          <section class="panel table-panel"><div class="table-wrap"><table>
            <thead><tr><th>顺序</th><th>上游</th><th>连接</th><th>能力</th><th>余额</th><th>熔断</th><th class="right">操作</th></tr></thead>
            <tbody>
              <tr v-for="item in upstreams" :key="item.id" :class="{ subdued: !item.enabled }">
                <td><span class="priority">{{ item.priority }}</span></td>
                <td><strong>{{ item.name }}</strong><small>{{ item.kind }} · {{ item.base_url }}</small></td>
                <td><span class="status" :class="statusTone(item.health_status)"><i></i>{{ statusText(item.health_status) }}</span><small v-if="item.last_error" class="truncate" :title="item.last_error">{{ item.last_error }}</small></td>
                <td><div class="tag-row"><span class="tag" v-for="protocol in item.protocols" :key="protocol">{{ protocol }}</span></div><small>{{ item.models?.length || 0 }} 个模型</small></td>
                <td><strong class="balance">{{ fmtBalance(item) }}</strong><small>{{ fmtDate(item.balance?.updated_at) }}</small></td>
                <td><span v-if="item.circuit_open_until" class="status bad">至 {{ fmtDate(item.circuit_open_until) }}</span><small v-else>{{ item.consecutive_failures || 0 }} / {{ item.failure_threshold }} 次失败</small></td>
                <td><div class="row-actions">
                  <button class="icon" title="检查连接" @click="upstreamAction(item, 'check')"><Activity :size="16" /></button>
                  <button class="icon" title="刷新余额" @click="upstreamAction(item, 'balance')"><CircleDollarSign :size="16" /></button>
                  <button class="icon" title="刷新模型" @click="upstreamAction(item, 'models')"><RefreshCw :size="16" /></button>
                  <button class="icon" title="编辑" @click="openUpstream(item)"><Pencil :size="16" /></button>
                  <button class="icon danger" title="删除" @click="removeUpstream(item)"><Trash2 :size="16" /></button>
                </div></td>
              </tr>
              <tr v-if="!upstreams.length"><td colspan="7"><div class="empty">还没有配置上游。</div></td></tr>
            </tbody>
          </table></div></section>
        </section>

        <section v-else-if="view === 'keys'" class="view-stack">
          <div class="action-row"><p>为每个客户端分配独立密钥与模型权限。</p><button class="primary" @click="openKey()"><Plus :size="17" />创建密钥</button></div>
          <section class="panel table-panel"><div class="table-wrap"><table>
            <thead><tr><th>名称</th><th>密钥前缀</th><th>状态</th><th>协议</th><th>模型限制</th><th>最后使用</th><th class="right">操作</th></tr></thead>
            <tbody>
              <tr v-for="item in keys" :key="item.id" :class="{ subdued: !item.enabled }">
                <td><strong>{{ item.name }}</strong><small>创建于 {{ fmtDate(item.created_at) }}</small></td>
                <td><code>{{ item.prefix || item.key_prefix || '—' }}••••••••</code></td>
                <td><span class="status" :class="item.enabled ? 'good' : 'warn'"><i></i>{{ item.enabled ? '启用' : '停用' }}</span></td>
                <td><span class="tag" v-for="protocol in item.protocols" :key="protocol">{{ protocol }}</span><span v-if="!item.protocols?.length" class="muted">全部</span></td>
                <td>{{ item.models?.length ? `${item.models.length} 个模型` : '全部模型' }}</td><td class="muted">{{ fmtDate(item.last_used_at) }}</td>
                <td><div class="row-actions"><button class="icon" title="编辑" @click="openKey(item)"><Pencil :size="16" /></button><button class="icon danger" title="删除" @click="removeKey(item)"><Trash2 :size="16" /></button></div></td>
              </tr>
              <tr v-if="!keys.length"><td colspan="7"><div class="empty">还没有客户端密钥。</div></td></tr>
            </tbody>
          </table></div></section>
        </section>

        <section v-else-if="view === 'logs'" class="view-stack">
          <form class="filterbar" @submit.prevent="logFilter.offset = 0; loadLogs()">
            <label><span>状态</span><select v-model="logFilter.status"><option value="">全部</option><option value="success">成功</option><option value="error">失败</option><option value="429">429</option><option value="5xx">5xx</option></select></label>
            <label><span>上游</span><select v-model="logFilter.upstream_id"><option value="">全部</option><option v-for="item in upstreams" :value="String(item.id)" :key="item.id">{{ item.name }}</option></select></label>
            <button class="secondary"><Search :size="16" />筛选</button>
          </form>
          <section class="panel table-panel"><div class="table-wrap"><table>
            <thead><tr><th>时间</th><th>请求</th><th>协议 / 模型</th><th>结果</th><th>上游</th><th>耗时</th><th>Token</th><th></th></tr></thead>
            <tbody>
              <template v-for="item in logs" :key="item.request_id">
                <tr class="clickable" @click="expandedLog = expandedLog === item.request_id ? null : item.request_id">
                  <td class="nowrap">{{ fmtDate(item.created_at) }}</td><td><code>{{ item.request_id.slice(0, 12) }}</code><small>{{ item.api_key_name || '未知客户端' }}</small></td>
                  <td><strong>{{ item.protocol }}</strong><small>{{ item.model || '—' }}</small></td>
                  <td><span class="status" :class="item.status_code < 400 ? 'good' : 'bad'"><i></i>{{ item.status_code }}</span><small v-if="item.error_code">{{ item.error_code }}</small></td>
                  <td>{{ item.upstream_name || (item.upstream_id ? `#${item.upstream_id}` : '—') }}</td><td>{{ fmtNumber(item.duration_ms) }} ms</td>
                  <td>{{ item.usage?.input_tokens == null ? '未知' : fmtNumber((item.usage.input_tokens || 0) + (item.usage.output_tokens || 0)) }}</td>
                  <td><ChevronRight :size="16" :class="{ rotate: expandedLog === item.request_id }" /></td>
                </tr>
                <tr v-if="expandedLog === item.request_id" class="attempt-row"><td colspan="8"><div class="attempts">
                  <div class="attempt-head"><strong>切换链</strong><span>{{ item.attempts?.length || 0 }} 次尝试</span></div>
                  <ol v-if="item.attempts?.length"><li v-for="(attempt, index) in item.attempts" :key="index"><span>{{ index + 1 }}</span><strong>{{ attempt.upstream_name || `上游 #${attempt.upstream_id}` }}</strong><code>{{ attempt.status_code || 'ERR' }}</code><small>{{ attempt.duration_ms || 0 }} ms</small><em>{{ attempt.error || '已响应' }}</em></li></ol>
                  <p v-else class="muted">未记录上游尝试。</p>
                </div></td></tr>
              </template>
              <tr v-if="!logs.length"><td colspan="8"><div class="empty">暂无符合条件的日志。</div></td></tr>
            </tbody>
          </table></div><div class="pagination"><span>每页 {{ logFilter.limit }} 条</span><div><button class="icon" title="上一页" :disabled="logFilter.offset === 0" @click="logFilter.offset = Math.max(0, logFilter.offset - logFilter.limit); loadLogs()"><ChevronLeft :size="17" /></button><button class="icon" title="下一页" :disabled="logs.length < logFilter.limit" @click="logFilter.offset += logFilter.limit; loadLogs()"><ChevronRight :size="17" /></button></div></div></section>
        </section>

        <section v-else-if="view === 'usage'" class="view-stack">
          <div class="metric-grid three">
            <article><span class="metric-icon ink"><Activity :size="19" /></span><div><small>30 天请求</small><strong>{{ fmtNumber(usageTotals.requests) }}</strong></div></article>
            <article><span class="metric-icon green"><ChevronRight :size="19" /></span><div><small>输入 Token</small><strong>{{ fmtNumber(usageTotals.input_tokens) }}</strong></div></article>
            <article><span class="metric-icon blue"><ChevronLeft :size="19" /></span><div><small>输出 Token</small><strong>{{ fmtNumber(usageTotals.output_tokens) }}</strong></div></article>
          </div>
          <section class="panel usage-panel"><div class="panel-head"><div><h2>请求趋势</h2><p>近 30 天本地记录</p></div></div>
            <div v-if="usageRows.length" class="bar-chart" aria-label="每日请求量柱状图"><div v-for="row in usageRows" :key="row.day" class="bar-column" :title="`${row.day}: ${row.requests || 0} 次请求`"><span>{{ fmtNumber(row.requests) }}</span><i :style="{ height: `${Math.max(3, Number(row.requests || 0) / maxUsageRequests * 100)}%` }"></i><small>{{ String(row.day).slice(5) }}</small></div></div>
            <div v-else class="empty">暂无用量数据。</div>
          </section>
          <section class="panel table-panel"><div class="table-wrap"><table><thead><tr><th>日期</th><th>请求</th><th>成功</th><th>输入 Token</th><th>输出 Token</th><th>缓存 Token</th></tr></thead><tbody><tr v-for="row in [...usageRows].reverse()" :key="row.day"><td><strong>{{ row.day }}</strong></td><td>{{ fmtNumber(row.requests) }}</td><td>{{ fmtNumber(row.successes) }}</td><td>{{ fmtNumber(row.input_tokens) }}</td><td>{{ fmtNumber(row.output_tokens) }}</td><td>{{ fmtNumber(row.cached_input_tokens) }}</td></tr></tbody></table></div></section>
        </section>

        <section v-else class="view-stack">
          <div class="action-row"><p>上游状态与安全事件将发送到已启用渠道。</p><button class="primary" @click="Object.assign(channelForm, { name: '', kind: 'email', enabled: true, target: '', smtp_host: '', smtp_port: 587, username: '', password: '' }); channelModal = true"><Plus :size="17" />添加渠道</button></div>
          <div class="channel-grid">
            <article v-for="item in channels" :key="item.id" class="channel-card"><span class="channel-icon"><Mail v-if="item.kind === 'email'" :size="21" /><Webhook v-else :size="21" /></span><div><strong>{{ item.name }}</strong><small>{{ item.kind === 'email' ? '邮件' : 'Webhook' }} · {{ item.enabled ? '已启用' : '已停用' }}</small></div><span class="status" :class="item.enabled ? 'good' : 'warn'"><i></i>{{ item.enabled ? '启用' : '停用' }}</span><button class="icon danger" title="删除" @click="removeChannel(item)"><Trash2 :size="16" /></button></article>
            <div v-if="!channels.length" class="empty panel">还没有通知渠道。</div>
          </div>
          <section class="panel settings-strip"><div><h2>路由设置</h2><p>单次请求最多尝试的上游数量</p></div><label>最大尝试次数<input v-model.number="maxAttempts" type="number" min="1" max="5" /></label><button class="secondary" @click="saveSettings"><Check :size="16" />保存</button></section>
          <section class="panel table-panel"><div class="panel-head"><div><h2>告警规则</h2><p>全局默认规则可直接调整；上游规则会覆盖同类默认值</p></div></div>
            <div class="rule-add"><select v-model="newRule.event"><option v-for="event in ['low_balance','balance_unavailable','error_rate','latency','client_error_rate']" :key="event" :value="event">{{ alertEventText(event) }}</option></select><select v-model="newRule.upstream_id"><option value="">选择上游</option><option v-for="item in upstreams" :key="item.id" :value="String(item.id)">{{ item.name }}</option></select><input v-model.number="newRule.threshold" type="number" min="0" step="0.1" title="阈值" /><button class="secondary" @click="addRule"><Plus :size="16" />添加覆盖</button></div>
            <div class="table-wrap"><table><thead><tr><th>事件</th><th>范围</th><th>阈值</th><th>窗口（秒）</th><th>冷却（秒）</th><th>启用</th><th></th></tr></thead><tbody>
              <tr v-for="rule in alertRules" :key="rule.id"><td><strong>{{ alertEventText(rule.event) }}</strong></td><td>{{ upstreamName(rule.upstream_id) }}</td><td><input v-model.number="rule.threshold" class="table-input" type="number" min="0" step="0.1" /></td><td><input v-model.number="rule.window_seconds" class="table-input" type="number" min="60" /></td><td><input v-model.number="rule.cooldown_seconds" class="table-input" type="number" min="60" /></td><td><label class="switch"><input v-model="rule.enabled" type="checkbox" /><span></span></label></td><td class="right"><button class="icon" title="保存规则" @click="saveRule(rule)"><Check :size="16" /></button><button v-if="rule.upstream_id" class="icon danger" title="删除覆盖" @click="removeRule(rule)"><Trash2 :size="16" /></button></td></tr>
            </tbody></table></div>
          </section>
        </section>
      </div>
    </main>
  </div>

  <div v-if="upstreamModal" class="modal-backdrop" @mousedown.self="upstreamModal = false">
    <section class="modal wide-modal" role="dialog" aria-modal="true" aria-labelledby="upstream-title">
      <header><div><p class="eyebrow">路由目标</p><h2 id="upstream-title">{{ editingUpstream ? '编辑上游' : '添加上游' }}</h2></div><button class="icon" title="关闭" @click="upstreamModal = false"><X :size="19" /></button></header>
      <form ref="upstreamFormElement" @submit.prevent="saveUpstream" @input="connectionTest = null">
        <div class="form-section"><h3>基本信息</h3><div class="form-grid">
          <label>名称<input v-model.trim="upstreamForm.name" required placeholder="生产主线路" /></label>
          <label>类型<select v-model="upstreamForm.kind"><option value="newapi">NewAPI</option><option value="sub2api">Sub2API</option></select></label>
          <label class="span-2">Base URL<input v-model.trim="upstreamForm.base_url" type="url" required placeholder="https://api.example.com" /></label>
          <label>API Key<input v-model="upstreamForm.api_key" type="password" :required="!editingUpstream" :placeholder="editingUpstream ? '留空表示不修改' : 'sk-…'" autocomplete="off" /></label>
          <label>优先级<input v-model.number="upstreamForm.priority" type="number" min="0" required /></label>
          <template v-if="usesNewAPICredentials(upstreamForm.kind)">
            <label>Access Token <span>可选</span><input v-model="upstreamForm.access_token" type="password" placeholder="用于余额查询" autocomplete="off" /></label>
            <label>User ID <span>可选</span><input v-model.trim="upstreamForm.user_id" placeholder="用于余额查询" /></label>
            <label v-if="editingUpstream" class="switch span-2"><input v-model="upstreamForm.clear_balance_credentials" type="checkbox" /><span></span>清除已保存的 Access Token 与 User ID</label>
          </template>
          <label class="switch span-2"><input v-model="upstreamForm.enabled" type="checkbox" /><span></span>启用该上游</label>
        </div></div>
        <div class="form-section"><h3>能力与模型</h3><div class="form-grid">
          <fieldset class="span-2"><legend>协议 <span>新建时默认全选</span></legend><div class="check-row"><label v-for="protocol in UPSTREAM_PROTOCOLS" :key="protocol"><input v-model="upstreamForm.protocols" type="checkbox" :value="protocol" />{{ protocol }}</label></div></fieldset>
          <fieldset class="span-2"><legend>模型策略</legend><div class="check-row">
            <label><input v-model="upstreamModelMode" type="radio" value="auto" />自动发现 / 不限制（默认）</label>
            <label><input v-model="upstreamModelMode" type="radio" value="manual" />指定模型</label>
          </div></fieldset>
          <template v-if="upstreamModelMode === 'manual'">
            <fieldset class="span-2"><legend>常用模型</legend><div class="check-row"><label v-for="model in COMMON_UPSTREAM_MODELS" :key="model"><input type="checkbox" :checked="parseModelList(upstreamForm.models).includes(model)" @change="selectCommonModel(model, $event)" />{{ model }}</label></div></fieldset>
            <label class="span-2">模型列表 <span>逗号分隔</span><textarea v-model="upstreamForm.models" rows="2" placeholder="gpt-5, claude-sonnet-4"></textarea></label>
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
        <footer><span v-if="connectionTest" class="test-result" :class="connectionTest.ok ? 'good' : 'bad'">{{ connectionTest.message }}</span><button type="button" class="secondary" :disabled="saving || testingUpstream" @click="testUpstreamConnection"><LoaderCircle v-if="testingUpstream" class="spin" :size="16" /><Activity v-else :size="16" />测试连通性</button><button type="button" class="secondary" @click="upstreamModal = false">取消</button><button class="primary" :disabled="saving || testingUpstream"><LoaderCircle v-if="saving" class="spin" :size="16" />保存上游</button></footer>
      </form>
    </section>
  </div>

  <div v-if="keyModal" class="modal-backdrop" @mousedown.self="keyModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="key-title"><header><div><p class="eyebrow">访问控制</p><h2 id="key-title">{{ editingKey ? '编辑客户端密钥' : '创建客户端密钥' }}</h2></div><button class="icon" title="关闭" @click="keyModal = false"><X :size="19" /></button></header><form @submit.prevent="saveKey"><div class="form-section"><div class="form-grid">
    <label class="span-2">名称<input v-model.trim="keyForm.name" required placeholder="Claude Code · 工作站" /></label>
    <fieldset class="span-2"><legend>允许协议</legend><div class="check-row"><label v-for="protocol in ['chat', 'responses', 'messages']" :key="protocol"><input v-model="keyForm.protocols" type="checkbox" :value="protocol" />{{ protocol }}</label></div></fieldset>
    <label class="span-2">允许模型 <span>逗号分隔；留空表示全部</span><textarea v-model="keyForm.models" rows="3" placeholder="gpt-5, claude-sonnet"></textarea></label>
    <label class="switch span-2"><input v-model="keyForm.enabled" type="checkbox" /><span></span>启用密钥</label>
  </div></div><footer><button type="button" class="secondary" @click="keyModal = false">取消</button><button class="primary" :disabled="saving">{{ editingKey ? '保存修改' : '创建密钥' }}</button></footer></form></section></div>

  <div v-if="revealedKey" class="modal-backdrop"><section class="modal secret-modal" role="dialog" aria-modal="true" aria-labelledby="secret-title"><header><div><p class="eyebrow">仅显示一次</p><h2 id="secret-title">客户端密钥已创建</h2></div></header><div class="secret-body"><p>关闭后无法再次查看此密钥。</p><div class="secret-value"><code>{{ revealedKey }}</code><button class="icon" title="复制密钥" @click="copySecret"><Clipboard :size="17" /></button></div></div><footer><button class="primary" @click="revealedKey = ''">我已保存</button></footer></section></div>

  <div v-if="channelModal" class="modal-backdrop" @mousedown.self="channelModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="channel-title"><header><div><p class="eyebrow">告警发送</p><h2 id="channel-title">添加通知渠道</h2></div><button class="icon" title="关闭" @click="channelModal = false"><X :size="19" /></button></header><form @submit.prevent="saveChannel"><div class="form-section"><div class="form-grid">
    <label>名称<input v-model.trim="channelForm.name" required placeholder="运维告警" /></label><label>类型<select v-model="channelForm.kind"><option value="email">邮件</option><option value="webhook">Webhook</option></select></label>
    <template v-if="channelForm.kind === 'webhook'"><label class="span-2">Webhook URL<input v-model.trim="channelForm.target" type="url" required placeholder="https://hooks.example.com/…" /></label></template>
    <template v-else><label class="span-2">收件地址<input v-model.trim="channelForm.target" type="email" required /></label><label>SMTP 主机<input v-model.trim="channelForm.smtp_host" required placeholder="smtp.example.com" /></label><label>端口<input v-model.number="channelForm.smtp_port" type="number" min="1" max="65535" required /></label><label>用户名<input v-model="channelForm.username" autocomplete="off" /></label><label>密码<input v-model="channelForm.password" type="password" autocomplete="new-password" /></label></template>
    <label class="switch span-2"><input v-model="channelForm.enabled" type="checkbox" /><span></span>启用渠道</label>
  </div></div><footer><button type="button" class="secondary" @click="channelModal = false">取消</button><button class="primary" :disabled="saving">添加渠道</button></footer></form></section></div>

  <div v-if="passwordModal" class="modal-backdrop" @mousedown.self="passwordModal = false"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="password-title"><header><div><p class="eyebrow">账户安全</p><h2 id="password-title">修改管理员密码</h2></div><button class="icon" title="关闭" @click="passwordModal = false"><X :size="19" /></button></header><form @submit.prevent="changePassword"><div class="form-section"><div class="form-stack">
    <label>当前密码<input v-model="passwordForm.current_password" type="password" autocomplete="current-password" required /></label>
    <label>新密码 <span>至少 12 个字符</span><input v-model="passwordForm.new_password" type="password" minlength="12" autocomplete="new-password" required /></label>
    <label>确认新密码<input v-model="passwordForm.confirm_password" type="password" minlength="12" autocomplete="new-password" required /></label>
  </div></div><footer><button type="button" class="secondary" @click="passwordModal = false">取消</button><button class="primary" :disabled="saving">修改并重新登录</button></footer></form></section></div>

  <Transition name="toast"><div v-if="toast.show" class="toast" :class="{ error: toast.error }" role="status"><AlertCircle v-if="toast.error" :size="17" /><Check v-else :size="17" />{{ toast.message }}</div></Transition>
</template>
