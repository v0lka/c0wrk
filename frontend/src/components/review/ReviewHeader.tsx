import { useState } from 'react'
import { Check, Send, Loader2, MessageSquare, X, GitCommit, ChevronUp, ChevronDown, Columns2, Rows3 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import { useReviewStore, totalCommentCount } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { emit } from '@/api/runtime'
import { useReviewActions } from './useReviewActions'

interface ReviewHeaderProps {
  sessionId: string
  /** Read-only mode: hide all controls (comment/submit/approve buttons). */
  readOnly?: boolean
  /** Commit SHA shown for context in read-only commit-review mode. */
  commitSha?: string
  /** Currently-focused hunk index (0-based) for chunk navigation. */
  currentHunk?: number
  /** Total number of hunks across all files in the review diff. */
  totalHunks?: number
  /** Scroll the review pane so the previous hunk sits at the top. */
  onPrevHunk?: () => void
  /** Scroll the review pane so the next hunk sits at the top. */
  onNextHunk?: () => void
}

export function ReviewHeader({
  sessionId,
  readOnly,
  commitSha,
  currentHunk = 0,
  totalHunks = 0,
  onPrevHunk,
  onNextHunk,
}: ReviewHeaderProps) {
  const [showGeneral, setShowGeneral] = useState(false)
  const [draft, setDraft] = useState('')
  const reviewState = useReviewStore((s) => s.bySession[sessionId])
  const setGeneralComment = useReviewStore((s) => s.setGeneralComment)
  const diffViewMode = useReviewStore((s) => s.diffViewMode)
  const setDiffViewMode = useReviewStore((s) => s.setDiffViewMode)
  const { hasComments, isStaging, isSubmitting, handleApprove, handleSubmit } = useReviewActions(sessionId)

  const generalComment = reviewState?.generalComment ?? ''
  const totalComments = reviewState ? totalCommentCount(reviewState) : 0
  const isBusy = isStaging || isSubmitting

  // Read-only mode: stripped-down header with no controls. All hooks above
  // are still called unconditionally (React rules of hooks), but none of the
  // interactive state is rendered.
  if (readOnly) {
    return (
      <div className="shrink-0 border-b border-border bg-secondary/30 px-3 py-2">
        <div className="flex items-center gap-2">
          <GitCommit className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="text-sm font-medium">Commit View</span>
          {commitSha && (
            <code className="text-xs text-info font-mono">{commitSha.slice(0, 7)}</code>
          )}
          <DiffViewModeToggle
            diffViewMode={diffViewMode}
            setDiffViewMode={setDiffViewMode}
            className="ml-auto"
          />
          <HunkNavControls
            className="ml-1"
            currentHunk={currentHunk}
            totalHunks={totalHunks}
            onPrevHunk={onPrevHunk}
            onNextHunk={onNextHunk}
          />
        </div>
      </div>
    )
  }

  const openGeneral = () => {
    setDraft(generalComment)
    setShowGeneral(true)
  }

  const handleGeneralSave = async () => {
    const previous = generalComment
    setGeneralComment(sessionId, draft)
    setShowGeneral(false)
    try {
      await reviewApi.saveReviewGeneralComment(sessionId, draft)
    } catch (err) {
      logger.error('Failed to save general comment:', err)
      // Roll back the optimistic update so the UI matches the DB state.
      setGeneralComment(sessionId, previous)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to save general comment',
      })
    }
  }

  return (
    <div className="shrink-0 border-b border-border bg-secondary/30 px-3 py-2 space-y-2">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">Code Review</span>
          {totalComments > 0 && (
            <span className="text-xs text-muted-foreground">
              {totalComments} comment{totalComments !== 1 ? 's' : ''}
            </span>
          )}
          <HunkNavControls
            className="ml-1"
            currentHunk={currentHunk}
            totalHunks={totalHunks}
            onPrevHunk={onPrevHunk}
            onNextHunk={onNextHunk}
          />
          <DiffViewModeToggle
            diffViewMode={diffViewMode}
            setDiffViewMode={setDiffViewMode}
          />
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => (showGeneral ? setShowGeneral(false) : openGeneral())}
            disabled={isBusy}
          >
            <MessageSquare className="h-3 w-3 mr-1" />
            Comment All
          </Button>
          {hasComments ? (
            <Button
              size="xs"
              onClick={() => void handleSubmit()}
              disabled={isBusy}
            >
              {isSubmitting ? (
                <Loader2 className="h-3 w-3 mr-1 animate-spin" />
              ) : (
                <Send className="h-3 w-3 mr-1" />
              )}
              Submit
            </Button>
          ) : (
            <Button
              size="xs"
              onClick={() => void handleApprove()}
              disabled={isBusy}
            >
              {isStaging ? (
                <Loader2 className="h-3 w-3 mr-1 animate-spin" />
              ) : (
                <Check className="h-3 w-3 mr-1" />
              )}
              Approve
            </Button>
          )}
        </div>
      </div>

      {showGeneral && (
        <div className="flex items-start gap-2">
          <textarea
            className="flex-1 rounded-md border border-border bg-background p-2 text-xs resize-none"
            rows={2}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="General review comment..."
            autoFocus
          />
          <Button
            size="xs"
            onClick={handleGeneralSave}
            className="shrink-0"
          >
            Save
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setShowGeneral(false)}
            title="Close"
          >
            <X className="h-3 w-3" />
          </Button>
        </div>
      )}
    </div>
  )
}

