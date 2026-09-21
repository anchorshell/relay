import type { Endpoint, RoutingLane } from '~/types/admin'

export type RoutingTargetOption = {
  label: string
  value: string
  description: string
  kind: 'group' | 'model'
  laneID: string
  endpointID: string
  providerSlug?: string
  searchText?: string
  class?: string
}

export type RoutingTargetLabel = {
  type: 'label'
  label: string
  class?: string
}

export type RoutingTargetSelectItem = RoutingTargetOption | RoutingTargetLabel

export function isRoutingTargetOption(item: RoutingTargetSelectItem): item is RoutingTargetOption {
  return 'value' in item
}

export function routingTargetKind(value: string): 'group' | 'model' | '' {
  const [kind] = String(value || '').split(':')
  return kind === 'group' || kind === 'model' ? kind : ''
}

export function routingTargetID(value: string) {
  return String(value || '').split(':')[1] || ''
}

export function useRoutingTargetOptions() {
  const catalog = useCatalogStore()

  const visibleLanes = computed(() =>
    catalog.lanes
      .filter((lane) => !lane.hidden)
      .slice()
      .sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
  )

  function endpointProviderSlug(endpoint: Endpoint) {
    return catalog.providerMap[endpoint.provider_id]?.slug?.trim() || 'provider-missing'
  }

  function modelTitle(endpoint: Endpoint) {
    return endpoint.slug?.trim() || endpoint.name || endpoint.upstream_model
  }

  function firstLaneForEndpoint(endpointID: string): RoutingLane | null {
    if (!endpointID) return null
    const membership = catalog.memberships
      .filter((item) => item.enabled && item.endpoint_id === endpointID)
      .sort((a, b) => a.manual_rank - b.manual_rank || a.id.localeCompare(b.id))[0]
    if (!membership) return null
    return visibleLanes.value.find((lane) => lane.id === membership.lane_id) || null
  }

  const targetGroups = computed<RoutingTargetSelectItem[][]>(() => {
    const groups = visibleLanes.value.map((lane) => {
      const modelCount = catalog.membershipsByLane(lane.id).filter((item) => item.enabled).length
      const modelCountLabel = `${modelCount} model${modelCount === 1 ? '' : 's'}`
      return {
        label: `${lane.name} (${modelCountLabel})`,
        value: `group:${lane.id}`,
        description: '',
        kind: 'group' as const,
        laneID: lane.id,
        endpointID: ''
      }
    })

    const modelsByProvider = new Map<string, { providerSlug: string, models: RoutingTargetOption[] }>()
    catalog.endpoints
      .filter((endpoint) => endpoint.enabled)
      .slice()
      .sort((a, b) => {
        const aProvider = endpointProviderSlug(a)
        const bProvider = endpointProviderSlug(b)
        return aProvider.localeCompare(bProvider) || modelTitle(a).localeCompare(modelTitle(b)) || a.id.localeCompare(b.id)
      })
      .forEach((endpoint) => {
        const lane = firstLaneForEndpoint(endpoint.id)
        const providerSlug = endpointProviderSlug(endpoint)
        const modelName = modelTitle(endpoint)
        const entry = modelsByProvider.get(endpoint.provider_id) || {
          providerSlug,
          models: []
        }
        entry.models.push({
          label: modelName,
          value: `model:${endpoint.id}`,
          description: '',
          kind: 'model' as const,
          laneID: lane?.id || '',
          endpointID: endpoint.id,
          providerSlug,
          searchText: `models ${providerSlug} ${modelName}`,
          class: 'routing-target-select-model-option'
        })
        modelsByProvider.set(endpoint.provider_id, entry)
      })

    const modelGroups = [...modelsByProvider.values()]
      .sort((a, b) => a.providerSlug.localeCompare(b.providerSlug))
      .map((entry, index) => [
        ...(index === 0 ? [{ type: 'label' as const, label: 'Models', class: 'routing-target-select-major-label' }] : []),
        { type: 'label' as const, label: entry.providerSlug, class: 'routing-target-select-provider-label' },
        ...entry.models
      ])

    return [
      groups.length ? [{ type: 'label' as const, label: 'Groups' }, ...groups] : [],
      ...modelGroups
    ].filter((group) => group.length)
  })

  const targetOptions = computed(() => targetGroups.value.flat().filter(isRoutingTargetOption))
  const targetOptionByValue = computed<Record<string, RoutingTargetOption>>(() =>
    Object.fromEntries(targetOptions.value.map((option) => [option.value, option]))
  )

  return {
    visibleLanes,
    targetGroups,
    targetOptions,
    targetOptionByValue,
    endpointProviderSlug,
    modelTitle,
    firstLaneForEndpoint
  }
}
