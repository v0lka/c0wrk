/**
 * Reconciliation of the visual session state with the backend runtime status.
 *
 * After history load the UI would otherwise default to "idle/completed" —
 * even for a session whose task is still running or died mid-way (app crash,
 * backend panic). This module aligns the chat store with the authoritative
 * backend answers from GetSessionRuntimeStatus / GetPendingActions.
 *
 * IMPORTANT: these functions only mutate the in-memory Zustand store. They are
 * invoked by ChatArea AFTER history is merged (see loadSessionHistory), so the
 * store is populated when they run — this is what fixes the prior race where a
 * fast status/pending RPC resolved before the (slower) history RPC and thus
 * reconciled an empty store.
 *
 * Each function returns the messages it resolved as stale so the caller can
 * persist that resolution to the backend (otherwise the prompts reappear on
 * the next reload).
 */
import type { PendingActionsResponse, SessionRuntimeStatus } from '@/api/chat'
import { getSessionRuntimeStatus } from '@/api/chat'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useGoalStore } from '@/stores/goalStore'
import type { ChatMessageUI, MessageType } from '@/types/messages'
import { HITL_PROMPT_TYPES } from '@/lib/hitlTypes'

/** Message content for the synthetic resume banner injected on reload. */
const UNFINISHED_TASK_MESSAGE =
  'A previous task did not finish. You can resume it or discard it.'

/**
 * Maps a stale prompt type to the metadata field that identifies the specific
 * persisted message, so its resolution can be persisted via ResolvePendingMessage.
 * Returns null for types that cannot be persisted by id (e.g. synthetic banners).
 */
export function stalePromptMatchField(type: MessageType): string | null {
  switch (type) {
    case 'tool_confirm': return 'confirm_id'
    case 'ask_user':
    case 'step_limit':
    case 'plan_review':
    case 'goal_proposal': return 'request_id'
    case 'task_failed_resumable': return 'task_id'
    default: return null
  }
}

/**
 * Resolves stale HITL prompts (step_limit, plan_review, tool_confirm, ask_user)
 * whose owning executor no longer exists after a restart. Returns the messages
 * that were resolved. Messages already resolved or not HITL types are skipped.
 * Used by both the paused and the unfinished-task reconciliation paths so the
 * two resumable states stay consistent.
 */
function resolveStaleHitlPrompts(sessionId: string): ChatMessageUI[] {
  const store = useChatStore.getState()
  const order = store.messageOrder[sessionId] ?? []
  const index = store.messages[sessionId] ?? {}
  const resolvedStale: ChatMessageUI[] = []
  for (const id of order) {
    const msg = index[id]
    if (!msg) continue
    if (msg.metadata?.resolved === true) continue
    if (HITL_PROMPT_TYPES.has(msg.type)) {
      store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true, stale: true } })
      resolvedStale.push(msg)
    }
  }
  return resolvedStale
}

/**
 * Align chat-store state for `sessionId` with the backend runtime status.
 *
 * - `active` → restore the "task running" flag (input disabled, no false idle).
 * - not active + unfinished task persisted → ensure an unresolved
 *   `task_failed_resumable` banner exists (Resume/Cancel affordance) and
 *   resolve stale interactive prompts (step_limit, plan_review, tool_confirm,
 *   ask_user) whose owning executor is gone.
 * - not active + no unfinished task → resolve all stale prompts left over
 *   from previous runs.
 *
 * `snapshotReadAt` is the time the caller read the status (before the RPC
 * resolved). When a live event already updated the activity label or the
 * streaming text after that read, the snapshot's view of those two fields is
 * older than what the user is seeing — applying it would roll the label back
 * a phase and/or wipe an open stream mid-chunk. The activity/streaming
 * application is therefore skipped in that case; every other decision
 * (taskActive/paused/pausing, prompt resolution, resume banner) still comes
 * from the snapshot, which stays authoritative for them.
 *
 * Returns the messages that were resolved as stale (so the caller can persist
 * the resolution to the backend). Messages already resolved are skipped.
 */
