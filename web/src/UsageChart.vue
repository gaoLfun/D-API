<script setup lang="ts">
import {
  BarController, BarElement, CategoryScale, Chart, Legend, LinearScale,
  LineController, LineElement, PointElement, Tooltip,
} from 'chart.js'
import type { ChartConfiguration, Plugin } from 'chart.js'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { reducedMotion } from './motion'
import type { UsageRow } from './types'

type UsageMetric = 'overview' | 'requests' | 'tokens' | 'latency' | 'cache' | 'cost'

const props = withDefaults(defineProps<{ rows: UsageRow[]; theme: 'light' | 'dark'; rangeLabel?: string; metric?: UsageMetric; selected?: UsageRow | null }>(), {
  rangeLabel: '近期',
  metric: 'overview',
})
const chartLabel = computed(() => {
  const metric = { overview: '请求与 Token', requests: '请求与成功', tokens: '输入、输出与缓存 Token', cache: '缓存读写 Token', latency: '平均与 P95 耗时', cost: '估算成本' }[props.metric]
  return `${props.rangeLabel}${metric}趋势图；详细数据见下方明细表`
})
const emit = defineEmits<{ select: [row: UsageRow | null] }>()
const rowKey = (row: UsageRow) => JSON.stringify([row.hour || row.day || row.date, row.upstream_id, row.api_key_id, row.group_id, row.group_name, row.upstream_name, row.api_key_name, row.protocol, row.model, row.label, row.dimension_label])
const selectedIndex = computed(() => props.selected ? props.rows.findIndex(row => rowKey(row) === rowKey(props.selected!)) : -1)
function selectIndex(index: number) { emit('select', props.rows[index] || null) }
const selectionLine: Plugin = {
 id: 'selectionLine',
 afterDatasetsDraw(chart) {
  const index = selectedIndex.value
  if (index < 0 || !chart.scales.x) return
  const x = chart.scales.x.getPixelForValue(index)
  const { ctx, chartArea } = chart
  ctx.save(); ctx.strokeStyle = color('--accent', '#176d4f'); ctx.lineWidth = 1.5; ctx.setLineDash([4, 3])
  ctx.beginPath(); ctx.moveTo(x, chartArea.top); ctx.lineTo(x, chartArea.bottom); ctx.stroke(); ctx.restore()
 },
}
const canvas = ref<HTMLCanvasElement | null>(null)
let chart: Chart | null = null
let renderSequence = 0

Chart.register(BarController, BarElement, CategoryScale, LinearScale, LineController, LineElement, PointElement, Tooltip, Legend)

function nullable(value: number | null | undefined) {
  return value == null ? null : Number(value)
}

function rowTokens(row: UsageRow) {
  if (row.tokens != null) return Number(row.tokens)
  if (row.input_tokens == null && row.output_tokens == null) return null
  return Number(row.input_tokens || 0) + Number(row.output_tokens || 0)
}

function rowLabel(row: UsageRow) {
  const source = row.hour || row.day || row.date || ''
  const day = row.hour
    ? new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(String(source)))
    : String(source).slice(5, 10)
  const dimension = row.label || row.dimension_label || row.upstream_name || row.api_key_name || row.protocol || row.model || ''
  const raw = dimension ? `${day} · ${dimension}` : day
  return String(raw).length > 18 ? `${String(raw).slice(0, 17)}…` : String(raw)
}

