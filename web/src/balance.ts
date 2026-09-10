import type { Upstream, UpstreamGroup } from './types'
import { fmtDate } from './format'

export function balanceRank(upstream?: Upstream) {
  const balance = upstream?.balance
  if (!balance) return -3
  if (balance.status === 'unsupported') return -2
  if (balance.available == null && !balance.unlimited) return -1
  // Unlimited is intentionally the lowest usable balance so a numeric
  // account remains the representative when accounts are mixed.
  if (balance.unlimited) return 0
  const available = Number(balance.available)
  return Number.isFinite(available) ? 2 + Math.atan(available) / (Math.PI / 2) : -1
}

export function maxBalanceUpstream(items: Upstream[]) {
  if (!items.length) return undefined
  return items.reduce((best, item) => balanceRank(item) > balanceRank(best) ? item : best, items[0])
}

export function fmtBalance(upstream?: Upstream) {
  const balance = upstream?.balance
  if (!balance || balance.status === 'unsupported') return '不支持'
  if (balance.unlimited) return '无限额'
  if (balance.available == null) return '未知'
  return `${balance.currency || '币种未知'} ${balance.available.toFixed(2)}`
}

export function groupBalance(group: UpstreamGroup) {
  return fmtBalance(group.total > 1 ? maxBalanceUpstream(group.items) : group.items[0])
}

export function groupBalanceMeta(group: UpstreamGroup) {
  if (group.total <= 1) return fmtDate(group.items[0]?.balance?.updated_at)
  const selected = maxBalanceUpstream(group.items)
  return selected ? `取最高账号：${selected.name || `#${selected.id}`}` : '暂无可用余额'
}

export function fmtBalanceUsed(upstream: Upstream) {
  const balance = upstream.balance
  return balance?.used == null ? '暂无数据' : `${balance.currency || '币种未知'} ${balance.used.toFixed(2)}`
}

export function balanceUsedUpdatedAt(upstream: Upstream) {
  return upstream.balance?.last_success_at || upstream.balance?.updated_at
}
