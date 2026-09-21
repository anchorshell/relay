<script setup lang="ts">
import { inferenceHeaders, INFERENCE_AUTH_HELP } from '../../utils/inference'
import type {
  Credential,
  Endpoint,
  LaneMembership,
  LimitPolicy,
  Metric,
  Period,
  PricingPolicy,
  Provider,
  RouteKind,
  RoutingLane
} from '~/types/admin'

type WizardStep = {
  id: number
  label: string
  eyebrow: string
  title: string
  description: string
}

type GroupPreset = {
  value: string
  label: string
  description: string
}

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

const route = useRoute()
const api = useRelayApi()
const catalog = useCatalogStore()
const app = useAppStore()
const { modelRelayApiPath, modelRelayPath } = useModelRelayRoute()
const { previewResponse } = useRelayUi()

const stepIndex = ref(0)
const furthestStep = ref(0)
const saving = ref(false)
const testing = ref(false)
const inferenceToken = useInferenceToken()
const showAPIKey = ref(false)
const showRawResponse = ref(false)
const importOpen = ref(false)
const importCurl = ref('')
const importError = ref('')
const showGroupingMetadata = false

const testResult = ref<any>(null)
const testRaw = ref<any>(null)
const testMeta = ref<Record<string, string>>({})

const errors = reactive<Record<string, string>>({})

const GROUP_PRESETS: GroupPreset[] = [
  { value: 'reasoning', label: 'Reasoning', description: 'Best available reasoning models for slower, higher-value work.' },
  { value: 'agentic', label: 'Agentic', description: 'Models suited for longer, tool-driven, multi-step flows.' },
  { value: 'coding', label: 'Coding', description: 'Models that should be available for code generation and technical debugging.' },
  { value: 'vision', label: 'Vision', description: 'Models that should be available for image-heavy or multimodal routing.' },
  { value: 'structured', label: 'Structured', description: 'Models used for JSON-heavy extraction or schema-constrained responses.' },
  { value: 'embeddings', label: 'Embeddings', description: 'Vector-generation models for retrieval, indexing, and semantic search.' },
  { value: 'fast', label: 'Fast', description: 'Low-latency models for quick turnarounds and throughput-sensitive work.' }
]

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

const steps: WizardStep[] = [
  {
    id: 0,
    label: 'Connect',
    eyebrow: 'Step 1',
    title: 'Connect your first model',
    description: 'Add the upstream provider and model that Model Relay should call. Start with the minimum useful connection details.'
  },
  {
    id: 1,
    label: 'Grouping',
    eyebrow: 'Step 2',
    title: 'Choose groups and descriptors',
    description: 'Add the model into the routing groups it should serve and tag the capabilities operators should recognize later.'
  },
  {
    id: 2,
    label: 'Limits & Budgets',
    eyebrow: 'Step 3',
    title: 'Add optional limits, budgets, and costs',
    description: 'Set rate, token, spend, and cost controls only if they are useful for this model right now.'
  },
  {
    id: 3,
    label: 'Verify',
    eyebrow: 'Step 4',
    title: 'Send a test request',
    description: 'Confirm the provider, model, and primary routing group work together before you move on to the broader console.'
  }
]

const connection = reactive({
  provider_label: '',
  base_url: '',
  integration: 'chat',
  model_id: '',
  api_key: ''
})

const grouping = reactive({
  group_names: [] as string[],
  descriptor_tags: [] as string[],
  modalities: [] as string[]
})

const guardrails = reactive({
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
  spend_month: '',
  input_cost: '',
  output_cost: '',
  request_cost: ''
})

const testRequest = reactive({
  prompt: 'Explain in one sentence why queue-first routing preserves the better model.',
  stream: false
})

const createdProvider = ref<Provider | null>(null)
const createdCredential = ref<Credential | null>(null)
const createdEndpoint = ref<Endpoint | null>(null)
const createdLane = ref<RoutingLane | null>(null)

const providerID = computed(() => {
  const raw = route.query.providerId
  return String(Array.isArray(raw) ? raw[0] || '' : raw || '')
})

const wizardMode = computed(() => {
  const raw = Array.isArray(route.query.mode) ? route.query.mode[0] : route.query.mode
  return raw === 'model' ? 'model' : raw === 'provider' ? 'provider' : 'new'
})

const existingProvider = computed(() => catalog.providers.find((item) => item.id === providerID.value) || null)
const existingCredential = computed(() =>
  catalog.credentials
    .filter((item) => item.provider_id === providerID.value)
    .sort((a, b) => a.id.localeCompare(b.id))[0] || null
)
const lockedProvider = computed(() => wizardMode.value === 'model' && !!existingProvider.value)

const stepperItems = computed(() => steps.map((step, index) => ({
  value: step.id,
  title: step.label,
  description: step.title,
  disabled: index > furthestStep.value
})))

const routeKind = computed<RouteKind>(() => {
  switch (connection.integration) {
    case 'responses':
      return 'responses'
    case 'embeddings':
      return 'embeddings'
    default:
      return 'chat'
  }
})

const integrationOptions = [
  { value: 'chat', label: 'OpenAI-compatible chat completions' },
  { value: 'responses', label: 'OpenAI-compatible responses' },
  { value: 'embeddings', label: 'OpenAI-compatible embeddings' }
]

const groupOptions = computed(() => {
  return GROUP_PRESETS.map((preset) => {
    const existing = catalog.lanes.find((lane) => lane.name === preset.value)
    return {
      ...preset,
      description: existing?.description || preset.description
    }
  })
})

const descriptorOptions = computed(() => DESCRIPTOR_OPTIONS)
const modalityOptions = computed(() => MODALITY_OPTIONS)

const selectedGroups = computed(() => groupOptions.value.filter((item) => grouping.group_names.includes(item.value)))
const primaryGroupName = computed(() => selectedGroups.value[0]?.label || '')

const resolvedProviderName = computed(() => {
  if (existingProvider.value?.name) return existingProvider.value.name
  return connection.provider_label.trim()
})

const resolvedModelName = computed(() => connection.model_id.trim() || 'Primary model')

const summaryRows = computed(() => [
  { label: 'Provider', value: resolvedProviderName.value || 'Not chosen yet' },
  { label: 'Base URL', value: connection.base_url || 'Add your upstream URL' },
  { label: 'Model ID', value: connection.model_id || 'Not set yet' },
  { label: 'Groups', value: selectedGroups.value.length ? selectedGroups.value.map((item) => item.label).join(', ') : 'None selected (optional)' },
  { label: 'Type', value: integrationOptions.find((item) => item.value === connection.integration)?.label || 'Chat completions' }
])

