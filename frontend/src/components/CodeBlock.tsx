import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown, ChevronUp, Copy, Search, X } from './icons'

export function CodeBlock({ text }: { text: string }) {
  const [query, setQuery] = useState('')
  const [idx, setIdx] = useState(0)
  const [copied, setCopied] = useState(false)
  const matchRefs = useRef<(HTMLSpanElement | null)[]>([])
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)

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

  const handleClear = useCallback(() => {
    setQuery('')
    inputRef.current?.blur()
  }, [])

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
            ref={inputRef}
            type="text"
            className="search-input"
            placeholder="Find…"
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') go(e.shiftKey ? -1 : 1)
              if (e.key === 'Escape') handleClear()
            }}
          />
          <button
            type="button"
            className="btn ghost icon"
            style={{ width: 22, height: 22 }}
            disabled={!query}
            onClick={handleClear}
            title="Clear search"
          >
            <X size={11} />
          </button>
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
