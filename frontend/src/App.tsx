import { useState, useCallback, useEffect, useRef } from 'react'
import type { LogEntry, Mock, ServerInfo, UpdateInfo } from './types'
import { useSSE } from './hooks/useSSE'
import { useToast } from './hooks/useToast'
import * as api from './api'
import { Header } from './components/Header'
import { UpdateBanner } from './components/UpdateBanner'
import { Sidebar } from './components/Sidebar'
import { LogPanel } from './components/LogPanel'
import { MockEditorModal, createNewMockState, createEditMockState } from './components/MockEditorModal'
import type { MockEditorState } from './components/MockEditorModal'
import { QRModal } from './components/QRModal'
import { Footer } from './components/Footer'
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
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null)
  const [modalState, setModalState] = useState<MockEditorState | null>(null)
  const [qrOpen, setQrOpen] = useState(false)
  const { toasts, showToast } = useToast()

  const isDesktop = useRef(isInsideWails()).current
  const isMobile = useRef(isMobileDevice()).current

  // Load mocks and server info
  const loadMocks = useCallback(async () => {
    try {
      const data = await api.fetchMocks()
      setMocks(data.mocks)
      setServerInfo(data.info)
    } catch (err) {
      console.error('Failed to load mocks:', err)
    }
  }, [])

  // SSE connection
  useSSE(
    useCallback((event) => {
      const entry: LogEntry = { ...event, id: String(++nextLogId) }
      setLogEntries(prev => [...prev, entry])
    }, []),
    useCallback(() => {
      setConnected(true)
      loadMocks()
    }, [loadMocks]),
    useCallback(() => setConnected(false), []),
    useCallback(() => loadMocks(), [loadMocks]),
  )

  // Initial load
  useEffect(() => {
    loadMocks()
    api.fetchUpdateCheck().then(data => {
      if (data.available) setUpdateInfo(data)
    }).catch(() => {})
  }, [loadMocks])

  // Keyboard shortcuts
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setModalState(null)
        setQrOpen(false)
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  // Actions
  const handleReloadMocks = useCallback(async () => {
    await api.reloadMocks()
    await loadMocks()
  }, [loadMocks])

  const handleClearLog = useCallback(() => {
    setLogEntries([])
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

      <main className="flex flex-1 overflow-hidden">
        <Sidebar
          open={sidebarOpen}
          mocks={mocks}
          serverInfo={serverInfo}
          onClose={() => setSidebarOpen(false)}
          onMocksChanged={loadMocks}
          onEditMock={handleEditMock}
          onCreateMock={handleCreateMock}
          showToast={showToast}
        />
        <LogPanel entries={logEntries} onSaveAsMock={handleSaveAsMock} />
      </main>

      <Footer />

      {/* Modals */}
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
