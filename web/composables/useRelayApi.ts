import type { AdminPageKey, AdminPagePayload, CharacterizationIntentAnalyticsResponse, LaneSimulationResult, LiveFlowSimulationResult, QueueItem, QueueSnapshot, RecentFlowActivityResponse, RecentModelUsageResponse, RequestLog, RequestLogPage, SystemInfo, TelemetryEvent, UsageAnalyticsBucket, UsageAnalyticsResponse } from '~/types/admin'

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'
type QueryParamValue = string | number | boolean | Array<string | number | boolean> | null | undefined

export function useRelayApi() {
  const auth = useAuthStore()
  const { apiURL } = useRelayURLs()

  async function request<T>(path: string, method: Method = 'GET', body?: any, extraHeaders?: HeadersInit): Promise<T> {
    const headers: HeadersInit = {
      ...(auth.token ? { Authorization: `Bearer ${auth.token}` } : {}),
      ...extraHeaders
    }
    if (body && !(body instanceof FormData) && !(body instanceof Blob) && typeof body !== 'string') {
      headers['Content-Type'] = 'application/json'
    }
    return await $fetch<T>(apiURL(path), {
      method,
      body: body instanceof Blob || body instanceof FormData || typeof body === 'string' ? body : body ? JSON.stringify(body) : undefined,
      credentials: 'include',
      headers
    })
  }

  return {
    get: <T>(path: string) => request<T>(path),
    post: <T>(path: string, body?: any, headers?: HeadersInit) => request<T>(path, 'POST', body, headers),
    put: <T>(path: string, body?: any) => request<T>(path, 'PUT', body),
    del: <T>(path: string) => request<T>(path, 'DELETE'),
    sessionStatus: () => request<{ authenticated: boolean, system: SystemInfo }>('/api/session'),
    pageData: (page: AdminPageKey) => request<AdminPagePayload>(`/api/page-data/${page}`),
    events: () => request<TelemetryEvent[]>('/api/logs/events'),
    requestLogs: (params: { limit?: number, offset?: number, sort?: string, direction?: 'asc' | 'desc', user_uuid?: string[], primary_action?: string, domain?: string, guardrail_applied?: boolean, guardrail_status?: string, guardrail_preset?: string } = {}) => {
      const query = new URLSearchParams()
      if (params.limit != null) query.set('limit', String(params.limit))
      if (params.offset != null) query.set('offset', String(params.offset))
      if (params.sort) query.set('sort', params.sort)
      if (params.direction) query.set('direction', params.direction)
      if (params.primary_action) query.set('primary_action', params.primary_action)
      if (params.domain) query.set('domain', params.domain)
      if (params.guardrail_applied != null) query.set('guardrail_applied', String(params.guardrail_applied))
      if (params.guardrail_status) query.set('guardrail_status', params.guardrail_status)
      if (params.guardrail_preset) query.set('guardrail_preset', params.guardrail_preset)
      for (const userUUID of params.user_uuid || []) {
        if (userUUID) query.append('user_uuid', userUUID)
      }
      const suffix = query.toString()
      return request<RequestLogPage>(`/api/logs/requests${suffix ? `?${suffix}` : ''}`)
    },
    requestDetail: (id: string) => request<RequestLog>(`/api/logs/requests/${id}`),
    usageAnalytics: (
      startDate: string,
      endDate: string,
      metric: 'requests' | 'tokens' | 'spend',
      bucket: UsageAnalyticsBucket,
      timezoneOffsetMinutes: number,
      extraParams: Record<string, QueryParamValue> = {}
    ) => {
      const query = new URLSearchParams({
        start_date: startDate,
        end_date: endDate,
        metric,
        bucket,
        tz_offset_minutes: String(timezoneOffsetMinutes)
      })
      for (const [key, value] of Object.entries(extraParams)) {
        if (value == null || value === '') continue
        if (Array.isArray(value)) {
          for (const item of value) {
            query.append(key, String(item))
          }
          continue
        }
        query.set(key, String(value))
      }
      return request<UsageAnalyticsResponse>(`/api/stats/usage-analytics?${query.toString()}`)
    },
    characterizationIntentAnalytics: (
      startDate: string,
      endDate: string,
      metric: 'requests' | 'tokens' | 'spend',
      timezoneOffsetMinutes: number,
      extraParams: Record<string, QueryParamValue> = {}
    ) => {
      const query = new URLSearchParams({
        start_date: startDate,
        end_date: endDate,
        metric,
        tz_offset_minutes: String(timezoneOffsetMinutes)
      })
      for (const [key, value] of Object.entries(extraParams)) {
        if (value == null || value === '') continue
        if (Array.isArray(value)) {
          for (const item of value) {
            query.append(key, String(item))
          }
          continue
        }
        query.set(key, String(value))
      }
      return request<CharacterizationIntentAnalyticsResponse>(`/api/stats/characterization-intents?${query.toString()}`)
    },
    recentFlowActivity: (params: { limit?: number, laneId?: string, endpointId?: string } = {}) => {
      const query = new URLSearchParams()
      if (params.limit != null) query.set('limit', String(params.limit))
      if (params.laneId) query.set('lane_id', params.laneId)
      if (params.endpointId) query.set('endpoint_id', params.endpointId)
      const suffix = query.toString()
      return request<RecentFlowActivityResponse>(`/api/stats/recent-flow-activity${suffix ? `?${suffix}` : ''}`)
    },
    recentModelUsage: (params: { limit?: number } = {}) => {
      const query = new URLSearchParams()
      if (params.limit != null) query.set('limit', String(params.limit))
      const suffix = query.toString()
      return request<RecentModelUsageResponse>(`/api/stats/recent-model-usage${suffix ? `?${suffix}` : ''}`)
    },
    queueItems: () => request<QueueItem[]>('/api/queue/items'),
    deleteQueueItem: (taskId: string) => request<void>(`/api/queue/items/${taskId}`, 'DELETE'),
    queueStats: () => request<QueueSnapshot>('/api/stats/queue'),
    simulateLane: (payload: any) => request<LaneSimulationResult>('/api/simulate/lane', 'POST', payload),
    simulateLiveFlow: (payload: any) => request<LiveFlowSimulationResult>('/api/simulate/live-flow', 'POST', payload),
    cancelQueueItem: (taskId: string) => request<void>(`/api/queue/cancel/${taskId}`, 'POST')
  }
}
