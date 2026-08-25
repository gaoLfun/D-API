export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, { ...options, headers, credentials: 'include' })
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
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, data?: unknown) => request<T>(path, { method: 'POST', body: data === undefined ? undefined : JSON.stringify(data) }),
  put: <T>(path: string, data: unknown) => request<T>(path, { method: 'PUT', body: JSON.stringify(data) }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
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
