import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from 'react'
import { Alert } from './icons'

export interface ConfirmOptions {
  title?: string
  message: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
}

type ConfirmFn = (opts: ConfirmOptions | string) => Promise<boolean>

const ConfirmContext = createContext<ConfirmFn | null>(null)

export function useConfirm(): ConfirmFn {
  const fn = useContext(ConfirmContext)
  if (!fn) throw new Error('useConfirm must be used inside ConfirmProvider')
  return fn
}

interface PendingState {
  opts: ConfirmOptions
  resolve: (value: boolean) => void
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<PendingState | null>(null)

  const confirm = useCallback<ConfirmFn>(opts => {
    const options: ConfirmOptions = typeof opts === 'string' ? { message: opts } : opts
    return new Promise<boolean>(resolve => {
      setPending({ opts: options, resolve })
    })
  }, [])

  const close = useCallback(
    (ok: boolean) => {
      if (!pending) return
      pending.resolve(ok)
      setPending(null)
    },
    [pending],
  )

  useEffect(() => {
    if (!pending) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close(false)
      else if (e.key === 'Enter') close(true)
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [pending, close])

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {pending && (
        <div className="modal-scrim" onMouseDown={() => close(false)}>
          <div
            className="modal"
            onMouseDown={e => e.stopPropagation()}
            style={{ width: 420 }}
            role="dialog"
            aria-modal="true"
          >
            <div className="modal-head">
              <div
                className="flex items-center justify-center w-7 h-7 rounded-md"
                style={{
                  color: pending.opts.danger ? 'var(--err)' : 'var(--accent)',
                  background: pending.opts.danger
                    ? 'color-mix(in oklch, var(--err) 12%, transparent)'
                    : 'color-mix(in oklch, var(--accent) 12%, transparent)',
                }}
              >
                <Alert />
              </div>
              <h2>{pending.opts.title ?? 'Confirm'}</h2>
            </div>
            <div className="modal-body text-[13px] text-fg-1 leading-relaxed">
              {pending.opts.message}
            </div>
            <div className="modal-foot">
              <button type="button" className="btn ghost" onClick={() => close(false)}>
                {pending.opts.cancelLabel ?? 'Cancel'}
              </button>
              <button
                type="button"
                className={`btn ${pending.opts.danger ? 'danger' : 'primary'}`}
                onClick={() => close(true)}
                autoFocus
              >
                {pending.opts.confirmLabel ?? 'Confirm'}
              </button>
            </div>
          </div>
        </div>
      )}
    </ConfirmContext.Provider>
  )
}
