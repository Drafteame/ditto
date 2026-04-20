import { useState, useRef, useEffect, useCallback, useMemo } from 'react'
import type { LogEntry } from '../types'
import { Bookmark, Clock } from './icons'

interface LogPanelProps {
  entries: LogEntry[]
  selectedId: string | null
  onSelect: (id: string | null) => void
  onSaveAsMock: (entry: LogEntry) => void
}

type FilterType = 'ALL' | 'MOCK' | 'PROXY' | 'MISS'

const FILTERS: FilterType[] = ['ALL', 'MOCK', 'PROXY', 'MISS']

export function LogPanel({ entries, selectedId, onSelect, onSaveAsMock }: LogPanelProps) {
  const [search, setSearch] = useState('')
  const [activeFilter, setActiveFilter] = useState<FilterType>('ALL')
  const [autoScroll, setAutoScroll] = useState(true)
  const [showJump, setShowJump] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  const counts = useMemo(() => {
    const c = { ALL: entries.length, MOCK: 0, PROXY: 0, MISS: 0 } as Record<FilterType, number>
    entries.forEach(e => {
      c[e.type as FilterType] = (c[e.type as FilterType] || 0) + 1
    })
    return c
  }, [entries])

  const filteredEntries = entries.filter(entry => {
    if (activeFilter !== 'ALL' && entry.type !== activeFilter) return false
    const searchLower = search.toLowerCase().trim()
    if (!searchLower) return true
    return `${entry.method} ${entry.path} ${entry.type} ${entry.status}`
      .toLowerCase()
      .includes(searchLower)
  })

  useEffect(() => {
    if (autoScroll && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight
    } else if (!autoScroll && entries.length > 0) {
      setShowJump(true)
    }
  }, [entries.length, autoScroll])

  const handleScroll = useCallback(() => {
    const el = containerRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
    setAutoScroll(atBottom)
    if (atBottom) setShowJump(false)
  }, [])

  const jumpToLatest = useCallback(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight
    }
    setAutoScroll(true)
    setShowJump(false)
  }, [])

  const handleRowClick = useCallback(
    (id: string) => {
      onSelect(selectedId === id ? null : id)
    },
    [onSelect, selectedId],
  )

  const isEmpty = entries.length === 0

  return (
    <section className="flex-1 flex flex-col min-w-0 bg-bg-0 relative overflow-hidden">
      <div className="log-head">
        <span className="log-title">Request log</span>
        <div className="relative flex-1 max-w-[360px]">
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Filter by path, method…"
            className="filter-input w-full"
          />
          {search && (
            <button
              type="button"
              onClick={() => setSearch('')}
              className="absolute right-2 top-1/2 -translate-y-1/2 bg-transparent border-0 text-fg-3 text-sm cursor-pointer px-1 leading-none hover:text-fg-0"
              aria-label="Clear search"
            >
              ×
            </button>
          )}
        </div>
        <div className="seg seg-type">
          {FILTERS.map(t => (
            <button
              key={t}
              type="button"
              className={`${activeFilter === t ? 'active' : ''} t-${t}`}
              onClick={() => setActiveFilter(t)}
            >
              {t}
              <span className="c">{counts[t] || 0}</span>
            </button>
          ))}
        </div>
        <div className="flex-1" />
      </div>

      {showJump && (
        <button type="button" onClick={jumpToLatest} className="jump-btn">
          New requests below
        </button>
      )}

      {isEmpty ? (
        <div className="empty">
          <div className="glyph">
            <Clock />
          </div>
          <h3>Waiting for requests…</h3>
          <p>
            Point your app at Ditto and requests will stream in here in real time. Requests matching
            a mock are returned instantly; everything else is forwarded to your target.
          </p>
        </div>
      ) : (
        <div ref={containerRef} onScroll={handleScroll} className="log-table">
          <div className="log-row-head">
            <span>Time</span>
            <span>Type</span>
            <span>Method</span>
            <span>Path</span>
            <span className="text-right">Status</span>
            <span className="text-right">Duration</span>
            <span />
          </div>
          {filteredEntries.map(entry => (
            <LogRow
              key={entry.id}
              entry={entry}
              selected={selectedId === entry.id}
              onClick={() => handleRowClick(entry.id)}
              onSave={() => onSaveAsMock(entry)}
            />
          ))}
          {filteredEntries.length === 0 && (
            <div className="px-4 py-6 text-center text-[12px] text-fg-3 font-sans">
              No requests match the current filters.
            </div>
          )}
        </div>
      )}
    </section>
  )
}

function StatusCell({ status }: { status: number }) {
  const cls =
    status >= 500
      ? 'status-5'
      : status >= 400
        ? 'status-4'
        : status >= 300
          ? 'status-3'
          : 'status-200'
  return <span className={`st ${cls}`}>{status || '-'}</span>
}

function LogRow({
  entry,
  selected,
  onClick,
  onSave,
}: {
  entry: LogEntry
  selected: boolean
  onClick: () => void
  onSave: () => void
}) {
  const methodUpper = entry.method.toUpperCase()
  const isProxy = entry.type === 'PROXY'

  return (
    <div onClick={onClick} className={`log-row ${selected ? 'selected' : ''}`}>
      <span className="t">{entry.timestamp}</span>
      <span>
        <span className={`tag-type ${entry.type}`}>{entry.type}</span>
      </span>
      <span>
        <span className={`method ${methodUpper}`}>{methodUpper}</span>
      </span>
      <span className="p" title={entry.path}>
        {entry.path}
      </span>
      <StatusCell status={entry.status} />
      <span className="dur">{entry.duration_ms}ms</span>
      <span className="flex justify-end">
        {isProxy && (
          <button
            type="button"
            onClick={e => {
              e.stopPropagation()
              onSave()
            }}
            className="btn ghost"
            style={{ height: 22, padding: '0 8px', fontSize: 11 }}
            title="Save as mock"
          >
            <Bookmark /> Save
          </button>
        )}
      </span>
    </div>
  )
}
