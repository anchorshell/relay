<script setup lang="ts">
type TabItem = string | { value: string, label: string, disabled?: boolean, title?: string }

const props = defineProps<{ tabs: TabItem[], modelValue: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

function valueFor(tab: TabItem) {
  return typeof tab === 'string' ? tab : tab.value
}

function labelFor(tab: TabItem) {
  if (typeof tab !== 'string') return tab.label
  return tab
    .replaceAll('_', ' ')
    .replace(/\b\w/g, match => match.toUpperCase())
}

const items = computed(() => props.tabs.map(tab => ({
  value: valueFor(tab),
  label: labelFor(tab),
  disabled: typeof tab === 'string' ? false : Boolean(tab.disabled),
  title: typeof tab === 'string' ? '' : tab.title || ''
})))

function updateValue(value: string | number) {
  emit('update:modelValue', String(value))
}
</script>

<template>
  <UTabs
    :model-value="modelValue"
    :items="items"
    :content="false"
    activation-mode="automatic"
    variant="pill"
    class="ui-tabs"
    :ui="{
      list: 'ui-tabs-list',
      indicator: 'ui-tabs-indicator',
      trigger: 'ui-tabs-trigger',
      label: 'ui-tabs-label'
    }"
    @update:model-value="updateValue"
  >
    <template #default="{ item }">
      <span :title="item.title || undefined">{{ item.label }}</span>
    </template>
  </UTabs>
</template>
