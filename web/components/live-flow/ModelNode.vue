<script setup lang="ts">
import { limitProgressBand } from '../../utils/limitProgress'

type NodeTone = 'idle' | 'selected' | 'rejected' | 'muted' | 'active' | 'cooling'
type BadgeTone = 'slate' | 'emerald' | 'amber' | 'rose' | 'sky' | 'violet' | 'status-emerald' | 'status-amber' | 'status-rose'
type CircuitRouteVisualState = 'idle' | 'active' | 'holding' | 'blocked' | 'cooldown' | 'fallback-active' | 'completed' | 'cancelled' | 'error'

type LimitRow = {
  key: string
  label: string
  metric?: string
  period?: string
  source?: string
  actorId?: string
  scopeId?: string
  targetType?: string
  targetKey?: string
  configured: number
  used: number
  percent: number
  blocked?: boolean
  blockedRemainingMs?: number
}

type ModelBadge = {
  key: string
  label: string
  tone: BadgeTone
}

const { number, currencyMicros, countdownMs } = useFormatters()
const catalog = useCatalogStore()

const props = withDefaults(defineProps<{
  endpointId: string
  laneId?: string
  rank: number
  title: string
  provider: string
  status?: string
  statusTone?: BadgeTone
  tone?: NodeTone
  routeState?: CircuitRouteVisualState
  waitLabel?: string
  limits?: LimitRow[]
  badges?: ModelBadge[]
  x: number
  y: number
  setup?: boolean
}>(), {
  status: 'healthy',
  statusTone: 'slate',
  tone: 'idle',
  routeState: 'idle',
  waitLabel: 'ready',
  limits: () => [],
  badges: () => [],
  setup: false
})

const effectiveGuardrails = computed(() => props.laneId
  ? catalog.effectiveGuardrailsForEndpoint(props.endpointId, props.laneId)
  : catalog.effectiveGuardrailsForEndpointAcrossGroups(props.endpointId))

function limitFillStyle(limit: LimitRow) {
  const percent = limit.blocked ? 100 : Math.max(0, Math.min(100, Number(limit.percent) || 0))
  return { width: `${percent}%` }
}

function limitColorClass(limit: LimitRow) {
  const band = limitProgressBand(
    limit.percent,
    Boolean(limit.blocked) || (limit.configured > 0 && limit.used >= limit.configured)
  )
  if (band === 'full') return 'bg-red-700'
  if (band === 'critical') return 'bg-red-400/90'
  if (band === 'high') return 'bg-orange-400/90'
  if (band === 'medium') return 'bg-yellow-300/90'
  return 'bg-emerald-400/90'
}

function limitValue(limit: LimitRow, value: number) {
  return limit.metric === 'spend' ? currencyMicros(value) : number(value)
}

function limitStatusLabel(limit: LimitRow) {
  if (limit.blocked) {
    const remaining = Math.max(0, Number(limit.blockedRemainingMs || 0))
    return remaining > 0 ? `blocked ${countdownMs(remaining)}` : 'blocked'
  }
  return `${Math.round(limit.percent)}%`
}

</script>

