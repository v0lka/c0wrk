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
import { useAttachmentEvents } from './events/useAttachmentEvents'
import { useReviewRestore } from './events/useReviewRestore'
import { useGoalEvents } from './events/useGoalEvents'
import { useSoundEvents } from './events/useSoundEvents'

export function useSessionEvents(sessionId: string | null): void {
  // Reset session state on session change
  useEffect(() => {
    if (!sessionId) return
    let cancelled = false

    // Clear previous session UI state (batched to avoid cascading re-renders).
    // taskActive is reset to false here so the send button doesn't render as
    // a red "stop" immediately on switch — the reconcile effect in ChatArea
    // will set it back to true if the session is genuinely still running.
    // NOTE: streamingText/activityStatus are now per-session keyed maps, so
    // they are naturally preserved across A->B->A switches and must NOT be
    // reset here — doing so would wipe another (background) session's state.
    useChatStore.setState({
      stepContextFill: {},
      taskActive: { ...useChatStore.getState().taskActive, [sessionId]: false },
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
  useAttachmentEvents(sessionId)
  useReviewRestore(sessionId)
  useGoalEvents(sessionId)
  useSoundEvents(sessionId)
}
