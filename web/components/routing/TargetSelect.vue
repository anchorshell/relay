<script setup lang="ts">
import type { RoutingTargetSelectItem } from '~/composables/useRoutingTargetOptions'

const model = defineModel<string>({ required: true })

const props = defineProps<{
  items: RoutingTargetSelectItem[][]
  loading?: boolean
  placeholder?: string
  align?: 'start' | 'center' | 'end'
}>()

const menuOpen = ref(false)
const stableItems = shallowRef<RoutingTargetSelectItem[][]>([])

function itemKey(item: RoutingTargetSelectItem) {
  if ('value' in item) {
    return [
      item.kind,
      item.value,
      item.label,
      item.description,
      item.providerSlug || '',
      item.searchText || '',
      item.class || ''
    ].join('\u001f')
  }
  return ['label', item.label, item.class || ''].join('\u001f')
}

function itemsKey(items: RoutingTargetSelectItem[][]) {
  return items.map(group => group.map(itemKey).join('\u001e')).join('\u001d')
}

watch(
  () => itemsKey(props.items),
  () => {
    stableItems.value = props.items.map(group => group.slice())
  },
  { immediate: true }
)
</script>

<template>
  <USelectMenu
    v-model:open="menuOpen"
    v-model="model"
    class="routing-target-select"
    :items="stableItems"
    :loading="loading"
    value-key="value"
    label-key="label"
    description-key="description"
    :placeholder="placeholder || 'Select a group or model'"
    :filter-fields="['label', 'description', 'providerSlug', 'searchText']"
    :content="{ align: align || 'end', sideOffset: 8, collisionPadding: 12 }"
    :search-input="{ placeholder: placeholder || 'Search groups and models...' }"
    :ui="{
      base: 'routing-target-select-base',
      content: 'routing-target-select-menu',
      viewport: 'routing-target-select-viewport',
      label: 'routing-target-select-section-label',
      item: 'routing-target-select-item',
      itemWrapper: 'routing-target-select-item-wrapper',
      itemLabel: 'routing-target-select-item-label',
      itemDescription: 'routing-target-select-item-description',
      value: 'routing-target-select-value',
      input: 'routing-target-select-input'
    }"
  />
</template>

<style scoped>
:global(.routing-target-select-base) {
  width: 100%;
  min-height: 2.5rem;
  justify-content: space-between;
  border: 1px solid var(--app-input-border);
  border-radius: 0.375rem;
  background: var(--app-input-bg);
  color: var(--app-heading);
  box-shadow: var(--app-input-shadow);
}

:global(.routing-target-select-base:focus-visible) {
  border-color: var(--app-focus);
  outline: none;
  box-shadow: 0 0 0 3px var(--app-focus-ring);
}

:global(.routing-target-select-base:hover) {
  border-color: color-mix(in srgb, var(--app-accent) 36%, var(--app-border));
}

:global(.routing-target-select-menu) {
  width: min(var(--reka-combobox-trigger-width, 24rem), calc(100vw - 2rem));
  min-width: min(var(--reka-combobox-trigger-width, 24rem), calc(100vw - 2rem));
  max-width: min(24rem, calc(100vw - 2rem));
  max-height: min(38rem, calc(100vh - 8rem));
  border: 1px solid var(--app-input-border);
  background: var(--app-surface);
  color: var(--app-heading);
  box-shadow: var(--app-panel-shadow);
}

:global(.routing-target-select-viewport) {
  max-height: min(36rem, calc(100vh - 8.5rem));
  overflow-y: auto;
  overscroll-behavior: contain;
}

:global(.routing-target-select-section-label),
:global(.routing-target-select-major-label) {
  padding: 0.5rem 0.8rem 0.25rem;
  color: color-mix(in srgb, var(--app-muted) 88%, var(--app-heading));
  font-size: 0.68rem;
  font-weight: 850;
  letter-spacing: 0.16em;
  text-transform: uppercase;
}

:global(.routing-target-select-provider-label) {
  padding: 0.45rem 0.9rem 0.2rem;
  color: color-mix(in srgb, var(--app-heading) 72%, var(--app-muted));
  font-size: 0.76rem;
  font-weight: 850;
  letter-spacing: 0.015em;
  text-transform: none;
}

:global(.routing-target-select-item) {
  min-height: 2.35rem;
  color: var(--app-heading);
}

:global(.routing-target-select-model-option) {
  padding-left: 1.15rem;
}

:global(.routing-target-select-item[data-highlighted]:not([data-disabled])) {
  background: color-mix(in srgb, var(--app-accent) 14%, var(--app-surface));
  color: var(--app-heading);
}

:global(.routing-target-select-item[data-highlighted]:not([data-disabled]) .routing-target-select-item-description) {
  color: color-mix(in srgb, var(--app-heading) 78%, var(--app-muted));
}

:global(.routing-target-select-item-wrapper) {
  min-width: 0;
}

:global(.routing-target-select-item-label),
:global(.routing-target-select-item-description),
:global(.routing-target-select-value) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.routing-target-select-value) {
  color: var(--app-heading);
  font-size: 1rem;
  font-weight: 800;
  letter-spacing: 0;
}

:global(.routing-target-select-item-label) {
  max-width: 19rem;
  color: var(--app-heading);
}

:global(.routing-target-select-item-description) {
  max-width: 18rem;
  color: var(--app-muted);
}

:global(.routing-target-select-item-description:empty) {
  display: none;
}

:global(.routing-target-select-input) {
  min-width: 100%;
  background: var(--app-surface);
  color: var(--app-heading);
}
</style>
