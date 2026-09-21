<script setup lang="ts">
const queue = useQueueStore()
const pageData = useAdminPageDataStore()
const selected = ref<any>(null)
const tab = ref<'all' | 'waiting' | 'ready' | 'in_flight' | 'failed' | 'cancelled'>('all')
const { durationMs, relativeTime, currencyMicros } = useFormatters()
const { explainCandidate, groupedLimitImpact } = useRelayUi()
const relayPermissions = useModelRelayPermissions()
const canManageQueue = computed(() => relayPermissions.has('relay:queue:manage'))

const selectedCandidate = computed(() => selected.value?.candidate_trace?.find((item: any) => item.decision === 'selected') || null)
const selectedLimits = computed(() => groupedLimitImpact(selectedCandidate.value?.effective_limits || []))
const visibleItems = computed(() => queue.byState(tab.value))
const queueDepth = computed(() => queue.items.length)
const waitingCount = computed(() => queue.items.filter(item => item.state === 'waiting').length)
const readyCount = computed(() => queue.items.filter(item => item.state === 'ready').length)
const inFlightCount = computed(() => queue.items.filter(item => item.state === 'in_flight').length)
const queuedSpend = computed(() =>
  queue.items.reduce((sum, item) => sum + Number(item.estimated_cost_micros || 0), 0)
)
const queueSummaryCards = computed(() => [
  { label: 'Queue depth', value: queueDepth.value, hint: 'Live queued items' },
  { label: 'Waiting', value: waitingCount.value, hint: 'Delayed by pacing' },
  { label: 'Ready', value: readyCount.value, hint: 'Eligible for dispatch' },
  { label: 'In flight', value: inFlightCount.value, hint: 'Requests currently running' },
  { label: 'Oldest wait', value: durationMs(queue.oldestAgeMs), hint: 'Longest live queue wait' },
  { label: 'Average wait', value: durationMs(queue.averageWaitMs), hint: 'Mean live queue wait' },
  { label: 'Queued spend', value: currencyMicros(queuedSpend.value), hint: 'Estimated cost of live queued work' }
])

function canDelete(state: string) {
  return canManageQueue.value && !!state
}

onMounted(async () => {
  await pageData.load('queue')
})
</script>

