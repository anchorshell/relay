<script setup lang="ts">
defineProps<{
  label: string
  help?: string
  error?: string
  required?: boolean
  optional?: boolean
  compact?: boolean
}>()
</script>

<template>
  <label
    :class="[
      'ui-field flex h-full flex-col',
      compact ? 'h-auto' : '',
      error ? 'ui-field--error' : ''
    ]"
  >
    <div class="ui-field-header flex items-baseline gap-2">
      <span class="app-heading-text text-sm font-medium">{{ label }}</span>
      <span v-if="required" class="text-[11px] font-semibold uppercase tracking-[0.16em] text-amber-300">Required</span>
      <span v-else-if="optional" class="app-faint-text text-[11px] font-semibold uppercase tracking-[0.16em]">Optional</span>
    </div>
    <div v-if="help" class="ui-field-help mt-0.5 flex items-start">
      <p v-if="help" :class="['app-copy-text max-w-2xl text-sm', compact ? 'leading-5' : 'leading-6']">{{ help }}</p>
    </div>
    <div class="ui-field-control mt-1.5">
      <slot />
    </div>
    <p v-if="error" class="ui-field-error mt-1.5 text-xs leading-4">{{ error }}</p>
  </label>
</template>
