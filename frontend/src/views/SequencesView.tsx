import { memo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry, ServerInfo } from '../types'
import { useEventTemplateStore } from '../stores/useEventTemplateStore'
import { useSchemaStore } from '../stores/useSchemaStore'
import { useSequenceStore } from '../stores/useSequenceStore'
import { SequencesPanel } from '../components/SequencesPanel'

interface SequencesViewProps {
  serverInfo: ServerInfo | null
  selectedLogId: string | null
  onSelectLog: (id: string | null) => void
  onSaveAsMock: (entry: LogEntry) => void
  showToast: (message: string, kind?: 'warn') => void
}

export const SequencesView = memo(function SequencesView({
  showToast,
}: SequencesViewProps) {
  const eventTemplates = useEventTemplateStore(useShallow(state => state.templates))
  const schemaTypes = useSchemaStore(useShallow(state => state.types))
  const {
    sequences,
    playerStates,
    waitingEvents,
    sequencesLoading,
    sequencesError,
  } = useSequenceStore(useShallow(state => ({
    sequences: state.sequences,
    playerStates: state.playerStates,
    waitingEvents: state.waitingEvents,
    sequencesLoading: state.loading,
    sequencesError: state.error,
  })))
  const {
    loadSequences,
    saveSequence,
    deleteSequence,
    playSequence,
    pauseSequence,
    stopSequence,
    seekSequence,
    setSequenceSpeed,
  } = useSequenceStore(useShallow(state => ({
    loadSequences: state.loadSequences,
    saveSequence: state.saveSequence,
    deleteSequence: state.deleteSequence,
    playSequence: state.playSequence,
    pauseSequence: state.pauseSequence,
    stopSequence: state.stopSequence,
    seekSequence: state.seekSequence,
    setSequenceSpeed: state.setSequenceSpeed,
  })))

  return (
    <SequencesPanel
      sequences={sequences}
      templates={eventTemplates}
      schemaTypes={schemaTypes}
      playerStates={playerStates}
      waitingEvents={waitingEvents}
      loading={sequencesLoading}
      error={sequencesError}
      onRefresh={loadSequences}
      onSave={saveSequence}
      onDelete={deleteSequence}
      onPlay={async (id) => { await playSequence(id) }}
      onPause={async (id) => { await pauseSequence(id) }}
      onStop={async (id) => { await stopSequence(id) }}
      onSeek={async (id, step) => { await seekSequence(id, step) }}
      onSpeed={async (id, speed) => { await setSequenceSpeed(id, speed) }}
      showToast={showToast}
    />
  )
})
