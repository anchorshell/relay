<script setup lang="ts">
defineProps<{ title: string, description: string, actionLabel?: string }>()
defineEmits<{ action: [] }>()
</script>

<template>
  <div class="ui-empty flex flex-col items-start gap-5 rounded-[1.45rem] border border-dashed p-5 sm:gap-6 sm:p-10">
    <div class="ui-empty-icon flex h-11 w-11 items-center justify-center rounded-2xl ring-1">
      <slot name="icon">
        <span class="h-2.5 w-2.5 rounded-full bg-amber-300/90" />
      </slot>
    </div>
    <div>
      <h3 class="app-heading-text text-lg font-semibold tracking-tight sm:text-xl">{{ title }}</h3>
      <p class="app-copy-text mt-3 max-w-2xl text-[15px] leading-7">{{ description }}</p>
    </div>
    <UiButton v-if="actionLabel" @click="$emit('action')">{{ actionLabel }}</UiButton>
  </div>
</template>
