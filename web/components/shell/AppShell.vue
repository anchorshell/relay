<script setup lang="ts">
const auth = useAuthStore()
const catalog = useCatalogStore()
const telemetry = useTelemetryStore()
const route = useRoute()
const { isDark } = useSiteTheme()
const sidebarOpen = ref(false)
const logoSrc = computed(() => isDark.value ? '/anchorshell-logo-dark.png' : '/anchorshell-logo-light-high-contrast.png')
let cooldownTimer: ReturnType<typeof window.setInterval> | null = null

onMounted(() => {
  if (!auth.authenticated) return
  if (process.client) {
    cooldownTimer = window.setInterval(() => {
      catalog.normalizeCooldowns()
    }, 1000)
  }
  telemetry.connect()
})

onBeforeUnmount(() => {
  if (cooldownTimer && process.client) {
    window.clearInterval(cooldownTimer)
    cooldownTimer = null
  }
  if (process.client) {
    document.body.style.overflow = ''
  }
  telemetry.disconnect()
})

watch(() => route.fullPath, () => {
  sidebarOpen.value = false
})

watch(sidebarOpen, (open) => {
  if (!process.client) return
  document.body.style.overflow = open ? 'hidden' : ''
})
</script>

<template>
  <div class="app-shell min-h-screen lg:flex">
    <ShellSidebarNav :mobile-open="sidebarOpen" @close="sidebarOpen = false" />

    <div class="min-w-0 flex-1">
      <header class="app-mobile-topbar sticky top-0 z-50 flex items-center justify-between gap-3 border-b px-4 py-3 backdrop-blur lg:hidden">
        <div class="flex min-w-0 items-center gap-3">
          <img
            class="h-9 w-9 shrink-0 rounded-lg object-cover"
            :src="logoSrc"
            width="36"
            height="36"
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
        <button
          type="button"
          class="app-mobile-menu-button inline-flex h-10 w-10 items-center justify-center rounded-xl border transition"
          aria-label="Open navigation menu"
          @click="sidebarOpen = true"
        >
          <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" aria-hidden="true">
            <path d="M5 7h14" />
            <path d="M5 12h14" />
            <path d="M5 17h14" />
          </svg>
        </button>
      </header>

      <main class="px-4 py-5 sm:px-6 sm:py-7 xl:px-10">
        <div class="mx-auto max-w-[1460px]">
          <slot />
        </div>
      </main>
    </div>
  </div>
</template>
