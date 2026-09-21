<script setup lang="ts">
const auth = useAuthStore()
useSiteTheme()

onMounted(() => {
  void auth.bootstrap()
})
</script>

<template>
  <AuthUnlockScreen v-if="auth.ready && !auth.authenticated" />
  <div v-else-if="!auth.ready" class="app-loading-shell flex min-h-screen items-center justify-center">
    <div class="app-loading-card animate-pulse rounded-2xl px-6 py-4">Loading Model Relay...</div>
  </div>
  <ShellAppShell v-else>
    <slot />
  </ShellAppShell>
  <UiToastStack />
</template>
