<script setup lang="ts">
const props = withDefaults(defineProps<{
  title?: string
  subtitle?: string
  padded?: boolean
  compact?: boolean
  tone?: 'surface' | 'subsurface'
}>(), {
  tone: 'surface'
})
</script>

<template>
  <section :class="[
    'ui-panel',
    props.tone === 'subsurface' ? 'app-subsurface' : 'app-surface',
    props.compact ? 'px-4 pb-3 sm:px-5 sm:pb-4' : 'px-4 pb-5 sm:px-6 sm:pb-6'
  ]">
    <header
      v-if="title || subtitle"
      :class="['ui-panel__header', props.compact
        ? 'flex flex-col gap-2 pb-0.5 pt-3.5 sm:flex-row sm:items-start sm:justify-between sm:gap-3 md:pt-4'
        : 'flex flex-col gap-3 pb-1 pt-5 sm:flex-row sm:items-start sm:justify-between sm:gap-4 md:pt-7']"
    >
      <div class="min-w-0">
        <h3 v-if="title" class="app-heading-text text-[18px] font-semibold tracking-tight">{{ title }}</h3>
        <p v-if="subtitle" :class="props.compact ? 'app-copy-text mt-1 max-w-3xl text-sm leading-5' : 'app-copy-text mt-1.5 max-w-3xl text-sm leading-6'">{{ subtitle }}</p>
      </div>
      <slot name="header" />
    </header>
    <div
      :class="['ui-panel__body', props.padded === false
        ? ''
        : props.title || props.subtitle
          ? props.compact
            ? 'px-1 pt-2 md:px-2 md:pt-2.5'
            : 'px-1 pt-4 md:px-2 md:pt-4'
          : props.tone === 'subsurface'
            ? 'px-1 pt-1 md:px-2'
            : 'px-1 pt-1 md:px-2']"
    >
      <slot />
    </div>
  </section>
</template>
