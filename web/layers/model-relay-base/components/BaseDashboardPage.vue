<script setup lang="ts">
import type { CapacityLimitRow, Endpoint, Metric, UsageSeriesPoint, UsageSeriesResponse, UsageWindow } from '~/types/admin'
import { explicitProviderModelTarget } from '../../../utils/requestTarget'
import { requestTimingBreakdown } from '../../../utils/requestTiming'

type UsagePeriod = 'hour' | 'day' | 'week' | 'month'

const props = defineProps<{
  capacityRowFilter?: (row: CapacityLimitRow) => boolean
}>()

const catalog = useCatalogStore()
const queue = useQueueStore()
const requests = useRequestsStore()
const telemetry = useTelemetryStore()
const capacity = useCapacityStore()
const pageData = useAdminPageDataStore()
const relayApi = useRelayApi()
const relayPermissions = useModelRelayPermissions()
const { number, currencyMicros, durationMs, relativeTime, clockTime, healthTone } = useFormatters()
const { refreshModelLimitRows, scheduleModelLimitRowsRefresh } = useModelLimitRows()
const { modelRelayPath } = useModelRelayRoute()
const usageTab = ref<'requests' | 'tokens' | 'spend'>('requests')
const usageWindow = ref<UsageWindow>('hour')
const usageSeries = ref<UsageSeriesPoint[]>([])
const usagePeriodTotals = ref<Record<UsagePeriod, number>>({
  hour: 0,
  day: 0,
  week: 0,
  month: 0
})
const usageSeriesLoading = ref(false)
const nowTick = ref(Date.now())
let tickTimer: ReturnType<typeof window.setInterval> | null = null
let usageSeriesRefreshTimer: ReturnType<typeof window.setTimeout> | null = null
let usagePeriodTotalsRefreshTimer: ReturnType<typeof window.setTimeout> | null = null
let usageSeriesSeq = 0
let usagePeriodTotalsSeq = 0
let usageSeriesFetchedAtMs = 0
let usagePeriodTotalsFetchedAtMs = 0

const periodWindowMs = {
  second: 1_000,
  minute: 60_000,
  hour: 3_600_000,
  day: 86_400_000,
  week: 7 * 86_400_000,
  month: 30 * 86_400_000
} as const
const usagePeriodKeys: UsagePeriod[] = ['hour', 'day', 'week', 'month']
const recentModelPrimaryWindowMs = 3_600_000
const recentModelFallbackWindowMs = 86_400_000
const usageSeriesRefreshDelayMs = 3000
const canReadUsage = computed(() => relayPermissions.hasAny(['relay:usage:read:self', 'relay:usage:read:organization']))
const apiKeyUUIDByRequestID = computed(() => {
  const entries: Array<[string, string]> = []
  for (const log of requests.logs) {
    const requestID = String(log.request_id || '')
    const apiKeyUUID = String(log.api_key_uuid || '')
    if (requestID && apiKeyUUID) entries.push([requestID, apiKeyUUID])
  }
  return Object.fromEntries(entries)
})

function requestAPIKeyUUID(requestID: string) {
  return apiKeyUUIDByRequestID.value[requestID] || ''
}

function logConsumesUsage(log: {
  task_state: string
  status_code: number
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  actual_cost_micros: number
  started_at?: string | null
}, _metric: 'requests' | 'tokens' | 'spend') {
  return logSuccessful(log) || (logTerminal(log.task_state) && logHasActualUsage(log))
}

function logTerminal(state?: string | null) {
  return state === 'completed' || state === 'failed' || state === 'cancelled'
}

function logSuccessful(log: { task_state: string, status_code: number }) {
  return log.task_state === 'completed' && Number(log.status_code || 0) >= 200 && Number(log.status_code || 0) < 300
}

function logHasActualUsage(log: {
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  actual_cost_micros: number
}) {
  return log.actual_total_tokens > 0 || log.actual_input_tokens > 0 || log.actual_output_tokens > 0 || log.actual_cost_micros > 0
}

function goSetup() {
  return navigateTo(modelRelayPath('/setup'))
}

type RequestDeferSource = {
  actor_id?: string | null
  defer_scope?: string | null
  defer_reason?: string | null
  delay_reason?: string | null
  predicted_eligible_at?: string | null
  resource_eligible_at?: string | null
  user_eligible_at?: string | null
  user_limit_reason?: string | null
  endpoint_id?: string | null
  state?: string | null
}

function isUserScopedDefer(item: RequestDeferSource) {
  return ['user', 'user_model', 'user_provider', 'api_key', 'api_key_model', 'api_key_provider'].includes(String(item.defer_scope || ''))
}

function isUserRequestDefer(item: RequestDeferSource) {
  if (!isUserScopedDefer(item)) return false
  if (
    item.state === 'ready' ||
    item.state === 'in_flight' ||
    item.state === 'completed' ||
    item.state === 'failed' ||
    item.state === 'cancelled'
  ) {
    return false
  }
  const requestEligibleMs = parseMs(item.user_eligible_at || item.predicted_eligible_at)
  return requestEligibleMs <= 0 || requestEligibleMs > nowTick.value
}

function requestUserLimitTypeLabel(item: RequestDeferSource) {
  const reason = `${item.user_limit_reason || ''} ${item.delay_reason || ''} ${item.defer_reason || ''}`.toLowerCase()
  const match = reason.match(/\b(requests|tokens|spend)\/(second|minute|hour|day|month)\b/)
  if (!match) return ''
  return limitAbbrev(match[1] as 'requests' | 'tokens' | 'spend', match[2])
}

function requestUserLimitCountdown(item: RequestDeferSource) {
  const eligibleMs = parseMs(item.user_eligible_at || item.predicted_eligible_at)
  if (eligibleMs <= 0) return ''
  const remainingMs = Math.max(0, eligibleMs - nowTick.value)
  return `${(remainingMs / 1000).toFixed(1)}s remaining`
}

function requestUserLimitMessage(item: RequestDeferSource) {
  if (!isUserRequestDefer(item)) return ''
  const typeLabel = requestUserLimitTypeLabel(item)
  const countdown = requestUserLimitCountdown(item)
  return [
    `Blocked by User Limit${typeLabel ? ` ${typeLabel}` : ''}`,
    countdown
  ].filter(Boolean).join(' · ')
}