<template>
  <div class="space-y-8">
    <div>
      <h1 class="app-title">Queue</h1>
    </div>

    <UiTabs
      v-model="tab"
      :tabs="[
        { value: 'all', label: 'All' },
        { value: 'waiting', label: 'Waiting' },
        { value: 'ready', label: 'Ready' },
        { value: 'in_flight', label: 'In flight' },
        { value: 'failed', label: 'Failed' },
        { value: 'cancelled', label: 'Cancelled' }
      ]"
    />

    <div class="ui-metric-strip">
        <div v-for="item in queueSummaryCards" :key="item.label" class="ui-metric-strip__item">
          <p class="text-xs uppercase tracking-[0.2em] text-slate-400">{{ item.label }}</p>
          <p class="mt-1 text-xl font-semibold text-white">{{ item.value }}</p>
          <p class="mt-1 text-xs leading-5 text-slate-300/68">{{ item.hint }}</p>
        </div>
    </div>

    <div class="grid gap-6 xl:grid-cols-[1fr_0.9fr]">
      <section class="min-w-0 space-y-4">
        <h2 class="app-heading-text text-lg font-semibold">Queue Items</h2>
        <UiEmptyState
          v-if="!visibleItems.length"
          title="No queue items in this state"
          description="Matching queue items will appear here."
        />

        <UiTable v-else :columns="['Request', 'Group', 'Model', 'State', 'Wait', 'Eligible', 'Estimated cost', 'Actions']">
          <tr v-for="item in visibleItems" :key="item.task_id">
            <td class="font-mono text-xs text-slate-300">{{ item.request_id }}</td>
            <td class="text-slate-200/84">{{ item.lane || '—' }}</td>
            <td class="text-slate-200/84">{{ item.endpoint_name }}</td>
            <td>
              <div class="space-y-1">
                <UiBadge tone="sky">{{ item.state.replaceAll('_', ' ') }}</UiBadge>
                <p v-if="item.delay_reason" class="text-xs leading-5 text-amber-300/80">{{ item.delay_reason }}</p>
              </div>
            </td>
            <td class="text-slate-200/84">{{ durationMs(item.wait_ms) }}</td>
            <td class="text-slate-200/84">{{ relativeTime(item.predicted_eligible_at) }}</td>
            <td class="text-slate-200/84">{{ currencyMicros(item.estimated_cost_micros) }}</td>
            <td>
              <div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap">
                <UiButton tone="secondary" size="sm" @click="selected = item">Inspect</UiButton>
                <UiButton v-if="canDelete(item.state)" tone="danger" size="sm" @click="queue.remove(item.task_id)">Delete</UiButton>
              </div>
            </td>
          </tr>
        </UiTable>
      </section>

      <section class="min-w-0 space-y-4">
        <h2 class="app-heading-text text-lg font-semibold">Queue Timeline</h2>
        <div class="divide-y divide-[var(--app-border)] border-y border-[var(--app-border)] bg-[var(--app-table-bg)]">
          <div
            v-for="item in [...queue.items].sort((a, b) => a.predicted_eligible_at.localeCompare(b.predicted_eligible_at)).slice(0, 12)"
            :key="item.task_id"
            class="px-1 py-4 sm:px-3"
          >
            <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
              <div class="min-w-0">
                <p class="font-medium text-white">{{ item.endpoint_name }}</p>
                <p class="mt-1 text-sm text-slate-300/72">{{ item.incoming_model || item.lane || 'Unnamed request' }}</p>
              </div>
              <UiBadge tone="amber">{{ relativeTime(item.predicted_eligible_at) }}</UiBadge>
            </div>
            <p class="mt-3 text-sm text-slate-200/78">Waiting {{ durationMs(item.wait_ms) }} so far · fallback count {{ item.fallback_count }}</p>
          </div>
        </div>
      </section>
    </div>

    <UiInspectorDrawer :open="!!selected" :title="selected?.endpoint_name || 'Queue item'" :subtitle="selected?.request_id" @close="selected = null">
      <div v-if="selected" class="space-y-6">
        <section class="border-b border-[var(--app-border)] pb-5">
          <h3 class="app-heading-text mb-3 font-semibold">Queue item summary</h3>
          <div class="space-y-3 text-sm">
            <div class="app-kv">
              <dt>Request ID</dt>
              <dd class="font-mono text-xs">{{ selected.request_id }}</dd>
            </div>
            <div class="app-kv">
              <dt>Group</dt>
              <dd>{{ selected.lane || '—' }}</dd>
            </div>
            <div class="app-kv">
              <dt>Model</dt>
              <dd>{{ selected.endpoint_name }}</dd>
            </div>
            <div class="app-kv">
              <dt>State</dt>
              <dd>{{ selected.state.replaceAll('_', ' ') }}</dd>
            </div>
            <div class="app-kv">
              <dt>Wait so far</dt>
              <dd>{{ durationMs(selected.wait_ms) }}</dd>
            </div>
            <div class="app-kv border-b-0 pb-0">
              <dt>Predicted eligible</dt>
              <dd>{{ relativeTime(selected.predicted_eligible_at) }}</dd>
            </div>
            <div v-if="selected.delay_reason" class="app-kv border-b-0 pb-0">
              <dt>Delay reason</dt>
              <dd>{{ selected.delay_reason }}</dd>
            </div>
          </div>
        </section>

        <section class="border-b border-[var(--app-border)] pb-5">
          <h3 class="app-heading-text font-semibold">Routing decision</h3>
          <p class="app-copy-text mt-1 text-sm">The chosen candidate and the reason it was accepted.</p>
          <div v-if="selectedCandidate" class="mt-4 border-y border-[var(--app-border)] py-4">
            <div class="flex items-start justify-between gap-4">
              <div>
                <p class="font-medium text-white">{{ selectedCandidate.endpoint_name }}</p>
                <p class="mt-1 text-sm text-slate-300/72">{{ selectedCandidate.upstream_model }} · rank #{{ selectedCandidate.rank }}</p>
              </div>
              <UiBadge tone="emerald">selected</UiBadge>
            </div>
            <p class="mt-3 text-sm leading-6 text-slate-200/78">{{ explainCandidate(selectedCandidate) }}</p>
            <p class="mt-2 text-xs text-slate-400">Predicted wait {{ durationMs(selectedCandidate.predicted_wait_ms) }}</p>
          </div>
        </section>

        <section class="border-b border-[var(--app-border)] pb-5">
          <h3 class="app-heading-text font-semibold">Effective limits</h3>
          <p class="app-copy-text mt-1 text-sm">Provider and model scope detail only, so the inheritance story matches the configured objects.</p>
          <div v-if="selectedLimits.length" class="mt-4 divide-y divide-[var(--app-border)] border-y border-[var(--app-border)]">
            <div v-for="item in selectedLimits" :key="`${item.scope_type}-${item.scope_id}-${item.metric}-${item.period}`" class="py-4">
              <div class="flex items-start justify-between gap-4">
                <div>
                  <p class="font-medium text-white">{{ item.metric }} / {{ item.period }}</p>
                  <p class="mt-1 text-sm text-slate-300/72">{{ item.scopeName }}</p>
                </div>
                <div class="text-right text-sm leading-6">
                  <p class="text-slate-200/78">Configured {{ item.configured ?? '—' }} · Observed {{ item.observed ?? '—' }}</p>
                  <p class="font-medium text-sky-300">Effective {{ item.effective ?? '—' }}</p>
                </div>
              </div>
            </div>
          </div>
          <p v-else class="text-sm leading-6 text-slate-300/72">No provider/model limit detail was captured for this queued item.</p>
        </section>

        <details class="border-b border-[var(--app-border)] pb-5">
          <summary class="cursor-pointer list-none">
            <div class="flex items-center justify-between">
              <span class="text-sm font-semibold text-white">Raw queue item</span>
              <span class="text-xs uppercase tracking-[0.2em] text-slate-500">Debug</span>
            </div>
          </summary>
          <div class="mt-4">
            <UiJsonViewer :value="selected" />
          </div>
        </details>
      </div>
    </UiInspectorDrawer>
  </div>
</template>
