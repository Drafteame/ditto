import { useEffect, useRef } from 'react'
import type { LogEvent, LogHistory, LogHistoryGap } from '../types'
import { keepCursorSession, laterCursor, recoveryCursor } from '../logStream'

const SSE_URL = '/__ditto__/events'
const MAX_PENDING = 5000

export function useSSE(
  onEvents: (events: LogEvent[]) => void,
  onConnect: () => void,
  onDisconnect: () => void,
  onReconnect: () => void,
  recover: (since: string, marker: LogEvent) => Promise<LogHistory>,
  onGap: (gap: LogHistoryGap | null, marker: LogEvent) => void,
) {
  const onEventsRef = useRef(onEvents)
  const onConnectRef = useRef(onConnect)
  const onDisconnectRef = useRef(onDisconnect)
  const onReconnectRef = useRef(onReconnect)
  const recoverRef = useRef(recover)
  const onGapRef = useRef(onGap)

  onEventsRef.current = onEvents
  onConnectRef.current = onConnect
  onDisconnectRef.current = onDisconnect
  onReconnectRef.current = onReconnect
  recoverRef.current = recover
  onGapRef.current = onGap

  useEffect(() => {
    let es: EventSource | null = null
    let reconnectTimeout: ReturnType<typeof setTimeout> | null = null
    let flushTimeout: ReturnType<typeof setTimeout> | null = null
    let hasConnectedBefore = false
    let alive = true
    let recoveryInFlight = false
    let queuedGap: LogEvent | null = null
    let lastAppliedCursor = ''
    let pending: LogEvent[] = []

    function flushPending() {
      if (flushTimeout) clearTimeout(flushTimeout)
      flushTimeout = null
      if (recoveryInFlight) return
      if (!pending.length) return
      const batch = pending
      pending = []
      onEventsRef.current(batch)
      for (const event of batch) {
        if (event.cursor) lastAppliedCursor = lastAppliedCursor ? laterCursor(lastAppliedCursor, event.cursor) : event.cursor
      }
    }

    function scheduleFlush() {
      if (flushTimeout) return
      flushTimeout = setTimeout(flushPending, 50)
    }

    async function handleGap(marker: LogEvent, chained = false) {
      if (recoveryInFlight && !chained) {
        queuedGap = marker
        return
      }
      if (!chained) {
        flushPending()
        recoveryInFlight = true
      }
      const requestedFrom = lastAppliedCursor
      let recovered = false
      try {
        const history = await recoverRef.current(requestedFrom, marker)
        if (!alive) return
        const gap = history.gap ?? (marker.gap_reason ? { reason: marker.gap_reason, from_cursor: marker.gap_from_cursor, to_cursor: marker.gap_to_cursor } : undefined)
        if (gap?.reason === 'server_reset') {
          lastAppliedCursor = ''
          pending = keepCursorSession(pending, history.latest_cursor)
          onGapRef.current(gap, marker)
        }
        if (history.events.length) onEventsRef.current(history.events)
        lastAppliedCursor = recoveryCursor(lastAppliedCursor, history)
        if (gap?.reason !== 'server_reset') onGapRef.current(gap ?? null, marker)
        recovered = true
      } catch {
        if (!alive) return
        onGapRef.current({ reason: 'recovery_failed', from_cursor: requestedFrom }, marker)
        pending = []
        if (flushTimeout) clearTimeout(flushTimeout)
        flushTimeout = null
        es?.close()
        if (reconnectTimeout) clearTimeout(reconnectTimeout)
        reconnectTimeout = setTimeout(connect, 1000)
      } finally {
        if (alive && queuedGap) {
          const next = queuedGap
          queuedGap = null
          void handleGap(next, true)
        } else {
          recoveryInFlight = false
          if (alive && recovered) flushPending()
        }
      }
    }

    function connect() {
      const url = lastAppliedCursor ? `${SSE_URL}?since=${encodeURIComponent(lastAppliedCursor)}` : SSE_URL
      es = new EventSource(url)
      es.onopen = () => {
        onConnectRef.current()
        if (hasConnectedBefore) onReconnectRef.current()
        hasConnectedBefore = true
      }
      es.onmessage = (e) => {
        const event: LogEvent = JSON.parse(e.data)
        if (event.stream_gap || event.type === 'GAP') {
          void handleGap(event)
          return
        }
        if (pending.length >= MAX_PENDING) {
          if (recoveryInFlight) {
            pending = []
            queuedGap = { type: 'GAP', method: 'GAP', id: 'pending-overflow', timestamp: new Date().toISOString(), path: '', status: 0, duration_ms: 0, stream_gap: true, gap_reason: 'pending_overflow' }
          } else flushPending()
        }
        pending.push(event)
        scheduleFlush()
      }
      es.onerror = () => {
        if (!recoveryInFlight) flushPending()
        onDisconnectRef.current()
        es?.close()
        reconnectTimeout = setTimeout(connect, 3000)
      }
    }

    const onVisibility = () => { if (document.visibilityState === 'hidden') flushPending() }
    document.addEventListener('visibilitychange', onVisibility)
    connect()
    return () => {
      alive = false
      es?.close()
      if (reconnectTimeout) clearTimeout(reconnectTimeout)
      if (flushTimeout) clearTimeout(flushTimeout)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [])
}
