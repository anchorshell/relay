<script setup lang="ts">
type Engine = 'anchorshell' | 'laya'
withDefaults(defineProps<{
  modelValue: Engine
  disabled?: boolean
  basicLabel?: string
  layaLabel?: string
  laya?: { configured: boolean, ready: boolean } | null
}>(), { disabled: false, laya: null, basicLabel: 'AnchorShell Classifier', layaLabel: 'Laya' })
const emit = defineEmits<{ 'update:modelValue': [value: Engine] }>()
const name = useId()
const options = [
  { value: 'anchorshell' as const, label: 'AnchorShell Classifier', speed: 'Fastest', description: 'Built-in low-latency classification.' },
  { value: 'laya' as const, label: 'Laya', speed: 'Fast', description: 'Typed request classification using Laya.' }
]
</script>

<template>
  <fieldset :disabled="disabled" class="mt-5">
    <legend class="app-heading-text text-sm font-semibold">Characterization model</legend>
    <div class="mt-3 space-y-1">
      <slot name="before-options" />
      <label v-for="option in options" :key="option.value" class="engine-option">
        <input
          :name="name" type="radio" :value="option.value"
          :checked="modelValue === option.value"
          @change="emit('update:modelValue', option.value)"
        >
        <span>
          <span class="app-heading-text font-semibold">{{ option.value === 'anchorshell' ? basicLabel : layaLabel }}</span>
          <span class="app-copy-text ml-2 text-xs">{{ option.speed }}</span>
          <span class="app-copy-text mt-1 block text-sm">{{ option.description }}</span>
        </span>
      </label>
      <slot name="after-options" />
    </div>
    <slot name="help" />
    <p v-if="modelValue === 'laya' && laya && !laya.ready" role="status" class="app-copy-text mt-3 text-sm leading-6">
      Laya is currently unavailable. AnchorShell Classifier will handle requests until it is ready. Your selection will stay saved.
    </p>
  </fieldset>
</template>

<style scoped>
.engine-option { display: flex; align-items: flex-start; gap: 0.75rem; padding: 0.75rem 0; cursor: pointer; }
.engine-option input { margin-top: 0.25rem; accent-color: var(--app-accent); flex-shrink: 0; }
.engine-option input:focus-visible { outline: 2px solid var(--app-focus); outline-offset: 4px; }
fieldset:disabled .engine-option { cursor: default; opacity: 0.65; }
</style>
