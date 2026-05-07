import { useMemo, useState } from 'react'
import type { EventTemplate, EventTemplateVariable, SchemaTypeDescriptor } from '../types'
import { Braces, Edit, Plus, Refresh, Send, Trash, X } from './icons'
import { useConfirm } from './ConfirmDialog'

type SocketAdapter = '' | 'raw' | 'appsync'

const DEFAULT_TEMPLATE_PAYLOAD = `{
  "ticketId": "{{ticketId}}",
  "sentAt": "{{now}}"
}`

interface EventTemplatesPanelProps {
  templates: EventTemplate[]
  schemaTypes: SchemaTypeDescriptor[]
  loading: boolean
  error: string
  onRefresh: () => void
  onSave: (template: Partial<EventTemplate>, id?: string) => Promise<EventTemplate>
  onDelete: (id: string) => Promise<void>
  showToast: (message: string, kind?: 'warn') => void
}

interface TemplateDraft {
  id?: string
  name: string
  description: string
  channel: string
  adapter: SocketAdapter
  type_name: string
  payload: string
  variables: EventTemplateVariable[]
}

function draftFromTemplate(template?: EventTemplate): TemplateDraft {
  return {
    id: template?.id,
    name: template?.name ?? '',
    description: template?.description ?? '',
    channel: template?.channel ?? '',
    adapter: (template?.adapter ?? '') as SocketAdapter,
    type_name: template?.type_name ?? '',
    payload: template ? JSON.stringify(template.payload, null, 2) : DEFAULT_TEMPLATE_PAYLOAD,
    variables: template?.variables?.length
      ? template.variables.map(variable => ({ ...variable }))
      : [{ name: 'ticketId', description: '' }],
  }
}

export function EventTemplatesPanel({
  templates,
  schemaTypes,
  loading,
  error,
  onRefresh,
  onSave,
  onDelete,
  showToast,
}: EventTemplatesPanelProps) {
  const [draft, setDraft] = useState<TemplateDraft | null>(null)
  const confirm = useConfirm()

  async function handleDelete(template: EventTemplate) {
    const ok = await confirm({
      title: 'Delete template?',
      message: (
        <>
          <code className="font-mono text-fg-0">{template.name}</code> will be removed from disk.
        </>
      ),
      confirmLabel: 'Delete',
      danger: true,
    })
    if (!ok) return
    try {
      await onDelete(template.id)
      showToast('Template deleted')
    } catch (err) {
      showToast(`Delete failed: ${(err as Error).message}`, 'warn')
    }
  }

  return (
    <section className="socket-panel">
      <div className="socket-head">
        <div className="socket-title">
          <Braces />
          <span>Event Templates</span>
          <span className="socket-count">{templates.length}</span>
        </div>
        <div className="socket-url">Reusable parameterized dispatches</div>
        <button type="button" className="btn ghost" onClick={onRefresh} disabled={loading}>
          <Refresh /> Refresh
        </button>
        <button type="button" className="btn primary" onClick={() => setDraft(draftFromTemplate())}>
          <Plus /> New
        </button>
      </div>

      <div className="template-list">
        {error && <div className="socket-error">{error}</div>}
        {templates.length === 0 ? (
          <div className="socket-empty">No event templates yet.</div>
        ) : (
          templates.map(template => (
            <div key={template.id} className="template-row">
              <div className="template-main">
                <div className="template-name">{template.name}</div>
                <div className="template-id" title={template.id}>{template.id}</div>
                {template.description && <div className="template-desc">{template.description}</div>}
              </div>
              <div className="template-meta">
                <span title={template.channel}>{template.channel}</span>
                <span>{template.adapter || 'client default'}</span>
                <span>{template.type_name || 'Raw JSON'}</span>
              </div>
              <div className="template-actions">
                <button type="button" className="btn icon ghost" onClick={() => setDraft(draftFromTemplate(template))} aria-label={`Edit ${template.name}`}>
                  <Edit size={14} />
                </button>
                <button type="button" className="btn icon ghost danger" onClick={() => handleDelete(template)} aria-label={`Delete ${template.name}`}>
                  <Trash size={14} />
                </button>
              </div>
            </div>
          ))
        )}
      </div>

      {draft && (
        <TemplateEditorModal
          draft={draft}
          schemaTypes={schemaTypes}
          onClose={() => setDraft(null)}
          onSave={async (next) => {
            await onSave(next, draft.id)
            setDraft(null)
            showToast(draft.id ? 'Template updated' : 'Template created')
          }}
          showToast={showToast}
        />
      )}
    </section>
  )
}

