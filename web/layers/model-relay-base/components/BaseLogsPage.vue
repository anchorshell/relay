<script setup lang="ts">
import type { RequestCharacterization, RequestLog } from '~/types/admin'
import { explicitProviderModelTarget } from '../../../utils/requestTarget'
import { requestTimingBreakdown } from '../../../utils/requestTiming'

const props = withDefaults(defineProps<{
  requestLogUserUUIDs?: string[]
  characterizationEnabled?: boolean
}>(), {
  requestLogUserUUIDs: () => [],
  characterizationEnabled: true
})

const requests = useRequestsStore()
const catalog = useCatalogStore()
const settings = useSettingsStore()
const pageData = useAdminPageDataStore()
const inspector = computed(() => requests.selected)
const storedRequest = ref<RequestLog | null>(null)
const storedRequestLoading = ref('')
const logsTableShell = ref<HTMLElement | null>(null)
const paginationBounds = reactive({ left: 0, width: 0 })
const { durationMs, currencyMicros, number } = useFormatters()
const slots = useSlots()
const logColumns = computed(() => [...(slots.requester ? ['Requester'] : []), 'Time', 'Result', 'Characterization', 'Guardrails', 'Route', 'Tokens', 'Timing', 'Cost', 'Actions'])

const actionOptions = ['conversation', 'answer', 'explain', 'retrieve', 'research', 'extract', 'classify', 'summarize', 'transform', 'translate', 'analyze', 'compare', 'evaluate', 'verify', 'recommend', 'decide', 'plan', 'organize', 'create', 'modify', 'diagnose', 'test', 'communicate', 'schedule', 'execute', 'monitor', 'transact', 'orchestrate', 'remember', 'unknown'].sort((left, right) => left.localeCompare(right))
const domainOptions = ['general', 'software', 'infrastructure', 'security', 'data_analytics', 'math', 'science', 'research', 'business_strategy', 'product', 'marketing', 'sales', 'customer_support', 'operations', 'finance_accounting', 'legal_compliance', 'healthcare', 'education', 'creative_media', 'travel', 'commerce', 'personal_productivity', 'human_resources', 'government_policy', 'communications'].sort((left, right) => left.localeCompare(right))
const selectedAction = computed({ get: () => requests.primaryAction, set: value => { requests.primaryAction = String(value || '') } })
const selectedDomain = computed({ get: () => requests.domain, set: value => { requests.domain = String(value || '') } })
const selectedGuardrailStatus = computed({ get: () => requests.guardrailStatus, set: value => { requests.guardrailStatus = String(value || '') } })
const guardrailStatusOptions = ['blocked_post', 'blocked_pre', 'error', 'fail_open', 'passed', 'replaced_post']
const actionFilterItems = computed(() => [
  { label: 'All actions', value: '' },
  ...actionOptions.map(value => ({ label: friendlyLabel(value), value }))
])
const domainFilterItems = computed(() => [
  { label: 'All domains', value: '' },
  ...domainOptions.map(value => ({ label: friendlyLabel(value), value }))
])
const guardrailFilterItems = computed(() => [
  { label: 'All guardrails', value: '' },
  ...guardrailStatusOptions.map(value => ({ label: friendlyLabel(value), value }))
])
const logFilterSelectUI = {
  content: 'z-[80] max-h-[min(28rem,var(--reka-combobox-content-available-height,28rem))]'
}
const requestTimings = computed(() => new Map(requests.logs.map(log => [log.request_id, requestTimingBreakdown(log)])))

const perPageOptions = [25, 50, 100]
const storingRequests = computed(() => Boolean(settings.parsed.store_requests))
const pageStart = computed(() => requests.total > 0 ? requests.offset + 1 : 0)
const pageEnd = computed(() => Math.min(requests.offset + requests.logs.length, requests.total))
const currentPage = computed(() => requests.limit > 0 ? Math.floor(requests.offset / requests.limit) + 1 : 1)
const totalPages = computed(() => Math.max(1, Math.ceil(requests.total / Math.max(1, requests.limit))))
const requestLogUserUUIDs = computed(() => [...new Set(props.requestLogUserUUIDs
  .map(value => String(value || '').trim().toLowerCase())
  .filter(Boolean))])
