import { useCallback, useEffect, useState } from 'react'
import type { LogEntry, LogPayloadMetadata, ServerInfo } from '../types'
import * as api from '../api'
import { statusClass } from '../status'
import { CodeBlock } from './CodeBlock'
import { Alert, Bookmark, Check, Download, Globe, X } from './icons'

export const DRAWER_MIN_WIDTH = 340
export const DRAWER_MAX_WIDTH = 720

interface DrawerProps {
  entry: LogEntry
  serverInfo: ServerInfo | null
  width: number
  onResize: (next: number) => void
  onClose: () => void
  onSaveAsMock: (entry: LogEntry) => void
}

type Tab = 'overview' | 'request' | 'response'

function StatusCell({ status }: { status: number }) {
  return <span className={`st ${statusClass(status)} font-mono text-[12px]`}>{status || '-'}</span>
}

function prettyJson(raw: string | undefined): string | undefined {
  if (!raw) return undefined
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return undefined
  }
}

function formatHeaders(headers: Record<string, string[]> | undefined): string {
  if (!headers) return ''
  return Object.keys(headers).sort((a, b) => a.localeCompare(b))
    .flatMap(name => headers[name].map(value => `${name}: ${value}`)).join('\n')
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

function captureMessage(meta: LogPayloadMetadata | undefined, label: string): string | null {
  if (!meta) return null
  switch (meta.capture_status) {
    case 'empty': return `${label} capture completed; the body is empty.`
    case 'not_captured': return `${label} body was not captured.`
    case 'captured': return `Captured ${formatBytes(meta.size_bytes)}${meta.encoding ? ` (${meta.encoding})` : ''}.`
    case 'truncated': return meta.captured_bytes >= meta.size_bytes
      ? `The decoded preview is limited; all ${formatBytes(meta.size_bytes)} of captured wire bytes are available.`
      : `Captured ${formatBytes(meta.captured_bytes)} of ${formatBytes(meta.size_bytes)}. The body is truncated.`
    case 'binary': return `Binary payload: ${formatBytes(meta.size_bytes)}; ${formatBytes(meta.captured_bytes)} bytes are available.`
    case 'metadata_only': return `Payload metadata captured (${formatBytes(meta.size_bytes)}); content is not stored separately.`
    case 'error': return `${label} capture failed${meta.error ? `: ${meta.error}` : '.'}`
    case 'unavailable': return `${label} body is unavailable.`
    case 'omitted': return `${label} body was omitted.`
    default: return `${label} capture status: ${meta.capture_status}.`
  }
}

function DownloadCapture({ metadata, filename }: { metadata: LogPayloadMetadata | undefined; filename: string }) {
  if (!metadata?.raw_base64) return null
  const download = () => {
    const binary = atob(metadata.raw_base64!)
    const bytes = Uint8Array.from(binary, char => char.charCodeAt(0))
    const url = URL.createObjectURL(new Blob([bytes], { type: metadata.content_type || 'application/octet-stream' }))
    const link = document.createElement('a')
    link.href = url
    link.download = filename
    link.click()
    URL.revokeObjectURL(url)
  }
  return <button type="button" className="btn ghost" onClick={download}><Download /> Download captured bytes</button>
}

function MatchBanner({ entry, target }: { entry: LogEntry; target: string }) {
  if (entry.error) {
    return <div className="match-banner miss"><Alert /><div className="flex-1 min-w-0"><div className="title">Request or response error</div><div className="detail">{entry.error}</div></div></div>
  }
  if (entry.type === 'MOCK') {
    return <div className="match-banner"><Check /><div className="flex-1 min-w-0"><div className="title">Served from a mock</div><div className="detail"><code>{entry.method.toUpperCase()} {entry.path}</code> matched mock {entry.mock_index ?? ''}{entry.sequence_len ? ` · sequence step ${entry.sequence_step}/${entry.sequence_len}` : ''}.</div></div></div>
  }
  if (entry.type === 'PROXY') {
    return <div className="match-banner proxy"><Globe /><div className="flex-1 min-w-0"><div className="title">Forwarded to target</div><div className="detail">No mock matched — response came from <code>{target || 'target unavailable for this event'}</code></div></div></div>
  }
  if (entry.type === 'SOCKET') {
    return <div className="match-banner proxy"><Globe /><div className="flex-1 min-w-0"><div className="title">Socket event</div><div className="detail">Ditto handled <code>{entry.method}</code> for <code>{entry.path}</code>.</div></div></div>
  }
  return <div className="match-banner miss"><Alert /><div className="flex-1 min-w-0"><div className="title">No mock, no target</div><div className="detail">Add a mock or configure a target URL to handle <code>{entry.path}</code></div></div></div>
}

function QueryRows({ entry }: { entry: LogEntry }) {
  let url: URL
  try { url = new URL(entry.url || entry.path, window.location.origin) } catch { return <span className="text-fg-3">No query parameters.</span> }
  const rows = [...url.searchParams.entries()]
  if (!rows.length) return <span className="text-fg-3">No query parameters.</span>
  return <div className="log-inspector-kv">{rows.map(([key, value], i) => <div key={`${key}-${i}`}><code>{key}</code><span>{value}</span></div>)}</div>
}

export function Drawer({ entry, width, onResize, onClose, onSaveAsMock }: DrawerProps) {
  const [tab, setTab] = useState<Tab>('overview')
  const [detail, setDetail] = useState<LogEntry | null>(null)
  const [detailState, setDetailState] = useState<'loading' | 'ready' | 'expired' | 'error'>('loading')

  useEffect(() => {
    const controller = new AbortController()
    setDetail(null)
    setDetailState('loading')
    api.fetchLogDetail(entry.id, controller.signal).then(value => {
      if (controller.signal.aborted) return
      setDetail(value)
      setDetailState('ready')
    }).catch(error => {
      if (controller.signal.aborted) return
      setDetailState(error instanceof Error && error.message.includes('expired') ? 'expired' : 'error')
    })
    return () => controller.abort()
  }, [entry.id])

  const handleDragStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = width
    const onMove = (ev: MouseEvent) => onResize(Math.max(DRAWER_MIN_WIDTH, Math.min(DRAWER_MAX_WIDTH, startW - (ev.clientX - startX))))
    const onUp = () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }, [width, onResize])

  const shown = detail ?? entry
  const method = shown.method.toUpperCase()
  const target = shown.target || ''
  const requestHeaders = formatHeaders(shown.request_headers)
  const responseHeaders = formatHeaders(shown.response_headers)
  const requestNotice = captureMessage(shown.request_payload, 'Request')
  const responseNotice = captureMessage(shown.response_payload, 'Response')

  return (
    <aside className="drawer" style={{ width }}>
      <div className="resize-handle left" onMouseDown={handleDragStart} title="Drag to resize" />
      <div className="drawer-head">
        <div className="row">
          <span className={`tag-type ${shown.type}`}>{shown.type}</span>
          <span className={`method ${method}`}>{method}</span>
          <StatusCell status={shown.status} />
          <div className="flex-1" />
          {shown.type === 'PROXY' && detailState === 'ready' && detail && <button type="button" className="btn ghost" style={{ height: 24, padding: '0 8px', fontSize: 11 }} onClick={() => onSaveAsMock(detail)} title="Save as mock"><Bookmark /> Save</button>}
          <button type="button" className="btn ghost icon" onClick={onClose} aria-label="Close drawer"><X /></button>
        </div>
        <div className="drawer-path">{shown.path}</div>
        <div className="drawer-meta"><span className="k">Time</span><span className="v">{shown.timestamp}</span><span className="k">Duration</span><span className="v">{shown.duration_ms}ms</span></div>
      </div>

      <div style={{ padding: '12px 14px 0' }}><MatchBanner entry={shown} target={target} /></div>
      {detailState === 'loading' && <div className="log-detail-state">Loading retained event details…</div>}
      {detailState === 'expired' && <div className="log-detail-state warn">Detailed event data expired or is no longer retained. Showing the live event copy where available.</div>}
      {detailState === 'error' && <div className="log-detail-state warn">Could not load retained event details. Check the connection and select the event again.</div>}

      <div className="tabs">
        {(['overview', 'request', 'response'] as const).map(name => <button type="button" key={name} className={tab === name ? 'active' : ''} onClick={() => setTab(name)}>{name[0].toUpperCase() + name.slice(1)}</button>)}
      </div>

      <div className="drawer-body">
        {tab === 'overview' && <div className="log-overview">
          <div><span>URL</span><code>{shown.url || shown.path}</code></div>
          <div><span>Host</span><code>{shown.host || '—'}</code></div>
          <div><span>Remote address</span><code>{shown.remote_addr || '—'}</code></div>
          <div><span>Protocol</span><code>{shown.protocol || '—'}</code></div>
          <div><span>Target used</span><code>{shown.target || '—'}</code></div>
          {shown.type === 'MOCK' && <div><span>Mock</span><code>{shown.mock_index}{shown.sequence_len ? ` · step ${shown.sequence_step}/${shown.sequence_len}` : ''}</code></div>}
          {shown.error && <div className="error"><span>Error</span><code>{shown.error}</code></div>}
          <div className="log-overview-section"><b>Query parameters</b><QueryRows entry={shown} /></div>
        </div>}

        {tab === 'request' && <div className="log-inspector-content">
          {requestNotice && <div className={`capture-notice ${shown.request_payload?.capture_status}`}>{requestNotice}</div>}
          <div className="log-inspector-section"><b>Query parameters</b><QueryRows entry={shown} /></div>
          {!!shown.request_form_fields && <div className="log-inspector-section"><b>Form fields</b>{shown.request_forms_truncated && <span className="capture-notice truncated">Some form fields were only partially captured.</span>}<div className="log-inspector-kv">{Object.entries(shown.request_form_fields).flatMap(([name, values]) => values.map((value, i) => <div key={`${name}-${i}`}><code>{name}</code><span>{value}</span></div>))}</div></div>}
          {!!shown.request_files?.length && <div className="log-inspector-section"><b>Uploaded files</b><div className="log-inspector-kv">{shown.request_files.map((file, i) => <div key={`${file.name}-${i}`}><code>{file.name}</code><span>{file.content_type || 'unknown type'} · {formatBytes(file.size_bytes)} · {file.capture_status}</span></div>)}</div></div>}
          {shown.request_body && <CodeBlock key={`req-${shown.id}`} text={shown.request_body} prettyText={prettyJson(shown.request_body)} />}
          <DownloadCapture metadata={shown.request_payload} filename={`request-${shown.id}.bin`} />
          <div className="log-inspector-section"><b>Request headers</b>{requestHeaders ? <CodeBlock key={`reqh-${shown.id}`} text={requestHeaders} /> : <span className="text-fg-3">No request headers captured.</span>}</div>
        </div>}

        {tab === 'response' && <div className="log-inspector-content">
          {responseNotice && <div className={`capture-notice ${shown.response_payload?.capture_status}`}>{responseNotice}</div>}
          {shown.response_body ? <CodeBlock key={`res-${shown.id}`} text={shown.response_body} prettyText={prettyJson(shown.response_body)} /> : <div className="text-fg-3 font-sans text-[12px]">{shown.response_payload?.capture_status === 'empty' ? 'The captured response body is empty.' : shown.response_payload?.capture_status === 'not_captured' ? 'No response body was captured.' : 'No response body is available.'}</div>}
          <DownloadCapture metadata={shown.response_payload} filename={`response-${shown.id}.bin`} />
          <div className="log-inspector-section"><b>Response headers</b>{responseHeaders ? <CodeBlock key={`resh-${shown.id}`} text={responseHeaders} /> : <span className="text-fg-3">No response headers captured.</span>}</div>
        </div>}
      </div>
    </aside>
  )
}
