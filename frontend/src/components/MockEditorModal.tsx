import { useState, useEffect, useCallback, useMemo } from 'react'
import type { Mock } from '../types'
import * as api from '../api'
import { Alert, Check, Trash, X } from './icons'
import { useConfirm } from './ConfirmDialog'

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
  }, [initial])

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

  const isEditing = initial.editingIndex !== null

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
            <label>Response body (JSON)</label>
            <JsonEditor value={body} onChange={setBody} />
          </div>

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
