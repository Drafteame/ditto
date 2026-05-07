import { useCallback, useEffect, useRef, useMemo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry } from './types'
import { useSSE } from './hooks/useSSE'
import { useToast } from './hooks/useToast'
import * as api from './api'
import { useAppUiStore } from './stores/useAppUiStore'
import { useLogStore } from './stores/useLogStore'
import { useMockStore } from './stores/useMockStore'
import { useSocketStore } from './stores/useSocketStore'
import { Header } from './components/Header'
import { UpdateBanner } from './components/UpdateBanner'
import { Sidebar, CollapsedSidebarRail } from './components/Sidebar'
import { LogPanel, LOG_SEARCH_INPUT_ID } from './components/LogPanel'
import { SocketPanel } from './components/SocketPanel'
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
  const { mocks, serverInfo, loadMocks, reloadMocks, advanceSequenceCursor } =
    useMockStore(useShallow(state => ({
      mocks: state.mocks,
      serverInfo: state.serverInfo,
      loadMocks: state.loadMocks,
      reloadMocks: state.reloadMocks,
      advanceSequenceCursor: state.advanceSequenceCursor,
    })))
  const {
    logEntries,
    connected,
    selectedLogId,
    setConnected,
    appendLogEvent,
    clearLog,
    selectLog,
  } = useLogStore(useShallow(state => ({
    logEntries: state.logEntries,
    connected: state.connected,
    selectedLogId: state.selectedLogId,
    setConnected: state.setConnected,
    appendLogEvent: state.appendLogEvent,
    clearLog: state.clearLog,
    selectLog: state.selectLog,
  })))
  const {
    sidebarOpen,
    sidebarCollapsed,
    activeView,
    drawerWidth,
    updateInfo,
    modalState,
    qrOpen,
    setSidebarOpen,
    toggleSidebarOpen,
    setSidebarCollapsed,
    toggleSidebarCollapsed,
    setDrawerWidth,
    setUpdateInfo,
    setModalState,
    setQrOpen,
    setActiveView,
  } = useAppUiStore(useShallow(state => ({
    sidebarOpen: state.sidebarOpen,
    sidebarCollapsed: state.sidebarCollapsed,
    activeView: state.activeView,
    drawerWidth: state.drawerWidth,
    updateInfo: state.updateInfo,
    modalState: state.modalState,
    qrOpen: state.qrOpen,
    setSidebarOpen: state.setSidebarOpen,
    toggleSidebarOpen: state.toggleSidebarOpen,
    setSidebarCollapsed: state.setSidebarCollapsed,
    toggleSidebarCollapsed: state.toggleSidebarCollapsed,
    setDrawerWidth: state.setDrawerWidth,
    setUpdateInfo: state.setUpdateInfo,
    setModalState: state.setModalState,
    setQrOpen: state.setQrOpen,
    setActiveView: state.setActiveView,
  })))
  const {
    connectedClients,
    socketClientsLoading,
    socketClientsError,
    loadSocketClients,
  } = useSocketStore(useShallow(state => ({
    connectedClients: state.connectedClients,
    socketClientsLoading: state.loading,
    socketClientsError: state.error,
    loadSocketClients: state.loadClients,
  })))
  const { toasts, showToast } = useToast()

  const isDesktop = useRef(isInsideWails()).current
  const isMobile = useRef(isMobileDevice()).current
  const socketRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const scheduleSocketClientRefresh = useCallback(() => {
    if (socketRefreshTimer.current) {
      clearTimeout(socketRefreshTimer.current)
    }
    socketRefreshTimer.current = setTimeout(() => {
      loadSocketClients()
      socketRefreshTimer.current = null
    }, 250)
  }, [loadSocketClients])

  useSSE(
    useCallback((event) => {
      appendLogEvent(event)
      advanceSequenceCursor(event)
      if (event.type === 'SOCKET') {
        scheduleSocketClientRefresh()
      }
    }, [advanceSequenceCursor, appendLogEvent, scheduleSocketClientRefresh]),
    useCallback(() => {
      setConnected(true)
      loadMocks()
      loadSocketClients()
    }, [loadMocks, loadSocketClients, setConnected]),
    useCallback(() => setConnected(false), [setConnected]),
    useCallback(() => {
      loadMocks()
      loadSocketClients()
    }, [loadMocks, loadSocketClients]),
  )

  useEffect(() => {
    loadMocks()
    loadSocketClients()
    api.fetchUpdateCheck().then(data => {
      if (data.available) setUpdateInfo(data)
    }).catch(() => {})
  }, [loadMocks, loadSocketClients, setUpdateInfo])

  useEffect(() => {
    return () => {
      if (socketRefreshTimer.current) {
        clearTimeout(socketRefreshTimer.current)
      }
    }
  }, [])

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
        <section className="flex-1 flex flex-col min-w-0 min-h-0">
          <div className="main-tabs">
            <button
              type="button"
              className={activeView === 'requests' ? 'active' : ''}
              onClick={() => setActiveView('requests')}
            >
              Requests
            </button>
            <button
              type="button"
              className={activeView === 'sockets' ? 'active' : ''}
              onClick={() => setActiveView('sockets')}
            >
              Sockets
              <span className="c">{connectedClients.length}</span>
            </button>
          </div>
          {activeView === 'requests' ? (
            <LogPanel
              entries={logEntries}
              serverInfo={serverInfo}
              selectedId={selectedLogId}
              onSelect={selectLog}
              onSaveAsMock={handleSaveAsMock}
            />
          ) : (
            <SocketPanel
              clients={connectedClients}
              entries={logEntries}
              serverInfo={serverInfo}
              loading={socketClientsLoading}
              error={socketClientsError}
              onRefresh={loadSocketClients}
              showToast={showToast}
            />
          )}
        </section>
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
