<script setup lang="ts">
import type { CapacityLimitRow, Metric, ObservedLimit, Period } from '~/types/admin'
import { limitProgressBand } from '../../../../utils/limitProgress'

const catalog = useCatalogStore()
const api = useRelayApi()
const pageData = useAdminPageDataStore()
const capacity = useCapacityStore()
const app = useAppStore()
const { number, currencyMicros, microsToDollarInput, dollarsToMicros, dateTime } = useFormatters()
const { PERIODS, metricLabel, periodLabel, modelLimitRow, providerLimitRow } = useRelayUi()
const relayPermissions = useModelRelayPermissions()
const canManageLimits = computed(() => relayPermissions.has('relay:limits:manage'))
const observedLimitsUIEnabled = false

const editOpen = ref(false)
const editTarget = ref<{ scopeType: 'provider' | 'endpoint', scopeId: string, name: string } | null>(null)
const editValues = reactive<Record<string, string>>({})
const applyingObservedKey = ref('')
const modelEditEndpointID = ref('')

const metricsForEditing: Metric[] = ['requests', 'tokens', 'spend']

function compareCreated(left: { created_at?: string, id: string }, right: { created_at?: string, id: string }) {
  const leftCreated = left.created_at ? Date.parse(left.created_at) : 0
  const rightCreated = right.created_at ? Date.parse(right.created_at) : 0
  if (Number.isFinite(leftCreated) && Number.isFinite(rightCreated) && leftCreated !== rightCreated) {
    return leftCreated - rightCreated
  }
  return left.id.localeCompare(right.id)
}

const rows = computed(() => {
  return catalog.providers.slice().sort(compareCreated).flatMap((provider) => {
    const providerRow = {
      key: `provider-${provider.id}`,
      scopeType: 'provider' as const,
      scopeId: provider.id,
      providerName: provider.name,
      modelName: 'Provider default',
      values: Object.fromEntries(metricsForEditing.flatMap((metric) =>
        PERIODS.map((period) => [`${metric}_${period}`, providerLimitRow(provider.id, metric, period)])
      ))
    }

    const providerModels = catalog.endpoints
      .filter((endpoint) => endpoint.provider_id === provider.id)
      .slice()
      .sort(compareCreated)
      .map((endpoint) => ({
        key: `endpoint-${endpoint.id}`,
        scopeType: 'endpoint' as const,
        scopeId: endpoint.id,
        providerName: provider.name,
        modelName: endpoint.name,
        values: Object.fromEntries(metricsForEditing.flatMap((metric) =>
          PERIODS.map((period) => [`${metric}_${period}`, modelLimitRow(endpoint, metric, period)])
        ))
      }))

    return [providerRow, ...providerModels]
  })
})

function fieldKey(metric: Metric, period: Period) {
  return `${metric}_${period}`
}

function editablePeriods(metric: Metric) {
  return PERIODS.filter((period) => !(metric === 'spend' && period === 'second'))
}

function formatLimitValue(metric: Metric, value?: number | null) {
  if (value == null) return '—'
  return metric === 'spend' ? currencyMicros(value) : number(value)
}

function applicableCapacityRows(scopeType: 'provider' | 'endpoint', scopeId: string, metric: Metric, period: Period) {
  const endpoint = scopeType === 'endpoint' ? catalog.endpointMap[scopeId] : null
  const models = scopeType === 'endpoint'
    ? [capacity.modelsByEndpoint[scopeId]].filter(Boolean)
    : capacity.snapshot?.models || []

  return models.flatMap(model => (model?.limit_rows || []).filter((row) => {
    if (row.user_scoped || row.metric !== metric || row.period !== period) return false
    if (scopeType === 'provider') return row.scope_type === 'provider' && row.scope_id === scopeId
    return (row.scope_type === 'endpoint' && row.scope_id === scopeId) ||
      Boolean(endpoint && row.scope_type === 'provider' && row.scope_id === endpoint.provider_id)
  }))
}

function capacityRowPercent(row: CapacityLimitRow) {
  const effective = Number(row.effective || row.configured || 0)
  const used = Number(row.used || 0) + Number(row.reserved || 0)
  const reported = Number(row.percent)
  return Number.isFinite(reported) && reported >= 0
    ? reported
    : effective > 0 ? used / effective * 100 : 0
}

