// Terminal events: terminal_output, terminal_exited

import { useEffect, useRef } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isTerminalOutputData } from '@/types/events'

interface UseTerminalEventsOptions {
  sessionId: string
  onOutput: (bytes: Uint8Array) => void
  /** Fired when the session's shell exits on its own (backend
   * `terminal_exited` event, no payload). The terminal instance marks itself
   * dead and resurrects the shell lazily on next activation. */
  onExited?: () => void
}

/**
 * Subscribes to terminal events for the given session:
 * - `terminal_output`: validates the payload at the event boundary and
 *   base64-decodes the raw PTY bytes (Go encodes them to preserve
 *   invalid-UTF-8 byte sequences across JSON serialization).
 * - `terminal_exited`: no payload, signals natural shell termination.
 * Returns an unsubscribe function.
 */
export function useTerminalEvents({ sessionId, onOutput, onExited }: UseTerminalEventsOptions): void {
  const onOutputRef = useRef(onOutput)
  onOutputRef.current = onOutput
  const onExitedRef = useRef(onExited)
  onExitedRef.current = onExited

  useEffect(() => {
    if (!sessionId) return
    const unsubOutput = onSessionEvent(sessionId, 'terminal_output', (data) => {
      if (!data) return
      if (!isTerminalOutputData(data)) { reportDroppedEvent('terminal_output', data); return }
      const decoded = atob(data.data)
      const bytes = new Uint8Array(decoded.length)
      for (let i = 0; i < decoded.length; i++) {
        bytes[i] = decoded.charCodeAt(i)
      }
      onOutputRef.current(bytes)
    })
    const unsubExit = onSessionEvent(sessionId, 'terminal_exited', () => {
      onExitedRef.current?.()
    })
    return () => {
      unsubOutput()
      unsubExit()
    }
  }, [sessionId])
}
