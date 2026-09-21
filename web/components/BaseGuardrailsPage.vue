<script setup lang="ts">
import type { Guardrail, GuardrailBinding, GuardrailPreset, GuardrailRuleSet, RequestLog } from '~/types/admin'

type WizardForm = Omit<Guardrail, 'id' | 'slug' | 'created_at' | 'updated_at'> & { id?: string, secret?: string }

const api = useRelayApi()
const catalog = useCatalogStore()
const guardrails = ref<Guardrail[]>([])
const presets = ref<GuardrailPreset[]>([])
const recentLogs = ref<RequestLog[]>([])
const catalogBindings = ref<GuardrailBinding[]>([])
const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')
const wizardOpen = ref(false)
const testOpen = ref(false)
const step = ref(1)
const selectedPreset = ref('')
const selectedStarterPolicy = ref('')
const bindings = ref<GuardrailBinding[]>([])
const customHeaders = ref<Array<{ name: string, value: string }>>([{ name: '', value: '' }])
const curlInput = ref('')
const sampleResponse = ref('{"allowed":true}')
const previewResult = ref<any>(null)
const testStage = ref<'pre_dispatch' | 'post_response'>('pre_dispatch')
const testRequestText = ref('Please review this example request.')
const testResponseText = ref('This is an example assistant response.')
const testResult = ref<any>(null)
const mappingStage = ref<'pre_dispatch' | 'post_response'>('pre_dispatch')
const activeRuleIndex = ref(0)
type RuleValueType = 'string' | 'number' | 'boolean' | 'json'
const ruleValueTypes = ref<RuleValueType[]>([])
const selectedTemplateVariable = ref('request.text')
const templateVariables = [
  'request.raw_json', 'request.messages', 'request.text', 'request.latest_user_message', 'request.model', 'request.id',
  'request.status_code'
]
const blankRules = (): GuardrailRuleSet => ({
  match_mode: 'any',
  missing_path: 'error',
  rules: [{ id: 'policy-match', label: 'Policy match', source: 'json_body', path: '$.allowed', operator: 'equals', value: false }],
  on_match: { action: 'block', http_status: 403, error_type: 'content_policy_violation', error_code: 'content_policy_violation', message: 'This request was blocked by the configured policy.' },
  on_no_match: { action: 'allow' }
})

const defaultFailure = JSON.stringify({ mode: 'fail_closed', http_status: 503, error_type: 'guardrail_error', error_code: 'guardrail_unavailable', message: 'The configured guardrail service is unavailable.' }, null, 2)

function blankForm(): WizardForm {
  const rules = JSON.stringify(blankRules(), null, 2)
  return {
    name: '', description: '', enabled: false, preset_slug: 'custom-http', priority: 100,
    http_method: 'POST', base_url: '', auth_mode: 'none', credential_id: null, secret_header_name: '',
    request_headers_json: '{}', timeout_ms: 3000, max_request_bytes: 1048576, max_response_bytes: 1048576,
    follow_redirects: false, network_access_mode: 'public_https', pre_dispatch_enabled: true,
    post_response_enabled: false, pre_request_template_json: '{\n  "text": "{{request.text}}"\n}',
    pre_response_rules_json: rules, pre_failure_policy_json: defaultFailure,
    post_request_template_json: '{\n  "text": "{{request.text}}"\n}', post_response_rules_json: rules,
    post_failure_policy_json: defaultFailure, sample_response_json: '{\n  "allowed": true\n}', last_test_status: '', last_test_latency_ms: 0,
    last_test_error_code: '', has_credential: false, binding_count: 0, secret: ''
  }
}

const form = reactive<WizardForm>(blankForm())
const chosenPreset = computed(() => presets.value.find(item => item.slug === selectedPreset.value))
const starterPolicies = computed(() => chosenPreset.value?.starter_policies || [])
const enabledGuardrails = computed(() => guardrails.value.filter(item => item.enabled).length)
const protectedGroups = computed(() => uniqueBindingCount('routing_lane_id'))
const protectedProviders = computed(() => uniqueBindingCount('provider_id'))
const protectedModels = computed(() => uniqueBindingCount('endpoint_id'))
const recentGuarded = computed(() => recentLogs.value.filter(item => item.guardrail_status && item.guardrail_status !== 'none'))
const blockedRecent = computed(() => recentGuarded.value.filter(item => String(item.guardrail_status).startsWith('blocked_')).length)
const averageLatency = computed(() => {
  if (!recentGuarded.value.length) return '—'
  const total = recentGuarded.value.reduce((sum, item) => sum + Number(item.guardrail_duration_ms || 0), 0)
  return `${Math.round(total / recentGuarded.value.length)} ms`
})
const visualRuleSet = ref<GuardrailRuleSet>(blankRules())
const parsedSampleResponse = computed(() => {
  try { return JSON.parse(sampleResponse.value) }
  catch { return { invalid_json: 'Correct the sample JSON to browse its fields.' } }
})
const matchedPreviewRules = computed(() => (previewResult.value?.rules || []).filter((rule: any) => rule.matched))
const previewMatched = computed(() => Boolean(previewResult.value?.matched))
const previewNextAction = computed(() => String(previewResult.value?.next_action || previewResult.value?.action?.action || 'allow').replaceAll('_', ' '))
function activeTemplate() {
  return mappingStage.value === 'post_response' ? form.post_request_template_json : form.pre_request_template_json
}

function setActiveTemplate(value: string) {
  if (mappingStage.value === 'post_response') form.post_request_template_json = value
  else form.pre_request_template_json = value
}

function activeRules() {
  return mappingStage.value === 'post_response' ? form.post_response_rules_json : form.pre_response_rules_json
}

function setActiveRules(value: string) {
  if (mappingStage.value === 'post_response') form.post_response_rules_json = value
  else form.pre_response_rules_json = value
}

function syncMappingStage() {
  if (mappingStage.value === 'pre_dispatch' && !form.pre_dispatch_enabled && form.post_response_enabled) mappingStage.value = 'post_response'
  if (mappingStage.value === 'post_response' && !form.post_response_enabled && form.pre_dispatch_enabled) mappingStage.value = 'pre_dispatch'
}

function loadVisualRules() {
  syncMappingStage()
  try { visualRuleSet.value = JSON.parse(activeRules()) }
  catch { visualRuleSet.value = blankRules() }
  ruleValueTypes.value = visualRuleSet.value.rules.map(rule => inferRuleValueType(rule.value))
  activeRuleIndex.value = Math.min(activeRuleIndex.value, Math.max(visualRuleSet.value.rules.length - 1, 0))
}

function inferRuleValueType(value: any): RuleValueType {
  if (typeof value === 'boolean') return 'boolean'
  if (typeof value === 'number') return 'number'
  if (value !== null && typeof value === 'object') return 'json'
  return 'string'
}

function changeRuleValueType(index: number, nextType: string) {
  const rule = visualRuleSet.value.rules[index]
  if (!rule) return
  const type = nextType as RuleValueType
  ruleValueTypes.value[index] = type
  if (type === 'boolean') rule.value = true
  else if (type === 'number') rule.value = Number.isFinite(Number(rule.value)) ? Number(rule.value) : 0
  else if (type === 'json') rule.value = Array.isArray(rule.value) || (rule.value && typeof rule.value === 'object') ? rule.value : []
  else rule.value = rule.value == null ? '' : String(rule.value)
  commitVisualRules()
}

