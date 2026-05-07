import { useShallow } from 'zustand/react/shallow'
import { useAppUiStore } from '../stores/useAppUiStore'
import { useEventTemplateStore } from '../stores/useEventTemplateStore'
import { useLogStore } from '../stores/useLogStore'
import { useMockStore } from '../stores/useMockStore'
import { useSequenceStore } from '../stores/useSequenceStore'
import { useSocketStore } from '../stores/useSocketStore'

export function useAppShellState() {
  const mock = useMockStore(useShallow(state => ({
    mocks: state.mocks,
    serverInfo: state.serverInfo,
    loadMocks: state.loadMocks,
    reloadMocks: state.reloadMocks,
    advanceSequenceCursor: state.advanceSequenceCursor,
  })))
  const log = useLogStore(useShallow(state => ({
    logEntries: state.logEntries,
    connected: state.connected,
    selectedLogId: state.selectedLogId,
    setConnected: state.setConnected,
    appendLogEvent: state.appendLogEvent,
    clearLog: state.clearLog,
    selectLog: state.selectLog,
  })))
  const ui = useAppUiStore(useShallow(state => ({
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

  return {
    mock,
    log,
    ui,
    counts: {
      connectedClientCount: useSocketStore(state => state.connectedClients.length),
      eventTemplateCount: useEventTemplateStore(state => state.templates.length),
      sequenceCount: useSequenceStore(state => state.sequences.length),
    },
  }
}
