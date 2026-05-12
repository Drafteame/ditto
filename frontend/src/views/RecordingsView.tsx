import { memo, useEffect, useMemo, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry, RecordedFrame, RecordingManifest, ServerInfo } from '../types'
import { useRecordingStore } from '../stores/useRecordingStore'
import { Braces, Refresh, Send } from '../components/icons'

interface RecordingsViewProps {
  serverInfo: ServerInfo | null
  selectedLogId: string | null
  onSelectLog: (id: string | null) => void
  onSaveAsMock: (entry: LogEntry) => void
  showToast: (message: string, kind?: 'warn') => void
}

export const RecordingsView = memo(function RecordingsView({ showToast }: RecordingsViewProps) {
  const [name, setName] = useState('QA Recording')
  const [description, setDescription] = useState('')
  const [selectedId, setSelectedId] = useState('')
  const {
    recordings,
    activeId,
    selected,
    frames,
    loading,
    error,
    loadRecordings,
    startRecording,
    stopRecording,
    loadRecording,
    loadFrames,
  } = useRecordingStore(useShallow(state => ({
    recordings: state.recordings,
    activeId: state.activeId,
    selected: state.selected,
    frames: state.frames,
    loading: state.loading,
    error: state.error,
    loadRecordings: state.loadRecordings,
    startRecording: state.startRecording,
    stopRecording: state.stopRecording,
    loadRecording: state.loadRecording,
    loadFrames: state.loadFrames,
  })))

  useEffect(() => {
    loadRecordings()
  }, [loadRecordings])

  const selectedRecording = selectedId ? recordings.find(item => item.id === selectedId) ?? null : null
  const totalEvents = useMemo(
    () => recordings.reduce((sum, item) => sum + item.channels.reduce((inner, ch) => inner + ch.events, 0), 0),
    [recordings],
  )

  async function handleStart() {
    try {
      await startRecording(name, description)
      showToast('Recording started')
    } catch (err) {
      showToast(`Recording failed: ${(err as Error).message}`, 'warn')
    }
  }

  async function handleStop(id: string) {
    try {
      await stopRecording(id)
      showToast('Recording stopped')
    } catch (err) {
      showToast(`Stop failed: ${(err as Error).message}`, 'warn')
    }
  }

  async function selectRecording(id: string) {
    setSelectedId(id)
    await loadRecording(id)
  }

  return (
    <section className="socket-panel recordings-panel">
      <div className="socket-head">
        <div className="socket-title">
          <Braces />
          <span>Recordings</span>
          <span className="socket-count">{recordings.length}</span>
        </div>
        <div className="socket-url">{totalEvents} captured frames</div>
        <button type="button" className="btn ghost" onClick={loadRecordings} disabled={loading}>
          <Refresh /> Refresh
        </button>
      </div>

      <div className="recording-body">
        <div className="recording-start-row">
          <input className="input" value={name} onChange={e => setName(e.target.value)} placeholder="Recording name" />
          <input className="input" value={description} onChange={e => setDescription(e.target.value)} placeholder="Description" />
          <button type="button" className="btn primary" onClick={handleStart} disabled={!!activeId || loading}>
            <Send /> Start
          </button>
        </div>
        {error && <div className="socket-error">{error}</div>}

        <div className="recording-grid">
          <section className="recording-list">
            {recordings.length === 0 ? (
              <div className="socket-empty">No recordings yet.</div>
            ) : recordings.map(item => {
              const events = item.channels.reduce((sum, channel) => sum + channel.events, 0)
              const stopped = item.stopped_at ? new Date(item.stopped_at).getTime() : Date.now()
              const duration = Math.max(0, stopped - new Date(item.started_at).getTime())
              const rowClass = selectedId === item.id ? 'recording-row active' : 'recording-row'
              return (
                <div
                  key={item.id}
                  role="button"
                  tabIndex={0}
                  className={rowClass}
                  onClick={() => selectRecording(item.id)}
                  onKeyDown={e => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      selectRecording(item.id)
                    }
                  }}
                >
                  <span className="recording-name">{item.name}</span>
                  <span>{item.channels.length} channels</span>
                  <span>{events} events</span>
                  <span>{Math.round(duration / 1000)}s</span>
                  <span className={item.stopped_at ? 'recording-state' : 'recording-state live'}>
                    {item.stopped_at ? 'Stopped' : 'Active'}
                  </span>
                  {!item.stopped_at && (
                    <button
                      type="button"
                      className="btn small recording-stop"
                      onClick={e => {
                        e.stopPropagation()
                        handleStop(item.id)
                      }}
                    >
                      Stop
                    </button>
                  )}
                </div>
              )
            })}
          </section>

          <RecordingDetail
            id={selectedId}
            manifest={selected ?? selectedRecording}
            frames={frames}
            onLoadFrames={loadFrames}
          />
        </div>
      </div>
    </section>
  )
})

function RecordingDetail({
  id,
  manifest,
  frames,
  onLoadFrames,
}: {
  id: string
  manifest: RecordingManifest | null
  frames: RecordedFrame[]
  onLoadFrames: (id: string, channel: string, offset?: number) => Promise<void>
}) {
  const firstChannel = manifest?.channels[0]?.channel ?? ''
  if (!manifest) {
    return <section className="recording-detail socket-empty">Select a recording to inspect its manifest.</section>
  }
  return (
    <section className="recording-detail">
      <div className="panel-label">{manifest.id}</div>
      <div className="recording-channel-list">
        {manifest.channels.map(channel => (
          <button key={channel.channel} type="button" className="quick-template" onClick={() => onLoadFrames(id, channel.channel, 0)}>
            <span>{channel.channel}</span>
            <small>{channel.events} events / {channel.dropped} capped / {channel.queue_dropped ?? 0} queued</small>
          </button>
        ))}
      </div>
      <div className="panel-label">Frames preview</div>
      {frames.length === 0 ? (
        <div className="socket-empty compact">
          {firstChannel ? 'Load a channel to fetch the first 100 frames.' : 'No channels recorded.'}
        </div>
      ) : (
        <div className="recording-frame-list">
          {frames.map((frame, index) => (
            <div key={`${frame.ts_ms}-${index}`} className="socket-event-row">
              <span className="time">{frame.ts_ms}ms</span>
              <span className="method">{frame.direction}</span>
              <span className="path">{frame.channel}</span>
              <span className="status">{frame.frame_kind}</span>
              <span className="payload">{frame.decoded?.alias || frame.decode_error || 'raw'}</span>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
