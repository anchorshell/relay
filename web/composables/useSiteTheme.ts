type SiteTheme = 'light' | 'dark'

const siteThemeStorageKey = 'anchorshell-site-theme'

function readStoredTheme(): SiteTheme | null {
  if (!import.meta.client) {
    return null
  }

  try {
    const storedTheme = window.localStorage.getItem(siteThemeStorageKey)
    return storedTheme === 'light' || storedTheme === 'dark' ? storedTheme : null
  } catch {
    return null
  }
}

function storeTheme(theme: SiteTheme) {
  if (!import.meta.client) {
    return
  }

  try {
    window.localStorage.setItem(siteThemeStorageKey, theme)
  } catch {
    // Theme persistence is a convenience. The UI still works if storage is unavailable.
  }
}

function readInitialTheme(): SiteTheme {
  if (!import.meta.client) return 'dark'
  const storedTheme = readStoredTheme()
  if (storedTheme) return storedTheme

  try {
    return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  } catch {
    return 'dark'
  }
}

function applyTheme(theme: SiteTheme) {
  if (!import.meta.client) {
    return
  }

  document.documentElement.dataset.siteTheme = theme
  document.documentElement.classList.toggle('dark', theme === 'dark')
  document.documentElement.classList.toggle('light', theme === 'light')
  document.documentElement.style.colorScheme = theme
}

export function useSiteTheme() {
  const theme = useState<SiteTheme>('anchorshell-site-theme', () => 'dark')
  const hydrated = useState('anchorshell-site-theme-hydrated', () => false)
  const colorMode = import.meta.client
    ? useColorMode() as unknown as { preference: string }
    : null

  const isDark = computed(() => theme.value === 'dark')

  function setTheme(nextTheme: SiteTheme) {
    theme.value = nextTheme
    if (colorMode) {
      colorMode.preference = nextTheme
    }
    applyTheme(nextTheme)
    storeTheme(nextTheme)
  }

  function toggleTheme() {
    setTheme(isDark.value ? 'light' : 'dark')
  }

  if (import.meta.client) {
    onMounted(() => {
      if (!hydrated.value) {
        theme.value = readInitialTheme()
        hydrated.value = true
      }

      if (colorMode && colorMode.preference !== theme.value) {
        colorMode.preference = theme.value
      }
      applyTheme(theme.value)
    })

    watch(theme, (nextTheme) => {
      if (colorMode && colorMode.preference !== nextTheme) {
        colorMode.preference = nextTheme
      }
      applyTheme(nextTheme)

      if (hydrated.value) {
        storeTheme(nextTheme)
      }
    })
  }

  return {
    theme,
    isDark,
    setTheme,
    toggleTheme
  }
}
