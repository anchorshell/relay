<script setup lang="ts">
import type { CandidateTrace, CapacityLimitRow, Endpoint, LaneMembership, Metric, Period, RecentFlowRequest, RecentModelUsage, RequestLog } from '~/types/admin'
import { explicitProviderModelTarget, recentModelUsageLabels } from '../../../utils/requestTarget'
import { requestTimingBreakdown } from '../../../utils/requestTiming'

type FlowMode = 'live' | 'setup' | 'preview'
type TokenTone = 'queued' | 'active' | 'fallback' | 'completed' | 'failed'
type NodeTone = 'idle' | 'selected' | 'rejected' | 'muted' | 'active' | 'cooling'
type BadgeTone = 'slate' | 'emerald' | 'amber' | 'rose' | 'sky' | 'violet' | 'status-emerald' | 'status-amber' | 'status-rose'

const props = defineProps<{
  capacityRowFilter?: (row: CapacityLimitRow) => boolean
  capacityRowTransform?: (row: CapacityLimitRow) => CapacityLimitRow
}>()

type FlowToken = {
  id: string
  title: string
  subtitle?: string
  detail?: string
  x: number
  y: number
  tone: TokenTone
  compact?: boolean
  pulse?: boolean
}

type CompletionFeedItem = {
  id: string
  actorId: string
  apiKeyUUID: string
  title: string
  detail: string
  state: string
  tone: BadgeTone
  queueTiming: string
  timing: string
  phase: string
  tokens: string
  statusCode: number
}

type CircuitRouteVisualState =
  | 'idle'
  | 'active'
  | 'holding'
  | 'blocked'
  | 'cooldown'
  | 'fallback-active'
  | 'completed'
  | 'cancelled'
  | 'error'

type FlowPathLayer = 'skeleton' | 'overlay'

type FlowPath = {
  id: string
  d: string
  layer: FlowPathLayer
  active: boolean
  packet: boolean
  tone: CircuitRouteVisualState
  labelPoint: FlowPoint
}

type FlowPoint = {
  x: number
  y: number
}

type RouteBlockKind = 'cooldown' | 'rate_limited' | 'unhealthy' | 'queued' | 'rejected'

