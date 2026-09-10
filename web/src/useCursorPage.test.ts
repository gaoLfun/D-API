import { afterEach, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { api } from './api'
import { useCursorPage } from './useCursorPage'

afterEach(() => vi.restoreAllMocks())
function setup() {
 const scope = effectScope()
 const state = scope.run(() => useCursorPage<{ id: number }>())!
 return { state, scope }
}
it('翻页失败保留当前内容和页码，重试后再一起提交，末页禁止继续', async () => {
 const get = vi.spyOn(api, 'get').mockResolvedValueOnce({ items: [{ id: 1 }], next_cursor: 'next' }).mockRejectedValueOnce(new Error('连接失败')).mockResolvedValueOnce({ items: [{ id: 2 }], next_cursor: '' })
 const { state, scope } = setup()
 await state.load('/logs')
 await expect(state.load('/logs', 1)).rejects.toThrow('连接失败')
 expect(state.page.value).toBe(0)
 expect(state.items.value).toEqual([{ id: 1 }])
 expect(state.nextCursor.value).toBe('next')
 await state.load('/logs', state.attemptedPage.value)
 expect(state.page.value).toBe(1)
 expect(state.items.value).toEqual([{ id: 2 }])
 expect(state.nextCursor.value).toBe('')
 expect(get.mock.calls[2]![0]).toContain('cursor=next')
 scope.stop()
})
it('筛选变化回到第一页；被取消的旧响应不能覆盖新结果', async () => {
 const requests: Array<{ path: string; resolve: (data: unknown) => void; signal: AbortSignal }> = []
 vi.spyOn(api, 'get').mockImplementation((path, options) => new Promise(resolve => requests.push({ path, resolve, signal: options!.signal as AbortSignal })))
 const { state, scope } = setup()
 const old = state.load('/logs?status=error')
 const fresh = state.load('/logs?status=success', 3)
 expect(requests[0]!.signal.aborted).toBe(true)
 requests[1]!.resolve({ items: [{ id: 2 }], next_cursor: '' }); await fresh
 requests[0]!.resolve({ items: [{ id: 1 }], next_cursor: 'old' }); await old
 expect(state.items.value).toEqual([{ id: 2 }]); expect(state.page.value).toBe(0)
 const unmount = state.load('/logs')
 scope.stop()
 expect(requests[2]!.signal.aborted).toBe(true)
 requests[2]!.resolve({ items: [{ id: 3 }], next_cursor: '' }); await unmount
 expect(state.items.value).toEqual([{ id: 2 }])
})
it('恢复历史页会取消在途请求，迟到响应不能覆盖恢复内容和游标', async () => {
 const get = vi.spyOn(api, 'get').mockResolvedValueOnce({ items: [{ id: 1 }], next_cursor: 'next' })
 const { state, scope } = setup()
 await state.load('/logs')
 const saved = state.snapshot()
 let resolve!: (data: unknown) => void
 get.mockImplementationOnce(() => new Promise(done => { resolve = done }))
 const pending = state.load('/logs', 1)
 state.restore(saved)
 resolve({ items: [{ id: 2 }], next_cursor: '' }); await pending
 expect(state.items.value).toEqual([{ id: 1 }])
 expect(state.nextCursor.value).toBe('next')
 expect(state.page.value).toBe(0)
 expect(state.busy.value).toBe(false)
 scope.stop()
})
