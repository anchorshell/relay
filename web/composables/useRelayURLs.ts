import { resolveRelayAPIURL, resolveRelayWebSocketURL } from '../utils/relayURLs'
import { DEFAULT_RELAY_PORT } from '../utils/relayDefaults'

export function useRelayURLs() {
  const config = useRuntimeConfig()
  const origin = String(config.public.modelRelayApiBase || '')
  const prefix = String(config.public.relayApiPrefix || '/api')

  return {
    apiURL: (path: string) => resolveRelayAPIURL(path, origin, prefix),
    webSocketURL: (path: string) => {
      let target = String(config.public.relayWsTarget || origin)
      if (!target && import.meta.dev && ['localhost', '127.0.0.1'].includes(location.hostname) && location.port === '3030') {
        target = `${location.protocol}//${location.hostname}:${DEFAULT_RELAY_PORT}`
      }
      return resolveRelayWebSocketURL(path, target, location.origin, prefix)
    }
  }
}
