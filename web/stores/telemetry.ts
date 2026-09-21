import { defineStore } from 'pinia'
import type { TelemetryEvent } from '~/types/admin'

type LiveRequestProgress = {
  request_id: string
  task_id?: string
  actor_id?: string
  endpoint_id?: string
  endpoint_name?: string
  provider_id?: string
  lane?: string
  incoming_model?: string
  selected_upstream_model?: string
  state?: string
  substatus?: string
  queued_at?: string
  predicted_eligible_at?: string
  resource_eligible_at?: string | null
  user_eligible_at?: string | null
  user_limit_reason?: string
  defer_scope?: string
  defer_reason?: string
  delay_reason?: string
  started_at?: string
  finished_at?: string
  wait_ms?: number
  latency_ms?: number
  characterization_duration_ms?: number
  guardrail_pre_duration_ms?: number
  provider_latency_ms?: number
  guardrail_post_duration_ms?: number
  total_time_ms?: number
  status_code?: number
  uploaded_tokens?: number
  downloaded_tokens?: number
  fallback?: number
  fallback_count?: number
  estimated_input_tokens?: number
  estimated_output_tokens?: number
  actual_input_tokens?: number
  actual_output_tokens?: number
  actual_total_tokens?: number
  estimated_cost_micros?: number
  actual_cost_micros?: number
  updated_at?: string
}

const reconnectDelayMs = 2500
const watchdogPeriodMs = 1000
const websocketSilenceGraceMs = 30000
const fallbackRefreshCooldownMs = 2500
const terminalRequestEventTypes = new Set(['request_finished', 'request_completed', 'request_failed', 'request_cancelled'])
const requestLogUpsertEventTypes = new Set([...terminalRequestEventTypes, 'request_in_flight'])

let recoveryHandlersInstalled = false

