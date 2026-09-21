<script setup lang="ts">
import colors from 'tailwindcss/colors'

definePageMeta({
  layout: false
})

useSeoMeta({
  title: 'AnchorShell logo lab',
  description: 'Internal visual checks for AnchorShell wordmark color, weight, tracking, and navbar readability.',
  ogTitle: 'AnchorShell logo lab',
  ogDescription: 'Internal visual checks for AnchorShell wordmark color, weight, tracking, and navbar readability.'
})

useHead({
  meta: [
    { name: 'robots', content: 'noindex, nofollow' }
  ]
})

const logoLightSrc = '/anchorshell-logo-light-high-contrast.png'
const logoDarkSrc = '/anchorshell-logo-dark.png'

const tokens = {
  anchorLight: '#06182c',
  anchorDark: '#f8fafc',
  shellBlueTeal: '#0f9a9a',
  shellDeepTeal: '#0f766e',
  shellCyanTeal: '#0891b2',
  shellCyanTealDark: '#0f97b8',
  shellSteelBlue: '#4f7f9f',
  shellClearBlue: '#1a7aff',
  shellSignalBlue: '#39a8ff',
  shellSky500: '#0ea5e9',
  shellBlue: '#2563eb',
  shellMonochrome: '#06182c',
  shellDarkTeal: '#2dd4bf',
  shellDarkCyan: '#22d3ee',
  shellDarkBlue: '#60a5fa',
  shellDarkMuted: '#93a4b8',
  shellDarkOffWhite: '#f8fafc'
} as const

const shadeSteps = ['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950'] as const

const paletteFamilyNames = [
  'red',
  'orange',
  'amber',
  'yellow',
  'lime',
  'green',
  'emerald',
  'teal',
  'cyan',
  'sky',
  'blue',
  'indigo',
  'violet',
  'purple',
  'fuchsia',
  'pink',
  'rose',
  'slate',
  'gray',
  'zinc',
  'neutral',
  'stone',
  'taupe',
  'mauve',
  'mist',
  'olive'
] as const

const customPaletteColors: Record<string, Record<string, string>> = {
  taupe: {
    50: '#faf9f7',
    100: '#f1eee9',
    200: '#e4ded6',
    300: '#d2c8bb',
    400: '#b8aa99',
    500: '#9f8f7e',
    600: '#817364',
    700: '#675b50',
    800: '#4b423a',
    900: '#322c27',
    950: '#1f1b17'
  },
  mauve: {
    50: '#fbf7fb',
    100: '#f4eef5',
    200: '#eadfea',
    300: '#dacbdb',
    400: '#c2afc4',
    500: '#a78fa9',
    600: '#886f8b',
    700: '#6d5870',
    800: '#504153',
    900: '#352b38',
    950: '#221c25'
  },
  mist: {
    50: '#f8fbfc',
    100: '#eef6f8',
    200: '#dcecef',
    300: '#c5dde3',
    400: '#a8c9d1',
    500: '#87b0bb',
    600: '#6897a4',
    700: '#517a86',
    800: '#3d5e68',
    900: '#2a4148',
    950: '#1a2b30'
  },
  olive: {
    50: '#fafbf3',
    100: '#f1f4df',
    200: '#e3e9c4',
    300: '#d1dba1',
    400: '#b8c678',
    500: '#98ab55',
    600: '#778a3b',
    700: '#5e6d2f',
    800: '#465123',
    900: '#303819',
    950: '#1e2410'
  }
}

const tailwindColorMap = colors as unknown as Record<string, Record<string, string>>
const paletteGroups = paletteFamilyNames.map((name) => {
  const palette = tailwindColorMap[name] || customPaletteColors[name]

  return {
    name,
    label: name.charAt(0).toUpperCase() + name.slice(1),
    custom: Boolean(customPaletteColors[name]),
    shades: shadeSteps.map((shade) => ({
      shade,
      color: palette[shade]
    }))
  }
})

type WordmarkVariant = {
  label: string
  note: string
  anchor: string
  shell: string
  weight: 600 | 700 | 800
  tracking: string
  recommended?: boolean
}

