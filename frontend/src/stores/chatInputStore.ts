// Zustand store for the per-session chat-input state: the message draft,
// the prompt-optimization in-flight flag, the prompt-optimization error, and
// the send error.
//
// Keying by session id means:
//  - typed text survives session switches, project switches, and CHAT↔CODE
//    mode switches (each of those changes the active session id);
//  - an Optimize round-trip that completes after the user switched sessions
//    lands its result (or restores its original text on failure) in the
//    session captured at click time — never in the editor of another
//    session;
//  - the toolbar spinner reflects the ACTIVE session's optimize flag only.
//
// Stable selectors: every consumer selects primitives (draft / flags / error)
// or the direct slice reference — never an object allocated inside a
// selector. React 19's useSyncExternalStore compares snapshots by reference;
// a fresh object on every call would trigger an infinite re-render loop
// (error #185).

import { create } from 'zustand'

/**
 * Reserved record key under which the draft typed while NO session is active
 * is stored (a project can be selected without a session, and the input is
 * editable then — sends auto-create the session). Real session ids are
 * UUID-like strings, so '' can never collide. The sentinel keeps `hasContent`
 * derived purely from the store and preserves that scratch text across
 * switches away and back. It is intentionally NOT dropped by session
 * deletion — it is not a session.
 */
export const NULL_SESSION_KEY = ''

/**
 * Per-session chat-input slice: draft text, optimize-in-flight flag, the
 * last optimize error (drives both the toolbar's transient inline error and
 * the persistent dismissible banner), and the last send error (keyed to the
 * session the failed send originated from, so it never bleeds into another
 * session's input). Transient — NOT persisted.
 */
export interface ChatInputSessionState {
  /** Current draft text of the session's message input. */
  draft: string
  /** True while a prompt optimization for this session is in flight. */
  isOptimizing: boolean
  /** Last optimization error for this session; null = none. */
  optimizeError: string | null
  /** Last send error for this session; null = none. */
  sendError: string | null
}

/**
 * Default per-session slice, used when a session has no entry yet.
 * Referentially stable (module constant) so consumers can derive defaults
 * without allocating inside Zustand selectors.
 */
export const EMPTY_CHAT_INPUT: ChatInputSessionState = {
  draft: '',
  isOptimizing: false,
  optimizeError: null,
  sendError: null,
}

interface ChatInputState {
  /** Per-session input slices, keyed by session id (or NULL_SESSION_KEY). */
  inputs: Record<string, ChatInputSessionState>
}

interface ChatInputActions {
  /** Update the draft for a session (every keystroke). */
  setDraft: (sessionId: string, draft: string) => void
  /** Set/clear the optimize-in-flight flag for a session. */
  setOptimizing: (sessionId: string, optimizing: boolean) => void
  /** Set/clear the optimize error for a session. */
  setOptimizeError: (sessionId: string, message: string | null) => void
  /** Set/clear the send error for a session. */
  setSendError: (sessionId: string, message: string | null) => void
  /** Drop the slices of deleted sessions (keeps the map bounded). */
  dropSessions: (sessionIds: string[]) => void
}

/** Resolve a session's slice (null → the NULL_SESSION_KEY sentinel), or the
 *  stable empty default when absent. Never allocates. */
export function getInputState(
  inputs: Record<string, ChatInputSessionState>,
  sessionId: string | null,
): ChatInputSessionState {
  return inputs[sessionId ?? NULL_SESSION_KEY] ?? EMPTY_CHAT_INPUT
}

export const useChatInputStore = create<ChatInputState & ChatInputActions>()((set) => ({
  inputs: {},

  setDraft: (sessionId, draft) =>
    set((s) => {
      const current = s.inputs[sessionId]
      // No-op writes (keystroke echo of the same text, clearing an absent
      // slice) keep the record reference stable so subscribers don't churn.
      if ((current ?? EMPTY_CHAT_INPUT).draft === draft) return s
      return {
        inputs: {
          ...s.inputs,
          [sessionId]: { ...(current ?? EMPTY_CHAT_INPUT), draft },
        },
      }
    }),

  setOptimizing: (sessionId, optimizing) =>
    set((s) => {
      const current = s.inputs[sessionId]
      if ((current ?? EMPTY_CHAT_INPUT).isOptimizing === optimizing) return s
      return {
        inputs: {
          ...s.inputs,
          [sessionId]: { ...(current ?? EMPTY_CHAT_INPUT), isOptimizing: optimizing },
        },
      }
    }),

  setOptimizeError: (sessionId, message) =>
    set((s) => {
      const current = s.inputs[sessionId]
      if ((current ?? EMPTY_CHAT_INPUT).optimizeError === message) return s
      return {
        inputs: {
          ...s.inputs,
          [sessionId]: { ...(current ?? EMPTY_CHAT_INPUT), optimizeError: message },
        },
      }
    }),

  setSendError: (sessionId, message) =>
    set((s) => {
      const current = s.inputs[sessionId]
      if ((current ?? EMPTY_CHAT_INPUT).sendError === message) return s
      return {
        inputs: {
          ...s.inputs,
          [sessionId]: { ...(current ?? EMPTY_CHAT_INPUT), sendError: message },
        },
      }
    }),

  dropSessions: (sessionIds) =>
    set((s) => {
      let inputs = s.inputs
      for (const id of sessionIds) {
        if (id in inputs) {
          if (inputs === s.inputs) inputs = { ...s.inputs }
          delete inputs[id]
        }
      }
      if (inputs === s.inputs) return s
      return { inputs }
    }),
}))
