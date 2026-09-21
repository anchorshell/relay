<script setup lang="ts">
import type { RequestTimingBreakdown } from '../../utils/requestTiming'
import { formatRequestTiming } from '../../utils/requestTiming'

withDefaults(defineProps<{
  timings: RequestTimingBreakdown
  size?: 'table' | 'inspector'
}>(), {
  size: 'table'
})
</script>

<template>
  <dl class="request-timing-list" :class="{ 'request-timing-list-inspector': size === 'inspector' }">
    <div><dt>Queue wait</dt><dd>{{ formatRequestTiming(timings.queueWaitMS) }}</dd></div>
    <div><dt>Characterization</dt><dd>{{ formatRequestTiming(timings.characterizationMS) }}</dd></div>
    <div><dt>Guardrails pre</dt><dd>{{ formatRequestTiming(timings.guardrailPreMS) }}</dd></div>
    <div><dt>Provider latency</dt><dd>{{ formatRequestTiming(timings.providerLatencyMS) }}</dd></div>
    <div><dt>Guardrails post</dt><dd>{{ formatRequestTiming(timings.guardrailPostMS) }}</dd></div>
    <div class="request-timing-total"><dt>Total time</dt><dd>{{ formatRequestTiming(timings.totalMS) }}</dd></div>
  </dl>
</template>

<style scoped>
.request-timing-list {
  display: grid;
  width: 100%;
  gap: 0.15rem;
  color: var(--app-muted);
  font-size: 0.66rem;
  line-height: 0.9rem;
}

.request-timing-list div {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.65rem;
}

.request-timing-list dd {
  color: var(--app-copy);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.request-timing-total {
  margin-top: 0.1rem;
  padding-top: 0.2rem;
  border-top: 1px solid var(--app-border);
  font-weight: 650;
}

.request-timing-list-inspector {
  gap: 0.25rem;
  font-size: 0.75rem;
  line-height: 1.05rem;
}
</style>