const lightCandidates: WordmarkVariant[] = [
  {
    label: 'A. Blue-teal',
    note: 'Most ownable balance: technical, calm, and not green/wellness.',
    anchor: tokens.anchorLight,
    shell: tokens.shellBlueTeal,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'B. Deeper teal',
    note: 'Stronger contrast, more conservative, slightly less signal-like.',
    anchor: tokens.anchorLight,
    shell: tokens.shellDeepTeal,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'C. Technical cyan-teal',
    note: 'Current light navbar/footer trial: technical, blue-leaning, and clear without feeling generic.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 800,
    tracking: '-0.035em',
    recommended: true
  },
  {
    label: 'D. Premium steel-blue',
    note: 'Restrained and premium, retained as a comparison candidate.',
    anchor: tokens.anchorLight,
    shell: tokens.shellSteelBlue,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'E. Sky light',
    note: 'Clear blue signal retained as a comparison candidate.',
    anchor: tokens.anchorLight,
    shell: tokens.shellSky500,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'F. Clear blue',
    note: 'Direct and legible, but a bit more saturated than the cyan-teal trial.',
    anchor: tokens.anchorLight,
    shell: tokens.shellClearBlue,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'G. Classic blue fallback',
    note: 'Safe and legible, slightly more conventional.',
    anchor: tokens.anchorLight,
    shell: tokens.shellBlue,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'H. Monochrome fallback',
    note: 'Formal, quiet, and useful for constrained contexts.',
    anchor: tokens.anchorLight,
    shell: tokens.shellMonochrome,
    weight: 800,
    tracking: '-0.035em'
  }
]

const darkCandidates: WordmarkVariant[] = [
  {
    label: 'A. Dark teal',
    note: 'Recommended companion: clear without becoming neon.',
    anchor: tokens.anchorDark,
    shell: tokens.shellDarkTeal,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'B. Technical cyan-teal darkmode',
    note: 'Current dark navbar/footer trial: blue-leaning cyan-teal tuned for dark mode.',
    anchor: tokens.anchorDark,
    shell: tokens.shellCyanTealDark,
    weight: 800,
    tracking: '-0.035em',
    recommended: true
  },
  {
    label: 'C. Dark blue',
    note: 'Clean and safe, but less distinctive.',
    anchor: tokens.anchorDark,
    shell: tokens.shellDarkBlue,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'D. Dark muted',
    note: 'Low-noise option for subdued placements, retained as a comparison candidate.',
    anchor: tokens.anchorDark,
    shell: tokens.shellDarkMuted,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'E. Signal blue',
    note: 'Approved saturated blue for dark-mode product identity.',
    anchor: tokens.anchorDark,
    shell: tokens.shellSignalBlue,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'F. Clear blue',
    note: 'More saturated dark-mode option retained for comparison.',
    anchor: tokens.anchorDark,
    shell: tokens.shellClearBlue,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'G. Off-white fallback',
    note: 'Monochrome fallback for formal or tiny contexts.',
    anchor: tokens.anchorDark,
    shell: tokens.shellDarkOffWhite,
    weight: 800,
    tracking: '-0.035em'
  }
]

const navbarPreviews = [
  {
    label: 'Recommended light navbar',
    bgClass: 'border-[#071d36]/15 bg-[#edf6fb] text-[#06182c]',
    navClass: 'border-[#071d36]/12 bg-[rgba(248,252,255,0.9)]',
    linkClass: 'text-[#25364b]',
    ctaClass: 'bg-[#071d36] text-white',
    logoSrc: logoLightSrc,
    tone: 'on-light' as const,
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 700 as const,
    tracking: '-0.025em'
  },
  {
    label: 'Recommended dark navbar',
    bgClass: 'border-white/10 bg-[#030712] text-[#f8fafc]',
    navClass: 'border-white/10 bg-[rgba(4,15,29,0.86)]',
    linkClass: 'text-[#d7e1ee]',
    ctaClass: 'bg-[#f7f2e8] text-[#020617]',
    logoSrc: logoDarkSrc,
    tone: 'on-dark' as const,
    anchor: tokens.anchorDark,
    shell: tokens.shellCyanTealDark,
    weight: 700 as const,
    tracking: '-0.025em'
  },
  {
    label: 'Monochrome light fallback',
    bgClass: 'border-[#071d36]/15 bg-[#edf6fb] text-[#06182c]',
    navClass: 'border-[#071d36]/12 bg-[rgba(248,252,255,0.9)]',
    linkClass: 'text-[#25364b]',
    ctaClass: 'bg-[#071d36] text-white',
    logoSrc: logoLightSrc,
    tone: 'on-light' as const,
    anchor: tokens.anchorLight,
    shell: tokens.anchorLight,
    weight: 700 as const,
    tracking: '-0.025em'
  },
  {
    label: 'Monochrome dark fallback',
    bgClass: 'border-white/10 bg-[#030712] text-[#f8fafc]',
    navClass: 'border-white/10 bg-[rgba(4,15,29,0.86)]',
    linkClass: 'text-[#d7e1ee]',
    ctaClass: 'bg-[#f7f2e8] text-[#020617]',
    logoSrc: logoDarkSrc,
    tone: 'on-dark' as const,
    anchor: tokens.anchorDark,
    shell: tokens.anchorDark,
    weight: 700 as const,
    tracking: '-0.025em'
  }
]

