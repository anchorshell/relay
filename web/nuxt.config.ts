import { statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Plugin } from 'vite'
import { DEFAULT_RELAY_PORT } from './utils/relayDefaults'
import { DEFAULT_RELAY_UPGRADE_URL } from './utils/paidFeatures'

const adminPrerenderRoutes = [
  '/dashboard',
  '/endpoints',
  '/guardrails',
  '/groups',
  '/lanes',
  '/limits',
  '/playground',
  '/pricing',
  '/providers',
  '/queue',
  '/realtime',
  '/requests',
  '/settings',
  '/setup',
  '/usage'
]

const devApiTarget = process.env.NUXT_DEV_API_TARGET || `http://127.0.0.1:${DEFAULT_RELAY_PORT}`
const devHost = process.env.NUXT_DEV_HOST || '127.0.0.1'
const devPort = Number.parseInt(process.env.NUXT_DEV_PORT || '3030', 10)
const devHmrHost = process.env.NUXT_DEV_HMR_HOST || devHost
const devHmrPort = Number.parseInt(process.env.NUXT_DEV_HMR_PORT || String(devPort), 10)
const devHmrProtocol = (process.env.NUXT_DEV_HMR_PROTOCOL || 'ws') as 'ws' | 'wss'
const devBackendReloadFile = process.env.NUXT_DEV_BACKEND_RELOAD_FILE
const relayWsTarget = process.env.NUXT_PUBLIC_RELAY_WS_TARGET || (process.env.NODE_ENV === 'development' ? devApiTarget : '')
const modelRelayApiBase = (process.env.NUXT_PUBLIC_MODEL_RELAY_API_BASE || '').replace(/\/+$/, '')
const webRoot = dirname(fileURLToPath(import.meta.url))

function backendReadyReloadPlugin(): Plugin {
  return {
    name: 'anchorshell-backend-ready-reload',
    apply: 'serve',
    configureServer(server) {
      if (!devBackendReloadFile) {
        return
      }

      const reloadFile = resolve(devBackendReloadFile)
      let lastStamp = getReloadFileStamp(reloadFile)

      const maybeReload = (changedPath: string) => {
        if (resolve(changedPath) !== reloadFile) {
          return
        }

        const nextStamp = getReloadFileStamp(reloadFile)
        if (!nextStamp || nextStamp === lastStamp) {
          return
        }

        lastStamp = nextStamp
        server.ws.send({ type: 'full-reload', path: '*' })
      }

      server.watcher.add([reloadFile, dirname(reloadFile)])
      server.watcher.on('add', maybeReload)
      server.watcher.on('change', maybeReload)
    }
  }
}

function getReloadFileStamp(path: string) {
  try {
    const stat = statSync(path)
    return `${stat.mtimeMs}:${stat.size}`
  } catch {
    return ''
  }
}

export default defineNuxtConfig({
  extends: ['./layers/model-relay-base'],
  compatibilityDate: '2026-03-27',
  srcDir: '.',
  devtools: { enabled: false },
  ssr: false,
  modules: ['@pinia/nuxt', '@nuxt/ui'],
  ui: {
    fonts: false
  },
  colorMode: {
    preference: 'system',
    fallback: 'dark',
    storageKey: 'anchorshell-site-theme'
  },
  app: {
    head: {
      title: 'Model Relay | AnchorShell',
      meta: [
        { name: 'color-scheme', content: 'dark light' },
        { name: 'apple-mobile-web-app-title', content: 'AnchorShell' }
      ],
      link: [
        { rel: 'icon', type: 'image/png', href: '/favicon-96x96.png', sizes: '96x96' },
        { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' },
        { rel: 'shortcut icon', href: '/favicon.ico' },
        { rel: 'apple-touch-icon', sizes: '180x180', href: '/apple-touch-icon.png' },
        { rel: 'manifest', href: '/site.webmanifest' }
      ]
    }
  },
  nitro: {
    prerender: {
      routes: ['/', '/contact', '/privacy', '/terms', '/security'],
      ignore: adminPrerenderRoutes
    }
  },
  runtimeConfig: {
    public: {
      relayWsTarget,
      relayApiPrefix: '/api',
      relayUpgradeUrl: DEFAULT_RELAY_UPGRADE_URL,
      modelRelayApiBase
    }
  },
  devServer: {
    host: devHost,
    port: devPort
  },
  vite: {
    resolve: {
      alias: {
        'tailwindcss/colors': resolve(webRoot, 'node_modules/tailwindcss/dist/colors.mjs')
      },
      dedupe: ['tailwindcss']
    },
    optimizeDeps: {
      include: [
        'tailwindcss/colors',
        'echarts/core',
        'echarts/charts',
        'echarts/components',
        'echarts/renderers'
      ]
    },
    plugins: [backendReadyReloadPlugin()],
    server: {
      hmr: {
        protocol: devHmrProtocol,
        host: devHmrHost,
        port: devHmrPort,
        clientPort: devHmrPort
      },
      proxy: {
        '/api': {
          target: devApiTarget,
          changeOrigin: true,
          ws: true
        },
        '/v1': {
          target: devApiTarget,
          changeOrigin: true,
          ws: true
        }
      }
    }
  },
  css: ['~/assets/css/tailwind.css']
})
