import { useToastStore } from '../stores/useToastStore'

export function useToast() {
  const toasts = useToastStore(state => state.toasts)
  const showToast = useToastStore(state => state.showToast)
  return { toasts, showToast }
}
