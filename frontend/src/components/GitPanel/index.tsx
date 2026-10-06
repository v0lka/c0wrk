import { useCallback } from 'react'
import { GitBranch, AlertCircle, FolderTree, FileDiff, History } from 'lucide-react'
import { logger } from '@/lib/logger'
import { useGitPanelStore, selectGitPanelTab } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useGitStatusEvents } from '@/hooks/useGitStatusEvents'
import { getFileDiff } from '@/api/workspace'
import { stageFile, unstageFile } from '@/api/git'
import { runGitOperation } from '@/lib/gitOperation'
import type { StageAction } from '@/lib/gitStatus'
import { GitPanelToolbar } from './GitPanelToolbar'
import { ChangesList } from './ChangesList'
import { CommitSection } from './CommitSection'
import { GitHistoryTab } from './GitHistoryTab'
import { GitPanelFooter } from './GitPanelFooter'
import { GitFocusButton } from './GitFocusButton'
import { FileTreePanel } from '@/components/layout/FileTreePanel'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { useGitFocusSync } from '@/hooks/useGitFocusSync'

// ────────────────────────────────────────────────────────────────────────────

export function GitPanel() {
  // Side-effect hook: subscribes to git:status_changed + workspace:tree_changed
  // events and keeps the store in sync with the backend. Returns void.
  useGitStatusEvents()

  // Side-effect hook: moves the Git-panel FOCUS target automatically on
  // every project/session switch (the active session's execution workspace
  // is the default target). Returns void. Mounted exactly once, here.
  useGitFocusSync()

  // Stable individual selectors — each only triggers re-render when its
  // specific slice changes (prevents infinite re-render loops per AGENTS.md).
  // The git-repo flag is PAIRED with gitRepoProjectId per the store contract:
  // a stale `isGitRepo=true` checked against a previously active project must
  // never leak into the "Not a git repository" decision during rapid project
  // switches. The selector returns a primitive (React #185 safe).
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isGitRepo = useGitPanelStore(
    (s) => s.isGitRepo && s.gitRepoProjectId === activeProjectId,
  )
  const isLoading = useGitPanelStore((s) => s.isLoading)
  const error = useGitPanelStore((s) => s.error)
  // The active tab is per project: derived from the active project id so a
  // project switch instantly shows that project's remembered tab (default
  // 'files' for a first visit) with no transient wrong-section frame.
  const activeTab = useGitPanelStore((s) => selectGitPanelTab(s, activeProjectId))
  const setActiveTab = useGitPanelStore((s) => s.setActiveTab)

  // ── Callbacks ──────────────────────────────────────────────────────────

  /**
   * Toggle a file's stage state along a specific porcelain axis. The action is
   * supplied by the row that fired it — `unstage` when the row's checkbox was
   * checked (an index row), `stage` when it was unchecked (a worktree row) — so
   * no store lookup is required, and a file that is both staged and unstaged
   * (`MM`) resolves correctly per row.
   */
  const onToggleFile = useCallback(async (path: string, action: StageAction): Promise<boolean> => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return false
    // The backend emits `git:status_changed` after StageFile/UnstageFile,
    // which is picked up by useGitStatusEvents — no manual refresh needed.
    // Success is silent (the row moves between sections); only a failure is
    // recorded in the operation console. The boolean result lets the row keep
    // its optimistic checkbox on success and revert it on failure.
    const outcome = await runGitOperation({
      projectId,
      kind: action === 'unstage' ? 'unstage' : 'stage',
      label: `${action === 'unstage' ? 'Unstaged' : 'Staged'} ${path}`,
      fn: () => (action === 'unstage' ? unstageFile(path) : stageFile(path)),
      recordSuccess: false,
      logLevel: 'warn',
    })
    return outcome.ok
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
      logger.error('Failed to load file diff:', err)
      viewerStore.setFileError(path,
        err instanceof Error ? err.message : 'Failed to load diff',
      )
    }
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
      <GitPanelToolbar />
      {/* Files | Changes | History tab switcher with the focus button on the
          right. "files" hosts the workspace file explorer (filter bar + tree)
          as the FIRST section — the workspace-level Explorer tab does not
          exist for git projects, so this is where the explorer lives. Graph
          was merged into History. The shared app-wide segmented control
          (ARIA tabs). The crosshair button focuses the Git panel on the
          current session's worktree (highlighted while diverged). */}
      <div className="@container flex shrink-0 items-center border-b border-border bg-secondary/20 px-1.5 py-1">
        <div className="min-w-0 flex-1">
          <SegmentedControl
            items={[
              { value: 'files', label: 'Files', icon: <FolderTree className="size-4" /> },
              { value: 'changes', label: 'Changes', icon: <FileDiff className="size-4" /> },
              { value: 'history', label: 'History', icon: <History className="size-4" /> },
            ]}
            value={activeTab}
            onValueChange={(tab) => {
              if (activeProjectId !== null) setActiveTab(activeProjectId, tab)
            }}
            fullWidth
            size="md"
            ariaLabel="Git panel view"
            labelClassName="hidden @min-[272px]:inline"
            className="p-0"
          />
        </div>
        <GitFocusButton />
      </div>
      {error && (
        <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20">
          <AlertCircle className="size-3.5 shrink-0" />
          <span className="truncate">{error}</span>
        </div>
      )}
      {activeTab === 'files' ? (
        <FileTreePanel />
      ) : activeTab === 'changes' ? (
        <>
          <ChangesList onToggleFile={onToggleFile} onOpenDiff={onOpenDiff} />
          <CommitSection />
        </>
      ) : (
        <GitHistoryTab />
      )}
      <GitPanelFooter />
    </div>
  )
}