function endpointUserLimitBadgeActive(endpoint: Endpoint) {
  return endpointReachedLimitRows(endpoint).some(row => row.source !== 'pro_api_key_limit')
}

function endpointAPIKeyLimitBadgeActive(endpoint: Endpoint) {
  return endpointReachedLimitRows(endpoint).some(row => row.source === 'pro_api_key_limit')
}

function endpointReachedLimitRows(endpoint: Endpoint) {
  return capacityRowsForEndpoint(endpoint.id).filter((row) => {
    if (!row.user_scoped) return false
    const effective = Number(row.effective || row.configured || 0)
    const consumed = Number(row.used || 0) + Number(row.reserved || 0)
    return Boolean(row.blocked) || (effective > 0 && consumed >= effective) || Number(row.percent || 0) >= 99.5
  })
}

function liveCapacityFresh() {
  return capacity.isFresh(nowTick.value)
}

function capacityModelForEndpoint(endpointID: string) {
  if (!liveCapacityFresh()) return null
  return capacity.model(endpointID)
}

function capacityRowsForEndpoint(endpointID: string): CapacityLimitRow[] {
  if (!liveCapacityFresh()) return []
  const rows = capacity.rowsForEndpoint(endpointID)
  return props.capacityRowFilter ? rows.filter(props.capacityRowFilter) : rows
}

function endpointDisplayHealth(endpoint: Endpoint) {
  const model = capacityModelForEndpoint(endpoint.id)
  if (model?.capacity_state === 'rate-limited') return 'rate_limited'
  if (model?.capacity_state === 'cooling-down') return 'cooling_down'
  if (model?.capacity_state === 'unhealthy') return 'unhealthy'
  if (model?.capacity_state === 'unavailable') return 'unavailable'
  if (model?.health_status) return model.health_status
  return 'unknown'
}

function endpointDisplayCooldownUntil(endpoint: Endpoint) {
  const model = capacityModelForEndpoint(endpoint.id)
  if (model?.cooldown_until) return model.cooldown_until
  return ''
}

function endpointCooldownBadge(endpoint: Endpoint): { label: string, tone: 'amber' | 'rose' } | null {
  const model = capacityModelForEndpoint(endpoint.id)
  const cooldownUntil = model?.cooldown_until || endpoint.cooldown_until
  if (!cooldownUntil || parseMs(cooldownUntil) <= nowTick.value) return null

  const statusCode = Number(model?.cooldown_status_code || endpoint.cooldown_status_code || 0)
  if (statusCode >= 400 && statusCode <= 599) {
    return { label: `server ${statusCode}`, tone: statusCode === 429 ? 'amber' : 'rose' }
  }
  switch (String(model?.cooldown_reason || endpoint.cooldown_reason || '')) {
    case 'upstream_rate_limited': return { label: 'server rate limit', tone: 'amber' }
    case 'upstream_server_error': return { label: 'server unavailable', tone: 'rose' }
    case 'upstream_retry_after': return { label: 'server retry after', tone: 'amber' }
    default: return null
  }
}

const healthCounts = computed(() =>
  catalog.endpoints.reduce((acc, endpoint) => {
    const health = endpointDisplayHealth(endpoint)
    acc[health] = (acc[health] || 0) + 1
    return acc
  }, {} as Record<string, number>)
)

const modelStatusCards = computed(() => {
  const healthy = healthCounts.value.healthy || 0
  const cooling = (healthCounts.value.cooling_down || 0) + (healthCounts.value.rate_limited || 0)
  const unhealthy = healthCounts.value.unhealthy || 0

  return [
    {
      key: 'healthy',
      label: 'Healthy',
      count: healthy,
      summary: healthy > 0 ? `${healthy} models can dispatch immediately.` : 'No models are currently dispatch-ready.',
      dotClass: 'bg-emerald-300',
      textClass: 'text-emerald-300',
      pulse: false
    },
    {
      key: 'cooling',
      label: 'Cooling down',
      count: cooling,
      summary: cooling > 0 ? `${cooling} models are waiting on pacing or longer cooldown windows.` : 'No models are cooling down right now.',
      dotClass: 'bg-sky-300',
      textClass: 'text-sky-300',
      pulse: cooling > 0
    },
    {
      key: 'unhealthy',
      label: 'Unhealthy',
      count: unhealthy,
      summary: unhealthy > 0 ? `${unhealthy} models are backing off after upstream failures.` : 'No models are marked unhealthy right now.',
      dotClass: 'bg-rose-300',
      textClass: 'text-rose-300',
      pulse: unhealthy > 0
    }
  ]
})

const modelBoard = computed(() => catalog.endpoints.map((endpoint) => ({
  endpoint,
  provider: catalog.providerMap[endpoint.provider_id],
  displayHealth: endpointDisplayHealth(endpoint),
  displayCooldownUntil: endpointDisplayCooldownUntil(endpoint),
  cooldownBadge: endpointCooldownBadge(endpoint),
  userLimitActive: endpointUserLimitBadgeActive(endpoint),
  apiKeyLimitActive: endpointAPIKeyLimitBadgeActive(endpoint),
  guardrails: catalog.effectiveGuardrailsForEndpointAcrossGroups(endpoint.id),
  queueDepth: queue.items.filter(item => item.endpoint_id === endpoint.id && !isUserRequestDefer(item)).length,
  inFlight: queue.items.filter(item => item.endpoint_id === endpoint.id && item.state === 'in_flight').length,
  spendToday: canReadUsage.value
    ? requests.logs
        .filter((item) => item.endpoint_id === endpoint.id && logConsumesUsage(item, 'spend'))
        .reduce((sum, item) => sum + requestMetricValue(item, 'spend'), 0)
    : 0
})))

function taskStateTone(state: string) {
  switch (state) {
    case 'completed':
      return 'emerald'
    case 'failed':
      return 'rose'
    case 'cancelled':
      return 'slate'
    case 'in_flight':
      return 'sky'
    case 'waiting':
    case 'ready':
      return 'amber'
    default:
      return 'slate'
  }
}

