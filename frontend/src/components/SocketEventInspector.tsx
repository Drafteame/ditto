import { useMemo, useState } from 'react'
import type { LogEntry } from '../types'
import { useLogStore } from '../stores/useLogStore'
import { CodeBlock } from './CodeBlock'
import { DownloadCapture, decodePayloadText, formatBytes } from './PayloadTools'

type SocketEvent = Pick<LogEntry, 'type' | 'method' | 'path' | 'timestamp'> & Partial<LogEntry>
type InspectorTab = 'overview' | 'payload' | 'frame' | 'delivery'

function HeaderBlock({ title, headers }: { title: string; headers?: Record<string, string[]> }) {
  const text = Object.entries(headers ?? {}).flatMap(([name, values]) => values.map(value => `${name}: ${value}`)).join('\n')
  return <section className="log-inspector-section"><b>{title}</b>{text ? <CodeBlock text={text} /> : <span className="text-fg-3">No handshake headers captured.</span>}</section>
}

export function SocketEventInspector({ entry }: { entry: SocketEvent }) {
  const [tab, setTab] = useState<InspectorTab>('overview')
  const allEntries = useLogStore(state => state.logEntries)
  const selectLog = useLogStore(state => state.selectLog)
  const linked = useMemo(() => entry.dispatch_id
    ? allEntries.filter(item => item.type === 'SOCKET' && item.dispatch_id === entry.dispatch_id && item.id !== entry.id)
    : [], [allEntries, entry.dispatch_id, entry.id])
  const observedWrites = new Set([
    ...(entry.delivery_state === 'written' ? [`${entry.client_id || entry.connection_id || entry.id}:${entry.direction || ''}`] : []),
    ...linked.filter(item => item.delivery_state === 'written').map(item => `${item.client_id || item.connection_id || item.id}:${item.direction || ''}`),
  ]).size
  const observedFailures = linked.filter(item => item.delivery_state === 'dropped' || item.delivery_state === 'write_error').length
    + (entry.delivery_state === 'dropped' || entry.delivery_state === 'write_error' ? 1 : 0)
  const payloadMeta = entry.request_payload ?? entry.response_payload
  const raw = decodePayloadText(payloadMeta) ?? entry.request_body ?? entry.response_body ?? ''
  const pretty = entry.decoded_payload || ''
  const tabs: InspectorTab[] = ['overview', 'payload', 'frame', 'delivery']

  return <>
    <div className="tabs">{tabs.map(name => <button type="button" key={name} className={tab === name ? 'active' : ''} onClick={() => setTab(name)}>{name[0].toUpperCase() + name.slice(1)}</button>)}</div>
    <div className="drawer-body socket-inspector-body">
      {tab === 'overview' && <div className="log-overview">
        {[
          ['Time', entry.timestamp], ['URL', entry.url], ['Host', entry.host], ['Remote address', entry.remote_addr],
          ['Protocol', entry.protocol], ['Status', entry.status], ['Duration', entry.duration_ms == null ? undefined : `${entry.duration_ms}ms`],
          ['Direction', entry.direction], ['Connection', entry.connection_id],
          ['Client', entry.client_id], ['Channel', entry.channel || entry.path], ['Subscription', entry.subscription_id],
          ['Adapter', entry.adapter], ['Subprotocol', entry.subprotocol], ['Mode', entry.mode], ['Source', entry.source],
          ['Target', entry.target], ['Frame', entry.frame_kind], ['Control', entry.control_type],
          ['Type / alias', [entry.type_name, entry.alias].filter(Boolean).join(' / ')],
          ['Capture', payloadMeta?.capture_status], ['Payload size', payloadMeta ? `${formatBytes(payloadMeta.captured_bytes)} of ${formatBytes(payloadMeta.size_bytes)}` : undefined],
          ['Close', entry.close_code == null ? entry.close_reason : `${entry.close_code}${entry.close_reason ? ` · ${entry.close_reason}` : ''}`],
        ].map(([label, value]) => value != null && value !== '' ? <div key={label}><span>{label}</span><code>{value}</code></div> : null)}
        {entry.error && <div className="error"><span>Error</span><code>{entry.error}</code></div>}
        {entry.decode_error && <div className="error"><span>Decode error</span><code>{entry.decode_error}</code></div>}
        <HeaderBlock title="Handshake request headers" headers={entry.request_headers} />
        <HeaderBlock title="Handshake response headers" headers={entry.response_headers} />
      </div>}

      {tab === 'payload' && <div className="log-inspector-content">
        {entry.decode_error && <div className="capture-notice error">Decode failed: {entry.decode_error}</div>}
        {pretty ? <CodeBlock text={pretty} prettyText={(() => { try { return JSON.stringify(JSON.parse(pretty), null, 2) } catch { return undefined } })()} />
          : <div className="socket-inspector-empty">{entry.decode_error || 'No decoded payload is available for this event.'}</div>}
        {entry.decoded_truncated && <div className="capture-notice truncated">Decoded payload preview is truncated.</div>}
      </div>}

      {tab === 'frame' && <div className="log-inspector-content">
        {payloadMeta && <div className={`capture-notice ${payloadMeta.capture_status}`}>
          {payloadMeta.capture_status}: {formatBytes(payloadMeta.captured_bytes)} captured of {formatBytes(payloadMeta.size_bytes)}
          {payloadMeta.content_type ? ` · ${payloadMeta.content_type}` : ''}{payloadMeta.error ? ` · ${payloadMeta.error}` : ''}
        </div>}
        {entry.frame_kind === 'binary' ? (
          payloadMeta?.raw_base64 ? <><div className="capture-notice">Binary frame shown as Base64.</div><CodeBlock text={payloadMeta.raw_base64} /></> : <div className="socket-inspector-empty">No binary frame bytes were captured.</div>
        ) : raw ? <CodeBlock text={raw} prettyText={(() => { try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return undefined } })()} />
          : <div className="socket-inspector-empty">{payloadMeta?.capture_status === 'empty' ? 'The captured frame is empty.' : payloadMeta?.capture_status === 'not_captured' ? 'No frame payload was captured.' : 'No raw frame is available.'}</div>}
        {payloadMeta?.capture_status === 'truncated' && payloadMeta.captured_bytes < payloadMeta.size_bytes && <div className="capture-notice truncated">Showing the captured prefix; the original frame exceeded the capture limit.</div>}
        <DownloadCapture metadata={payloadMeta} filename={`socket-${entry.connection_id || entry.dispatch_id || 'frame'}.bin`} />
        {entry.method !== 'FRAME' && !!entry.response_body && <section className="log-inspector-section"><b>Event details</b><CodeBlock text={entry.response_body} prettyText={(() => { try { return JSON.stringify(JSON.parse(entry.response_body!), null, 2) } catch { return undefined } })()} /></section>}
      </div>}

      {tab === 'delivery' && <div className="log-inspector-content">
        <div className="log-overview">
          <div><span>State</span><code>{entry.delivery_state || (entry.method === 'DISPATCH' ? 'dispatch summary' : '—')}</code></div>
          <div><span>Queued</span><code>{entry.queued ?? (entry.method === 'DISPATCH' ? 0 : entry.delivery_state === 'queued' ? 1 : '—')}</code></div>
          <div><span>Observed writes</span><code>{observedWrites}</code></div>
          <div><span>Dispatch ID</span><code>{entry.dispatch_id || '—'}</code></div>
          <div><span>Observed drops / write errors</span><code>{observedFailures || entry.error || '0'}</code></div>
        </div>
        <div className="socket-delivery-note">Queued means accepted by Ditto’s local send queue; written means the local socket write completed. Neither confirms that a remote application processed the frame.</div>
        {entry.dispatch_id && <section className="log-inspector-section"><b>Related client frames</b>
          {linked.length ? <div className="socket-linked-deliveries">{linked.map(item => <button type="button" key={item.id} onClick={() => selectLog(item.id)}><code>{item.client_id || 'client'} · {item.delivery_state || item.method}</code><span>{item.error || item.timestamp}</span></button>)}</div>
            : <span className="text-fg-3">No related frame records are loaded in this log window.</span>}
        </section>}
      </div>}
    </div>
  </>
}
