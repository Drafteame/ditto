import { useEffect } from 'react'
import { LOG_SEARCH_INPUT_ID } from '../components/LogPanel'

interface AppShortcutHandlers {
  onEscape: () => void
  onToggleSidebar: () => void
  onToggleSidebarCollapsed: () => void
  onClearLog: () => void
}

export function useAppShortcuts({
  onEscape,
  onToggleSidebar,
  onToggleSidebarCollapsed,
  onClearLog,
}: AppShortcutHandlers) {
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onEscape()
        return
      }

      const mod = e.metaKey || e.ctrlKey
      if (!mod) return

      const key = e.key.toLowerCase()
      if (key === 'k') {
        e.preventDefault()
        const input = document.getElementById(LOG_SEARCH_INPUT_ID) as HTMLInputElement | null
        input?.focus()
        input?.select()
      } else if (key === '\\') {
        e.preventDefault()
        if (window.matchMedia('(min-width: 768px)').matches) {
          onToggleSidebarCollapsed()
        } else {
          onToggleSidebar()
        }
      } else if (key === 'l') {
        e.preventDefault()
        onClearLog()
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [onClearLog, onEscape, onToggleSidebar, onToggleSidebarCollapsed])
}
