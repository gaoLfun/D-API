import { expect, test } from '@playwright/test'

test('运维指标与通知死信重试', async ({ page }) => {
 let retried = false
 await page.route('**/api/admin/**', async route => {
  const path = new URL(route.request().url()).pathname
  if (path.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (path.endsWith('/metrics')) return route.fulfill({ json: { gateway: { active_requests: 3, buffered_request_bytes: 1024, buffered_request_limit: 536870912, request_log_queue: 2, dropped_request_logs: 1 }, database: { in_use: 1, open_connections: 3, wait_count: 2, wait_duration_ms: 15 }, notifications: { pending: retried ? 1 : 0, dead: retried ? 0 : 1 } } })
  if (path.endsWith('/notifications/dead')) return route.fulfill({ json: retried ? [] : [{ id: 7, channel_name: '测试通知', attempts: 5, last_error: '连接超时', dead_at: '2026-09-10T00:00:00Z', retryable: true }] })
  if (path.endsWith('/notifications/7/retry')) { retried = true; return route.fulfill({ json: { queued: true } }) }
  return route.fulfill({ json: [] })
 })
 await page.goto('/#channels')
 await expect(page.getByRole('heading', { name: '运行状态与通知重试' })).toBeVisible()
 await expect(page.getByText('连接超时', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: '重新发送', exact: true }).click()
 await expect(page.getByText('暂无停止重试的通知')).toBeVisible()
 expect(retried).toBe(true)
})

test('总览只加载价格摘要，编辑时获取模型价格', async ({ page }) => {
 let fullLoads = 0
 const profile = { id: 1, name: '回归价格', provider: 'manual', source_url: '', source_version: 'test', model_count: 1 }
 await page.route('**/api/admin/**', route => {
  const url = new URL(route.request().url())
  if (url.pathname.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (url.pathname.endsWith('/pricing/profiles/1')) {
   fullLoads++
   return route.fulfill({ json: { ...profile, prices: [{ model: 'regression-model', input_usd_per_million: 2, output_usd_per_million: 8, cache_read_usd_per_million: 0.5, cache_write_usd_per_million: 3 }] } })
  }
  if (url.pathname.endsWith('/pricing')) {
   const summary = url.searchParams.get('summary') === 'true'
   expect(summary).toBe(true)
   return route.fulfill({ json: { usd_cny_rate: 7.2, profiles: [summary ? profile : { ...profile, prices: [{ model: 'regression-model', input_usd_per_million: 2, output_usd_per_million: 8, cache_read_usd_per_million: 0.5, cache_write_usd_per_million: 3 }] }] } })
  }
  return route.fulfill({ json: url.pathname.endsWith('/dashboard') || url.pathname.endsWith('/usage') ? {} : [] })
 })
 await page.goto('/#dashboard')
 await expect(page.getByText('回归价格', { exact: true })).toBeVisible()
 expect(fullLoads).toBe(0)
 await page.getByTitle('编辑价格档案').click()
 await expect(page.locator('textarea').filter({ visible: true })).toHaveValue(/regression-model/)
 expect(fullLoads).toBe(1)
})
