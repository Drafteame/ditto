import { useEffect, useMemo, useRef } from 'react'
import { useCopy } from '../hooks/useCopy'
import { useSearch } from '../hooks/useSearch'
import { Check, Copy } from './icons'
import { SearchBar } from './SearchBar'

export function CodeBlock({ text }: { text: string }) {
  const matchRefs = useRef<(HTMLSpanElement | null)[]>([])
  const search = useSearch(text)
  const { copied, handleCopy } = useCopy(text)

  // Scroll to active match whenever idx or matches change
  useEffect(() => {
    if (search.matches.length > 0) {
      matchRefs.current[search.idx]?.scrollIntoView({ block: 'start', behavior: 'smooth' })
    }
  }, [search.idx, search.matches])

  const content = useMemo(() => {
    const q = search.query.trim()
    if (!q || search.matches.length === 0) return text
    const parts: React.ReactNode[] = []
    let last = 0
    search.matches.forEach((start, i) => {
      const end = start + q.length
      if (start > last) parts.push(text.slice(last, start))
      parts.push(
        <span
          key={start}
          ref={el => {
            matchRefs.current[i] = el
          }}
          className={i === search.idx ? 'match active' : 'match'}
        >
          {text.slice(start, end)}
        </span>,
      )
      last = end
    })
    if (last < text.length) parts.push(text.slice(last))
    return parts
  }, [text, search.query, search.matches, search.idx])

  return (
    <div className="code-block">
      <div className="code">
        <SearchBar {...search}>
          <button
            type="button"
            className="btn ghost icon"
            style={{ width: 22, height: 22, color: copied ? 'var(--accent)' : undefined }}
            onClick={handleCopy}
            title="Copy JSON"
          >
            {copied ? <Check size={12} /> : <Copy size={12} />}
          </button>
        </SearchBar>
        <pre className="code-pre">{content}</pre>
      </div>
    </div>
  )
}