const paginationShellStyle = computed(() => paginationBounds.width > 0
  ? {
      left: `${paginationBounds.left}px`,
      width: `${paginationBounds.width}px`
    }
  : undefined
)
const pageNumbers = computed(() => {
  const total = totalPages.value
  const current = currentPage.value
  const visiblePages = 5
  const halfWindow = Math.floor(visiblePages / 2)
  const start = Math.max(1, Math.min(current - halfWindow, total - visiblePages + 1))
  const end = Math.min(total, start + visiblePages - 1)

  return Array.from({ length: end - start + 1 }, (_, index) => start + index)
})

let paginationResizeObserver: ResizeObserver | null = null
let paginationMeasureFrame = 0

function measurePaginationBounds() {
  if (!process.client) return
  if (paginationMeasureFrame) {
    window.cancelAnimationFrame(paginationMeasureFrame)
  }
  paginationMeasureFrame = window.requestAnimationFrame(() => {
    paginationMeasureFrame = 0
    const element = logsTableShell.value
    if (!element) return
    const rect = element.getBoundingClientRect()
    const sideInset = rect.width >= 640 ? 16 : 8
    paginationBounds.left = Math.max(12, rect.left + sideInset)
    paginationBounds.width = Math.max(0, rect.width - (sideInset * 2))
  })
}

function statusCodeTone(statusCode?: number | null): 'slate' | 'status-amber' | 'status-rose' | 'status-emerald' {
  if (!statusCode) return 'slate'
  if (statusCode === 429) return 'status-amber'
  if (statusCode >= 500) return 'status-rose'
  if (statusCode >= 400) return 'status-rose'
  if (statusCode >= 200 && statusCode < 300) return 'status-emerald'
  return 'slate'
}

function statusCodeLabel(statusCode?: number | null) {
  if (!statusCode) return '—'
  return String(statusCode)
}

function logTimestamp(value?: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  }).format(date)
}

function successfulResponse(log: Pick<RequestLog, 'task_state' | 'status_code'>) {
  return log.task_state === 'completed' && log.status_code >= 200 && log.status_code < 300
}

function hasActualUsage(log: {
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
}) {
  return log.actual_total_tokens > 0 || log.actual_input_tokens > 0 || log.actual_output_tokens > 0
}

function tokenValue(log: {
  task_state: string
  status_code: number
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  estimated_input_tokens: number
  estimated_output_tokens: number
}, kind: 'input' | 'output') {
  if (hasActualUsage(log)) {
    return kind === 'input' ? log.actual_input_tokens : log.actual_output_tokens
  }
  if (!successfulResponse(log)) return 0
  return kind === 'input' ? log.estimated_input_tokens : log.estimated_output_tokens
}

function tokenLabel(log: {
  task_state: string
  status_code: number
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  estimated_input_tokens: number
  estimated_output_tokens: number
}, kind: 'input' | 'output') {
  const value = tokenValue(log, kind)
  if (hasActualUsage(log)) return number(value)
  if (!value) return '—'
  return `~${number(value)}`
}

function resultLabel(log: Pick<RequestLog, 'task_state' | 'status_code'>) {
  if (log.task_state === 'cancelled') return 'Cancelled'
  if (log.status_code === 429) return 'Rate limited'
  if (log.status_code === 401) return 'Unauthorized'
  if (log.status_code === 403) return 'Forbidden'
  if (log.status_code >= 500) return 'Failed'
  if (log.status_code >= 400) return 'Rejected'
  return log.task_state.replaceAll('_', ' ')
}

function resultTone(log: Pick<RequestLog, 'task_state' | 'status_code'>): 'slate' | 'emerald' | 'amber' | 'rose' | 'sky' {
  if (log.status_code === 429) return 'amber'
  if (log.status_code >= 400 || log.task_state === 'failed') return 'rose'
  if (log.task_state === 'completed') return 'emerald'
  if (log.task_state === 'cancelled') return 'slate'
  return 'sky'
}

