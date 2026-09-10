import { ref, reactive, getCurrentScope, onScopeDispose, type Ref } from 'vue'
import { api, listOf } from './api'
import type { PricingData, PricingProfile, ModelPrice } from './types'

export function usePricing(actions: { saving: Ref<boolean>; notify: (message: string, error?: boolean) => void; requestConfirmation: (title: string, message: string, label?: string) => Promise<boolean>; loadCurrent: () => Promise<void> }) {
const { saving, notify, requestConfirmation, loadCurrent } = actions
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)
const pricing = ref<PricingData>({})
const pricingModal = ref(false)
const editingPricing = ref<number | null>(null)
const pricingForm = reactive({ name: '', provider: '', source_url: '', source_version: 'custom', prices: '' })
async function refreshPricing() {
  saving.value = true
  try {
    await api.post('/api/admin/pricing/refresh')
    pricing.value = await api.get<PricingData>('/api/admin/pricing?summary=true')
    notify('LiteLLM 价格已同步；手动档案仍作为兜底')
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function backfillPricing() {
  if (!(await requestConfirmation('回算历史成本', '将按请求发生时有效的价格档案补齐最近 365 天未知成本，并修正可核验的旧版 Messages 缓存写入重复计费；已清理的请求不在回算范围。'))) return
  saving.value = true
  try {
    const result = await api.post<{ logs_updated: number }>('/api/admin/pricing/backfill', {})
    await loadCurrent()
    notify(`已回算 ${Number(result?.logs_updated || 0).toLocaleString()} 条请求成本`)
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

const pricingLoading = ref(false)
let detailController: AbortController | null = null
let editorVersion = 0
function closePricingProfile() {
  editorVersion++
  detailController?.abort()
  detailController = null
  pricingLoading.value = false
  pricingModal.value = false
}
if (getCurrentScope()) onScopeDispose(closePricingProfile)

async function openPricingProfile(profile?: PricingProfile) {
  closePricingProfile()
  const version = editorVersion
  editingPricing.value = profile?.id ?? null
  Object.assign(pricingForm, { name: '', provider: '', source_url: '', source_version: 'custom', prices: '' })
  pricingModal.value = true
  if (profile) {
    const current = new AbortController()
    detailController = current
    pricingLoading.value = true
    try {
      profile = await api.get<PricingProfile>(`/api/admin/pricing/profiles/${profile.id}`, { signal: current.signal })
      if (detailController !== current || version !== editorVersion || current.signal.aborted) return
    } catch (error) {
      if (detailController === current && !current.signal.aborted) { closePricingProfile(); notify(errorMessage(error), true) }
      return
    } finally {
      if (detailController === current) { detailController = null; pricingLoading.value = false }
    }
  }
  const prices = listOf<ModelPrice>(profile?.prices).map((price) => [price.model, price.input_usd_per_million, price.output_usd_per_million, price.cache_read_usd_per_million, price.cache_write_usd_per_million].join(', ')).join('\n')
  Object.assign(pricingForm, profile ? { name: profile.name, provider: profile.provider || '', source_url: profile.source_url || '', source_version: profile.source_version || 'custom', prices } : { name: '', provider: '', source_url: '', source_version: 'custom', prices: '' })
}

async function savePricingProfile() {
  if (pricingLoading.value || saving.value) return
  const version = editorVersion
  const id = editingPricing.value
  const prices = pricingForm.prices.split('\n').map((line) => line.split(',').map((value) => value.trim())).filter((parts) => parts[0]).map(([model, input, output, cacheRead, cacheWrite]) => ({ model, input_usd_per_million: Number(input || 0), output_usd_per_million: Number(output || 0), cache_read_usd_per_million: Number(cacheRead || 0), cache_write_usd_per_million: Number(cacheWrite || 0) }))
  if (prices.some((price) => !Number.isFinite(price.input_usd_per_million) || !Number.isFinite(price.output_usd_per_million) || !Number.isFinite(price.cache_read_usd_per_million) || !Number.isFinite(price.cache_write_usd_per_million))) {
    notify('价格必须是数字', true)
    return
  }
  saving.value = true
  try {
    const payload = { name: pricingForm.name, provider: pricingForm.provider, source_url: pricingForm.source_url, source_version: pricingForm.source_version, prices }
    if (id) await api.put(`/api/admin/pricing/profiles/${id}`, payload)
    else await api.post('/api/admin/pricing/profiles', payload)
    pricing.value = await api.get<PricingData>('/api/admin/pricing?summary=true')
    if (version === editorVersion) closePricingProfile()
    notify(id ? '价格档案已更新' : '价格档案已创建')
  } catch (error) { notify(errorMessage(error), true) } finally { saving.value = false }
}

async function removePricingProfile(profile: PricingProfile) {
  if (!(await requestConfirmation('删除价格档案', `“${profile.name}”将被删除，已绑定上游会恢复为未计价。`))) return
  try {
    await api.delete(`/api/admin/pricing/profiles/${profile.id}`)
    pricing.value = await api.get<PricingData>('/api/admin/pricing?summary=true')
    notify('价格档案已删除')
  } catch (error) { notify(errorMessage(error), true) }
}

function pricingProfileName(id?: number) {
  if (!id) return '未计价'
  return listOf<PricingProfile>(pricing.value.profiles).find((profile) => Number(profile.id) === Number(id))?.name || `档案 #${id}`
}

return { pricingLoading, closePricingProfile, pricing, pricingModal, editingPricing, pricingForm, refreshPricing, backfillPricing, openPricingProfile, savePricingProfile, removePricingProfile, pricingProfileName }
}
