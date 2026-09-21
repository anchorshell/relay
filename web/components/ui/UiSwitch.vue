<script setup lang="ts">
const props = withDefaults(defineProps<{
  modelValue: boolean
  label: string
  description?: string
  disabled?: boolean
}>(), {
  description: '',
  disabled: false
})

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
}>()

function toggle() {
  if (props.disabled) return
  emit('update:modelValue', !props.modelValue)
}
</script>

<template>
  <button
    type="button"
    class="ui-switch cursor-pointer"
    role="switch"
    :aria-checked="modelValue"
    :disabled="disabled"
    @click="toggle"
  >
    <span :class="['ui-switch-track', modelValue && 'ui-switch-track-on']" aria-hidden="true">
      <span :class="['ui-switch-thumb', modelValue && 'ui-switch-thumb-on']" />
    </span>
    <span class="ui-switch-copy">
      <span class="ui-switch-label">{{ label }}</span>
      <span v-if="description" class="ui-switch-description">{{ description }}</span>
    </span>
    <span v-if="$slots.aside" class="ui-switch-aside">
      <slot name="aside" />
    </span>
  </button>
</template>

<style scoped>
.ui-switch {
  display: flex;
  width: 100%;
  align-items: flex-start;
  gap: 0.9rem;
  border: 1px solid var(--app-border);
  border-radius: 1rem;
  background: var(--app-surface-muted);
  padding: 0.95rem 1rem;
  text-align: left;
  transition:
    border-color 0.18s ease,
    box-shadow 0.18s ease,
    background-color 0.18s ease;
}

.ui-switch:hover {
  border-color: color-mix(in srgb, var(--app-accent) 24%, var(--app-border));
}

.ui-switch:disabled {
  cursor: not-allowed;
  opacity: 0.62;
}

.ui-switch:disabled:hover {
  border-color: var(--app-border);
}

.ui-switch:focus-visible {
  outline: none;
  border-color: var(--app-focus);
  box-shadow: 0 0 0 4px var(--app-focus-ring);
}

.ui-switch-track {
  position: relative;
  display: inline-flex;
  width: 2.65rem;
  height: 1.45rem;
  flex: 0 0 auto;
  align-items: center;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-input-bg);
  box-shadow: inset 0 1px 6px color-mix(in srgb, var(--app-bg) 35%, transparent);
  transition:
    background-color 0.18s ease,
    border-color 0.18s ease;
}

.ui-switch-track-on {
  border-color: color-mix(in srgb, var(--app-accent) 42%, var(--app-border));
  background: color-mix(in srgb, var(--app-accent) 28%, var(--app-input-bg));
}

.ui-switch-thumb {
  position: absolute;
  left: 0.18rem;
  width: 1.05rem;
  height: 1.05rem;
  border-radius: 999px;
  background: var(--app-muted);
  box-shadow: 0 3px 10px color-mix(in srgb, var(--app-bg) 36%, transparent);
  transition:
    transform 0.2s cubic-bezier(0.22, 1, 0.36, 1),
    background-color 0.18s ease;
}

.ui-switch-thumb-on {
  background: var(--app-accent);
  transform: translateX(1.16rem);
}

.ui-switch-copy {
  display: grid;
  min-width: 0;
  flex: 1 1 auto;
  gap: 0.25rem;
}

.ui-switch-aside {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: flex-start;
}

.ui-switch-label {
  color: var(--app-heading);
  font-size: 0.9rem;
  font-weight: 760;
}

.ui-switch-description {
  color: var(--app-muted);
  font-size: 0.82rem;
  line-height: 1.45rem;
}

@media (prefers-reduced-motion: reduce) {
  .ui-switch,
  .ui-switch-track,
  .ui-switch-thumb {
    transition: none;
  }
}

@media (max-width: 639px) {
  .ui-switch {
    flex-wrap: wrap;
    gap: 0.75rem;
    padding: 0.85rem;
  }

  .ui-switch-aside {
    width: 100%;
    padding-left: 3.4rem;
  }
}
</style>
