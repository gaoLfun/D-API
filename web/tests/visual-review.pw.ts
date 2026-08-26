import { expect, test, type Page } from '@playwright/test'
import path from 'node:path'

const reviewDir = path.resolve('..', '.impeccable', 'review')

const upstreams = [
  { id: 1, name: '新加坡主线路', kind: 'newapi', base_url: 'https://sg-api.example.com', enabled: true, balance_protection_enabled: true, balance_suspended: false, zero_balance_checks: 0, priority: 10, protocols: ['chat', 'responses'], models: ['gpt-5.6', 'claude-opus-4-6'], model_aliases: {}, failure_threshold: 3, health_status: 'healthy', consecutive_failures: 0, today_requests: 1842, today_tokens: 2840150, last_check_at: '2026-08-25T15:28:00Z', balance: { status: 'ok', available: 82.47, used: 17.53, currency: '$', updated_at: '2026-08-25T15:25:00Z' } },
  { id: 2, name: '东京备用线路', kind: 'sub2api', base_url: 'https://jp-api.example.com', enabled: true, balance_protection_enabled: true, balance_suspended: false, zero_balance_checks: 0, priority: 20, protocols: ['chat', 'messages'], models: ['claude-opus-4-5'], model_aliases: {}, failure_threshold: 3, health_status: 'healthy', consecutive_failures: 1, today_requests: 734, today_tokens: 912420, last_check_at: '2026-08-25T15:27:00Z', balance: { status: 'unsupported' } },
  { id: 3, name: '美西应急线路', kind: 'newapi', base_url: 'https://us-api.example.com', enabled: true, balance_protection_enabled: true, balance_suspended: true, zero_balance_checks: 2, priority: 30, protocols: ['chat', 'responses', 'messages'], models: ['gpt-5.6-mini'], model_aliases: {}, failure_threshold: 3, health_status: 'unhealthy', consecutive_failures: 3, today_requests: 81, today_tokens: 132900, last_check_at: '2026-08-25T15:21:00Z', last_error: 'upstream returned 502', circuit_open_until: '2026-08-25T15:31:00Z', balance: { status: 'ok', available: 0, used: 100, currency: '$', updated_at: '2026-08-25T15:20:00Z' } },
]

const keys = [
  { id: 1, name: 'Claude Code 工作站', prefix: 'dapi_live_8k2', enabled: true, protocols: ['messages'], models: ['claude-sonnet-4'], last_used_at: '2026-08-25T15:26:00Z', created_at: '2026-07-04T08:00:00Z' },
  { id: 2, name: '内部自动化', prefix: 'dapi_live_3m9', enabled: true, protocols: ['chat', 'responses'], models: [], last_used_at: '2026-08-25T14:58:00Z', created_at: '2026-07-12T08:00:00Z' },
  { id: 3, name: '旧测试环境', prefix: 'dapi_test_1p4', enabled: false, protocols: ['chat'], models: ['gpt-5-mini'], created_at: '2026-06-18T08:00:00Z' },
]

const logs = [
  { request_id: 'req_01J6M81QCF8TN4Z2', upstream_id: 1, upstream_name: '新加坡主线路', api_key_name: 'Claude Code 工作站', protocol: 'messages', model: 'claude-sonnet-4', status_code: 200, duration_ms: 842, attempts: [{ upstream_id: 1, upstream_name: '新加坡主线路', status_code: 200, duration_ms: 842 }], usage: { input_tokens: 1842, output_tokens: 416 }, created_at: '2026-08-25T15:26:00Z' },
  { request_id: 'req_01J6M7ZPWYV6KM8A', upstream_id: 2, upstream_name: '东京备用线路', api_key_name: '内部自动化', protocol: 'responses', model: 'gpt-5', status_code: 200, duration_ms: 1326, attempts: [{ upstream_id: 3, upstream_name: '美西应急线路', status_code: 502, duration_ms: 392, error: 'bad gateway' }, { upstream_id: 2, upstream_name: '东京备用线路', status_code: 200, duration_ms: 934 }], usage: { input_tokens: 914, output_tokens: 122 }, created_at: '2026-08-25T15:22:00Z' },
  { request_id: 'req_01J6M7VJN7C3QP9F', upstream_id: 3, upstream_name: '美西应急线路', api_key_name: '内部自动化', protocol: 'chat', model: 'gpt-5-mini', status_code: 502, duration_ms: 612, attempts: [{ upstream_id: 3, upstream_name: '美西应急线路', status_code: 502, duration_ms: 612, error: 'upstream returned 502' }], error_code: 'upstream_error', created_at: '2026-08-25T15:18:00Z' },
]

