// Component-local memory only: no Nuxt useState payload, Pinia persistence,
// localStorage, sessionStorage, cookies, or server-provided credentials.
export function useInferenceToken() {
  const token = ref('')
  const auth = useAuthStore()
  const clear = () => { token.value = '' }
  watch(() => auth.authenticated, authenticated => { if (!authenticated) clear() })
  onMounted(() => window.addEventListener('pagehide', clear))
  onBeforeUnmount(() => {
    clear()
    window.removeEventListener('pagehide', clear)
  })
  return token
}
