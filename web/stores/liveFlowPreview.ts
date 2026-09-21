import { defineStore } from 'pinia'
import type { CapacityLimitRow, CapacitySnapshot, QueueItem, QueueSnapshot, RequestLog, TelemetryEvent } from '~/types/admin'

type LiveRequestProgress = {
  request_id: string
  task_id?: string
  endpoint_id?: string
  endpoint_name?: string
  provider_id?: string
  lane?: string
  incoming_model?: string
  selected_upstream_model?: string
  state?: string
  substatus?: string
  queued_at?: string
  started_at?: string
  finished_at?: string
  wait_ms?: number
  latency_ms?: number
  status_code?: number
  uploaded_tokens?: number
  downloaded_tokens?: number
  fallback_count?: number
  fallback?: number
  updated_at?: string
}

type PreviewEndpointHealth = {
  endpoint_id: string
  provider_id: string
  health_status: string
  cooldown_until: string | null
}

type PreviewSessionResponse = {
  preview_session_id: string
  preview_generation?: number
}

const terminalQueueStates = new Set(['completed', 'failed', 'cancelled'])
const queuedPreviewStates = new Set(['queued', 'waiting', 'ready'])
const requestScopedDeferScopes = new Set(['user', 'user_model', 'user_provider', 'api_key', 'api_key_model', 'api_key_provider'])
const deferredSnapshotGapRetentionMs = 3500
const previewUserRowStaleGuardMs = 5 * 60 * 1000