interface HunkNavControlsProps {
  currentHunk: number
  totalHunks: number
  onPrevHunk?: () => void
  onNextHunk?: () => void
  className?: string
}

/**
 * Prev/next chunk navigation: two arrow buttons + a "current/total" indicator.
 * Sits next to the review title and jumps the review pane so the target hunk's
 * top edge aligns with the top of the scroll viewport.
 */
function HunkNavControls({
  currentHunk,
  totalHunks,
  onPrevHunk,
  onNextHunk,
  className,
}: HunkNavControlsProps) {
  if (totalHunks <= 0) return null
  return (
    <div className={cn('flex items-center gap-0.5', className)}>
      <span className="mr-0.5 text-xs tabular-nums text-muted-foreground">
        {currentHunk + 1}/{totalHunks}
      </span>
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={onPrevHunk}
        disabled={currentHunk === 0}
        title="Previous hunk"
        aria-label="Previous hunk"
      >
        <ChevronUp className="h-3 w-3" />
      </Button>
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={onNextHunk}
        disabled={currentHunk >= totalHunks - 1}
        title="Next hunk"
        aria-label="Next hunk"
      >
        <ChevronDown className="h-3 w-3" />
      </Button>
    </div>
  )
}

interface DiffViewModeToggleProps {
  diffViewMode: 'unified' | 'split'
  setDiffViewMode: (mode: 'unified' | 'split') => void
  className?: string
}

/**
 * Unified / split diff view-mode toggle. A global preference (no session
 * affinity): lives in the review store and is shared across the interactive
 * and read-only commit-review headers. Mirrors the `ChangesToolbar`
 * view-mode pattern — a bordered container with two `icon-xs` ghost buttons;
 * the active button gets `text-primary bg-muted/50`.
 */
function DiffViewModeToggle({
  diffViewMode,
  setDiffViewMode,
  className,
}: DiffViewModeToggleProps) {
  return (
    <div className={cn('flex items-center rounded-md border border-border/50 overflow-hidden', className)}>
      <Button
        variant="ghost"
        size="icon-xs"
        className={cn(
          'rounded-none text-muted-foreground hover:text-foreground',
          diffViewMode === 'unified' && 'text-primary bg-muted/50',
        )}
        onClick={() => setDiffViewMode('unified')}
        title="Unified view"
        aria-label="Switch to unified view"
      >
        <Rows3 className="size-3.5" />
      </Button>
      <Button
        variant="ghost"
        size="icon-xs"
        className={cn(
          'rounded-none text-muted-foreground hover:text-foreground',
          diffViewMode === 'split' && 'text-primary bg-muted/50',
        )}
        onClick={() => setDiffViewMode('split')}
        title="Split view"
        aria-label="Switch to split view"
      >
        <Columns2 className="size-3.5" />
      </Button>
    </div>
  )
}
