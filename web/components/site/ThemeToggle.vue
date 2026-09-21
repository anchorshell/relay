<script setup lang="ts">
const { isDark, toggleTheme } = useSiteTheme()

const label = computed(() => isDark.value ? 'Switch to light mode' : 'Switch to dark mode')
</script>

<template>
  <button
    class="site-theme-toggle"
    type="button"
    :aria-label="label"
    :title="label"
    :aria-pressed="isDark"
    @click="toggleTheme"
  >
    <span class="site-theme-toggle-track" aria-hidden="true">
      <span class="site-theme-toggle-icon site-theme-toggle-icon-light">
        <svg viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="4" stroke="currentColor" stroke-width="1.8" />
          <path d="M12 2.8v2.4M12 18.8v2.4M4.2 4.2l1.7 1.7M18.1 18.1l1.7 1.7M2.8 12h2.4M18.8 12h2.4M4.2 19.8l1.7-1.7M18.1 5.9l1.7-1.7" stroke="currentColor" stroke-linecap="round" stroke-width="1.8" />
        </svg>
      </span>
      <span :class="['site-theme-toggle-thumb', isDark && 'site-theme-toggle-thumb-dark']" />
      <span class="site-theme-toggle-icon site-theme-toggle-icon-dark">
        <svg viewBox="0 0 24 24" fill="none">
          <path d="M20.1 14.4A7.7 7.7 0 0 1 9.6 3.9 8.4 8.4 0 1 0 20.1 14.4Z" stroke="currentColor" stroke-linejoin="round" stroke-width="1.8" />
        </svg>
      </span>
    </span>
  </button>
</template>
