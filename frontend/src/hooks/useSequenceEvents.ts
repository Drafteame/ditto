import { useEffect, useRef } from 'react'
import type { PlayerEvent } from '../types'

const SSE_URL = '/__ditto__/api/sequences/events'

export function useSequenceEvents(onEvent: (event: PlayerEvent) => void, onReconnect: () => void) {
  const onEventRef = useRef(onEvent)
  const onReconnectRef = useRef(onReconnect)
  onEventRef.current = onEvent
  onReconnectRef.current = onReconnect

  useEffect(() => {
    let es: EventSource | null = null
    let reconnectTimeout: ReturnType<typeof setTimeout> | null = null
    let hasConnectedBefore = false

    function connect() {
      es = new EventSource(SSE_URL)
      es.onopen = () => {
        if (hasConnectedBefore) onReconnectRef.current()
        hasConnectedBefore = true
      }
      es.onmessage = (e) => {
        const event: PlayerEvent = JSON.parse(e.data)
        onEventRef.current(event)
      }
      es.onerror = () => {
        es?.close()
        reconnectTimeout = setTimeout(connect, 3000)
      }
    }

    connect()
    return () => {
      es?.close()
      if (reconnectTimeout) clearTimeout(reconnectTimeout)
    }
  }, [])
}

