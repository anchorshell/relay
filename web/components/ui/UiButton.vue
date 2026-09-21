<script setup lang="ts">
const props = withDefaults(defineProps<{
  tone?: 'primary' | 'secondary' | 'danger' | 'ghost'
  size?: 'xs' | 'sm' | 'md'
  disabled?: boolean
  loading?: boolean
  icon?: string | false
}>(), {
  tone: 'primary',
  size: 'md',
  disabled: false,
  loading: false
})
</script>

<template>
  <button
    :disabled="disabled || loading"
    :aria-busy="loading || undefined"
    :class="[
      'inline-flex cursor-pointer items-center justify-center rounded-md font-medium transition-[transform,background-color,border-color,color,box-shadow] duration-150 ease-out focus:outline-none focus:ring-2 focus:ring-[color:var(--app-focus)] focus:ring-offset-2 focus:ring-offset-[color:var(--app-bg)] active:translate-y-px active:scale-[0.98] disabled:cursor-not-allowed aria-disabled:cursor-not-allowed disabled:opacity-50 disabled:active:transform-none',
      size === 'xs' ? 'px-2.5 py-1.5 text-xs' : size === 'sm' ? 'px-3 py-2 text-sm' : 'px-4 py-2.5 text-sm',
      tone === 'primary' && 'ui-button-primary',
      tone === 'secondary' && 'ui-button-secondary ring-1',
      tone === 'danger' && 'ui-button-danger',
      tone === 'ghost' && 'ui-button-ghost ring-1'
    ]"
  >
    <UIcon v-if="loading" name="i-lucide-loader-circle" class="h-4 w-4 shrink-0 animate-spin" />
    <UIcon v-else-if="icon" :name="icon" class="h-4 w-4 shrink-0" />
    <slot />
  </button>
</template>
