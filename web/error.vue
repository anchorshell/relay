<script setup lang="ts">
const props = defineProps<{
  error?: {
    statusCode?: number
    statusMessage?: string
    message?: string
  }
}>()

const isNotFound = computed(() => props.error?.statusCode === 404)
const { isDark } = useSiteTheme()
const statusCode = computed(() => props.error?.statusCode || 500)
const errorTitle = computed(() => isNotFound.value ? 'Page not found.' : 'The page could not load.')
const logoSrc = computed(() => isDark.value ? '/anchorshell-logo-dark.png' : '/anchorshell-logo-light-high-contrast.png')
const errorCopy = computed(() => {
  if (isNotFound.value) {
    return "The page you're looking for does not exist or has moved."
  }
  return 'A temporary development update or backend restart interrupted the page. Reloading usually restores the app.'
})

useSeoMeta({
  title: () => isNotFound.value ? 'Page not found - AnchorShell' : 'Error - AnchorShell',
  description: () => isNotFound.value
    ? 'The page you are looking for does not exist or has moved.'
    : 'Something went wrong while loading AnchorShell.'
})

function goHome() {
  clearError({ redirect: '/' })
}

function reloadPage() {
  window.location.reload()
}
</script>

<template>
  <main class="site-shell error-shell relative grid min-h-screen place-items-center overflow-hidden px-6 py-16">
    <div class="pointer-events-none absolute inset-0">
      <div class="site-bg-base absolute inset-0" />
      <div class="site-bg-grid absolute inset-0 bg-[size:72px_72px]" />
    </div>

    <section class="relative mx-auto w-full max-w-2xl text-center">
      <button class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed error-logo mx-auto flex items-center justify-center gap-3 rounded-xl" type="button" @click="goHome">
        <img
          class="h-12 w-12 shrink-0 rounded-xl object-cover"
          :src="logoSrc"
          width="48"
          height="48"
          alt=""
          aria-hidden="true"
        />
        <SiteAnchorShellWordmark size="md" tone="auto" :weight="700" tracking="-0.025em" />
      </button>

      <div class="error-status mt-10 text-sm font-semibold">
        {{ statusCode }}
      </div>
      <h1 class="site-heading mt-4 text-4xl font-semibold leading-tight sm:text-5xl">
        {{ errorTitle }}
      </h1>
      <p class="site-copy mx-auto mt-5 max-w-xl text-base leading-7 sm:text-lg sm:leading-8">
        {{ errorCopy }}
      </p>

      <div class="mt-8 flex flex-col justify-center gap-3 sm:flex-row">
        <button class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed error-button error-button-primary rounded-full px-5 py-3 text-sm font-semibold transition" type="button" @click="reloadPage">
          Reload page
        </button>
        <button class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed error-button error-button-secondary rounded-full px-5 py-3 text-sm font-semibold transition" type="button" @click="goHome">
          Go home
        </button>
      </div>
    </section>
  </main>
</template>

<style scoped>
.error-shell {
  font-family: var(--font-sans, ui-sans-serif, system-ui, sans-serif);
}

.error-shell section {
  color: var(--site-text, #f7f2e8);
}

.error-logo {
  outline: none;
}

.error-logo:focus-visible {
  outline: 2px solid rgba(34, 211, 238, 0.72);
  outline-offset: 5px;
}

.error-status {
  color: var(--site-accent-text, #cffafe);
}

.error-button {
  outline: none;
}

.error-button:focus-visible {
  outline: 2px solid rgba(34, 211, 238, 0.72);
  outline-offset: 3px;
}

.error-button-primary {
  background: var(--site-primary-bg, #f7f2e8);
  color: var(--site-primary-text, #020617);
  border: 1px solid transparent;
}

.error-button-primary:hover {
  background: var(--site-primary-hover, #ffffff);
}

.error-button-secondary {
  background: var(--site-surface-soft, rgba(255, 255, 255, 0.045));
  border: 1px solid var(--site-border, rgba(255, 255, 255, 0.1));
  color: var(--site-heading, #fffaf0);
}

.error-button-secondary:hover {
  background: var(--site-surface-muted, rgba(3, 11, 21, 0.76));
  border-color: var(--site-border-strong, rgba(165, 243, 252, 0.2));
}
</style>
