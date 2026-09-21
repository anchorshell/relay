export function useModelRelayRoute() {
  const config = useRuntimeConfig()
  const base = computed(() => String(config.public.modelRelayRouteBase || '').replace(/\/+$/, ''))
  const apiBase = computed(() => String(config.public.modelRelayApiBase || '').replace(/\/+$/, ''))

  function modelRelayPath(path: string) {
    const normalized = path.startsWith('/') ? path : `/${path}`
    return `${base.value}${normalized}`
  }

  function modelRelayApiPath(path: string) {
    const normalized = path.startsWith('/') ? path : `/${path}`
    return `${apiBase.value}${normalized}`
  }

  return {
    modelRelayApiPath,
    modelRelayPath
  }
}
