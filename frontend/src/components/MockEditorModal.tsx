import { Fragment, useState, useEffect, useCallback, useMemo } from 'react'
import type { Mock, ResponseMode, SequenceStep } from '../types'
import * as api from '../api'
import { Alert, Check, Chevron, Plus, Refresh, Trash, X } from './icons'
import { useConfirm } from './ConfirmDialog'

export interface SequenceStepDraft {
  status: number
  body: string // JSON as editable text
  delay: number
}

export interface MockEditorState {
  editingIndex: number | null // null = creating new
  method: string
  path: string
  status: number
  delay: number
  body: string
  matchQuery: string
  matchHeaders: string
  matchBody: string
  matchOpen: boolean
  responseMode: ResponseMode
  sequenceSteps: SequenceStepDraft[]
  sequenceOnEnd: 'loop' | 'stay' | 'reset'
  sequenceCurrentStep: number
}

function makeDefaultStep(status: number, body: string): SequenceStepDraft {
  return { status, body, delay: 0 }
}

function sequenceStepsFromMock(mock: Mock): SequenceStepDraft[] {
  const steps = mock.sequence?.steps ?? []
  return steps.map(s => ({
    status: s.status || 200,
    body: jsonToText(s.body),
    delay: s.delay_ms || 0,
  }))
}

function jsonToText(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return ''
  }
}

function truncatePreview(text: string, maxLines = 6): string {
  if (!text) return ''
  const lines = text.split('\n')
  if (lines.length <= maxLines) return text
  return lines.slice(0, maxLines).join('\n') + '\n…'
}

interface MockEditorModalProps {
  state: MockEditorState
  onClose: () => void
  onSaved: () => void
  showToast: (message: string, kind?: 'warn') => void
}

export function createNewMockState(
  method: string,
  path: string,
  status: number,
  responseBody?: string,
): MockEditorState {
  let cleanPath = path || ''
  let queryString = ''
  const queryIdx = cleanPath.indexOf('?')
  if (queryIdx >= 0) {
    queryString = cleanPath.slice(queryIdx + 1)
    cleanPath = cleanPath.slice(0, queryIdx)
  }

  let prettyBody = ''
  try {
    prettyBody = JSON.stringify(JSON.parse(responseBody || '{}'), null, 2)
  } catch {
    prettyBody = responseBody || '{}'
  }

  const matchQuery = queryString
    ? new URLSearchParams(queryString).toString().split('&').join('\n')
    : ''

  return {
    editingIndex: null,
    method: method || 'GET',
    path: cleanPath,
    status: status || 200,
    delay: 0,
    body: prettyBody,
    matchQuery,
    matchHeaders: '',
    matchBody: '',
    matchOpen: !!queryString,
    responseMode: 'static',
    sequenceSteps: [],
    sequenceOnEnd: 'loop',
    sequenceCurrentStep: 0,
  }
}

export function createEditMockState(index: number, mock: Mock): MockEditorState {
  let prettyBody = ''
  try {
    prettyBody = JSON.stringify(mock.body, null, 2)
  } catch {
    prettyBody = JSON.stringify(mock.body)
  }

  const match = mock.match || {}
  const pills = getMatchPillCount(match)

  return {
    editingIndex: index,
    method: mock.method,
    path: mock.path,
    status: mock.status,
    delay: mock.delay_ms || 0,
    body: prettyBody,
    matchQuery: mapToLines(match.query, '='),
    matchHeaders: mapToLines(match.headers, ': '),
    matchBody: match.body ? JSON.stringify(match.body, null, 2) : '',
    matchOpen: pills > 0,
    responseMode: mock.response_mode === 'sequence' ? 'sequence' : 'static',
    sequenceSteps: sequenceStepsFromMock(mock),
    sequenceOnEnd: mock.sequence?.on_end ?? 'loop',
    sequenceCurrentStep: mock.sequence?.current_step ?? 0,
  }
}

function mapToLines(obj: Record<string, string> | undefined, separator: string): string {
  if (!obj) return ''
  return Object.entries(obj)
    .map(([k, v]) => `${k}${separator}${v}`)
    .join('\n')
}

