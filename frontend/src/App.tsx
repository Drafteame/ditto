import { useCallback, useEffect, useRef } from 'react'
import type { LogEntry } from './types'
import { useAppShellState } from './hooks/useAppShellState'
import { useSSE } from './hooks/useSSE'
import { useSequenceEvents } from './hooks/useSequenceEvents'
import { useToast } from './hooks/useToast'
import { useAppShortcuts } from './hooks/useAppShortcuts'
import * as api from './api'
import { useEventTemplateStore } from './stores/useEventTemplateStore'
import { useSequenceStore } from './stores/useSequenceStore'
import { useLogStore } from './stores/useLogStore'
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
  const { connected, selectedLogId, selectedEntry, gapNotice, setConnected, appendLogEvents, clearLog, selectLog, setGapNotice, setSelectedEntry } = log
  const { sidebarOpen, sidebarCollapsed, activeView, drawerWidth, updateInfo, modalState, qrOpen, setSidebarOpen, toggleSidebarOpen, setSidebarCollapsed, toggleSidebarCollapsed, setDrawerWidth, setUpdateInfo, setModalState, setQrOpen, setActiveView } = ui
  const { connectedClientCount, channelCount, eventTemplateCount, sequenceCount, recordingCount } = counts
  const { toasts, showToast } = useToast()

  const isDesktop = useRef(isInsideWails()).current
  const isMobile = useRef(isMobileDevice()).current
  const socketRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const modeRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const recordingRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

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
    if (socketRefreshTimer.current) return
    socketRefreshTimer.current = setTimeout(() => {
      useSocketStore.getState().loadClients()
      socketRefreshTimer.current = null
    }, 250)
  }, [])

  const scheduleModeRefresh = useCallback(() => {
    if (modeRefreshTimer.current) return
    modeRefreshTimer.current = setTimeout(() => {
      useChannelModeStore.getState().loadModes()
      modeRefreshTimer.current = null
    }, 250)
  }, [])

  const scheduleRecordingRefresh = useCallback(() => {
    if (recordingRefreshTimer.current) return
    recordingRefreshTimer.current = setTimeout(() => {
      useRecordingStore.getState().loadRecordings()
      recordingRefreshTimer.current = null
    }, 250)
  }, [])

  useSSE(
    useCallback((events) => {
      appendLogEvents(events)
      events.forEach(event => {
        advanceSequenceCursor(event)
        if (event.type === 'SOCKET' && ['CONNECT', 'CLOSE', 'SUBSCRIBE', 'UNSUBSCRIBE', 'LIVE_CONNECT', 'LIVE_CLOSE'].includes(event.method)) {
          scheduleSocketClientRefresh()
        }
        if (event.type === 'MODE') scheduleModeRefresh()
        if (event.type === 'RECORD') scheduleRecordingRefresh()
      })
    }, [advanceSequenceCursor, appendLogEvents, scheduleModeRefresh, scheduleRecordingRefresh, scheduleSocketClientRefresh]),
    useCallback(() => setConnected(true), [setConnected]),
    useCallback(() => setConnected(false), [setConnected]),
    useCallback(() => refreshData(), [refreshData]),
    useCallback(async (since: string) => {
      return api.fetchLogHistory({ since, limit: 5000 })
    }, []),
    useCallback((gap, marker) => {
      const reason = gap?.reason ?? marker.gap_reason ?? 'stream_gap'
      const suffix = ` (${reason.replace(/_/g, ' ')})`
      if (reason === 'server_reset') clearLog()
      setGapNotice(`Event history had a gap${suffix}. Retained events were reloaded; older evicted events may be missing.`)
    }, [clearLog, setGapNotice]),
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
      if (modeRefreshTimer.current) {
        clearTimeout(modeRefreshTimer.current)
      }
      if (recordingRefreshTimer.current) {
        clearTimeout(recordingRefreshTimer.current)
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

  const handleSaveAsMock = useCallback(async (summary: LogEntry) => {
    let entry: LogEntry
    try {
      entry = await api.fetchLogDetail(summary.id)
    } catch (err) {
      showToast(`This event is no longer available: ${(err as Error).message}`, 'warn')
      return
    }
    const captureStatus = entry.response_payload?.capture_status
    if (entry.error || ['binary', 'truncated', 'error', 'unavailable', 'not_captured'].includes(captureStatus ?? '')) {
      showToast('This response cannot be saved as a mock because its body is binary, incomplete, or unavailable.', 'warn')
      return
    }
    try {
      JSON.parse(entry.response_body || '')
    } catch {
      showToast('This mock editor only supports JSON response bodies. The captured response was left unchanged.', 'warn')
      return
    }
    const transportHeaders = new Set([
      'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer',
      'transfer-encoding', 'upgrade', 'content-length', 'content-encoding', 'set-cookie',
    ])
    const headers = Object.fromEntries(
      Object.entries(entry.response_headers ?? {})
        .filter(([name]) => !transportHeaders.has(name.toLowerCase()))
        .map(([name, values]) => [name, values.join(', ')]),
    )
    setModalState(createNewMockState(entry.method, entry.path, entry.status, entry.response_body, headers))
  }, [setModalState, showToast])

  const handleSelectLog = useCallback((id: string | null) => {
    selectLog(id)
    if (!id) return
    if (useLogStore.getState().selectedEntry) return
    api.fetchLogDetail(id).then(entry => {
      if (useLogStore.getState().selectedLogId === id) setSelectedEntry(entry)
    }).catch(() => {
      if (useLogStore.getState().selectedLogId === id) setGapNotice('This event is no longer retained. Its summary remains available in the log history.')
    })
  }, [selectLog, setGapNotice, setSelectedEntry])

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

  const View = views[activeView]

  return (
    <AppShell
      activeView={activeView}
      channelCount={channelCount}
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
      gapNotice={gapNotice}
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
      onDismissGap={() => setGapNotice(null)}
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
        onSelectLog={handleSelectLog}
        onSaveAsMock={handleSaveAsMock}
        showToast={showToast}
      />
    </AppShell>
  )
}
