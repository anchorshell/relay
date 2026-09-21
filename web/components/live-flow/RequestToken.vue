<script setup lang="ts">
type TokenTone = 'queued' | 'active' | 'fallback' | 'completed' | 'failed' | 'preview'

const props = withDefaults(defineProps<{
  title: string
  subtitle?: string
  detail?: string
  x: number
  y: number
  tone?: TokenTone
  compact?: boolean
  pulse?: boolean
}>(), {
  tone: 'queued',
  compact: false,
  pulse: false
})

const tokenStyle = computed(() => ({
  left: `${props.x}%`,
  top: `${props.y}%`
}))
</script>

<template>
  <div
    :class="[
      'flow-token',
      `flow-token-${tone}`,
      compact && 'flow-token-compact',
      pulse && 'flow-token-pulse'
    ]"
    :style="tokenStyle"
  >
    <span class="flow-token-orb" aria-hidden="true" />
    <span class="min-w-0">
      <span class="flow-token-title">{{ title }}</span>
      <span v-if="subtitle" class="flow-token-subtitle">{{ subtitle }}</span>
      <span v-if="detail" class="flow-token-detail">{{ detail }}</span>
    </span>
  </div>
</template>

<style scoped>
.flow-token {
  position: absolute;
  z-index: 17;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  width: 11.25rem;
  min-width: 11.25rem;
  max-width: 11.25rem;
  align-items: center;
  gap: 0.55rem;
  border: 1px solid color-mix(in srgb, var(--token-accent, var(--app-accent)) 34%, var(--app-border));
  border-radius: 0.72rem;
  background: var(--app-surface);
  padding: 0.48rem 0.62rem;
  color: var(--app-heading);
  box-shadow: none;
  transform: translate(-50%, -50%);
  transition:
    left 0.2s linear,
    top 0.2s linear,
    border-color 0.2s ease,
    box-shadow 0.2s ease,
    opacity 0.2s ease;
  animation: tokenEnter 0.24s ease both;
  will-change: left, top;
}

.flow-token-compact {
  width: 9.25rem;
  min-width: 9.25rem;
  max-width: 9.25rem;
}

.flow-token-orb {
  width: 0.78rem;
  height: 0.78rem;
  border-radius: 999px;
  background: var(--token-accent, var(--app-accent));
  box-shadow: none;
}

.flow-token-title,
.flow-token-subtitle,
.flow-token-detail {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.flow-token-title {
  font-size: 0.72rem;
  font-weight: 800;
  line-height: 0.95rem;
}

.flow-token-subtitle {
  margin-top: 0.1rem;
  color: var(--app-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.58rem;
  line-height: 0.78rem;
}

.flow-token-detail {
  margin-top: 0.1rem;
  color: color-mix(in srgb, var(--token-accent, var(--app-accent)) 82%, var(--app-heading));
  font-size: 0.6rem;
  font-weight: 700;
  line-height: 0.78rem;
}

.flow-token-active {
  --token-accent: #22c55e;
  border-color: color-mix(in srgb, #22c55e 62%, var(--app-border));
  box-shadow: none;
}

.flow-token-fallback {
  --token-accent: #f59e0b;
  border-color: rgba(245, 158, 11, 0.56);
  box-shadow: none;
}

.flow-token-fallback .flow-token-orb {
  background: #f59e0b;
  box-shadow: none;
}

.flow-token-completed {
  --token-accent: #10b981;
  border-color: rgba(16, 185, 129, 0.48);
  opacity: 0.92;
}

.flow-token-completed .flow-token-orb {
  background: #10b981;
}

.flow-token-failed {
  --token-accent: #f43f5e;
  border-color: rgba(244, 63, 94, 0.52);
}

.flow-token-failed .flow-token-orb {
  background: #f43f5e;
}

.flow-token-preview {
  --token-accent: var(--app-accent);
  border-color: color-mix(in srgb, var(--app-accent) 72%, var(--app-border));
}

.flow-token-pulse .flow-token-orb {
  animation: tokenPulse 1.35s ease-out infinite;
}

@keyframes tokenPulse {
  to {
    opacity: 0.72;
    transform: scale(1.18);
  }
}

@keyframes tokenEnter {
  from {
    opacity: 0;
    transform: translate(-50%, -45%) scale(0.96);
  }
}

@media (prefers-reduced-motion: reduce) {
  .flow-token {
    transition: none;
    animation: none;
  }

  .flow-token-pulse .flow-token-orb {
    animation: none;
  }
}
</style>
