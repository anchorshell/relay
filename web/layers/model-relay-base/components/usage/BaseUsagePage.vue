<script setup lang="ts">
import type { CharacterizationIntentAnalyticsResponse, RequestLog, UsageAnalyticsBucket, UsageAnalyticsResponse, UsageAnalyticsRow, UsageAnalyticsTotals } from '../../../../types/admin'

const MAX_USAGE_ANALYTICS_BUCKETS = 2000
const BUCKET_ORDER: UsageAnalyticsBucket[] = ['minute', 'hour', 'day', 'week', 'month']
type UsageAnalyticsQueryParamValue = string | number | boolean | Array<string | number | boolean> | null | undefined

const props = withDefaults(defineProps<{
  usageAnalyticsParams?: Record<string, UsageAnalyticsQueryParamValue>
}>(), {
  usageAnalyticsParams: () => ({})
})

const api = useRelayApi()
const catalog = useCatalogStore()
const queue = useQueueStore()
const requests = useRequestsStore()
const telemetry = useTelemetryStore()
const pageData = useAdminPageDataStore()
const { number, currencyMicros } = useFormatters()

const metric = ref<'requests' | 'tokens' | 'spend'>('requests')
const bucket = ref<UsageAnalyticsBucket>('hour')
const startDate = ref(localDateInputValue())
const endDate = ref(localDateInputValue())
const analytics = ref<UsageAnalyticsResponse | null>(null)
const zoomAnalytics = ref<UsageAnalyticsResponse | null>(null)
const intentAnalytics = ref<CharacterizationIntentAnalyticsResponse | null>(null)
const zoomIntentAnalytics = ref<CharacterizationIntentAnalyticsResponse | null>(null)
const zoomRange = ref<{ startIndex: number, endIndex: number } | null>(null)
const usageChart = ref<{ resetZoom: () => void } | null>(null)
const tableExpanded = ref(false)
const intentTableExpanded = ref(false)
const loading = ref(false)
const loadError = ref('')
const zoomLoadError = ref('')
const intentLoading = ref(false)
const intentLoadError = ref('')
const analyticsFetchedAtMs = ref(0)
const nowTick = ref(Date.now())

let refreshSeq = 0
let zoomRefreshSeq = 0
let intentRefreshSeq = 0
let zoomIntentRefreshSeq = 0
let tickTimer: ReturnType<typeof window.setInterval> | null = null