function routeSlug(value?: string | null) {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'provider missing' || normalized === 'no upstream selected' || normalized === 'no model selected') return ''
  return normalized
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function routingGroupName(laneID?: string | null, laneName?: string | null) {
  if (laneID && catalog.laneMap[laneID]) return catalog.laneMap[laneID].name
  const target = String(laneName || '').trim().toLowerCase()
  if (!target) return ''
  return catalog.lanes.find(lane => lane.name.toLowerCase() === target || lane.slug === target)?.name || ''
}

function requestRouteDetail(providerID?: string | null, endpointID?: string | null, incomingModel?: string | null, fallbackModelName?: string | null, statusCode = 0) {
  const endpoint = endpointID ? catalog.endpointMap[endpointID] : null
  const provider = catalog.providerMap[providerID || endpoint?.provider_id || '']
  const requested = explicitProviderModelTarget(incomingModel)
  const providerSlug = routeSlug(provider?.slug || requested?.provider)
  const modelSlug = routeSlug(endpoint?.slug || requested?.model || fallbackModelName)
  if (statusCode === 429 && !providerSlug && !modelSlug) return ''
  return `${providerSlug || 'provider-missing'}/${modelSlug || 'no-model-selected'}`
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
  if (!statusCode) return ''
  return `Status ${statusCode}`
}

function parseMs(value?: string | null) {
  if (!value) return 0
  const ms = new Date(value).getTime()
  return Number.isNaN(ms) ? 0 : ms
}

function requestAnchorMs(log: {
  queued_at?: string | null
  created_at: string
  started_at?: string | null
  wait_ms: number
}) {
  const queuedMs = parseMs(log.queued_at)
  if (queuedMs > 0) return queuedMs
  const startedMs = parseMs(log.started_at)
  if (startedMs > 0) {
    return Math.max(0, startedMs - Math.max(0, log.wait_ms))
  }
  return parseMs(log.created_at)
}

function hasActualUsage(log: {
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
}) {
  return log.actual_total_tokens > 0 || log.actual_input_tokens > 0 || log.actual_output_tokens > 0
}

function requestStartedMs(log: {
  queued_at?: string | null
  started_at?: string | null
  finished_at?: string | null
  created_at: string
}) {
  return parseMs(log.started_at || log.queued_at || log.finished_at || log.created_at)
}

function metricWindowStart(period: keyof typeof periodWindowMs) {
  return nowTick.value - periodWindowMs[period]
}

function requestMetricValue(log: {
  task_state: string
  status_code: number
  actual_total_tokens: number
  actual_input_tokens: number
  actual_output_tokens: number
  estimated_input_tokens: number
  estimated_output_tokens: number
  actual_cost_micros: number
  estimated_cost_micros: number
}, metric: 'requests' | 'tokens' | 'spend') {
  switch (metric) {
    case 'requests':
      return 1
    case 'tokens':
      if (hasActualUsage(log)) {
        return log.actual_total_tokens || (log.actual_input_tokens + log.actual_output_tokens)
      }
      return logSuccessful(log) ? log.estimated_input_tokens + log.estimated_output_tokens : 0
    case 'spend':
      return log.actual_cost_micros || (logSuccessful(log) ? log.estimated_cost_micros : 0)
  }
}

function matchesScope(scopeType: 'global' | 'provider' | 'endpoint', scopeId: string, providerId?: string | null, endpointId?: string | null) {
  if (scopeType === 'global') return true
  if (scopeType === 'provider') return String(providerId || '') === scopeId
  return String(endpointId || '') === scopeId
}

