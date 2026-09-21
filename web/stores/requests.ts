import { defineStore } from 'pinia'
import type { RecentFlowRequest, RequestLog, RequestLogPage } from '~/types/admin'

const DEFAULT_LOG_PAGE_LIMIT = 50
const RECENT_LOG_LIMIT = 24
type RequestLogFilters = { userUUIDs?: string[], primaryAction?: string, domain?: string, guardrailStatus?: string }

export const useRequestsStore = defineStore('requests', {
  state: () => ({
    logs: [] as RequestLog[],
    selected: null as RequestLog | null,
    loading: false,
    refreshSeq: 0,
    total: 0,
    limit: DEFAULT_LOG_PAGE_LIMIT,
    offset: 0,
    hasMore: false,
    nextOffset: 0,
    userUUIDs: [] as string[],
    primaryAction: '',
    domain: '',
    guardrailStatus: '',
    lastError: ''
  }),
  getters: {
    recentFailures: state => state.logs.filter(item => item.task_state === 'failed').slice(0, 12),
    previousOffset: state => Math.max(0, state.offset - state.limit)
  },
  actions: {
    hydrate(logs?: RequestLog[]) {
      if (!Array.isArray(logs)) return
      this.logs = logs
      this.total = logs.length
      this.offset = 0
      this.hasMore = false
      this.nextOffset = logs.length
      this.userUUIDs = []
      this.primaryAction = ''
      this.domain = ''
      this.guardrailStatus = ''
    },
    hydratePage(page: RequestLogPage) {
      this.logs = Array.isArray(page.items) ? page.items : []
      this.total = Number(page.total || 0)
      this.limit = Number(page.limit || DEFAULT_LOG_PAGE_LIMIT)
      this.offset = Number(page.offset || 0)
      this.hasMore = Boolean(page.has_more)
      this.nextOffset = Number(page.next_offset || (this.offset + this.logs.length))
    },
    hydrateRecentActivity(items?: RecentFlowRequest[], limit = RECENT_LOG_LIMIT) {
      const logs = Array.isArray(items) ? items.map(recentFlowRequestToLog) : []
      this.logs = logs
      this.total = logs.length
      this.limit = limit
      this.offset = 0
      this.hasMore = false
      this.nextOffset = logs.length
      this.userUUIDs = []
    },
    mergeFromTelemetry(payload: Record<string, any>) {
      const requestID = String(payload.request_id || '')
      if (!requestID) return
      const index = this.logs.findIndex(item => item.request_id === requestID)
      const existing = index >= 0 ? this.logs[index] : null
      const actorID = String(payload.actor_id || existing?.actor_id || '').trim().toLowerCase()
      if (this.userUUIDs.length > 0 && !this.userUUIDs.includes(actorID)) return
      let realtimeCharacterization = payload.characterization && typeof payload.characterization === 'object'
        ? payload.characterization
        : null
      // Deferred terminal events may arrive after classification enrichment.
      // Never replace a finished classification with an older pending snapshot.
      if (realtimeCharacterization?.classifier_status === 'pending' && existing?.characterization_json) {
        try {
          const previous = JSON.parse(existing.characterization_json)
          if (previous.classifier_status && previous.classifier_status !== 'pending') realtimeCharacterization = previous
        } catch { /* Preserve normal handling for legacy malformed records. */ }
      }
      const primaryAction = String(realtimeCharacterization?.primary_action || payload.primary_action || existing?.primary_action || '')
      const characterizationJSON = realtimeCharacterization
        ? JSON.stringify(realtimeCharacterization)
        : String(existing?.characterization_json || '')
      if (this.primaryAction && primaryAction !== this.primaryAction) return
      if (this.domain && !characterizationHasDomain(characterizationJSON, this.domain)) return
      const actualInputTokens = numberFromTelemetry(payload.actual_input_tokens, existing?.actual_input_tokens)
      const actualOutputTokens = numberFromTelemetry(payload.actual_output_tokens, existing?.actual_output_tokens)
      const explicitActualTotalTokens = numberFromTelemetry(payload.actual_total_tokens, existing?.actual_total_tokens)
      const actualTotalTokens = explicitActualTotalTokens || (actualInputTokens || actualOutputTokens ? actualInputTokens + actualOutputTokens : 0)
      const next: RequestLog = {
        id: String(payload.id || existing?.id || requestID),
        request_id: requestID,
        actor_id: String(payload.actor_id || existing?.actor_id || ''),
        api_key_uuid: String(payload.api_key_uuid || existing?.api_key_uuid || ''),
        parent_request_id: String(payload.parent_request_id || existing?.parent_request_id || ''),
        lane_id: stringOrExisting(payload.lane_id, existing?.lane_id),
        endpoint_id: stringOrExisting(payload.endpoint_id, existing?.endpoint_id),
        provider_id: stringOrExisting(payload.provider_id, existing?.provider_id),
        route_kind: (payload.route_kind || existing?.route_kind || 'chat') as RequestLog['route_kind'],
        incoming_model: String(payload.incoming_model || existing?.incoming_model || ''),
        selected_upstream_model: String(payload.selected_upstream_model || existing?.selected_upstream_model || payload.endpoint_name || ''),
        status_code: numberFromTelemetry(payload.status_code, existing?.status_code),
        task_state: String(payload.task_state || payload.state || existing?.task_state || ''),
        queued_at: stringOrExisting(payload.queued_at, existing?.queued_at),
        started_at: stringOrExisting(payload.started_at, existing?.started_at),
        finished_at: stringOrExisting(payload.finished_at, existing?.finished_at),
        wait_ms: numberFromTelemetry(payload.wait_ms, existing?.wait_ms),
        latency_ms: numberFromTelemetry(payload.latency_ms, existing?.latency_ms),
        streaming: payload.streaming == null ? Boolean(existing?.streaming) : Boolean(payload.streaming),
        priority: numberFromTelemetry(payload.priority, existing?.priority),
        fallback_count: numberFromTelemetry(payload.fallback_count, payload.fallback, existing?.fallback_count),
        estimated_input_tokens: numberFromTelemetry(payload.estimated_input_tokens, existing?.estimated_input_tokens),
        estimated_output_tokens: numberFromTelemetry(payload.estimated_output_tokens, existing?.estimated_output_tokens),
        actual_input_tokens: actualInputTokens,
        actual_output_tokens: actualOutputTokens,
        actual_total_tokens: actualTotalTokens,
        estimated_cost_micros: numberFromTelemetry(payload.estimated_cost_micros, existing?.estimated_cost_micros),
        actual_cost_micros: numberFromTelemetry(payload.actual_cost_micros, existing?.actual_cost_micros),
        error_text: String(payload.error_text || existing?.error_text || ''),
        request_bodies_stored: payload.request_bodies_stored == null
          ? Boolean(existing?.request_bodies_stored)
          : Boolean(payload.request_bodies_stored),
        primary_action: primaryAction || null,
        action_confidence: nullableNumber(payload.action_confidence, existing?.action_confidence),
        characterization_version: stringOrExisting(payload.characterization_version, existing?.characterization_version),
        characterization_json: characterizationJSON || null,
        characterization_duration_ms: numberFromTelemetry(realtimeCharacterization?.classification_duration_ms, payload.characterization_duration_ms, existing?.characterization_duration_ms),
        guardrail_status: String(payload.guardrail_status || existing?.guardrail_status || ''),
        guardrail_duration_ms: numberFromTelemetry(payload.guardrail_duration_ms, existing?.guardrail_duration_ms),
        guardrail_pre_duration_ms: numberFromTelemetry(payload.guardrail_pre_duration_ms, existing?.guardrail_pre_duration_ms),
        guardrail_post_duration_ms: numberFromTelemetry(payload.guardrail_post_duration_ms, existing?.guardrail_post_duration_ms),
        provider_latency_ms: numberFromTelemetry(payload.provider_latency_ms, existing?.provider_latency_ms),
        total_time_ms: numberFromTelemetry(payload.total_time_ms, existing?.total_time_ms),
        guardrail_results_json: String(payload.guardrail_results_json || existing?.guardrail_results_json || ''),
        created_at: String(payload.created_at || existing?.created_at || payload.queued_at || payload.finished_at || new Date().toISOString()),
        updated_at: String(payload.updated_at || payload.finished_at || existing?.updated_at || payload.created_at || new Date().toISOString())
      }
      if (index >= 0) {
        this.logs.splice(index, 1, {
          ...this.logs[index],
          ...next
        })
      } else if (this.offset === 0) {
        this.logs.unshift(next)
        this.total += 1
      } else {
        return
      }
      this.logs.sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
      this.logs = this.logs.slice(0, Math.max(this.limit || DEFAULT_LOG_PAGE_LIMIT, RECENT_LOG_LIMIT))
      if (realtimeCharacterization && this.selected?.request_id === requestID) {
        this.selected = { ...this.selected, primary_action: next.primary_action, characterization_json: next.characterization_json, characterization_duration_ms: next.characterization_duration_ms }
      }
    },
    async refresh(options: { limit?: number, offset?: number } & RequestLogFilters = {}) {
      const refreshSeq = ++this.refreshSeq
      const userUUIDs = options.userUUIDs === undefined ? this.userUUIDs : normalizedUserUUIDs(options.userUUIDs)
      const primaryAction = options.primaryAction === undefined ? this.primaryAction : String(options.primaryAction || '')
      const domain = options.domain === undefined ? this.domain : String(options.domain || '')
      const guardrailStatus = options.guardrailStatus === undefined ? this.guardrailStatus : String(options.guardrailStatus || '')
      this.userUUIDs = userUUIDs
      this.primaryAction = primaryAction
      this.domain = domain
      this.guardrailStatus = guardrailStatus
      this.loading = true
      this.lastError = ''
      try {
        const page = await useRelayApi().requestLogs({
          limit: options.limit ?? this.limit ?? DEFAULT_LOG_PAGE_LIMIT,
          offset: options.offset ?? this.offset,
          user_uuid: userUUIDs,
          primary_action: primaryAction,
          domain,
          guardrail_status: guardrailStatus
        })
        if (refreshSeq !== this.refreshSeq) return
        this.hydratePage(page)
      } catch (error: any) {
        if (refreshSeq !== this.refreshSeq) return
        this.lastError = error?.data?.message || error?.message || 'Unable to load request logs.'
      } finally {
        if (refreshSeq === this.refreshSeq) {
          this.loading = false
        }
      }
    },
    async refreshRecentActivity(options: { limit?: number } = {}) {
      const refreshSeq = ++this.refreshSeq
      const limit = options.limit ?? RECENT_LOG_LIMIT
      this.loading = true
      this.lastError = ''
      try {
        const response = await useRelayApi().recentFlowActivity({ limit })
        if (refreshSeq !== this.refreshSeq) return
        this.hydrateRecentActivity(response.requests, limit)
      } catch (error: any) {
        if (refreshSeq !== this.refreshSeq) return
        this.hydrateRecentActivity([], limit)
        this.lastError = error?.data?.message || error?.message || 'Unable to load recent request activity.'
      } finally {
        if (refreshSeq === this.refreshSeq) {
          this.loading = false
        }
      }
    },
    async firstPage(userUUIDs: string[] = []) {
      await this.refresh({ limit: DEFAULT_LOG_PAGE_LIMIT, offset: 0, userUUIDs })
    },
    async previousPage() {
      await this.refresh({ offset: this.previousOffset })
    },
    async nextPage() {
      if (!this.hasMore) return
      await this.refresh({ offset: this.nextOffset })
    },
    async inspect(requestId: string) {
      this.selected = await useRelayApi().requestDetail(requestId)
    },
    clearSelection() {
      this.selected = null
    }
  }
})

