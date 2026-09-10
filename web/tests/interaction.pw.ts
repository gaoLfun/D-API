import { expect, test, type Page } from '@playwright/test'

async function settleTransitions(page: Page) {
 await expect(page.locator('.overlay-enter-active, .overlay-leave-active')).toHaveCount(0)
 await page.evaluate(async () => {
  const animations = document.getAnimations().filter(animation => animation.effect?.getTiming().iterations !== Infinity)
  await Promise.allSettled(animations.map(animation => animation.finished))
 })
}

async function fixtures(page: Page) {
 const ups = [1, 2].map(id => ({ id, name: `交互上游-${id}`, kind: 'sub2api', base_url: `https://cluster-${id}.example.com`, enabled: true, priority: id * 10, protocols: ['responses'], models: ['model-a'], health_status: 'healthy', model_aliases: {}, failure_threshold: 3, cooldown_seconds: 60 }))
 const groups = [{ id: 1, name: '交互分组', enabled: true, upstream_ids: [1, 2], key_count: 1 }]
 const keys = [{ id: 1, name: '交互客户端', enabled: true, group_id: 1, protocols: ['responses'], models: ['model-a'] }]
 const row = (id: string) => ({ request_id: id, upstream_id: 1, upstream_name: ups[0]!.name, api_key_id: 1, api_key_name: '交互客户端', model: 'model-a', protocol: 'responses', status_code: 200, duration_ms: 32, created_at: '2026-09-10T02:00:00Z', attempts: [{ upstream_id: 1, upstream_name: ups[0]!.name, status_code: 200, duration_ms: 32 }] })
 const logURLs: URL[] = [], balances: number[] = []
 let failSecond = true
 await page.route('**/api/admin/**', async route => {
  const url = new URL(route.request().url()), path = url.pathname
  if (path.endsWith('/me')) return route.fulfill({ json: { username: 'tester' } })
  if (path.endsWith('/dashboard')) return route.fulfill({ json: { requests_24h: 42, hourly: [{ hour: '2026-09-10T02:00:00Z', requests: 42, successes: 40, input_tokens: 100 }] } })
  if (path.endsWith('/upstreams')) return route.fulfill({ json: ups })
  if (/\/upstreams\/\d+$/.test(path) && route.request().method() === 'PUT') { Object.assign(ups[Number(path.split('/').at(-1)) - 1]!, route.request().postDataJSON()); return route.fulfill({ json: {} }) }
  if (path.endsWith('/balance')) {
   const id = Number(path.split('/').at(-2)); balances.push(id)
   await new Promise(resolve => setTimeout(resolve, 120))
   return route.fulfill({ json: id === 2 && failSecond ? { status: 'unavailable', error: '模拟余额失败' } : { status: 'ok' } })
  }
  if (path.endsWith('/groups')) return route.fulfill({ json: groups })
  if (path.endsWith('/keys')) return route.fulfill({ json: keys })
  if (path.endsWith('/pricing')) return route.fulfill({ json: { profiles: [] } })
  if (path.endsWith('/usage')) return route.fulfill({ json: { daily: [{ day: '2026-09-10', model: 'model-a', requests: 42 }] } })
  if (path.endsWith('/logs')) {
   logURLs.push(url)
   const next = Boolean(url.searchParams.get('cursor'))
   return route.fulfill({ json: { items: [row(next ? 'second-context-row' : 'first-context-row')], next_cursor: next ? '' : 'next-page' } })
  }
  return route.fulfill({ json: [] })
 })
 return { logURLs, balances, recover: () => { failSecond = false } }
}

