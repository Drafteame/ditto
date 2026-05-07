import { memo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry, ServerInfo } from '../types'
import { useEventTemplateStore } from '../stores/useEventTemplateStore'
import { useSchemaStore } from '../stores/useSchemaStore'
import { EventTemplatesPanel } from '../components/EventTemplatesPanel'

interface TemplatesViewProps {
  serverInfo: ServerInfo | null
  selectedLogId: string | null
  onSelectLog: (id: string | null) => void
  onSaveAsMock: (entry: LogEntry) => void
  showToast: (message: string, kind?: 'warn') => void
}

export const TemplatesView = memo(function TemplatesView({
  showToast,
}: TemplatesViewProps) {
  const schemaTypes = useSchemaStore(useShallow(state => state.types))
  const {
    eventTemplates,
    eventTemplatesLoading,
    eventTemplatesError,
    loadEventTemplates,
    saveEventTemplate,
    deleteEventTemplate,
  } = useEventTemplateStore(useShallow(state => ({
    eventTemplates: state.templates,
    eventTemplatesLoading: state.loading,
    eventTemplatesError: state.error,
    loadEventTemplates: state.loadTemplates,
    saveEventTemplate: state.saveTemplate,
    deleteEventTemplate: state.deleteTemplate,
  })))

  return (
    <EventTemplatesPanel
      templates={eventTemplates}
      schemaTypes={schemaTypes}
      loading={eventTemplatesLoading}
      error={eventTemplatesError}
      onRefresh={loadEventTemplates}
      onSave={saveEventTemplate}
      onDelete={deleteEventTemplate}
      showToast={showToast}
    />
  )
})