export const useLiveFlowPreviewStore = defineStore('liveFlowPreview', {
  state: () => ({
    sessionID: '',
    previewGeneration: 0,
    connected: false,
    reconnecting: false,
    enqueueRunning: false,
    loading: false,
    phase: 'Ready to enqueue synthetic traffic',
    events: [] as TelemetryEvent[],
    queueItems: [] as QueueItem[],
    logs: [] as RequestLog[],
    capacitySnapshot: null as CapacitySnapshot | null,
    queueSnapshot: null as QueueSnapshot | null,
    lastCapacityHydratedAt: 0,
    requestProgress: {} as Record<string, LiveRequestProgress>,
    generatedRequestIDs: {} as Record<string, true>,
    completedRequestIDs: {} as Record<string, true>,
    endpointHealth: {} as Record<string, PreviewEndpointHealth>,
    lastSeenByRequestID: {} as Record<string, number>,
    socket: null as WebSocket | null,
    retryTimer: null as number | null,
    shouldReconnect: true
  }),
  getters: {
    generatedCount: state => Object.keys(state.generatedRequestIDs).length,
    queuedCount: state => state.queueSnapshot
      ? queueStateCount(state.queueSnapshot, ['queued', 'waiting', 'ready'])
      : state.queueItems.filter(item => queuedPreviewStates.has(item.state)).length,
    activeCount: state => Math.max(
      queueStateCount(state.queueSnapshot, ['in_flight']),
      state.queueItems.filter(item => item.state === 'in_flight').length,
      new Set(Object.values(state.requestProgress)
        .filter(item => item.state === 'in_flight')
        .map(item => item.request_id)).size
    ),
    completedCount: state => Object.keys(state.completedRequestIDs).length,
    fallbackCount: state => Math.max(
      0,
      ...state.queueItems.map(item => item.fallback_count || 0),
      ...Object.values(state.requestProgress).map(item => Number(item.fallback_count ?? item.fallback ?? 0)),
      ...state.logs.map(item => item.fallback_count || 0)
    )
  },
  actions: {
    applySessionResponse(response?: PreviewSessionResponse | null) {
      if (!response) return
      if (response.preview_session_id) {
        this.sessionID = response.preview_session_id
      }
      this.applyPreviewGeneration(response.preview_generation)
    },
    async ensureSession() {
      if (this.sessionID) return this.sessionID
      const response = await useRelayApi().post<PreviewSessionResponse>('/api/preview/live-flow/sessions')
      this.applySessionResponse(response)
      return this.sessionID
    },
    async connect() {
      if (!process.client) return
      const sessionID = await this.ensureSession()
      if (this.socket) return
      this.shouldReconnect = true
      const socket = new WebSocket(previewWebSocketURL(sessionID))
      this.socket = socket
      this.reconnecting = this.events.length > 0
      socket.onopen = () => {
        this.connected = true
        this.reconnecting = false
      }
      socket.onmessage = (event) => {
        let payload: TelemetryEvent
        try {
          payload = JSON.parse(event.data) as TelemetryEvent
        } catch {
          return
        }
        this.applyEvent(payload)
      }
      socket.onclose = () => {
        if (this.retryTimer && process.client) window.clearTimeout(this.retryTimer)
        const resetClosedSession = this.shouldReconnect
        this.connected = false
        this.reconnecting = false
        this.retryTimer = null
        if (this.socket === socket) this.socket = null
        if (resetClosedSession) {
          this.sessionID = ''
          this.previewGeneration = 0
          this.clearRuntimeState()
          this.phase = 'Preview stream closed; ready to start a fresh preview engine'
        }
      }
      socket.onerror = () => {
        this.connected = false
      }
    },
    async enqueue(payload: Record<string, any>) {
      this.loading = true
      try {
        const sessionID = await this.ensureSession()
        await this.connect()
        const response = await useRelayApi().post<PreviewSessionResponse & { ok?: boolean }>(`/api/preview/live-flow/sessions/${encodeURIComponent(sessionID)}/enqueue`, payload)
        this.applySessionResponse(response)
      } finally {
        this.loading = false
      }
    },
    async stopEnqueue() {
      if (!this.sessionID) return
      const response = await useRelayApi().post<PreviewSessionResponse & { ok?: boolean }>(`/api/preview/live-flow/sessions/${encodeURIComponent(this.sessionID)}/stop`)
      this.applySessionResponse(response)
      this.enqueueRunning = false
      this.phase = this.queuedCount ? 'Traffic enqueue paused; visible requests will keep cycling' : 'Ready to enqueue synthetic traffic'
    },
    async resetSession() {
      const sessionID = await this.ensureSession()
      this.clearRuntimeState()
      if (this.previewGeneration > 0) {
        this.previewGeneration += 1
      }
      const response = await useRelayApi().post<PreviewSessionResponse>(`/api/preview/live-flow/sessions/${encodeURIComponent(sessionID)}/reset`)
      this.applySessionResponse(response)
      await this.connect()
      this.phase = 'Ready to enqueue synthetic traffic'
    },
    disconnect() {
      if (this.retryTimer && process.client) window.clearTimeout(this.retryTimer)
      this.shouldReconnect = false
      this.retryTimer = null
      this.socket?.close()
      this.socket = null
      this.connected = false
      this.reconnecting = false
      this.sessionID = ''
      this.previewGeneration = 0
    },
    async destroySession() {
      const sessionID = this.sessionID
      this.disconnect()
      this.clearRuntimeState()
      this.phase = 'Ready to enqueue synthetic traffic'
      if (!sessionID) return
      try {
        await deletePreviewSession(sessionID)
      } catch {
        // The websocket close path also tears down the backend session.
      }
    },
    destroySessionOnUnload() {
      const sessionID = this.sessionID
      if (this.retryTimer && process.client) window.clearTimeout(this.retryTimer)
      this.shouldReconnect = false
      this.retryTimer = null
      this.socket?.close()
      this.socket = null
      this.connected = false
      this.reconnecting = false
      this.sessionID = ''
      this.previewGeneration = 0
      this.clearRuntimeState()
      this.phase = 'Ready to enqueue synthetic traffic'
      if (sessionID) void deletePreviewSession(sessionID, true).catch(() => {})
    },
    clearRuntimeState() {
      this.events = []
      this.queueItems = []
      this.logs = []
      this.capacitySnapshot = null
      this.queueSnapshot = null
      this.lastCapacityHydratedAt = 0
      this.requestProgress = {}
      this.generatedRequestIDs = {}
      this.completedRequestIDs = {}
      this.endpointHealth = {}
      this.lastSeenByRequestID = {}
      this.enqueueRunning = false
    },
    applyEvent(event: TelemetryEvent) {
      if (!this.eventBelongsToActivePreview(event)) return
      this.events.unshift(summarizeTelemetryEvent(event))
      this.events = this.events.slice(0, 80)

      if (event.type === 'preview_reset') {
        this.applyPreviewIdentity(event.payload)
        this.clearRuntimeState()
        this.phase = 'Ready to enqueue synthetic traffic'
        return
      }
      if (event.type === 'capacity_snapshot' || event.type === 'realtime_snapshot') {
        this.applyPreviewIdentity(event.payload)
        this.capacitySnapshot = mergeRecentPreviewUserRows(event.payload as CapacitySnapshot, this.capacitySnapshot, this.lastCapacityHydratedAt)
        this.queueSnapshot = (event.payload as CapacitySnapshot)?.queue || null
        this.lastCapacityHydratedAt = Date.now()
        this.hydrateQueueItems(event.payload?.queue_items)
        return
      }
      if (event.type === 'preview_enqueue_started') {
        this.applyPreviewIdentity(event.payload)
        this.enqueueRunning = true
        this.phase = 'Backend preview engine enqueueing traffic'
        return
      }
      if (event.type === 'preview_enqueue_stopped') {
        this.applyPreviewIdentity(event.payload)
        this.enqueueRunning = false
        this.phase = this.queuedCount ? 'Traffic enqueue paused; visible requests will keep cycling' : 'Ready to enqueue synthetic traffic'
        return
      }
      if (event.type === 'preview_enqueue_finished') {
        this.applyPreviewIdentity(event.payload)
        this.enqueueRunning = false
        this.phase = `${this.generatedCount} generated · ${this.queuedCount} queued · enqueue complete`
        return
      }
      if (event.type.startsWith('request_') && event.payload?.request_id) {
        this.mergeRequestProgress(event.payload)
        this.mergeQueueFromTelemetry(event.payload)
      }
      if (event.type === 'request_log' && event.payload?.request_id) {
        this.mergeRequestLog(event.payload)
        this.mergeQueueFromTelemetry(event.payload)
      }
      if (event.type === 'endpoint_health_change') {
        this.mergeEndpointHealth(event.payload)
      }
      this.phase = `${this.generatedCount} generated · ${this.queuedCount} queued · ${this.completedCount} completed`
    },
    applyPreviewIdentity(payload: Record<string, any> = {}) {
      const sessionID = String(payload.preview_session_id || '')
      if (sessionID) this.sessionID = sessionID
      this.applyPreviewGeneration(payload.preview_generation)
    },
    applyPreviewGeneration(value: any) {
      const generation = Number(value || 0)
      if (Number.isFinite(generation) && generation > 0 && (!this.previewGeneration || generation >= this.previewGeneration)) {
        this.previewGeneration = generation
      }
    },
    eventBelongsToActivePreview(event: TelemetryEvent) {
      const payload = event.payload || {}
      if (event.type === 'preview_reset') {
        return this.previewPayloadHasIdentity(payload) && this.previewPayloadBelongsToSession(payload) && this.previewPayloadIsCurrentOrNewerGeneration(payload)
      }
      if (event.type === 'capacity_snapshot' || event.type === 'realtime_snapshot' || event.type === 'preview_enqueue_started' || event.type === 'preview_enqueue_stopped' || event.type === 'preview_enqueue_finished') {
        return this.previewPayloadHasIdentity(payload) && this.previewPayloadBelongsToSession(payload) && this.previewPayloadBelongsToGeneration(payload)
      }
      if (!this.previewPayloadBelongsToSession(payload)) return false
      const requestID = String(payload.request_id || '')
      if (requestID) return this.requestIDBelongsToActivePreview(requestID)
      return this.previewPayloadBelongsToGeneration(payload)
    },
    previewPayloadBelongsToSession(payload: Record<string, any> = {}) {
      const sessionID = String(payload.preview_session_id || '')
      return !sessionID || !this.sessionID || sessionID === this.sessionID
    },
    previewPayloadHasIdentity(payload: Record<string, any> = {}) {
      return Boolean(String(payload.preview_session_id || '') && Number(payload.preview_generation || 0) > 0)
    },
    previewPayloadBelongsToGeneration(payload: Record<string, any> = {}) {
      const generation = Number(payload.preview_generation || 0)
      return !Number.isFinite(generation) || generation <= 0 || !this.previewGeneration || generation === this.previewGeneration
    },
    previewPayloadIsCurrentOrNewerGeneration(payload: Record<string, any> = {}) {
      const generation = Number(payload.preview_generation || 0)
      return !Number.isFinite(generation) || generation <= 0 || !this.previewGeneration || generation >= this.previewGeneration
    },
    requestIDBelongsToActivePreview(requestID: string) {
      const parts = parsePreviewRequestID(requestID)
      if (!parts) return true
      if (this.sessionID && parts.sessionID !== this.sessionID) return false
      if (this.previewGeneration > 0 && parts.generation !== this.previewGeneration) return false
      return true
    },
    hydrateQueueItems(items?: QueueItem[]) {
      if (!Array.isArray(items)) return
      const now = Date.now()
      const existingByRequestID = new Map(this.queueItems.map(item => [item.request_id, item]))
      const snapshotRequestIDs = new Set<string>()
      const nextItems: QueueItem[] = []

      for (const rawItem of items) {
        if (!this.requestIDBelongsToActivePreview(rawItem.request_id)) continue
        this.rememberGeneratedRequest(rawItem.request_id)
        const item = normalizeQueueItem(rawItem, existingByRequestID.get(rawItem.request_id))
        if (!item) continue
        snapshotRequestIDs.add(item.request_id)
        if (isTerminalQueueState(item.state)) {
          delete this.lastSeenByRequestID[item.request_id]
          continue
        }
        nextItems.push(item)
        this.lastSeenByRequestID[item.request_id] = now
      }

      const retained = this.queueItems.filter((item) => {
        if (snapshotRequestIDs.has(item.request_id) || !isRecentRequestScopedDefer(item, this.lastSeenByRequestID[item.request_id], now)) {
          return false
        }
        return true
      })

      this.queueItems = dedupeQueueItems([...nextItems, ...retained])
      this.pruneSeenRequestIDs(now)
    },
    mergeQueueFromTelemetry(payload: Record<string, any>) {
      const requestID = String(payload.request_id || '')
      if (!requestID || !this.requestIDBelongsToActivePreview(requestID)) return
      this.rememberGeneratedRequest(requestID)

      const index = this.queueItems.findIndex(item => item.request_id === requestID || item.task_id === String(payload.task_id || ''))
      const existing = index >= 0 ? this.queueItems[index] : null
      const state = String(payload.state || payload.task_state || existing?.state || '')
      if (!state) return

      if (isTerminalQueueState(state)) {
        if (state === 'completed') this.rememberCompletedRequest(requestID)
        if (index >= 0) this.queueItems.splice(index, 1)
        delete this.lastSeenByRequestID[requestID]
        return
      }

      const next = normalizeQueueItem(payload, existing)
      if (!next) return
      this.lastSeenByRequestID[next.request_id] = Date.now()
      if (index >= 0) {
        this.queueItems.splice(index, 1, next)
        return
      }
      this.queueItems.unshift(next)
    },
    pruneSeenRequestIDs(now = Date.now()) {
      const activeRequestIDs = new Set(this.queueItems.map(item => item.request_id))
      for (const [requestID, lastSeen] of Object.entries(this.lastSeenByRequestID)) {
        if (activeRequestIDs.has(requestID)) continue
        if (now - lastSeen > deferredSnapshotGapRetentionMs) {
          delete this.lastSeenByRequestID[requestID]
        }
      }
    },
    mergeRequestProgress(payload: Record<string, any>) {
      const requestID = String(payload.request_id || '')
      if (!requestID || !this.requestIDBelongsToActivePreview(requestID)) return
      this.rememberGeneratedRequest(requestID)
      const state = String(payload.state || payload.task_state || '')
      if (isTerminalQueueState(state)) {
        if (state === 'completed') this.rememberCompletedRequest(requestID)
        delete this.requestProgress[requestID]
        return
      }
      const current = this.requestProgress[requestID] || { request_id: requestID }
      this.requestProgress[requestID] = {
        ...current,
        ...payload,
        updated_at: new Date().toISOString()
      }
    },
    mergeRequestLog(payload: Record<string, any>) {
      const requestID = String(payload.request_id || '')
      if (!requestID || !this.requestIDBelongsToActivePreview(requestID)) return
      this.rememberGeneratedRequest(requestID)
      const next: RequestLog = {
        id: String(payload.id || requestID),
        request_id: requestID,
        parent_request_id: String(payload.parent_request_id || ''),
        lane_id: payload.lane_id == null ? null : String(payload.lane_id),
        endpoint_id: payload.endpoint_id == null ? null : String(payload.endpoint_id),
        provider_id: payload.provider_id == null ? null : String(payload.provider_id),
        route_kind: (payload.route_kind || 'chat') as RequestLog['route_kind'],
        incoming_model: String(payload.incoming_model || ''),
        selected_upstream_model: String(payload.selected_upstream_model || ''),
        status_code: Number(payload.status_code || 0),
        task_state: String(payload.task_state || payload.state || ''),
        queued_at: payload.queued_at ? String(payload.queued_at) : null,
        started_at: payload.started_at ? String(payload.started_at) : null,
        finished_at: payload.finished_at ? String(payload.finished_at) : null,
        wait_ms: Number(payload.wait_ms || 0),
        latency_ms: Number(payload.latency_ms || 0),
        streaming: Boolean(payload.streaming),
        priority: Number(payload.priority || 0),
        fallback_count: Number(payload.fallback_count || 0),
        estimated_input_tokens: Number(payload.estimated_input_tokens || 0),
        estimated_output_tokens: Number(payload.estimated_output_tokens || 0),
        actual_input_tokens: Number(payload.actual_input_tokens || 0),
        actual_output_tokens: Number(payload.actual_output_tokens || 0),
        actual_total_tokens: Number(payload.actual_total_tokens || 0),
        estimated_cost_micros: Number(payload.estimated_cost_micros || 0),
        actual_cost_micros: Number(payload.actual_cost_micros || 0),
        error_text: String(payload.error_text || ''),
        created_at: String(payload.created_at || payload.queued_at || payload.finished_at || new Date().toISOString()),
        updated_at: String(payload.updated_at || payload.finished_at || payload.created_at || new Date().toISOString())
      }
      const index = this.logs.findIndex(item => item.request_id === requestID)
      if (index >= 0) this.logs.splice(index, 1, { ...this.logs[index], ...next })
      else this.logs.unshift(next)
      this.logs.sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
      this.logs = this.logs.slice(0, 80)
      if (next.task_state === 'completed') this.rememberCompletedRequest(requestID)
      delete this.requestProgress[requestID]
    },
    rememberGeneratedRequest(requestID: string) {
      if (!requestID) return
      this.generatedRequestIDs[requestID] = true
    },
    rememberCompletedRequest(requestID: string) {
      if (!requestID) return
      this.completedRequestIDs[requestID] = true
    },
    mergeEndpointHealth(payload: Record<string, any>) {
      const endpointID = String(payload.endpoint_id || '')
      if (!endpointID) return
      this.endpointHealth[endpointID] = {
        endpoint_id: endpointID,
        provider_id: String(payload.provider_id || ''),
        health_status: String(payload.health_status || 'healthy'),
        cooldown_until: payload.cooldown_until ? String(payload.cooldown_until) : null
      }
    }
  }
})

