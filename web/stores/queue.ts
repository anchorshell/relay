import { defineStore } from 'pinia'
import type { CapacitySnapshot, QueueItem, QueueSnapshot } from '~/types/admin'

const terminalQueueStates = new Set(['completed', 'failed', 'cancelled'])
const requestScopedDeferScopes = new Set(['user', 'user_model', 'user_provider', 'api_key', 'api_key_model', 'api_key_provider'])
const deferredSnapshotGapRetentionMs = 3500

export const useQueueStore = defineStore('queue', {
  state: () => ({
    items: [] as QueueItem[],
    queueSnapshot: null as QueueSnapshot | null,
    lastSeenByRequestID: {} as Record<string, number>,
    loading: false,
    refreshSeq: 0,
    lastError: ''
  }),
  getters: {
    byState: state => (stateName: string) => stateName === 'all' ? state.items : state.items.filter(item => item.state === stateName),
    queuedCount: state => state.queueSnapshot
      ? queueStateCount(state.queueSnapshot, ['queued', 'waiting', 'ready'])
      : state.items.filter(item => ['queued', 'waiting', 'ready'].includes(item.state)).length,
    activeCount: state => state.queueSnapshot
      ? queueStateCount(state.queueSnapshot, ['in_flight'])
      : state.items.filter(item => item.state === 'in_flight').length,
    oldestAgeMs: state => state.items.reduce((max, item) => Math.max(max, item.wait_ms), 0),
    averageWaitMs: state => state.items.length ? Math.round(state.items.reduce((sum, item) => sum + item.wait_ms, 0) / state.items.length) : 0
  },
  actions: {
    clear() {
      this.items = []
      this.queueSnapshot = null
      this.lastSeenByRequestID = {}
      this.lastError = ''
    },
    hydrateSnapshot(snapshot?: CapacitySnapshot | null) {
      if (!snapshot) return
      this.queueSnapshot = snapshot.queue || null
      this.hydrate(snapshot.queue_items)
    },
    hydrate(items?: QueueItem[]) {
      if (!Array.isArray(items)) return
      const now = Date.now()
      const existingByRequestID = new Map(this.items.map(item => [item.request_id, item]))
      const snapshotRequestIDs = new Set<string>()
      const nextItems: QueueItem[] = []

      for (const rawItem of items) {
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

      const retained = this.items.filter((item) => {
        if (snapshotRequestIDs.has(item.request_id) || !isRecentRequestScopedDefer(item, this.lastSeenByRequestID[item.request_id], now)) {
          return false
        }
        return true
      })

      this.items = dedupeQueueItems([...nextItems, ...retained])
      this.pruneSeenRequestIDs(now)
    },
    mergeFromTelemetry(payload: Record<string, any>) {
      const requestID = String(payload.request_id || '')
      const index = this.items.findIndex(item => item.request_id === requestID || item.task_id === String(payload.task_id || ''))
      const existing = index >= 0 ? this.items[index] : null
      const state = String(payload.state || payload.task_state || existing?.state || '')
      if (!requestID || !state) return

      this.queueSnapshot = null

      if (isTerminalQueueState(state)) {
        if (index >= 0) {
          this.items.splice(index, 1)
        }
        delete this.lastSeenByRequestID[requestID]
        return
      }

      const next = normalizeQueueItem(payload, existing)
      if (!next) return
      this.lastSeenByRequestID[next.request_id] = Date.now()

      if (index >= 0) {
        this.items.splice(index, 1, next)
        return
      }

      this.items.unshift(next)
    },
    pruneSeenRequestIDs(now = Date.now()) {
      const activeRequestIDs = new Set(this.items.map(item => item.request_id))
      for (const [requestID, lastSeen] of Object.entries(this.lastSeenByRequestID)) {
        if (activeRequestIDs.has(requestID)) continue
        if (now - lastSeen > deferredSnapshotGapRetentionMs) {
          delete this.lastSeenByRequestID[requestID]
        }
      }
    },
    async refresh() {
      const refreshSeq = ++this.refreshSeq
      this.loading = true
      this.lastError = ''
      try {
        const items = await useRelayApi().queueItems()
        if (refreshSeq !== this.refreshSeq) return
        this.hydrate(items)
      } catch (error: any) {
        if (refreshSeq !== this.refreshSeq) return
        this.lastError = error?.data?.message || error?.message || 'Unable to load queue items.'
      } finally {
        if (refreshSeq === this.refreshSeq) {
          this.loading = false
        }
      }
    },
    async cancel(taskId: string) {
      await useRelayApi().cancelQueueItem(taskId)
      await this.refresh()
    },
    async remove(taskId: string) {
      await useRelayApi().deleteQueueItem(taskId)
      await this.refresh()
    }
  }
})

function queueStateCount(snapshot: QueueSnapshot | null, states: string[]) {
  if (!snapshot?.states) return 0
  return states.reduce((sum, state) => sum + Number(snapshot.states[state] || 0), 0)
}

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
    api_key_uuid: stringField(payload, 'api_key_uuid', existing?.api_key_uuid || ''),
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