function limitCellUsagePercent(scopeType: 'provider' | 'endpoint', scopeId: string, metric: Metric, period: Period, effective?: number | null) {
  if (effective == null) return null
  const rows = applicableCapacityRows(scopeType, scopeId, metric, period)
  if (!rows.length) return null
  const closest = rows.reduce((current, candidate) =>
    capacityRowPercent(candidate) > capacityRowPercent(current) ? candidate : current
  )
  const percent = capacityRowPercent(closest)
  const forcedFull = Boolean(closest.blocked) || (Number(closest.effective || closest.configured || 0) > 0 && Number(closest.used || 0) + Number(closest.reserved || 0) >= Number(closest.effective || closest.configured || 0))
  return Math.round(Math.max(0, Math.min(100, forcedFull ? 100 : percent)))
}

function limitCellClass(scopeType: 'provider' | 'endpoint', scopeId: string, metric: Metric, period: Period, effective?: number | null) {
  const percent = limitCellUsagePercent(scopeType, scopeId, metric, period, effective)
  if (percent == null) return ''
  switch (limitProgressBand(percent, percent >= 100)) {
    case 'full': return 'bg-red-700/25'
    case 'critical': return 'bg-red-400/15'
    case 'high': return 'bg-orange-400/15'
    case 'medium': return 'bg-yellow-300/10'
    default: return 'bg-emerald-400/10'
  }
}

function editInputValue(metric: Metric, value?: number | null) {
  if (value == null) return ''
  return metric === 'spend' ? microsToDollarInput(value) : String(value)
}

function parseLimitInput(metric: Metric, raw: string) {
  return metric === 'spend' ? dollarsToMicros(raw) : Number(raw)
}

function configuredValue(scopeType: 'provider' | 'endpoint', scopeId: string, metric: Metric, period: Period) {
  return catalog.limitPolicies.find((item) =>
    item.scope_type === scopeType &&
    item.scope_id === scopeId &&
    item.metric === metric &&
    item.period === period &&
    item.enabled
  )?.limit_value
}

function observedKey(item: Pick<ObservedLimit, 'scope_type' | 'scope_id' | 'metric' | 'period'>) {
  return `${item.scope_type}:${item.scope_id}:${item.metric}:${item.period}`
}

function isEditableScope(scopeType: string): scopeType is 'provider' | 'endpoint' {
  return scopeType === 'provider' || scopeType === 'endpoint'
}

function scopeDisplayName(scopeType: 'provider' | 'endpoint', scopeId: string) {
  if (scopeType === 'provider') {
    return `${catalog.providerMap[scopeId]?.name || 'Provider missing'} (provider wide)`
  }

  const endpoint = catalog.endpointMap[scopeId]
  if (!endpoint) return 'Model missing'
  const providerName = catalog.providerMap[endpoint.provider_id]?.name || 'Provider missing'
  return `${providerName} / ${endpoint.name}`
}

function scopeEditName(scopeType: 'provider' | 'endpoint', scopeId: string) {
  if (scopeType === 'provider') {
    return catalog.providerMap[scopeId]?.name || 'Provider missing'
  }

  const endpoint = catalog.endpointMap[scopeId]
  if (!endpoint) return 'Model missing'
  const providerName = catalog.providerMap[endpoint.provider_id]?.name || 'Provider missing'
  return `${providerName} · ${endpoint.name}`
}

