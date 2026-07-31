import { useEffect, useMemo, useState } from 'react'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { getSessionWorkspace } from '@/api/workspace'

/**
 * Resolves the active workspace root path.
 *
 * For a regular project this is the project's workspace_path. For No Project
 * (CHAT mode) each session has its own isolated workspace, so the per-session
 * workspace is fetched asynchronously and the project-level path is used only
 * as a fallback while loading.
 *
 * Returns `null` while the path is unavailable.
 */
export function useWorkspacePath(): string | null {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)

  const isNoProject = useProjectStore(selectIsNoProject)

  // Project-level workspace path (correct for regular projects; for No
  // Project this is the empty placeholder directory).
  const projectWorkspacePath = useMemo(() => {
    if (!activeProjectId || !projects) return null
    return projects.find((p) => p.id === activeProjectId)?.workspace_path ?? null
  }, [activeProjectId, projects])

  // For No Project, each session has its own isolated workspace. Fetch it.
  const [sessionWorkspacePath, setSessionWorkspacePath] = useState<string | null>(null)
  useEffect(() => {
    if (!isNoProject || !activeSessionId) {
      setSessionWorkspacePath(null)
      return
    }
    let cancelled = false
    getSessionWorkspace(activeSessionId)
      .then((wsPath) => { if (!cancelled) setSessionWorkspacePath(wsPath) })
      .catch(() => { /* keep previous value on error; fall back to project workspace */ })
    return () => { cancelled = true }
  }, [isNoProject, activeSessionId])

  return isNoProject ? (sessionWorkspacePath ?? projectWorkspacePath) : projectWorkspacePath
}
