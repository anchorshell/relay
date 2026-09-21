<script setup lang="ts">
import { limitProgressBand } from '../../utils/limitProgress'

type BadgeTone = 'slate' | 'emerald' | 'amber' | 'rose' | 'sky' | 'violet' | 'status-emerald' | 'status-amber' | 'status-rose'

type ModelLimit = {
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

const props = defineProps<{
  endpointId: string
  providerName: string
  modelName: string
  queueDepth: number
  cooldownRemaining: number
  healthStatus: string
  healthTone: string
  cooldownLabel?: string
  cooldownTone?: BadgeTone
  userLimitActive?: boolean
  apiKeyLimitActive?: boolean
  lastActiveLabel: string
  limits: ModelLimit[]
}>()

const catalog = useCatalogStore()
const effectiveGuardrails = computed(() => catalog.effectiveGuardrailsForEndpointAcrossGroups(props.endpointId))

const { number, currencyMicros, countdownMs } = useFormatters()
const badgeTone = computed(() => props.healthTone as BadgeTone)
const readinessLabel = computed(() => {
  if (props.cooldownRemaining > 0) {
    return `Cooldown ${countdownMs(props.cooldownRemaining)}`
  }
  if (props.healthStatus === 'rate_limited') {
    return 'Rate limited'
  }
  if (props.healthStatus === 'cooling_down') {
    return 'Cooling down'
  }
  return 'Ready now'
})

function limitValue(limit: ModelLimit, value: number) {
  return limit.metric === 'spend' ? currencyMicros(value) : number(value)
}

function limitDisplayPercent(limit: ModelLimit) {
  return limit.blocked ? 100 : Math.max(0, Math.min(100, Number(limit.percent || 0)))
}

function limitColorClass(limit: ModelLimit) {
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

function limitStatusLabel(limit: ModelLimit) {
  if (limit.blocked) {
    const remaining = Math.max(0, Number(limit.blockedRemainingMs || 0))
    return remaining > 0 ? `blocked ${countdownMs(remaining)}` : 'blocked'
  }
  return `${Math.round(limit.percent)}%`
}
</script>

<template>
  <div class="app-subsurface px-4 py-3">
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2"><p class="text-sm font-medium text-white">{{ providerName }} <span class="text-slate-500">/</span> <span class="text-slate-300/84">{{ modelName }}</span></p><GuardrailBadge v-if="effectiveGuardrails.length" :count="effectiveGuardrails.length" :sources="['Provider, model, or assigned group binding']" /></div>
        <p class="mt-1 text-xs leading-5 text-slate-300/72">
          {{ lastActiveLabel }}
          <span class="text-slate-500">·</span>
          Queue {{ number(queueDepth) }}
          <span class="text-slate-500">·</span>
          {{ readinessLabel }}
        </p>
      </div>
      <div class="flex flex-wrap items-center justify-end gap-2">
        <UiBadge :tone="badgeTone" size="sm">{{ healthStatus.replaceAll('_', ' ') }}</UiBadge>
        <UiBadge v-if="cooldownLabel" :tone="cooldownTone || 'amber'" size="sm">{{ cooldownLabel }}</UiBadge>
        <UiBadge v-if="userLimitActive" tone="amber" size="sm">user limit</UiBadge>
        <UiBadge v-if="apiKeyLimitActive" tone="amber" size="sm">API key limit</UiBadge>
      </div>
    </div>

    <div class="mt-3 space-y-2">
      <div v-if="limits.length" class="space-y-2">
        <div v-for="limit in limits" :key="limit.key" class="grid grid-cols-[auto_1fr] items-center gap-3">
          <div class="flex min-w-[78px] items-baseline gap-1">
            <slot name="limit-label" :limit="limit">
              <span class="text-[11px] font-semibold text-slate-200">{{ limit.label }}</span>
            </slot>
            <span class="text-[10px] text-slate-500">({{ limitValue(limit, limit.configured) }})</span>
          </div>
          <div class="space-y-1">
            <div class="h-1 overflow-hidden rounded-full bg-[var(--app-input-bg)] ring-1 ring-white/6">
              <div
                :class="[
                  'h-full rounded-full transition-[width,background-color] duration-500',
                  limitColorClass(limit)
                ]"
                :style="{ width: `${limitDisplayPercent(limit)}%` }"
              />
            </div>
            <div class="flex items-center justify-between text-[10px] text-slate-500">
              <span>{{ limitValue(limit, limit.used) }}</span>
              <span>{{ limitStatusLabel(limit) }}</span>
            </div>
          </div>
        </div>
      </div>
      <p v-else class="text-xs leading-5 text-slate-400">No configured limits on this model or provider yet.</p>
    </div>
  </div>
</template>
