<script setup lang="ts">
import type { Endpoint, LimitPolicy, PricingPolicy, Provider } from '~/types/admin'

const props = withDefaults(defineProps<{
  subscriptionProviderIds?: string[]
  showSubscriptionSection?: boolean
}>(), {
  subscriptionProviderIds: () => [],
  showSubscriptionSection: false
})

const catalog = useCatalogStore()
const api = useRelayApi()
const pageData = useAdminPageDataStore()
const app = useAppStore()
const { modelRelayPath } = useModelRelayRoute()
const relayPermissions = useModelRelayPermissions()
const canManageProviders = computed(() => relayPermissions.has('relay:providers:manage'))
const canManageLimits = computed(() => relayPermissions.has('relay:limits:manage'))
const subscriptionProviderIDSet = computed(() => new Set(props.subscriptionProviderIds))
const subscriptionProviders = computed(() => catalog.providers.filter(provider => isSubscriptionProvider(provider.id)))
const apiKeyProviders = computed(() => catalog.providers.filter(provider => !isSubscriptionProvider(provider.id)))
const providerSections = computed(() => [
  ...(props.showSubscriptionSection
    ? [{ key: 'subscription', label: 'Subscription Models', emptyLabel: 'No subscription models are connected yet.', providers: subscriptionProviders.value }]
    : []),
  { key: 'api-key', label: 'API Key Models', emptyLabel: 'No API key models have been added yet.', providers: apiKeyProviders.value }
])

function isSubscriptionProvider(providerId: string) {
  return subscriptionProviderIDSet.value.has(providerId)
}

const editOpen = ref(false)
const selectedModelID = ref('')
const limitsModelID = ref('')
const duplicateModelOpen = ref(false)
const duplicateModelID = ref('')
const duplicatingModel = ref(false)
const duplicateProviderOpen = ref(false)
const duplicateProviderID = ref('')
const duplicatingProvider = ref(false)
const deleteModelOpen = ref(false)
const deleteModelID = ref('')
const deletingModel = ref(false)
const deleteProviderOpen = ref(false)
const deleteProviderPendingID = ref('')
const deletingProvider = ref(false)
const showSecret = ref(false)
const form = reactive({
  id: '',
  name: '',
  base_url: '',
  max_latency_ms: '',
  auth_mode: 'bearer_static',
  auth_header_name: 'Authorization',
  api_key: '',
  notes: '',
  enabled: true
})

function openProviderSetup() {
  if (!canManageProviders.value) return
  return navigateTo(modelRelayPath('/setup'))
}

function addModel(providerId: string) {
  if (!canManageProviders.value || isSubscriptionProvider(providerId)) return
  return navigateTo({ path: modelRelayPath('/setup'), query: { providerId: String(providerId), mode: 'model' } })
}

function providerCredential(providerId: string) {
  return catalog.credentials
    .filter((item) => item.provider_id === providerId)
    .sort((a, b) => a.id.localeCompare(b.id))[0]
}

function modelGroups(endpointId: string) {
  return catalog.memberships
    .filter((item) => item.endpoint_id === endpointId)
    .sort((a, b) => a.manual_rank - b.manual_rank)
    .map((item) => catalog.laneMap[item.lane_id]?.name)
    .filter((value): value is string => !!value)
}

function openEdit(provider: Provider) {
  if (isSubscriptionProvider(provider.id)) return
  showSecret.value = false
  Object.assign(form, {
    id: provider.id,
    name: provider.name,
    base_url: provider.base_url,
    max_latency_ms: String(provider.max_latency_ms || ''),
    auth_mode: provider.auth_mode || 'bearer_static',
    auth_header_name: provider.auth_header_name || 'Authorization',
    api_key: '',
    notes: provider.notes || '',
    enabled: provider.enabled
  })
  editOpen.value = true
}

function requestDeleteProvider(provider: Provider) {
  if (!canManageProviders.value || isSubscriptionProvider(provider.id)) return
  deleteProviderPendingID.value = provider.id
  deleteProviderOpen.value = true
}

function closeDeleteProvider() {
  deleteProviderOpen.value = false
  deleteProviderPendingID.value = ''
}

function requestDuplicateProvider(provider: Provider) {
  if (!canManageProviders.value || isSubscriptionProvider(provider.id)) return
  duplicateProviderID.value = provider.id
  duplicateProviderOpen.value = true
}

