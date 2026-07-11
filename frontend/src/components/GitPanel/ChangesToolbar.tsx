import { List, FolderTree, Loader2, Plus, Minus, Ban } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useGitToolbarActions } from '@/hooks/useGitToolbarActions'
import { GitStashButtons } from './GitStashButtons'

/**
 * Toolbar for the "Changes" section (D8/Phase 5/6).
 *
 * Hosts the bulk change operations that only make sense while viewing the
 * working-tree changes: abort merge/rebase, stage/unstage all, stash
 * create/pop, plus the flat/tree view-mode toggle and a shared error
 * indicator. Rendered above `SortGroupControls` inside `ChangesList`.
 *
 * The branch indicator lives in `GitPanelToolbar` (always visible across
 * tabs); everything else that used to share that toolbar was moved here so
 * the branch indicator can span the full panel width.
 *
 * All mutations go through `@/api/*` wrappers (via `useGitToolbarActions`);
 * the backend emits `git:status_changed` after each so `useGitStatusEvents`
 * refreshes the store automatically — no manual refresh is needed here.
 */
export function ChangesToolbar() {
  const viewMode = useGitPanelStore((s) => s.viewMode)
  const setViewMode = useGitPanelStore((s) => s.setViewMode)
  const mergeRebaseState = useGitPanelStore((s) => s.mergeRebaseState)
  const {
    isStagingAll,
    isUnstagingAll,
    isAborting,
    isBusy,
    error,
    setError,
    handleStageAll,
    handleUnstageAll,
    handleAbort,
  } = useGitToolbarActions()

  const isAbortVisible =
    mergeRebaseState.is_merging || mergeRebaseState.is_rebasing

  return (
    <div className="flex items-center gap-1 px-2 py-1 min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
      {/* Abort merge / rebase — shown only while a merge or rebase is in progress (Phase 6) */}
      {isAbortVisible && (
        <>
          <Button
            variant="destructive"
            size="xs"
            disabled={isBusy}
            onClick={() =>
              void handleAbort(
                mergeRebaseState.is_merging ? 'merge' : 'rebase',
              )
            }
            title={
              mergeRebaseState.is_merging ? 'Abort merge' : 'Abort rebase'
            }
            aria-label={
              mergeRebaseState.is_merging ? 'Abort merge' : 'Abort rebase'
            }
          >
            {isAborting ? <Loader2 className="animate-spin" /> : <Ban />}
            Abort {mergeRebaseState.is_merging ? 'Merge' : 'Rebase'}
          </Button>

          {/* Separator — only when abort is visible, separates it from the stage group */}
          <div className="w-px h-4 bg-border mx-0.5" />
        </>
      )}

      {/* Stage All / Unstage All icon group */}
      <div className="flex items-center rounded-md border border-border/50 overflow-hidden">
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={isBusy}
          onClick={handleStageAll}
          title="Stage all changes"
          aria-label="Stage all changes"
        >
          {isStagingAll ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <Plus className="size-3.5" />
          )}
        </Button>
        <div className="w-px h-4 bg-border/50" />
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={isBusy}
          onClick={handleUnstageAll}
          title="Unstage all changes"
          aria-label="Unstage all changes"
        >
          {isUnstagingAll ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <Minus className="size-3.5" />
          )}
        </Button>
      </div>

      {/* Stash / Pop stash */}
      <GitStashButtons onError={setError} />

      {/* Spacer */}
      <div className="flex-1" />

      {/* Error indicator */}
      {error && (
        <span
          className="text-[10px] text-destructive truncate max-w-[120px]"
          title={error}
        >
          {error}
        </span>
      )}

      {/* View mode toggle */}
      <div className="flex items-center rounded-md border border-border/50 overflow-hidden">
        <Button
          variant="ghost"
          size="icon-xs"
          className={cn(
            'rounded-none text-muted-foreground hover:text-foreground',
            viewMode === 'flat' && 'text-primary bg-muted/50',
          )}
          onClick={() => setViewMode('flat')}
          title="Flat view"
          aria-label="Switch to flat view"
        >
          <List className="size-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          className={cn(
            'rounded-none text-muted-foreground hover:text-foreground',
            viewMode === 'tree' && 'text-primary bg-muted/50',
          )}
          onClick={() => setViewMode('tree')}
          title="Tree view"
          aria-label="Switch to tree view"
        >
          <FolderTree className="size-3.5" />
        </Button>
      </div>
    </div>
  )
}
