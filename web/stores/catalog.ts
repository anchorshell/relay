import { defineStore } from 'pinia'
import type { AdminCatalogPayload, Credential, Endpoint, Guardrail, GuardrailBinding, LaneMembership, LimitPolicy, ObservedLimit, PricingPolicy, Provider, RoutingLane } from '~/types/admin'

export const useCatalogStore = defineStore('catalog', {
  state: () => ({
    providers: [] as Provider[],
    credentials: [] as Credential[],
    endpoints: [] as Endpoint[],
    lanes: [] as RoutingLane[],
    memberships: [] as LaneMembership[],
    limitPolicies: [] as LimitPolicy[],
    observedLimits: [] as ObservedLimit[],
    pricingPolicies: [] as PricingPolicy[],
    guardrails: [] as Guardrail[],
    guardrailBindings: [] as GuardrailBinding[],
    loading: false,
    refreshSeq: 0
  }),
  getters: {
    providerMap: state => Object.fromEntries(state.providers.map(item => [item.id, item])),
    endpointMap: state => Object.fromEntries(state.endpoints.map(item => [item.id, item])),
    laneMap: state => Object.fromEntries(state.lanes.map(item => [item.id, item])),
    endpointsByProvider: state => (providerId: string) => state.endpoints
      .filter(item => item.provider_id === providerId)
      .slice()
      .sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id)),
    membershipsByLane: state => (laneId: string) => state.memberships.filter(item => item.lane_id === laneId).sort((a, b) => a.manual_rank - b.manual_rank),
    endpointPricing: state => (endpointId: string) => state.pricingPolicies.find(item => item.endpoint_id === endpointId),
    enabledGuardrails: state => state.guardrails.filter(item => item.enabled),
    directGuardrailsForLane: state => (laneId: string) => state.guardrailBindings
      .filter(binding => binding.enabled && binding.routing_lane_id === laneId)
      .map(binding => state.guardrails.find(item => item.id === binding.guardrail_id && item.enabled))
      .filter(Boolean) as Guardrail[],
    directGuardrailsForProvider: state => (providerId: string) => state.guardrailBindings
      .filter(binding => binding.enabled && binding.provider_id === providerId)
      .map(binding => state.guardrails.find(item => item.id === binding.guardrail_id && item.enabled))
      .filter(Boolean) as Guardrail[],
    effectiveGuardrailsForEndpoint: state => (endpointId: string, laneId = '') => {
      const endpoint = state.endpoints.find(item => item.id === endpointId)
      if (!endpoint) return [] as Guardrail[]
      const ids = new Set(state.guardrailBindings.filter(binding => binding.enabled && (
        binding.endpoint_id === endpointId || binding.provider_id === endpoint.provider_id || (laneId && binding.routing_lane_id === laneId)
      )).map(binding => binding.guardrail_id))
      return state.guardrails.filter(item => item.enabled && ids.has(item.id))
    },
    effectiveGuardrailsForEndpointAcrossGroups: state => (endpointId: string) => {
      const endpoint = state.endpoints.find(item => item.id === endpointId)
      if (!endpoint) return [] as Guardrail[]
      const laneIds = new Set(state.memberships
        .filter(membership => membership.enabled && membership.endpoint_id === endpointId)
        .map(membership => membership.lane_id))
      const ids = new Set(state.guardrailBindings.filter(binding => binding.enabled && (
        binding.endpoint_id === endpointId ||
        binding.provider_id === endpoint.provider_id ||
        Boolean(binding.routing_lane_id && laneIds.has(binding.routing_lane_id))
      )).map(binding => binding.guardrail_id))
      return state.guardrails.filter(item => item.enabled && ids.has(item.id))
    }
  },
  actions: {
    applyQueueDerivedHealth(endpointID: string, queueItems: Array<{
      endpoint_id: string
      state: string
      predicted_eligible_at?: string
      resource_eligible_at?: string | null
      defer_scope?: string
    }>) {
      if (!endpointID) return
      const endpointIndex = this.endpoints.findIndex(item => item.id === endpointID)
      if (endpointIndex < 0) return

      const endpoint = this.endpoints[endpointIndex]
      const now = Date.now()
      const waitingItems = queueItems
        .filter((item) => {
          if (item.endpoint_id !== endpointID || item.state !== 'waiting') return false
          return !['user', 'user_model', 'user_provider', 'api_key', 'api_key_model', 'api_key_provider'].includes(String(item.defer_scope || ''))
        })
        .map((item) => {
          const eligibleAt = item.resource_eligible_at || item.predicted_eligible_at
          return eligibleAt ? new Date(eligibleAt).getTime() : 0
        })
        .filter(value => value > now)
        .sort((a, b) => a - b)

      let nextHealth = endpoint.health_status
      let nextCooldownUntil = endpoint.cooldown_until ?? null
      let nextCooldownReason = endpoint.cooldown_reason || ''
      let nextCooldownStatusCode = Number(endpoint.cooldown_status_code || 0)

      if (waitingItems.length > 0) {
        nextHealth = 'cooling_down'
        nextCooldownUntil = new Date(waitingItems[0]).toISOString()
      } else if (
        (endpoint.health_status === 'cooling_down' || endpoint.health_status === 'rate_limited') &&
        endpoint.cooldown_until &&
        new Date(endpoint.cooldown_until).getTime() <= now
      ) {
        nextHealth = 'healthy'
        nextCooldownUntil = null
        nextCooldownReason = ''
        nextCooldownStatusCode = 0
      }

      this.endpoints.splice(endpointIndex, 1, {
        ...endpoint,
        health_status: nextHealth,
        cooldown_until: nextCooldownUntil,
        cooldown_reason: nextCooldownReason,
        cooldown_status_code: nextCooldownStatusCode
      })

      const providerIndex = this.providers.findIndex(item => item.id === endpoint.provider_id)
      if (providerIndex < 0) return

      const severity = (status: string) => {
        switch (status) {
          case 'unhealthy': return 3
          case 'rate_limited': return 2
          case 'cooling_down': return 1
          default: return 0
        }
      }
      const providerEndpoints = this.endpoints.filter(item => item.provider_id === endpoint.provider_id)
      const derived = providerEndpoints.reduce((worst, item) =>
        severity(item.health_status) > severity(worst) ? item.health_status : worst
      , 'healthy')
      this.providers.splice(providerIndex, 1, {
        ...this.providers[providerIndex],
        health_status: derived
      })
    },
    normalizeCooldowns(now = Date.now()) {
      let changed = false

      this.endpoints = this.endpoints.map((endpoint) => {
        if (endpoint.health_status === 'healthy' && endpoint.cooldown_until) {
          changed = true
          return {
            ...endpoint,
            cooldown_until: null,
            cooldown_reason: '',
            cooldown_status_code: 0
          }
        }
        if (
          (endpoint.health_status !== 'cooling_down' && endpoint.health_status !== 'rate_limited') ||
          !endpoint.cooldown_until
        ) {
          return endpoint
        }
        if (new Date(endpoint.cooldown_until).getTime() > now) {
          return endpoint
        }

        changed = true
        return {
          ...endpoint,
          health_status: 'healthy',
          cooldown_until: null,
          cooldown_reason: '',
          cooldown_status_code: 0
        }
      })

      if (!changed) return

      this.providers = this.providers.map((provider) => {
        const childEndpoints = this.endpoints.filter(item => item.provider_id === provider.id)
        const severity = (status: string) => {
          switch (status) {
            case 'unhealthy': return 3
            case 'rate_limited': return 2
            case 'cooling_down': return 1
            default: return 0
          }
        }
        const derived = childEndpoints.reduce((worst, endpoint) =>
          severity(endpoint.health_status) > severity(worst) ? endpoint.health_status : worst
        , 'healthy')
        return {
          ...provider,
          health_status: derived
        }
      })
    },
    applyEndpointHealthChange(payload: Record<string, any>) {
      const endpointID = String(payload.endpoint_id || '')
      if (!endpointID) return
      const endpointIndex = this.endpoints.findIndex(item => item.id === endpointID)
      if (endpointIndex >= 0) {
        const current = this.endpoints[endpointIndex]
        const hasCooldown = Object.prototype.hasOwnProperty.call(payload, 'cooldown_until')
        const hasCooldownReason = Object.prototype.hasOwnProperty.call(payload, 'cooldown_reason')
        const hasCooldownStatusCode = Object.prototype.hasOwnProperty.call(payload, 'cooldown_status_code')
        this.endpoints.splice(endpointIndex, 1, {
          ...current,
          health_status: payload.health_status || current.health_status,
          cooldown_until: hasCooldown ? (payload.cooldown_until ? String(payload.cooldown_until) : null) : current.cooldown_until ?? null,
          cooldown_reason: hasCooldownReason ? String(payload.cooldown_reason || '') : current.cooldown_reason || '',
          cooldown_status_code: hasCooldownStatusCode ? Number(payload.cooldown_status_code || 0) : Number(current.cooldown_status_code || 0)
        })
      }

      const providerID = String(payload.provider_id || '')
      if (!providerID) return
      const providerIndex = this.providers.findIndex(item => item.id === providerID)
      if (providerIndex < 0) return
      const childEndpoints = this.endpoints.filter(item => item.provider_id === providerID)
      const severity = (status: string) => {
        switch (status) {
          case 'unhealthy': return 3
          case 'rate_limited': return 2
          case 'cooling_down': return 1
          default: return 0
        }
      }
      const derived = childEndpoints.reduce((worst, endpoint) =>
        severity(endpoint.health_status) > severity(worst) ? endpoint.health_status : worst
      , this.providers[providerIndex].health_status || 'healthy')
      this.providers.splice(providerIndex, 1, {
        ...this.providers[providerIndex],
        health_status: derived
      })
    },
    applyObservedLimit(payload: Record<string, any>) {
      const scopeType = String(payload.scope_type || '')
      const scopeID = String(payload.scope_id || '')
      const metric = String(payload.metric || '')
      const period = String(payload.period || '')
      if (!scopeType || !scopeID || !metric || !period) return
      const next = {
        id: String(payload.id || (globalThis.crypto?.randomUUID?.() ?? `local-${Date.now()}`)),
        scope_type: scopeType as any,
        scope_id: scopeID,
        metric: metric as any,
        period: period as any,
        observed_value: Number(payload.observed_value || 0),
        source_header: String(payload.source_header || ''),
        observed_at: String(payload.observed_at || new Date().toISOString()),
        expires_at: payload.expires_at ? String(payload.expires_at) : null,
        enabled: true,
        created_at: String(payload.observed_at || new Date().toISOString()),
        updated_at: String(payload.observed_at || new Date().toISOString())
      }
      const index = this.observedLimits.findIndex(item =>
        item.scope_type === next.scope_type &&
        item.scope_id === next.scope_id &&
        item.metric === next.metric &&
        item.period === next.period
      )
      if (index >= 0) {
        this.observedLimits.splice(index, 1, {
          ...this.observedLimits[index],
          ...next
        })
        return
      }
      this.observedLimits.unshift(next as any)
    },
    applyLimitPolicyChange(payload: Record<string, any>) {
      const id = String(payload.id || '')
      const action = String(payload.action || '')
      const scopeType = String(payload.scope_type || '')
      const scopeID = String(payload.scope_id || '')
      const metric = String(payload.metric || '')
      const period = String(payload.period || '')
      if (!id || !scopeType || !metric || !period) return

      const index = this.limitPolicies.findIndex(item =>
        item.id === id ||
        (
          item.scope_type === scopeType &&
          item.scope_id === scopeID &&
          item.metric === metric &&
          item.period === period
        )
      )

      if (action === 'deleted') {
        if (index >= 0) this.limitPolicies.splice(index, 1)
        return
      }

      const next: LimitPolicy = {
        id,
        scope_type: scopeType as any,
        scope_id: scopeID,
        metric: metric as any,
        period: period as any,
        limit_value: Number(payload.limit_value || 0),
        enabled: payload.enabled == null ? true : Boolean(payload.enabled),
        source: String(payload.source || 'configured'),
        created_at: String(payload.created_at || payload.updated_at || new Date().toISOString()),
        updated_at: String(payload.updated_at || new Date().toISOString())
      }

      if (index >= 0) {
        this.limitPolicies.splice(index, 1, {
          ...this.limitPolicies[index],
          ...next
        })
        return
      }
      this.limitPolicies.unshift(next)
    },
    hydrate(payload?: AdminCatalogPayload, markChanged = true) {
      if (!payload) return
      if (Array.isArray(payload.providers)) this.providers = payload.providers
      if (Array.isArray(payload.credentials)) this.credentials = payload.credentials
      if (Array.isArray(payload.endpoints)) this.endpoints = payload.endpoints
      if (Array.isArray(payload.lanes)) this.lanes = payload.lanes
      if (Array.isArray(payload.memberships)) this.memberships = payload.memberships
      if (Array.isArray(payload.limit_policies)) this.limitPolicies = payload.limit_policies
      if (Array.isArray(payload.observed_limits)) this.observedLimits = payload.observed_limits
      if (Array.isArray(payload.pricing_policies)) this.pricingPolicies = payload.pricing_policies
      if (Array.isArray(payload.guardrails)) this.guardrails = payload.guardrails
      if (Array.isArray(payload.guardrail_bindings)) this.guardrailBindings = payload.guardrail_bindings
      if (markChanged) this.refreshSeq++
    },
    async refreshAll() {
      const refreshSeq = ++this.refreshSeq
      this.loading = true
      const api = useRelayApi()
      try {
        const payload = await api.pageData('setup')
        if (refreshSeq !== this.refreshSeq) return
        this.hydrate(payload.catalog, false)
      } finally {
        if (refreshSeq === this.refreshSeq) {
          this.loading = false
        }
      }
    },
    async saveResource<T>(resource: string, payload: Partial<T> & { id?: string }) {
      const api = useRelayApi()
      if (payload.id) await api.put(`/api/${resource}/${payload.id}`, payload)
      else await api.post(`/api/${resource}`, payload)
      await this.refreshAll()
    },
    async deleteResource(resource: string, id: string) {
      const api = useRelayApi()
      await api.del(`/api/${resource}/${id}`)
      await this.refreshAll()
    },
    async saveCredential(payload: { id?: string, provider_id: string, name: string, secret?: string, enabled: boolean }) {
      const api = useRelayApi()
      if (payload.id) await api.put(`/api/credentials/${payload.id}`, payload)
      else await api.post('/api/credentials', payload)
      await this.refreshAll()
    },
    async recomputeSuggestedRank(endpointId: string) {
      const api = useRelayApi()
      await api.post(`/api/endpoints/${endpointId}/recompute-suggested-rank`)
      await this.refreshAll()
    },
    async recomputeAllSuggestions() {
      const api = useRelayApi()
      await api.post('/api/suggestions/recompute-all')
      await this.refreshAll()
    },
    async reorderEndpoints(sortedIds: string[]) {
      const api = useRelayApi()
      await Promise.all(sortedIds.map((id, index) => {
        const endpoint = this.endpoints.find(item => item.id === id)
        if (!endpoint) return Promise.resolve()
        return api.put(`/api/endpoints/${id}`, { ...endpoint, manual_rank: index + 1 })
      }))
      await this.refreshAll()
    },
    async reorderMemberships(laneId: string, sortedMembershipIds: string[]) {
      const api = useRelayApi()
      await Promise.all(sortedMembershipIds.map((id, index) => {
        const membership = this.memberships.find(item => item.id === id && item.lane_id === laneId)
        if (!membership) return Promise.resolve()
        return api.put(`/api/lane-memberships/${id}`, { ...membership, manual_rank: index + 1 })
      }))
      await this.refreshAll()
    }
  }
})
