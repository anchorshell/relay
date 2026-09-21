<script setup lang="ts">
type BadgeTone = 'slate' | 'emerald' | 'amber' | 'rose' | 'sky' | 'violet' | 'status-emerald' | 'status-amber' | 'status-rose'

const props = defineProps<{
  item: {
    id: string
    actorId?: string
    apiKeyUUID?: string
    state: string
    tone: string
    title: string
    detail: string
    queueTiming: string
    timing: string
    phase: string
    tokens: string
    statusCode?: number | null
  }
}>()

const badgeTone = computed<BadgeTone>(() => {
  const status = props.item.statusCode || 0
  if (status === 499) return 'slate'
  if (status === 429) return 'status-amber'
  if (status >= 400) return 'status-rose'
  if (status >= 200 && status < 300) return 'status-emerald'
  return props.item.tone as BadgeTone
})
const stateLabel = computed(() => {
  const status = props.item.statusCode || 0
  if (status === 499) return 'cancelled'
  if (status === 429) return 'rate limited'
  if (status === 401) return 'unauthorized'
  if (status === 403) return 'forbidden'
  if (status >= 500) return 'failed'
  if (status >= 400) return 'rejected'
  return props.item.state.replaceAll('_', ' ')
})

function activityTokenLine(value: string, direction: 'up' | 'down') {
  const [up, down] = value.split(' · ')
  const source = direction === 'up' ? up : down
  if (!source) return ''
  const isApprox = source.startsWith('~')
  const raw = isApprox ? source.slice(1) : source
  if (direction === 'up') {
    return raw.replace('Up ', isApprox ? '↑~ ' : '↑ ')
  }
  return raw.replace('Down ', isApprox ? '↓~ ' : '↓ ')
}

function statusCodeTextClass(statusCode?: number | null) {
  if (!statusCode) return 'text-slate-400'
  if (statusCode === 499) return 'text-slate-400'
  if (statusCode === 429) return 'text-amber-300'
  if (statusCode >= 500) return 'text-rose-300'
  if (statusCode >= 400) return 'text-rose-300'
  if (statusCode >= 200 && statusCode < 300) return 'text-emerald-300'
  return 'text-slate-400'
}
</script>

<template>
  <div class="app-subsurface px-4 py-3">
    <div class="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-start">
      <div class="min-w-0">
        <div v-if="$slots.requester"><slot name="requester" /></div>
        <div v-if="item.title || item.detail" :class="{ 'mt-2': $slots.requester }">
          <p v-if="item.title" class="truncate text-xs font-normal leading-5 text-slate-300/78">{{ item.title }}</p>
          <p
            v-if="item.detail"
            class="break-words text-xs font-normal leading-5 text-slate-300/68"
            :class="{ 'mt-0.5': item.title }"
          >
            {{ item.detail }}
          </p>
        </div>

        <div class="mt-2 grid gap-1 text-[12px] leading-5 text-slate-300/76">
          <p v-if="item.queueTiming">{{ item.queueTiming }}</p>
          <p v-if="item.timing">{{ item.timing }}</p>
          <p v-if="item.phase" class="text-slate-300/72">{{ item.phase }}</p>
        </div>
      </div>

      <div class="flex flex-col items-start gap-2 text-left md:items-end md:text-right">
        <UiBadge :tone="badgeTone" size="sm">{{ stateLabel }}</UiBadge>
        <div v-if="item.tokens" class="space-y-0.5 text-[11px] leading-5 text-slate-400">
          <p>{{ activityTokenLine(item.tokens, 'up') }}</p>
          <p>{{ activityTokenLine(item.tokens, 'down') }}</p>
        </div>
        <p v-if="item.statusCode" class="text-[11px] leading-5 text-white/88">
          <span class="text-white">Status </span>
          <span :class="statusCodeTextClass(item.statusCode)">{{ item.statusCode }}</span>
        </p>
      </div>
    </div>
  </div>
</template>
