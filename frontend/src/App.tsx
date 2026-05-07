import { useCallback, useEffect, useRef, useMemo } from 'react'
import type { LogEntry } from './types'
import { useSSE } from './hooks/useSSE'
import { useToast } from './hooks/useToast'
import * as api from './api'
import { useAppUiStore } from './stores/useAppUiStore'
import { useLogStore } from './stores/useLogStore'
import { useMockStore } from './stores/useMockStore'
import { Header } from './components/Header'
import { UpdateBanner } from './components/UpdateBanner'
import { Sidebar, CollapsedSidebarRail } from './components/Sidebar'
import { LogPanel, LOG_SEARCH_INPUT_ID } from './components/LogPanel'
import { Drawer } from './components/Drawer'
import { MockEditorModal, createNewMockState, createEditMockState } from './components/MockEditorModal'
import { QRModal } from './components/QRModal'
import { ToastContainer } from './components/ToastContainer'

function isInsideWails(): boolean {
  return new URLSearchParams(window.location.search).get('desktop') === '1'
}

function isMobileDevice(): boolean {
  return /iPhone|iPad|iPod|Android/i.test(navigator.userAgent)
}

export default function App() {
  const mocks = useMockStore(state => state.mocks)
  const serverInfo = useMockStore(state => state.serverInfo)
  const loadMocks = useMockStore(state => state.loadMocks)
  const reloadMocks = useMockStore(state => state.reloadMocks)
  const advanceSequenceCursor = useMockStore(state => state.advanceSequenceCursor)
  const logEntries = useLogStore(state => state.logEntries)
  const connected = useLogStore(state => state.connected)
  const selectedLogId = useLogStore(state => state.selectedLogId)
  const setConnected = useLogStore(state => state.setConnected)
  const appendLogEvent = useLogStore(state => state.appendLogEvent)
  const clearLog = useLogStore(state => state.clearLog)
  const selectLog = useLogStore(state => state.selectLog)
  const sidebarOpen = useAppUiStore(state => state.sidebarOpen)
  const sidebarCollapsed = useAppUiStore(state => state.sidebarCollapsed)
  const drawerWidth = useAppUiStore(state => state.drawerWidth)
  const updateInfo = useAppUiStore(state => state.updateInfo)
  const modalState = useAppUiStore(state => state.modalState)
  const qrOpen = useAppUiStore(state => state.qrOpen)
  const setSidebarOpen = useAppUiStore(state => state.setSidebarOpen)
  const toggleSidebarOpen = useAppUiStore(state => state.toggleSidebarOpen)
  const setSidebarCollapsed = useAppUiStore(state => state.setSidebarCollapsed)
  const toggleSidebarCollapsed = useAppUiStore(state => state.toggleSidebarCollapsed)
  const setDrawerWidth = useAppUiStore(state => state.setDrawerWidth)
  const setUpdateInfo = useAppUiStore(state => state.setUpdateInfo)
  const setModalState = useAppUiStore(state => state.setModalState)
  const setQrOpen = useAppUiStore(state => state.setQrOpen)
  const { toasts, showToast } = useToast()

  const isDesktop = useRef(isInsideWails()).current
  const isMobile = useRef(isMobileDevice()).current

  useSSE(
    useCallback((event) => {
      appendLogEvent(event)
      advanceSequenceCursor(event)
    }, [advanceSequenceCursor, appendLogEvent]),
    useCallback(() => {
      setConnected(true)
      loadMocks()
    }, [loadMocks, setConnected]),
    useCallback(() => setConnected(false), [setConnected]),
    useCallback(() => loadMocks(), [loadMocks]),
  )

  useEffect(() => {
    loadMocks()
    api.fetchUpdateCheck().then(data => {
      if (data.available) setUpdateInfo(data)
    }).catch(() => {})
  }, [loadMocks, setUpdateInfo])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setModalState(null)
        setQrOpen(false)
        selectLog(null)
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
          toggleSidebarCollapsed()
        } else {
          toggleSidebarOpen()
        }
      } else if (key === 'l') {
        e.preventDefault()
        clearLog()
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [
    clearLog,
    selectLog,
    setModalState,
    setQrOpen,
    toggleSidebarCollapsed,
    toggleSidebarOpen,
  ])

  const handleReloadMocks = useCallback(async () => {
    await reloadMocks()
  }, [reloadMocks])

  const handleClearLog = useCallback(() => {
    clearLog()
  }, [clearLog])

  const handleSaveAsMock = useCallback((entry: LogEntry) => {
    setModalState(createNewMockState(entry.method, entry.path, entry.status, entry.response_body))
  }, [setModalState])

  const handleCreateMock = useCallback(() => {
    setModalState(createNewMockState('GET', '', 200))
  }, [setModalState])

  const handleEditMock = useCallback(async (index: number) => {
    try {
      const data = await api.fetchMocks()
      const mock = data.mocks[index]
      if (mock) setModalState(createEditMockState(index, mock))
    } catch (err) {
      console.error('Failed to load mock for editing:', err)
    }
  }, [setModalState])

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
        onToggleSidebar={toggleSidebarOpen}
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
          onSelect={selectLog}
          onSaveAsMock={handleSaveAsMock}
        />
        {selectedEntry && (
          <Drawer
            entry={selectedEntry}
            serverInfo={serverInfo}
            width={drawerWidth}
            onResize={setDrawerWidth}
            onClose={() => selectLog(null)}
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
