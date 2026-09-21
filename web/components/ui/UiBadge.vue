<script setup lang="ts">
const props = withDefaults(defineProps<{
  tone?: 'slate' | 'neutral' | 'coder' | 'emerald' | 'amber' | 'rose' | 'sky' | 'violet' | 'status-emerald' | 'status-amber' | 'status-rose'
  size?: 'xs' | 'sm' | 'md'
}>(), {
  tone: 'slate',
  size: 'md'
})

const toneClass = computed(() => ({
  slate: 'ui-badge-slate',
  neutral: 'ui-badge-neutral',
  coder: 'ui-badge-coder',
  emerald: 'ui-badge-emerald',
  amber: 'ui-badge-amber',
  rose: 'ui-badge-rose',
  sky: 'ui-badge-sky',
  violet: 'ui-badge-violet',
  'status-emerald': 'ui-badge-status-emerald',
  'status-amber': 'ui-badge-status-amber',
  'status-rose': 'ui-badge-status-rose'
}[props.tone]))
</script>

<template>
  <span :class="[
    'ui-badge inline-flex items-center gap-1 rounded-full px-2.5 py-1 font-medium ring-1',
    size === 'xs' ? 'gap-0.5 px-1 py-0 text-[9px] leading-tight' : size === 'sm' ? 'text-[11px]' : 'text-xs',
    toneClass
  ]">
    <slot />
  </span>
</template>
