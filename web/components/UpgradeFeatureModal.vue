<script setup lang="ts">
import { relayUpgradeURL, type PaidFeature } from '../utils/paidFeatures'

const props = defineProps<{ feature: PaidFeature, returnFocusTo: HTMLElement | null }>()
const open = defineModel<boolean>('open', { required: true })
const runtime = useRuntimeConfig()
const { isDark } = useSiteTheme()
const logoSrc = computed(() => isDark.value ? '/anchorshell-logo-dark.png' : '/anchorshell-logo-light-high-contrast.png')
const ctaURL = computed(() => relayUpgradeURL(runtime.public.relayUpgradeUrl))

function restoreFocus(event: Event) {
  event.preventDefault()
  if (props.returnFocusTo?.isConnected && props.returnFocusTo.getClientRects().length) {
    props.returnFocusTo.focus({ preventScroll: true })
  }
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="feature.title"
    :description="feature.description"
    :transition="false"
    :content="{ onCloseAutoFocus: restoreFocus }"
    :ui="{ overlay: 'upgrade-overlay', content: 'upgrade-dialog app-themed' }"
  >
    <template #content>
      <div class="upgrade-content">
        <div class="upgrade-brand">
          <div class="flex items-center gap-3">
            <img :src="logoSrc" alt="" aria-hidden="true" width="40" height="40" class="h-10 w-10 rounded-lg" />
            <SiteAnchorShellWordmark size="sm" tone="auto" :weight="700" tracking="-0.025em" />
          </div>
          <button type="button" class="cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed upgrade-close" aria-label="Close upgrade information" @click="open = false">
            <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" /></svg>
          </button>
        </div>
        <h2 class="upgrade-title">{{ feature.title }}</h2>
        <p class="upgrade-headline">{{ feature.headline }}</p>
        <p class="upgrade-description">{{ feature.description }}</p>
        <p class="upgrade-availability">{{ feature.availability }}</p>
        <div class="upgrade-actions">
          <a :href="ctaURL" target="_blank" rel="noopener noreferrer" class="ui-button-primary upgrade-cta">
            {{ feature.ctaLabel }}
            <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7 17 17 7M7 7h10v10" /></svg>
            <span class="sr-only">(opens in a new tab)</span>
          </a>
          <UiButton type="button" tone="ghost" @click="open = false">Maybe later</UiButton>
        </div>
        <p class="upgrade-oss-note">{{ feature.ossNote }}</p>
      </div>
    </template>
  </UModal>
</template>

<style>
/* Local addition to the operational UI: one calm, user-invoked decision.
   Existing surface colors, logo, and controls carry the brand; no campaign
   art, tracking, automatic prompts, or changes to the surrounding pages. */
.upgrade-overlay { z-index: 80; background: rgb(0 0 0 / 55%); }
.upgrade-dialog {
  z-index: 81;
  width: calc(100vw - 2rem);
  max-width: 34rem;
  max-height: calc(100dvh - 2rem);
  overflow-y: auto;
  border-radius: 1rem;
  background: var(--app-surface);
  color: var(--app-heading);
  box-shadow: var(--app-panel-shadow);
  --tw-ring-shadow: 0 0 #0000;
}
.upgrade-content { padding: clamp(1.25rem, 5vw, 2rem); }
.upgrade-brand { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-bottom: 2rem; }
.upgrade-close { display: grid; place-items: center; width: 2.75rem; height: 2.75rem; border-radius: 0.375rem; color: var(--app-muted); }
.upgrade-close:hover { background: var(--app-nav-hover-bg); color: var(--app-heading); }
.upgrade-title { font-size: clamp(1.5rem, 5vw, 1.875rem); font-weight: 650; line-height: 1.2; letter-spacing: -0.025em; text-wrap: balance; }
.upgrade-headline { margin-top: 1.5rem; font-size: 1.0625rem; font-weight: 600; line-height: 1.5; }
.upgrade-description { margin-top: 0.625rem; font-size: 1rem; line-height: 1.7; color: var(--app-copy); }
.upgrade-availability { margin-top: 1.25rem; font-size: 0.875rem; line-height: 1.6; color: var(--app-copy); }
.upgrade-actions { display: flex; flex-wrap: wrap; gap: 0.75rem; margin-top: 1.5rem; }
.upgrade-cta { display: inline-flex; align-items: center; justify-content: center; gap: 0.5rem; min-height: 2.75rem; padding: 0.625rem 1rem; border-radius: 0.375rem; font-size: 0.875rem; font-weight: 600; }
.upgrade-oss-note { margin-top: 1.75rem; padding-top: 1.25rem; border-top: 1px solid var(--app-border); color: var(--app-copy); font-size: 0.875rem; line-height: 1.65; }
.upgrade-close:focus-visible, .upgrade-cta:focus-visible { outline: 2px solid var(--app-focus); outline-offset: 3px; }
@media (max-width: 420px) {
  .upgrade-actions { flex-direction: column; }
  .upgrade-actions > * { width: 100%; }
}
</style>
