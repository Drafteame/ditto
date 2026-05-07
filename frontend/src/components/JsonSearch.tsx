import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

interface SearchState {
  query: string
  setQuery: (query: string) => void
  matches: number[]
  activeIndex: number
  go: (direction: 1 | -1) => void
  clear: () => void
}

function findMatches(text: string, query: string): number[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return []

  const haystack = text.toLowerCase()
  const matches: number[] = []
  let index = 0

  while ((index = haystack.indexOf(needle, index)) !== -1) {
    matches.push(index)
    index += needle.length || 1
  }

  return matches
}

function useTextSearch(text: string): SearchState {
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)

  const matches = useMemo(() => findMatches(text, query), [text, query])

  useEffect(() => {
    setActiveIndex(0)
  }, [query])

  useEffect(() => {
    setActiveIndex(current => (
      matches.length === 0 ? 0 : Math.min(current, matches.length - 1)
    ))
  }, [matches.length])

  const go = useCallback((direction: 1 | -1) => {
    setActiveIndex(current => {
      if (matches.length === 0) return 0
      return (current + direction + matches.length) % matches.length
    })
  }, [matches.length])

  const clear = useCallback(() => {
    setQuery('')
    setActiveIndex(0)
  }, [])

  return { query, setQuery, matches, activeIndex, go, clear }
}

function SearchBar({
  search,
  children,
}: {
  search: SearchState
  children?: ReactNode
}) {
  const { query, setQuery, matches, activeIndex, go, clear } = search
  const noMatches = query.trim() !== '' && matches.length === 0

  return (
    <div className="json-search-bar">
      <span className="json-search-icon" aria-hidden="true">⌕</span>
      <input
        type="text"
        value={query}
        onChange={e => setQuery(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Enter') go(e.shiftKey ? -1 : 1)
          if (e.key === 'Escape') clear()
        }}
        placeholder="Find..."
        className="json-search-input"
      />
      <button
        type="button"
        onClick={clear}
        disabled={!query}
        title="Clear search"
        className="json-search-button"
      >
        ×
      </button>
      <span className={`json-search-counter ${noMatches ? 'no-matches' : ''}`}>
        {query.trim() ? `${matches.length > 0 ? activeIndex + 1 : 0}/${matches.length}` : ''}
      </span>
      <button
        type="button"
        onClick={() => go(-1)}
        disabled={matches.length === 0}
        title="Previous match (Shift+Enter)"
        className="json-search-button"
      >
        ↑
      </button>
      <button
        type="button"
        onClick={() => go(1)}
        disabled={matches.length === 0}
        title="Next match (Enter)"
        className="json-search-button"
      >
        ↓
      </button>
      {children && (
        <>
          <span className="json-search-separator" />
          {children}
        </>
      )}
    </div>
  )
}

function renderHighlightedText(
  text: string,
  query: string,
  matches: number[],
  activeIndex: number,
  refs: React.MutableRefObject<Array<HTMLSpanElement | null>>,
) {
  refs.current = []

  const needle = query.trim()
  if (!needle || matches.length === 0) return text

  const parts: React.ReactNode[] = []
  let cursor = 0

  matches.forEach((matchStart, index) => {
    const matchEnd = matchStart + needle.length
    if (matchStart > cursor) parts.push(text.slice(cursor, matchStart))
    parts.push(
      <span
        key={`${matchStart}-${index}`}
        ref={el => { refs.current[index] = el }}
        className={index === activeIndex ? 'json-match active' : 'json-match'}
      >
        {text.slice(matchStart, matchEnd)}
      </span>,
    )
    cursor = matchEnd
  })

  if (cursor < text.length) parts.push(text.slice(cursor))
  return parts
}

function syncOverlayScroll(source: HTMLElement | null, mirror: HTMLElement | null) {
  if (!source || !mirror) return
  mirror.scrollTop = source.scrollTop
  mirror.scrollLeft = source.scrollLeft
}

