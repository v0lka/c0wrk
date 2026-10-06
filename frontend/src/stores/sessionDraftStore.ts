import { create } from 'zustand'

// The pending "New Session" draft for CODE projects (ADR-080): clicking New
// Session arms a draft instead of creating a session row. No orchestrator,
// terminal, or DB row exists while the draft is armed — it is committed by
// the first real action (send / attachment / terminal open) through
// lib/sessionDraft.ts's createSessionFromDraft, which creates either a plain
// local session or a managed-worktree session provisioned on the drafted
// branch via CreateManagedSession.
//
// The store is deliberately transient (never persisted): an un-committed
// draft has no backend identity, so an app restart starts fresh.


/**
 * The execution workspace the drafted session will be created with.
 *
 * - `local` — the project checkout (the pre-draft default).
 * - `branch` — a session-owned managed worktree pinned to `branch`. When
 *   `createBranch` is true the branch is CREATED at `startPoint` (empty =
 *   repository HEAD) by the provisioning path instead of being required to
 *   already exist — mirroring CreateManagedSession's contract.
 */
export type SessionDraftWorkspace =
  | { readonly kind: 'local' }
  | {
      readonly kind: 'branch'
      readonly branch: string
      readonly createBranch: boolean
      readonly startPoint: string
    }

/** A pending session draft: the owning project plus the drafted workspace. */
export interface SessionDraft {
  readonly projectId: string
  readonly workspace: SessionDraftWorkspace
}

// --- State types ---

interface SessionDraftState {
  /** The armed draft, or null when the next created session is plain local. */
  draft: SessionDraft | null
  /** True while the draft's creation RPC is in flight (guards double-commit). */
  committing: boolean
  /**
   * Arm a fresh draft for a CODE project (the New Session gesture). Resets
   * any previous workspace choice — every New Session starts `local`.
   */
  startDraft: (projectId: string) => void
  /**
   * Replace the drafted workspace. No-op when no draft is armed (a bare
   * no-session state already means "next session is local").
   */
  setDraftWorkspace: (workspace: SessionDraftWorkspace) => void
  /** Drop the draft (commit succeeded, another session was selected, …). */
  clearDraft: () => void
  /** Mark the draft's creation RPC in flight / settled. */
  setCommitting: (committing: boolean) => void
}

// --- Store ---

export const useSessionDraftStore = create<SessionDraftState>((set) => ({
  draft: null,
  committing: false,

  startDraft: (projectId) =>
    set({ draft: { projectId, workspace: { kind: 'local' } } }),

  setDraftWorkspace: (workspace) =>
    set((s) =>
      s.draft === null ? s : { draft: { ...s.draft, workspace } },
    ),

  clearDraft: () => set({ draft: null }),

  setCommitting: (committing) => set({ committing }),
}))
