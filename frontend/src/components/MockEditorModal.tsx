import { useState, useEffect, useCallback } from 'react'
import type { Mock } from '../types'
import * as api from '../api'
import { JsonEditor } from './JsonSearch'

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
  return Object.entries(obj).map(([k, v]) => `${k}${separator}${v}`).join('\n')
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

export function MockEditorModal({ state: initial, onClose, onSaved, showToast }: MockEditorModalProps) {
  const [method, setMethod] = useState(initial.method)
  const [path, setPath] = useState(initial.path)
  const [status, setStatus] = useState(initial.status)
  const [delay, setDelay] = useState(initial.delay)
  const [body, setBody] = useState(initial.body)
  const [matchQuery, setMatchQuery] = useState(initial.matchQuery)
  const [matchHeaders, setMatchHeaders] = useState(initial.matchHeaders)
  const [matchBody, setMatchBody] = useState(initial.matchBody)
  const [matchOpen, setMatchOpen] = useState(initial.matchOpen)

  // Reset when initial state changes
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
      alert('Invalid JSON in response body: ' + (err as Error).message)
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
        alert('Invalid JSON in match body: ' + (err as Error).message)
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
        showToast(`${result.disabled_duplicates.length} duplicate mock(s) auto-disabled`, 'warn')
      }
      onClose()
      onSaved()
    } catch (err) {
      alert('Failed to save mock: ' + (err as Error).message)
    }
  }, [method, path, status, delay, body, matchQuery, matchHeaders, matchBody, initial.editingIndex, onClose, onSaved, showToast])

  const handleOverlayClick = useCallback((e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onClose()
  }, [onClose])

  const isEditing = initial.editingIndex !== null

  return (
    <div
      onClick={handleOverlayClick}
      className="fixed inset-0 bg-black/60 flex items-center justify-center z-[100]"
    >
      <div
        onClick={e => e.stopPropagation()}
        className="bg-dt-surface border border-dt-border rounded-xl w-[700px] max-w-[90vw] max-h-[85vh] flex flex-col max-md:w-full max-md:max-w-[100vw] max-md:max-h-[100vh] max-md:h-screen max-md:rounded-none"
      >
        {/* Header */}
        <div className="flex justify-between items-center px-5 py-4 border-b border-dt-border">
          <h3 className="text-base font-semibold">{isEditing ? 'Edit Mock' : 'Save as Mock'}</h3>
          <button
            onClick={onClose}
            className="bg-transparent border-none text-dt-muted text-[22px] cursor-pointer px-1 hover:text-dt-text"
          >
            &times;
          </button>
        </div>

        {/* Body */}
        <div className="px-5 py-5 overflow-y-auto flex-1">
          {/* Top row: Method, Path, Status, Delay */}
          <div className="flex gap-4 mb-5 max-md:flex-col max-md:gap-3">
            <div className="flex flex-col gap-2 w-[120px] max-md:w-full">
              <label className="form-label">Method</label>
              <select value={method} onChange={e => setMethod(e.target.value)} className="form-input">
                {['GET', 'POST', 'PUT', 'DELETE', 'PATCH'].map(m => (
                  <option key={m}>{m}</option>
                ))}
              </select>
            </div>
            <div className="flex flex-col gap-2 flex-1 max-md:w-full">
              <label className="form-label">Path</label>
              <input
                type="text"
                value={path}
                onChange={e => setPath(e.target.value)}
                placeholder="/api/v1/users"
                className="form-input"
              />
            </div>
            <div className="flex flex-col gap-2 w-20 max-md:w-full">
              <label className="form-label">Status</label>
              <input
                type="number"
                value={status}
                onChange={e => setStatus(parseInt(e.target.value) || 200)}
                className="form-input"
              />
            </div>
            <div className="flex flex-col gap-2 w-[100px] max-md:w-full">
              <label className="form-label">Delay (ms)</label>
              <input
                type="number"
                value={delay}
                onChange={e => setDelay(parseInt(e.target.value) || 0)}
                className="form-input"
              />
            </div>
          </div>

          {/* Response body */}
          <div className="flex flex-col gap-2">
            <label className="form-label">Response Body (JSON)</label>
            <JsonEditor
              value={body}
              onChange={setBody}
              minRows={12}
            />
          </div>

          {/* Match conditions */}
          <div className="mt-4 border-t border-dt-border pt-4">
            <button
              onClick={() => setMatchOpen(!matchOpen)}
              className="text-xs text-dt-muted uppercase tracking-wider cursor-pointer select-none bg-transparent border-none p-1 hover:text-dt-text"
            >
              {matchOpen ? '▼' : '▶'} Match conditions (optional)
            </button>
            {matchOpen && (
              <div className="mt-2">
                <p className="text-xs text-dt-muted mb-3 leading-relaxed">
                  Use these to differentiate multiple mocks for the same method + path.
                  A request must satisfy every defined condition to match. Leave empty to match any request.
                </p>

                <div className="flex flex-col gap-2 mt-3">
                  <label className="form-label">
                    Query parameters (one per line, <code className="bg-dt-bg px-1 py-px rounded text-[11px]">key=value</code>)
                  </label>
                  <textarea
                    value={matchQuery}
                    onChange={e => setMatchQuery(e.target.value)}
                    rows={3}
                    spellCheck={false}
                    placeholder={'status=pending\nlimit=20'}
                    className="form-input min-h-[60px] text-xs"
                  />
                </div>

                <div className="flex flex-col gap-2 mt-3">
                  <label className="form-label">
                    Request headers (one per line, <code className="bg-dt-bg px-1 py-px rounded text-[11px]">key: value</code>)
                  </label>
                  <textarea
                    value={matchHeaders}
                    onChange={e => setMatchHeaders(e.target.value)}
                    rows={3}
                    spellCheck={false}
                    placeholder="x-user-id: 123"
                    className="form-input min-h-[60px] text-xs"
                  />
                </div>

                <div className="flex flex-col gap-2 mt-3">
                  <label className="form-label">
                    Request body subset (JSON — partial match)
                  </label>
                  <textarea
                    value={matchBody}
                    onChange={e => setMatchBody(e.target.value)}
                    rows={4}
                    spellCheck={false}
                    placeholder='{"type": "credit"}'
                    className="form-input min-h-[60px] text-xs"
                  />
                </div>
              </div>
            )}
          </div>
        </div>

        {/* Footer */}
        <div className="px-5 py-4 border-t border-dt-border flex justify-end gap-2">
          <button onClick={onClose} className="btn bg-transparent border-dt-border">
            Cancel
          </button>
          <button onClick={handleSave} className="btn bg-dt-accent text-white border-dt-accent hover:bg-[#4c93e6]">
            Save
          </button>
        </div>
      </div>
    </div>
  )
}
