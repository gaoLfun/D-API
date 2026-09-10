import { afterEach, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import { api } from './api'
import { usePricing } from './usePricing'
import type { PricingProfile } from './types'

vi.mock('./api', () => ({ api: { get: vi.fn(), put: vi.fn(), post: vi.fn() }, listOf: (value: unknown) => Array.isArray(value) ? value : [] }))
afterEach(() => vi.resetAllMocks())
const profile = (id: number): PricingProfile => ({ id, name: `profile-${id}`, provider: 'manual', source_url: '', source_version: 'test', prices: [] })
function setup() {
  const scope = effectScope()
  const notify = vi.fn()
  const state = scope.run(() => usePricing({ saving: ref(false), notify, requestConfirmation: async () => true, loadCurrent: async () => {} }))!
  return { scope, state, notify }
}
function delayedDetails() {
  const requests: Array<{ resolve: (profile: PricingProfile) => void; signal: AbortSignal }> = []
  vi.mocked(api.get).mockImplementation((_path, options) => new Promise(resolve => requests.push({ resolve, signal: options!.signal as AbortSignal })))
  return requests
}
it('连续编辑只接受最后一次点击，取消旧详情请求', async () => {
  const { scope, state } = setup()
  const requests = delayedDetails()
  const first = state.openPricingProfile(profile(1))
  const second = state.openPricingProfile(profile(2))
  expect(requests[0]!.signal.aborted).toBe(true)
  requests[1]!.resolve(profile(2)); await second
  requests[0]!.resolve(profile(1)); await first
  expect(state.editingPricing.value).toBe(2)
  expect(state.pricingForm.name).toBe('profile-2')
  expect(api.get).toHaveBeenCalledWith('/api/admin/pricing/profiles/2', expect.anything())
  scope.stop()
})
it.each(['close', 'unmount', 'new'] as const)('%s 使未完成详情失效，旧响应不能重开或覆盖弹窗', async action => {
  const { scope, state } = setup()
  const requests = delayedDetails()
  const pending = state.openPricingProfile(profile(1))
  expect(state.pricingLoading.value).toBe(true)
  if (action === 'close') state.closePricingProfile()
  else if (action === 'unmount') scope.stop()
  else await state.openPricingProfile()
  requests[0]!.resolve(profile(1)); await pending
  expect(requests[0]!.signal.aborted).toBe(true)
  expect(state.pricingLoading.value).toBe(false)
  expect(state.pricingModal.value).toBe(action === 'new')
  expect(state.pricingForm.name).toBe('')
  scope.stop()
})
it('详情加载期间禁止保存，失败后不会留下可误保存的空表单', async () => {
  const { scope, state, notify } = setup()
  let reject!: (reason: Error) => void
  vi.mocked(api.get).mockImplementation(() => new Promise((_resolve, fail) => { reject = fail }))
  const pending = state.openPricingProfile(profile(1))
  await state.savePricingProfile()
  expect(api.put).not.toHaveBeenCalled()
  reject(new Error('已删除')); await pending
  expect(state.pricingModal.value).toBe(false)
  expect(notify).toHaveBeenCalledWith('已删除', true)
  scope.stop()
})