const daily = Array.from({ length: 30 }, (_, index) => ({
  day: `2026-08-${String(index + 1).padStart(2, '0')}T00:00:00Z`,
  requests: 184 + ((index * 67) % 311),
  successes: 177 + ((index * 63) % 298),
  input_tokens: 48200 + index * 1731,
  output_tokens: 12100 + index * 611,
  cached_input_tokens: 8200 + index * 347,
}))

async function mockApi(page: Page) {
  let guest = false
  let emptyKeys = false
  let delayDashboard = false
  let failDashboard = false
  let modelTestDelay = 0
  let availableModels = ['gpt-5.6', 'gpt-5.6-codex', 'claude-opus-4-6']
  const modelTestRequests: string[] = []
  const modelAuditRequests: unknown[] = []

  await page.route('**/api/admin/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname
    if (pathname.endsWith('/me') && guest) return route.fulfill({ status: 401, contentType: 'application/json', body: '{"message":"unauthorized"}' })
    if (pathname.endsWith('/dashboard') && delayDashboard) await new Promise((resolve) => setTimeout(resolve, 700))
    if (pathname.endsWith('/dashboard') && failDashboard) {
      failDashboard = false
      return route.fulfill({ status: 503, contentType: 'application/json', body: '{"message":"测试期模拟：无法读取总览"}' })
    }
    if (pathname.endsWith('/upstreams/test-models/audit')) {
      modelAuditRequests.push(route.request().postDataJSON())
      return route.fulfill({ status: 204 })
    }
    if (pathname.endsWith('/upstreams/test-model')) {
      const payload = route.request().postDataJSON() as { model: string; protocols?: string[] }
      modelTestRequests.push(payload.model)
      if (modelTestDelay) await new Promise((resolve) => setTimeout(resolve, modelTestDelay))
      const protocols = payload.protocols?.length ? payload.protocols : ['responses']
      const results = protocols.map((protocol, index) => ({
        protocol,
        status: payload.model.includes('codex') && protocol === 'messages' ? 'failed' : 'success',
        status_code: payload.model.includes('codex') && protocol === 'messages' ? 404 : 200,
        latency_ms: 20 + index,
        error: payload.model.includes('codex') && protocol === 'messages' ? 'not supported' : undefined,
      }))
      const body = { model: payload.model, status: results.every((result) => result.status === 'success') ? 'available' : 'partial', results }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
    }
    let body: unknown = {}
    if (pathname.endsWith('/me')) body = { username: 'admin' }
    else if (pathname.endsWith('/dashboard')) body = { stats: { upstreams_total: 3, upstreams_healthy: 2, requests_24h: 12847, success_rate: 97.4, avg_latency_ms: 914, input_tokens_24h: 2874300, output_tokens_24h: 1011170 }, daily: daily.slice(-7).map((row) => ({ day: row.day, requests: row.requests, successes: row.successes, tokens: row.input_tokens + row.output_tokens })), upstreams }
    else if (pathname.endsWith('/upstreams/test')) body = { status: 'healthy', status_code: 200, latency: 18_000_000, models: availableModels }
    else if (pathname.endsWith('/upstreams')) body = upstreams
    else if (/\/keys\/\d+\/secret$/.test(pathname)) body = { key: 'dapi_live_visual_secret' }
    else if (pathname.endsWith('/keys')) body = route.request().method() === 'POST' ? { key: 'dapi_live_created_secret' } : (emptyKeys ? [] : keys)
    else if (pathname.endsWith('/logs')) body = logs
    else if (pathname.endsWith('/usage')) body = { daily }
    else if (pathname.endsWith('/channels')) body = [{ id: 1, name: '值班邮箱', kind: 'email', enabled: true }, { id: 2, name: '运维 Webhook', kind: 'webhook', enabled: true }]
    else if (pathname.endsWith('/alert-rules')) body = [{ id: 1, event: 'low_balance', threshold: 5, window_seconds: 300, cooldown_seconds: 1800, enabled: true }, { id: 2, event: 'error_rate', upstream_id: 3, threshold: 20, window_seconds: 300, cooldown_seconds: 900, enabled: true }]
    else if (pathname.endsWith('/settings')) body = { max_attempts: 3 }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })

  return {
    setGuest(value: boolean) { guest = value },
    setEmptyKeys(value: boolean) { emptyKeys = value },
    setDelayDashboard(value: boolean) { delayDashboard = value },
    failNextDashboard() { failDashboard = true },
    setAvailableModels(models: string[]) { availableModels = models },
    setModelTestDelay(delay: number) { modelTestDelay = delay },
    modelTestRequests,
    modelAuditRequests,
  }
}

