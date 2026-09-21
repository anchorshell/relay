<script setup lang="ts">
import { buildInferenceCurl, inferenceExampleURL, inferenceHeaders, INFERENCE_AUTH_HELP } from '../utils/inference'
import { highlightCurl } from '../utils/curlHighlight'

const runtimeConfig = useRuntimeConfig()
const apiToken = useInferenceToken()
const catalog = useCatalogStore()
const pageData = useAdminPageDataStore()
const {
  targetGroups,
  targetOptions,
  targetOptionByValue,
  modelTitle
} = useRoutingTargetOptions()
const targetValue = ref('')
const loading = ref(false)
const result = ref<any>(null)
const meta = ref<Record<string, string>>({})
const runtimeOrigin = ref('')
const copiedCurl = ref(false)
let copiedCurlTimer: ReturnType<typeof setTimeout> | null = null
const { previewResponse } = useRelayUi()
const { currencyMicros } = useFormatters()
const { modelRelayApiPath } = useModelRelayRoute()

const form = reactive({
  body: ''
})

const selectedTarget = computed(() => targetOptionByValue.value[targetValue.value] || null)
const selectedTargetKind = computed(() => selectedTarget.value?.kind || '')
const selectedLaneID = computed(() => selectedTargetKind.value === 'group' ? selectedTarget.value?.laneID || '' : '')
const selectedEndpointID = computed(() => selectedTargetKind.value === 'model' ? selectedTarget.value?.endpointID || '' : '')
const selectedLane = computed(() => selectedLaneID.value ? catalog.laneMap[selectedLaneID.value] || null : null)
const selectedEndpoint = computed(() => selectedEndpointID.value ? catalog.endpointMap[selectedEndpointID.value] || null : null)
const selectedEndpointProvider = computed(() => selectedEndpoint.value ? catalog.providerMap[selectedEndpoint.value.provider_id] || null : null)
const selectedModelName = computed(() => selectedEndpoint.value ? modelTitle(selectedEndpoint.value) : selectedTarget.value?.label || '')
const selectedRequestModel = computed(() => {
  if (selectedTargetKind.value === 'group') return selectedLane.value?.name || selectedTarget.value?.label || ''
  if (selectedTargetKind.value === 'model' && selectedEndpoint.value) {
    return `${selectedEndpointProvider.value?.name || 'Provider missing'}/${selectedModelName.value}`
  }
  return selectedTarget.value?.label || ''
})
const selectedTargetText = computed(() => selectedTargetKind.value === 'group'
  ? selectedRequestModel.value.toLowerCase()
  : [
      selectedTarget.value?.label,
      selectedTarget.value?.providerName,
      selectedTarget.value?.searchText,
      selectedEndpoint.value?.name,
      selectedEndpoint.value?.upstream_model,
      selectedEndpointProvider.value?.name,
      selectedEndpointProvider.value?.slug
    ].filter(Boolean).join(' ').toLowerCase()
)
const selectedPrompt = computed(() => selectedTargetText.value.includes('dummy')
  ? 'wait_5'
  : 'Explain why queue-first routing preserves better models.'
)
const responsePreview = computed(() => previewResponse('chat', result.value))
const usage = computed(() => result.value?.usage || {})
const selectedProviderName = computed(() => meta.value.providerName || 'n/a')
const selectedTargetLabel = computed(() => selectedTargetKind.value === 'group'
  ? (selectedLane.value?.name || selectedTarget.value?.label || 'n/a')
  : selectedEndpoint.value
    ? `${selectedEndpointProvider.value?.name || 'Provider missing'} / ${selectedModelName.value}`
    : selectedTarget.value?.label || 'n/a'
)

function buildRequestBody() {
  return JSON.stringify({
    model: selectedRequestModel.value || 'thinking',
    messages: [{ role: 'user', content: selectedPrompt.value }]
  }, null, 2)
}

const requestURL = computed(() => modelRelayApiPath('/v1/chat/completions'))
const curlURL = computed(() => inferenceExampleURL(requestURL.value, runtimeOrigin.value, String(runtimeConfig.public.relayWsTarget || '')))
const curlEquivalent = computed(() => buildInferenceCurl(form.body || buildRequestBody(), curlURL.value, apiToken.value))
const curlSyntaxLines = computed(() => highlightCurl(curlEquivalent.value))

async function copyCurl() {
  if (!import.meta.client || !navigator?.clipboard) return
  await navigator.clipboard.writeText(curlEquivalent.value)
  copiedCurl.value = true
  if (copiedCurlTimer) clearTimeout(copiedCurlTimer)
  copiedCurlTimer = setTimeout(() => {
    copiedCurl.value = false
  }, 1200)
}

