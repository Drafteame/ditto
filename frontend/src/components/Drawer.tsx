import { useCallback, useEffect, useRef, useState } from 'react'
import type { LogEntry, ServerInfo } from '../types'
import { Alert, Bookmark, Check, Copy, Globe, X } from './icons'

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

type Tab = 'response' | 'request' | 'headers'

function StatusCell({ status }: { status: number }) {
  const cls =
    status >= 500
      ? 'status-5'
      : status >= 400
        ? 'status-4'
        : status >= 300
          ? 'status-3'
          : 'status-200'
  return <span className={`st ${cls} font-mono text-[12px]`}>{status || '-'}</span>
}

function prettyJson(raw: string | undefined): string {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

function MatchBanner({ entry, target }: { entry: LogEntry; target: string }) {
  if (entry.type === 'MOCK') {
    return (
      <div className="match-banner">
        <Check />
        <div className="flex-1 min-w-0">
          <div className="title">Served from a mock</div>
          <div className="detail">
            <code>
              {entry.method.toUpperCase()} {entry.path}
            </code>{' '}
            matched a configured mock.
          </div>
        </div>
      </div>
    )
  }
  if (entry.type === 'PROXY') {
    return (
      <div className="match-banner proxy">
        <Globe />
        <div className="flex-1 min-w-0">
          <div className="title">Forwarded to target</div>
          <div className="detail">
            No mock matched — response came from{' '}
            <code>{target || 'the configured target'}</code>
          </div>
        </div>
      </div>
    )
  }
  return (
    <div className="match-banner miss">
      <Alert />
      <div className="flex-1 min-w-0">
        <div className="title">No mock, no target</div>
        <div className="detail">
          Add a mock or configure a target URL to handle <code>{entry.path}</code>
        </div>
      </div>
    </div>
  )
}

export function Drawer({
  entry,
  serverInfo,
  width,
  onResize,
  onClose,
  onSaveAsMock,
}: DrawerProps) {
  const [tab, setTab] = useState<Tab>('response')
  const [copied, setCopied] = useState(false)
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const handleCopy = useCallback((text: string) => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      if (copyTimer.current) clearTimeout(copyTimer.current)
      copyTimer.current = setTimeout(() => setCopied(false), 1500)
    })
  }, [])

  useEffect(() => {
    setTab('response')
    setCopied(false)
  }, [entry.id])

  const handleDragStart = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault()
      const startX = e.clientX
      const startW = width
      const onMove = (ev: MouseEvent) => {
        const next = Math.max(
          DRAWER_MIN_WIDTH,
          Math.min(DRAWER_MAX_WIDTH, startW - (ev.clientX - startX)),
        )
        onResize(next)
      }
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
    },
    [width, onResize],
  )

  const method = entry.method.toUpperCase()
  const hasResponse = !!entry.response_body?.trim()
  const target = serverInfo?.target ?? ''

  return (
    <aside className="drawer" style={{ width }}>
      <div
        className="resize-handle left"
        onMouseDown={handleDragStart}
        title="Drag to resize"
      />
      <div className="drawer-head">
        <div className="row">
          <span className={`tag-type ${entry.type}`}>{entry.type}</span>
          <span className={`method ${method}`}>{method}</span>
          <StatusCell status={entry.status} />
          <div className="flex-1" />
          {entry.type === 'PROXY' && (
            <button
              type="button"
              className="btn ghost"
              style={{ height: 24, padding: '0 8px', fontSize: 11 }}
              onClick={() => onSaveAsMock(entry)}
              title="Save as mock"
            >
              <Bookmark /> Save
            </button>
          )}
          <button
            type="button"
            className="btn ghost icon"
            onClick={onClose}
            aria-label="Close drawer"
          >
            <X />
          </button>
        </div>
        <div className="drawer-path">{entry.path}</div>
        <div className="drawer-meta">
          <span className="k">Time</span>
          <span className="v">{entry.timestamp}</span>
          <span className="k">Duration</span>
          <span className="v">{entry.duration_ms}ms</span>
        </div>
      </div>

      <div style={{ padding: '12px 14px 0' }}>
        <MatchBanner entry={entry} target={target} />
      </div>

      <div className="tabs">
        <button
          type="button"
          className={tab === 'response' ? 'active' : ''}
          onClick={() => setTab('response')}
        >
          Response
        </button>
        <button
          type="button"
          className={tab === 'request' ? 'active' : ''}
          onClick={() => setTab('request')}
        >
          Request
        </button>
        <button
          type="button"
          className={tab === 'headers' ? 'active' : ''}
          onClick={() => setTab('headers')}
        >
          Headers
        </button>
      </div>

      <div className="drawer-body">
        {tab === 'response' &&
          (hasResponse ? (
            <div className="code-block">
              <pre className="code">{prettyJson(entry.response_body)}</pre>
              <button
                type="button"
                className={`copy-btn btn ghost icon${copied ? ' copied' : ''}`}
                onClick={() => handleCopy(prettyJson(entry.response_body))}
                title="Copy JSON"
              >
                {copied ? <Check size={13} /> : <Copy size={13} />}
              </button>
            </div>
          ) : (
            <div className="text-fg-3 font-sans text-[12px]">
              No response body captured for this request.
            </div>
          ))}
        {tab === 'request' && (() => {
          const requestJson = JSON.stringify(
            { method, path: entry.path, timestamp: entry.timestamp },
            null,
            2,
          )
          return (
            <div className="code-block">
              <pre className="code">{requestJson}</pre>
              <button
                type="button"
                className={`copy-btn btn ghost icon${copied ? ' copied' : ''}`}
                onClick={() => handleCopy(requestJson)}
                title="Copy JSON"
              >
                {copied ? <Check size={13} /> : <Copy size={13} />}
              </button>
            </div>
          )
        })()}
        {tab === 'headers' && (
          <div className="text-fg-3 font-sans text-[12px]">
            Request and response headers aren't captured yet. Track issue{' '}
            <code className="font-mono text-fg-1 bg-bg-code px-1 rounded-xs">
              ditto/headers
            </code>{' '}
            for updates.
          </div>
        )}
      </div>
    </aside>
  )
}