for (const width of [1440, 390]) {
 test(`图表定位 → 日志 → 尝试上游 → 编辑保存保留现场 ${width}px`, async ({ page }) => {
  const { logURLs } = await fixtures(page)
  await page.setViewportSize({ width, height: 900 })
  await page.goto('/#dashboard')
  await page.getByRole('combobox', { name: '定位图表时间' }).selectOption('0')
  await expect(page.getByText('已选时段', { exact: true })).toBeVisible()
  await settleTransitions(page)
  await page.screenshot({ path: `../.impeccable/review/interaction-chart-${width}.png`, fullPage: true })
  await page.getByRole('button', { name: '查看请求日志', exact: true }).click()
  await expect(page.getByText('first-contex', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  expect(logURLs.at(-1)!.searchParams.get('since')).toBe('2026-09-10T02:00:00.000Z')
  expect(logURLs.at(-1)!.searchParams.get('until')).toBe('2026-09-10T03:00:00.000Z')
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  const log = page.getByRole('button', { name: '展开请求 second-context-row 详情' })
  await log.press('Enter')
  await page.locator('.attempt-timeline').getByRole('button', { name: '交互上游-1', exact: true }).click()
  await page.locator('.upstream-group-drawer').getByRole('button', { name: '编辑 Key' }).click()
  const editor = page.getByRole('dialog', { name: '编辑上游' })
  await editor.getByLabel('名称', { exact: true }).fill('更新后的上游')
  const calls = logURLs.length
  await editor.getByRole('button', { name: '保存上游', exact: true }).click()
  await expect(editor).toBeHidden()
  await expect(page.locator('.upstream-group-drawer')).toContainText('更新后的上游')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  await expect(page.locator('.upstream-group-drawer').getByRole('button', { name: '编辑 Key' })).toBeFocused()
  await settleTransitions(page)
  await page.screenshot({ path: `../.impeccable/review/interaction-return-${width}.png`, fullPage: true })
  await page.locator('.upstream-group-drawer').getByTitle('关闭').click()
  await expect(page.locator('.pagination')).toContainText('第 2 页')
  await expect(log).toHaveAttribute('aria-expanded', 'true')
  expect(logURLs.length).toBe(calls)
  await page.goBack()
  await expect(page.getByRole('heading', { name: '总览', exact: true })).toBeVisible()
  await expect(page.getByRole('combobox', { name: '定位图表时间' })).toHaveValue('0')
 })
}

test('批量操作按真实结果更新进度，仅重试失败项', async ({ page }) => {
 const { balances, recover } = await fixtures(page)
 await page.goto('/#upstreams')
 await page.getByRole('checkbox', { name: '选择全部可见上游 Key' }).check()
 await page.getByRole('button', { name: '批量刷新余额', exact: true }).click()
 await expect(page.getByRole('progressbar', { name: '批量操作完成进度' })).toHaveAttribute('value', '2')
 await expect(page.locator('.batch-progress')).toContainText('1 个失败')
 expect([...balances].sort()).toEqual([1, 2])
 recover()
 await page.getByRole('button', { name: '仅重试失败项' }).click()
 await expect(page.locator('.batch-progress')).toContainText('0 个失败')
 expect(balances).toEqual([1, 2, 2])
})

test('后退恢复日志筛选、排序、分页和已展开项，移除标签只改对应条件', async ({ page }) => {
 const { logURLs } = await fixtures(page)
 await page.goto('/#logs')
 await page.getByRole('combobox', { name: '筛选客户端' }).selectOption('1')
 await page.getByRole('textbox', { name: '筛选模型' }).fill('model-a')
 await page.getByRole('button', { name: '筛选', exact: true }).click()
 await page.getByRole('button', { name: '下一页', exact: true }).click()
 await page.getByRole('button', { name: '耗时', exact: true }).click()
 const log = page.getByRole('button', { name: '展开请求 second-context-row 详情' })
 await log.press('Enter')
 const before = logURLs.length
 await page.locator('.sidebar nav').getByRole('button', { name: '上游', exact: true }).click()
 await page.goBack()
 await expect(page.locator('.pagination')).toContainText('第 2 页')
 await expect(log).toHaveAttribute('aria-expanded', 'true')
 await expect(page.locator('th').filter({ hasText: '耗时' })).toHaveAttribute('aria-sort', 'ascending')
 expect(logURLs.length).toBe(before)
 await page.getByTitle('移除筛选：模型：model-a').click()
 await expect(page.locator('.pagination')).toContainText('第 1 页')
 expect(logURLs.at(-1)!.searchParams.get('api_key_id')).toBe('1')
 expect(logURLs.at(-1)!.searchParams.has('model')).toBe(false)
})

test('拓扑聚焦支持键盘，并将客户端范围传到日志', async ({ page }) => {
 const { logURLs } = await fixtures(page)
 await page.goto('/#dashboard')
 await page.getByRole('button', { name: '拓扑', exact: true }).click()
 await page.getByRole('button', { name: '聚焦客户端密钥 交互客户端' }).press('Enter')
 await page.locator('.topology-focus-actions').getByRole('button', { name: '查看日志' }).click()
 await expect(page.locator('.applied-filters')).toContainText('交互客户端')
 expect(logURLs.at(-1)!.searchParams.get('api_key_id')).toBe('1')
})

test('减少动态效果时关闭弹窗仍恢复焦点，退出节点不可交互', async ({ page }) => {
 await fixtures(page)
 await page.emulateMedia({ reducedMotion: 'reduce' })
 await page.goto('/#upstreams')
 const trigger = page.getByRole('button', { name: '添加上游', exact: true })
 await trigger.click()
 await expect(page.getByRole('dialog', { name: '添加上游' })).toBeVisible()
 await page.keyboard.press('Escape')
 await expect(page.getByRole('dialog', { name: '添加上游' })).toBeHidden()
 await expect(trigger).toBeFocused()
 const animations = await page.evaluate(() => document.getAnimations().map(animation => Number(animation.effect?.getTiming().duration || 0)))
 expect(animations.every(duration => duration <= 1)).toBe(true)
})

 test('定位图表后自动刷新暂停，避免覆盖正在排查的时间点', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('dapi-auto-refresh', 'true'))
  await fixtures(page)
  await page.clock.install()
  await page.goto('/#dashboard')
  await page.getByRole('combobox', { name: '定位图表时间' }).selectOption('0')
  await expect(page.getByText('自动刷新已暂停')).toBeVisible()
  await page.clock.fastForward(31_000)
  await expect(page.getByRole('combobox', { name: '定位图表时间' })).toHaveValue('0')
  await page.getByRole('button', { name: '清除定位' }).click()
  await expect(page.getByText('自动刷新已暂停')).toHaveCount(0)
 })