const observedActionItems = computed(() => {
  const strictestByScope = new Map<string, ObservedLimit>()

  for (const item of catalog.observedLimits) {
    if (!item.enabled || !isEditableScope(item.scope_type)) continue

    const key = observedKey(item)
    const current = strictestByScope.get(key)
    if (!current) {
      strictestByScope.set(key, item)
      continue
    }

    const itemObservedAt = new Date(item.observed_at).getTime()
    const currentObservedAt = new Date(current.observed_at).getTime()
    if (
      item.observed_value < current.observed_value ||
      (item.observed_value === current.observed_value && itemObservedAt > currentObservedAt)
    ) {
      strictestByScope.set(key, item)
    }
  }

  return [...strictestByScope.values()]
    .map((item) => {
      const scopeType = item.scope_type as 'provider' | 'endpoint'
      const configured = configuredValue(scopeType, item.scope_id, item.metric, item.period)
      const needsConfig = typeof configured !== 'number'
      const needsTightening = typeof configured === 'number' && configured > item.observed_value
      const actionKind = needsConfig ? 'missing' : needsTightening ? 'tighten' : 'none'
      const actionLabel = needsConfig ? 'Missing configured cap' : 'Configured cap is looser'
      const summary = needsConfig
        ? `Upstream taught a ${metricLabel(item.metric).toLowerCase()} / ${periodLabel(item.period)} cap of ${formatLimitValue(item.metric, item.observed_value)}, but no configured policy exists for this scope.`
        : `Configured ${formatLimitValue(item.metric, configured)} is above the learned upstream cap of ${formatLimitValue(item.metric, item.observed_value)}. The live effective limit is already constrained by the observed value.`

      return {
        item,
        scopeType,
        configured,
        actionKind,
        actionLabel,
        scopeName: scopeDisplayName(scopeType, item.scope_id),
        editName: scopeEditName(scopeType, item.scope_id),
        sourceLine: `${item.source_header || 'Unknown source header'} · learned ${dateTime(item.observed_at)}${item.expires_at ? ` · expires ${dateTime(item.expires_at)}` : ''}`,
        summary
      }
    })
    .filter((item) => item.actionKind !== 'none')
    .sort((a, b) => {
      const severity = (kind: string) => (kind === 'tighten' ? 0 : 1)
      const severityDiff = severity(a.actionKind) - severity(b.actionKind)
      if (severityDiff !== 0) return severityDiff
      return new Date(b.item.observed_at).getTime() - new Date(a.item.observed_at).getTime()
    })
})

function openEdit(scopeType: 'provider' | 'endpoint', scopeId: string, name: string) {
  if (scopeType === 'endpoint') {
    modelEditEndpointID.value = scopeId
    return
  }

  editTarget.value = { scopeType, scopeId, name }
  for (const metric of metricsForEditing) {
    for (const period of editablePeriods(metric)) {
      editValues[fieldKey(metric, period)] = editInputValue(metric, configuredValue(scopeType, scopeId, metric, period))
    }
  }
  editOpen.value = true
}

function closeModelEdit() {
  modelEditEndpointID.value = ''
  void pageData.load('limits')
}

async function applyObservedLimit(item: ObservedLimit) {
  if (!canManageLimits.value) return
  if (!isEditableScope(item.scope_type)) return

  const key = observedKey(item)
  applyingObservedKey.value = key

  try {
    const existing = catalog.limitPolicies.find((policy) =>
      policy.scope_type === item.scope_type &&
      policy.scope_id === item.scope_id &&
      policy.metric === item.metric &&
      policy.period === item.period
    )

    const payload = {
      scope_type: item.scope_type,
      scope_id: item.scope_id,
      metric: item.metric,
      period: item.period,
      limit_value: item.observed_value,
      enabled: true,
      source: 'configured'
    }

    if (existing) await api.put(`/api/limit-policies/${existing.id}`, { ...existing, ...payload })
    else await api.post('/api/limit-policies', payload)

    await pageData.load('limits')
    app.pushToast({
      title: 'Observed limit adopted',
      description: `${scopeDisplayName(item.scope_type, item.scope_id)} now has a configured ${metricLabel(item.metric).toLowerCase()} / ${periodLabel(item.period)} cap of ${formatLimitValue(item.metric, item.observed_value)}.`
    })
  } finally {
    applyingObservedKey.value = ''
  }
}

async function saveLimits() {
  if (!canManageLimits.value) return
  if (!editTarget.value) return
  const { scopeType, scopeId } = editTarget.value

  for (const metric of metricsForEditing) {
    for (const period of editablePeriods(metric)) {
      const raw = editValues[fieldKey(metric, period)]
      const existing = catalog.limitPolicies.find((item) =>
        item.scope_type === scopeType &&
        item.scope_id === scopeId &&
        item.metric === metric &&
        item.period === period
      )
      const parsed = parseLimitInput(metric, raw)
      if (!raw || !Number.isFinite(parsed) || parsed <= 0) {
        if (existing) await api.del(`/api/limit-policies/${existing.id}`)
        continue
      }
      const payload = {
        scope_type: scopeType,
        scope_id: scopeId,
        metric,
        period,
        limit_value: parsed,
        enabled: true,
        source: 'configured'
      }
      if (existing) await api.put(`/api/limit-policies/${existing.id}`, { ...existing, ...payload })
      else await api.post('/api/limit-policies', payload)
    }
  }

  await pageData.load('limits')
  editOpen.value = false
  app.pushToast({ title: 'Limits saved', description: 'The selected provider or model limits were updated.' })
}

