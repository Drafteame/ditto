import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { LogEntry, ServerInfo } from '../types'
import {
  Alert,
  Bookmark,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  Globe,
  Search,
  X,
} from './icons'

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

function CodeBlock({ text }: { text: string }) {
  const [query, setQuery] = useState('')
  const [idx, setIdx] = useState(0)
  const [copied, setCopied] = useState(false)
  const matchRefs = useRef<(HTMLSpanElement | null)[]>([])
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const matches = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return []
    const lower = text.toLowerCase()
    const result: number[] = []
    let i = 0
    while ((i = lower.indexOf(q, i)) !== -1) {
      result.push(i)
      i += q.length
    }
    return result
  }, [query, text])

  // Reset everything when the text changes (new entry or tab)
  useEffect(() => {
    setQuery('')
    setIdx(0)
    setCopied(false)
  }, [text])

  // Reset index when query changes
  useEffect(() => {
    setIdx(0)
  }, [query])

  // Scroll to active match
  useEffect(() => {
    if (matches.length > 0) {
      matchRefs.current[idx]?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
    }
  }, [idx, matches])

  const go = useCallback(
    (dir: 1 | -1) => {
      if (matches.length === 0) return
      setIdx(i => (i + dir + matches.length) % matches.length)
    },
    [matches.length],
  )

  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      if (copyTimer.current) clearTimeout(copyTimer.current)
      copyTimer.current = setTimeout(() => setCopied(false), 1500)
    })
  }, [text])

  const content = useMemo(() => {
    const q = query.trim()
    if (!q || matches.length === 0) return text
    const parts: React.ReactNode[] = []
    let last = 0
    matches.forEach((start, i) => {
      const end = start + q.length
      if (start > last) parts.push(text.slice(last, start))
      parts.push(
        <span
          key={start}
          ref={el => {
            matchRefs.current[i] = el
          }}
          className={i === idx ? 'match active' : 'match'}
        >
          {text.slice(start, end)}
        </span>,
      )
      last = end
    })
    if (last < text.length) parts.push(text.slice(last))
    return parts
  }, [text, query, matches, idx])

  const noMatches = query.trim() !== '' && matches.length === 0

  return (
    <div className="code-block">
      <div className="code">
        <div className="search-bar">
          <Search size={12} />
          <input
            type="text"
            className="search-input"
            placeholder="Find…"
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') go(e.shiftKey ? -1 : 1)
              if (e.key === 'Escape') setQuery('')
            }}
          />
          <span className={`search-counter${noMatches ? ' no-matches' : ''}`}>
            {query.trim() ? `${matches.length > 0 ? idx + 1 : 0}/${matches.length}` : ''}
          </span>
          <button
            type="button"
            className="btn ghost icon"
            style={{ width: 22, height: 22 }}
            disabled={matches.length === 0}
            onClick={() => go(-1)}
            title="Previous match (Shift+Enter)"
          >
            <ChevronUp size={12} />
          </button>
          <button
            type="button"
            className="btn ghost icon"
            style={{ width: 22, height: 22 }}
            disabled={matches.length === 0}
            onClick={() => go(1)}
            title="Next match (Enter)"
          >
            <ChevronDown size={12} />
          </button>
          <div className="search-sep" />
          <button
            type="button"
            className="btn ghost icon"
            style={{ width: 22, height: 22, color: copied ? 'var(--accent)' : undefined }}
            onClick={handleCopy}
            title="Copy JSON"
          >
            {copied ? <Check size={12} /> : <Copy size={12} />}
          </button>
        </div>
        <pre className="code-pre">{content}</pre>
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

  useEffect(() => {
    setTab('response')
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
            <CodeBlock text={prettyJson(entry.response_body)} />
          ) : (
            <div className="text-fg-3 font-sans text-[12px]">
              No response body captured for this request.
            </div>
          ))}
        {tab === 'request' && (
          <CodeBlock
            text={JSON.stringify(
              { method, path: entry.path, timestamp: entry.timestamp },
              null,
              2,
            )}
          />
        )}
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
