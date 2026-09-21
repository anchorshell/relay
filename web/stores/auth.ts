import { defineStore } from 'pinia'
import type { SystemInfo } from '~/types/admin'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    ready: false,
    authenticated: false,
    token: '',
    error: '',
    unlocking: false,
    system: null as SystemInfo | null
  }),
  actions: {
    async bootstrap() {
      if (this.ready) return
      const api = useRelayApi()
      try {
        const status = await api.sessionStatus()
        this.authenticated = status.authenticated
        this.system = status.system
      } catch {
        this.authenticated = false
      } finally {
        this.ready = true
      }
    },
    async unlock(token: string, silent = false) {
      this.unlocking = true
      this.error = ''
      try {
        await $fetch(useRelayURLs().apiURL('/api/session'), {
          method: 'POST',
          credentials: 'include',
          headers: { Authorization: `Bearer ${token}` }
        })
        this.token = token
        this.authenticated = true
      } catch (error: any) {
        this.authenticated = false
        this.error = error?.data?.error ?? error?.data?.message ?? 'Failed to unlock admin console'
        if (!silent) throw error
      } finally {
        this.unlocking = false
      }
    },
    async lock() {
      try {
        await $fetch(useRelayURLs().apiURL('/api/session'), { method: 'DELETE', credentials: 'include', headers: this.token ? { Authorization: `Bearer ${this.token}` } : {} })
      } catch {
        // ignore logout failures and clear local state anyway
      }
      this.authenticated = false
      this.token = ''
    }
  }
})
