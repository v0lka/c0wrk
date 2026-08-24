import { useMemo } from 'react'
import { create } from 'zustand'
import type { ChatMessageUI } from '@/types/messages'
import type { TokenInfo } from '@/types/models'
import { HITL_PROMPT_TYPES } from '@/lib/hitlTypes'

// Re-export types and grouping functions so existing imports continue to work
export type { MessageType, ChatMessageUI, DisplayItem, GroupedMessages } from '@/types/messages'
export { groupMessages } from '@/lib/chatUtils'

// --- State types ---

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
  // Context fill per step, keyed by session then step: sessionId -> stepId -> fill percent.
  // Nested per-session: plan step ids (step_1, ...) are NOT globally unique
  // across sessions, and fills must survive session switches (A→B→A) — the
  // global wipe that preceded this keyed form erased the badges of whichever
  // session the user switched to.
  stepContextFill: Record<string, Record<string, number>>
  // Session tokens: sessionId -> token info
  sessionTokens: Record<string, TokenInfo>
  // Timestamp of the last LIVE update to a session's activity label or
  // streaming text: sessionId -> Date.now() at the mutation. Every event
  // handler that touches activityStatus/streamingText goes through the store
  // actions, which stamp this map — reconcileRuntimeStatus compares it against
  // the time the backend status snapshot was read to detect (and skip) a
  // stale-snapshot overwrite of newer live state. Absent key = never touched.
  runtimeEventAt: Record<string, number>
}

interface ChatActions {
  addMessage: (sessionId: string, message: ChatMessageUI) => void
  updateMessage: (sessionId: string, messageId: string, updates: Partial<ChatMessageUI>) => void
  removeMessage: (sessionId: string, messageId: string) => void
  upsertChecklistMessage: (sessionId: string, message: ChatMessageUI) => void
  setMessages: (sessionId: string, messages: ChatMessageUI[]) => void
  mergeHistoryMessages: (sessionId: string, history: ChatMessageUI[], loadStartedAt: number) => void
  setStreamingText: (sessionId: string, text: string) => void
  appendStreamingText: (sessionId: string, delta: string) => void
  clearStreamingText: (sessionId: string) => void
  setActivityStatus: (sessionId: string, status: string | null) => void
  setTaskActive: (sessionId: string, active: boolean) => void
  setPaused: (sessionId: string, paused: boolean) => void
  setPausing: (sessionId: string, pausing: boolean) => void
  setStepContextFill: (sessionId: string, stepId: string, fill: number) => void
  clearStepContextFill: (sessionId: string) => void
  setSessionTokens: (sessionId: string, tokens: Partial<TokenInfo>) => void
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

// --- Store ---

export const useChatStore = create<ChatState & ChatActions>((set) => ({
  messages: {},
  messageOrder: {},
  streamingText: {},
  activityStatus: {},
  taskActive: {},
  paused: {},
  pausing: {},
  stepContextFill: {},
  sessionTokens: {},
  runtimeEventAt: {},

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
  mergeHistoryMessages: (sessionId, history, loadStartedAt) => set((s) => {
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
  }),

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
    if (!(sessionId in s.streamingText)) return s
    const { [sessionId]: _stream, ...rest } = s.streamingText
    return {
      streamingText: rest,
      runtimeEventAt: { ...s.runtimeEventAt, [sessionId]: Date.now() },
    }
  }),

  setActivityStatus: (sessionId, status) => set((s) => {
    if (status === null || status === undefined) {
      if (!(sessionId in s.activityStatus)) return s
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

  setTaskActive: (sessionId, active) => set((s) => ({
    taskActive: { ...s.taskActive, [sessionId]: active },
  })),

  setPaused: (sessionId, paused) => set((s) => {
    // Clear the entry when un-pausing so the key's absence encodes "not paused"
    // (consistent with the absent-key semantics used elsewhere in this store).
    if (!paused) {
      if (!(sessionId in s.paused)) return s
      const { [sessionId]: _paused, ...rest } = s.paused
      return { paused: rest }
    }
    return { paused: { ...s.paused, [sessionId]: true } }
  }),

  setPausing: (sessionId, pausing) => set((s) => {
    // Same absent-key convention as paused: clearing deletes the entry so no
    // state change is emitted for sessions that were never pausing.
    if (!pausing) {
      if (!(sessionId in s.pausing)) return s
      const { [sessionId]: _pausing, ...rest } = s.pausing
      return { pausing: rest }
    }
    return { pausing: { ...s.pausing, [sessionId]: true } }
  }),

  setStepContextFill: (sessionId, stepId, fill) => set((s) => ({
    stepContextFill: {
      ...s.stepContextFill,
      [sessionId]: { ...s.stepContextFill[sessionId], [stepId]: fill },
    },
  })),

  // Clears one session's step fills (absent key = nothing to do); other
  // sessions' fills survive.
  clearStepContextFill: (sessionId) => set((s) => {
    if (!(sessionId in s.stepContextFill)) return s
    const { [sessionId]: _fills, ...rest } = s.stepContextFill
    return { stepContextFill: rest }
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
}))
