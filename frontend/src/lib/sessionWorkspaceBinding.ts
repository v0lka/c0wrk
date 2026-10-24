import type { ProjectInfo, SessionInfo } from '@/types/models'

/** Store state the workspace-root rule resolves against. */
export interface WorkspaceRootContext {
  activeProject: ProjectInfo | null
  activeProjectId: string | null
  activeSessionId: string | null
  sessions: SessionInfo[] | null | undefined
}

export interface ManagedBindingResolution {
  /**
   * The active session is a managed-worktree session of the active (real)
   * project — its own tree owns the workspace root.
   */
  isManaged: boolean
  /**
   * The binding's tree path; '' while the binding row has not landed in the
   * sessions list yet (the caller decides how to await it).
   */
  bindingPath: string
}

/**
 * The binding-first rule of the "workspace root follows the active session"
 * protocol, in ONE place: the ACTIVE session's managed-worktree binding owns
 * the root when the session belongs to the active (real) project. Anything
 * else — a foreign session, an absent active project, No Project, a local or
 * missing binding — is not managed, and the caller applies its own
 * project-level fallback (no cross-project leak).
 *
 * Shared by useWorkspacePath (viewer/chat links), resolveChatWorkspaceRoot
 * and the file tree so the three surfaces cannot drift.
 *
 * Rule: specs/domains/session-worktrees.md (User-facing workspace roots);
 * specs/decisions/095-chat-file-reference-path-semantics.md (Decision 6).
 */
export function resolveManagedBinding(ctx: WorkspaceRootContext): ManagedBindingResolution {
  const session = ctx.activeSessionId
    ? (ctx.sessions?.find((s) => s.id === ctx.activeSessionId) ?? null)
    : null
  if (!session || !ctx.activeSessionId || !ctx.activeProject || ctx.activeProject.is_no_project) {
    return { isManaged: false, bindingPath: '' }
  }
  if (session.project_id !== ctx.activeProjectId) {
    return { isManaged: false, bindingPath: '' }
  }
  const binding = session.workspace_binding
  if (binding?.kind !== 'managed_worktree') {
    return { isManaged: false, bindingPath: '' }
  }
  return { isManaged: true, bindingPath: binding.workspace_path }
}
