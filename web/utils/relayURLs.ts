// Callers use canonical Relay paths. The embedding application selects the
// browser-facing prefix; the API origin is independent of that prefix.
export function resolveRelayAPIPath(path: string, prefix = '/api'): string {
  const base = prefix.replace(/\/+$/, '')
  if (path === '/api') return base
  if (path.startsWith('/api/')) return `${base}${path.slice(4)}`
  return path.startsWith('/') ? path : `/${path}`
}

export function resolveRelayAPIURL(path: string, origin = '', prefix = '/api'): string {
  return `${origin.replace(/\/+$/, '')}${resolveRelayAPIPath(path, prefix)}`
}

export function resolveRelayWebSocketURL(path: string, target: string, browserOrigin: string, prefix = '/api'): string {
  const apiPath = resolveRelayAPIPath(path, prefix)
  let url: URL
  try {
    url = new URL(apiPath, target || browserOrigin)
  } catch {
    url = new URL(apiPath, browserOrigin)
  }
  url.protocol = url.protocol === 'https:' || url.protocol === 'wss:' ? 'wss:' : 'ws:'
  const browser = new URL(browserOrigin)
  if (isLoopback(url.hostname) && isLoopback(browser.hostname)) {
    // Cookies are host-scoped: keep localhost and 127.0.0.1 consistent with
    // the page even when the development backend uses the other spelling.
    url.hostname = browser.hostname
  }
  return url.toString()
}

function isLoopback(hostname: string): boolean {
  return hostname === '127.0.0.1' || hostname === 'localhost'
}