function guardrailLabel(log: RequestLog) {
  return String(log.guardrail_status || '').replaceAll('_', ' ')
}

function guardrailTone(log: RequestLog): 'slate' | 'emerald' | 'amber' | 'rose' | 'sky' {
  const status = String(log.guardrail_status || '')
  if (status === 'passed') return 'emerald'
  if (status === 'fail_open') return 'amber'
  if (status.startsWith('blocked') || status === 'error') return 'rose'
  if (status === 'replaced_post') return 'sky'
  return 'slate'
}

function costLabel(log: RequestLog) {
  const cost = log.actual_cost_micros || (successfulResponse(log) ? log.estimated_cost_micros : 0)
  return cost > 0 ? currencyMicros(cost) : '—'
}

function routeSlug(value?: string | null) {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'provider missing' || normalized === 'no upstream selected' || normalized === 'no model selected') return ''
  return normalized
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function logRoutingGroup(log: Pick<RequestLog, 'lane_id' | 'incoming_model'>) {
  if (log.lane_id && catalog.laneMap[log.lane_id]) return catalog.laneMap[log.lane_id].name
  const target = String(log.incoming_model || '').trim().toLowerCase()
  if (!target) return ''
  return catalog.lanes.find(lane => lane.name.toLowerCase() === target || lane.slug === target)?.name || ''
}

function logRouteTarget(log: Pick<RequestLog, 'provider_id' | 'endpoint_id' | 'incoming_model' | 'selected_upstream_model' | 'status_code'>) {
  const endpoint = log.endpoint_id ? catalog.endpointMap[log.endpoint_id] : null
  const provider = catalog.providerMap[log.provider_id || endpoint?.provider_id || '']
  const requested = explicitProviderModelTarget(log.incoming_model)
  const providerSlug = routeSlug(provider?.slug || requested?.provider)
  const modelSlug = routeSlug(endpoint?.slug || requested?.model || log.selected_upstream_model)
  if (log.status_code === 429 && !providerSlug && !modelSlug) return ''
  return `${providerSlug || 'provider-missing'}/${modelSlug || 'no-model-selected'}`
}

