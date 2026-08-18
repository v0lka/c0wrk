// Resolves the workspace root that relative file links in chat must be
// resolved against.

import { getSessionWorkspace } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'

/**
 * Resolves the workspace root for relative file links rendered in the chat.
 *
 * A relative link in chat must resolve against the workspace the user is
 * actually looking at. The file tree (driven by the active project) and the
 * active session can transiently diverge after a project switch, so neither one
 * is unconditionally authoritative. The rules below handle both directions of
 * that desync:
 *
 *  1. A concrete (real-project) session always owns its links. Even when the
 *     file tree is still showing No Project, a relative link in a real
 *     project's chat must resolve against that project's workspace.
 *  2. When the active project is a real project, it wins over a No Project (or
 *     unknown/stale) session. This is the opposite desync: the file tree shows
 *     the correct project while `activeSessionId` still points at a No Project
 *     session. Resolving against the stale No Project session workspace would
 *     produce paths like
 *     `~/.c0wrk/projects/__no_project__/<sid>/workspace/<relative-path>`.
 *  3. Otherwise the active project is No Project (or absent). Ask the backend
 *     for the active session's workspace — this resolves both a consistent
 *     No Project session (per-session workspace) and a session that belongs to
 *     a real project but is not yet present in the session list.
 *  4. The file-tree root is the final fallback.
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