export function reconcileRuntimeStatus(sessionId: string, status: SessionRuntimeStatus, snapshotReadAt?: number): ChatMessageUI[] {
  const store = useChatStore.getState()

  // Stale-snapshot guard: runtimeEventAt is stamped by the streaming/activity
  // store actions on every LIVE event-driven mutation (including no-op
  // clears), so a mark newer than the snapshot read means the UI already
  // holds fresher live state. Computed BEFORE the mirror writes below: the
  // mirrors (unfinished/compacting/no-op flags) revert live terminal state
  // exactly like the activity label does — e.g. a live compaction_finished
  // (input unlocked) must not be re-locked by a snapshot that still says
  // compacting=true.
  const hasFresherLiveState =
    snapshotReadAt !== undefined && (useChatStore.getState().runtimeEventAt[sessionId] ?? 0) > snapshotReadAt

  // Task-flag freshness uses its OWN stamp (chatStore.taskFlagsEventAt):
  // activity events (chunks) stamp runtimeEventAt without owning the flags,
  // so a background-started run still gets its flags restored from the
  // snapshot — while a live pause/resume/terminal transition (which stamps
  // the flags map) must never be reverted by an older snapshot.
  const hasFresherTaskFlags =
    snapshotReadAt !== undefined && (useChatStore.getState().taskFlagsEventAt[sessionId] ?? 0) > snapshotReadAt

  // The session list's `has_unfinished_task` is a snapshot from the last list
  // load — nothing refreshed it since, so a session whose task finished while
  // unviewed stayed "busy" for the archive/delete confirmation until an app
  // restart. The runtime snapshot is authoritative for this flag in every
  // branch below (running, paused, compacting, idle): mirror it into the
  // session store. No-op when the value already matches. Skipped when a live
  // terminal event already updated the flag after the snapshot was read.
  if (!hasFresherLiveState) {
    useSessionStore.getState().setUnfinishedTask(sessionId, status.has_unfinished_task === true)
  }

  // Manual compaction in flight: mirror the flag so a switch back to the
  // session restores the Compacting UI (locked input, cancel affordance)
  // even when the compaction_started event fired with no live listener.
  // Skipped when a live compaction event (compaction_started/finished)
  // already landed after the snapshot was read.
  if (!hasFresherLiveState) {
    store.setCompacting(sessionId, status.compacting === true)
  }

  // Manual compaction would be a no-op: mirror the backend's prediction so
  // the compact button renders disabled (with its tooltip) for a history
  // already within the compaction target. Absent field (older backend)
  // resolves to false — fail-open, the button stays clickable. Skipped when
  // a live compaction_finished already refreshed the prediction.
  if (!hasFresherLiveState) {
    store.setCompactionNoOp(sessionId, status.compaction_noop === true)
  }

  // The compaction flow owns the session: the task it paused shows neither
  // paused affordances nor a "did not finish" banner — compaction_finished
  // and the flow's auto-resume own the terminal transitions. Mirrors the
  // paused branch's stale-HITL resolution so prompts from before the flow
  // (if any) resolve identically.
  if (status.compacting) {
    if (!hasFresherTaskFlags) {
      store.setPausing(sessionId, false)
      store.setPaused(sessionId, false)
      store.setTaskActive(sessionId, status.active)
    }
    if (!hasFresherLiveState) {
      store.setActivityStatus(sessionId, 'Compacting')
      store.clearStreamingText(sessionId)
    }
    return resolveStaleHitlPrompts(sessionId)
  }

  // A cooperatively paused task is a clean checkpoint: set the paused flag and
  // taskActive=false so the UI shows the paused state (unlocked input,
  // Resume/Stop). Crucially, a paused task must NOT trigger the
  // task_failed_resumable banner below — it is resumable via the Resume button
  // or a nudge message, not a "previous task did not finish" banner.
  // ONLY when the task is no longer active: `active` wins over `paused`. A
  // snapshot can still carry paused=true after a resume that landed in the
  // background (snapshot read before resume, session_resumed emitted after)
  // — entering the paused branch then would paint the paused UI (white dot,
  // Resume button) over a live task. The active branch below clears paused
  // and restores taskActive=true instead.
  if (status.paused && !status.active) {
    if (!hasFresherTaskFlags) {
      store.setPausing(sessionId, false)
      store.setPaused(sessionId, true)
      store.setTaskActive(sessionId, false)
    }
    // A checkpointed task has no open assistant stream; any frozen streaming
    // text predates the pause and would render a phantom partial answer.
    // Skipped when a live event already updated the label/stream after the
    // snapshot was read (e.g. the pause landed live on switch-back).
    if (!hasFresherLiveState) {
      store.clearStreamingText(sessionId)
      store.setActivityStatus(sessionId, 'Paused')
    }
    // Resolve stale interactive prompts (step_limit, plan_review,
    // tool_confirm, ask_user) whose owning executor no longer exists after a
    // restart, so the two resumable states (paused vs failed-but-resumable)
    // stay consistent. Do NOT inject a task_failed_resumable banner: a paused
    // task is resumable via the Resume button or a nudge message.
    return resolveStaleHitlPrompts(sessionId)
  }
  if (!hasFresherTaskFlags) {
    store.setPaused(sessionId, false)
    store.setTaskActive(sessionId, status.active)
    // A pause-in-flight flag survives while the task is still running: the
    // backend status has no visibility into the in-memory pause window, so
    // clearing it here would unlock the input for a send the backend then
    // rejects with ErrPausePending. Once the task is no longer active the flag
    // is stale and is cleared.
    if (!status.active) {
      store.setPausing(sessionId, false)
    }
  }

  if (status.active) {
    // Replace the frozen activity label (the session advanced while the user
    // was viewing another session/project — its events had no listener) with
    // the backend-tracked phase. Falls back to the generic label when the
    // backend has no tracked event yet (e.g. between routing dispatch and the
    // first tracked emission). Skipped when live events delivered a newer
    // label/stream after the snapshot was read — the subscription mounts
    // before the status RPC resolves, so an assistant_chunk that lands in
    // that window is fresher than the snapshot's phase.
    if (!hasFresherLiveState) {
      store.setActivityStatus(sessionId, status.activity || 'Processing...')
      // A stream that ended in the background leaves frozen partial text; the
      // completed answer arrives via the history merge. While a stream IS open,
      // the frozen text stays — the next assistant_chunk carries the full
      // accumulated content and replaces it.
      if (!status.streaming) {
        store.clearStreamingText(sessionId)
      }
    }
    // A live task owns its pending prompts (step_limit, ask_user, ...);
    // leave them untouched.
    return []
  }

  // Terminal state: no task running — any lingering activity label or frozen
  // stream from before the switch is stale (the background watcher may have
  // missed the terminal event if the session was never watched). Skipped when
  // a live terminal event already cleared them after the snapshot was read.
  if (!hasFresherLiveState) {
    store.setActivityStatus(sessionId, null)
    store.clearStreamingText(sessionId)
  }

  const order = store.messageOrder[sessionId] ?? []
  const index = store.messages[sessionId] ?? {}
  const unresolved: ChatMessageUI[] = []
  for (const id of order) {
    const msg = index[id]
    if (!msg) continue
    if (msg.metadata?.resolved === true) continue
    if (HITL_PROMPT_TYPES.has(msg.type) || msg.type === 'task_failed_resumable') {
      unresolved.push(msg)
    }
  }

  if (status.has_unfinished_task) {
    // Stale interactive prompts cannot be answered after a restart — the
    // executor waiting for the response no longer exists.
    const resolvedStale = resolveStaleHitlPrompts(sessionId)
    const hasResumable = unresolved.some(m => m.type === 'task_failed_resumable')
    if (!hasResumable) {
      store.addMessage(sessionId, {
        id: `resume-runtime-${Date.now()}`,
        sessionId,
        type: 'task_failed_resumable',
        content: UNFINISHED_TASK_MESSAGE,
        metadata: { resolved: false },
        timestamp: Date.now(),
      })
    }
    return resolvedStale
  }

  // No unfinished task: any surviving prompts are stale.
  const resolvedStale: ChatMessageUI[] = []
  for (const msg of unresolved) {
    store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true, stale: true } })
    resolvedStale.push(msg)
  }
  return resolvedStale
}

