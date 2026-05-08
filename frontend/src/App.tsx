import { useCallback, useEffect, useRef, useMemo } from 'react'
import type { LogEntry } from './types'
import { useAppShellState } from './hooks/useAppShellState'
import { useSSE } from './hooks/useSSE'
import { useSequenceEvents } from './hooks/useSequenceEvents'
import { useToast } from './hooks/useToast'
import { useAppShortcuts } from './hooks/useAppShortcuts'
import * as api from './api'
import { useEventTemplateStore } from './stores/useEventTemplateStore'
import { useSequenceStore } from './stores/useSequenceStore'
import { useSchemaStore } from './stores/useSchemaStore'
import { useSocketStore } from './stores/useSocketStore'
import { useChannelModeStore } from './stores/useChannelModeStore'
import { useRecordingStore } from './stores/useRecordingStore'
import { createNewMockState, createEditMockState } from './components/MockEditorModal'
import { AppShell } from './components/AppShell'
import { RequestsView } from './views/RequestsView'
import { SocketsView } from './views/SocketsView'
import { TemplatesView } from './views/TemplatesView'
import { SequencesView } from './views/SequencesView'
import { RecordingsView } from './views/RecordingsView'

function isInsideWails(): boolean { return new URLSearchParams(window.location.search).get('desktop') === '1' }
function isMobileDevice(): boolean { return /iPhone|iPad|iPod|Android/i.test(navigator.userAgent) }

const views = { requests: RequestsView, sockets: SocketsView, templates: TemplatesView, sequences: SequencesView, recordings: RecordingsView }

export default function App() {
  const { mock, log, ui, counts } = useAppShellState()
  const { mocks, serverInfo, loadMocks, reloadMocks, advanceSequenceCursor } = mock
  const { logEntries, connected, selectedLogId, setConnected, appendLogEvent, clearLog, selectLog } = log
  const { sidebarOpen, sidebarCollapsed, activeView, drawerWidth, updateInfo, modalState, qrOpen, setSidebarOpen, toggleSidebarOpen, setSidebarCollapsed, toggleSidebarCollapsed, setDrawerWidth, setUpdateInfo, setModalState, setQrOpen, setActiveView } = ui
  const { connectedClientCount, eventTemplateCount, sequenceCount, recordingCount } = counts
  const { toasts, showToast } = useToast()

  const isDesktop = useRef(isInsideWails()).current
  const isMobile = useRef(isMobileDevice()).current
  const socketRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const refreshData = useCallback(() => {
    loadMocks()
    useSocketStore.getState().loadClients()
    useSocketStore.getState().loadAdapterProfiles()
    useChannelModeStore.getState().loadModes()
    useChannelModeStore.getState().loadLiveTarget()
    useRecordingStore.getState().loadRecordings()
    useSchemaStore.getState().loadSchemas()
    useEventTemplateStore.getState().loadTemplates()
    useSequenceStore.getState().loadSequences()
  }, [loadMocks])

  const scheduleSocketClientRefresh = useCallback(() => {
    if (socketRefreshTimer.current) {
      clearTimeout(socketRefreshTimer.current)
    }
    socketRefreshTimer.current = setTimeout(() => {
      useSocketStore.getState().loadClients()
      socketRefreshTimer.current = null
    }, 250)
  }, [])

  useSSE(
    useCallback((event) => {
      appendLogEvent(event)
      advanceSequenceCursor(event)
      if (event.type === 'SOCKET') {
        scheduleSocketClientRefresh()
      }
      if (event.type === 'MODE') {
        useChannelModeStore.getState().loadModes()
      }
      if (event.type === 'RECORD') {
        useRecordingStore.getState().loadRecordings()
      }
    }, [advanceSequenceCursor, appendLogEvent, scheduleSocketClientRefresh]),
    useCallback(() => {
      setConnected(true)
      refreshData()
    }, [refreshData, setConnected]),
    useCallback(() => setConnected(false), [setConnected]),
    refreshData,
  )

  useSequenceEvents(
    useCallback((event) => {
      useSequenceStore.getState().applyPlayerEvent(event)
    }, []),
    useCallback(() => {
      useSequenceStore.getState().loadPlayerStates()
    }, []),
  )

  useEffect(() => {
    refreshData()
    api.fetchUpdateCheck().then(data => {
      if (data.available) setUpdateInfo(data)
    }).catch(() => {})
  }, [refreshData, setUpdateInfo])

  useEffect(() => {
    return () => {
      if (socketRefreshTimer.current) {
        clearTimeout(socketRefreshTimer.current)
      }
    }
  }, [])

  useAppShortcuts({
    onEscape: useCallback(() => {
      setModalState(null)
      setQrOpen(false)
      selectLog(null)
    }, [selectLog, setModalState, setQrOpen]),
    onToggleSidebar: toggleSidebarOpen,
    onToggleSidebarCollapsed: toggleSidebarCollapsed,
    onClearLog: clearLog,
  })

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

  const View = views[activeView]

  return (
    <AppShell
      activeView={activeView}
      connected={connected}
      connectedClientCount={connectedClientCount}
      drawerWidth={drawerWidth}
      eventTemplateCount={eventTemplateCount}
      isDesktop={isDesktop}
      isMobile={isMobile}
      modalState={modalState}
      mocks={mocks}
      qrOpen={qrOpen}
      selectedEntry={selectedEntry}
      sequenceCount={sequenceCount}
      recordingCount={recordingCount}
      serverInfo={serverInfo}
      sidebarCollapsed={sidebarCollapsed}
      sidebarOpen={sidebarOpen}
      toasts={toasts}
      updateInfo={updateInfo}
      onChangeView={setActiveView}
      onClearLog={handleClearLog}
      onCloseDrawer={() => selectLog(null)}
      onCreateMock={handleCreateMock}
      onEditMock={handleEditMock}
      onMocksChanged={loadMocks}
      onReloadMocks={handleReloadMocks}
      onResizeDrawer={setDrawerWidth}
      onSaveAsMock={handleSaveAsMock}
      onSetModalState={setModalState}
      onSetQrOpen={setQrOpen}
      onSetSidebarCollapsed={setSidebarCollapsed}
      onSetSidebarOpen={setSidebarOpen}
      onSetUpdateInfo={setUpdateInfo}
      onToggleSidebar={toggleSidebarOpen}
      showToast={showToast}
    >
      <View
        serverInfo={serverInfo}
        selectedLogId={selectedLogId}
        onSelectLog={selectLog}
        onSaveAsMock={handleSaveAsMock}
        showToast={showToast}
      />
    </AppShell>
  )
}
