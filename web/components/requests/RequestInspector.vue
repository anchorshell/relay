<script setup lang="ts">
import type { RequestCharacterization, RequestLog } from '~/types/admin'
import { requestTimingBreakdown } from '../../utils/requestTiming'

const props = defineProps<{ request: RequestLog | null }>()
const { durationMs, currencyMicros, dateTime, number } = useFormatters()
const { explainCandidate, parsedCandidateTrace, parsedLimitImpact } = useRelayUi()

const candidateTrace = computed(() => parsedCandidateTrace(props.request))
const limitImpact = computed(() => parsedLimitImpact(props.request))
const catalog = useCatalogStore()

const characterization = computed<RequestCharacterization | null>(() => {
  if (!props.request?.characterization_json) return null
  try {
    return JSON.parse(props.request.characterization_json) as RequestCharacterization
  } catch {
    return null
  }
})
const timings = computed(() => requestTimingBreakdown(props.request))

const overrides = computed(() => {
  if (!props.request?.applied_overrides_json) return {}
  try {
    return JSON.parse(props.request.applied_overrides_json)
  } catch {
    return {}
  }
})

const providerName = computed(() => props.request?.provider_id ? catalog.providerMap[props.request.provider_id]?.name : '')
const modelName = computed(() => props.request?.endpoint_id ? catalog.endpointMap[props.request.endpoint_id]?.name : '')

const guardrailResults = computed<any[]>(() => {
  if (!props.request?.guardrail_results_json) return []
  try {
    const parsed = JSON.parse(props.request.guardrail_results_json)
    return Array.isArray(parsed) ? parsed : []
  } catch { return [] }
})

const guardrailResponses = computed<any[]>(() => {
  if (!props.request?.guardrail_response_bodies_json) return []
  try {
    const parsed = JSON.parse(props.request.guardrail_response_bodies_json)
    return Array.isArray(parsed) ? parsed : []
  } catch { return [] }
})

function guardrailResponse(result: any) {
  return guardrailResponses.value.find(response =>
    response.guardrail_uuid === result.guardrail_uuid && response.stage === result.stage
  )
}

function responseFieldName(path: string) {
  return String(path || '').replace(/^results\[\d+\]\.categories\./, '').replaceAll('_', ' ')
}

const hasActualUsage = computed(() =>
  !!props.request && (
    props.request.actual_total_tokens > 0 ||
    props.request.actual_input_tokens > 0 ||
    props.request.actual_output_tokens > 0
  )
)

function resolvedTokens(kind: 'input' | 'output') {
  if (!props.request) return 0
  if (hasActualUsage.value) {
    return kind === 'input' ? props.request.actual_input_tokens : props.request.actual_output_tokens
  }
  return kind === 'input' ? props.request.estimated_input_tokens : props.request.estimated_output_tokens
}

function tokenSourceLabel() {
  return hasActualUsage.value ? 'Actual usage reported by upstream' : 'Approximate fallback because upstream usage was absent'
}
</script>