function closeDuplicateProvider() {
  duplicateProviderOpen.value = false
  duplicateProviderID.value = ''
}

function providerMoreMenuItems(provider: Provider) {
  if (!canManageProviders.value || isSubscriptionProvider(provider.id)) return []
  return [[
    {
      label: 'Duplicate Provider',
      onSelect: () => requestDuplicateProvider(provider)
    },
    {
      label: 'Delete Provider',
      color: 'error' as const,
      onSelect: () => requestDeleteProvider(provider)
    }
  ]]
}

const duplicateProviderTarget = computed(() => catalog.providerMap[duplicateProviderID.value] || null)

function duplicateProviderName(provider: Provider) {
  const existingNames = new Set(catalog.providers.map((item) => item.name.trim().toLowerCase()))
  let candidate = `${provider.name.trim()} copy`
  let index = 2
  while (existingNames.has(candidate.toLowerCase())) {
    candidate = `${provider.name.trim()} copy ${index}`
    index += 1
  }
  return candidate
}

async function confirmDuplicateProvider() {
  if (!canManageProviders.value) return
  const source = duplicateProviderTarget.value
  if (!source || duplicatingProvider.value) return

  duplicatingProvider.value = true
  const duplicateName = duplicateProviderName(source)
  try {
    const duplicate = await api.post<Provider>('/api/providers', {
      name: duplicateName,
      slug: duplicateName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''),
      base_url: source.base_url,
      max_latency_ms: source.max_latency_ms,
      auth_mode: source.auth_mode,
      auth_header_name: source.auth_header_name,
      enabled: source.auth_mode === 'none' ? source.enabled : false,
      notes: source.notes,
      health_status: 'healthy'
    })

    const sourceProviderLimits = catalog.limitPolicies.filter((item) =>
      item.scope_type === 'provider' &&
      item.scope_id === source.id
    )
    await Promise.all(sourceProviderLimits.map((item) => api.post<LimitPolicy>('/api/limit-policies', {
      scope_type: 'provider',
      scope_id: duplicate.id,
      metric: item.metric,
      period: item.period,
      limit_value: item.limit_value,
      enabled: item.enabled,
      source: item.source || 'configured'
    })))

    const sourceModels = catalog.endpointsByProvider(source.id)
    for (const sourceModel of sourceModels) {
      const duplicateModel = await api.post<Endpoint>('/api/endpoints', {
        provider_id: duplicate.id,
        credential_id: '',
        name: displayModelName(sourceModel),
        upstream_model: sourceModel.upstream_model,
        route_kind: sourceModel.route_kind,
        enabled: duplicate.enabled && sourceModel.enabled,
        pacing: sourceModel.pacing !== false,
        manual_rank: sourceModel.manual_rank,
        suggested_rank: sourceModel.suggested_rank,
        suggested_score: sourceModel.suggested_score,
        quality_score: sourceModel.quality_score,
        context_window: sourceModel.context_window,
        param_size_b: sourceModel.param_size_b,
        modalities: [...(sourceModel.modalities || [])],
        descriptor_tags: [...(sourceModel.descriptor_tags || [])],
        supports_streaming: sourceModel.supports_streaming,
        supports_tools: sourceModel.supports_tools,
        supports_vision: sourceModel.supports_vision,
        reasoning_control_kind: sourceModel.reasoning_control_kind || 'none',
        allowed_reasoning_efforts: [...(sourceModel.allowed_reasoning_efforts || [])],
        default_reasoning_effort: sourceModel.default_reasoning_effort || '',
        maximum_reasoning_effort: sourceModel.maximum_reasoning_effort || '',
        health_status: 'healthy',
        cooldown_until: null,
        max_latency_ms: sourceModel.max_latency_ms,
        notes: sourceModel.notes
      })

      const sourceModelLimits = catalog.limitPolicies.filter((item) =>
        item.scope_type === 'endpoint' &&
        item.scope_id === sourceModel.id
      )
      await Promise.all(sourceModelLimits.map((item) => api.post<LimitPolicy>('/api/limit-policies', {
        scope_type: 'endpoint',
        scope_id: duplicateModel.id,
        metric: item.metric,
        period: item.period,
        limit_value: item.limit_value,
        enabled: item.enabled,
        source: item.source || 'configured'
      })))

      const pricing = catalog.endpointPricing(sourceModel.id)
      if (pricing) {
        await api.post<PricingPolicy>('/api/pricing-policies', {
          endpoint_id: duplicateModel.id,
          currency: pricing.currency,
          input_cost_micros_per_1m_tokens: pricing.input_cost_micros_per_1m_tokens,
          output_cost_micros_per_1m_tokens: pricing.output_cost_micros_per_1m_tokens,
          cached_input_cost_micros_per_1m_tokens: pricing.cached_input_cost_micros_per_1m_tokens,
          flat_request_cost_micros: pricing.flat_request_cost_micros
        })
      }
    }

    await pageData.load('providers')
    closeDuplicateProvider()
    app.pushToast({
      title: 'Provider duplicated',
      description: `${duplicate.name} was created with ${sourceModels.length} model${sourceModels.length === 1 ? '' : 's'} and no routing-group memberships${source.auth_mode === 'none' ? '.' : '. Add its API key before enabling it.'}`
    })
  } catch (cause: any) {
    app.pushToast({
      title: 'Provider could not be duplicated',
      description: cause?.data?.error || cause?.data?.message || cause?.message || 'Please try again.',
      tone: 'error'
    })
  } finally {
    duplicatingProvider.value = false
  }
}

