<script setup lang="ts">
import type { Endpoint, Metric, Period } from '~/types/admin'

type DescriptorOption = {
  value: string
  label: string
  description: string
}

type ModalityOption = {
  value: string
  label: string
  description: string
}

const props = withDefaults(defineProps<{
  open: boolean
  endpointId: string | null
  subtitle?: string
  readonly?: boolean
  variant?: 'full' | 'limits'
}>(), {
  subtitle: 'Adjust one model\'s upstream ID, guardrails, pricing, and operator-facing labels.',
  readonly: false,
  variant: 'full'
})

const emit = defineEmits<{
  close: []
}>()

const catalog = useCatalogStore()
const settings = useSettingsStore()
const queue = useQueueStore()
const metrics = useMetricsStore()
const api = useRelayApi()
const app = useAppStore()
const { PERIODS, metricLabel, periodLabel } = useRelayUi()
const { currencyMicros, microsToDollarInput, dollarsToMicros } = useFormatters()

const DESCRIPTOR_OPTIONS: DescriptorOption[] = [
  { value: 'chat', label: 'Chat', description: 'General conversational interaction.' },
  { value: 'tool-calling', label: 'Tool calling', description: 'Works well with structured tool invocation.' },
  { value: 'streaming', label: 'Streaming', description: 'Can stream partial output as it is generated.' },
  { value: 'long-context', label: 'Long context', description: 'Comfortable with larger prompts and contexts.' },
  { value: 'low-latency', label: 'Low latency', description: 'Optimized for fast response time.' },
  { value: 'low-cost', label: 'Low cost', description: 'Better suited for budget-sensitive traffic.' },
  { value: 'coding', label: 'Coding', description: 'Strong at code generation and technical reasoning.' },
  { value: 'extraction', label: 'Extraction', description: 'Useful for pulling structured facts from text.' },
  { value: 'summarization', label: 'Summarization', description: 'Good at compressing long input into concise output.' },
  { value: 'multilingual', label: 'Multilingual', description: 'Comfortable across multiple languages.' },
  { value: 'json-reliable', label: 'JSON reliable', description: 'Better at well-formed JSON output.' },
  { value: 'multimodal', label: 'Multimodal', description: 'Designed for text plus image-style input.' },
  { value: 'function-calling', label: 'Function calling', description: 'Supports function-style tool execution.' },
  { value: 'json-mode', label: 'JSON mode', description: 'Supports JSON-only response modes cleanly.' },
  { value: 'premium', label: 'Premium', description: 'Reserved for the highest-quality traffic.' },
  { value: 'creative', label: 'Creative', description: 'Better for open-ended or more stylistic output.' }
]

const MODALITY_OPTIONS: ModalityOption[] = [
  { value: 'text', label: 'Text', description: 'Accepts plain text prompts and standard text output.' },
  { value: 'image', label: 'Image', description: 'Can work with image inputs or image-grounded multimodal tasks.' },
  { value: 'video', label: 'Video', description: 'Supports video inputs or video-grounded processing workflows.' },
  { value: 'audio', label: 'Audio', description: 'Supports audio inputs or audio-grounded generation and analysis.' },
  { value: 'pdf', label: 'PDF', description: 'Can work with PDF-style document inputs and document parsing flows.' }
]

const requestLimitFields: Array<{ key: string, metric: Metric, period: Period }> = PERIODS.map((period) => ({
  key: `requests_${period}`,
  metric: 'requests',
  period
}))

const tokenLimitFields: Array<{ key: string, metric: Metric, period: Period }> = PERIODS.map((period) => ({
  key: `tokens_${period}`,
  metric: 'tokens',
  period
}))

const spendLimitFields: Array<{ key: string, metric: Metric, period: Period }> = PERIODS
  .filter((period) => period !== 'second')
  .map((period) => ({
    key: `spend_${period}`,
    metric: 'spend' as Metric,
    period
  }))

