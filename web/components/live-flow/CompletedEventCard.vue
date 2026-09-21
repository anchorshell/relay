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
const statusToneClass = computed(() => {
  const status = props.item.statusCode || 0
  if (status === 499) return 'live-flow-status-cancelled'
  if (status === 429) return 'live-flow-status-rate'
  if (status >= 400) return 'live-flow-status-error'
  if (status >= 200 && status < 300) return 'live-flow-status-ok'
  return ''
})
const tokenLines = computed(() => {
  const [up, down] = props.item.tokens.split(' · ')
  return [
    { direction: 'up', value: compactTokenLine(up, 'up') },
    { direction: 'down', value: compactTokenLine(down, 'down') }
  ].filter((line) => line.value)
})

const cardToneClass = computed(() => {
  const status = props.item.statusCode || 0
  if (status === 499) return 'live-flow-event-cancelled'
  if (status === 429) return 'live-flow-event-rate'
  if (status >= 400) return 'live-flow-event-error'
  if (props.item.state === 'failed') return 'live-flow-event-error'
  return 'live-flow-event-ok'
})

function compactTokenLine(value: string | undefined, direction: 'up' | 'down') {
  if (!value) return ''
  const approx = value.startsWith('~')
  const raw = approx ? value.slice(1) : value
  if (direction === 'up') {
    return raw.replace('Up ', approx ? '↑~ ' : '↑ ')
  }
  return raw.replace('Down ', approx ? '↓~ ' : '↓ ')
}
</script>

<template>
  <article :class="['live-flow-event-card', cardToneClass]">
    <div class="live-flow-event-main">
      <div class="min-w-0">
        <div class="live-flow-event-title-row">
          <div class="min-w-0">
            <slot v-if="$slots.requester" name="requester" />
            <div v-if="item.title || item.detail" class="live-flow-event-route-block">
              <p v-if="item.title" class="live-flow-event-route" :title="item.title">{{ item.title }}</p>
              <p v-if="item.detail" class="live-flow-event-route" :title="item.detail">{{ item.detail }}</p>
            </div>
          </div>
          <div class="live-flow-event-outcome">
            <UiBadge class="live-flow-event-state" :tone="badgeTone" size="sm">{{ stateLabel }}</UiBadge>
            <span v-if="item.statusCode" class="live-flow-status-code">
              <span>Status</span>
              <strong :class="statusToneClass">{{ item.statusCode }}</strong>
            </span>
          </div>
        </div>
      </div>
    </div>

    <div v-if="tokenLines.length" class="live-flow-event-metrics">
      <span v-for="line in tokenLines" :key="line.direction" class="live-flow-event-token">{{ line.value }}</span>
    </div>

    <div class="live-flow-event-timeline">
      <p v-if="item.queueTiming">{{ item.queueTiming }}</p>
      <p v-if="item.timing">{{ item.timing }}</p>
      <p v-if="item.phase">{{ item.phase }}</p>
    </div>
  </article>
</template>

<style scoped>
.live-flow-event-card {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 0.95rem;
  background: var(--app-surface);
  padding: 0.85rem;
  box-shadow: var(--app-subpanel-shadow);
}

.live-flow-event-card::before {
  content: "";
  position: absolute;
  inset: 0 auto 0 0;
  width: 0.18rem;
  background: var(--event-accent, var(--app-accent));
  opacity: 0.9;
}

.live-flow-event-main {
  display: block;
  min-width: 0;
}

.live-flow-event-title-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) max-content;
  min-width: 0;
  align-items: flex-start;
  column-gap: 0.75rem;
}

.live-flow-event-title-row > div {
  min-width: 0;
}

.live-flow-event-route-block {
  min-width: 0;
  margin-top: 0.28rem;
}

.live-flow-event-outcome {
  display: grid;
  min-width: max-content;
  justify-items: end;
  gap: 0.22rem;
  padding-right: 0.12rem;
}

.live-flow-event-state {
  white-space: nowrap;
}

.live-flow-status-code {
  display: inline-flex;
  justify-content: flex-end;
  gap: 0.25rem;
  color: var(--app-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.62rem;
  font-weight: 650;
  line-height: 0.85rem;
  padding-right: 0.18rem;
}

.live-flow-status-code strong {
  font-weight: 850;
}

.live-flow-status-ok {
  color: color-mix(in srgb, #10b981 70%, var(--app-heading));
}

.live-flow-status-rate {
  color: color-mix(in srgb, #f59e0b 72%, var(--app-heading));
}

.live-flow-status-cancelled {
  color: var(--app-muted);
}

.live-flow-status-error {
  color: color-mix(in srgb, #f43f5e 72%, var(--app-heading));
}

.live-flow-event-route {
  display: block;
  min-width: 0;
  margin-top: 0.12rem;
  overflow: hidden;
  color: var(--app-muted);
  font-size: 0.7rem;
  line-height: 1rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-flow-event-route:first-child {
  margin-top: 0;
}

.live-flow-event-metrics {
  display: flex;
  flex-wrap: wrap;
  gap: 0.6rem;
  margin-top: 0.62rem;
}

.live-flow-event-token {
  color: var(--app-copy);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.66rem;
  font-weight: 650;
  line-height: 0.9rem;
}

.live-flow-event-timeline {
  display: grid;
  gap: 0.22rem;
  margin-top: 0;
  color: var(--app-subtle);
  font-size: 0.68rem;
  line-height: 1.05rem;
}

.live-flow-event-rate,
.live-flow-event-warn {
  --event-accent: #f59e0b;
  border-color: rgba(245, 158, 11, 0.3);
}

.live-flow-event-error {
  --event-accent: #f43f5e;
  border-color: rgba(244, 63, 94, 0.34);
}

.live-flow-event-cancelled {
  --event-accent: #64748b;
  border-color: color-mix(in srgb, #64748b 34%, var(--app-border));
}

.live-flow-event-ok {
  --event-accent: #22c55e;
}
</style>