async function persistProvider() {
  if (!canManageProviders.value) return
  const saved = await api.put<Provider>(`/api/providers/${form.id}`, {
    name: form.name.trim(),
    slug: form.name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''),
    base_url: form.base_url.trim().replace(/\/+$/, ''),
    max_latency_ms: Number(form.max_latency_ms || 0),
    auth_mode: form.auth_mode,
    auth_header_name: form.auth_mode === 'custom_header' ? form.auth_header_name.trim() : form.auth_mode === 'none' ? '' : 'Authorization',
    enabled: form.enabled,
    notes: form.notes.trim(),
    health_status: catalog.providerMap[form.id]?.health_status || 'healthy'
  })

  const existingCredential = providerCredential(saved.id)
  if (form.auth_mode === 'none') {
    if (existingCredential) {
      await catalog.saveCredential({
        id: existingCredential.id,
        provider_id: saved.id,
        name: existingCredential.name,
        enabled: false
      })
    }
  } else if (form.api_key.trim() || existingCredential) {
    await catalog.saveCredential({
      id: existingCredential?.id,
      provider_id: saved.id,
      name: existingCredential?.name || `${saved.name} key`,
      secret: form.api_key.trim() || undefined,
      enabled: saved.enabled
    })
  }

  await pageData.load('providers')
  editOpen.value = false
  app.pushToast({ title: 'Provider updated', description: 'Connection details and access settings were saved.' })
}

async function saveProvider() {
  try {
    await persistProvider()
  } catch (cause: any) {
    app.pushToast({
      title: 'Provider could not be saved',
      description: cause?.data?.error || cause?.data?.message || cause?.message || 'Please choose a different provider name.',
      tone: 'error'
    })
  }
}

async function confirmDeleteProvider() {
  const target = deleteProviderTarget.value
  if (!target || deletingProvider.value) return
  deletingProvider.value = true
  try {
    await api.del(`/api/providers/${target.provider.id}`)
    await pageData.load('providers')
    closeDeleteProvider()
    app.pushToast({
      title: 'Provider deleted',
      description: `${target.provider.name} and its models were deleted.`
    })
  } finally {
    deletingProvider.value = false
  }
}

function openModelEdit(model: Endpoint) {
  if (isSubscriptionProvider(model.provider_id)) return
  selectedModelID.value = model.id
}

function closeModelEdit() {
  selectedModelID.value = ''
}

function openModelLimits(model: Endpoint) {
  if (!isSubscriptionProvider(model.provider_id) || !canManageLimits.value) return
  limitsModelID.value = model.id
}

function closeModelLimits() {
  limitsModelID.value = ''
  void pageData.load('providers')
}

function displayModelName(model: Endpoint) {
  return model.name || model.upstream_model
}

function modelCallName(model: Endpoint) {
  const providerSlug = catalog.providerMap[model.provider_id]?.slug?.trim() || 'provider-missing'
  const modelSlug = model.slug?.trim() || 'unnamed-model'
  return `${providerSlug}/${modelSlug}`
}

