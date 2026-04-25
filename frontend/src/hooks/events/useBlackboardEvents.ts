// Blackboard events: blackboard_updated (debounced RPC fetch)

import { useEffect, useRef } from 'react'
import { onSessionEvent } from '@/api/runtime'
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

    // Fetch initial state on session change.
    fetchBlackboard(sessionId)

    const cleanup = onSessionEvent(sessionId, 'blackboard_updated', (data) => {
      if (!isBlackboardUpdatedData(data)) return

      // Debounce: clear any pending fetch and schedule a new one.
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current)
      }
      timerRef.current = setTimeout(() => {
        timerRef.current = null
        fetchBlackboard(sessionId)
      }, DEBOUNCE_MS)
    })

    return () => {
      cleanup()
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current)
        timerRef.current = null
      }
    }
  }, [sessionId])
}

function fetchBlackboard(sessionId: string): void {
  useBlackboardStore.getState().setLoading(true)
  getBlackboardState(sessionId)
    .then((state) => {
      useBlackboardStore.getState().setState(state)
      useBlackboardStore.getState().setLoading(false)
    })
    .catch((err) => {
      logger.error('Failed to fetch blackboard state:', err)
      useBlackboardStore.getState().setError('Failed to load blackboard state')
    })
}
