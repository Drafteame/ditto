import type { LogEvent, LogHistory } from './types'

export function laterCursor(a: string, b: string): string {
  const [aSession, aSeq] = a.split(':')
  const [bSession, bSeq] = b.split(':')
  if (aSession !== bSession) return b
  return Number(aSeq) >= Number(bSeq) ? a : b
}

export function mergeLogEvents(current: LogEvent[], incoming: LogEvent[], limit: number): LogEvent[] {
  const ids = new Set(current.map(event => event.id))
  const additions = incoming.filter(event => {
    if (event.type === 'GAP' || ids.has(event.id)) return false
    ids.add(event.id)
    return true
  })
  if (!additions.length) return current
  return [...current, ...additions]
    .sort((a, b) => sequence(a.cursor) - sequence(b.cursor))
    .slice(-limit)
}

export function recoveryCursor(previous: string, history: LogHistory): string {
  const recoveredThrough = history.has_more
    ? history.events[history.events.length - 1]?.cursor
    : history.latest_cursor
  return recoveredThrough ? (previous ? laterCursor(previous, recoveredThrough) : recoveredThrough) : previous
}

export function keepCursorSession(events: LogEvent[], cursor?: string): LogEvent[] {
  const session = cursor?.slice(0, cursor.lastIndexOf(':'))
  return session ? events.filter(event => event.cursor?.slice(0, event.cursor.lastIndexOf(':')) === session) : []
}

function sequence(cursor?: string): number {
  const value = Number(cursor?.slice(cursor.lastIndexOf(':') + 1))
  return Number.isFinite(value) ? value : 0
}