async function copyModelCallName(model: Endpoint) {
  const callName = modelCallName(model)
  try {
    await navigator.clipboard.writeText(callName)
    app.pushToast({
      title: 'Model name copied',
      description: `${callName} was copied to your clipboard.`,
      tone: 'success'
    })
  } catch {
    app.pushToast({
      title: 'Could not copy model name',
      description: 'Copy the model name manually and try again.',
      tone: 'error'
    })
  }
}

function requestDuplicateModel(model: Endpoint) {
  if (!canManageProviders.value || isSubscriptionProvider(model.provider_id)) return
  duplicateModelID.value = model.id
  duplicateModelOpen.value = true
}

function closeDuplicateModel() {
  duplicateModelOpen.value = false
  duplicateModelID.value = ''
}

const duplicateModelTarget = computed(() => catalog.endpointMap[duplicateModelID.value] || null)

function requestDeleteModel(model: Endpoint) {
  if (!canManageProviders.value || isSubscriptionProvider(model.provider_id)) return
  deleteModelID.value = model.id
  deleteModelOpen.value = true
}

function closeDeleteModel() {
  deleteModelOpen.value = false
  deleteModelID.value = ''
}

function modelMoreMenuItems(model: Endpoint) {
  if (!canManageProviders.value || isSubscriptionProvider(model.provider_id)) return []
  return [[
    {
      label: 'Duplicate Model',
      onSelect: () => requestDuplicateModel(model)
    },
    {
      label: 'Delete Model',
      color: 'error' as const,
      onSelect: () => requestDeleteModel(model)
    }
  ]]
}

function duplicateModelName(model: Endpoint) {
  const baseName = displayModelName(model)
  const providerModels = catalog.endpointsByProvider(model.provider_id)
  const existingNames = new Set(providerModels.map((item) => displayModelName(item).toLowerCase()))
  let candidate = `${baseName} copy`
  let index = 2
  while (existingNames.has(candidate.toLowerCase())) {
    candidate = `${baseName} copy ${index}`
    index += 1
  }
  return candidate
}

async function confirmDuplicateModel() {
  if (!canManageProviders.value) return
  const source = duplicateModelTarget.value
  if (!source || duplicatingModel.value) return

  duplicatingModel.value = true
  try {
    const providerModels = catalog.endpointsByProvider(source.provider_id)
    const duplicateName = duplicateModelName(source)
    const duplicate = await api.post<Endpoint>('/api/endpoints', {
      provider_id: source.provider_id,
      credential_id: source.credential_id,
      name: duplicateName,
      upstream_model: source.upstream_model,
      route_kind: source.route_kind,
      enabled: source.enabled,
      pacing: source.pacing !== false,
      manual_rank: providerModels.length + 1,
      suggested_rank: source.suggested_rank,
      suggested_score: source.suggested_score,
      quality_score: source.quality_score,
      context_window: source.context_window,
      param_size_b: source.param_size_b,
      modalities: [...(source.modalities || [])],
      descriptor_tags: [...(source.descriptor_tags || [])],
      supports_streaming: source.supports_streaming,
      supports_tools: source.supports_tools,
      supports_vision: source.supports_vision,
      health_status: 'healthy',
      cooldown_until: null,
      max_latency_ms: source.max_latency_ms,
      notes: source.notes
    })

    const sourceLimits = catalog.limitPolicies.filter((item) =>
      item.scope_type === 'endpoint' &&
      item.scope_id === source.id
    )
    await Promise.all(sourceLimits.map((item) => api.post<LimitPolicy>('/api/limit-policies', {
      scope_type: 'endpoint',
      scope_id: duplicate.id,
      metric: item.metric,
      period: item.period,
      limit_value: item.limit_value,
      enabled: item.enabled,
      source: item.source || 'configured'
    })))

    const pricing = catalog.endpointPricing(source.id)
    if (pricing) {
      await api.post<PricingPolicy>('/api/pricing-policies', {
        endpoint_id: duplicate.id,
        currency: pricing.currency,
        input_cost_micros_per_1m_tokens: pricing.input_cost_micros_per_1m_tokens,
        output_cost_micros_per_1m_tokens: pricing.output_cost_micros_per_1m_tokens,
        cached_input_cost_micros_per_1m_tokens: pricing.cached_input_cost_micros_per_1m_tokens,
        flat_request_cost_micros: pricing.flat_request_cost_micros
      })
    }

    await pageData.load('providers')
    closeDuplicateModel()
    app.pushToast({
      title: 'Model duplicated',
      description: `${duplicateName} was copied with its limits and pricing. Add it to a group when you want it to receive traffic.`
    })
  } finally {
    duplicatingModel.value = false
  }
}