async function openView(page: Page, label: string) {
  await page.getByRole('button', { name: label, exact: true }).first().click()
  await expect(page.locator('.topbar h1')).toHaveText(label)
}

test('capture the operational surface across themes and viewports', async ({ page }) => {
  const state = await mockApi(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.addInitScript(() => {
    if (!localStorage.getItem('dapi-theme')) localStorage.setItem('dapi-theme', 'light')
  })
  await page.goto('/')
  await expect(page.locator('.topbar h1')).toHaveText('总览')

  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-dashboard.png'), fullPage: true })
  await openView(page, '上游')
  await expect(page.getByText(/余额暂停/).first()).toBeVisible()
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-upstreams.png'), fullPage: true })
  await page.locator('.upstream-table tbody tr.clickable').first().click()
  await page.locator('.upstream-group-drawer').getByRole('button', { name: '编辑 Key' }).first().click()
  await expect(page.getByRole('dialog', { name: '编辑上游' })).toBeVisible()
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-drawer.png'), fullPage: true })
  await page.getByRole('dialog', { name: '编辑上游' }).getByTitle('关闭').click()
  await page.locator('.upstream-table tbody tr.clickable').first().click()
  await page.locator('.upstream-group-drawer').getByTitle('删除上游').first().click()
  await expect(page.getByRole('alertdialog', { name: '删除上游' })).toBeVisible()
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-confirm.png'), fullPage: true })
  await page.getByRole('alertdialog', { name: '删除上游' }).getByRole('button', { name: '取消' }).click()
  await page.locator('.upstream-group-drawer').getByTitle('关闭').click()
  await openView(page, '请求日志')
  await page.locator('tbody tr.clickable').nth(1).click()
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-logs.png'), fullPage: true })
  await openView(page, '用量')
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-usage.png'), fullPage: true })
  await openView(page, '通知')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1440)
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-notifications.png'), fullPage: true })

  await page.getByTitle('深色').click()
  await openView(page, '总览')
  await page.screenshot({ path: path.join(reviewDir, 'desktop-dark-dashboard.png'), fullPage: true })

  state.setDelayDashboard(true)
  await page.getByTitle('刷新当前页面').click()
  await page.waitForTimeout(120)
  await page.screenshot({ path: path.join(reviewDir, 'desktop-dark-loading.png'), fullPage: true })
  await page.waitForTimeout(700)
  state.setDelayDashboard(false)
  state.failNextDashboard()
  await page.getByTitle('刷新当前页面').click()
  await expect(page.getByText('测试期模拟：无法读取总览')).toBeVisible()
  await page.screenshot({ path: path.join(reviewDir, 'desktop-dark-error.png'), fullPage: true })

  state.setEmptyKeys(true)
  await openView(page, '客户端密钥')
  await page.screenshot({ path: path.join(reviewDir, 'desktop-dark-empty.png'), fullPage: true })

  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByTitle('浅色').click()
  await page.goto('/#dashboard')
  await page.reload()
  await expect(page.locator('.topbar h1')).toHaveText('总览')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.screenshot({ path: path.join(reviewDir, 'mobile-light-dashboard.png'), fullPage: true })
  await page.goto('/#upstreams')
  await page.reload()
  await expect(page.locator('.topbar h1')).toHaveText('上游')
  await page.locator('.upstream-table tbody tr.clickable').first().click()
  await page.waitForTimeout(250)
  await page.screenshot({ path: path.join(reviewDir, 'mobile-light-upstreams.png'), fullPage: true })
  await page.goto('/#logs')
  await page.reload()
  await expect(page.locator('.topbar h1')).toHaveText('请求日志')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.screenshot({ path: path.join(reviewDir, 'mobile-light-logs.png'), fullPage: true })
  await page.getByTitle('深色').click()
  await page.goto('/#usage')
  await page.reload()
  await expect(page.locator('.topbar h1')).toHaveText('用量')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
  await page.screenshot({ path: path.join(reviewDir, 'mobile-dark-usage.png'), fullPage: true })

  state.setGuest(true)
  await page.goto('/')
  await page.reload()
  await expect(page.getByRole('heading', { name: '登录' })).toBeVisible()
  await page.screenshot({ path: path.join(reviewDir, 'mobile-dark-login.png'), fullPage: true })
})