function updateJSONRuleValue(index: number, event: Event) {
  const raw = inputValue(event)
  try {
    visualRuleSet.value.rules[index].value = JSON.parse(raw)
    errorMessage.value = ''
    commitVisualRules()
  } catch {
    errorMessage.value = 'Comparison JSON must be a valid JSON array or object.'
  }
}

function displayJSONRuleValue(value: any) {
  try { return JSON.stringify(value, null, 2) }
  catch { return '[]' }
}

function apiErrorMessage(error: any, fallback: string) {
  const nested = error?.data?.error
  if (typeof error?.data?.message === 'string') return error.data.message
  if (typeof nested?.message === 'string') return nested.message
  if (typeof nested === 'string') return nested
  if (typeof error?.statusMessage === 'string' && error.statusMessage) return error.statusMessage
  if (typeof error?.message === 'string' && !/^\[(GET|POST|PUT|DELETE)\]/.test(error.message)) return error.message
  return fallback
}

type GuardrailJSONField = 'request_headers_json' | 'pre_request_template_json' | 'post_request_template_json' | 'pre_response_rules_json' | 'post_response_rules_json' | 'pre_failure_policy_json' | 'post_failure_policy_json'

function formatJSONField(field: GuardrailJSONField) {
  try { form[field] = JSON.stringify(JSON.parse(form[field]), null, 2) }
  catch { /* Validation reports malformed JSON without destroying the draft. */ }
}

function formatSampleResponse() {
  try {
    sampleResponse.value = JSON.stringify(JSON.parse(sampleResponse.value), null, 2)
    form.sample_response_json = sampleResponse.value
  } catch {
    form.sample_response_json = sampleResponse.value
  }
}

function formatRulesField(field: 'pre_response_rules_json' | 'post_response_rules_json') {
  formatJSONField(field)
  loadVisualRules()
}

function commitVisualRules() {
  const next = JSON.parse(JSON.stringify(visualRuleSet.value)) as GuardrailRuleSet
  setActiveRules(JSON.stringify(next, null, 2))
  visualRuleSet.value = next
}

function addVisualRule() {
  visualRuleSet.value.rules.push({ id: `rule-${visualRuleSet.value.rules.length + 1}`, label: 'Policy match', source: 'json_body', path: '$.flagged', operator: 'equals', value: true })
  ruleValueTypes.value.push('boolean')
  activeRuleIndex.value = visualRuleSet.value.rules.length - 1
  commitVisualRules()
}

function removeVisualRule(index: number) {
  if (visualRuleSet.value.rules.length <= 1) return
  visualRuleSet.value.rules.splice(index, 1)
  ruleValueTypes.value.splice(index, 1)
  activeRuleIndex.value = Math.min(activeRuleIndex.value, visualRuleSet.value.rules.length - 1)
  commitVisualRules()
}

function selectSamplePath(path: string) {
  if (!visualRuleSet.value.rules.length) addVisualRule()
  const rule = visualRuleSet.value.rules[activeRuleIndex.value] || visualRuleSet.value.rules[0]
  rule.source = 'json_body'
  rule.path = path
  commitVisualRules()
}

function selectRule(index: number) {
  activeRuleIndex.value = index
}

function updateRuleAction(target: 'on_match' | 'on_no_match', action: string) {
  const value = visualRuleSet.value[target]
  value.action = action as GuardrailRuleSet['on_match']['action']
  if (action === 'block') {
    value.http_status ||= 403
    value.error_type ||= 'content_policy_violation'
    value.error_code ||= 'content_policy_violation'
    value.message ||= 'This request was blocked by the configured policy.'
  }
  commitVisualRules()
}

function insertTemplateVariable() {
  syncMappingStage()
  try {
    const parsed = JSON.parse(activeTemplate())
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('template root must be an object')
    const base = selectedTemplateVariable.value.split('.').pop() || 'value'
    let key = base
    let suffix = 2
    while (Object.prototype.hasOwnProperty.call(parsed, key)) key = `${base}_${suffix++}`
    parsed[key] = `{{${selectedTemplateVariable.value}}}`
    setActiveTemplate(JSON.stringify(parsed, null, 2))
  } catch {
    errorMessage.value = 'The request template must be a JSON object before a variable can be inserted.'
  }
}

function uniqueBindingCount(field: 'routing_lane_id' | 'provider_id' | 'endpoint_id') {
  return new Set(catalogBindings.value.map(item => item[field]).filter(Boolean)).size
}

function replaceForm(value: WizardForm) {
  Object.assign(form, blankForm(), JSON.parse(JSON.stringify(value)))
  form.post_request_template_json = normalizePostTemplateNamespace(form.post_request_template_json)
  for (const field of ['request_headers_json', 'pre_request_template_json', 'post_request_template_json', 'pre_response_rules_json', 'post_response_rules_json', 'pre_failure_policy_json', 'post_failure_policy_json'] as GuardrailJSONField[]) formatJSONField(field)
  const presetSample = presets.value.find(item => item.slug === form.preset_slug)?.sample_response
  sampleResponse.value = form.sample_response_json || presetSample || '{"allowed":true}'
  formatSampleResponse()
  loadCustomHeaders()
}

function normalizePostTemplateNamespace(value: string) {
  return String(value || '').replace(/\{\{\s*response\.(raw_json|text|status_code)\s*\}\}/g, '{{request.$1}}')
}

function loadCustomHeaders() {
  try {
    const parsed = JSON.parse(form.request_headers_json || '{}')
    customHeaders.value = Object.entries(parsed).map(([name, value]) => ({ name, value: String(value) }))
  } catch {
    customHeaders.value = []
  }
  if (!customHeaders.value.length) customHeaders.value.push({ name: '', value: '' })
}

function commitCustomHeaders() {
  const values: Record<string, string> = {}
  for (const header of customHeaders.value) {
    const name = header.name.trim()
    if (name) values[name] = header.value
  }
  form.request_headers_json = JSON.stringify(values, null, 2)
}

function addCustomHeader() {
  customHeaders.value.push({ name: '', value: '' })
}

function removeCustomHeader(index: number) {
  customHeaders.value.splice(index, 1)
  if (!customHeaders.value.length) customHeaders.value.push({ name: '', value: '' })
  commitCustomHeaders()
}

