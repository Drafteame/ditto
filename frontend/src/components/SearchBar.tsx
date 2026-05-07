import type { RefObject } from 'react'
import { ChevronDown, ChevronUp, Search, X } from './icons'

interface SearchBarProps {
  query: string
  setQuery: (q: string) => void
  matches: number[]
  idx: number
  go: (dir: 1 | -1) => void
  handleClear: () => void
  searchInputRef: RefObject<HTMLInputElement>
  noMatches: boolean
  /** Buttons rendered after the separator (copy, format, etc.) */
  children?: React.ReactNode
}

export function SearchBar({
  query,
  setQuery,
  matches,
  idx,
  go,
  handleClear,
  searchInputRef,
  noMatches,
  children,
}: SearchBarProps) {
  return (
    <div className="search-bar">
      <Search size={12} />
      <input
        ref={searchInputRef}
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
      {children && (
        <>
          <div className="search-sep" />
          {children}
        </>
      )}
    </div>
  )
}