function TemplateEditorModal({
  draft,
  schemaTypes,
  onClose,
  onSave,
  showToast,
}: {
  draft: TemplateDraft
  schemaTypes: SchemaTypeDescriptor[]
  onClose: () => void
  onSave: (template: Partial<EventTemplate>) => Promise<void>
  showToast: (message: string, kind?: 'warn') => void
}) {
  const [state, setState] = useState(draft)
  const [saving, setSaving] = useState(false)
  const [jsonError, setJsonError] = useState('')

  const detectedVariables = useMemo(() => detectTemplateVariables(state.payload), [state.payload])

  function updateVariable(index: number, patch: Partial<EventTemplateVariable>) {
    setState(current => ({
      ...current,
      variables: current.variables.map((variable, i) => i === index ? { ...variable, ...patch } : variable),
    }))
  }

  function removeVariable(index: number) {
    setState(current => ({
      ...current,
      variables: current.variables.filter((_, i) => i !== index),
    }))
  }

  function addDetectedVariables() {
    setState(current => {
      const names = new Set(current.variables.map(variable => variable.name.trim()).filter(Boolean))
      const next = [...current.variables]
      detectedVariables.forEach(name => {
        if (!names.has(name) && !isBuiltinVariable(name)) {
          next.push({ name, description: '' })
        }
      })
      return { ...current, variables: next }
    })
  }

  async function handleSave() {
    if (!state.name.trim()) {
      setJsonError('Name is required')
      return
    }
    if (!state.channel.trim()) {
      setJsonError('Channel is required')
      return
    }
    let payload: unknown
    try {
      payload = JSON.parse(state.payload)
    } catch (err) {
      setJsonError(`Invalid JSON: ${(err as Error).message}`)
      return
    }
    const variables = state.variables
      .map(variable => {
        const next: EventTemplateVariable = {
          name: variable.name.trim(),
          description: variable.description?.trim() || undefined,
        }
        if (variable.default !== undefined) next.default = variable.default
        return next
      })
      .filter(variable => variable.name)
    setSaving(true)
    setJsonError('')
    try {
      await onSave({
        name: state.name.trim(),
        description: state.description.trim() || undefined,
        channel: state.channel.trim(),
        adapter: state.adapter,
        type_name: state.type_name || undefined,
        payload,
        variables,
      })
    } catch (err) {
      showToast(`Save failed: ${(err as Error).message}`, 'warn')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-scrim" onMouseDown={onClose}>
      <div className="modal template-modal" onMouseDown={e => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <h2>{draft.id ? 'Edit Template' : 'New Template'}</h2>
            <div className="sub">{draft.id ?? 'Stored in event_templates/'}</div>
          </div>
          <button type="button" className="btn icon ghost ml-auto" onClick={onClose} aria-label="Close">
            <X />
          </button>
        </div>
        <div className="modal-body template-editor">
          <div className="template-form-grid">
            <label>
              <span>Name</span>
              <input className="input" value={state.name} onChange={e => setState({ ...state, name: e.target.value })} />
            </label>
            <label>
              <span>Channel</span>
              <input className="input" value={state.channel} onChange={e => setState({ ...state, channel: e.target.value })} placeholder="/events/tickets" />
            </label>
            <label>
              <span>Adapter</span>
              <select className="select" value={state.adapter} onChange={e => setState({ ...state, adapter: e.target.value as SocketAdapter })}>
                <option value="">Client default</option>
                <option value="raw">Raw</option>
                <option value="appsync">AppSync</option>
              </select>
            </label>
            <label>
              <span>Payload type</span>
              <select className="select" value={state.type_name} onChange={e => setState({ ...state, type_name: e.target.value })}>
                <option value="">Raw JSON</option>
                {schemaTypes.map(type => (
                  <option key={type.full_name} value={type.full_name}>{type.full_name}</option>
                ))}
              </select>
            </label>
          </div>
          <label className="template-description">
            <span>Description</span>
            <input className="input" value={state.description} onChange={e => setState({ ...state, description: e.target.value })} />
          </label>
          <label className="socket-json-label">
            <span>Payload JSON</span>
            <textarea
              className="socket-json template-json"
              value={state.payload}
              spellCheck={false}
              onChange={e => setState({ ...state, payload: e.target.value })}
            />
          </label>
          <div className="template-vars-head">
            <div className="panel-label">Variables</div>
            <button type="button" className="btn ghost" onClick={addDetectedVariables}>
              <Braces /> Detect
            </button>
            <button type="button" className="btn ghost" onClick={() => setState({ ...state, variables: [...state.variables, { name: '', description: '' }] })}>
              <Plus /> Add
            </button>
          </div>
          <div className="template-detected">
            {detectedVariables.length ? `Detected: ${detectedVariables.join(', ')}` : 'No {{variables}} detected in values.'}
          </div>
          <div className="template-variable-list">
            {state.variables.map((variable, index) => (
              <div key={index} className="template-variable-row">
                <input className="input" value={variable.name} onChange={e => updateVariable(index, { name: e.target.value })} placeholder="name" />
                <input className="input" value={variable.default ?? ''} onChange={e => updateVariable(index, { default: e.target.value })} placeholder="default" />
                <input className="input" value={variable.description ?? ''} onChange={e => updateVariable(index, { description: e.target.value })} placeholder="description" />
                {variable.default !== undefined && (
                  <button type="button" className="btn ghost" onClick={() => updateVariable(index, { default: undefined })}>
                    Clear
                  </button>
                )}
                <button type="button" className="btn icon ghost" onClick={() => removeVariable(index)} aria-label="Remove variable">
                  <X size={14} />
                </button>
              </div>
            ))}
          </div>
          {jsonError && <div className="socket-error">{jsonError}</div>}
        </div>
        <div className="modal-foot">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="button" className="btn primary" onClick={handleSave} disabled={saving}>
            <Send /> Save
          </button>
        </div>
      </div>
    </div>
  )
}

export function detectTemplateVariables(payloadText: string): string[] {
  try {
    return detectTemplateVariablesInValue(JSON.parse(payloadText))
  } catch {
    return detectTemplateVariablesInString(payloadText)
  }
}

export function detectTemplateVariablesInValue(value: unknown): string[] {
  const names = new Set<string>()
  const visit = (current: unknown) => {
    if (typeof current === 'string') {
      detectTemplateVariablesInString(current).forEach(name => names.add(name))
      return
    }
    if (Array.isArray(current)) {
      current.forEach(visit)
      return
    }
    if (current && typeof current === 'object') {
      Object.values(current as Record<string, unknown>).forEach(visit)
    }
  }
  visit(value)
  return [...names].sort((a, b) => a.localeCompare(b))
}

function detectTemplateVariablesInString(payloadText: string): string[] {
  const names = new Set<string>()
  const re = /\{\{\s*(?:(?:\w+):)?([A-Za-z_][A-Za-z0-9_]*)\s*\}\}/g
  let match: RegExpExecArray | null
  while ((match = re.exec(payloadText)) !== null) {
    names.add(match[1])
  }
  return [...names].sort((a, b) => a.localeCompare(b))
}

export function isBuiltinVariable(name: string): boolean {
  return name === 'now' || name === 'now_unix' || name === 'now_unix_ms' || name === 'uuid'
}
