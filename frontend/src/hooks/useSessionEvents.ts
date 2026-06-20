// Thin orchestrator — composes all focused event hooks and manages session transitions.

import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { getSessionTokens } from '@/api/chat'
import { usePlanEvents } from './events/usePlanEvents'
import { useToolEvents } from './events/useToolEvents'
import { useChatEvents } from './events/useChatEvents'
import { useLifecycleEvents } from './events/useLifecycleEvents'
import { useContextEvents } from './events/useContextEvents'
import { useSubagentEvents } from './events/useSubagentEvents'
import { useActionEvents } from './events/useActionEvents'
import { useBlackboardEvents } from './events/useBlackboardEvents'
import { usePlanReviewEvents } from './events/usePlanReviewEvents'

export function useSessionEvents(sessionId: string | null): void {
  // Reset session state on session change
  useEffect(() => {
    if (!sessionId) return
    let cancelled = false

    // Clear previous session UI state (batched to avoid cascading re-renders)
    useChatStore.setState({
      streamingText: null,
      streamingSessionId: null,
      activityStatus: null,
      stepContextFill: {},
    })
    usePlanStore.setState({ planGroups: [] })

    // Load persisted session token totals
    getSessionTokens(sessionId).then((tokens) => {
      if (cancelled) return
      useChatStore.getState().setSessionTokens(sessionId, tokens)
    }).catch(() => { /* ignore — will show 0 */ })

    return () => { cancelled = true }
  }, [sessionId])

  // Compose all event hooks
  usePlanEvents(sessionId)
  useToolEvents(sessionId)
  useChatEvents(sessionId)
  useLifecycleEvents(sessionId)
  useContextEvents(sessionId)
  useSubagentEvents(sessionId)
  useActionEvents(sessionId)
  useBlackboardEvents(sessionId)
  usePlanReviewEvents(sessionId)
}
