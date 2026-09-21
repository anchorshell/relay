import { defineStore } from 'pinia'

export interface ToastItem {
  id: number
  title: string
  description?: string
  tone?: 'default' | 'success' | 'error'
}

export const useAppStore = defineStore('app-shell', {
  state: () => ({
    toasts: [] as ToastItem[],
    currentNav: 'dashboard'
  }),
  actions: {
    pushToast(toast: Omit<ToastItem, 'id'>) {
      const id = Date.now() + Math.floor(Math.random() * 1000)
      this.toasts.push({ id, ...toast })
      setTimeout(() => {
        this.toasts = this.toasts.filter(item => item.id !== id)
      }, 4200)
    }
  }
})
