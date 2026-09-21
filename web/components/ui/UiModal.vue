<script setup lang="ts">
defineProps<{
  open: boolean
  title: string
  subtitle?: string
  showEyebrow?: boolean
}>()

defineEmits<{ close: [] }>()
</script>

<template>
  <teleport to="body">
    <transition
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition duration-150 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
    <div v-if="open" class="ui-modal-overlay fixed inset-0 z-40 cursor-pointer backdrop-blur-sm" @click="$emit('close')" />
    </transition>

    <transition
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="translate-y-4 opacity-0"
      enter-to-class="translate-y-0 opacity-100"
      leave-active-class="transition duration-150 ease-in"
      leave-from-class="translate-y-0 opacity-100"
      leave-to-class="translate-y-4 opacity-0"
    >
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center px-3 py-4 sm:px-5 sm:py-8">
        <section class="ui-modal app-themed app-surface flex max-h-[92vh] w-full max-w-[72rem] flex-col overflow-hidden sm:max-h-[90vh]">
          <header class="flex items-start justify-between gap-3 px-4 pb-2 pt-5 sm:gap-4 md:px-8 md:pt-7">
            <div class="min-w-0">
              <p v-if="showEyebrow !== false" class="app-faint-text text-xs uppercase tracking-[0.22em]">Edit</p>
              <h2 class="app-heading-text mt-1.5 text-lg font-semibold tracking-tight sm:text-xl">{{ title }}</h2>
              <p v-if="subtitle" class="app-copy-text mt-1 text-sm leading-6">{{ subtitle }}</p>
            </div>
            <UiButton class="shrink-0" tone="ghost" size="sm" @click="$emit('close')">Close</UiButton>
          </header>
          <div class="min-h-0 flex-1 overflow-y-auto px-4 pb-6 pt-3 md:px-8 md:pb-9 md:pt-4">
            <slot />
          </div>
        </section>
      </div>
    </transition>
  </teleport>
</template>
