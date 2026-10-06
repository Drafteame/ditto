import { useEffect, useMemo, useRef, useState } from 'react'
import { useCopy } from '../hooks/useCopy'
import { useSearch } from '../hooks/useSearch'
import { Check, Copy } from './icons'
import { SearchBar } from './SearchBar'

export function CodeBlock({ text, prettyText }: { text: string; prettyText?: string }) {
  const [mode, setMode] = useState<'raw' | 'pretty'>(prettyText === undefined ? 'raw' : 'pretty')
  const visibleText = mode === 'pretty' && prettyText !== undefined ? prettyText : text
  const matchRefs = useRef<(HTMLSpanElement | null)[]>([])
  const search = useSearch(visibleText)
  const { copied, handleCopy } = useCopy(visibleText)

  // Scroll to active match whenever idx or matches change
  useEffect(() => {
    if (search.matches.length > 0) {
      matchRefs.current[search.idx]?.scrollIntoView({ block: 'center', inline: 'nearest' })
    }
  }, [search.idx, search.matches])

  const content = useMemo(() => {
    const q = search.query.trim()
    if (!q || search.matches.length === 0) return visibleText
    const parts: React.ReactNode[] = []
    let last = 0
    search.matches.forEach((start, i) => {
      const end = start + q.length
      if (start > last) parts.push(visibleText.slice(last, start))
      parts.push(
        <span
          key={start}
          ref={el => {
            matchRefs.current[i] = el
          }}
          className={i === search.idx ? 'match active' : 'match'}
        >
          {visibleText.slice(start, end)}
        </span>,
      )
      last = end
    })
    if (last < visibleText.length) parts.push(visibleText.slice(last))
    return parts
  }, [visibleText, search.query, search.matches, search.idx])

  return (
    <div className="code-block">
      <div className="code">
        <SearchBar {...search}>
          {prettyText !== undefined && (
            <div className="seg" style={{ marginRight: 6 }}>
              <button type="button" className={mode === 'raw' ? 'active' : ''} onClick={() => setMode('raw')}>Raw</button>
              <button type="button" className={mode === 'pretty' ? 'active' : ''} onClick={() => setMode('pretty')}>Pretty</button>
            </div>
          )}
          <button
            type="button"
            className="btn ghost icon"
            style={{ width: 22, height: 22, color: copied ? 'var(--accent)' : undefined }}
            onClick={handleCopy}
            title="Copy"
          >
            {copied ? <Check size={12} /> : <Copy size={12} />}
          </button>
        </SearchBar>
        <pre className="code-pre">{content}</pre>
      </div>
    </div>
  )
}
