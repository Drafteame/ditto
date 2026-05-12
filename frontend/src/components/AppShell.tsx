import type { ReactNode } from 'react'
import type { LogEntry, Mock, ServerInfo, Toast, UpdateInfo } from '../types'
import type { MockEditorState } from './MockEditorModal'
import { Header } from './Header'
import { UpdateBanner } from './UpdateBanner'
import { Sidebar, CollapsedSidebarRail } from './Sidebar'
import { Drawer } from './Drawer'
import { MainTabs } from './MainTabs'
import { MockEditorModal } from './MockEditorModal'
import { QRModal } from './QRModal'
import { ToastContainer } from './ToastContainer'

type MainView = 'requests' | 'sockets' | 'templates' | 'sequences' | 'recordings'

interface AppShellProps {
  children: ReactNode
  activeView: MainView
  channelCount: number
  connected: boolean
  connectedClientCount: number
  drawerWidth: number
  eventTemplateCount: number
  isDesktop: boolean
  isMobile: boolean
  modalState: MockEditorState | null
  mocks: Mock[]
  qrOpen: boolean
  selectedEntry: LogEntry | null
  sequenceCount: number
  recordingCount: number
  serverInfo: ServerInfo | null
  sidebarCollapsed: boolean
  sidebarOpen: boolean
  toasts: Toast[]
  updateInfo: UpdateInfo | null
  onChangeView: (view: MainView) => void
  onClearLog: () => void
  onCloseDrawer: () => void
  onCreateMock: () => void
  onEditMock: (index: number) => void
  onMocksChanged: () => void
  onReloadMocks: () => Promise<void>
  onResizeDrawer: (width: number) => void
  onSaveAsMock: (entry: LogEntry) => void
  onSetModalState: (state: MockEditorState | null) => void
  onSetQrOpen: (open: boolean) => void
  onSetSidebarCollapsed: (collapsed: boolean) => void
  onSetSidebarOpen: (open: boolean) => void
  onSetUpdateInfo: (info: UpdateInfo | null) => void
  onToggleSidebar: () => void
  showToast: (message: string, kind?: 'warn') => void
}

export function AppShell({
  children,
  activeView,
  channelCount,
  connected,
  connectedClientCount,
  drawerWidth,
  eventTemplateCount,
  isDesktop,
  isMobile,
  modalState,
  mocks,
  qrOpen,
  selectedEntry,
  sequenceCount,
  recordingCount,
  serverInfo,
  sidebarCollapsed,
  sidebarOpen,
  toasts,
  updateInfo,
  onChangeView,
  onClearLog,
  onCloseDrawer,
  onCreateMock,
  onEditMock,
  onMocksChanged,
  onReloadMocks,
  onResizeDrawer,
  onSaveAsMock,
  onSetModalState,
  onSetQrOpen,
  onSetSidebarCollapsed,
  onSetSidebarOpen,
  onSetUpdateInfo,
  onToggleSidebar,
  showToast,
}: AppShellProps) {
  return (
    <>
      <Header
        version={serverInfo?.version || ''}
        connected={connected}
        isDesktop={isDesktop}
        isMobile={isMobile}
        onReloadMocks={onReloadMocks}
        onClearLog={onClearLog}
        onShowQR={() => onSetQrOpen(true)}
        onToggleSidebar={onToggleSidebar}
      />

      {updateInfo && <UpdateBanner info={updateInfo} onDismiss={() => onSetUpdateInfo(null)} />}

      <main className="flex flex-1 overflow-hidden min-h-0">
        {sidebarCollapsed && <CollapsedSidebarRail onExpand={() => onSetSidebarCollapsed(false)} />}
        <Sidebar
          open={sidebarOpen}
          collapsed={sidebarCollapsed}
          mocks={mocks}
          serverInfo={serverInfo}
          onClose={() => onSetSidebarOpen(false)}
          onCollapse={() => onSetSidebarCollapsed(true)}
          onMocksChanged={onMocksChanged}
          onEditMock={onEditMock}
          onCreateMock={onCreateMock}
          showToast={showToast}
        />
        <section className="flex-1 flex flex-col min-w-0 min-h-0">
          <MainTabs
            activeView={activeView}
            channelCount={channelCount}
            connectedClientCount={connectedClientCount}
            eventTemplateCount={eventTemplateCount}
            sequenceCount={sequenceCount}
            recordingCount={recordingCount}
            onChange={onChangeView}
          />
          {children}
        </section>
        {selectedEntry && (
          <Drawer
            entry={selectedEntry}
            serverInfo={serverInfo}
            width={drawerWidth}
            onResize={onResizeDrawer}
            onClose={onCloseDrawer}
            onSaveAsMock={onSaveAsMock}
          />
        )}
      </main>

      {modalState && (
        <MockEditorModal
          state={modalState}
          onClose={() => onSetModalState(null)}
          onSaved={onMocksChanged}
          showToast={showToast}
        />
      )}
      {qrOpen && <QRModal onClose={() => onSetQrOpen(false)} />}

      <ToastContainer toasts={toasts} />
    </>
  )
}
