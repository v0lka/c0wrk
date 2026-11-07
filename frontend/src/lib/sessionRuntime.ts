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
import type { PendingActionsResponse, SessionRuntimeStatus, WorkUnitSnapshot } from '@/api/chat'
import { getSessionRuntimeStatus } from '@/api/chat'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { useGoalStore } from '@/stores/goalStore'
import type { ChatMessageUI, MessageType, WorkUnitBlockStatus } from '@/types/messages'
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
    case 'goal_proposal':
    case 'goal_suggestion': return 'request_id'
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
 * Maps a durable work-unit status onto the block status the chat renders. A
 * 'pending' unit (registered but never started) has no pending visual on a
 * subagent block, so it renders as 'running'; every other status maps through
 * unchanged. Returns null for an unknown/malformed status so the caller skips
 * it rather than writing a bogus overlay entry.
 */
export function workUnitBlockStatus(status: string): WorkUnitBlockStatus | null {
  switch (status) {
    case 'running':
    case 'paused':
    case 'completed':
    case 'failed':
    case 'interrupted':
      return status
    case 'pending':
      return 'running'
    default:
      return null
  }
}

/**
 * Align the chat store's work-unit overlay for `sessionId` with the durable
 * snapshot from GetSessionRuntimeStatus.work_units, so a paused/interrupted
 * delegate or plan-step block renders its true state after a restart/session
 * load instead of a stale "running" (its replayed history carries no terminal
 * event). The overlay is consumed by groupMessages. Returns the overlay stored
 * (also handy for tests, and for the plan-panel reconcile).
 *
 * `snapshotReadAt` is the time the caller read the status (before the RPC
 * resolved). A live `work_unit_settled` / fresh-launch clear that landed after
 * that read is fresher than the snapshot's view of that step, so those entries
 * are carried over rather than overwritten with the older status.
 */
export function reconcileWorkUnits(
  sessionId: string,
  units: WorkUnitSnapshot[] | undefined,
  snapshotReadAt?: number,
): Record<string, WorkUnitBlockStatus> {
  const state = useChatStore.getState()
  // No DATA is not the same as no UNITS: an absent snapshot (an older backend,
  // or a task with no durable unit storage) must leave the overlay alone rather
  // than silently clearing entries live events recorded.
  if (units === undefined) return state.workUnitStatus[sessionId] ?? {}

  const liveAt = state.workUnitEventAt[sessionId] ?? {}
  const overlay: Record<string, WorkUnitBlockStatus> = {}
  if (snapshotReadAt !== undefined) {
    for (const [stepId, status] of Object.entries(state.workUnitStatus[sessionId] ?? {})) {
      if ((liveAt[stepId] ?? 0) > snapshotReadAt) overlay[stepId] = status
    }
  }
  for (const unit of units) {
    const status = workUnitBlockStatus(unit.status)
    if (!status) continue
    // A live write for THIS step that landed after the read wins over the
    // snapshot's older view; overwriting would roll a settled block back to the
    // stale 'running' this reconciliation exists to remove.
    if (snapshotReadAt !== undefined && (liveAt[unit.step_id] ?? 0) > snapshotReadAt) continue
    overlay[unit.step_id] = status
  }
  useChatStore.getState().setWorkUnitStatus(sessionId, overlay)
  return overlay
}

/**
 * A live `work_unit_settled` event: a unit was explicitly settled because the
 * resume funnel will not relaunch it (an abandoned in-flight unit → interrupted).
 * Record it in the overlay so a live view aligns immediately; the reload path
 * gets the same fact from the work-unit snapshot. Written through the store
 * action (not setWorkUnitStatus) so it also stamps workUnitEventAt — this is
 * LIVE knowledge, and an in-flight snapshot read before it must not outrank it.
 */
export function applyWorkUnitSettled(sessionId: string, stepId: string, status: string): void {
  const blockStatus = workUnitBlockStatus(status)
  if (!stepId || !blockStatus) return
  useChatStore.getState().settleWorkUnit(sessionId, stepId, blockStatus)
}