function linesToMap(text: string, separator: string): Record<string, string> | null {
  if (!text?.trim()) return null
  const result: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const sepIdx = trimmed.indexOf(separator)
    if (sepIdx <= 0) continue
    const key = trimmed.slice(0, sepIdx).trim()
    const value = trimmed.slice(sepIdx + separator.length).trim()
    if (key) result[key] = value
  }
  return Object.keys(result).length > 0 ? result : null
}

function getMatchPillCount(match: Mock['match']): number {
  if (!match) return 0
  let count = 0
  if (match.query) count += Object.keys(match.query).length
  if (match.headers) count += Object.keys(match.headers).length
  if (match.body && Object.keys(match.body).length > 0) count++
  return count
}

function JsonEditor({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { error, lineCount } = useMemo(() => {
    const lines = value.split('\n').length
    if (!value.trim()) return { error: null as string | null, lineCount: lines }
    try {
      JSON.parse(value)
      return { error: null, lineCount: lines }
    } catch (e) {
      return { error: (e as Error).message, lineCount: lines }
    }
  }, [value])

  return (
    <div className="json-editor">
      <textarea value={value} onChange={e => onChange(e.target.value)} spellCheck={false} />
      <div className={`json-status ${error ? 'err' : 'ok'}`}>
        {error ? (
          <>
            <Alert />
            <span>{error}</span>
          </>
        ) : (
          <>
            <Check />
            <span>Valid JSON · {lineCount} lines</span>
          </>
        )}
      </div>
    </div>
  )
}

export function MockEditorModal({
  state: initial,
  onClose,
  onSaved,
  showToast,
}: MockEditorModalProps) {
  const [method, setMethod] = useState(initial.method)
  const [path, setPath] = useState(initial.path)
  const [status, setStatus] = useState(initial.status)
  const [delay, setDelay] = useState(initial.delay)
  const [body, setBody] = useState(initial.body)
  const [matchQuery, setMatchQuery] = useState(initial.matchQuery)
  const [matchHeaders, setMatchHeaders] = useState(initial.matchHeaders)
  const [matchBody, setMatchBody] = useState(initial.matchBody)
  const [matchOpen, setMatchOpen] = useState(initial.matchOpen)
  const [responseMode, setResponseMode] = useState<ResponseMode>(initial.responseMode)
  const [sequenceSteps, setSequenceSteps] = useState<SequenceStepDraft[]>(initial.sequenceSteps)
  const [sequenceOnEnd, setSequenceOnEnd] = useState(initial.sequenceOnEnd)
  const [sequenceCurrentStep, setSequenceCurrentStep] = useState(initial.sequenceCurrentStep)
  const [editingStepIndex, setEditingStepIndex] = useState<number | null>(null)
  const confirm = useConfirm()

  useEffect(() => {
    setMethod(initial.method)
    setPath(initial.path)
    setStatus(initial.status)
    setDelay(initial.delay)
    setBody(initial.body)
    setMatchQuery(initial.matchQuery)
    setMatchHeaders(initial.matchHeaders)
    setMatchBody(initial.matchBody)
    setMatchOpen(initial.matchOpen)
    setResponseMode(initial.responseMode)
    setSequenceSteps(initial.sequenceSteps)
    setSequenceOnEnd(initial.sequenceOnEnd)
    setSequenceCurrentStep(initial.sequenceCurrentStep)
    setEditingStepIndex(null)
  }, [initial])

  const switchToSequence = useCallback(() => {
    setResponseMode('sequence')
    // Seed with a step built from the current static body so the user
    // doesn't start from empty.
    setSequenceSteps(prev => {
      if (prev.length > 0) return prev
      return [makeDefaultStep(status || 200, body || '{}')]
    })
  }, [body, status])

  const handleSave = useCallback(async () => {
    let parsedBody: unknown
    try {
      parsedBody = JSON.parse(body)
    } catch (err) {
      showToast(`Invalid JSON in response body: ${(err as Error).message}`, 'warn')
      return
    }

    const match: Record<string, unknown> = {}
    const queryMap = linesToMap(matchQuery, '=')
    const headersMap = linesToMap(matchHeaders, ':')

    if (queryMap) match.query = queryMap
    if (headersMap) match.headers = headersMap
    if (matchBody.trim()) {
      try {
        match.body = JSON.parse(matchBody)
      } catch (err) {
        showToast(`Invalid JSON in match body: ${(err as Error).message}`, 'warn')
        return
      }
    }

    const mock: Record<string, unknown> = {
      method,
      path,
      status,
      body: parsedBody,
      delay_ms: delay,
    }
    if (Object.keys(match).length > 0) mock.match = match

    // Always persist both static body AND sequence steps when the user has
    // defined any — response_mode decides which one the backend serves.
    if (sequenceSteps.length > 0) {
      const serialized: SequenceStep[] = []
      for (let i = 0; i < sequenceSteps.length; i++) {
        const s = sequenceSteps[i]
        let parsedStepBody: unknown
        try {
          parsedStepBody = s.body.trim() ? JSON.parse(s.body) : null
        } catch (err) {
          showToast(
            `Invalid JSON in step ${i + 1}: ${(err as Error).message}`,
            'warn',
          )
          return
        }
        serialized.push({
          status: s.status || 200,
          body: parsedStepBody,
          delay_ms: s.delay || 0,
        })
      }
      mock.sequence = { steps: serialized, on_end: sequenceOnEnd }
    }
    mock.response_mode = responseMode

    try {
      const result = await api.saveMock(
        mock as unknown as Omit<Mock, 'enabled'>,
        initial.editingIndex,
      )
      if (result.disabled_duplicates?.length) {
        showToast(
          `${result.disabled_duplicates.length} duplicate mock(s) auto-disabled`,
          'warn',
        )
      }
      onClose()
      onSaved()
    } catch (err) {
      showToast(`Failed to save mock: ${(err as Error).message}`, 'warn')
    }
  }, [
    method,
    path,
    status,
    delay,
    body,
    matchQuery,
    matchHeaders,
    matchBody,
    responseMode,
    sequenceSteps,
    sequenceOnEnd,
    initial.editingIndex,
    onClose,
    onSaved,
    showToast,
  ])

  const handleDelete = useCallback(async () => {
    if (initial.editingIndex === null) return
    const ok = await confirm({
      title: 'Delete mock?',
      message: (
        <>
          <code className="font-mono text-fg-0">
            {method} {path}
          </code>{' '}
          will be removed and its JSON file deleted from disk.
        </>
      ),
      confirmLabel: 'Delete',
      danger: true,
    })
    if (!ok) return
    try {
      await api.deleteMock(initial.editingIndex)
      onClose()
      onSaved()
      showToast('Mock deleted')
    } catch (err) {
      showToast(`Failed to delete mock: ${(err as Error).message}`, 'warn')
    }
  }, [initial.editingIndex, method, path, confirm, onClose, onSaved, showToast])

  const handleOverlayMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (e.target === e.currentTarget) onClose()
    },
    [onClose],
  )

  const handleResetCounter = useCallback(async () => {
    if (initial.editingIndex === null) return
    try {
      await api.resetSequence(initial.editingIndex)
      setSequenceCurrentStep(0)
      showToast('Sequence counter reset')
    } catch (err) {
      showToast(`Failed to reset counter: ${(err as Error).message}`, 'warn')
    }
  }, [initial.editingIndex, showToast])

  const addStep = useCallback(() => {
    setSequenceSteps(prev => {
      const base = prev.length > 0 ? prev[prev.length - 1] : { status: status || 200, body: '{}', delay: 0 }
      const next = [...prev, { status: base.status || 200, body: base.body || '{}', delay: 0 }]
      setEditingStepIndex(next.length - 1)
      return next
    })
  }, [status])

  const updateStep = useCallback((index: number, patch: Partial<SequenceStepDraft>) => {
    setSequenceSteps(prev => prev.map((s, i) => (i === index ? { ...s, ...patch } : s)))
  }, [])

  const removeStep = useCallback((index: number) => {
    setSequenceSteps(prev => prev.filter((_, i) => i !== index))
    setEditingStepIndex(prev => (prev === index ? null : prev !== null && prev > index ? prev - 1 : prev))
  }, [])

  const isEditing = initial.editingIndex !== null
  const nextStepForDisplay =
    sequenceSteps.length > 0
      ? ((sequenceCurrentStep % sequenceSteps.length) + sequenceSteps.length) % sequenceSteps.length
      : 0

  return (
    <div onMouseDown={handleOverlayMouseDown} className="modal-scrim">
      <div onMouseDown={e => e.stopPropagation()} className="modal">
        <div className="modal-head">
          <h2>{isEditing ? 'Edit mock' : 'Save as mock'}</h2>
          <span className="sub">
            {method} {path}
          </span>
          <div className="flex-1" />
          <button type="button" className="btn ghost icon" onClick={onClose} aria-label="Close">
            <X />
          </button>
        </div>

        <div className="modal-body">
          <div className="grid-2">
            <div className="fld">
              <label>Method</label>
              <select className="select" value={method} onChange={e => setMethod(e.target.value)}>
                {['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].map(m => (
                  <option key={m}>{m}</option>
                ))}
              </select>
            </div>
            <div className="fld">
              <label>Path</label>
              <input
                className="input"
                value={path}
                onChange={e => setPath(e.target.value)}
                placeholder="/api/v1/users"
              />
            </div>
            <div className="fld">
              <label>Status</label>
              <input
                className="input"
                type="number"
                value={status}
                onChange={e => setStatus(parseInt(e.target.value) || 200)}
              />
            </div>
            <div className="fld">
              <label>Delay (ms)</label>
              <input
                className="input"
                type="number"
                value={delay}
                onChange={e => setDelay(parseInt(e.target.value) || 0)}
              />
            </div>
          </div>

          <div className="fld">
            <label>Response mode</label>
            <div className="seg" role="tablist" aria-label="Response mode">
              <button
                type="button"
                role="tab"
                aria-selected={responseMode === 'static'}
                className={responseMode === 'static' ? 'active' : ''}
                onClick={() => setResponseMode('static')}
              >
                Static
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={responseMode === 'sequence'}
                className={responseMode === 'sequence' ? 'active' : ''}
                onClick={switchToSequence}
              >
                Sequence
              </button>
            </div>
          </div>

          {responseMode === 'static' ? (
            <div className="fld">
              <label>Response body (JSON)</label>
              <JsonEditor value={body} onChange={setBody} />
            </div>
          ) : (
            <div className="seq-editor">
              <div className="seq-editor-head">
                <div className="seg">
                  <button
                    type="button"
                    className={sequenceOnEnd === 'loop' ? 'active' : ''}
                    onClick={() => setSequenceOnEnd('loop')}
                  >
                    Loop
                  </button>
                  <button
                    type="button"
                    className={sequenceOnEnd === 'stay' ? 'active' : ''}
                    onClick={() => setSequenceOnEnd('stay')}
                  >
                    Stay on last
                  </button>
                  <button
                    type="button"
                    className={sequenceOnEnd === 'reset' ? 'active' : ''}
                    onClick={() => setSequenceOnEnd('reset')}
                  >
                    Reset
                  </button>
                </div>
                <div className="flex-1" />
                {isEditing && (
                  <button type="button" className="btn ghost" onClick={handleResetCounter}>
                    <Refresh size={14} /> Reset counter
                  </button>
                )}
              </div>

              {sequenceSteps.length > 0 && (
                <div className="seq-editor-status">
                  <span>
                    Next call returns <b>step {nextStepForDisplay + 1}</b> of {sequenceSteps.length}.
                  </span>
                </div>
              )}

              <div className="seq-timeline">
                {sequenceSteps.map((step, i) => {
                  const active = i === nextStepForDisplay
                  return (
                    <Fragment key={i}>
                      <div className={`seq-step ${active ? 'active' : ''}`}>
                        <span className="n">step {i + 1}</span>
                        <h4>
                          <span className="tag-type MOCK">{step.status}</span>
                          {active && <span className="next-tag">NEXT</span>}
                        </h4>
                        <pre className="preview">{truncatePreview(step.body)}</pre>
                        <div className="row-actions">
                          <button
                            type="button"
                            className="btn ghost"
                            style={{ flex: 1 }}
                            onClick={() => setEditingStepIndex(i)}
                          >
                            Edit
                          </button>
                          <button
                            type="button"
                            className="btn ghost"
                            aria-label={`Delete step ${i + 1}`}
                            onClick={() => removeStep(i)}
                          >
                            <Trash size={12} />
                          </button>
                        </div>
                      </div>
                      <div className="seq-arrow">
                        <Chevron size={18} />
                      </div>
                    </Fragment>
                  )
                })}
                <button type="button" className="seq-end" onClick={addStep}>
                  <Plus size={18} />
                  <div style={{ marginTop: 6 }}>Add step</div>
                </button>
              </div>

              {editingStepIndex !== null && sequenceSteps[editingStepIndex] && (
                <div className="seq-step-editor">
                  <div className="fld">
                    <label>Status</label>
                    <input
                      className="input"
                      type="number"
                      value={sequenceSteps[editingStepIndex].status}
                      onChange={e =>
                        updateStep(editingStepIndex, { status: parseInt(e.target.value) || 200 })
                      }
                    />
                  </div>
                  <div className="fld">
                    <label>Delay (ms)</label>
                    <input
                      className="input"
                      type="number"
                      value={sequenceSteps[editingStepIndex].delay}
                      onChange={e =>
                        updateStep(editingStepIndex, { delay: parseInt(e.target.value) || 0 })
                      }
                    />
                  </div>
                  <div className="fld wide">
                    <label>Body (JSON) — step {editingStepIndex + 1}</label>
                    <JsonEditor
                      value={sequenceSteps[editingStepIndex].body}
                      onChange={v => updateStep(editingStepIndex, { body: v })}
                    />
                  </div>
                  <div className="wide" style={{ display: 'flex', justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      className="btn ghost"
                      onClick={() => setEditingStepIndex(null)}
                    >
                      Close
                    </button>
                  </div>
                </div>
              )}

              <details className="details-row" open>
                <summary onClick={e => e.preventDefault()}>
                  Fallback static body
                  <span className="hint">
                    used when on-end is “reset” (served between cycles); also saved for when you switch back to Static mode
                  </span>
                </summary>
                <JsonEditor value={body} onChange={setBody} />
              </details>
            </div>
          )}

          <details className="details-row" open={matchOpen}>
            <summary onClick={e => {
              e.preventDefault()
              setMatchOpen(o => !o)
            }}>
              Match conditions
              <span className="hint">
                optional — differentiate multiple mocks for the same method + path
              </span>
            </summary>
            <div className="kv-list">
              <div>
                <div className="label">
                  Query parameters{' '}
                  <span className="text-fg-3">(one per line, key=value)</span>
                </div>
                <textarea
                  value={matchQuery}
                  onChange={e => setMatchQuery(e.target.value)}
                  spellCheck={false}
                  placeholder={'cursor=\nlimit=20'}
                />
              </div>
              <div>
                <div className="label">
                  Request headers{' '}
                  <span className="text-fg-3">(one per line, key: value)</span>
                </div>
                <textarea
                  value={matchHeaders}
                  onChange={e => setMatchHeaders(e.target.value)}
                  spellCheck={false}
                  placeholder="x-user-id: 123"
                />
              </div>
              <div>
                <div className="label">Request body (partial JSON subset)</div>
                <textarea
                  value={matchBody}
                  onChange={e => setMatchBody(e.target.value)}
                  spellCheck={false}
                  placeholder='{ "type": "credit" }'
                />
              </div>
            </div>
          </details>
        </div>

        <div className="modal-foot">
          {isEditing && (
            <button type="button" className="btn danger" onClick={handleDelete}>
              <Trash /> Delete
            </button>
          )}
          <div className="flex-1" />
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={handleSave}>
            <Check /> Save mock
          </button>
        </div>
      </div>
    </div>
  )
}
