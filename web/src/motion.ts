import type { ObjectDirective } from 'vue'

export const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches
const highlights = new WeakMap<HTMLElement, Animation>()
export const vChangeGlow: ObjectDirective<HTMLElement, unknown> = {
 updated(el, binding) {
  if (Object.is(binding.value, binding.oldValue) || reducedMotion() || !el.animate) return
  highlights.get(el)?.cancel()
  const color = getComputedStyle(document.documentElement).getPropertyValue('--accent-soft').trim() || '#d4eade'
  const animation = el.animate([{ backgroundColor: color }, { backgroundColor: getComputedStyle(el).backgroundColor }], { duration: 650, easing: 'ease-out' })
  highlights.set(el, animation)
 },
 beforeUnmount(el) { highlights.get(el)?.cancel(); highlights.delete(el) },
}

export function retireOverlay(el: Element) { (el as HTMLElement).inert = true; el.setAttribute('aria-hidden', 'true') }
export function activateOverlay(el: Element) { (el as HTMLElement).inert = false; el.removeAttribute('aria-hidden') }