function normalizeQueueItem(payload: Record<string, any>, existing?: QueueItem | null): QueueItem | null {
  const requestID = stringField(payload, 'request_id', existing?.request_id || '')
  const state = stringField(payload, 'state', '') || stringField(payload, 'task_state', existing?.state || '')
  if (!requestID || !state) return null

  const queuedAt = isoField(payload, 'queued_at', existing?.queued_at || new Date().toISOString())
  const predictedEligibleAt = isoValue(payload.predicted_eligible_at ?? payload.eligible_at, existing?.predicted_eligible_at || queuedAt)
  const selectedUpstreamModel = stringField(payload, 'selected_upstream_model', '')
  const endpointName = stringField(payload, 'endpoint_name', existing?.endpoint_name || selectedUpstreamModel || 'Pending endpoint')
  const fallbackCount = payload.fallback_count == null
    ? numberValue(payload.fallback, existing?.fallback_count || 0)
    : numberValue(payload.fallback_count, existing?.fallback_count || 0)

  return {
    task_id: stringField(payload, 'task_id', existing?.task_id || requestID),
    request_id: requestID,
    lane: stringField(payload, 'lane', existing?.lane || ''),
    incoming_model: stringField(payload, 'incoming_model', existing?.incoming_model || ''),
    endpoint_id: stringField(payload, 'endpoint_id', existing?.endpoint_id || ''),
    endpoint_name: endpointName,
    provider_id: stringField(payload, 'provider_id', existing?.provider_id || ''),
    actor_id: stringField(payload, 'actor_id', existing?.actor_id || ''),
    state,
    priority: numberField(payload, 'priority', existing?.priority || 0),
    queued_at: queuedAt,
    started_at: nullableISOField(payload, 'started_at', existing?.started_at || null),
    wait_ms: numberField(payload, 'wait_ms', existing?.wait_ms || 0),
    predicted_eligible_at: predictedEligibleAt,
    resource_eligible_at: nullableISOField(payload, 'resource_eligible_at', existing?.resource_eligible_at || null),
    user_eligible_at: nullableISOField(payload, 'user_eligible_at', existing?.user_eligible_at || null),
    user_limit_reason: stringField(payload, 'user_limit_reason', existing?.user_limit_reason || ''),
    defer_scope: stringField(payload, 'defer_scope', existing?.defer_scope || ''),
    defer_reason: stringField(payload, 'defer_reason', existing?.defer_reason || ''),
    delay_reason: stringField(payload, 'delay_reason', existing?.delay_reason || ''),
    substatus: stringField(payload, 'substatus', existing?.substatus || ''),
    uploaded_tokens: numberField(payload, 'uploaded_tokens', existing?.uploaded_tokens || 0),
    downloaded_tokens: numberField(payload, 'downloaded_tokens', existing?.downloaded_tokens || 0),
    estimated_cost_micros: numberField(payload, 'estimated_cost_micros', existing?.estimated_cost_micros || 0),
    fallback_count: fallbackCount,
    candidate_trace: Array.isArray(payload.candidate_trace) ? payload.candidate_trace : existing?.candidate_trace || []
  }
}

