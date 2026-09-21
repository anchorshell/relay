<script setup lang="ts">
const slots = useSlots()

const props = defineProps<{ open: boolean, title: string, subtitle?: string, hideFooter?: boolean }>()
defineEmits<{ close: [] }>()
</script>

<template>
  <transition
    enter-active-class="transition duration-200 ease-out"
    enter-from-class="translate-x-full opacity-0"
    enter-to-class="translate-x-0 opacity-100"
    leave-active-class="transition duration-150 ease-in"
    leave-from-class="translate-x-0 opacity-100"
    leave-to-class="translate-x-full opacity-0"
  >
    <aside v-if="open" class="ui-drawer fixed inset-x-0 right-0 z-[80] flex w-full flex-col border-l shadow-2xl backdrop-blur sm:left-auto sm:max-w-[68rem] 2xl:max-w-[74rem]">
      <div class="ui-drawer-header sticky top-0 z-10 flex items-start justify-between gap-3 border-b px-4 py-4 sm:gap-4 md:px-9 md:py-5">
        <div class="min-w-0">
          <h2 class="app-heading-text text-lg font-semibold sm:text-xl">{{ title }}</h2>
          <p v-if="subtitle" class="app-copy-text mt-1 text-sm">{{ subtitle }}</p>
        </div>
        <UiButton class="shrink-0" tone="ghost" size="sm" @click="$emit('close')">Close</UiButton>
      </div>
      <div :class="['ui-drawer-body min-h-0 flex-1 overflow-y-auto px-4 pt-4 md:px-9 md:pt-5', slots.footer && !props.hideFooter ? 'pb-24 md:pb-28' : 'pb-8 md:pb-10']">
        <slot />
      </div>
      <div v-if="slots.footer && !props.hideFooter" class="ui-drawer-footer border-t px-4 py-3.5 md:px-9 md:py-4">
        <slot name="footer" />
      </div>
    </aside>
  </transition>
</template>
