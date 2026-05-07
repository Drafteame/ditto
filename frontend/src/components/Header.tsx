import { useCallback } from 'react'
import * as api from '../api'
import { LogoMark, Refresh, Trash, QR, Globe, Menu } from './icons'

interface HeaderProps {
  version: string
  connected: boolean
  isDesktop: boolean
  isMobile: boolean
  onReloadMocks: () => void
  onClearLog: () => void
  onShowQR: () => void
  onToggleSidebar: () => void
}

export function Header({
  version,
  connected,
  isDesktop,
  isMobile,
  onReloadMocks,
  onClearLog,
  onShowQR,
  onToggleSidebar,
}: HeaderProps) {
  const handleOpenBrowser = useCallback(() => {
    api.openInBrowser()
  }, [])

  const showBrowserBtn = isDesktop
  const showQRBtn = !isMobile

  return (
    <header className="h-[52px] flex items-center gap-2.5 px-3.5 border-b border-line bg-bg-1 flex-shrink-0">
      <button
        type="button"
        onClick={onToggleSidebar}
        data-tip="Toggle sidebar (⌘\)"
        data-tip-side="bottom"
        aria-label="Toggle sidebar"
        className="btn ghost icon md:hidden"
      >
        <Menu />
      </button>

      <div className="flex items-center gap-2.5">
        <LogoMark size={26} />
        <span className="font-bold text-[15px] tracking-[0.01em] text-fg-0">Ditto</span>
        {version && (
          <span className="font-mono text-[11px] text-fg-3 px-1.5 py-0.5 border border-line rounded-xs">
            {version}
          </span>
        )}
      </div>

      <span className={`pill ${connected ? 'ok' : 'err'}`}>
        <span className="dot" />
        {connected ? 'Connected' : 'Disconnected'}
      </span>

      <div className="flex-1" />

      <div className="flex items-center gap-1.5">
        {showBrowserBtn && (
          <button
            type="button"
            onClick={handleOpenBrowser}
            data-tip="Open dashboard in your browser"
            data-tip-side="bottom"
            className="btn ghost"
          >
            <Globe />
            <span className="max-md:hidden">Browser</span>
          </button>
        )}
        {showQRBtn && (
          <button
            type="button"
            onClick={onShowQR}
            data-tip="Show QR code to connect your phone"
            data-tip-side="bottom"
            className="btn ghost"
          >
            <QR />
            <span className="max-md:hidden">QR</span>
          </button>
        )}
        <button
          type="button"
          onClick={onReloadMocks}
          className="btn ghost"
          data-tip="Reload mock files from disk"
          data-tip-side="bottom"
        >
          <Refresh />
          <span className="max-md:hidden">Reload</span>
        </button>
        <button
          type="button"
          onClick={onClearLog}
          className="btn ghost"
          data-tip="Clear request log (⌘L)"
          data-tip-side="bottom"
        >
          <Trash />
          <span className="max-md:hidden">Clear</span>
        </button>
      </div>
    </header>
  )
}
