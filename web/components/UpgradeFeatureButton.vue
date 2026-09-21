<script setup lang="ts">
import { paidFeatures, paidFeatureLabel, type PaidFeatureID } from '../utils/paidFeatures'

const props = withDefaults(defineProps<{
  feature: PaidFeatureID
  navigation?: boolean
  tone?: 'primary' | 'secondary' | 'ghost'
  size?: 'xs' | 'sm' | 'md'
  icon?: string | false
}>(), { navigation: false, tone: 'primary', size: 'md', icon: false })
const discovery = useUpgradeDiscovery()
const definition = computed(() => paidFeatures[props.feature])
const label = computed(() => paidFeatureLabel(props.feature, props.navigation))

function showFeature(event: MouseEvent) {
  if (event.currentTarget instanceof HTMLElement) discovery?.open(props.feature, event.currentTarget)
}
</script>

<template>
  <button
    v-if="discovery && navigation"
    type="button"
    class="app-sidebar-link upgrade-nav-link flex w-full items-center gap-3 rounded-2xl px-4 py-3 text-left text-[15px] font-medium transition"
    :aria-label="label"
    aria-haspopup="dialog"
    :data-upgrade-feature="feature"
    @click="showFeature"
  >
    <svg class="h-[18px] w-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path v-for="path in definition.icon" :key="path" :d="path" />
    </svg>
    <span>{{ label }}</span>
  </button>
  <UiButton
    v-else-if="discovery"
    type="button"
    :tone="tone"
    :size="size"
    :icon="icon"
    :aria-label="label"
    aria-haspopup="dialog"
    :data-upgrade-feature="feature"
    @click="showFeature"
  >
    {{ label }}
  </UiButton>
</template>

<style scoped>
.upgrade-nav-link:focus-visible { outline: 2px solid var(--app-focus); outline-offset: -2px; }
</style>
