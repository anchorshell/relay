<script setup lang="ts">
const props = defineProps<{ code: string, label?: string }>()
const app = useAppStore()

async function copy() {
  if (!process.client) return
  await navigator.clipboard.writeText(props.code)
  app.pushToast({
    title: 'Copied',
    description: props.label ? `${props.label} copied to your clipboard.` : 'Snippet copied to your clipboard.'
  })
}
</script>

<template>
  <div class="app-subsurface overflow-hidden">
    <div class="flex items-center justify-between gap-4 px-5 pb-2 pt-5">
      <p class="app-subtle-text text-xs font-semibold uppercase tracking-[0.2em]">{{ label || 'Snippet' }}</p>
      <UiButton tone="ghost" size="sm" @click="copy">Copy</UiButton>
    </div>
    <div class="px-4 pb-4 pt-2">
      <pre class="app-code border-white/7">{{ code }}</pre>
    </div>
  </div>
</template>
