<script setup lang="ts">
const props = withDefaults(defineProps<{
  additionalHasChanges?: boolean
  saveAdditional?: () => Promise<number>
}>(), {
  additionalHasChanges: false,
  saveAdditional: undefined
})

const settings = useSettingsStore()
const app = useAppStore()
const pageData = useAdminPageDataStore()
const editable = reactive<Record<string, any>>({})
const baseline = ref<Record<string, number | boolean>>({})
const saving = ref(false)
const relayPermissions = useModelRelayPermissions()
const canViewSettings = computed(() => relayPermissions.hasAny(['relay:settings:view', 'relay:settings:manage']))
const canManageSettings = computed(() => relayPermissions.has('relay:settings:manage'))

const schedulerFields = [
  { key: 'default_max_wait_ms', label: 'Default cooldown max wait (ms)', help: 'How long Model Relay should wait through cooldown for the preferred model before considering fallback.', placeholder: '60000' },
  { key: 'default_max_latency_ms', label: 'Default max latency (ms)', help: 'How long Model Relay should let one upstream request run before cancelling and retrying it transparently.', placeholder: '600000' }
] as const

function normalizedValues(source: Record<string, any>) {
  return {
    default_max_wait_ms: Number(source.default_max_wait_ms ?? 0),
    default_max_latency_ms: Number(source.default_max_latency_ms ?? 0),
    reserve_estimated_tokens_for_limits: Boolean(source.reserve_estimated_tokens_for_limits),
    reserve_estimated_spend_for_limits: Boolean(source.reserve_estimated_spend_for_limits),
    store_requests: Boolean(source.store_requests)
  }
}

watch(
  () => settings.parsed,
  (parsed) => {
    const next = normalizedValues({
      ...parsed,
      default_max_wait_ms: parsed.default_max_wait_ms ?? 60000,
      default_max_latency_ms: parsed.default_max_latency_ms ?? 600000,
      reserve_estimated_tokens_for_limits: parsed.reserve_estimated_tokens_for_limits ?? true,
      reserve_estimated_spend_for_limits: parsed.reserve_estimated_spend_for_limits ?? true
    })
    Object.assign(editable, next)
    baseline.value = { ...next }
  },
  { immediate: true }
)

const dirtyEntries = computed(() => {
  const current = normalizedValues(editable)
  return Object.entries(current)
    .filter(([key, value]) => !Object.is(value, baseline.value[key]))
    .map(([key, value]) => ({ key, value }))
})
const hasChanges = computed(() => dirtyEntries.value.length > 0 || props.additionalHasChanges)

async function saveSettings() {
  if (!canManageSettings.value || !hasChanges.value || saving.value) return
  saving.value = true
  try {
    let changedCount = dirtyEntries.value.length
    if (dirtyEntries.value.length) {
      await settings.saveMany(dirtyEntries.value)
    }
    if (props.additionalHasChanges && props.saveAdditional) {
      changedCount += await props.saveAdditional()
    }
    app.pushToast({
      title: 'Settings saved',
      description: `${changedCount} setting${changedCount === 1 ? '' : 's'} updated.`
    })
  } catch (cause: any) {
    app.pushToast({
      title: 'Settings could not be saved',
      description: cause?.data?.error || cause?.data?.message || cause?.message || 'Please try again.',
      tone: 'error'
    })
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  if (canViewSettings.value) {
    pageData.load('settings')
  }
})
</script>

<template>
  <div class="space-y-8">
    <div>
      <h1 class="app-title">Settings</h1>
    </div>

    <UiEmptyState
      v-if="!canViewSettings"
      title="Settings unavailable"
      description="Your account does not have permission to view Model Relay settings."
    />

    <UiPanel v-else>
      <div class="space-y-8 pt-4 md:pt-6">
        <section>
          <h2 class="text-lg font-semibold text-white">Scheduler Defaults</h2>
          <p class="mt-1 text-sm leading-6 text-slate-400">Set the default cooldown wait and upstream latency.</p>
          <div class="mt-5 grid gap-5 md:auto-rows-fr md:grid-cols-2">
            <UiField
              v-for="field in schedulerFields"
              :key="field.key"
              :label="field.label"
              :help="field.help"
              required
            >
              <input v-model="editable[field.key]" class="app-input" inputmode="numeric" :placeholder="field.placeholder" :disabled="!canManageSettings" />
            </UiField>
          </div>
        </section>

        <section v-if="$slots['limit-views']" class="border-t border-white/10 pt-8">
          <slot name="limit-views" />
        </section>

        <section class="border-t border-white/10 pt-8">
          <h2 class="text-lg font-semibold text-white">Limit Enforcement</h2>
          <p class="mt-1 text-sm leading-6 text-slate-400">Choose whether token and dollar caps reserve estimated usage before dispatch or wait until recorded usage reaches the threshold.</p>
          <div class="mt-5 grid gap-5 xl:grid-cols-2">
            <UiSwitch
              v-model="editable.reserve_estimated_tokens_for_limits"
              label="Stop before token caps using estimates"
              description="When enabled, Model Relay reserves estimated input and output tokens before dispatch so requests do not intentionally cross token caps. When disabled, token caps stop new work only after recorded token usage reaches the cap."
              :disabled="!canManageSettings"
            />
            <UiSwitch
              v-model="editable.reserve_estimated_spend_for_limits"
              label="Stop before dollar caps using estimates"
              description="When enabled, Model Relay reserves estimated request cost before dispatch so requests do not intentionally cross dollar spend caps. When disabled, dollar caps stop new work only after recorded spend reaches the cap."
              :disabled="!canManageSettings"
            />
          </div>
        </section>

        <section v-if="$slots.characterization" class="border-t border-white/10 pt-8">
          <slot name="characterization" :saving="saving" />
        </section>

        <section class="border-t border-white/10 pt-8">
          <h2 class="text-lg font-semibold text-white">Request Payloads</h2>
          <p class="mt-1 text-sm leading-6 text-slate-400">Control whether new logs retain client requests, upstream requests, and downstream responses.</p>
          <div class="mt-5">
            <UiSwitch
              v-model="editable.store_requests"
              label="Store request payloads"
              description="When enabled, the Logs page can show payloads for new traffic. Keep this off when retention is not needed."
              :disabled="!canManageSettings"
            />
          </div>
        </section>

        <div v-if="canManageSettings" class="flex justify-end border-t border-white/10 pt-7">
          <UiButton class="w-full sm:w-auto" :disabled="saving || !hasChanges" @click="saveSettings">
            {{ saving ? 'Saving...' : 'Save' }}
          </UiButton>
        </div>
      </div>
    </UiPanel>
  </div>
</template>