function numberFromTelemetry(...values: any[]) {
  for (const value of values) {
    if (value == null || value === '') continue
    const numberValue = Number(value)
    return Number.isFinite(numberValue) ? numberValue : 0
  }
  return 0
}

function stringOrExisting(value: any, existing?: string | null) {
  if (value == null || value === '') return existing ?? null
  return String(value)
}

function nullableNumber(value: any, existing?: number | null) {
  if (value == null || value === '') return existing ?? null
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : null
}

function characterizationHasDomain(value: string, domain: string) {
  if (!value || !domain) return false
  try {
    const parsed = JSON.parse(value)
    return Array.isArray(parsed?.domains) && parsed.domains.includes(domain)
  } catch {
    return false
  }
}

function normalizedUserUUIDs(values: string[] = []) {
  return [...new Set(values
    .map(value => String(value || '').trim().toLowerCase())
    .filter(Boolean))]
}

function recentFlowRequestToLog(item: RecentFlowRequest): RequestLog {
  return {
    id: item.request_id,
    request_id: item.request_id,
    actor_id: String(item.actor_id || ''),
    api_key_uuid: String(item.api_key_uuid || ''),
    parent_request_id: '',
    lane_id: item.lane_id ?? null,
    endpoint_id: item.endpoint_id ?? null,
    provider_id: item.provider_id ?? null,
    route_kind: 'chat',
    incoming_model: item.incoming_model,
    selected_upstream_model: item.selected_upstream_model,
    status_code: Number(item.status_code || 0),
    task_state: item.task_state,
    queued_at: item.queued_at ?? null,
    started_at: item.started_at ?? null,
    finished_at: item.finished_at ?? null,
    wait_ms: Number(item.wait_ms || 0),
    latency_ms: Number(item.latency_ms || 0),
    characterization_duration_ms: Number(item.characterization_duration_ms || 0),
    guardrail_status: String(item.guardrail_status || ''),
    guardrail_pre_duration_ms: Number(item.guardrail_pre_duration_ms || 0),
    provider_latency_ms: Number(item.provider_latency_ms || 0),
    guardrail_post_duration_ms: Number(item.guardrail_post_duration_ms || 0),
    total_time_ms: Number(item.total_time_ms || 0),
    streaming: false,
    priority: 0,
    fallback_count: Number(item.fallback_count || 0),
    estimated_input_tokens: Number(item.estimated_input_tokens || 0),
    estimated_output_tokens: Number(item.estimated_output_tokens || 0),
    actual_input_tokens: Number(item.actual_input_tokens || 0),
    actual_output_tokens: Number(item.actual_output_tokens || 0),
    actual_total_tokens: Number(item.actual_total_tokens || 0),
    estimated_cost_micros: Number(item.estimated_cost_micros || 0),
    actual_cost_micros: Number(item.actual_cost_micros || 0),
    error_text: item.error_text || '',
    created_at: item.created_at,
    updated_at: item.updated_at
  }
}
