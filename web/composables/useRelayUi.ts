import type { CandidateTrace, EffectiveLimit, Endpoint, Metric, Period, RequestLog } from '~/types/admin'
import { buildInferenceCurl } from '../utils/inference'

const PERIODS: Period[] = ['second', 'minute', 'hour', 'day', 'month']
const METRICS: Metric[] = ['requests', 'tokens', 'spend']

function minValue(...values: Array<number | null | undefined>) {
  const filtered = values.filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  if (!filtered.length) return null
  return Math.min(...filtered)
}

export function useRelayUi() {
  const catalog = useCatalogStore()

  function metricLabel(metric: Metric) {
    switch (metric) {
      case 'requests':
        return 'Requests'
      case 'tokens':
        return 'Tokens'
      case 'spend':
        return 'Spend'
      case 'concurrency':
        return 'Concurrency'
      default:
        return metric
    }
  }

  function periodLabel(period: Period) {
    return period === 'second' ? 'sec' : period
  }

  function scopeLabel(scopeType: string, scopeID: string) {
    if (scopeType === 'provider') return catalog.providerMap[scopeID]?.name || `Provider ${scopeID}`
    if (scopeType === 'endpoint') return catalog.endpointMap[scopeID]?.name || `Model ${scopeID}`
    if (scopeType === 'lane') return catalog.laneMap[scopeID]?.name || `Group ${scopeID}`
    return 'Global'
  }

  function firstConfigured(scopeType: string, scopeID: string, metric: Metric, period: Period) {
    return catalog.limitPolicies.find((item) =>
      item.scope_type === scopeType &&
      item.scope_id === scopeID &&
      item.metric === metric &&
      item.period === period &&
      item.enabled
    )?.limit_value ?? null
  }

  function firstObserved(scopeType: string, scopeID: string, metric: Metric, period: Period) {
    const candidates = catalog.observedLimits
      .filter((item) =>
        item.scope_type === scopeType &&
        item.scope_id === scopeID &&
        item.metric === metric &&
        item.period === period &&
        item.enabled
      )
      .sort((a, b) => a.observed_value - b.observed_value)
    return candidates[0]?.observed_value ?? null
  }

  function modelLimitRow(endpoint: Endpoint, metric: Metric, period: Period) {
    const providerConfigured = firstConfigured('provider', endpoint.provider_id, metric, period)
    const modelConfigured = firstConfigured('endpoint', endpoint.id, metric, period)
    const configured = modelConfigured ?? providerConfigured

    const providerObserved = firstObserved('provider', endpoint.provider_id, metric, period)
    const modelObserved = firstObserved('endpoint', endpoint.id, metric, period)
    const observed = minValue(providerObserved, modelObserved)

    return {
      providerConfigured,
      modelConfigured,
      providerObserved,
      modelObserved,
      configured,
      observed,
      effective: minValue(configured, observed)
    }
  }

  function providerLimitRow(providerID: string, metric: Metric, period: Period) {
    const configured = firstConfigured('provider', providerID, metric, period)
    const observed = firstObserved('provider', providerID, metric, period)
    return { configured, observed, effective: minValue(configured, observed) }
  }

  function explainCandidate(candidate: CandidateTrace) {
    if (candidate.reason) return candidate.reason
    if (candidate.decision === 'selected') return 'Accepted within the allowed wait budget.'
    if (candidate.decision === 'not_needed') return 'A higher-ranked model was already acceptable.'
    if (candidate.decision === 'rejected') return 'This model did not fit the current wait, limit, or cost policy.'
    return 'No explicit reason recorded.'
  }

  function groupedLimitImpact(raw: EffectiveLimit[] | null | undefined) {
    const items = raw || []
    return items
      .filter((item) => item.scope_type === 'provider' || item.scope_type === 'endpoint')
      .filter((item) =>
        typeof item.configured === 'number' ||
        typeof item.observed === 'number' ||
        typeof item.effective === 'number'
      )
      .map((item) => ({
        ...item,
        scopeName: scopeLabel(item.scope_type, item.scope_id)
      }))
  }

  function parsedLimitImpact(request: RequestLog | null) {
    if (!request?.limit_impact_json) return []
    try {
      return groupedLimitImpact(JSON.parse(request.limit_impact_json) as EffectiveLimit[])
    } catch {
      return []
    }
  }

  function parsedCandidateTrace(request: RequestLog | null) {
    if (!request?.candidate_trace_json) return []
    try {
      return JSON.parse(request.candidate_trace_json) as CandidateTrace[]
    } catch {
      return []
    }
  }

  function previewResponse(routeKind: string, result: any) {
    if (!result) return ''
    if (result.error) return result.error
    if (result.raw_text) return result.raw_text
    if (routeKind === 'chat') return result.choices?.[0]?.message?.content || 'The model returned a response without assistant text.'
    if (routeKind === 'responses') return result.output_text || result.output?.flatMap((item: any) => item.content || []).map((item: any) => item.text).join('\n') || 'The model responded successfully.'
    if (routeKind === 'embeddings') return Array.isArray(result.data?.[0]?.embedding) ? `Embedding received. Vector length ${result.data[0].embedding.length}.` : 'Embedding response received.'
    return 'Request completed.'
  }

  function buildModelCurl(endpoint: Endpoint) {
    const laneMembership = catalog.memberships.find((membership) => membership.endpoint_id === endpoint.id)
    const laneName = laneMembership ? catalog.laneMap[laneMembership.lane_id]?.name : ''
    const targetModel = laneName || endpoint.upstream_model
    return buildInferenceCurl(JSON.stringify({ model: targetModel, messages: [{ role: 'user', content: 'Say hello from Model Relay.' }] }))
  }

  function buildGroupCurl(groupName: string) {
    return buildInferenceCurl(JSON.stringify({ model: groupName, messages: [{ role: 'user', content: 'Explain which model you selected.' }] }))
  }

  return {
    PERIODS,
    METRICS,
    metricLabel,
    periodLabel,
    scopeLabel,
    modelLimitRow,
    providerLimitRow,
    explainCandidate,
    groupedLimitImpact,
    parsedLimitImpact,
    parsedCandidateTrace,
    previewResponse,
    buildModelCurl,
    buildGroupCurl
  }
}