test('dialogs preserve keyboard focus and close predictably', async ({ page }) => {
  await mockApi(page)
  await page.goto('/')
  await openView(page, '上游')

  await page.locator('.upstream-table tbody tr.clickable').first().click()
  await page.locator('.upstream-group-drawer').getByRole('button', { name: '编辑 Key' }).first().click()
  const drawer = page.getByRole('dialog', { name: '编辑上游' })
  await expect(drawer).toBeVisible()
  await expect(drawer.locator('input').first()).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(drawer).toBeHidden()

  await page.locator('.upstream-table tbody tr.clickable').first().click()
  await page.locator('.upstream-group-drawer').getByTitle('刷新余额').first().focus()
  await expect(page.locator('.upstream-group-drawer').getByTitle('刷新余额').first()).toBeFocused()
  await page.locator('.upstream-group-drawer').getByTitle('刷新模型').first().focus()
  await expect(page.locator('.upstream-group-drawer').getByTitle('刷新模型').first()).toBeFocused()
  await page.locator('.upstream-group-drawer').getByTitle('删除上游').first().click()
  const confirmation = page.getByRole('alertdialog', { name: '删除上游' })
  await expect(confirmation.getByRole('button', { name: '删除' })).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(confirmation.getByRole('button', { name: '取消' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(confirmation).toBeHidden()
})

test('direct routes, chained dialogs, dates, and edge menus remain operable', async ({ page }) => {
  await mockApi(page)
  await page.goto('/#logs')
  await expect(page.locator('.filterbar select').nth(1).locator('option')).toHaveCount(upstreams.length + 1)

  await openView(page, '用量')
  await expect(page.locator('.usage-table-panel tbody td').first()).toHaveText('2026-08-30')

  await openView(page, '客户端密钥')
  const gatewayOrigin = new URL(page.url()).origin
  await expect(page.locator('.gateway-base-url code')).toHaveText(gatewayOrigin)
  await page.evaluate(() => {
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (value: string) => { (window as unknown as { copiedGateway?: string }).copiedGateway = value } },
    })
  })
  await page.getByTitle('复制网关 Base URL').click()
  await expect(page.getByText('网关 Base URL已复制')).toBeVisible()
  expect(await page.evaluate(() => (window as unknown as { copiedGateway?: string }).copiedGateway)).toBe(gatewayOrigin)
  const firstKeyRow = page.locator('.key-table tbody tr').first()
  await firstKeyRow.getByTitle('导入 CCSwitch').click()
  const ccswitchDialog = page.getByRole('dialog', { name: '导入到 CCSwitch' })
  await expect(ccswitchDialog.locator('select').first()).toHaveValue('claude')
  await expect(ccswitchDialog.getByLabel('完整客户端密钥')).toHaveValue('dapi_live_visual_secret')
  await ccswitchDialog.getByLabel('完整客户端密钥').fill('dapi_live_visual_test')
  await page.evaluate(() => {
    Object.defineProperty(window, 'open', {
      configurable: true,
      value: (url: string) => { (window as unknown as { ccswitchURL?: string }).ccswitchURL = url },
    })
  })
  await ccswitchDialog.getByRole('button', { name: '打开 CCSwitch' }).click()
  const ccswitchURL = await page.evaluate(() => (window as unknown as { ccswitchURL?: string }).ccswitchURL)
  expect(ccswitchURL).toBeTruthy()
  const ccswitchParams = new URL(ccswitchURL!.replace('ccswitch://', 'http://')).searchParams
  expect(ccswitchParams.get('resource')).toBe('provider')
  expect(ccswitchParams.get('app')).toBe('claude')
  expect(ccswitchParams.get('endpoint')).toBe(gatewayOrigin)
  expect(ccswitchParams.get('apiKey')).toBe('dapi_live_visual_test')
  await page.getByRole('button', { name: '创建密钥' }).first().click()
  const keyDialog = page.getByRole('dialog', { name: '创建客户端密钥' })
  await keyDialog.locator('input').first().fill('回归测试密钥')
  await keyDialog.getByRole('button', { name: '创建密钥' }).click()
  const secretDialog = page.getByRole('dialog', { name: '客户端密钥已创建' })
  await expect(secretDialog.getByRole('button', { name: '我已保存' })).toBeFocused()
  await expect(secretDialog).not.toContainText('dapi_live_created_secret')
  await expect(secretDialog.locator('.secret-mask')).toHaveAttribute('type', 'password')
  await expect(secretDialog.locator('.secret-mask')).toHaveValue('dapi_live_created_secret')
  await secretDialog.getByRole('button', { name: '导入 CCSwitch' }).click()
  const createdImportDialog = page.getByRole('dialog', { name: '导入到 CCSwitch' })
  await expect(createdImportDialog.getByLabel('完整客户端密钥')).toHaveValue('dapi_live_created_secret')
  await createdImportDialog.getByRole('button', { name: '取消' }).click()
  await expect(secretDialog).toBeVisible()
  await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: () => Promise.reject(new DOMException('blocked', 'NotAllowedError')) },
  }))
  await secretDialog.getByRole('button', { name: '复制密钥', exact: true }).click()
  await expect(page.getByText('密钥已复制')).toBeVisible()
  await secretDialog.getByRole('button', { name: '我已保存' }).click()

  await page.setViewportSize({ width: 1440, height: 430 })
  await openView(page, '上游')
  const table = page.locator('.table-wrap').last()
  await table.evaluate((element) => { element.scrollTop = element.scrollHeight })
  await page.locator('.upstream-table tbody tr.clickable').last().scrollIntoViewIfNeeded()
  await page.locator('.upstream-table tbody tr.clickable').last().click()
  expect(await page.locator('.upstream-group-drawer').evaluate((element) => element.getBoundingClientRect().bottom <= window.innerHeight + 430)).toBe(true)
})

