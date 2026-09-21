import { defineStore } from 'pinia'
import type { CapacityLimitRow, CapacitySnapshot, ModelCapacitySnapshot } from '~/types/admin'

const userRowStaleGuardMs = 30000
const userRowBoundaryGraceMs = 25
const userRowBoundaryRetryMs = 1000
let userRowBoundaryTimer: ReturnType<typeof window.setTimeout> | null = null

function userScopedRows(snapshot: CapacitySnapshot | null | undefined) {
  return (snapshot?.models || []).flatMap(model =>
    (model.limit_rows || []).filter(row => row.user_scoped && row.actor_id)
  )
}

function externalRowsStatus(snapshot: CapacitySnapshot | null | undefined) {
  return String(snapshot?.external_rows_status || 'ok')
}

function nextUserRowBoundary(snapshot: CapacitySnapshot | null | undefined, now = Date.now()) {
  let earliest = Number.POSITIVE_INFINITY
  let hasUsedRowPastBoundary = false
  for (const row of userScopedRows(snapshot)) {
    if (Number(row.used || 0) + Number(row.reserved || 0) <= 0 || !row.reset_at) continue
    const resetAt = new Date(row.reset_at).getTime()
    if (!Number.isFinite(resetAt)) continue
    if (resetAt <= now) {
      hasUsedRowPastBoundary = true
      continue
    }
    if (resetAt < earliest) earliest = resetAt
  }
  if (hasUsedRowPastBoundary) return now + userRowBoundaryRetryMs
  return Number.isFinite(earliest) ? earliest + userRowBoundaryGraceMs : 0
}

function sameLimitRow(left: CapacityLimitRow, right: CapacityLimitRow) {
  if (left.key && right.key && left.key === right.key) return true
  return left.scope_type === right.scope_type &&
    String(left.scope_id || '') === String(right.scope_id || '') &&
    left.metric === right.metric &&
    left.period === right.period &&
    String(left.actor_id || '') === String(right.actor_id || '')
}

function resourceRowIsBlocked(row: CapacityLimitRow) {
  if (row.user_scoped) return false
  if (row.blocked) return true
  const effective = Number(row.effective || row.configured || 0)
  return effective > 0 && Number(row.used || 0) + Number(row.reserved || 0) >= effective
}

function capacityStateForModel(model: ModelCapacitySnapshot): ModelCapacitySnapshot['capacity_state'] {
  if (model.capacity_state === 'unavailable') return 'unavailable'
  if (model.health_status === 'unhealthy') return 'unhealthy'
  if ((model.limit_rows || []).some(resourceRowIsBlocked)) return 'rate-limited'
  switch (String(model.health_status || '')) {
    case 'rate_limited': return 'rate-limited'
    case 'cooling_down': return 'cooling-down'
  }
  if (model.cooldown_until && new Date(model.cooldown_until).getTime() > Date.now()) return 'cooling-down'
  return 'healthy'
}

function resourceLimitCooldownUntil(rows: CapacityLimitRow[]) {
  let latest = 0
  for (const row of rows) {
    if (!resourceRowIsBlocked(row)) continue
    const raw = row.blocked_until || row.reset_at
    const timestamp = raw ? new Date(raw).getTime() : 0
    if (Number.isFinite(timestamp) && timestamp > latest) latest = timestamp
  }
  return latest > 0 ? new Date(latest).toISOString() : null
}

function configuredLimitStateCanBeCleared(model: ModelCapacitySnapshot) {
  const statusCode = Number(model.cooldown_status_code || 0)
  const reason = String(model.cooldown_reason || '')
  if (statusCode !== 0) return false
  if (reason && reason !== 'configured_limit') return false
  return model.health_status !== 'unhealthy'
}

