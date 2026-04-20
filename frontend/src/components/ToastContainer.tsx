import type { Toast } from '../types'
import { Alert, Check } from './icons'

interface ToastContainerProps {
  toasts: Toast[]
}

export function ToastContainer({ toasts }: ToastContainerProps) {
  if (toasts.length === 0) return null

  return (
    <>
      {toasts.map(toast => (
        <div
          key={toast.id}
          className={`toast toast-enter ${toast.kind === 'warn' ? 'warn' : ''}`}
        >
          {toast.kind === 'warn' ? <Alert /> : <Check />}
          <span>{toast.message}</span>
        </div>
      ))}
    </>
  )
}