const deleteModelTarget = computed(() => {
  const model = catalog.endpointMap[deleteModelID.value]
  if (!model) return null
  return {
    model,
    provider: catalog.providerMap[model.provider_id] || null,
    groups: modelGroups(model.id)
  }
})

async function confirmDeleteModel() {
  if (!canManageProviders.value) return
  const target = deleteModelTarget.value
  if (!target || deletingModel.value) return

  deletingModel.value = true
  try {
    await api.del(`/api/endpoints/${target.model.id}`)
    if (selectedModelID.value === target.model.id) {
      selectedModelID.value = ''
    }
    await pageData.load('providers')
    closeDeleteModel()
    app.pushToast({
      title: 'Model deleted',
      description: `${displayModelName(target.model)} was deleted from ${target.provider?.name || 'this provider'}.`
    })
  } finally {
    deletingModel.value = false
  }
}

const deleteProviderTarget = computed(() => {
  const provider = catalog.providerMap[deleteProviderPendingID.value]
  if (!provider) return null
  const models = catalog.endpointsByProvider(provider.id).map((endpoint) => ({
    endpoint,
    groups: modelGroups(endpoint.id)
  }))
  return {
    provider,
    models,
    groupLinks: models.reduce((sum, item) => sum + item.groups.length, 0)
  }
})

onMounted(() => pageData.load('providers'))
</script>