const testResponsePreview = computed(() => {
  if (!testResult.value) return ''
  return previewResponse(routeKind.value, testResult.value)
})

const verificationStats = computed(() => {
  const usage = testResult.value?.usage || {}
  return [
    { label: 'Prompt tokens', value: usage.prompt_tokens ?? usage.input_tokens ?? 'n/a' },
    { label: 'Completion tokens', value: usage.completion_tokens ?? usage.output_tokens ?? 'n/a' },
    { label: 'Total tokens', value: usage.total_tokens ?? 'n/a' },
    { label: 'HTTP status', value: testMeta.value.status || 'n/a' }
  ]
})

const requestLimitFields: Array<{ key: keyof typeof guardrails, label: string, period: Period }> = [
  { key: 'requests_second', label: 'Requests / sec', period: 'second' },
  { key: 'requests_minute', label: 'Requests / min', period: 'minute' },
  { key: 'requests_hour', label: 'Requests / hour', period: 'hour' },
  { key: 'requests_day', label: 'Requests / day', period: 'day' },
  { key: 'requests_month', label: 'Requests / month', period: 'month' }
]

const tokenLimitFields: Array<{ key: keyof typeof guardrails, label: string, period: Period }> = [
  { key: 'tokens_second', label: 'Tokens / sec', period: 'second' },
  { key: 'tokens_minute', label: 'Tokens / min', period: 'minute' },
  { key: 'tokens_hour', label: 'Tokens / hour', period: 'hour' },
  { key: 'tokens_day', label: 'Tokens / day', period: 'day' },
  { key: 'tokens_month', label: 'Tokens / month', period: 'month' }
]

const spendLimitFields: Array<{ key: keyof typeof guardrails, label: string, period: Period }> = [
  { key: 'spend_minute', label: 'Spend / min', period: 'minute' },
  { key: 'spend_hour', label: 'Spend / hour', period: 'hour' },
  { key: 'spend_day', label: 'Spend / day', period: 'day' },
  { key: 'spend_month', label: 'Spend / month', period: 'month' }
]

watch(
  existingProvider,
  (provider) => {
    if (!provider) return
    connection.provider_label = provider.name
    connection.base_url = provider.base_url
  },
  { immediate: true }
)

function setStep(target: number) {
  furthestStep.value = Math.max(furthestStep.value, target)
  stepIndex.value = target
}

function clearErrors() {
  for (const key of Object.keys(errors)) delete errors[key]
}

function slugify(value: string) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function normalizeBaseURL(value: string) {
  return value.trim().replace(/\/+$/, '')
}

const KNOWN_PROVIDER_NAMES: Record<string, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  openrouter: 'OpenRouter',
  deepseek: 'DeepSeek',
  mistral: 'Mistral',
  nvidia: 'NVIDIA',
  groq: 'Groq',
  together: 'Together',
  fireworks: 'Fireworks',
  cohere: 'Cohere',
  perplexity: 'Perplexity'
}

function registrableDomainLabel(hostname: string) {
  const labels = hostname.toLowerCase().split('.').filter(Boolean)
  if (labels.length <= 1) return labels[0] || ''

  const secondLevelTLDs = new Set(['co', 'com', 'net', 'org', 'gov', 'edu', 'ac'])
  if (labels.length >= 3 && labels[labels.length - 1].length === 2 && secondLevelTLDs.has(labels[labels.length - 2])) {
    return labels[labels.length - 3]
  }

  return labels[labels.length - 2]
}

function deriveProviderName(value: string) {
  try {
    const url = new URL(value)
    const isLoopback = url.hostname === 'localhost' ||
      url.hostname.startsWith('127.') ||
      url.hostname === '[::1]'
    if (isLoopback) return `localhost${url.port ? `:${url.port}` : ''}`
    const baseLabel = registrableDomainLabel(url.hostname)
    if (!baseLabel) return 'OpenAI-compatible'
    if (KNOWN_PROVIDER_NAMES[baseLabel]) return KNOWN_PROVIDER_NAMES[baseLabel]
    return baseLabel
      .split(/[-_]+/)
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
      .join(' ')
  } catch {
    return 'OpenAI-compatible'
  }
}

function tokenizeCurl(input: string) {
  const normalized = input.replace(/\\\r?\n/g, ' ').trim()
  const tokens: string[] = []
  let current = ''
  let quote: '"' | "'" | null = null
  let escape = false

  for (const char of normalized) {
    if (escape) {
      current += char
      escape = false
      continue
    }

    if (quote === "'") {
      if (char === "'") quote = null
      else current += char
      continue
    }

    if (quote === '"') {
      if (char === '"') quote = null
      else if (char === '\\') escape = true
      else current += char
      continue
    }

    if (char === "'" || char === '"') {
      quote = char as '"' | "'"
      continue
    }

    if (char === '\\') {
      escape = true
      continue
    }

    if (/\s/.test(char)) {
      if (current) {
        tokens.push(current)
        current = ''
      }
      continue
    }

    current += char
  }

  if (current) tokens.push(current)
  return tokens
}

