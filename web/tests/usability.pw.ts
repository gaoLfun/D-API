import { expect, test } from '@playwright/test'

test('成本接口未返回且价格失败时，核心指标立即显示并可单独重试价格', async ({ page }) => {
 let releaseCost!: () => void
 const costWait = new Promise<void>(resolve => { releaseCost = resolve })
 let pricingLoads = 0, dashboardLoads = 0
 await page.route('**/api/admin/**', async route => {
  const path = new URL(route.request().url()).pathname
  if (path.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (path.endsWith('/dashboard')) { dashboardLoads++; return route.fulfill({ json: { requests_24h: 1234 } }) }
  if (path.endsWith('/usage')) { await costWait; return route.fulfill({ json: { daily: [] } }) }
  if (path.endsWith('/pricing')) {
   pricingLoads++
   return pricingLoads === 1 ? route.fulfill({ status: 500, json: { message: '价格暂不可用' } }) : route.fulfill({ json: { profiles: [{ id: 1, name: '恢复后的价格档案', model_count: 1 }] } })
  }
  return route.fulfill({ json: [] })
 })
 try {
  await page.goto('/#dashboard')
  await expect(page.locator('.dashboard-metric').filter({ has: page.getByText('24 小时请求', { exact: true }) })).toContainText('1,234')
  await expect(page.locator('.cost-breakdown')).toContainText('正在加载')
  await expect(page.locator('.pricing-snapshot')).toContainText('价格暂不可用')
  await expect(page.locator('.pricing-snapshot')).not.toContainText('暂无价格档案')
  await page.locator('.pricing-snapshot').getByRole('button', { name: '重试', exact: true }).click()
  await expect(page.getByText('恢复后的价格档案')).toBeVisible()
  expect(dashboardLoads).toBe(1)
 } finally { releaseCost() }
})

test('日志失败翻页保留原页，重试抵达目标页，200 流失败标记为未完成', async ({ page }) => {
 let secondRequests = 0
 const row = (id: string) => ({ request_id: id, protocol: 'responses', model: 'test', status_code: 200, error_code: 'stream_incomplete', created_at: '2026-09-10T00:00:00Z', duration_ms: 1 })
 await page.route('**/api/admin/**', route => {
  const url = new URL(route.request().url())
  if (url.pathname.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (url.pathname.endsWith('/logs')) {
   expect(url.searchParams.get('pagination')).toBe('cursor')
   expect(url.searchParams.has('offset')).toBe(false)
   if (url.searchParams.get('cursor')) {
    secondRequests++
    return secondRequests === 1 ? route.fulfill({ status: 503, json: { message: '暂时无法读取日志' } }) : route.fulfill({ json: { items: [row('second-row')], next_cursor: '' } })
   }
   return route.fulfill({ json: { items: [row('first-row')], next_cursor: 'next-page' } })
  }
  return route.fulfill({ json: [] })
 })
 await page.goto('/#logs')
 await expect(page.getByText('first-row', { exact: true })).toBeVisible()
 await expect(page.getByText('200 · 未完成')).toHaveClass(/bad/)
 await page.getByRole('button', { name: '下一页', exact: true }).click()
 await expect(page.getByRole('alert')).toContainText('当前仍显示上次结果')
 await expect(page.locator('.pagination')).toContainText('第 1 页')
 await expect(page.getByText('first-row', { exact: true })).toBeVisible()
 await page.getByRole('button', { name: '重试', exact: true }).click()
 await expect(page.getByText('second-row', { exact: true })).toBeVisible()
 await expect(page.locator('.pagination')).toContainText('第 2 页')
 await expect(page.getByRole('button', { name: '下一页', exact: true })).toBeDisabled()
 await page.getByRole('button', { name: '返回首页' }).click()
 await expect(page.getByText('first-row', { exact: true })).toBeVisible()
})

test('通知状态故障不阻止列表加载，列表故障不显示无通知', async ({ page }) => {
 let failList = true
 await page.route('**/api/admin/**', route => {
  const path = new URL(route.request().url()).pathname
  if (path.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (path.endsWith('/metrics')) return route.fulfill({ status: 500, json: { message: '指标暂不可用' } })
  if (path.endsWith('/notifications/dead')) return failList ? route.fulfill({ status: 500, json: { message: '列表暂不可用' } }) : route.fulfill({ json: { items: [{ id: 9, channel_name: '故障渠道', attempts: 5, last_error: '连接超时', dead_at: '2026-09-10T00:00:00Z', retryable: false }], next_cursor: '' } })
  return route.fulfill({ json: [] })
 })
 await page.goto('/#channels')
 const panel = page.locator('.operations')
 await expect(panel).toContainText('列表暂不可用')
 await expect(panel).not.toContainText('暂无停止重试的通知')
 failList = false
 await panel.getByRole('button', { name: '重试列表加载' }).click()
 await expect(panel.getByText('故障渠道', { exact: true })).toBeVisible()
 await expect(panel).toContainText('指标暂不可用')
 await expect(panel.getByRole('button', { name: '重新发送' })).toBeDisabled()
 await expect(panel).toContainText('渠道不可用')
})

test('系统设置的自动刷新暂停，避免覆盖未保存输入', async ({ page }) => {
 let loads = 0
 await page.addInitScript(() => localStorage.setItem('dapi-auto-refresh', 'true'))
 await page.route('**/api/admin/**', route => {
  const path = new URL(route.request().url()).pathname
  if (path.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (path.endsWith('/settings')) { loads++; return route.fulfill({ json: { max_attempts: 3 } }) }
  return route.fulfill({ json: [] })
 })
 await page.clock.install()
 await page.goto('/#settings')
 await expect(page.getByText('自动刷新已暂停')).toBeVisible()
 const input = page.locator('input[type=number]').first()
 await input.fill('5')
 await page.clock.fastForward(31_000)
 await expect(input).toHaveValue('5')
 expect(loads).toBe(1)
})