for (const clipboardMode of ['unavailable', 'rejected'] as const) {
  test(`copies a newly created key when clipboard is ${clipboardMode}`, async ({ page }) => {
    await mockApi(page)
    await page.goto('/#keys')
    await expect(page.locator('.topbar h1')).toHaveText('客户端密钥')
    await page.getByRole('button', { name: '创建密钥' }).first().click()
    const keyDialog = page.getByRole('dialog', { name: '创建客户端密钥' })
    await keyDialog.locator('input').first().fill(`HTTP fallback ${clipboardMode}`)
    await keyDialog.getByRole('button', { name: '创建密钥' }).click()

    await page.evaluate((mode) => {
      Object.defineProperty(window, 'isSecureContext', { configurable: true, value: mode === 'rejected' })
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: mode === 'unavailable'
          ? undefined
          : { writeText: () => Promise.reject(new DOMException('blocked', 'NotAllowedError')) },
      })
      document.execCommand = (command) => command === 'copy'
    }, clipboardMode)

    const secretDialog = page.getByRole('dialog', { name: '客户端密钥已创建' })
    await secretDialog.getByRole('button', { name: '复制密钥', exact: true }).click()
    await expect(page.getByText('密钥已复制')).toBeVisible()
  })
}

test('starts and changes theme when local storage is unavailable', async ({ page }) => {
  await mockApi(page)
  await page.addInitScript(() => {
    Storage.prototype.getItem = () => { throw new DOMException('blocked', 'SecurityError') }
    Storage.prototype.setItem = () => { throw new DOMException('blocked', 'SecurityError') }
  })
  await page.goto('/')
  await expect(page.locator('.topbar h1')).toHaveText('总览')
  await page.getByTitle('深色').click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
})

test('日志详情支持键盘展开且导航支持后退', async ({ page }) => {
  await mockApi(page)
  await page.goto('/#dashboard')
  await page.getByRole('button', { name: '请求日志' }).click()
  await expect(page.locator('.topbar h1')).toHaveText('请求日志')
  const row = page.locator('.clickable').first()
  await row.focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('.attempt-row').first()).toBeVisible()
  await page.goBack()
  await expect(page.locator('.topbar h1')).toHaveText('总览')
})

