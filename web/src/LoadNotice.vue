<script setup lang="ts">
defineProps<{ busy: boolean; error: string; updated: number; label?: string }>()
defineEmits<{ retry: [] }>()
</script>
<template>
 <div v-if="busy || error" class="load-notice" :role="error ? 'alert' : 'status'">
  <strong v-if="label">{{ label }}</strong>
  <span v-if="busy">{{ updated ? '正在更新，当前显示上次结果…' : '正在加载…' }}</span>
  <template v-else><span>{{ error }}。{{ updated ? '当前保留上次成功加载的结果。' : '暂时无法显示数据。' }}</span><button class="secondary" @click="$emit('retry')">重试</button></template>
 </div>
</template>
<style scoped>
.load-notice { display:flex; align-items:center; gap:12px; padding:12px 16px; color:var(--muted); font-size:13px; overflow-wrap:anywhere; }
</style>