/**
 * Reconcile persisted HITL messages against the live pending-action set.
 *
 * For each HITL message in the store whose id is NOT reported as pending by
 * the backend, mark it resolved (the response was already given or the
 * channel was drained on shutdown). For each pending item NOT yet in the
 * store, add it (its event was missed while the session was in the
 * background).
 *
 * Returns the messages that were resolved as stale (so the caller can persist
 * the resolution to the backend).
 */
export function reconcilePendingActions(sessionId: string, pending: PendingActionsResponse): ChatMessageUI[] {
  const store = useChatStore.getState()
  const msgs = selectSessionMessages(store, sessionId)

  // Collect the set of request/confirm IDs that are still pending.
  const pendingConfirmIds = new Set(pending.tool_confirms.map(c => c.confirm_id))
  const pendingStepLimitIds = new Set(pending.step_limits.map(s => s.request_id))
  const pendingPlanReviewIds = new Set(pending.plan_approvals.map(p => p.request_id))
  const pendingAskUserIds = new Set(pending.ask_user.map(a => a.request_id))

  const resolvedStale: ChatMessageUI[] = []

  // Reconcile existing HITL messages: mark resolved if NOT in pending.
  for (const m of msgs) {
    if (m.metadata?.resolved === true) continue
    if (m.type === 'tool_confirm') {
      const cid = m.metadata?.confirm_id as string | undefined
      if (cid && !pendingConfirmIds.has(cid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    } else if (m.type === 'step_limit') {
      const rid = m.metadata?.request_id as string | undefined
      if (rid && !pendingStepLimitIds.has(rid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    } else if (m.type === 'plan_review') {
      const rid = m.metadata?.request_id as string | undefined
      if (rid && !pendingPlanReviewIds.has(rid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    } else if (m.type === 'ask_user') {
      const rid = m.metadata?.request_id as string | undefined
      if (rid && !pendingAskUserIds.has(rid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    }
  }

  // Add messages for pending items that weren't in the store (event
  // was missed while the session was in the background).
  const existingIDs = new Set(msgs.map(m => m.id))

  for (const c of pending.tool_confirms) {
    const id = `tool-confirm-${c.confirm_id}`
    if (existingIDs.has(id)) continue
    // Link the confirmation to the tool_call that triggered it (mirroring the
    // live-event path in hitlHandlers) so that, once resolved, the decision
    // card anchors directly beneath the tool card rather than at the bottom.
    // Prefer a precise tool_call_id match; fall back to tool-name matching.
    let toolMsgId: string | undefined
    let toolPlanStepId: string | undefined
    const findTool = (predicate: (meta: Record<string, unknown>) => boolean): void => {
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i]!
        if (m.type === 'tool_call' && m.metadata && predicate(m.metadata)) {
          toolMsgId = m.id
          toolPlanStepId = m.metadata.plan_step_id as string | undefined
          return
        }
      }
    }
    if (c.tool_call_id) findTool(meta => meta.tool_call_id === c.tool_call_id)
    if (!toolMsgId) findTool(meta => meta.tool === c.tool)

    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'tool_confirm',
      content: `Confirm: ${c.tool}`,
      metadata: {
        confirm_id: c.confirm_id,
        tool: c.tool,
        args: c.args,
        reasoning: c.reasoning ?? '',
        tool_call_id: c.tool_call_id,
        disable_judge: c.disable_judge,
        tool_msg_id: toolMsgId,
        plan_step_id: toolPlanStepId,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  for (const s of pending.step_limits) {
    const id = `step-limit-${s.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'step_limit',
      content: s.reason
        ? `Circuit breaker: ${s.reason}`
        : `Step limit reached: ${s.current_step} of ${s.max_steps}`,
      metadata: {
        request_id: s.request_id,
        current_step: s.current_step,
        max_steps: s.max_steps,
        reason: s.reason ?? '',
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  for (const p of pending.plan_approvals) {
    const id = `plan-review-${p.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'plan_review',
      content: p.plan_content,
      metadata: {
        request_id: p.request_id,
        plan_path: p.plan_path,
        resolved: false,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  for (const a of pending.ask_user) {
    const id = `ask-user-${a.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'ask_user',
      content: a.questions.map(q => q.question).join('; '),
      metadata: {
        request_id: a.request_id,
        questions: a.questions,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  // Goal proposals. NOTE: goal_proposal is intentionally NOT added to the
  // resolve-stale branch above: the backend's GetPendingActions does not yet
  // report goal_proposals, so the pending set is always empty here, and
  // treating "not in pending" as stale would falsely resolve every persisted
  // proposal. The genuine stale case (restart with no running task) is handled
  // by reconcileRuntimeStatus via HITL_PROMPT_TYPES. This add-missing branch is
  // forward-compatible — it stays a no-op until the backend populates the
  // goal_proposals field. Persisted proposals reappear via history reload
  // (role goal_proposal) regardless.
  for (const g of pending.goal_proposals) {
    const id = `goal-proposal-${g.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'goal_proposal',
      content: g.condition,
      metadata: {
        request_id: g.request_id,
        condition: g.condition,
        verify: g.verify,
        verification_mode: g.verification_mode ?? '',
        resolved: false,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
    // Keep the goal store in sync for a background-delivered proposal that
    // surfaces here on session switch.
    useGoalStore.getState().setPendingProposal(sessionId, {
      request_id: g.request_id,
      session_id: sessionId,
      condition: g.condition,
      verify: g.verify,
      verification_mode: g.verification_mode,
    })
  }

  // A genuinely pending prompt means the session's agent goroutine is blocked
  // on the user RIGHT NOW (the backend reports only prompts with a live
  // waiter). Surface that in the activity label — mirrors what the live HITL
  // handlers set and overrides any pre-HITL phase the runtime status reported.
  const pendingCount = pending.tool_confirms.length + pending.step_limits.length
    + pending.plan_approvals.length + pending.ask_user.length + pending.goal_proposals.length
  if (pendingCount > 0) {
    store.setActivityStatus(sessionId, 'Waiting for your response...')
  }

  return resolvedStale
}

/**
 * Targeted refresh of the compaction no-op flag (the compact button's
 * disabled state) after an event that changed the conversation history
 * WITHOUT a session switch — a finished task appended its exchange to the
 * history, so a previously-no-op session may now be compactable again.
 * reconcileRuntimeStatus above only runs on history load (ChatArea), so
 * terminal task events call this instead.
 *
 * Deliberately narrow: fetches the runtime status and applies ONLY
 * compactionNoOp — the full reconcile is load-time logic (stale-prompt
 * resolution, resume banners) that must not run from an event handler.
 * Best-effort: on RPC failure the flag keeps its previous value until the
 * next reconcile/refetch.
 */
export function refreshCompactionNoOp(sessionId: string): void {
  getSessionRuntimeStatus(sessionId)
    .then((status) => {
      if (!status) return
      useChatStore.getState().setCompactionNoOp(sessionId, status.compaction_noop === true)
    })
    .catch(() => { /* best-effort — see doc comment */ })
}