const styleTests: WordmarkVariant[] = [
  {
    label: 'Display recommended',
    note: 'Preferred large display wordmark.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 800,
    tracking: '-0.035em'
  },
  {
    label: 'Navbar 700 / -0.025em',
    note: 'Recommended navbar baseline for crisp small rendering.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 700,
    tracking: '-0.025em'
  },
  {
    label: 'Navbar 800 / -0.025em',
    note: 'Slightly stronger navbar option if 700 feels too light.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 800,
    tracking: '-0.025em'
  },
  {
    label: 'Weight 600 / -0.025em',
    note: 'Softer than recommended; useful as a lower-bound comparison.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 600,
    tracking: '-0.025em'
  },
  {
    label: 'Weight 700 / -0.035em',
    note: 'Tighter and polished at larger sizes, less ideal for tiny nav.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 700,
    tracking: '-0.035em'
  },
  {
    label: 'Weight 800 / -0.05em',
    note: 'Very tight stress test; likely too compressed for nav use.',
    anchor: tokens.anchorLight,
    shell: tokens.shellCyanTeal,
    weight: 800,
    tracking: '-0.05em'
  }
]
</script>

<template>
  <SiteShell>
    <section class="px-4 pb-12 pt-32 sm:px-6 sm:pt-36 lg:pt-44">
      <div class="mx-auto max-w-7xl">
        <p class="site-eyebrow text-sm font-medium">Brand QA</p>
        <div class="mt-5 grid gap-6 lg:grid-cols-[0.9fr_0.55fr] lg:items-end">
          <div>
            <h1 class="site-heading text-5xl font-semibold leading-tight sm:text-6xl">
              AnchorShell logo lab
            </h1>
            <p class="site-copy mt-5 max-w-3xl text-lg leading-8">
              Internal visual checks for AnchorShell wordmark color, weight, tracking, and navbar readability.
            </p>
          </div>

          <div class="site-card rounded-[1.5rem] p-5">
            <p class="site-label text-sm font-medium">Current recommendation</p>
            <div class="mt-4 grid gap-4">
              <div>
                <p class="site-muted text-xs font-semibold uppercase tracking-[0.18em]">Light mode</p>
                <SiteAnchorShellWordmark
                  class="mt-2 block"
                  size="lg"
                  tone="on-light"
                  :weight="800"
                  tracking="-0.035em"
                  :anchor-color="tokens.anchorLight"
                  :shell-color="tokens.shellCyanTeal"
                />
                <p class="mt-2 font-mono text-[11px] leading-5 text-slate-400">
                  Anchor: #06182c / Shell: #0891b2
                </p>
              </div>
              <div>
                <p class="site-muted text-xs font-semibold uppercase tracking-[0.18em]">Dark mode</p>
                <SiteAnchorShellWordmark
                  class="mt-2 block"
                  size="lg"
                  tone="on-dark"
                  :weight="800"
                  tracking="-0.035em"
                  :anchor-color="tokens.anchorDark"
                  :shell-color="tokens.shellCyanTealDark"
                />
                <p class="mt-2 font-mono text-[11px] leading-5 text-slate-400">
                  Anchor: #f8fafc / Shell: #0f97b8
                </p>
              </div>
              <p class="site-copy border-t pt-4 text-sm leading-6">
                Use monochrome for formal, tiny, or low-noise contexts.
              </p>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section class="px-4 py-10 sm:px-6">
      <div class="mx-auto max-w-7xl">
        <div class="mb-6 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <p class="site-eyebrow text-sm font-medium">Final wordmark candidates</p>
            <h2 class="site-heading mt-2 text-3xl font-semibold">Blue-teal recommendation and fallbacks</h2>
          </div>
          <p class="site-muted max-w-xl text-sm leading-6">
            The current trial uses technical cyan-teal Shell in light mode and technical cyan-teal darkmode Shell in dark mode.
          </p>
        </div>

        <div class="grid gap-5 md:grid-cols-2 lg:grid-cols-3">
          <article
            v-for="candidate in lightCandidates"
            :key="candidate.label"
            class="rounded-[1.5rem] border border-[#071d36]/15 bg-[#edf6fb] p-6 text-[#06182c]"
          >
            <div class="flex items-start justify-between gap-4">
              <div>
                <p class="text-sm font-semibold">{{ candidate.label }}</p>
                <p class="mt-2 text-xs leading-5 text-[#465a70]">{{ candidate.note }}</p>
              </div>
              <span v-if="candidate.recommended" class="rounded-full bg-[#0891b2]/12 px-2.5 py-1 text-xs font-semibold text-[#075985]">Lead</span>
            </div>
            <div class="mt-7 grid gap-5">
              <SiteAnchorShellWordmark
                size="md"
                tone="on-light"
                :weight="700"
                tracking="-0.025em"
                :anchor-color="candidate.anchor"
                :shell-color="candidate.shell"
              />
              <SiteAnchorShellWordmark
                size="display"
                tone="on-light"
                :weight="candidate.weight"
                :tracking="candidate.tracking"
                :anchor-color="candidate.anchor"
                :shell-color="candidate.shell"
              />
            </div>
            <p class="mt-6 font-mono text-[11px] leading-5 text-[#64748b]">
              Anchor {{ candidate.anchor }} / Shell {{ candidate.shell }}
            </p>
          </article>
        </div>

        <div class="mt-8 grid gap-5 md:grid-cols-2 lg:grid-cols-5">
          <article
            v-for="candidate in darkCandidates"
            :key="candidate.label"
            class="rounded-[1.5rem] border border-white/10 bg-[#030712] p-5 text-[#f8fafc]"
          >
            <div class="flex items-start justify-between gap-3">
              <p class="text-sm font-semibold">{{ candidate.label }}</p>
              <span v-if="candidate.recommended" class="rounded-full bg-[#0f97b8]/14 px-2.5 py-1 text-xs font-semibold text-[#67e8f9]">Lead</span>
            </div>
            <SiteAnchorShellWordmark
              class="mt-7 block"
              size="md"
              tone="on-dark"
              :weight="700"
              tracking="-0.025em"
              :anchor-color="candidate.anchor"
              :shell-color="candidate.shell"
            />
            <p class="mt-5 text-xs leading-5 text-[#b7c6d8]">{{ candidate.note }}</p>
            <p class="mt-3 font-mono text-[11px] leading-5 text-[#8ea3b8]">
              Shell {{ candidate.shell }}
            </p>
          </article>
        </div>
      </div>
    </section>

    <section class="px-4 py-10 sm:px-6">
      <div class="mx-auto max-w-7xl">
        <div class="mb-6 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <p class="site-eyebrow text-sm font-medium">Tailwind palette sweep</p>
            <h2 class="site-heading mt-2 text-3xl font-semibold">Shell color across Tailwind families</h2>
          </div>
          <p class="site-muted max-w-xl text-sm leading-6">
            Anchor follows the active site theme while Shell cycles through each family and shade. Taupe, mauve, mist, and olive are lab-only custom scales for comparison.
          </p>
        </div>

        <div class="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
          <article
            v-for="family in paletteGroups"
            :key="family.name"
            class="site-card rounded-[1.5rem] p-5 text-[var(--site-heading)]"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <h3 class="text-sm font-semibold">{{ family.label }}</h3>
                <p class="site-muted mt-1 text-xs leading-5">
                  {{ family.custom ? 'Lab custom scale' : 'Tailwind default scale' }}
                </p>
              </div>
              <span class="rounded-full border border-[var(--site-border)] bg-[var(--site-surface-soft)] px-2.5 py-1 font-mono text-[11px] text-[var(--site-subtle)]">
                {{ family.shades.length }} shades
              </span>
            </div>

            <div class="mt-4 grid gap-2">
              <div
                v-for="swatch in family.shades"
                :key="`${family.name}-${swatch.shade}`"
                class="rounded-xl border border-[var(--site-border)] bg-[var(--site-surface-soft)] p-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]"
              >
                <div class="flex items-center justify-between gap-3">
                  <span class="font-mono text-[11px] text-[var(--site-subtle)]">{{ family.name }}-{{ swatch.shade }}</span>
                  <span class="font-mono text-[11px] text-[var(--site-subtle)]">{{ swatch.color }}</span>
                </div>
                <div class="mt-3 flex min-w-0 items-center justify-between gap-3">
                  <SiteAnchorShellWordmark
                    class="min-w-0"
                    size="sm"
                    tone="auto"
                    :weight="700"
                    tracking="-0.025em"
                    :shell-color="swatch.color"
                  />
                  <span
                    class="h-5 w-5 shrink-0 rounded-full border border-[var(--site-border)] shadow-inner"
                    :style="{ backgroundColor: swatch.color }"
                    aria-hidden="true"
                  />
                </div>
              </div>
            </div>
          </article>
        </div>
      </div>
    </section>

    <section class="px-4 py-10 sm:px-6">
      <div class="mx-auto max-w-7xl">
        <div class="mb-5 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <p class="site-eyebrow text-sm font-medium">Navbar previews</p>
            <h2 class="site-heading mt-2 text-3xl font-semibold">Existing PNG icon plus wordmark</h2>
          </div>
          <p class="site-muted max-w-xl text-sm leading-6">
            These rows use the current premade icon assets. Only the wordmark color is being tested.
          </p>
        </div>

        <div class="grid gap-5 lg:grid-cols-2">
          <article
            v-for="preview in navbarPreviews"
            :key="preview.label"
            :class="['rounded-[1.5rem] border p-4 shadow-[0_18px_54px_rgba(7,29,54,0.10)]', preview.bgClass]"
          >
            <p class="mb-3 text-sm font-semibold">{{ preview.label }}</p>
            <div :class="['flex items-center justify-between gap-4 rounded-2xl border px-4 py-3', preview.navClass]">
              <div class="flex min-w-0 items-center gap-3">
                <img class="h-12 w-12 shrink-0 rounded-xl object-cover" :src="preview.logoSrc" alt="" aria-hidden="true" />
                <SiteAnchorShellWordmark
                  class="min-w-0 truncate"
                  size="md"
                  :tone="preview.tone"
                  :weight="preview.weight"
                  :tracking="preview.tracking"
                  :anchor-color="preview.anchor"
                  :shell-color="preview.shell"
                />
              </div>
              <div :class="['hidden items-center gap-5 text-sm font-medium sm:flex', preview.linkClass]">
                <span>Platform</span>
                <span>Approach</span>
                <span>Use Cases</span>
              </div>
              <span :class="['rounded-xl px-4 py-2 text-sm font-semibold', preview.ctaClass]">Request</span>
            </div>
          </article>
        </div>
      </div>
    </section>

    <section class="px-4 py-10 sm:px-6">
      <div class="mx-auto max-w-7xl">
        <p class="site-eyebrow text-sm font-medium">Realistic size checks</p>
        <h2 class="site-heading mt-2 text-3xl font-semibold">Navbar, wordmark-only, display, and fallback sizes</h2>

        <div class="mt-6 grid gap-5 lg:grid-cols-4">
          <article class="rounded-[1.5rem] border border-[#071d36]/15 bg-[#edf6fb] p-6 text-[#06182c]">
            <p class="text-sm font-semibold">Icon + wordmark</p>
            <div class="mt-6 flex items-center gap-3">
              <img class="h-12 w-12 shrink-0 rounded-xl object-cover" :src="logoLightSrc" alt="" aria-hidden="true" />
              <SiteAnchorShellWordmark
                size="md"
                tone="on-light"
                :weight="700"
                tracking="-0.025em"
                :anchor-color="tokens.anchorLight"
                :shell-color="tokens.shellCyanTeal"
              />
            </div>
          </article>

          <article class="rounded-[1.5rem] border border-[#071d36]/15 bg-[#edf6fb] p-6 text-[#06182c]">
            <p class="text-sm font-semibold">Wordmark only</p>
            <SiteAnchorShellWordmark
              class="mt-7 block"
              size="md"
              tone="on-light"
              :weight="700"
              tracking="-0.025em"
              :anchor-color="tokens.anchorLight"
              :shell-color="tokens.shellCyanTeal"
            />
          </article>

          <article class="rounded-[1.5rem] border border-[#071d36]/15 bg-[#edf6fb] p-6 text-[#06182c] lg:col-span-2">
            <p class="text-sm font-semibold">Large display wordmark</p>
            <SiteAnchorShellWordmark
              class="mt-7 block"
              size="display"
              tone="on-light"
              :weight="800"
              tracking="-0.035em"
              :anchor-color="tokens.anchorLight"
              :shell-color="tokens.shellCyanTeal"
            />
          </article>

          <article class="rounded-[1.5rem] border border-white/10 bg-[#030712] p-6 text-[#f8fafc] lg:col-span-2">
            <p class="text-sm font-semibold">Small monochrome fallback</p>
            <div class="mt-6 flex items-center gap-3">
              <img class="h-10 w-10 shrink-0 rounded-lg object-cover" :src="logoDarkSrc" alt="" aria-hidden="true" />
              <SiteAnchorShellWordmark
                size="sm"
                tone="on-dark"
                :weight="700"
                tracking="-0.025em"
                :anchor-color="tokens.anchorDark"
                :shell-color="tokens.anchorDark"
              />
            </div>
          </article>

          <article class="rounded-[1.5rem] border border-white/10 bg-[#030712] p-6 text-[#f8fafc] lg:col-span-2">
            <p class="text-sm font-semibold">Dark recommended navbar size</p>
            <SiteAnchorShellWordmark
              class="mt-7 block"
              size="md"
              tone="on-dark"
              :weight="700"
              tracking="-0.025em"
              :anchor-color="tokens.anchorDark"
              :shell-color="tokens.shellCyanTealDark"
            />
          </article>
        </div>
      </div>
    </section>

    <section class="px-4 py-10 sm:px-6">
      <div class="mx-auto max-w-7xl">
        <p class="site-eyebrow text-sm font-medium">Source Sans 3 wordmark tests</p>
        <h2 class="site-heading mt-2 text-3xl font-semibold">Weight and tracking</h2>

        <div class="mt-6 grid gap-5 md:grid-cols-2 lg:grid-cols-3">
          <article
            v-for="variant in styleTests"
            :key="variant.label"
            class="rounded-[1.5rem] border border-[#071d36]/15 bg-[#edf6fb] p-6 text-[#06182c]"
          >
            <p class="text-sm font-semibold">{{ variant.label }}</p>
            <div class="mt-7">
              <SiteAnchorShellWordmark
                size="lg"
                tone="on-light"
                :weight="variant.weight"
                :tracking="variant.tracking"
                :anchor-color="variant.anchor"
                :shell-color="variant.shell"
              />
            </div>
            <div class="mt-5 flex items-center gap-3">
              <img class="h-10 w-10 shrink-0 rounded-lg object-cover" :src="logoLightSrc" alt="" aria-hidden="true" />
              <SiteAnchorShellWordmark
                size="sm"
                tone="on-light"
                :weight="variant.weight"
                :tracking="variant.tracking"
                :anchor-color="variant.anchor"
                :shell-color="variant.shell"
              />
            </div>
            <p class="mt-5 text-xs leading-5 text-[#465a70]">{{ variant.note }}</p>
            <p class="mt-3 font-mono text-[11px] leading-5 text-[#64748b]">
              weight {{ variant.weight }} / tracking {{ variant.tracking }}
            </p>
          </article>
        </div>
      </div>
    </section>

    <section class="px-4 pb-24 pt-10 sm:px-6 lg:pb-32">
      <div class="mx-auto max-w-7xl">
        <div class="site-card rounded-[1.5rem] p-6 sm:p-8">
          <p class="site-eyebrow text-sm font-medium">Notes</p>
          <div class="site-copy mt-5 grid gap-4 text-sm leading-7 md:grid-cols-2">
            <p>Current trial wordmark: technical cyan-teal Shell in light mode, technical cyan-teal darkmode Shell in dark mode.</p>
            <p>Monochrome fallback: all navy in light mode, all off-white in dark mode.</p>
            <p>Avoid greenish teal for the main wordmark; it should read blue-teal and technical.</p>
            <p>Avoid overly bright cyan as the default wordmark. Keep it for accents and signals.</p>
            <p>Use the premade PNG icon assets for now. This page only tests Source Sans 3 wordmark text.</p>
            <p>For display, prefer 800 with -0.035em. For navbar, prefer 700 or 800 with -0.025em.</p>
          </div>
        </div>
      </div>
    </section>
  </SiteShell>
</template>
