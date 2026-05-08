import { useEffect, useMemo, useState } from 'react'
import type { ChangeEvent } from 'react'
import type {
  EventTemplate,
  EventTemplateDispatchResult,
  LogEntry,
  SchemaPack,
  SchemaTypeDescriptor,
  ServerInfo,
  SocketClient,
} from '../types'
import { parseDispatchLogBody } from '../types'
import * as api from '../api'
import { Braces, Download, Radio, Refresh, Send, X } from './icons'
import { detectTemplateVariablesInValue, isBuiltinVariable } from './EventTemplatesPanel'
import { useSocketStore, buildAdapterOptions } from '../stores/useSocketStore'
import { useChannelModeStore } from '../stores/useChannelModeStore'
import type { ChannelMode } from '../types'

interface SocketPanelProps {
  clients: SocketClient[]
  entries: LogEntry[]
  serverInfo: ServerInfo | null
  schemaPacks: SchemaPack[]
  schemaTypes: SchemaTypeDescriptor[]
  schemasLoading: boolean
  schemasError: string
  templates: EventTemplate[]
  templatesLoading: boolean
  templatesError: string
  loading: boolean
  error: string
  onRefresh: () => void
  onRefreshSchemas: () => void
  onRefreshTemplates: () => void
  onUploadSchemaPack: (file: File) => Promise<void>
  onDeleteSchemaPack: (id: string) => Promise<void>
  onDispatchTemplate: (id: string, variables: Record<string, string>) => Promise<EventTemplateDispatchResult>
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
  schemaPacks,
  schemaTypes,
  schemasLoading,
  schemasError,
  templates,
  templatesLoading,
  templatesError,
  loading,
  error,
  onRefresh,
  onRefreshSchemas,
  onRefreshTemplates,
  onUploadSchemaPack,
  onDeleteSchemaPack,
  onDispatchTemplate,
  showToast,
}: SocketPanelProps) {
  const channels = useMemo(() => {
    const set = new Set<string>()
    clients.forEach(client => client.subscriptions.forEach(channel => set.add(channel)))
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [clients])

  const adapterProfiles = useSocketStore(state => state.adapterProfiles)
  const channelModes = useChannelModeStore(state => state.modes)
  const setChannelMode = useChannelModeStore(state => state.setMode)
  const liveTarget = useChannelModeStore(state => state.liveTarget || serverInfo?.live_target || '')
  const adapterOptions = useMemo(() => buildAdapterOptions(adapterProfiles), [adapterProfiles])

  const [channel, setChannel] = useState('')
  const [adapter, setAdapter] = useState<string>('')
  const [typeName, setTypeName] = useState('')
  const [payload, setPayload] = useState(DEFAULT_PAYLOAD)
  const [dispatching, setDispatching] = useState(false)
  const [jsonError, setJsonError] = useState('')
  const [schemaModalOpen, setSchemaModalOpen] = useState(false)
  const [selectedTemplateId, setSelectedTemplateId] = useState('')
  const [templateVars, setTemplateVars] = useState<Record<string, string>>({})
  const [templateDispatching, setTemplateDispatching] = useState(false)
  const [lastResolvedTemplatePayload, setLastResolvedTemplatePayload] = useState('')

  const selectedType = useMemo(
    () => schemaTypes.find(type => type.full_name === typeName) ?? null,
    [schemaTypes, typeName],
  )

  const socketEntries = useMemo(
    () => entries.filter(entry => entry.type === 'SOCKET').slice(-200).reverse(),
    [entries],
  )
  const selectedTemplate = useMemo(
    () => templates.find(template => template.id === selectedTemplateId) ?? null,
    [templates, selectedTemplateId],
  )
  const selectedTemplateVars = useMemo(() => {
    if (!selectedTemplate) return []
    const names = new Set<string>()
    selectedTemplate.variables?.forEach(variable => {
      if (variable.name && !isBuiltinVariable(variable.name)) names.add(variable.name)
    })
    detectTemplateVariablesInValue(selectedTemplate.payload).forEach(name => {
      if (!isBuiltinVariable(name)) names.add(name)
    })
    return [...names].sort((a, b) => a.localeCompare(b))
  }, [selectedTemplate])
  const scheme = serverInfo?.https ? 'wss' : 'ws'
  const wsUrl = serverInfo ? `${scheme}://localhost:${serverInfo.port}` : ''
  const visibleChannels = useMemo(() => {
    const set = new Set([...channels, ...Object.keys(channelModes)])
    if (channel.trim()) set.add(channel.trim())
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [channel, channels, channelModes])

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
        adapter,
        type_name: typeName || undefined,
      })
      const dropped = result.dropped?.length ?? 0
      const errors = result.errors?.length ?? 0
      const suffix = dropped || errors ? `, ${dropped} dropped, ${errors} errors` : ''
      const detail = result.errors?.[0] ? `: ${result.errors[0]}` : result.dropped?.[0] ? `: dropped ${result.dropped[0]}` : ''
      showToast(`Dispatched to ${result.delivered} client${result.delivered === 1 ? '' : 's'}${suffix}${detail}`)
    } catch (err) {
      showToast(`Dispatch failed: ${(err as Error).message}`, 'warn')
    } finally {
      setDispatching(false)
    }
  }

  function handleTypeChange(nextTypeName: string) {
    setTypeName(nextTypeName)
    const nextType = schemaTypes.find(type => type.full_name === nextTypeName)
    if (nextType?.example_json) {
      setPayload(JSON.stringify(nextType.example_json, null, 2))
      setJsonError('')
    }
  }

  function selectTemplate(template: EventTemplate) {
    setSelectedTemplateId(template.id)
    setLastResolvedTemplatePayload('')
    setTemplateVars(current => {
      const next: Record<string, string> = {}
      template.variables?.forEach(variable => {
        if (!isBuiltinVariable(variable.name)) {
          next[variable.name] = current[variable.name] ?? variable.default ?? ''
        }
      })
      detectTemplateVariablesInValue(template.payload).forEach(name => {
        if (!isBuiltinVariable(name) && next[name] === undefined) {
          next[name] = current[name] ?? ''
        }
      })
      return next
    })
  }

  async function handleTemplateDispatch() {
    if (!selectedTemplate) return
    setTemplateDispatching(true)
    try {
      const vars: Record<string, string> = {}
      selectedTemplateVars.forEach(name => {
        const value = templateVars[name] ?? ''
        if (value !== '') vars[name] = value
      })
      const result = await onDispatchTemplate(selectedTemplate.id, vars)
      setLastResolvedTemplatePayload(JSON.stringify(result.resolved_payload, null, 2))
      const dropped = result.dropped?.length ?? 0
      const errors = result.errors?.length ?? 0
      const suffix = dropped || errors ? `, ${dropped} dropped, ${errors} errors` : ''
      showToast(`Template dispatched to ${result.delivered} client${result.delivered === 1 ? '' : 's'}${suffix}`)
    } catch (err) {
      showToast(`Template dispatch failed: ${(err as Error).message}`, 'warn')
    } finally {
      setTemplateDispatching(false)
    }
  }

  function insertField(fieldName: string) {
    setPayload(current => {
      try {
        const parsed = JSON.parse(current || '{}')
        if (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object') {
          setJsonError('Payload must be a JSON object to insert a field')
          return current
        }
        return JSON.stringify({ ...parsed, [fieldName]: null }, null, 2)
      } catch (err) {
        setJsonError(`Invalid JSON: ${(err as Error).message}`)
        return current
      }
    })
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
        <button type="button" className="btn ghost" onClick={() => setSchemaModalOpen(true)}>
          <Braces /> Schemas
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

        <section className="socket-modes">
          <div className="panel-label">Channel modes</div>
          {visibleChannels.length === 0 ? (
            <div className="socket-empty compact">Subscribe or type a channel to configure its mode.</div>
          ) : (
            <div className="channel-mode-list">
              {visibleChannels.map(item => {
                const current = channelModes[item]?.mode ?? 'mock'
                return (
                  <div className="channel-mode-row" key={item}>
                    <span title={item}>{item}</span>
                    <select
                      className="select"
                      value={current}
                      title={!liveTarget ? 'Configura un Live Target en Settings' : ''}
                      onChange={async e => {
                        const next = e.target.value as ChannelMode
                        try {
                          await setChannelMode(item, next, channelModes[item]?.rate_cap_hz ?? 0)
                        } catch (err) {
                          showToast(`Mode update failed: ${(err as Error).message}`, 'warn')
                        }
                      }}
                    >
                      <option value="mock">Mock</option>
                      <option value="live" disabled={!liveTarget}>Live</option>
                      <option value="record">Record</option>
                      <option value="mixed" disabled={!liveTarget}>Mixed</option>
                    </select>
                    <ChannelRateCapInput
                      value={channelModes[item]?.rate_cap_hz ?? 0}
                      onCommit={async rate => {
                        await setChannelMode(item, current, rate)
                      }}
                      showToast={showToast}
                    />
                  </div>
                )
              })}
            </div>
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
                {visibleChannels.map(item => <option key={item} value={item} />)}
              </datalist>
            </label>
            <label>
              <span>Adapter</span>
              <select className="select" value={adapter} onChange={e => setAdapter(e.target.value)}>
                <option value="">Client default</option>
                {adapterOptions.map(option => (
                  <option key={option.value} value={option.value}>{option.label}</option>
                ))}
              </select>
            </label>
          </div>
          <label className="socket-type-label">
            <span>Payload type</span>
            <select className="select" value={typeName} onChange={e => handleTypeChange(e.target.value)}>
              <option value="">Raw JSON</option>
              {schemaTypes.map(type => (
                <option key={type.full_name} value={type.full_name}>
                  {type.full_name}
                </option>
              ))}
            </select>
          </label>
          {selectedType && (
            <div className="schema-field-strip">
              {selectedType.fields.map(field => (
                <button
                  key={field.json_name}
                  type="button"
                  title={`${field.type}${field.repeated ? ' repeated' : ''}${field.map ? ' map' : ''}`}
                  onClick={() => insertField(field.json_name)}
                >
                  {field.json_name}
                </button>
              ))}
            </div>
          )}
          <label className="socket-json-label">
            <span>{selectedType ? 'Payload JSON -> Protobuf' : 'Payload JSON'}</span>
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

        <section className="socket-templates">
          <div className="socket-templates-head">
            <div className="panel-label">Templates</div>
            <button type="button" className="btn icon ghost" onClick={onRefreshTemplates} disabled={templatesLoading} aria-label="Refresh templates">
              <Refresh size={14} />
            </button>
          </div>
          {templatesError && <div className="socket-error">{templatesError}</div>}
          {templates.length === 0 ? (
            <div className="socket-empty compact">Saved templates will appear here.</div>
          ) : (
            <div className="quick-template-list">
              {templates.map(template => (
                <button
                  key={template.id}
                  type="button"
                  className={selectedTemplateId === template.id ? 'quick-template active' : 'quick-template'}
                  onClick={() => selectTemplate(template)}
                >
                  <span>{template.name}</span>
                  <small title={template.channel}>{template.channel}</small>
                </button>
              ))}
            </div>
          )}
          {selectedTemplate && (
            <div className="quick-template-form">
              <div className="quick-template-title">{selectedTemplate.name}</div>
              <div className="template-detected" title={selectedTemplate.channel}>
                {selectedTemplate.channel} / {selectedTemplate.adapter || 'client default'}
              </div>
              {selectedTemplateVars.length === 0 ? (
                <div className="template-detected">Only built-ins or no variables.</div>
              ) : (
                selectedTemplateVars.map(name => (
                  <label key={name}>
                    <span>{name}</span>
                    <input
                      className="input"
                      value={templateVars[name] ?? ''}
                      onChange={e => setTemplateVars(current => ({ ...current, [name]: e.target.value }))}
                    />
                  </label>
                ))
              )}
              <button type="button" className="btn primary" onClick={handleTemplateDispatch} disabled={templateDispatching}>
                <Send /> Dispatch
              </button>
              {lastResolvedTemplatePayload && (
                <pre className="template-resolved-preview">{lastResolvedTemplatePayload}</pre>
              )}
            </div>
          )}
        </section>
      </div>

      <section className="socket-events">
        <div className="panel-label">Live socket log</div>
        {socketEntries.length === 0 ? (
          <div className="socket-empty compact">Socket events will stream here through the existing SSE log.</div>
        ) : (
          <div className="socket-event-list">
            {socketEntries.map(entry => <SocketEventRow key={entry.id} entry={entry} />)}
          </div>
        )}
      </section>

      {schemaModalOpen && (
        <SchemaPacksModal
          packs={schemaPacks}
          types={schemaTypes}
          loading={schemasLoading}
          error={schemasError}
          onClose={() => setSchemaModalOpen(false)}
          onRefresh={onRefreshSchemas}
          onUpload={onUploadSchemaPack}
          onDelete={onDeleteSchemaPack}
          showToast={showToast}
        />
      )}
    </section>
  )
}

function SocketEventRow({ entry }: { entry: LogEntry }) {
  const parsed = entry.method === 'DISPATCH' ? parseDispatchLogBody(entry.response_body) : null
  const hasPayload = parsed && parsed.payload !== undefined
  const label = parsed?.alias || parsed?.type_name || (hasPayload ? 'JSON' : '')
  return (
    <div className={entry.method === 'DISPATCH_BURST' ? 'socket-event-row burst' : 'socket-event-row'}>
      <span className="time">{entry.timestamp}</span>
      <span className="method">{entry.method}</span>
      <span className="path" title={entry.path}>{entry.path}</span>
      <span className="status">{entry.status || '-'}</span>
      {entry.method === 'DISPATCH_BURST' && <BurstBadge body={entry.response_body} />}
      {parsed ? (
        <div className="payload dispatch-payload">
          <span className="dispatch-counts" title={entry.response_body}>
            {parsed.delivered} delivered | {parsed.dropped} dropped | {parsed.errors} errors
          </span>
          {label && <span className="payload-chip">{label}</span>}
          {parsed.truncated && <span className="truncate-badge">truncated 4KB</span>}
          {parsed.decode_error && (
            <span className="decode-badge" title={parsed.decode_error}>decode</span>
          )}
          {hasPayload && (
            <details className="payload-details">
              <summary>payload</summary>
              <pre>{formatPayload(parsed.payload)}</pre>
            </details>
          )}
        </div>
      ) : entry.response_body ? (
        <span className="payload" title={entry.response_body}>
          <Braces size={13} /> {entry.response_body}
        </span>
      ) : null}
    </div>
  )
}

function formatPayload(payload: unknown) {
  if (typeof payload === 'string') return payload
  return JSON.stringify(payload, null, 2)
}

function ChannelRateCapInput({
  value,
  onCommit,
  showToast,
}: {
  value: number
  onCommit: (rate: number) => Promise<void>
  showToast: (message: string, kind?: 'warn') => void
}) {
  const [draft, setDraft] = useState(String(value))

  useEffect(() => {
    setDraft(String(value))
  }, [value])

  async function commit() {
    const rate = Math.max(0, Number.parseInt(draft || '0', 10) || 0)
    setDraft(String(rate))
    if (rate === value) return
    try {
      await onCommit(rate)
    } catch (err) {
      setDraft(String(value))
      showToast(`Rate cap update failed: ${(err as Error).message}`, 'warn')
    }
  }

  return (
    <input
      className="input rate-cap"
      type="number"
      min="0"
      value={draft}
      title="Recording rate cap (Hz), 0 disables"
      onChange={e => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={e => {
        if (e.key === 'Enter') {
          e.currentTarget.blur()
        }
      }}
    />
  )
}

function ClientRow({ client }: { client: SocketClient }) {
  return (
    <div className="socket-client-row">
      <div className="socket-client-main">
        <span className="client-id">{client.id}</span>
        {!!client.dropped_to_client && <span className="drop-badge">{client.dropped_to_client} dropped</span>}
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

function BurstBadge({ body }: { body?: string }) {
  let label = 'burst'
  try {
    const parsed = JSON.parse(body || '{}') as { total_frames?: number; frames?: number; window_ms?: number }
    const frames = parsed.total_frames ?? parsed.frames
    if (frames) label = `${frames} total frames in ${Math.round((parsed.window_ms || 1000) / 1000)}s`
  } catch {
    label = 'burst'
  }
  return <span className="burst-badge">{label}</span>
}

function SchemaPacksModal({
  packs,
  types,
  loading,
  error,
  onClose,
  onRefresh,
  onUpload,
  onDelete,
  showToast,
}: {
  packs: SchemaPack[]
  types: SchemaTypeDescriptor[]
  loading: boolean
  error: string
  onClose: () => void
  onRefresh: () => void
  onUpload: (file: File) => Promise<void>
  onDelete: (id: string) => Promise<void>
  showToast: (message: string, kind?: 'warn') => void
}) {
  const [uploading, setUploading] = useState(false)

  async function handleFileChange(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setUploading(true)
    try {
      await onUpload(file)
      showToast(`Loaded schema pack ${file.name}`)
    } catch (err) {
      showToast(`Schema upload failed: ${(err as Error).message}`, 'warn')
    } finally {
      setUploading(false)
    }
  }

  async function handleDelete(pack: SchemaPack) {
    if (!window.confirm(`Delete schema pack "${pack.name}"?`)) {
      return
    }
    try {
      await onDelete(pack.id)
      showToast(`Deleted schema pack ${pack.name}`)
    } catch (err) {
      showToast(`Delete failed: ${(err as Error).message}`, 'warn')
    }
  }

  return (
    <div className="modal-scrim" onMouseDown={onClose}>
      <div className="modal schema-modal" onMouseDown={e => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <h2>Schema Packs</h2>
            <div className="sub">{packs.length} packs / {types.length} types</div>
          </div>
          <button type="button" className="btn icon ghost ml-auto" onClick={onClose} aria-label="Close">
            <X />
          </button>
        </div>
        <div className="modal-body">
          <div className="schema-upload-row">
            <label className="btn primary">
              <Download /> Upload .proto or .zip
              <input type="file" accept=".proto,.zip" onChange={handleFileChange} disabled={uploading || loading} />
            </label>
            <button type="button" className="btn ghost" onClick={onRefresh} disabled={loading}>
              <Refresh /> Refresh
            </button>
          </div>
          {error && <div className="socket-error">{error}</div>}
          <div className="schema-modal-grid">
            <section>
              <div className="panel-label">Loaded packs</div>
              {packs.length === 0 ? (
                <div className="socket-empty compact">No schema packs loaded.</div>
              ) : (
                packs.map(pack => (
                  <div key={pack.id} className="schema-pack-row">
                    <div className="schema-pack-main">
                      <div className="schema-pack-name">{pack.name}</div>
                      <button type="button" className="btn icon ghost" onClick={() => handleDelete(pack)} aria-label={`Delete ${pack.name}`}>
                        <X size={14} />
                      </button>
                    </div>
                    <div className="schema-pack-id" title={pack.id}>{pack.id}</div>
                    <div className="schema-pack-path" title={pack.path}>{pack.path}</div>
                    <div className="schema-pack-count">{pack.types.length} types</div>
                  </div>
                ))
              )}
            </section>
            <section>
              <div className="panel-label">Available types</div>
              <div className="schema-type-list">
                {types.map(type => (
                  <div key={type.full_name} className="schema-type-row">
                    <span title={type.full_name}>{type.full_name}</span>
                    <small>{type.file}</small>
                  </div>
                ))}
              </div>
            </section>
          </div>
        </div>
      </div>
    </div>
  )
}
