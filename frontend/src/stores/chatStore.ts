import { useMemo } from 'react'
import { create } from 'zustand'
import type { ChatMessageUI, WorkUnitBlockStatus } from '@/types/messages'
import type { TokenInfo, CompactionAvailability } from '@/types/models'
import { HITL_PROMPT_TYPES } from '@/lib/hitlTypes'

// Re-export types and grouping functions so existing imports continue to work
export type { MessageType, ChatMessageUI, DisplayItem, GroupedMessages, WorkUnitBlockStatus } from '@/types/messages'
export { groupMessages } from '@/lib/chatUtils'

// --- State types ---

/** Per-step context-window token totals carried by step-scoped context_fill events. */
export interface StepContextTokens {
  used_tokens: number
  max_tokens: number
}

interface ChatState {
  // Messages indexed by session: sessionId -> messageId -> message
  messages: Record<string, Record<string, ChatMessageUI>>
  // Ordered message IDs per session: sessionId -> messageId[]
  messageOrder: Record<string, string[]>
  // Streaming per session: sessionId -> accumulated text (absent key = not streaming)
  streamingText: Record<string, string>
  // Activity status per session: sessionId -> status (absent key = no activity)
  activityStatus: Record<string, string>
  // Task active per session
  taskActive: Record<string, boolean>
  // Live unfinished-task status per session: sessionId -> '' | 'failed' |
  // 'in_progress' | 'paused'. THE single live overlay every session-status
  // surface colors from (sidebar session list, radar badge, radar dropdown
  // rows); it outranks the DB snapshot's `unfinished_task_status` whenever the
  // key is PRESENT, so one lifecycle event repaints every dot at once without
  // waiting for a list refresh. Absent key = chatStore holds no live knowledge
  // for the session → surfaces fall back to the DB snapshot. An explicit ''
  // means "the task settled", which overrides a stale snapshot value.
  unfinishedTaskStatus: Record<string, string>
  // Cooperatively paused per session: sessionId -> true when the running task
  // is suspended at a checkpoint (input unlocked, Resume/Stop controls). The
  // backend's session_paused/session_resumed events and GetSessionRuntimeStatus
  // .paused field are the authoritative sources; the UI sets this optimistically
  // on Pause/Resume button clicks and reconciles on session switch/restart.
  paused: Record<string, boolean>
  // Pause-in-flight per session: sessionId -> true between the user clicking
  // Pause and the backend's session_paused event. A cooperative pause lands at
  // the next step boundary — the ReAct loop keeps emitting progress events
  // (step_start, assistant_chunk, ...) meanwhile — so `paused` must NOT be
  // set optimistically. While `pausing` is true the pause action renders as a
  // non-clickable spinner, the input stays locked (taskActive is still true)
  // and the activity label is overridden with "Pausing". Cleared by the
  // terminal events (session_paused/task_complete/task_cancelled/error) and
  // by the runtime reconcile once the task is no longer active.
  pausing: Record<string, boolean>
  // Manual compaction in flight per session: sessionId -> true while
  // the backend compact flow runs (input locked, compact button → cancel
  // button). Cleared by compaction_finished and reconciled from the runtime
  // status snapshot on session switch/restart.
  compacting: Record<string, boolean>
  // Per-strategy manual-compaction prediction per session: sessionId -> the
  // ordered availability list the backend computed for the session's current
  // conversation history. Each entry says whether that strategy would actually
  // shrink the dialogue now and how many tokens it would reclaim. Absent key =
  // unknown (fail-open: every strategy shown clickable). Set from the
  // runtime-status snapshot (reconcile/refetch), refreshed by
  // compaction_finished, and re-evaluated by the targeted refetch after
  // terminal task events (a finished task grew the history again).
  compactionAvailability: Record<string, CompactionAvailability[]>
  // Context fill per step, keyed by session then step: sessionId -> stepId -> fill percent.
  // Nested per-session: plan step ids (step_1, ...) are NOT globally unique
  // across sessions, and fills must survive session switches (A→B→A) — the
  // global wipe that preceded this keyed form erased the badges of whichever
  // session the user switched to.
  stepContextFill: Record<string, Record<string, number>>
  // Context-window token totals per step, keyed like stepContextFill:
  // sessionId -> stepId -> { used_tokens, max_tokens }. Fed from the
  // step-scoped context_fill events (a subagent/executor step reports its own
  // window, separate from the conductor's session-level totals), so a step
  // block can render "used of max" the same way the status bar renders the
  // session-level window. Cleared by clearStepContextFill alongside
  // stepContextFill. Entries may be partial: merge semantics let the first
  // event payload for a step carry only one of the two fields.
  stepContextTokens: Record<string, Record<string, Partial<StepContextTokens>>>
  // Session tokens: sessionId -> token info
  sessionTokens: Record<string, TokenInfo>
  // Timestamp of the last LIVE update to a session's activity label or
  // streaming text: sessionId -> Date.now() at the mutation. Every event
  // handler that touches activityStatus/streamingText goes through the store
  // actions, which stamp this map — reconcileRuntimeStatus compares it against
  // the time the backend status snapshot was read to detect (and skip) a
  // stale-snapshot overwrite of newer live state. Absent key = never touched.
  runtimeEventAt: Record<string, number>
  // Timestamp of the last LIVE update to a session's task FLAGS
  // (taskActive/paused/pausing): sessionId -> Date.now() at the mutation.
  // Separate from runtimeEventAt because chunks stamp the activity map
  // without owning the flags: reconcileRuntimeStatus must still restore
  // flags from a snapshot for a background-started run (no lifecycle event
  // ever set them), while a live pause/resume/terminal transition (which
  // stamps THIS map) must never be reverted by an older snapshot.
  taskFlagsEventAt: Record<string, number>
  // Durable work-unit status per session, keyed by step id: sessionId ->
  // stepId -> block status. Written by the session-load reconciliation
  // (reconcileWorkUnits) from GetSessionRuntimeStatus.work_units so a
  // paused/interrupted delegate or plan-step block renders its true state
  // instead of a stale "running" after a restart/session load (the replayed
  // history has no terminal event for it). Absent key = no snapshot knowledge;
  // the block status then comes from the replayed messages alone. A step's
  // entry is dropped when a fresh live launch proves the unit running again.
  workUnitStatus: Record<string, Record<string, WorkUnitBlockStatus>>
  // Timestamp of the last LIVE work-unit write for a step (a `work_unit_settled`
  // settlement, or a fresh-launch clear), keyed like workUnitStatus:
  // sessionId -> stepId -> Date.now(). reconcileWorkUnits compares a step's
  // stamp against the time its snapshot was read to tell live knowledge from
  // the snapshot's older view of the same step (the snapshot is read BEFORE it
  // resolves, so a live event landing in that window is fresher).
  workUnitEventAt: Record<string, Record<string, number>>
  // Last-known scroll position per session: sessionId -> the scroll state the
  // chat viewport held when the user last left the session (its
  // ChatScrollManager unmounted on the session switch). The next initial mount
  // of that session's viewport restores the reading position instead of
  // jumping — unless the session's task is still running, in which case the
  // viewport opens pinned to the live tail. A session with no entry opens
  // pinned to the newest content.
  // Cleared when the session is deleted.
  scrollPositions: Record<string, { scrollTop: number; scrollHeight: number }>
}

