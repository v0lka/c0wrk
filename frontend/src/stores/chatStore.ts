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
  // Context fill per step: stepId -> fill percent
  stepContextFill: Record<string, number>
  // Session tokens: sessionId -> token info
  sessionTokens: Record<string, TokenInfo>
}

interface ChatActions {
  addMessage: (sessionId: string, message: ChatMessageUI) => void
  updateMessage: (sessionId: string, messageId: string, updates: Partial<ChatMessageUI>) => void
  removeMessage: (sessionId: string, messageId: string) => void
  setMessages: (sessionId: string, messages: ChatMessageUI[]) => void
  mergeHistoryMessages: (sessionId: string, history: ChatMessageUI[], loadStartedAt: number) => void
  setStreamingText: (sessionId: string, text: string) => void
  appendStreamingText: (sessionId: string, delta: string) => void
  clearStreamingText: (sessionId: string) => void
  setActivityStatus: (sessionId: string, status: string | null) => void
  setTaskActive: (sessionId: string, active: boolean) => void
  setStepContextFill: (stepId: string, fill: number) => void
  clearStepContextFill: () => void
  setSessionTokens: (sessionId: string, tokens: Partial<TokenInfo>) => void
  clearSession: (sessionId: string) => void
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
  stepContextFill: {},
  sessionTokens: {},

  addMessage: (sessionId, message) => set((s) => {
    const sessionIndex = s.messages[sessionId] ?? {}
    const sessionOrder = s.messageOrder[sessionId] ?? []
    return {
      messages: {
        ...s.messages,
        [sessionId]: { ...sessionIndex, [message.id]: message },
      },
      messageOrder: {
        ...s.messageOrder,
        [sessionId]: [...sessionOrder, message.id],
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
  // This cannot duplicate a persisted row: the live event handler
  // (hooks/events/hitlHandlers.ts) and the history→UI converter
  // (lib/chatUtilsHelpers.ts) derive the SAME semantic id
  // (`<type>-<request_id|confirm_id>`), and any message already in historyIds
  // is skipped above before this check runs.
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

  setStreamingText: (sessionId, text) => set((s) => ({
    streamingText: { ...s.streamingText, [sessionId]: text },
  })),

  appendStreamingText: (sessionId, delta) => set((s) => {
    const prev = s.streamingText[sessionId] ?? ''
    return { streamingText: { ...s.streamingText, [sessionId]: prev + delta } }
  }),

  clearStreamingText: (sessionId) => set((s) => {
    if (!(sessionId in s.streamingText)) return s
    const { [sessionId]: _stream, ...rest } = s.streamingText
    return { streamingText: rest }
  }),

  setActivityStatus: (sessionId, status) => set((s) => {
    if (status === null || status === undefined) {
      if (!(sessionId in s.activityStatus)) return s
      const { [sessionId]: _status, ...rest } = s.activityStatus
      return { activityStatus: rest }
    }
    return { activityStatus: { ...s.activityStatus, [sessionId]: status } }
  }),

  setTaskActive: (sessionId, active) => set((s) => ({
    taskActive: { ...s.taskActive, [sessionId]: active },
  })),

  setStepContextFill: (stepId, fill) => set((s) => ({
    stepContextFill: { ...s.stepContextFill, [stepId]: fill },
  })),

  clearStepContextFill: () => set({ stepContextFill: {} }),

  // Merge partial token info into the session entry so event-driven updates
  // (context_fill / session_tokens) that omit fill_percent don't clobber it,
  // while the full TokenInfo from GetSessionTokens still replaces everything.
  setSessionTokens: (sessionId, tokens) => set((s) => {
    const existing = s.sessionTokens[sessionId]
    return {
      sessionTokens: { ...s.sessionTokens, [sessionId]: { ...existing, ...tokens } as TokenInfo },
    }
  }),

  clearSession: (sessionId) => set((s) => {
    const { [sessionId]: _msgs, ...restMessages } = s.messages
    const { [sessionId]: _order, ...restOrder } = s.messageOrder
    const { [sessionId]: _active, ...restActive } = s.taskActive
    const { [sessionId]: _tokens, ...restTokens } = s.sessionTokens
    const { [sessionId]: _stream, ...restStream } = s.streamingText
    const { [sessionId]: _status, ...restStatus } = s.activityStatus
    return {
      messages: restMessages,
      messageOrder: restOrder,
      taskActive: restActive,
      sessionTokens: restTokens,
      streamingText: restStream,
      activityStatus: restStatus,
      stepContextFill: {},
    }
  }),
}))
