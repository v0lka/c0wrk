import { GitBranch, List, FolderTree, Loader2, Plus, Minus, ChevronDown, Ban } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useGitToolbarActions } from '@/hooks/useGitToolbarActions'
import { GitStashButtons } from './GitStashButtons'
import type { BranchInfo } from '@/types/models'

interface GitPanelToolbarProps {
  branch: BranchInfo
  viewMode: 'flat' | 'tree'
  onViewModeChange: (mode: 'flat' | 'tree') => void
}

export function GitPanelToolbar({
  branch,
  viewMode,
  onViewModeChange,
}: GitPanelToolbarProps) {
  const openBranchPicker = useGitPanelStore((s) => s.openBranchPicker)
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

  return (
    <div className="flex items-center gap-1 px-2 py-1 min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
      {/* Branch indicator — click to open the branch picker */}
      <button
        type="button"
        onClick={openBranchPicker}
        title="Switch or create branch"
        className="flex items-center gap-1.5 px-1.5 py-0.5 text-xs text-muted-foreground min-w-0 mr-1 rounded-md hover:bg-muted transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
      >
        <GitBranch className="size-3.5 shrink-0" />
        <span className="truncate font-mono text-[11px]">
          {branch.name || <span className="italic opacity-50">no branch</span>}
        </span>
        {(branch.ahead > 0 || branch.behind > 0) && (
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground/70">
            ↑{branch.ahead} ↓{branch.behind}
          </span>
        )}
        <ChevronDown className="size-3 shrink-0 opacity-60" />
      </button>

      {/* Abort merge / rebase — shown only while a merge or rebase is in progress (Phase 6) */}
      {(mergeRebaseState.is_merging || mergeRebaseState.is_rebasing) && (
        <Button
          variant="destructive"
          size="xs"
          disabled={isBusy}
          onClick={() => void handleAbort(mergeRebaseState.is_merging ? 'merge' : 'rebase')}
          title={mergeRebaseState.is_merging ? 'Abort merge' : 'Abort rebase'}
          aria-label={mergeRebaseState.is_merging ? 'Abort merge' : 'Abort rebase'}
        >
          {isAborting ? <Loader2 className="animate-spin" /> : <Ban />}
          Abort {mergeRebaseState.is_merging ? 'Merge' : 'Rebase'}
        </Button>
      )}

      {/* Separator */}
      <div className="w-px h-4 bg-border mx-0.5" />

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
          onClick={() => onViewModeChange('flat')}
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
          onClick={() => onViewModeChange('tree')}
          title="Tree view"
          aria-label="Switch to tree view"
        >
          <FolderTree className="size-3.5" />
        </Button>
      </div>
    </div>
  )
}
