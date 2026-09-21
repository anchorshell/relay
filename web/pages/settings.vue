<script setup lang="ts">
const discovery = useUpgradeDiscovery()
const settings = useSettingsStore()
const permissions = useModelRelayPermissions()
const engine = ref<'anchorshell' | 'laya'>('anchorshell')
const baseline = ref<'anchorshell' | 'laya'>('anchorshell')
const laya = ref<{ configured: boolean, ready: boolean } | null>(null)
watch(() => settings.parsed.characterization_engine, value => {
  const next = value === 'laya' ? 'laya' : 'anchorshell'
  // Other settings can refresh during Save; do not discard an unsaved choice.
  if (engine.value === baseline.value) engine.value = next
  baseline.value = next
}, { immediate: true })
async function saveEngine() {
  const selected = engine.value
  await settings.save('characterization_engine', selected)
  baseline.value = selected
  return 1
}
onMounted(async () => {
  if (!permissions.hasAny(['relay:settings:view', 'relay:settings:manage'])) return
  try {
    const response = await useRelayApi().get<{ laya: { configured: boolean, ready: boolean } }>('/api/system')
    laya.value = response.laya
  } catch { /* Settings remain usable when the optional readiness check fails. */ }
})
</script>

<template>
  <div class="space-y-8">
    <BaseSettingsPage :additional-has-changes="engine !== baseline" :save-additional="saveEngine">
      <template #characterization="{ saving }">
        <h2 class="app-heading-text text-lg font-semibold">Request Characterization</h2>
        <p class="app-copy-text mt-1 text-sm leading-6">Choose how this Relay deployment classifies requests. Automatic Smart Group assignments are available in hosted Relay.</p>
        <CommunityCharacterizationEngineSelector v-model="engine" :laya="laya" :disabled="saving || !permissions.has('relay:settings:manage')" />
      </template>
    </BaseSettingsPage>
    <section v-if="discovery" class="border-t border-[var(--app-border)] pt-7" aria-labelledby="relay-access-title">
      <h2 id="relay-access-title" class="app-heading-text text-lg font-semibold">API access &amp; permissions</h2>
      <p class="app-copy-text mt-2 max-w-2xl text-sm leading-6">
        Open-source Relay uses <code>RELAY_API_TOKEN</code> for deployment-wide inference access
        when configured, separately from management authentication. For individual credentials
        and team access controls, explore hosted Relay, which includes a free tier.
      </p>
      <div class="mt-4 flex flex-wrap gap-3">
        <UpgradeFeatureButton feature="apiKeys" size="sm" />
        <UpgradeFeatureButton feature="permissions" size="sm" />
      </div>
    </section>
  </div>
</template>