interface ChatActions {
  addMessage: (sessionId: string, message: ChatMessageUI) => void
  updateMessage: (sessionId: string, messageId: string, updates: Partial<ChatMessageUI>) => void
  removeMessage: (sessionId: string, messageId: string) => void
  upsertChecklistMessage: (sessionId: string, message: ChatMessageUI) => void
  setMessages: (sessionId: string, messages: ChatMessageUI[]) => void
  /** Replace the session's messages with the persisted history, preserving
   *  live-event rows that arrived while the RPC was in flight. */
  mergeHistoryMessages: (sessionId: string, history: ChatMessageUI[], loadStartedAt: number) => void
  setStreamingText: (sessionId: string, text: string) => void
  appendStreamingText: (sessionId: string, delta: string) => void
  clearStreamingText: (sessionId: string) => void
  setActivityStatus: (sessionId: string, status: string | null) => void
  setTaskActive: (sessionId: string, active: boolean) => void
  setUnfinishedTaskStatus: (sessionId: string, status: string | undefined) => void
  setPaused: (sessionId: string, paused: boolean) => void
  setPausing: (sessionId: string, pausing: boolean) => void
  setCompacting: (sessionId: string, compacting: boolean) => void
  setCompactionAvailability: (sessionId: string, availability: CompactionAvailability[]) => void
  setStepContextFill: (sessionId: string, stepId: string, fill: number) => void
  setStepContextTokens: (sessionId: string, stepId: string, tokens: Partial<StepContextTokens>) => void
  clearStepContextFill: (sessionId: string) => void
  setSessionTokens: (sessionId: string, tokens: Partial<TokenInfo>) => void
  setWorkUnitStatus: (sessionId: string, status: Record<string, WorkUnitBlockStatus>) => void
  settleWorkUnit: (sessionId: string, stepId: string, status: WorkUnitBlockStatus) => void
  clearWorkUnitStep: (sessionId: string, stepId: string) => void
  /** Persist the scroll position the session's chat viewport held when it
   *  unmounted (session switch / app close), so the next visit restores the
   *  reading position instead of jumping. */
  saveScrollPosition: (sessionId: string, position: { scrollTop: number; scrollHeight: number }) => void
  /** Forget the session's saved scroll position (session deletion). */
  clearScrollPosition: (sessionId: string) => void
}

