export const UPSTREAM_PROTOCOLS = ['chat', 'responses', 'messages'] as const
export const DEFAULT_UPSTREAM_PROTOCOLS = [...UPSTREAM_PROTOCOLS]
export const COMMON_UPSTREAM_MODELS = ['gpt-5', 'gpt-5-mini', 'claude-sonnet-4', 'claude-opus-4'] as const

export function usesNewAPICredentials(kind: string) {
  return kind === 'newapi'
}

export function parseModelList(value: string) {
  return [...new Set(value.split(',').map((model) => model.trim()).filter(Boolean))]
}

export function setModelSelected(value: string, model: string, selected: boolean) {
  const models = parseModelList(value).filter((item) => item !== model)
  if (selected) models.push(model)
  return models.join(', ')
}

export function modelsForPayload(value: string, manual: boolean) {
  return manual ? parseModelList(value) : []
}

export function connectionTestText(result: { status?: string; status_code?: number; latency?: number; error?: string }) {
  if (result.status !== 'healthy') return result.error || '连接失败'
  const parts = ['连接正常']
  if (result.status_code) parts.push(`HTTP ${result.status_code}`)
  if (Number.isFinite(result.latency)) parts.push(`${Math.round(Number(result.latency) / 1_000_000)} ms`)
  return parts.join(' · ')
}
