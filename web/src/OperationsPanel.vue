<script setup lang="ts">
import { computed, nextTick, onMounted, onBeforeUnmount, ref } from 'vue'
import { useCursorPage } from './useCursorPage'
import { api } from './api'
import { vChangeGlow, retireOverlay } from './motion'
interface Metrics {
 cleanup?: { started_at: string; skipped: boolean; tables: { table: string; deleted: number; budget_exhausted: boolean; failed: boolean }[] }
 gateway?: { transport_cache_entries?: number; transport_cache_limit?: number; buffered_response_bytes?: number; buffered_response_limit?: number; log_fallback_active?: number; log_fallback_waiting?: number; log_fallback_count?: number; log_fallback_rejected?: number; log_fallback_wait_ms?: number; log_fallback_duration_ms?: number; active_requests: number; buffered_request_bytes: number; buffered_request_limit: number; request_log_queue: number; dropped_request_logs: number }
 database?: { in_use: number; open_connections: number; wait_count: number; wait_duration_ms: number }
 notifications?: { pending: number; dead: number }
}
interface DeadJob { id: number; channel_name: string; attempts: number; last_error: string; dead_at: string; retryable: boolean }
const metrics = ref<Metrics>({}), statsBusy = ref(false), statsError = ref(''), retrying = ref<number | null>(null), actionError = ref(''), message = ref('')
const deadPages = useCursorPage<DeadJob>()
const { items: jobs, page: pageIndex, nextCursor, busy: jobsBusy, error: jobsError, loaded } = deadPages
const busy = computed(() => statsBusy.value || jobsBusy.value || retrying.value !== null)
const cleanupSummary = computed(() => {
 const report = metrics.value.cleanup
 if (!report?.tables?.length) return ''
 const deleted = report.tables.reduce((sum, item) => sum + item.deleted, 0)
 const incomplete = report.tables.some(item => item.budget_exhausted || item.failed)
 return `最近清理 ${deleted} 条历史记录${incomplete ? '，部分任务未完成，将在下一轮继续' : ''}`
})
let controller: AbortController | null = null
const actionController = new AbortController()
const messageElement = ref<HTMLElement | null>(null)
function leaveJob(element: Element) { (element as HTMLElement).style.width = `${element.getBoundingClientRect().width}px`; retireOverlay(element) }
async function loadStats() {
 controller?.abort(); const current = new AbortController(); controller = current; statsBusy.value = true; statsError.value = ''
 try {
  const stats = await api.get<Metrics>('/api/admin/metrics', { signal: current.signal })
  if (!current.signal.aborted && controller === current) metrics.value = stats
 } catch (failure) { if (!current.signal.aborted) statsError.value = failure instanceof Error ? failure.message : '状态加载失败' }
 finally { if (controller === current) statsBusy.value = false }
}
async function loadJobs(target = jobsError.value ? deadPages.attemptedPage.value : pageIndex.value) {
 try { await deadPages.load('/api/admin/notifications/dead', target) } catch { /* Persistent error and retry stay beside the list. */ }
}
async function load() { await Promise.all([loadStats(), loadJobs()]) }
async function retry(job: DeadJob) {
 if (retrying.value !== null) return
 retrying.value = job.id; actionError.value = ''; message.value = ''
 try {
  await api.post(`/api/admin/notifications/${job.id}/retry`, undefined, { signal: actionController.signal })
  if (actionController.signal.aborted) return
  jobs.value = jobs.value.filter(item => item.id !== job.id)
  message.value = `通知 #${job.id} 已加入发送队列，可在待发送数量中查看进度。`
  await nextTick(); messageElement.value?.focus({ preventScroll: true })
  await Promise.all([loadStats(), loadJobs(jobs.value.length === 0 ? Math.max(0, pageIndex.value - 1) : pageIndex.value)])
 } catch (failure) { if (!actionController.signal.aborted) actionError.value = failure instanceof Error ? failure.message : '重试失败' }
 finally { retrying.value = null }
}
onMounted(load)
onBeforeUnmount(() => { controller?.abort(); actionController.abort() })
</script>
<template>
 <section class="operations panel">
  <div class="panel-head"><div><h2>运行状态与通知重试</h2><p>累计值在进程重启后归零；重试将重新发送通知。</p></div><button class="secondary" :disabled="busy" @click="load">刷新状态</button></div>
  <p v-if="statsError" role="alert">运行状态加载失败：{{ statsError }} <button class="text-button" :disabled="statsBusy" @click="loadStats">重试状态加载</button></p>
  <p v-if="actionError" role="alert">{{ actionError }}</p><p v-if="message" ref="messageElement" tabindex="-1" role="status">{{ message }}</p>
  <dl class="metrics" :aria-busy="statsBusy">
   <div><dt>连接配置缓存 / 上限</dt><dd>{{ metrics.gateway?.transport_cache_entries ?? '—' }} / {{ metrics.gateway?.transport_cache_limit ?? '—' }}</dd></div>
   <div><dt>进行中请求</dt><dd>{{ metrics.gateway?.active_requests ?? '—' }}</dd></div>
   <div><dt>请求体预留 / 上限（MiB）</dt><dd>{{ metrics.gateway ? (metrics.gateway.buffered_request_bytes / 1048576).toFixed(1) + ' / ' + (metrics.gateway.buffered_request_limit / 1048576).toFixed(0) : '—' }}</dd></div>
   <div><dt>响应缓冲 / 上限（MiB）</dt><dd>{{ metrics.gateway?.buffered_response_bytes == null ? '—' : (metrics.gateway.buffered_response_bytes / 1048576).toFixed(1) + ' / ' + ((metrics.gateway.buffered_response_limit ?? 0) / 1048576).toFixed(0) }}</dd></div>
   <div><dt>日志同步写入 / 等待</dt><dd>{{ metrics.gateway?.log_fallback_active ?? '—' }} / {{ metrics.gateway?.log_fallback_waiting ?? '—' }}</dd></div>
   <div><dt>日志同步回退 / 容量拒绝</dt><dd>{{ metrics.gateway?.log_fallback_count ?? '—' }} / {{ metrics.gateway?.log_fallback_rejected ?? '—' }}</dd></div>
   <div><dt>日志回退累计等待 / 总耗时（毫秒）</dt><dd>{{ metrics.gateway?.log_fallback_wait_ms ?? '—' }} / {{ metrics.gateway?.log_fallback_duration_ms ?? '—' }}</dd></div>
   <div><dt>日志排队 / 累计丢弃</dt><dd>{{ metrics.gateway?.request_log_queue ?? '—' }} / {{ metrics.gateway?.dropped_request_logs ?? '—' }}</dd></div>
   <div><dt>数据库使用 / 已打开连接</dt><dd>{{ metrics.database?.in_use ?? '—' }} / {{ metrics.database?.open_connections ?? '—' }}</dd></div>
   <div><dt>数据库累计等待次数 / 毫秒</dt><dd>{{ metrics.database?.wait_count ?? '—' }} / {{ metrics.database?.wait_duration_ms ?? '—' }}</dd></div>
   <div><dt>待发送 / 已停止重试</dt><dd v-change-glow="`${metrics.notifications?.pending}/${metrics.notifications?.dead}`">{{ metrics.notifications?.pending ?? '—' }} / {{ metrics.notifications?.dead ?? '—' }}</dd></div>
  </dl>
  <p v-if="cleanupSummary">{{ cleanupSummary }}</p>
  <p v-if="jobsError" role="alert">通知列表加载失败：{{ jobsError }}。{{ loaded ? '当前保留上次结果。' : '' }} <button class="text-button" :disabled="jobsBusy" @click="loadJobs()">重试列表加载</button></p>
  <p v-if="jobsBusy && !loaded" role="status">正在加载通知…</p>
  <div class="table-wrap" :aria-busy="jobsBusy"><table><thead><tr><th>任务</th><th>渠道</th><th>尝试次数</th><th>最后错误</th><th>停止时间</th><th>操作</th></tr></thead><TransitionGroup name="task-row" tag="tbody" @before-leave="leaveJob">
   <tr v-for="job in jobs" :key="job.id"><td>#{{ job.id }}</td><td>{{ job.channel_name || '渠道已删除' }}</td><td>{{ job.attempts }}</td><td class="failure">{{ job.last_error }}</td><td>{{ new Date(job.dead_at).toLocaleString('zh-CN') }}</td><td><button class="secondary" :disabled="busy || !job.retryable" :title="!job.retryable ? '渠道已停用或删除，请先检查通知渠道' : undefined" @click="retry(job)">{{ retrying === job.id ? '正在加入队列…' : '重新发送' }}</button><small v-if="!job.retryable">渠道不可用</small></td></tr>
   <tr v-if="!jobs.length && loaded && !jobsError && !jobsBusy" key="empty"><td colspan="6">暂无停止重试的通知</td></tr>
  </TransitionGroup></table></div>
  <div class="pages"><span>第 {{ pageIndex + 1 }} 页 · 本页 {{ jobs.length }} 条</span><button class="text-button" :disabled="busy || pageIndex === 0" @click="loadJobs(0)">返回首页</button><button class="secondary" :disabled="busy || pageIndex === 0" @click="loadJobs(pageIndex - 1)">上一页</button><button class="secondary" :disabled="busy || !nextCursor" @click="loadJobs(pageIndex + 1)">下一页</button></div>
 </section>
</template>
<style scoped>
.operations { padding: 20px; } .metrics { display: grid; grid-template-columns: repeat(auto-fit,minmax(220px,1fr)); gap: 16px; } dt { color: var(--muted); font-size: 12px; } dd { margin: 5px 0; font-weight: 600; } .failure { max-width: 420px; overflow-wrap: anywhere; } .pages { display:flex; flex-wrap:wrap; align-items:center; justify-content:flex-end; gap:8px; margin-top:12px; }
.task-row-move, .task-row-enter-active, .task-row-leave-active { transition: transform 220ms ease, opacity 180ms ease; } .task-row-enter-from, .task-row-leave-to { opacity:0; transform:translateY(-5px); } .task-row-leave-active { position:absolute; pointer-events:none; }
</style>
