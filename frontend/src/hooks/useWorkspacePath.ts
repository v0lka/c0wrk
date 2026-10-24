import { useEffect, useMemo, useState } from 'react'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { getSessionWorkspace } from '@/api/workspace'
import { resolveManagedBinding } from '@/lib/sessionWorkspaceBinding'

/**
 * The workspace-root resolution shared by every user-facing consumer (the
 * file tree, chat links, the viewer's relative paths). The root follows the
 * active session's own workspace — its managed worktree, or the per-session
 * No Project workspace — and the project workspace_path otherwise.
 *
 * `pending` is true while a session is active and the root has not settled
 * yet (a managed binding whose path has not landed, a session missing from
 * the list, or a No Project session whose RPC is in flight). Callers that
 * would re-root a live view (the file tree re-scopes the backend watcher)
 * MUST NOT fall back to the project-level path while pending: a transient
 * wrong root both flashes the UI and re-scopes the watcher onto the wrong
 * tree, dropping the real tree's events until an unrelated change.
 *
 * Rule: specs/domains/session-worktrees.md (User-facing workspace roots);
 * specs/decisions/095-chat-file-reference-path-semantics.md (Decision 6).
 */
export interface WorkspaceRootState {
  /** The resolved root, or null while pending (or when nothing resolves). */
  path: string | null
  /** True while a session is active and the root has not settled yet. */
  pending: boolean
}

// Bounded retry for the session-workspace RPC: a transiently unavailable
// backend must converge instead of permanently pinning the caller to its
// fallback. Backoff between attempts: 1s, 2s, 3s, 4s.
const sessionRpcMaxRetries = 5

export function useWorkspaceRootState(): WorkspaceRootState {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const sessions = useSessionStore((s) => s.sessions)
  const isNoProject = useProjectStore(selectIsNoProject)

  // Project-level workspace path (correct for regular projects; for No
  // Project this is the empty placeholder directory, which is NEVER exposed
  // as a root — see the isNoProject guard in `path` below).
  const activeProject = useMemo(
    () => projects?.find((p) => p.id === activeProjectId) ?? null,
    [projects, activeProjectId],
  )
  const projectWorkspacePath = activeProject?.workspace_path ?? null

  // The binding-first rule, shared with resolveChatWorkspaceRoot: a landed
  // managed-worktree binding of the active project IS the root — no RPC, no
  // fallback. Foreign sessions, No Project and local bindings fall through.
  const binding = useMemo(
    () => resolveManagedBinding({ activeProject, activeProjectId, activeSessionId, sessions }),
    [activeProject, activeProjectId, activeSessionId, sessions],
  )

  // A session absent from the (loaded or still-loading) list cannot be
  // classified locally — the backend's authoritative RPC decides.
  const sessionKnown = useMemo(
    () => (activeSessionId ? (sessions?.some((s) => s.id === activeSessionId) ?? false) : true),
    [activeSessionId, sessions],
  )

  // The RPC covers every case the sessions list cannot answer instantly: an
  // unknown session, a managed binding whose path has not landed yet, and
  // every No Project session (its isolated workspace lives outside any
  // project row).
  const needsRpc =
    activeSessionId !== null &&
    (isNoProject || !sessionKnown || (binding.isManaged && binding.bindingPath === ''))

  // RPC result, keyed by the session it belongs to: a delayed answer can
  // never serve another session (anything whose key no longer matches is
  // masked below — the same guarantee FileTreePanel's cancelled flag used to
  // provide, derived instead of stored).
  const [rpc, setRpc] = useState<{ forSession: string | null; path: string | null; settled: boolean }>({
    forSession: null,
    path: null,
    settled: false,
  })

  useEffect(() => {
    if (!needsRpc || !activeSessionId) return
    const sessionId = activeSessionId
    let cancelled = false
    let attempts = 0
    let retryTimer: ReturnType<typeof setTimeout> | null = null

    async function fetchWithRetry(): Promise<void> {
      while (attempts < sessionRpcMaxRetries && !cancelled) {
        try {
          const wsPath = await getSessionWorkspace(sessionId)
          if (!cancelled) setRpc({ forSession: sessionId, path: wsPath, settled: true })
          return
        } catch {
          attempts++
          if (attempts < sessionRpcMaxRetries && !cancelled) {
            await new Promise<void>((resolve) => {
              retryTimer = setTimeout(resolve, 1000 * attempts)
            })
          }
        }
      }
      // Retries exhausted: settle on the fallback (project path for a real
      // project, no tree for No Project) instead of pending forever.
      if (!cancelled) setRpc({ forSession: sessionId, path: null, settled: true })
    }
    void fetchWithRetry()
    return () => {
      cancelled = true
      if (retryTimer !== null) clearTimeout(retryTimer)
    }
  }, [needsRpc, activeSessionId])

  const rpcForSession = rpc.forSession === activeSessionId ? rpc : null
  const pending = needsRpc && rpcForSession?.settled !== true

  const path = binding.isManaged && binding.bindingPath
    ? binding.bindingPath
    : pending
      ? null
      : (rpcForSession?.path || (isNoProject ? null : projectWorkspacePath))

  return { path, pending }
}

/**
 * Resolves the active workspace path. The path follows the active session's
 * own workspace when it carries one — a managed worktree of the active
 * project, or the per-session No Project workspace — and the project
 * workspace_path otherwise. Returns `null` while the path is unavailable.
 */
export function useWorkspacePath(): string | null {
  return useWorkspaceRootState().path
}
