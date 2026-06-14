// Tool judge response events: tool_judge_response

import { useEffect, useRef } from 'react'
import { onSessionEvent } from '@/api/runtime'
import { isToolJudgeResponseData } from '@/types/events'

interface UseToolJudgeEventsCallbacks {
  onResponse: (reasoning: string | null, error: string | null) => void
}

/**
 * Subscribes to tool_judge_response events for the given session.
 * Filters by confirm_id and validates the payload with isToolJudgeResponseData
 * before calling onResponse.
 */
export function useToolJudgeEvents(
  sessionId: string | undefined,
  confirmId: string | undefined,
  callbacks: UseToolJudgeEventsCallbacks,
): void {
  const callbacksRef = useRef(callbacks)
  callbacksRef.current = callbacks

  useEffect(() => {
    if (!confirmId || !sessionId) return
    const unsub = onSessionEvent(sessionId, 'tool_judge_response', (data) => {
      if (!data) return
      if (!isToolJudgeResponseData(data)) return
      if (data.confirm_id !== confirmId) return
      callbacksRef.current.onResponse(data.reasoning ?? null, data.error ?? null)
    })
    return unsub
  }, [sessionId, confirmId])
}
