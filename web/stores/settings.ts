import { defineStore } from 'pinia'
import type { AppSetting, SystemInfo } from '~/types/admin'

export const useSettingsStore = defineStore('settings', {
  state: () => ({
    settings: [] as AppSetting[],
    system: null as SystemInfo | null,
    loading: false,
    lastError: ''
  }),
  getters: {
    parsed: state => Object.fromEntries(state.settings.map(item => {
      try {
        return [item.key, JSON.parse(item.value_json)]
      } catch {
        return [item.key, item.value_json]
      }
    }))
  },
  actions: {
    hydrate(settings?: AppSetting[], system?: SystemInfo) {
      if (Array.isArray(settings)) this.settings = settings
      if (system) this.system = system
    },
    async refresh() {
      this.loading = true
      this.lastError = ''
      const api = useRelayApi()
      try {
        const [settings, system] = await Promise.all([
          api.get<AppSetting[]>('/api/settings'),
          api.get<SystemInfo>('/api/system')
        ])
        this.hydrate(settings, system)
      } catch (error: any) {
        this.lastError = error?.data?.message || error?.message || 'Unable to load settings.'
      } finally {
        this.loading = false
      }
    },
    async save(key: string, value: any) {
      await this.saveMany([{ key, value }])
    },
    async saveMany(entries: Array<{ key: string, value: any }>) {
      if (!entries.length) return
      const updated = await useRelayApi().put<AppSetting[]>(
        '/api/settings',
        Object.fromEntries(entries.map(entry => [entry.key, entry.value]))
      )
      this.hydrate(updated)
    }
  }
})
