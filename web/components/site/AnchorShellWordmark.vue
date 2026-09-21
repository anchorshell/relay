<script setup lang="ts">
type WordmarkSize = 'sm' | 'md' | 'lg' | 'display'
type WordmarkTone = 'auto' | 'on-light' | 'on-dark' | 'muted'
type WordmarkWeight = 600 | 700 | 800

const props = withDefaults(defineProps<{
  size?: WordmarkSize
  tone?: WordmarkTone
  weight?: WordmarkWeight
  tracking?: string
  anchorColor?: string
  shellColor?: string
}>(), {
  size: 'md',
  tone: 'auto',
  weight: 600,
  tracking: '0'
})

const sizeClasses: Record<WordmarkSize, string> = {
  sm: 'text-xl',
  md: 'text-2xl',
  lg: 'text-3xl',
  display: 'text-5xl'
}

const toneClasses: Record<WordmarkTone, string> = {
  auto: 'anchorshell-wordmark-auto',
  'on-light': 'anchorshell-wordmark-on-light',
  'on-dark': 'anchorshell-wordmark-on-dark',
  muted: 'anchorshell-wordmark-muted'
}

const wordmarkStyle = computed(() => {
  const styles: Record<string, string> = {
    '--wordmark-weight': String(props.weight),
    '--wordmark-tracking': props.tracking
  }

  if (props.anchorColor) {
    styles['--wordmark-anchor'] = props.anchorColor
  }

  if (props.shellColor) {
    styles['--wordmark-shell'] = props.shellColor
  }

  return styles
})
</script>

<template>
  <span
    :class="[
      'anchorshell-wordmark font-logo leading-none',
      sizeClasses[props.size],
      toneClasses[props.tone]
    ]"
    :style="wordmarkStyle"
  >
    <span class="anchorshell-wordmark-anchor">Anchor</span><span class="anchorshell-wordmark-shell">Shell</span>
  </span>
</template>
