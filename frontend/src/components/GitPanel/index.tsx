import { useCallback } from 'react'
import { GitBranch, AlertCircle } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useGitStatusEvents } from '@/hooks/useGitStatusEvents'
import { getGitStatus } from '@/api/workspace'
import { getFileDiff } from '@/api/workspace'
import { stageFile, unstageFile, getCurrentBranch } from '@/api/git'
import { GitPanelToolbar } from './GitPanelToolbar'
import { ChangesList } from './ChangesList'
import { CommitSection } from './CommitSection'
import { BranchPicker } from './BranchPicker'
import type { GitPanelEntry } from '@/stores/gitPanelStore'
import type { GitStatusEntry } from '@/types/models'

// ─────────────────────────────── Helpers ─────────────────────────────────────

/** Convert backend GitStatusEntry map into store-compatible GitPanelEntry[]. */
function toEntries(
  statusMap: Record<string, GitStatusEntry>,
): GitPanelEntry[] {
  return Object.entries(statusMap).map(([path, entry]) => ({
    path,
    status: entry.status,
    staged: entry.staged,
    diffStat: null,
  }))
}

/** Resolve the current workspace path from the project store. */
function getWorkspacePath(): string | null {
  const { projects, activeProjectId } = useProjectStore.getState()
  if (!activeProjectId || !projects) return null
  return (
    projects.find((p) => p.id === activeProjectId)?.workspace_path ?? null
  )
}

// ─────────────────────────────── Component ───────────────────────────────────

export function GitPanel() {
  // Side-effect hook: subscribes to git:status_changed events and
  // keeps the store in sync with the backend. Returns void.
  useGitStatusEvents()

  // Stable individual selectors — each only triggers re-render when its
  // specific slice changes (prevents infinite re-render loops per AGENTS.md).
  const isGitRepo = useGitPanelStore((s) => s.isGitRepo)
  const branch = useGitPanelStore((s) => s.branch)
  const viewMode = useGitPanelStore((s) => s.viewMode)
  const isLoading = useGitPanelStore((s) => s.isLoading)
  const error = useGitPanelStore((s) => s.error)

  // ── Callbacks ──────────────────────────────────────────────────────────

  /** Toggle staged/unstaged state of a file. */
  const onToggleFile = useCallback(async (path: string) => {
    const entry = useGitPanelStore.getState().entries.find(
      (e) => e.path === path,
    )
    if (!entry) return

    try {
      if (entry.staged) {
        await unstageFile(path)
      } else {
        await stageFile(path)
      }
      // The backend emits `git:status_changed` after StageFile/UnstageFile,
      // which is picked up by useGitStatusEvents — no manual refresh needed.
    } catch (err) {
      console.error('Failed to toggle file stage:', err)
      useGitPanelStore.getState().setError(
        err instanceof Error ? err.message : 'Failed to toggle file stage',
      )
    }
  }, [])

  /** Open a file diff in the FileViewerPanel. */
  const onOpenDiff = useCallback(async (path: string) => {
    const viewerStore = useFileViewerStore.getState()
    // Create the file entry synchronously so the tab exists
    viewerStore.openFile(path)

    try {
      const diff = await getFileDiff(path)
      viewerStore.setFileDiff(path, diff)
    } catch (err) {
      console.error('Failed to load file diff:', err)
      viewerStore.setFileError(path,
        err instanceof Error ? err.message : 'Failed to load diff',
      )
    }
  }, [])

  /** Manually refresh git status and branch from the backend. */
  const onRefresh = useCallback(async () => {
    const workspacePath = getWorkspacePath()
    if (!workspacePath) return

    const store = useGitPanelStore.getState()
    store.setLoading(true)
    store.setError(null)

    try {
      const statusMap = await getGitStatus(workspacePath)
      store.loadEntries(toEntries(statusMap))
      store.setGitRepo(true)

      // Fetch branch name (non-critical — best-effort)
      try {
        const branchName = await getCurrentBranch()
        store.setBranch(branchName)
      } catch {
        store.setBranch('')
      }
    } catch {
      store.setGitRepo(false)
      store.loadEntries([])
      store.setError('Failed to load git status')
    } finally {
      store.setLoading(false)
    }
  }, [])

  /** Switch between flat and tree view modes. */
  const onViewModeChange = useCallback((mode: 'flat' | 'tree') => {
    useGitPanelStore.getState().setViewMode(mode)
  }, [])

  // ── "Not a git repository" state ───────────────────────────────────────

  if (!isGitRepo && !isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <div className="flex flex-col items-center gap-2 text-muted-foreground select-none">
          <GitBranch className="size-8 opacity-30" />
          <span className="text-sm">Not a git repository</span>
        </div>
      </div>
    )
  }

  // ── Main layout ────────────────────────────────────────────────────────

  return (
    <div className="flex flex-col h-full min-h-0">
      <GitPanelToolbar
        branch={branch}
        viewMode={viewMode}
        onViewModeChange={onViewModeChange}
        onRefresh={onRefresh}
      />
      {error && (
        <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20">
          <AlertCircle className="size-3.5 shrink-0" />
          <span className="truncate">{error}</span>
        </div>
      )}
      <ChangesList onToggleFile={onToggleFile} onOpenDiff={onOpenDiff} />
      <CommitSection />
      <BranchPicker />
    </div>
  )
}