function applyPreset(preset: GuardrailPreset) {
  selectedPreset.value = preset.slug
  selectedStarterPolicy.value = ''
  const fallback = blankForm()
  replaceForm({
    ...fallback,
    preset_slug: preset.ready ? preset.slug : 'custom-http',
    name: preset.ready ? preset.name : `${preset.name} (Custom)`,
    description: preset.description,
    base_url: preset.default_url || '',
    http_method: (preset.http_method || 'POST') as WizardForm['http_method'],
    auth_mode: (preset.auth_mode || 'none') as WizardForm['auth_mode'],
    secret_header_name: preset.secret_header_name || '',
    network_access_mode: preset.slug === 'local-http' ? 'loopback' : 'public_https',
    pre_request_template_json: preset.pre_request_template_json || fallback.pre_request_template_json,
    post_request_template_json: preset.post_request_template_json || fallback.post_request_template_json,
    pre_response_rules_json: JSON.stringify(preset.suggested_response_rules || blankRules(), null, 2),
    post_response_rules_json: JSON.stringify(preset.suggested_response_rules || blankRules(), null, 2),
    sample_response_json: preset.sample_response || '{"allowed":true}'
  })
  mappingStage.value = 'pre_dispatch'
  loadVisualRules()
  step.value = 2
}

function applyStarterPolicy() {
  const policy = starterPolicies.value.find(item => item.slug === selectedStarterPolicy.value)
  if (!policy) return
  setActiveRules(JSON.stringify(policy.response_rules, null, 2))
  loadVisualRules()
  previewResult.value = null
}

function openCreate() {
  replaceForm(blankForm())
  selectedPreset.value = ''
  selectedStarterPolicy.value = ''
  bindings.value = []
  step.value = 1
  errorMessage.value = ''
  previewResult.value = null
  wizardOpen.value = true
}

async function openEdit(item: Guardrail) {
  errorMessage.value = ''
  const response = await api.get<{ guardrail: Guardrail, bindings: GuardrailBinding[] }>(`/api/guardrails/${item.id}`)
  replaceForm({ ...response.guardrail, secret: '' })
  selectedPreset.value = response.guardrail.preset_slug
  selectedStarterPolicy.value = ''
  bindings.value = response.bindings || []
  mappingStage.value = response.guardrail.pre_dispatch_enabled ? 'pre_dispatch' : 'post_response'
  loadVisualRules()
  step.value = 2
  wizardOpen.value = true
}

function duplicate(item: Guardrail) {
  replaceForm({ ...item, id: undefined, name: `${item.name} copy`, enabled: false, credential_id: null, secret: '' })
  bindings.value = []
  selectedPreset.value = item.preset_slug
  selectedStarterPolicy.value = ''
  mappingStage.value = item.pre_dispatch_enabled ? 'pre_dispatch' : 'post_response'
  loadVisualRules()
  step.value = 2
  wizardOpen.value = true
}

async function refresh() {
  loading.value = true
  errorMessage.value = ''
  try {
    const [guardrailRows, presetRows, bindingRows, logPage] = await Promise.all([
      api.get<Guardrail[]>('/api/guardrails'),
      api.get<GuardrailPreset[]>('/api/guardrail-presets'),
      api.get<GuardrailBinding[]>('/api/guardrail-bindings'),
      api.requestLogs({ limit: 100 })
    ])
    guardrails.value = guardrailRows || []
    presets.value = presetRows || []
    recentLogs.value = logPage.items || []
    catalogBindings.value = bindingRows || []
  } catch (error: any) {
    errorMessage.value = apiErrorMessage(error, 'Guardrails could not be loaded.')
  } finally {
    loading.value = false
  }
}

function targetBindings() {
  return bindings.value.map(item => ({
    routing_lane_id: item.routing_lane_id || null,
    provider_id: item.provider_id || null,
    endpoint_id: item.endpoint_id || null,
    enabled: item.enabled !== false
  }))
}

function draftJSONIssue() {
  const fields: Array<{ enabled: boolean, stage: 'pre_dispatch' | 'post_response', step: number, label: string, value: string }> = [
    { enabled: form.pre_dispatch_enabled, stage: 'pre_dispatch', step: 4, label: 'Pre-dispatch request template', value: form.pre_request_template_json },
    { enabled: form.post_response_enabled, stage: 'post_response', step: 4, label: 'Post-response request template', value: form.post_request_template_json },
    { enabled: form.pre_dispatch_enabled, stage: 'pre_dispatch', step: 5, label: 'Pre-dispatch response rules', value: form.pre_response_rules_json },
    { enabled: form.post_response_enabled, stage: 'post_response', step: 5, label: 'Post-response response rules', value: form.post_response_rules_json },
    { enabled: form.pre_dispatch_enabled, stage: 'pre_dispatch', step: 6, label: 'Pre-dispatch failure behavior', value: form.pre_failure_policy_json },
    { enabled: form.post_response_enabled, stage: 'post_response', step: 6, label: 'Post-response failure behavior', value: form.post_failure_policy_json }
  ]
  for (const field of fields) {
    if (!field.enabled || !field.value.trim()) continue
    try { JSON.parse(field.value) }
    catch (error: any) {
      return { ...field, message: `${field.label} is not valid JSON: ${error?.message || 'correct the highlighted JSON'}` }
    }
  }
  return null
}

async function saveDraft() {
  saving.value = true
  errorMessage.value = ''
  formatSampleResponse()
  try { JSON.parse(form.sample_response_json) }
  catch (error: any) {
    step.value = 5
    errorMessage.value = `Sample response is not valid JSON: ${error?.message || 'correct the highlighted JSON'}`
    saving.value = false
    return
  }
  const issue = draftJSONIssue()
  if (issue) {
    mappingStage.value = issue.stage
    step.value = issue.step
    errorMessage.value = issue.message
    saving.value = false
    return
  }
  try {
    commitCustomHeaders()
    const hasExecutionStage = form.pre_dispatch_enabled || form.post_response_enabled
    const payload = { ...form, enabled: Boolean(form.id && form.enabled && hasExecutionStage) }
    const saved = form.id
      ? await api.put<Guardrail>(`/api/guardrails/${form.id}`, payload)
      : await api.post<Guardrail>('/api/guardrails', payload)
    await api.put(`/api/guardrails/${saved.id}/bindings`, { bindings: targetBindings() })
    wizardOpen.value = false
    await refresh()
  } catch (error: any) {
    errorMessage.value = apiErrorMessage(error, 'Guardrail could not be saved.')
  } finally {
    saving.value = false
  }
}

async function removeGuardrail(item: Guardrail) {
  if (!confirm(`Delete ${item.name}? Historical request-log summaries will remain readable.`)) return
  await api.del(`/api/guardrails/${item.id}`)
  await refresh()
}

function guardrailMoreMenuItems(item: Guardrail) {
  return [[
    {
      label: 'Test Guardrail',
      onSelect: () => openTest(item)
    },
    {
      label: 'Duplicate Guardrail',
      onSelect: () => duplicate(item)
    },
    {
      label: 'Delete Guardrail',
      color: 'error' as const,
      onSelect: () => removeGuardrail(item)
    }
  ]]
}

async function importCurl() {
  try {
    const result = await api.post<any>('/api/guardrails/import-curl', { curl: curlInput.value })
    form.http_method = result.http_method || form.http_method
    form.base_url = result.base_url || form.base_url
    form.request_headers_json = JSON.stringify(Object.fromEntries(Object.entries(result.request_headers || {}).filter(([, value]) => value !== '[REDACTED]')), null, 2)
    loadCustomHeaders()
    if (result.request_template) form.pre_request_template_json = JSON.stringify(result.request_template, null, 2)
    errorMessage.value = result.detected_secret_headers?.length ? `Secret header detected and removed: ${result.detected_secret_headers.join(', ')}. Enter it as the authentication key.` : ''
  } catch (error: any) {
    errorMessage.value = apiErrorMessage(error, 'cURL could not be imported.')
  }
}

