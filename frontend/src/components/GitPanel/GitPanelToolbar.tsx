import { useState, useCallback } from 'react'
import { GitBranch, List, FolderTree, RefreshCw, Loader2, Plus, Minus, ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { stageAll, unstageAll } from '@/api/git'
import { useGitPanelStore } from '@/stores/gitPanelStore'

interface GitPanelToolbarProps {
  branch: string
  viewMode: 'flat' | 'tree'
  onViewModeChange: (mode: 'flat' | 'tree') => void
  onRefresh: () => void
}

export function GitPanelToolbar({
  branch,
  viewMode,
  onViewModeChange,
  onRefresh,
}: GitPanelToolbarProps) {
  const openBranchPicker = useGitPanelStore((s) => s.openBranchPicker)
  const [isStagingAll, setIsStagingAll] = useState(false)
  const [isUnstagingAll, setIsUnstagingAll] = useState(false)
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const isBusy = isStagingAll || isUnstagingAll

  const handleStageAll = useCallback(async () => {
    setIsStagingAll(true)
    setError(null)
    try {
      await stageAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to stage all files')
    } finally {
      setIsStagingAll(false)
    }
  }, [])

  const handleUnstageAll = useCallback(async () => {
    setIsUnstagingAll(true)
    setError(null)
    try {
      await unstageAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to unstage all files')
    } finally {
      setIsUnstagingAll(false)
    }
  }, [])

  const handleRefresh = useCallback(async () => {
    setIsRefreshing(true)
    setError(null)
    try {
      await onRefresh()
    } catch {
      // onRefresh handles its own errors
    } finally {
      setIsRefreshing(false)
    }
  }, [onRefresh])

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
          {branch || <span className="italic opacity-50">no branch</span>}
        </span>
        <ChevronDown className="size-3 shrink-0 opacity-60" />
      </button>

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

      {/* Refresh button */}
      <Button
        variant="ghost"
        size="icon-xs"
        disabled={isRefreshing}
        onClick={handleRefresh}
        title="Refresh git status"
        aria-label="Refresh git status"
      >
        <RefreshCw
          className={cn('size-3.5', isRefreshing && 'animate-spin')}
        />
      </Button>
    </div>
  )
}
