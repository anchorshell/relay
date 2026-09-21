<script setup lang="ts">
const app = useAppStore()
</script>

<template>
  <teleport to="body">
    <div
      class="pointer-events-none fixed inset-x-4 bottom-4 z-[200] flex w-auto flex-col gap-3 sm:left-auto sm:right-4 sm:w-full sm:max-w-sm"
      aria-live="polite"
      aria-atomic="false"
    >
      <transition-group name="toast">
        <div
          v-for="toast in app.toasts"
          :key="toast.id"
          class="ui-toast pointer-events-auto rounded-2xl border p-4 shadow-xl"
          :class="{
            'border-rose-400/60': toast.tone === 'error',
            'border-emerald-400/50': toast.tone === 'success'
          }"
          :role="toast.tone === 'error' ? 'alert' : 'status'"
        >
          <p class="app-heading-text text-sm font-semibold">{{ toast.title }}</p>
          <p v-if="toast.description" class="app-subtle-text mt-1 text-sm">{{ toast.description }}</p>
        </div>
      </transition-group>
    </div>
  </teleport>
</template>