for (const view of ['logs', 'usage'] as const) {
 for (const firstResult of ['pending', 'failed'] as const) {
  test(`${view} 首次加载${firstResult === 'pending' ? '未完成' : '失败'}后离开再后退会重新加载`, async ({ page }) => {
   await fixtures(page)
   let calls = 0
   let release!: () => void
   const gate = new Promise<void>(resolve => { release = resolve })
   const rows = view === 'logs'
    ? [{ request_id: 'restored-log-row', model: 'model-a', protocol: 'responses', status_code: 200, duration_ms: 1, created_at: '2026-09-10T02:00:00Z' }]
    : [{ day: '2026-09-10T00:00:00Z', upstream_name: 'restored-usage-row', requests: 12 }]
   await page.route(`**/api/admin/${view}?**`, async route => {
    if (view === 'usage' && new URL(route.request().url()).searchParams.get('dimension') !== 'upstream') return route.fallback()
    calls++
    if (calls === 1) {
     if (firstResult === 'failed') return route.fulfill({ status: 500, json: { error: { message: '首次加载失败' } } })
     await gate
    }
    await route.fulfill({ json: view === 'logs' ? { items: rows, next_cursor: '' } : { daily: rows } }).catch(() => {})
   })
   try {
    await page.goto('/#dashboard')
    await page.locator('.sidebar nav').getByRole('button', { name: view === 'logs' ? '请求日志' : '用量', exact: true }).click()
    await expect.poll(() => calls).toBe(1)
    if (firstResult === 'failed') await expect(page.locator('.toast')).toContainText('首次加载失败')
    await page.locator('.sidebar nav').getByRole('button', { name: '上游', exact: true }).click()
    await page.goBack()
    await expect.poll(() => calls).toBe(2)
    await expect(page.locator('.content tbody')).toContainText('restored-')
    release()
   } finally { release() }
  })
 }
}

test('同一天不同分组的图表定位和日志下钻保持一致', async ({ page }) => {
 const { logURLs } = await fixtures(page)
 await page.addInitScript(() => localStorage.setItem('dapi-usage-filter', JSON.stringify({ dimension: 'group' })))
 const rows = [1, 2].map(id => ({ day: '2026-09-10T00:00:00Z', group_id: id, group_name: `图表分组-${id}`, api_key_name: '', upstream_name: '', protocol: '', model: '', requests: id * 10 }))
 await page.route('**/api/admin/usage?**', route => route.fulfill({ json: { daily: rows } }))
 await page.goto('/#usage')
 const select = page.getByRole('combobox', { name: '定位图表时间' })
 await expect(select.locator('option').nth(2)).toContainText('图表分组-2')
 await select.selectOption('1')
 await expect(select).toHaveValue('1')
 await expect(page.locator('.drilldown-bar')).toContainText('图表分组-2')
 await page.getByRole('button', { name: '查看请求日志', exact: true }).click()
 await expect.poll(() => logURLs.at(-1)?.searchParams.get('group_id')).toBe('2')
 await page.goBack()
 await expect(select).toHaveValue('1')
})