watch(targetOptions, () => {
  if (!targetOptions.value.some(option => option.value === targetValue.value)) {
    targetValue.value = targetOptions.value.find(option => option.kind === 'group')?.value || targetOptions.value[0]?.value || ''
  }
}, { immediate: true })

watch([selectedRequestModel, selectedPrompt], () => {
  form.body = buildRequestBody()
}, { immediate: true })

async function send() {
  if (loading.value) return
  loading.value = true
  result.value = null
  meta.value = {}

  try {
    const headers = inferenceHeaders(apiToken.value)
    const response = await fetch(modelRelayApiPath('/v1/chat/completions'), {
      method: 'POST',
      credentials: 'include',
      headers,
      body: form.body
    })

    meta.value = {
      endpoint: response.headers.get('X-Relay-Selected-Endpoint') || 'n/a',
      endpointId: response.headers.get('X-Relay-Selected-Endpoint-Id') || 'n/a',
      providerId: response.headers.get('X-Relay-Selected-Provider-Id') || 'n/a',
      wait: response.headers.get('X-Relay-Wait-Ms') || '0',
      fallback: response.headers.get('X-Relay-Fallback-Count') || '0',
      estimatedCost: response.headers.get('X-Relay-Estimated-Cost-Micros') || '0',
      status: String(response.status)
    }

    const text = await response.text()
    const contentType = response.headers.get('content-type') || ''

    if (response.status === 401) {
      result.value = { error: INFERENCE_AUTH_HELP }
      return
    }

    if (contentType.includes('text/event-stream') || text.trimStart().startsWith('data:')) {
      const preview = text
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice(5).trim())
        .filter((line) => line && line !== '[DONE]')
        .map((line) => {
          try {
            const parsed = JSON.parse(line)
            return parsed.choices?.[0]?.delta?.content ||
              parsed.choices?.[0]?.message?.content ||
              parsed.output_text ||
              ''
          } catch {
            return line
          }
        })
        .join('')

      result.value = { choices: [{ message: { content: preview || 'Streaming response completed.' } }] }
    } else {
      try {
        result.value = text ? JSON.parse(text) : { message: 'Empty response body' }
      } catch {
        result.value = response.ok
          ? { raw_text: text, choices: [{ message: { content: text || 'Plain-text response received.' } }] }
          : { raw_text: text, error: text || 'Request failed' }
      }
    }

    const providerId = String(meta.value.providerId || '')
    meta.value.providerName = providerId ? (catalog.providerMap[providerId]?.name || 'Provider missing') : 'n/a'
  } catch (error: any) {
    result.value = { error: error?.message || 'Request failed' }
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  runtimeOrigin.value = window.location.origin
  pageData.load('playground')
})
</script>

