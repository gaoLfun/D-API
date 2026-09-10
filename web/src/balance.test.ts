import { describe, expect, it } from 'vitest'
import { balanceRank, fmtBalance } from './balance'
import type { Upstream } from './types'
const upstream = (balance: Upstream['balance']) => ({ balance }) as Upstream

describe('balance display', () => {
 it('未知币种不补美元', () => expect(fmtBalance(upstream({ available: -2 }))).toBe('币种未知 -2.00'))
 it('透支仍排在未知余额之前，负数之间按实际大小排序', () => {
  const a = balanceRank(upstream({ available: -100 }))
  expect(a).toBeGreaterThan(balanceRank(upstream({ status: 'unknown' })))
  expect(a).toBeLessThan(balanceRank(upstream({ available: -1 })))
 })
})
