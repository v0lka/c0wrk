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
import { useWorkUnitEvents } from './events/useWorkUnitEvents'
import { useActionEvents } from './events/useActionEvents'
import { useBlackboardEvents } from './events/useBlackboardEvents'
import { useAttachmentEvents } from './events/useAttachmentEvents'
import { useReviewRestore } from './events/useReviewRestore'
import { useGoalEvents } from './events/useGoalEvents'
import { useE2SStateEvents } from './events/useE2SStateEvents'
import { useSoundEvents } from './events/useSoundEvents'

export function useSessionEvents(sessionId: string | null): void {
  // Reset session state on session change
  useEffect(() => {
    if (!sessionId) return
    let cancelled = false

    // Clear previous session UI state (batched to avoid cascading re-renders).
    //
    // NOTE: taskActive is deliberately NOT reset here. The old blind
    // `taskActive[sessionId] = false` corrupted the live map on every
    // switch-TO: the flag is only restored by the async fast-restore RPC
    // (useTaskFlagRestore), so a user toggling away before it landed left a
    // genuinely-running background session flagged idle — useBackgroundSessionWatcher
    // then dropped it from its watched set and the session's completion/HITL
    // events had no listener (no sound cue, no pending-action card) until an
    // unrelated live-set change happened to re-trigger the snapshot refresh.
    // The authoritative corrector in BOTH directions (stale-true from an
    // unobserved completion, or true for a still-running task) is
    // useTaskFlagRestore's `status.active` write, which is guarded against
    // reverting fresher live flag transitions (taskFlagsEventAt).
    //
    // NOTE: streamingText/activityStatus/stepContextFill are per-session keyed
    // maps, so they are naturally preserved across A->B->A switches and must
    // NOT be reset here — doing so would wipe another (background) session's
    // state or the same session's fills the user returns to. The runtime
    // reconcile (reconcileRuntimeStatus) refreshes or clears the activity
    // label and streaming text from the backend snapshot on every switch.
    usePlanStore.setState({ planGroups: [] })

    // Load persisted session token totals. On failure the entry keeps its
    // previous live values (event-driven updates) — or stays absent for a
    // never-viewed session, in which case the status bar renders no context
    // badge until the first session_tokens/context_fill event arrives.
    getSessionTokens(sessionId).then((tokens) => {
      if (cancelled) return
      useChatStore.getState().setSessionTokens(sessionId, tokens)
    }).catch(() => { /* ignore — see comment above */ })

    return () => { cancelled = true }
  }, [sessionId])

  // Compose all event hooks
  usePlanEvents(sessionId)
  useToolEvents(sessionId)
  useChatEvents(sessionId)
  useLifecycleEvents(sessionId)
  useContextEvents(sessionId)
  useSubagentEvents(sessionId)
  useWorkUnitEvents(sessionId)
  useActionEvents(sessionId)
  useBlackboardEvents(sessionId)
  useAttachmentEvents(sessionId)
  useReviewRestore(sessionId)
  useGoalEvents(sessionId)
  useE2SStateEvents(sessionId)
  useSoundEvents(sessionId)
}