function localDateInputValue(now = new Date()) {
  const year = now.getFullYear()
  const month = String(now.getMonth() + 1).padStart(2, '0')
  const day = String(now.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function timezoneOffsetMinutes() {
  return new Date().getTimezoneOffset()
}

function normalizedDateRange(start: string, end: string) {
  const fallback = localDateInputValue()
  let nextStart = start || end || fallback
  let nextEnd = end || start || fallback
  if (nextStart > nextEnd) {
    [nextStart, nextEnd] = [nextEnd, nextStart]
  }
  return {
    start: nextStart,
    end: nextEnd
  }
}

function pseudoLocalDate(dateValue: string) {
  return new Date(`${dateValue}T00:00:00.000Z`)
}

function addBucket(local: Date, bucketValue: UsageAnalyticsBucket) {
  const next = new Date(local.getTime())
  switch (bucketValue) {
    case 'minute':
      next.setUTCMinutes(next.getUTCMinutes() + 1)
      break
    case 'hour':
      next.setUTCHours(next.getUTCHours() + 1)
      break
    case 'day':
      next.setUTCDate(next.getUTCDate() + 1)
      break
    case 'week':
      next.setUTCDate(next.getUTCDate() + 7)
      break
    case 'month':
      next.setUTCMonth(next.getUTCMonth() + 1)
      break
  }
  return next
}

function bucketCountForRange(start: string, end: string, bucketValue: UsageAnalyticsBucket) {
  const startLocal = pseudoLocalDate(start)
  const endExclusive = pseudoLocalDate(end)
  endExclusive.setUTCDate(endExclusive.getUTCDate() + 1)

  let count = 0
  let cursor = startLocal
  while (cursor < endExclusive && count <= MAX_USAGE_ANALYTICS_BUCKETS) {
    cursor = addBucket(cursor, bucketValue)
    count += 1
  }
  return count
}

const selectedRange = computed(() => normalizedDateRange(startDate.value, endDate.value))
const activeStartDate = computed(() => analytics.value?.start_date || selectedRange.value.start)
const activeEndDate = computed(() => analytics.value?.end_date || selectedRange.value.end)
const rangeIncludesToday = computed(() => {
  const today = localDateInputValue()
  return activeStartDate.value <= today && activeEndDate.value >= today
})
const rangeLabel = computed(() =>
  activeStartDate.value === activeEndDate.value
    ? activeStartDate.value
    : `${activeStartDate.value} → ${activeEndDate.value}`
)

const bucketTabs = computed(() =>
  BUCKET_ORDER.map((value) => {
    const count = bucketCountForRange(selectedRange.value.start, selectedRange.value.end, value)
    const disabled = count > MAX_USAGE_ANALYTICS_BUCKETS
    return {
      value,
      label: value,
      disabled,
      title: disabled ? `Too many ${value} buckets for this date range.` : ''
    }
  })
)

watchEffect(() => {
  const current = bucketTabs.value.find(item => item.value === bucket.value)
  if (current && !current.disabled) {
    return
  }
  const fallback = bucketTabs.value.find(item => !item.disabled)
  if (fallback) {
    bucket.value = fallback.value
  }
})

async function refreshAnalytics() {
  const seq = ++refreshSeq
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.usageAnalytics(
      selectedRange.value.start,
      selectedRange.value.end,
      metric.value,
      bucket.value,
      timezoneOffsetMinutes(),
      props.usageAnalyticsParams
    )
    if (seq !== refreshSeq) return
    analytics.value = response
    startDate.value = response.start_date
    endDate.value = response.end_date
    analyticsFetchedAtMs.value = Date.now()
  } catch (error: any) {
    if (seq !== refreshSeq) return
    loadError.value = error?.data?.message || error?.message || 'Unable to load usage analytics.'
  } finally {
    if (seq === refreshSeq) {
      loading.value = false
    }
  }
}

function parseMs(value?: string | null) {
  if (!value) return 0
  const ms = new Date(value).getTime()
  return Number.isFinite(ms) ? ms : 0
}

function dateForOffset(ms: number, offsetMinutes: number) {
  return new Date(ms - offsetMinutes * 60_000).toISOString().slice(0, 10)
}

function isInActiveRange(ms: number, offsetMinutes: number) {
  if (ms <= 0) return false
  const dateValue = dateForOffset(ms, offsetMinutes)
  return dateValue >= activeStartDate.value && dateValue <= activeEndDate.value
}

function analyticsRowKey(providerID: string, endpointID: string, modelName: string) {
  return `${providerID}:${endpointID}:${modelName}`
}

function sortAnalyticsRows(rows: UsageAnalyticsRow[]) {
  return [...rows].sort((a, b) => {
    if (a.cost_micros !== b.cost_micros) return b.cost_micros - a.cost_micros
    if (a.requests !== b.requests) return b.requests - a.requests
    if (a.tokens_in !== b.tokens_in) return b.tokens_in - a.tokens_in
    if (a.provider_name !== b.provider_name) return a.provider_name.localeCompare(b.provider_name)
    return a.model_name.localeCompare(b.model_name)
  })
}

function ensureAnalyticsRow(rowsMap: Map<string, UsageAnalyticsRow>, providerID: string, endpointID: string, providerName: string, modelName: string) {
  const key = analyticsRowKey(providerID, endpointID, modelName)
  let row = rowsMap.get(key)
  if (!row) {
    row = {
      provider_id: providerID,
      endpoint_id: endpointID,
      provider_name: providerName,
      model_name: modelName,
      requests: 0,
      tokens_in: 0,
      tokens_down: 0,
      cost_micros: 0
    }
    rowsMap.set(key, row)
  }
  return row
}

type LiveAnalyticsEntry = {
  requestID: string
  providerID: string
  endpointID: string
  providerName: string
  modelName: string
  anchorMs: number
  updatedMs: number
  requestCount: number
  tokensIn: number
  tokensDown: number
  costMicros: number
}

function resolveAnalyticsProvider(providerID: string, endpointID: string) {
  const resolvedProviderID = providerID || catalog.endpointMap[endpointID]?.provider_id || ''
  return {
    providerID: resolvedProviderID,
    providerName: catalog.providerMap[resolvedProviderID]?.name || ''
  }
}

function resolveAnalyticsModel(endpointID: string, fallbackName?: string | null) {
  const endpoint = catalog.endpointMap[endpointID]
  if (endpoint?.upstream_model) return endpoint.upstream_model
  if (endpoint?.name) return endpoint.name
  if (fallbackName && fallbackName.trim()) return fallbackName.trim()
  return 'Unknown model'
}

function usageRouteSlug(value?: string | null) {
  return String(value || '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function usageRouteTarget(row: UsageAnalyticsRow) {
  const endpoint = catalog.endpointMap[row.endpoint_id]
  const provider = catalog.providerMap[endpoint?.provider_id || row.provider_id]
  const providerSlug = provider?.slug?.trim() || usageRouteSlug(row.provider_name) || 'provider-missing'
  const modelSlug = endpoint?.slug?.trim() || usageRouteSlug(row.model_name) || 'unknown-model'
  return `${providerSlug}/${modelSlug}`
}

function queueAnalyticsEntry(_item: typeof queue.items[number], _offsetMinutes: number): LiveAnalyticsEntry | null {
  return null
}

function progressAnalyticsEntry(progress: Record<string, any>, offsetMinutes: number): LiveAnalyticsEntry | null {
  const requestID = String(progress.request_id || '')
  if (!requestID) return null

  const endpointID = String(progress.endpoint_id || '')
  const providerMeta = resolveAnalyticsProvider(String(progress.provider_id || ''), endpointID)
  const modelName = resolveAnalyticsModel(
    endpointID,
    String(progress.selected_upstream_model || progress.endpoint_name || progress.incoming_model || '')
  )
  const startedMs = parseMs(progress.started_at)
  const queuedMs = parseMs(progress.queued_at)
  const anchorMs = startedMs > 0 ? startedMs : queuedMs
  if (!isInActiveRange(anchorMs, offsetMinutes)) {
    return null
  }

  const state = String(progress.state || '')
  const statusCode = Number(progress.status_code || 0)
  let tokensIn = Number(progress.actual_input_tokens || 0)
  let tokensDown = Number(progress.actual_output_tokens || 0)
  const actualTotal = Number(progress.actual_total_tokens || 0)
  const actualCost = Number(progress.actual_cost_micros || 0)
  if (actualTotal > 0 && tokensIn + tokensDown <= 0) {
    tokensDown = actualTotal
  }
  const successful = usageLogSuccessful(state, statusCode)
  const hasActualUsage = actualTotal > 0 || tokensIn > 0 || tokensDown > 0 || actualCost > 0
  if (!successful && (!usageLogTerminal(state) || !hasActualUsage)) {
    return null
  }
  const updatedMs = parseMs(progress.updated_at || progress.finished_at || progress.started_at || progress.queued_at)
  if (updatedMs <= analyticsFetchedAtMs.value) {
    return null
  }

  return {
    requestID,
    providerID: providerMeta.providerID,
    endpointID,
    providerName: providerMeta.providerName,
    modelName,
    anchorMs,
    updatedMs,
    requestCount: 1,
    tokensIn: tokensIn || (successful ? Number(progress.estimated_input_tokens || 0) : 0),
    tokensDown: tokensDown || (successful ? Number(progress.estimated_output_tokens || 0) : 0),
    costMicros: actualCost || (successful ? Number(progress.estimated_cost_micros || 0) : 0)
  }
}

function requestLogAnalyticsEntry(log: RequestLog, offsetMinutes: number): LiveAnalyticsEntry | null {
  const requestID = String(log.request_id || '')
  if (!requestID) return null

  const startedMs = parseMs(log.started_at)
  const queuedMs = parseMs(log.queued_at)
  const finishedMs = parseMs(log.finished_at)
  const createdMs = parseMs(log.created_at)
  const anchorMs = startedMs > 0 ? startedMs : (queuedMs || finishedMs || createdMs)
  if (!isInActiveRange(anchorMs, offsetMinutes)) {
    return null
  }

  const updatedMs = parseMs(log.updated_at || log.finished_at || log.started_at || log.queued_at || log.created_at)
  if (updatedMs <= analyticsFetchedAtMs.value) {
    return null
  }

  const state = String(log.task_state || '')
  const successful = usageLogSuccessful(state, Number(log.status_code || 0))
  const hasActualUsage = usageLogHasActualUsage(log)
  if (!successful && (!usageLogTerminal(state) || !hasActualUsage)) {
    return null
  }

  const endpointID = String(log.endpoint_id || '')
  const providerMeta = resolveAnalyticsProvider(String(log.provider_id || ''), endpointID)
  const modelName = resolveAnalyticsModel(
    endpointID,
    String(log.selected_upstream_model || log.incoming_model || '')
  )
  const hasActualTokens = log.actual_total_tokens > 0 || log.actual_input_tokens > 0 || log.actual_output_tokens > 0
  const actualTotalOnly = log.actual_total_tokens > 0 && log.actual_input_tokens + log.actual_output_tokens <= 0
  const tokensIn = hasActualTokens ? log.actual_input_tokens : (successful ? log.estimated_input_tokens : 0)
  const tokensDown = actualTotalOnly ? log.actual_total_tokens : (hasActualTokens ? log.actual_output_tokens : (successful ? log.estimated_output_tokens : 0))

  return {
    requestID,
    providerID: providerMeta.providerID,
    endpointID,
    providerName: providerMeta.providerName,
    modelName,
    anchorMs,
    updatedMs,
    requestCount: 1,
    tokensIn,
    tokensDown,
    costMicros: log.actual_cost_micros || (successful ? log.estimated_cost_micros : 0)
  }
}

function usageLogTerminal(state?: string | null) {
  return state === 'completed' || state === 'failed' || state === 'cancelled'
}

function usageLogSuccessful(state: string, statusCode: number) {
  return state === 'completed' && statusCode >= 200 && statusCode < 300
}

function usageLogHasActualUsage(log: RequestLog) {
  return log.actual_total_tokens > 0 || log.actual_input_tokens > 0 || log.actual_output_tokens > 0 || log.actual_cost_micros > 0
}

function applyAnalyticsEntry(
  entry: LiveAnalyticsEntry,
  rowsMap: Map<string, UsageAnalyticsRow>,
  totals: UsageAnalyticsTotals,
  seriesPoints: Array<{
    label: string
    value: number
    requests: number
    tokensUp: number
    tokensDown: number
    costMicros: number
    bucketStartMs: number
    bucketEndMs: number
  }>,
  metricName: 'requests' | 'tokens' | 'spend'
) {
  const row = ensureAnalyticsRow(rowsMap, entry.providerID, entry.endpointID, entry.providerName, entry.modelName)

  if (entry.requestCount > 0) {
    row.requests += entry.requestCount
    totals.requests += entry.requestCount
  }
  if (entry.tokensIn > 0 || entry.tokensDown > 0) {
    row.tokens_in += entry.tokensIn
    row.tokens_down += entry.tokensDown
    totals.tokens_in += entry.tokensIn
    totals.tokens_down += entry.tokensDown
  }
  if (entry.costMicros > 0) {
    row.cost_micros += entry.costMicros
    totals.cost_micros += entry.costMicros
  }

  const point = seriesPoints.find(item => entry.anchorMs >= item.bucketStartMs && entry.anchorMs < item.bucketEndMs)
  if (!point) return

  point.requests += entry.requestCount
  point.tokensUp += entry.tokensIn
  point.tokensDown += entry.tokensDown
  point.costMicros += entry.costMicros

  switch (metricName) {
    case 'requests':
      point.value += entry.requestCount
      break
    case 'tokens':
      point.value += entry.tokensIn + entry.tokensDown
      break
    case 'spend':
      point.value += entry.costMicros
      break
  }
}

const analyticsOffsetMinutes = computed(() => analytics.value?.timezone_offset_minutes ?? timezoneOffsetMinutes())

const baseTotals = computed<UsageAnalyticsTotals>(() => analytics.value?.totals ?? {
  requests: 0,
  tokens_in: 0,
  tokens_down: 0,
  cost_micros: 0
})

const baseRows = computed(() => analytics.value?.rows ?? [])
const baseSeries = computed(() =>
  (analytics.value?.series ?? []).map((point) => ({
    label: point.label,
    value: point.value,
    requests: point.requests ?? 0,
    tokensUp: point.tokens_up ?? 0,
    tokensDown: point.tokens_down ?? 0,
    costMicros: point.cost_micros ?? 0,
    bucketStartMs: parseMs(point.bucket_start),
    bucketEndMs: parseMs(point.bucket_end)
  }))
)
const usageAnalyticsParamsKey = computed(() => JSON.stringify(props.usageAnalyticsParams ?? {}))
const hasUsageAnalyticsParams = computed(() => usageAnalyticsParamsKey.value !== '{}')

const liveAnalytics = computed(() => {
  const totals: UsageAnalyticsTotals = {
    requests: baseTotals.value.requests,
    tokens_in: baseTotals.value.tokens_in,
    tokens_down: baseTotals.value.tokens_down,
    cost_micros: baseTotals.value.cost_micros
  }
  const rowsMap = new Map<string, UsageAnalyticsRow>(
    baseRows.value.map((row) => [
      analyticsRowKey(row.provider_id, row.endpoint_id, row.model_name),
      { ...row }
    ])
  )
  const seriesPoints = baseSeries.value.map((point) => ({ ...point }))

  if (!rangeIncludesToday.value || hasUsageAnalyticsParams.value) {
    return {
      totals,
      rows: sortAnalyticsRows([...rowsMap.values()]),
      series: seriesPoints
    }
  }

  const offsetMinutes = analyticsOffsetMinutes.value
  const liveEntries = new Map<string, LiveAnalyticsEntry>()

  for (const item of queue.items) {
    const entry = queueAnalyticsEntry(item, offsetMinutes)
    if (entry) {
      liveEntries.set(entry.requestID, entry)
    }
  }

  for (const progress of Object.values(telemetry.requestProgress)) {
    const requestID = String(progress.request_id || '')
    if (!requestID || liveEntries.has(requestID)) continue
    const entry = progressAnalyticsEntry(progress, offsetMinutes)
    if (entry) {
      liveEntries.set(entry.requestID, entry)
    }
  }

  for (const log of requests.logs) {
    const entry = requestLogAnalyticsEntry(log, offsetMinutes)
    if (entry) {
      liveEntries.set(entry.requestID, entry)
    }
  }

  for (const entry of liveEntries.values()) {
    applyAnalyticsEntry(entry, rowsMap, totals, seriesPoints, metric.value)
  }

  return {
    totals,
    rows: sortAnalyticsRows([...rowsMap.values()]),
    series: seriesPoints
  }
})

const series = computed(() => liveAnalytics.value.series)
const totals = computed(() => zoomAnalytics.value?.totals ?? liveAnalytics.value.totals)
const rows = computed(() => zoomAnalytics.value?.rows ?? liveAnalytics.value.rows)
const visibleRows = computed(() => tableExpanded.value ? rows.value : rows.value.slice(0, 3))
const activeIntentAnalytics = computed(() => zoomIntentAnalytics.value ?? intentAnalytics.value)
const intentItems = computed(() => activeIntentAnalytics.value?.items ?? [])
const visibleIntentItems = computed(() => intentTableExpanded.value ? intentItems.value : intentItems.value.slice(0, 3))

function intentLabel(value: string) {
  return value
    .replace(/[_-]+/g, ' ')
    .replace(/\b\w/g, character => character.toUpperCase())
}

const intentMetricLabel = computed(() => {
  switch (metric.value) {
    case 'tokens':
      return 'Tokens'
    case 'spend':
      return 'Spend'
    default:
      return 'Requests'
  }
})

function intentMetricValue(value: number) {
  return metric.value === 'spend' ? currencyMicros(value) : number(value)
}

async function refreshIntentAnalytics() {
  const seq = ++intentRefreshSeq
  intentLoading.value = true
  intentLoadError.value = ''
  intentAnalytics.value = null
  try {
    const response = await api.characterizationIntentAnalytics(
      selectedRange.value.start,
      selectedRange.value.end,
      metric.value,
      timezoneOffsetMinutes(),
      props.usageAnalyticsParams
    )
    if (seq !== intentRefreshSeq) return
    intentAnalytics.value = response
  } catch (error: any) {
    if (seq !== intentRefreshSeq) return
    intentLoadError.value = error?.data?.message || error?.message || 'Unable to load characterization intent analytics.'
  } finally {
    if (seq === intentRefreshSeq) {
      intentLoading.value = false
    }
  }
}

async function refreshZoomIntentAnalytics(range: { startIndex: number, endIndex: number }) {
  const firstPoint = series.value[range.startIndex]
  const lastPoint = series.value[range.endIndex]
  if (!firstPoint || !lastPoint || firstPoint.bucketStartMs <= 0 || lastPoint.bucketEndMs <= firstPoint.bucketStartMs) {
    zoomIntentAnalytics.value = null
    return
  }

  const seq = ++zoomIntentRefreshSeq
  intentLoading.value = true
  intentLoadError.value = ''
  try {
    const response = await api.characterizationIntentAnalytics(
      selectedRange.value.start,
      selectedRange.value.end,
      metric.value,
      timezoneOffsetMinutes(),
      {
        ...props.usageAnalyticsParams,
        range_start: new Date(firstPoint.bucketStartMs).toISOString(),
        range_end: new Date(lastPoint.bucketEndMs).toISOString()
      }
    )
    if (
      seq !== zoomIntentRefreshSeq ||
      zoomRange.value?.startIndex !== range.startIndex ||
      zoomRange.value?.endIndex !== range.endIndex
    ) return
    zoomIntentAnalytics.value = response
  } catch (error: any) {
    if (seq !== zoomIntentRefreshSeq) return
    zoomIntentAnalytics.value = null
    intentLoadError.value = error?.data?.message || error?.message || 'Unable to load characterization intents for the selected graph range.'
  } finally {
    if (seq === zoomIntentRefreshSeq) {
      intentLoading.value = false
    }
  }
}

async function refreshZoomAnalytics(range: { startIndex: number, endIndex: number }) {
  const firstPoint = series.value[range.startIndex]
  const lastPoint = series.value[range.endIndex]
  if (!firstPoint || !lastPoint || firstPoint.bucketStartMs <= 0 || lastPoint.bucketEndMs <= firstPoint.bucketStartMs) {
    zoomAnalytics.value = null
    return
  }

  const seq = ++zoomRefreshSeq
  zoomLoadError.value = ''
  try {
    const response = await api.usageAnalytics(
      selectedRange.value.start,
      selectedRange.value.end,
      metric.value,
      bucket.value,
      timezoneOffsetMinutes(),
      {
        ...props.usageAnalyticsParams,
        range_start: new Date(firstPoint.bucketStartMs).toISOString(),
        range_end: new Date(lastPoint.bucketEndMs).toISOString()
      }
    )
    if (
      seq !== zoomRefreshSeq ||
      zoomRange.value?.startIndex !== range.startIndex ||
      zoomRange.value?.endIndex !== range.endIndex
    ) return
    zoomAnalytics.value = response
  } catch (error: any) {
    if (seq !== zoomRefreshSeq) return
    zoomAnalytics.value = null
    zoomLoadError.value = error?.data?.message || error?.message || 'Unable to load usage for the selected graph range.'
  }
}

async function handleZoomRange(range: { startIndex: number, endIndex: number } | null) {
  zoomRefreshSeq += 1
  zoomIntentRefreshSeq += 1
  tableExpanded.value = false
  intentTableExpanded.value = false
  zoomRange.value = range
  zoomAnalytics.value = null
  zoomIntentAnalytics.value = null
  zoomLoadError.value = ''
  if (range) {
    await Promise.all([
      refreshZoomAnalytics(range),
      refreshZoomIntentAnalytics(range)
    ])
  }
}

const chartSeries = computed(() => {
  const localToday = localDateInputValue()
  const nowMs = nowTick.value
  const shouldClampFutureBuckets = activeEndDate.value >= localToday
  let lastVisibleIndex = -1

  const points: Array<{
    label: string
    value: number | null
    tokensUp?: number | null
    tokensDown?: number | null
    highlight?: boolean
  }> = series.value.map((point, index) => {
    const isFutureBucket = shouldClampFutureBuckets && Number.isFinite(point.bucketStartMs) && point.bucketStartMs > nowMs
    const nextPoint = {
      label: point.label,
      value: isFutureBucket ? null : point.value,
      tokensUp: metric.value === 'tokens' ? (isFutureBucket ? null : point.tokensUp) : undefined,
      tokensDown: metric.value === 'tokens' ? (isFutureBucket ? null : point.tokensDown) : undefined
    }
    if (nextPoint.value != null) {
      lastVisibleIndex = index
    }
    return nextPoint
  })

  if (rangeIncludesToday.value && lastVisibleIndex >= 0) {
    points[lastVisibleIndex] = {
      ...points[lastVisibleIndex],
      highlight: true
    }
  }

  return points
})

const totalCards = computed(() => [
  {
    label: 'Total requests',
    value: number(totals.value.requests),
    hint: 'Requests represented in the selected range.'
  },
  {
    label: 'Tokens up',
    value: number(totals.value.tokens_in),
    hint: 'Prompt-side tokens sent upstream.'
  },
  {
    label: 'Tokens down',
    value: number(totals.value.tokens_down),
    hint: 'Response-side tokens returned downstream.'
  },
  {
    label: 'Spend',
    value: currencyMicros(totals.value.cost_micros),
    hint: 'Actual cost when available, otherwise estimate.'
  }
])

const bucketSubtitle = computed(() => {
  switch (bucket.value) {
    case 'minute':
      return 'Minute buckets across the selected date range.'
    case 'hour':
      return 'Hourly buckets across the selected date range.'
    case 'day':
      return 'Daily buckets across the selected date range.'
    case 'week':
      return 'Seven-day buckets across the selected date range.'
    case 'month':
      return 'Monthly buckets across the selected date range.'
  }
})

onMounted(async () => {
  if (process.client) {
    tickTimer = window.setInterval(() => {
      nowTick.value = Date.now()
    }, 60_000)
  }

  await Promise.all([
    refreshAnalytics(),
    refreshIntentAnalytics(),
    pageData.load('usage')
  ])
})

onBeforeUnmount(() => {
  if (tickTimer && process.client) {
    window.clearInterval(tickTimer)
    tickTimer = null
  }
})

watch([metric, bucket, () => selectedRange.value.start, () => selectedRange.value.end, usageAnalyticsParamsKey], async () => {
  tableExpanded.value = false
  intentTableExpanded.value = false
  usageChart.value?.resetZoom()
  await Promise.all([
    refreshAnalytics(),
    refreshIntentAnalytics()
  ])
})
</script>

<template>
  <div class="space-y-8">
    <div>
      <h1 class="app-title">Usage</h1>
    </div>

    <UiPanel :padded="false">
      <div class="space-y-3 px-4 pb-2 pt-5 sm:px-6 md:px-8 md:pt-9">
        <div class="flex min-w-0 flex-wrap items-center justify-end gap-3">
          <UiTabs v-model="metric" :tabs="['requests', 'tokens', 'spend']" />
          <UiTabs v-model="bucket" :tabs="bucketTabs" />
          <UiDateRangePicker
            v-model:start-date="startDate"
            v-model:end-date="endDate"
            class="w-full shrink-0 sm:w-auto"
          />
        </div>

        <div class="flex min-w-0 flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center sm:justify-end">
          <slot
            name="filters-extra"
            :range-label="rangeLabel"
            :metric="metric"
            :bucket="bucket"
            :loading="loading"
          />
        </div>
      </div>

      <div class="px-4 pb-5 pt-4 sm:px-6 sm:pb-7 md:px-8">
        <DashboardUsageAreaChart
          ref="usageChart"
          :metric="metric"
          window="day"
          :points="chartSeries"
          title=""
          :subtitle="bucketSubtitle"
          :badge-label="`${metric} · ${rangeLabel}`"
          zoomable
          :surface="false"
          @zoom-range="handleZoomRange"
        />
        <p v-if="loading" class="mt-3 px-1 text-xs text-slate-400">Loading usage analytics…</p>
        <p v-else-if="loadError" class="mt-3 px-1 text-xs text-rose-300">{{ loadError }}</p>
        <p v-if="zoomLoadError" class="mt-3 px-1 text-xs text-rose-300">{{ zoomLoadError }}</p>
      </div>
    </UiPanel>

    <div class="ui-metric-strip sm:grid-cols-2 xl:grid-cols-4">
      <div
        v-for="card in totalCards"
        :key="card.label"
        class="app-metric"
      >
        <p class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">{{ card.label }}</p>
        <p class="mt-2 text-2xl font-semibold tracking-tight text-white">{{ card.value }}</p>
        <p class="mt-1 text-sm leading-6 text-slate-200/72">{{ card.hint }}</p>
      </div>
    </div>

    <slot
      name="summary-extra"
      :range-label="rangeLabel"
      :totals="totals"
      :rows="rows"
      :loading="loading"
      :load-error="loadError"
    />

    <section class="space-y-4">
      <header>
        <h2 class="text-xl font-semibold tracking-tight text-white">Provider and Model Totals</h2>
      </header>
      <slot
        name="table-before"
        :rows="rows"
        :loading="loading"
        :load-error="loadError"
      />

      <UiEmptyState
        v-if="!rows.length && !loading && !loadError"
        title="No usage for this range"
        description="Choose another range or send traffic."
      />
      <slot
        v-if="!rows.length && !loading && !loadError"
        name="empty-extra"
        :range-label="rangeLabel"
      />

      <div v-else class="space-y-1">
        <div class="relative">
          <UiTable
            :class="!tableExpanded && rows.length > 3 ? 'max-h-[183px]' : ''"
            :columns="['Provider / model', 'Requests', 'Tokens up', 'Tokens down', 'Cost']"
          >
            <tr v-for="row in visibleRows" :key="`${row.provider_id}-${row.endpoint_id}-${row.model_name}`">
              <td class="px-5 py-4">
                <p class="font-medium text-white">{{ usageRouteTarget(row) }}</p>
              </td>
              <td class="px-5 py-4 text-slate-200/84">{{ number(row.requests) }}</td>
              <td class="px-5 py-4 text-slate-200/84">{{ number(row.tokens_in) }}</td>
              <td class="px-5 py-4 text-slate-200/84">{{ number(row.tokens_down) }}</td>
              <td class="px-5 py-4 text-slate-200/84">{{ currencyMicros(row.cost_micros) }}</td>
            </tr>
          </UiTable>
          <div
            v-if="!tableExpanded && rows.length > 3"
            class="usage-table-bottom-shadow pointer-events-none absolute inset-x-px bottom-px h-10"
            aria-hidden="true"
          />
        </div>

        <div v-if="rows.length > 3" class="flex justify-center">
          <UiButton tone="ghost" size="sm" @click="tableExpanded = !tableExpanded">
            {{ tableExpanded ? 'Show less' : 'Show more' }}
            <svg class="ml-1 h-4 w-4" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path
                :d="tableExpanded ? 'M6 15l6-6 6 6' : 'M6 9l6 6 6-6'"
                stroke="currentColor"
                stroke-width="2"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </UiButton>
        </div>
      </div>

      <slot
        name="table-after"
        :rows="rows"
        :loading="loading"
        :load-error="loadError"
      />
    </section>

    <section class="space-y-4">
      <header>
        <h2 class="text-xl font-semibold tracking-tight text-white">Characterization Intent Breakdown</h2>
      </header>

      <UiPanel v-if="intentItems.length" :padded="false">
        <div class="px-4 py-4 sm:px-6 md:px-8">
          <DashboardUsageIntentPieChart :items="intentItems" :metric="metric" />
        </div>
      </UiPanel>

      <p v-if="intentLoading" class="px-1 text-xs text-slate-400">Loading characterization intent analytics…</p>
      <p v-else-if="intentLoadError" class="px-1 text-xs text-rose-300">{{ intentLoadError }}</p>
      <UiEmptyState
        v-else-if="!intentItems.length"
        title="No characterized requests for this range"
        description="Characterized requests will appear here as traffic is classified."
      />

      <div v-if="intentItems.length" class="space-y-1">
        <div class="relative">
          <UiTable
            :class="!intentTableExpanded && intentItems.length > 3 ? 'max-h-[183px]' : ''"
            :columns="['Intent', intentMetricLabel, 'Share']"
          >
            <tr v-for="item in visibleIntentItems" :key="item.primary_action">
              <td class="px-5 py-4 font-medium text-white">{{ intentLabel(item.primary_action) }}</td>
              <td class="px-5 py-4 text-slate-200/84">{{ intentMetricValue(item.value) }}</td>
              <td class="px-5 py-4 text-slate-200/84">{{ item.percentage.toFixed(1) }}%</td>
            </tr>
          </UiTable>
          <div
            v-if="!intentTableExpanded && intentItems.length > 3"
            class="usage-table-bottom-shadow pointer-events-none absolute inset-x-px bottom-px h-10"
            aria-hidden="true"
          />
        </div>

        <div v-if="intentItems.length > 3" class="flex justify-center">
          <UiButton tone="ghost" size="sm" @click="intentTableExpanded = !intentTableExpanded">
            {{ intentTableExpanded ? 'Show less' : 'Show more' }}
            <svg class="ml-1 h-4 w-4" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path
                :d="intentTableExpanded ? 'M6 15l6-6 6 6' : 'M6 9l6 6 6-6'"
                stroke="currentColor"
                stroke-width="2"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </UiButton>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.usage-table-bottom-shadow {
  background: transparent;
  box-shadow: inset 0 -20px 18px -18px color-mix(in srgb, var(--app-heading) 38%, transparent);
}
</style>