const PACED_REQUEST_PERIODS: Period[] = ['second', 'minute', 'hour']

const modelAdvancedOpen = ref(false)
const limitEnforcementForm = reactive({
  reserve_estimated_tokens_for_limits: true,
  reserve_estimated_spend_for_limits: true
})
const modelForm = reactive({
  id: '',
  provider_id: '',
  name: '',
  route_kind: 'chat',
  upstream_model: '',
  enabled: true,
  pacing: true,
  max_latency_ms: '',
  context_window: '',
  param_size_b: '',
  modalities: [] as string[],
  descriptor_tags: [] as string[],
  notes: '',
  input_cost_micros_per_1m_tokens: '',
  output_cost_micros_per_1m_tokens: '',
  flat_request_cost_micros: '',
  requests_second: '',
  requests_minute: '',
  requests_hour: '',
  requests_day: '',
  requests_month: '',
  tokens_second: '',
  tokens_minute: '',
  tokens_hour: '',
  tokens_day: '',
  tokens_month: '',
  spend_minute: '',
  spend_hour: '',
  spend_day: '',
  spend_month: ''
})

const selectedEndpointID = computed(() => String(props.endpointId || ''))
const selectedModel = computed(() => catalog.endpointMap[selectedEndpointID.value] || null)
const isReadonly = computed(() => props.readonly)
const limitsOnly = computed(() => props.variant === 'limits')
const descriptorOptions = computed(() => DESCRIPTOR_OPTIONS)
const modalityOptions = computed(() => MODALITY_OPTIONS)
const selectedModelGroups = computed(() => modelGroups(modelForm.id))
const configuredPacedRequestLimits = computed(() =>
  requestLimitFields
    .filter((field) => PACED_REQUEST_PERIODS.includes(field.period))
    .filter((field) => {
      const value = Number(modelForm[field.key as keyof typeof modelForm] || 0)
      return Number.isFinite(value) && value > 0
    })
)
const pacingLimitSummary = computed(() => {
  if (!configuredPacedRequestLimits.value.length) return 'No paced request windows configured yet.'
  return configuredPacedRequestLimits.value
    .map((field) => `${metricLabel(field.metric)} / ${periodLabel(field.period)}`)
    .join(', ')
})

watch([selectedModel, () => props.open], ([model, open]) => {
  if (!open || !model) return
  populateModelForm(model)
}, { immediate: true })

watch(
  () => props.open,
  (open) => {
    if (!open || settings.settings.length || settings.loading) return
    void settings.refresh().catch(() => {
      app.pushToast({ title: 'Settings unavailable', description: 'Limit enforcement modes could not be loaded.' })
    })
  },
  { immediate: true }
)

watch(
  () => settings.parsed,
  (parsed) => {
    limitEnforcementForm.reserve_estimated_tokens_for_limits = parsed.reserve_estimated_tokens_for_limits ?? true
    limitEnforcementForm.reserve_estimated_spend_for_limits = parsed.reserve_estimated_spend_for_limits ?? true
  },
  { immediate: true }
)

function closeDrawer() {
  emit('close')
}

function providerCredential(providerId: string) {
  return catalog.credentials
    .filter((item) => item.provider_id === providerId)
    .sort((a, b) => a.id.localeCompare(b.id))[0]
}

function modelLimit(endpointId: string, metric: Metric, period: Period) {
  return catalog.limitPolicies.find((item) =>
    item.scope_type === 'endpoint' &&
    item.scope_id === endpointId &&
    item.metric === metric &&
    item.period === period &&
    item.enabled
  )?.limit_value ?? null
}

function modelPricing(endpointId: string) {
  return catalog.pricingPolicies.find((item) => item.endpoint_id === endpointId)
}

function modelGroups(endpointId: string) {
  return catalog.memberships
    .filter((item) => item.endpoint_id === endpointId)
    .sort((a, b) => a.manual_rank - b.manual_rank)
    .map((item) => catalog.laneMap[item.lane_id]?.name)
    .filter((value): value is string => !!value)
}

