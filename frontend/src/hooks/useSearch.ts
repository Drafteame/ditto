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

  // Reset to first match when the query changes, but NOT when text changes
  // (editing the textarea while searching shouldn't reset or trigger navigation)
  useEffect(() => {
    setIdx(0)
  }, [query])

  // Clamp idx so it stays valid when matches shrink (e.g. text was edited)
  useEffect(() => {
    setIdx(prev => (prev < matches.length ? prev : Math.max(0, matches.length - 1)))
  }, [matches.length])

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