<template>
  <div>
    <div class="space-y-8">
      <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div>
          <h1 class="app-title">Providers and Models</h1>
        </div>
        <div v-if="canManageProviders || $slots['provider-header-actions']" class="flex w-full flex-col gap-2 sm:w-auto sm:flex-row">
          <slot name="provider-header-actions" />
          <UiButton v-if="canManageProviders" icon="i-lucide-plus" class="w-full sm:w-auto" @click="openProviderSetup">Add API Provider</UiButton>
        </div>
      </div>

      <section>
        <UiEmptyState
          v-if="!catalog.providers.length"
          title="No providers connected yet"
          description="Add an API provider or subscription plan to start routing traffic."
          :action-label="canManageProviders ? 'Add API Provider' : ''"
          @action="openProviderSetup"
        />

        <div v-else class="space-y-8">
          <section v-for="section in providerSections" :key="section.key" class="ui-directory-section" :aria-labelledby="`provider-section-${section.key}`">
            <header class="ui-directory-section__header">
              <h2 :id="`provider-section-${section.key}`" class="ui-directory-section__title">{{ section.label }}</h2>
            </header>

            <div v-if="section.providers.length" class="ui-directory" :aria-label="section.label">
              <section v-for="provider in section.providers" :key="provider.id" class="ui-directory__parent">
                <header class="ui-directory__heading">
                  <h3 class="ui-directory__name text-base">{{ provider.name }}</h3>
                  <div class="ui-directory__actions">
                    <slot v-if="isSubscriptionProvider(provider.id)" name="subscription-provider-actions" :provider="provider" />
                    <template v-else>
                      <UiButton tone="secondary" size="sm" @click="openEdit(provider)">{{ canManageProviders ? 'Edit provider' : 'View provider' }}</UiButton>
                      <UiButton v-if="canManageProviders" tone="secondary" size="sm" @click="addModel(provider.id)">Add model</UiButton>
                      <UDropdownMenu
                        v-if="canManageProviders"
                        :items="providerMoreMenuItems(provider)"
                        :content="{ align: 'end', sideOffset: 8, collisionPadding: 12 }"
                        :ui="{ content: 'provider-more-menu-panel' }"
                      >
                        <button type="button" class="ui-action-menu-button" aria-label="More provider actions">
                          <span>More</span>
                          <UIcon name="i-lucide-ellipsis" class="h-4 w-4" />
                        </button>
                      </UDropdownMenu>
                    </template>
                  </div>
                </header>

                <ul class="ui-directory__children" :aria-label="`Models for ${provider.name}`">
                  <li v-for="model in catalog.endpointsByProvider(provider.id)" :key="model.id" class="ui-directory__child">
                    <div class="min-w-0">
                      <p class="ui-directory__name">{{ displayModelName(model) }}</p>
                      <div class="ui-directory__detail flex min-w-0 items-center gap-1.5">
                        <code class="min-w-0 break-all font-mono">{{ modelCallName(model) }}</code>
                        <button
                          type="button"
                          class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-[var(--app-muted)] transition-colors hover:bg-[var(--app-nav-hover-bg)] hover:text-[var(--app-heading)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus)]"
                          :aria-label="`Copy ${modelCallName(model)} to clipboard`"
                          title="Copy model name"
                          @click="copyModelCallName(model)"
                        >
                          <UIcon name="i-lucide-copy" class="h-3.5 w-3.5" />
                        </button>
                      </div>
                    </div>

                    <div class="ui-directory__actions">
                      <UiButton v-if="isSubscriptionProvider(provider.id) && canManageLimits" tone="secondary" size="sm" @click="openModelLimits(model)">Edit limits</UiButton>
                      <template v-else-if="!isSubscriptionProvider(provider.id)">
                        <UiButton tone="secondary" size="sm" @click="openModelEdit(model)">{{ canManageProviders ? 'Edit model' : 'View model' }}</UiButton>
                        <UDropdownMenu
                          v-if="canManageProviders"
                          :items="modelMoreMenuItems(model)"
                          :content="{ align: 'end', sideOffset: 8, collisionPadding: 12 }"
                          :ui="{ content: 'provider-more-menu-panel' }"
                        >
                          <button type="button" class="ui-action-menu-button" aria-label="More model actions">
                            <span>More</span>
                            <UIcon name="i-lucide-ellipsis" class="h-4 w-4" />
                          </button>
                        </UDropdownMenu>
                      </template>
                    </div>
                  </li>
                  <li v-if="!catalog.endpointsByProvider(provider.id).length" class="ui-directory__child">
                    <p class="ui-directory__detail">No models have been added under this provider yet.</p>
                  </li>
                </ul>
              </section>
            </div>
            <p v-else class="ui-directory-section__empty">{{ section.emptyLabel }}</p>
          </section>
        </div>
      </section>
    </div>

    <UiInspectorDrawer
      :open="editOpen"
      :title="form.name || (canManageProviders ? 'Edit Provider' : 'View Provider')"
      subtitle="Adjust the provider connection, authentication, and timeout behavior from the same inspector pattern used for models."
      @close="editOpen = false"
    >
      <div class="space-y-8">
        <UiPanel tone="subsurface" title="Core details" subtitle="Keep the provider editor focused on connection settings that materially affect how Model Relay reaches the upstream.">
          <div class="space-y-5">
            <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2">
              <UiField label="Provider name" help="Human-friendly name shown across the console." required>
                <input v-model="form.name" class="app-input" placeholder="OpenAI production" :disabled="!canManageProviders" />
              </UiField>
              <UiField label="Base URL" help="The upstream API root Model Relay should call." required>
                <input v-model="form.base_url" class="app-input" placeholder="https://api.example.com/v1" :disabled="!canManageProviders" />
              </UiField>
            </div>

            <label class="cursor-pointer has-disabled:cursor-not-allowed block border-y border-[var(--app-border)] py-4 text-sm text-slate-200/84">
              <span class="flex items-start gap-3">
                <input v-model="form.enabled" type="checkbox" class="app-checkbox mt-1" :disabled="!canManageProviders" />
                <span>
                  <span class="block font-medium text-white">Provider enabled</span>
                  <span class="mt-1 block leading-6 text-slate-300/72">Disable this provider if you want to keep the record but stop routing traffic through it.</span>
                </span>
              </span>
            </label>
          </div>
        </UiPanel>

        <UiPanel tone="subsurface" title="Access and timeouts" subtitle="Configure how Model Relay authenticates upstream requests and how long it should tolerate a hanging provider call.">
          <div class="space-y-5">
            <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2">
              <UiField label="Access method" help="How Model Relay should authenticate upstream requests." required>
                <select v-model="form.auth_mode" class="app-select" :disabled="!canManageProviders">
                  <option value="bearer_static">Bearer API key</option>
                  <option value="none">No authentication</option>
                  <option value="custom_header">Custom header</option>
                </select>
              </UiField>

              <UiField label="Max latency (ms)" help="Optional provider-wide timeout before Model Relay cancels and retries a hanging request. Leave blank to inherit the system default." optional>
                <input v-model="form.max_latency_ms" class="app-input" inputmode="numeric" placeholder="600000" :disabled="!canManageProviders" />
              </UiField>
            </div>

            <UiField v-if="form.auth_mode !== 'none'" label="API key or secret" help="Stored securely. Leave blank to keep the current secret." optional>
              <div class="relative">
                <input v-model="form.api_key" :type="showSecret ? 'text' : 'password'" class="app-input pr-16" placeholder="sk-..." :disabled="!canManageProviders" />
                <button type="button" class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed absolute inset-y-0 right-0 flex items-center px-4 text-sm font-medium text-slate-300 transition hover:text-white" @click="showSecret = !showSecret">
                  {{ showSecret ? 'Hide' : 'Show' }}
                </button>
              </div>
            </UiField>

            <UiField v-if="form.auth_mode === 'custom_header'" label="Custom header name" help="The upstream header name that should carry the secret." required>
              <input v-model="form.auth_header_name" class="app-input" placeholder="X-API-Key" :disabled="!canManageProviders" />
            </UiField>
          </div>
        </UiPanel>

        <UiPanel tone="subsurface" title="Operator notes" subtitle="Keep provider-specific context here for other operators and future maintenance.">
          <UiField label="Notes" help="Optional provider-specific context for operators." optional>
            <textarea v-model="form.notes" class="app-textarea" placeholder="Add operational notes for this provider" :disabled="!canManageProviders" />
          </UiField>
        </UiPanel>
      </div>

      <template #footer>
        <div class="flex justify-end">
          <UiButton v-if="canManageProviders" class="w-full sm:w-auto" @click="saveProvider">Save Provider</UiButton>
        </div>
      </template>
    </UiInspectorDrawer>

    <UiModal
      :open="duplicateProviderOpen"
      title="Duplicate Provider"
      subtitle="Create a new API provider with copies of its models, limits, and pricing. Credentials and routing-group memberships are not copied."
      @close="closeDuplicateProvider"
    >
      <div v-if="duplicateProviderTarget" class="space-y-6">
        <div class="app-subsurface p-5">
          <p class="text-sm font-semibold text-white">{{ duplicateProviderTarget.name }}</p>
          <p class="mt-2 text-sm leading-6 text-slate-300/74">
            This will create <span class="font-medium text-slate-100">{{ duplicateProviderName(duplicateProviderTarget) }}</span>
            with the same connection settings and {{ catalog.endpointsByProvider(duplicateProviderTarget.id).length }}
            model{{ catalog.endpointsByProvider(duplicateProviderTarget.id).length === 1 ? '' : 's' }}. The copied models will not belong to any routing groups.
          </p>
          <p v-if="duplicateProviderTarget.auth_mode !== 'none'" class="mt-2 text-sm leading-6 text-slate-300/74">
            The copy will start disabled because API keys and other secrets are never duplicated.
          </p>
        </div>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <UiButton tone="ghost" @click="closeDuplicateProvider">Cancel</UiButton>
          <UiButton :disabled="duplicatingProvider" @click="confirmDuplicateProvider">
            {{ duplicatingProvider ? 'Duplicating...' : 'Accept' }}
          </UiButton>
        </div>
      </div>
    </UiModal>

    <UiModal
      :open="duplicateModelOpen"
      title="Duplicate Model"
      subtitle="Create a new model record from this configuration. The copy keeps limits and pricing, but it will not be added to routing groups automatically."
      @close="closeDuplicateModel"
    >
      <div v-if="duplicateModelTarget" class="space-y-6">
        <div class="app-subsurface p-5">
          <p class="text-sm font-semibold text-white">{{ displayModelName(duplicateModelTarget) }}</p>
          <p class="mt-2 text-sm leading-6 text-slate-300/74">
            This will create <span class="font-medium text-slate-100">{{ duplicateModelName(duplicateModelTarget) }}</span>
            under {{ catalog.providerMap[duplicateModelTarget.provider_id]?.name || 'this provider' }} with the same upstream model, guardrails, pricing, pacing setting, and capabilities.
          </p>
        </div>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <UiButton tone="ghost" @click="closeDuplicateModel">Cancel</UiButton>
          <UiButton :disabled="duplicatingModel" @click="confirmDuplicateModel">
            {{ duplicatingModel ? 'Duplicating...' : 'Accept' }}
          </UiButton>
        </div>
      </div>
    </UiModal>

    <UiModal
      :open="deleteModelOpen"
      title="Delete Model"
      subtitle="This deletes the model and makes it unavailable for routing."
      @close="closeDeleteModel"
    >
      <div v-if="deleteModelTarget" class="space-y-6">
        <div class="app-subsurface p-5">
          <p class="text-sm font-semibold text-white">{{ displayModelName(deleteModelTarget.model) }}</p>
          <p class="mt-2 text-sm leading-6 text-slate-300/74">
            Deleting this model will make it unavailable in {{ deleteModelTarget.groups.length }} routing group{{ deleteModelTarget.groups.length === 1 ? '' : 's' }}.
          </p>
        </div>

        <div v-if="deleteModelTarget.groups.length" class="space-y-3">
          <p class="text-xs font-semibold uppercase tracking-[0.22em] text-slate-400">Routing groups</p>
          <div class="flex flex-wrap gap-2">
            <UiBadge v-for="group in deleteModelTarget.groups" :key="group" tone="slate" size="sm">{{ group }}</UiBadge>
          </div>
        </div>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <UiButton tone="ghost" @click="closeDeleteModel">Cancel</UiButton>
          <UiButton tone="danger" :disabled="deletingModel" @click="confirmDeleteModel">
            {{ deletingModel ? 'Deleting...' : 'Delete Model' }}
          </UiButton>
        </div>
      </div>
    </UiModal>

    <UiModal
      :open="deleteProviderOpen"
      title="Delete Provider"
      subtitle="This deletes the provider and makes every model under it unavailable for routing."
      @close="closeDeleteProvider"
    >
      <div v-if="deleteProviderTarget" class="space-y-6">
        <div class="app-subsurface p-5">
          <p class="text-sm font-semibold text-white">{{ deleteProviderTarget.provider.name }}</p>
          <p class="mt-2 text-sm leading-6 text-slate-300/74">
            Deleting this provider will make {{ deleteProviderTarget.models.length }} model{{ deleteProviderTarget.models.length === 1 ? '' : 's' }}
            unavailable across {{ deleteProviderTarget.groupLinks }} routing-group association{{ deleteProviderTarget.groupLinks === 1 ? '' : 's' }}.
          </p>
        </div>

        <div class="space-y-3">
          <p class="text-xs font-semibold uppercase tracking-[0.22em] text-slate-400">Affected models and group links</p>
          <div v-if="deleteProviderTarget.models.length" class="space-y-3">
            <div
              v-for="item in deleteProviderTarget.models"
              :key="item.endpoint.id"
              class="app-subsurface p-4"
            >
              <div class="flex flex-wrap items-center gap-2">
                <p class="text-sm font-medium text-white">{{ item.endpoint.upstream_model }}</p>
                <UiBadge tone="slate" size="sm">{{ item.endpoint.enabled ? 'Enabled' : 'Disabled' }}</UiBadge>
              </div>
              <div class="mt-3 flex flex-wrap gap-2">
                <UiBadge v-for="group in item.groups" :key="`${item.endpoint.id}-${group}`" tone="slate" size="sm">{{ group }}</UiBadge>
                <span v-if="!item.groups.length" class="text-xs leading-5 text-slate-400">No routing groups linked.</span>
              </div>
            </div>
          </div>
          <p v-else class="text-sm leading-6 text-slate-300/72">This provider has no models yet.</p>
        </div>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <UiButton tone="ghost" @click="closeDeleteProvider">Cancel</UiButton>
          <UiButton tone="danger" :disabled="deletingProvider" @click="confirmDeleteProvider">
            {{ deletingProvider ? 'Deleting…' : 'Delete Provider' }}
          </UiButton>
        </div>
      </div>
    </UiModal>

    <ModelsModelEditDrawer
      :open="!!selectedModelID"
      :endpoint-id="selectedModelID"
      :readonly="!canManageProviders"
      @close="closeModelEdit"
    />

    <ModelsModelEditDrawer
      :open="!!limitsModelID"
      :endpoint-id="limitsModelID"
      :readonly="!canManageLimits"
      variant="limits"
      subtitle="Edit this subscription model’s limits and pacing without changing its managed provider identity, name, or upstream model."
      @close="closeModelLimits"
    />
  </div>
</template>

<style scoped>
:global(.provider-more-menu-panel) {
  min-width: 12rem;
}
</style>
