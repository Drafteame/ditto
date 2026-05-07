import { useMemo, useState } from 'react'
import type { EventSequence, EventSequenceStep, EventTemplate, PlayerState, SchemaTypeDescriptor } from '../types'
import { Edit, Grip, Plus, Refresh, Sequence, Trash, X } from './icons'
import { useConfirm } from './ConfirmDialog'
import { SequencePlayerView } from './SequencePlayerView'

type SocketAdapter = '' | 'raw' | 'appsync'

interface StepDraft {
  id?: string
  name: string
  delay_ms: number
  template_ref: string
  channel: string
  adapter: SocketAdapter
  type_name: string
  payload: string
  vars_override: string
}

interface SequenceDraft {
  id?: string
  name: string
  description: string
  on_end: 'loop' | 'stay' | 'reset'
  vars: string
  steps: StepDraft[]
}

interface SequencesPanelProps {
  sequences: EventSequence[]
  templates: EventTemplate[]
  schemaTypes: SchemaTypeDescriptor[]
  playerStates: Record<string, PlayerState>
  loading: boolean
  error: string
  onRefresh: () => void
  onSave: (sequence: Partial<EventSequence>, id?: string) => Promise<EventSequence>
  onDelete: (id: string) => Promise<void>
  onPlay: (id: string) => Promise<void>
  onPause: (id: string) => Promise<void>
  onStop: (id: string) => Promise<void>
  onSeek: (id: string, step: number) => Promise<void>
  onSpeed: (id: string, speed: number) => Promise<void>
  showToast: (message: string, kind?: 'warn') => void
}

const DEFAULT_PAYLOAD = `{
  "event": "example",
  "sentAt": "{{now}}"
}`

function draftFromSequence(sequence?: EventSequence): SequenceDraft {
  return {
    id: sequence?.id,
    name: sequence?.name ?? '',
    description: sequence?.description ?? '',
    on_end: sequence?.on_end ?? 'stay',
    vars: JSON.stringify(sequence?.vars ?? {}, null, 2),
    steps: sequence?.steps?.length
      ? sequence.steps.map(step => ({
        id: step.id,
        name: step.name ?? '',
        delay_ms: step.delay_ms ?? 0,
        template_ref: step.template_ref ?? '',
        channel: step.channel ?? '',
        adapter: (step.adapter ?? '') as SocketAdapter,
        type_name: step.type_name ?? '',
        payload: step.payload === undefined ? '' : JSON.stringify(step.payload, null, 2),
        vars_override: JSON.stringify(step.vars_override ?? {}, null, 2),
      }))
      : [{
        name: 'First step',
        delay_ms: 0,
        template_ref: '',
        channel: '',
        adapter: '',
        type_name: '',
        payload: DEFAULT_PAYLOAD,
        vars_override: '{}',
      }],
  }
}