function token(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function color(name: string, fallback: string) {
  return token(name) || fallback
}

function datasets() {
  const rows = props.rows
  if (props.metric === 'cost') return [{
    type: 'bar' as const,
    label: '估算成本 USD',
    data: rows.map((row) => nullable(row.cost_usd)),
    backgroundColor: color('--chart-cost', '#176d4f'),
    hoverBackgroundColor: color('--accent-hover', '#105b40'),
    borderRadius: 3,
    borderSkipped: false,
    maxBarThickness: 22,
    yAxisID: 'value',
    order: 2,
  }]
  if (props.metric === 'tokens') return [
    { type: 'bar' as const, label: '输入 Token', data: rows.map((row) => nullable(row.input_tokens)), backgroundColor: color('--chart-bar', '#2f8968'), borderRadius: 3, borderSkipped: false, maxBarThickness: 22, yAxisID: 'value', order: 2 },
    { type: 'line' as const, label: '输出 Token', data: rows.map((row) => nullable(row.output_tokens)), borderColor: color('--chart-line', '#3978a8'), backgroundColor: color('--chart-line', '#3978a8'), borderWidth: 2, pointRadius: 0, pointHoverRadius: 3, tension: .32, yAxisID: 'value', order: 1 },
    { type: 'line' as const, label: '缓存读取', data: rows.map((row) => nullable(row.cached_input_tokens ?? row.cache_read_tokens)), borderColor: color('--chart-cache-read', '#b0782d'), backgroundColor: color('--chart-cache-read', '#b0782d'), borderWidth: 1.5, borderDash: [4, 3], pointRadius: 0, tension: .32, yAxisID: 'value', order: 1 },
    { type: 'line' as const, label: '缓存写入', data: rows.map((row) => nullable(row.cache_creation_input_tokens ?? row.cache_write_tokens)), borderColor: color('--chart-cache-write', '#8b5ba8'), backgroundColor: color('--chart-cache-write', '#8b5ba8'), borderWidth: 1.5, borderDash: [2, 2], pointRadius: 0, tension: .32, yAxisID: 'value', order: 1 },
  ]
  if (props.metric === 'latency') return [
    { type: 'line' as const, label: '平均耗时', data: rows.map((row) => { const value = row.avg_duration_ms ?? row.average_duration_ms; return value == null ? null : Number(value) / 1000 }), borderColor: color('--chart-line', '#3978a8'), backgroundColor: color('--chart-line', '#3978a8'), borderWidth: 2, pointRadius: 0, pointHoverRadius: 3, tension: .32, yAxisID: 'value', order: 1 },
    { type: 'line' as const, label: 'P95 耗时', data: rows.map((row) => { const value = row.p95_duration_ms ?? row.p95_ms; return value == null ? null : Number(value) / 1000 }), borderColor: color('--warning', '#b0782d'), backgroundColor: color('--warning', '#b0782d'), borderWidth: 2, borderDash: [5, 4], pointRadius: 0, pointHoverRadius: 3, tension: .32, yAxisID: 'value', order: 1 },
  ]
  if (props.metric === 'cache') return [
    { type: 'bar' as const, label: '缓存读取', data: rows.map((row) => nullable(row.cached_input_tokens ?? row.cache_read_tokens)), backgroundColor: color('--chart-cache-read', '#b0782d'), borderRadius: 3, borderSkipped: false, maxBarThickness: 22, yAxisID: 'value', order: 2 },
    { type: 'bar' as const, label: '缓存写入', data: rows.map((row) => nullable(row.cache_creation_input_tokens ?? row.cache_write_tokens)), backgroundColor: color('--chart-cache-write', '#8b5ba8'), borderRadius: 3, borderSkipped: false, maxBarThickness: 22, yAxisID: 'value', order: 2 },
  ]
  if (props.metric === 'requests') return [{
    type: 'bar' as const,
    label: '请求',
    data: rows.map((row) => nullable(row.requests)),
    backgroundColor: color('--chart-bar', '#2f8968'),
    hoverBackgroundColor: color('--accent-hover', '#105b40'),
    borderRadius: 3,
    borderSkipped: false,
    maxBarThickness: 22,
    yAxisID: 'value',
    order: 2,
  }, {
    type: 'line' as const,
    label: '成功',
    data: rows.map((row) => nullable(row.successes)),
    borderColor: color('--chart-line', '#3978a8'),
    backgroundColor: color('--chart-line', '#3978a8'),
    borderWidth: 2,
    pointRadius: 0,
    pointHoverRadius: 3,
    tension: .32,
    yAxisID: 'value',
    order: 1,
  }]
  return [{
    type: 'bar' as const,
    label: '请求',
    data: rows.map((row) => nullable(row.requests)),
    backgroundColor: color('--chart-bar', '#2f8968'),
    hoverBackgroundColor: color('--accent-hover', '#105b40'),
    borderRadius: 3,
    borderSkipped: false,
    maxBarThickness: 22,
    yAxisID: 'requests',
    order: 2,
  }, {
    type: 'line' as const,
    label: 'Token',
    data: rows.map(rowTokens),
    borderColor: color('--chart-line', '#3978a8'),
    backgroundColor: color('--chart-line', '#3978a8'),
    borderWidth: 2,
    pointRadius: 0,
    pointHoverRadius: 3,
    tension: .32,
    yAxisID: 'tokens',
    order: 1,
  }]
}

async function renderChart() {
  const sequence = ++renderSequence
  await nextTick()
  await document.fonts.ready
  if (sequence !== renderSequence || !canvas.value) return
  const reduceMotion = reducedMotion()
  Chart.defaults.font.family = getComputedStyle(document.body).fontFamily
  const config: ChartConfiguration = {
    plugins: [selectionLine],
    type: 'bar',
    data: {
      labels: props.rows.map(rowLabel),
      datasets: datasets(),
    },
    options: {
      onClick: (event, elements) => {
        if (!elements.length || !event.native) return
        selectIndex(elements[0]!.index)
      },
      responsive: true,
      maintainAspectRatio: false,
      animation: reduceMotion ? false : { duration: 260 },
      interaction: { intersect: false, mode: 'index' },
      plugins: {
        legend: {
          align: 'end',
          labels: {
            boxWidth: 9,
            boxHeight: 9,
            color: token('--text-muted'),
            font: { size: 10 },
            padding: 14,
            usePointStyle: true,
          },
        },
        tooltip: {
          displayColors: true,
          backgroundColor: token('--floating'),
          titleColor: token('--text'),
          bodyColor: token('--text-muted'),
          borderColor: token('--line-strong'),
          borderWidth: 1,
          padding: 10,
          callbacks: {
            label: (context) => `${context.dataset.label}: ${context.raw == null ? '—' : Number(context.raw).toLocaleString('zh-CN', { maximumFractionDigits: context.dataset.label?.includes('成本') ? 6 : context.dataset.label?.includes('耗时') ? 2 : 1 })}${context.dataset.label === '请求' ? ' 次' : context.dataset.label?.includes('耗时') ? ' s' : context.dataset.label?.includes('成本') ? ' USD' : ''}`,
            afterBody: (contexts) => {
              const row = props.rows[contexts[0]?.dataIndex ?? -1]
              if (!row?.requests || row.successes == null || props.metric !== 'requests') return ''
              return `成功率: ${(100 * Number(row.successes) / Number(row.requests)).toFixed(1)}%`
            },
          },
        },
      },
      scales: {
        x: {
          border: { display: false },
          grid: { display: false },
          ticks: { color: token('--text-faint'), maxRotation: 0, autoSkipPadding: 14, font: { size: 10 } },
        },
        value: {
          position: 'left',
          display: props.metric !== 'overview',
          beginAtZero: true,
          border: { display: false },
          grid: { color: token('--chart-grid') },
          ticks: { color: token('--text-faint'), precision: 0, font: { size: 10 } },
        },
        requests: {
          position: 'left',
          display: props.metric === 'overview',
          beginAtZero: true,
          border: { display: false },
          grid: { color: token('--chart-grid') },
          ticks: { color: token('--text-faint'), precision: 0, font: { size: 10 } },
        },
        tokens: {
          position: 'right',
          display: props.metric === 'overview',
          beginAtZero: true,
          border: { display: false },
          grid: { drawOnChartArea: false },
          ticks: {
            color: token('--text-faint'),
            font: { size: 10 },
            callback: (value) => new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(Number(value)),
          },
        },
      },
    },
  }
  if (chart) {
    const hidden = new Map(chart.data.datasets.map((dataset, index) => [dataset.label, !chart!.isDatasetVisible(index)]))
    chart.data = config.data
    chart.options = config.options || {}
    chart.data.datasets.forEach((dataset,index) => chart!.setDatasetVisibility(index, !hidden.get(dataset.label)))
    chart.update(reduceMotion ? 'none' : undefined)
  } else { chart = new Chart(canvas.value, config) }
}

const motionPreference = matchMedia('(prefers-reduced-motion: reduce)')
onMounted(() => { void renderChart(); motionPreference.addEventListener('change', renderChart) })
watch(selectedIndex, () => chart?.draw())
watch(() => [props.rows, props.theme, props.metric], renderChart, { deep: true })
onBeforeUnmount(() => {
  renderSequence++
  motionPreference.removeEventListener('change', renderChart)
  chart?.destroy()
  chart = null
})
</script>

<template>
  <div class="usage-chart">
    <div class="chart-canvas"><canvas ref="canvas" role="img" :aria-label="chartLabel">{{ chartLabel }}。</canvas></div>
    <div class="chart-selection-control">
      <label>定位时间<select aria-label="定位图表时间" :value="selectedIndex" @change="selectIndex(Number(($event.target as HTMLSelectElement).value))"><option :value="-1">选择时间，查看明细</option><option v-for="(row, index) in rows" :key="rowKey(row)" :value="index">{{ row.hour ? new Date(row.hour).toLocaleString('zh-CN') : (row.day || row.date || '').slice(0, 10) }} · {{ row.label || row.dimension_label || row.upstream_name || row.api_key_name || row.group_name || row.model || row.protocol || '全部' }}</option></select></label>
      <button v-if="selectedIndex >= 0" class="text-button" @click="selectIndex(-1)">清除定位</button>
    </div>
  </div>
</template>
<style scoped>
.usage-chart { height:100%; display:flex; flex-direction:column; min-width:0; }
.chart-canvas { position:relative; flex:1; min-height:0; }
.chart-selection-control { display:grid; grid-template-columns:minmax(0,1fr) auto; align-items:center; gap:8px; padding-top:8px; font-size:12px; }
.chart-selection-control label { display:grid; grid-template-columns:auto minmax(0,1fr); align-items:center; gap:8px; min-width:0; white-space:nowrap; }
.chart-selection-control select { min-height:30px; padding:4px 8px; max-width:280px; min-width:0; width:100%; }
@media(max-width:640px) { .chart-selection-control select { max-width:100%; } }
</style>