function queueStateCount(snapshot: QueueSnapshot | null, states: string[]) {
  if (!snapshot?.states) return 0
  return states.reduce((sum, state) => sum + Number(snapshot.states[state] || 0), 0)
}

function summarizeTelemetryEvent(event: TelemetryEvent): TelemetryEvent {
  const payload = event.payload || {}
  return {
    type: event.type,
    timestamp: event.timestamp,
    payload: {
      request_id: payload.request_id,
      task_id: payload.task_id,
      state: payload.state,
      task_state: payload.task_state,
      endpoint_id: payload.endpoint_id,
      provider_id: payload.provider_id,
      lane: payload.lane,
      incoming_model: payload.incoming_model,
      selected_upstream_model: payload.selected_upstream_model,
      status_code: payload.status_code,
      sequence: payload.sequence,
      preview_session_id: payload.preview_session_id,
      preview_generation: payload.preview_generation,
      reason: payload.reason
    }
  }
}

function stringField(payload: Record<string, any>, key: string, fallback: string) {
  if (!hasOwn(payload, key)) return fallback
  const value = payload[key]
  return value == null ? '' : String(value)
}

function numberField(payload: Record<string, any>, key: string, fallback: number) {
  if (!hasOwn(payload, key)) return fallback
  return numberValue(payload[key], fallback)
}