<template>
  <article
    :class="[
      'flow-model-node',
      `flow-model-node-${tone}`,
      `flow-model-route-${routeState}`,
      setup && 'flow-model-node-setup'
    ]"
    :data-flow-model-id="endpointId"
    :data-flow-port="`model-${endpointId}-center`"
  >
    <span class="flow-model-port flow-model-port-in" :data-flow-port="`model-${endpointId}-in`" aria-hidden="true" />
    <span class="flow-model-port flow-model-port-out" :data-flow-port="`model-${endpointId}-out`" aria-hidden="true" />
    <div class="flow-model-header">
      <div class="flow-model-rank" :data-flow-port="`model-${endpointId}-rank`">#{{ rank }}</div>
      <div class="flow-model-heading">
        <div class="flow-model-identity">
          <h3 class="flow-model-title" :title="`${provider} / ${title}`">{{ provider }} / {{ title }}</h3>
        </div>
        <div class="flow-model-wait" :data-flow-port="`model-${endpointId}-status`">
          <slot name="status">{{ waitLabel }}</slot>
        </div>
      </div>
      <div class="flow-model-badges">
        <UiBadge class="flow-model-badge" :tone="statusTone" size="sm">{{ status.replaceAll('_', ' ') }}</UiBadge>
        <UiBadge v-for="badge in badges" :key="badge.key" class="flow-model-badge" :tone="badge.tone" size="sm">{{ badge.label }}</UiBadge>
        <GuardrailBadge v-if="effectiveGuardrails.length" :count="effectiveGuardrails.length" :sources="[laneId ? 'Group, provider, or model binding' : 'Provider, model, or assigned group binding']" />
      </div>
    </div>

    <div v-if="limits.length" class="flow-model-limits">
      <div v-for="limit in limits" :key="limit.key" class="flow-model-limit-row">
        <div class="flow-model-limit-label">
          <slot name="limit-label" :limit="limit">
            <span>{{ limit.label }}</span>
          </slot>
          <em>({{ limitValue(limit, limit.configured) }})</em>
        </div>
        <div class="flow-model-limit-track">
          <i :class="limitColorClass(limit)" :style="limitFillStyle(limit)" />
        </div>
        <strong>{{ limitValue(limit, limit.used) }}</strong>
        <em>{{ limitStatusLabel(limit) }}</em>
      </div>
    </div>

    <div v-if="setup && $slots.setup" class="flow-model-setup">
      <slot name="setup" />
    </div>
  </article>
</template>

<style scoped>
.flow-model-node {
  position: relative;
  z-index: 18;
  width: 100%;
  min-width: 0;
  overflow: visible;
  border: 1px solid color-mix(in srgb, var(--app-border-strong) 58%, var(--app-border));
  border-radius: 1.05rem;
  background: var(--app-surface);
  padding: 1rem 1rem 0.95rem;
  box-shadow: none;
  transition:
    border-color 0.2s ease,
    box-shadow 0.2s ease,
    opacity 0.2s ease;
}

.flow-model-port {
  position: absolute;
  top: 50%;
  z-index: 2;
  width: 0.72rem;
  height: 0.72rem;
  border: 1px solid color-mix(in srgb, var(--node-port-accent, var(--node-accent, var(--app-accent))) 56%, var(--app-border));
  border-radius: 999px;
  background: var(--app-surface);
  box-shadow: none;
  transform: translateY(-50%);
}

.flow-model-port-in {
  left: -0.36rem;
}

.flow-model-port-out {
  right: -0.36rem;
}

