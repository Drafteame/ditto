import { memo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry, ServerInfo } from '../types'
import { useLogStore } from '../stores/useLogStore'
import { LogPanel } from '../components/LogPanel'

interface RequestsViewProps {
  serverInfo: ServerInfo | null
  selectedLogId: string | null
  onSelectLog: (id: string | null) => void
  onSaveAsMock: (entry: LogEntry) => void
  showToast: (message: string, kind?: 'warn') => void
}

export const RequestsView = memo(function RequestsView({
  serverInfo,
  selectedLogId,
  onSelectLog,
  onSaveAsMock,
}: RequestsViewProps) {
  const logEntries = useLogStore(useShallow(state => state.logEntries))

  return (
    <LogPanel
      entries={logEntries}
      serverInfo={serverInfo}
      selectedId={selectedLogId}
      onSelect={onSelectLog}
      onSaveAsMock={onSaveAsMock}
    />
  )
})
