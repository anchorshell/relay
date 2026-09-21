<script setup lang="ts">
defineProps<{
  label: string
  value: string | number
  hint?: string
}>()
</script>

<template>
  <div class="app-subsurface p-7">
    <div class="space-y-4">
      <p class="app-subtle-text text-[11px] font-semibold uppercase tracking-[0.24em]">{{ label }}</p>
      <div class="flex items-end justify-between gap-4">
        <p class="app-heading-text text-3xl font-semibold tracking-tight">{{ value }}</p>
        <p v-if="hint" class="app-copy-text max-w-[140px] text-right text-xs leading-5">{{ hint }}</p>
      </div>
    </div>
  </div>
</template>