<template>
  <div class="space-y-8">
    <div>
      <h1 class="app-title">Playground</h1>
    </div>

    <section class="space-y-4">
      <h2 class="app-heading-text text-lg font-semibold">Request</h2>
      <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(22rem,0.86fr)]">
        <div class="space-y-6">
          <div class="grid gap-5">
            <UiField label="Target" help="Search routing groups and provider/model targets." required>
              <RoutingTargetSelect
                v-model="targetValue"
                :items="targetGroups"
                :loading="catalog.loading"
                align="start"
              />
            </UiField>
          </div>

          <div class="grid gap-2.5">
            <div class="grid gap-1">
              <span class="app-heading-text text-sm font-medium">Request body</span>
              <p class="app-copy-text max-w-2xl text-sm leading-6">Send the same payload shape your real client would send.</p>
            </div>
            <textarea v-model="form.body" rows="14" class="app-textarea font-mono text-sm" placeholder="Enter the JSON request body" />
          </div>

          <InferenceTokenField
            v-model="apiToken"
            help="Kept only in memory until you leave this page. Included in the curl command when entered—keep copied commands private."
          />

          <div class="flex justify-end">
            <UiButton class="w-full sm:w-auto" :disabled="loading" @click="send">{{ loading ? 'Sending Request...' : 'Send Request' }}</UiButton>
          </div>
        </div>

        <div class="flex min-h-full flex-col border-t border-[var(--app-border)] pt-5 xl:border-l xl:border-t-0 xl:pl-6 xl:pt-0">
          <div class="flex items-center justify-between gap-3">
            <p class="text-xs font-semibold uppercase tracking-[0.2em] text-slate-400">cURL</p>
            <UiButton tone="ghost" size="sm" @click="copyCurl">{{ copiedCurl ? 'Copied' : 'Copy' }}</UiButton>
          </div>
          <pre class="playground-curl-preview mt-4 min-h-[18rem] flex-1 overflow-hidden whitespace-pre-wrap break-words rounded-[1rem] border p-4 text-xs leading-6 shadow-inner"><code><span v-for="(line, lineIndex) in curlSyntaxLines" :key="lineIndex" class="block min-h-6"><span v-for="(token, tokenIndex) in line" :key="`${lineIndex}-${tokenIndex}`" :class="`playground-curl-token--${token.tone}`">{{ token.text }}</span></span></code></pre>
        </div>
      </div>
    </section>

    <section class="space-y-4">
      <h2 class="app-heading-text text-lg font-semibold">Result</h2>
      <div v-if="!result" class="text-sm leading-6 text-slate-300/72">
        Send a request to see the selected model, wait time, usage, and returned content here.
      </div>

      <div v-else class="space-y-5">
        <div class="rounded-[1.2rem] border px-4 py-4 sm:px-5 sm:py-5" :class="result.error ? 'border-rose-500/25 bg-rose-500/10' : 'border-emerald-400/20 bg-emerald-400/10'">
          <p class="text-xs uppercase tracking-[0.22em]" :class="result.error ? 'text-rose-300' : 'text-emerald-300'">
            {{ result.error ? 'Needs attention' : 'Response received' }}
          </p>
          <p class="mt-3 text-lg font-semibold leading-8 text-white">{{ responsePreview }}</p>
        </div>

        <div class="ui-metric-strip grid-cols-2 xl:grid-cols-6">
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Selected model</p>
            <p class="mt-2 text-sm font-medium text-white">{{ meta.endpoint || 'n/a' }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Provider</p>
            <p class="mt-2 text-sm font-medium text-white">{{ selectedProviderName }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Target</p>
            <p class="mt-2 text-sm font-medium text-white">{{ selectedTargetLabel }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Wait time</p>
            <p class="mt-2 text-sm font-medium text-white">{{ meta.wait || '0' }}ms</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Fallback count</p>
            <p class="mt-2 text-sm font-medium text-white">{{ meta.fallback || '0' }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Estimated cost</p>
            <p class="mt-2 text-sm font-medium text-white">{{ currencyMicros(Number(meta.estimatedCost || 0)) }}</p>
          </div>
        </div>

        <div class="ui-metric-strip grid-cols-2 xl:grid-cols-4">
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Prompt tokens</p>
            <p class="mt-2 text-sm font-medium text-white">{{ usage.prompt_tokens ?? usage.input_tokens ?? 'n/a' }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Completion tokens</p>
            <p class="mt-2 text-sm font-medium text-white">{{ usage.completion_tokens ?? usage.output_tokens ?? 'n/a' }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">Total tokens</p>
            <p class="mt-2 text-sm font-medium text-white">{{ usage.total_tokens ?? 'n/a' }}</p>
          </div>
          <div class="app-metric">
            <p class="text-xs uppercase tracking-[0.2em] text-slate-400">HTTP status</p>
            <p class="mt-2 text-sm font-medium text-white">{{ meta.status || 'n/a' }}</p>
          </div>
        </div>

        <details class="border-y border-[var(--app-border)] py-4">
          <summary class="cursor-pointer list-none">
            <div class="flex items-center justify-between gap-4">
              <span class="text-sm font-semibold text-white">Show raw response</span>
              <span class="text-xs uppercase tracking-[0.2em] text-slate-500">Debug</span>
            </div>
          </summary>
          <div class="mt-4">
            <UiJsonViewer :value="result" />
          </div>
        </details>
      </div>
    </section>
  </div>
</template>

<style scoped>
.playground-curl-preview {
  border-color: #3c4650;
  background: #202428;
  color: #d8dee9 !important;
}

.playground-curl-preview code {
  color: inherit;
  overflow-wrap: anywhere;
  white-space: inherit;
}

.playground-curl-token--command {
  color: #39a8ff;
  font-weight: 700;
}

.playground-curl-token--flag,
.playground-curl-token--key {
  color: #c4b5fd;
}

.playground-curl-token--method {
  color: #fbbf24;
}

.playground-curl-token--string {
  color: #86efac;
}

.playground-curl-token--variable {
  color: #f9a8d4;
}

.playground-curl-token--operator {
  color: #94a3b8;
}

:global(:root[data-site-theme='light']) .playground-curl-preview {
  border-color: #45515c;
  background: #202428;
  color: #d8dee9 !important;
}
</style>