// --- Helpers ---

function indexMessages(msgs: ChatMessageUI[]): Record<string, ChatMessageUI> {
  const index: Record<string, ChatMessageUI> = {}
  for (const msg of msgs) {
    index[msg.id] = msg
  }
  return index
}

// --- Selectors ---

/**
 * Derives ordered session messages from raw store state.
 * ⚠️ DO NOT use as a Zustand selector — always returns a new array.
 * Use the {@link useSessionMessages} hook for reactive subscriptions instead.
 */
export function selectSessionMessages(state: ChatState & ChatActions, sessionId: string): ChatMessageUI[] {
  const order = state.messageOrder[sessionId]
  const index = state.messages[sessionId]
  if (!order || !index) return []
  const result: ChatMessageUI[] = []
  for (const id of order) {
    const msg = index[id]
    if (msg) result.push(msg)
  }
  return result
}

// --- Stable empty array for hooks ---

const EMPTY_MESSAGES: ChatMessageUI[] = []

/**
 * Hook returning memoised ordered messages for a session.
 * Uses granular selectors so the component only re-renders when
 * the specific session's data actually changes — avoids the infinite-loop
 * caused by selectSessionMessages creating a new array on every call.
 */
export function useSessionMessages(sessionId: string | null): ChatMessageUI[] {
  const messageOrder = useChatStore(
    s => (sessionId ? s.messageOrder[sessionId] : undefined),
  )
  const messageIndex = useChatStore(
    s => (sessionId ? s.messages[sessionId] : undefined),
  )

  return useMemo(() => {
    if (!messageOrder || !messageIndex) return EMPTY_MESSAGES
    const result: ChatMessageUI[] = []
    for (const id of messageOrder) {
      const msg = messageIndex[id]
      if (msg) result.push(msg)
    }
    return result
  }, [messageOrder, messageIndex])
}

// Stable empty overlay so the hook never allocates a new object per render
// (AGENTS.md: selectors must return referentially stable values).
const EMPTY_WORK_UNIT_STATUS: Record<string, WorkUnitBlockStatus> = {}

/**
 * Hook returning a session's durable work-unit status overlay (stepId -> block
 * status) for {@link groupMessages}. Returns a stable empty object when the
 * session has no snapshot, and the store's own object otherwise (a direct
 * store reference — no per-call allocation).
 */
export function useSessionWorkUnits(sessionId: string | null): Record<string, WorkUnitBlockStatus> {
  return useChatStore(s => (sessionId ? s.workUnitStatus[sessionId] : undefined)) ?? EMPTY_WORK_UNIT_STATUS
}

// --- Store ---