function scrollMatchInEditableOverlay(
  textarea: HTMLTextAreaElement | null,
  mirror: HTMLElement | null,
  match: HTMLElement | null,
) {
  if (!textarea || !mirror || !match) return

  const targetTop = Math.max(0, match.offsetTop - textarea.clientHeight / 3)
  const targetLeft = Math.max(0, match.offsetLeft - textarea.clientWidth / 3)

  textarea.scrollTop = targetTop
  textarea.scrollLeft = targetLeft
  mirror.scrollTop = targetTop
  mirror.scrollLeft = targetLeft
}

function prettyJsonInfo(value: string): { error: string | null; lineCount: number } {
  const lineCount = value.split('\n').length
  if (!value.trim()) return { error: null, lineCount }
  try {
    JSON.parse(value)
    return { error: null, lineCount }
  } catch (err) {
    return { error: (err as Error).message, lineCount }
  }
}

export function JsonViewer({ text }: { text: string }) {
  const search = useTextSearch(text)
  const matchRefs = useRef<Array<HTMLSpanElement | null>>([])

  useEffect(() => {
    const activeMatch = matchRefs.current[search.activeIndex]
    activeMatch?.scrollIntoView({ block: 'center', inline: 'nearest' })
  }, [search.activeIndex, search.matches.length])

  const highlighted = useMemo(() => (
    renderHighlightedText(text, search.query, search.matches, search.activeIndex, matchRefs)
  ), [text, search.query, search.matches, search.activeIndex])

  return (
    <div className="json-viewer">
      <SearchBar search={search} />
      <pre className="json-viewer-pre">{highlighted}</pre>
    </div>
  )
}

export function JsonEditor({
  value,
  onChange,
  minRows = 12,
}: {
  value: string
  onChange: (value: string) => void
  minRows?: number
}) {
  const search = useTextSearch(value)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const mirrorRef = useRef<HTMLPreElement>(null)
  const matchRefs = useRef<Array<HTMLSpanElement | null>>([])
  const { error, lineCount } = useMemo(() => prettyJsonInfo(value), [value])

  useEffect(() => {
    const textarea = textareaRef.current
    const activeMatch = matchRefs.current[search.activeIndex]
    if (!textarea || !activeMatch || search.matches.length === 0) return

    const matchStart = search.matches[search.activeIndex]
    const matchEnd = matchStart + search.query.trim().length

    requestAnimationFrame(() => {
      textarea.focus({ preventScroll: true })
      textarea.setSelectionRange(matchStart, matchEnd)
      scrollMatchInEditableOverlay(textarea, mirrorRef.current, activeMatch)
    })
  }, [search.activeIndex, search.matches, search.query])

  const highlighted = useMemo(() => (
    renderHighlightedText(value, search.query, search.matches, search.activeIndex, matchRefs)
  ), [value, search.query, search.matches, search.activeIndex])

  const formatJson = useCallback(() => {
    const trimmed = value.trim()
    if (!trimmed) return

    try {
      onChange(JSON.stringify(JSON.parse(trimmed), null, 2))
      return
    } catch {
      // Try the common trailing-comma mistake before giving up.
    }

    try {
      const withoutTrailingCommas = trimmed.replace(/,(\s*[}\]])/g, '$1')
      onChange(JSON.stringify(JSON.parse(withoutTrailingCommas), null, 2))
    } catch {
      // Leave the user's text untouched if it cannot be formatted.
    }
  }, [value, onChange])

  return (
    <div className="json-editor">
      <SearchBar search={search}>
        <button
          type="button"
          onClick={formatJson}
          disabled={!value.trim()}
          title="Format JSON"
          className="json-search-button"
        >
          {'{}'}
        </button>
      </SearchBar>
      <div className="json-editor-overlay">
        <pre ref={mirrorRef} className="json-editor-pre" aria-hidden="true">
          {highlighted}
        </pre>
        <textarea
          ref={textareaRef}
          value={value}
          onChange={e => onChange(e.target.value)}
          onScroll={() => syncOverlayScroll(textareaRef.current, mirrorRef.current)}
          rows={minRows}
          spellCheck={false}
          className="json-editor-textarea"
        />
      </div>
      <div className={`json-editor-status ${error ? 'err' : 'ok'}`}>
        {error ? `Invalid JSON: ${error}` : `Valid JSON · ${lineCount} lines`}
      </div>
    </div>
  )
}