function parseCurlJSON(data: string) {
  try {
    return JSON.parse(data)
  } catch {
    const modelMatch = data.match(/["']model["']\s*:\s*["']([^"']+)["']/)
    const messagesMatch = data.match(/["']messages["']\s*:/)
    return {
      model: modelMatch?.[1] || '',
      hasMessages: !!messagesMatch
    }
  }
}

function inferIntegrationFromCurl(url: string, body: any) {
  const lowered = url.toLowerCase()
  if (lowered.includes('/embeddings')) return 'embeddings'
  if (lowered.includes('/responses')) return 'responses'
  if (Array.isArray(body?.messages) || body?.hasMessages) return 'chat'
  if (typeof body?.input === 'string' || Array.isArray(body?.input)) return lowered.includes('/responses') ? 'responses' : 'embeddings'
  return 'chat'
}

function isGroupSelected(value: string) {
  return grouping.group_names.includes(value)
}

function toggleGroup(value: string) {
  if (isGroupSelected(value)) {
    grouping.group_names = grouping.group_names.filter((item) => item !== value)
    return
  }
  grouping.group_names = groupOptions.value
    .map((item) => item.value)
    .filter((item) => item === value || grouping.group_names.includes(item))
}

function isDescriptorSelected(value: string) {
  return grouping.descriptor_tags.includes(value)
}

function toggleDescriptor(value: string) {
  if (isDescriptorSelected(value)) {
    grouping.descriptor_tags = grouping.descriptor_tags.filter((item) => item !== value)
    return
  }
  grouping.descriptor_tags = descriptorOptions.value
    .map((item) => item.value)
    .filter((item) => item === value || grouping.descriptor_tags.includes(item))
}

function isModalitySelected(value: string) {
  return grouping.modalities.includes(value)
}

function toggleModality(value: string) {
  if (isModalitySelected(value)) {
    grouping.modalities = grouping.modalities.filter((item) => item !== value)
    return
  }
  grouping.modalities = modalityOptions.value
    .map((item) => item.value)
    .filter((item) => item === value || grouping.modalities.includes(item))
}

function endpointDescriptors() {
  return Array.from(new Set(grouping.descriptor_tags))
}

function endpointModalities() {
  return Array.from(new Set(grouping.modalities))
}

function endpointCapabilityFlags() {
  const descriptors = endpointDescriptors()
  const modalities = endpointModalities()
  return {
    supportsStreaming: descriptors.includes('streaming'),
    supportsTools: descriptors.includes('tool-calling') || descriptors.includes('function-calling'),
    supportsVision: descriptors.includes('multimodal') ||
      grouping.group_names.includes('vision') ||
      modalities.some((value) => ['image', 'video', 'pdf'].includes(value))
  }
}

function inferBaseURLFromCurl(urlString: string) {
  const url = new URL(urlString)
  let path = url.pathname.replace(/\/+$/, '')
  const suffixes = ['/chat/completions', '/completions', '/responses', '/embeddings']
  for (const suffix of suffixes) {
    if (path.endsWith(suffix)) {
      path = path.slice(0, -suffix.length)
      break
    }
  }
  return `${url.origin}${path}`.replace(/\/+$/, '')
}

function extractAPIKey(headers: Record<string, string>) {
  const authorization = headers.authorization
  if (authorization) {
    const bearer = authorization.match(/^Bearer\s+(.+)$/i)
    return bearer ? bearer[1].trim() : authorization.trim()
  }
  return headers['x-api-key'] || headers['api-key'] || headers['anthropic-api-key'] || headers['x-auth-token'] || ''
}

function parseCurlImport(raw: string) {
  const tokens = tokenizeCurl(raw)
  if (!tokens.length || tokens[0] !== 'curl') {
    throw new Error('Paste a full curl command that starts with curl.')
  }

  let url = ''
  const headers: Record<string, string> = {}
  const dataParts: string[] = []

  for (let i = 1; i < tokens.length; i += 1) {
    const token = tokens[i]

    if ((token === '-H' || token === '--header') && tokens[i + 1]) {
      const rawHeader = tokens[i + 1]
      const splitIndex = rawHeader.indexOf(':')
      if (splitIndex > 0) {
        headers[rawHeader.slice(0, splitIndex).trim().toLowerCase()] = rawHeader.slice(splitIndex + 1).trim()
      }
      i += 1
      continue
    }

    if ((token === '-d' || token === '--data' || token === '--data-raw' || token === '--data-binary' || token === '--data-ascii') && tokens[i + 1]) {
      dataParts.push(tokens[i + 1])
      i += 1
      continue
    }

    if (token === '--url' && tokens[i + 1]) {
      url = tokens[i + 1]
      i += 1
      continue
    }

    if (/^https?:\/\//i.test(token)) {
      url = token
    }
  }

  if (!url) throw new Error('Could not find a request URL in that curl command.')

  const body = parseCurlJSON(dataParts.join(' ').trim())
  const modelID = typeof body?.model === 'string' ? body.model.trim() : ''
  if (!modelID) throw new Error('Could not find a model in the curl request body.')

  const baseURL = inferBaseURLFromCurl(url)

  return {
    providerLabel: deriveProviderName(baseURL),
    modelID,
    baseURL,
    apiKey: extractAPIKey(headers),
    integration: inferIntegrationFromCurl(url, body)
  }
}

function openImportModal() {
  importError.value = ''
  importOpen.value = true
}

function importFromCurl() {
  importError.value = ''
  try {
    const parsed = parseCurlImport(importCurl.value)

    if (!lockedProvider.value) {
      connection.provider_label = parsed.providerLabel
      connection.base_url = parsed.baseURL
      connection.api_key = parsed.apiKey
    }

    connection.model_id = parsed.modelID
    connection.integration = parsed.integration
    importOpen.value = false

    app.pushToast({
      title: 'Curl imported',
      description: 'Connection details were extracted into step 1.',
      tone: 'success'
    })
  } catch (error: any) {
    importError.value = error?.message || 'Could not parse that curl command.'
  }
}

function toPositiveInteger(value: string, fallback = 0) {
  const parsed = Number.parseInt(value, 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback
}

function toMicrosFromUSD(value: string) {
  const parsed = Number.parseFloat(value)
  if (!Number.isFinite(parsed) || parsed < 0) return 0
  return Math.round(parsed * 1_000_000)
}

function integrationLabel(value: string) {
  return integrationOptions.find((item) => item.value === value)?.label || value
}

function inferredContextWindow() {
  const model = connection.model_id.toLowerCase()
  if (routeKind.value === 'embeddings' || model.includes('embed')) return 8192
  if (/200k|256k/.test(model)) return 200000
  if (/32k/.test(model)) return 32000
  return 128000
}

function validatePositiveOptional(key: string, label: string) {
  const raw = (guardrails as Record<string, string>)[key]
  if (!raw) return
  if (!toPositiveInteger(raw)) errors[key] = `${label} must be a positive whole number.`
}

function validateMoneyOptional(key: string, label: string) {
  const raw = (guardrails as Record<string, string>)[key]
  if (!raw) return
  const parsed = Number.parseFloat(raw)
  if (!Number.isFinite(parsed) || parsed < 0) errors[key] = `${label} must be a valid non-negative amount in USD.`
}

function validateConnection() {
  clearErrors()
  if (!lockedProvider.value && !connection.provider_label.trim()) {
    errors.provider_label = 'Enter a provider name operators will recognize.'
  }
  if (!lockedProvider.value && !normalizeBaseURL(connection.base_url)) {
    errors.base_url = 'Enter the base URL that Model Relay should call for this provider.'
  }
  if (!connection.model_id.trim()) {
    errors.model_id = 'Enter the upstream model ID the provider expects.'
  }
  return Object.keys(errors).length === 0
}

function validateGuardrails() {
  clearErrors()
  for (const field of requestLimitFields) validatePositiveOptional(field.key, field.label)
  for (const field of tokenLimitFields) validatePositiveOptional(field.key, field.label)
  for (const field of spendLimitFields) validateMoneyOptional(field.key, field.label)
  validateMoneyOptional('input_cost', 'Input cost per 1M tokens')
  validateMoneyOptional('output_cost', 'Output cost per 1M tokens')
  validateMoneyOptional('request_cost', 'Cost per request')
  return Object.keys(errors).length === 0
}

async function saveConnection() {
  if (!validateConnection()) return

  saving.value = true
  try {
    let provider = createdProvider.value ?? existingProvider.value
    let credential = createdCredential.value ?? existingCredential.value

    if (!lockedProvider.value) {
      const providerPayload = {
        name: resolvedProviderName.value,
        slug: slugify(resolvedProviderName.value) || 'openai-compatible',
        base_url: normalizeBaseURL(connection.base_url),
        auth_mode: connection.api_key.trim() ? 'bearer_static' : 'none',
        auth_header_name: connection.api_key.trim() ? 'Authorization' : '',
        enabled: true,
        notes: '',
        health_status: provider?.health_status || 'healthy'
      }

      provider = provider
        ? await api.put<Provider>(`/api/providers/${provider.id}`, providerPayload)
        : await api.post<Provider>('/api/providers', providerPayload)

      if (connection.api_key.trim()) {
        const credentialPayload = {
          provider_id: provider.id,
          name: `${resolvedProviderName.value} key`,
          secret: connection.api_key.trim(),
          enabled: true
        }
        credential = credential
          ? await api.put<Credential>(`/api/credentials/${credential.id}`, credentialPayload)
          : await api.post<Credential>('/api/credentials', credentialPayload)
      } else {
        if (credential?.id) await api.del(`/api/credentials/${credential.id}`)
        credential = null
      }
    }

    if (!provider) throw new Error('Provider could not be resolved for this model.')

    const capabilityFlags = endpointCapabilityFlags()

    const endpointPayload = {
      provider_id: provider.id,
      credential_id: credential?.id || '',
      name: resolvedModelName.value,
      upstream_model: connection.model_id.trim(),
      route_kind: routeKind.value,
      enabled: true,
      manual_rank: createdEndpoint.value?.manual_rank || 1,
      suggested_rank: createdEndpoint.value?.suggested_rank || 0,
      suggested_score: createdEndpoint.value?.suggested_score || 0,
      quality_score: createdEndpoint.value?.quality_score || 80,
      context_window: createdEndpoint.value?.context_window || inferredContextWindow(),
      param_size_b: createdEndpoint.value?.param_size_b || 0,
      modalities: createdEndpoint.value?.modalities || [],
      descriptor_tags: createdEndpoint.value?.descriptor_tags || [],
      supports_streaming: createdEndpoint.value?.supports_streaming ?? capabilityFlags.supportsStreaming,
      supports_tools: createdEndpoint.value?.supports_tools ?? capabilityFlags.supportsTools,
      supports_vision: createdEndpoint.value?.supports_vision ?? capabilityFlags.supportsVision,
      pacing: createdEndpoint.value?.pacing ?? true,
      health_status: createdEndpoint.value?.health_status || 'healthy',
      notes: ''
    }

    createdProvider.value = provider
    createdCredential.value = credential
    createdEndpoint.value = createdEndpoint.value
      ? await api.put<Endpoint>(`/api/endpoints/${createdEndpoint.value.id}`, endpointPayload)
      : await api.post<Endpoint>('/api/endpoints', endpointPayload)

    await catalog.refreshAll()
    setStep(1)
    app.pushToast({
      title: lockedProvider.value ? 'Model added under provider' : 'Provider and model connected',
      description: lockedProvider.value
        ? 'Model Relay reused the existing provider and added this model underneath it.'
        : 'The upstream connection and its first model are now ready for routing setup.',
      tone: 'success'
    })
  } catch (error: any) {
    app.pushToast({
      title: 'Could not save this connection',
      description: error?.data?.message || error?.data?.error || error?.message || 'Please review the connection details and try again.',
      tone: 'error'
    })
  } finally {
    saving.value = false
  }
}

async function saveGrouping() {
  if (!createdEndpoint.value) return

  clearErrors()
  saving.value = true
  try {
    const selectedGroupValues = groupOptions.value
      .map((item) => item.value)
      .filter((value) => grouping.group_names.includes(value))
    const modalities = endpointModalities()
    const descriptors = endpointDescriptors()
    const capabilityFlags = endpointCapabilityFlags()

    createdEndpoint.value = await api.put<Endpoint>(`/api/endpoints/${createdEndpoint.value.id}`, {
      ...createdEndpoint.value,
      context_window: createdEndpoint.value.context_window || inferredContextWindow(),
      modalities,
      descriptor_tags: descriptors,
      supports_streaming: capabilityFlags.supportsStreaming,
      supports_tools: capabilityFlags.supportsTools,
      supports_vision: capabilityFlags.supportsVision,
      pacing: createdEndpoint.value.pacing ?? true
    })

    const resolvedLanes: RoutingLane[] = []
    for (const groupName of selectedGroupValues) {
      const existingLane = catalog.lanes.find((item) => item.name === groupName)
      const lane = existingLane
        ? existingLane
        : await api.post<RoutingLane>('/api/routing-lanes', {
          name: groupName,
          description: groupOptions.value.find((item) => item.value === groupName)?.description || `Routing group for ${groupName}.`,
          enabled: true,
          default_max_wait_ms: 60000,
          allow_fallback: true,
          default_priority: 50
        })
      resolvedLanes.push(lane)
    }

    const existingMemberships = catalog.memberships.filter((item) => item.endpoint_id === createdEndpoint.value.id)
    const selectedLaneIDs = new Set(resolvedLanes.map((item) => item.id))

    for (const membership of existingMemberships) {
      if (!selectedLaneIDs.has(membership.lane_id)) {
        await api.del(`/api/lane-memberships/${membership.id}`)
      }
    }

    for (const lane of resolvedLanes) {
      const existingMembership = existingMemberships.find((item) => item.lane_id === lane.id)
      const nextRank = Math.max(
        0,
        ...catalog.memberships
          .filter((item) => item.lane_id === lane.id)
          .map((item) => item.manual_rank)
      ) + 1

      const membershipPayload = {
        lane_id: lane.id,
        endpoint_id: createdEndpoint.value.id,
        manual_rank: existingMembership?.manual_rank || nextRank,
        enabled: true
      }

      if (existingMembership) {
        await api.put<LaneMembership>(`/api/lane-memberships/${existingMembership.id}`, membershipPayload)
      } else {
        await api.post<LaneMembership>('/api/lane-memberships', membershipPayload)
      }
    }

    createdLane.value = resolvedLanes[0] || null

    await catalog.refreshAll()
    setStep(2)
    app.pushToast({
      title: selectedGroupValues.length ? 'Grouping saved' : 'Grouping skipped',
      description: selectedGroupValues.length
        ? `${resolvedModelName.value} is now available in ${selectedGroupValues.join(', ')}.`
        : `${resolvedModelName.value} was saved without a routing group. You can add one later.`,
      tone: 'success'
    })
  } catch (error: any) {
    app.pushToast({
      title: 'Could not save the group configuration',
      description: error?.data?.message || error?.data?.error || error?.message || 'Please review the group and functionality fields and try again.',
      tone: 'error'
    })
  } finally {
    saving.value = false
  }
}

async function upsertLimit(metric: Metric, period: Period, rawValue: string) {
  if (!createdEndpoint.value) return

  const existing = catalog.limitPolicies.find((item) =>
    item.scope_type === 'endpoint' &&
    item.scope_id === createdEndpoint.value?.id &&
    item.metric === metric &&
    item.period === period
  )

  const value = metric === 'spend'
    ? toMicrosFromUSD(rawValue)
    : toPositiveInteger(rawValue)

  if (!rawValue || !value) {
    if (existing) await api.del(`/api/limit-policies/${existing.id}`)
    return
  }

  const payload = {
    scope_type: 'endpoint',
    scope_id: createdEndpoint.value.id,
    metric,
    period,
    limit_value: value,
    enabled: true,
    source: 'configured'
  }

  if (existing) await api.put<LimitPolicy>(`/api/limit-policies/${existing.id}`, { ...existing, ...payload })
  else await api.post<LimitPolicy>('/api/limit-policies', payload)
}

async function upsertPricing() {
  if (!createdEndpoint.value) return

  const existing = catalog.pricingPolicies.find((item) => item.endpoint_id === createdEndpoint.value?.id)
  const hasPricing = !!(guardrails.input_cost || guardrails.output_cost || guardrails.request_cost)

  if (!hasPricing) {
    if (existing) await api.del(`/api/pricing-policies/${existing.id}`)
    return
  }

  const payload = {
    endpoint_id: createdEndpoint.value.id,
    currency: 'USD',
    input_cost_micros_per_1m_tokens: toMicrosFromUSD(guardrails.input_cost),
    output_cost_micros_per_1m_tokens: toMicrosFromUSD(guardrails.output_cost),
    cached_input_cost_micros_per_1m_tokens: 0,
    flat_request_cost_micros: toMicrosFromUSD(guardrails.request_cost)
  }

  if (existing) await api.put<PricingPolicy>(`/api/pricing-policies/${existing.id}`, { ...existing, ...payload })
  else await api.post<PricingPolicy>('/api/pricing-policies', payload)
}

async function saveGuardrails() {
  if (!validateGuardrails()) return

  saving.value = true
  try {
    for (const field of requestLimitFields) await upsertLimit('requests', field.period, guardrails[field.key])
    for (const field of tokenLimitFields) await upsertLimit('tokens', field.period, guardrails[field.key])
    for (const field of spendLimitFields) await upsertLimit('spend', field.period, guardrails[field.key])
    await upsertPricing()
    await catalog.refreshAll()
    setStep(3)
    app.pushToast({
      title: 'Limits and budgets saved',
      description: 'Optional limits and cost assumptions are now attached to this model.',
      tone: 'success'
    })
  } catch (error: any) {
    app.pushToast({
      title: 'Could not save limits and budgets',
      description: error?.data?.message || error?.data?.error || error?.message || 'Please review the guardrail values and try again.',
      tone: 'error'
    })
  } finally {
    saving.value = false
  }
}

async function readStreamingResponse(response: Response) {
  if (!response.body) {
    const fallbackText = await response.text()
    return {
      preview: fallbackText || 'Streaming response completed.',
      raw: fallbackText
    }
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let preview = ''
  let raw = ''
  const consumeBuffer = (chunk: string) => {
    const lines = chunk.split(/\r?\n/)
    buffer = lines.pop() || ''

    for (const line of lines) {
      const trimmed = line.trim()
      if (!trimmed.startsWith('data:')) continue
      const payload = trimmed.slice(5).trim()
      if (!payload || payload === '[DONE]') continue
      try {
        const parsed = JSON.parse(payload)
        preview += parsed.choices?.[0]?.delta?.content || ''
        preview += parsed.output_text || ''
        if (Array.isArray(parsed.output)) {
          preview += parsed.output
            .flatMap((item: any) => item.content || [])
            .map((item: any) => item.text || '')
            .join('')
        }
      } catch {
        preview += `${payload} `
      }
    }
  }

  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    const text = decoder.decode(value, { stream: true })
    buffer += text
    raw += text
    consumeBuffer(buffer)
  }

  const tail = decoder.decode()
  if (tail) {
    buffer += tail
    raw += tail
  }
  if (buffer.trim()) consumeBuffer(`${buffer}\n`)

  if (!preview.trim()) preview = 'Streaming response completed.'

  return {
    preview: preview.trim(),
    raw
  }
}

async function sendTest() {
  if (testing.value || !createdEndpoint.value || !createdLane.value) return

  testing.value = true
  testResult.value = null
  testRaw.value = null
  testMeta.value = {}
  showRawResponse.value = false

  try {
    const path = routeKind.value === 'responses'
      ? '/v1/responses'
      : routeKind.value === 'embeddings'
        ? '/v1/embeddings'
        : '/v1/chat/completions'

    const body = routeKind.value === 'responses'
      ? { model: createdLane.value.name, input: testRequest.prompt, stream: testRequest.stream }
      : routeKind.value === 'embeddings'
        ? { model: createdLane.value.name, input: testRequest.prompt }
        : { model: createdLane.value.name, messages: [{ role: 'user', content: testRequest.prompt }], stream: testRequest.stream }

    const response = await fetch(modelRelayApiPath(path), {
      method: 'POST',
      credentials: 'include',
      headers: inferenceHeaders(inferenceToken.value),
      body: JSON.stringify(body)
    })

    testMeta.value = {
      endpoint: response.headers.get('X-Relay-Selected-Endpoint') || createdEndpoint.value.name,
      provider: createdProvider.value?.name || resolvedProviderName.value,
      group: createdLane.value.name,
      wait: response.headers.get('X-Relay-Wait-Ms') || '0',
      fallback: response.headers.get('X-Relay-Fallback-Count') || '0',
      status: String(response.status)
    }

    if (response.status === 401) {
      await response.body?.cancel()
      testResult.value = { error: INFERENCE_AUTH_HELP }
      return
    }

    const isStreaming = !!testRequest.stream && routeKind.value !== 'embeddings'
    if (isStreaming) {
      const streamed = await readStreamingResponse(response)
      testRaw.value = streamed.raw
      testResult.value = routeKind.value === 'responses'
        ? { output_text: streamed.preview }
        : { choices: [{ message: { content: streamed.preview } }] }
    } else {
      const text = await response.text()
      testRaw.value = text
      try {
        testResult.value = text ? JSON.parse(text) : { message: 'Empty response body' }
      } catch {
        testResult.value = { error: text || 'The provider returned a non-JSON response.' }
      }
    }

    if (!response.ok) {
      throw new Error(testResult.value?.error || 'The gateway returned a non-success response.')
    }

    app.pushToast({
      title: 'Test request finished',
      description: 'Model Relay successfully routed a live request through the configured group.',
      tone: 'success'
    })
  } catch (error: any) {
    if (!testResult.value) testResult.value = { error: error?.message || 'Test request failed.' }
    app.pushToast({
      title: 'Test request failed',
      description: error?.message || 'The gateway could not complete the verification request.',
      tone: 'error'
    })
  } finally {
    testing.value = false
  }
}

function openDashboard() {
  return navigateTo(modelRelayPath('/dashboard'))
}

function openModels() {
  return navigateTo(modelRelayPath('/providers'))
}

onMounted(async () => {
  await catalog.refreshAll()
})
</script>

<template>
  <div class="setup-wizard mx-auto max-w-6xl space-y-6">
    <UStepper
      v-model="stepIndex"
      :items="stepperItems"
      color="info"
      size="lg"
      class="px-1 py-2 sm:px-2"
      :ui="{
        wrapper: 'text-center',
        title: 'text-sm font-semibold text-slate-900 dark:text-white',
        description: 'mx-auto mt-1 hidden max-w-52 text-xs leading-5 text-slate-600 dark:text-slate-300/75 lg:block',
        separator: 'bg-[var(--app-border-strong)]'
      }"
    />

    <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_300px]">
      <UiPanel :padded="false">
        <div class="p-7 sm:p-8">
          <div class="mx-auto max-w-4xl space-y-8">
            <div v-if="stepIndex === 0" class="space-y-8">
              <section
                v-if="lockedProvider && existingProvider"
                class="border-y border-[var(--app-border)] py-5"
              >
                <p class="text-xs font-semibold uppercase tracking-[0.22em] text-slate-400">Using existing provider</p>
                <div class="mt-3 space-y-2">
                  <p class="text-lg font-semibold text-white">{{ existingProvider.name }}</p>
                  <p class="text-sm text-slate-300/78">{{ existingProvider.base_url }}</p>
                  <p class="text-sm text-slate-300/70">
                    {{ existingCredential ? 'Provider auth already configured.' : 'No stored provider secret found. Add one from the Providers page if needed.' }}
                  </p>
                </div>
              </section>

              <div class="grid gap-5 md:grid-cols-2 md:auto-rows-fr">
                <UiField
                  v-if="!lockedProvider"
                  label="Provider name"
                  help="Name this upstream provider the way operators should recognize it in routing, logs, and usage."
                  required
                  :error="errors.provider_label"
                >
                  <input v-model="connection.provider_label" class="app-input" placeholder="OpenRouter production">
                </UiField>

                <UiField
                  label="Model ID"
                  help="Use the exact upstream model name the provider expects."
                  required
                  :error="errors.model_id"
                >
                  <input v-model="connection.model_id" class="app-input" placeholder="Enter upstream model ID">
                </UiField>
              </div>

              <UiField
                v-if="!lockedProvider"
                label="Base URL"
                help="Point Model Relay at the upstream API root. Include `/v1` if the provider expects it."
                required
                :error="errors.base_url"
              >
                <input v-model="connection.base_url" class="app-input" placeholder="https://api.openai.com/v1">
              </UiField>

              <div class="grid gap-5 md:grid-cols-2 md:auto-rows-fr">
                <UiField
                  v-if="!lockedProvider"
                  label="API key"
                  help="Stored securely and used when Model Relay calls this provider."
                  optional
                >
                  <div class="relative">
                    <input
                      v-model="connection.api_key"
                      :type="showAPIKey ? 'text' : 'password'"
                      class="app-input pr-14"
                      placeholder="sk-..."
                    >
                    <button
                      type="button"
                      class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed absolute inset-y-0 right-0 flex items-center px-4 text-sm font-medium text-slate-300 transition hover:text-white"
                      @click="showAPIKey = !showAPIKey"
                    >
                      {{ showAPIKey ? 'Hide' : 'Show' }}
                    </button>
                  </div>
                </UiField>

                <UiField
                  label="Integration type"
                  help="Choose the request shape Model Relay should use for this model."
                  required
                >
                  <select v-model="connection.integration" class="app-select">
                    <option v-for="option in integrationOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
                  </select>
                </UiField>
              </div>

              <div class="flex flex-col items-stretch gap-3 border-t border-white/8 pt-6 sm:flex-row sm:items-center sm:justify-end">
                <UiButton :disabled="saving" @click="saveConnection">
                  {{ saving ? 'Saving Connection...' : 'Save and Continue' }}
                </UiButton>
              </div>
            </div>

            <div v-else-if="stepIndex === 1" class="space-y-8">
              <div>
                <div class="flex items-start justify-between gap-4">
                  <div class="min-w-0">
                    <p class="text-base font-semibold text-white">Routing groups</p>
                    <p class="mt-2 text-sm leading-6 text-slate-300/76">
                      Add this model into the routing groups it should serve.
                    </p>
                  </div>
                  <UiBadge class="shrink-0 whitespace-nowrap" tone="slate">{{ grouping.group_names.length }} selected</UiBadge>
                </div>

                <div class="mt-5 space-y-2">
                  <button
                    v-for="option in groupOptions"
                    :key="option.value"
                    type="button"
                    class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed flex w-full items-center justify-between gap-4 rounded-xl border px-4 py-3 text-left transition"
                    :class="isGroupSelected(option.value)
                      ? 'border-amber-400/30 bg-amber-400/10 text-amber-200'
                      : 'border-white/10 text-white hover:border-white/20 hover:bg-white/[0.03]'"
                    @click="toggleGroup(option.value)"
                  >
                    <span class="truncate text-sm font-semibold">{{ option.label }}</span>
                    <UiBadge class="shrink-0" :tone="isGroupSelected(option.value) ? 'amber' : 'slate'">
                      {{ isGroupSelected(option.value) ? 'Selected' : 'Add' }}
                    </UiBadge>
                  </button>
                </div>

              </div>

              <section v-if="showGroupingMetadata" class="border-t border-[var(--app-border)] pt-5">
                <div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                  <div class="max-w-2xl">
                    <p class="text-base font-semibold text-white">Modalities</p>
                    <p class="mt-2 text-sm leading-6 text-slate-300/76">
                      Mark the input families this model can handle. Keep them explicit so downstream routing can separate text-only models from broader multimodal targets later.
                    </p>
                  </div>
                  <UiBadge tone="slate">{{ grouping.modalities.length }} selected</UiBadge>
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
                    @click="toggleModality(modality.value)"
                  >
                    {{ modality.label }}
                  </button>
                </div>
              </section>

              <section v-if="showGroupingMetadata" class="border-t border-[var(--app-border)] pt-5">
                <div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                  <div class="max-w-2xl">
                    <p class="text-base font-semibold text-white">Model descriptors</p>
                    <p class="mt-2 text-sm leading-6 text-slate-300/76">
                      Descriptors live on the model itself. They help explain what this model is good at and will support smarter routing guidance later.
                    </p>
                  </div>
                  <UiBadge tone="slate">{{ grouping.descriptor_tags.length }} selected</UiBadge>
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
                    @click="toggleDescriptor(descriptor.value)"
                  >
                    {{ descriptor.label }}
                  </button>
                </div>
              </section>

              <div class="flex flex-col-reverse items-stretch gap-3 border-t border-white/8 pt-6 sm:flex-row sm:items-center sm:justify-between">
                <UiButton tone="ghost" @click="stepIndex = 0">Back</UiButton>
                <UiButton :disabled="saving" @click="saveGrouping">
                  {{ saving ? 'Saving Grouping...' : 'Save and Continue' }}
                </UiButton>
              </div>
            </div>

            <div v-else-if="stepIndex === 2" class="space-y-6">
              <section class="border-b border-white/8 pb-6">
                <div>
                  <p class="text-base font-semibold text-white">Requests</p>
                  <p class="mt-1 text-sm leading-6 text-slate-300/74">Optional caps for how many requests this model should serve over each time window.</p>
                </div>
                <div class="mt-5 grid gap-5 md:grid-cols-2 md:auto-rows-fr xl:grid-cols-5">
                  <UiField
                    v-for="field in requestLimitFields"
                    :key="field.key"
                    :label="field.label"
                    compact
                    :error="errors[field.key]"
                  >
                    <input v-model="guardrails[field.key]" type="number" min="1" class="app-input" placeholder="e.g. 60">
                  </UiField>
                </div>
              </section>

              <section class="border-b border-white/8 pb-6">
                <div>
                  <p class="text-base font-semibold text-white">Tokens</p>
                  <p class="mt-1 text-sm leading-6 text-slate-300/74">Useful when the upstream enforces token-based pacing or you want a clearer throughput ceiling.</p>
                </div>
                <div class="mt-5 grid gap-5 md:grid-cols-2 md:auto-rows-fr xl:grid-cols-5">
                  <UiField
                    v-for="field in tokenLimitFields"
                    :key="field.key"
                    :label="field.label"
                    compact
                    :error="errors[field.key]"
                  >
                    <input v-model="guardrails[field.key]" type="number" min="1" class="app-input" placeholder="e.g. 100000">
                  </UiField>
                </div>
              </section>

              <section class="border-b border-white/8 pb-6">
                <div>
                  <p class="text-base font-semibold text-white">Spend caps</p>
                  <p class="mt-1 text-sm leading-6 text-slate-300/74">Optional USD ceilings per period. Model Relay stores and enforces these in micros internally.</p>
                </div>
                <div class="mt-5 grid gap-5 md:grid-cols-2 md:auto-rows-fr xl:grid-cols-5">
                  <UiField
                    v-for="field in spendLimitFields"
                    :key="field.key"
                    :label="field.label"
                    compact
                    :error="errors[field.key]"
                  >
                    <input v-model="guardrails[field.key]" type="number" min="0" step="0.0001" class="app-input" placeholder="e.g. 25.00">
                  </UiField>
                </div>
              </section>

              <section class="border-y border-[var(--app-border)] py-5">
                <div>
                  <p class="text-base font-semibold text-white">Costs</p>
                  <p class="mt-1 text-sm leading-6 text-slate-300/74">Optional per-model pricing so Model Relay can estimate queued spend and reconcile actual cost after completion.</p>
                </div>
                <div class="mt-5 grid gap-5 md:grid-cols-2 md:auto-rows-fr xl:grid-cols-3">
                  <UiField label="Input cost / 1M tokens" compact :error="errors.input_cost">
                    <input v-model="guardrails.input_cost" type="number" min="0" step="0.0001" class="app-input" placeholder="e.g. 2.50">
                  </UiField>
                  <UiField label="Output cost / 1M tokens" compact :error="errors.output_cost">
                    <input v-model="guardrails.output_cost" type="number" min="0" step="0.0001" class="app-input" placeholder="e.g. 10.00">
                  </UiField>
                  <UiField label="Cost per request" compact :error="errors.request_cost">
                    <input v-model="guardrails.request_cost" type="number" min="0" step="0.0001" class="app-input" placeholder="e.g. 0.001">
                  </UiField>
                </div>
              </section>

              <div class="flex flex-col-reverse items-stretch gap-3 border-t border-white/8 pt-6 sm:flex-row sm:items-center sm:justify-between">
                <UiButton tone="ghost" @click="stepIndex = 1">Back</UiButton>
                <UiButton :disabled="saving" @click="saveGuardrails">
                  {{ saving ? 'Saving Limits & Budgets...' : 'Save and Continue' }}
                </UiButton>
              </div>
            </div>

            <div v-else class="space-y-8">
              <UiField
                :label="routeKind === 'embeddings' ? 'Sample text' : 'Test prompt'"
                :help="createdLane
                  ? 'Send one simple request through the gateway to confirm the provider, model, and primary routing group are wired correctly.'
                  : 'Testing becomes available after you add this model to a routing group.'"
              >
                <textarea
                  v-model="testRequest.prompt"
                  rows="5"
                  class="app-textarea min-h-[140px] resize-y"
                  :placeholder="routeKind === 'embeddings' ? 'Text to embed for verification.' : 'Write a short prompt to verify the model.'"
                />
              </UiField>

              <InferenceTokenField v-if="createdLane" v-model="inferenceToken" />

              <div class="flex flex-col-reverse items-stretch gap-3 border-t border-white/8 pt-6 sm:flex-row sm:items-center sm:justify-between">
                <UiButton tone="ghost" @click="stepIndex = 2">Back</UiButton>
                <div class="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:justify-end">
                  <UiButton tone="secondary" @click="openModels">Edit Advanced Model Settings</UiButton>
                  <UiButton v-if="createdLane" :disabled="testing" @click="sendTest">
                    {{ testing ? 'Sending Test Request...' : 'Send Test Request' }}
                  </UiButton>
                  <UiButton v-else @click="openDashboard">Finish Setup</UiButton>
                </div>
              </div>

              <div v-if="testResult" class="space-y-5">
                <div
                  class="rounded-[1.2rem] border px-5 py-5"
                  :class="testResult.error ? 'border-rose-500/25 bg-rose-500/10' : 'border-emerald-400/20 bg-emerald-400/10'"
                >
                  <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
                    <div>
                      <p class="text-xs font-semibold uppercase tracking-[0.22em]" :class="testResult.error ? 'text-rose-300' : 'text-emerald-300'">
                        {{ testResult.error ? 'Needs attention' : 'Connection verified' }}
                      </p>
                      <p class="mt-3 text-lg font-semibold leading-8 text-white">{{ testResponsePreview }}</p>
                    </div>
                    <UiButton v-if="!testResult.error" tone="secondary" @click="openDashboard">Finish Setup</UiButton>
                  </div>
                </div>

                <div class="ui-metric-strip grid-cols-2 xl:grid-cols-4">
                  <div class="app-metric">
                    <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Selected model</p>
                    <p class="mt-2 text-sm font-medium text-white">{{ testMeta.endpoint || createdEndpoint?.name || 'n/a' }}</p>
                  </div>
                  <div class="app-metric">
                    <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Provider</p>
                    <p class="mt-2 text-sm font-medium text-white">{{ testMeta.provider || resolvedProviderName }}</p>
                  </div>
                  <div class="app-metric">
                    <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Group</p>
                    <p class="mt-2 text-sm font-medium text-white">{{ testMeta.group || primaryGroupName || 'n/a' }}</p>
                  </div>
                  <div class="app-metric">
                    <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Fallback count</p>
                    <p class="mt-2 text-sm font-medium text-white">{{ testMeta.fallback || '0' }}</p>
                  </div>
                </div>

                <div class="ui-metric-strip grid-cols-2 xl:grid-cols-5">
                  <div class="app-metric">
                    <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Wait time</p>
                    <p class="mt-2 text-sm font-medium text-white">{{ testMeta.wait || '0' }}ms</p>
                  </div>
                  <div
                    v-for="stat in verificationStats"
                    :key="stat.label"
                    class="app-metric"
                  >
                    <p class="text-xs uppercase tracking-[0.2em] text-slate-400">{{ stat.label }}</p>
                    <p class="mt-2 text-sm font-medium text-white">{{ stat.value }}</p>
                  </div>
                </div>

                <details class="border-y border-[var(--app-border)] py-5" :open="showRawResponse">
                  <summary class="cursor-pointer list-none" @click.prevent="showRawResponse = !showRawResponse">
                    <div class="flex items-center justify-between">
                      <span class="text-sm font-semibold text-white">Show raw response</span>
                      <span class="text-xs uppercase tracking-[0.2em] text-slate-500">Debug</span>
                    </div>
                  </summary>
                  <div class="mt-4 space-y-4">
                    <UiCodeBlock v-if="typeof testRaw === 'string'" :code="testRaw" />
                    <UiJsonViewer v-else :value="testRaw || testResult" />
                  </div>
                </details>
              </div>
            </div>
          </div>
        </div>
      </UiPanel>

      <UiPanel
        v-if="stepIndex === 0"
        title="Import From cURL"
        subtitle="Paste a real upstream curl command and Model Relay will prefill the provider, model, base URL, API key, and request type for step 1."
      >
        <div class="space-y-5">
          <UiButton class="w-full justify-center" tone="secondary" @click="openImportModal">Import From cURL</UiButton>
          <p class="text-sm leading-6 text-slate-300/74">
            Paste the exact curl request you already use against the upstream API. Model Relay will extract the useful connection fields automatically.
          </p>
        </div>
      </UiPanel>

      <UiPanel v-else title="Setup summary" subtitle="A compact preview of the provider, model, and routing groups you are building.">
        <dl>
          <div
            v-for="row in summaryRows"
            :key="row.label"
            class="app-kv"
          >
            <dt>{{ row.label }}</dt>
            <dd class="max-w-[160px] break-words text-right">{{ row.value }}</dd>
          </div>
        </dl>
      </UiPanel>
    </div>

    <UiModal
      :open="importOpen"
      title="Import From cURL"
      subtitle="Paste a real upstream curl request. Model Relay will extract the connection details and prefill step 1."
      @close="importOpen = false"
    >
      <div class="space-y-6">
        <UiField
          label="Curl command"
          help="Paste the full curl command exactly as you already use it, including headers and JSON body."
          required
          :error="importError"
        >
          <textarea
            v-model="importCurl"
            rows="9"
            class="app-textarea min-h-[220px] resize-y font-mono text-sm"
            placeholder="curl -i -X POST https://api.mistral.ai/v1/chat/completions \
-H &quot;Authorization: Bearer REDACTED&quot; \
-H &quot;Content-Type: application/json&quot; \
-d '{&quot;model&quot;: &quot;labs-leanstral-2603&quot;, &quot;messages&quot;: [{&quot;role&quot;: &quot;user&quot;, &quot;content&quot;: &quot;Hello World&quot;}]}'"
          />
        </UiField>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <UiButton tone="ghost" @click="importOpen = false">Cancel</UiButton>
          <UiButton @click="importFromCurl">Import From cURL</UiButton>
        </div>
      </div>
    </UiModal>
  </div>
</template>

<style scoped>
.setup-wizard :deep([data-slot='trigger']) {
  border: 1px solid var(--app-border-strong);
  background: var(--app-table-bg);
  color: var(--app-copy);
  box-shadow: none;
}

.setup-wizard :deep([data-slot='item'][data-state='active'] [data-slot='trigger']),
.setup-wizard :deep([data-slot='item'][data-state='completed'] [data-slot='trigger']) {
  border-color: var(--app-primary-bg);
  background: var(--app-primary-bg);
  color: var(--app-primary-text);
}

.setup-wizard :deep([data-slot='separator']) {
  background: var(--app-border-strong);
}

.setup-wizard :deep([data-slot='item'][data-state='completed'] [data-slot='separator']) {
  background: var(--app-primary-bg);
}
</style>