onMounted(() => {
  void pageData.load('limits')
  void capacity.refresh()
})
</script>

<template>
  <div>
    <div class="space-y-8">
      <div>
        <h1 class="app-title">Limits</h1>
      </div>

      <section v-if="observedLimitsUIEnabled" class="space-y-4">
        <header class="flex items-center justify-between gap-4">
          <h2 class="text-xl font-semibold tracking-tight text-white">Observed Limits Needing Action</h2>
          <UiBadge tone="slate" size="sm">{{ observedActionItems.length }} pending</UiBadge>
        </header>

        <UiEmptyState
          v-if="!observedActionItems.length"
          title="No learned upstream caps need action"
          description="New or stricter upstream caps will appear here."
        />

        <div v-else class="divide-y divide-[var(--app-border)] border-y border-[var(--app-border)] bg-[var(--app-table-bg)]">
          <div
            v-for="entry in observedActionItems"
            :key="observedKey(entry.item)"
            class="py-4"
          >
            <div class="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <p class="font-medium text-white">{{ entry.scopeName }}</p>
                  <UiBadge tone="slate" size="sm">{{ entry.item.scope_type === 'provider' ? 'Provider' : 'Model' }}</UiBadge>
                  <UiBadge tone="slate" size="sm">{{ metricLabel(entry.item.metric) }} / {{ periodLabel(entry.item.period) }}</UiBadge>
                  <UiBadge :tone="entry.actionKind === 'tighten' ? 'amber' : 'sky'" size="sm">{{ entry.actionLabel }}</UiBadge>
                </div>
                <p class="mt-3 text-sm leading-6 text-slate-200/80">{{ entry.summary }}</p>
                <p class="mt-2 text-xs leading-5 text-slate-400">{{ entry.sourceLine }}</p>
              </div>

              <div class="grid w-full grid-cols-2 gap-6 border-t border-[var(--app-border)] pt-3 text-sm xl:w-auto xl:min-w-[18rem] xl:border-l xl:border-t-0 xl:pl-5 xl:pt-0 xl:text-right">
                <div>
                  <p class="text-[11px] uppercase tracking-[0.18em] text-slate-500">Observed</p>
                  <p class="mt-1 font-semibold text-white">{{ formatLimitValue(entry.item.metric, entry.item.observed_value) }}</p>
                </div>
                <div>
                  <p class="text-[11px] uppercase tracking-[0.18em] text-slate-500">Configured</p>
                  <p class="mt-1 font-medium text-slate-200/84">{{ formatLimitValue(entry.item.metric, entry.configured) }}</p>
                </div>
              </div>
            </div>

            <div class="mt-4 flex flex-col gap-3 sm:flex-row sm:flex-wrap">
              <UiButton
                v-if="canManageLimits"
                class="w-full sm:w-auto"
                tone="secondary"
                size="sm"
                :disabled="applyingObservedKey === observedKey(entry.item)"
                @click="applyObservedLimit(entry.item)"
              >
                {{ applyingObservedKey === observedKey(entry.item) ? 'Applying...' : 'Apply observed cap' }}
              </UiButton>
              <UiButton class="w-full sm:w-auto" tone="ghost" size="sm" @click="openEdit(entry.scopeType, entry.item.scope_id, entry.editName)">
                {{ canManageLimits ? 'Review scope' : 'View scope' }}
              </UiButton>
            </div>
          </div>
        </div>
      </section>

      <section class="space-y-4">
        <header>
          <h2 class="text-xl font-semibold tracking-tight text-white">Provider and Model Limits</h2>
          <p class="mt-1 text-sm leading-6 text-slate-300/82">Configured and learned caps across each provider and model.</p>
        </header>
        <UiEmptyState
          v-if="!rows.length"
          title="No providers or models to limit yet"
          description="Add a provider and model before editing limits."
        />

        <div v-else class="ui-table-wrap max-w-full overflow-x-auto rounded-lg border">
          <table class="limits-matrix ui-table w-full min-w-[960px] table-fixed border-separate border-spacing-0">
            <colgroup>
              <col class="limits-matrix__scope-column">
              <col v-for="index in 14" :key="`limit-column-${index}`" class="limits-matrix__value-column">
              <col class="limits-matrix__action-column">
            </colgroup>
            <thead>
              <tr>
                <th rowspan="2" class="ui-table-head-cell border-r border-[var(--app-border-strong)] text-left">
                  Provider / Model
                </th>
                <th colspan="5" class="ui-table-head-cell border-r border-[var(--app-border-strong)] text-center">
                  Requests limit per
                </th>
                <th colspan="5" class="ui-table-head-cell border-r border-[var(--app-border-strong)] text-center">
                  Token limit per
                </th>
                <th colspan="4" class="ui-table-head-cell border-r border-[var(--app-border-strong)] text-center">
                  Spend limit per
                </th>
                <th rowspan="2" class="ui-table-head-cell text-center">
                  Actions
                </th>
              </tr>
              <tr>
                <th
                  v-for="(column, index) in ['Sec', 'Min', 'Hour', 'Day', 'Month', 'Sec', 'Min', 'Hour', 'Day', 'Month', 'Min', 'Hour', 'Day', 'Month']"
                  :key="`${column}-${index}`"
                  class="ui-table-head-cell border-r border-[var(--app-border-soft)] text-center"
                  :class="{
                    'border-l border-l-[var(--app-border-strong)]': index === 0 || index === 5 || index === 10,
                    'border-r border-r-[var(--app-border-strong)]': column === 'Month'
                  }"
                >
                  {{ column }}
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in rows" :key="row.key" :class="row.scopeType === 'provider' ? 'limit-provider-row' : ''">
                <td class="overflow-hidden border-r border-[var(--app-border-strong)]">
                  <div class="min-w-0">
                    <template v-if="row.modelName === 'Provider default'">
                      <p class="truncate" :title="`${row.providerName} (provider wide)`">
                        <span class="font-semibold text-[var(--app-heading)]">{{ row.providerName }}</span>
                        <span class="app-faint-text"> (provider wide)</span>
                      </p>
                    </template>
                    <template v-else>
                      <p class="flex min-w-0 items-center gap-1.5 pl-3 font-medium text-[var(--app-heading)]" :title="`${row.providerName} / ${row.modelName}`">
                        <span class="font-mono text-xs text-[var(--app-faint)]" aria-hidden="true">↳</span>
                        <span class="truncate">{{ row.providerName }} <span class="app-faint-text">/</span> <span class="app-copy-text">{{ row.modelName }}</span></span>
                      </p>
                    </template>
                  </div>
                </td>

                <template v-for="metric in metricsForEditing" :key="metric">
                  <td
                    v-for="period in editablePeriods(metric)"
                    :key="`${row.key}-${metric}-${period}`"
                    class="border-r border-[var(--app-border-soft)] text-center transition-colors duration-300"
                    :class="[
                      {
                        'border-l border-l-[var(--app-border-strong)]': (metric === 'requests' && period === 'second') || (metric === 'tokens' && period === 'second') || (metric === 'spend' && period === 'minute'),
                        'border-r border-r-[var(--app-border-strong)]': period === 'month'
                      },
                      limitCellClass(row.scopeType, row.scopeId, metric, period, row.values[fieldKey(metric, period)]?.effective)
                    ]"
                    :title="`Configured ${formatLimitValue(metric, row.values[fieldKey(metric, period)]?.configured)} · Observed ${formatLimitValue(metric, row.values[fieldKey(metric, period)]?.observed)} · Effective ${formatLimitValue(metric, row.values[fieldKey(metric, period)]?.effective)}`"
                  >
                    <div
                      class="whitespace-nowrap text-[11px] font-medium tabular-nums"
                      :class="row.values[fieldKey(metric, period)]?.effective == null ? 'font-medium text-[var(--app-faint)]' : 'font-medium text-[var(--app-heading)]'"
                    >
                      {{ formatLimitValue(metric, row.values[fieldKey(metric, period)]?.effective) }}
                    </div>
                    <div
                      v-if="limitCellUsagePercent(row.scopeType, row.scopeId, metric, period, row.values[fieldKey(metric, period)]?.effective) !== null"
                      class="app-faint-text mt-0.5 whitespace-nowrap text-[8px] font-semibold tabular-nums"
                    >
                      {{ limitCellUsagePercent(row.scopeType, row.scopeId, metric, period, row.values[fieldKey(metric, period)]?.effective) }}% used
                    </div>
                  </td>
                </template>

                <td class="whitespace-nowrap border-l border-[var(--app-border-strong)] text-center">
                  <UiButton tone="secondary" size="xs" @click="openEdit(row.scopeType, row.scopeId, row.modelName === 'Provider default' ? row.providerName : `${row.providerName} · ${row.modelName}`)">
                    {{ canManageLimits ? 'Edit' : 'View' }}
                  </UiButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <slot name="after-provider-model-limits" />
    </div>

    <UiInspectorDrawer
      :open="editOpen"
      :title="editTarget?.name || (canManageLimits ? 'Edit limits' : 'View limits')"
      :subtitle="canManageLimits ? 'Leave a field blank to remove that explicit provider-wide limit. Observed limits remain read-only and continue to influence the live effective cap.' : 'Configured and observed provider-wide limits are shown read-only for this scope.'"
      @close="editOpen = false"
    >
      <div class="space-y-8">
        <section
          v-for="metric in metricsForEditing"
          :key="metric"
          class="border-b border-[var(--app-border)] pb-6 last:border-b-0 last:pb-0"
        >
          <header class="mb-4">
            <h3 class="app-heading-text font-semibold">{{ metricLabel(metric) }}</h3>
            <p class="app-copy-text mt-1 text-sm">Edit the configured values for this scope across time windows.</p>
          </header>
          <div class="grid gap-5 md:auto-rows-fr md:grid-cols-2 xl:grid-cols-5">
            <UiField
              v-for="period in editablePeriods(metric)"
              :key="`${metric}-${period}`"
              :label="`${metricLabel(metric)} / ${periodLabel(period)}`"
              :help="metric === 'spend' ? `USD spend ceiling per ${periodLabel(period)}. Saved internally as micros.` : `Configured ${metricLabel(metric).toLowerCase()} limit per ${periodLabel(period)}.`"
              optional
            >
              <input
                v-model="editValues[fieldKey(metric, period)]"
                :type="metric === 'spend' ? 'number' : 'text'"
                :step="metric === 'spend' ? '0.000001' : undefined"
                min="0"
                class="app-input"
                :inputmode="metric === 'spend' ? 'decimal' : 'numeric'"
                :placeholder="metric === 'spend' ? '1.00' : '1000'"
                :disabled="!canManageLimits"
              />
            </UiField>
          </div>
        </section>
      </div>

      <template #footer>
        <div class="flex justify-end">
          <UiButton v-if="canManageLimits" class="w-full sm:w-auto" @click="saveLimits">Save Limits</UiButton>
        </div>
      </template>
    </UiInspectorDrawer>

    <ModelsModelEditDrawer
      :open="!!modelEditEndpointID"
      :endpoint-id="modelEditEndpointID"
      :readonly="!canManageLimits"
      variant="limits"
      subtitle="Edit model limits, pacing, token estimates, and spend estimate behavior without leaving the Limits page."
      @close="closeModelEdit"
    />
  </div>
</template>

<style scoped>
.limit-provider-row {
  background: var(--app-surface-muted);
}

.limit-provider-row td {
  border-top-color: var(--app-border-strong);
}

.limits-matrix__scope-column {
  width: 11.5rem;
}

.limits-matrix__value-column {
  width: 3rem;
}

.limits-matrix__action-column {
  width: 4rem;
}

.limits-matrix :where(th, td) {
  padding: 0.375rem 0.25rem;
  font-size: 0.6875rem;
  line-height: 1rem;
}

.limits-matrix :where(th:first-child, td:first-child) {
  padding-inline: 0.625rem;
}

.limits-matrix :where(th:last-child, td:last-child) {
  padding-inline: 0.25rem;
}
</style>
