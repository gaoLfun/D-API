import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request } from './api'

describe('request', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('带 cookie 请求并解析 API 错误', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code: 'invalid_credentials', message: '密码错误' } }),
      { status: 401, headers: { 'content-type': 'application/json' } },
    ))
    vi.stubGlobal('fetch', fetchMock)

    await expect(request('/api/admin/me')).rejects.toEqual(new ApiError(401, '密码错误'))
    expect(fetchMock).toHaveBeenCalledWith('/api/admin/me', expect.objectContaining({ credentials: 'include' }))
  })

  it('支持调用方取消请求', async () => {
    const fetchMock = vi.fn().mockImplementation((_path, options: RequestInit) => new Promise((_resolve, reject) => {
      options.signal?.addEventListener('abort', () => reject(options.signal?.reason || new DOMException('aborted', 'AbortError')), { once: true })
    }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()
    const pending = request('/api/admin/slow', { signal: controller.signal })
    controller.abort()
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('请求超时会中止底层 fetch', async () => {
    vi.useFakeTimers()
    try {
      const fetchMock = vi.fn().mockImplementation((_path, options: RequestInit) => new Promise((_resolve, reject) => {
        options.signal?.addEventListener('abort', () => reject(options.signal?.reason), { once: true })
      }))
      vi.stubGlobal('fetch', fetchMock)
      const pending = request('/api/admin/timeout', {}, 10)
      const assertion = expect(pending).rejects.toMatchObject({ name: 'TimeoutError' })
      await vi.advanceTimersByTimeAsync(11)
      await assertion
    } finally {
      vi.useRealTimers()
    }
  })
})
