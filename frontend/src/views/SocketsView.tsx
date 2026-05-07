import { memo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry, ServerInfo } from '../types'
import { useEventTemplateStore } from '../stores/useEventTemplateStore'
import { useLogStore } from '../stores/useLogStore'
import { useSchemaStore } from '../stores/useSchemaStore'
import { useSocketStore } from '../stores/useSocketStore'
import { SocketPanel } from '../components/SocketPanel'

interface SocketsViewProps {
  serverInfo: ServerInfo | null
  selectedLogId: string | null
  onSelectLog: (id: string | null) => void
  onSaveAsMock: (entry: LogEntry) => void
  showToast: (message: string, kind?: 'warn') => void
}

export const SocketsView = memo(function SocketsView({
  serverInfo,
  showToast,
}: SocketsViewProps) {
  const logEntries = useLogStore(useShallow(state => state.logEntries))
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
  const {
    schemaPacks,
    schemaTypes,
    schemasLoading,
    schemasError,
    loadSchemas,
    uploadSchemaPack,
    deleteSchemaPack,
  } = useSchemaStore(useShallow(state => ({
    schemaPacks: state.packs,
    schemaTypes: state.types,
    schemasLoading: state.loading,
    schemasError: state.error,
    loadSchemas: state.loadSchemas,
    uploadSchemaPack: state.uploadPack,
    deleteSchemaPack: state.deletePack,
  })))
  const {
    eventTemplates,
    eventTemplatesLoading,
    eventTemplatesError,
    loadEventTemplates,
    dispatchEventTemplate,
  } = useEventTemplateStore(useShallow(state => ({
    eventTemplates: state.templates,
    eventTemplatesLoading: state.loading,
    eventTemplatesError: state.error,
    loadEventTemplates: state.loadTemplates,
    dispatchEventTemplate: state.dispatchTemplate,
  })))

  return (
    <SocketPanel
      clients={connectedClients}
      entries={logEntries}
      serverInfo={serverInfo}
      schemaPacks={schemaPacks}
      schemaTypes={schemaTypes}
      schemasLoading={schemasLoading}
      schemasError={schemasError}
      templates={eventTemplates}
      templatesLoading={eventTemplatesLoading}
      templatesError={eventTemplatesError}
      loading={socketClientsLoading}
      error={socketClientsError}
      onRefresh={loadSocketClients}
      onRefreshSchemas={loadSchemas}
      onRefreshTemplates={loadEventTemplates}
      onUploadSchemaPack={uploadSchemaPack}
      onDeleteSchemaPack={deleteSchemaPack}
      onDispatchTemplate={(id, variables) => dispatchEventTemplate(id, variables)}
      showToast={showToast}
    />
  )
})
