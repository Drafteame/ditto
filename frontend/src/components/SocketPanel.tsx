import { useMemo, useState } from 'react'
import type { LogEntry, ServerInfo, SocketClient } from '../types'
import * as api from '../api'
import { Braces, Radio, Refresh, Send } from './icons'

interface SocketPanelProps {
  clients: SocketClient[]
  entries: LogEntry[]
  serverInfo: ServerInfo | null
  loading: boolean
  error: string
  onRefresh: () => void
  showToast: (message: string, kind?: 'warn') => void
}

const DEFAULT_PAYLOAD = `{
  "type": "example.event",
  "message": "Hello from Ditto"
}`

export function SocketPanel({
  clients,
  entries,
  serverInfo,
  loading,
  error,
  onRefresh,
  showToast,
}: SocketPanelProps) {
  const channels = useMemo(() => {
    const set = new Set<string>()
    clients.forEach(client => client.subscriptions.forEach(channel => set.add(channel)))
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [clients])

  const [channel, setChannel] = useState('')
  const [adapter, setAdapter] = useState('')
  const [payload, setPayload] = useState(DEFAULT_PAYLOAD)
  const [dispatching, setDispatching] = useState(false)
  const [jsonError, setJsonError] = useState('')

  const socketEntries = entries.filter(entry => entry.type === 'SOCKET').slice(-200).reverse()
  const scheme = serverInfo?.https ? 'wss' : 'ws'
  const wsUrl = serverInfo ? `${scheme}://localhost:${serverInfo.port}` : ''

  async function handleDispatch() {
    const selectedChannel = channel.trim()
    if (!selectedChannel) {
      setJsonError('Channel is required')
      return
    }

    let parsed: unknown
    try {
      parsed = JSON.parse(payload)
    } catch (err) {
      setJsonError(`Invalid JSON: ${(err as Error).message}`)
      return
    }

    setJsonError('')
    setDispatching(true)
    try {
      const result = await api.dispatchSocketEvent({
        channel: selectedChannel,
        payload: parsed,
        adapter: adapter as 'raw' | 'appsync' | '',
      })
      showToast(`Dispatched to ${result.delivered} client${result.delivered === 1 ? '' : 's'}`)
    } catch (err) {
      showToast(`Dispatch failed: ${(err as Error).message}`, 'warn')
    } finally {
      setDispatching(false)
    }
  }

  return (
    <section className="socket-panel">
      <div className="socket-head">
        <div className="socket-title">
          <Radio />
          <span>Sockets</span>
          <span className="socket-count">{clients.length}</span>
        </div>
        <div className="socket-url" title={wsUrl}>
          {wsUrl || 'Waiting for server info'}
        </div>
        <button type="button" className="btn ghost" onClick={onRefresh} disabled={loading}>
          <Refresh /> Refresh
        </button>
      </div>

      <div className="socket-grid">
        <section className="socket-clients">
          <div className="panel-label">Connected clients</div>
          {error && <div className="socket-error">{error}</div>}
          {clients.length === 0 ? (
            <div className="socket-empty">
              Point a WebSocket client at Ditto, then send a subscribe message for a channel.
            </div>
          ) : (
            clients.map(client => <ClientRow key={client.id} client={client} />)
          )}
        </section>

        <section className="socket-dispatcher">
          <div className="panel-label">Manual dispatch</div>
          <div className="socket-form-row">
            <label>
              <span>Channel</span>
              <input
                className="input"
                list="socket-channels"
                value={channel}
                onChange={e => setChannel(e.target.value)}
                placeholder="/events/example"
              />
              <datalist id="socket-channels">
                {channels.map(item => <option key={item} value={item} />)}
              </datalist>
            </label>
            <label>
              <span>Adapter</span>
              <select className="select" value={adapter} onChange={e => setAdapter(e.target.value)}>
                <option value="">Client default</option>
                <option value="raw">Raw</option>
                <option value="appsync">AppSync</option>
              </select>
            </label>
          </div>
          <label className="socket-json-label">
            <span>Payload JSON</span>
            <textarea
              className="socket-json"
              value={payload}
              spellCheck={false}
              onChange={e => setPayload(e.target.value)}
            />
          </label>
          {jsonError && <div className="socket-error">{jsonError}</div>}
          <div className="socket-actions">
            <button
              type="button"
              className="btn primary"
              onClick={handleDispatch}
              disabled={dispatching}
            >
              <Send /> Dispatch
            </button>
          </div>
        </section>
      </div>

      <section className="socket-events">
        <div className="panel-label">Live socket log</div>
        {socketEntries.length === 0 ? (
          <div className="socket-empty compact">Socket events will stream here through the existing SSE log.</div>
        ) : (
          <div className="socket-event-list">
            {socketEntries.map(entry => (
              <div key={entry.id} className="socket-event-row">
                <span className="time">{entry.timestamp}</span>
                <span className="method">{entry.method}</span>
                <span className="path" title={entry.path}>{entry.path}</span>
                <span className="status">{entry.status || '-'}</span>
                {entry.response_body && (
                  <span className="payload" title={entry.response_body}>
                    <Braces size={13} /> {entry.response_body}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
      </section>
    </section>
  )
}

function ClientRow({ client }: { client: SocketClient }) {
  return (
    <div className="socket-client-row">
      <div className="socket-client-main">
        <span className="client-id">{client.id}</span>
        <span className="client-adapter">{client.adapter}</span>
      </div>
      <div className="client-remote" title={client.remote_addr}>{client.remote_addr}</div>
      <div className="client-subs">
        {client.subscriptions.length === 0 ? (
          <span className="sub empty-sub">No subscriptions yet</span>
        ) : (
          client.subscriptions.map(channel => (
            <span key={channel} className="sub" title={channel}>{channel}</span>
          ))
        )}
      </div>
    </div>
  )
}