async function previewRules() {
  errorMessage.value = ''
  previewResult.value = null
  try {
    syncMappingStage()
    formatJSONField(mappingStage.value === 'post_response' ? 'post_request_template_json' : 'pre_request_template_json')
    formatSampleResponse()
    if (form.id || step.value > 1) {
      formatJSONField(mappingStage.value === 'post_response' ? 'post_response_rules_json' : 'pre_response_rules_json')
      loadVisualRules()
      commitVisualRules()
    }
    previewResult.value = await api.post('/api/guardrails/preview', {
      stage: mappingStage.value,
      template_json: activeTemplate(),
      rules_json: activeRules(),
      request_text: 'Example request', response_text: 'Example response', sample_response: JSON.parse(sampleResponse.value), sample_status: 200
    })
  } catch (error: any) {
    errorMessage.value = apiErrorMessage(error, 'The preview could not be evaluated.')
  }
}

async function openTest(item: Guardrail) {
  replaceForm({ ...item, secret: '' })
  testStage.value = item.pre_dispatch_enabled ? 'pre_dispatch' : 'post_response'
  testResult.value = null
  testOpen.value = true
}

async function runTest() {
  if (!form.id) return
  try {
    testResult.value = await api.post(`/api/guardrails/${form.id}/test`, { stage: testStage.value, request_text: testRequestText.value, response_text: testResponseText.value })
    await refresh()
  } catch (error: any) {
    errorMessage.value = apiErrorMessage(error, 'Guardrail test failed.')
  }
}

function bindingSelected(field: 'routing_lane_id' | 'provider_id' | 'endpoint_id', id: string) {
  return bindings.value.some(item => item[field] === id)
}

function toggleBinding(field: 'routing_lane_id' | 'provider_id' | 'endpoint_id', id: string) {
  const index = bindings.value.findIndex(item => item[field] === id)
  if (index >= 0) bindings.value.splice(index, 1)
  else bindings.value.push({ id: `draft-${field}-${id}`, guardrail_id: form.id || '', [field]: id, enabled: true } as GuardrailBinding)
}

function failureMode(item: Guardrail) {
  try {
    const values = [item.pre_dispatch_enabled ? JSON.parse(item.pre_failure_policy_json).mode : '', item.post_response_enabled ? JSON.parse(item.post_failure_policy_json).mode : ''].filter(Boolean)
    return [...new Set(values)].join(' / ').replaceAll('_', ' ')
  } catch { return 'review required' }
}

function parsedFailurePolicy(stage: 'pre_dispatch' | 'post_response') {
  const raw = stage === 'pre_dispatch' ? form.pre_failure_policy_json : form.post_failure_policy_json
  try { return { ...JSON.parse(defaultFailure), ...JSON.parse(raw) } }
  catch { return JSON.parse(defaultFailure) }
}

function updateFailurePolicy(stage: 'pre_dispatch' | 'post_response', field: string, value: any) {
  const policy = parsedFailurePolicy(stage)
  policy[field] = value
  const encoded = JSON.stringify(policy, null, 2)
  if (stage === 'pre_dispatch') form.pre_failure_policy_json = encoded
  else form.post_failure_policy_json = encoded
}

function inputValue(event: Event) {
  return (event.target as HTMLInputElement | HTMLSelectElement)?.value || ''
}

onMounted(async () => {
  await Promise.all([catalog.refreshAll(), refresh()])
})

watch(step, (current) => {
  if (current === 4) syncMappingStage()
  if (current === 5 || current === 6) loadVisualRules()
})

watch(mappingStage, () => {
  if (form.id || step.value > 1) loadVisualRules()
})
</script>

