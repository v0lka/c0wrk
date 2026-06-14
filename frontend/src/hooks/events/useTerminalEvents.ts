// Terminal output events: terminal_output

import { useEffect, useRef } from 'react'
import { onSessionEvent } from '@/api/runtime'
import { isTerminalOutputData } from '@/types/events'

interface UseTerminalEventsOptions {
  sessionId: string
  onOutput: (bytes: Uint8Array) => void
}

/**
 * Subscribes to terminal_output events for the given session.
 * Validates payload at the event boundary and base64-decodes the
 * raw PTY bytes (Go encodes them to preserve invalid-UTF-8 byte
 * sequences across JSON serialization).
 * Returns an unsubscribe function.
 */
export function useTerminalEvents({ sessionId, onOutput }: UseTerminalEventsOptions): void {
  const onOutputRef = useRef(onOutput)
  onOutputRef.current = onOutput

  useEffect(() => {
    if (!sessionId) return
    const unsub = onSessionEvent(sessionId, 'terminal_output', (data) => {
      if (!data) return
      if (!isTerminalOutputData(data)) return
      const decoded = atob(data.data)
      const bytes = new Uint8Array(decoded.length)
      for (let i = 0; i < decoded.length; i++) {
        bytes[i] = decoded.charCodeAt(i)
      }
      onOutputRef.current(bytes)
    })
    return unsub
  }, [sessionId])
}