export function SequencesPanel({
  sequences,
  templates,
  schemaTypes,
  playerStates,
  loading,
  error,
  onRefresh,
  onSave,
  onDelete,
  onPlay,
  onPause,
  onStop,
  onSeek,
  onSpeed,
  showToast,
}: SequencesPanelProps) {
  const [draft, setDraft] = useState<SequenceDraft | null>(null)
  const [openPlayerId, setOpenPlayerId] = useState<string | null>(sequences[0]?.id ?? null)
  const confirm = useConfirm()
  const selectedSequence = useMemo(
    () => sequences.find(sequence => sequence.id === openPlayerId) ?? sequences[0] ?? null,
    [openPlayerId, sequences],
  )

  async function handleDelete(sequence: EventSequence) {
    const ok = await confirm({
      title: 'Delete sequence?',
      message: <><code className="font-mono text-fg-0">{sequence.name}</code> will be removed from disk.</>,
      confirmLabel: 'Delete',
      danger: true,
    })
    if (!ok) return
    try {
      await onDelete(sequence.id)
      showToast('Sequence deleted')
    } catch (err) {
      showToast(`Delete failed: ${(err as Error).message}`, 'warn')
    }
  }

  return (
    <section className="socket-panel">
      <div className="socket-head">
        <div className="socket-title">
          <Sequence />
          <span>Sequences</span>
          <span className="socket-count">{sequences.length}</span>
        </div>
        <div className="socket-url">Timed WebSocket timelines</div>
        <button type="button" className="btn ghost" onClick={onRefresh} disabled={loading}>
          <Refresh /> Refresh
        </button>
        <button type="button" className="btn primary" onClick={() => setDraft(draftFromSequence())}>
          <Plus /> New
        </button>
      </div>

      <div className="sequences-grid">
        <div className="template-list">
          {error && <div className="socket-error">{error}</div>}
          {sequences.length === 0 ? (
            <div className="socket-empty">No event sequences yet.</div>
          ) : sequences.map(sequence => (
            <div key={sequence.id} className={`template-row ${selectedSequence?.id === sequence.id ? 'active-row' : ''}`}>
              <div className="template-main">
                <div className="template-name">{sequence.name}</div>
                <div className="template-id" title={sequence.id}>{sequence.id}</div>
                {sequence.description && <div className="template-desc">{sequence.description}</div>}
              </div>
              <div className="template-meta">
                <span>{sequence.steps.length} steps</span>
                <span>on_end: {sequence.on_end}</span>
                <span>{playerStates[sequence.id]?.status ?? 'idle'}</span>
              </div>
              <div className="template-actions">
                <button type="button" className="btn ghost" onClick={() => setOpenPlayerId(sequence.id)}>Open player</button>
                <button type="button" className="btn icon ghost" onClick={() => setDraft(draftFromSequence(sequence))} aria-label={`Edit ${sequence.name}`}>
                  <Edit size={14} />
                </button>
                <button type="button" className="btn icon ghost danger" onClick={() => handleDelete(sequence)} aria-label={`Delete ${sequence.name}`}>
                  <Trash size={14} />
                </button>
              </div>
            </div>
          ))}
        </div>

        {selectedSequence ? (
          <SequencePlayerView
            sequence={selectedSequence}
            state={playerStates[selectedSequence.id]}
            onPlay={() => onPlay(selectedSequence.id)}
            onPause={() => onPause(selectedSequence.id)}
            onStop={() => onStop(selectedSequence.id)}
            onSeek={(step) => onSeek(selectedSequence.id, step)}
            onSpeed={(speed) => onSpeed(selectedSequence.id, speed)}
          />
        ) : (
          <div className="sequence-player socket-empty">Create a sequence to open the player.</div>
        )}
      </div>

      {draft && (
        <SequenceEditorModal
          draft={draft}
          templates={templates}
          schemaTypes={schemaTypes}
          onClose={() => setDraft(null)}
          onSave={async (sequence) => {
            const saved = await onSave(sequence, draft.id)
            setDraft(null)
            setOpenPlayerId(saved.id)
            showToast(draft.id ? 'Sequence updated' : 'Sequence created')
          }}
          showToast={showToast}
        />
      )}
    </section>
  )
}