<template>
  <div class="space-y-8">
    <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h1 class="app-title">Guardrails</h1>
        <p class="app-copy-text mt-2 max-w-3xl text-sm leading-6">Call an external safety or policy service before a model request is dispatched and/or before its response is returned.</p>
      </div>
      <UiButton @click="openCreate"><UIcon name="i-lucide-shield-plus" class="mr-2 h-4 w-4" />Add Guardrail</UiButton>
    </div>

    <div v-if="errorMessage && !wizardOpen && !testOpen" class="rounded-2xl border border-rose-400/30 bg-rose-500/10 px-5 py-4 text-sm text-rose-200">{{ errorMessage }}</div>

    <div class="ui-metric-strip">
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Enabled guardrails</p><p class="app-heading-text mt-1 text-xl">{{ enabledGuardrails }}</p><p class="app-copy-text mt-1 text-xs">Explicitly active hooks</p></div>
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Protected groups</p><p class="app-heading-text mt-1 text-xl">{{ protectedGroups }}</p><p class="app-copy-text mt-1 text-xs">Direct group bindings</p></div>
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Protected providers</p><p class="app-heading-text mt-1 text-xl">{{ protectedProviders }}</p><p class="app-copy-text mt-1 text-xs">Direct provider bindings</p></div>
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Protected models</p><p class="app-heading-text mt-1 text-xl">{{ protectedModels }}</p><p class="app-copy-text mt-1 text-xs">Direct model bindings</p></div>
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Checks in recent logs</p><p class="app-heading-text mt-1 text-xl">{{ recentGuarded.length }}</p><p class="app-copy-text mt-1 text-xs">{{ blockedRecent }} blocked</p></div>
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Blocked requests</p><p class="app-heading-text mt-1 text-xl">{{ blockedRecent }}</p><p class="app-copy-text mt-1 text-xs">Across recent request logs</p></div>
      <div class="ui-metric-strip__item"><p class="app-faint-text text-xs">Average latency</p><p class="app-heading-text mt-1 text-xl">{{ averageLatency }}</p><p class="app-copy-text mt-1 text-xs">Across recent logged checks</p></div>
    </div>

    <section class="space-y-4">
      <div><h2 class="app-heading-text text-lg font-semibold">Configured guardrails</h2><p class="app-copy-text mt-1 text-sm">Bindings inherit from groups to providers and models. Duplicate inheritance executes one check and records every source.</p></div>
      <div v-if="loading" class="app-copy-text py-10 text-center text-sm">Loading guardrails…</div>
      <UiEmptyState v-else-if="!guardrails.length" title="No guardrails configured" description="Create a disabled draft, test it deliberately, then enable it when its templates, rules, and failure behavior are ready." />
      <UiTable v-else :columns="['Guardrail', 'Status', 'Stages', 'Bindings', 'Timeout / failure', 'Last test', 'Actions']">
        <tr v-for="item in guardrails" :key="item.id">
          <td><p class="app-heading-text font-semibold">{{ item.name }}</p><p class="app-copy-text mt-1 text-xs">{{ presets.find(preset => preset.slug === item.preset_slug)?.name || 'Custom HTTP' }}</p></td>
          <td><UiBadge :tone="item.enabled ? 'emerald' : 'slate'" size="sm">{{ item.enabled ? 'Enabled' : 'Draft' }}</UiBadge></td>
          <td>
            <div class="flex flex-wrap gap-1.5">
              <UiBadge v-if="item.pre_dispatch_enabled" tone="sky" size="sm">Pre</UiBadge>
              <UiBadge v-if="item.post_response_enabled" tone="sky" size="sm">Post</UiBadge>
              <span v-if="!item.pre_dispatch_enabled && !item.post_response_enabled" class="app-faint-text">—</span>
            </div>
          </td>
          <td class="font-medium text-[var(--app-heading)]">{{ item.binding_count || 0 }}</td>
          <td><p class="app-heading-text">{{ item.timeout_ms }} ms</p><p class="app-copy-text mt-1 text-xs capitalize">{{ failureMode(item) }}</p></td>
          <td><p class="app-heading-text capitalize">{{ item.last_test_status ? item.last_test_status.replaceAll('_', ' ') : 'Not run' }}</p><p v-if="item.last_test_status" class="app-copy-text mt-1 text-xs">{{ item.last_test_latency_ms }} ms</p></td>
          <td>
            <div class="flex flex-wrap gap-2">
              <UiButton tone="secondary" size="xs" @click="openEdit(item)">Edit</UiButton>
              <UDropdownMenu :items="guardrailMoreMenuItems(item)" :content="{ align: 'end', sideOffset: 8, collisionPadding: 12 }">
                <button type="button" class="ui-action-menu-button">
                  <span>More</span>
                  <span class="app-faint-text">⋯</span>
                </button>
              </UDropdownMenu>
            </div>
          </td>
        </tr>
      </UiTable>
    </section>

    <GuardrailsEditorShell
      :open="wizardOpen"
      :footer="Boolean(form.id) || step > 1"
      :title="form.id ? `Edit ${form.name}` : 'Add Guardrail'"
      :subtitle="form.id || step > 1 ? 'Configure the guardrail and its bindings.' : 'Choose a provider template to begin.'"
      @close="wizardOpen = false"
    >
      <div v-if="errorMessage" class="mb-5 rounded-xl border border-rose-400/30 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">{{ errorMessage }}</div>

      <div v-if="!form.id && step === 1" class="grid gap-x-7 md:grid-cols-2">
        <div class="border-b border-[var(--app-border)] py-4 md:col-span-2">
          <h3 class="app-heading-text text-lg font-semibold">Choose provider template</h3>
          <p class="app-copy-text mt-1 text-sm">Select a guardrail provider to prefill its connection, request, and response settings.</p>
        </div>
        <button v-for="preset in presets" :key="preset.slug" type="button" class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed border-b border-[var(--app-border)] px-1 py-5 text-left transition hover:bg-sky-400/[0.04]" @click="applyPreset(preset)">
          <div class="flex items-center justify-between gap-3"><UIcon name="i-lucide-shield" class="h-5 w-5 text-sky-300" /><UiBadge :tone="preset.ready ? 'sky' : 'amber'" size="sm">{{ preset.ready ? 'Ready' : 'Contract required' }}</UiBadge></div>
          <h3 class="app-heading-text mt-3 font-semibold">{{ preset.name }}</h3>
          <p class="app-copy-text mt-2 text-sm leading-6">{{ preset.description }}</p>
          <p v-if="preset.warning_text" class="app-faint-text mt-3 text-xs leading-5">{{ preset.warning_text }}</p>
        </button>
      </div>

      <div v-if="form.id || step > 1" class="guardrail-edit-section space-y-5">
        <div><h3 class="app-heading-text text-lg font-semibold">Connection</h3><p class="app-copy-text mt-1 text-sm">Configure the service endpoint, authentication, and request headers.</p></div>
        <UiSwitch v-if="form.id" v-model="form.enabled" label="Guardrail enabled" description="Disable this guardrail to keep its configuration and bindings without running it on requests." />
        <div class="grid items-start gap-4 md:grid-cols-2">
          <UiField label="Name" required><input v-model="form.name" class="app-input" placeholder="Content safety" /></UiField>
          <UiField label="URL" required><input v-model="form.base_url" class="app-input font-mono text-xs" placeholder="https://guardrail.example/v1/check" /></UiField>
          <UiField label="Method"><select v-model="form.http_method" class="app-select"><option>POST</option><option>PUT</option><option>PATCH</option></select></UiField>
          <UiField label="Timeout (ms)"><input v-model.number="form.timeout_ms" class="app-input" type="number" min="50" max="30000" placeholder="3000" /></UiField>
        </div>
        <section class="border-t border-[var(--app-border)] pt-5">
          <h4 class="app-heading-text text-sm font-semibold">Authentication</h4>
          <div class="mt-3 grid items-start gap-4 md:grid-cols-2">
            <UiField label="Authentication type"><select v-model="form.auth_mode" class="app-select"><option value="none">None</option><option value="bearer">Bearer</option><option value="api_key_header">API key header</option><option value="basic">Basic</option><option value="custom_secret_header">Custom secret header</option></select></UiField>
            <UiField v-if="form.auth_mode !== 'none'" label="Authentication key"><input v-model="form.secret" class="app-input" type="password" autocomplete="new-password" placeholder="Enter a new secret" /><p class="app-copy-text mt-1.5 text-xs">Stored encrypted. {{ form.has_credential ? 'Leave blank to keep the existing key.' : '' }}</p></UiField>
            <UiField v-if="['api_key_header', 'custom_secret_header'].includes(form.auth_mode)" label="Authentication header"><input v-model="form.secret_header_name" class="app-input" placeholder="X-API-Key" /></UiField>
          </div>
        </section>
        <section class="border-t border-[var(--app-border)] pt-5">
          <div class="flex items-center justify-between gap-3"><div><h4 class="app-heading-text text-sm font-semibold">Custom header and value combinations</h4><p class="app-copy-text mt-1 text-xs">Optional header values sent with the guardrail request.</p></div><UiButton tone="secondary" size="xs" @click="addCustomHeader">Add header</UiButton></div>
          <div class="mt-3 space-y-3">
            <div v-for="(header, index) in customHeaders" :key="index" class="grid items-end gap-3 sm:grid-cols-[minmax(0,0.85fr)_minmax(0,1.15fr)_auto]">
              <UiField label="Custom header"><input v-model="header.name" class="app-input font-mono text-sm" placeholder="X-Custom-Header" @change="commitCustomHeaders" /></UiField>
              <UiField label="Value"><input v-model="header.value" class="app-input font-mono text-sm" placeholder="value" @change="commitCustomHeaders" /></UiField>
              <UiButton class="mb-0.5" tone="ghost" size="xs" @click="removeCustomHeader(index)">Remove</UiButton>
            </div>
          </div>
        </section>
        <details class="border-t border-[var(--app-border)] pt-4"><summary class="cursor-pointer app-heading-text text-sm font-semibold">Import a cURL request</summary><textarea v-model="curlInput" class="app-textarea mt-3 min-h-24 resize-y font-mono text-[13px] leading-6" placeholder="curl https://guardrail.example/v1/check …" spellcheck="false" /><UiButton class="mt-3" tone="secondary" size="sm" @click="importCurl">Parse without executing</UiButton></details>
      </div>

      <div v-if="form.id || step > 1" class="guardrail-edit-section grid gap-4 md:grid-cols-2">
        <div class="md:col-span-2"><h3 class="app-heading-text text-lg font-semibold">Stages</h3><p class="app-copy-text mt-1 text-sm">Choose when this guardrail runs.</p></div>
        <UiSwitch v-model="form.pre_dispatch_enabled" label="Pre-dispatch" description="Runs after the final route is selected and before the provider is called." />
        <UiSwitch v-model="form.post_response_enabled" label="Post-response" description="Runs after the provider response is buffered and usage is measured." />
      </div>

      <div v-if="form.id || step > 1" class="guardrail-edit-section space-y-4">
        <div><h3 class="app-heading-text text-lg font-semibold">Request Body</h3><p class="app-copy-text mt-1 text-sm">Choose exactly what Relay sends to the guardrail service.</p></div>
        <p class="app-copy-text text-sm">Only the fields present in these constrained JSON templates are sent. Unknown variables are rejected before enabling.</p>
        <UiField v-if="form.pre_dispatch_enabled && form.post_response_enabled" label="Mapping stage"><select v-model="mappingStage" class="app-select"><option value="pre_dispatch">Pre-dispatch request</option><option value="post_response">Post-response request</option></select></UiField>
        <UiField v-if="mappingStage === 'pre_dispatch' && form.pre_dispatch_enabled" label="Pre-dispatch request JSON"><textarea v-model="form.pre_request_template_json" class="app-textarea min-h-56 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Enter the pre-dispatch JSON template" spellcheck="false" @blur="formatJSONField('pre_request_template_json')" /></UiField>
        <UiField v-if="mappingStage === 'post_response' && form.post_response_enabled" label="Post-response request JSON"><textarea v-model="form.post_request_template_json" class="app-textarea min-h-56 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Enter the post-response JSON template" spellcheck="false" @blur="formatJSONField('post_request_template_json')" /></UiField>
        <div class="border-t border-[var(--app-border)] pt-4">
          <p class="app-heading-text text-sm font-semibold">Variable picker</p>
          <p class="app-copy-text mt-1 text-xs">Insert a request field. For pre-dispatch this is the user's request; for post-response it is the model's response. Whole-value placeholders preserve arrays, objects, numbers, booleans, and null.</p>
          <div class="mt-3 flex flex-col gap-3 sm:flex-row"><select v-model="selectedTemplateVariable" class="app-select flex-1 font-mono text-xs"><option v-for="variable in templateVariables" :key="variable" :value="variable">{{ variable }}</option></select><UiButton tone="secondary" size="sm" @click="insertTemplateVariable">Insert field</UiButton></div>
        </div>
        <UiButton tone="secondary" @click="previewRules">Validate and render preview</UiButton>
        <UiJsonViewer v-if="previewResult" :value="previewResult.rendered_request_body || previewResult" />
      </div>

      <div v-if="form.id || step > 1" class="guardrail-edit-section space-y-5">
        <div><h3 class="app-heading-text text-lg font-semibold">Response Rules</h3><p class="app-copy-text mt-1 text-sm">Map the service response to policy rules.</p></div>
        <UiField v-if="form.pre_dispatch_enabled && form.post_response_enabled" label="Rule stage"><select v-model="mappingStage" class="app-select"><option value="pre_dispatch">Pre-dispatch response</option><option value="post_response">Post-response response</option></select></UiField>
        <div v-if="!form.id && starterPolicies.length" class="border-b border-[var(--app-border)] pb-5">
          <p class="app-heading-text text-sm font-semibold">Optional starter policy</p>
          <p class="app-copy-text mt-1 text-xs">Apply a reviewed starting rule, then verify its threshold and action.</p>
          <div class="mt-3 flex flex-col gap-3 sm:flex-row"><select v-model="selectedStarterPolicy" class="app-select flex-1"><option value="">Choose a reviewed template…</option><option v-for="policy in starterPolicies" :key="policy.slug" :value="policy.slug">{{ policy.name }} — {{ policy.description }}</option></select><UiButton tone="secondary" size="sm" :disabled="!selectedStarterPolicy" @click="applyStarterPolicy">Apply</UiButton></div>
        </div>
        <UiField label="Sample JSON response" help="Saved with this guardrail and used to configure and preview its response rules."><textarea v-model="sampleResponse" class="app-textarea min-h-52 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Paste a sample JSON response" spellcheck="false" @blur="formatSampleResponse" /></UiField>
        <div class="grid border-y border-[var(--app-border)] lg:grid-cols-[minmax(0,0.8fr)_minmax(0,1.35fr)]">
          <div class="py-5 lg:border-r lg:border-[var(--app-border)] lg:pr-6"><p class="app-heading-text text-sm font-semibold">Response fields</p><p class="app-copy-text mt-1 text-xs">Click a value to use its JSON path.</p><div class="mt-3 max-h-96 overflow-auto pr-2"><GuardrailsJsonPathTree :value="parsedSampleResponse" @select="selectSamplePath" /></div></div>
          <div class="py-1 lg:pl-6">
            <div v-for="(rule, index) in visualRuleSet.rules" :key="`${rule.id}-${index}`" class="cursor-pointer border-b border-[var(--app-border)] py-5 pl-3 transition last:border-b-0" :class="activeRuleIndex === index ? 'border-l-2 border-l-sky-400 bg-sky-400/[0.035]' : 'border-l-2 border-l-transparent'" @click="selectRule(index)">
              <div class="flex items-center justify-between gap-3"><div class="flex items-center gap-2"><p class="app-heading-text text-sm font-semibold">Rule {{ index + 1 }}</p><span v-if="activeRuleIndex === index" class="text-[10px] font-semibold uppercase tracking-wider text-sky-300">Selected</span></div><UiButton tone="ghost" size="xs" :disabled="visualRuleSet.rules.length <= 1" @click.stop="removeVisualRule(index)">Remove</UiButton></div>
              <div class="mt-3 grid items-start gap-3 sm:grid-cols-2">
                <UiField label="Source"><select v-model="rule.source" class="app-select" @change="commitVisualRules"><option value="json_body">JSON body</option><option value="http_status">HTTP status</option><option value="response_header">Response header</option></select></UiField>
                <UiField label="Operator"><select v-model="rule.operator" class="app-select" @change="commitVisualRules"><option v-for="operator in ['exists','not_exists','equals','not_equals','truthy','falsy','greater_than','greater_than_or_equal','less_than','less_than_or_equal','contains','not_contains','in','not_in','matches_regex']" :key="operator" :value="operator">{{ operator.replaceAll('_', ' ') }}</option></select></UiField>
                <UiField v-if="rule.source === 'json_body'" label="JSON path"><input v-model="rule.path" class="app-input font-mono text-xs" placeholder="$.result.allowed" @change="commitVisualRules" /></UiField>
                <UiField v-if="rule.source === 'response_header'" label="Header"><input v-model="rule.header" class="app-input font-mono text-xs" placeholder="X-Guardrail-Result" @change="commitVisualRules" /></UiField>
                <template v-if="!['exists','not_exists','truthy','falsy'].includes(rule.operator)">
                  <UiField label="Value type"><select :value="ruleValueTypes[index] || inferRuleValueType(rule.value)" class="app-select" @change="changeRuleValueType(index, inputValue($event))"><option value="string">Text</option><option value="number">Number</option><option value="boolean">Boolean</option><option value="json">JSON array or object</option></select></UiField>
                  <UiField label="Comparison value">
                    <select v-if="ruleValueTypes[index] === 'boolean'" v-model="rule.value" class="app-select" @change="commitVisualRules"><option :value="true">True</option><option :value="false">False</option></select>
                    <input v-else-if="ruleValueTypes[index] === 'number'" v-model.number="rule.value" class="app-input font-mono text-xs" type="number" step="any" placeholder="0.8" @change="commitVisualRules" />
                    <textarea v-else-if="ruleValueTypes[index] === 'json'" :value="displayJSONRuleValue(rule.value)" class="app-textarea min-h-24 resize-y whitespace-pre font-mono text-xs" placeholder="Enter a JSON value" spellcheck="false" @change="updateJSONRuleValue(index, $event)" />
                    <input v-else v-model="rule.value" class="app-input font-mono text-xs" placeholder="Comparison value" @change="commitVisualRules" />
                  </UiField>
                </template>
              </div>
            </div>
            <UiButton class="my-4" tone="secondary" size="sm" @click="addVisualRule">Add rule</UiButton>
          </div>
        </div>
        <div class="grid gap-4 sm:grid-cols-2"><UiField label="Match mode"><select v-model="visualRuleSet.match_mode" class="app-select" @change="commitVisualRules"><option value="any">Any rule</option><option value="all">All rules</option></select></UiField><UiField label="Missing path"><select v-model="visualRuleSet.missing_path" class="app-select" @change="commitVisualRules"><option value="error">Treat as evaluation error</option><option value="no_match">Treat as no match</option></select></UiField></div>
        <details class="border-t border-[var(--app-border)] pt-4">
          <summary class="cursor-pointer app-heading-text text-sm font-semibold">Advanced rules JSON</summary>
          <textarea v-if="mappingStage === 'pre_dispatch'" v-model="form.pre_response_rules_json" class="app-textarea mt-3 min-h-72 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Enter advanced pre-dispatch rules JSON" spellcheck="false" @blur="formatRulesField('pre_response_rules_json')" />
          <textarea v-else v-model="form.post_response_rules_json" class="app-textarea mt-3 min-h-72 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Enter advanced post-response rules JSON" spellcheck="false" @blur="formatRulesField('post_response_rules_json')" />
        </details>
        <UiButton tone="secondary" @click="previewRules">Render and evaluate sample</UiButton>
        <div v-if="previewResult" class="border-y border-[var(--app-border)] py-4">
          <div class="flex flex-wrap items-center gap-2"><UiBadge :tone="previewMatched ? 'amber' : 'emerald'" size="sm">{{ previewMatched ? 'Match' : 'No match' }}</UiBadge><span class="app-heading-text text-sm font-semibold">Next action: {{ previewNextAction }}</span></div>
          <p class="app-copy-text mt-2 text-sm">{{ matchedPreviewRules.length ? `${matchedPreviewRules.length} matching ${matchedPreviewRules.length === 1 ? 'rule' : 'rules'}: ${matchedPreviewRules.map((rule: any) => rule.label || rule.id).join(', ')}` : 'No response rules matched the sample.' }}</p>
        </div>
      </div>

      <div v-if="form.id || step > 1" class="guardrail-edit-section space-y-5">
        <div><h3 class="app-heading-text text-lg font-semibold">Actions</h3><p class="app-copy-text mt-1 text-sm">Set match behavior and service-failure handling.</p></div>
        <p class="app-copy-text text-sm">Choose what Relay does when the configured rules match. Replacement is available only for a post-response hook.</p>
        <div class="grid gap-6 lg:grid-cols-2">
          <section class="border-t border-[var(--app-border)] pt-4">
            <h4 class="app-heading-text text-sm font-semibold">When rules match</h4>
            <div class="mt-3 space-y-4">
              <UiField label="Action"><select :value="visualRuleSet.on_match.action" class="app-select" @change="updateRuleAction('on_match', inputValue($event))"><option value="allow">Allow</option><option value="log_only">Log only</option><option value="block">Block</option><option v-if="mappingStage === 'post_response'" value="replace_response">Replace response</option></select></UiField>
              <div v-if="visualRuleSet.on_match.action === 'block'" class="grid items-start gap-3 sm:grid-cols-2">
                <UiField label="Client HTTP status"><input v-model.number="visualRuleSet.on_match.http_status" class="app-input" type="number" min="400" max="599" placeholder="403" @change="commitVisualRules" /></UiField>
                <UiField label="Public message"><input v-model="visualRuleSet.on_match.message" class="app-input" placeholder="Request blocked by policy" @change="commitVisualRules" /></UiField>
              </div>
              <div v-if="visualRuleSet.on_match.action === 'replace_response'" class="grid items-start gap-3 sm:grid-cols-2">
                <UiField label="Replacement JSON path"><input v-model="visualRuleSet.on_match.replacement_path" class="app-input font-mono text-xs" placeholder="$.revised_response" @change="commitVisualRules" /></UiField>
                <UiField label="Static replacement"><input v-model="visualRuleSet.on_match.replacement_text" class="app-input" placeholder="Safe replacement response" @change="commitVisualRules" /></UiField>
              </div>
            </div>
          </section>
          <section class="border-t border-[var(--app-border)] pt-4">
            <h4 class="app-heading-text text-sm font-semibold">When rules do not match</h4>
            <div class="mt-3 space-y-4">
              <UiField label="Action"><select :value="visualRuleSet.on_no_match.action" class="app-select" @change="updateRuleAction('on_no_match', inputValue($event))"><option value="allow">Allow</option><option value="log_only">Log only</option><option value="block">Block</option></select></UiField>
              <div v-if="visualRuleSet.on_no_match.action === 'block'" class="grid items-start gap-3 sm:grid-cols-2">
                <UiField label="Client HTTP status"><input v-model.number="visualRuleSet.on_no_match.http_status" class="app-input" type="number" min="400" max="599" placeholder="403" @change="commitVisualRules" /></UiField>
                <UiField label="Public message"><input v-model="visualRuleSet.on_no_match.message" class="app-input" placeholder="Request blocked by policy" @change="commitVisualRules" /></UiField>
              </div>
            </div>
          </section>
        </div>
        <section v-if="form.pre_dispatch_enabled" class="border-t border-[var(--app-border)] pt-5">
          <p class="app-heading-text text-sm font-semibold">If the pre-dispatch guardrail service is unavailable</p>
          <p class="app-copy-text mt-1 text-xs">Applied when the service times out, cannot connect, or returns an invalid response.</p>
          <div class="mt-3 grid items-start gap-4 sm:grid-cols-2">
            <UiField label="Behavior"><select :value="parsedFailurePolicy('pre_dispatch').mode" class="app-select" @change="updateFailurePolicy('pre_dispatch', 'mode', inputValue($event))"><option value="fail_open">Fail open</option><option value="fail_closed">Fail closed</option><option value="return_error">Return configured error</option></select></UiField>
            <UiField label="HTTP status"><input :value="parsedFailurePolicy('pre_dispatch').http_status" class="app-input" type="number" min="400" max="599" placeholder="503" @change="updateFailurePolicy('pre_dispatch', 'http_status', Number(inputValue($event)))" /></UiField>
            <UiField class="sm:col-span-2" label="Public message"><input :value="parsedFailurePolicy('pre_dispatch').message" class="app-input" placeholder="Guardrail service unavailable" @change="updateFailurePolicy('pre_dispatch', 'message', inputValue($event))" /></UiField>
          </div>
          <details class="mt-4"><summary class="cursor-pointer app-copy-text text-xs">Advanced JSON</summary><textarea v-model="form.pre_failure_policy_json" class="app-textarea mt-3 min-h-44 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Enter the pre-dispatch failure policy JSON" spellcheck="false" @blur="formatJSONField('pre_failure_policy_json')" /></details>
        </section>
        <section v-if="form.post_response_enabled" class="border-t border-[var(--app-border)] pt-5">
          <p class="app-heading-text text-sm font-semibold">If the post-response guardrail service is unavailable</p>
          <p class="app-copy-text mt-1 text-xs">Applied when the service times out, cannot connect, or returns an invalid response.</p>
          <div class="mt-3 grid items-start gap-4 sm:grid-cols-2">
            <UiField label="Behavior"><select :value="parsedFailurePolicy('post_response').mode" class="app-select" @change="updateFailurePolicy('post_response', 'mode', inputValue($event))"><option value="fail_open">Fail open</option><option value="fail_closed">Fail closed</option><option value="return_error">Return configured error</option></select></UiField>
            <UiField label="HTTP status"><input :value="parsedFailurePolicy('post_response').http_status" class="app-input" type="number" min="400" max="599" placeholder="503" @change="updateFailurePolicy('post_response', 'http_status', Number(inputValue($event)))" /></UiField>
            <UiField class="sm:col-span-2" label="Public message"><input :value="parsedFailurePolicy('post_response').message" class="app-input" placeholder="Guardrail service unavailable" @change="updateFailurePolicy('post_response', 'message', inputValue($event))" /></UiField>
          </div>
          <details class="mt-4"><summary class="cursor-pointer app-copy-text text-xs">Advanced JSON</summary><textarea v-model="form.post_failure_policy_json" class="app-textarea mt-3 min-h-44 resize-y whitespace-pre font-mono text-[13px] leading-6 [tab-size:2]" placeholder="Enter the post-response failure policy JSON" spellcheck="false" @blur="formatJSONField('post_failure_policy_json')" /></details>
        </section>
        <p class="app-copy-text border-t border-[var(--app-border)] pt-4 text-xs leading-5">Fail open continues and records the failure. Fail closed blocks with a safe response. Return error returns a distinct configured error. Calls are never retried automatically.</p>
      </div>

      <div v-if="form.id || step > 1" class="guardrail-edit-section grid gap-5 lg:grid-cols-3">
        <div class="lg:col-span-3"><h3 class="app-heading-text text-lg font-semibold">Bindings</h3><p class="app-copy-text mt-1 text-sm">Attach this guardrail to groups, providers, and models.</p></div>
        <div><h3 class="app-heading-text mb-2 text-sm font-semibold">Groups</h3><div class="border-t border-[var(--app-border)]"><label v-for="lane in catalog.lanes" :key="lane.id" class="has-disabled:cursor-not-allowed flex cursor-pointer items-center gap-3 border-b border-[var(--app-border)] px-1 py-3 text-sm font-medium text-[var(--app-heading)] transition hover:bg-sky-400/[0.05]"><input class="app-checkbox" type="checkbox" :checked="bindingSelected('routing_lane_id', lane.id)" @change="toggleBinding('routing_lane_id', lane.id)" /><span>{{ lane.name }}</span></label></div></div>
        <div><h3 class="app-heading-text mb-2 text-sm font-semibold">Providers</h3><div class="border-t border-[var(--app-border)]"><label v-for="provider in catalog.providers" :key="provider.id" class="has-disabled:cursor-not-allowed flex cursor-pointer items-center gap-3 border-b border-[var(--app-border)] px-1 py-3 text-sm font-medium text-[var(--app-heading)] transition hover:bg-sky-400/[0.05]"><input class="app-checkbox" type="checkbox" :checked="bindingSelected('provider_id', provider.id)" @change="toggleBinding('provider_id', provider.id)" /><span>{{ provider.name }}</span></label></div></div>
        <div><h3 class="app-heading-text mb-2 text-sm font-semibold">Models</h3><div class="max-h-96 overflow-y-auto border-t border-[var(--app-border)] pr-1"><label v-for="endpoint in catalog.endpoints" :key="endpoint.id" class="has-disabled:cursor-not-allowed flex cursor-pointer items-center gap-3 border-b border-[var(--app-border)] px-1 py-3 text-sm font-medium text-[var(--app-heading)] transition hover:bg-sky-400/[0.05]"><input class="app-checkbox" type="checkbox" :checked="bindingSelected('endpoint_id', endpoint.id)" @change="toggleBinding('endpoint_id', endpoint.id)" /><span>{{ endpoint.name }}<span class="app-copy-text mt-0.5 block text-xs">{{ catalog.providerMap[endpoint.provider_id]?.name }}</span></span></label></div></div>
      </div>

      <template #footer>
        <div class="flex justify-end">
          <UiButton :disabled="saving || !form.name || !form.base_url" @click="saveDraft">{{ saving ? 'Saving…' : form.id ? 'Save changes' : 'Add guardrail' }}</UiButton>
        </div>
      </template>
    </GuardrailsEditorShell>

    <UiModal :open="testOpen" :title="`Test ${form.name}`" subtitle="This explicit admin action calls only the configured guardrail service. It does not call an LLM provider or write request logs." @close="testOpen = false">
      <div class="space-y-5"><UiField label="Stage"><select v-model="testStage" class="app-select"><option v-if="form.pre_dispatch_enabled" value="pre_dispatch">Pre-dispatch</option><option v-if="form.post_response_enabled" value="post_response">Post-response</option></select></UiField><UiField label="Test request text"><textarea v-model="testRequestText" class="app-textarea min-h-24" placeholder="Enter a request to evaluate" /></UiField><UiField v-if="testStage === 'post_response'" label="Test response text"><textarea v-model="testResponseText" class="app-textarea min-h-24" placeholder="Enter a response to evaluate" /></UiField><UiButton @click="runTest">Run deliberate test</UiButton><UiJsonViewer v-if="testResult" :value="testResult" /></div>
    </UiModal>
  </div>
</template>

<style scoped>
.guardrail-edit-section {
  padding-top: 1.5rem;
  padding-bottom: 1.5rem;
  border-bottom: 1px solid var(--app-border-soft);
}

.guardrail-edit-section:first-of-type {
  padding-top: 0;
}

.guardrail-edit-section:last-of-type {
  padding-bottom: 0;
  border-bottom: 0;
}
</style>
