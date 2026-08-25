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
})