function normalizeModelCapacity(model: ModelCapacitySnapshot): ModelCapacitySnapshot {
  const resourceCooldownUntil = resourceLimitCooldownUntil(model.limit_rows || [])
  const currentCooldownAt = model.cooldown_until ? new Date(model.cooldown_until).getTime() : 0
  const resourceCooldownAt = resourceCooldownUntil ? new Date(resourceCooldownUntil).getTime() : 0
  const next: ModelCapacitySnapshot = {
    ...model,
    cooldown_until: resourceCooldownAt > currentCooldownAt ? resourceCooldownUntil : (model.cooldown_until || null)
  }
  next.capacity_state = capacityStateForModel(next)
  return next
}

function mergeRecentUserRows(next: CapacitySnapshot, previous: CapacitySnapshot | null, previousHydratedAt: number) {
  const previousRows = userScopedRows(previous)
  if (!previousRows.length || userScopedRows(next).length) return next
  if (externalRowsStatus(next) === 'ok') return next
  if (Date.now() - previousHydratedAt > userRowStaleGuardMs) return next

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

export const useCapacityStore = defineStore('capacity', {
  state: () => ({
    snapshot: null as CapacitySnapshot | null,
    stale: false,
    loading: false,
    lastHydratedAt: 0,
    lastExternalRowsAt: 0,
    externalRowsStatus: 'ok',
    lastErrorAt: 0,
    lastErrorReason: '',
    limitVisibility: 'mine' as 'mine' | 'organization'
  }),
  getters: {
    modelsByEndpoint: state => {
      const out: Record<string, ModelCapacitySnapshot> = {}
      for (const model of state.snapshot?.models || []) {
        if (model.endpoint_id) out[model.endpoint_id] = model
      }
      return out
    },
    userRows: state => (state.snapshot?.models || []).flatMap(model =>
      (model.limit_rows || []).filter(row => row.user_scoped)
    ),
    selfActorID(): string {
      const row = this.userRows.find(item => item.actor_id)
      return row?.actor_id || ''
    }
  },
  actions: {
    setLimitVisibility(visibility: 'mine' | 'organization') {
      this.limitVisibility = visibility === 'organization' ? 'organization' : 'mine'
    },
    scheduleUserRowBoundaryRefresh() {
      if (!process.client) return
      if (userRowBoundaryTimer) {
        window.clearTimeout(userRowBoundaryTimer)
        userRowBoundaryTimer = null
      }
      const refreshAt = nextUserRowBoundary(this.snapshot)
      if (!refreshAt) return
      userRowBoundaryTimer = window.setTimeout(async () => {
        userRowBoundaryTimer = null
        const refreshed = await this.refresh()
        if (!refreshed) {
          userRowBoundaryTimer = window.setTimeout(() => {
            userRowBoundaryTimer = null
            this.scheduleUserRowBoundaryRefresh()
          }, userRowBoundaryRetryMs)
        }
      }, Math.max(0, refreshAt - Date.now()))
    },
    hydrate(snapshot: CapacitySnapshot | null | undefined) {
      if (!snapshot || !Array.isArray(snapshot.models)) return false
      const nextSequence = Number(snapshot.sequence || 0)
      const currentSequence = Number(this.snapshot?.sequence || 0)
      const nextGenerated = new Date(snapshot.generated_at || '').getTime()
      const currentGenerated = new Date(this.snapshot?.generated_at || '').getTime()
      if (this.snapshot && nextSequence > 0 && currentSequence > 0 && nextSequence < currentSequence && (!nextGenerated || !currentGenerated || nextGenerated <= currentGenerated)) return false
      if (this.snapshot && nextSequence === currentSequence && nextGenerated > 0 && currentGenerated > 0 && nextGenerated < currentGenerated) return false
      const status = externalRowsStatus(snapshot)
      const hasUserRows = userScopedRows(snapshot).length > 0
      const merged = mergeRecentUserRows(snapshot, this.snapshot, this.lastHydratedAt)
      this.snapshot = {
        ...merged,
        models: merged.models.map(normalizeModelCapacity)
      }
      this.externalRowsStatus = status
      if (status === 'ok' || hasUserRows) {
        this.lastExternalRowsAt = Date.now()
      }
      this.stale = false
      this.lastHydratedAt = Date.now()
      this.lastErrorReason = status === 'ok' ? '' : `capacity_external_rows_${status}`
      this.scheduleUserRowBoundaryRefresh()
      return true
    },
    async refresh() {
      if (this.loading) return
      this.loading = true
      try {
        const snapshot = await useRelayApi().get<CapacitySnapshot>(`/api/capacity-snapshot?limit_visibility=${encodeURIComponent(this.limitVisibility)}`)
        const hydrated = this.hydrate(snapshot)
        if (hydrated) {
          useQueueStore().hydrateSnapshot(snapshot)
        }
        return hydrated
      } catch {
        this.stale = true
        this.lastErrorAt = Date.now()
        this.lastErrorReason = 'capacity_snapshot_refresh_failed'
        return false
      } finally {
        this.loading = false
      }
    },
    markStale() {
      this.stale = true
    },
    markSnapshotError(reason = 'capacity_snapshot_unavailable') {
      this.stale = true
      this.lastErrorAt = Date.now()
      this.lastErrorReason = reason
    },
    applyLimitState(payload: Record<string, any>) {
      const endpointID = String(payload.endpoint_id || '')
      const incomingRows = (Array.isArray(payload.rows) ? payload.rows : [payload.row]) as Array<CapacityLimitRow | undefined>
      const validRows = incomingRows.filter((row): row is CapacityLimitRow => Boolean(row?.scope_type && row?.scope_id && row?.metric && row?.period))
      const removedKeys = (Array.isArray(payload.removed_keys) ? payload.removed_keys : [])
        .map((key: unknown) => String(key || '').trim())
        .filter(Boolean)
      if (!endpointID || (!validRows.length && !removedKeys.length)) return false
      if (!this.snapshot) return false

      let applied = false
      const models = this.snapshot.models.map((current) => {
        const currentRows = current.limit_rows || []
        const rows = currentRows.filter(row => !removedKeys.includes(String(row.key || '')))
        let modelChanged = rows.length !== currentRows.length
        for (const incoming of validRows) {
          const rowIndex = rows.findIndex(row => sameLimitRow(row, incoming))
          if (rowIndex < 0 && current.endpoint_id !== endpointID) continue
          const nextRow = rowIndex >= 0 ? { ...rows[rowIndex], ...incoming } : incoming
          if (rowIndex >= 0) rows.splice(rowIndex, 1, nextRow)
          else rows.push(nextRow)
          modelChanged = true
        }
        if (!modelChanged) return current
        const nextModel = { ...current, limit_rows: rows }
        const resourceRows = rows.filter(row => !row.user_scoped)
        const resourceBlocked = resourceRows.some(resourceRowIsBlocked)
        if (resourceBlocked) {
          // A capacity delta is authoritative for the same state rendered by
          // the model card. Promote its next release boundary immediately so
          // the live view shows a countdown without waiting for a replacement
          // snapshot or a separately ordered health event.
          nextModel.health_status = payload.health_status || 'rate_limited'
          const resourceCooldownUntil = resourceLimitCooldownUntil(resourceRows)
          // Policy edits publish an endpoint-health delta and a capacity-row
          // delta separately. Do not let a row delta without a boundary erase
          // the timer supplied by the immediately preceding health delta.
          nextModel.cooldown_until = payload.cooldown_until
            ? String(payload.cooldown_until)
            : (resourceCooldownUntil || current.cooldown_until || null)
          nextModel.cooldown_reason = String(payload.cooldown_reason || 'configured_limit')
          nextModel.cooldown_status_code = Number(payload.cooldown_status_code || 0)
        } else if (configuredLimitStateCanBeCleared(current)) {
          // The merged rows are authoritative after a limit edit. Clear an old
          // configured-limit timer even when an earlier health delta has
          // already changed the model back to healthy. This covers both
          // rate_limited and cooling_down transitions without touching an
          // upstream 429/5xx cooldown.
          if (current.health_status === 'rate_limited' || current.health_status === 'cooling_down') {
            nextModel.health_status = 'healthy'
          }
          nextModel.cooldown_until = null
          nextModel.cooldown_reason = ''
          nextModel.cooldown_status_code = 0
        }
        applied = true
        return normalizeModelCapacity(nextModel)
      })
      if (!applied) return false
      // Publish the delta as one new snapshot identity. Realtime cards derive
      // their progress rows through modelsByEndpoint, so every consumer sees
      // the updated values and percentage as one coherent state transition.
      this.snapshot = { ...this.snapshot, models }
      this.stale = false
      if (validRows.some(row => row.user_scoped)) this.lastExternalRowsAt = Date.now()
      this.scheduleUserRowBoundaryRefresh()
      return true
    },
    applyEndpointHealthChange(payload: Record<string, any>) {
      const endpointID = String(payload.endpoint_id || '')
      const modelIndex = this.snapshot?.models.findIndex(model => model.endpoint_id === endpointID) ?? -1
      if (!this.snapshot || !endpointID || modelIndex < 0) return false
      const current = this.snapshot.models[modelIndex]
      const next = {
        ...current,
        health_status: payload.health_status || current.health_status,
        cooldown_until: Object.prototype.hasOwnProperty.call(payload, 'cooldown_until')
          ? (payload.cooldown_until ? String(payload.cooldown_until) : null)
          : current.cooldown_until,
        cooldown_reason: Object.prototype.hasOwnProperty.call(payload, 'cooldown_reason')
          ? String(payload.cooldown_reason || '')
          : current.cooldown_reason,
        cooldown_status_code: Object.prototype.hasOwnProperty.call(payload, 'cooldown_status_code')
          ? Number(payload.cooldown_status_code || 0)
          : current.cooldown_status_code
      } as ModelCapacitySnapshot
      const normalized = normalizeModelCapacity(next)
      const models = [...this.snapshot.models]
      models.splice(modelIndex, 1, normalized)
      this.snapshot = { ...this.snapshot, models }
      this.stale = false
      return true
    },
    isFresh(_now = Date.now(), _maxAgeMs = Number.POSITIVE_INFINITY) {
      return Boolean(this.snapshot && !this.stale && this.lastHydratedAt > 0)
    },
    model(endpointID: string) {
      return this.modelsByEndpoint[endpointID] || null
    },
    rowsForEndpoint(endpointID: string) {
      return this.model(endpointID)?.limit_rows || []
    },
    userRowsForEndpoint(endpointID: string) {
      return this.rowsForEndpoint(endpointID).filter(row => row.user_scoped)
    },
    rowValue(scopeType: string, scopeID: string | number, metric: string, period: string) {
      const id = String(scopeID)
      for (const model of this.snapshot?.models || []) {
        const row = (model.limit_rows || []).find(item =>
          item.scope_type === scopeType &&
          item.scope_id === id &&
          item.metric === metric &&
          item.period === period
        )
        if (row) return Number(row.used || 0)
      }
      return 0
    }
  }
})

export function capacityRowToModelLimit(row: CapacityLimitRow) {
  const blockedUntil = row.blocked_until || row.reset_at || ''
  return {
    key: row.key,
    label: row.label,
    metric: row.metric,
    configured: Number(row.effective || row.configured || 0),
    used: Number(row.used || 0) + Number(row.reserved || 0),
    percent: Math.max(0, Math.min(100, Number(row.percent || 0))),
    blocked: Boolean(row.blocked),
    blockedUntil,
    blockedRemainingMs: blockedUntil ? Math.max(0, new Date(blockedUntil).getTime() - Date.now()) : 0
  }
}
