<script setup lang="ts">
const props = withDefaults(defineProps<{
  mobileOpen?: boolean
}>(), {
  mobileOpen: false
})

const emit = defineEmits<{ close: [] }>()
const route = useRoute()
const auth = useAuthStore()
const telemetry = useTelemetryStore()
const { isDark } = useSiteTheme()
const logoSrc = computed(() => isDark.value ? '/anchorshell-logo-dark.png' : '/anchorshell-logo-light-high-contrast.png')
const themeLabel = computed(() => isDark.value ? 'Dark mode' : 'Light mode')
const items = [
  { label: 'Dashboard', to: '/dashboard', icon: ['M4 5.5h6.5v6H4z', 'M13.5 5.5H20v13h-6.5z', 'M4 14.5h6.5v4H4z'] },
  { label: 'Realtime', to: '/realtime', icon: ['M4 12h3l2-5 4 10 2-5h5', 'M5 19h14'] },
  { label: 'Providers', to: '/providers', icon: ['M6 6.5h12v4H6z', 'M6 13.5h12v4H6z', 'M8.5 8.5h.01', 'M8.5 15.5h.01'] },
  { label: 'Groups', to: '/groups', icon: ['M7 8.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z', 'M17 20.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z', 'M17 8.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z', 'M9.5 7.2h5', 'M8.7 9.8l6.6 5.4'] },
  { label: 'Guardrails', to: '/guardrails', icon: ['M12 3.5l7 3v5.2c0 4.2-2.8 7.3-7 8.8-4.2-1.5-7-4.6-7-8.8V6.5z', 'M9 12l2 2 4-4'] },
  { label: 'Limits', to: '/limits', icon: ['M5 7h14', 'M8 7v10', 'M5 17h14', 'M16 7v10'] },
  { label: 'Usage', to: '/usage', icon: ['M5 19V5', 'M5 19h14', 'M8 16v-4', 'M12 16V8', 'M16 16v-7'] },
  { label: 'Queue', to: '/queue', icon: ['M7 7h12', 'M7 12h12', 'M7 17h12', 'M4 7h.01', 'M4 12h.01', 'M4 17h.01'] },
  { label: 'Logs', to: '/requests', icon: ['M7 4.5h7l3 3v12H7z', 'M14 4.5v4h4', 'M9.5 12h5', 'M9.5 15.5h5'] },
  { label: 'Playground', to: '/playground', icon: ['M4.5 6.5h15v11h-15z', 'M7 10l2 2-2 2', 'M11.5 14h4.5'] },
  { label: 'Settings', to: '/settings', icon: ['M12 8.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7Z', 'M12 3.5v2', 'M12 18.5v2', 'M4.6 7.2l1.7 1', 'M17.7 15.8l1.7 1', 'M19.4 7.2l-1.7 1', 'M6.3 15.8l-1.7 1'] },
  { label: 'Setup', to: '/setup', icon: ['M14.5 4.5l5 5', 'M5 19l4.5-1 9-9-4-4-9 9z', 'M12.5 6.5l4 4'] }
] as const

function closeMobileNav() {
  emit('close')
}

function lockApp() {
  emit('close')
  auth.lock()
}
</script>

<template>
  <transition
    enter-active-class="transition-opacity duration-200 ease-out"
    enter-from-class="opacity-0"
    enter-to-class="opacity-100"
    leave-active-class="transition-opacity duration-150 ease-in"
    leave-from-class="opacity-100"
    leave-to-class="opacity-0"
  >
    <button
      v-if="props.mobileOpen"
      type="button"
      class="app-sidebar-backdrop fixed inset-0 z-40 lg:hidden"
      aria-label="Close navigation menu"
      @click="closeMobileNav"
    />
  </transition>

  <aside
    class="app-sidebar fixed inset-y-0 left-0 z-50 flex h-dvh w-[min(20rem,calc(100vw-1.5rem))] shrink-0 flex-col overflow-y-auto border-r px-4 py-4 backdrop-blur transition-transform duration-200 ease-out lg:sticky lg:top-0 lg:z-auto lg:h-screen lg:w-72 lg:translate-x-0 lg:py-5"
    :class="props.mobileOpen ? 'translate-x-0' : '-translate-x-full'"
  >
    <div class="flex justify-end lg:hidden">
      <button
        type="button"
        class="app-sidebar-close inline-flex h-9 w-9 items-center justify-center rounded-xl border transition"
        aria-label="Close navigation menu"
        @click="closeMobileNav"
      >
        <svg class="h-4.5 w-4.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" aria-hidden="true">
          <path d="M6 6l12 12" />
          <path d="M18 6L6 18" />
        </svg>
      </button>
    </div>

    <div class="app-sidebar-brand hidden border-b pb-5 lg:block">
      <div class="flex items-center gap-3">
        <img
          class="h-11 w-11 shrink-0 rounded-xl object-cover"
          :src="logoSrc"
          width="44"
          height="44"
          alt=""
          aria-hidden="true"
        />
        <div class="min-w-0">
          <SiteAnchorShellWordmark
            class="min-w-0 truncate"
            size="sm"
            tone="auto"
            :weight="700"
            tracking="-0.025em"
          />
          <p class="app-brand-kicker">Model Relay</p>
        </div>
      </div>
    </div>
    <nav class="mt-3 flex-none space-y-1.5 overflow-visible pr-0 lg:mt-8 lg:flex-1 lg:overflow-y-auto lg:pr-1">
      <NuxtLink
        v-for="item in items"
        :key="item.to"
        :to="item.to"
        class="app-sidebar-link flex items-center gap-3 rounded-2xl px-4 py-3 text-[15px] font-medium transition"
        :class="route.path === item.to ? 'app-sidebar-link-active' : ''"
        @click="closeMobileNav"
      >
        <svg
          class="h-[18px] w-[18px] shrink-0"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path v-for="path in item.icon" :key="path" :d="path" />
        </svg>
        <span>{{ item.label }}</span>
      </NuxtLink>
      <UpgradeFeatureButton feature="teams" navigation />
      <UpgradeFeatureButton feature="apiKeys" navigation />
    </nav>
    <div class="app-sidebar-footer mt-5 space-y-3 border-t pt-5">
      <div class="flex items-center justify-between gap-3">
        <span class="app-sidebar-footer-label">Connection</span>
        <UiBadge :tone="telemetry.connected ? 'emerald' : 'rose'" size="sm">
          {{ telemetry.connected ? 'Live' : 'Disconnected' }}
        </UiBadge>
      </div>

      <div class="flex items-center justify-between gap-3">
        <span class="app-sidebar-footer-label">{{ themeLabel }}</span>
        <SiteThemeToggle />
      </div>

      <UiButton class="w-full" tone="ghost" size="sm" @click="lockApp">Lock</UiButton>
    </div>
  </aside>
</template>