const activityFeed = computed(() => {
  if (!canReadUsage.value) return []
  const terminalStates = new Set(['completed', 'failed', 'cancelled'])
  const feedByRequest = new Map<string, {
    id: string
    actorId: string
    apiKeyUUID: string
    state: string
    tone: string
    title: string
    detail: string
    queueTiming: string
    timing: string
    phase: string
    tokens: string
    statusCode: number
    statusLabel: string
    statusTone: 'slate' | 'status-amber' | 'status-rose' | 'status-emerald'
    timestamp: string
    anchorMs: number
    updatedMs: number
  }>()

  for (const item of queue.items) {
      const live = telemetry.requestProgress[item.request_id]
      const userLimitMessage = requestUserLimitMessage(item)
      const startedAt = live?.started_at || item.started_at || ''
      const queuedAtMs = parseMs(item.queued_at)
      const startedAtMs = parseMs(startedAt)
      const runningForMs = startedAtMs > 0 ? Math.max(0, nowTick.value - startedAtMs) : 0
      const queueWaitMs = item.state === 'in_flight' && startedAtMs > 0
        ? Math.max(0, startedAtMs - queuedAtMs)
        : Math.max(0, nowTick.value - queuedAtMs)
      feedByRequest.set(item.request_id || `task-${item.task_id}`, {
        id: item.request_id || `task-${item.task_id}`,
        actorId: String(item.actor_id || ''),
        apiKeyUUID: String(item.api_key_uuid || requestAPIKeyUUID(item.request_id)),
        state: userLimitMessage ? 'request_blocked' : item.state,
        tone: userLimitMessage ? 'amber' : taskStateTone(item.state),
        title: routingGroupName('', item.lane),
        detail: requestRouteDetail(item.provider_id, item.endpoint_id, item.incoming_model, item.endpoint_name),
        queueTiming: `Queued ${clockTime(item.queued_at)} · Waited ${durationMs(queueWaitMs)}`,
        timing: item.state === 'in_flight' && startedAtMs > 0
          ? `Start ${clockTime(startedAt)} · Duration ${durationMs(runningForMs)}`
          : '',
        phase: item.state === 'in_flight'
          ? (live?.substatus || item.substatus || 'Dispatching upstream')
          : userLimitMessage || item.delay_reason || (item.state === 'ready' ? 'Eligible to dispatch' : 'Still queued'),
        tokens: item.state === 'in_flight'
          ? `~Up ${number(live?.uploaded_tokens ?? item.uploaded_tokens ?? 0)} tok · ~Down ${number(live?.downloaded_tokens ?? item.downloaded_tokens ?? 0)} tok`
          : '',
        statusCode: 0,
        statusLabel: '',
        statusTone: 'slate',
        timestamp: item.queued_at,
        anchorMs: queuedAtMs,
        updatedMs: startedAtMs || queuedAtMs
      })
  }

  for (const log of requests.logs) {
      const startedAt = log.started_at || log.created_at
      const finishedAt = log.finished_at || log.updated_at || log.created_at
      const queuedMs = requestAnchorMs(log)
      const queuedAtLabel = queuedMs > 0 ? clockTime(new Date(queuedMs).toISOString()) : ''
      const startedMs = parseMs(startedAt)
      const finishedMs = parseMs(finishedAt)
      const duration = requestTimingBreakdown(log).totalMS ?? Math.max(0, finishedMs - startedMs)
      const actualUsage = hasActualUsage(log)
      const inputTokens = actualUsage ? log.actual_input_tokens : log.estimated_input_tokens
      const outputTokens = actualUsage ? log.actual_output_tokens : log.estimated_output_tokens
      const tokenSummary = actualUsage
        ? `Up ${number(inputTokens)} tok · Down ${number(outputTokens)} tok`
        : logSuccessful(log)
          ? `~Up ${number(inputTokens)} tok · ~Down ${number(outputTokens)} tok`
          : ''
      const existing = feedByRequest.get(log.request_id)
      if (existing && terminalStates.has(existing.state) && existing.updatedMs >= finishedMs) {
        continue
      }
      if (existing && !terminalStates.has(log.task_state)) {
        continue
      }
      feedByRequest.set(log.request_id, {
        id: log.request_id,
        actorId: String(log.actor_id || ''),
        apiKeyUUID: String(log.api_key_uuid || ''),
        state: log.task_state,
        tone: taskStateTone(log.task_state),
        title: routingGroupName(log.lane_id),
        detail: requestRouteDetail(log.provider_id, log.endpoint_id, log.incoming_model, '', log.status_code),
        queueTiming: log.wait_ms > 0
          ? (queuedAtLabel
              ? `Queued ${queuedAtLabel} · Waited ${durationMs(log.wait_ms)} before dispatch`
              : `Waited ${durationMs(log.wait_ms)} before dispatch`)
          : (queuedAtLabel ? `Queued ${queuedAtLabel}` : ''),
        timing: `Start ${clockTime(startedAt)} · End ${clockTime(finishedAt)} · Duration ${durationMs(duration)}`,
        phase: log.error_text || '',
        tokens: tokenSummary,
        statusCode: log.status_code,
        statusLabel: statusCodeLabel(log.status_code),
        statusTone: statusCodeTone(log.status_code),
        timestamp: log.updated_at || log.created_at,
        anchorMs: queuedMs,
        updatedMs: finishedMs
      })
  }

  for (const progress of Object.values(telemetry.requestProgress)) {
      if (!progress.request_id || feedByRequest.has(progress.request_id)) {
        continue
      }
      const queuedMs = requestAnchorMs({
        queued_at: progress.queued_at,
        created_at: progress.updated_at || progress.finished_at || progress.started_at || new Date(nowTick.value).toISOString(),
        started_at: progress.started_at,
        wait_ms: progress.wait_ms || 0
      })
      const startedMs = parseMs(progress.started_at)
      const finishedMs = parseMs(progress.finished_at)
      const updatedMs = parseMs(progress.updated_at || progress.finished_at || progress.started_at)
      const isCompleted = progress.state === 'completed'
      const isInFlight = progress.state === 'in_flight'
      const isWaiting = progress.state === 'waiting'
      const isReady = progress.state === 'ready'
      const userLimitMessage = requestUserLimitMessage(progress)
      const queueTiming = (progress.wait_ms || 0) > 0
        ? `Queued ${clockTime(progress.queued_at || new Date(queuedMs).toISOString())} · Waited ${durationMs(progress.wait_ms || 0)} before dispatch`
        : (queuedMs > 0 ? `Queued ${clockTime(progress.queued_at || new Date(queuedMs).toISOString())}` : '')
      const timing = isCompleted && startedMs > 0 && finishedMs > 0
        ? `Start ${clockTime(progress.started_at)} · End ${clockTime(progress.finished_at)} · Duration ${durationMs(progress.total_time_ms || ((progress.wait_ms || 0) + (progress.latency_ms || Math.max(0, finishedMs - startedMs))))}`
        : (isInFlight && startedMs > 0 ? `Start ${clockTime(progress.started_at)} · Duration ${durationMs(Math.max(0, nowTick.value - startedMs))}` : '')
      feedByRequest.set(progress.request_id, {
        id: progress.request_id,
        actorId: String(progress.actor_id || ''),
        apiKeyUUID: requestAPIKeyUUID(progress.request_id),
        state: userLimitMessage ? 'request_blocked' : progress.state || 'in_flight',
        tone: userLimitMessage ? 'amber' : taskStateTone(progress.state || 'in_flight'),
        title: routingGroupName('', progress.lane),
        detail: requestRouteDetail(progress.provider_id, progress.endpoint_id, progress.incoming_model, progress.endpoint_name, progress.status_code),
        queueTiming,
        timing,
        phase: isCompleted ? '' : userLimitMessage || (isWaiting ? 'Waiting in queue' : isReady ? 'Eligible to dispatch' : (progress.substatus || 'Updating')),
        tokens: isCompleted
          ? `Up ${number(progress.uploaded_tokens || 0)} tok · Down ${number(progress.downloaded_tokens || 0)} tok`
          : isInFlight
            ? `~Up ${number(progress.uploaded_tokens || 0)} tok · ~Down ${number(progress.downloaded_tokens || 0)} tok`
            : '',
        statusCode: progress.status_code || 0,
        statusLabel: statusCodeLabel(progress.status_code),
        statusTone: statusCodeTone(progress.status_code),
        timestamp: progress.updated_at || progress.finished_at || progress.started_at || progress.queued_at || '',
        anchorMs: queuedMs,
        updatedMs: updatedMs || queuedMs
      })
  }

  return [...feedByRequest.values()]
    .sort((a, b) => b.anchorMs - a.anchorMs || b.updatedMs - a.updatedMs)
    .slice(0, 18)
})

