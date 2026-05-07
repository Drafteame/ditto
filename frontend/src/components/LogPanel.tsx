import { useState, useRef, useEffect, useCallback } from 'react'
import type { LogEntry } from '../types'
import { JsonViewer } from './JsonSearch'

interface LogPanelProps {
  entries: LogEntry[]
  onSaveAsMock: (entry: LogEntry) => void
}

type FilterType = 'all' | 'mock' | 'proxy' | 'miss'

const FILTER_BUTTONS: { type: FilterType; label: string }[] = [
  { type: 'all', label: 'All' },
  { type: 'mock', label: 'Mock' },
  { type: 'proxy', label: 'Proxy' },
  { type: 'miss', label: 'Miss' },
]

const FILTER_ACTIVE_STYLES: Record<FilterType, string> = {
  all: '!text-dt-accent !border-dt-accent !bg-[rgba(88,166,255,0.1)]',
  mock: '!text-dt-green !border-dt-green !bg-[rgba(63,185,80,0.1)]',
  proxy: '!text-dt-accent !border-dt-accent !bg-[rgba(88,166,255,0.1)]',
  miss: '!text-dt-red !border-dt-red !bg-[rgba(248,81,73,0.1)]',
}

export function LogPanel({ entries, onSaveAsMock }: LogPanelProps) {
  const [search, setSearch] = useState('')
  const [activeFilter, setActiveFilter] = useState<FilterType>('all')
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set())
  const [autoScroll, setAutoScroll] = useState(true)
  const [showJump, setShowJump] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  const filteredEntries = entries.filter(entry => {
    const typeLower = entry.type.toLowerCase()
    const matchesType = activeFilter === 'all' || typeLower === activeFilter
    const searchLower = search.toLowerCase().trim()
    const matchesSearch = !searchLower || `${entry.method} ${entry.path} ${entry.type} ${entry.status}`.toLowerCase().includes(searchLower)
    return matchesType && matchesSearch
  })

  // Auto-scroll on new entries
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

  const toggleExpand = useCallback((id: string) => {
    setExpandedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  const isEmpty = entries.length === 0

  return (
    <section className="flex-1 flex flex-col overflow-hidden relative">
      {/* Header with search and filters */}
      <div className="px-4 py-2.5 border-b border-dt-border flex items-center gap-3 flex-wrap max-md:flex-col max-md:items-stretch">
        <h2 className="text-[13px] uppercase tracking-wider text-dt-muted m-0 whitespace-nowrap font-semibold">
          Request Log
        </h2>
        <div className="flex items-center gap-2 flex-1 max-md:flex-col max-md:gap-1.5">
          <div className="relative flex-1 min-w-[120px] max-w-[300px] max-md:max-w-none">
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Filter by path, method..."
              className="w-full bg-dt-bg border border-dt-border text-dt-text py-[5px] pl-2.5 pr-7 rounded-md text-xs font-mono focus:outline-none focus:border-dt-accent placeholder:text-dt-muted"
            />
            {search && (
              <button
                onClick={() => setSearch('')}
                className="absolute right-1 top-1/2 -translate-y-1/2 bg-transparent border-none text-dt-muted text-base cursor-pointer px-1 leading-none hover:text-dt-text"
              >
                &times;
              </button>
            )}
          </div>
          <div className="flex gap-1">
            {FILTER_BUTTONS.map(({ type, label }) => (
              <button
                key={type}
                onClick={() => setActiveFilter(type)}
                className={`bg-transparent border border-dt-border text-dt-muted px-2.5 py-1 rounded text-[11px] font-semibold cursor-pointer uppercase hover:text-dt-text hover:border-dt-muted ${
                  activeFilter === type ? FILTER_ACTIVE_STYLES[type] : ''
                }`}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Jump to latest button */}
      {showJump && (
        <button
          onClick={jumpToLatest}
          className="absolute bottom-[50px] left-1/2 -translate-x-1/2 bg-dt-accent text-white border-none px-4 py-2 rounded-[20px] text-xs font-semibold cursor-pointer z-10 shadow-[0_2px_8px_rgba(0,0,0,0.3)] hover:bg-[#4c93e6]"
        >
          New requests below
        </button>
      )}

      {/* Empty state */}
      {isEmpty && (
        <div className="flex items-center justify-center flex-1 text-dt-muted text-sm">
          Waiting for requests...
        </div>
      )}

      {/* Log table */}
      {!isEmpty && (
        <div ref={containerRef} onScroll={handleScroll} className="flex-1 overflow-y-auto">
          <table className="w-full border-collapse text-[13px] font-mono">
            <thead className="sticky top-0 z-[1]">
              <tr>
                <th className="log-th max-md:hidden">Time</th>
                <th className="log-th max-md:w-[60px]">Type</th>
                <th className="log-th max-md:hidden">Method</th>
                <th className="log-th">Path</th>
                <th className="log-th text-right max-md:hidden">Status</th>
                <th className="log-th text-right max-md:hidden">Duration</th>
                <th className="log-th text-right pr-5"></th>
              </tr>
            </thead>
            <tbody>
              {filteredEntries.map(entry => (
                <LogRow
                  key={entry.id}
                  entry={entry}
                  expanded={expandedIds.has(entry.id)}
                  onToggle={() => toggleExpand(entry.id)}
                  onSave={() => onSaveAsMock(entry)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

const TYPE_BADGE_STYLES: Record<string, string> = {
  mock: 'bg-[rgba(63,185,80,0.15)] text-dt-green',
  proxy: 'bg-[rgba(88,166,255,0.15)] text-dt-accent',
  miss: 'bg-[rgba(248,81,73,0.15)] text-dt-red',
}

const METHOD_COLORS: Record<string, string> = {
  get: 'text-dt-green',
  post: 'text-dt-accent',
  put: 'text-dt-orange',
  delete: 'text-dt-red',
  patch: 'text-dt-orange',
}

function LogRow({
  entry,
  expanded,
  onToggle,
  onSave,
}: {
  entry: LogEntry
  expanded: boolean
  onToggle: () => void
  onSave: () => void
}) {
  const typeLower = entry.type.toLowerCase()
  const methodLower = entry.method.toLowerCase()
  const isMiss = typeLower === 'miss'
  const isProxy = typeLower === 'proxy'

  let prettyBody = ''
  try {
    prettyBody = JSON.stringify(JSON.parse(entry.response_body || ''), null, 2)
  } catch {
    prettyBody = entry.response_body || '(no body)'
  }

  return (
    <>
      <tr
        onClick={onToggle}
        className={`border-b border-[rgba(48,54,61,0.5)] cursor-pointer transition-colors ${
          isMiss ? 'bg-[rgba(248,81,73,0.06)] hover:bg-[rgba(248,81,73,0.1)]' : 'hover:bg-[rgba(88,166,255,0.04)]'
        } ${expanded ? 'bg-[rgba(88,166,255,0.06)]' : ''}`}
      >
        <td className="px-3 py-1.5 whitespace-nowrap max-md:hidden">{entry.timestamp}</td>
        <td className="px-3 py-1.5">
          <span className={`text-[11px] font-bold px-1.5 py-0.5 rounded ${TYPE_BADGE_STYLES[typeLower] || ''}`}>
            {entry.type}
          </span>
        </td>
        <td className={`px-3 py-1.5 max-md:hidden ${METHOD_COLORS[methodLower] || ''}`}>
          {entry.method}
        </td>
        <td className="px-3 py-1.5 whitespace-nowrap overflow-hidden text-ellipsis max-w-[500px]" title={entry.path}>
          {entry.path}
        </td>
        <td className="px-3 py-1.5 text-right whitespace-nowrap max-md:hidden">{entry.status || '-'}</td>
        <td className="px-3 py-1.5 text-right whitespace-nowrap max-md:hidden">{entry.duration_ms}ms</td>
        <td className="px-3 py-1.5 text-right pr-5">
          {isProxy && (
            <button
              onClick={e => { e.stopPropagation(); onSave() }}
              className="text-dt-green bg-[rgba(63,185,80,0.1)] border border-[rgba(63,185,80,0.3)] px-3.5 py-[5px] text-xs font-semibold cursor-pointer rounded whitespace-nowrap hover:bg-[rgba(63,185,80,0.2)] hover:border-[rgba(63,185,80,0.5)]"
              title="Save as mock"
            >
              Save
            </button>
          )}
        </td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={7} className="p-0">
            <div className="px-4 py-3 bg-dt-bg border-b border-dt-border">
              <div className="flex justify-between items-center mb-2">
                <span className="text-[11px] uppercase text-dt-muted tracking-wider">Response Body</span>
              </div>
              <JsonViewer text={prettyBody} />
            </div>
          </td>
        </tr>
      )}
    </>
  )
}