export const useTelemetryStore = defineStore('telemetry', {
  state: () => ({
    connected: false,
    reconnecting: false,
    events: [] as TelemetryEvent[],
    requestProgress: {} as Record<string, LiveRequestProgress>,
    requestProgressClearTimers: {} as Record<string, number>,
    socket: null as WebSocket | null,
    retryTimer: null as number | null,
    watchdogTimer: null as number | null,
    connectedAt: 0,
    lastMessageAt: 0,
    lastCapacitySnapshotAt: 0,
    lastCapacityFallbackAt: 0,
    capacityFallbackSuppressed: false,
    limitVisibility: 'mine' as 'mine' | 'organization',
    streamID: '',
    streamSequence: 0,
    shouldReconnect: false
  }),
  actions: {
    setLimitVisibility(visibility: 'mine' | 'organization') {
      const next = visibility === 'organization' ? 'organization' : 'mine'
      useCapacityStore().setLimitVisibility(next)
      if (this.limitVisibility === next) return
      this.limitVisibility = next
      if (process.client && this.shouldReconnect) {
        this.reconnectForScopeChange()
      }
    },
    setCapacityFallbackSuppressed(suppressed: boolean) {
      this.capacityFallbackSuppressed = suppressed
      if (suppressed) {
        this.lastCapacityFallbackAt = Date.now()
      }
    },
    mergeRequestProgress(payload: Record<string, any>) {
      const requestID = payload.request_id as string | undefined
      if (!requestID) return
      const current = this.requestProgress[requestID] || { request_id: requestID }
      const next = {
        ...current,
        ...payload,
        updated_at: new Date().toISOString()
      }
      if (next.state && next.state !== 'in_flight' && next.state !== 'completed' && next.state !== 'failed' && next.state !== 'cancelled') {
        delete next.started_at
        delete next.finished_at
        delete next.status_code
        delete next.substatus
        next.uploaded_tokens = 0
        next.downloaded_tokens = 0
      }
      this.requestProgress[requestID] = next
      const timer = this.requestProgressClearTimers[requestID]
      if (timer && process.client) {
        window.clearTimeout(timer)
        delete this.requestProgressClearTimers[requestID]
      }
    },
    clearRequestProgress(requestID?: string) {
      if (!requestID) return
      const timer = this.requestProgressClearTimers[requestID]
      if (timer && process.client) {
        window.clearTimeout(timer)
        delete this.requestProgressClearTimers[requestID]
      }
      delete this.requestProgress[requestID]
    },
    scheduleRequestProgressClear(requestID?: string, delayMs = 2500) {
      if (!process.client || !requestID) return
      const current = this.requestProgressClearTimers[requestID]
      if (current) {
        window.clearTimeout(current)
      }
      this.requestProgressClearTimers[requestID] = window.setTimeout(() => {
        delete this.requestProgressClearTimers[requestID]
        delete this.requestProgress[requestID]
      }, delayMs)
    },
    clearRetryTimer() {
      if (this.retryTimer && process.client) {
        window.clearTimeout(this.retryTimer)
      }
      this.retryTimer = null
    },
    clearScopedState() {
      this.events = []
      for (const timer of Object.values(this.requestProgressClearTimers)) {
        if (process.client) window.clearTimeout(timer)
      }
      this.requestProgressClearTimers = {}
      this.requestProgress = {}
      useQueueStore().clear()
      useRequestsStore().hydrate([])
    },
    resetStreamCursor() {
      this.streamID = ''
      this.streamSequence = 0
    },
    acceptStreamEnvelope(payload: TelemetryEvent) {
      const streamID = String(payload.stream_id || '')
      const sequence = Number(payload.sequence || 0)
      // Keep compatibility with an older server during a rolling local reload.
      // Once a sequenced frame arrives, every later frame on that connection
      // must belong to the same contiguous stream.
      if (!streamID || !Number.isSafeInteger(sequence) || sequence <= 0) {
        return this.streamID === ''
      }
      if (!this.streamID) {
        // Scoped streams share a topic cursor. A new connection begins at the
        // cursor carried by its authoritative baseline, which may be greater
        // than one when the scope already has connected viewers.
        this.streamID = streamID
        this.streamSequence = sequence
        return true
      }
      if (streamID !== this.streamID || sequence !== this.streamSequence + 1) {
        return false
      }
      this.streamSequence = sequence
      return true
    },
    async refreshScopedState() {
      const requests = useRequestsStore()
      const permissions = useModelRelayPermissions()
      const canReadUsage = permissions.hasAny(['relay:usage:read:self', 'relay:usage:read:organization'])
      if (!canReadUsage) {
        requests.hydrate([])
        await Promise.allSettled([
          useQueueStore().refresh(),
          useCapacityStore().refresh()
        ])
        return
      }
      await Promise.allSettled([
        requests.refreshRecentActivity({ limit: Math.max(requests.limit || 0, 24) }),
        useQueueStore().refresh(),
        useCapacityStore().refresh()
      ])
    },
    reconnectForScopeChange() {
      if (!process.client) return
      this.clearScopedState()
      this.shouldReconnect = true
      this.clearRetryTimer()
      const current = this.socket
      if (current) {
        current.onopen = null
        current.onmessage = null
        current.onclose = null
        current.onerror = null
      }
      this.socket = null
      this.connected = false
      this.reconnecting = true
      this.resetStreamCursor()
      useCapacityStore().markStale()
      if (current) {
        try {
          current.close()
        } catch {
          // Browser websocket close can throw after a low-level fault.
        }
      }
      this.connect()
      void this.refreshScopedState()
    },
    scheduleReconnect(delayMs = reconnectDelayMs) {
      if (!process.client || this.retryTimer || !this.shouldReconnect) return
      this.retryTimer = window.setTimeout(() => {
        this.retryTimer = null
        this.connect()
      }, delayMs)
    },
    startWatchdog() {
      if (!process.client || this.watchdogTimer) return
      this.watchdogTimer = window.setInterval(() => {
        this.checkWatchdog()
      }, watchdogPeriodMs)
    },
    stopWatchdog() {
      if (this.watchdogTimer && process.client) {
        window.clearInterval(this.watchdogTimer)
      }
      this.watchdogTimer = null
    },
    installBrowserRecoveryHandlers() {
      if (!process.client || recoveryHandlersInstalled) return
      window.addEventListener('online', handleBrowserRecovery)
      document.addEventListener('visibilitychange', handleBrowserRecovery)
      recoveryHandlersInstalled = true
    },
    removeBrowserRecoveryHandlers() {
      if (!process.client || !recoveryHandlersInstalled) return
      window.removeEventListener('online', handleBrowserRecovery)
      document.removeEventListener('visibilitychange', handleBrowserRecovery)
      recoveryHandlersInstalled = false
    },
    async refreshCapacityFallback(force = false) {
      if (!process.client) return
      if (this.capacityFallbackSuppressed) return
      const now = Date.now()
      if (!force && now - this.lastCapacityFallbackAt < fallbackRefreshCooldownMs) return
      this.lastCapacityFallbackAt = now
      const capacity = useCapacityStore()
      await capacity.refresh()
      if (capacity.isFresh(Date.now())) {
        this.lastCapacitySnapshotAt = capacity.lastHydratedAt
      }
    },
    checkWatchdog() {
      if (!process.client || !this.connected) return
      const now = Date.now()
      if (this.lastMessageAt > 0 && now - this.lastMessageAt > websocketSilenceGraceMs) {
        this.handleSocketFault(this.socket)
      }
    },
    handleSocketFault(socket?: WebSocket | null) {
      if (!process.client) return
      if (socket && this.socket && socket !== this.socket) return
      const current = socket || this.socket
      this.connected = false
      this.socket = null
      this.stopWatchdog()
      this.resetStreamCursor()
      useCapacityStore().markStale()
      if (current) {
        current.onopen = null
        current.onmessage = null
        current.onclose = null
        current.onerror = null
        try {
          current.close()
        } catch {
          // Browser websocket close can throw after a low-level fault.
        }
      }
      if (!this.shouldReconnect) {
        this.reconnecting = false
        return
      }
      this.reconnecting = true
      this.scheduleReconnect()
    },
    resyncCapacity() {
      if (!process.client) return
      if (!this.socket && this.shouldReconnect) {
        this.connect()
      }
      if (this.capacityFallbackSuppressed) return
      void this.refreshCapacityFallback(true)
    },
    connect() {
      if (!process.client) return
      if (this.socket && this.socket.readyState !== WebSocket.CLOSED) return
      if (this.socket) this.socket = null
      this.shouldReconnect = true
      this.clearRetryTimer()
      this.installBrowserRecoveryHandlers()
      const url = relayWebSocketURL(this.limitVisibility)
      this.reconnecting = this.events.length > 0
      let socket: WebSocket
      try {
        socket = new WebSocket(url)
      } catch {
        this.connected = false
        this.socket = null
        this.reconnecting = true
        useCapacityStore().markStale()
        if (this.shouldReconnect) {
          this.scheduleReconnect()
        }
        return
      }
      this.socket = socket
      socket.onopen = () => {
        const now = Date.now()
        this.resetStreamCursor()
        this.connected = true
        this.reconnecting = false
        this.connectedAt = now
        this.lastMessageAt = now
        this.startWatchdog()
      }
      socket.onmessage = async (event) => {
        let payload: TelemetryEvent
        try {
          payload = JSON.parse(event.data) as TelemetryEvent
        } catch {
          useCapacityStore().markSnapshotError('realtime_invalid_event')
          this.handleSocketFault(socket)
          return
        }
        const receivedAt = Date.now()
        this.lastMessageAt = receivedAt
        // Connection-local controls do not advance a shared scope cursor.
        if (payload.type === 'connection_heartbeat') return
        if (payload.type === 'resync_required') {
          useCapacityStore().markSnapshotError(String(payload.payload?.reason || 'realtime_resync_required'))
          this.handleSocketFault(socket)
          return
        }
        if (payload.type === 'capacity_snapshot_error') {
          useCapacityStore().markSnapshotError(String(payload.payload?.reason || 'capacity_snapshot_unavailable'))
          if (payload.payload?.reconnect === true) {
            this.handleSocketFault(socket)
          } else {
            void this.refreshCapacityFallback()
          }
          return
        }
        if (!this.acceptStreamEnvelope(payload)) {
          useCapacityStore().markSnapshotError('realtime_sequence_gap')
          this.handleSocketFault(socket)
          return
        }
        this.events.unshift(summarizeTelemetryEvent(payload))
        this.events = this.events.slice(0, 60)
        const queue = useQueueStore()
        const requests = useRequestsStore()
        const catalog = useCatalogStore()
        const capacity = useCapacityStore()
        if (payload.type === 'capacity_snapshot' || payload.type === 'realtime_snapshot') {
          if (capacity.hydrate(payload.payload as any)) {
            this.lastCapacitySnapshotAt = capacity.lastHydratedAt || receivedAt
            queue.hydrateSnapshot(payload.payload as any)
          }
          const externalRowsStatus = String(payload.payload?.external_rows_status || 'ok')
          if (externalRowsStatus !== 'ok') {
            void this.refreshCapacityFallback()
          }
          return
        }
        if (payload.type.startsWith('request_') && payload.payload?.request_id) {
          this.mergeRequestProgress(payload.payload)
          queue.mergeFromTelemetry(payload.payload)
        }
        if (requestLogUpsertEventTypes.has(payload.type)) {
          requests.mergeFromTelemetry(payload.payload)
        }
        if (terminalRequestEventTypes.has(payload.type)) {
          this.scheduleRequestProgressClear(payload.payload.request_id as string | undefined)
        }
        if (payload.type === 'request_log' && payload.payload?.request_id) {
          requests.mergeFromTelemetry(payload.payload)
        }
        if (payload.type === 'capacity_limit_state') {
          capacity.applyLimitState(payload.payload)
        }
        if (payload.type === 'observed_limit_learned' || payload.type === 'endpoint_health_change') {
          if (payload.type === 'endpoint_health_change') {
            catalog.applyEndpointHealthChange(payload.payload)
            capacity.applyEndpointHealthChange(payload.payload)
          }
          if (payload.type === 'observed_limit_learned') {
            catalog.applyObservedLimit(payload.payload)
          }
        }
        if (payload.type === 'limit_policy_changed' || payload.type === 'limit_policy_deleted') {
          catalog.applyLimitPolicyChange(payload.payload)
        }
      }
      socket.onclose = () => {
        if (this.socket && this.socket !== socket) return
        this.connected = false
        this.socket = null
        this.stopWatchdog()
        this.resetStreamCursor()
        useCapacityStore().markStale()
        if (!this.shouldReconnect) {
          this.reconnecting = false
          return
        }
        this.reconnecting = true
        this.scheduleReconnect()
      }
      socket.onerror = () => {
        this.handleSocketFault(socket)
      }
    },
    disconnect() {
      this.shouldReconnect = false
      this.clearRetryTimer()
      this.stopWatchdog()
      this.removeBrowserRecoveryHandlers()
      for (const timer of Object.values(this.requestProgressClearTimers)) {
        if (process.client) window.clearTimeout(timer)
      }
      this.requestProgressClearTimers = {}
      this.requestProgress = {}
      this.socket?.close()
      this.socket = null
      this.connected = false
      this.reconnecting = false
      this.connectedAt = 0
      this.lastMessageAt = 0
      this.lastCapacitySnapshotAt = 0
      this.resetStreamCursor()
    }
  }
})

function handleBrowserRecovery() {
  if (!process.client) return
  if (document.visibilityState && document.visibilityState !== 'visible') return
  useTelemetryStore().resyncCapacity()
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
      lane_id: payload.lane_id,
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

function relayWebSocketURL(limitVisibility: 'mine' | 'organization' = 'mine') {
  const url = new URL(useRelayURLs().webSocketURL('/api/ws'))
  url.searchParams.set('limit_visibility', limitVisibility)
  return url.toString()
}