type QueueSlotView = {
  id: string
  actorId: string
  apiKeyUUID: string
  title: string
  subtitle: string
  detail: string
  tone: TokenTone
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

type QueueMotionPhase = 'stable' | 'moving' | 'entering' | 'leaving'

type QueueMotionItem = QueueSlotView & {
  currentIndex: number
  targetIndex: number
  phase: QueueMotionPhase
  leavingToBay?: boolean
}

type ActiveBayView = {
  id: string
  actorId: string
  apiKeyUUID: string
  subtitle: string
  detail: string
  state: string
  tone: CircuitRouteVisualState
}

type ModelActivityState = 'idle' | 'routing' | 'fallback-active' | 'holding' | 'blocked' | 'completed'
type ModelCapacityState = 'healthy' | 'cooling-down' | 'rate-limited' | 'unhealthy' | 'unavailable' | 'unknown'

type RuntimeRequestView = {
  requestID: string
  actorID: string
  apiKeyUUID: string
  taskID: string
  endpointID: string
  endpointName: string
  providerID: string
  incomingModel: string
  selectedUpstreamModel: string
  lane: string
  state: string
  substatus: string
  queuedAt: string
  startedAt: string
  finishedAt: string
  waitMs: number
  statusCode: number
  uploadedTokens: number
  downloadedTokens: number
  fallbackCount: number
  candidateTrace: CandidateTrace[]
  updatedAt: string
}

type LiveFlowCanonicalState = {
  activeRequest: RuntimeRequestView | null
  activeRequests: RuntimeRequestView[]
  activeEndpointID: string
  activeRequestID: string
  fallbackActive: boolean
  candidateTrace: CandidateTrace[]
  selectedCandidate: CandidateTrace | null
  candidateByEndpointID: Map<string, CandidateTrace>
  blockedEndpointKinds: Map<string, RouteBlockKind>
  holding: boolean
  holdingUserDeferred: boolean
  holdingEndpointID: string
}

type VisualTokenPhase = 'active' | 'terminal'

type ModelNodeView = {
  membership: LaneMembership
  endpoint: Endpoint
  index: number
  x: number
  y: number
  candidate: CandidateTrace | null
  tone: NodeTone
  activityState: ModelActivityState
  capacityState: ModelCapacityState
  queueDepth: number
  cooldownRemaining: number
  healthStatus: string
  availabilityIssue: string
  statusLabel: string
  statusTone: BadgeTone
  badges: Array<{
    key: string
    label: string
    tone: BadgeTone
  }>
  limits: Array<{
    key: string
    label: string
    metric: Metric
    period: Period
    source: string
    actorId: string
    scopeId: string
    targetType: string
    targetKey: string
    configured: number
    used: number
    percent: number
    blocked: boolean
    blockedUntil: string
    blockedRemainingMs: number
  }>
}

type RecentFlowActivityTarget = {
  key: string
  laneId?: string
  endpointId?: string
}

type FlowOverviewRow = {
  value: string
  kind: 'group' | 'model'
  title: string
  subtitle: string
  lastUsedAt: string
  queued: number
  models: number
  active: number
  completed: number
  badges: Array<{
    key: string
    label: string
    tone: BadgeTone
  }>
}

const catalog = useCatalogStore()
const queue = useQueueStore()
const capacity = useCapacityStore()
const requests = useRequestsStore()
const pageData = useAdminPageDataStore()
const telemetry = useTelemetryStore()
const preview = useLiveFlowPreviewStore()
const api = useRelayApi()
const app = useAppStore()
const { number, durationMs, clockTime, countdownMs, relativeTime } = useFormatters()
const { PERIODS, modelLimitRow } = useRelayUi()
const { refreshModelLimitRows, scheduleModelLimitRowsRefresh } = useModelLimitRows()
const { modelRelayPath } = useModelRelayRoute()
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

useHead({
  title: 'Realtime Flow | Model Relay'
})

const mode = ref<FlowMode>('live')
const selectedLaneID = ref('')
const selectedFlowTargetValue = ref('overview')
const groupSettingsOpen = ref(false)
const modelSettingsEndpointID = ref('')
const previewTrafficRunning = computed(() => preview.enqueueRunning)
const previewPlanLoading = computed(() => preview.loading)
const requestQueuedAtMemory = reactive<Record<string, string>>({})
const nowTick = ref(Date.now())
const recentFlowRequests = ref<RecentFlowRequest[]>([])
const recentModelUsageRows = ref<RecentModelUsage[]>([])
const recentModelUsageLoaded = ref(false)
let tickTimer: ReturnType<typeof setInterval> | null = null
let applyingFlowTargetSelection = false
let initialFlowTargetSelectionApplied = false
let recentFlowActivityRequestID = 0
const visualTokenPhaseStartedAt: Record<string, Partial<Record<VisualTokenPhase, number>>> = {}
const visualTokenLastSeen: Record<string, number> = {}
const destroyPreviewSessionOnUnload = () => preview.destroySessionOnUnload()
const stageRef = ref<HTMLElement | null>(null)
const flowPorts = ref<Record<string, FlowPoint>>({})
const flowStageSize = reactive({ width: 1, height: 1 })
const debugFlowPorts = ref(false)
const debugLiveFlowEvents = ref(false)
let flowResizeObserver: ResizeObserver | null = null
let flowMeasureFrame = 0
let observedFlowPortElements = new Set<Element>()

const FLOW_QUEUE_X = 10.5
const FLOW_QUEUE_PORT_X = 19.5
const FLOW_QUEUE_EXIT_Y = 20
const FLOW_TRUNK_X = 24
const FLOW_MODEL_X = 49
const FLOW_MODEL_BAY_BASE_Y_OFFSET = 9.2
const FLOW_MODEL_BAY_LIMIT_ROW_OFFSET = 0.7
const FLOW_MODEL_BAY_PORT_OFFSET = 5.7
const FLOW_OUTPUT_X = 68
const FLOW_COMPLETED_ENTRY_Y = 20
const FLOW_COMPLETED_DOCK_X = 71.5
const FLOW_QUEUE_TOP = 27
const FLOW_QUEUE_STEP = 6.6
const FLOW_TO_MODEL_MS = 1600
const FLOW_TO_COMPLETED_MS = 950

const PREVIEW_REQUEST_COUNT = 72
const PREVIEW_ARRIVAL_INTERVAL_MS = 250
const PREVIEW_SERVICE_MS = 1800
const QUEUE_VISIBLE_SLOT_COUNT = 6
const QUEUE_CARD_HEIGHT_PX = 68
const QUEUE_CARD_GAP_PX = 12
const QUEUE_ROW_PITCH_PX = QUEUE_CARD_HEIGHT_PX + QUEUE_CARD_GAP_PX
const QUEUE_STACK_HEIGHT_PX = (QUEUE_CARD_HEIGHT_PX * QUEUE_VISIBLE_SLOT_COUNT) + (QUEUE_CARD_GAP_PX * (QUEUE_VISIBLE_SLOT_COUNT - 1))
const PREVIEW_MAX_VISIBLE_QUEUE_REQUESTS = 9
const PREVIEW_MAX_VISIBLE_TRAILING_REQUESTS = 10
const LIVE_MAX_VISIBLE_QUEUE_REQUESTS = 9
const RECENT_COMPLETION_LIMIT = 10

const modeOptions: Array<{ value: FlowMode, label: string }> = [
  { value: 'live', label: 'Live' },
  { value: 'setup', label: 'Settings' },
  { value: 'preview', label: 'Preview' }
]

const {
  visibleLanes,
  targetGroups: flowTargetOptionGroups,
  targetOptions: flowTargetOptions,
  targetOptionByValue: flowTargetOptionByValue,
  endpointProviderSlug: routingTargetProviderSlug,
  modelTitle,
  firstLaneForEndpoint
} = useRoutingTargetOptions()

const realtimeFlowTargetOptionGroups = computed(() => [
  [{
    label: 'Overview',
    value: 'overview',
    description: '',
    kind: 'group' as const,
    laneID: '',
    endpointID: ''
  }],
  ...flowTargetOptionGroups.value
])

const selectedFlowTargetIsOverview = computed(() => selectedFlowTargetValue.value === 'overview')
const selectedFlowTargetEndpointID = computed(() => {
  const [kind, rawID] = selectedFlowTargetValue.value.split(':')
  const endpointID = String(rawID || '')
  return kind === 'model' && endpointID ? endpointID : ''
})
const selectedFlowTargetIsModel = computed(() => Boolean(selectedFlowTargetEndpointID.value))
const selectedFlowTargetEndpoint = computed(() => selectedFlowTargetEndpointID.value ? catalog.endpointMap[selectedFlowTargetEndpointID.value] || null : null)
const selectedLane = computed(() => visibleLanes.value.find((lane) => lane.id === selectedLaneID.value) || visibleLanes.value[0] || null)
const selectedFlowTargetIsSmartGroup = computed(() => !selectedFlowTargetIsOverview.value && !selectedFlowTargetIsModel.value && /^smart\//i.test(selectedLane.value?.name?.trim() || ''))
const selectedFlowTargetSupportsConcurrentRequests = computed(() => selectedFlowTargetIsModel.value || selectedFlowTargetIsSmartGroup.value)
const selectedMemberships = computed(() => {
  const endpointID = selectedFlowTargetEndpointID.value
  const memberships = selectedLane.value ? catalog.membershipsByLane(selectedLane.value.id).filter((item) => item.enabled) : []
  if (!endpointID) return memberships

  const scopedMemberships = memberships.filter((item) => item.endpoint_id === endpointID)
  if (scopedMemberships.length) return scopedMemberships

  const endpoint = catalog.endpointMap[endpointID]
  if (!endpoint) return []
  return [{
    id: `virtual:${endpointID}`,
    lane_id: selectedLane.value?.id || '',
    endpoint_id: endpointID,
    manual_rank: 0,
    enabled: true,
    created_at: endpoint.created_at,
    updated_at: endpoint.updated_at
  }]
})
const selectedEndpoints = computed(() =>
  selectedMemberships.value
    .map((membership) => catalog.endpointMap[membership.endpoint_id])
    .filter((endpoint): endpoint is Endpoint => Boolean(endpoint))
)
const selectedRoutableEndpoints = computed(() => selectedEndpoints.value.filter((endpoint) => !modelAvailabilityIssue(endpoint)))
const selectedRecentFlowActivityKey = computed(() => selectedRecentFlowActivityTarget()?.key || '')

const selectedFlowQueueItems = computed(() =>
  queue.items.filter((item) => requestMatchesSelectedFlowTarget(item.lane, item.endpoint_id))
)

function parseMs(value?: string | null) {
  if (!value) return 0
  const parsed = new Date(value).getTime()
  return Number.isFinite(parsed) ? parsed : 0
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

function requestUserLimitDetail(item: RequestDeferSource) {
  if (!isUserRequestDefer(item)) return ''
  const typeLabel = requestUserLimitTypeLabel(item)
  const countdown = requestUserLimitCountdown(item)
  return [
    `Blocked by User Limit${typeLabel ? ` ${typeLabel}` : ''}`,
    countdown
  ].filter(Boolean).join(' · ')
}

function endpointUserLimitBadgeActive(endpoint: Endpoint) {
  return mode.value !== 'preview' && endpointReachedLimitRows(endpoint)
    .some(row => row.source !== 'pro_api_key_limit')
}

function endpointAPIKeyLimitBadgeActive(endpoint: Endpoint) {
  return mode.value !== 'preview' && endpointReachedLimitRows(endpoint)
    .some(row => row.source === 'pro_api_key_limit')
}

function endpointReachedLimitRows(endpoint: Endpoint) {
  return capacityRowsForEndpoint(endpoint.id).filter((row) => {
    if (!row.user_scoped) return false
    const effective = Number(row.effective || row.configured || 0)
    return Boolean(row.blocked) || (effective > 0 && Number(row.used || 0) >= effective) || Number(row.percent || 0) >= 99.5
  })
}

function visualPhaseStartedAtISO(requestID: string, phase: VisualTokenPhase) {
  if (!requestID) return new Date(nowTick.value).toISOString()
  const entry = visualTokenPhaseStartedAt[requestID] || {}
  if (!entry[phase]) {
    entry[phase] = nowTick.value
    visualTokenPhaseStartedAt[requestID] = entry
  }
  return new Date(entry[phase] || nowTick.value).toISOString()
}

function shouldRetainVisualToken(token: FlowToken, now: number) {
  const lastSeen = visualTokenLastSeen[token.id] || 0
  const age = now - lastSeen
  if (age < 0) return false
  if (token.tone === 'queued') return age < 450
  if (token.tone === 'active' || token.tone === 'fallback') return age < 1200
  return false
}

function clearVisualTokenMemory() {
  for (const key of Object.keys(visualTokenPhaseStartedAt)) {
    delete visualTokenPhaseStartedAt[key]
  }
  for (const key of Object.keys(visualTokenLastSeen)) {
    delete visualTokenLastSeen[key]
  }
}

function queueReadyMs(item: { state: string, predicted_eligible_at?: string | null, queued_at: string }) {
  if (item.state === 'ready') return 0
  return parseMs(item.predicted_eligible_at || item.queued_at)
}

function compareQueuedItems(
  a: { state: string, predicted_eligible_at?: string | null, queued_at: string },
  b: { state: string, predicted_eligible_at?: string | null, queued_at: string }
) {
  const readyDelta = queueReadyMs(a) - queueReadyMs(b)
  if (readyDelta !== 0) return readyDelta
  return parseMs(a.queued_at) - parseMs(b.queued_at)
}

const queuedItems = computed(() =>
  selectedFlowQueueItems.value
    .filter((item) => item.state !== 'in_flight')
    .slice()
    .sort(compareQueuedItems)
    .slice(0, LIVE_MAX_VISIBLE_QUEUE_REQUESTS)
)

const actionItems = computed(() => {
  const queueItems = selectedFlowQueueItems.value
    .filter((item) => item.state === 'in_flight')
    .map((item) => ({
      requestID: item.request_id,
      endpointID: item.endpoint_id,
      endpointName: item.endpoint_name,
      providerID: item.provider_id,
      incomingModel: item.incoming_model || item.lane || 'Request',
      state: item.state,
      substatus: item.substatus || 'dispatching',
      tokensUp: item.uploaded_tokens || 0,
      tokensDown: item.downloaded_tokens || 0,
      startedAt: item.started_at || item.queued_at,
      fallbackCount: item.fallback_count || 0
    }))

  const queueRequestIDs = new Set(queueItems.map((item) => item.requestID))
  const progressItems = Object.values(telemetry.requestProgress)
    .filter((item) => item.state === 'in_flight' && !queueRequestIDs.has(item.request_id))
    .filter((item) => requestMatchesSelectedFlowTarget(item.lane || '', String(item.endpoint_id || '')))
    .map((item) => ({
      requestID: item.request_id,
      endpointID: String(item.endpoint_id || ''),
      endpointName: item.endpoint_name || item.selected_upstream_model || 'Selected model',
      providerID: String(item.provider_id || ''),
      incomingModel: item.incoming_model || item.lane || 'Request',
      state: item.state || 'in_flight',
      substatus: item.substatus || 'streaming',
      tokensUp: Number(item.uploaded_tokens || 0),
      tokensDown: Number(item.downloaded_tokens || 0),
      startedAt: item.started_at || item.queued_at || item.updated_at || '',
      fallbackCount: 0
    }))

  return [...queueItems, ...progressItems].slice(0, 12)
})

const terminalProgressItems = computed(() =>
  Object.values(telemetry.requestProgress)
    .filter((item) => item.state === 'completed' || item.state === 'failed' || item.state === 'cancelled')
    .filter((item) => requestMatchesSelectedFlowTarget(item.lane || '', String(item.endpoint_id || '')))
    .slice(0, 5)
)

function recentFlowRequestFromLog(log: RequestLog): RecentFlowRequest | null {
  if (!log.request_id || !isTerminalState(log.task_state)) return null
  const timings = requestTimingBreakdown(log)
  return {
    request_id: log.request_id,
    actor_id: log.actor_id,
    api_key_uuid: log.api_key_uuid,
    lane_id: log.lane_id ?? null,
    endpoint_id: log.endpoint_id ?? null,
    provider_id: log.provider_id ?? null,
    incoming_model: log.incoming_model,
    selected_upstream_model: log.selected_upstream_model,
    status_code: Number(log.status_code || 0),
    task_state: log.task_state,
    queued_at: log.queued_at ?? null,
    started_at: log.started_at ?? null,
    finished_at: log.finished_at ?? null,
    wait_ms: Number(log.wait_ms || 0),
    latency_ms: Number(log.latency_ms || 0),
    characterization_duration_ms: Number(timings.characterizationMS || 0),
    guardrail_status: String(log.guardrail_status || ''),
    guardrail_pre_duration_ms: Number(timings.guardrailPreMS || 0),
    provider_latency_ms: Number(timings.providerLatencyMS || 0),
    guardrail_post_duration_ms: Number(timings.guardrailPostMS || 0),
    total_time_ms: Number(timings.totalMS || 0),
    fallback_count: Number(log.fallback_count || 0),
    estimated_input_tokens: Number(log.estimated_input_tokens || 0),
    estimated_output_tokens: Number(log.estimated_output_tokens || 0),
    actual_input_tokens: Number(log.actual_input_tokens || 0),
    actual_output_tokens: Number(log.actual_output_tokens || 0),
    actual_total_tokens: Number(log.actual_total_tokens || 0),
    estimated_cost_micros: Number(log.estimated_cost_micros || 0),
    actual_cost_micros: Number(log.actual_cost_micros || 0),
    error_text: log.error_text || '',
    created_at: log.created_at,
    updated_at: log.updated_at
  }
}

const liveRecentFlowRequests = computed(() =>
  requests.logs
    .map((log) => recentFlowRequestFromLog(log))
    .filter((item): item is RecentFlowRequest => !!item)
)

const flowRequests = computed(() => {
  const target = selectedRecentFlowActivityTarget()
  const byRequestID = new Map<string, RecentFlowRequest>()
  for (const item of recentFlowRequests.value) {
    if (!recentFlowRequestMatchesTarget(item, target)) continue
    byRequestID.set(item.request_id, item)
  }
  for (const item of liveRecentFlowRequests.value) {
    if (!recentFlowRequestMatchesTarget(item, target)) continue
    byRequestID.set(item.request_id, item)
  }
  return [...byRequestID.values()]
    .sort((a, b) => recentRequestMs(b) - recentRequestMs(a))
    .slice(0, RECENT_COMPLETION_LIMIT)
})

function recentModelUsageFromRequest(log: RecentFlowRequest): RecentModelUsage | null {
  if (!log.endpoint_id && !log.lane_id && !log.incoming_model && !log.selected_upstream_model) return null
  const lane = resolveRecentRequestLane(log)
  const endpoint = log.endpoint_id ? catalog.endpointMap[log.endpoint_id] : null
  const provider = endpoint
    ? catalog.providerMap[endpoint.provider_id]
    : (log.provider_id ? catalog.providerMap[log.provider_id] : null)
  const laneID = lane?.id || log.lane_id || ''
  const modelName = endpoint
    ? modelTitle(endpoint)
    : log.selected_upstream_model || log.incoming_model || 'Unknown model'
  const labels = recentModelUsageLabels(provider?.name || providerName(log.provider_id), modelName)
  const endpointID = endpoint?.id || log.endpoint_id || ''
  return {
    key: `${laneID}:${endpointID || modelName}`,
    lane_id: laneID,
    lane_name: lane?.name || '',
    endpoint_id: endpointID,
    provider_id: provider?.id || log.provider_id || '',
    provider_name: labels.provider,
    model_name: labels.model,
    last_used_at: new Date(recentRequestMs(log) || Date.now()).toISOString(),
    request_count: 1
  }
}

const liveRecentModelUsage = computed(() => {
  const entries = new Map<string, RecentModelUsage>()
  for (const log of liveRecentFlowRequests.value) {
    const entry = recentModelUsageFromRequest(log)
    if (!entry) continue
    const existing = entries.get(entry.key)
    if (!existing) {
      entries.set(entry.key, entry)
      continue
    }
    existing.request_count += 1
    if (parseMs(entry.last_used_at) > parseMs(existing.last_used_at)) {
      entries.set(entry.key, {
        ...existing,
        ...entry,
        request_count: existing.request_count
      })
    }
  }
  return [...entries.values()]
})

const completedEvents = computed(() =>
  flowRequests.value
    .filter((log) => isTerminalState(log.task_state))
    .slice()
    .sort((a, b) => recentRequestMs(b) - recentRequestMs(a))
    .slice(0, RECENT_COMPLETION_LIMIT)
)

const liveFlowState = computed<LiveFlowCanonicalState>(() => buildLiveFlowCanonicalState())
const activeRuntimeRequests = computed(() => liveFlowState.value.activeRequests)
const activeTrace = computed(() => liveFlowState.value.candidateTrace)
const visibleCandidates = computed(() => activeTrace.value)

const previewQueuedCount = computed(() =>
  preview.queuedCount
)

const previewActiveCount = computed(() =>
  preview.activeCount
)

const fallbackCount = computed(() => {
  if (mode.value === 'preview') {
    return preview.fallbackCount
  }
  return Math.max(
    ...selectedFlowQueueItems.value.map((item) => item.fallback_count || 0),
    ...completedEvents.value.map((item) => item.fallback_count || 0),
    0
  )
})

const liveQueuedCount = computed(() =>
  queue.queueSnapshot ? queue.queuedCount : selectedFlowQueueItems.value.filter((item) => item.state !== 'in_flight').length
)

const modelNodes = computed<ModelNodeView[]>(() => {
  const endpointCount = selectedEndpoints.value.length || 1
  return selectedMemberships.value
    .flatMap((membership, index): ModelNodeView[] => {
      const endpoint = catalog.endpointMap[membership.endpoint_id]
      if (!endpoint) return []
      const point = modelCoordinate(index, endpointCount)
      const candidate = candidateForEndpoint(endpoint.id)
      const availabilityIssue = modelAvailabilityIssue(endpoint)
      const status = modelHealthStatus(endpoint)
      const capacityState = modelCapacityState(endpoint)
      const activityState = modelActivityState(endpoint.id)
      const cooldownRemaining = modelCooldownRemaining(endpoint)
      return [{
        membership,
        endpoint,
        index,
        x: point.x,
        y: point.y,
        candidate,
        tone: nodeTone(endpoint.id, candidate, activityState, capacityState, cooldownRemaining),
        activityState,
        capacityState,
        queueDepth: modelQueueDepth(endpoint.id),
        cooldownRemaining,
        healthStatus: status,
        availabilityIssue,
        statusLabel: modelStatusLabel(activityState, capacityState, cooldownRemaining, availabilityIssue),
        statusTone: modelStatusTone(activityState, capacityState, cooldownRemaining),
        badges: modelFeatureBadges(endpoint),
        limits: modelUsageLimits(endpoint)
      }]
    })
})

const routePaths = computed<FlowPath[]>(() => buildMeasuredRoutePaths())

const skeletonRoutePaths = computed(() => routePaths.value.filter((path) => path.layer === 'skeleton'))
const overlayRoutePaths = computed(() => routePaths.value.filter((path) => path.layer === 'overlay'))
const packetRoutePaths = computed(() => overlayRoutePaths.value.filter((path) => path.packet))
const circuitReady = computed(() => skeletonRoutePaths.value.length > 0)

const debugMissingFlowAnchors = computed(() => {
  const required = [
    'queue-out',
    'active-bay-in',
    'active-bay-out',
    'completed-in',
    ...(modelNodes.value.length
      ? modelNodes.value.flatMap((node) => [
          `model-${node.endpoint.id}-in`,
          `model-${node.endpoint.id}-out`,
          `model-${node.endpoint.id}-center`,
          `model-${node.endpoint.id}-rank`,
          `model-${node.endpoint.id}-status`
        ])
      : [
          'no-model-card-in',
          'no-model-card-out'
        ])
  ]
  return required.filter((name) => !flowPorts.value[name])
})

const debugLiveFlowEventItems = computed(() =>
  (mode.value === 'preview' ? preview.events : telemetry.events).slice(0, 8)
)

const liveTokens = computed<FlowToken[]>(() => {
  const queued = queuedItems.value.map((item, index) => {
    const point = queueCoordinate(index)
    return {
      id: item.request_id,
      title: item.incoming_model || item.lane || 'Request',
      subtitle: isUserRequestDefer(item) ? 'request-level block' : shortID(item.request_id),
      detail: requestUserLimitDetail(item) || `queued · ${durationMs(item.wait_ms)}`,
      x: point.x,
      y: point.y,
      tone: isUserRequestDefer(item) ? ('fallback' as TokenTone) : ('queued' as TokenTone),
      compact: true,
      pulse: true
    }
  })

  const active = actionItems.value.map((item, index) => {
    const point = activeRequestCoordinate(item.endpointID, index, visualPhaseStartedAtISO(item.requestID, 'active'))
    return {
      id: item.requestID,
      title: item.incomingModel,
      subtitle: item.endpointName || providerName(item.providerID),
      detail: `${item.substatus} · ${number(item.tokensUp + item.tokensDown)} tok`,
      x: point.x,
      y: point.y,
      tone: item.fallbackCount > 0 ? 'fallback' as TokenTone : 'active' as TokenTone,
      compact: true,
      pulse: true
    }
  })

  const terminal = terminalProgressItems.value.map((item, index) => {
    const point = terminalRequestCoordinate(String(item.endpoint_id || ''), index, visualPhaseStartedAtISO(item.request_id, 'terminal'))
    return {
      id: item.request_id,
      title: item.incoming_model || item.selected_upstream_model || 'Request',
      subtitle: shortID(item.request_id),
      detail: item.state || 'finished',
      x: point.x,
      y: point.y,
      tone: item.state === 'failed' ? 'failed' as TokenTone : 'completed' as TokenTone,
      compact: true
    }
  })

  return [...queued, ...active, ...terminal].slice(0, 18)
})

const previewTokens = computed<FlowToken[]>(() => {
  const queuedItems = preview.queueItems
    .filter((item) => item.state === 'queued' || item.state === 'waiting' || item.state === 'ready')
    .slice()
    .sort(compareQueueItems)

  const queued = queuedItems
    .slice(0, PREVIEW_MAX_VISIBLE_QUEUE_REQUESTS)
    .map((item, index) => {
      const point = queueCoordinate(index)
      return {
        id: item.request_id,
        title: previewRequestTitle(item.request_id),
        subtitle: item.lane || item.incoming_model || 'preview',
        detail: previewQueueItemDetail(item),
        x: point.x,
        y: point.y,
        tone: isUserRequestDefer(item) || item.state === 'waiting' ? 'fallback' as TokenTone : 'queued' as TokenTone,
        compact: true,
        pulse: true
      }
    })

  const active = preview.queueItems
    .filter((item) => item.state === 'in_flight')
    .slice()
    .sort(compareQueueItems)
    .slice(0, PREVIEW_MAX_VISIBLE_TRAILING_REQUESTS)
    .map((item, index) => {
      const progress = preview.requestProgress[item.request_id]
      const point = activeRequestCoordinate(item.endpoint_id, index, visualPhaseStartedAtISO(item.request_id, 'active'))
      const tokens = Number(progress?.uploaded_tokens || item.uploaded_tokens || 0) + Number(progress?.downloaded_tokens || item.downloaded_tokens || 0)
      return {
        id: item.request_id,
        title: previewRequestTitle(item.request_id),
        subtitle: item.endpoint_name || providerName(item.provider_id),
        detail: `${progress?.substatus || item.substatus || 'dispatching'} · ${number(tokens)} tok`,
        x: point.x,
        y: point.y,
        tone: item.fallback_count > 0 ? 'fallback' as TokenTone : 'active' as TokenTone,
        compact: true,
        pulse: true
      }
    })

  const terminal = preview.logs
    .filter((item) => item.finished_at && nowTick.value - parseMs(item.finished_at) < 2600)
    .slice(0, 5)
    .map((item, index) => {
      const point = terminalRequestCoordinate(String(item.endpoint_id || ''), index, visualPhaseStartedAtISO(item.request_id, 'terminal'))
      return {
        id: item.request_id,
        title: previewRequestTitle(item.request_id),
        subtitle: item.selected_upstream_model || shortID(item.request_id),
        detail: item.task_state || 'finished',
        x: point.x,
        y: point.y,
        tone: item.task_state === 'failed' ? 'failed' as TokenTone : 'completed' as TokenTone,
        compact: true
      }
    })

  return [...queued, ...active, ...terminal].slice(0, 18)
})

const rawVisibleTokens = computed(() => {
  if (mode.value === 'preview') {
    return previewTokens.value
  }
  if (mode.value === 'live') return liveTokens.value
  return []
})

const visibleTokens = ref<FlowToken[]>([])

watch(rawVisibleTokens, (tokens) => {
  const now = nowTick.value
  const incomingIDs = new Set(tokens.map((token) => token.id))
  for (const token of tokens) {
    visualTokenLastSeen[token.id] = now
  }

  const retained = visibleTokens.value.filter((token) =>
    !incomingIDs.has(token.id) && shouldRetainVisualToken(token, now)
  )
  visibleTokens.value = [...tokens, ...retained].slice(0, 18)
}, { immediate: true })

const completionFeed = computed<CompletionFeedItem[]>(() => {
  const actorByRequestID = new Map(requests.logs.map(log => [log.request_id, String(log.actor_id || '')]))
  const sourceEvents = mode.value === 'preview' ? preview.logs : completedEvents.value
  const real = sourceEvents.map((event) => {
    const startedAt = event.started_at || event.created_at
    const finishedAt = event.finished_at || event.updated_at || event.created_at
    const queuedMs = completionQueuedMs(event)
    const queuedAtLabel = queuedMs > 0 ? clockTime(new Date(queuedMs).toISOString()) : ''
    const startedMs = parseMs(startedAt)
    const finishedMs = parseMs(finishedAt)
    const waitMs = completionWaitMs(event, queuedMs, startedMs)
    const duration = event.total_time_ms && event.total_time_ms > 0
      ? event.total_time_ms
      : Math.max(0, waitMs) + (event.latency_ms > 0 ? event.latency_ms : Math.max(0, finishedMs - startedMs)) + Math.max(0, Number(event.characterization_duration_ms || 0))
    const actualUsage = hasActualUsage(event)
    const inputTokens = actualUsage ? event.actual_input_tokens : event.estimated_input_tokens
    const outputTokens = actualUsage ? event.actual_output_tokens : event.estimated_output_tokens
    const canEstimateUsage = event.task_state === 'completed' && event.status_code >= 200 && event.status_code < 300
    const tokenSummary = actualUsage
      ? `Up ${number(inputTokens)} tok · Down ${number(outputTokens)} tok`
      : canEstimateUsage
        ? `~Up ${number(inputTokens)} tok · ~Down ${number(outputTokens)} tok`
        : ''
    const routingGroup = routingGroupName(event.lane_id, event.incoming_model)
    const routeDetail = requestRouteDetail(event.provider_id, event.endpoint_id, event.incoming_model, '', event.status_code)

    return {
      id: event.request_id,
      actorId: String(event.actor_id || actorByRequestID.get(event.request_id) || ''),
      apiKeyUUID: String(event.api_key_uuid || requestAPIKeyUUID(event.request_id)),
      state: event.task_state,
      tone: taskStateTone(event.task_state),
      title: routingGroup,
      detail: routeDetail,
      queueTiming: waitMs > 0
        ? (queuedAtLabel
            ? `Queued ${queuedAtLabel} · Waited ${durationMs(waitMs)} before dispatch`
            : `Waited ${durationMs(waitMs)} before dispatch`)
        : (queuedAtLabel ? `Queued ${queuedAtLabel}` : ''),
      timing: `Start ${clockTime(startedAt)} · End ${clockTime(finishedAt)} · Duration ${durationMs(duration)}`,
      phase: event.error_text || '',
      tokens: tokenSummary,
      statusCode: event.status_code
    }
  })

  return real.slice(0, 10)
})

const recentFlowUsage = computed<RecentModelUsage[]>(() => {
  const entries = new Map<string, RecentModelUsage>()
  for (const item of recentModelUsageRows.value) {
    const labels = recentModelUsageLabels(item.provider_name, item.model_name)
    entries.set(item.key, {
      ...item,
      provider_name: labels.provider,
      model_name: labels.model
    })
  }
  for (const item of liveRecentModelUsage.value) {
    const existing = entries.get(item.key)
    if (!existing) {
      entries.set(item.key, item)
      continue
    }
    entries.set(item.key, {
      ...existing,
      ...item,
      request_count: existing.request_count + item.request_count,
      last_used_at: parseMs(item.last_used_at) > parseMs(existing.last_used_at) ? item.last_used_at : existing.last_used_at
    })
  }
  return [...entries.values()]
    .sort((a, b) => parseMs(b.last_used_at) - parseMs(a.last_used_at))
    .slice(0, 200)
})

function recentUsageRoute(entry: RecentModelUsage) {
  const endpoint = entry.endpoint_id ? catalog.endpointMap[entry.endpoint_id] : null
  const provider = catalog.providerMap[endpoint?.provider_id || entry.provider_id || '']
  const providerSlug = routeSlug(provider?.slug || entry.provider_name)
  const modelSlug = routeSlug(endpoint?.slug || entry.model_name)
  return `${providerSlug || 'provider-missing'}/${modelSlug || 'no-model-selected'}`
}

const OVERVIEW_RECENT_WINDOW_MS = 60 * 60 * 1000

function overviewLaneFromSource(source: { laneID?: string | null, laneName?: string | null, incomingModel?: string | null }) {
  const laneID = String(source.laneID || '')
  if (laneID && catalog.laneMap[laneID]) return catalog.laneMap[laneID]
  const laneName = String(source.laneName || source.incomingModel || '').trim().toLowerCase()
  if (!laneName) return null
  return catalog.lanes.find((lane) => lane.name.trim().toLowerCase() === laneName || lane.slug?.trim().toLowerCase() === laneName) || null
}

const liveOverviewTargetUsage = computed(() => {
  const byValue = new Map<string, string>()
  const remember = (value: string, usedAt?: string | null) => {
    if (!flowTargetOptionByValue.value[value]) return
    const timestamp = String(usedAt || new Date().toISOString())
    const existing = byValue.get(value)
    if (!existing || parseMs(timestamp) > parseMs(existing)) byValue.set(value, timestamp)
  }
  const rememberSource = (source: {
    laneID?: string | null
    laneName?: string | null
    incomingModel?: string | null
    endpointID?: string | null
    usedAt?: string | null
  }) => {
    const lane = overviewLaneFromSource(source)
    if (lane) remember(`group:${lane.id}`, source.usedAt)
    const endpointID = String(source.endpointID || '')
    if (endpointID) remember(`model:${endpointID}`, source.usedAt)
  }

  for (const item of queue.items) {
    rememberSource({
      laneName: item.lane,
      incomingModel: item.incoming_model,
      endpointID: item.endpoint_id,
      usedAt: item.started_at || item.queued_at
    })
  }
  for (const item of Object.values(telemetry.requestProgress)) {
    rememberSource({
      laneName: item.lane,
      incomingModel: item.incoming_model,
      endpointID: item.endpoint_id,
      usedAt: item.updated_at || item.finished_at || item.started_at || item.queued_at
    })
  }
  for (const event of telemetry.events) {
    if (!event.type.startsWith('request_')) continue
    rememberSource({
      laneID: String(event.payload?.lane_id || ''),
      laneName: String(event.payload?.lane || ''),
      incomingModel: String(event.payload?.incoming_model || ''),
      endpointID: String(event.payload?.endpoint_id || ''),
      usedAt: event.timestamp
    })
  }

  return [...byValue.entries()].map(([value, lastUsedAt]) => ({ value, lastUsedAt }))
})

const overviewTargetOptions = computed(() => {
  const byValue = new Map<string, { value: string, lastUsedAt: string }>()
  const sortedUsage = recentFlowUsage.value
    .filter((entry) => parseMs(entry.last_used_at) > 0)
    .slice()
    .sort((a, b) => parseMs(b.last_used_at) - parseMs(a.last_used_at))

  const rememberTarget = (value: string, lastUsedAt: string) => {
    if (!flowTargetOptionByValue.value[value]) return
    const existing = byValue.get(value)
    if (!existing || parseMs(lastUsedAt) > parseMs(existing.lastUsedAt)) {
      byValue.set(value, { value, lastUsedAt })
    }
  }

  for (const target of liveOverviewTargetUsage.value) rememberTarget(target.value, target.lastUsedAt)
  for (const entry of sortedUsage) {
    if (entry.lane_id) rememberTarget(`group:${entry.lane_id}`, entry.last_used_at)
    if (entry.endpoint_id) rememberTarget(`model:${entry.endpoint_id}`, entry.last_used_at)
  }

  const usedTargets = [...byValue.values()].sort((a, b) => parseMs(b.lastUsedAt) - parseMs(a.lastUsedAt))
  const cutoff = Date.now() - OVERVIEW_RECENT_WINDOW_MS
  const recentTargets = usedTargets.filter((target) => parseMs(target.lastUsedAt) >= cutoff)
  if (recentTargets.length) return recentTargets
  if (usedTargets.length) return usedTargets.slice(0, 5)

  return flowTargetOptions.value.slice(0, 5).map((option) => ({
    value: option.value,
    lastUsedAt: ''
  }))
})

function overviewTargetMatches(value: string, source: { laneID?: string | null, laneName?: string | null, endpointID?: string | null }) {
  const option = flowTargetOptionByValue.value[value]
  if (!option) return false
  if (option.kind === 'model') return Boolean(option.endpointID && source.endpointID === option.endpointID)
  if (source.laneID) return source.laneID === option.laneID
  const lane = catalog.laneMap[option.laneID]
  return Boolean(lane && source.laneName === lane.name)
}

function overviewActiveRequestIDs(value: string) {
  const ids = new Set<string>()
  for (const item of queue.items) {
    if (item.state !== 'in_flight') continue
    if (overviewTargetMatches(value, { laneName: item.lane, endpointID: item.endpoint_id })) ids.add(item.request_id)
  }
  for (const item of Object.values(telemetry.requestProgress)) {
    if (item.state !== 'in_flight') continue
    if (overviewTargetMatches(value, {
      laneName: String(item.lane || ''),
      endpointID: String(item.endpoint_id || '')
    })) ids.add(String(item.request_id || ''))
  }
  ids.delete('')
  return ids
}

const overviewCompletedRequests = computed(() => {
  const byRequestID = new Map<string, RecentFlowRequest>()
  for (const item of recentFlowRequests.value) byRequestID.set(item.request_id, item)
  for (const item of liveRecentFlowRequests.value) byRequestID.set(item.request_id, item)
  const cutoff = Date.now() - OVERVIEW_RECENT_WINDOW_MS
  return [...byRequestID.values()].filter((item) => isTerminalState(item.task_state) && recentRequestMs(item) >= cutoff)
})

function overviewModelBadges(endpoint: Endpoint) {
  const badges = modelFeatureBadges(endpoint).slice()
  const capacityState = modelCapacityState(endpoint)
  const cooldownRemaining = modelCooldownRemaining(endpoint)
  const hasRateLimitBadge = badges.some((badge) => badge.key.includes('rate') || badge.label.includes('rate limit'))
  if (capacityState === 'rate-limited' && !hasRateLimitBadge) {
    badges.push({ key: 'rate-limited', label: 'rate limited', tone: 'amber' })
  } else if (capacityState === 'cooling-down' && !badges.some((badge) => badge.key.includes('cooldown') || badge.label.includes('server'))) {
    badges.push({ key: 'cooling-down', label: 'cooling down', tone: 'amber' })
  } else if (capacityState === 'unhealthy') {
    badges.push({ key: 'unhealthy', label: 'unhealthy', tone: 'rose' })
  } else if (capacityState === 'unavailable') {
    badges.push({ key: 'unavailable', label: 'unavailable', tone: 'rose' })
  }

  const guardrailCount = catalog.effectiveGuardrailsForEndpointAcrossGroups(endpoint.id).length
  if (guardrailCount) {
    badges.push({
      key: 'guarded',
      label: guardrailCount > 1 ? `${guardrailCount} guardrails` : 'guarded',
      tone: 'sky'
    })
  }
  if (cooldownRemaining > 0) {
    badges.push({
      key: 'cooldown-timer',
      label: `cooldown ${countdownMs(cooldownRemaining)}`,
      tone: 'amber'
    })
  }
  return badges
}

const flowOverviewRows = computed<FlowOverviewRow[]>(() => overviewTargetOptions.value.flatMap(({ value, lastUsedAt }) => {
  const option = flowTargetOptionByValue.value[value]
  if (!option) return []
  const endpoint = option.endpointID ? catalog.endpointMap[option.endpointID] : null
  const lane = option.laneID ? catalog.laneMap[option.laneID] : null
  const provider = endpoint ? catalog.providerMap[endpoint.provider_id] : null
  const memberships = option.kind === 'group'
    ? catalog.membershipsByLane(option.laneID).filter((item) => item.enabled)
    : []
  const queued = queue.items.filter((item) => item.state !== 'in_flight' && overviewTargetMatches(value, {
    laneName: item.lane,
    endpointID: item.endpoint_id
  })).length
  const completed = overviewCompletedRequests.value.filter((item) => overviewTargetMatches(value, {
    laneID: item.lane_id,
    laneName: item.incoming_model,
    endpointID: item.endpoint_id
  })).length

  return [{
    value,
    kind: option.kind,
    title: option.kind === 'group' ? lane?.name || option.label : endpoint ? modelTitle(endpoint) : option.label,
    subtitle: option.kind === 'group'
      ? 'Routing group'
      : `${routeSlug(provider?.slug || provider?.name) || 'provider-missing'}/${routeSlug(endpoint?.slug || endpoint?.name) || 'no-model-selected'}`,
    lastUsedAt,
    queued,
    models: option.kind === 'group' ? memberships.length : 1,
    active: overviewActiveRequestIDs(value).size,
    completed,
    badges: option.kind === 'model' && endpoint ? overviewModelBadges(endpoint) : []
  }]
}))

const flowOverviewSections = computed(() => [
  {
    key: 'groups',
    label: 'Groups',
    rows: flowOverviewRows.value.filter((row) => row.kind === 'group')
  },
  {
    key: 'models',
    label: 'Models',
    rows: flowOverviewRows.value.filter((row) => row.kind === 'model')
  }
].filter((section) => section.rows.length))

const currentFlowTargetLabel = computed(() => {
  if (selectedFlowTargetIsOverview.value) return ''
  if (selectedFlowTargetEndpoint.value) {
    return `${routingTargetProviderSlug(selectedFlowTargetEndpoint.value)}/${modelTitle(selectedFlowTargetEndpoint.value)}`
  }
  return selectedLane.value?.name || 'Routing target'
})

const boardQueuedCount = computed(() => mode.value === 'preview' ? previewQueuedCount.value : liveQueuedCount.value)
const boardActiveCount = computed(() => activeRuntimeRequests.value.length)
const recentTerminalCount = computed(() => {
  if (mode.value === 'preview') {
    return preview.logs.filter((item) => terminalLogIsRecent(item)).length
  }
  return terminalProgressItems.value.filter((item) => terminalProgressIsRecent(item)).length
})
const previewPhaseText = computed(() => preview.phase)
const previewGeneratedDisplayCount = computed(() => preview.generatedCount)
const previewCompletedDisplayCount = computed(() => preview.completedCount)

const sourceQueueSlotItems = computed<QueueSlotView[]>(() => {
  if (mode.value === 'preview') {
    return preview.queueItems
      .filter((item) => item.state === 'queued' || item.state === 'waiting' || item.state === 'ready')
      .slice()
      .sort(compareQueueItems)
      .slice(0, QUEUE_VISIBLE_SLOT_COUNT)
      .map((item) => ({
        id: item.request_id,
        actorId: String(item.actor_id || ''),
        apiKeyUUID: '',
        title: previewRequestTitle(item.request_id),
        subtitle: item.lane || item.incoming_model || 'preview',
        detail: previewQueueItemDetail(item),
        tone: isUserRequestDefer(item) || item.state === 'waiting' ? 'fallback' : 'queued'
      }))
  }

  return queuedItems.value
    .slice(0, QUEUE_VISIBLE_SLOT_COUNT)
    .map((item) => ({
      id: item.request_id,
      actorId: String(item.actor_id || ''),
      apiKeyUUID: String(item.api_key_uuid || requestAPIKeyUUID(item.request_id)),
      title: item.incoming_model || item.lane || 'Request',
      subtitle: isUserRequestDefer(item) ? 'request-level block' : shortID(item.request_id),
      detail: requestUserLimitDetail(item) || queueElapsedDetail(item),
      tone: isUserRequestDefer(item) || item.state === 'waiting' ? 'fallback' : 'queued'
    }))
})

const queueOverflowCount = computed(() => {
  return Math.max(0, boardQueuedCount.value - sourceQueueSlotItems.value.length)
})

const queueMotionItems = ref<QueueMotionItem[]>([])
let queueMotionFrame = 0
let queueMotionSettleTimer: ReturnType<typeof setTimeout> | null = null
let queueMotionVersion = 0

function activeBayView(item: RuntimeRequestView): ActiveBayView {
  const isPrimaryActiveRequest = item.requestID === liveFlowState.value.activeRequestID
  const tone = selectedFlowTargetSupportsConcurrentRequests.value
    ? item.fallbackCount > 0 ? 'fallback-active' : 'active'
    : isPrimaryActiveRequest && liveFlowState.value.fallbackActive
      ? 'fallback-active'
      : activeBayToneForEndpoint(item.endpointID)
  return {
    id: item.requestID,
    actorId: item.actorID,
    apiKeyUUID: item.apiKeyUUID,
    subtitle: requestRouteDetail(item.providerID, item.endpointID, item.incomingModel, item.selectedUpstreamModel),
    detail: item.substatus || 'routing',
    state: 'routing',
    tone
  }
}

const activeBayItems = computed<ActiveBayView[]>(() => {
  const requests = selectedFlowTargetSupportsConcurrentRequests.value
    ? activeRuntimeRequests.value
    : activeRuntimeRequests.value.slice(0, 1)
  return requests.map(activeBayView)
})

const activeBayItem = computed<ActiveBayView | null>(() => activeBayItems.value[0] || null)

const activeBayTransitionKey = computed(() => {
  const item = activeBayItem.value
  if (!item) return 'empty'
  return `${item.id}:${item.state}:${liveFlowState.value.activeEndpointID || ''}`
})

const activeBayTone = computed<CircuitRouteVisualState>(() => {
  if (activeBayItem.value) return activeBayItem.value.tone
  const holdingState = queueHoldingState.value
  if (holdingState) return holdingState
  return 'idle'
})

const activeBayLabel = computed(() => {
  if (activeBayItem.value) return activeBayItem.value.state
  if (activeBayTone.value === 'holding' || activeBayTone.value === 'blocked') {
    const kind = queueHoldingBlockKind.value
    return blockKindLabel(kind) || 'queued'
  }
  return 'ready'
})

const queueHoldingState = computed<CircuitRouteVisualState | null>(() => {
  if (!modelNodes.value.length || !liveFlowState.value.holding) return null
  return 'holding'
})

const queueHoldingBlockKind = computed<RouteBlockKind | null>(() => {
  if (!queueHoldingState.value) return null
  return liveFlowState.value.blockedEndpointKinds.values().next().value ||
    modelNodes.value.map((node) => endpointBlockKind(node)).find((kind): kind is RouteBlockKind => Boolean(kind)) ||
    'queued'
})

const boardStageStyle = computed(() => ({
  '--live-flow-model-count': String(Math.max(1, modelNodes.value.length)),
  '--queue-move-duration': `${queueMotionDurationMs()}ms`,
  '--queue-card-height': `${QUEUE_CARD_HEIGHT_PX}px`,
  '--queue-card-gap': `${QUEUE_CARD_GAP_PX}px`,
  '--queue-stack-height': `${QUEUE_STACK_HEIGHT_PX}px`
}))

watch(visibleLanes, (lanes) => {
  if (!lanes.length) return
  if (!selectedLaneID.value || !lanes.some((lane) => lane.id === selectedLaneID.value)) {
    selectedLaneID.value = lanes[0]?.id || ''
  }
  if (!selectedFlowTargetValue.value && selectedLaneID.value) {
    selectedFlowTargetValue.value = `group:${selectedLaneID.value}`
  }
}, { immediate: true })

watch(selectedLaneID, () => {
  if (!applyingFlowTargetSelection && !selectedFlowTargetIsOverview.value) {
    selectedFlowTargetValue.value = selectedLaneID.value ? `group:${selectedLaneID.value}` : ''
  }
  if (mode.value === 'preview') {
    void resetPreviewTraffic()
  } else {
    resetPreviewVisualState()
  }
})

watch(selectedFlowTargetValue, (value, previous) => {
  if (!value || value === previous || applyingFlowTargetSelection) return
  applyFlowTargetSelection(value)
})

watch(selectedRecentFlowActivityKey, () => {
  if (mode.value === 'preview') return
  void refreshRecentFlowActivity()
}, { flush: 'post' })

watch(
  () => `${requests.logs[0]?.request_id || ''}:${requests.logs[0]?.updated_at || ''}`,
  (requestKey) => {
    if (!requests.logs[0]?.request_id || !requestKey || mode.value === 'preview') return
    scheduleModelLimitRowsRefresh()
  }
)

watch(
  () => [
    recentFlowUsage.value.map((item) => `${item.key}:${item.last_used_at}`).join('|'),
    flowTargetOptions.value.map((option) => option.value).join('|')
  ],
  () => applyInitialFlowTargetSelection(),
  { flush: 'post' }
)

watch(mode, (nextMode) => {
  telemetry.setCapacityFallbackSuppressed(nextMode === 'preview')
  if (nextMode !== 'preview' && previewTrafficRunning.value) {
    void stopPreviewTraffic()
  }
  if (nextMode === 'live') {
    void refreshRecentFlowActivity()
  }
}, { immediate: true })

watch(
  sourceQueueSlotItems,
  (items) => refreshQueueMotionData(items),
  { immediate: true }
)

watch(
  () => sourceQueueSlotItems.value.map((item) => item.id).join('|'),
  () => syncQueueMotionItems(),
  { immediate: true }
)

watch(
  () => [
    ...queue.items.map((item) => `${item.request_id}:${item.queued_at || ''}`),
    ...preview.queueItems.map((item) => `${item.request_id}:${item.queued_at || ''}`)
  ],
  () => {
    rememberQueueAnchors(queue.items)
    rememberQueueAnchors(preview.queueItems)
  },
  { immediate: true }
)

async function refreshRecentFlowActivity() {
  const target = selectedRecentFlowActivityTarget()
  const requestID = ++recentFlowActivityRequestID
  if (selectedFlowTargetIsOverview.value) {
    recentFlowRequests.value = []
    try {
      const response = await api.recentFlowActivity({ limit: 50 })
      if (requestID !== recentFlowActivityRequestID) return
      recentFlowRequests.value = Array.isArray(response.requests) ? response.requests : []
    } catch {
      if (requestID !== recentFlowActivityRequestID) return
      recentFlowRequests.value = []
    }
    return
  }
  if (!target) {
    recentFlowRequests.value = []
    return
  }
  recentFlowRequests.value = []
  try {
    const response = await api.recentFlowActivity({
      limit: RECENT_COMPLETION_LIMIT,
      laneId: target.laneId,
      endpointId: target.endpointId
    })
    if (requestID !== recentFlowActivityRequestID) return
    recentFlowRequests.value = Array.isArray(response.requests) ? response.requests : []
  } catch {
    if (requestID !== recentFlowActivityRequestID) return
    recentFlowRequests.value = []
  }
}

async function refreshRecentModelUsage() {
  try {
    const response = await api.recentModelUsage({ limit: 200 })
    recentModelUsageRows.value = Array.isArray(response.items) ? response.items : []
  } catch {
    recentModelUsageRows.value = []
  } finally {
    recentModelUsageLoaded.value = true
  }
}

onMounted(async () => {
  const query = new URLSearchParams(window.location.search)
  debugFlowPorts.value = query.get('debugFlowPorts') === '1'
  debugLiveFlowEvents.value = query.get('debugLiveFlowEvents') === '1'
  window.addEventListener('beforeunload', destroyPreviewSessionOnUnload)
  window.addEventListener('pagehide', destroyPreviewSessionOnUnload)
  window.addEventListener('resize', scheduleFlowMeasure)
  if (typeof ResizeObserver !== 'undefined') {
    flowResizeObserver = new ResizeObserver(() => scheduleFlowMeasure())
  }
  tickTimer = window.setInterval(() => {
    nowTick.value = Date.now()
    catalog.normalizeCooldowns(nowTick.value)
  }, 100)
  await Promise.all([
    pageData.load('realtime'),
    refreshRecentModelUsage(),
    capacity.refresh(),
    refreshModelLimitRows()
  ])
  await nextTick()
  applyInitialFlowTargetSelection()
  await refreshRecentFlowActivity()
  await nextTick()
  scheduleFlowMeasure()
  const fontsReady = document.fonts?.ready
  if (fontsReady) void fontsReady.then(() => scheduleFlowMeasure())
})

onBeforeUnmount(() => {
  if (tickTimer) {
    window.clearInterval(tickTimer)
    tickTimer = null
  }
  resetPreviewVisualState()
  clearVisualTokenMemory()
  clearQueueMotionTimers()
  telemetry.setCapacityFallbackSuppressed(false)
  window.removeEventListener('beforeunload', destroyPreviewSessionOnUnload)
  window.removeEventListener('pagehide', destroyPreviewSessionOnUnload)
  window.removeEventListener('resize', scheduleFlowMeasure)
  if (flowMeasureFrame) {
    window.cancelAnimationFrame(flowMeasureFrame)
    flowMeasureFrame = 0
  }
  flowResizeObserver?.disconnect()
  flowResizeObserver = null
  observedFlowPortElements = new Set<Element>()
  void preview.destroySession()
})

watch(
  () => [
    mode.value,
    selectedLaneID.value,
    modelNodes.value.map((node) => `${node.endpoint.id}:${node.tone}:${node.healthStatus}:${node.cooldownRemaining}:${node.queueDepth}:${node.badges.map((badge) => badge.key).join(',')}`).join('|'),
    boardQueuedCount.value,
    boardActiveCount.value,
    completionFeed.value.length,
    activeBayItems.value.map((item) => `${item.id}:${item.state}`).join('|'),
    activeBayTone.value
  ],
  () => nextTick(() => scheduleFlowMeasure()),
  { flush: 'post' }
)

watch(
  () => debugMissingFlowAnchors.value.join(','),
  (missing) => {
    if (!debugFlowPorts.value || !missing) return
    console.warn(`[LiveFlow] Missing flow anchors: ${missing}`)
  },
  { flush: 'post' }
)

function requestMatchesSelectedFlowTarget(laneName: string, endpointID = '') {
  const selectedEndpointID = selectedFlowTargetEndpointID.value
  if (selectedEndpointID) {
    return endpointID === selectedEndpointID
  }

  if (!selectedLane.value) return true
  return laneName === selectedLane.value.name
}

function buildLiveFlowCanonicalState(): LiveFlowCanonicalState {
  const activeRequests = currentRuntimeRequests().filter((item) => item.state === 'in_flight')
  const activeRequest = activeRequests[0] || null
  const selectedCandidate = activeRequest?.candidateTrace.find((item) => item.decision === 'selected') || null
  const holdingSource = activeRequest ? null : holdingQueueSource()
  const holdingUserDeferred = Boolean(holdingSource && isUserRequestDefer(holdingSource))
  const candidateTrace = activeRequest?.candidateTrace.length
    ? activeRequest.candidateTrace
    : !holdingUserDeferred && holdingSource?.candidate_trace?.length
      ? holdingSource.candidate_trace
      : []
  const candidateByEndpointID = new Map<string, CandidateTrace>()
  for (const candidate of candidateTrace) {
    if (candidate.endpoint_id) candidateByEndpointID.set(candidate.endpoint_id, candidate)
  }

  const activeEndpointID = activeRequest?.endpointID || selectedCandidate?.endpoint_id || ''
  const activeEndpointIndex = selectedEndpoints.value.findIndex((endpoint) => endpoint.id === activeEndpointID)
  const fallbackActive = Boolean(activeRequest && activeEndpointID && (
    activeRequest.fallbackCount > 0 ||
    selectedCandidate?.fallback_count ||
    activeEndpointIndex > 0
  ))
  const blockedEndpointKinds = new Map<string, RouteBlockKind>()
  for (const candidate of candidateTrace) {
    if (!candidate.endpoint_id || candidate.endpoint_id === activeEndpointID) continue
    const kind = blockKindFromCandidate(candidate)
    if (kind) blockedEndpointKinds.set(candidate.endpoint_id, kind)
  }

  const holding = !activeRequest && currentQueuedRequests().length > 0
  let holdingEndpointID = ''
  if (holding) {
    if (holdingUserDeferred) {
      holdingEndpointID = ''
    } else {
      const firstBlockedEndpointID = blockedEndpointKinds.keys().next().value
      holdingEndpointID = String(firstBlockedEndpointID || holdingSource?.endpoint_id || selectedEndpoints.value[0]?.id || '')
      if (holdingEndpointID && !blockedEndpointKinds.has(holdingEndpointID)) {
        blockedEndpointKinds.set(holdingEndpointID, blockKindFromQueueSource(holdingSource) || 'queued')
      }
    }
  }

  return {
    activeRequest,
    activeRequests,
    activeEndpointID,
    activeRequestID: activeRequest?.requestID || '',
    fallbackActive,
    candidateTrace,
    selectedCandidate,
    candidateByEndpointID,
    blockedEndpointKinds,
    holding,
    holdingUserDeferred,
    holdingEndpointID
  }
}

function currentRuntimeRequests(): RuntimeRequestView[] {
  const byRequestID = new Map<string, RuntimeRequestView>()
  for (const item of currentQueueSourceItems()) {
    if (item.state === 'in_flight') {
      byRequestID.set(item.request_id, runtimeRequestFromQueueItem(item))
    }
  }

  for (const progress of currentRequestProgressItems()) {
    if (progress.state !== 'in_flight') continue
    const requestID = String(progress.request_id || '')
    if (!requestID) continue
    const existing = byRequestID.get(requestID)
    byRequestID.set(requestID, runtimeRequestFromProgress(progress, existing))
  }

  return Array.from(byRequestID.values())
    .filter((item) => item.endpointID && requestMatchesSelectedFlowTarget(item.lane, item.endpointID))
    .sort(compareRuntimeRequests)
}

function currentQueueSourceItems() {
  return mode.value === 'preview' ? preview.queueItems : selectedFlowQueueItems.value
}

function currentQueuedRequests() {
  return currentQueueSourceItems()
    .filter((item) => item.state === 'queued' || item.state === 'waiting' || item.state === 'ready')
    .slice()
    .sort(compareQueueItems)
}

function holdingQueueSource() {
  return currentQueuedRequests().find((item) => item.candidate_trace?.length) || currentQueuedRequests()[0] || null
}

function currentRequestProgressItems() {
  return Object.values(mode.value === 'preview' ? preview.requestProgress : telemetry.requestProgress)
}

function runtimeRequestFromQueueItem(item: ReturnType<typeof currentQueueSourceItems>[number]): RuntimeRequestView {
  return {
    requestID: item.request_id,
    actorID: String(item.actor_id || ''),
    apiKeyUUID: String(item.api_key_uuid || requestAPIKeyUUID(item.request_id)),
    taskID: item.task_id,
    endpointID: String(item.endpoint_id || ''),
    endpointName: item.endpoint_name || '',
    providerID: String(item.provider_id || ''),
    incomingModel: item.incoming_model || item.lane || '',
    selectedUpstreamModel: '',
    lane: item.lane || '',
    state: item.state,
    substatus: item.substatus || '',
    queuedAt: item.queued_at || '',
    startedAt: item.started_at || item.queued_at || '',
    finishedAt: '',
    waitMs: Number(item.wait_ms || 0),
    statusCode: 0,
    uploadedTokens: Number(item.uploaded_tokens || 0),
    downloadedTokens: Number(item.downloaded_tokens || 0),
    fallbackCount: Number(item.fallback_count || 0),
    candidateTrace: item.candidate_trace || [],
    updatedAt: item.started_at || item.queued_at || ''
  }
}

function runtimeRequestFromProgress(progress: Record<string, any>, existing?: RuntimeRequestView): RuntimeRequestView {
  const requestID = String(progress.request_id || existing?.requestID || '')
  return {
    requestID,
    actorID: String(progress.actor_id || existing?.actorID || ''),
    apiKeyUUID: String(progress.api_key_uuid || existing?.apiKeyUUID || requestAPIKeyUUID(requestID)),
    taskID: String(progress.task_id || existing?.taskID || ''),
    endpointID: String(progress.endpoint_id || existing?.endpointID || ''),
    endpointName: String(progress.endpoint_name || existing?.endpointName || progress.selected_upstream_model || ''),
    providerID: String(progress.provider_id || existing?.providerID || ''),
    incomingModel: String(progress.incoming_model || existing?.incomingModel || progress.lane || ''),
    selectedUpstreamModel: String(progress.selected_upstream_model || existing?.selectedUpstreamModel || ''),
    lane: String(progress.lane || existing?.lane || ''),
    state: String(progress.state || existing?.state || ''),
    substatus: String(progress.substatus || existing?.substatus || ''),
    queuedAt: String(progress.queued_at || existing?.queuedAt || ''),
    startedAt: String(progress.started_at || existing?.startedAt || progress.queued_at || ''),
    finishedAt: String(progress.finished_at || existing?.finishedAt || ''),
    waitMs: Number(progress.wait_ms ?? existing?.waitMs ?? 0),
    statusCode: Number(progress.status_code ?? existing?.statusCode ?? 0),
    uploadedTokens: Number(progress.uploaded_tokens ?? existing?.uploadedTokens ?? 0),
    downloadedTokens: Number(progress.downloaded_tokens ?? existing?.downloadedTokens ?? 0),
    fallbackCount: Number(progress.fallback_count ?? progress.fallback ?? existing?.fallbackCount ?? 0),
    candidateTrace: existing?.candidateTrace || [],
    updatedAt: String(progress.updated_at || existing?.updatedAt || progress.started_at || progress.queued_at || '')
  }
}

function compareRuntimeRequests(a: RuntimeRequestView, b: RuntimeRequestView) {
  const aStarted = parseMs(a.startedAt || a.queuedAt || a.updatedAt)
  const bStarted = parseMs(b.startedAt || b.queuedAt || b.updatedAt)
  if (aStarted !== bStarted) return aStarted - bStarted
  return a.requestID.localeCompare(b.requestID)
}

function blockKindFromCandidate(candidate: CandidateTrace): RouteBlockKind | null {
  if (candidate.decision === 'rejected') return 'rejected'
  if (candidate.decision !== 'queued') return null
  const reason = candidate.reason.toLowerCase()
  if (reason.includes('rate') || reason.includes('/')) return 'rate_limited'
  if (reason.includes('cooldown')) return 'cooldown'
  if (reason.includes('unhealthy')) return 'unhealthy'
  return 'queued'
}

function blockKindFromQueueSource(item: ReturnType<typeof holdingQueueSource>): RouteBlockKind | null {
  const reason = (item?.delay_reason || '').toLowerCase()
  if (reason.includes('rate') || reason.includes('/')) return 'rate_limited'
  if (reason.includes('cooldown')) return 'cooldown'
  if (reason.includes('unhealthy')) return 'unhealthy'
  if (item?.state === 'waiting' || item?.state === 'ready' || item?.state === 'queued') return 'queued'
  return null
}

function scheduleFlowMeasure() {
  if (typeof window === 'undefined') return
  if (flowMeasureFrame) window.cancelAnimationFrame(flowMeasureFrame)
  flowMeasureFrame = window.requestAnimationFrame(() => {
    flowMeasureFrame = 0
    measureFlowAnchors()
  })
}

function measureFlowAnchors() {
  const stage = stageRef.value
  if (!stage) return

  const stageRect = stage.getBoundingClientRect()
  const nextWidth = Math.max(1, roundFlow(stageRect.width))
  const nextHeight = Math.max(1, roundFlow(stageRect.height))
  if (Math.abs(flowStageSize.width - nextWidth) >= 0.5) flowStageSize.width = nextWidth
  if (Math.abs(flowStageSize.height - nextHeight) >= 0.5) flowStageSize.height = nextHeight

  const nextPorts: Record<string, FlowPoint> = {}
  const portElements = Array.from(stage.querySelectorAll<HTMLElement>('[data-flow-port]'))
  for (const element of portElements) {
    const name = element.dataset.flowPort
    if (!name) continue
    const rect = element.getBoundingClientRect()
    nextPorts[name] = {
      x: roundFlow(rect.left + rect.width / 2 - stageRect.left),
      y: roundFlow(rect.top + rect.height / 2 - stageRect.top)
    }
  }
  if (flowPortsChanged(flowPorts.value, nextPorts)) {
    flowPorts.value = nextPorts
  }
  const measuredElements = Array.from(stage.querySelectorAll<HTMLElement>('[data-flow-measure]'))
  syncObservedFlowPorts([stage, ...measuredElements, ...portElements])
}

function flowPortsChanged(current: Record<string, FlowPoint>, next: Record<string, FlowPoint>) {
  const currentKeys = Object.keys(current)
  const nextKeys = Object.keys(next)
  if (currentKeys.length !== nextKeys.length) return true
  for (const key of nextKeys) {
    const currentPoint = current[key]
    const nextPoint = next[key]
    if (!currentPoint || !nextPoint) return true
    if (Math.abs(currentPoint.x - nextPoint.x) >= 0.5 || Math.abs(currentPoint.y - nextPoint.y) >= 0.5) return true
  }
  return false
}

function syncObservedFlowPorts(elements: Element[]) {
  if (!flowResizeObserver) return
  const next = new Set(elements)
  for (const element of observedFlowPortElements) {
    if (!next.has(element)) flowResizeObserver.unobserve(element)
  }
  for (const element of next) {
    if (!observedFlowPortElements.has(element)) flowResizeObserver.observe(element)
  }
  observedFlowPortElements = next
}

function portPoint(name: string) {
  return flowPorts.value[name] || null
}

function buildMeasuredRoutePaths(): FlowPath[] {
  const nodes = modelNodes.value
  const queueOut = portPoint('queue-out')
  const activeIn = portPoint('active-bay-in')
  const activeOut = portPoint('active-bay-out')
  const completedIn = portPoint('completed-in')
  if (!queueOut || !activeIn || !activeOut || !completedIn) return []

  if (!nodes.length) {
    const noModelIn = portPoint('no-model-card-in')
    const noModelOut = portPoint('no-model-card-out')
    if (!noModelIn || !noModelOut) return []
    return buildNoModelRoutePaths(queueOut, noModelIn, noModelOut, activeIn, activeOut, completedIn)
  }

  const measuredNodes = nodes
    .map((node) => ({
      node,
      input: portPoint(`model-${node.endpoint.id}-in`),
      output: portPoint(`model-${node.endpoint.id}-out`)
    }))
    .filter((entry): entry is { node: ModelNodeView, input: FlowPoint, output: FlowPoint } => Boolean(entry.input && entry.output))
  if (!measuredNodes.length) return []

  const sortedNodes = measuredNodes.slice().sort((a, b) => a.node.index - b.node.index)
  const paths: FlowPath[] = []
  if (sortedNodes.length === 1 && sortedNodes[0]) {
    pushSingleModelSkeleton(paths, sortedNodes[0], queueOut, activeIn, activeOut, completedIn)
    pushSingleModelOverlay(paths, sortedNodes[0], queueOut, activeIn, activeOut, completedIn)
    return paths
  }
  pushRankedModelSkeleton(paths, sortedNodes, queueOut, activeIn, activeOut, completedIn)
  pushRankedModelOverlays(paths, sortedNodes, queueOut, activeIn, activeOut, completedIn)
  return paths
}

function buildNoModelRoutePaths(
  queueOut: FlowPoint,
  noModelIn: FlowPoint,
  noModelOut: FlowPoint,
  activeIn: FlowPoint,
  activeOut: FlowPoint,
  completedIn: FlowPoint
) {
  const paths: FlowPath[] = []
  pushFlowPath(paths, 'skeleton-queue-no-model', orthogonalForwardRoute(queueOut, noModelIn), 'idle', 'skeleton')
  pushFlowPath(paths, 'skeleton-no-model-active', orthogonalForwardRoute(noModelOut, activeIn), 'idle', 'skeleton')
  pushFlowPath(paths, 'skeleton-active-completed', orthogonalForwardRoute(activeOut, completedIn), 'idle', 'skeleton')
  return paths
}

function pushSingleModelSkeleton(
  paths: FlowPath[],
  entry: { node: ModelNodeView, input: FlowPoint, output: FlowPoint },
  queueOut: FlowPoint,
  activeIn: FlowPoint,
  activeOut: FlowPoint,
  completedIn: FlowPoint
) {
  pushFlowPath(paths, `skeleton-queue-model-${entry.node.endpoint.id}`, orthogonalForwardRoute(queueOut, entry.input), 'idle', 'skeleton')
  pushFlowPath(paths, `skeleton-model-bay-${entry.node.endpoint.id}`, orthogonalForwardRoute(entry.output, activeIn), 'idle', 'skeleton')
  pushFlowPath(paths, 'skeleton-bay-completed', orthogonalForwardRoute(activeOut, completedIn), 'idle', 'skeleton')
}

function pushSingleModelOverlay(
  paths: FlowPath[],
  entry: { node: ModelNodeView, input: FlowPoint, output: FlowPoint },
  queueOut: FlowPoint,
  activeIn: FlowPoint,
  activeOut: FlowPoint,
  completedIn: FlowPoint
) {
  const tone = getCircuitRouteState({ node: entry.node })
  if (tone === 'idle') return
  const packet = routeStateHasPacket(tone)
  const emphasized = routeStateIsEmphasized(tone)
  pushFlowPath(paths, `overlay-queue-model-${entry.node.endpoint.id}`, orthogonalForwardRoute(queueOut, entry.input), tone, 'overlay', emphasized, packet)
  pushFlowPath(paths, `overlay-model-bay-${entry.node.endpoint.id}`, orthogonalForwardRoute(entry.output, activeIn), tone, 'overlay', emphasized, packet)
  pushFlowPath(paths, 'overlay-bay-completed', orthogonalForwardRoute(activeOut, completedIn), tone, 'overlay', emphasized, packet)
}

function pushRankedModelSkeleton(
  paths: FlowPath[],
  entries: Array<{ node: ModelNodeView, input: FlowPoint, output: FlowPoint }>,
  queueOut: FlowPoint,
  activeIn: FlowPoint,
  activeOut: FlowPoint,
  completedIn: FlowPoint
) {
  const inputX = Math.min(...entries.map((entry) => entry.input.x))
  const outputX = Math.max(...entries.map((entry) => entry.output.x))
  const trunkX = betweenRouteX(queueOut.x, inputX, 0.58)
  const outputTrunkX = betweenRouteX(outputX, activeIn.x, 0.46)

  for (const entry of entries) {
    pushFlowPath(paths, `skeleton-route-in-${entry.node.endpoint.id}`, rankedInputRoute(queueOut, entry.input, trunkX), 'idle', 'skeleton')
    pushFlowPath(paths, `skeleton-route-out-${entry.node.endpoint.id}`, rankedOutputRoute(entry.output, activeIn, outputTrunkX), 'idle', 'skeleton')
  }
  pushFlowPath(paths, 'skeleton-active-bay-completed', orthogonalForwardRoute(activeOut, completedIn), 'idle', 'skeleton')
}

function pushRankedModelOverlays(
  paths: FlowPath[],
  entries: Array<{ node: ModelNodeView, input: FlowPoint, output: FlowPoint }>,
  queueOut: FlowPoint,
  activeIn: FlowPoint,
  activeOut: FlowPoint,
  completedIn: FlowPoint
) {
  const inputX = Math.min(...entries.map((entry) => entry.input.x))
  const outputX = Math.max(...entries.map((entry) => entry.output.x))
  const trunkX = betweenRouteX(queueOut.x, inputX, 0.58)
  const outputTrunkX = betweenRouteX(outputX, activeIn.x, 0.46)
  const activeEntry = activeMeasuredNode(entries)
  const holdingEntry = !activeEntry && queueHoldingState.value
    ? entries.find((entry) => isQueueHoldingNode(entry.node)) || entries[0] || null
    : null
  const spineEntry = activeEntry || holdingEntry
  const spineTone = spineEntry ? getCircuitRouteState({ node: spineEntry.node, segment: 'spine' }) : getCircuitRouteState({ segment: 'spine' })
  const spineActive = routeStateIsEmphasized(spineTone)
  const spinePacket = routeStateHasPacket(spineTone)
  const overlayEntries = entries
    .map((entry) => {
      const tone = getCircuitRouteState({ node: entry.node })
      return {
        entry,
        tone,
        emphasized: routeStateIsEmphasized(tone),
        packet: routeStateHasPacket(tone)
      }
    })
    .filter((item) => item.tone !== 'idle')
    .sort((a, b) => routeOverlayPriority(a.tone) - routeOverlayPriority(b.tone))

  for (const { entry, tone, emphasized, packet } of overlayEntries) {
    pushFlowPath(paths, `overlay-route-in-${entry.node.endpoint.id}`, rankedInputRoute(queueOut, entry.input, trunkX), tone, 'overlay', emphasized, packet)
    const routeToBay = tone === 'active' ||
      tone === 'fallback-active' ||
      tone === 'holding' ||
      tone === 'blocked' ||
      tone === 'cooldown'
    const outputRoute = routeToBay
      ? rankedOutputRoute(entry.output, activeIn, outputTrunkX)
      : rankedOutputStubRoute(entry.output, outputTrunkX)
    pushFlowPath(paths, `overlay-route-out-${entry.node.endpoint.id}`, outputRoute, tone, 'overlay', emphasized, routeToBay && packet)
  }

  if (spineTone !== 'idle') {
    pushFlowPath(paths, 'overlay-active-bay-completed', orthogonalForwardRoute(activeOut, completedIn), spineTone, 'overlay', spineActive, spinePacket)
  }
}

function routeOverlayPriority(state: CircuitRouteVisualState) {
  if (state === 'holding' || state === 'blocked' || state === 'cancelled' || state === 'error') return 1
  if (state === 'active' || state === 'fallback-active' || state === 'completed') return 2
  return 0
}

function activeMeasuredNode(entries: Array<{ node: ModelNodeView, input: FlowPoint, output: FlowPoint }>) {
  return entries.find((entry) => entry.node.endpoint.id === liveFlowState.value.activeEndpointID) ||
    (circuitHasMovingTraffic() ? entries.find((entry) => entry.node.candidate?.decision === 'selected') : null) ||
    entries.find((entry) => routeStateIsEmphasized(getCircuitRouteState({ node: entry.node }))) ||
    null
}

function getCircuitRouteState(context: { node?: ModelNodeView | null, segment?: 'spine' | 'branch' | 'bay' | 'completed' } = {}): CircuitRouteVisualState {
  if (!modelNodes.value.length) return 'idle'
  const state = liveFlowState.value
  const node = context.node || null
  if (node) {
    if (state.activeEndpointID === node.endpoint.id) {
      return state.fallbackActive ? 'fallback-active' : 'active'
    }
    if (state.blockedEndpointKinds.has(node.endpoint.id)) return state.holding ? 'holding' : 'blocked'
    if (endpointBlockKind(node) && (state.holding || state.activeEndpointID || state.candidateByEndpointID.has(node.endpoint.id))) {
      return state.holding ? 'holding' : 'blocked'
    }
    const idleBlockKind = endpointBlockKind(node)
    if (idleBlockKind === 'cooldown' || idleBlockKind === 'rate_limited') return 'cooldown'
    return 'idle'
  }
  if (state.activeEndpointID) {
    return state.fallbackActive ? 'fallback-active' : 'active'
  }
  if (state.holding) return 'holding'
  if (recentTerminalCount.value > 0) return 'completed'
  return 'idle'
}

function activeBayToneForEndpoint(endpointID: string): CircuitRouteVisualState {
  const node = modelNodes.value.find((entry) => entry.endpoint.id === endpointID)
  if (!node) return 'active'
  return getCircuitRouteState({ node, segment: 'bay' })
}

function routeStateIsEmphasized(state: CircuitRouteVisualState) {
  return state !== 'idle' && state !== 'cooldown'
}

function routeStateHasPacket(state: CircuitRouteVisualState) {
  return state === 'active' || state === 'fallback-active' || state === 'completed'
}

function circuitHasMovingTraffic() {
  return Boolean(liveFlowState.value.activeEndpointID) || recentTerminalCount.value > 0
}

function isQueueHoldingNode(node: ModelNodeView) {
  const state = liveFlowState.value
  if (!state.holding) return false
  if (state.holdingUserDeferred) return false
  if (state.blockedEndpointKinds.has(node.endpoint.id)) return true
  if (state.holdingEndpointID) return node.endpoint.id === state.holdingEndpointID
  return modelNodes.value.length === 1 || node.index === 0
}

function betweenRouteX(leftX: number, rightX: number, ratio = 0.5) {
  if (rightX <= leftX) return roundFlow(leftX)
  const padding = Math.min(36, Math.max(10, (rightX - leftX) * 0.22))
  const raw = leftX + (rightX - leftX) * ratio
  return roundFlow(Math.min(rightX - padding, Math.max(leftX + padding, raw)))
}

function orthogonalForwardRoute(from: FlowPoint, to: FlowPoint) {
  if (to.x < from.x - 0.1) return []
  const midX = betweenRouteX(from.x, to.x)
  return [
    from,
    { x: midX, y: from.y },
    { x: midX, y: to.y },
    to
  ]
}

function rankedInputRoute(queueOut: FlowPoint, modelInput: FlowPoint, trunkX: number) {
  return [
    queueOut,
    { x: trunkX, y: queueOut.y },
    { x: trunkX, y: modelInput.y },
    modelInput
  ]
}

function rankedOutputRoute(modelOutput: FlowPoint, activeIn: FlowPoint, trunkX: number) {
  return [
    modelOutput,
    { x: trunkX, y: modelOutput.y },
    { x: trunkX, y: activeIn.y },
    activeIn
  ]
}

function rankedOutputStubRoute(modelOutput: FlowPoint, trunkX: number) {
  return [
    modelOutput,
    { x: trunkX, y: modelOutput.y }
  ]
}

function isTerminalState(state: string) {
  return state === 'completed' || state === 'failed' || state === 'cancelled'
}

function providerName(providerID?: string | null) {
  if (!providerID) return 'Provider missing'
  return catalog.providerMap[providerID]?.name || 'Provider missing'
}

function endpointProviderSlug(endpoint: Endpoint) {
  return catalog.providerMap[endpoint.provider_id]?.slug?.trim() || 'provider-missing'
}

function endpointModelSlug(endpoint: Endpoint) {
  return endpoint.slug?.trim() || 'unnamed-model'
}

function routeSlug(value?: string | null) {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'provider missing' || normalized === 'no upstream selected' || normalized === 'no model selected') return ''
  return normalized
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function routingGroupName(laneID?: string | null, incomingModel?: string | null) {
  if (laneID && catalog.laneMap[laneID]) return catalog.laneMap[laneID].name
  const target = String(incomingModel || '').trim().toLowerCase()
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

function recentRequestMs(log: Pick<RecentFlowRequest, 'finished_at' | 'updated_at' | 'started_at' | 'queued_at' | 'created_at'>) {
  return parseMs(log.finished_at || log.updated_at || log.started_at || log.queued_at || log.created_at)
}

function resolveRecentRequestLane(log: Pick<RecentFlowRequest, 'lane_id' | 'incoming_model'>) {
  if (log.lane_id && catalog.laneMap[log.lane_id]) return catalog.laneMap[log.lane_id]
  return overviewLaneFromSource({ incomingModel: log.incoming_model })
}

function selectedRecentFlowActivityTarget(): RecentFlowActivityTarget | null {
  if (selectedFlowTargetIsOverview.value) return null
  const endpointID = selectedFlowTargetEndpointID.value
  if (endpointID) {
    return {
      key: `model:${endpointID}`,
      endpointId: endpointID
    }
  }

  const laneID = selectedLaneID.value
  if (!laneID) return null
  return {
    key: `group:${laneID}`,
    laneId: laneID
  }
}

function recentFlowRequestMatchesTarget(log: RecentFlowRequest, target: RecentFlowActivityTarget | null = selectedRecentFlowActivityTarget()) {
  if (!target) return false
  if (target.endpointId) {
    return log.endpoint_id === target.endpointId
  }
  if (target.laneId) {
    return log.lane_id === target.laneId
  }
  return true
}

function recentUsageTargetLane(entry: RecentModelUsage) {
  if (entry.lane_id && catalog.laneMap[entry.lane_id]) return catalog.laneMap[entry.lane_id]
  return visibleLanes.value.find((lane) => lane.name === entry.lane_name) || null
}

function canOpenRecentUsage(entry: RecentModelUsage) {
  return Boolean(recentUsageTargetLane(entry))
}

function applyInitialFlowTargetSelection() {
  if (initialFlowTargetSelectionApplied) return
  if (catalog.loading || !recentModelUsageLoaded.value) return
  initialFlowTargetSelectionApplied = true
  selectedFlowTargetValue.value = 'overview'
  applyFlowTargetSelection('overview')
}

function applyFlowTargetSelection(value: string) {
  if (value === 'overview') {
    applyingFlowTargetSelection = true
    try {
      setFlowMode('live')
    } finally {
      window.setTimeout(() => {
        applyingFlowTargetSelection = false
      }, 0)
    }
    return
  }
  const option = flowTargetOptionByValue.value[value]
  if (!option) return
  applyingFlowTargetSelection = true
  try {
    if (option.kind === 'group') {
      selectedLaneID.value = option.laneID
    } else if (option.laneID) {
      selectedLaneID.value = option.laneID
    }
    setFlowMode('live')
    void nextTick(() => scheduleFlowMeasure())
  } finally {
    window.setTimeout(() => {
      applyingFlowTargetSelection = false
    }, 0)
  }
}

function selectFlowTarget(value: string) {
  if (value === 'overview') {
    if (selectedFlowTargetValue.value === value) applyFlowTargetSelection(value)
    else selectedFlowTargetValue.value = value
    return true
  }
  if (!flowTargetOptionByValue.value[value]) return false
  if (selectedFlowTargetValue.value === value) {
    applyFlowTargetSelection(value)
  } else {
    selectedFlowTargetValue.value = value
  }
  return true
}

function openRecentUsage(entry: RecentModelUsage) {
  const lane = recentUsageTargetLane(entry)
  if (!lane) return
  if (selectFlowTarget(`group:${lane.id}`)) return
  selectedLaneID.value = lane.id
  setFlowMode('live')
  void nextTick(() => scheduleFlowMeasure())
}

function openRecentModel(entry: RecentModelUsage) {
  if (entry.endpoint_id && selectFlowTarget(`model:${entry.endpoint_id}`)) {
    return
  }
  if (entry.endpoint_id) {
    void openRealtimeModelSettings(entry.endpoint_id)
  }
}

function taskStateTone(state: string): BadgeTone {
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

function modelUsageLimits(endpoint: Endpoint) {
  return capacityRowsForEndpoint(endpoint.id).map(capacityLimitRowToCardRow)
}

function liveCapacityFresh() {
  return capacity.isFresh(nowTick.value)
}

function capacityRowsForEndpoint(endpointID: string): CapacityLimitRow[] {
  let rows: CapacityLimitRow[]
  if (mode.value === 'preview') {
    rows = preview.capacitySnapshot?.models.find(model => model.endpoint_id === endpointID)?.limit_rows || []
  } else {
    if (!liveCapacityFresh()) return []
    rows = capacity.rowsForEndpoint(endpointID)
  }
  if (props.capacityRowFilter) rows = rows.filter(props.capacityRowFilter)
  if (props.capacityRowTransform) rows = rows.map(props.capacityRowTransform)
  return rows
}

function capacityModelForEndpoint(endpointID: string) {
  if (mode.value === 'preview') {
    return preview.capacitySnapshot?.models.find(model => model.endpoint_id === endpointID) || null
  }
  if (!liveCapacityFresh()) return null
  return capacity.model(endpointID)
}

function capacityLimitRowToCardRow(row: CapacityLimitRow) {
  const blockedUntil = row.blocked_until || row.reset_at || ''
  return {
    key: row.key,
    label: row.label || limitAbbrev(row.metric as 'requests' | 'tokens' | 'spend', row.period),
    metric: row.metric,
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

function modelHasPacedRequestLimit(endpoint: Endpoint) {
  if (endpoint.pacing === false) return false
  return (['second', 'minute', 'hour'] as Period[]).some((period) => {
    const row = modelLimitRow(endpoint, 'requests', period)
    return row.modelConfigured != null || row.providerConfigured != null
  })
}

function modelFeatureBadges(endpoint: Endpoint): Array<{ key: string, label: string, tone: BadgeTone }> {
  const badges: Array<{ key: string, label: string, tone: BadgeTone }> = []
  const cooldownBadge = modelCooldownBadge(endpoint)
  if (cooldownBadge) {
    badges.push(cooldownBadge)
  }
  if (endpointUserLimitBadgeActive(endpoint)) {
    badges.push({ key: 'user-limit', label: 'user limit', tone: 'amber' })
  }
  if (endpointAPIKeyLimitBadgeActive(endpoint)) {
    badges.push({ key: 'api-key-limit', label: 'API key limit', tone: 'amber' })
  }
  if (modelHasPacedRequestLimit(endpoint)) {
    badges.push({ key: 'pacing', label: 'pacing', tone: 'sky' })
  }
  return badges
}

function modelCooldownBadge(endpoint: Endpoint): { key: string, label: string, tone: BadgeTone } | null {
  const model = capacityModelForEndpoint(endpoint.id)
  const cooldownUntil = model?.cooldown_until || endpoint.cooldown_until
  if (!cooldownUntil || parseMs(cooldownUntil) <= nowTick.value) return null

  const statusCode = Number(model?.cooldown_status_code || endpoint.cooldown_status_code || 0)
  if (statusCode >= 400 && statusCode <= 599) {
    return {
      key: `upstream-${statusCode}`,
      label: `server ${statusCode}`,
      tone: statusCode === 429 ? 'amber' : 'rose'
    }
  }

  switch (String(model?.cooldown_reason || endpoint.cooldown_reason || '')) {
    case 'upstream_rate_limited':
      return { key: 'upstream-rate-limit', label: 'upstream rate limited', tone: 'amber' }
    case 'upstream_server_error':
      return { key: 'upstream-server-error', label: 'upstream server error', tone: 'rose' }
    case 'upstream_retry_after':
      return { key: 'upstream-retry-after', label: 'upstream retry after', tone: 'amber' }
    case 'upstream_unavailable':
      return { key: 'upstream-unavailable', label: 'upstream unavailable', tone: 'rose' }
  }

  return null
}

function modelQueueDepth(endpointID: string) {
  if (mode.value === 'preview') {
    return preview.queueItems.filter((item) =>
      item.endpoint_id === endpointID &&
      (item.state === 'queued' || item.state === 'waiting' || item.state === 'in_flight')
    ).length
  }
  return selectedFlowQueueItems.value.filter((item) => item.endpoint_id === endpointID && !isUserRequestDefer(item)).length
}

function modelCooldownRemaining(endpoint: Endpoint) {
  const model = capacityModelForEndpoint(endpoint.id)
  const cooldownUntil = model?.cooldown_until || endpoint.cooldown_until
  if (!cooldownUntil) return 0
  return Math.max(0, parseMs(cooldownUntil) - nowTick.value)
}

function modelHealthStatus(endpoint: Endpoint) {
  if (modelAvailabilityIssue(endpoint)) return 'unhealthy'
  const model = capacityModelForEndpoint(endpoint.id)
  if (model?.health_status) return model.health_status
  return 'unknown'
}

function modelCapacityState(endpoint: Endpoint): ModelCapacityState {
  if (modelAvailabilityIssue(endpoint)) return 'unavailable'
  const model = capacityModelForEndpoint(endpoint.id)
  if (model?.capacity_state) return model.capacity_state as ModelCapacityState
  return 'unknown'
}

function modelActivityState(endpointID: string): ModelActivityState {
  const state = liveFlowState.value
  if (state.activeEndpointID === endpointID) {
    return state.fallbackActive ? 'fallback-active' : 'routing'
  }
  if (state.blockedEndpointKinds.has(endpointID)) {
    return state.holding ? 'holding' : 'blocked'
  }
  return 'idle'
}

function modelAvailabilityIssue(endpoint: Endpoint) {
  if (!endpoint.enabled) return 'model disabled'
  const provider = catalog.providerMap[endpoint.provider_id]
  if (!provider) return 'provider missing'
  if (!provider.enabled) return 'provider disabled'
  return ''
}

function modelStatusLabel(activity: ModelActivityState, capacity: ModelCapacityState, cooldownRemaining = 0, availabilityIssue = '') {
  if (availabilityIssue) return availabilityIssue
  if (capacity === 'rate-limited') return 'rate limited'
  if (capacity === 'cooling-down' && cooldownRemaining > 0) return 'cooldown'
  if (capacity === 'unhealthy') return 'unhealthy'
  if (capacity === 'unavailable') return 'unavailable'
  if (capacity === 'unknown' && mode.value === 'preview') return 'ready for preview'
  if (capacity === 'unknown') return telemetry.connected ? 'syncing' : 'offline'
  if (activity === 'routing' || activity === 'fallback-active') return 'healthy'
  if (activity === 'blocked' || activity === 'holding') {
    return activity === 'holding' ? 'holding' : 'blocked'
  }
  return 'healthy'
}

function modelStatusTone(activity: ModelActivityState, capacity: ModelCapacityState, cooldownRemaining = 0): BadgeTone {
  if (capacity === 'rate-limited' || (capacity === 'cooling-down' && cooldownRemaining > 0)) return 'amber'
  if (capacity === 'unavailable') return 'rose'
  if (capacity === 'unhealthy') return 'rose'
  if (capacity === 'unknown' && mode.value === 'preview') return 'emerald'
  if (capacity === 'unknown') return telemetry.connected ? 'sky' : 'rose'
  if (activity === 'routing' || activity === 'fallback-active') return 'emerald'
  if (activity === 'blocked' || activity === 'holding') return 'amber'
  return 'emerald'
}

function shortID(requestID: string) {
  if (!requestID) return 'request'
  return requestID.length > 12 ? requestID.slice(0, 8) : requestID
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

function completionQueuedMs(log: Pick<RecentFlowRequest, 'request_id' | 'queued_at' | 'created_at' | 'started_at' | 'wait_ms'>) {
  const rememberedMs = parseMs(requestQueuedAtMemory[log.request_id] || '')
  if (rememberedMs > 0) return rememberedMs
  return requestAnchorMs(log)
}

function completionWaitMs(log: Pick<RecentFlowRequest, 'wait_ms'>, queuedMs: number, startedMs: number) {
  if (queuedMs > 0 && startedMs > queuedMs) {
    return startedMs - queuedMs
  }
  return Math.max(0, Number(log.wait_ms || 0))
}

function rememberQueueAnchors(items: Array<{ request_id: string, queued_at?: string | null }>) {
  for (const item of items) {
    rememberQueueAnchor(item.request_id, item.queued_at || '')
  }
}

function rememberQueueAnchor(requestID: string, queuedAt: string) {
  if (!requestID) return
  const nextMs = parseMs(queuedAt)
  if (nextMs <= 0) return
  const currentMs = parseMs(requestQueuedAtMemory[requestID] || '')
  if (!currentMs || nextMs < currentMs) {
    requestQueuedAtMemory[requestID] = new Date(nextMs).toISOString()
  }
}

function clearPreviewQueueAnchorMemory() {
  for (const key of Object.keys(requestQueuedAtMemory)) {
    if (key.startsWith('live-flow-preview-')) {
      delete requestQueuedAtMemory[key]
    }
  }
}

function candidateForEndpoint(endpointID: string) {
  return visibleCandidates.value.find((item) => item.endpoint_id === endpointID) || null
}

function nodeTone(endpointID: string, candidate: CandidateTrace | null, activity: ModelActivityState, capacity: ModelCapacityState, cooldownRemaining = 0): NodeTone {
  if (activity === 'routing' || activity === 'fallback-active') return 'active'
  if (activity === 'blocked' || activity === 'holding') {
    if (capacity === 'cooling-down' && cooldownRemaining > 0) return 'cooling'
    return 'rejected'
  }
  if (capacity === 'unavailable') return 'rejected'
  if (capacity === 'rate-limited' || capacity === 'unhealthy') return 'rejected'
  if (capacity === 'cooling-down' && cooldownRemaining > 0) return 'cooling'
  if (candidate?.decision === 'rejected') return 'rejected'
  if (candidate?.decision === 'not_needed') return 'muted'
  return 'idle'
}

function periodMs(period: Period) {
  switch (period) {
    case 'second': return 1000
    case 'minute': return 60_000
    case 'hour': return 3_600_000
    case 'day': return 86_400_000
    case 'month': return 30 * 86_400_000
  }
}

function requestStartedMs(log: RecentFlowRequest) {
  return requestUsageMs(log)
}

function requestUsageMs(item: {
  started_at?: string | null
  queued_at?: string | null
  finished_at?: string | null
  created_at?: string | null
}) {
  return parseMs(item.started_at || item.queued_at || item.finished_at || item.created_at || '')
}

function requestConsumesUsage(log: RecentFlowRequest, metric: Metric) {
  if (log.task_state === 'completed' || log.task_state === 'failed') return true
  return metric === 'requests' && log.task_state === 'cancelled' && Boolean(log.started_at)
}

function hasActualUsage(log: RecentFlowRequest) {
  return log.actual_total_tokens > 0 || log.actual_input_tokens > 0 || log.actual_output_tokens > 0
}

function requestMetricValue(log: RecentFlowRequest, metric: Metric) {
  if (metric === 'requests') return 1
  if (metric === 'tokens') {
    return hasActualUsage(log)
      ? log.actual_total_tokens || (log.actual_input_tokens + log.actual_output_tokens)
      : log.estimated_input_tokens + log.estimated_output_tokens
  }
  if (metric === 'spend') return log.actual_cost_micros || log.estimated_cost_micros
  return 0
}

function compareQueueItems(a: { queued_at: string, request_id: string }, b: { queued_at: string, request_id: string }) {
  const sequenceDelta = requestQueueSequence(a.request_id) - requestQueueSequence(b.request_id)
  if (sequenceDelta !== 0) return sequenceDelta
  const queuedDelta = parseMs(a.queued_at) - parseMs(b.queued_at)
  if (queuedDelta !== 0) return queuedDelta
  return a.request_id.localeCompare(b.request_id)
}

function requestQueueSequence(requestID: string) {
  const match = requestID.match(/(?:^|[-_])(\d+)$/)
  if (!match) return Number.MAX_SAFE_INTEGER
  const parsed = Number.parseInt(match[1] || '', 10)
  return Number.isFinite(parsed) ? parsed : Number.MAX_SAFE_INTEGER
}

function previewRequestTitle(requestID: string) {
  const match = requestID.match(/-(\d+)$/)
  return match ? `request ${(match[1] || '').padStart(3, '0')}` : shortID(requestID)
}

function previewQueueItemDetail(item: RequestDeferSource & { request_id: string, queued_at: string, wait_ms: number }) {
  return requestUserLimitDetail(item) || queueElapsedDetail(item)
}

function queueElapsedDetail(item: { request_id?: string, queued_at?: string | null, wait_ms?: number | null }) {
  return `queued for ${durationMs(queueElapsedMs(item))}`
}

function queueElapsedMs(item: { request_id?: string, queued_at?: string | null, wait_ms?: number | null }) {
  const queuedMs = parseMs((item.request_id && requestQueuedAtMemory[item.request_id]) || item.queued_at || '')
  const liveElapsedMs = queuedMs > 0 ? nowTick.value - queuedMs : 0
  return Math.max(0, Number(item.wait_ms || 0), liveElapsedMs)
}

function refreshQueueMotionData(items: QueueSlotView[]) {
  const latestByID = new Map(items.map((item) => [item.id, item]))
  queueMotionItems.value = queueMotionItems.value.map((item) => {
    const latest = latestByID.get(item.id)
    return latest && item.phase !== 'leaving'
      ? { ...item, ...latest }
      : item
  })
}

function syncQueueMotionItems() {
  const sourceItems = sourceQueueSlotItems.value
  const existingItems = queueMotionItems.value
  if (!existingItems.length) {
    queueMotionItems.value = sourceItems.map((item, index) => queueMotionItem(item, index, index, 'stable'))
    return
  }

  const sourceIDSet = new Set(sourceItems.map((item) => item.id))
  const existingActiveItems = existingItems.filter((item) => item.phase !== 'leaving')
  const existingLeavingItems = existingItems.filter((item) => item.phase === 'leaving')
  const existingActiveByID = new Map(existingActiveItems.map((item) => [item.id, item]))
  const existingLeavingByID = new Map(existingLeavingItems.map((item) => [item.id, item]))
  const leavingItems = [
    ...existingLeavingItems.filter((item) => !sourceIDSet.has(item.id)),
    ...existingActiveItems
      .filter((item) => !sourceIDSet.has(item.id))
      .map((item) => ({
        ...item,
        phase: 'leaving' as QueueMotionPhase,
        leavingToBay: true
      }))
  ]

  const nextItems: QueueMotionItem[] = [...leavingItems]
  for (const [index, sourceItem] of sourceItems.entries()) {
    const existing = existingActiveByID.get(sourceItem.id) || existingLeavingByID.get(sourceItem.id)
    if (existing) {
      nextItems.push({
        ...existing,
        ...sourceItem,
        targetIndex: index,
        phase: existing.currentIndex === index ? 'stable' : 'moving',
        leavingToBay: false
      })
      continue
    }

    nextItems.push(queueMotionItem(sourceItem, Math.min(index + 1, QUEUE_VISIBLE_SLOT_COUNT), index, 'entering'))
  }

  clearQueueMotionTimers()
  queueMotionItems.value = nextItems
  const motionVersion = ++queueMotionVersion

  if (!process.client || motionIsReduced()) {
    settleQueueMotion()
    return
  }

  void nextTick(() => {
    if (motionVersion !== queueMotionVersion) return
    queueMotionFrame = requestAnimationFrame(() => {
      if (motionVersion !== queueMotionVersion) return
      queueMotionFrame = 0
      queueMotionItems.value = queueMotionItems.value.map((item) => {
        if (item.phase === 'leaving') return item
        return {
          ...item,
          currentIndex: item.targetIndex,
          phase: 'moving'
        }
      })
      queueMotionSettleTimer = setTimeout(() => settleQueueMotion(), queueMotionDurationMs() + 40)
    })
  })
}

function queueMotionItem(item: QueueSlotView, currentIndex: number, targetIndex: number, phase: QueueMotionPhase): QueueMotionItem {
  return {
    ...item,
    currentIndex,
    targetIndex,
    phase
  }
}

function queueMotionStyle(item: QueueMotionItem) {
  const y = Math.max(0, item.currentIndex) * QUEUE_ROW_PITCH_PX
  const x = item.phase === 'leaving' && item.leavingToBay ? 24 : 0
  return {
    transform: `translate3d(${x}px, ${y}px, 0)`
  }
}

function settleQueueMotion() {
  clearQueueMotionTimers()
  queueMotionItems.value = sourceQueueSlotItems.value.map((item, index) => queueMotionItem(item, index, index, 'stable'))
}

function clearQueueMotionTimers() {
  if (queueMotionFrame && process.client) {
    cancelAnimationFrame(queueMotionFrame)
  }
  queueMotionFrame = 0
  if (queueMotionSettleTimer) {
    clearTimeout(queueMotionSettleTimer)
  }
  queueMotionSettleTimer = null
}

function queueMotionDurationMs() {
  if (motionIsReduced()) return 0
  return 300
}

function motionIsReduced() {
  return process.client && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function previewEndpointHealthRemaining(endpointID: string) {
  const cooldownUntil = preview.endpointHealth[endpointID]?.cooldown_until
  if (!cooldownUntil) return 0
  return Math.max(0, parseMs(cooldownUntil) - nowTick.value)
}

async function openRealtimeGroupSettings() {
  if (!selectedLane.value) return
  await catalog.refreshAll()
  groupSettingsOpen.value = true
}

async function openRealtimeModelSettings(endpointID: string) {
  await catalog.refreshAll()
  modelSettingsEndpointID.value = endpointID
}

function closeRealtimeModelSettings() {
  modelSettingsEndpointID.value = ''
}

function queueCoordinate(index: number) {
  return {
    x: FLOW_QUEUE_X,
    y: Math.min(82, FLOW_QUEUE_TOP + index * FLOW_QUEUE_STEP)
  }
}

function modelCoordinate(index: number, total: number) {
  if (total <= 1) return { x: 50, y: 50 }
  const y = 24 + index * (56 / Math.max(total - 1, 1))
  return { x: 50, y }
}

function modelBayYOffset(node: { limits?: Array<unknown> }) {
  const limitCount = Math.min(node.limits?.length || 0, 4)
  return FLOW_MODEL_BAY_BASE_Y_OFFSET + limitCount * FLOW_MODEL_BAY_LIMIT_ROW_OFFSET
}

function modelBayPoint(node: { x: number, y: number, limits?: Array<unknown> }): FlowPoint {
  return {
    x: node.x,
    y: Math.min(92, node.y + modelBayYOffset(node))
  }
}

function modelBayInputPoint(node: { x: number, y: number }): FlowPoint {
  const point = modelBayPoint(node)
  return {
    x: point.x - FLOW_MODEL_BAY_PORT_OFFSET,
    y: point.y
  }
}

function modelBayOutputPoint(node: { x: number, y: number }): FlowPoint {
  const point = modelBayPoint(node)
  return {
    x: point.x + FLOW_MODEL_BAY_PORT_OFFSET,
    y: point.y
  }
}

function modelBayStyle(node: { x: number, y: number }) {
  const point = modelBayPoint(node)
  return {
    left: `${point.x}%`,
    top: `${point.y}%`
  }
}

function routePathD(points: FlowPoint[]) {
  const cleanPoints = compactRoutePoints(points)
  if (!cleanPoints.length) return ''
  const [firstPoint, ...restPoints] = cleanPoints
  let d = `M ${roundFlow(firstPoint.x)} ${roundFlow(firstPoint.y)}`
  for (let index = 0; index < restPoints.length; index++) {
    const current = restPoints[index]
    if (!current) continue
    const previous = cleanPoints[index]
    const next = restPoints[index + 1]
    if (!previous || !next || !routeTurns(previous, current, next)) {
      d += ` L ${roundFlow(current.x)} ${roundFlow(current.y)}`
      continue
    }
    const radius = Math.min(12, flowDistance(previous, current) / 2, flowDistance(current, next) / 2)
    if (radius <= 0) {
      d += ` L ${roundFlow(current.x)} ${roundFlow(current.y)}`
      continue
    }
    const before = pointToward(current, previous, radius)
    const after = pointToward(current, next, radius)
    d += ` L ${roundFlow(before.x)} ${roundFlow(before.y)} Q ${roundFlow(current.x)} ${roundFlow(current.y)} ${roundFlow(after.x)} ${roundFlow(after.y)}`
  }
  return d
}

function pushFlowPath(
  paths: FlowPath[],
  id: string,
  points: FlowPoint[],
  tone: CircuitRouteVisualState,
  layer: FlowPathLayer,
  active = layer === 'overlay' && tone !== 'idle',
  packet = false
) {
  if (points.length < 2) return
  const [from, to] = [points[0], points[points.length - 1]]
  if (!from || !to || flowDistance(from, to) <= 0) return
  if (routeMovesBackward(points)) return
  const d = routePathD(points)
  if (!d) return
  const cleanPoints = compactRoutePoints(points)
  paths.push({
    id,
    d,
    layer,
    active,
    packet,
    tone,
    labelPoint: pointOnRoute(cleanPoints, 0.5)
  })
}

function compactRoutePoints(points: FlowPoint[]) {
  const compact: FlowPoint[] = []
  for (const point of points) {
    const previous = compact[compact.length - 1]
    if (previous && flowDistance(previous, point) < 0.01) continue
    compact.push(point)
  }
  return compact
}

function routeTurns(previous: FlowPoint, current: FlowPoint, next: FlowPoint) {
  const incomingHorizontal = Math.abs(current.y - previous.y) < 0.01
  const outgoingHorizontal = Math.abs(next.y - current.y) < 0.01
  return incomingHorizontal !== outgoingHorizontal
}

function pointToward(from: FlowPoint, to: FlowPoint, distance: number): FlowPoint {
  const length = flowDistance(from, to)
  if (length <= 0) return from
  const ratio = Math.min(1, distance / length)
  return {
    x: from.x + (to.x - from.x) * ratio,
    y: from.y + (to.y - from.y) * ratio
  }
}

function routeMovesBackward(points: FlowPoint[]) {
  for (let index = 1; index < points.length; index++) {
    const previous = points[index - 1]
    const current = points[index]
    if (previous && current && current.x < previous.x - 0.1) return true
  }
  return false
}

function roundFlow(value: number) {
  return Math.round(value * 100) / 100
}

function selectedEndpointIndex(endpointID: string, fallbackIndex = 0) {
  const matchedIndex = selectedEndpoints.value.findIndex((endpoint) => endpoint.id === endpointID)
  return matchedIndex >= 0 ? matchedIndex : fallbackIndex
}

function modelPointForEndpoint(endpointID: string, fallbackIndex = 0) {
  return modelCoordinate(selectedEndpointIndex(endpointID, fallbackIndex), selectedEndpoints.value.length || 1)
}

function modelNodeForEndpoint(endpointID: string, fallbackIndex = 0): { x: number, y: number, limits?: Array<unknown> } {
  return modelNodes.value.find((node) => node.endpoint.id === endpointID) || modelPointForEndpoint(endpointID, fallbackIndex)
}

function routeToModel(endpointID: string, fallbackIndex = 0): FlowPoint[] {
  const nodePoint = modelNodeForEndpoint(endpointID, fallbackIndex)
  const bayInputPoint = modelBayInputPoint(nodePoint)
  return [
    { x: FLOW_QUEUE_X, y: FLOW_QUEUE_TOP },
    { x: FLOW_QUEUE_X, y: FLOW_QUEUE_EXIT_Y },
    { x: FLOW_QUEUE_PORT_X, y: FLOW_QUEUE_EXIT_Y },
    { x: FLOW_TRUNK_X, y: FLOW_QUEUE_EXIT_Y },
    { x: FLOW_TRUNK_X, y: bayInputPoint.y },
    bayInputPoint
  ]
}

function routeTokenToModel(endpointID: string, fallbackIndex = 0): FlowPoint[] {
  const nodePoint = modelNodeForEndpoint(endpointID, fallbackIndex)
  const bayPoint = modelBayPoint(nodePoint)
  return [
    ...routeToModel(endpointID, fallbackIndex),
    bayPoint
  ]
}

function routeToCompleted(endpointID: string, fallbackIndex = 0): FlowPoint[] {
  const nodePoint = modelNodeForEndpoint(endpointID, fallbackIndex)
  const bayOutputPoint = modelBayOutputPoint(nodePoint)
  return [
    bayOutputPoint,
    { x: FLOW_OUTPUT_X, y: bayOutputPoint.y },
    { x: FLOW_OUTPUT_X, y: FLOW_COMPLETED_ENTRY_Y },
    { x: FLOW_COMPLETED_DOCK_X, y: FLOW_COMPLETED_ENTRY_Y }
  ]
}

function routeTokenToCompleted(endpointID: string, fallbackIndex = 0): FlowPoint[] {
  const nodePoint = modelNodeForEndpoint(endpointID, fallbackIndex)
  const bayPoint = modelBayPoint(nodePoint)
  const bayOutputPoint = modelBayOutputPoint(nodePoint)
  return [
    bayPoint,
    bayOutputPoint,
    ...routeToCompleted(endpointID, fallbackIndex).slice(1)
  ]
}

function activeRequestCoordinate(endpointID: string, fallbackIndex: number, startedAt?: string | null) {
  const route = routeTokenToModel(endpointID, fallbackIndex)
  const startedMs = parseMs(startedAt)
  if (!startedMs) return route[route.length - 1] || endpointTokenCoordinate(endpointID, fallbackIndex)
  const progress = clamp01((nowTick.value - startedMs) / FLOW_TO_MODEL_MS)
  return pointOnRoute(route, progress)
}

function terminalRequestCoordinate(endpointID: string, fallbackIndex: number, finishedAt?: string | null) {
  const route = routeTokenToCompleted(endpointID, fallbackIndex)
  const finishedMs = parseMs(finishedAt)
  if (!finishedMs) return route[route.length - 1] || completedTokenCoordinate(fallbackIndex)
  const progress = clamp01((nowTick.value - finishedMs) / FLOW_TO_COMPLETED_MS)
  return pointOnRoute(route, progress)
}

function terminalLogIsRecent(item: Pick<RecentFlowRequest, 'finished_at' | 'updated_at' | 'created_at'>) {
  const ts = parseMs(item.finished_at || item.updated_at || item.created_at)
  return ts > 0 && nowTick.value - ts < 2600
}

function terminalProgressIsRecent(item: {
  finished_at?: string | null
  updated_at?: string | null
  started_at?: string | null
  queued_at?: string | null
}) {
  const ts = parseMs(item.finished_at || item.updated_at || item.started_at || item.queued_at)
  return ts > 0 && nowTick.value - ts < 2600
}

function endpointBlockKind(node: ModelNodeView): RouteBlockKind | null {
  const streamKind = liveFlowState.value.blockedEndpointKinds.get(node.endpoint.id)
  if (streamKind) return streamKind
  if (node.healthStatus === 'unhealthy') return 'unhealthy'
  if (node.healthStatus === 'rate_limited') return 'rate_limited'
  if ((node.healthStatus === 'cooling_down' || node.tone === 'cooling') && node.cooldownRemaining > 0) return 'cooldown'
  if (node.cooldownRemaining > 0) return 'cooldown'
  if (node.candidate?.decision === 'queued') return 'queued'
  if (node.candidate?.decision === 'rejected') return 'rejected'
  return null
}

function blockKindLabel(kind: RouteBlockKind | null | undefined) {
  switch (kind) {
    case 'rate_limited':
      return 'rate limited'
    case 'cooldown':
      return 'cooldown'
    case 'unhealthy':
      return 'unhealthy'
    case 'queued':
      return 'queued'
    case 'rejected':
      return 'skipped'
    default:
      return ''
  }
}

function flowPathClasses(path: FlowPath) {
  const staticPath = path.id.includes('spine')
  return [
    'live-flow-wire',
    `live-flow-wire-${path.layer}`,
    staticPath && 'live-flow-wire-static',
    `live-flow-wire-state-${path.tone}`,
    path.active && 'live-flow-wire-emphasized',
  ]
}

function nodeWaitLabel(node: ModelNodeView) {
  if (node.availabilityIssue) return 'not routable'
  if (node.capacityState === 'rate-limited') return node.cooldownRemaining > 0 ? `cooldown ${countdownMs(node.cooldownRemaining)}` : 'rate limited'
  if (node.capacityState === 'cooling-down' && node.cooldownRemaining > 0) return `cooldown ${countdownMs(node.cooldownRemaining)}`
  if (node.capacityState === 'unhealthy') return 'unhealthy'
  if (node.capacityState === 'unknown' && mode.value === 'preview') return 'ready for preview'
  if (node.capacityState === 'unknown') return telemetry.connected ? 'syncing' : 'offline'
  if (node.cooldownRemaining > 0) return `cooldown ${countdownMs(node.cooldownRemaining)}`
  if (node.healthStatus === 'rate_limited') return 'rate limited'
  if (node.healthStatus === 'unhealthy') return 'unhealthy'
  if (node.activityState === 'routing') return 'routing now'
  if (node.activityState === 'fallback-active') return 'routing now'
  if (node.activityState === 'blocked' || node.activityState === 'holding') {
    const kind = endpointBlockKind(node)
    if (kind === 'cooldown' && node.cooldownRemaining > 0) return `cooldown ${countdownMs(node.cooldownRemaining)}`
    if (kind === 'rate_limited') return node.cooldownRemaining > 0 ? `cooldown ${countdownMs(node.cooldownRemaining)}` : 'rate limited'
    if (kind === 'unhealthy') return 'unhealthy'
    if (kind === 'rejected') return 'blocked'
    return node.activityState === 'holding' ? 'holding' : 'blocked'
  }
  return 'ready'
}

function nodeLimitRows(limits: ModelNodeView['limits']) {
  return limits.map((limit) => ({
    key: limit.key,
    label: limit.label,
    metric: limit.metric,
    period: limit.period,
    source: limit.source,
    actorId: limit.actorId,
    scopeId: limit.scopeId,
    targetType: limit.targetType,
    targetKey: limit.targetKey,
    configured: limit.configured,
    used: limit.used,
    percent: limit.percent,
    blocked: limit.blocked,
    blockedUntil: limit.blockedUntil,
    blockedRemainingMs: limit.blockedRemainingMs
  }))
}

function pointOnRoute(points: FlowPoint[], progress: number): FlowPoint {
  if (!points.length) return { x: FLOW_QUEUE_X, y: FLOW_COMPLETED_ENTRY_Y }
  if (points.length === 1) return points[0] || { x: FLOW_QUEUE_X, y: FLOW_COMPLETED_ENTRY_Y }
  const lengths = points.slice(1).map((point, index) => flowDistance(points[index] || point, point))
  const total = lengths.reduce((sum, value) => sum + value, 0)
  if (total <= 0) return points[points.length - 1] || points[0] || { x: FLOW_QUEUE_X, y: FLOW_COMPLETED_ENTRY_Y }
  let remaining = clamp01(progress) * total
  for (let index = 1; index < points.length; index++) {
    const segmentLength = lengths[index - 1] || 0
    const from = points[index - 1]
    const to = points[index]
    if (!from || !to) continue
    if (remaining <= segmentLength || index === points.length - 1) {
      const localProgress = segmentLength <= 0 ? 1 : remaining / segmentLength
      return {
        x: from.x + (to.x - from.x) * localProgress,
        y: from.y + (to.y - from.y) * localProgress
      }
    }
    remaining -= segmentLength
  }
  return points[points.length - 1] || points[0] || { x: FLOW_QUEUE_X, y: FLOW_COMPLETED_ENTRY_Y }
}

function flowDistance(a: FlowPoint, b: FlowPoint) {
  return Math.abs(a.x - b.x) + Math.abs(a.y - b.y)
}

function clamp01(value: number) {
  if (!Number.isFinite(value)) return 0
  return Math.min(1, Math.max(0, value))
}

function completedTokenCoordinate(index: number) {
  return {
    x: FLOW_COMPLETED_DOCK_X,
    y: Math.min(32, FLOW_COMPLETED_ENTRY_Y + index * 2.4)
  }
}

function endpointTokenCoordinate(endpointID: string, fallbackIndex: number) {
  const point = modelNodeForEndpoint(endpointID, fallbackIndex)
  return modelBayPoint(point)
}

async function startPreviewTraffic() {
  if (!selectedRoutableEndpoints.value.length || !selectedLane.value) {
    app.pushToast({
      title: 'No models to simulate',
      description: selectedEndpoints.value.length
        ? 'Enable a model with an active provider before running the Realtime Flow preview.'
        : 'Add group models before running the Realtime Flow preview.',
      tone: 'error'
    })
    return
  }

  if (previewTrafficRunning.value) return
  mode.value = 'preview'
  try {
    await pageData.load('realtime')
    await preview.enqueue({
      lane: selectedLane.value.name,
      route_kind: 'chat',
      request_count: PREVIEW_REQUEST_COUNT,
      arrival_interval_ms: PREVIEW_ARRIVAL_INTERVAL_MS,
      max_wait_ms: selectedLane.value.default_max_wait_ms,
      allow_fallback: selectedLane.value.allow_fallback,
      priority: selectedLane.value.default_priority,
      estimated_input_tokens: 1500,
      estimated_output_tokens: 600,
      synthetic_service_ms: PREVIEW_SERVICE_MS
    })
  } catch (error: any) {
    app.pushToast({
      title: 'Realtime Flow preview failed',
      description: error?.data?.message || error?.data?.error || error?.message || 'Could not start backend preview.',
      tone: 'error'
    })
  }
}

async function stopPreviewTraffic() {
  await preview.stopEnqueue()
}

async function resetPreviewTraffic() {
  try {
    await stopPreviewTraffic()
  } finally {
    resetPreviewVisualState()
  }
  await preview.resetSession()
}

function resetPreviewVisualState() {
  clearVisualTokenMemory()
  clearPreviewQueueAnchorMemory()
  clearQueueMotionTimers()
  visibleTokens.value = []
  queueMotionItems.value = []
}

function setFlowMode(nextMode: FlowMode) {
  mode.value = nextMode
  if (nextMode === 'preview' && !previewTrafficRunning.value && !preview.generatedCount) {
    resetPreviewVisualState()
  }
}

function setFlowTab(value: string) {
  if (value === 'live' || value === 'setup' || value === 'preview') setFlowMode(value)
}

function togglePreviewTraffic() {
  if (previewTrafficRunning.value) {
    void stopPreviewTraffic()
    return
  }
  void startPreviewTraffic()
}

</script>

<template>
  <div class="live-flow-page">
    <div class="live-flow-header">
      <div class="live-flow-title-block min-w-0">
        <h1 class="app-title">Realtime Flow</h1>
        <div class="live-flow-header-summary" aria-label="Selected flow summary">
          <nav class="live-flow-breadcrumb" aria-label="Realtime flow breadcrumb">
            <button class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed" type="button" :aria-current="selectedFlowTargetIsOverview ? 'page' : undefined" @click="selectFlowTarget('overview')">All</button>
            <template v-if="!selectedFlowTargetIsOverview">
              <UIcon name="i-lucide-chevron-right" aria-hidden="true" />
              <span>{{ currentFlowTargetLabel }}</span>
            </template>
          </nav>
        </div>
      </div>

      <div class="live-flow-toolbar">
        <div v-if="$slots['controls-extra']" class="live-flow-toolbar-extra">
          <slot name="controls-extra" />
        </div>
        <div class="live-flow-toolbar-primary">
          <UiTabs
            class="live-flow-mode-switch"
            :model-value="mode"
            :tabs="modeOptions"
            @update:model-value="setFlowTab"
          />

          <div class="live-flow-routing-group-card">
            <p class="live-flow-toolbar-title">{{ selectedFlowTargetIsOverview ? 'Routing Overview' : selectedFlowTargetIsModel ? 'Routing Model' : 'Routing Group' }}</p>
            <div class="live-flow-group-control">
              <RoutingTargetSelect
                v-model="selectedFlowTargetValue"
                class="live-flow-group-select live-flow-target-select"
                :items="realtimeFlowTargetOptionGroups"
                :loading="catalog.loading"
              />
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="!visibleLanes.length" class="app-surface mt-8 p-8">
      <UiEmptyState
        title="No routing groups yet"
        description="Create a group and add models before Realtime Flow can visualize queue movement."
        action-label="Open Setup"
        @action="navigateTo(modelRelayPath('/setup'))"
      />
    </div>

    <div v-else class="live-flow-board-shell">
      <section v-if="selectedFlowTargetIsOverview" class="live-flow-overview" aria-label="Routing overview">
        <div class="live-flow-overview-header">
          <div>
            <p>Targets used during the last hour, with the five most recently used targets shown when the last hour is quiet.</p>
          </div>
          <UiBadge tone="slate">{{ flowOverviewRows.length }}</UiBadge>
        </div>

        <div v-if="flowOverviewSections.length" class="live-flow-overview-sections">
          <section v-for="section in flowOverviewSections" :key="section.key" class="live-flow-overview-section">
            <h3>{{ section.label }}</h3>
            <div class="live-flow-overview-rows">
              <button class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed"
                v-for="row in section.rows"
                :key="row.value"
                type="button"
                :class="[
                  'live-flow-overview-row',
                  row.queued > 0 && 'live-flow-overview-row-queued',
                  row.active > 0 && 'live-flow-overview-row-active'
                ]"
                @click="selectFlowTarget(row.value)"
              >
                <span class="live-flow-overview-row-heading">
                  <span class="min-w-0">
                    <strong>{{ row.title }}</strong>
                    <small>{{ row.subtitle }}</small>
                    <span v-if="row.badges.length" class="live-flow-overview-model-badges">
                      <UiBadge v-for="badge in row.badges" :key="badge.key" :tone="badge.tone" size="sm">
                        <UIcon v-if="badge.key === 'guarded'" name="i-lucide-shield-check" aria-hidden="true" />
                        <UIcon v-else-if="badge.key === 'cooldown-timer'" name="i-lucide-clock-3" aria-hidden="true" />
                        {{ badge.label }}
                      </UiBadge>
                    </span>
                    <small class="live-flow-overview-last-used">{{ row.lastUsedAt ? `Used ${relativeTime(row.lastUsedAt)}` : 'Not used yet' }}</small>
                  </span>
                  <span class="live-flow-overview-row-meta">
                    <UIcon name="i-lucide-chevron-right" aria-hidden="true" />
                  </span>
                </span>
                <span class="live-flow-overview-metrics">
                  <span>
                    <small>In queue</small>
                    <strong>{{ number(row.queued) }}</strong>
                  </span>
                  <span>
                    <small>Models</small>
                    <strong>{{ number(row.models) }}</strong>
                  </span>
                  <span>
                    <small>Active requests</small>
                    <strong>{{ number(row.active) }}</strong>
                  </span>
                  <span>
                    <small>Recently completed</small>
                    <strong>{{ number(row.completed) }}</strong>
                  </span>
                </span>
              </button>
            </div>
          </section>
        </div>

        <div v-else class="live-flow-overview-empty">
          <strong>No routing targets available</strong>
          <p>Create a group or enable a model to populate the realtime overview.</p>
        </div>
      </section>

      <div v-else ref="stageRef" class="live-flow-board" :class="`live-flow-board-${mode}`" :style="boardStageStyle">
        <svg
          class="live-flow-wires"
          :viewBox="`0 0 ${flowStageSize.width} ${flowStageSize.height}`"
          aria-hidden="true"
        >
          <path
            v-for="path in skeletonRoutePaths"
            :key="path.id"
            :class="flowPathClasses(path)"
            :d="path.d"
            vector-effect="non-scaling-stroke"
          />
          <path
            v-for="path in overlayRoutePaths"
            :key="path.id"
            :class="flowPathClasses(path)"
            :d="path.d"
            vector-effect="non-scaling-stroke"
          />
          <path
            v-for="path in packetRoutePaths"
            :key="`${path.id}-packet`"
            :class="['live-flow-wire-packet', `live-flow-wire-packet-state-${path.tone}`]"
            :d="path.d"
            vector-effect="non-scaling-stroke"
          />
        </svg>

        <div class="live-flow-stage-columns" data-flow-measure>
          <section class="live-flow-stage-column live-flow-queue-column" data-flow-measure>
            <div class="live-flow-zone-label">
              <span>Queue</span>
            </div>

            <span
              :class="['live-flow-stage-port', 'live-flow-queue-out-port', `live-flow-stage-port-state-${activeBayTone}`, !circuitReady && 'live-flow-stage-port-hidden']"
              data-flow-port="queue-out"
              aria-hidden="true"
            />

            <aside class="live-flow-queue-panel" aria-label="Queued requests" data-flow-measure>
              <div class="live-flow-queue-panel-header">
                <p>Queue</p>
                <UiBadge tone="slate" size="sm">{{ boardQueuedCount }}</UiBadge>
              </div>
              <div v-if="queueMotionItems.length" class="live-flow-queue-stack">
                <div class="live-flow-queue-slot-background" aria-hidden="true">
                  <span v-for="slot in QUEUE_VISIBLE_SLOT_COUNT" :key="slot" class="live-flow-queue-slot-shell" />
                </div>
                <div class="live-flow-queue-motion-layer">
                  <div
                    v-for="item in queueMotionItems"
                    :key="item.id"
                    :class="[
                      'live-flow-queue-slot',
                      'live-flow-queue-motion-card',
                      `live-flow-queue-slot-${item.tone}`,
                      `live-flow-queue-motion-card-${item.phase}`
                    ]"
                    :style="queueMotionStyle(item)"
                  >
                    <article class="live-flow-queue-card">
                      <span class="live-flow-queue-card-dot" aria-hidden="true" />
                      <span class="min-w-0">
                        <slot v-if="$slots['queue-requester']" name="queue-requester" :item="item" />
                        <strong v-else>{{ item.title }}</strong>
                        <em>{{ item.subtitle }}</em>
                        <small>{{ item.detail }}</small>
                      </span>
                    </article>
                  </div>
                </div>
              </div>
              <div v-if="!queueMotionItems.length" class="live-flow-queue-empty" aria-hidden="true" />
              <div v-if="queueOverflowCount" class="live-flow-queue-overflow">
                +{{ number(queueOverflowCount) }} waiting
              </div>
            </aside>
          </section>

          <section class="live-flow-stage-column live-flow-model-column" data-flow-measure>
            <div class="live-flow-zone-label">
              <span>{{ selectedFlowTargetIsModel ? 'Model' : 'Group Models' }}</span>
            </div>

            <div v-if="modelNodes.length" class="live-flow-model-stack" data-flow-measure>
              <LiveFlowModelNode
                v-for="node in modelNodes"
                :key="node.endpoint.id"
                :rank="node.index + 1"
                :title="endpointModelSlug(node.endpoint)"
                :provider="endpointProviderSlug(node.endpoint)"
                :status="node.statusLabel"
                :status-tone="node.statusTone"
                :tone="node.tone"
                :route-state="getCircuitRouteState({ node })"
                :wait-label="nodeWaitLabel(node)"
                :limits="nodeLimitRows(node.limits)"
                :badges="node.badges"
                :endpoint-id="node.endpoint.id"
                :lane-id="selectedFlowTargetIsModel ? '' : selectedLane?.id || ''"
                :x="node.x"
                :y="node.y"
                :setup="mode === 'setup'"
              >
                <template #status>
                  <UiButton v-if="mode === 'setup'" class="live-flow-model-edit-button" size="sm" @click="openRealtimeModelSettings(node.endpoint.id)">Edit Model</UiButton>
                  <template v-else>{{ nodeWaitLabel(node) }}</template>
                </template>
                <template v-if="$slots['limit-label']" #limit-label="{ limit }">
                  <slot name="limit-label" :limit="limit" />
                </template>
              </LiveFlowModelNode>
            </div>

            <div v-else class="live-flow-empty-node" role="status">
              <span
                :class="['live-flow-empty-node-port', 'live-flow-empty-node-port-in', !circuitReady && 'live-flow-stage-port-hidden']"
                data-flow-port="no-model-card-in"
                aria-hidden="true"
              />
              <span
                :class="['live-flow-empty-node-port', 'live-flow-empty-node-port-out', !circuitReady && 'live-flow-stage-port-hidden']"
                data-flow-port="no-model-card-out"
                aria-hidden="true"
              />
              <div class="live-flow-empty-orbit" aria-hidden="true">
                <span />
                <span />
                <span />
              </div>
              <p class="live-flow-empty-kicker">Routing circuit</p>
              <h2>No models in this group</h2>
              <p>Add models to this group and they will appear here as routing nodes.</p>
              <UiButton size="sm" @click="openRealtimeGroupSettings">Open Group Settings</UiButton>
            </div>
          </section>

          <section
            :class="['live-flow-stage-column', 'live-flow-active-column', selectedFlowTargetSupportsConcurrentRequests && 'live-flow-active-column-concurrent']"
            data-flow-measure
          >
            <div class="live-flow-zone-label">
              <span>{{ selectedFlowTargetSupportsConcurrentRequests ? 'Active Requests' : 'Active Request' }}</span>
            </div>

            <div
              :class="[
                'live-flow-active-bay',
                `live-flow-active-bay-state-${activeBayTone}`,
                activeBayItem && 'live-flow-active-bay-occupied',
                selectedFlowTargetSupportsConcurrentRequests && 'live-flow-active-bay-concurrent'
              ]"
              :aria-label="selectedFlowTargetSupportsConcurrentRequests ? 'Active requests' : 'Active request bay'"
              data-flow-measure
            >
              <span :class="['live-flow-stage-port', 'live-flow-active-bay-in-port', !circuitReady && 'live-flow-stage-port-hidden']" data-flow-port="active-bay-in" aria-hidden="true" />
              <span :class="['live-flow-stage-port', 'live-flow-active-bay-out-port', !circuitReady && 'live-flow-stage-port-hidden']" data-flow-port="active-bay-out" aria-hidden="true" />
              <template v-if="selectedFlowTargetSupportsConcurrentRequests">
                <div class="live-flow-active-panel-header">
                  <UiBadge tone="slate" size="sm">{{ activeBayItems.length }}</UiBadge>
                </div>
                <TransitionGroup v-if="activeBayItems.length" name="live-flow-bay" tag="div" class="live-flow-active-stack">
                  <article
                    v-for="item in activeBayItems"
                    :key="item.id"
                    :class="['live-flow-active-bay-card', `live-flow-active-bay-state-${item.tone}`]"
                  >
                    <span class="live-flow-active-bay-dot" aria-hidden="true" />
                    <span class="min-w-0">
                      <slot v-if="$slots['active-requester']" name="active-requester" :item="item" />
                      <em>{{ item.subtitle }}</em>
                      <small>{{ item.detail }}</small>
                    </span>
                    <b>{{ item.state }}</b>
                  </article>
                </TransitionGroup>
                <div v-else class="live-flow-active-bay-empty live-flow-active-list-empty">
                  <span>No active requests</span>
                  <strong>ready</strong>
                </div>
              </template>
              <Transition v-else name="live-flow-bay" mode="out-in">
                <article v-if="activeBayItem" :key="activeBayTransitionKey" class="live-flow-active-bay-card">
                  <span class="live-flow-active-bay-dot" aria-hidden="true" />
                  <span class="min-w-0">
                    <slot v-if="$slots['active-requester']" name="active-requester" :item="activeBayItem" />
                    <em>{{ activeBayItem.subtitle }}</em>
                    <small>{{ activeBayItem.detail }}</small>
                  </span>
                  <b>{{ activeBayItem.state }}</b>
                </article>
                <div v-else class="live-flow-active-bay-empty">
                  <span>Active Request Bay</span>
                  <strong>{{ activeBayLabel }}</strong>
                </div>
              </Transition>
            </div>
          </section>

          <section class="live-flow-stage-column live-flow-completed-column" data-flow-measure>
            <div class="live-flow-zone-label">
              <span>Completed</span>
            </div>

            <span
              :class="['live-flow-stage-port', 'live-flow-completed-in-port', `live-flow-stage-port-state-${activeBayTone}`, !circuitReady && 'live-flow-stage-port-hidden']"
              data-flow-port="completed-in"
              aria-hidden="true"
            />

            <aside class="live-flow-completed-rail" aria-label="Completed events" data-flow-measure>
              <TransitionGroup name="live-flow-feed" tag="div" class="live-flow-feed">
                <LiveFlowCompletedEventCard v-for="event in completionFeed" :key="event.id" :item="event">
                  <template v-if="$slots['completed-requester']" #requester><slot name="completed-requester" :item="event" /></template>
                </LiveFlowCompletedEventCard>
              </TransitionGroup>

              <div v-if="!completionFeed.length" class="live-flow-rail-empty">
                <span aria-hidden="true" />
                <strong>Awaiting terminal events</strong>
                <p>Completed, failed, and cancelled requests will settle into this feed.</p>
              </div>
            </aside>
          </section>
        </div>

        <div v-if="debugFlowPorts" class="live-flow-port-debug-layer" aria-hidden="true">
          <div class="live-flow-debug-stage-bounds" />
          <span
            v-for="[name, point] in Object.entries(flowPorts)"
            :key="name"
            :style="{ left: `${point.x}px`, top: `${point.y}px` }"
            class="live-flow-debug-port"
          >
            {{ name }} {{ Math.round(point.x) }},{{ Math.round(point.y) }}
          </span>
          <span
            v-for="path in routePaths"
            :key="`path-${path.id}`"
            :style="{ left: `${path.labelPoint.x}px`, top: `${path.labelPoint.y}px` }"
            class="live-flow-debug-path"
          >
            {{ path.id }}
          </span>
          <div v-if="debugMissingFlowAnchors.length" class="live-flow-debug-missing">
            Missing: {{ debugMissingFlowAnchors.join(', ') }}
          </div>
        </div>

        <div v-if="debugLiveFlowEvents" class="live-flow-event-debug-panel" aria-hidden="true">
          <strong>Realtime Flow state</strong>
          <p>
            active {{ liveFlowState.activeRequestID || 'none' }}
            · endpoint {{ liveFlowState.activeEndpointID || 'none' }}
            · fallback {{ liveFlowState.fallbackActive ? 'yes' : 'no' }}
            · holding {{ liveFlowState.holding ? 'yes' : 'no' }}
          </p>
          <p v-for="node in modelNodes" :key="`debug-node-${node.endpoint.id}`">
            #{{ node.index + 1 }} {{ modelTitle(node.endpoint) }}:
            {{ node.activityState }} / {{ node.capacityState }}
          </p>
          <p>
            events
            <span v-for="event in debugLiveFlowEventItems" :key="`${event.timestamp}-${event.type}`">{{ event.type }}</span>
          </p>
        </div>
      </div>

      <div v-if="mode !== 'live'" class="live-flow-control-dock">
        <div v-if="mode === 'setup'" class="space-y-4">
          <div class="live-flow-control-row">
            <div>
              <p class="live-flow-dock-kicker">Settings</p>
              <h2>Edit Group Settings</h2>
              <p>{{ selectedLane ? `Adjust ranked models and cooldown max wait for ${selectedLane.name}.` : 'Choose a routing group to edit its settings.' }}</p>
            </div>
            <UiButton :disabled="!selectedLane" @click="openRealtimeGroupSettings">Edit Group Settings</UiButton>
          </div>
        </div>

        <div v-else class="space-y-4">
          <div class="live-flow-control-row">
            <div>
              <p class="live-flow-dock-kicker">Preview stream</p>
              <h2>Backend preview engine</h2>
              <p>{{ previewPhaseText }}</p>
            </div>
            <div class="grid min-w-[18rem] grid-cols-[minmax(0,1fr)_auto] gap-2">
              <UiButton class="w-full" :disabled="previewPlanLoading && !previewTrafficRunning" @click="togglePreviewTraffic">
                {{ previewTrafficRunning ? 'Stop enqueue' : previewPlanLoading ? 'Starting engine...' : 'Run traffic enqueue' }}
              </UiButton>
              <UiButton tone="ghost" @click="resetPreviewTraffic">Reset</UiButton>
            </div>
          </div>

          <div class="ui-metric-strip live-flow-dock-metrics live-flow-preview-metrics">
            <div class="app-metric">
              <span>generated</span>
              <strong>{{ number(previewGeneratedDisplayCount) }}</strong>
            </div>
            <div class="app-metric">
              <span>queued</span>
              <strong>{{ number(previewQueuedCount) }}</strong>
            </div>
            <div class="app-metric">
              <span>active</span>
              <strong>{{ number(previewActiveCount) }}</strong>
            </div>
            <div class="app-metric">
              <span>completed</span>
              <strong>{{ number(previewCompletedDisplayCount) }}</strong>
            </div>
          </div>
        </div>
      </div>

      <section class="live-flow-recent-section" aria-labelledby="live-flow-recent-title">
        <div class="live-flow-recent-header">
          <div>
            <p class="live-flow-dock-kicker">Recent</p>
            <h2 id="live-flow-recent-title">Groups / Models Used</h2>
          </div>
          <UiBadge tone="slate">{{ number(recentFlowUsage.length) }}</UiBadge>
        </div>

        <UiTable
          v-if="recentFlowUsage.length"
          :columns="['Last used', 'Group', 'Provider / Model']"
        >
          <tr v-for="entry in recentFlowUsage" :key="entry.key" class="live-flow-recent-row">
            <td class="live-flow-recent-cell">
              <p>{{ relativeTime(entry.last_used_at) }}</p>
              <p class="live-flow-recent-secondary">{{ clockTime(entry.last_used_at) }}</p>
            </td>
            <td class="live-flow-recent-cell">
              <button
                v-if="canOpenRecentUsage(entry)"
                type="button"
                class="live-flow-recent-link"
                @click="openRecentUsage(entry)"
              >
                {{ entry.lane_name }}
              </button>
            </td>
            <td class="live-flow-recent-cell">
              <button
                type="button"
                class="live-flow-recent-link live-flow-recent-link-model"
                @click="openRecentModel(entry)"
              >
                {{ recentUsageRoute(entry) }}
              </button>
              <p class="live-flow-recent-secondary">{{ number(entry.request_count) }} request{{ entry.request_count === 1 ? '' : 's' }}</p>
            </td>
          </tr>
        </UiTable>

        <p v-else class="live-flow-recent-empty">
          Recent groups and models will appear here after gateway traffic completes.
        </p>
      </section>
    </div>

    <GroupsGroupEditDrawer
      :open="groupSettingsOpen"
      :group-id="selectedLaneID"
      subtitle="Add models and adjust ranked membership without leaving Realtime Flow."
      @close="groupSettingsOpen = false"
    />

    <ModelsModelEditDrawer
      :open="!!modelSettingsEndpointID"
      :endpoint-id="modelSettingsEndpointID"
      subtitle="Edit this model without leaving Realtime Flow."
      @close="closeRealtimeModelSettings"
    />
  </div>
</template>

<style scoped>
.live-flow-page {
  margin: -0.35rem -1.5rem -2rem;
}

.live-flow-header {
  display: flex;
  gap: 1.5rem;
  align-items: flex-end;
  justify-content: space-between;
  padding: 0.35rem 1.5rem 1.5rem;
}

.live-flow-toolbar {
  display: grid;
  grid-template-columns: auto minmax(0, auto);
  grid-template-rows: auto auto;
  gap: 0.65rem;
  align-items: stretch;
  justify-content: flex-start;
  width: fit-content;
  max-width: 100%;
}

.live-flow-toolbar-primary {
  display: contents;
}

.live-flow-toolbar-extra {
  grid-column: 1;
  grid-row: 2;
  min-width: 0;
  align-self: stretch;
}

.live-flow-toolbar-primary > .live-flow-mode-switch {
  grid-column: 2;
  grid-row: 1;
}

.live-flow-toolbar-primary > .live-flow-routing-group-card {
  grid-column: 2;
  grid-row: 2;
}

.live-flow-mode-switch {
  min-width: 0;
}

.live-flow-group-select {
  min-width: 14rem;
  width: 100%;
}

.live-flow-target-select {
  min-height: 2.85rem;
  border-radius: 0.85rem;
}

.live-flow-board-shell {
  overflow: visible;
  padding: 0 1rem 1rem;
}

.live-flow-board {
  position: relative;
  width: 100%;
  min-width: 0;
  min-height: 620px;
  overflow: hidden;
  isolation: isolate;
  border: 1px solid var(--app-border);
  border-radius: 1.15rem;
  background: var(--app-surface);
  box-shadow: none;
}

.live-flow-wires {
  position: absolute;
  inset: 0;
  z-index: 4;
  width: 100%;
  height: 100%;
  overflow: visible;
}

.live-flow-wire {
  fill: none;
  stroke: color-mix(in srgb, var(--app-border-strong) 88%, transparent);
  stroke-linecap: butt;
  stroke-linejoin: miter;
  stroke-width: 4.5;
  vector-effect: non-scaling-stroke;
  animation: none;
}

.live-flow-wire-active {
  stroke-width: 5.6;
  filter: none;
}

.live-flow-wire-static.live-flow-wire-active {
  stroke-width: 5.2;
}

.live-flow-wire-idle {
  stroke: color-mix(in srgb, var(--app-border-strong) 88%, transparent);
}

.live-flow-wire-working {
  color: #10b981;
  stroke: #10b981;
}

.live-flow-wire-rate_limited {
  color: #f59e0b;
  stroke: #f59e0b;
}

.live-flow-wire-cooldown {
  color: var(--app-accent);
  stroke: var(--app-accent);
}

.live-flow-wire-unhealthy {
  color: #ef4444;
  stroke: #ef4444;
}

.live-flow-junction {
  position: absolute;
  z-index: 16;
  width: 0.72rem;
  height: 0.72rem;
  border: 1px solid color-mix(in srgb, var(--app-border-strong) 88%, transparent);
  border-radius: 999px;
  background: var(--app-surface);
  transform: translate(-50%, -50%);
  pointer-events: none;
}

.live-flow-junction-active {
  box-shadow: none;
}

.live-flow-junction-idle {
  color: color-mix(in srgb, var(--app-border-strong) 88%, transparent);
  border-color: color-mix(in srgb, var(--app-border-strong) 88%, transparent);
  background: var(--app-surface);
}

.live-flow-junction-working {
  color: #10b981;
  border-color: #10b981;
  background: #10b981;
}

.live-flow-junction-rate_limited {
  color: #f59e0b;
  border-color: #f59e0b;
  background: #f59e0b;
}

.live-flow-junction-cooldown {
  color: var(--app-accent);
  border-color: var(--app-accent);
  background: var(--app-accent);
}

.live-flow-junction-unhealthy {
  color: #ef4444;
  border-color: #ef4444;
  background: #ef4444;
}

.live-flow-zone-label {
  z-index: 8;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  border: 0;
  background: transparent;
  padding: 0;
  color: color-mix(in srgb, var(--app-heading) 44%, var(--app-muted));
  font-family: var(--font-editorial);
  font-size: clamp(1.02rem, 1.05vw, 1.36rem);
  font-weight: 500;
  line-height: 1.1;
  letter-spacing: 0.01em;
  text-align: center;
  text-transform: none;
  opacity: 0.62;
  text-shadow: 0 1px 0 color-mix(in srgb, var(--app-surface) 62%, transparent);
  pointer-events: none;
}

.live-flow-zone-label strong {
  display: none;
}

.live-flow-queue-panel {
  position: absolute;
  z-index: 9;
  top: 4.4rem;
  bottom: 1.35rem;
  left: 10.5%;
  width: 14.75rem;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 1rem;
  background: color-mix(in srgb, var(--app-surface) 96%, var(--app-bg));
  padding: 1rem;
  transform: translateX(-50%);
  box-shadow: none;
  pointer-events: none;
}

.live-flow-queue-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

.live-flow-queue-panel-header p {
  color: var(--app-heading);
  font-size: 0.78rem;
  font-weight: 850;
}

.live-flow-queue-overflow {
  position: absolute;
  z-index: 16;
  left: 10.5%;
  top: 86%;
  width: 9.25rem;
  transform: translateX(-50%);
  border: 1px solid var(--app-border);
  border-radius: 0.55rem;
  background: var(--app-surface);
  padding: 0.48rem 0.6rem;
  color: var(--app-muted);
  font-size: 0.72rem;
  font-weight: 800;
  text-align: center;
}

.live-flow-model-card {
  position: absolute;
  z-index: 24;
  width: 27rem;
  min-width: 27rem;
  transform: translate(-50%, -50%);
  transition:
    left 0.32s ease,
    top 0.32s ease,
    opacity 0.2s ease,
    filter 0.2s ease;
}

.live-flow-model-card-selected,
.live-flow-model-card-active {
  filter: none;
}

.live-flow-model-card-cooling {
  filter: none;
}

.live-flow-model-card-rejected {
  filter: none;
}

.live-flow-model-card-muted {
  opacity: 0.72;
}

.live-flow-model-card-setup {
  width: 29rem;
  min-width: 29rem;
}

.live-flow-model-bay {
  position: absolute;
  z-index: 15;
  width: 10.4rem;
  height: 2.85rem;
  border: 1.5px dashed color-mix(in srgb, var(--app-border-strong) 88%, transparent);
  border-radius: 0.7rem;
  background: var(--app-surface-muted);
  transform: translate(-50%, -50%);
  pointer-events: none;
}

.live-flow-model-bay::before {
  content: "";
  position: absolute;
  left: 50%;
  top: -0.78rem;
  height: 0.78rem;
  border-left: 1.5px dashed color-mix(in srgb, var(--app-border-strong) 88%, transparent);
  transform: translateX(-50%);
}

.live-flow-model-bay-working {
  border-color: #10b981;
  background: color-mix(in srgb, #10b981 4%, var(--app-surface));
  box-shadow: none;
}

.live-flow-model-bay-working::before {
  border-left-color: #10b981;
}

.live-flow-model-bay-rate_limited {
  border-color: #f59e0b;
  background: color-mix(in srgb, #f59e0b 4.5%, var(--app-surface));
}

.live-flow-model-bay-rate_limited::before {
  border-left-color: #f59e0b;
}

.live-flow-model-bay-cooldown {
  border-color: var(--app-accent);
  background: color-mix(in srgb, var(--app-accent) 4.5%, var(--app-surface));
}

.live-flow-model-bay-cooldown::before {
  border-left-color: var(--app-accent);
}

.live-flow-model-bay-unhealthy {
  border-color: #ef4444;
  background: color-mix(in srgb, #ef4444 4.5%, var(--app-surface));
}

.live-flow-model-bay-unhealthy::before {
  border-left-color: #ef4444;
}

.live-flow-control-dock {
  position: relative;
  z-index: 1;
  margin-top: 1rem;
  width: 100%;
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: 1.4rem;
  background: var(--app-surface);
  padding: 1rem 1.1rem;
  box-shadow: none;
  backdrop-filter: none;
}

.live-flow-control-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1.5rem;
}

.live-flow-control-dock h2 {
  margin-top: 0.3rem;
  color: var(--app-heading);
  font-size: 1.12rem;
  font-weight: 850;
  letter-spacing: -0.02em;
}

.live-flow-control-dock p {
  margin-top: 0.45rem;
  color: var(--app-copy);
  font-size: 0.82rem;
  line-height: 1.45rem;
}

.live-flow-dock-kicker {
  margin: 0 !important;
  color: var(--app-accent-text) !important;
  font-size: 0.64rem !important;
  font-weight: 900;
  letter-spacing: 0.18em;
  line-height: 1rem !important;
  text-transform: uppercase;
}

.live-flow-recent-section {
  margin-top: 1rem;
}

.live-flow-recent-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  padding: 0 0 0.9rem;
}

.live-flow-recent-header h2 {
  margin-top: 0.28rem;
  color: var(--app-heading);
  font-size: 1.12rem;
  font-weight: 850;
  letter-spacing: -0.02em;
}

.live-flow-recent-row {
  transition: background-color 0.16s ease;
}

.live-flow-recent-row:hover {
  background: var(--app-nav-hover-bg);
}

.live-flow-recent-cell {
  padding: 0.78rem 1rem;
  color: var(--app-copy);
}

.live-flow-recent-secondary {
  margin-top: 0.18rem;
  color: var(--app-subtle);
  font-size: 0.72rem;
}

.live-flow-recent-link {
  display: inline-flex;
  max-width: 100%;
  min-width: 0;
  border: 0;
  background: transparent;
  padding: 0;
  color: var(--app-heading);
  font: inherit;
  font-weight: 800;
  line-height: 1.35;
  text-align: left;
  text-decoration: underline;
  text-decoration-color: color-mix(in srgb, var(--app-accent) 48%, transparent);
  text-underline-offset: 0.2em;
  cursor: pointer;
}

.live-flow-recent-link:hover {
  color: var(--app-accent-text);
}

.live-flow-recent-link-model {
  display: inline-block;
  overflow-wrap: anywhere;
  vertical-align: bottom;
}

.live-flow-recent-empty {
  border: 1px dashed var(--app-border);
  border-radius: 1rem;
  padding: 1rem;
  color: var(--app-copy);
  font-size: 0.86rem;
  line-height: 1.5;
}

.live-flow-dock-metrics {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0;
}

.live-flow-dock-metrics div {
  border: 0;
  border-right: 1px solid var(--app-border);
  border-radius: 0;
  background: transparent;
  padding: 0.65rem;
}

.live-flow-dock-metrics div:last-child {
  border-right: 0;
}

.live-flow-dock-metrics span {
  display: block;
  color: var(--app-subtle);
  font-size: 0.62rem;
  font-weight: 900;
  letter-spacing: 0.14em;
  text-transform: uppercase;
}

.live-flow-dock-metrics strong {
  display: block;
  margin-top: 0.25rem;
  color: var(--app-heading);
  font-size: 1rem;
  font-weight: 850;
}

.live-flow-preview-metrics {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.live-flow-completed-rail {
  position: absolute;
  z-index: 34;
  top: 4.4rem;
  right: 1rem;
  bottom: 1.35rem;
  width: 24rem;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 1rem;
  background: color-mix(in srgb, var(--app-surface) 98%, var(--app-bg));
  padding: 0;
  box-shadow: none;
  backdrop-filter: none;
}

.live-flow-model-edit-button {
  letter-spacing: 0 !important;
  text-transform: none !important;
}

.live-flow-feed {
  display: grid;
  max-height: calc(100% - 2.75rem);
  gap: 0.65rem;
  overflow-y: auto;
  padding: 0 1rem;
}

.live-flow-rail-empty,
.live-flow-empty-node {
  color: var(--app-muted);
}

.live-flow-rail-empty {
  border: 1px dashed var(--app-border-strong);
  border-radius: 1rem;
  padding: 1rem;
  font-size: 0.82rem;
  line-height: 1.45rem;
}

.live-flow-empty-node {
  position: absolute;
  z-index: 20;
  left: 50%;
  top: 50%;
  width: 26rem;
  transform: translate(-50%, -50%);
}

.live-flow-feed-enter-active,
.live-flow-feed-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.live-flow-feed-enter-from,
.live-flow-feed-leave-to {
  opacity: 0;
  transform: translateX(12px);
}

.live-flow-feed-move {
  transition: transform 0.24s ease;
}

.live-flow-page {
  --flow-radius: 1.25rem;
  --flow-green: #22c55e;
  --flow-aqua: var(--app-accent);
  --flow-teal: var(--app-accent);
  --flow-amber: #f59e0b;
  --flow-rose: #f43f5e;
  margin: -0.35rem -1.5rem -2rem;
}

.live-flow-header {
  gap: 2rem;
  padding: 0.35rem 1.5rem 1.35rem;
}

.live-flow-title-block {
  max-width: 55rem;
  padding-left: 0.7rem;
}

.live-flow-title-block .app-title {
  letter-spacing: -0.045em;
}

.live-flow-title-block .app-lead {
  max-width: 48rem;
}

.live-flow-header-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.65rem;
  margin-top: 1rem;
  color: var(--app-muted);
  font-size: 0.78rem;
  line-height: 1.2rem;
}

.live-flow-breadcrumb {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 0.35rem;
}

.live-flow-breadcrumb button {
  border: 0;
  background: transparent;
  padding: 0;
  color: var(--app-accent-text);
  font: inherit;
  font-weight: 800;
}

.live-flow-breadcrumb button:hover {
  text-decoration: underline;
  text-underline-offset: 0.18rem;
}

.live-flow-breadcrumb svg {
  width: 0.85rem;
  height: 0.85rem;
  flex: 0 0 auto;
  color: var(--app-subtle);
}

.live-flow-breadcrumb span {
  overflow: hidden;
  color: var(--app-heading);
  font-weight: 750;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-flow-toolbar {
  --flow-toolbar-control-width: 15.75rem;
  display: grid;
  grid-template-columns: auto var(--flow-toolbar-control-width);
  grid-template-rows: auto auto;
  align-items: stretch;
  justify-content: flex-start;
  gap: 0.65rem;
  width: fit-content;
  max-width: 100%;
}

.live-flow-toolbar-primary {
  display: contents;
}

.live-flow-toolbar-extra {
  grid-column: 1;
  grid-row: 2;
  min-width: 0;
  align-self: stretch;
}

.live-flow-toolbar-primary > .live-flow-mode-switch {
  grid-column: 2;
  grid-row: 1;
}

.live-flow-toolbar-primary > .live-flow-routing-group-card {
  grid-column: 2;
  grid-row: 2;
}

.live-flow-routing-group-card {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  width: min(100%, var(--flow-toolbar-control-width));
  border: 1px solid color-mix(in srgb, var(--app-border) 86%, transparent);
  border-radius: 1rem;
  background: var(--app-subsurface);
  padding: 0.55rem;
  box-shadow: var(--app-subpanel-shadow);
  backdrop-filter: none;
}

.live-flow-toolbar-title {
  margin: 0 0 0.15rem;
  color: color-mix(in srgb, var(--app-heading) 84%, var(--app-muted));
  font-size: 0.78rem;
  font-weight: 850;
  letter-spacing: 0.02em;
}

.live-flow-group-control {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  min-width: 0;
  width: 100%;
}

.live-flow-mode-switch {
  width: min(100%, var(--flow-toolbar-control-width));
}

.live-flow-target-select:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 2px;
}

.live-flow-group-select {
  min-width: 0;
  flex: 1 1 auto;
  width: 100%;
}

.live-flow-target-select {
  min-height: 2.65rem;
  width: 100%;
}

.live-flow-board-shell {
  overflow: visible;
  padding: 0 1rem 1.1rem;
}

.live-flow-board {
  min-width: 0;
  min-height: 620px;
  border-color: color-mix(in srgb, var(--app-border-strong) 54%, var(--app-border));
  border-radius: var(--flow-radius);
  background: var(--app-surface);
  box-shadow: none;
}

.live-flow-board::before {
  content: "";
  position: absolute;
  inset: 0;
  z-index: 1;
  background: transparent;
  pointer-events: none;
}

.live-flow-wires {
  z-index: 7;
}

.live-flow-wire {
  stroke: color-mix(in srgb, var(--app-border-strong) 70%, transparent);
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 3.8;
}

.live-flow-wire-static {
  stroke-width: 3.45;
}

.live-flow-wire-idle {
  stroke: color-mix(in srgb, var(--app-border-strong) 62%, var(--app-border));
  opacity: 0.68;
}

.live-flow-wire-active {
  stroke-width: 5.8;
  opacity: 1;
  filter: none;
}

.live-flow-wire-static.live-flow-wire-active {
  stroke-width: 5;
}

.live-flow-wire-working {
  color: var(--flow-green);
  stroke: var(--flow-green);
}

.live-flow-wire-fallback,
.live-flow-wire-rate_limited {
  color: var(--flow-amber);
  stroke: var(--flow-amber);
}

.live-flow-wire-cooldown {
  color: var(--flow-aqua);
  stroke: var(--flow-aqua);
}

.live-flow-wire-unhealthy {
  color: var(--flow-rose);
  stroke: var(--flow-rose);
}

.live-flow-wire-pulse {
  fill: none;
  stroke: currentColor;
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 8.5;
  stroke-dasharray: 1 23;
  stroke-dashoffset: 0;
  opacity: 0.95;
  vector-effect: non-scaling-stroke;
  filter: none;
  animation: liveFlowCurrent 1.1s linear infinite;
}

.live-flow-wire-pulse-working {
  color: var(--flow-green);
}

.live-flow-wire-pulse-fallback,
.live-flow-wire-pulse-cooldown,
.live-flow-wire-pulse-rate_limited {
  color: var(--flow-amber);
}

.live-flow-wire-pulse-unhealthy {
  color: var(--flow-rose);
}

.live-flow-junction {
  z-index: 16;
  width: 0.82rem;
  height: 0.82rem;
  border-width: 1.5px;
  box-shadow: inset 0 0 0 2px color-mix(in srgb, var(--app-bg) 30%, transparent);
}

.live-flow-junction-active {
  box-shadow: none;
}

.live-flow-junction-working {
  color: var(--flow-green);
  border-color: var(--flow-green);
  background: var(--flow-green);
}

.live-flow-junction-fallback,
.live-flow-junction-rate_limited {
  color: var(--flow-amber);
  border-color: var(--flow-amber);
  background: var(--flow-amber);
}

.live-flow-junction-cooldown {
  color: var(--flow-aqua);
  border-color: var(--flow-aqua);
  background: var(--flow-aqua);
}

.live-flow-junction-unhealthy {
  color: var(--flow-rose);
  border-color: var(--flow-rose);
  background: var(--flow-rose);
}

.live-flow-zone-label {
  z-index: 18;
  background: transparent;
  box-shadow: none;
}

.live-flow-queue-panel {
  z-index: 10;
  top: 4.65rem;
  bottom: 1.55rem;
  width: 15.5rem;
  border-color: color-mix(in srgb, var(--app-border-strong) 38%, var(--app-border));
  border-radius: 1rem;
  background: var(--app-surface);
  box-shadow: none;
}

.live-flow-queue-rungs {
  display: grid;
  gap: 0.72rem;
  margin-top: 1rem;
}

.live-flow-queue-rungs span {
  height: 2.55rem;
  border: 1px dashed color-mix(in srgb, var(--app-border-strong) 42%, transparent);
  border-radius: 0.72rem;
  background: var(--app-surface-muted);
}

.live-flow-queue-empty {
  position: static;
  display: block;
  min-height: var(--queue-card-height, 58px);
  margin-top: 0.9rem;
  border: 1px dashed color-mix(in srgb, var(--app-border-strong) 32%, transparent);
  border-radius: 0.72rem;
  background: var(--app-surface-muted);
  opacity: 0.62;
}

.live-flow-queue-overflow {
  position: static;
  width: auto;
  margin-top: 0.7rem;
  transform: none;
  border-color: color-mix(in srgb, var(--app-accent) 28%, var(--app-border));
  border-radius: 999px;
  background: var(--app-subsurface);
  color: var(--app-heading);
  box-shadow: none;
}

.live-flow-token-layer {
  position: absolute;
  inset: 0;
  z-index: 30;
  pointer-events: none;
}

.live-flow-token-enter-active,
.live-flow-token-leave-active {
  transition: opacity 0.22s ease, transform 0.22s ease;
}

.live-flow-token-enter-from,
.live-flow-token-leave-to {
  opacity: 0;
}

.live-flow-token-move {
  transition: transform 0.22s ease;
}

.live-flow-model-bay {
  z-index: 14;
  width: 11.35rem;
  height: 3.1rem;
  border-color: color-mix(in srgb, var(--app-border-strong) 64%, transparent);
  border-radius: 0.82rem;
  background: var(--app-surface);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--app-heading) 3%, transparent);
}

.live-flow-model-bay-working {
  border-color: var(--flow-green);
  background: color-mix(in srgb, var(--flow-green) 6%, var(--app-surface));
  box-shadow: none;
}

.live-flow-model-bay-fallback,
.live-flow-model-bay-rate_limited {
  border-color: var(--flow-amber);
  background: color-mix(in srgb, var(--flow-amber) 6%, var(--app-surface));
}

.live-flow-model-bay-fallback::before,
.live-flow-model-bay-rate_limited::before {
  border-left-color: var(--flow-amber);
}

.live-flow-model-bay-cooldown {
  border-color: var(--flow-aqua);
  background: color-mix(in srgb, var(--flow-aqua) 5.5%, var(--app-surface));
}

.live-flow-model-bay-unhealthy {
  border-color: var(--flow-rose);
  background: color-mix(in srgb, var(--flow-rose) 5.5%, var(--app-surface));
}

.live-flow-control-dock {
  min-width: 0;
  border-color: color-mix(in srgb, var(--app-border-strong) 38%, var(--app-border));
  border-radius: 1rem;
  background: var(--app-surface);
  padding: 1rem;
  box-shadow: none;
}

.live-flow-control-dock h2 {
  font-size: 1.05rem;
  letter-spacing: -0.015em;
}

.live-flow-control-dock p {
  max-width: 52rem;
}

.live-flow-dock-metrics {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.live-flow-preview-metrics {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.live-flow-dock-metrics div {
  border-radius: 0;
  background: transparent;
  padding: 0.78rem 0.85rem;
}

.live-flow-dock-metrics strong {
  font-size: 1.16rem;
}

.live-flow-completed-rail {
  z-index: 34;
  top: 4.65rem;
  right: 1rem;
  bottom: 1.55rem;
  width: 25rem;
  border-color: color-mix(in srgb, var(--app-border-strong) 38%, var(--app-border));
  border-radius: 1rem;
  background: var(--app-surface);
  box-shadow: none;
}

.live-flow-feed {
  gap: 0.72rem;
  max-height: calc(100%);
  padding-top: 2rem;
  scrollbar-width: thin;
}

.live-flow-rail-empty {
  display: grid;
  justify-items: start;
  gap: 0.42rem;
  border-color: color-mix(in srgb, var(--app-border-strong) 46%, transparent);
  background: var(--app-surface-muted);
}

.live-flow-rail-empty span {
  width: 1.7rem;
  height: 0.28rem;
  border-radius: 999px;
  background: var(--app-accent);
}

.live-flow-rail-empty strong {
  color: var(--app-heading);
  font-size: 0.82rem;
  font-weight: 850;
}

.live-flow-rail-empty p {
  color: var(--app-muted);
  font-size: 0.76rem;
  line-height: 1.25rem;
}

.live-flow-empty-node {
  display: grid;
  justify-items: center;
  gap: 0.75rem;
  width: 30rem;
  border: 1px solid color-mix(in srgb, var(--app-border-strong) 42%, var(--app-border));
  border-radius: 1.1rem;
  background: var(--app-surface);
  padding: 1.35rem;
  text-align: center;
  box-shadow: none;
}

.live-flow-empty-orbit {
  position: relative;
  width: 8.25rem;
  height: 4.6rem;
  margin-bottom: 0.15rem;
}

.live-flow-empty-orbit::before,
.live-flow-empty-orbit::after {
  content: "";
  position: absolute;
  inset: 0.65rem 0;
  border: 1px solid color-mix(in srgb, var(--app-border-strong) 48%, transparent);
  border-radius: 999px;
}

.live-flow-empty-orbit::after {
  inset: 1.4rem 1.2rem;
  border-style: dashed;
}

.live-flow-empty-orbit span {
  position: absolute;
  top: 50%;
  width: 0.62rem;
  height: 0.62rem;
  border-radius: 999px;
  background: var(--app-accent);
  box-shadow: none;
  transform: translateY(-50%);
  animation: liveFlowEmptyCurrent 3.8s ease-in-out infinite;
}

.live-flow-empty-orbit span:nth-child(1) {
  left: 0.8rem;
}

.live-flow-empty-orbit span:nth-child(2) {
  left: 50%;
  animation-delay: 0.35s;
}

.live-flow-empty-orbit span:nth-child(3) {
  right: 0.8rem;
  animation-delay: 0.7s;
}

.live-flow-empty-kicker {
  margin: 0;
  color: var(--app-accent-text);
  font-size: 0.64rem;
  font-weight: 900;
  letter-spacing: 0.18em;
  line-height: 1rem;
  text-transform: uppercase;
}

.live-flow-empty-node h2 {
  color: var(--app-heading);
  font-size: 1.12rem;
  font-weight: 850;
  letter-spacing: -0.02em;
}

.live-flow-empty-node p:not(.live-flow-empty-kicker) {
  max-width: 22rem;
  color: var(--app-copy);
  font-size: 0.84rem;
  line-height: 1.45rem;
}

.live-flow-board {
  overflow: visible;
}

.live-flow-queue-panel {
  z-index: 30;
}

.live-flow-wires {
  z-index: 11;
  pointer-events: none;
}

.live-flow-wire {
  stroke: color-mix(in srgb, var(--flow-aqua) 32%, var(--app-border-strong));
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 3.25;
  opacity: 0.5;
}

.live-flow-wire-active {
  stroke-width: 4.2;
  opacity: 1;
  filter: none;
}

.live-flow-wire-working {
  color: var(--flow-teal);
  stroke: var(--flow-teal);
}

.live-flow-wire-fallback,
.live-flow-wire-cooldown,
.live-flow-wire-rate_limited {
  color: var(--flow-amber);
  stroke: var(--flow-amber);
}

.live-flow-wire-unhealthy {
  color: var(--flow-rose);
  stroke: var(--flow-rose);
}

.live-flow-wire-pulse {
  stroke-width: 6.5;
  stroke-dasharray: 2 22;
  opacity: 0.92;
}

.live-flow-stage-port {
  position: absolute;
  z-index: 36;
  width: 0.75rem;
  height: 0.75rem;
  border: 1px solid color-mix(in srgb, var(--port-color, var(--flow-aqua)) 64%, var(--app-border));
  border-radius: 999px;
  background: var(--app-surface);
  box-shadow: none;
  pointer-events: none;
}

.live-flow-stage-port-working {
  --port-color: var(--flow-teal);
}

.live-flow-stage-port-fallback,
.live-flow-stage-port-cooldown,
.live-flow-stage-port-rate_limited {
  --port-color: var(--flow-amber);
}

.live-flow-stage-port-unhealthy {
  --port-color: var(--flow-rose);
}

.live-flow-queue-out-port {
  right: -0.42rem;
  top: 8.35rem;
  transform: translateY(-50%);
}

.live-flow-completed-in-port {
  left: -0.42rem;
  top: 8.35rem;
  transform: translateY(-50%);
}

.live-flow-active-bay-cooldown .live-flow-stage-port,
.live-flow-active-bay-rate_limited .live-flow-stage-port,
.live-flow-active-bay-fallback .live-flow-stage-port {
  --port-color: var(--flow-amber);
}

.live-flow-active-bay-working .live-flow-stage-port {
  --port-color: var(--flow-teal);
}

.live-flow-queue-stack {
  position: relative;
  height: var(--queue-stack-height, 478px);
  margin-top: 1rem;
}

.live-flow-queue-slot-background,
.live-flow-queue-motion-layer {
  position: absolute;
  inset: 0;
}

.live-flow-queue-slot-background {
  display: grid;
  grid-template-rows: repeat(6, var(--queue-card-height, 68px));
  gap: var(--queue-card-gap, 12px);
  pointer-events: none;
}

.live-flow-queue-motion-layer {
  z-index: 1;
}

.live-flow-queue-slot-shell,
.live-flow-queue-slot {
  display: grid;
  width: 100%;
  height: var(--queue-card-height, 68px);
  align-items: center;
  border: 1px dashed color-mix(in srgb, var(--app-border-strong) 34%, transparent);
  border-radius: 0.72rem;
  background: var(--app-surface-muted);
}

.live-flow-queue-motion-card {
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  z-index: 2;
  opacity: 1;
  transition:
    transform var(--queue-move-duration, 300ms) cubic-bezier(0.22, 1, 0.36, 1),
    opacity 180ms ease,
    border-color 180ms ease,
    box-shadow 180ms ease;
  will-change: transform;
}

.live-flow-queue-motion-card-entering {
  opacity: 0;
}

.live-flow-queue-motion-card-leaving {
  z-index: 4;
  opacity: 0;
  pointer-events: none;
}

.live-flow-queue-card {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 0.6rem;
  height: 100%;
  border: 1px solid color-mix(in srgb, var(--queue-accent, var(--flow-aqua)) 36%, var(--app-border));
  border-radius: inherit;
  background: var(--app-surface);
  padding: 0.42rem 0.62rem;
  box-shadow:
    inset 0 1px 0 color-mix(in srgb, var(--app-heading) 8%, transparent),
    0 10px 24px color-mix(in srgb, var(--app-bg) 22%, transparent);
}

.live-flow-queue-slot-fallback {
  --queue-accent: var(--flow-amber);
}

.live-flow-queue-card-dot {
  width: 0.72rem;
  height: 0.72rem;
  border-radius: 999px;
  background: var(--queue-accent, var(--flow-aqua));
  box-shadow: 0 0 0 0.24rem color-mix(in srgb, var(--queue-accent, var(--flow-aqua)) 12%, transparent);
}

.live-flow-queue-card strong,
.live-flow-queue-card em,
.live-flow-queue-card small {
  display: block;
  overflow-wrap: anywhere;
}

.live-flow-queue-card strong {
  color: var(--app-heading);
  font-size: 0.76rem;
  font-weight: 850;
  line-height: 1rem;
}

.live-flow-queue-card em {
  margin-top: 0.05rem;
  color: var(--app-muted);
  font-style: normal;
  font-size: 0.62rem;
  line-height: 0.85rem;
}

.live-flow-queue-card small {
  margin-top: 0.05rem;
  color: var(--app-accent-text);
  font-size: 0.62rem;
  font-weight: 750;
  line-height: 0.85rem;
}

.live-flow-queue-rungs,
.live-flow-token-layer,
.live-flow-model-bay {
  display: none;
}

.live-flow-active-bay {
  position: absolute;
  z-index: 32;
  left: 61.5%;
  top: 56.5%;
  width: 15.5rem;
  min-height: 3.2rem;
  border: 1px dashed color-mix(in srgb, var(--bay-accent, var(--flow-aqua)) 42%, var(--app-border));
  border-radius: 999px;
  background: var(--app-surface);
  box-shadow:
    inset 0 1px 0 color-mix(in srgb, var(--app-heading) 8%, transparent),
    0 18px 46px color-mix(in srgb, var(--app-bg) 24%, transparent);
  transform: translate(-50%, -50%);
  backdrop-filter: none;
}

.live-flow-active-bay-working {
  --bay-accent: var(--flow-teal);
}

.live-flow-active-bay-fallback,
.live-flow-active-bay-cooldown,
.live-flow-active-bay-rate_limited {
  --bay-accent: var(--flow-amber);
}

.live-flow-active-bay-unhealthy {
  --bay-accent: var(--flow-rose);
}

.live-flow-active-bay-occupied {
  border-radius: 1rem;
  border-style: solid;
  box-shadow: none;
}

.live-flow-active-bay-in-port {
  left: -0.42rem;
  top: 50%;
  transform: translateY(-50%);
}

.live-flow-active-bay-out-port {
  right: -0.42rem;
  top: 50%;
  transform: translateY(-50%);
}

.live-flow-active-bay-empty {
  display: flex;
  min-height: 3.2rem;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.7rem 1rem;
  color: var(--app-muted);
}

.live-flow-active-bay-empty span {
  font-size: 0.66rem;
  font-weight: 900;
  letter-spacing: 0.14em;
  text-transform: uppercase;
}

.live-flow-active-bay-empty strong {
  color: color-mix(in srgb, var(--bay-accent, var(--app-accent)) 74%, var(--app-heading));
  font-size: 0.72rem;
  font-weight: 850;
  text-transform: uppercase;
}

.live-flow-active-bay-card {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  min-height: 3.2rem;
  align-items: center;
  gap: 0.62rem;
  padding: 0.58rem 0.82rem;
}

.live-flow-active-bay-dot {
  width: 0.76rem;
  height: 0.76rem;
  border-radius: 999px;
  background: var(--bay-accent, var(--flow-aqua));
  box-shadow: 0 0 0 0.26rem color-mix(in srgb, var(--bay-accent, var(--flow-aqua)) 13%, transparent);
  animation: liveFlowBayPulse 1.4s ease-out infinite;
}

.live-flow-active-bay-card strong,
.live-flow-active-bay-card em,
.live-flow-active-bay-card small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-flow-active-bay-card strong {
  color: var(--app-heading);
  font-size: 0.78rem;
  font-weight: 850;
}

.live-flow-active-bay-card em {
  color: var(--app-muted);
  font-style: normal;
  font-size: 0.62rem;
  overflow-wrap: anywhere;
  white-space: normal;
}

.live-flow-active-bay-card small {
  color: color-mix(in srgb, var(--bay-accent, var(--app-accent)) 78%, var(--app-heading));
  font-size: 0.62rem;
  font-weight: 750;
}

.live-flow-active-bay-card b {
  border: 1px solid color-mix(in srgb, var(--bay-accent, var(--flow-aqua)) 32%, var(--app-border));
  border-radius: 999px;
  background: color-mix(in srgb, var(--bay-accent, var(--flow-aqua)) 11%, var(--app-surface));
  padding: 0.22rem 0.48rem;
  color: color-mix(in srgb, var(--bay-accent, var(--flow-aqua)) 78%, var(--app-heading));
  font-size: 0.6rem;
  font-weight: 850;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.live-flow-bay-enter-active,
.live-flow-bay-leave-active {
  transition:
    opacity 0.2s ease,
    transform 0.2s cubic-bezier(0.2, 0.82, 0.24, 1);
  will-change: opacity, transform;
}

.live-flow-bay-enter-from {
  opacity: 0;
  transform: translateX(-0.45rem);
}

.live-flow-bay-leave-to {
  opacity: 0;
  transform: translateX(0.45rem);
}

.live-flow-stage-columns {
  position: relative;
  z-index: 22;
  display: grid;
  grid-template-columns:
    minmax(180px, 260px)
    minmax(250px, 1fr)
    minmax(205px, 300px)
    minmax(225px, 360px);
  height: 100%;
  min-height: 0;
  align-items: stretch;
  column-gap: clamp(12px, 1.6vw, 34px);
  overflow: visible;
  padding: 3rem clamp(0.75rem, 1.6vw, 1.25rem) 1.35rem;
  box-sizing: border-box;
  pointer-events: none;
}

.live-flow-page {
  width: 100%;
  min-width: 0;
  max-width: 1680px;
  margin: 0 auto -2rem;
}

.live-flow-header {
  min-width: 0;
  padding-right: 0;
  padding-left: 0;
}

.live-flow-board-shell {
  width: 100%;
  min-width: 0;
  overflow: visible;
  padding: 0 0 1.1rem;
}

.live-flow-overview {
  min-height: 36rem;
  border: 1px solid color-mix(in srgb, var(--app-border-strong) 54%, var(--app-border));
  border-radius: var(--flow-radius);
  background: var(--app-surface);
  padding: clamp(1rem, 2vw, 1.6rem);
  box-shadow: none;
}

.live-flow-overview-header,
.live-flow-overview-row-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}

.live-flow-overview-header {
  padding: 0.25rem 0.2rem 1.25rem;
}

.live-flow-overview-header h2 {
  margin-top: 0.25rem;
  color: var(--app-heading);
  font-size: 1.25rem;
  font-weight: 850;
  letter-spacing: -0.025em;
}

.live-flow-overview-header p:not(.live-flow-dock-kicker) {
  margin-top: 0.35rem;
  color: var(--app-muted);
  font-size: 0.8rem;
  line-height: 1.35rem;
}

.live-flow-overview-sections {
  display: grid;
  gap: 1.4rem;
}

.live-flow-overview-section {
  min-width: 0;
}

.live-flow-overview-section h3 {
  margin: 0 0 0.55rem 0.2rem;
  color: var(--app-muted);
  font-size: 0.68rem;
  font-weight: 900;
  letter-spacing: 0.18em;
  text-transform: uppercase;
}

.live-flow-overview-rows {
  display: grid;
  gap: 0.8rem;
}

.live-flow-overview-row {
  --overview-circuit-color: color-mix(in srgb, var(--flow-aqua) 36%, var(--app-border-strong));
  --overview-circuit-opacity: 0.58;
  position: relative;
  isolation: isolate;
  display: grid;
  grid-template-columns: minmax(13rem, 0.82fr) minmax(28rem, 1.8fr);
  align-items: center;
  gap: clamp(0.8rem, 1.6vw, 1.4rem);
  width: 100%;
  border: 1px solid color-mix(in srgb, var(--app-border-strong) 38%, var(--app-border));
  border-radius: 1rem;
  background: var(--app-surface);
  padding: 0.9rem;
  color: inherit;
  text-align: left;
  box-shadow: inset 0 1px 0 color-mix(in srgb, var(--app-heading) 5%, transparent);
  transition: border-color 0.16s ease, background-color 0.16s ease, transform 0.16s ease;
}

.live-flow-overview-row > * {
  position: relative;
  z-index: 1;
}

.live-flow-overview-row:hover {
  border-color: color-mix(in srgb, var(--app-accent) 38%, var(--app-border));
  background: color-mix(in srgb, var(--app-accent) 5%, var(--app-surface));
  transform: translateY(-1px);
}

.live-flow-overview-row-queued {
  --overview-activity-color: var(--flow-aqua);
  --overview-activity-fill: 5%;
  --overview-circuit-color: var(--flow-aqua);
  --overview-circuit-opacity: 1;
  border-color: color-mix(in srgb, var(--flow-aqua) 46%, var(--app-border));
  background: color-mix(in srgb, var(--flow-aqua) 7%, var(--app-surface));
  box-shadow:
    inset 0 1px 0 color-mix(in srgb, var(--app-heading) 5%, transparent),
    inset 3px 0 0 color-mix(in srgb, var(--flow-aqua) 78%, transparent);
}

.live-flow-overview-row-queued:hover {
  border-color: color-mix(in srgb, var(--flow-aqua) 62%, var(--app-border));
  background: color-mix(in srgb, var(--flow-aqua) 10%, var(--app-surface));
}

.live-flow-overview-row-active {
  --overview-activity-color: var(--flow-teal);
  --overview-activity-fill: 7%;
  --overview-circuit-color: var(--flow-teal);
  --overview-circuit-opacity: 1;
  border-color: color-mix(in srgb, var(--flow-teal) 58%, var(--app-border));
  background: color-mix(in srgb, var(--flow-teal) 11%, var(--app-surface));
  box-shadow:
    inset 0 1px 0 color-mix(in srgb, var(--app-heading) 5%, transparent),
    inset 3px 0 0 var(--flow-teal);
}

.live-flow-overview-row-active:hover {
  border-color: color-mix(in srgb, var(--flow-teal) 72%, var(--app-border));
  background: color-mix(in srgb, var(--flow-teal) 14%, var(--app-surface));
}

.live-flow-overview-row-queued::before,
.live-flow-overview-row-active::before {
  content: "";
  position: absolute;
  inset: 0;
  z-index: 0;
  border-radius: inherit;
  background: color-mix(in srgb, var(--overview-activity-color) var(--overview-activity-fill), var(--app-surface));
  opacity: 0.28;
  pointer-events: none;
  animation: live-flow-overview-activity-tint 6s ease-in-out infinite alternate;
}

@keyframes live-flow-overview-activity-tint {
  from {
    opacity: 0.25;
  }

  to {
    opacity: 0.68;
  }
}

@media (prefers-reduced-motion: reduce) {
  .live-flow-overview-row-queued::before,
  .live-flow-overview-row-active::before {
    animation: none;
    opacity: 0.45;
  }
}

.live-flow-overview-row-heading {
  min-width: 0;
  align-items: center;
}

.live-flow-overview-row-heading strong,
.live-flow-overview-row-heading small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-flow-overview-row-heading strong {
  color: var(--app-heading);
  font-size: 0.88rem;
  font-weight: 850;
}

.live-flow-overview-row-heading small {
  margin-top: 0.22rem;
  color: var(--app-muted);
  font-size: 0.68rem;
}

.live-flow-overview-row-heading .live-flow-overview-last-used {
  color: var(--app-subtle);
  font-size: 0.64rem;
}

.live-flow-overview-model-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 0.28rem;
  margin-top: 0.35rem;
}

.live-flow-overview-model-badges :deep(.ui-badge) {
  min-height: 1.2rem;
  padding: 0.12rem 0.42rem;
  font-size: 0.6rem;
  line-height: 0.8rem;
}

.live-flow-overview-model-badges svg {
  width: 0.72rem;
  height: 0.72rem;
}

.live-flow-overview-row-meta {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 0.45rem;
}

.live-flow-overview-row-meta > svg {
  width: 1rem;
  height: 1rem;
  color: var(--app-subtle);
}

.live-flow-overview-metrics {
  display: grid;
  min-width: 0;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 2.25rem;
}

.live-flow-overview-metrics > span {
  position: relative;
  z-index: 1;
  min-width: 0;
  border: 1px solid color-mix(in srgb, var(--flow-teal) 28%, var(--app-border));
  border-radius: 0.75rem;
  background: var(--app-surface-muted);
  padding: 0.65rem 0.72rem;
  box-shadow: none;
}

.live-flow-overview-metrics > span:not(:last-child)::after {
  content: "";
  position: absolute;
  top: 0;
  left: 100%;
  z-index: 3;
  width: calc(2.25rem + 2px);
  height: 100%;
  background: var(--overview-circuit-color);
  opacity: var(--overview-circuit-opacity);
  -webkit-mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 38 70' preserveAspectRatio='none'%3E%3Cpath d='M3.5 14 H11 C13.2 14 15 15.8 15 18 V52 C15 54.2 16.8 56 19 56 H34.5' fill='none' stroke='black' stroke-width='2.5' stroke-linecap='round' stroke-linejoin='round'/%3E%3Ccircle cx='3.5' cy='14' r='3.2' fill='black'/%3E%3Ccircle cx='34.5' cy='56' r='3.2' fill='black'/%3E%3C/svg%3E") center / 100% 100% no-repeat;
  mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 38 70' preserveAspectRatio='none'%3E%3Cpath d='M3.5 14 H11 C13.2 14 15 15.8 15 18 V52 C15 54.2 16.8 56 19 56 H34.5' fill='none' stroke='black' stroke-width='2.5' stroke-linecap='round' stroke-linejoin='round'/%3E%3Ccircle cx='3.5' cy='14' r='3.2' fill='black'/%3E%3Ccircle cx='34.5' cy='56' r='3.2' fill='black'/%3E%3C/svg%3E") center / 100% 100% no-repeat;
  pointer-events: none;
}

.live-flow-overview-metrics small,
.live-flow-overview-metrics strong {
  display: block;
}

.live-flow-overview-metrics small {
  min-height: 1.8rem;
  color: var(--app-muted);
  font-size: 0.66rem;
  font-weight: 700;
  line-height: 0.9rem;
}

.live-flow-overview-metrics strong {
  margin-top: 0.15rem;
  color: var(--app-heading);
  font-size: 1.12rem;
  font-weight: 850;
}

.live-flow-overview-empty {
  display: flex;
  min-height: 24rem;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  color: var(--app-muted);
  text-align: center;
}

.live-flow-overview-empty strong {
  color: var(--app-heading);
}

.live-flow-overview-empty p {
  margin-top: 0.35rem;
  font-size: 0.8rem;
}

@media (max-width: 980px) {
  .live-flow-overview-row {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 640px) {
  .live-flow-overview-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .live-flow-overview-row-heading {
    align-items: flex-start;
    flex-direction: column;
  }

  .live-flow-overview-metrics > span::before,
  .live-flow-overview-metrics > span::after {
    display: none;
  }
}

.live-flow-board {
  position: relative;
  width: 100%;
  min-width: 0;
  max-width: 100%;
  height: clamp(620px, calc(100vh - 340px), 820px);
  min-height: 620px;
  overflow: hidden;
}

.live-flow-control-dock {
  width: 100%;
  min-width: 0;
  max-width: 100%;
  margin-top: 1rem;
}

.live-flow-stage-column {
  position: relative;
  display: flex;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  gap: 1rem;
  overflow: visible;
  pointer-events: none;
}

.live-flow-stage-column > * {
  pointer-events: auto;
}

.live-flow-zone-label {
  position: relative;
  top: auto;
  right: auto;
  left: auto;
  z-index: 12;
  align-self: stretch;
  width: 100%;
  margin-bottom: 0.1rem;
}

.live-flow-queue-panel,
.live-flow-completed-rail {
  position: relative;
  top: auto;
  right: auto;
  bottom: auto;
  left: auto;
  width: 100%;
  transform: none;
}

.live-flow-queue-panel {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
  overflow: hidden;
}

.live-flow-completed-rail {
  min-height: 0;
  flex: 1 1 auto;
  overflow: hidden;
}

.live-flow-queue-out-port {
  right: -0.42rem;
  top: 4.35rem;
  transform: translateY(-50%);
}

.live-flow-completed-in-port {
  left: -0.42rem;
  top: 6.15rem;
  transform: translateY(-50%);
}

.live-flow-model-column {
  min-width: 0;
}

.live-flow-model-stack {
  position: relative;
  display: grid;
  min-width: 0;
  min-height: 0;
  flex: 1 1 auto;
  align-content: start;
  gap: 1rem;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 0.1rem 0.7rem 0.5rem;
  scrollbar-width: thin;
}

.live-flow-model-stack :deep(.flow-model-node) {
  position: relative;
  top: auto !important;
  left: auto !important;
  width: min(100%, 21rem);
  min-width: 0;
  max-width: 100%;
  justify-self: center;
  transform: none;
}

.live-flow-model-stack :deep(.flow-model-node-setup) {
  width: min(100%, 27.5rem);
}

.live-flow-active-column {
  align-items: center;
  overflow: visible;
}

.live-flow-active-column-concurrent {
  align-items: stretch;
  overflow: visible;
}

.live-flow-active-bay {
  position: relative;
  top: auto;
  left: auto;
  display: grid;
  --port-color: var(--bay-accent, var(--flow-aqua));
  width: clamp(190px, 100%, 320px);
  height: auto;
  min-height: 4.75rem;
  max-height: none;
  margin: 0;
  overflow: visible;
  box-sizing: border-box;
  transform: none;
  align-self: center;
}

.live-flow-active-bay-concurrent {
  display: flex;
  width: 100%;
  height: 0;
  min-height: 0;
  max-height: 100%;
  flex: 1 1 0;
  align-self: stretch;
  flex-direction: column;
  overflow: visible;
  border-color: color-mix(in srgb, var(--app-border-strong) 38%, var(--app-border));
  border-radius: 1rem;
  border-style: solid;
  background: var(--app-surface);
  box-shadow: none;
  backdrop-filter: none;
}

.live-flow-active-panel-header {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: flex-end;
  gap: 0.75rem;
  padding: 1rem 1rem 0.75rem;
}

.live-flow-active-stack {
  display: grid;
  height: 0;
  min-height: 0;
  flex: 1 1 0;
  align-content: start;
  grid-auto-rows: minmax(4.4rem, max-content);
  gap: 0.65rem;
  overflow-x: hidden;
  overflow-y: auto;
  overscroll-behavior-y: contain;
  padding: 0 0.75rem 0.85rem;
  scrollbar-color: color-mix(in srgb, var(--app-border-strong) 68%, transparent) transparent;
  scrollbar-width: thin;
}

.live-flow-active-stack::-webkit-scrollbar {
  width: 0.35rem;
}

.live-flow-active-stack::-webkit-scrollbar-thumb {
  border-radius: 999px;
  background: var(--app-border-strong);
}

.live-flow-active-stack::-webkit-scrollbar-track {
  background: transparent;
}

.live-flow-active-bay-concurrent .live-flow-active-bay-card {
  grid-area: auto;
  width: 100%;
  min-height: 4.4rem;
  align-self: start;
  border: 1px solid color-mix(in srgb, var(--bay-accent, var(--flow-aqua)) 24%, var(--app-border));
  border-radius: 0.8rem;
  background: var(--app-surface);
  box-shadow: inset 0 1px 0 color-mix(in srgb, var(--app-heading) 5%, transparent);
}

.live-flow-active-list-empty {
  flex: 1 1 auto;
  flex-direction: column;
  justify-content: center;
  text-align: center;
}

.live-flow-active-bay-card,
.live-flow-active-bay-empty {
  grid-area: 1 / 1;
  min-width: 0;
  min-height: 0;
  width: 100%;
  height: auto;
  overflow: visible;
  border-radius: inherit;
}

.live-flow-active-bay-card {
  grid-template-columns: auto minmax(0, 1fr) auto;
}

.live-flow-active-bay-card b {
  max-width: 5.9rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-flow-empty-node {
  position: relative;
  top: auto;
  left: auto;
  width: min(100%, 30rem);
  margin: 0 auto auto;
  overflow: visible;
  transform: none;
}

.live-flow-empty-node-port {
  position: absolute;
  top: 50%;
  z-index: 4;
  width: 0.72rem;
  height: 0.72rem;
  border: 1px solid color-mix(in srgb, var(--flow-aqua) 56%, var(--app-border));
  border-radius: 999px;
  background: var(--app-surface);
  box-shadow: none;
  transform: translateY(-50%);
  pointer-events: none;
}

.live-flow-empty-node-port-in {
  left: -0.36rem;
}

.live-flow-empty-node-port-out {
  right: -0.36rem;
}

.live-flow-wires {
  position: absolute;
  inset: 0;
  z-index: 12;
  width: 100%;
  height: 100%;
  overflow: visible;
  pointer-events: none;
}

.live-flow-wire {
  fill: none;
  stroke: color-mix(in srgb, var(--flow-aqua) 32%, var(--app-border-strong));
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 2.35;
  opacity: 0.64;
}

.live-flow-wire-skeleton {
  stroke-width: 2.15;
  opacity: 0.72;
}

.live-flow-wire-overlay {
  stroke-width: 3.65;
  opacity: 1;
}

.live-flow-wire-static {
  stroke-width: 2.55;
}

.live-flow-wire-emphasized {
  stroke-width: 3.85;
  opacity: 1;
  filter: none;
}

.live-flow-wire-state-idle {
  color: color-mix(in srgb, var(--flow-aqua) 36%, var(--app-border-strong));
  stroke: currentColor;
  opacity: 0.58;
}

.live-flow-wire-skeleton.live-flow-wire-state-idle {
  opacity: 0.7;
}

.live-flow-wire-state-active,
.live-flow-wire-state-fallback-active,
.live-flow-wire-state-completed {
  color: var(--flow-teal);
  stroke: currentColor;
}

.live-flow-wire-state-holding,
.live-flow-wire-state-blocked,
.live-flow-wire-state-cancelled {
  color: var(--flow-amber);
  stroke: currentColor;
}

.live-flow-wire-state-cooldown {
  color: var(--flow-amber);
  stroke: currentColor;
}

.live-flow-wire-overlay.live-flow-wire-state-cooldown {
  stroke-width: 2.45;
  opacity: 0.74;
}

.live-flow-wire-state-error {
  color: var(--flow-rose);
  stroke: currentColor;
}

.live-flow-wire-packet {
  fill: none;
  stroke: currentColor;
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 6.4;
  stroke-dasharray: 0.1 22;
  stroke-dashoffset: 0;
  opacity: 0.95;
  vector-effect: non-scaling-stroke;
  filter: none;
  animation: liveFlowCurrent 1.35s linear infinite;
}

.live-flow-wire-packet-state-active,
.live-flow-wire-packet-state-fallback-active,
.live-flow-wire-packet-state-completed {
  color: var(--flow-teal);
}

.live-flow-wire-packet-state-holding,
.live-flow-wire-packet-state-blocked,
.live-flow-wire-packet-state-cancelled {
  color: var(--flow-amber);
}

.live-flow-wire-packet-state-error {
  color: var(--flow-rose);
}

.live-flow-stage-port-state-idle {
  --port-color: var(--flow-aqua);
}

.live-flow-stage-port-state-active,
.live-flow-stage-port-state-fallback-active,
.live-flow-stage-port-state-completed {
  --port-color: var(--flow-teal);
}

.live-flow-stage-port-state-holding,
.live-flow-stage-port-state-blocked,
.live-flow-stage-port-state-cancelled {
  --port-color: var(--flow-amber);
}

.live-flow-stage-port-state-error {
  --port-color: var(--flow-rose);
}

.live-flow-stage-port-hidden {
  opacity: 0;
}

.live-flow-active-bay-state-idle {
  --bay-accent: var(--flow-aqua);
}

.live-flow-active-bay-state-active,
.live-flow-active-bay-state-fallback-active,
.live-flow-active-bay-state-completed {
  --bay-accent: var(--flow-teal);
}

.live-flow-active-bay-state-holding,
.live-flow-active-bay-state-blocked,
.live-flow-active-bay-state-cancelled {
  --bay-accent: var(--flow-amber);
}

.live-flow-active-bay-state-error {
  --bay-accent: var(--flow-rose);
}

.live-flow-port-debug-layer {
  position: absolute;
  inset: 0;
  z-index: 80;
  pointer-events: none;
}

.live-flow-debug-stage-bounds {
  position: absolute;
  inset: 0;
  border: 1px dashed color-mix(in srgb, var(--flow-amber) 72%, transparent);
  border-radius: inherit;
}

.live-flow-debug-port,
.live-flow-debug-path {
  position: absolute;
  max-width: 18rem;
  overflow: hidden;
  border: 1px solid var(--flow-amber);
  border-radius: 0.35rem;
  background: var(--app-bg);
  padding: 0.1rem 0.28rem;
  color: var(--flow-amber);
  font-size: 0.58rem;
  font-weight: 850;
  text-overflow: ellipsis;
  transform: translate(0.35rem, -50%);
  white-space: nowrap;
}

.live-flow-debug-path {
  border-color: color-mix(in srgb, var(--flow-teal) 72%, transparent);
  color: var(--flow-teal);
  transform: translate(-50%, -50%);
}

.live-flow-debug-missing {
  position: absolute;
  left: 0.75rem;
  bottom: 0.75rem;
  max-width: min(48rem, calc(100% - 1.5rem));
  border: 1px solid color-mix(in srgb, var(--flow-rose) 72%, transparent);
  border-radius: 0.55rem;
  background: var(--app-bg);
  padding: 0.45rem 0.6rem;
  color: var(--flow-rose);
  font-size: 0.68rem;
  font-weight: 850;
}

.live-flow-event-debug-panel {
  position: absolute;
  right: 0.85rem;
  bottom: 0.85rem;
  z-index: 82;
  display: grid;
  width: min(32rem, calc(100% - 1.7rem));
  gap: 0.22rem;
  border: 1px solid color-mix(in srgb, var(--flow-teal) 44%, var(--app-border));
  border-radius: 0.75rem;
  background: var(--app-bg);
  padding: 0.65rem 0.75rem;
  color: var(--app-muted);
  font-size: 0.68rem;
  line-height: 1.1rem;
  pointer-events: none;
}

.live-flow-event-debug-panel strong {
  color: var(--flow-teal);
  font-size: 0.72rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.live-flow-event-debug-panel span {
  display: inline-flex;
  margin-left: 0.32rem;
  border: 1px solid color-mix(in srgb, var(--flow-teal) 24%, var(--app-border));
  border-radius: 999px;
  padding: 0 0.32rem;
  color: var(--app-heading);
}

.live-flow-queue-column,
.live-flow-queue-panel,
.live-flow-queue-stack,
.live-flow-model-column,
.live-flow-model-stack,
.live-flow-active-column,
.live-flow-active-bay,
.live-flow-completed-column,
.live-flow-completed-rail,
.live-flow-feed {
  -ms-overflow-style: none;
  scrollbar-width: none;
}

.live-flow-queue-column::-webkit-scrollbar,
.live-flow-queue-panel::-webkit-scrollbar,
.live-flow-queue-stack::-webkit-scrollbar,
.live-flow-model-column::-webkit-scrollbar,
.live-flow-model-stack::-webkit-scrollbar,
.live-flow-active-column::-webkit-scrollbar,
.live-flow-active-bay::-webkit-scrollbar,
.live-flow-completed-column::-webkit-scrollbar,
.live-flow-completed-rail::-webkit-scrollbar,
.live-flow-feed::-webkit-scrollbar {
  display: none;
  width: 0;
  height: 0;
}

@keyframes liveFlowBayPulse {
  to {
    opacity: 0.72;
    transform: scale(1.18);
  }
}

@keyframes liveFlowCurrent {
  to {
    stroke-dashoffset: -36;
  }
}

@keyframes liveFlowEmptyCurrent {
  0%,
  100% {
    opacity: 0.48;
    transform: translateY(-50%) scale(0.9);
  }

  50% {
    opacity: 1;
    transform: translateY(-50%) scale(1.08);
  }
}

@media (min-width: 1680px) {
  .live-flow-page {
    width: 100%;
    max-width: 1680px;
    margin-left: auto;
  }
}

.live-flow-wire-active,
.live-flow-wire-pulse,
.live-flow-wire-emphasized,
.live-flow-wire-packet {
  filter: none;
}

.live-flow-junction-active,
.live-flow-stage-port,
.live-flow-queue-card,
.live-flow-queue-card-dot,
.live-flow-active-bay,
.live-flow-active-bay-occupied,
.live-flow-active-bay-dot,
.live-flow-empty-node-port,
.live-flow-empty-orbit span {
  box-shadow: none;
}

@media (prefers-reduced-motion: reduce) {
  .live-flow-wire-pulse,
  .live-flow-wire-packet,
  .live-flow-active-bay-dot,
  .live-flow-queue-card,
  .live-flow-empty-orbit span {
    animation: none;
  }

  .live-flow-feed-enter-active,
  .live-flow-feed-leave-active,
  .live-flow-feed-move,
  .live-flow-queue-motion-card,
  .live-flow-bay-enter-active,
  .live-flow-bay-leave-active,
  .live-flow-token-enter-active,
  .live-flow-token-leave-active,
  .live-flow-token-move {
    transition: none;
  }
}

@media (max-width: 1180px) {
  .live-flow-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .live-flow-toolbar {
    justify-content: flex-start;
  }
}

@media (max-width: 760px) {
  .live-flow-page {
    margin-right: auto;
    margin-left: auto;
  }

  .live-flow-header,
  .live-flow-board-shell {
    padding-right: 1rem;
    padding-left: 1rem;
  }

  .live-flow-control-row {
    align-items: stretch;
    flex-direction: column;
  }

  .live-flow-toolbar,
  .live-flow-toolbar-primary,
  .live-flow-toolbar-extra,
  .live-flow-routing-group-card,
  .live-flow-mode-switch,
  .live-flow-group-select {
    width: 100%;
    min-width: 0;
  }

  .live-flow-toolbar {
    display: flex;
    align-items: stretch;
    flex-direction: column;
  }

  .live-flow-toolbar-primary {
    display: flex;
    flex: 0 0 auto;
    flex-direction: column;
    gap: 0.65rem;
  }

  .live-flow-group-select {
    flex-basis: auto;
  }
}
</style>