test('new defaults, model discovery, and dashboard chart work together', async ({ page }) => {
  await mockApi(page)
  await page.goto('/')

  const chart = page.locator('.dashboard-usage-panel canvas')
  await expect(chart).toBeVisible()
  await expect.poll(() => chart.evaluate((element) => {
    const canvas = element as HTMLCanvasElement
    const pixels = canvas.getContext('2d')?.getImageData(0, 0, canvas.width, canvas.height).data || []
    return Array.from(pixels).some((value, index) => index % 4 === 3 && value > 0)
  })).toBe(true)

  await openView(page, '上游')
  await page.getByRole('button', { name: '添加上游' }).first().click()
  const upstreamDialog = page.getByRole('dialog', { name: '添加上游' })
  await expect(upstreamDialog.getByLabel('类型')).toHaveValue('sub2api')
  await expect(upstreamDialog.getByLabel('余额耗尽时自动暂停路由')).toBeChecked()
  await expect(upstreamDialog.getByText('Access Token')).toHaveCount(0)
  await upstreamDialog.getByLabel('名称', { exact: true }).fill('模型探测线路')
  await upstreamDialog.getByLabel('Base URL').fill('https://models.example.com')
  await upstreamDialog.getByLabel('API Key').fill('sk-model-test')
  await upstreamDialog.getByRole('button', { name: '获取上游模型' }).click()
  await expect(upstreamDialog.locator('.model-row')).toHaveCount(3)
  await page.screenshot({ path: path.join(reviewDir, 'desktop-light-model-discovery.png'), fullPage: true })

  const modelList = upstreamDialog.getByLabel('模型列表')
  await modelList.fill('manual-model')
  await upstreamDialog.locator('.model-select-toggle').click()
  expect((await modelList.inputValue()).split(',').map((model) => model.trim()))
    .toEqual(['manual-model', 'claude-opus-4-6', 'gpt-5.6', 'gpt-5.6-codex'])
  await expect(upstreamDialog.locator('.model-select-toggle')).toHaveText('取消全选')
  await upstreamDialog.locator('.model-select-toggle').click()
  await expect(modelList).toHaveValue('manual-model')
  await expect(upstreamDialog.locator('.model-select-toggle')).toHaveText('全选')

  await upstreamDialog.locator('.model-row label', { hasText: /^gpt-5\.6$/ }).click()
  await upstreamDialog.locator('.model-test-one[title="测试 gpt-5.6"]').click()
  const report = upstreamDialog.locator('.model-test-report', { hasText: 'gpt-5.6' })
  await expect(report.locator('.protocol-result')).toHaveCount(1)
  await expect(report.locator('.protocol-result').nth(0)).toContainText('responses成功HTTP 200')
  await expect(upstreamDialog.locator('.model-test-summary')).toContainText('可用 1')
  await expect(upstreamDialog.locator('.model-test-summary')).toContainText('部分可用 0')
  await expect(upstreamDialog.locator('.model-test-summary')).toContainText('不可用 0')
  await upstreamDialog.getByTitle('关闭').click()

  await openView(page, '通知')
  await page.getByRole('button', { name: '添加渠道' }).click()
  const channelDialog = page.getByRole('dialog', { name: '添加通知渠道' })
  await expect(channelDialog.getByLabel('类型')).toHaveValue('webhook')
  await expect(channelDialog.getByLabel('Webhook URL')).toBeVisible()
})

test('model batch testing enforces its limit, confirms large runs, and stops cleanly', async ({ page }) => {
  const state = await mockApi(page)
  const models = Array.from({ length: 21 }, (_, index) => `model-${String(index + 1).padStart(2, '0')}`)
  state.setAvailableModels(models)
  await page.goto('/#upstreams')
  await page.getByRole('button', { name: '添加上游' }).first().click()
  const dialog = page.getByRole('dialog', { name: '添加上游' })
  await dialog.getByLabel('名称', { exact: true }).fill('批量模型测试')
  await dialog.getByLabel('Base URL').fill('https://models.example.com')
  await dialog.getByLabel('API Key').fill('sk-model-test')
  await dialog.getByRole('button', { name: '获取上游模型' }).click()
  await expect(dialog.locator('.model-row')).toHaveCount(21)

  await dialog.locator('.model-select-toggle').click()
  await dialog.locator('.model-batch-test').click()
  await expect(page.getByText('一次最多测试 20 个模型，当前已选 21 个')).toBeVisible()
  expect(state.modelTestRequests).toHaveLength(0)

  await dialog.getByLabel('模型列表').fill(models.slice(0, 6).join(', '))
  state.setModelTestDelay(1_000)
  await dialog.locator('.model-batch-test').click()
  const confirmation = page.getByRole('alertdialog', { name: '批量测试模型' })
  await expect(confirmation).toContainText('将测试 6 个模型，共 6 个协议请求')
  await confirmation.getByRole('button', { name: '开始测试' }).click()
  await expect(dialog.locator('.model-test-stop')).toBeVisible()
  await dialog.locator('.model-test-stop').click()
  await expect(page.getByText(/已停止，保留 \d+ 个完成结果/)).toBeVisible()
  await expect.poll(() => state.modelAuditRequests.length).toBe(1)
  expect(state.modelAuditRequests[0]).toMatchObject({ models_count: 0, stopped: true })
})