function limitAbbrev(metric: 'requests' | 'tokens' | 'spend', period: string) {
  if (metric === 'requests') {
    switch (period) {
      case 'second': return 'RPS'
      case 'minute': return 'RPM'
      case 'hour': return 'RPH'
      case 'day': return 'RPD'
      case 'month': return 'RPMO'
    }
  }
  if (metric === 'tokens') {
    switch (period) {
      case 'second': return 'TPS'
      case 'minute': return 'TPM'
      case 'hour': return 'TPH'
      case 'day': return 'TPD'
      case 'month': return 'TPMO'
    }
  }
  switch (period) {
    case 'minute': return 'SPM'
    case 'hour': return 'SPH'
    case 'day': return 'SPD'
    case 'month': return 'SPMO'
    default: return 'SPEND'
  }
}

function capacityLimitRowToCardRow(row: CapacityLimitRow) {
  const blockedUntil = row.blocked_until || ''
  return {
    key: row.key,
    label: row.label || limitAbbrev(row.metric as 'requests' | 'tokens' | 'spend', row.period),
    metric: row.metric as Metric,
    period: row.period,
    source: String(row.source || ''),
    actorId: String(row.actor_id || ''),
    scopeId: String(row.scope_id || ''),
    targetType: String(row.target_type || ''),
    targetKey: String(row.target_key || ''),
    configured: Number(row.effective || row.configured || 0),
    used: Number(row.used || 0) + Number(row.reserved || 0),
    percent: Math.max(0, Math.min(100, Number(row.percent || 0))),
    blocked: Boolean(row.blocked),
    blockedUntil,
    blockedRemainingMs: blockedUntil ? Math.max(0, parseMs(blockedUntil) - nowTick.value) : 0
  }
}

function usageValue(scopeType: 'global' | 'provider' | 'endpoint', scopeId: string, metric: 'requests' | 'tokens' | 'spend', period: string) {
  if (!canReadUsage.value) return 0
  const periodKey = period as keyof typeof periodWindowMs
  const windowStart = metricWindowStart(periodKey)
  let total = 0

  for (const log of requests.logs) {
    if (!logConsumesUsage(log, metric)) continue
    if (!matchesScope(scopeType, scopeId, log.provider_id, log.endpoint_id)) continue
    const startedMs = requestStartedMs(log)
    if (startedMs < windowStart) continue
    total += requestMetricValue(log, metric)
  }

  return total
}

function usageSeriesResponseTotal(response: UsageSeriesResponse) {
  return (response.points || []).reduce((sum, point) => sum + Number(point.value || 0), 0)
}

function resetUsagePeriodTotals() {
  usagePeriodTotals.value = {
    hour: 0,
    day: 0,
    week: 0,
    month: 0
  }
  usagePeriodTotalsFetchedAtMs = Date.now()
}

function usagePeriodLiveDelta(period: UsagePeriod) {
  if (!canReadUsage.value) return 0
  const windowStart = metricWindowStart(period)
  let total = 0
  for (const log of requests.logs) {
    if (!logConsumesUsage(log, usageTab.value)) continue
    const updatedMs = parseMs(log.updated_at || log.finished_at || log.created_at)
    if (updatedMs <= usagePeriodTotalsFetchedAtMs) continue
    const startedMs = requestStartedMs(log)
    if (startedMs < windowStart) continue
    total += requestMetricValue(log, usageTab.value)
  }
  return total
}

function usagePeriodTotal(period: UsagePeriod) {
  return Number(usagePeriodTotals.value[period] || 0) + usagePeriodLiveDelta(period)
}

const recentModelUsage = computed(() => {
  if (!canReadUsage.value) return []
  const timestamps = new Map<string, number>()
  const queueDepthByEndpoint = queue.items.reduce((acc, item) => {
    if (!item.endpoint_id) return acc
    if (isUserRequestDefer(item)) return acc
    acc[item.endpoint_id] = (acc[item.endpoint_id] || 0) + 1
    return acc
  }, {} as Record<string, number>)

  for (const item of queue.items) {
    if (!item.endpoint_id) continue
    const ts = parseMs(item.started_at || item.queued_at)
    if (ts > 0) {
      timestamps.set(item.endpoint_id, Math.max(ts, timestamps.get(item.endpoint_id) || 0))
    }
  }

  for (const log of requests.logs) {
    if (!log.endpoint_id) continue
    if (!logConsumesUsage(log, 'requests')) continue
    const ts = parseMs(log.finished_at || log.started_at || log.queued_at || log.created_at || log.updated_at)
    if (ts > 0) {
      timestamps.set(log.endpoint_id, Math.max(ts, timestamps.get(log.endpoint_id) || 0))
    }
  }

  for (const progress of Object.values(telemetry.requestProgress)) {
    if (!progress.endpoint_id) continue
    const ts = parseMs(progress.updated_at || progress.finished_at || progress.started_at || progress.queued_at)
    if (ts > 0) {
      timestamps.set(progress.endpoint_id, Math.max(ts, timestamps.get(progress.endpoint_id) || 0))
    }
  }

  const entries = [...timestamps.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([endpointId, timestamp]) => {
      const endpoint = catalog.endpointMap[endpointId]
      if (!endpoint) return null
      const provider = catalog.providerMap[endpoint.provider_id]
      const limits = capacityRowsForEndpoint(endpoint.id).map(capacityLimitRowToCardRow)

      const displayHealth = endpointDisplayHealth(endpoint)
      const displayCooldownUntil = endpointDisplayCooldownUntil(endpoint)
      const cooldownBadge = endpointCooldownBadge(endpoint)
      const cooldownRemaining = displayCooldownUntil
        ? Math.max(0, new Date(displayCooldownUntil).getTime() - nowTick.value)
        : 0

      return {
        endpoint,
        provider,
        queueDepth: Number(queueDepthByEndpoint[endpoint.id] || 0),
        cooldownRemaining,
        displayHealth,
        cooldownBadge,
        userLimitActive: endpointUserLimitBadgeActive(endpoint),
        apiKeyLimitActive: endpointAPIKeyLimitBadgeActive(endpoint),
        lastActiveAt: timestamp,
        lastActiveLabel: `Used ${relativeTime(new Date(timestamp).toISOString())}`,
        limits
      }
    })
    .filter((entry): entry is NonNullable<typeof entry> => Boolean(entry))

  const oneHourCutoff = nowTick.value - recentModelPrimaryWindowMs
  const oneDayCutoff = nowTick.value - recentModelFallbackWindowMs
  const lastHour = entries.filter(entry => entry.lastActiveAt >= oneHourCutoff)
  const scoped = lastHour.length ? lastHour : entries.filter(entry => entry.lastActiveAt >= oneDayCutoff)
  return scoped.slice(0, 12)
})