function populateModelForm(model: Endpoint) {
  const pricing = modelPricing(model.id)
  Object.assign(modelForm, {
    id: model.id,
    provider_id: model.provider_id,
    name: model.name || model.upstream_model || '',
    route_kind: model.route_kind,
    upstream_model: model.upstream_model,
    enabled: model.enabled,
    pacing: model.pacing !== false,
    max_latency_ms: String(model.max_latency_ms || ''),
    context_window: String(model.context_window || ''),
    param_size_b: String(model.param_size_b || ''),
    modalities: [...(model.modalities || [])],
    descriptor_tags: [...(model.descriptor_tags || [])],
    notes: model.notes || '',
    input_cost_micros_per_1m_tokens: microsToDollarInput(pricing?.input_cost_micros_per_1m_tokens),
    output_cost_micros_per_1m_tokens: microsToDollarInput(pricing?.output_cost_micros_per_1m_tokens),
    flat_request_cost_micros: microsToDollarInput(pricing?.flat_request_cost_micros),
    requests_second: String(modelLimit(model.id, 'requests', 'second') ?? ''),
    requests_minute: String(modelLimit(model.id, 'requests', 'minute') ?? ''),
    requests_hour: String(modelLimit(model.id, 'requests', 'hour') ?? ''),
    requests_day: String(modelLimit(model.id, 'requests', 'day') ?? ''),
    requests_month: String(modelLimit(model.id, 'requests', 'month') ?? ''),
    tokens_second: String(modelLimit(model.id, 'tokens', 'second') ?? ''),
    tokens_minute: String(modelLimit(model.id, 'tokens', 'minute') ?? ''),
    tokens_hour: String(modelLimit(model.id, 'tokens', 'hour') ?? ''),
    tokens_day: String(modelLimit(model.id, 'tokens', 'day') ?? ''),
    tokens_month: String(modelLimit(model.id, 'tokens', 'month') ?? ''),
    spend_minute: microsToDollarInput(modelLimit(model.id, 'spend', 'minute')),
    spend_hour: microsToDollarInput(modelLimit(model.id, 'spend', 'hour')),
    spend_day: microsToDollarInput(modelLimit(model.id, 'spend', 'day')),
    spend_month: microsToDollarInput(modelLimit(model.id, 'spend', 'month'))
  })
  modelAdvancedOpen.value = false
}

function isDescriptorSelected(value: string) {
  return modelForm.descriptor_tags.includes(value)
}

function toggleDescriptor(value: string) {
  if (props.readonly) return
  if (isDescriptorSelected(value)) {
    modelForm.descriptor_tags = modelForm.descriptor_tags.filter((item) => item !== value)
    return
  }
  modelForm.descriptor_tags = descriptorOptions.value
    .map((item) => item.value)
    .filter((item) => item === value || modelForm.descriptor_tags.includes(item))
}

function isModalitySelected(value: string) {
  return modelForm.modalities.includes(value)
}

function toggleModality(value: string) {
  if (props.readonly) return
  if (isModalitySelected(value)) {
    modelForm.modalities = modelForm.modalities.filter((item) => item !== value)
    return
  }
  modelForm.modalities = modalityOptions.value
    .map((item) => item.value)
    .filter((item) => item === value || modelForm.modalities.includes(item))
}

function endpointCapabilityFlags() {
  const descriptors = Array.from(new Set(modelForm.descriptor_tags))
  const modalities = Array.from(new Set(modelForm.modalities))
  return {
    supportsStreaming: descriptors.includes('streaming'),
    supportsTools: descriptors.includes('tool-calling') || descriptors.includes('function-calling'),
    supportsVision: descriptors.includes('multimodal') || modalities.some((value) => ['image', 'video', 'pdf'].includes(value))
  }
}

