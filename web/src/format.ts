export function fmtDate(value?: string) {
  return value ? new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value)) : '从未'
}

export function fmtNumber(value?: number) {
  const number = Number(value || 0)
  const locale = Math.abs(number) > 99999 ? 'en-US' : 'zh-CN'
  return new Intl.NumberFormat(locale, { notation: Math.abs(number) > 99999 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(number)
}

export function fmtMetric(value?: number | null, suffix = '') {
  return value == null || Number.isNaN(Number(value)) ? '—' : `${fmtNumber(Number(value))}${suffix}`
}

export function fmtDuration(value?: number | null) {
  if (value == null || Number.isNaN(Number(value))) return '—'
  return `${(Number(value) / 1000).toFixed(2)} s`
}

export function fmtCurrency(value: number, currency: 'USD' | 'CNY') {
  return new Intl.NumberFormat('zh-CN', { style: 'currency', currency, minimumFractionDigits: currency === 'USD' && Math.abs(value) < 1 ? 4 : 2, maximumFractionDigits: currency === 'USD' && Math.abs(value) < 1 ? 6 : 2 }).format(Number(value || 0))
}

export function fmtPercent(value?: number | null) {
  if (value == null || Number.isNaN(Number(value))) return '—'
  const number = Number(value)
  return `${(number <= 1 ? number * 100 : number).toFixed(1)}%`
}
