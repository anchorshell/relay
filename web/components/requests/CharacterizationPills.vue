<script setup lang="ts">
import type { RequestCharacterization } from '~/types/admin'

const props = defineProps<{
  characterization?: RequestCharacterization | null
  primaryAction?: string | null
  pending?: boolean
}>()

const isPending = computed(() => props.pending || props.characterization?.classifier_status === 'pending')
const primaryAction = computed(() => isPending.value ? 'pending' : (props.primaryAction || props.characterization?.primary_action || ''))
const primaryTone = computed(() => isPending.value ? 'neutral' : 'sky')
const object = computed(() => props.characterization?.target_objects?.[0] || props.characterization?.output_objects?.[0] || '')
const domain = computed(() => props.characterization?.domains?.[0] || '')
const capability = computed(() => props.characterization?.required_capabilities?.[0] || '')
const burden = computed(() => {
  const tier = props.characterization?.context_burden?.tier || ''
  return tier === 'large' || tier === 'extreme' ? tier : ''
})
const hasMetadata = computed(() => !!(object.value || domain.value || capability.value || burden.value))

function friendlyLabel(value?: string | null) {
  if (!value) return 'Unknown'
  return value.replaceAll('_', ' ').replace(/\b\w/g, letter => letter.toUpperCase())
}
</script>

<template>
  <div v-if="pending || primaryAction || characterization" class="space-y-1.5">
    <UiBadge :tone="primaryTone" size="sm">{{ friendlyLabel(primaryAction) }}</UiBadge>
    <div v-if="hasMetadata && !isPending" class="flex flex-wrap gap-1">
      <UiBadge v-if="object" tone="neutral" size="xs">{{ friendlyLabel(object) }}</UiBadge>
      <UiBadge v-if="domain" tone="neutral" size="xs">{{ friendlyLabel(domain) }}</UiBadge>
      <UiBadge v-if="capability" :tone="capability === 'needs_coder' ? 'coder' : 'neutral'" size="xs">{{ friendlyLabel(capability) }}</UiBadge>
      <UiBadge v-if="burden" tone="neutral" size="xs">{{ friendlyLabel(burden) }} context</UiBadge>
    </div>
  </div>
</template>
