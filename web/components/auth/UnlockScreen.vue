<script setup lang="ts">
const auth = useAuthStore()
const { isDark } = useSiteTheme()
const token = ref(auth.token)
const logoSrc = computed(() => isDark.value ? '/anchorshell-logo-dark.png' : '/anchorshell-logo-light-high-contrast.png')

async function submit() {
  try {
    await auth.unlock(token.value)
    const app = useAppStore()
    app.pushToast({ title: 'Admin console unlocked', description: 'Authenticated against the Model Relay admin API.', tone: 'success' })
  } catch {
    // inline error already set in store
  }
}
</script>

<template>
  <div class="app-shell flex min-h-screen items-center justify-center px-4 py-6 sm:px-6">
    <div class="app-surface w-full max-w-xl p-5 sm:p-8">
      <div class="flex items-start justify-between gap-3 sm:gap-4">
        <div class="flex min-w-0 items-center gap-3">
          <img
            class="h-12 w-12 shrink-0 rounded-xl object-cover"
            :src="logoSrc"
            width="48"
            height="48"
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
        <SiteThemeToggle />
      </div>
      <h1 class="app-heading-text mt-6 text-3xl font-semibold tracking-tight sm:text-4xl">Unlock the operator console</h1>
      <p class="app-copy-text mt-3 text-sm leading-7">
        Authenticate with the admin bearer token to manage providers, ranked models, queue pacing, spend controls, and live routing telemetry.
      </p>

      <form class="mt-8 space-y-5" @submit.prevent="submit">
        <div>
          <label class="app-heading-text mb-2 block text-sm font-medium">Admin bearer token</label>
          <input v-model="token" type="password" autocomplete="current-password" class="app-input" placeholder="Paste RELAY_ADMIN_TOKEN">
        </div>
        <div v-if="auth.error" class="rounded-2xl border border-rose-500/20 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">
          {{ auth.error }}
        </div>
        <UiButton class="w-full" :disabled="auth.unlocking">{{ auth.unlocking ? 'Unlocking...' : 'Unlock Model Relay' }}</UiButton>
      </form>

      <div class="app-border-line app-faint-text mt-8 flex items-center justify-between border-t pt-5 text-xs">
        <span>{{ auth.system?.http_addr || 'Server not yet discovered' }}</span>
        <span>{{ auth.system?.insecure_dev ? 'Insecure dev enabled' : 'Secure mode' }}</span>
      </div>
    </div>
  </div>
</template>