/**
 * Apply the task-flag snapshot (`taskActive` / `paused` / `pausing`) for
 * `sessionId` from a runtime status. THE single implementation of the flag
 * decision, shared by BOTH readers of the same backend snapshot:
 *
 * - the post-history-load reconcile ({@link reconcileRuntimeStatus}), and
 * - the switch-time fast restore (`useTaskFlagRestore`).
 *
 * They MUST share it. The fast restore's write stamps `taskFlagsEventAt`, which
 * makes the (possibly slower) reconcile below skip its OWN flag restore via the
 * stale-snapshot guard — so if the fast restore applied only `taskActive`, a
 * cooperatively PAUSED checkpoint would leave `chatStore.paused` unset. The
 * chat toolbar (which, unlike the sidebar dot / radar, reads the live flag with
 * no DB fallback) would then paint a plain Send button instead of Resume while
 * the list and radar showed the session paused.
 *
 * - compacting: the compaction flow owns the session; neither Pause nor Resume
 *   is meaningful and a stale pause signal must not survive.
 * - paused (and not active): a cooperative checkpoint — Resume + Stop, input
 *   unlocked.
 * - otherwise: running (`taskActive`) or idle; a pause-in-flight flag survives
 *   while the task is still active (the backend status cannot see the in-memory
 *   pause window), and is cleared once the task is no longer active.
 */
export function applyRuntimeTaskFlags(sessionId: string, status: SessionRuntimeStatus): void {
  const store = useChatStore.getState()
  if (status.compacting) {
    store.setPausing(sessionId, false)
    store.setPaused(sessionId, false)
    store.setTaskActive(sessionId, status.active)
    return
  }
  if (status.paused && !status.active) {
    store.setPausing(sessionId, false)
    store.setPaused(sessionId, true)
    store.setTaskActive(sessionId, false)
    return
  }
  store.setPaused(sessionId, false)
  store.setTaskActive(sessionId, status.active)
  if (!status.active) {
    store.setPausing(sessionId, false)
  }
}

/**
 * Reconcile the chat-store state for `sessionId` with the backend runtime status.
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
 * `flagsSnapshotReadAt` is the read time the TASK-FLAG freshness guard uses,
 * defaulting to `snapshotReadAt`. It exists for callers whose snapshot is
 * taken long after their loading window began (ChatArea's post-history-load
 * reconcile reads the status AFTER the history RPC resolves): a flag
 * transition that lands during the load — above all the OPTIMISTIC SEND
 * ACTIVATION (setTaskActive(true) in useMessageSender, stamped
 * taskFlagsEventAt before the SendMessage RPC is even issued) — is fresher
 * than any snapshot whose read merely postdates it, because the backend may
 * not have activated the session yet when the status RPC is handled. Guarding
 * the flags against the LOAD START instead makes any live transition during
 * the load beat the snapshot; the activation race that intermittently killed
 * the flag mid-run (settling the live work block to `interrupted` while the
 * loop kept streaming) is closed at this source.
 *
 * Returns the messages that were resolved as stale (so the caller can persist
 * the resolution to the backend). Messages already resolved are skipped.
 */
