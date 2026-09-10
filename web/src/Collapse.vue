<script setup lang="ts">
import { reducedMotion } from './motion'
defineProps<{ open: boolean }>()
const animations = new WeakMap<Element, Animation>()
function animate(element: Element, done: () => void, opening: boolean) {
 const el = element as HTMLElement
 el.inert = !opening
 if (reducedMotion() || !el.animate) { done(); return }
 const height = el.scrollHeight
 const frames = [{ height: '0px', opacity: 0 }, { height: `${height}px`, opacity: 1 }]
 const animation = el.animate(opening ? frames : [...frames].reverse(), { duration: 220, easing: 'cubic-bezier(.2,.8,.2,1)' })
 animations.set(el, animation)
 animation.onfinish = () => { animations.delete(el); done() }
}
function cancel(el: Element) { animations.get(el)?.cancel(); animations.delete(el) }
</script>
<template>
 <Transition :css="false" @enter="(el, done) => animate(el, done, true)" @leave="(el, done) => animate(el, done, false)" @enter-cancelled="cancel" @leave-cancelled="cancel">
  <div v-if="open" class="collapse-content"><slot /></div>
 </Transition>
</template>
<style scoped>.collapse-content { overflow: hidden; min-width: 0; }</style>