const recentModelUsageScopeLabel = computed(() => {
  if (!recentModelUsage.value.length) {
    return 'No models used in the last day.'
  }
  const oneHourCutoff = nowTick.value - recentModelPrimaryWindowMs
  return recentModelUsage.value.some(entry => entry.lastActiveAt >= oneHourCutoff)
    ? 'Showing models used in the last hour.'
    : 'No models in the last hour; showing models used in the last day.'
})

const waitingCount = computed(() => queue.items.filter(item => item.state === 'waiting').length)
const inFlightCount = computed(() => queue.items.filter(item => item.state === 'in_flight').length)
const queueDepth = computed(() => queue.items.length)

const usagePeriods = computed(() =>
  usagePeriodKeys.map((period) => ({
    period,
    label: period.charAt(0).toUpperCase() + period.slice(1),
    global: usagePeriodTotal(period)
  }))
)

const usageChartPoints = computed(() =>
  usageSeries.value.map(item => ({
    label: item.label,
    value: item.value,
    bucketStartMs: parseMs(item.bucket_start),
    bucketEndMs: parseMs(item.bucket_end)
  }))
)

const usageChartWithLivePoints = computed(() => {
  if (!canReadUsage.value) return []
  const points = usageChartPoints.value.map(item => ({ ...item }))
  for (const log of requests.logs) {
    if (!logConsumesUsage(log, usageTab.value)) continue
    const updatedMs = parseMs(log.updated_at || log.finished_at || log.created_at)
    if (updatedMs <= usageSeriesFetchedAtMs) continue

    const startedMs = requestStartedMs(log)
    if (startedMs <= 0) continue

    const bucket = points.find(point => startedMs >= point.bucketStartMs && startedMs < point.bucketEndMs)
    if (!bucket) continue

    bucket.value += requestMetricValue(log, usageTab.value)
  }

  const chartPoints: Array<{ label: string, value: number | null, highlight?: boolean }> = points.map(({ label, value }) => ({ label, value }))
  let lastVisibleIndex = -1

  for (let index = chartPoints.length - 1; index >= 0; index -= 1) {
    if (chartPoints[index]?.value != null) {
      lastVisibleIndex = index
      break
    }
  }

  if (lastVisibleIndex >= 0) {
    const highlightedPoint = chartPoints[lastVisibleIndex]
    if (highlightedPoint) {
      chartPoints[lastVisibleIndex] = {
        ...highlightedPoint,
        highlight: true
      }
    }
  }

  return chartPoints
})

async function refreshUsageSeries() {
  if (usageSeriesRefreshTimer && process.client) {
    window.clearTimeout(usageSeriesRefreshTimer)
    usageSeriesRefreshTimer = null
  }
  const refreshSeq = ++usageSeriesSeq
  const requestedAtMs = Date.now()
  if (!canReadUsage.value) {
    usageSeriesLoading.value = false
    usageSeries.value = []
    usageSeriesFetchedAtMs = requestedAtMs
    return
  }
  usageSeriesLoading.value = true
  try {
    const response = await relayApi.get<UsageSeriesResponse>(`/api/stats/usage-series?metric=${usageTab.value}&window=${usageWindow.value}`)
    if (refreshSeq !== usageSeriesSeq) return
    usageSeries.value = response.points || []
    usageSeriesFetchedAtMs = requestedAtMs
  } catch {
    if (refreshSeq !== usageSeriesSeq) return
    usageSeries.value = []
  } finally {
    if (refreshSeq === usageSeriesSeq) {
      usageSeriesLoading.value = false
    }
  }
}

async function refreshUsagePeriodTotals() {
  if (usagePeriodTotalsRefreshTimer && process.client) {
    window.clearTimeout(usagePeriodTotalsRefreshTimer)
    usagePeriodTotalsRefreshTimer = null
  }
  const refreshSeq = ++usagePeriodTotalsSeq
  const requestedAtMs = Date.now()
  if (!canReadUsage.value) {
    resetUsagePeriodTotals()
    usagePeriodTotalsFetchedAtMs = requestedAtMs
    return
  }

  try {
    const responses = await Promise.all(
      usagePeriodKeys.map((period) =>
        relayApi.get<UsageSeriesResponse>(`/api/stats/usage-series?metric=${usageTab.value}&window=${period}`)
      )
    )
    if (refreshSeq !== usagePeriodTotalsSeq) return
    usagePeriodTotals.value = usagePeriodKeys.reduce((acc, period, index) => {
      acc[period] = usageSeriesResponseTotal(responses[index] || {
        metric: usageTab.value,
        window: period,
        points: []
      })
      return acc
    }, {} as Record<UsagePeriod, number>)
    usagePeriodTotalsFetchedAtMs = requestedAtMs
  } catch {
    if (refreshSeq !== usagePeriodTotalsSeq) return
    resetUsagePeriodTotals()
    usagePeriodTotalsFetchedAtMs = requestedAtMs
  }
}

function scheduleUsageSeriesRefresh() {
  if (!process.client || !canReadUsage.value) return
  if (usageSeriesRefreshTimer) {
    window.clearTimeout(usageSeriesRefreshTimer)
  }
  usageSeriesRefreshTimer = window.setTimeout(() => {
    usageSeriesRefreshTimer = null
    void refreshUsageSeries()
  }, usageSeriesRefreshDelayMs)
}

function scheduleUsagePeriodTotalsRefresh() {
  if (!process.client || !canReadUsage.value) return
  if (usagePeriodTotalsRefreshTimer) {
    window.clearTimeout(usagePeriodTotalsRefreshTimer)
  }
  usagePeriodTotalsRefreshTimer = window.setTimeout(() => {
    usagePeriodTotalsRefreshTimer = null
    void refreshUsagePeriodTotals()
  }, usageSeriesRefreshDelayMs)
}

