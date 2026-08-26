export const UPSTREAM_PROTOCOLS = ['responses', 'chat', 'messages'] as const
export const DEFAULT_UPSTREAM_PROTOCOLS = ['responses'] as const
export const UPSTREAM_USER_AGENTS = {
  default: '',
  codex: 'codex_cli_rs/0.101.0',
  opencode: 'opencode/1.0.0',
} as const
export type UpstreamUserAgentMode = keyof typeof UPSTREAM_USER_AGENTS | 'custom'
export const COMMON_UPSTREAM_MODELS = [
  'gpt-5.6',
  'gpt-5.6-mini',
  'gpt-5.6-codex',
  'claude-opus-4',
  'claude-opus-4-1',
  'claude-opus-4-5',
  'claude-opus-4-6',
] as const

export function usesNewAPICredentials(kind: string) {
  return kind === 'newapi'
}

export function userAgentMode(value: string): UpstreamUserAgentMode {
  const match = Object.entries(UPSTREAM_USER_AGENTS).find(([, userAgent]) => userAgent === value)
  return (match?.[0] as keyof typeof UPSTREAM_USER_AGENTS | undefined) ?? 'custom'
}

export function userAgentValue(mode: UpstreamUserAgentMode, custom: string) {
  return mode === 'custom' ? custom.trim() : UPSTREAM_USER_AGENTS[mode]
}

export function parseModelList(value: string) {
  return [...new Set(value.split(',').map((model) => model.trim()).filter(Boolean))]
}

export function setModelSelected(value: string, model: string, selected: boolean) {
  const models = parseModelList(value).filter((item) => item !== model)
  if (selected) models.push(model)
  return models.join(', ')
}

export function bulkSetModels(value: string, discovered: string[], selected: boolean) {
  const discoveredModels = parseModelList(discovered.join(','))
  const discoveredSet = new Set(discoveredModels)
  const manualModels = parseModelList(value).filter((model) => !discoveredSet.has(model))
  return [...manualModels, ...(selected ? discoveredModels : [])].join(', ')
}

export function modelBatchSelection(value: string, discovered: string[], limit = 20) {
  const selected = new Set(parseModelList(value))
  const models = parseModelList(discovered.join(',')).filter((model) => selected.has(model))
  return { models: models.slice(0, limit), total: models.length, exceedsLimit: models.length > limit }
}

export function modelsForPayload(value: string, manual: boolean) {
  return manual ? parseModelList(value) : []
}

export function connectionTestText(result: { status?: string; status_code?: number; latency?: number; error?: string }) {
  if (result.status !== 'healthy') return result.error || '连接失败'
  const parts = ['连接正常']
  if (result.status_code) parts.push(`HTTP ${result.status_code}`)
  if (Number.isFinite(result.latency)) parts.push(`${(Number(result.latency) / 1_000_000_000).toFixed(2)} s`)
  return parts.join(' · ')
}
