import { defineStore } from 'pinia'
import type { AdminMetricsPayload, QueueSnapshot, UsageSummary } from '~/types/admin'

export const useMetricsStore = defineStore('metrics', {
  state: () => ({
    summary: null as Record<string, any> | null,
    queue: { queue_depth_global: 0, queue_depth_by_endpoint: {}, in_flight_by_endpoint: {}, states: {} } as QueueSnapshot,
    usage: [] as UsageSummary[],
    spend: [] as UsageSummary[],
    loading: false,
    refreshSeq: 0,
    lastError: ''
  }),
  getters: {
    spendToday: state => state.spend.find(item => item.period === 'day' && item.scope_type === 'global')?.used_value ?? 0,
    totalInFlight: state => Object.values(state.queue.in_flight_by_endpoint ?? {}).reduce((sum, value) => sum + Number(value || 0), 0)
  },
  actions: {
    hydrate(payload?: AdminMetricsPayload) {
      if (!payload) return
      if (payload.summary) this.summary = payload.summary
      if (payload.queue) this.queue = payload.queue
      if (Array.isArray(payload.usage)) this.usage = payload.usage
      if (Array.isArray(payload.spend)) this.spend = payload.spend
    },
    async refresh() {
      const refreshSeq = ++this.refreshSeq
      this.loading = true
      this.lastError = ''
      const api = useRelayApi()
      try {
        const [summary, queue, usage, spend] = await Promise.all([
          api.get<Record<string, any>>('/api/stats/summary'),
          api.queueStats(),
          api.get<UsageSummary[]>('/api/stats/usage'),
          api.get<UsageSummary[]>('/api/stats/spend')
        ])
        if (refreshSeq !== this.refreshSeq) return
        this.hydrate({ summary, queue, usage, spend })
      } catch (error: any) {
        if (refreshSeq !== this.refreshSeq) return
        this.lastError = error?.data?.message || error?.message || 'Unable to load relay metrics.'
      } finally {
        if (refreshSeq === this.refreshSeq) {
          this.loading = false
        }
      }
    }
  }
})