function formatStoredPayload(value?: string | null) {
  if (!value) return ''
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

function parsedCharacterization(log: RequestLog): RequestCharacterization | null {
  if (!log.characterization_json) return null
  try {
    return JSON.parse(log.characterization_json) as RequestCharacterization
  } catch {
    return null
  }
}

function friendlyLabel(value?: string | null) {
  if (!value) return 'Unknown'
  return value.replaceAll('_', ' ').replace(/\b\w/g, letter => letter.toUpperCase())
}

function logTiming(log: RequestLog) {
  return requestTimings.value.get(log.request_id) || requestTimingBreakdown(log)
}

async function changeCharacterizationFilters() {
  await requests.refresh({ limit: requests.limit, offset: 0, primaryAction: selectedAction.value, domain: selectedDomain.value, guardrailStatus: selectedGuardrailStatus.value })
}

async function viewStoredRequest(requestId: string, payloadAvailable: boolean) {
  if (!storingRequests.value || !payloadAvailable) return
  storedRequestLoading.value = requestId
  requests.clearSelection()
  try {
    storedRequest.value = await useRelayApi().requestDetail(requestId)
  } finally {
    storedRequestLoading.value = ''
  }
}

function closeStoredRequest() {
  storedRequest.value = null
}

async function inspectRequest(requestId: string) {
  closeStoredRequest()
  await requests.inspect(requestId)
}

function pageOffset(page: number) {
  return Math.max(0, (page - 1) * requests.limit)
}

async function goToPage(page: number) {
  if (page < 1 || page > totalPages.value || page === currentPage.value) return
  await requests.refresh({ limit: requests.limit, offset: pageOffset(page) })
}

async function changePerPage(event: Event) {
  const value = Number((event.target as HTMLSelectElement).value)
  if (!perPageOptions.includes(value)) return
  await requests.refresh({ limit: value, offset: 0 })
}

watch(() => requestLogUserUUIDs.value.join(','), async () => {
  await requests.firstPage(requestLogUserUUIDs.value)
  await nextTick()
  measurePaginationBounds()
})

onMounted(async () => {
  await pageData.load('requests')
  await requests.firstPage(requestLogUserUUIDs.value)
  await nextTick()
  measurePaginationBounds()
  window.addEventListener('resize', measurePaginationBounds, { passive: true })
  if (typeof ResizeObserver !== 'undefined' && logsTableShell.value) {
    paginationResizeObserver = new ResizeObserver(() => measurePaginationBounds())
    paginationResizeObserver.observe(logsTableShell.value)
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', measurePaginationBounds)
  paginationResizeObserver?.disconnect()
  paginationResizeObserver = null
  if (paginationMeasureFrame) {
    window.cancelAnimationFrame(paginationMeasureFrame)
    paginationMeasureFrame = 0
  }
})
</script>

<template>
  <div class="space-y-8 pb-28">
    <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <h1 class="app-title">Logs</h1>
      <div class="flex w-full flex-wrap items-end justify-end gap-3 sm:w-auto">
        <div class="flex min-w-36 flex-col gap-1 text-xs text-[var(--app-muted)]">
          <span>Action</span>
          <USelectMenu v-model="selectedAction" :items="actionFilterItems" value-key="value" label-key="label" placeholder="All actions" :search-input="{ placeholder: 'Search actions...', icon: 'i-lucide-search' }" :disabled="requests.loading" class="w-full" :ui="logFilterSelectUI" @update:model-value="changeCharacterizationFilters" />
        </div>
        <div class="flex min-w-36 flex-col gap-1 text-xs text-[var(--app-muted)]">
          <span>Domain</span>
          <USelectMenu v-model="selectedDomain" :items="domainFilterItems" value-key="value" label-key="label" placeholder="All domains" :search-input="{ placeholder: 'Search domains...', icon: 'i-lucide-search' }" :disabled="requests.loading" class="w-full" :ui="logFilterSelectUI" @update:model-value="changeCharacterizationFilters" />
        </div>
        <div class="flex min-w-36 flex-col gap-1 text-xs text-[var(--app-muted)]">
          <span>Guardrails</span>
          <USelectMenu v-model="selectedGuardrailStatus" :items="guardrailFilterItems" value-key="value" label-key="label" placeholder="All guardrails" :search-input="{ placeholder: 'Search statuses...', icon: 'i-lucide-search' }" :disabled="requests.loading" class="w-full" :ui="logFilterSelectUI" @update:model-value="changeCharacterizationFilters" />
        </div>
        <div v-if="$slots['filters-extra']">
          <slot name="filters-extra" :loading="requests.loading" />
        </div>
      </div>
    </div>

    <UiEmptyState
      v-if="!requests.logs.length"
      title="No logs yet"
      :description="requestLogUserUUIDs.length ? 'No logs match the selected users.' : 'Gateway traffic will appear here.'"
    />

    <div v-else ref="logsTableShell" class="request-logs-table-shell space-y-4">
      <UiTable :columns="logColumns">
        <tr v-for="log in requests.logs" :key="log.request_id">
          <td v-if="$slots.requester" class="request-logs-requester-cell"><slot name="requester" :log="log" /></td>
          <td class="request-logs-time-cell"><time :datetime="log.created_at">{{ logTimestamp(log.created_at) }}</time></td>
          <td class="request-logs-result-cell">
            <UiBadge :tone="resultTone(log)" size="sm">{{ resultLabel(log) }}</UiBadge>
            <UiBadge :tone="statusCodeTone(log.status_code)" size="sm">{{ statusCodeLabel(log.status_code) }}</UiBadge>
          </td>
          <td class="request-logs-characterization-cell">
            <RequestsCharacterizationPills
              class="max-w-65"
              :characterization="parsedCharacterization(log)"
              :primary-action="log.primary_action"
              :pending="props.characterizationEnabled && log.task_state === 'in_flight'"
            />
          </td>
          <td class="request-logs-guardrail-cell">
            <div v-if="log.guardrail_status && log.guardrail_status !== 'none'" class="flex items-start">
              <UiBadge :tone="guardrailTone(log)" size="sm"><UIcon name="i-lucide-shield-check" class="h-3 w-3" />{{ guardrailLabel(log) }}</UiBadge>
            </div>
          </td>
          <td class="request-logs-route-cell">
            <p v-if="logRoutingGroup(log)" class="request-logs-route-group">{{ logRoutingGroup(log) }}</p>
            <p v-if="logRouteTarget(log)" class="request-logs-route-target">{{ logRouteTarget(log) }}</p>
          </td>
          <td class="request-logs-token-cell">
            <span title="Input tokens">↑ {{ tokenLabel(log, 'input') }}</span>
            <span title="Output tokens">↓ {{ tokenLabel(log, 'output') }}</span>
          </td>
          <td class="request-logs-timing-cell">
            <RequestsRequestTimingList :timings="logTiming(log)" />
          </td>
          <td class="request-logs-cost-cell">{{ costLabel(log) }}</td>
          <td class="request-logs-actions-cell">
            <div class="flex flex-nowrap items-center gap-2 whitespace-nowrap">
              <UiButton :icon="false" tone="secondary" size="xs" @click="inspectRequest(log.request_id)">Inspect</UiButton>
              <UiButton
                v-if="storingRequests"
                :icon="false"
                :tone="log.request_bodies_stored ? 'secondary' : 'ghost'"
                size="xs"
                :disabled="storedRequestLoading === log.request_id || !log.request_bodies_stored"
                :title="log.request_bodies_stored ? 'View stored request and response bodies' : 'No request or response bodies were stored for this log entry'"
                @click="viewStoredRequest(log.request_id, Boolean(log.request_bodies_stored))"
              >
                {{ storedRequestLoading === log.request_id ? '…' : 'Payload' }}
              </UiButton>
            </div>
          </td>
        </tr>
      </UiTable>

      <div class="request-logs-pagination-shell" :style="paginationShellStyle">
        <div class="request-logs-pagination-bar">
          <div class="flex flex-wrap items-center gap-3">
            <p class="request-logs-pagination-summary">
              Showing {{ number(pageStart) }}-{{ number(pageEnd) }} of {{ number(requests.total) }}
            </p>
            <label class="request-logs-per-page-label">
              Per page
              <select class="app-select h-9 w-24 py-1.5 text-sm" :value="requests.limit" :disabled="requests.loading" @change="changePerPage">
                <option v-for="option in perPageOptions" :key="option" :value="option">
                  {{ option }}
                </option>
              </select>
            </label>
          </div>

          <div class="flex flex-wrap items-center gap-1">
            <UiButton :icon="false" tone="secondary" size="xs" :disabled="requests.loading || currentPage <= 1" @click="goToPage(1)">
              « First
            </UiButton>
            <UiButton :icon="false" tone="secondary" size="xs" :disabled="requests.loading || requests.offset <= 0" @click="requests.previousPage()">
              ‹ Prev
            </UiButton>
            <UiButton
              v-for="page in pageNumbers"
              :key="page"
              :icon="false"
              :tone="page === currentPage ? 'primary' : 'secondary'"
              size="xs"
              :disabled="requests.loading"
              @click="goToPage(page)"
            >
              {{ page }}
            </UiButton>
            <UiButton :icon="false" tone="secondary" size="xs" :disabled="requests.loading || !requests.hasMore" @click="requests.nextPage()">
              {{ requests.loading ? 'Loading…' : 'Next ›' }}
            </UiButton>
            <UiButton :icon="false" tone="secondary" size="xs" :disabled="requests.loading || currentPage >= totalPages" @click="goToPage(totalPages)">
              Last »
            </UiButton>
          </div>
        </div>
      </div>
    </div>

    <UiInspectorDrawer :open="!!inspector" :title="inspector?.incoming_model || 'Request inspector'" :subtitle="inspector?.request_id" @close="requests.clearSelection()">
      <RequestsRequestInspector :request="inspector">
        <template v-if="$slots['characterization-actions']" #characterization-actions="slotProps">
          <slot name="characterization-actions" v-bind="slotProps" />
        </template>
      </RequestsRequestInspector>
    </UiInspectorDrawer>

    <UiInspectorDrawer
      :open="!!storedRequest"
      title="Stored Request"
      :subtitle="storedRequest?.request_id"
      @close="closeStoredRequest()"
    >
      <div v-if="storedRequest" class="space-y-5">
        <section class="border-b border-[var(--app-border)] pb-5">
          <h3 class="app-heading-text mb-3 font-semibold">Client Request</h3>
          <pre class="app-code whitespace-pre-wrap">{{ formatStoredPayload(storedRequest.request_body_json) || 'No stored client request body for this log entry.' }}</pre>
        </section>
        <section class="border-b border-[var(--app-border)] pb-5">
          <h3 class="app-heading-text mb-3 font-semibold">Upstream Request</h3>
          <pre class="app-code whitespace-pre-wrap">{{ formatStoredPayload(storedRequest.upstream_request_json) || 'No stored upstream request body for this log entry.' }}</pre>
        </section>
        <section>
          <h3 class="app-heading-text mb-3 font-semibold">Downstream Response</h3>
          <pre class="app-code whitespace-pre-wrap">{{ formatStoredPayload(storedRequest.response_body_json) || 'No stored downstream response body for this log entry.' }}</pre>
        </section>
      </div>
    </UiInspectorDrawer>
  </div>
</template>

<style scoped>
.request-logs-table-shell :deep(td) {
  vertical-align: middle;
}

.request-logs-requester-cell {
  width: 12rem;
  min-width: 10rem;
}

.request-logs-time-cell,
.request-logs-cost-cell {
  color: var(--app-copy);
  white-space: nowrap;
}

.request-logs-time-cell {
  width: 8.5rem;
  font-size: 0.75rem;
}

.request-logs-result-cell,
.request-logs-token-cell,
.request-logs-timing-cell {
  white-space: nowrap;
}

.request-logs-result-cell :deep(.ui-badge + .ui-badge) {
  margin-left: 0.3rem;
}

.request-logs-route-cell {
  min-width: 10rem;
  max-width: 15rem;
}

.request-logs-route-cell p {
  overflow-wrap: anywhere;
}

.request-logs-route-group {
  color: var(--app-heading);
  font-size: 0.78rem;
  font-weight: 650;
}

.request-logs-route-target {
  margin-top: 0.15rem;
  color: var(--app-muted);
  font-size: 0.68rem;
}

.request-logs-token-cell {
  color: var(--app-copy);
  font-size: 0.72rem;
}

.request-logs-token-cell span {
  display: block;
  line-height: 1rem;
}

.request-logs-token-cell span:last-child {
  color: var(--app-muted);
}

.request-logs-timing-cell {
  min-width: 10.5rem;
}

.request-logs-cost-cell {
  font-size: 0.75rem;
}

.request-logs-actions-cell :deep(button) {
  padding-right: 0.65rem;
  padding-left: 0.65rem;
}

.request-logs-pagination-shell {
  position: fixed;
  right: auto;
  bottom: max(0.75rem, env(safe-area-inset-bottom));
  left: 1.25rem;
  z-index: 40;
  width: calc(100vw - 2.5rem);
  pointer-events: none;
}

.request-logs-pagination-bar {
  display: flex;
  flex-direction: column;
  gap: 0.55rem;
  width: 100%;
  border: 1px solid var(--app-border);
  border-radius: 0.95rem;
  background: var(--app-subsurface);
  padding: 0.55rem 0.65rem;
  color: var(--app-copy);
  box-shadow: var(--app-subpanel-shadow);
  backdrop-filter: none;
  pointer-events: auto;
}

.request-logs-pagination-summary {
  color: var(--app-muted);
  font-size: 0.88rem;
}

.request-logs-per-page-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  color: var(--app-faint);
  font-size: 0.72rem;
  font-weight: 850;
  letter-spacing: 0.16em;
  text-transform: uppercase;
}

@media (min-width: 640px) {
  .request-logs-pagination-bar {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
    padding-right: 1rem;
    padding-left: 1rem;
  }
}
</style>
