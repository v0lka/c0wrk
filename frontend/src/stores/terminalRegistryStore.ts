import { create } from 'zustand'

// Per-session terminal instance registry.
//
// Terminals are kept alive for the whole app lifetime: every session for
// which the terminal was ever opened keeps its Terminal component (xterm.js
// instance + backend PTY) mounted, hidden while inactive. Switching sessions
// or projects never touches this registry — the session list in sessionStore
// is scoped to the active project, so deriving liveness from it would tear
// down terminals of other projects' sessions.
//
// Entries are removed ONLY on explicit deletion (useSessionActions.handleDelete
// for a session, ProjectSelector.handleDelete for all sessions of a project) —
// mirroring the backend, which stops the PTYs at exactly those points.

interface TerminalRegistryState {
  /** Insertion-ordered session IDs with a mounted terminal instance. */
  instances: readonly string[]
  /** Sessions whose terminal finished (or failed) its initial start —
   * drives the loading overlay for the active instance. */
  readySessions: ReadonlySet<string>
}

interface TerminalRegistryActions {
  /** Register a session's terminal instance (no-op when already present). */
  ensureInstance: (sessionId: string) => void
  /** Mark a session's initial terminal start as finished. */
  markReady: (sessionId: string) => void
  /** Drop instances — called when sessions are explicitly deleted. */
  removeInstances: (sessionIds: readonly string[]) => void
}

export const useTerminalRegistryStore = create<TerminalRegistryState & TerminalRegistryActions>()((set) => ({
  instances: [],
  readySessions: new Set<string>(),

  ensureInstance: (sessionId) =>
    set((s) =>
      s.instances.includes(sessionId)
        ? s
        : { instances: [...s.instances, sessionId] },
    ),

  markReady: (sessionId) =>
    set((s) => {
      if (s.readySessions.has(sessionId)) return s
      const readySessions = new Set(s.readySessions)
      readySessions.add(sessionId)
      return { readySessions }
    }),

  removeInstances: (sessionIds) =>
    set((s) => {
      const drop = new Set(sessionIds)
      const instances = s.instances.filter((id) => !drop.has(id))
      if (instances.length === s.instances.length) return s
      let readyChanged = false
      const readySessions = new Set<string>()
      for (const id of s.readySessions) {
        if (!drop.has(id)) {
          readySessions.add(id)
        } else {
          readyChanged = true
        }
      }
      return readyChanged || readySessions.size !== s.readySessions.size
        ? { instances, readySessions }
        : { instances }
    }),
}))
