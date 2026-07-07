import { useMemo } from 'react'
import { create } from 'zustand'
import type { ChatMessageUI } from '@/types/messages'
import type { TokenInfo } from '@/types/models'
import { generateMessageId } from '@/lib/ids'

// Re-export types and grouping functions so existing imports continue to work
export type { MessageType, ChatMessageUI, DisplayItem, GroupedMessages } from '@/types/messages'
export { groupMessages } from '@/lib/chatUtils'

// --- State types ---

interface ChatState {
  // Messages indexed by session: sessionId -> messageId -> message
  messages: Record<string, Record<string, ChatMessageUI>>
  // Ordered message IDs per session: sessionId -> messageId[]
  messageOrder: Record<string, string[]>
  // Streaming
  streamingText: string | null
  streamingSessionId: string | null
  // Activity
  activityStatus: string | null
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
  setStreamingText: (text: string, sessionId: string) => void
  appendStreamingText: (delta: string) => void
  flushStreaming: () => ChatMessageUI | null
  clearStreamingText: () => void
  setActivityStatus: (status: string | null) => void
  setTaskActive: (sessionId: string, active: boolean) => void
  setStepContextFill: (stepId: string, fill: number) => void
  clearStepContextFill: () => void
  setSessionTokens: (sessionId: string, tokens: TokenInfo) => void
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

export const useChatStore = create<ChatState & ChatActions>((set, get) => ({
  messages: {},
  messageOrder: {},
  streamingText: null,
  streamingSessionId: null,
  activityStatus: null,
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
  mergeHistoryMessages: (sessionId, history, loadStartedAt) => set((s) => {
    const liveIndex = s.messages[sessionId] ?? {}
    const liveOrder = s.messageOrder[sessionId] ?? []
    const historyIds = new Set(history.map(m => m.id))
    const preserved: ChatMessageUI[] = []
    for (const id of liveOrder) {
      const msg = liveIndex[id]
      if (!msg || historyIds.has(id)) continue
      if (msg.timestamp >= loadStartedAt) preserved.push(msg)
    }
    const merged = [...history, ...preserved]
    return {
      messages: { ...s.messages, [sessionId]: indexMessages(merged) },
      messageOrder: { ...s.messageOrder, [sessionId]: merged.map(m => m.id) },
    }
  }),

  setStreamingText: (text, sessionId) => set({
    streamingText: text,
    streamingSessionId: sessionId,
  }),

  appendStreamingText: (delta) => set((s) => ({
    streamingText: (s.streamingText ?? '') + delta,
  })),

  flushStreaming: () => {
    const { streamingText, streamingSessionId } = get()
    if (!streamingText || !streamingSessionId) {
      set({ streamingText: null, streamingSessionId: null })
      return null
    }
    const message: ChatMessageUI = {
      id: generateMessageId(),
      sessionId: streamingSessionId,
      type: 'assistant',
      content: streamingText,
      timestamp: Date.now(),
    }
    // Add to store and clear streaming
    const s = get()
    const sessionIndex = s.messages[streamingSessionId] ?? {}
    const sessionOrder = s.messageOrder[streamingSessionId] ?? []
    set({
      messages: {
        ...s.messages,
        [streamingSessionId]: { ...sessionIndex, [message.id]: message },
      },
      messageOrder: {
        ...s.messageOrder,
        [streamingSessionId]: [...sessionOrder, message.id],
      },
      streamingText: null,
      streamingSessionId: null,
    })
    return message
  },

  clearStreamingText: () => set({
    streamingText: null,
    streamingSessionId: null,
  }),

  setActivityStatus: (status) => set({ activityStatus: status }),

  setTaskActive: (sessionId, active) => set((s) => ({
    taskActive: { ...s.taskActive, [sessionId]: active },
  })),

  setStepContextFill: (stepId, fill) => set((s) => ({
    stepContextFill: { ...s.stepContextFill, [stepId]: fill },
  })),

  clearStepContextFill: () => set({ stepContextFill: {} }),

  setSessionTokens: (sessionId, tokens) => set((s) => ({
    sessionTokens: { ...s.sessionTokens, [sessionId]: tokens },
  })),

  clearSession: (sessionId) => set((s) => {
    const { [sessionId]: _msgs, ...restMessages } = s.messages
    const { [sessionId]: _order, ...restOrder } = s.messageOrder
    const { [sessionId]: _active, ...restActive } = s.taskActive
    const { [sessionId]: _tokens, ...restTokens } = s.sessionTokens
    return {
      messages: restMessages,
      messageOrder: restOrder,
      taskActive: restActive,
      sessionTokens: restTokens,
      activityStatus: null,
      streamingText: null,
      streamingSessionId: null,
      stepContextFill: {},
    }
  }),
}))
