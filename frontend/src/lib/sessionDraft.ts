import type { SessionInfo } from '@/types/models'
import { createSession, createManagedSession } from '@/api/sessions'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionDraftStore } from '@/stores/sessionDraftStore'
import { logger } from '@/lib/logger'

/**
 * Create the next session, honoring the armed session draft.
 *
 * Called by every "ensure a session exists" site (send, attachment staging,
 * paste, terminal open). With a `branch` draft the session is created through
 * `CreateManagedSession` — the backend validates the binding, provisions the
 * managed worktree under `<repo>/.worktrees`, and compensates on failure —
 * otherwise a plain local session is created exactly like the pre-draft
 * flow. A draft belonging to a different project than the active one is
 * ignored and cleared (stale after a project switch).
 *
 * On success the session is added, selected (persisted as the project's
 * saved session, mirroring the previous auto-create sites) and the draft is
 * cleared; on failure the draft stays armed so the user can retry or re-pick
 * the branch, and the error propagates to the caller's existing handling.
 */
export async function createSessionFromDraft(): Promise<SessionInfo> {
  const activeProjectId = useProjectStore.getState().activeProjectId
  const draft = useSessionDraftStore.getState().draft
  const active = draft !== null && draft.projectId === activeProjectId ? draft : null
  if (draft !== null && draft !== active) {
    // Stale draft from another project — drop it so it cannot resurface.
    useSessionDraftStore.getState().clearDraft()
  }

  useSessionDraftStore.getState().setCommitting(true)
  try {
    const session =
      active !== null && active.workspace.kind === 'branch'
        ? await createManagedSession(
            active.workspace.branch,
            active.workspace.createBranch,
            active.workspace.startPoint,
          )
        : await createSession()
    useSessionStore.getState().addSession(session)
    useSessionStore.getState().selectSession(session.id, session.project_id)
    useSessionDraftStore.getState().clearDraft()
    return session
  } catch (err) {
    logger.error('Failed to create session from draft:', err)
    throw err
  } finally {
    useSessionDraftStore.getState().setCommitting(false)
  }
}
