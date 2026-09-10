import { onScopeDispose, ref, shallowRef } from 'vue'
import { api, listOf } from './api'

export interface CursorPage<T> { items: T[]; next_cursor: string }

// Commit page position and rows together, only after the request succeeds.
export function useCursorPage<T>() {
 const items = shallowRef<T[]>([])
 const attemptedPage = ref(0)
 const page = ref(0), nextCursor = ref(''), busy = ref(false), error = ref(''), loaded = ref(false)
 let pathKey = '', cursors = [''], controller: AbortController | null = null
 async function load(path: string, target = page.value, signal?: AbortSignal) {
  if (path !== pathKey) target = 0
  const cursor = target === 0 ? '' : cursors[target]
  if (cursor === undefined || target < 0) return
  attemptedPage.value = target
  controller?.abort()
  const current = new AbortController()
  controller = current
  const abort = () => current.abort(signal?.reason)
  if (signal?.aborted) abort()
  else signal?.addEventListener('abort', abort, { once: true })
  busy.value = true; error.value = ''
  try {
   const params = new URLSearchParams({ pagination: 'cursor', cursor })
   const result = await api.get<CursorPage<T>>(`${path}${path.includes('?') ? '&' : '?'}${params}`, { signal: current.signal })
   if (current.signal.aborted || controller !== current) return
   items.value = listOf<T>(result)
   nextCursor.value = result.next_cursor || ''
   if (path !== pathKey) cursors = ['']
   pathKey = path; page.value = target; loaded.value = true
   cursors = cursors.slice(0, target + 1)
   if (nextCursor.value) cursors.push(nextCursor.value)
  } catch (failure) {
   if (!current.signal.aborted && controller === current) {
    error.value = failure instanceof Error ? failure.message : '加载失败，请重试'
    throw failure
   }
  } finally {
   signal?.removeEventListener('abort', abort)
   if (controller === current) busy.value = false
  }
 }
 function cancel() { controller?.abort(); busy.value = false }
 onScopeDispose(cancel)
 function snapshot() { return { items: [...items.value], page: page.value, attemptedPage: attemptedPage.value, nextCursor: nextCursor.value, loaded: loaded.value, pathKey, cursors: [...cursors] } }
 function restore(value: ReturnType<typeof snapshot>) {
  cancel(); controller = null; items.value = [...value.items]; page.value = value.page; attemptedPage.value = value.attemptedPage; nextCursor.value = value.nextCursor; loaded.value = value.loaded; error.value = ''; pathKey = value.pathKey; cursors = [...value.cursors]
 }
 return { snapshot, restore, items, page, attemptedPage, nextCursor, busy, error, loaded, load, cancel }
}