.flow-model-node-selected,
.flow-model-node-active {
  --node-accent: #22c55e;
  border-color: color-mix(in srgb, #22c55e 54%, var(--app-border));
  box-shadow: none;
}

.flow-model-node-selected .flow-model-rank,
.flow-model-node-active .flow-model-rank {
  background: color-mix(in srgb, #22c55e 18%, var(--app-secondary-bg));
  color: var(--app-heading);
}

.flow-model-node-selected .flow-model-wait,
.flow-model-node-active .flow-model-wait {
  color: #86efac;
}

.flow-model-node-selected .flow-model-rank::after,
.flow-model-node-active .flow-model-rank::after {
  content: "";
  position: absolute;
  inset: -0.28rem;
  border-radius: 999px;
  border: 1px solid color-mix(in srgb, #22c55e 34%, transparent);
  animation: nodeBeacon 1.6s ease-out infinite;
}

.flow-model-node-rejected {
  --node-accent: #f59e0b;
  border-color: rgba(245, 158, 11, 0.42);
}

.flow-model-node-cooling {
  --node-accent: #f59e0b;
  border-color: rgba(245, 158, 11, 0.64);
  box-shadow: none;
}

.flow-model-node-cooling .flow-model-wait {
  color: #f59e0b;
}

.flow-model-node-muted {
  opacity: 0.72;
}

.flow-model-route-idle {
  --node-port-accent: var(--app-accent);
}

.flow-model-route-active,
.flow-model-route-fallback-active,
.flow-model-route-completed {
  --node-port-accent: var(--app-accent);
}

.flow-model-route-holding,
.flow-model-route-blocked,
.flow-model-route-cancelled {
  --node-port-accent: #f59e0b;
}

.flow-model-route-cooldown {
  --node-port-accent: #f59e0b;
}

.flow-model-route-error {
  --node-port-accent: #f43f5e;
}

.flow-model-node-setup {
  width: 100%;
}

.flow-model-header {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  column-gap: 0.75rem;
  row-gap: 0.48rem;
  align-items: start;
}

.flow-model-heading {
  display: flex;
  min-height: 0;
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  justify-content: flex-start;
  gap: 0.18rem;
}

.flow-model-rank {
  position: relative;
  display: inline-flex;
  width: 2.05rem;
  height: 2.05rem;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 999px;
  border: 1px solid var(--app-border);
  background: var(--app-secondary-bg);
  color: var(--app-heading);
  font-size: 0.64rem;
  font-weight: 900;
  letter-spacing: 0.06em;
  line-height: 1;
  text-align: center;
}

.flow-model-identity {
  flex: 1 1 auto;
  min-width: 0;
}

.flow-model-title {
  max-width: 100%;
  overflow: hidden;
  color: var(--app-heading);
  font-size: 0.86rem;
  font-weight: 850;
  line-height: 1.12rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.flow-model-title span {
  color: var(--app-muted);
  font-weight: 700;
}

.flow-model-badges {
  display: flex;
  flex-wrap: wrap;
  grid-column: 1 / -1;
  gap: 0.35rem;
  width: 100%;
  min-width: 0;
}

.flow-model-badge {
  flex: 0 0 auto;
  white-space: nowrap;
}

.flow-model-wait {
  color: var(--app-muted);
}

.flow-model-wait {
  width: 100%;
  min-width: 0;
  font-size: 0.68rem;
  font-weight: 800;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.flow-model-limits {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 0.28rem;
  margin-top: 0.62rem;
  border: 1px solid color-mix(in srgb, var(--app-border) 86%, transparent);
  border-radius: 0.72rem;
  background: var(--app-surface-muted);
  padding: 0.45rem 0.52rem;
}

.flow-model-limit-row {
  display: grid;
  grid-template-columns: minmax(3.7rem, auto) minmax(0, 1fr) auto auto;
  gap: 0.34rem;
  align-items: center;
  color: var(--app-subtle);
  min-height: 1rem;
  font-size: 0.58rem;
  font-weight: 800;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.flow-model-limit-label {
  display: flex;
  min-width: 0;
  align-items: baseline;
  gap: 0.16rem;
  white-space: nowrap;
}

.flow-model-limit-label span {
  overflow: hidden;
  text-overflow: ellipsis;
}

.flow-model-limit-row strong {
  color: var(--app-heading);
  font-size: 0.58rem;
  letter-spacing: 0;
  text-transform: none;
  white-space: nowrap;
}

.flow-model-limit-row em {
  color: var(--app-muted);
  font-style: normal;
  font-size: 0.56rem;
  letter-spacing: 0;
  text-align: right;
  text-transform: none;
  white-space: nowrap;
}

.flow-model-limit-track {
  height: 0.25rem;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--app-border) 66%, transparent);
}

.flow-model-limit-track i {
  display: block;
  height: 100%;
  border-radius: inherit;
  transition:
    width 0.42s ease,
    background-color 0.2s ease;
}

.flow-model-setup {
  margin-top: 0.9rem;
  border-top: 1px solid var(--app-border);
  padding-top: 0.85rem;
}

@media (max-width: 1180px) {
  .flow-model-node,
  .flow-model-node-setup {
    width: 100%;
  }
}

@keyframes nodeBeacon {
  to {
    opacity: 0;
    transform: scale(1.35);
  }
}

@media (prefers-reduced-motion: reduce) {
  .flow-model-node,
  .flow-model-limit-track i {
    transition: none;
  }

  .flow-model-node-selected .flow-model-rank::after,
  .flow-model-node-active .flow-model-rank::after {
    animation: none;
  }
}
</style>