function numberValue(value: any, fallback: number) {
  if (value == null || value === '') return fallback
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

function isoField(payload: Record<string, any>, key: string, fallback: string) {
  if (!hasOwn(payload, key)) return fallback
  return isoValue(payload[key], fallback)
}

function nullableISOField(payload: Record<string, any>, key: string, fallback: string | null) {
  if (!hasOwn(payload, key)) return fallback
  const value = payload[key]
  if (value == null || value === '') return null
  return isoValue(value, fallback || '') || fallback
}

function hasOwn(payload: Record<string, any>, key: string) {
  return Object.prototype.hasOwnProperty.call(payload, key)
}

function isoValue(value: any, fallback: string) {
  if (value == null || value === '') return fallback
  const parsed = new Date(value).getTime()
  return Number.isFinite(parsed) ? new Date(parsed).toISOString() : fallback
}

function isTerminalQueueState(state: string) {
  return terminalQueueStates.has(String(state || ''))
}

function isRequestScopedDefer(item: QueueItem) {
  return requestScopedDeferScopes.has(String(item.defer_scope || ''))
}

function isRecentRequestScopedDefer(item: QueueItem, lastSeen = 0, now = Date.now()) {
  return isRequestScopedDefer(item) &&
    !isTerminalQueueState(item.state) &&
    lastSeen > 0 &&
    now - lastSeen <= deferredSnapshotGapRetentionMs
}

function dedupeQueueItems(items: QueueItem[]) {
  const seen = new Set<string>()
  return items.filter((item) => {
    const key = item.request_id || item.task_id
    if (!key || seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function userScopedRows(snapshot: CapacitySnapshot | null | undefined) {
  return (snapshot?.models || []).flatMap(model =>
    (model.limit_rows || []).filter(row => row.user_scoped && row.actor_id)
  )
}

function externalRowsStatus(snapshot: CapacitySnapshot | null | undefined) {
  return String(snapshot?.external_rows_status || 'ok')
}

function mergeRecentPreviewUserRows(next: CapacitySnapshot, previous: CapacitySnapshot | null, previousHydratedAt: number) {
  const previousRows = userScopedRows(previous)
  if (!previousRows.length || userScopedRows(next).length) return next
  if (externalRowsStatus(next) === 'ok') return next
  if (Date.now() - previousHydratedAt > previewUserRowStaleGuardMs) return next

  const previousByEndpoint = new Map<string, CapacityLimitRow[]>()
  for (const model of previous?.models || []) {
    const rows = (model.limit_rows || []).filter(row => row.user_scoped && row.actor_id)
    if (rows.length) previousByEndpoint.set(model.endpoint_id, rows)
  }
  if (!previousByEndpoint.size) return next

  return {
    ...next,
    models: next.models.map(model => {
      const rows = previousByEndpoint.get(model.endpoint_id)
      if (!rows?.length) return model
      return {
        ...model,
        limit_rows: [
          ...(model.limit_rows || []).filter(row => !row.user_scoped),
          ...rows
        ]
      }
    })
  }
}

function parsePreviewRequestID(requestID: string) {
  const match = requestID.match(/^live-flow-preview-(.+)-(\d+)-(\d+)$/)
  if (!match) return null
  const generation = Number.parseInt(match[2] || '', 10)
  const sequence = Number.parseInt(match[3] || '', 10)
  if (!Number.isFinite(generation) || !Number.isFinite(sequence)) return null
  return {
    sessionID: match[1] || '',
    generation,
    sequence
  }
}

function previewWebSocketURL(sessionID: string) {
  const path = `/api/preview/live-flow/sessions/${encodeURIComponent(sessionID)}/ws`
  return useRelayURLs().webSocketURL(path)
}

async function deletePreviewSession(sessionID: string, keepalive = false) {
  const path = previewSessionPath(sessionID)
  if (keepalive && process.client) {
    const auth = useAuthStore()
    await fetch(useRelayURLs().apiURL(path), {
      method: 'DELETE',
      credentials: 'include',
      keepalive: true,
      headers: auth.token ? { Authorization: `Bearer ${auth.token}` } : undefined
    })
    return
  }
  await useRelayApi().del(path)
}

function previewSessionPath(sessionID: string) {
  return `/api/preview/live-flow/sessions/${encodeURIComponent(sessionID)}`
}