export const useChatStore = create<ChatState & ChatActions>((set) => ({
  messages: {},
  messageOrder: {},
  streamingText: {},
  activityStatus: {},
  taskActive: {},
  unfinishedTaskStatus: {},
  paused: {},
  pausing: {},
  compacting: {},
  compactionAvailability: {},
  stepContextFill: {},
  stepContextTokens: {},
  sessionTokens: {},
  runtimeEventAt: {},
  taskFlagsEventAt: {},
  workUnitStatus: {},
  workUnitEventAt: {},
  scrollPositions: {},

  addMessage: (sessionId, message) => set((s) => {
    const sessionIndex = s.messages[sessionId] ?? {}
    const sessionOrder = s.messageOrder[sessionId] ?? []
    // Idempotent upsert: when `message.id` is already tracked, update its
    // content in place WITHOUT appending a duplicate order entry. This makes a
    // re-emission of the same deterministic id (e.g. a goal_status snapshot
    // re-sent on pause/resume) update the row instead of rendering it twice —
    // matching the no-duplicate semantics mergeHistoryMessages already enforces.
    const order = sessionIndex[message.id] ? sessionOrder : [...sessionOrder, message.id]
    return {
      messages: {
        ...s.messages,
        [sessionId]: { ...sessionIndex, [message.id]: message },
      },
      messageOrder: {
        ...s.messageOrder,
        [sessionId]: order,
      },
    }
  }),

  updateMessage: (sessionId, messageId, updates) => set((s) => {
    const sessionIndex = s.messages[sessionId]
    if (!sessionIndex) return s
    const existing = sessionIndex[messageId]
    if (!existing) return s
    return {
      messages: {
        ...s.messages,
        [sessionId]: {
          ...sessionIndex,
          [messageId]: {
            ...existing,
            ...updates,
            metadata: updates.metadata
              ? { ...existing.metadata, ...updates.metadata }
              : existing.metadata,
          },
        },
      },
    }
  }),

  removeMessage: (sessionId, messageId) => set((s) => {
    const sessionIndex = s.messages[sessionId]
    const sessionOrder = s.messageOrder[sessionId]
    if (!sessionIndex || !sessionOrder) return s
    const { [messageId]: _, ...rest } = sessionIndex
    return {
      messages: { ...s.messages, [sessionId]: rest },
      messageOrder: {
        ...s.messageOrder,
        [sessionId]: sessionOrder.filter(id => id !== messageId),
      },
    }
  }),

  // Replace any existing step_todo_update message for the same step_id with
  // the new one, appending the new message at the stream end. The Conductor
  // updates its checklist after every tool call, so storing one row per
  // update would grow the message list without bound and make groupMessages
  // re-run over an ever-larger history (O(n^2)). Collapsing to one checklist
  // row per step_id keeps the live store bounded while preserving the
  // "settled checklist stays at its stream position" semantics (the surviving
  // row sits where the latest update landed). Root-level collapse across
  // DIFFERENT step_ids (standalone "" vs an ad-hoc step_id whose block is
  // suppressed/closed) is handled separately by groupMessages, which keys
  // root checklists by level rather than step_id.
  upsertChecklistMessage: (sessionId, message) => set((s) => {
    const sessionIndex = s.messages[sessionId] ?? {}
    const sessionOrder = s.messageOrder[sessionId] ?? []
    const stepId = (message.metadata?.step_id as string | undefined) ?? ''
    const nextIndex = { ...sessionIndex }
    let order = sessionOrder
    for (const id of sessionOrder) {
      const m = sessionIndex[id]
      if (!m || m.type !== 'step_todo_update') continue
      if (((m.metadata?.step_id as string | undefined) ?? '') !== stepId) continue
      delete nextIndex[id]
      order = order.filter(oid => oid !== id)
    }
    nextIndex[message.id] = message
    order = [...order, message.id]
    return {
      messages: { ...s.messages, [sessionId]: nextIndex },
      messageOrder: { ...s.messageOrder, [sessionId]: order },
    }
  }),

  setMessages: (sessionId, messages) => set((s) => ({
    messages: { ...s.messages, [sessionId]: indexMessages(messages) },
    messageOrder: { ...s.messageOrder, [sessionId]: messages.map(m => m.id) },
  })),

  // Replace the session's messages with the persisted history, but preserve
  // live-event messages that arrived while the history RPC was in flight
  // (timestamp >= loadStartedAt and not present in the snapshot). A plain
  // setMessages would clobber e.g. an `error` event delivered between the
  // backend's DB read and this state update, leaving the session looking
  // cleanly finished when it actually failed.
  //
  // Additionally, UNRESOLVED HITL prompt messages (tool_confirm, ask_user,
  // step_limit, plan_review) are preserved even when they predate the switch.
  // The combined emit function delivers these events to the UI before
  // persisting them (backend/application.go), so a live card can be
  // momentarily absent from the history snapshot loaded right after a switch.
  // Without preservation the card would disappear here and only reappear once
  // the (async) GetPendingActions reconcile re-adds it — a visible flicker, or
  // a permanent loss if that reconcile is skipped (e.g. the RPC fails).
  // HITL preservation cannot duplicate a persisted row: the live event handler
  // (hooks/events/hitlHandlers.ts) and the history→UI converter
  // (lib/chatUtilsHelpers.ts) derive the SAME semantic id
  // (`<type>-<request_id|confirm_id>`), and any message already in historyIds
  // is skipped above before this check runs.
  // Caveat: this id-equivalence holds only for HITL messages. The
  // `timestamp >= loadStartedAt` branch below preserves recent non-HITL live
  // messages (e.g. the final assistant answer) that use random ids; those are
  // only safe from duplication when the loaded history snapshot does not
  // already contain them — a narrow window, since history is read from the DB
  // and a live event can land during that RPC flight. If duplication of the
  // final answer is ever observed, dedupe preserved non-HITL messages by
  // (type, content) here, or have live handlers reuse a backend-supplied id.
  mergeHistoryMessages: (sessionId, history, loadStartedAt) => {
    set((s) => {
      const liveIndex = s.messages[sessionId] ?? {}
      const liveOrder = s.messageOrder[sessionId] ?? []
      const historyIds = new Set(history.map(m => m.id))
      const preserved: ChatMessageUI[] = []
      for (const id of liveOrder) {
        const msg = liveIndex[id]
        if (!msg || historyIds.has(id)) continue
        if (msg.timestamp >= loadStartedAt) { preserved.push(msg); continue }
        // Keep live, unresolved HITL prompts even if they predate the switch.
        if (HITL_PROMPT_TYPES.has(msg.type) && msg.metadata?.resolved !== true) {
          preserved.push(msg)
        }
      }
      const merged = [...history, ...preserved]
      return {
        messages: { ...s.messages, [sessionId]: indexMessages(merged) },
        messageOrder: { ...s.messageOrder, [sessionId]: merged.map(m => m.id) },
      }
    })
  },

  // Streaming/activity actions stamp runtimeEventAt (see state comment) so
  // reconcileRuntimeStatus can tell live state from snapshot state.
  setStreamingText: (sessionId, text) => set((s) => ({
    streamingText: { ...s.streamingText, [sessionId]: text },
    runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
  })),

  appendStreamingText: (sessionId, delta) => set((s) => {
    const prev = s.streamingText[sessionId] ?? ''
    return {
      streamingText: { ...s.streamingText, [sessionId]: prev + delta },
      runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
    }
  }),

  clearStreamingText: (sessionId) => set((s) => {
    // Stamp even when the key is absent: a live terminal event that clears
    // an already-clear stream is still fresher state than a runtime-status
    // snapshot read before it (see reconcileRuntimeStatus).
    if (!(sessionId in s.streamingText)) {
      return { runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() } }
    }
    const { [sessionId]: _stream, ...rest } = s.streamingText
    return {
      streamingText: rest,
      runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
    }
  }),

  setActivityStatus: (sessionId, status) => set((s) => {
    if (status === null || status === undefined) {
      // Stamp even when already absent (same freshness contract as
      // clearStreamingText's no-op path).
      if (!(sessionId in s.activityStatus)) {
        return { runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() } }
      }
      const { [sessionId]: _status, ...rest } = s.activityStatus
      return {
        activityStatus: rest,
        runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
      }
    }
    return {
      activityStatus: { ...s.activityStatus, [sessionId]: status },
      runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
    }
  }),

  setTaskActive: (sessionId, active) => set((s) => {
    // A deactivation leaves the unfinished-task overlay untouched: the terminal
    // event that clears taskActive sets the real overlay value right beside it
    // ('' when the task settled, 'failed' when it stays resumable). An
    // activation, however, means the session is running NOW, so any
    // unfinished-task status the DB snapshot still carries (a stale 'failed' /
    // 'paused' / 'in_progress' from a list load taken before this run) is
    // superseded — pin the live overlay to '' so every status surface repaints
    // green without a refresh.
    if (!active) {
      return {
        taskActive: { ...s.taskActive, [sessionId]: active },
        taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
      }
    }
    return {
      taskActive: { ...s.taskActive, [sessionId]: active },
      taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
      unfinishedTaskStatus: { ...s.unfinishedTaskStatus, [sessionId]: '' },
    }
  }),

  // THE single live unfinished-task status write, consumed by every session-
  // status surface through chatStore → deriveLiveSessionFlags →
  // deriveSessionStatus. Stamps taskFlagsEventAt (it IS a task flag: the
  // runtime-status reconcile consults that stamp before mirroring a snapshot
  // value, so a stale snapshot can never revert a live transition). No-ops when
  // the value already matches to keep the map reference stable (React #185).
  // `undefined` DELETES the entry, restoring "chatStore holds no live knowledge"
  // (an ABSENT key falls back to the DB snapshot). Used by an optimistic-send
  // rollback whose pre-send overlay was itself absent: writing a defined ''
  // there would outrank the DB snapshot and mask a real failed/in_progress task
  // as settled.
  setUnfinishedTaskStatus: (sessionId, status) => set((s) => {
    const prev = s.unfinishedTaskStatus[sessionId]
    if (status === undefined) {
      if (prev === undefined) return s
      const { [sessionId]: _dropped, ...rest } = s.unfinishedTaskStatus
      return {
        unfinishedTaskStatus: rest,
        taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
      }
    }
    if (prev === status) return s
    return {
      unfinishedTaskStatus: { ...s.unfinishedTaskStatus, [sessionId]: status },
      taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
    }
  }),

  setPaused: (sessionId, paused) => set((s) => {
    // Clear the entry when un-pausing so the key's absence encodes "not paused"
    // (consistent with the absent-key semantics used elsewhere in this store).
    // Stamps taskFlagsEventAt even on no-ops (same freshness contract as the
    // runtimeEventAt no-op stamps).
    if (!paused) {
      if (!(sessionId in s.paused)) {
        return { taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() } }
      }
      const { [sessionId]: _paused, ...rest } = s.paused
      return {
        paused: rest,
        taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
      }
    }
    return {
      paused: { ...s.paused, [sessionId]: true },
      taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
    }
  }),

  setPausing: (sessionId, pausing) => set((s) => {
    // Same absent-key convention as paused: clearing deletes the entry so no
    // state change is emitted for sessions that were never pausing.
    if (!pausing) {
      if (!(sessionId in s.pausing)) {
        return { taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() } }
      }
      const { [sessionId]: _pausing, ...rest } = s.pausing
      return {
        pausing: rest,
        taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
      }
    }
    return {
      pausing: { ...s.pausing, [sessionId]: true },
      taskFlagsEventAt: { ...s.taskFlagsEventAt, [sessionId]: Date.now() },
    }
  }),

  // Manual context compaction in flight: the input area is locked and the
  // compact button swaps for a cancel button. Absent key = not compacting.
  // Stamps runtimeEventAt (even on no-ops) so a live compaction_started /
  // compaction_finished always counts as fresher than an in-flight
  // runtime-status snapshot whose compacting flag predates it.
  setCompacting: (sessionId, compacting) => set((s) => {
    if (!compacting) {
      if (!(sessionId in s.compacting)) {
        return { runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() } }
      }
      const { [sessionId]: _compacting, ...rest } = s.compacting
      return {
        compacting: rest,
        runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
      }
    }
    return {
      compacting: { ...s.compacting, [sessionId]: true },
      runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
    }
  }),

  // Per-strategy manual-compaction prediction: replaces the compact menu's
  // availability list (absent key = unknown/fail-open — every strategy shows
  // clickable). Stamps runtimeEventAt like setCompacting.
  setCompactionAvailability: (sessionId, availability) => set((s) => ({
    compactionAvailability: { ...s.compactionAvailability, [sessionId]: availability },
    runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
  })),

  setStepContextFill: (sessionId, stepId, fill) => set((s) => ({
    stepContextFill: {
      ...s.stepContextFill,
      [sessionId]: { ...s.stepContextFill[sessionId], [stepId]: fill },
    },
  })),

  // Merge ONE step's token totals (setStepContextFill's merge semantics):
  // absent fields are omitted from the spread so they leave the previously
  // known values untouched instead of coercing them to 0/undefined.
  setStepContextTokens: (sessionId, stepId, tokens) => set((s) => {
    const existing = s.stepContextTokens[sessionId]?.[stepId]
    return {
      stepContextTokens: {
        ...s.stepContextTokens,
        [sessionId]: {
          ...s.stepContextTokens[sessionId],
          [stepId]: { ...existing, ...tokens },
        },
      },
    }
  }),

  // Clears one session's step fills (absent key = nothing to do); other
  // sessions' fills survive. Also drops the session's per-step token totals —
  // both maps are keyed by the same plan-step ids, so they must share the
  // plan-replace lifecycle (usePlanEvents' handlePlanGenerated clears them via
  // this one action without extra wiring).
  clearStepContextFill: (sessionId) => set((s) => {
    const patch: Partial<Pick<ChatState, 'stepContextFill' | 'stepContextTokens'>> = {}
    if (sessionId in s.stepContextFill) {
      const { [sessionId]: _fills, ...rest } = s.stepContextFill
      patch.stepContextFill = rest
    }
    if (sessionId in s.stepContextTokens) {
      const { [sessionId]: _tokens, ...rest } = s.stepContextTokens
      patch.stepContextTokens = rest
    }
    return patch
  }),

  // Merge partial token info into the session entry so event-driven updates
  // (context_fill / session_tokens) that omit fill_percent don't clobber it,
  // while the full TokenInfo from GetSessionTokens still replaces everything.
  setSessionTokens: (sessionId, tokens) => set((s) => {
    const existing = s.sessionTokens[sessionId]
    return {
      sessionTokens: { ...s.sessionTokens, [sessionId]: { ...existing, ...tokens } as TokenInfo },
    }
  }),

  // Replace the session's durable work-unit overlay wholesale: the snapshot is
  // authoritative for the whole session at load time. Deliberately does NOT
  // stamp runtimeEventAt/taskFlagsEventAt — the overlay is a fallback
  // consulted by groupMessages, not a live flag competing with events — and it
  // does not stamp workUnitEventAt either: only a LIVE write may outrank a
  // snapshot, and reconcileWorkUnits is what compares the two.
  setWorkUnitStatus: (sessionId, status) => set((s) => ({
    workUnitStatus: { ...s.workUnitStatus, [sessionId]: status },
  })),

  // Merge ONE step's overlay entry from a live `work_unit_settled` event and
  // stamp workUnitEventAt for that step, so a snapshot read before the event
  // cannot roll the step back to its stale value.
  settleWorkUnit: (sessionId, stepId, status) => set((s) => ({
    workUnitStatus: {
      ...s.workUnitStatus,
      [sessionId]: { ...s.workUnitStatus[sessionId], [stepId]: status },
    },
    workUnitEventAt: {
      ...s.workUnitEventAt,
      [sessionId]: { ...s.workUnitEventAt[sessionId], [stepId]: Date.now() },
    },
  })),

  // Drop one step's overlay entry. A fresh live launch (subagent_launch /
  // plan_step_start) proves the unit is running again, so the stale
  // paused/interrupted snapshot must stop overriding the block. The clear is a
  // LIVE write too, so it stamps workUnitEventAt on EVERY path — including
  // the no-entry path, where an older snapshot must not re-add the entry
  // (reconcileWorkUnits compares this stamp against the snapshot read time).
  // workUnitStatus itself is only rebuilt when an entry is actually dropped,
  // so the no-entry path keeps its exact reference (React #185).
  clearWorkUnitStep: (sessionId, stepId) => set((s) => {
    const session = s.workUnitStatus[sessionId]
    const hadEntry = session !== undefined && stepId in session
    const { [stepId]: _dropped, ...rest } = session ?? {}
    return {
      ...(hadEntry ? { workUnitStatus: { ...s.workUnitStatus, [sessionId]: rest } } : {}),
      workUnitEventAt: {
        ...s.workUnitEventAt,
        [sessionId]: { ...s.workUnitEventAt[sessionId], [stepId]: Date.now() },
      },
    }
  }),

  // Saved reading position: written by ChatScrollManager's unmount cleanup (the
  // component remounts per session via key={activeSessionId}, so the cleanup
  // fires exactly on session switches/app teardown) and read back by the
  // session's next initial mount. A no-op when the position is unchanged keeps
  // the map reference stable (React #185).
  saveScrollPosition: (sessionId, position) => set((s) => {
    const prev = s.scrollPositions[sessionId]
    if (prev && prev.scrollTop === position.scrollTop && prev.scrollHeight === position.scrollHeight) return s
    return { scrollPositions: { ...s.scrollPositions, [sessionId]: position } }
  }),

  // Forget the saved position (the session was deleted — a recreated session
  // id never collides, but dropping the entry keeps the map bounded). Same
  // absent-key convention as the other clearers: no state change when nothing
  // is stored.
  clearScrollPosition: (sessionId) => set((s) => {
    if (!(sessionId in s.scrollPositions)) return s
    const { [sessionId]: _pos, ...rest } = s.scrollPositions
    return { scrollPositions: rest }
  }),
}))
