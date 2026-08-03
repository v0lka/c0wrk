// Blackboard events: blackboard_updated (debounced RPC fetch)

import { useEffect, useRef } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { getBlackboardState } from '@/api/blackboard'
import { isBlackboardUpdatedData } from '@/types/events'
import { useBlackboardStore } from '@/stores/blackboardStore'
import { logger } from '@/lib/logger'

const DEBOUNCE_MS = 300

export function useBlackboardEvents(sessionId: string | null): void {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (!sessionId) {
      useBlackboardStore.getState().clear()
      return
    }

    // Guard against out-of-order resolution: a fetch for an old session can
    // settle after the session has already changed, which would overwrite the
    // new session's blackboard state. The cancelled flag lets the effect
    // teardown discard any in-flight response.
    let cancelled = false
    const isCancelled = (): boolean => cancelled

    // Fetch initial state on session change.
    fetchBlackboard(sessionId, isCancelled)

    const cleanup = onSessionEvent(sessionId, 'blackboard_updated', (data) => {
      if (!isBlackboardUpdatedData(data)) { reportDroppedEvent('blackboard_updated', data); return }

      // Debounce: clear any pending fetch and schedule a new one.
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current)
      }
      timerRef.current = setTimeout(() => {
        timerRef.current = null
        fetchBlackboard(sessionId, isCancelled)
      }, DEBOUNCE_MS)
    })

    return () => {
      cancelled = true
      cleanup()
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current)
        timerRef.current = null
      }
    }
  }, [sessionId])
}

function fetchBlackboard(sessionId: string, isCancelled?: () => boolean): void {
  useBlackboardStore.getState().setLoading(true)
  getBlackboardState(sessionId)
    .then((state) => {
      if (isCancelled?.()) return
      useBlackboardStore.getState().setState(state)
      useBlackboardStore.getState().setLoading(false)
    })
    .catch((err) => {
      if (isCancelled?.()) return
      logger.error('Failed to fetch blackboard state:', err)
      useBlackboardStore.getState().setError('Failed to load blackboard state')
    })
}
