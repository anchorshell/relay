<script setup lang="ts">
withDefaults(defineProps<{
  count?: number
  sources?: string[]
  stages?: string[]
}>(), {
  count: 1,
  sources: () => [],
  stages: () => []
})
</script>

<template>
  <UiBadge tone="sky" size="sm" :title="[sources.join(', '), stages.join(' / ')].filter(Boolean).join(' · ')">
    <UIcon name="i-lucide-shield-check" class="h-3.5 w-3.5" />
    {{ count > 1 ? `${count} Guardrails` : 'Guarded' }}
  </UiBadge>
</template>