export function reconcileRuntimeStatus(
  sessionId: string,
  status: SessionRuntimeStatus,
  snapshotReadAt?: number,
  flagsSnapshotReadAt?: number,
): ChatMessageUI[] {
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
  // the flags map) must never be reverted by an older snapshot. The guard's
  // read time is `flagsSnapshotReadAt` (falling back to snapshotReadAt): a
  // caller whose snapshot postdates a long loading window (ChatArea's
  // post-history reconcile) passes its LOAD START there, so a flag transition
  // landing during the load — above all the optimistic send activation — is
  // never reverted by a snapshot that cannot yet see the run on the backend.
  const hasFresherTaskFlags =
    snapshotReadAt !== undefined &&
    (useChatStore.getState().taskFlagsEventAt[sessionId] ?? 0) > (flagsSnapshotReadAt ?? snapshotReadAt)

  // Re-seed the SINGLE live unfinished-task overlay (chatStore.unfinishedTaskStatus)
  // every status surface reads. After a reload chatStore's map is empty, so the
  // switch-time runtime snapshot is the authoritative source: a session whose
  // task finished while unviewed must not stay red/busy, and a resumable failure
  // must not be lost. The value mirrors the snapshot —
  //   running            → '' (green comes from taskActive)
  //   unfinished         → the EXACT persisted status when the backend reports
  //                        it (so an orphaned 'in_progress' stays green, matching
  //                        the DB fallback of a never-reconciled sibling),
  //                        falling back to 'paused' then 'failed' for older
  //                        backends (preserves the previous precedence)
  //   settled            → '' (idle)
  // Skipped while compacting (that flow owns the transition) and when a live
  // task-flag event intervened after the snapshot was read (it stamps
  // taskFlagsEventAt, the task-flag freshness mark).
  if (!hasFresherTaskFlags && !status.compacting) {
    const liveStatus = status.active
      ? ''
      : status.unfinished_task_status || (status.paused ? 'paused' : status.has_unfinished_task ? 'failed' : '')
    useChatStore.getState().setUnfinishedTaskStatus(sessionId, liveStatus)
  }

  // Manual compaction in flight: mirror the flag so a switch back to the
  // session restores the Compacting UI (locked input, cancel affordance)
  // even when the compaction_started event fired with no live listener.
  // Skipped when a live compaction event (compaction_started/finished)
  // already landed after the snapshot was read.
  if (!hasFresherLiveState) {
    store.setCompacting(sessionId, status.compacting === true)
  }

  // Manual compaction availability: mirror the backend's per-strategy
  // prediction so the compact menu enables exactly the strategies that would
  // actually shrink the dialogue and disables the rest with a reason. Absent
  // field (older backend) leaves the menu fail-open. Skipped when a live
  // compaction_finished already refreshed the prediction.
  if (!hasFresherLiveState) {
    if (status.compaction_availability !== undefined) {
      store.setCompactionAvailability(sessionId, status.compaction_availability)
    }
  }

  // Task flags (taskActive / paused / pausing) are applied from the snapshot
  // unless a LIVE flag transition landed after it (hasFresherTaskFlags). One
  // call covers all three branches below — compacting, paused, and
  // running/idle — via applyRuntimeTaskFlags, which the switch-time fast
  // restore (useTaskFlagRestore) shares so the two readers cannot drift.
  if (!hasFresherTaskFlags) {
    applyRuntimeTaskFlags(sessionId, status)
  }

  // The compaction flow owns the session: the task it paused shows neither
  // paused affordances nor a "did not finish" banner — compaction_finished
  // and the flow's auto-resume own the terminal transitions. Mirrors the
  // paused branch's stale-HITL resolution so prompts from before the flow
  // (if any) resolve identically.
  if (status.compacting) {
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
 *
 * `readAt` is the time the caller read the pending set (before the RPC
 * resolved). The task-active re-assertion below consults it through the
 * task-flag freshness stamp (`taskFlagsEventAt`): a live flag transition —
 * above all a pause or terminal event — that landed after the read outranks
 * the sweep, so a pending snapshot racing such a transition cannot resurrect
 * `taskActive` over it. Callers that omit `readAt` get no guard (their read
 * is known current); reconcileRuntimeStatus/reconcileWorkUnits follow the
 * same convention for their snapshot guards.
 *
 * `liveFlagsEventAt` is the `taskFlagsEventAt` value captured after the
 * pending read resolved but BEFORE the caller applied any OTHER snapshot of
 * the same load. The runtime-status reconcile writes flags through the
 * stamping store actions (`setUnfinishedTaskStatus` / `setTaskActive`), so
 * by the time this function runs the map already carries the SNAPSHOT's own
 * stamp — posted after `readAt` — and the guard would read it as a live
 * pause/terminal transition, suppressing the heal exactly when a stale
 * `active=false` snapshot needs correcting. A caller that composes both
 * reconciles (sessionHistoryReload) passes the pre-application value so the
 * guard compares against LIVE transitions only; omitted, the live map is
 * read (correct for callers that applied no other snapshot since the read).
 */
export function reconcilePendingActions(
  sessionId: string,
  pending: PendingActionsResponse,
  readAt?: number,
  liveFlagsEventAt?: number,
): ChatMessageUI[] {
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
        // Mirror the persisted metadata shape (the Go persister writes the
        // full payload, including plan_content, as metadata) so a prompt
        // resurfaced here matches one restored from history.
        plan_content: p.plan_content,
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

  // Goal proposals and goal-mode suggestions. NOTE: neither goal kind is in
  // the resolve-stale branch above — "not in pending" is decided by the LIVE
  // pending map, whose entry exists exactly while the agent goroutine waits;
  // the genuine stale case (restart with no running task) is handled by
  // reconcileRuntimeStatus via HITL_PROMPT_TYPES. The add-missing branches
  // resurface a genuinely-pending card whose event was missed while the
  // session was in the background; persisted cards reappear via history
  // reload regardless (roles goal_proposal / goal_suggestion).
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

  for (const s of pending.goal_suggestions) {
    const id = `goal-suggestion-${s.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'goal_suggestion',
      content: s.reason,
      metadata: {
        request_id: s.request_id,
        reason: s.reason,
        resolved: false,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
    // Keep the goal store in sync for a background-delivered suggestion that
    // surfaces here on session switch (mirrors handleGoalSuggestionEvent).
    useGoalStore.getState().setPendingSuggestion(sessionId, {
      request_id: s.request_id,
      session_id: sessionId,
      reason: s.reason,
    })
  }

  // A genuinely pending prompt means the session's agent goroutine is blocked
  // on the user RIGHT NOW (the backend reports only prompts with a live
  // waiter). Surface that in the activity label — mirrors what the live HITL
  // handlers set and overrides any pre-HITL phase the runtime status reported.
  const pendingCount = pending.tool_confirms.length + pending.step_limits.length
    + pending.plan_approvals.length + pending.ask_user.length + pending.goal_proposals.length
    + pending.goal_suggestions.length
  if (pendingCount > 0) {
    // A live waiter is also authoritative for the TASK FLAGS: the waiter
    // exists exactly while the task goroutine runs (a cancel/exit deletes the
    // pending entry), so a runtime-status snapshot that raced a just-sent
    // task's backend activation (its read predates `session.active = true`)
    // cannot be allowed to leave `taskActive` false — that settles the live
    // turn's work block to `interrupted` and buries the prompt exactly while
    // the user must act on it. One-directional TRUE re-assertion, mirroring
    // the step_start / HITL-arrival heal; a truthful snapshot reports
    // active=true anyway. Freshness-guarded by `readAt` like every other
    // snapshot application here: a pause/terminal transition that landed
    // AFTER the pending read (the two RPCs race a user pause — the backend
    // deletes the waiter on it, but a read already in flight may still
    // report the entry) owns the flag, and a task that genuinely finished
    // between the read and this application is corrected by its terminal
    // event or the next reconcile (whose read postdates this stamp). The
    // compared stamp is the LIVE-transition time — `liveFlagsEventAt` when
    // the caller already applied a status snapshot (that application stamps
    // the map itself and must not masquerade as a live transition).
    const flagsEventAt = liveFlagsEventAt ?? store.taskFlagsEventAt[sessionId] ?? 0
    const hasFresherTaskFlags = readAt !== undefined && flagsEventAt > readAt
    if (!hasFresherTaskFlags && store.taskActive[sessionId] !== true) {
      store.setTaskActive(sessionId, true)
    }
    store.setActivityStatus(sessionId, 'Waiting for your response...')
  }

  return resolvedStale
}

/**
 * Targeted refresh of the compaction no-op flag (the compact button's
 * disabled state) after an event that changed the conversation history
 * WITHOUT a session switch — a finished task appended its exchange to the
 * history, so a previously-unavailable strategy may now be compactable again.
 * reconcileRuntimeStatus above only runs on history load (ChatArea), so
 * terminal task events call this instead.
 *
 * Deliberately narrow: fetches the runtime status and applies ONLY
 * compactionAvailability — the full reconcile is load-time logic (stale-prompt
 * resolution, resume banners) that must not run from an event handler.
 * Best-effort: on RPC failure the menu keeps its previous value until the
 * next reconcile/refetch.
 */
export function refreshCompactionAvailability(sessionId: string): void {
  getSessionRuntimeStatus(sessionId)
    .then((status) => {
      if (!status || status.compaction_availability === undefined) return
      useChatStore.getState().setCompactionAvailability(sessionId, status.compaction_availability)
    })
    .catch(() => { /* best-effort — see doc comment */ })
}
