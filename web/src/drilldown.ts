import type { UsageRow } from './types'

// SQL day buckets are UTC; hourly rows carry their timezone explicitly.
export function usageWindow(row: UsageRow, granularity = 'day') {
 const raw = row.hour || row.day || row.date
 if (!raw) return null
 const start = row.hour ? new Date(raw) : new Date(`${raw.slice(0, 10)}T00:00:00Z`)
 if (!Number.isFinite(start.getTime())) return null
 const end = new Date(start)
 if (row.hour) end.setUTCHours(end.getUTCHours() + 1)
 else if (granularity === 'month') end.setUTCMonth(end.getUTCMonth() + 1)
 else end.setUTCDate(end.getUTCDate() + (granularity === 'week' ? 7 : 1))
 return { since: start.toISOString(), until: end.toISOString() }
}

export function reportWindow(days: number, now = new Date()) {
 const end = new Date(now); end.setUTCHours(0, 0, 0, 0); end.setUTCDate(end.getUTCDate() + 1)
 const start = new Date(end); start.setUTCDate(start.getUTCDate() - days)
 return { since: start.toISOString(), until: end.toISOString() }
}
export function clipWindow(window: { since: string; until: string }, bounds: { since: string; until: string }) {
 const since = new Date(Math.max(Date.parse(window.since), Date.parse(bounds.since)))
 const until = new Date(Math.min(Date.parse(window.until), Date.parse(bounds.until)))
 return since < until ? { since: since.toISOString(), until: until.toISOString() } : null
}
