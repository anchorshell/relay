import { defineStore } from 'pinia'
import type { AdminPageKey } from '~/types/admin'

export const useAdminPageDataStore = defineStore('adminPageData', {
  state: () => ({
    loading: false,
    pageLoading: {} as Partial<Record<AdminPageKey, boolean>>,
    refreshSeq: 0,
    lastError: ''
  }),
  actions: {
    async load(page: AdminPageKey) {
      const refreshSeq = ++this.refreshSeq
      this.loading = true
      this.pageLoading[page] = true
      this.lastError = ''
      try {
        const payload = await useRelayApi().pageData(page)
        if (refreshSeq !== this.refreshSeq) return

        useCatalogStore().hydrate(payload.catalog)
        useQueueStore().hydrate(payload.queue_items)
        useSettingsStore().hydrate(payload.settings, payload.system)
      } catch (error: any) {
        if (refreshSeq !== this.refreshSeq) return
        this.lastError = error?.data?.message || error?.message || 'Unable to load Model Relay data.'
      } finally {
        if (refreshSeq === this.refreshSeq) {
          this.loading = false
        }
        this.pageLoading[page] = false
      }
    }
  }
})
