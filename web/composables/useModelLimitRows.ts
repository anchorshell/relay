import type { Endpoint, Metric } from '~/types/admin'

export type ModelLimitCardRow = {
  key: string
  label: string
  metric?: Metric
  configured: number
  used: number
  percent: number
  blocked?: boolean
  blockedUntil?: string
  blockedRemainingMs?: number
}

export type ModelLimitRowsRefreshOptions = {
  force?: boolean
}

export type ModelLimitRowsContext = {
  mode?: 'live' | 'preview'
  nowMs?: number
}

export function useModelLimitRows() {
  async function refreshModelLimitRows(_options: ModelLimitRowsRefreshOptions = {}) {}

  function scheduleModelLimitRowsRefresh(_delayMs = 0) {}

  function extraModelLimitRows(_endpoint: Endpoint, _context: ModelLimitRowsContext = {}): ModelLimitCardRow[] {
    return []
  }

  function hasSelfModelLimitRows(_endpoint: Endpoint) {
    return false
  }

  function isSelfModelLimitQueueItem(_item: { actor_id?: string | null }) {
    return false
  }

  return {
    refreshModelLimitRows,
    scheduleModelLimitRowsRefresh,
    extraModelLimitRows,
    hasSelfModelLimitRows,
    isSelfModelLimitQueueItem
  }
}
