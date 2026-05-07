import { create } from 'zustand'
import type { Toast } from '../types'

let nextToastId = 0
const toastTimers = new Map<string, ReturnType<typeof setTimeout>>()

interface ToastStore {
  toasts: Toast[]
  showToast: (message: string, kind?: 'warn') => void
}

export const useToastStore = create<ToastStore>((set) => ({
  toasts: [],

  showToast: (message, kind) => {
    const id = String(++nextToastId)
    set((state) => ({ toasts: [...state.toasts, { id, message, kind }] }))

    const timer = setTimeout(() => {
      set((state) => ({ toasts: state.toasts.filter((toast) => toast.id !== id) }))
      toastTimers.delete(id)
    }, 3000)
    toastTimers.set(id, timer)
  },
}))