async function upsertModelLimit(endpointId: string, metric: Metric, period: Period, rawValue: string) {
  const existing = catalog.limitPolicies.find((item) =>
    item.scope_type === 'endpoint' &&
    item.scope_id === endpointId &&
    item.metric === metric &&
    item.period === period
  )
  const parsed = metric === 'spend' ? dollarsToMicros(rawValue) : Number(rawValue)
  if (!rawValue || !Number.isFinite(parsed) || parsed <= 0) {
    if (existing) await api.del(`/api/limit-policies/${existing.id}`)
    return
  }
  const payload = {
    scope_type: 'endpoint',
    scope_id: endpointId,
    metric,
    period,
    limit_value: parsed,
    enabled: true,
    source: 'configured'
  }
  if (existing) await api.put(`/api/limit-policies/${existing.id}`, { ...existing, ...payload })
  else await api.post('/api/limit-policies', payload)
}

async function saveModelPricing(endpointId: string) {
  const existing = modelPricing(endpointId)
  const inputCost = dollarsToMicros(modelForm.input_cost_micros_per_1m_tokens)
  const outputCost = dollarsToMicros(modelForm.output_cost_micros_per_1m_tokens)
  const flatCost = dollarsToMicros(modelForm.flat_request_cost_micros)
  const hasAnyPricing = inputCost > 0 || outputCost > 0 || flatCost > 0
  if (!hasAnyPricing) {
    if (existing) await api.del(`/api/pricing-policies/${existing.id}`)
    return
  }
  const payload = {
    endpoint_id: endpointId,
    currency: 'USD',
    input_cost_micros_per_1m_tokens: inputCost,
    output_cost_micros_per_1m_tokens: outputCost,
    cached_input_cost_micros_per_1m_tokens: 0,
    flat_request_cost_micros: flatCost
  }
  if (existing) await api.put(`/api/pricing-policies/${existing.id}`, { ...existing, ...payload })
  else await api.post('/api/pricing-policies', payload)
}

async function saveLimitEnforcement() {
  await settings.saveMany([
    { key: 'reserve_estimated_tokens_for_limits', value: Boolean(limitEnforcementForm.reserve_estimated_tokens_for_limits) },
    { key: 'reserve_estimated_spend_for_limits', value: Boolean(limitEnforcementForm.reserve_estimated_spend_for_limits) }
  ])
}

async function persistModel() {
  if (props.readonly) return
  const current = selectedModel.value
  if (!current) return

  if (limitsOnly.value) {
    if (modelForm.pacing !== current.pacing) {
      await api.put<Endpoint>(`/api/endpoints/${current.id}`, {
        pacing: modelForm.pacing
      })
    }
    for (const field of requestLimitFields) {
      await upsertModelLimit(current.id, field.metric, field.period, modelForm[field.key as keyof typeof modelForm] as string)
    }
    for (const field of tokenLimitFields) {
      await upsertModelLimit(current.id, field.metric, field.period, modelForm[field.key as keyof typeof modelForm] as string)
    }
    for (const field of spendLimitFields) {
      await upsertModelLimit(current.id, field.metric, field.period, modelForm[field.key as keyof typeof modelForm] as string)
    }
    await saveLimitEnforcement()
    await catalog.refreshAll()
    await Promise.allSettled([
      queue.refresh(),
      metrics.refresh()
    ])
    emit('close')
    app.pushToast({ title: 'Limits updated', description: 'Model limits and pacing were updated.' })
    return
  }

  const providerId = current.provider_id
  const credential = providerCredential(providerId)
  const capabilities = endpointCapabilityFlags()
  const descriptors = Array.from(new Set(modelForm.descriptor_tags))
  const modalities = Array.from(new Set(modelForm.modalities))
  const upstreamModel = modelForm.upstream_model.trim()
  const displayName = modelForm.name.trim() || upstreamModel

  const payload = {
    provider_id: providerId,
    credential_id: credential?.id || current.credential_id || '',
    name: displayName,
    upstream_model: upstreamModel,
    route_kind: current.route_kind,
    enabled: modelForm.enabled,
    pacing: modelForm.pacing,
    max_latency_ms: Number(modelForm.max_latency_ms || 0),
    quality_score: current.quality_score || 80,
    context_window: Number(modelForm.context_window || 0),
    param_size_b: Number(modelForm.param_size_b || 0),
    modalities,
    descriptor_tags: descriptors,
    supports_streaming: capabilities.supportsStreaming,
    supports_tools: capabilities.supportsTools,
    supports_vision: capabilities.supportsVision,
    health_status: current.health_status || 'healthy',
    notes: modelForm.notes.trim()
  }

  const saved = await api.put<Endpoint>(`/api/endpoints/${modelForm.id}`, payload)
  for (const field of requestLimitFields) {
    await upsertModelLimit(saved.id, field.metric, field.period, modelForm[field.key as keyof typeof modelForm] as string)
  }
  for (const field of tokenLimitFields) {
    await upsertModelLimit(saved.id, field.metric, field.period, modelForm[field.key as keyof typeof modelForm] as string)
  }
  for (const field of spendLimitFields) {
    await upsertModelLimit(saved.id, field.metric, field.period, modelForm[field.key as keyof typeof modelForm] as string)
  }
  await saveModelPricing(saved.id)
  await saveLimitEnforcement()
  await catalog.refreshAll()
  await Promise.allSettled([
    queue.refresh(),
    metrics.refresh()
  ])
  emit('close')
  app.pushToast({ title: 'Model updated', description: 'Connection details, limits, and labels were updated.' })
}