function SequenceEditorModal({
  draft,
  templates,
  schemaTypes,
  onClose,
  onSave,
  showToast,
}: {
  draft: SequenceDraft
  templates: EventTemplate[]
  schemaTypes: SchemaTypeDescriptor[]
  onClose: () => void
  onSave: (sequence: Partial<EventSequence>) => Promise<void>
  showToast: (message: string, kind?: 'warn') => void
}) {
  const [state, setState] = useState(draft)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [dragIndex, setDragIndex] = useState<number | null>(null)

  function updateStep(index: number, patch: Partial<StepDraft>) {
    setState(current => ({
      ...current,
      steps: current.steps.map((step, i) => i === index ? { ...step, ...patch } : step),
    }))
  }

  function addStep() {
    setState(current => ({
      ...current,
      steps: [...current.steps, { name: '', delay_ms: 0, template_ref: '', channel: '', adapter: '', type_name: '', payload: DEFAULT_PAYLOAD, vars_override: '{}' }],
    }))
  }

  function removeStep(index: number) {
    setState(current => ({ ...current, steps: current.steps.filter((_, i) => i !== index) }))
  }

  function moveStep(from: number, to: number) {
    if (from === to) return
    setState(current => {
      const steps = [...current.steps]
      const [item] = steps.splice(from, 1)
      steps.splice(to, 0, item)
      return { ...current, steps }
    })
  }

  async function handleSave() {
    if (!state.name.trim()) {
      setError('Name is required')
      return
    }
    if (state.steps.length === 0) {
      setError('At least one step is required')
      return
    }
    let vars: Record<string, unknown>
    try {
      vars = JSON.parse(state.vars || '{}')
    } catch (err) {
      setError(`Invalid sequence vars JSON: ${(err as Error).message}`)
      return
    }
    if (!isPlainObject(vars)) {
      setError('Sequence vars must be a JSON object')
      return
    }
    const steps: EventSequenceStep[] = []
    for (const [index, step] of state.steps.entries()) {
      let payload: unknown | undefined
      let varsOverride: Record<string, unknown>
      try {
        payload = step.payload.trim() ? JSON.parse(step.payload) : undefined
      } catch (err) {
        setError(`Step ${index + 1} payload JSON: ${(err as Error).message}`)
        return
      }
      try {
        varsOverride = JSON.parse(step.vars_override || '{}')
      } catch (err) {
        setError(`Step ${index + 1} vars JSON: ${(err as Error).message}`)
        return
      }
      if (!isPlainObject(varsOverride)) {
        setError(`Step ${index + 1} vars must be a JSON object`)
        return
      }
      if (!step.template_ref && (!step.channel.trim() || payload === undefined)) {
        setError(`Step ${index + 1} needs a template or channel + payload`)
        return
      }
      steps.push({
        id: step.id ?? '',
        name: step.name.trim() || undefined,
        delay_ms: Math.max(0, Number(step.delay_ms) || 0),
        template_ref: step.template_ref || undefined,
        channel: step.channel.trim() || undefined,
        adapter: step.adapter,
        type_name: step.type_name || undefined,
        payload,
        vars_override: varsOverride,
      })
    }
    setSaving(true)
    setError('')
    try {
      await onSave({
        name: state.name.trim(),
        description: state.description.trim() || undefined,
        on_end: state.on_end,
        vars,
        steps,
      })
    } catch (err) {
      showToast(`Save failed: ${(err as Error).message}`, 'warn')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-scrim" onMouseDown={onClose}>
      <div className="modal sequence-modal" onMouseDown={e => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <h2>{draft.id ? 'Edit Sequence' : 'New Sequence'}</h2>
            <div className="sub">{draft.id ?? 'Stored in sequences/'}</div>
          </div>
          <button type="button" className="btn icon ghost ml-auto" onClick={onClose} aria-label="Close">
            <X />
          </button>
        </div>
        <div className="modal-body sequence-editor">
          <div className="template-form-grid">
            <label><span>Name</span><input className="input" value={state.name} onChange={e => setState({ ...state, name: e.target.value })} /></label>
            <label><span>On end</span><select className="select" value={state.on_end} onChange={e => setState({ ...state, on_end: e.target.value as SequenceDraft['on_end'] })}><option value="stay">Stay</option><option value="reset">Reset</option><option value="loop">Loop</option></select></label>
          </div>
          <label className="template-description"><span>Description</span><input className="input" value={state.description} onChange={e => setState({ ...state, description: e.target.value })} /></label>
          <label className="template-json"><span>Sequence vars</span><textarea className="socket-json seq-vars" value={state.vars} onChange={e => setState({ ...state, vars: e.target.value })} /></label>
          <div className="template-vars-head">
            <div className="panel-label">Steps</div>
            <button type="button" className="btn ghost" onClick={addStep}><Plus /> Add step</button>
          </div>
          <div className="sequence-step-list">
            {state.steps.map((step, index) => (
              <div
                key={`${step.id || 'new'}-${index}`}
                className="sequence-step-row"
                draggable
                onDragStart={() => setDragIndex(index)}
                onDragOver={e => e.preventDefault()}
                onDrop={() => { if (dragIndex !== null) moveStep(dragIndex, index); setDragIndex(null) }}
              >
                <Grip className="drag-grip" />
                <div className="step-index">{index + 1}</div>
                <input className="input" value={step.name} onChange={e => updateStep(index, { name: e.target.value })} placeholder="Step name" />
                <input className="input" type="number" min="0" value={step.delay_ms} onChange={e => updateStep(index, { delay_ms: Number(e.target.value) })} title="Delay before dispatch in ms" />
                <select className="select" value={step.template_ref} onChange={e => updateStep(index, { template_ref: e.target.value })}>
                  <option value="">Inline</option>
                  {templates.map(template => <option key={template.id} value={template.id}>{template.name}</option>)}
                </select>
                <input className="input" value={step.channel} onChange={e => updateStep(index, { channel: e.target.value })} placeholder="channel override" />
                <select className="select" value={step.adapter} onChange={e => updateStep(index, { adapter: e.target.value as SocketAdapter })}>
                  <option value="">Default</option>
                  <option value="raw">Raw</option>
                  <option value="appsync">AppSync</option>
                </select>
                <select className="select" value={step.type_name} onChange={e => updateStep(index, { type_name: e.target.value })}>
                  <option value="">Raw JSON</option>
                  {schemaTypes.map(type => <option key={type.full_name} value={type.full_name}>{type.full_name}</option>)}
                </select>
                <textarea className="socket-json step-json" value={step.payload} onChange={e => updateStep(index, { payload: e.target.value })} placeholder="payload override" />
                <textarea className="socket-json step-json" value={step.vars_override} onChange={e => updateStep(index, { vars_override: e.target.value })} placeholder="vars override" />
                <button type="button" className="btn icon ghost danger" onClick={() => removeStep(index)} aria-label={`Remove step ${index + 1}`}><Trash size={14} /></button>
              </div>
            ))}
          </div>
          {error && <div className="socket-error">{error}</div>}
        </div>
        <div className="modal-foot">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="button" className="btn primary" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}