const queueStatusCards = computed(() => {
  const cards = [
    { label: 'Queue depth', value: number(queueDepth.value), hint: 'Live queued items' },
    { label: 'Waiting', value: number(waitingCount.value), hint: 'Delayed by pacing' },
    { label: 'In flight', value: number(inFlightCount.value), hint: 'Requests currently running' }
  ]
  if (canReadUsage.value) {
    cards.push({
      label: 'Recent spend',
      value: currencyMicros(usageValue('global', 0, 'spend', 'day')),
      hint: 'Tracked from recent request activity'
    })
  }
  return cards
})

onMounted(async () => {
  if (process.client) {
    tickTimer = window.setInterval(() => {
      nowTick.value = Date.now()
    }, 100)
  }
  await Promise.all([
    pageData.load('dashboard'),
    canReadUsage.value ? requests.refreshRecentActivity({ limit: 24 }) : Promise.resolve(requests.hydrate([])),
    capacity.refresh(),
    refreshModelLimitRows()
  ])
  await Promise.all([
    refreshUsageSeries(),
    refreshUsagePeriodTotals()
  ])
})

onBeforeUnmount(() => {
  if (tickTimer && process.client) {
    window.clearInterval(tickTimer)
    tickTimer = null
  }
  if (usageSeriesRefreshTimer && process.client) {
    window.clearTimeout(usageSeriesRefreshTimer)
    usageSeriesRefreshTimer = null
  }
  if (usagePeriodTotalsRefreshTimer && process.client) {
    window.clearTimeout(usagePeriodTotalsRefreshTimer)
    usagePeriodTotalsRefreshTimer = null
  }
})

watch(usageWindow, async () => {
  await refreshUsageSeries()
})

watch(usageTab, async () => {
  await Promise.all([
    refreshUsageSeries(),
    refreshUsagePeriodTotals()
  ])
})

watch(canReadUsage, async (next) => {
  if (!next) {
    requests.hydrate([])
    usageSeries.value = []
    usageSeriesFetchedAtMs = Date.now()
    resetUsagePeriodTotals()
    return
  }
  await Promise.all([
    requests.refreshRecentActivity({ limit: 24 }),
    refreshUsageSeries(),
    refreshUsagePeriodTotals()
  ])
})

watch(() => `${requests.logs.length}:${requests.logs[0]?.updated_at || ''}`, async () => {
  scheduleModelLimitRowsRefresh()
  scheduleUsageSeriesRefresh()
  scheduleUsagePeriodTotalsRefresh()
})
</script>