<template>
  <div v-if="request" class="request-inspector space-y-3">
    <UiPanel compact tone="subsurface" title="Summary">
      <div class="grid gap-3 md:grid-cols-2 text-sm">
        <div>
          <div class="app-kv">
            <dt>Request ID</dt>
            <dd class="font-mono text-xs">{{ request.request_id }}</dd>
          </div>
          <div class="app-kv">
            <dt>Route kind</dt>
            <dd>{{ request.route_kind }}</dd>
          </div>
          <div class="app-kv">
            <dt>Status</dt>
            <dd>{{ request.task_state }}</dd>
          </div>
          <div class="app-kv border-b-0 pb-0">
            <dt>Incoming model</dt>
            <dd>{{ request.incoming_model || 'n/a' }}</dd>
          </div>
        </div>
        <div>
          <div class="app-kv">
            <dt>Selected model</dt>
            <dd>{{ modelName || request.selected_upstream_model || 'n/a' }}</dd>
          </div>
          <div class="app-kv">
            <dt>Provider</dt>
            <dd>{{ providerName || 'n/a' }}</dd>
          </div>
          <div class="app-kv">
            <dt>Streaming</dt>
            <dd>{{ request.streaming ? 'Yes' : 'No' }}</dd>
          </div>
          <div class="app-kv border-b-0 pb-0">
            <dt>Fallbacks</dt>
            <dd>{{ request.fallback_count }}</dd>
          </div>
        </div>
      </div>
    </UiPanel>

    <UiPanel compact tone="subsurface" title="Characterization" subtitle="Observe-only request metadata; it does not affect routing, limits, or billing.">
      <RequestsCharacterizationPills v-if="characterization" :characterization="characterization" :primary-action="characterization.primary_action" />
      <UiEmptyState v-else title="No characterization" description="This request predates characterization, the feature was disabled, or no result was stored." />
      <slot v-if="characterization" name="characterization-actions" :request="request" />
    </UiPanel>

    <UiPanel compact tone="subsurface" title="Guardrails" subtitle="Execution metadata and retained third-party responses. Responses are stored only when payload retention is enabled.">
      <div v-if="request.guardrail_status && request.guardrail_status !== 'none'" class="space-y-3">
        <div><UiBadge :tone="request.guardrail_status === 'passed' ? 'emerald' : 'rose'" size="sm">{{ request.guardrail_status.replaceAll('_', ' ') }}</UiBadge></div>
        <div v-for="(result, index) in guardrailResults" :key="`${result.guardrail_uuid}-${result.stage}-${index}`" class="app-subsurface p-4">
          <div class="flex flex-wrap items-start justify-between gap-3"><div><p class="app-heading-text font-semibold">{{ result.guardrail_name || 'Deleted guardrail' }}</p><p class="app-faint-text mt-1 text-xs">{{ result.preset || 'custom' }} · {{ (result.binding_sources || []).join(', ') || 'binding unavailable' }}</p></div><UiBadge :tone="result.decision === 'allow' ? 'emerald' : result.decision === 'log_only' ? 'amber' : 'rose'" size="sm">{{ String(result.decision || 'unknown').replaceAll('_', ' ') }}</UiBadge></div>
          <dl class="mt-2 grid gap-x-3 text-sm sm:grid-cols-2"><div class="app-kv"><dt>Stage</dt><dd>{{ String(result.stage || '').replaceAll('_', ' ') }}</dd></div><div class="app-kv"><dt>Third-party status</dt><dd>{{ result.http_status || '—' }}</dd></div><div class="app-kv"><dt>Failure policy</dt><dd>{{ String(result.fail_mode || '—').replaceAll('_', ' ') }}</dd></div><div v-if="result.error_code" class="app-kv"><dt>Error code</dt><dd>{{ result.error_code }}</dd></div><div v-if="result.matched_rule_labels?.length" class="app-kv"><dt>Matched rules</dt><dd>{{ result.matched_rule_labels.join(', ') }}</dd></div></dl>
          <div v-if="result.matched_rules?.length" class="mt-3 border-t border-[var(--app-border-soft)] pt-3">
            <p class="app-faint-text text-xs uppercase tracking-wider">Matched rules</p>
            <div class="mt-2 space-y-2"><div v-for="rule in result.matched_rules" :key="rule.id" class="flex flex-wrap items-center justify-between gap-2 text-sm"><span class="app-copy-text">{{ rule.label || rule.id }}</span><code class="app-heading-text text-xs">{{ rule.path || rule.source }} = {{ JSON.stringify(rule.actual) }}</code></div></div>
          </div>
          <div v-if="guardrailResponse(result)?.true_fields?.length" class="mt-3 border-t border-[var(--app-border-soft)] pt-3">
            <p class="app-faint-text text-xs uppercase tracking-wider">True response fields</p>
            <div class="mt-2 flex flex-wrap gap-2"><UiBadge v-for="field in guardrailResponse(result).true_fields" :key="field.path" tone="rose" size="sm">{{ responseFieldName(field.path) }} · true</UiBadge></div>
          </div>
          <details v-if="guardrailResponse(result)" class="mt-3 border-t border-[var(--app-border-soft)] pt-3">
            <summary class="cursor-pointer app-heading-text text-sm font-medium">Raw guardrail response</summary>
            <div class="mt-3"><UiJsonViewer :value="guardrailResponse(result).body" /></div>
          </details>
        </div>
        <p class="app-faint-text text-xs">Provider dispatch: {{ request.guardrail_status === 'blocked_pre' ? 'No' : 'Yes' }} · Provider usage incurred: {{ request.guardrail_status === 'blocked_pre' ? 'No' : (hasActualUsage ? 'Yes' : 'Not reported') }} · Client response replaced: {{ request.guardrail_status === 'replaced_post' || request.guardrail_status === 'blocked_post' ? 'Yes' : 'No' }}</p>
      </div>
      <UiEmptyState v-else title="No guardrail applied" description="No effective enabled guardrail was attached to the selected group, provider, or model." />
    </UiPanel>

    <UiPanel compact tone="subsurface" title="Timing">
      <RequestsRequestTimingList :timings="timings" size="inspector" />
    </UiPanel>

    <UiPanel compact tone="subsurface" title="Candidate evaluation" subtitle="This is the routing story: which models were considered, how long they would have waited, and why one model won.">
      <div class="space-y-3">
        <div v-for="candidate in candidateTrace" :key="`${candidate.endpoint_id}-${candidate.rank}`" class="app-subsurface p-4">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="font-medium text-white">{{ candidate.endpoint_name }}</p>
              <p class="mt-1 text-sm text-slate-300/72">{{ candidate.upstream_model }} · rank #{{ candidate.rank }}</p>
            </div>
            <UiBadge :tone="candidate.decision === 'selected' ? 'emerald' : candidate.decision === 'rejected' ? 'amber' : 'slate'">
              {{ candidate.decision.replaceAll('_', ' ') }}
            </UiBadge>
          </div>
          <p class="mt-3 text-sm leading-6 text-slate-200/78">{{ explainCandidate(candidate) }}</p>
          <p class="mt-2 text-xs text-slate-400">Predicted wait {{ durationMs(candidate.predicted_wait_ms) }}</p>
        </div>
      </div>
    </UiPanel>

    <UiPanel compact tone="subsurface" title="Limits impact" subtitle="Only the provider and model scopes are shown here so the inheritance story matches what operators actually configured.">
      <div v-if="limitImpact.length" class="space-y-3">
        <div v-for="item in limitImpact" :key="`${item.scope_type}-${item.scope_id}-${item.metric}-${item.period}`" class="app-subsurface p-4">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="font-medium text-white">{{ item.metric }} / {{ item.period }}</p>
              <p class="mt-1 text-sm text-slate-300/72">{{ item.scopeName }}</p>
            </div>
            <div class="text-right text-sm leading-6">
              <p class="text-slate-200/78">Configured {{ item.configured ?? '—' }} · Observed {{ item.observed ?? '—' }}</p>
              <p class="font-medium text-sky-300">Effective {{ item.effective ?? '—' }}</p>
              <p v-if="typeof item.used === 'number'" class="text-slate-300/72">Used {{ item.used }}<template v-if="item.reserved"> · Reserved {{ item.reserved }}</template></p>
              <p v-if="item.next_available_at" class="text-slate-400">Next available {{ dateTime(item.next_available_at) }}</p>
            </div>
          </div>
        </div>
      </div>
      <UiEmptyState v-else title="No limit detail stored" description="This request did not persist provider/model limit detail, or it predated the current routing trace format." />
    </UiPanel>

    <UiPanel compact tone="subsurface" title="Usage and cost">
      <div class="grid gap-3 md:grid-cols-2 text-sm">
        <div>
          <div class="app-kv">
            <dt>Input tokens</dt>
            <dd>{{ hasActualUsage ? number(resolvedTokens('input')) : `~${number(resolvedTokens('input'))}` }}</dd>
          </div>
          <div class="app-kv">
            <dt>Output tokens</dt>
            <dd>{{ hasActualUsage ? number(resolvedTokens('output')) : `~${number(resolvedTokens('output'))}` }}</dd>
          </div>
          <div class="app-kv">
            <dt>Usage source</dt>
            <dd>{{ tokenSourceLabel() }}</dd>
          </div>
          <div class="app-kv border-b-0 pb-0">
            <dt>Raw values</dt>
            <dd>
              est in {{ number(request.estimated_input_tokens) }} · est out {{ number(request.estimated_output_tokens) }}
              <span class="text-slate-500">|</span>
              act in {{ number(request.actual_input_tokens) }} · act out {{ number(request.actual_output_tokens) }}
            </dd>
          </div>
        </div>
        <div>
          <div class="app-kv">
            <dt>Estimated cost</dt>
            <dd>{{ currencyMicros(request.estimated_cost_micros) }}</dd>
          </div>
          <div class="app-kv">
            <dt>Actual cost</dt>
            <dd>{{ currencyMicros(request.actual_cost_micros) }}</dd>
          </div>
          <div class="app-kv border-b-0 pb-0">
            <dt>Variance</dt>
            <dd>{{ currencyMicros(request.actual_cost_micros - request.estimated_cost_micros) }}</dd>
          </div>
        </div>
      </div>
    </UiPanel>

    <details class="ui-drawer-section">
      <summary class="cursor-pointer list-none">
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold text-white">Applied overrides</span>
          <span class="text-xs uppercase tracking-[0.2em] text-slate-500">Debug</span>
        </div>
      </summary>
      <div class="mt-4">
        <UiJsonViewer :value="overrides" />
      </div>
    </details>

    <details class="ui-drawer-section">
      <summary class="cursor-pointer list-none">
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold text-white">Raw request log</span>
          <span class="text-xs uppercase tracking-[0.2em] text-slate-500">Debug</span>
        </div>
      </summary>
      <div class="mt-4">
        <UiJsonViewer :value="request" />
      </div>
    </details>
  </div>
</template>

<style scoped>
.request-inspector :deep(.app-kv) {
  padding-top: 0.6rem;
  padding-bottom: 0.6rem;
  border-color: var(--app-border-soft);
}
</style>
