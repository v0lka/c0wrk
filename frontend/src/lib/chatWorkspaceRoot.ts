// Resolves the workspace root that relative file links in chat must be
// resolved against.

import { getSessionWorkspace } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { resolveManagedBinding } from '@/lib/sessionWorkspaceBinding'

/**
 * Resolves the workspace root for relative file links rendered in the chat.
 * The root follows the active session's own workspace when it carries one (a
 * managed worktree of the active project); the branches below only resolve a
 * transient desync between the active session and the file tree after a
 * project switch.
 *
 * Rule: specs/decisions/095-chat-file-reference-path-semantics.md (Decision 6);
 * specs/domains/session-worktrees.md (User-facing workspace roots).
 */
export async function resolveChatWorkspaceRoot(): Promise<string | null> {
  const sessionState = useSessionStore.getState()
  const projectState = useProjectStore.getState()
  const treeRoot = useFileTreeStore.getState().rootPath

  const activeSessionId = sessionState.activeSessionId
  const session = activeSessionId
    ? (sessionState.sessions?.find((s) => s.id === activeSessionId) ?? null)
    : null

  const projects = projectState.projects
  const activeProject =
    projects?.find((p) => p.id === projectState.activeProjectId) ?? null
  const sessionProject = session?.project_id
    ? (projects?.find((p) => p.id === session.project_id) ?? null)
    : null

  // A managed-worktree session of the active project resolves against its
  // own tree — the shared binding-first rule (see resolveManagedBinding);
  // foreign/absent active projects fall through (no cross-project leak).
  const binding = resolveManagedBinding({
    activeProject,
    activeProjectId: projectState.activeProjectId,
    activeSessionId,
    sessions: sessionState.sessions,
  })
  if (binding.isManaged && activeSessionId) {
    if (binding.bindingPath) return binding.bindingPath
    try {
      return await getSessionWorkspace(activeSessionId)
    } catch {
      return treeRoot
    }
  }

  // A concrete (real-project) session owns its links.
  if (sessionProject && !sessionProject.is_no_project) {
    return sessionProject.workspace_path || treeRoot
  }

  // The active project is a real project. It wins over a No Project (or
  // unknown/stale) session — the file tree is authoritative in this case.
  if (activeProject && !activeProject.is_no_project) {
    return activeProject.workspace_path || treeRoot
  }

  // The active project is No Project (or absent). The backend knows the
  // session's actual workspace, whether the session is a consistent No Project
  // session or belongs to a real project but is not yet in the session list.
  if (activeSessionId) {
    try {
      return await getSessionWorkspace(activeSessionId)
    } catch {
      return treeRoot
    }
  }

  return treeRoot
}