async function saveModel() {
  try {
    await persistModel()
  } catch (cause: any) {
    app.pushToast({
      title: limitsOnly.value ? 'Limits could not be saved' : 'Model could not be saved',
      description: cause?.data?.error || cause?.data?.message || cause?.message || 'Please choose a different model name.',
      tone: 'error'
    })
  }
}
</script>

<template>
  <UiInspectorDrawer
    :open="open && !!selectedModel"
    :title="modelForm.name || modelForm.upstream_model || (isReadonly ? 'View Model' : 'Edit Model')"
    :subtitle="subtitle"
    @close="closeDrawer"
  >
    <div class="space-y-8">
      <UiPanel v-if="!limitsOnly" tone="subsurface" title="Core details" subtitle="Keep the main model edit flow focused on fields that materially affect runtime behavior.">
        <div class="space-y-5">
          <div class="grid gap-5 md:auto-rows-fr md:grid-cols-3">
            <UiField class="min-w-0" label="Provider" help="Models stay attached to the provider that owns them.">
              <div
                class="model-provider-lock rounded-xl border px-4 py-3"
                aria-readonly="true"
                title="Provider cannot be changed from the model editor"
              >
                <span class="min-w-0 truncate text-sm font-semibold">
                  {{ catalog.providerMap[modelForm.provider_id]?.name || 'Provider missing' }}
                </span>
                <span class="model-provider-lock__badge">Locked</span>
              </div>
            </UiField>
            <UiField class="min-w-0" label="Display name" help="The name shown in provider lists, groups, and operator views." required>
              <input v-model="modelForm.name" class="app-input" placeholder="GPT-5" :disabled="isReadonly" />
            </UiField>
            <UiField class="min-w-0" label="Upstream model ID" help="The exact model identifier your upstream expects." required>
              <input v-model="modelForm.upstream_model" class="app-input" placeholder="gpt-5" :disabled="isReadonly" />
            </UiField>
          </div>

          <UiSwitch
            v-model="modelForm.enabled"
            label="Model enabled"
            description="Disable this model if you want to keep the record but stop routing traffic through it."
            :disabled="isReadonly"
          />
        </div>
      </UiPanel>

      <UiPanel tone="subsurface" title="Limits" subtitle="Configure this model's request, token, and spend limits without leaving the model editor.">
        <div class="space-y-8">
          <div class="space-y-4">
            <div>
              <h3 class="text-lg font-semibold text-white">Requests</h3>
              <p class="mt-1 text-sm text-slate-300/76">Configured request caps for this model across time windows.</p>
            </div>
            <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2 xl:grid-cols-5">
              <UiField
                v-for="field in requestLimitFields"
                :key="field.key"
                class="model-limit-field"
                :label="`${metricLabel(field.metric)} / ${periodLabel(field.period)}`"
                :help="`Configured ${metricLabel(field.metric).toLowerCase()} limit per ${periodLabel(field.period)}.`"
                optional
              >
                <input v-model="modelForm[field.key]" class="app-input" inputmode="numeric" placeholder="1000" :disabled="isReadonly" />
              </UiField>
            </div>
            <UiSwitch
              v-model="modelForm.pacing"
              label="Pace request limits"
              :description="`Smooth request caps across supported request windows. Pacing only applies to this model's request limits for second, minute, and hour windows. Daily/monthly request caps, token limits, spend limits, and hard provider cooldowns remain normal limit gates. ${pacingLimitSummary}`"
              :disabled="isReadonly"
            >
              <template #aside>
                <UiBadge :tone="modelForm.pacing && configuredPacedRequestLimits.length ? 'emerald' : 'slate'" size="sm">
                  {{ modelForm.pacing && configuredPacedRequestLimits.length ? 'pacing ready' : 'not pacing' }}
                </UiBadge>
              </template>
            </UiSwitch>
          </div>

          <div class="space-y-4">
            <div>
              <h3 class="text-lg font-semibold text-white">Tokens</h3>
              <p class="mt-1 text-sm text-slate-300/76">Configured token caps for this model across time windows.</p>
            </div>
            <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2 xl:grid-cols-5">
              <UiField
                v-for="field in tokenLimitFields"
                :key="field.key"
                class="model-limit-field"
                :label="`${metricLabel(field.metric)} / ${periodLabel(field.period)}`"
                :help="`Configured ${metricLabel(field.metric).toLowerCase()} limit per ${periodLabel(field.period)}.`"
                optional
              >
                <input v-model="modelForm[field.key]" class="app-input" inputmode="numeric" placeholder="1000" :disabled="isReadonly" />
              </UiField>
            </div>
            <UiSwitch
              v-model="limitEnforcementForm.reserve_estimated_tokens_for_limits"
              label="Stop before token caps using estimates"
              description="Reserve estimated input and output tokens before dispatch. Turn off to stop only after recorded token usage reaches the cap."
              :disabled="isReadonly"
            />
          </div>

          <div class="space-y-4">
            <div>
              <h3 class="text-lg font-semibold text-white">Spend</h3>
              <p class="mt-1 text-sm text-slate-300/76">Configured spend caps for this model across time windows.</p>
            </div>
            <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2 xl:grid-cols-4">
              <UiField
                v-for="field in spendLimitFields"
                :key="field.key"
                class="model-limit-field"
                :label="`${metricLabel(field.metric)} / ${periodLabel(field.period)}`"
                :help="`USD spend ceiling per ${periodLabel(field.period)}. Saved internally as micros.`"
                optional
              >
                <input v-model="modelForm[field.key]" type="number" min="0" step="0.000001" class="app-input" inputmode="decimal" placeholder="1.00" :disabled="isReadonly" />
              </UiField>
            </div>
            <UiSwitch
              v-model="limitEnforcementForm.reserve_estimated_spend_for_limits"
              label="Stop before dollar caps using estimates"
              description="Reserve estimated request cost before dispatch. Turn off to stop only after recorded spend reaches the cap."
              :disabled="isReadonly"
            />
          </div>

          <div v-if="!limitsOnly" class="space-y-4">
            <div>
              <h3 class="text-lg font-semibold text-white">Pricing</h3>
              <p class="mt-1 text-sm text-slate-300/76">Enter provider prices as USD. Model Relay stores precise micro-dollar integers internally.</p>
            </div>
            <div class="grid gap-5 md:auto-rows-fr md:grid-cols-3">
              <UiField class="model-limit-field" label="Input cost / 1M tokens" help="USD amount from the provider price sheet." optional>
                <input v-model="modelForm.input_cost_micros_per_1m_tokens" type="number" min="0" step="0.000001" class="app-input" inputmode="decimal" placeholder="0.15" :disabled="isReadonly" />
              </UiField>
              <UiField class="model-limit-field" label="Output cost / 1M tokens" help="USD amount from the provider price sheet." optional>
                <input v-model="modelForm.output_cost_micros_per_1m_tokens" type="number" min="0" step="0.000001" class="app-input" inputmode="decimal" placeholder="0.60" :disabled="isReadonly" />
              </UiField>
              <UiField class="model-limit-field" label="Cost / request" help="Optional flat USD surcharge per request." optional>
                <input v-model="modelForm.flat_request_cost_micros" type="number" min="0" step="0.000001" class="app-input" inputmode="decimal" placeholder="0.0001" :disabled="isReadonly" />
              </UiField>
            </div>
            <p class="text-xs leading-5 text-slate-400">
              Current saved pricing:
              input {{ currencyMicros(catalog.pricingPolicies.find((item) => item.endpoint_id === modelForm.id)?.input_cost_micros_per_1m_tokens || 0) }} / 1M,
              output {{ currencyMicros(catalog.pricingPolicies.find((item) => item.endpoint_id === modelForm.id)?.output_cost_micros_per_1m_tokens || 0) }} / 1M,
              flat {{ currencyMicros(catalog.pricingPolicies.find((item) => item.endpoint_id === modelForm.id)?.flat_request_cost_micros || 0) }} / request.
            </p>
          </div>

          <UiField v-if="!limitsOnly" label="Max latency (ms)" help="Optional model-specific timeout before Model Relay cancels and retries a hanging upstream call. Leave blank to inherit the provider or system default." optional>
            <input v-model="modelForm.max_latency_ms" class="app-input" inputmode="numeric" placeholder="600000" :disabled="isReadonly" />
          </UiField>
        </div>
      </UiPanel>

      <details v-if="!limitsOnly" class="ui-drawer-section" :open="modelAdvancedOpen">
        <summary class="cursor-pointer list-none" @click.prevent="modelAdvancedOpen = !modelAdvancedOpen">
          <div class="flex items-center justify-between">
            <span class="text-sm font-semibold text-white">Advanced settings</span>
            <span class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ modelAdvancedOpen ? 'Hide' : 'Show' }}</span>
          </div>
        </summary>

        <div class="mt-6 space-y-6">
          <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2">
            <UiField label="Context window" help="Optional operator metadata." optional>
              <input v-model="modelForm.context_window" class="app-input" inputmode="numeric" placeholder="128000" :disabled="isReadonly" />
            </UiField>
            <UiField label="Parameter size (billions)" help="Optional operator metadata." optional>
              <input v-model="modelForm.param_size_b" class="app-input" inputmode="numeric" placeholder="70" :disabled="isReadonly" />
            </UiField>
          </div>

          <div class="ui-drawer-subsection">
            <div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div class="max-w-2xl">
                <p class="text-base font-semibold text-white">Routing groups</p>
                <p class="mt-2 text-sm leading-6 text-slate-300/76">Group membership and ranking stay on the Groups page, but the current labels still belong in view here.</p>
              </div>
              <UiBadge tone="slate">{{ selectedModelGroups.length }} linked</UiBadge>
            </div>

            <div class="mt-5 flex flex-wrap gap-3">
              <UiBadge v-for="group in selectedModelGroups" :key="group" tone="slate">{{ group }}</UiBadge>
              <p v-if="!selectedModelGroups.length" class="text-sm leading-6 text-slate-400">This model is not linked to any routing groups yet.</p>
            </div>
          </div>

          <div class="ui-drawer-subsection">
            <div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div class="max-w-2xl">
                <p class="text-base font-semibold text-white">Modalities</p>
                <p class="mt-2 text-sm leading-6 text-slate-300/76">Mark the input families this model can actually handle.</p>
              </div>
              <UiBadge tone="slate">{{ modelForm.modalities.length }} selected</UiBadge>
            </div>

            <div class="mt-5 flex flex-wrap gap-3">
              <button
                v-for="modality in modalityOptions"
                :key="modality.value"
                type="button"
                class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed rounded-full border px-4 py-2 text-sm font-medium transition"
                :class="isModalitySelected(modality.value)
                  ? 'border-sky-400/30 bg-sky-400/10 text-sky-100'
                  : 'border-[var(--app-border)] bg-[var(--app-input-bg)] text-[var(--app-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-nav-hover-bg)]'"
                :title="modality.description"
                :disabled="isReadonly"
                @click="toggleModality(modality.value)"
              >
                {{ modality.label }}
              </button>
            </div>
          </div>

          <div class="ui-drawer-subsection">
            <div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div class="max-w-2xl">
                <p class="text-base font-semibold text-white">Model descriptors</p>
                <p class="mt-2 text-sm leading-6 text-slate-300/76">Use the same label-style capability tagging as setup for tool calls, categorization, streaming, and related operator-facing traits.</p>
              </div>
              <UiBadge tone="slate">{{ modelForm.descriptor_tags.length }} selected</UiBadge>
            </div>

            <div class="mt-5 flex flex-wrap gap-3">
              <button
                v-for="descriptor in descriptorOptions"
                :key="descriptor.value"
                type="button"
                class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed rounded-full border px-4 py-2 text-sm font-medium transition"
                :class="isDescriptorSelected(descriptor.value)
                  ? 'border-emerald-400/30 bg-emerald-400/10 text-emerald-100'
                  : 'border-[var(--app-border)] bg-[var(--app-input-bg)] text-[var(--app-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-nav-hover-bg)]'"
                :title="descriptor.description"
                :disabled="isReadonly"
                @click="toggleDescriptor(descriptor.value)"
              >
                {{ descriptor.label }}
              </button>
            </div>
          </div>

          <UiField label="Notes" help="Optional performance or operational notes." optional>
            <textarea v-model="modelForm.notes" class="app-textarea" placeholder="Add operational notes for this model" :disabled="isReadonly" />
          </UiField>
        </div>
      </details>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <UiButton v-if="!isReadonly" class="w-full sm:w-auto" @click="saveModel">{{ limitsOnly ? 'Save Limits' : 'Save Model' }}</UiButton>
      </div>
    </template>
  </UiInspectorDrawer>
</template>

<style scoped>
.model-limit-field :deep(.ui-field-control) {
  margin-top: auto;
  padding-top: 0.375rem;
}

.model-provider-lock {
  display: flex;
  min-height: 2.75rem;
  cursor: not-allowed;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border-color: color-mix(in srgb, var(--app-border) 78%, transparent);
  background: var(--app-surface-muted);
  color: color-mix(in srgb, var(--app-text) 66%, var(--app-muted));
  box-shadow: inset 0 1px 0 color-mix(in srgb, var(--app-border) 36%, transparent);
}

.model-provider-lock__badge {
  flex: none;
  border-radius: 999px;
  border: 1px solid color-mix(in srgb, var(--app-border) 86%, transparent);
  background: var(--app-surface-muted);
  padding: 0.18rem 0.55rem;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: color-mix(in srgb, var(--app-muted) 88%, var(--app-text));
}

:global(:root[data-site-theme='light']) .model-provider-lock {
  background: var(--app-surface-muted);
  color: var(--app-muted);
}

:global(:root[data-site-theme='light']) .model-provider-lock__badge {
  background: var(--app-secondary-bg);
  color: var(--app-muted);
}
</style>
