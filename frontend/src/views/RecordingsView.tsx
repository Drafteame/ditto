import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import type { LogEntry, RecordedFrame, RecordingManifest, ServerInfo } from '../types'
import { useRecordingStore } from '../stores/useRecordingStore'
import { Braces, Refresh, Send } from '../components/icons'
import { SocketEventInspector } from '../components/SocketEventInspector'
import * as api from '../api'
import { formatLocalTimestamp } from '../time'

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
    loading,
    error,
    loadRecordings,
    startRecording,
    stopRecording,
  } = useRecordingStore(useShallow(state => ({
    recordings: state.recordings,
    activeId: state.activeId,
    loading: state.loading,
    error: state.error,
    loadRecordings: state.loadRecordings,
    startRecording: state.startRecording,
    stopRecording: state.stopRecording,
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

  function selectRecording(id: string) {
    setSelectedId(id)
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
            key={selectedId || 'empty'}
            manifest={selectedRecording}
          />
        </div>
      </div>
    </section>
  )
})

function RecordingDetail({
  id,
  manifest,
}: {
  id: string
  manifest: RecordingManifest | null
}) {
  const firstChannel = manifest?.channels[0]?.channel ?? ''
  const [channel, setChannel] = useState('')
  const [frames, setFrames] = useState<RecordedFrame[]>([])
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selectedFrame, setSelectedFrame] = useState<RecordedFrame | null>(null)
  const controller = useRef<AbortController | null>(null)
  const loadPage = useCallback(async (nextOffset: number) => {
    if (!id || !channel) return
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setLoading(true)
    setError('')
    try {
      const data = await api.fetchRecordingFrames(id, channel, nextOffset, 100, request.signal)
      if (request.signal.aborted) return
      const page = Array.isArray(data.frames) ? data.frames : []
      setFrames(page)
      setOffset(nextOffset)
      setSelectedFrame(null)
    } catch (err) {
      if (!request.signal.aborted) setError((err as Error).message)
    } finally {
      if (!request.signal.aborted) setLoading(false)
    }
  }, [channel, id])

  useEffect(() => {
    setFrames([])
    setOffset(0)
    setSelectedFrame(null)
    setError('')
    if (!channel) setLoading(false)
    if (channel) void loadPage(0)
    return () => controller.current?.abort()
  }, [id, channel, loadPage])

  if (!manifest) {
    return <section className="recording-detail socket-empty">Select a recording to inspect its manifest.</section>
  }
  const selectedEvent = selectedFrame ? recordedFrameEvent(selectedFrame, manifest.started_at) : null
  const channelTotal = manifest.channels.find(item => item.channel === channel)?.events
  const hasMore = channelTotal === undefined ? frames.length === 100 : offset + frames.length < channelTotal
  return (
    <section className="recording-detail">
      <div className="panel-label">{manifest.id}</div>
      <div className="recording-channel-list">
        {manifest.channels.map(item => (
          <button key={item.channel} type="button" className={channel === item.channel ? 'quick-template active' : 'quick-template'} onClick={() => setChannel(item.channel)}>
            <span>{item.channel}</span>
            <small>{item.events} events / {item.dropped} capped / {item.queue_dropped ?? 0} queued</small>
          </button>
        ))}
      </div>
      <div className="panel-label">Recorded frames {channel && `· ${channel}`}</div>
      {error && <div className="socket-error">{error}</div>}
      {loading && <div className="socket-empty compact">Loading frames…</div>}
      {!loading && !frames.length && <div className="socket-empty compact">{channel ? 'No frames in this channel.' : firstChannel ? 'Select a channel to load frames.' : 'No channels recorded.'}</div>}
      {!!frames.length && <>
        <div className="recording-frame-list">
          {frames.map((frame, index) => {
            const frameTime = new Date(Date.parse(manifest.started_at) + frame.ts_ms)
            const timestamp = Number.isNaN(frameTime.getTime()) ? `${frame.ts_ms}ms from recording start` : frameTime.toISOString()
            return <button key={`${frame.ts_ms}-${index}`} type="button" className={`socket-event-row recording-frame-row${selectedFrame === frame ? ' selected' : ''}`} onClick={() => setSelectedFrame(frame)}>
            <span className="time" title={`${timestamp} · +${frame.ts_ms}ms from recording start`}>{Number.isNaN(frameTime.getTime()) ? `${frame.ts_ms}ms` : `${formatLocalTimestamp(timestamp)} · +${frame.ts_ms}ms`}</span>
            <span className="method">{frame.direction}</span><span className="path">{frame.channel}</span>
            <span className="status">{frame.frame_kind}</span><span className="payload">{frame.decoded?.alias || frame.decode_error || frame.decoded?.type_name || 'raw'}</span>
          </button>})}
        </div>
        <div className="recording-pagination">
          <span>{offset + 1}–{offset + frames.length}{channelTotal ? ` of ${channelTotal}` : ''}</span>
          <button type="button" className="btn ghost" disabled={loading || offset === 0} onClick={() => void loadPage(Math.max(0, offset - 100))}>Previous</button>
          <button type="button" className="btn ghost" disabled={loading || !hasMore} onClick={() => void loadPage(offset + frames.length)}>Next</button>
        </div>
      </>}
      {selectedEvent && <div className="recorded-frame-inspector"><SocketEventInspector entry={selectedEvent} /></div>}
    </section>
  )
}

function recordedFrameEvent(frame: RecordedFrame, startedAt: string) {
  const bytes = Uint8Array.from(atob(frame.raw_b64), char => char.charCodeAt(0))
  const decodedPayload = frame.decoded?.payload_json
  const recordedAt = Date.parse(startedAt)
  return {
    type: 'SOCKET' as const,
    method: 'FRAME',
    path: frame.channel,
    timestamp: Number.isNaN(recordedAt) ? `${frame.ts_ms}ms from recording start` : new Date(recordedAt + frame.ts_ms).toISOString(),
    direction: frame.direction,
    source: 'recording',
    channel: frame.channel,
    frame_kind: frame.frame_kind,
    request_body: frame.frame_kind === 'text' ? new TextDecoder().decode(bytes) : undefined,
    request_payload: {
      size_bytes: bytes.length,
      captured_bytes: bytes.length,
      content_type: frame.frame_kind === 'text' ? 'text/plain; charset=utf-8' : 'application/octet-stream',
      capture_status: bytes.length ? 'captured' as const : 'empty' as const,
      raw_base64: frame.raw_b64,
    },
    type_name: frame.decoded?.type_name,
    alias: frame.decoded?.alias,
    decoded_payload: decodedPayload === undefined ? undefined : typeof decodedPayload === 'string' ? decodedPayload : JSON.stringify(decodedPayload),
    decode_error: frame.decode_error,
  }
}