<template>
  <div class="space-y-8">
    <UiPanel v-if="!catalog.providers.length || !catalog.endpoints.length" :padded="false">
      <div class="grid gap-6 p-4 sm:p-6 lg:grid-cols-[1.2fr_0.8fr] lg:p-10">
        <UiEmptyState
          title="No models are connected yet"
          description="Model Relay becomes operational once you connect one real upstream model and place it into a routing group. Start with guided setup and keep the first run deliberately simple."
          action-label="Start Guided Setup"
          @action="goSetup"
        />

        <div class="app-subsurface p-6">
          <p class="text-sm font-semibold tracking-tight text-slate-50">What happens next</p>
          <div class="mt-5 space-y-3">
            <div class="app-subsurface p-5">
              <p class="text-sm font-medium text-white">1. Connect one upstream</p>
              <p class="mt-2 text-sm leading-6 text-slate-200/76">Add one provider and one model so Model Relay has something real to route to.</p>
            </div>
            <div class="app-subsurface p-5">
              <p class="text-sm font-medium text-white">2. Put it in a group</p>
              <p class="mt-2 text-sm leading-6 text-slate-200/76">Assign the model to a routing group so requests know which ranked collection to use.</p>
            </div>
            <div class="app-subsurface p-5">
              <p class="text-sm font-medium text-white">3. Send one test request</p>
              <p class="mt-2 text-sm leading-6 text-slate-200/76">Verify the connection before you spend time tuning deeper pacing or cost policies.</p>
            </div>
          </div>
        </div>
      </div>
    </UiPanel>

    <div class="grid gap-6 xl:grid-cols-[1.3fr_0.7fr]">
      <section class="app-surface overflow-hidden px-4 pb-5 sm:px-6 sm:pb-6">
        <div class="flex flex-col gap-6 pt-6 sm:pt-8">
          <div class="order-3 space-y-3">
            <p class="text-sm leading-6 text-slate-300/76">Model health stays focused on dispatch-ready, timed cooldown pressure, and outright unhealthy upstream behavior.</p>
            <div class="divide-y divide-[color:var(--app-border)] border-y border-[color:var(--app-border)]">
              <div
                v-for="item in modelStatusCards"
                :key="item.key"
                class="flex items-center justify-between gap-4 px-1 py-3"
              >
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <span :class="['h-2 w-2 rounded-full', item.dotClass]" />
                    <p class="text-sm font-medium text-white">{{ item.label }}</p>
                  </div>
                  <p class="mt-1 text-xs leading-5 text-slate-300/78">{{ item.summary }}</p>
                </div>
                <span class="text-sm font-medium text-slate-200">{{ item.count }}</span>
              </div>
            </div>
          </div>

          <div class="ui-metric-strip ui-metric-strip--no-top order-1 md:grid-cols-2 xl:grid-cols-4">
              <div v-for="item in queueStatusCards" :key="item.label" class="app-metric">
                <p class="text-xs uppercase tracking-[0.2em] text-slate-400">{{ item.label }}</p>
                <p class="mt-1 text-xl font-semibold text-white">{{ item.value }}</p>
                <p class="mt-1 text-xs leading-5 text-slate-300/68">{{ item.hint }}</p>
              </div>
          </div>

          <div v-if="canReadUsage" class="order-2 space-y-3">
            <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <p class="text-xs font-semibold uppercase tracking-[0.24em] text-slate-400">Usage</p>
              <div class="flex flex-wrap items-center gap-2">
                <UiTabs v-model="usageTab" :tabs="['requests', 'tokens', 'spend']" />
                <UiTabs v-model="usageWindow" :tabs="['hour', 'day', 'week', 'month']" />
              </div>
            </div>
            <DashboardUsageAreaChart :metric="usageTab" :window="usageWindow" :points="usageChartWithLivePoints" zoomable />
            <p v-if="usageSeriesLoading" class="text-xs text-slate-400">Loading usage series…</p>
            <div class="ui-metric-strip md:grid-cols-2 xl:grid-cols-4">
              <div v-for="item in usagePeriods" :key="item.period" class="ui-metric-strip__item">
                <p class="text-xs uppercase tracking-[0.2em] text-slate-400">{{ item.label }}</p>
                <p class="mt-2 text-2xl font-semibold text-white">{{ usageTab === 'spend' ? currencyMicros(item.global) : number(item.global) }}</p>
                <p class="mt-2 text-xs text-slate-400">Global scope</p>
              </div>
            </div>
          </div>

          <div class="order-4 space-y-3">
            <p class="text-xs font-semibold uppercase tracking-[0.24em] text-slate-400">Model health board</p>
            <UiTable :columns="['Provider/model', 'Health', 'Cooldown', 'Queue', 'In flight', 'Recent spend']">
              <tr v-for="entry in modelBoard" :key="entry.endpoint.id" class="hover:bg-white/[0.02]">
                <td class="px-3 py-2.5">
                  <p class="font-medium text-white">
                    {{ entry.provider?.slug || 'provider-missing' }}
                    <span class="text-slate-500">/&nbsp;</span>
                    <span class="text-slate-300/84">{{ entry.endpoint.slug }}</span>
                  </p>
                </td>
                <td class="px-3 py-2.5">
                  <div class="flex flex-col items-start gap-2 whitespace-nowrap">
                    <UiBadge :tone="healthTone(entry.displayHealth)">{{ entry.displayHealth.replaceAll('_', ' ') }}</UiBadge>
                    <UiBadge v-if="entry.cooldownBadge" :tone="entry.cooldownBadge.tone">{{ entry.cooldownBadge.label }}</UiBadge>
                    <UiBadge v-if="entry.userLimitActive" tone="amber">user limit</UiBadge>
                    <UiBadge v-if="entry.apiKeyLimitActive" tone="amber">API key limit</UiBadge>
                    <GuardrailBadge v-if="entry.guardrails.length" :count="entry.guardrails.length" :sources="['Provider, model, or assigned group binding']" />
                  </div>
                </td>
                <td class="px-3 py-2.5 text-slate-200/84">{{ entry.displayCooldownUntil ? relativeTime(entry.displayCooldownUntil) : '—' }}</td>
                <td class="px-3 py-2.5 text-slate-200/84">{{ entry.queueDepth }}</td>
                <td class="px-3 py-2.5 text-slate-200/84">{{ entry.inFlight }}</td>
                <td class="px-3 py-2.5 text-slate-200/84">{{ currencyMicros(entry.spendToday) }}</td>
              </tr>
            </UiTable>
          </div>
        </div>
      </section>

      <div class="space-y-6">
        <section v-if="canReadUsage" class="overflow-hidden px-1 pb-1 sm:px-1 sm:pb-1">
          <div class="pb-2 pt-6 sm:pt-8">
            <h3 class="text-[18px] font-semibold tracking-tight text-slate-50">Recent model usage</h3>
            <p class="mt-2 max-w-3xl text-sm leading-6 text-slate-200/78">{{ recentModelUsageScopeLabel }}</p>
          </div>
          <div v-if="recentModelUsage.length" class="max-h-[18rem] overflow-y-auto pr-1 pt-5 sm:pr-2">
            <div class="space-y-3">
              <DashboardRecentModelUsageCard
                v-for="entry in recentModelUsage"
                :key="entry.endpoint.id"
                :endpoint-id="entry.endpoint.id"
                :provider-name="entry.provider?.name || 'Provider missing'"
                :model-name="entry.endpoint.name"
                :queue-depth="entry.queueDepth"
                :cooldown-remaining="entry.cooldownRemaining"
                :health-status="entry.displayHealth"
                :health-tone="healthTone(entry.displayHealth)"
                :cooldown-label="entry.cooldownBadge?.label"
                :cooldown-tone="entry.cooldownBadge?.tone"
                :user-limit-active="entry.userLimitActive"
                :api-key-limit-active="entry.apiKeyLimitActive"
                :last-active-label="entry.lastActiveLabel"
                :limits="entry.limits"
              >
                <template v-if="$slots['model-usage-limit-label']" #limit-label="{ limit }">
                  <slot name="model-usage-limit-label" :limit="limit" />
                </template>
              </DashboardRecentModelUsageCard>
            </div>
          </div>
          <div v-else class="app-subsurface mt-5 p-4 text-sm leading-6 text-slate-300/72">Recently used models appear here after real traffic touches them.</div>
        </section>

        <section v-if="canReadUsage" class="overflow-hidden px-1 pb-1 sm:px-1 sm:pb-1">
          <div class="pb-2 pt-6 sm:pt-8">
            <h3 class="text-[18px] font-semibold tracking-tight text-slate-50">Recent request activity</h3>
            <p class="mt-2 max-w-3xl text-sm leading-6 text-slate-200/78">Recent calls, grouped by routing group and selected model, updating as queue state changes.</p>
          </div>
          <div v-if="activityFeed.length" class="max-h-[52rem] overflow-y-auto pr-1 pt-5 sm:pr-2">
            <TransitionGroup name="activity-feed" tag="div" class="space-y-3">
              <DashboardRecentRequestActivityCard v-for="item in activityFeed" :key="item.id" :item="item">
                <template v-if="$slots['activity-requester']" #requester><slot name="activity-requester" :item="item" /></template>
              </DashboardRecentRequestActivityCard>
            </TransitionGroup>
          </div>
          <div v-else class="app-subsurface mt-5 p-4 text-sm leading-6 text-slate-300/72">Recent request activity appears here once requests begin moving through the gateway.</div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.activity-feed-enter-active,
.activity-feed-leave-active,
.activity-feed-move {
  transition: transform 220ms ease, opacity 220ms ease;
}

.activity-feed-enter-from {
  opacity: 0;
  transform: translateY(-14px);
}

.activity-feed-leave-to {
  opacity: 0;
  transform: translateY(10px);
}
</style>
