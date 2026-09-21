<script setup lang="ts">
const props = withDefaults(defineProps<{ value: any, path?: string }>(), { path: '$' })
const emit = defineEmits<{ select: [path: string] }>()

const entries = computed(() => {
  if (Array.isArray(props.value)) return props.value.map((value, index) => ({ key: String(index), value, path: `${props.path}[${index}]` }))
  if (props.value && typeof props.value === 'object') return Object.entries(props.value).map(([key, value]) => ({ key, value, path: /^[A-Za-z0-9_-]+$/.test(key) ? `${props.path}.${key}` : `${props.path}[${JSON.stringify(key)}]` }))
  return []
})

function primitive(value: any) {
  return typeof value === 'string' ? JSON.stringify(value) : String(value)
}
</script>

<template>
  <div class="space-y-1 font-mono text-xs">
    <template v-if="entries.length">
      <details v-for="entry in entries" :key="entry.path" open class="ml-3 border-l border-[var(--app-border)] pl-3">
        <summary v-if="entry.value && typeof entry.value === 'object'" class="cursor-pointer py-1 text-[var(--app-muted)]">
          {{ entry.key }} <span class="opacity-60">{{ Array.isArray(entry.value) ? '[]' : '{}' }}</span>
        </summary>
        <GuardrailsJsonPathTree v-if="entry.value && typeof entry.value === 'object'" :value="entry.value" :path="entry.path" @select="emit('select', $event)" />
        <button v-else type="button" class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed flex w-full items-start gap-2 rounded-lg px-2 py-1 text-left hover:bg-sky-400/10" @click="emit('select', entry.path)">
          <span class="text-sky-300">{{ entry.key }}</span><span class="break-all text-[var(--app-muted)]">{{ primitive(entry.value) }}</span>
        </button>
      </details>
    </template>
    <button v-else type="button" class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed rounded-lg px-2 py-1 text-left text-[var(--app-muted)] hover:bg-sky-400/10" @click="emit('select', path)">{{ primitive(value) }}</button>
  </div>
</template>
