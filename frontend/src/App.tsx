import { useState, useCallback, useEffect, useRef, useMemo } from 'react'
import type { LogEntry, Mock, ServerInfo, UpdateInfo } from './types'
import { useSSE } from './hooks/useSSE'
import { useToast } from './hooks/useToast'
import * as api from './api'
import { Header } from './components/Header'
import { UpdateBanner } from './components/UpdateBanner'
import { Sidebar, CollapsedSidebarRail } from './components/Sidebar'
import { LogPanel, LOG_SEARCH_INPUT_ID } from './components/LogPanel'
import { Drawer } from './components/Drawer'
import { MockEditorModal, createNewMockState, createEditMockState } from './components/MockEditorModal'
import type { MockEditorState } from './components/MockEditorModal'
import { QRModal } from './components/QRModal'
import { ToastContainer } from './components/ToastContainer'

let nextLogId = 0

function isInsideWails(): boolean {
  return new URLSearchParams(window.location.search).get('desktop') === '1'
}

function isMobileDevice(): boolean {
  return /iPhone|iPad|iPod|Android/i.test(navigator.userAgent)
}

export default function App() {
  const [mocks, setMocks] = useState<Mock[]>([])
  const [serverInfo, setServerInfo] = useState<ServerInfo | null>(null)
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const [connected, setConnected] = useState(false)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [drawerWidth, setDrawerWidth] = useState(480)
  const [selectedLogId, setSelectedLogId] = useState<string | null>(null)
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null)
  const [modalState, setModalState] = useState<MockEditorState | null>(null)
  const [qrOpen, setQrOpen] = useState(false)
  const { toasts, showToast } = useToast()

  const isDesktop = useRef(isInsideWails()).current
  const isMobile = useRef(isMobileDevice()).current

  const loadMocks = useCallback(async () => {
    try {
      const data = await api.fetchMocks()
      setMocks(data.mocks)
      setServerInfo(data.info)
    } catch (err) {
      console.error('Failed to load mocks:', err)
    }
  }, [])

  useSSE(
    useCallback((event) => {
      const entry: LogEntry = { ...event, id: String(++nextLogId) }
      setLogEntries(prev => [...prev, entry])

      // Advance the local sequence counter so the sidebar badge stays in sync
      // with the backend's in-memory cursor without a full refetch per request.
      if (
        event.has_mock &&
        event.sequence_step &&
        event.sequence_len &&
        typeof event.mock_index === 'number'
      ) {
        const served = event.sequence_step // 1-based
        const len = event.sequence_len
        const idx = event.mock_index
        setMocks(prev => {
          const target = prev[idx]
          if (!target?.sequence) return prev
          const onEnd = target.sequence.on_end
          let next: number
          if (onEnd === 'stay') next = Math.min(served, len - 1)
          else if (onEnd === 'reset') next = served // may equal len (next call serves fallback)
          else next = served % len // loop (default)
          if (target.sequence.current_step === next) return prev
          const copy = prev.slice()
          copy[idx] = {
            ...target,
            sequence: { ...target.sequence, current_step: next },
          }
          return copy
        })
      }
    }, []),
    useCallback(() => {
      setConnected(true)
      loadMocks()
    }, [loadMocks]),
    useCallback(() => setConnected(false), []),
    useCallback(() => loadMocks(), [loadMocks]),
  )

  useEffect(() => {
    loadMocks()
    api.fetchUpdateCheck().then(data => {
      if (data.available) setUpdateInfo(data)
    }).catch(() => {})
  }, [loadMocks])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setModalState(null)
        setQrOpen(false)
        setSelectedLogId(null)
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
        const isDesktopViewport = window.matchMedia('(min-width: 768px)').matches
        if (isDesktopViewport) {
          setSidebarCollapsed(c => !c)
        } else {
          setSidebarOpen(o => !o)
        }
      } else if (key === 'l') {
        e.preventDefault()
        setLogEntries([])
        setSelectedLogId(null)
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  const handleReloadMocks = useCallback(async () => {
    await api.reloadMocks()
    await loadMocks()
  }, [loadMocks])

  const handleClearLog = useCallback(() => {
    setLogEntries([])
    setSelectedLogId(null)
  }, [])

  const handleSaveAsMock = useCallback((entry: LogEntry) => {
    setModalState(createNewMockState(entry.method, entry.path, entry.status, entry.response_body))
  }, [])

  const handleCreateMock = useCallback(() => {
    setModalState(createNewMockState('GET', '', 200))
  }, [])

  const handleEditMock = useCallback(async (index: number) => {
    try {
      const data = await api.fetchMocks()
      const mock = data.mocks[index]
      if (mock) setModalState(createEditMockState(index, mock))
    } catch (err) {
      console.error('Failed to load mock for editing:', err)
    }
  }, [])

  const selectedEntry = useMemo(
    () => (selectedLogId ? logEntries.find(e => e.id === selectedLogId) ?? null : null),
    [selectedLogId, logEntries],
  )

  return (
    <>
      <Header
        version={serverInfo?.version || ''}
        connected={connected}
        isDesktop={isDesktop}
        isMobile={isMobile}
        onReloadMocks={handleReloadMocks}
        onClearLog={handleClearLog}
        onShowQR={() => setQrOpen(true)}
        onToggleSidebar={() => setSidebarOpen(prev => !prev)}
      />

      {updateInfo && (
        <UpdateBanner info={updateInfo} onDismiss={() => setUpdateInfo(null)} />
      )}

      <main className="flex flex-1 overflow-hidden min-h-0">
        {sidebarCollapsed && <CollapsedSidebarRail onExpand={() => setSidebarCollapsed(false)} />}
        <Sidebar
          open={sidebarOpen}
          collapsed={sidebarCollapsed}
          mocks={mocks}
          serverInfo={serverInfo}
          onClose={() => setSidebarOpen(false)}
          onCollapse={() => setSidebarCollapsed(true)}
          onMocksChanged={loadMocks}
          onEditMock={handleEditMock}
          onCreateMock={handleCreateMock}
          showToast={showToast}
        />
        <LogPanel
          entries={logEntries}
          serverInfo={serverInfo}
          selectedId={selectedLogId}
          onSelect={setSelectedLogId}
          onSaveAsMock={handleSaveAsMock}
        />
        {selectedEntry && (
          <Drawer
            entry={selectedEntry}
            serverInfo={serverInfo}
            width={drawerWidth}
            onResize={setDrawerWidth}
            onClose={() => setSelectedLogId(null)}
            onSaveAsMock={handleSaveAsMock}
          />
        )}
      </main>

      {modalState && (
        <MockEditorModal
          state={modalState}
          onClose={() => setModalState(null)}
          onSaved={loadMocks}
          showToast={showToast}
        />
      )}
      {qrOpen && <QRModal onClose={() => setQrOpen(false)} />}

      <ToastContainer toasts={toasts} />
    </>
  )
}
