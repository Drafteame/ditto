import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

/**
 * Manages text-search state for a block of text.
 * Navigation side-effects (scrolling, selection) are handled by the consumer
 * via useEffect on idx/matches, keeping the hook free of DOM concerns.
 */
export function useSearch(text: string) {
  const [query, setQuery] = useState('')
  const [idx, setIdx] = useState(0)
  const searchInputRef = useRef<HTMLInputElement>(null)

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

  useEffect(() => {
    setIdx(0)
  }, [query, text])

  const go = useCallback(
    (dir: 1 | -1) => {
      if (matches.length === 0) return
      setIdx(i => (i + dir + matches.length) % matches.length)
    },
    [matches.length],
  )

  const handleClear = useCallback(() => {
    setQuery('')
    searchInputRef.current?.blur()
  }, [])

  return {
    query,
    setQuery,
    idx,
    matches,
    go,
    handleClear,
    searchInputRef,
    noMatches: query.trim() !== '' && matches.length === 0,
  }
}
