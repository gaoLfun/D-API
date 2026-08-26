export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

const REQUEST_TIMEOUT_MS = 15_000

export async function request<T>(path: string, options: RequestInit = {}, timeoutMs = REQUEST_TIMEOUT_MS): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new DOMException('请求超时', 'TimeoutError')), timeoutMs)
  const abortFromCaller = () => controller.abort(options.signal?.reason)
  if (options.signal?.aborted) abortFromCaller()
  else options.signal?.addEventListener('abort', abortFromCaller, { once: true })
  try {
    const response = await fetch(path, { ...options, headers, credentials: 'include', signal: controller.signal })
    if (response.status === 204) return undefined as T

    const contentType = response.headers.get('content-type') || ''
    const body = contentType.includes('application/json')
      ? await response.json().catch(() => null)
      : await response.text().catch(() => '')
    if (!response.ok) {
      const error = typeof body === 'object' && body ? body.error : null
      const message = typeof body === 'object' && body
        ? body.message || (typeof error === 'object' && error ? error.message : error) || `请求失败 (${response.status})`
        : body || `请求失败 (${response.status})`
      throw new ApiError(response.status, String(message))
    }
    return body as T
  } finally {
    clearTimeout(timer)
    options.signal?.removeEventListener('abort', abortFromCaller)
  }
}

export const api = {
  get: <T>(path: string, options: RequestInit = {}) => request<T>(path, options),
  post: <T>(path: string, data?: unknown, options: RequestInit = {}) => request<T>(path, { ...options, method: 'POST', body: data === undefined ? undefined : JSON.stringify(data) }),
  put: <T>(path: string, data: unknown, options: RequestInit = {}) => request<T>(path, { ...options, method: 'PUT', body: JSON.stringify(data) }),
  delete: <T>(path: string, options: RequestInit = {}) => request<T>(path, { ...options, method: 'DELETE' }),
}

export function listOf<T>(value: unknown): T[] {
  if (Array.isArray(value)) return value as T[]
  if (!value || typeof value !== 'object') return []
  const object = value as Record<string, unknown>
  for (const key of ['items', 'data', 'results', 'upstreams', 'keys', 'logs', 'channels']) {
    if (Array.isArray(object[key])) return object[key] as T[]
  }
  return []
}
