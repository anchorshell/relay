<script setup lang="ts">
const props = defineProps<{
  open: boolean
  footer?: boolean
  title: string
  subtitle?: string
}>()

defineEmits<{ close: [] }>()
</script>

<template>
  <UiInspectorDrawer
    :open="props.open"
    :title="props.title"
    :subtitle="props.subtitle"
    :hide-footer="!props.footer"
    @close="$emit('close')"
  >
    <slot />
    <template #footer><slot name="footer" /></template>
  </UiInspectorDrawer>
</template>
